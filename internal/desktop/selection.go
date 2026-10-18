package desktop

import (
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// Application mutations and Table.Changed invalidate the selected-ID cache.
// Unchanged frames check only cardinality, row generation and snapshot;
// they do not walk a large selection. Fingerprints are retained for explicit
// checkpoints, and commands still invalidate before acting on a selection.
type selectionKey struct {
	n        int
	sum, mix uint64
	rows     uint64
	snap     *index.Snapshot
}

func fingerprint(sel *ui.Selection[index.ID], rows uint64, s *index.Snapshot) selectionKey {
	k := selectionKey{n: sel.Len(), rows: rows, snap: s}
	for id := range sel.All() {
		v := uint64(id) + 1
		k.sum += v * 0x9e3779b97f4a7c15
		k.mix ^= (v ^ v>>17) * 0xc2b2ae3d27d4eb4f
	}
	return k
}

// selectionCache is a view's selected entries in row order.
type selectionCache struct {
	ids      []index.ID
	key      selectionKey
	valid    bool   // false forces a rebuild
	checked  bool   // the fingerprint was compared since the last check point
	version  uint64 // increments with every rebuild
	peak     int    // largest cardinality since the selection map was trimmed
	borrowed bool   // ids aliases the file model's immutable result slice
}

// invalidate forces the next question to rebuild the list.
func (c *selectionCache) invalidate() { c.valid = false }

// reset releases the old scan and large selection buffers, even when this
// view stays hidden after another scan is installed.
func (c *selectionCache) reset() { *c = selectionCache{version: c.version + 1} }

// checkpoint makes the next question compare the fingerprint again.
func (c *selectionCache) checkpoint() { c.checked = false }

// get returns the cached list, rebuilding it with build when the selection
// may have changed.
func (c *selectionCache) get(sel *ui.Selection[index.ID], rows uint64, s *index.Snapshot, build func([]index.ID) []index.ID) []index.ID {
	// Selection.Clear retains its map. Release a former Select All map when
	// the user returns to a small selection, preserving the current keys.
	if c.peak > 16384 && sel.Len() <= 4096 {
		var compact ui.Selection[index.ID]
		for id := range sel.All() {
			compact.Add(id)
		}
		*sel = compact
		c.peak = sel.Len()
	} else {
		c.peak = max(c.peak, sel.Len())
	}
	if c.valid && c.checked && c.key.n == sel.Len() && c.key.rows == rows && c.key.snap == s {
		return c.ids
	}
	if c.valid {
		k := fingerprint(sel, rows, s)
		c.checked = true
		if k == c.key {
			return c.ids
		}
	}
	if c.borrowed || sel.Len() == 0 || cap(c.ids) > max(4096, 4*sel.Len()) {
		c.ids = nil
	}
	c.borrowed = false // build may explicitly borrow another immutable result
	c.ids = build(c.ids[:0])
	c.key = fingerprintSlice(c.ids, rows, s)
	c.valid, c.checked = true, true
	c.version++
	return c.ids
}

func fingerprintSlice(ids []index.ID, rows uint64, s *index.Snapshot) selectionKey {
	k := selectionKey{n: len(ids), rows: rows, snap: s}
	for _, id := range ids {
		v := uint64(id) + 1
		k.sum += v * 0x9e3779b97f4a7c15
		k.mix ^= (v ^ v>>17) * 0xc2b2ae3d27d4eb4f
	}
	return k
}

// selectionFacts are derived from one version of the active selection.
type selectionFacts struct {
	view      int
	version   uint64
	snap      *index.Snapshot
	allocated bool
	ok        bool

	onDisk bool   // every entry has a path Windows accepts
	total  uint64 // bytes of the entries not inside another selected one
}

func (a *App) selectionVersion() uint64 {
	switch a.prefs.View {
	case viewTree:
		return a.tree.selCache.version
	case viewFiles:
		return a.files.selCache.version
	}
	return 0
}

// facts returns the cached facts about the active selection.
func (a *App) facts() *selectionFacts {
	ids := a.selected()
	f := &a.selFacts
	if f.ok && f.view == a.prefs.View && f.version == a.selectionVersion() && f.snap == a.snap && f.allocated == a.metricAllocated() {
		return f
	}
	*f = selectionFacts{view: a.prefs.View, version: a.selectionVersion(), snap: a.snap, allocated: a.metricAllocated(), ok: true}
	if a.prefs.View == viewFiles && a.files.forSnap == a.snap && len(ids) == len(a.files.ids) && a.files.shownAllocated == a.metricAllocated() && a.files.err == "" {
		// The complete result set's non-overlapping total was already computed
		// by the query worker. Select All should not sum a million nodes again.
		f.total = a.files.total
		f.onDisk = a.files.shownOnDisk
		return f
	}
	f.onDisk = a.selectionOnDisk(ids)
	for _, id := range a.topLevel(ids) {
		f.total += a.metric(a.snap.Nodes[id])
	}
	return f
}

// selectionOnDisk preserves IsMetadata's rules while classifying each ancestor
// once. Walking to the root for every selected file stalls the first frame
// after Select All on a whole-drive scan.
func (a *App) selectionOnDisk(ids []index.ID) bool {
	s := a.snap
	if len(ids) < 64 {
		for _, id := range ids {
			if s.IsMetadata(id) {
				return false
			}
		}
		return true
	}
	buffer := mutStatesPool.Get().(*[]uint8)
	states := *buffer
	if cap(states) < len(s.Nodes) {
		states = make([]uint8, len(s.Nodes))
	} else {
		states = states[:len(s.Nodes)]
		clear(states)
	}
	defer func() {
		*buffer = states
		mutStatesPool.Put(buffer)
	}()
	states[0] = stateKnown
	var local [64]index.ID
	stack := local[:0]
	classify := func(id index.ID, parent uint8) uint8 {
		n := s.Nodes[id]
		if parent&pathBlocked != 0 || n.Flags&index.Unplaced != 0 || n.Parent == 0 && s.IsMetadata(id) {
			return stateKnown | pathBlocked
		}
		return stateKnown
	}
	for _, id := range ids {
		if id == 0 || id == index.None || int(id) >= len(s.Nodes) {
			continue // IsMetadata also returns false for these entries
		}
		if states[id]&stateKnown != 0 {
			if states[id]&pathBlocked != 0 {
				return false
			}
			continue
		}
		stack = stack[:0]
		for p := s.Nodes[id].Parent; p != index.None && states[p]&stateKnown == 0; p = s.Nodes[p].Parent {
			stack = append(stack, p)
		}
		for i := len(stack) - 1; i >= 0; i-- {
			p := stack[i]
			parent := uint8(0)
			if ancestor := s.Nodes[p].Parent; ancestor != index.None {
				parent = states[ancestor]
			}
			states[p] = classify(p, parent)
		}
		parent := uint8(0)
		if p := s.Nodes[id].Parent; p != index.None {
			parent = states[p]
		}
		state := classify(id, parent)
		if state&pathBlocked != 0 {
			return false
		}
		if s.Nodes[id].IsDir() {
			states[id] = state
		}
	}
	return true
}

// invalidateSelections makes the next questions about the selections exact:
// a command about to act on them must not see a cached answer.
func (a *App) invalidateSelections() {
	a.tree.selCache.invalidate()
	a.files.selCache.invalidate()
	a.selFacts.ok = false
}
