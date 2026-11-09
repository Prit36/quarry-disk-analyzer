package index

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

// DiffStatus says how an entry differs between two scans.
type DiffStatus uint8

const (
	DiffUnchanged DiffStatus = iota
	DiffAdded                // only in the current scan
	DiffRemoved              // only in the previous scan
	DiffGrown
	DiffShrunk
	DiffModified // same size, but a file's time or a folder's counts differ
)

func (d DiffStatus) String() string {
	return [...]string{"Unchanged", "Added", "Removed", "Grown", "Shrunk", "Modified"}[d]
}

// DiffEntry is one entry of a comparison: the same path in both scans, or
// in only one of them (Prev or Cur is None).
type DiffEntry struct {
	Prev, Cur     ID
	Dir           bool
	Before, After uint64 // the metric's value in each scan, 0 where absent
	Status        DiffStatus
}

// Delta is the change in bytes, positive for growth.
func (e DiffEntry) Delta() int64 {
	if e.After >= e.Before {
		return int64(min(e.After-e.Before, 1<<63-1))
	}
	return -int64(min(e.Before-e.After, 1<<63-1))
}

// Magnitude is the size of the change either way.
func (e DiffEntry) Magnitude() uint64 {
	if e.After >= e.Before {
		return e.After - e.Before
	}
	return e.Before - e.After
}

// Comparison relates two scans of the same root by path. Folder totals are
// already in each index, so one folder's changes are found by matching its
// children by name: no walk below it is needed until it is opened. Both
// snapshots are immutable; a Comparison is safe to share with workers.
type Comparison struct {
	Prev, Cur *Snapshot
	Allocated bool
}

// NewComparison compares previous and current, which must be scans of the
// same folder.
func NewComparison(previous, current *Snapshot, allocated bool) (*Comparison, error) {
	if previous == nil || current == nil || len(previous.Nodes) == 0 || len(current.Nodes) == 0 {
		return nil, fmt.Errorf("nothing to compare")
	}
	if !strings.EqualFold(filepath.Clean(previous.Root), filepath.Clean(current.Root)) {
		return nil, fmt.Errorf("the scans are of different folders (%s and %s); comparison requires the same scan root", previous.Root, current.Root)
	}
	return &Comparison{Prev: previous, Cur: current, Allocated: allocated}, nil
}

func (c *Comparison) value(s *Snapshot, id ID) uint64 {
	if id == None {
		return 0
	}
	if c.Allocated {
		return s.Nodes[id].Allocated
	}
	return s.Nodes[id].Size
}

func (c *Comparison) entry(prev, cur ID) DiffEntry {
	e := DiffEntry{Prev: prev, Cur: cur, Before: c.value(c.Prev, prev), After: c.value(c.Cur, cur)}
	switch {
	case prev == None:
		e.Dir, e.Status = c.Cur.Nodes[cur].IsDir(), DiffAdded
	case cur == None:
		e.Dir, e.Status = c.Prev.Nodes[prev].IsDir(), DiffRemoved
	default:
		p, n := c.Prev.Nodes[prev], c.Cur.Nodes[cur]
		e.Dir = n.IsDir()
		switch {
		case e.After > e.Before:
			e.Status = DiffGrown
		case e.After < e.Before:
			e.Status = DiffShrunk
		case p.IsDir() != n.IsDir(),
			n.IsDir() && (p.Files != n.Files || p.Dirs != n.Dirs),
			!n.IsDir() && p.Modified != n.Modified && p.Modified != 0 && n.Modified != 0:
			e.Status = DiffModified
		}
	}
	return e
}

// Root is the comparison of the two scan roots.
func (c *Comparison) Root() DiffEntry { return c.entry(0, 0) }

// Name is an entry's name, from the current scan where it exists.
func (c *Comparison) Name(e DiffEntry) string {
	if e.Cur != None {
		if e.Cur == 0 {
			return c.Cur.Root
		}
		return c.Cur.NameView(e.Cur)
	}
	if e.Prev == 0 {
		return c.Prev.Root
	}
	return c.Prev.NameView(e.Prev)
}

// Path is an entry's full path.
func (c *Comparison) Path(e DiffEntry) string {
	if e.Cur != None {
		return c.Cur.Path(e.Cur)
	}
	return c.Prev.Path(e.Prev)
}

// Modified is an entry's modification time in the scan it is newest in.
func (c *Comparison) Modified(e DiffEntry) int64 {
	if e.Cur != None {
		return c.Cur.Nodes[e.Cur].ModTime()
	}
	return c.Prev.Nodes[e.Prev].ModTime()
}

// matcher pairs the children of two folders by case-folded name, as
// Windows compares names. Everything it holds is reused from folder to
// folder, and folded names live in one arena, so matching allocates
// nothing per entry: only names outside ASCII need strings.ToLower.
type matcher struct {
	c       *Comparison
	byName  map[string]int // folded name â†’ index in prev, for wide folders
	prev    []ID           // the previous folder's children, in order
	keys    []string       // their folded names, parallel to prev
	used    []bool         // parallel to prev: matched in the current scan
	arena   []byte         // backs keys; reserved per folder, so it never moves
	scratch []byte
}

// linearMatch is the widest folder matched by scanning keys rather than
// through the map: for a handful of names a scan is quicker than hashing.
const linearMatch = 16

func (c *Comparison) newMatcher() *matcher {
	return &matcher{c: c, byName: map[string]int{}}
}

// key folds a previous child's name into the arena.
func (m *matcher) key(name string) string {
	start := len(m.arena)
	b, ascii := AppendFoldedASCII(m.arena, name)
	switch {
	case !ascii:
		m.arena = m.arena[:start]
		return strings.ToLower(name)
	case string(b[start:]) == name:
		m.arena = m.arena[:start]
		return name // already folded: the scan's own string serves
	}
	m.arena = b
	return unsafe.String(&b[start], len(name))
}

// index finds a current child's name among the previous children: the
// first with that folded name, as the map keeps the first.
func (m *matcher) index(name string) (int, bool) {
	var ascii bool
	m.scratch, ascii = AppendFoldedASCII(m.scratch[:0], name)
	var key string
	if ascii {
		key = unsafe.String(unsafe.SliceData(m.scratch), len(m.scratch))
	} else {
		key = strings.ToLower(name)
	}
	if len(m.prev) > linearMatch {
		i, ok := m.byName[key]
		return i, ok
	}
	for i, k := range m.keys {
		if k == key {
			return i, true
		}
	}
	return 0, false
}

// load reads the previous folder's children and their folded names.
func (m *matcher) load(s *Snapshot, folder ID) {
	if len(m.byName) > 4096 {
		m.byName = map[string]int{} // clearing a wide map costs its capacity on every later folder
	} else {
		clear(m.byName)
	}
	clear(m.keys) // drop references into the arena before it is reused
	m.prev, m.keys = m.prev[:0], m.keys[:0]
	if folder == None || !s.Nodes[folder].IsDir() {
		return
	}
	total := 0
	for k := s.Nodes[folder].FirstChild; k != None; k = s.Nodes[k].NextSibling {
		total += int(s.Nodes[k].NameLength)
	}
	m.arena = slices.Grow(m.arena[:0], total)
	for k := s.Nodes[folder].FirstChild; k != None; k = s.Nodes[k].NextSibling {
		m.prev = append(m.prev, k)
		m.keys = append(m.keys, m.key(s.NameView(k)))
	}
	if len(m.prev) > linearMatch {
		for i, key := range m.keys {
			if _, dup := m.byName[key]; !dup {
				m.byName[key] = i
			}
		}
	}
	if len(m.arena) > 1<<20 && cap(m.arena) > 4*len(m.arena) {
		m.arena = nil
	}
}

// pairs calls visit for every child of the folder pair (either side may be
// None): matched names first in the current scan's order, then those only
// in the previous scan.
func (m *matcher) pairs(ctx context.Context, prev, cur ID, visit func(prev, cur ID) error) error {
	c := m.c
	m.load(c.Prev, prev)
	m.used = slices.Grow(m.used[:0], len(m.prev))[:len(m.prev)]
	clear(m.used)
	count := 0
	if cur != None && c.Cur.Nodes[cur].IsDir() {
		for k := c.Cur.Nodes[cur].FirstChild; k != None; k = c.Cur.Nodes[k].NextSibling {
			if count&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			count++
			match := None
			if len(m.prev) > 0 {
				if i, ok := m.index(c.Cur.NameView(k)); ok && !m.used[i] {
					match = m.prev[i]
					m.used[i] = true
				}
			}
			if err := visit(match, k); err != nil {
				return err
			}
		}
	}
	for i, k := range m.prev {
		if count&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if !m.used[i] {
			if err := visit(k, None); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// Children compares the entries directly inside a folder of the
// comparison, largest change first. Unchanged entries are included; the
// caller filters by Status.
func (c *Comparison) Children(ctx context.Context, e DiffEntry) ([]DiffEntry, error) {
	if !e.Dir {
		return nil, nil
	}
	var out []DiffEntry
	err := c.newMatcher().pairs(ctx, e.Prev, e.Cur, func(prev, cur ID) error {
		out = append(out, c.entry(prev, cur))
		return nil
	})
	if err != nil {
		return nil, err
	}
	SortDiff(out)
	return out, nil
}

// diffBefore is the order of a list of changes: the size of the change,
// largest first, then the larger of the two sizes, then the entries' IDs,
// so that ties do not depend on which worker found an entry.
func diffBefore(a, b DiffEntry) int {
	if x := cmp.Compare(b.Magnitude(), a.Magnitude()); x != 0 {
		return x
	}
	if x := cmp.Compare(max(b.Before, b.After), max(a.Before, a.After)); x != 0 {
		return x
	}
	if x := cmp.Compare(a.Cur, b.Cur); x != 0 {
		return x
	}
	return cmp.Compare(a.Prev, b.Prev)
}

// SortDiff orders entries by the size of their change, largest first, then
// by the larger of their sizes.
func SortDiff(entries []DiffEntry) {
	slices.SortFunc(entries, diffBefore)
}

// DiffTotals counts the files that changed, by kind of change.
type DiffTotals struct {
	Added, Removed, Grown, Shrunk, Modified uint64 // files
	AddedBytes, RemovedBytes                uint64
	GrownBytes, ShrunkBytes                 uint64 // the change, not the sizes
}

func (t *DiffTotals) merge(o DiffTotals) {
	t.Added += o.Added
	t.Removed += o.Removed
	t.Grown += o.Grown
	t.Shrunk += o.Shrunk
	t.Modified += o.Modified
	t.AddedBytes += o.AddedBytes
	t.RemovedBytes += o.RemovedBytes
	t.GrownBytes += o.GrownBytes
	t.ShrunkBytes += o.ShrunkBytes
}

// diffTask is a folder still to compare: present in both scans, or in
// only one of them (every file below it then added or removed).
type diffTask struct {
	prev, cur ID
	kind      uint8
}

const (
	taskBoth uint8 = iota
	taskAdded
	taskRemoved
)

// diffWorker gathers the largest changes of the folders it is given, with
// its own matcher, tallies and heap, so workers share nothing.
type diffWorker struct {
	c      *Comparison
	m      *matcher
	limit  int
	keep   func(DiffStatus) bool
	totals DiffTotals
	top    []DiffEntry // a heap whose root is the smallest change kept
	stack  []diffTask
	count  int
}

func (c *Comparison) newDiffWorker(limit int, keep func(DiffStatus) bool) *diffWorker {
	return &diffWorker{c: c, m: c.newMatcher(), limit: limit, keep: keep}
}

// smaller reports whether top[i] ranks after top[j], for the heap.
func (w *diffWorker) smaller(i, j int) bool { return diffBefore(w.top[i], w.top[j]) > 0 }

func (w *diffWorker) consider(d DiffEntry) {
	t := &w.totals
	switch d.Status {
	case DiffAdded:
		t.Added++
		t.AddedBytes += d.After
	case DiffRemoved:
		t.Removed++
		t.RemovedBytes += d.Before
	case DiffGrown:
		t.Grown++
		t.GrownBytes += d.After - d.Before
	case DiffShrunk:
		t.Shrunk++
		t.ShrunkBytes += d.Before - d.After
	case DiffModified:
		t.Modified++
	default:
		return
	}
	if w.limit <= 0 || w.keep != nil && !w.keep(d.Status) {
		return
	}
	if len(w.top) < w.limit {
		w.top = append(w.top, d)
		for i := len(w.top) - 1; i > 0; {
			parent := (i - 1) / 2
			if !w.smaller(i, parent) {
				break
			}
			w.top[i], w.top[parent] = w.top[parent], w.top[i]
			i = parent
		}
		return
	}
	if diffBefore(d, w.top[0]) >= 0 {
		return // ranks no higher than the smallest kept
	}
	w.top[0] = d
	for i := 0; ; {
		least, l, r := i, 2*i+1, 2*i+2
		if l < len(w.top) && w.smaller(l, least) {
			least = l
		}
		if r < len(w.top) && w.smaller(r, least) {
			least = r
		}
		if least == i {
			break
		}
		w.top[i], w.top[least] = w.top[least], w.top[i]
		i = least
	}
}

// step compares one folder's direct contents: files are considered, and
// folders become tasks through push.
func (w *diffWorker) step(ctx context.Context, t diffTask, push func(diffTask)) error {
	c := w.c
	if t.kind != taskBoth {
		s, id := c.Cur, t.cur
		if t.kind == taskRemoved {
			s, id = c.Prev, t.prev
		}
		for k := s.Nodes[id].FirstChild; k != None; k = s.Nodes[k].NextSibling {
			if w.count&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			w.count++
			switch {
			case s.Nodes[k].IsDir() && t.kind == taskAdded:
				push(diffTask{None, k, taskAdded})
			case s.Nodes[k].IsDir():
				push(diffTask{k, None, taskRemoved})
			case t.kind == taskAdded:
				w.consider(c.entry(None, k))
			default:
				w.consider(c.entry(k, None))
			}
		}
		return nil
	}
	return w.m.pairs(ctx, t.prev, t.cur, func(prev, cur ID) error {
		d := c.entry(prev, cur)
		switch {
		case d.Status == DiffUnchanged && d.Dir && c.Prev.Nodes[prev].IsDir():
			// Same totals and counts: nothing below changed size. (A
			// file swapped for another of equal size is not reported.)
		case prev == None && d.Dir:
			push(diffTask{None, cur, taskAdded})
		case cur == None && d.Dir:
			push(diffTask{prev, None, taskRemoved})
		case d.Dir && prev != None && c.Prev.Nodes[prev].IsDir():
			push(diffTask{prev, cur, taskBoth})
		case d.Dir: // a file became a folder
			w.consider(c.entry(prev, None))
			push(diffTask{None, cur, taskAdded})
		case prev != None && c.Prev.Nodes[prev].IsDir(): // a folder became a file
			push(diffTask{prev, None, taskRemoved})
			w.consider(c.entry(None, cur))
		default:
			w.consider(d)
		}
		return nil
	})
}

// run compares a task and everything below it, depth first.
func (w *diffWorker) run(ctx context.Context, t diffTask) error {
	w.stack = append(w.stack[:0], t)
	push := func(t diffTask) { w.stack = append(w.stack, t) }
	for len(w.stack) > 0 {
		t := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		if err := w.step(ctx, t, push); err != nil {
			return err
		}
	}
	return nil
}

// weight estimates a task's work: the entries below its folder.
func (c *Comparison) weight(t diffTask) uint64 {
	var w uint64
	if t.prev != None {
		n := &c.Prev.Nodes[t.prev]
		w = uint64(n.Files) + uint64(n.Dirs)
	}
	if t.cur != None {
		n := &c.Cur.Nodes[t.cur]
		w = max(w, uint64(n.Files)+uint64(n.Dirs))
	}
	return w
}

// LargestChanges walks the whole comparison below e and returns the files
// with the largest changes, at most limit of them, largest first, with
// totals over every changed file. Within a folder that exists in only one
// scan, every file is added or removed. keep, when set, decides which
// statuses are collected; the totals count all of them.
//
// Large comparisons are split into folders of similar weight that workers
// take in turn, as searches are. Each worker keeps its own largest
// changes; the result does not depend on the split.
func (c *Comparison) LargestChanges(ctx context.Context, e DiffEntry, limit int, keep func(DiffStatus) bool) ([]DiffEntry, DiffTotals, error) {
	main := c.newDiffWorker(limit, keep)
	if !e.Dir {
		main.consider(e)
		return main.top, main.totals, ctx.Err()
	}
	root := diffTask{e.Prev, e.Cur, taskBoth}
	switch {
	case e.Prev == None:
		root.kind = taskAdded
	case e.Cur == None:
		root.kind = taskRemoved
	}
	total := c.weight(root)
	workers := min(max(1, runtime.GOMAXPROCS(0)-1), max(1, int(total/parallelSearchEntries)))
	if workers == 1 {
		if err := main.run(ctx, root); err != nil {
			return nil, DiffTotals{}, err
		}
		SortDiff(main.top)
		return main.top, main.totals, ctx.Err()
	}
	// Open the heaviest folders until there are enough pieces to share.
	tasks := []diffTask{root}
	target := max(1024, total/uint64(8*workers))
	for len(tasks) < 8*workers {
		best, heaviest := -1, target
		for i, t := range tasks {
			if w := c.weight(t); w > heaviest {
				best, heaviest = i, w
			}
		}
		if best < 0 {
			break
		}
		t := tasks[best]
		tasks = slices.Delete(tasks, best, best+1)
		if err := main.step(ctx, t, func(t diffTask) { tasks = append(tasks, t) }); err != nil {
			return nil, DiffTotals{}, err
		}
	}
	// Heaviest first, so that a large folder does not start last.
	slices.SortFunc(tasks, func(a, b diffTask) int { return cmp.Compare(c.weight(b), c.weight(a)) })
	workers = min(workers, len(tasks))
	all := make([]*diffWorker, workers)
	errs := make([]error, workers)
	var next atomic.Int64
	var wg sync.WaitGroup
	for i := range all {
		all[i] = c.newDiffWorker(limit, keep)
		wg.Add(1)
		go func(w *diffWorker, err *error) {
			defer wg.Done()
			for {
				n := int(next.Add(1) - 1)
				if n >= len(tasks) {
					return
				}
				if *err = w.run(ctx, tasks[n]); *err != nil {
					return
				}
			}
		}(all[i], &errs[i])
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, DiffTotals{}, err
	}
	n := len(main.top)
	for _, w := range all {
		n += len(w.top)
	}
	out, totals := make([]DiffEntry, 0, n), main.totals
	out = append(out, main.top...)
	for _, w := range all {
		out = append(out, w.top...)
		totals.merge(w.totals)
	}
	SortDiff(out)
	if len(out) > max(limit, 0) {
		out = slices.Clip(out[:max(limit, 0)])
	}
	return out, totals, ctx.Err()
}
