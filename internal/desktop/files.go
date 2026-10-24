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
	pinned         bool // scope chosen by the user; otherwise it follows the location
	shownScope     index.ID
	shownAllocated bool
	shownOnDisk    bool
	shownSort      ui.SortOrder
	shownGroups    []uint32 // immutable group lengths for Folder/Extension sorts

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
	f.shownGroups = nil
	f.idsGen++
	f.scope, f.pinned = 0, false
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

// follow moves an unpinned scope along with the location, so File View and
// Extensions describe the folder the user is looking at.
func (f *fileModel) follow(location index.ID) {
	if f.pinned || f.scope == location {
		return
	}
	f.scope = location
	f.a.ext.dirty = true
	f.invalidate()
}

// followLocation unpins the scope and returns it to the current location.
func (f *fileModel) followLocation() {
	f.pinned = false
	f.setScope(f.a.location)
}

// pinScope sets a scope that stays put while the user navigates.
func (f *fileModel) pinScope(id index.ID) {
	f.pinned = true
	f.setScope(id)
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
	previousSort := f.shownSort
	previousGroups := f.shownGroups
	go func() {
		defer cancel()
		filter, err := index.ParseFilter(query)
		var ids []index.ID
		var total uint64
		var groups []uint32
		onDisk := previousOnDisk
		if err == nil && reuse {
			if sameMetric && previousSort == order {
				ids = previous // unchanged, immutable results need no new buffer
			} else {
				ids = slices.Clone(previous)
			}
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
			groups = previousGroups
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
		if err == nil && !(reuse && sameMetric && previousSort == order) {
			if reuse && sameMetric && previousSort.Column == order.Column && previousSort.Descending != order.Descending {
				if order.Column == "Folder" || order.Column == "Extension" {
					groups, err = reverseEntryGroups(ctx, ids, previousGroups)
				} else {
					err = s.ReverseSortContext(ctx, ids, index.Sort{Column: order.Column, Allocated: allocated})
				}
			} else {
				groups = nil
				err = sortEntriesGrouped(ctx, s, ids, order, allocated, &groups)
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
			f.shownSort = order
			f.shownGroups = groups
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

// Folder and Extension directions change the order of groups while keeping
// each group's name/size order. Lengths come from the initial sort's exact
// ranks, including distinct parent IDs whose normalized paths are equal.
func reverseEntryGroups(ctx context.Context, ids []index.ID, previous []uint32) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups := slices.Clone(previous)
	slices.Reverse(groups)
	slices.Reverse(ids)
	start := 0
	for i, length := range groups {
		if i&4095 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		end := start + int(length)
		// Large equal-key groups also yield promptly to a new query.
		for lo, hi := start, end-1; lo < hi; lo, hi = lo+1, hi-1 {
			if lo&4095 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			ids[lo], ids[hi] = ids[hi], ids[lo]
		}
		start = end
	}
	return groups, ctx.Err()
}

// sortEntries orders results; "Folder" and "Extension" use derived keys.
// Each distinct folder path or extension is ranked once, and the entries
// are radix-sorted by rank: comparing derived strings per comparison took
// over a second for a million files.
func sortEntries(ctx context.Context, s *index.Snapshot, ids []index.ID, order ui.SortOrder, allocated bool) error {
	return sortEntriesGrouped(ctx, s, ids, order, allocated, nil)
}

func sortEntriesGrouped(ctx context.Context, s *index.Snapshot, ids []index.ID, order ui.SortOrder, allocated bool, groups *[]uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		if groups != nil && len(ids) == 1 && (order.Column == "Folder" || order.Column == "Extension") {
			*groups = []uint32{1}
		}
		return ctx.Err()
	}
	desc := order.Descending
	switch order.Column {
	case "Folder":
		pParentRanks, parentRanks := index.GetUint32s(len(s.Nodes))
		var parents []index.ID
		defer func() {
			for _, p := range parents {
				parentRanks[p] = 0
			}
			index.PutUint32s(pParentRanks)
		}()
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
				cursor := s.NewPathCursor()
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
					// Neighbouring parents share ancestors; their prefix is kept.
					path := cursor.Path(parentList[i].id)
					var ascii bool
					scratch, ascii = index.AppendFoldedASCII(scratch[:0], path)
					var unicodePath string
					length := len(scratch)
					if !ascii {
						unicodePath = strings.ToLower(path)
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
		folderRank := func(id index.ID) uint32 {
			if p := s.Nodes[id].Parent; p != index.None {
				return parentRanks[p]
			}
			return 0
		}
		// Counting the dense folder rank keeps unrelated folders out of the
		// more expensive name comparisons and needs only compact scratch IDs.
		return s.SortByRankGroups(ctx, ids, folderRank, rank, desc, index.Sort{Column: "Name"}, groups)
	case "Extension":
		pRanks, ranks, maxRank, err := extensionRanks(ctx, s, ids)
		if err != nil {
			return err
		}
		defer index.PutUint32s(pRanks)
		// Within an extension, largest first, then by ID: exactly as
		// Compare with Sort{Desc: true} orders them.
		return s.SortByRankValues(ctx, ids, ranks, maxRank, desc, index.Sort{Desc: true, Allocated: allocated}, groups)
	}
	column := order.Column
	if column == "% of Scan" {
		column = ""
	}
	return s.SortContext(ctx, ids, index.Sort{Column: column, Desc: desc, Allocated: allocated})
}

// extensionRanks returns the rank of each entry's lower-case extension
// among the distinct extensions of ids, in sorted order, and the largest
// rank. Workers intern the borrowed, original spellings of their ranges
// (consecutive entries mostly share one), so case folding happens once per
// distinct spelling and every entry is hashed at most once.
func extensionRanks(ctx context.Context, s *index.Snapshot, ids []index.ID) (*[]uint32, []uint32, uint32, error) {
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<15)))
	pRanks, ranks := index.GetUint32s(len(ids)) // slots into the worker's spellings, then ranks
	spellings := make([][]string, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots := map[string]uint32{}
			var raws []string
			var last string
			var lastSlot uint32
			for i := len(ids) * w / workers; i < len(ids)*(w+1)/workers; i++ {
				if i&4095 == 0 && ctx.Err() != nil {
					return
				}
				raw := rawExtOf(s, ids[i])
				if raw != last || len(raws) == 0 {
					slot, ok := slots[raw]
					if !ok {
						slot = uint32(len(raws))
						slots[raw] = slot
						raws = append(raws, raw)
					}
					last, lastSlot = raw, slot
				}
				ranks[i] = lastSlot
			}
			spellings[w] = raws
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		index.PutUint32s(pRanks)
		return nil, nil, 0, err
	}
	folded := map[string]uint32{}
	for _, raws := range spellings {
		for _, raw := range raws {
			folded[strings.ToLower(raw)] = 0
		}
	}
	exts := make([]string, 0, len(folded))
	for e := range folded {
		exts = append(exts, e)
	}
	slices.Sort(exts)
	for i, e := range exts {
		folded[e] = uint32(i)
	}
	tables := make([][]uint32, workers)
	for w, raws := range spellings {
		tables[w] = make([]uint32, len(raws))
		for slot, raw := range raws {
			tables[w][slot] = folded[strings.ToLower(raw)]
		}
	}
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			table := tables[w]
			for i := len(ids) * w / workers; i < len(ids)*(w+1)/workers; i++ {
				ranks[i] = table[ranks[i]]
			}
		}()
	}
	wg.Wait()
	return pRanks, ranks, uint32(max(1, len(exts)) - 1), nil
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
	f.sel = ui.Selection[index.ID]{}
	f.sel.Add(id)
	f.selCache.peak = 1
	f.selCache.invalidate()
	f.cursor = slices.Index(f.ids, id)
	if f.cursor >= 0 {
		f.list.ScrollIntoView(f.cursor)
	}
}
