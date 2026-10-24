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
	return s.sortWordKeysFrom(ctx, ids, key, tie, 0)
}

func (s *Snapshot) sortWordKeysFrom(ctx context.Context, ids []ID, key func(ID) wordKey, tie *Sort, namePrefix int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) < 2 {
		return nil
	}
	pKeys, keys := getWordKeys(len(ids))
	defer putWordKeys(pKeys)
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
	// Derived-key comparators resolve their own ID ties. Name sorting still
	// benefits from preordering IDs: real scans contain many identical names.
	if !orderedIDs && (tie == nil || namePrefix > 0) {
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
		pHist, hist := getUint32s(workers * 256)
		defer putUint32s(pHist)
		pDstIDs, dstIDs := getIDs(len(ids))
		defer putIDs(pDstIDs)
		pDstKeys, dstKeys := getWordKeys(len(ids))
		defer putWordKeys(pDstKeys)
		srcIDs := ids
		srcKeys := keys
		for pass, slot := range slots[:n] {
			if err := ctx.Err(); err != nil {
				return err
			}
			clear(hist)
			run(func(w, start, end int) {
				counts := hist[w*256 : (w+1)*256]
				switch {
				case slot < 4:
					shift := uint(slot * 8)
					for i := start; i < end; i++ {
						counts[byte(uint32(srcIDs[i])>>shift)]++
					}
				case slot < 12:
					shift := uint((slot - 4) * 8)
					for i := start; i < end; i++ {
						counts[byte(srcKeys[i].lo>>shift)]++
					}
				default:
					shift := uint((slot - 12) * 8)
					for i := start; i < end; i++ {
						counts[byte(srcKeys[i].hi>>shift)]++
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
			isLast := (pass == n-1)
			run(func(w, start, end int) {
				counts := hist[w*256 : (w+1)*256]
				if isLast && tie == nil {
					switch {
					case slot < 4:
						shift := uint(slot * 8)
						for i := start; i < end; i++ {
							d := byte(uint32(srcIDs[i]) >> shift)
							p := counts[d]
							dstIDs[p] = srcIDs[i]
							counts[d]++
						}
					case slot < 12:
						shift := uint((slot - 4) * 8)
						for i := start; i < end; i++ {
							d := byte(srcKeys[i].lo >> shift)
							p := counts[d]
							dstIDs[p] = srcIDs[i]
							counts[d]++
						}
					default:
						shift := uint((slot - 12) * 8)
						for i := start; i < end; i++ {
							d := byte(srcKeys[i].hi >> shift)
							p := counts[d]
							dstIDs[p] = srcIDs[i]
							counts[d]++
						}
					}
				} else {
					switch {
					case slot < 4:
						shift := uint(slot * 8)
						for i := start; i < end; i++ {
							d := byte(uint32(srcIDs[i]) >> shift)
							p := counts[d]
							dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
							counts[d]++
						}
					case slot < 12:
						shift := uint((slot - 4) * 8)
						for i := start; i < end; i++ {
							d := byte(srcKeys[i].lo >> shift)
							p := counts[d]
							dstIDs[p], dstKeys[p] = srcIDs[i], srcKeys[i]
							counts[d]++
						}
					default:
						shift := uint((slot - 12) * 8)
						for i := start; i < end; i++ {
							d := byte(srcKeys[i].hi >> shift)
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
		keys = srcKeys
	}
	if tie != nil {
		// Prefix ties are independent. Real scans have many long shared names
		// (assemblies and package files), so resolving every tie on one core
		// can cost more than the parallel radix passes themselves.
		bounds := make([]int, workers+1)
		bounds[workers] = len(ids)
		for w := 1; w < workers; w++ {
			position := max(len(ids)*w/workers, bounds[w-1])
			for position < len(ids) && position > 0 && keys[position] == keys[position-1] {
				if position&4095 == 0 && ctx.Err() != nil {
					return ctx.Err()
				}
				position++
			}
			bounds[w] = position
		}
		run(func(w, _, _ int) {
			nextCheck := bounds[w]
			for start := bounds[w]; start < bounds[w+1]; {
				if start >= nextCheck {
					if err := ctx.Err(); err != nil {
						return
					}
					nextCheck = start + 4096
				}
				end := start + 1
				for end < len(ids) && keys[end] == keys[start] {
					end++
				}
				if end-start > 1 {
					// Equal ASCII prefixes were already ordered by the radix
					// passes. Check them once per entry instead of comparing the
					// same sixteen bytes again at every comparison in this run.
					prefix := namePrefix
					if prefix > 0 {
						for _, id := range ids[start:end] {
							name := s.NameView(id)
							if len(name) < prefix || !isASCII(name[:prefix]) {
								prefix = 0
								break
							}
						}
					}
					if prefix > 0 {
						comparisons := 0
						canceled := false
						slices.SortFunc(ids[start:end], func(a, b ID) int {
							comparisons++
							if comparisons&4095 == 0 && ctx.Err() != nil {
								canceled = true
							}
							if canceled {
								return 0
							}
							v := compareFold(s.NameView(a)[prefix:], s.NameView(b)[prefix:])
							if tie.Desc {
								v = -v
							}
							if v != 0 {
								return v
							}
							if a < b {
								return -1
							}
							if a > b {
								return 1
							}
							return 0
						})
						start = end
						continue
					}
					if end-start >= 4096 {
						if err := s.sortCompare(ctx, ids[start:end], *tie); err != nil {
							return
						}
					} else {
						slices.SortFunc(ids[start:end], func(a, b ID) int { return s.Compare(a, b, *tie) })
					}
				}
				start = end
			}
		})
	}
	return ctx.Err()
}

// Fold a Unicode name once and pack both prefix words in the same pass.
func (s *Snapshot) nameWords(id ID) wordKey {
	name := s.NameView(id)
	isASCII := true
	for i := 0; i < len(name); i++ {
		if name[i] >= 128 {
			isASCII = false
			name = strings.ToLower(name)
			break
		}
	}
	var hi, lo uint64
	n := min(len(name), 16)
	limit8 := min(n, 8)
	for i := 0; i < limit8; i++ {
		b := name[i]
		if isASCII && b >= 'A' && b <= 'Z' {
			b += 32
		}
		hi = (hi << 8) | uint64(b)
	}
	if limit8 < 8 {
		hi <<= 8 * (8 - limit8)
	}
	if n > 8 {
		for i := 8; i < n; i++ {
			b := name[i]
			if isASCII && b >= 'A' && b <= 'Z' {
				b += 32
			}
			lo = (lo << 8) | uint64(b)
		}
		if n < 16 {
			lo <<= 8 * (16 - n)
		}
	}
	return wordKey{hi: hi, lo: lo}
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
