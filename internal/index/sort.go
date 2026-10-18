package index

import (
	"context"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	fillKeys(items, ids, hi, lo)
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

// fillKeys computes the keys of ids into items, on several goroutines for
// large lists: name keys read and fold every name, which is the slow part.
// The key functions only read the snapshot.
func fillKeys(items []keyed, ids []ID, hi, lo func(ID) uint64) {
	fill := func(items []keyed, ids []ID) {
		for i, id := range ids {
			items[i] = keyed{hi: hi(id), id: id}
			if lo != nil {
				items[i].lo = lo(id)
			}
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if len(ids) < 1<<16 || workers < 2 {
		fill(items, ids)
		return
	}
	chunk := (len(ids) + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		wg.Add(1)
		go func() {
			defer wg.Done()
			fill(items[start:end], ids[start:end])
		}()
	}
	wg.Wait()
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

// Radix digit slots, in the order the passes run (least significant first):
// the ID's two halves, then the lo word's and the hi word's four 16-bit digits.
const (
	slotID0, slotID1 = 0, 1
	slotLo           = 2 // slotLo+k holds lo's digit k, k = 0..3
	slotHi           = 6 // slotHi+k holds hi's digit k
	slotCount        = 10
)

// radixSort orders items by hi, lo and ID with stable 16-bit passes, least
// significant first. A digit in which every item agrees is skipped, so small
// keys cost few passes. The varying digits are found first with one cheap
// scan, and their histograms are counted in a second.
func radixSort(ctx context.Context, items []keyed, useLo bool) error {
	first := items[0]
	var diffLo, diffHi uint64
	var diffID uint32
	for i := range items {
		v := &items[i]
		diffID |= uint32(v.id ^ first.id)
		diffHi |= v.hi ^ first.hi
		if useLo {
			diffLo |= v.lo ^ first.lo
		}
	}
	// The active slots, least significant first.
	var order []int
	for k := range 2 {
		if diffID>>(16*k)&0xffff != 0 {
			order = append(order, slotID0+k)
		}
	}
	if useLo {
		for k := range 4 {
			if diffLo>>(16*k)&0xffff != 0 {
				order = append(order, slotLo+k)
			}
		}
	}
	for k := range 4 {
		if diffHi>>(16*k)&0xffff != 0 {
			order = append(order, slotHi+k)
		}
	}
	if len(order) == 0 {
		return ctx.Err()
	}
	counts := make([]uint32, len(order)<<16)
	for i := range items {
		v := &items[i]
		for p, slot := range order {
			counts[p<<16|radixDigit(v, slot)]++
		}
	}
	src, dst := items, make([]keyed, len(items))
	for p, slot := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		hist := counts[p<<16 : p<<16+1<<16]
		sum := uint32(0)
		for i, c := range hist {
			hist[i] = sum
			sum += c
		}
		for i := range src {
			d := radixDigit(&src[i], slot)
			dst[hist[d]] = src[i]
			hist[d]++
		}
		src, dst = dst, src
	}
	if &src[0] != &items[0] {
		copy(items, src)
	}
	return nil
}

// radixDigit returns the 16-bit digit of an item in a radix slot.
func radixDigit(v *keyed, slot int) int {
	switch {
	case slot == slotID0:
		return int(v.id & 0xffff)
	case slot == slotID1:
		return int(v.id >> 16)
	case slot < slotHi:
		return int(v.lo >> (16 * (slot - slotLo)) & 0xffff)
	}
	return int(v.hi >> (16 * (slot - slotHi)) & 0xffff)
}
