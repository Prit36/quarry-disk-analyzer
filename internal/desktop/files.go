package desktop

import (
	"cmp"
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
	forSnap *index.Snapshot
	ran     string // query of the shown results
	scope   index.ID

	list   ui.ListState
	sel    ui.Selection[index.ID]
	cursor int
	sort   ui.SortOrder

	selCache []index.ID
	selValid bool
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
	f.scope = 0
	f.sel.Clear()
	f.cursor = -1
	f.dirty = true
	f.selValid = false
}

func (f *fileModel) invalidate() {
	f.dirty = true
	if f.a.prefs.View == viewFiles {
		f.run()
	}
}

// ensure runs the query when File View is shown with outdated results.
func (f *fileModel) ensure() {
	if f.dirty || f.forSnap != f.a.snap {
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
	f.busy, f.dirty, f.due = true, false, time.Time{}
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
			keep := map[index.ID]bool{}
			for id := range f.sel.All() {
				keep[id] = true
			}
			f.ids, f.total = ids, total
			f.sel.Clear()
			f.cursor = -1
			if len(keep) > 0 {
				for i, id := range ids {
					if keep[id] {
						f.sel.Add(id)
						if id == cid {
							f.cursor = i
						}
					}
				}
			}
			f.selValid = false
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
func sortEntries(ctx context.Context, s *index.Snapshot, ids []index.ID, order ui.SortOrder, allocated bool) error {
	desc := order.Descending
	flip := func(v int) int {
		if desc {
			return -v
		}
		return v
	}
	switch order.Column {
	case "Folder":
		parents := map[index.ID]string{}
		for _, id := range ids {
			p := s.Nodes[id].Parent
			if _, ok := parents[p]; !ok && p != index.None {
				parents[p] = strings.ToLower(s.Path(p))
			}
		}
		n := 0
		slices.SortStableFunc(ids, func(x, y index.ID) int {
			if n++; n&4095 == 0 && ctx.Err() != nil {
				return 0
			}
			if v := strings.Compare(parents[s.Nodes[x].Parent], parents[s.Nodes[y].Parent]); v != 0 {
				return flip(v)
			}
			return s.Compare(x, y, index.Sort{Column: "Name"})
		})
		return ctx.Err()
	case "Extension":
		n := 0
		slices.SortStableFunc(ids, func(x, y index.ID) int {
			if n++; n&4095 == 0 && ctx.Err() != nil {
				return 0
			}
			if v := cmp.Compare(extOf(s, x), extOf(s, y)); v != 0 {
				return flip(v)
			}
			return s.Compare(x, y, index.Sort{Desc: true, Allocated: allocated})
		})
		return ctx.Err()
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
	f.selValid = false
}

func (f *fileModel) selectedIDs() []index.ID {
	if f.selValid {
		return f.selCache
	}
	f.selCache = f.selCache[:0]
	switch n := f.sel.Len(); {
	case n == 1:
		for id := range f.sel.All() {
			f.selCache = append(f.selCache, id)
		}
	case n > 1:
		for _, id := range f.ids {
			if f.sel.Has(id) {
				f.selCache = append(f.selCache, id)
			}
		}
	}
	f.selValid = true
	return f.selCache
}

func (f *fileModel) selectOnly(id index.ID) {
	f.sel.Clear()
	f.sel.Add(id)
	f.selValid = false
	f.cursor = slices.Index(f.ids, id)
	if f.cursor >= 0 {
		f.list.ScrollIntoView(f.cursor)
	}
}
