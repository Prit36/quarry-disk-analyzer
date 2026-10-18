package index

import (
	"context"
	"runtime"
	"sync/atomic"
)

// SortByRank groups a bounded, dense rank (for example an interned extension
// or folder) in one counting pass, then sorts within each group. It avoids
// carrying two full-width keys through every radix pass. Ranks must be in
// [0, maxRank]; ties follow Compare exactly, including ascending ID ties.
func (s *Snapshot) SortByRank(ctx context.Context, ids []ID, rank func(ID) uint32, maxRank uint32, desc bool, tie Sort) error {
	return s.SortByRankGroups(ctx, ids, rank, maxRank, desc, tie, nil)
}

// SortByRankGroups also returns immutable lengths of the nonempty rank
// groups, in sorted order. Dense ranks reuse the counting pass's boundaries
// rather than looking up every entry's derived key a second time.
func (s *Snapshot) SortByRankGroups(ctx context.Context, ids []ID, rank func(ID) uint32, maxRank uint32, desc bool, tie Sort, groups *[]uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		if groups != nil {
			*groups = nil
			if len(ids) == 1 {
				*groups = []uint32{1}
			}
		}
		return nil
	}
	if uint64(maxRank)+1 > uint64(max(4096, 2*len(ids))) {
		if err := s.SortByKeys(ctx, ids, func(id ID) uint64 {
			r := uint64(rank(id))
			if desc {
				r = ^r
			}
			return r
		}, nil, &tie); err != nil {
			return err
		}
		if groups != nil {
			var out []uint32
			start, last := 0, rank(ids[0])
			for i, id := range ids {
				if i&4095 == 0 && ctx.Err() != nil {
					return ctx.Err()
				}
				if next := rank(id); next != last {
					out = append(out, uint32(i-start))
					start, last = i, next
				}
			}
			*groups = append(out, uint32(len(ids)-start))
		}
		return ctx.Err()
	}
	buckets := int(maxRank) + 1
	// Bound histograms even for scans with almost one distinct folder per
	// result. Small scans also avoid goroutine setup.
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)), max(1, (8<<20)/(4*buckets)))
	hist := make([]uint32, workers*buckets)
	ranks := make([]uint32, len(ids))
	runSortWorkers(len(ids), workers, func(w, start, end int) {
		counts := hist[w*buckets : (w+1)*buckets]
		for i := start; i < end; i++ {
			if i&4095 == 0 && ctx.Err() != nil {
				return
			}
			r := rank(ids[i])
			if desc {
				r = maxRank - r
			}
			ranks[i] = r
			counts[r]++
		}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	bounds := make([]uint32, buckets+1)
	var sum uint32
	for d := range buckets {
		bounds[d] = sum
		for w := range workers {
			p := w*buckets + d
			hist[p], sum = sum, sum+hist[p]
		}
	}
	bounds[buckets] = sum
	spare := make([]ID, len(ids))
	runSortWorkers(len(ids), workers, func(w, start, end int) {
		counts := hist[w*buckets : (w+1)*buckets]
		for i := start; i < end; i++ {
			if i&4095 == 0 && ctx.Err() != nil {
				return
			}
			r := ranks[i]
			spare[counts[r]] = ids[i]
			counts[r]++
		}
	})
	copy(ids, spare)
	// No group writes outside its boundaries; small groups use comparison
	// sorting and large groups choose the existing numeric/name radix path.
	var next atomic.Int64
	workers = min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)))
	runSortWorkers(workers, workers, func(_, _, _ int) {
		for {
			start := int(next.Add(64) - 64)
			if start >= buckets || ctx.Err() != nil {
				return
			}
			for d := start; d < min(start+64, buckets); d++ {
				if bounds[d+1]-bounds[d] > 1 {
					_ = s.SortContext(ctx, ids[bounds[d]:bounds[d+1]], tie)
				}
			}
		}
	})
	if groups != nil && ctx.Err() == nil {
		// Compact lengths in place after all workers finish reading bounds.
		// No per-entry pass, extra group buffer, or retained rank table.
		out := bounds[:0]
		for d := range buckets {
			if d&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			if length := bounds[d+1] - bounds[d]; length != 0 {
				out = append(out, length)
			}
		}
		*groups = out[:len(out):len(out)]
	}
	return ctx.Err()
}
