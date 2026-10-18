package desktop

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// fileModel is File View: a flat, sorted list of entries from the whole scan
// or a chosen folder. With an empty search it lists files (largest first);
// a search also matches folders unless it says type:file. Every match is
// kept; the virtualized table only builds rows in view.
type fileModel struct {
	a       *App
	ids     []index.ID
	total   uint64
	busy    bool
	err     string
	gen     uint64
	cancel  context.CancelFunc
	dirty   bool
	due     time.Time
	forSnap *index.Snapshot // snapshot of the shown results
	runFor  *index.Snapshot // snapshot of the running query
	ran     string          // query of the shown results
	scope   index.ID

	list   ui.ListState
	sel    ui.Selection[index.ID]
	cursor int
	sort   ui.SortOrder

	selCache selectionCache
	idsGen   uint64 // changes whenever ids change
}

func (f *fileModel) init(a *App) {
	f.a = a
	f.sort = a.prefs.FileSort
	f.list.Columns = a.prefs.FileColumns
	f.reset()
}

func (f *fileModel) reset() {
	if f.cancel != nil {
		f.cancel()
	}
	cols := f.list.Columns
	f.list = ui.ListState{Columns: cols}
	f.ids, f.total, f.busy, f.err, f.forSnap, f.ran = nil, 0, false, "", nil, ""
	f.idsGen++
	f.scope = 0
	f.sel.Clear()
	f.cursor = -1
	f.dirty = true
	f.selCache.invalidate()
}

func (f *fileModel) invalidate() {
	f.dirty = true
	if f.a.prefs.View == viewFiles {
		f.run()
	}
}

// ensure runs the query when File View is shown with outdated results. A
// query already running for the shown snapshot is left to finish: every
// frame (a pointer move is enough) would otherwise restart it.
func (f *fileModel) ensure() {
	if !f.due.IsZero() {
		return // a typed query is still debouncing; tick runs it when due
	}
	if f.dirty || f.forSnap != f.a.snap && !(f.busy && f.runFor == f.a.snap) {
		f.run()
	}
}

// queryChanged debounces typing.
func (f *fileModel) queryChanged(now time.Time) {
	f.dirty = true
	f.due = now.Add(150 * time.Millisecond)
}

func (f *fileModel) tick(c *ui.Context) {
	if !f.due.IsZero() {
		if c.Now().Before(f.due) {
			c.After(f.due.Sub(c.Now()))
			return
		}
		f.due = time.Time{}
		f.run()
	}
}

func (f *fileModel) setScope(id index.ID) {
	if f.scope != id {
		f.scope = id
		f.dirty = true
		f.a.ext.dirty = true
		f.run()
	}
}

func (f *fileModel) scopeName() string {
	if f.scope == 0 || f.a.snap == nil || !f.a.snap.Reachable(f.scope) {
		return "Entire scan"
	}
	return f.a.pathOf(f.scope)
}

func (f *fileModel) run() {
	a := f.a
	if a.snap == nil {
		return
	}
	if f.cancel != nil {
		f.cancel()
	}
	if !a.snap.Reachable(f.scope) {
		f.scope = 0
	}
	f.gen++
	gen := f.gen
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.busy, f.dirty, f.due, f.runFor = true, false, time.Time{}, a.snap
	s, query, scope, order, allocated := a.snap, strings.TrimSpace(a.search), f.scope, f.sort, a.metricAllocated()
	go func() {
		filter, err := index.ParseFilter(query)
		var ids []index.ID
		var total uint64
		if err == nil {
			if filter.Kind == index.KindAny && query == "" {
				filter.Kind = index.KindFiles
			}
			ids, err = s.Search(ctx, scope, &filter)
		}
		if err == nil {
			err = sortEntries(ctx, s, ids, order, allocated)
		}
		// Count each matched byte once: skip matches inside a matched folder.
		matchedDirs := map[index.ID]bool{}
		for _, id := range ids {
			if s.Nodes[id].IsDir() {
				matchedDirs[id] = true
			}
		}
		for _, id := range ids {
			n := s.Nodes[id]
			nested := false
			for p := n.Parent; p != index.None && len(matchedDirs) > 0; p = s.Nodes[p].Parent {
				if matchedDirs[p] {
					nested = true
					break
				}
			}
			if nested {
				continue
			}
			if allocated {
				total += n.Allocated
			} else {
				total += n.Size
			}
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != f.gen {
				return
			}
			f.busy, f.forSnap, f.ran = false, s, query
			if err != nil {
				f.err = err.Error()
				return
			}
			f.err = ""
			cid := f.cursorID()
			// Keep the selected entries that the new results still contain.
			// Checked before the selection is cleared, without a map copy.
			var kept []index.ID
			cursor := -1
			if f.sel.Len() > 0 {
				for i, id := range ids {
					if f.sel.Has(id) {
						kept = append(kept, id)
						if id == cid {
							cursor = i
						}
					}
				}
			}
			f.ids, f.total = ids, total
			f.idsGen++
			f.sel.Clear()
			f.cursor = cursor
			for _, id := range kept {
				f.sel.Add(id)
			}
			f.selCache.invalidate()
			if f.cursor < 0 {
				f.list.ScrollTo(0, ui.Start)
			}
			if query != "" {
				a.prefs.SearchHistory = remember(a.prefs.SearchHistory, query, 15)
			}
		})
	}()
}

// sortEntries orders results; "Folder" and "Extension" use derived keys.
// Each distinct folder path or extension is ranked once, and the entries
// are radix-sorted by rank: comparing derived strings per comparison took
// over a second for a million files.
func sortEntries(ctx context.Context, s *index.Snapshot, ids []index.ID, order ui.SortOrder, allocated bool) error {
	desc := order.Descending
	keyOf := func(rank []uint32) func(index.ID) uint64 {
		return func(id index.ID) uint64 {
			k := uint64(rank[id])
			if desc {
				k = ^k
			}
			return k
		}
	}
	switch order.Column {
	case "Folder":
		parentRank := make([]uint32, len(s.Nodes))
		var parents []index.ID
		for _, id := range ids {
			if p := s.Nodes[id].Parent; p != index.None && parentRank[p] == 0 {
				parentRank[p] = 1
				parents = append(parents, p)
			}
		}
		paths := make(map[index.ID]string, len(parents))
		for i, p := range parents {
			if i&1023 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			paths[p] = strings.ToLower(s.Path(p))
		}
		slices.SortFunc(parents, func(x, y index.ID) int { return strings.Compare(paths[x], paths[y]) })
		rank := uint32(0)
		for i, p := range parents {
			if i == 0 || paths[p] != paths[parents[i-1]] {
				rank++
			}
			parentRank[p] = rank
		}
		entryRank := make([]uint32, len(s.Nodes))
		for _, id := range ids {
			if p := s.Nodes[id].Parent; p != index.None {
				entryRank[id] = parentRank[p]
			}
		}
		// Within a folder, by name: the first eight name bytes in the key, the
		// rest compared where those are equal.
		name := func(id index.ID) uint64 { return s.NameKey(id, 0) }
		return s.SortByKeys(ctx, ids, keyOf(entryRank), name, &index.Sort{Column: "Name"})
	case "Extension":
		ranks := map[string]uint32{}
		for _, id := range ids {
			ranks[extOf(s, id)] = 0
		}
		exts := make([]string, 0, len(ranks))
		for e := range ranks {
			exts = append(exts, e)
		}
		slices.Sort(exts)
		for i, e := range exts {
			ranks[e] = uint32(i)
		}
		entryRank := make([]uint32, len(s.Nodes))
		for _, id := range ids {
			entryRank[id] = ranks[extOf(s, id)]
		}
		// Within an extension, largest first, then by ID: exactly as
		// Compare with Sort{Desc: true} orders them.
		largest := func(id index.ID) uint64 {
			if allocated {
				return ^s.Nodes[id].Allocated
			}
			return ^s.Nodes[id].Size
		}
		return s.SortByKeys(ctx, ids, keyOf(entryRank), largest, nil)
	}
	column := order.Column
	if column == "% of Scan" {
		column = ""
	}
	return s.SortContext(ctx, ids, index.Sort{Column: column, Desc: desc, Allocated: allocated})
}

func extOf(s *index.Snapshot, id index.ID) string {
	if s.Nodes[id].IsDir() {
		return ""
	}
	return strings.ToLower(filepath.Ext(s.NameView(id)))
}

func (f *fileModel) cursorID() index.ID {
	if f.cursor >= 0 && f.cursor < len(f.ids) {
		return f.ids[f.cursor]
	}
	return index.None
}

func (f *fileModel) selectAll() {
	for _, id := range f.ids {
		f.sel.Add(id)
	}
	f.selCache.invalidate()
}

// selectedIDs lists selected results in row order (cached, see selectionCache).
func (f *fileModel) selectedIDs() []index.ID {
	return f.selCache.get(&f.sel, f.idsGen, f.a.snap, func(out []index.ID) []index.ID {
		switch n := f.sel.Len(); {
		case n == 1:
			for id := range f.sel.All() {
				out = append(out, id)
			}
		case n > 1:
			for _, id := range f.ids {
				if f.sel.Has(id) {
					out = append(out, id)
				}
			}
		}
		return out
	})
}

func (f *fileModel) selectOnly(id index.ID) {
	f.sel.Clear()
	f.sel.Add(id)
	f.selCache.invalidate()
	f.cursor = slices.Index(f.ids, id)
	if f.cursor >= 0 {
		f.list.ScrollIntoView(f.cursor)
	}
}
