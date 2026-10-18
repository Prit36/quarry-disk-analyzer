package index

import (
	"context"
	"math"
	"slices"
	"strings"
)

// SortContext orders ids as Compare does. Large lists are sorted by a
// precomputed key with a radix sort instead of a comparison sort: a million
// comparisons through Compare cost about as much as reading every node
// twenty times. Names are keyed by their first sixteen case-folded bytes,
// and only entries sharing those are compared in full.
func (s *Snapshot) SortContext(ctx context.Context, ids []ID, by Sort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 4096 {
		s.Sort(ids, by)
		return nil
	}
	if by.Column == "Name" {
		return s.sortWordKeysFrom(ctx, ids, func(id ID) wordKey {
			k := s.nameWords(id)
			if by.Desc {
				k.hi, k.lo = ^k.hi, ^k.lo
			}
			return k
		}, &by, 16)
	}
	key := s.sortKey(by)
	if key == nil {
		return s.sortCompare(ctx, ids, by)
	}
	if by.Desc {
		ascending := key
		key = func(id ID) uint64 { return ^ascending(id) }
	}
	return sortNumeric(ctx, ids, key)
}

// SortByKeys orders ids by hi, then lo (nil: none), then ID; with tie,
// entries with equal keys are ordered by Compare(tie) instead of by ID.
// It is for orders Compare does not know, such as by extension or folder.
func (s *Snapshot) SortByKeys(ctx context.Context, ids []ID, hi, lo func(ID) uint64, tie *Sort) error {
	return s.sortWordKeys(ctx, ids, func(id ID) wordKey {
		k := wordKey{hi: hi(id)}
		if lo != nil {
			k.lo = lo(id)
		}
		return k
	}, tie)
}

// sortKey returns a key that orders entries exactly as Compare does in
// ascending order before ties on ID, or nil for the name and for orders
// without one.
func (s *Snapshot) sortKey(by Sort) func(ID) uint64 {
	nodes := s.Nodes
	switch by.Column {
	case "Modified":
		return func(id ID) uint64 { return uint64(nodes[id].Modified) }
	case "Files":
		return func(id ID) uint64 { return uint64(nodes[id].Files) }
	case "Folders":
		return func(id ID) uint64 { return uint64(nodes[id].Dirs) }
	case "Allocated":
		return func(id ID) uint64 { return nodes[id].Allocated }
	case "Size":
		return func(id ID) uint64 { return nodes[id].Size }
	case "% of Parent":
		return func(id ID) uint64 {
			n := nodes[id]
			if n.Parent == None {
				return math.Float64bits(1)
			}
			value, total := n.Size, nodes[n.Parent].Size
			if by.Allocated {
				value, total = n.Allocated, nodes[n.Parent].Allocated
			}
			if total == 0 {
				return 0
			}
			return math.Float64bits(float64(value) / float64(total)) // non-negative: bits order as values
		}
	case "Name":
		return nil
	}
	// Compare orders any other column by the size metric.
	if by.Allocated {
		return func(id ID) uint64 { return nodes[id].Allocated }
	}
	return func(id ID) uint64 { return nodes[id].Size }
}

// NameKey packs eight bytes of the case-folded name, from offset on,
// big-endian, so keys order as compareFold orders names; a shorter name
// pads with zero bytes, which no name contains. Names with non-ASCII bytes
// are folded as compareFold folds them, with strings.ToLower.
func (s *Snapshot) NameKey(id ID, offset int) uint64 {
	name := s.NameView(id)
	for i := 0; i < len(name); i++ {
		if name[i] >= 128 {
			name = strings.ToLower(name)
			break
		}
	}
	var k uint64
	for i := offset; i < offset+8; i++ {
		var b byte
		if i < len(name) {
			b = name[i]
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
		}
		k = k<<8 | uint64(b)
	}
	return k
}

func (s *Snapshot) sortCompare(ctx context.Context, ids []ID, by Sort) error {
	comparisons := 0
	canceled := false
	slices.SortFunc(ids, func(first, second ID) int {
		comparisons++
		if comparisons&4095 == 0 && ctx.Err() != nil {
			canceled = true
		}
		if canceled {
			return 0
		}
		return s.Compare(first, second, by)
	})
	return ctx.Err()
}
