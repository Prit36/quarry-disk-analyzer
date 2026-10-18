package index

import (
	"context"
	"runtime"
	"sync"
)

// Stable byte passes keep histograms in cache. Workers count disjoint source
// ranges, then receive disjoint destination ranges for each digit. Prefixes
// are assigned in source-range order, preserving stability across workers.
func sortNumeric(ctx context.Context, ids []ID, key func(ID) uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	keys := make([]uint64, len(ids))
	var diff uint64
	var diffID uint32
	orderedIDs := true
	for i, id := range ids {
		if i&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		keys[i] = key(id)
		diff |= keys[i] ^ keys[0]
		diffID |= uint32(id ^ ids[0])
		if i > 0 && id < ids[i-1] {
			orderedIDs = false
		}
	}
	var slots [12]int
	n := 0
	if !orderedIDs {
		for k := range 4 {
			if diffID>>(8*k)&255 != 0 {
				slots[n], n = k, n+1
			}
		}
	}
	for k := range 8 {
		if diff>>(8*k)&255 != 0 {
			slots[n], n = k+4, n+1
		}
	}
	if n == 0 {
		return ctx.Err()
	}
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)))
	run := func(f func(worker, start, end int)) {
		var wg sync.WaitGroup
		for w := 1; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f(w, len(ids)*w/workers, len(ids)*(w+1)/workers)
			}()
		}
		f(0, 0, len(ids)/workers)
		wg.Wait()
	}
	hist := make([]uint32, workers*256)
	srcIDs, dstIDs := ids, make([]ID, len(ids))
	srcKeys, dstKeys := keys, make([]uint64, len(ids))
	for _, slot := range slots[:n] {
		if err := ctx.Err(); err != nil {
			return err
		}
		clear(hist)
		run(func(w, start, end int) {
			counts := hist[w*256 : (w+1)*256]
			if slot < 4 {
				shift := uint(slot * 8)
				for _, id := range srcIDs[start:end] {
					counts[uint32(id)>>shift&255]++
				}
			} else {
				shift := uint((slot - 4) * 8)
				for _, k := range srcKeys[start:end] {
					counts[k>>shift&255]++
				}
			}
		})
		var sum uint32
		for digit := range 256 {
			for w := range workers {
				p := w*256 + digit
				hist[p], sum = sum, sum+hist[p]
			}
		}
		run(func(w, start, end int) {
			counts := hist[w*256 : (w+1)*256]
			if slot < 4 {
				shift := uint(slot * 8)
				for i := start; i < end; i++ {
					d := uint32(srcIDs[i]) >> shift & 255
					p := counts[d]
					dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
					counts[d]++
				}
			} else {
				shift := uint((slot - 4) * 8)
				for i := start; i < end; i++ {
					d := srcKeys[i] >> shift & 255
					p := counts[d]
					dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
					counts[d]++
				}
			}
		})
		srcIDs, dstIDs = dstIDs, srcIDs
		srcKeys, dstKeys = dstKeys, srcKeys
	}
	copy(ids, srcIDs)
	return ctx.Err()
}
