package index

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Sort the first eight folded bytes, then read later chunks only in runs
// that actually share that prefix. Two eight-byte key buffers and one ID
// buffer cost 20 bytes/entry; the former full-prefix sort needed 36.
func (s *Snapshot) sortNames(ctx context.Context, ids []ID, by Sort) error {
	workers := min(runtime.GOMAXPROCS(0), max(1, len(ids)/(1<<16)))
	return s.sortNameChunks(ctx, ids, make([]ID, len(ids)), make([]uint64, len(ids)), make([]uint64, len(ids)), make([]uint32, workers*2048), 0, workers, by)
}

func runSortWorkers(n, workers int, f func(w, start, end int)) {
	var wg sync.WaitGroup
	for w := 1; w < workers; w++ {
		wg.Add(1)
		go func() { defer wg.Done(); f(w, n*w/workers, n*(w+1)/workers) }()
	}
	f(0, 0, n/workers)
	wg.Wait()
}

func (s *Snapshot) sortNameChunks(ctx context.Context, ids, spareIDs []ID, keys, spareKeys []uint64, hist []uint32, offset, workers int, by Sort) error {
	const digitBits, digits = 11, 1 << 11
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Very long shared names must not cause an unbounded number of full
		// name scans and radix passes. Resolve the remaining suffix directly.
		if offset >= 64 {
			return s.sortNameTie(ctx, ids, by, offset)
		}
		diffs := make([]uint64, workers)
		finished := make([]bool, workers)
		runSortWorkers(len(ids), workers, func(w, start, end int) {
			finished[w] = true
			var first uint64
			for i := start; i < end; i++ {
				if i&4095 == 0 && ctx.Err() != nil {
					return
				}
				name := s.NameView(ids[i])
				if !isASCII(name) {
					name = strings.ToLower(name)
				}
				finished[w] = finished[w] && len(name) <= offset+8
				k, ok := foldedWord(name, offset)
				if !ok {
					for j := offset; j < offset+8; j++ {
						var ch byte
						if j < len(name) {
							ch = name[j]
							if ch >= 'A' && ch <= 'Z' {
								ch += 32
							}
						}
						k = k<<8 | uint64(ch)
					}
				}
				if by.Desc {
					k = ^k
				}
				keys[i] = k
				if i == start {
					first = k
				}
				diffs[w] |= k ^ first
			}
		})
		if err := ctx.Err(); err != nil {
			return err
		}
		var diff uint64
		done := true
		for w := range workers {
			diff |= diffs[w] | (keys[len(ids)*w/workers] ^ keys[0])
			done = done && finished[w]
		}
		if diff == 0 {
			if done {
				return s.sortNameTie(ctx, ids, by, offset)
			}
			offset += 8
			continue
		}
		srcIDs, dstIDs := ids, spareIDs
		srcKeys, dstKeys := keys, spareKeys
		for shift := uint(0); shift < 64; shift += digitBits {
			if diff>>shift&(digits-1) == 0 {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			clear(hist)
			runSortWorkers(len(ids), workers, func(w, start, end int) {
				counts := hist[w*digits : (w+1)*digits]
				for _, k := range srcKeys[start:end] {
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
			runSortWorkers(len(ids), workers, func(w, start, end int) {
				counts := hist[w*digits : (w+1)*digits]
				for i := start; i < end; i++ {
					k := srcKeys[i]
					d := k >> shift & (digits - 1)
					p := counts[d]
					dstKeys[p], dstIDs[p] = k, srcIDs[i]
					counts[d]++
				}
			})
			srcIDs, dstIDs, srcKeys, dstKeys = dstIDs, srcIDs, dstKeys, srcKeys
		}
		if &ids[0] != &srcIDs[0] {
			copy(ids, srcIDs)
		}
		keys, spareKeys = srcKeys, dstKeys
		type span struct{ start, end int }
		spans := make([]span, 0, len(ids)/4096+1)
		for start := 0; start < len(ids); {
			end := min(start+4096, len(ids))
			for end < len(ids) && keys[end] == keys[end-1] {
				end++
			}
			spans = append(spans, span{start, end})
			start = end
		}
		var next atomic.Int64
		runSortWorkers(workers, workers, func(w, _, _ int) {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(spans) || ctx.Err() != nil {
					return
				}
				start, limit := spans[i].start, spans[i].end
				for start < limit {
					end := start + 1
					for end < len(ids) && keys[end] == keys[start] {
						end++
					}
					part := ids[start:end]
					if len(part) >= 2048 && !done {
						_ = s.sortNameChunks(ctx, part, spareIDs[start:end], keys[start:end], spareKeys[start:end], hist[w*digits:(w+1)*digits], offset+8, 1, by)
					} else if len(part) > 1 {
						_ = s.sortNameTie(ctx, part, by, offset+8)
					}
					start = end
				}
			}
		})
		return ctx.Err()
	}
}

// Skip a prefix only after verifying that every original name contains that
// many ASCII bytes. Short names, embedded zeros, and Unicode folds retain the
// full comparator's length and case rules.
func (s *Snapshot) sortNameTie(ctx context.Context, ids []ID, by Sort, prefix int) error {
	for i, id := range ids {
		if i&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		name := s.NameView(id)
		if len(name) < prefix || !isASCII(name[:prefix]) {
			return s.sortCompare(ctx, ids, by)
		}
	}
	if len(ids) >= keyedNameSort {
		s.sortNamesKeyed(ids, by.Desc, prefix)
		return ctx.Err()
	}
	comparisons := 0
	canceled := false
	slices.SortFunc(ids, func(a, b ID) int {
		comparisons++
		if comparisons&4095 == 0 && ctx.Err() != nil {
			canceled = true
		}
		if canceled {
			return 0
		}
		v := compareFold(s.NameView(a)[prefix:], s.NameView(b)[prefix:])
		if by.Desc {
			v = -v
		}
		if v == 0 {
			return cmp.Compare(a, b)
		}
		return v
	})
	return ctx.Err()
}
