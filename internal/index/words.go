package index

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
)

type wordKey struct{ hi, lo uint64 }

func (s *Snapshot) sortWordKeys(ctx context.Context, ids []ID, key func(ID) wordKey, tie *Sort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	keys := make([]wordKey, len(ids))
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
	run(func(w, start, end int) {
		for start < end {
			if ctx.Err() != nil {
				return
			}
			next := min(start+4096, end)
			partKeys, partIDs := keys[start:next], ids[start:next]
			for i, id := range partIDs {
				partKeys[i] = key(id)
			}
			start = next
		}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	var diff wordKey
	var diffID uint32
	orderedIDs := true
	for i, id := range ids {
		diff.hi |= keys[i].hi ^ keys[0].hi
		diff.lo |= keys[i].lo ^ keys[0].lo
		diffID |= uint32(id ^ ids[0])
		if i > 0 && id < ids[i-1] {
			orderedIDs = false
		}
	}
	var slots [20]int
	n := 0
	if !orderedIDs {
		for k := range 4 {
			if diffID>>(8*k)&255 != 0 {
				slots[n], n = k, n+1
			}
		}
	}
	for k := range 8 {
		if diff.lo>>(8*k)&255 != 0 {
			slots[n], n = k+4, n+1
		}
	}
	for k := range 8 {
		if diff.hi>>(8*k)&255 != 0 {
			slots[n], n = k+12, n+1
		}
	}
	if n > 0 {
		hist := make([]uint32, workers*256)
		srcIDs, dstIDs := ids, make([]ID, len(ids))
		srcKeys, dstKeys := keys, make([]wordKey, len(ids))
		for _, slot := range slots[:n] {
			if err := ctx.Err(); err != nil {
				return err
			}
			clear(hist)
			run(func(w, start, end int) {
				counts := hist[w*256 : (w+1)*256]
				for i := start; i < end; i++ {
					counts[wordDigit(srcIDs[i], srcKeys[i], slot)]++
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
				for i := start; i < end; i++ {
					d := wordDigit(srcIDs[i], srcKeys[i], slot)
					p := counts[d]
					dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
					counts[d]++
				}
			})
			srcIDs, dstIDs = dstIDs, srcIDs
			srcKeys, dstKeys = dstKeys, srcKeys
		}
		copy(ids, srcIDs)
		keys = srcKeys
	}
	if tie != nil {
		nextCheck := 0
		for start := 0; start < len(ids); {
			if start >= nextCheck {
				if err := ctx.Err(); err != nil {
					return err
				}
				nextCheck = start + 4096
			}
			end := start + 1
			for end < len(ids) && keys[end] == keys[start] {
				end++
			}
			if end-start > 1 {
				if end-start >= 4096 {
					if err := s.sortCompare(ctx, ids[start:end], *tie); err != nil {
						return err
					}
				} else {
					slices.SortFunc(ids[start:end], func(a, b ID) int { return s.Compare(a, b, *tie) })
				}
			}
			start = end
		}
	}
	return ctx.Err()
}

// Fold a Unicode name once and pack both prefix words in the same pass.
func (s *Snapshot) nameWords(id ID) wordKey {
	name := s.NameView(id)
	for i := range len(name) {
		if name[i] >= 128 {
			name = strings.ToLower(name)
			break
		}
	}
	var k wordKey
	for i := range 16 {
		var b byte
		if i < len(name) {
			b = name[i]
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
		}
		if i < 8 {
			k.hi = k.hi<<8 | uint64(b)
		} else {
			k.lo = k.lo<<8 | uint64(b)
		}
	}
	return k
}

func wordDigit(id ID, k wordKey, slot int) byte {
	if slot < 4 {
		return byte(uint32(id) >> uint(slot*8))
	}
	if slot < 12 {
		return byte(k.lo >> uint((slot-4)*8))
	}
	return byte(k.hi >> uint((slot-12)*8))
}
