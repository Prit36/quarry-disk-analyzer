package desktop

import (
	"slices"
	"strings"

	"quarry/internal/index"
	"quarry/internal/treemap"
)

// While File View lists a search's matches or duplicates, the treemap dims
// every tile that holds none of them, so where they are is visible at a
// glance. Which entries hold a match is computed on a worker when the
// results change; each tile then needs one lookup.

type matchKey struct {
	snap *index.Snapshot
	gen  uint64
	n    int
	dups bool
}

type mapMatches struct {
	key     matchKey        // the results the sets are being made for
	under   []uint64        // per entry: it matched, or something below it did
	direct  []uint64        // per entry: it matched itself
	forSnap *index.Snapshot // the snapshot under and direct describe
	ready   bool
	busy    bool
	gen     uint64
	version uint64 // changes whenever under and direct do

	// lit caches, per treemap cell, whether it holds a match.
	lit        []bool
	litCells   []treemap.Cell
	litVersion uint64
}

func bitSet(b []uint64, id index.ID)      { b[id>>6] |= 1 << (id & 63) }
func bitHas(b []uint64, id index.ID) bool { return int(id>>6) < len(b) && b[id>>6]&(1<<(id&63)) != 0 }

// markMatches builds the bit sets for ids in s.
func markMatches(s *index.Snapshot, ids []index.ID) (under, direct []uint64) {
	words := (len(s.Nodes) + 63) / 64
	under, direct = make([]uint64, words), make([]uint64, words)
	for _, id := range ids {
		if int(id) >= len(s.Nodes) {
			continue
		}
		bitSet(direct, id)
		for p := id; p != index.None && !bitHas(under, p); p = s.Nodes[p].Parent {
			bitSet(under, p)
		}
	}
	return under, direct
}

// matchesChanged drops the cached tiles, as after the option changed.
func (m *treemapModel) matchesChanged() {
	m.matches.litCells = nil
}

// highlightingMatches reports whether the treemap shows matches now: while
// File View lists a search's results or duplicates. A search running or
// waiting for typing to pause keeps the previous results listed, and so
// highlighted; turning highlighting off meanwhile made the map flash on
// every keystroke.
func (a *App) highlightingMatches() bool {
	f := &a.files
	return a.prefs.MapMatches && a.prefs.View == viewFiles && f.forSnap != nil &&
		(strings.TrimSpace(f.ran) != "" || a.duplicates.active)
}

// treemapMatches returns, per cell, whether it holds a match, or nil when
// nothing is highlighted.
func (a *App) treemapMatches(s *index.Snapshot, cells []treemap.Cell) []bool {
	m := &a.tm.matches
	if !a.highlightingMatches() || s == nil {
		return nil
	}
	f := &a.files
	// The key changes only when the listed results do, not with every
	// search started.
	key := matchKey{snap: s, gen: f.idsGen, n: len(f.ids), dups: a.duplicates.active}
	if m.key != key {
		m.key = key
		m.gen++
		gen := m.gen
		ids := append([]index.ID(nil), f.ids...)
		m.busy = true
		// Until the new sets arrive, the previous ones stay on screen if
		// they describe the same snapshot: no undimmed frames between
		// two searches.
		if m.forSnap != s {
			m.ready = false
		}
		go func() {
			under, direct := markMatches(s, ids)
			a.post(func() {
				if m.gen != gen {
					return
				}
				m.under, m.direct, m.forSnap, m.ready, m.busy = under, direct, s, true, false
				m.version++
			})
		}()
	}
	if !m.ready {
		return nil
	}
	if m.litVersion == m.version && len(m.litCells) == len(cells) && (len(cells) == 0 || &m.litCells[0] == &cells[0]) {
		return m.lit
	}
	lit := slices.Grow(m.lit[:0], len(cells))[:len(cells)] // reused between layouts
	clear(lit)
	for i, cell := range cells {
		id := cell.Tile.ID
		if int(id) >= len(s.Nodes) {
			continue
		}
		if bitHas(m.under, id) {
			lit[i] = true
			continue
		}
		// Inside a folder that matched as a whole.
		for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
			if bitHas(m.direct, p) {
				lit[i] = true
				break
			}
		}
	}
	m.lit, m.litCells, m.litVersion = lit, cells, m.version
	return lit
}
