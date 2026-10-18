package index

import (
	"context"
	"math"
	"runtime"
	"slices"
	"strings"
)

// SortContext orders ids as Compare does. Large lists are sorted by a
// precomputed key with a radix sort instead of a comparison sort: a million
// comparisons through Compare cost about as much as reading every node
// twenty times. Names use eight-byte chunks, reading later chunks only in
// groups sharing the earlier prefix.
func (s *Snapshot) SortContext(ctx context.Context, ids []ID, by Sort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 4096 {
		s.Sort(ids, by)
		return nil
	}
	if by.Column == "Name" {
		return s.sortNames(ctx, ids, by)
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
	if k, ok := foldedWord(name, offset); ok {
		return k
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

// ReverseSortContext changes only the direction of an already sorted list.
// Reverse the rows, then restore ascending ID order inside equal-value runs.
// The source order must have been produced with the same column and metric.
func (s *Snapshot) ReverseSortContext(ctx context.Context, ids []ID, by Sort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := s.sortKey(by)
	if by.Column != "Name" && key == nil {
		return s.SortContext(ctx, ids, by)
	}
	slices.Reverse(ids)
	if len(ids) < 2 {
		return ctx.Err()
	}
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)))
	bounds := make([]int, workers+1)
	bounds[workers] = len(ids)
	for w := 1; w < workers; w++ {
		p := max(len(ids)*w/workers, bounds[w-1])
		for p < len(ids) {
			if p&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			equal := false
			if by.Column == "Name" {
				equal = compareFold(s.NameView(ids[p-1]), s.NameView(ids[p])) == 0
			} else {
				equal = key(ids[p-1]) == key(ids[p])
			}
			if !equal {
				break
			}
			p++
		}
		bounds[w] = p
	}
	runSortWorkers(workers, workers, func(w, _, _ int) {
		nextCheck := bounds[w]
		for start := bounds[w]; start < bounds[w+1]; {
			if start >= nextCheck {
				if ctx.Err() != nil {
					return
				}
				nextCheck = start + 4096
			}
			end := start + 1
			if by.Column == "Name" {
				name := s.NameView(ids[start])
				for end < bounds[w+1] && compareFold(name, s.NameView(ids[end])) == 0 {
					if end&4095 == 0 && ctx.Err() != nil {
						return
					}
					end++
				}
			} else {
				value := key(ids[start])
				for end < bounds[w+1] && value == key(ids[end]) {
					if end&4095 == 0 && ctx.Err() != nil {
						return
					}
					end++
				}
			}
			slices.Reverse(ids[start:end])
			start = end
		}
	})
	return ctx.Err()
}
