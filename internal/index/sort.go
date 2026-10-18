package index

import (
	"context"
	"math"
	"slices"
	"strings"
)

// keyed is an entry with a precomputed two-word sort key.
type keyed struct {
	hi, lo uint64
	id     ID
}

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
		hi, lo := func(id ID) uint64 { return s.NameKey(id, 0) }, func(id ID) uint64 { return s.NameKey(id, 8) }
		if by.Desc {
			hi, lo = func(id ID) uint64 { return ^s.NameKey(id, 0) }, func(id ID) uint64 { return ^s.NameKey(id, 8) }
		}
		return s.SortByKeys(ctx, ids, hi, lo, &by)
	}
	key := s.sortKey(by)
	if key == nil {
		return s.sortCompare(ctx, ids, by)
	}
	if by.Desc {
		ascending := key
		key = func(id ID) uint64 { return ^ascending(id) }
	}
	return s.SortByKeys(ctx, ids, key, nil, nil)
}

// SortByKeys orders ids by hi, then lo (nil: none), then ID; with tie,
// entries with equal keys are ordered by Compare(tie) instead of by ID.
// It is for orders Compare does not know, such as by extension or folder.
func (s *Snapshot) SortByKeys(ctx context.Context, ids []ID, hi, lo func(ID) uint64, tie *Sort) error {
	if len(ids) < 2 {
		return ctx.Err()
	}
	items := make([]keyed, len(ids))
	for i, id := range ids {
		items[i] = keyed{hi: hi(id), id: id}
		if lo != nil {
			items[i].lo = lo(id)
		}
	}
	if err := radixSort(ctx, items, lo != nil); err != nil {
		return err
	}
	if tie != nil {
		for start := 0; start < len(items); {
			end := start + 1
			for end < len(items) && items[end].hi == items[start].hi && items[end].lo == items[start].lo {
				end++
			}
			if end-start > 1 {
				slices.SortFunc(items[start:end], func(a, b keyed) int { return s.Compare(a.id, b.id, *tie) })
			}
			start = end
			if start&0xffff == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	for i := range items {
		ids[i] = items[i].id
	}
	return ctx.Err()
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

// radixSort orders items by hi, lo and ID with stable 16-bit passes, least
// significant first. Passes in which every item has the same digit are
// skipped, so small keys cost few passes.
func radixSort(ctx context.Context, items []keyed, useLo bool) error {
	tmp := make([]keyed, len(items))
	counts := make([]int, 1<<16)
	src, dst := items, tmp
	pass := func(digit func(*keyed) int) {
		clear(counts)
		for i := range src {
			counts[digit(&src[i])]++
		}
		if counts[digit(&src[0])] == len(src) {
			return
		}
		sum := 0
		for i, c := range counts {
			counts[i] = sum
			sum += c
		}
		for i := range src {
			d := digit(&src[i])
			dst[counts[d]] = src[i]
			counts[d]++
		}
		src, dst = dst, src
	}
	pass(func(v *keyed) int { return int(v.id & 0xffff) })
	pass(func(v *keyed) int { return int(v.id >> 16) })
	for _, word := range []bool{false, true} {
		if !word && !useLo {
			continue
		}
		for shift := uint(0); shift < 64; shift += 16 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if word {
				pass(func(v *keyed) int { return int(v.hi >> shift & 0xffff) })
			} else {
				pass(func(v *keyed) int { return int(v.lo >> shift & 0xffff) })
			}
		}
	}
	if &src[0] != &items[0] {
		copy(items, src)
	}
	return nil
}
