package desktop

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// fileModel is File View: a flat, sorted list of entries from the whole scan
// or a chosen folder. With an empty search it lists files (largest first);
// a search also matches folders unless it says type:file. Every match is
// kept; the virtualized table only builds rows in view.
type fileModel struct {
	a              *App
	ids            []index.ID
	total          uint64
	busy           bool
	err            string
	gen            uint64
	cancel         context.CancelFunc
	dirty          bool
	due            time.Time
	forSnap        *index.Snapshot // snapshot of the shown results
	runFor         *index.Snapshot // snapshot of the running query
	ran            string          // query of the shown results
	scope          index.ID
	shownScope     index.ID
	shownAllocated bool
	shownOnDisk    bool

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
	f.gen++ // reject completions already posted before cancellation
	f.cancel, f.runFor, f.due = nil, nil, time.Time{}
	cols := f.list.Columns
	f.list = ui.ListState{Columns: cols}
	f.ids, f.total, f.busy, f.err, f.forSnap, f.ran = nil, 0, false, "", nil, ""
	f.idsGen++
	f.scope = 0
	f.sel = ui.Selection[index.ID]{}
	f.cursor = -1
	f.dirty = true
	f.selCache.reset()
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
	if f.cancel != nil {
		f.cancel()
	}
	f.gen++
	f.busy, f.runFor = false, nil
	f.dirty = true
	f.due = now.Add(60 * time.Millisecond)
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
	// Results remain immutable while a worker sorts its own copy. Changing
	// order or size metric need not search the same snapshot and scope again.
	reuse := f.forSnap == s && f.ran == query && f.shownScope == scope && f.err == ""
	previous, previousTotal, sameMetric := f.ids, f.total, f.shownAllocated == allocated
	previousOnDisk := f.shownOnDisk
	go func() {
		defer cancel()
		filter, err := index.ParseFilter(query)
		var ids []index.ID
		var total uint64
		onDisk := previousOnDisk
		if err == nil && reuse {
			ids = slices.Clone(previous)
		} else if err == nil {
			if filter.Kind == index.KindAny && query == "" {
				filter.Kind = index.KindFiles
			}
			var results index.SearchResult
			results, err = s.SearchResults(ctx, scope, &filter, allocated)
			ids, total, onDisk = results.IDs, results.Total, !results.HasMetadata
		}
		if err == nil && reuse && sameMetric {
			total = previousTotal
		} else if err == nil && reuse {
			// A metric change can reuse the matches. Classify their ancestors
			// once and sum before reordering the result for the new metric.
			var top []index.ID
			top, err = topLevelEntries(ctx, s, ids)
			for i, id := range top {
				if i&4095 == 0 && ctx.Err() != nil {
					return
				}
				n := s.Nodes[id]
				if allocated {
					total += n.Allocated
				} else {
					total += n.Size
				}
			}
		}
		if err == nil {
			err = sortEntries(ctx, s, ids, order, allocated)
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != f.gen {
				return
			}
			f.busy, f.forSnap, f.ran = false, s, query
			f.cancel, f.runFor = nil, nil
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
			if reuse {
				if f.sel.Has(cid) {
					cursor = slices.Index(ids, cid)
				}
			} else if f.sel.Len() > 0 {
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
			f.shownScope, f.shownAllocated = scope, allocated
			f.shownOnDisk = onDisk
			f.idsGen++
			f.cursor = cursor
			if !reuse {
				f.sel.Clear()
				for _, id := range kept {
					f.sel.Add(id)
				}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		return ctx.Err()
	}
	desc := order.Descending
	keyOf := func(rank uint32) uint64 {
		k := uint64(rank)
		if desc {
			k = ^k
		}
		return k
	}
	switch order.Column {
	case "Folder":
		parentRanks := make([]uint32, len(s.Nodes))
		var parents []index.ID
		for i, id := range ids {
			if i&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			if p := s.Nodes[id].Parent; p != index.None && parentRanks[p] == 0 {
				parentRanks[p] = 1
				parents = append(parents, p)
			}
		}
		type parentPath struct {
			id         index.ID
			arena      uint32
			start, end uint32
		}
		parentList := make([]parentPath, len(parents))
		for i, p := range parents {
			parentList[i].id = p
		}
		workers := min(runtime.GOMAXPROCS(0), max(1, len(parentList)/64))
		arenas := make([]string, 0, workers)
		var arenaLock sync.Mutex
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				start := len(parentList) * w / workers
				end := len(parentList) * (w + 1) / workers
				chunkSize := max(4096, min(1<<20, (end-start)*128))
				var scratch []byte
				var arena strings.Builder
				chunkStart := start
				publish := func(end int) {
					arenaLock.Lock()
					id := uint32(len(arenas))
					arenas = append(arenas, arena.String())
					arenaLock.Unlock()
					for i := chunkStart; i < end; i++ {
						parentList[i].arena = id
					}
					chunkStart = end
					arena = strings.Builder{}
				}
				for i := start; i < end; i++ {
					if i&1023 == 0 && ctx.Err() != nil {
						return
					}
					scratch = s.AppendPath(scratch[:0], parentList[i].id)
					ascii := true
					for j, ch := range scratch {
						if ch >= 128 {
							ascii = false
							break
						}
						if ch >= 'A' && ch <= 'Z' {
							scratch[j] = ch + 32
						}
					}
					var unicodePath string
					length := len(scratch)
					if !ascii {
						unicodePath = strings.ToLower(string(scratch))
						length = len(unicodePath)
					}
					// Fixed chunks bound unused capacity and avoid copying a large
					// arena when a path-length estimate turns out too small.
					if arena.Len() > 0 && arena.Cap()-arena.Len() < length {
						publish(i)
					}
					if arena.Len() == 0 {
						arena.Grow(max(chunkSize, length))
					}
					p := &parentList[i]
					p.start = uint32(arena.Len())
					if ascii {
						arena.Write(scratch)
					} else {
						arena.WriteString(unicodePath)
					}
					p.end = uint32(arena.Len())
				}
				if chunkStart < end {
					publish(end)
				}
			}(w)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		pathOf := func(p parentPath) string { return arenas[p.arena][p.start:p.end] }
		if err := parallelSort(ctx, parentList, func(x, y parentPath) int { return strings.Compare(pathOf(x), pathOf(y)) }); err != nil {
			return err
		}
		rank := uint32(0)
		for i, pp := range parentList {
			if i == 0 || pathOf(pp) != pathOf(parentList[i-1]) {
				rank++
			}
			parentRanks[pp.id] = rank
		}
		zeroKey := keyOf(0)
		folderKey := func(id index.ID) uint64 {
			if p := s.Nodes[id].Parent; p != index.None {
				return keyOf(parentRanks[p])
			}
			return zeroKey
		}
		// Within a folder, by name: the first eight name bytes in the key, the
		// rest compared where those are equal.
		name := func(id index.ID) uint64 { return s.NameKey(id, 0) }
		return s.SortByKeys(ctx, ids, folderKey, name, &index.Sort{Column: "Name"})
	case "Extension":
		// Intern by the borrowed, original spelling first. Case folding once
		// per distinct spelling avoids allocating ".TXT" -> ".txt" per file.
		rawRanks := map[string]uint32{}
		ranks := map[string]uint32{}
		var lastRaw string
		for i, id := range ids {
			if i&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			raw := rawExtOf(s, id)
			if raw == lastRaw && i > 0 {
				continue
			}
			lastRaw = raw
			if _, ok := rawRanks[raw]; !ok {
				rawRanks[raw] = 0
				ranks[strings.ToLower(raw)] = 0
			}
		}
		exts := make([]string, 0, len(ranks))
		for e := range ranks {
			exts = append(exts, e)
		}
		slices.Sort(exts)
		for i, e := range exts {
			ranks[e] = uint32(i)
		}
		rawKeyRanks := make(map[string]uint64, len(rawRanks))
		for raw := range rawRanks {
			rawKeyRanks[raw] = keyOf(ranks[strings.ToLower(raw)])
		}
		extensionKey := func(id index.ID) uint64 { return rawKeyRanks[rawExtOf(s, id)] }
		// Within an extension, largest first, then by ID: exactly as
		// Compare with Sort{Desc: true} orders them.
		largest := func(id index.ID) uint64 {
			if allocated {
				return ^s.Nodes[id].Allocated
			}
			return ^s.Nodes[id].Size
		}
		return s.SortByKeys(ctx, ids, extensionKey, largest, nil)
	}
	column := order.Column
	if column == "% of Scan" {
		column = ""
	}
	return s.SortContext(ctx, ids, index.Sort{Column: column, Desc: desc, Allocated: allocated})
}

func extOf(s *index.Snapshot, id index.ID) string {
	return strings.ToLower(rawExtOf(s, id))
}

func rawExtOf(s *index.Snapshot, id index.ID) string {
	if s.Nodes[id].IsDir() {
		return ""
	}
	name := s.NameView(id)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
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
	f.selCache.peak = max(f.selCache.peak, f.sel.Len())
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
			if n == len(f.ids) {
				f.selCache.borrowed = true
				return f.ids
			}
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
