package desktop

import (
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// The tables change their selections themselves, so a view cannot know when
// its selection changed. Selected entries, in row order, are kept between
// frames and rebuilt only when a fingerprint of the selection differs: its
// size, an order-independent mix of its IDs, the rows and the snapshot. The
// fingerprint is taken at most once per check point (the start of a frame
// and after the table is built), so menus, shortcuts and the status bar
// asking many times per frame cost nothing after the first. With a million
// entries selected, rebuilding the list on every question took seconds.
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
	ids     []index.ID
	key     selectionKey
	valid   bool   // false forces a rebuild
	checked bool   // the fingerprint was compared since the last check point
	version uint64 // increments with every rebuild
}

// invalidate forces the next question to rebuild the list.
func (c *selectionCache) invalidate() { c.valid = false }

// checkpoint makes the next question compare the fingerprint again.
func (c *selectionCache) checkpoint() { c.checked = false }

// get returns the cached list, rebuilding it with build when the selection
// may have changed.
func (c *selectionCache) get(sel *ui.Selection[index.ID], rows uint64, s *index.Snapshot, build func([]index.ID) []index.ID) []index.ID {
	if c.valid && c.checked {
		return c.ids
	}
	k := fingerprint(sel, rows, s)
	c.checked = true
	if c.valid && k == c.key {
		return c.ids
	}
	c.ids = build(c.ids[:0])
	c.key, c.valid = k, true
	c.version++
	return c.ids
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
	f.onDisk = true
	for _, id := range ids {
		if a.snap.IsMetadata(id) {
			f.onDisk = false
			break
		}
	}
	for _, id := range a.topLevel(ids) {
		f.total += a.metric(a.snap.Nodes[id])
	}
	return f
}

// invalidateSelections makes the next questions about the selections exact:
// a command about to act on them must not see a cached answer.
func (a *App) invalidateSelections() {
	a.tree.selCache.invalidate()
	a.files.selCache.invalidate()
	a.selFacts.ok = false
}
