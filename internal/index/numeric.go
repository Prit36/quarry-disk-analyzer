package index

import (
	"context"
	"math/bits"
	"runtime"
	"sync"
)

// Eleven-bit passes keep each worker's histogram in cache while reducing
// full-array scans and scatters. Workers count disjoint source
// ranges, then receive disjoint destination ranges for each digit. Prefixes
// are assigned in source-range order, preserving stability across workers.
func sortNumeric(ctx context.Context, ids []ID, key func(ID) uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	const digitBits, digits = 11, 1 << 11
	keys := make([]uint64, len(ids))
	var diff uint64
	var diffID uint32
	var maxID uint32
	orderedIDs := true
	for i, id := range ids {
		if i&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		keys[i] = key(id)
		diff |= keys[i] ^ keys[0]
		diffID |= uint32(id ^ ids[0])
		maxID = max(maxID, uint32(id))
		if i > 0 && id < ids[i-1] {
			orderedIDs = false
		}
	}
	// Most sizes and timestamps, together with the ID tie breaker, fit in
	// one word. Scatter only that word instead of separate keys and IDs.
	// Common high key bits do not affect ordering and can be omitted.
	if keyBits, idBits := bits.Len64(diff), bits.Len32(maxID); keyBits+idBits <= 64 {
		mask := ^uint64(0) >> (64 - keyBits)
		for i, id := range ids {
			keys[i] = (keys[i]&mask)<<idBits | uint64(id)
		}
		return sortPackedNumeric(ctx, ids, keys, uint(idBits))
	}
	var slots [12]int
	n := 0
	if !orderedIDs {
		for k := range 3 {
			if diffID>>(digitBits*k)&(digits-1) != 0 {
				slots[n], n = k, n+1
			}
		}
	}
	for k := range 6 {
		if diff>>(digitBits*k)&(digits-1) != 0 {
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
	hist := make([]uint32, workers*digits)
	srcIDs, dstIDs := ids, make([]ID, len(ids))
	srcKeys, dstKeys := keys, make([]uint64, len(ids))
	for pass, slot := range slots[:n] {
		if err := ctx.Err(); err != nil {
			return err
		}
		clear(hist)
		run(func(w, start, end int) {
			counts := hist[w*digits : (w+1)*digits]
			if slot < 4 {
				shift := uint(slot * digitBits)
				for _, id := range srcIDs[start:end] {
					counts[uint32(id)>>shift&(digits-1)]++
				}
			} else {
				shift := uint((slot - 4) * digitBits)
				for _, k := range srcKeys[start:end] {
					counts[k>>shift&(digits-1)]++
				}
			}
		})
		var sum uint32
		for digit := range digits {
			for w := range workers {
				p := w*digits + digit
				hist[p], sum = sum, sum+hist[p]
			}
		}
		isLast := (pass == n-1)
		run(func(w, start, end int) {
			counts := hist[w*digits : (w+1)*digits]
			if isLast {
				if slot < 4 {
					shift := uint(slot * digitBits)
					for i := start; i < end; i++ {
						d := uint32(srcIDs[i]) >> shift & (digits - 1)
						p := counts[d]
						dstIDs[p] = srcIDs[i]
						counts[d]++
					}
				} else {
					shift := uint((slot - 4) * digitBits)
					for i := start; i < end; i++ {
						d := srcKeys[i] >> shift & (digits - 1)
						p := counts[d]
						dstIDs[p] = srcIDs[i]
						counts[d]++
					}
				}
			} else {
				if slot < 4 {
					shift := uint(slot * digitBits)
					for i := start; i < end; i++ {
						d := uint32(srcIDs[i]) >> shift & (digits - 1)
						p := counts[d]
						dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
						counts[d]++
					}
				} else {
					shift := uint((slot - 4) * digitBits)
					for i := start; i < end; i++ {
						d := srcKeys[i] >> shift & (digits - 1)
						p := counts[d]
						dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
						counts[d]++
					}
				}
			}
		})
		srcIDs, dstIDs = dstIDs, srcIDs
		srcKeys, dstKeys = dstKeys, srcKeys
	}
	if &ids[0] != &srcIDs[0] {
		copy(ids, srcIDs)
	}
	return ctx.Err()
}

func sortPackedNumeric(ctx context.Context, ids []ID, keys []uint64, idBits uint) error {
	const digitBits, digits = 11, 1 << 11
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)))
	run := func(f func(w, start, end int)) {
		var wg sync.WaitGroup
		for w := 1; w < workers; w++ {
			wg.Add(1)
			go func() { defer wg.Done(); f(w, len(ids)*w/workers, len(ids)*(w+1)/workers) }()
		}
		f(0, 0, len(ids)/workers)
		wg.Wait()
	}
	var diff uint64
	for _, k := range keys {
		diff |= k ^ keys[0]
	}
	hist := make([]uint32, workers*digits)
	src, dst := keys, make([]uint64, len(keys))
	for shift := uint(0); shift < 64; shift += digitBits {
		if err := ctx.Err(); err != nil {
			return err
		}
		if diff>>shift&(digits-1) == 0 {
			continue
		}
		clear(hist)
		run(func(w, start, end int) {
			counts := hist[w*digits : (w+1)*digits]
			for _, k := range src[start:end] {
				counts[k>>shift&(digits-1)]++
			}
		})
		var sum uint32
		for d := range digits {
			for w := range workers {
				p := w*digits + d
				hist[p], sum = sum, sum+hist[p]
			}
		}
		run(func(w, start, end int) {
			counts := hist[w*digits : (w+1)*digits]
			for _, k := range src[start:end] {
				d := k >> shift & (digits - 1)
				dst[counts[d]] = k
				counts[d]++
			}
		})
		src, dst = dst, src
	}
	mask := uint64(1)<<idBits - 1
	for i, k := range src {
		ids[i] = ID(k & mask)
	}
	return ctx.Err()
}
