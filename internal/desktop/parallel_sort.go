package desktop

import (
	"context"
	"runtime"
	"slices"
	"sync"
)

// parallelSort divides a large comparison sort into disjoint ranges. Only
// log2(GOMAXPROCS) partition levels run before the standard library sorts
// each range, bounding partition work even on badly distributed input.
// Three-way partitioning leaves equal values together without extra storage.
func parallelSort[T any](ctx context.Context, values []T, compare func(T, T) int) error {
	var sortRange func([]T, int)
	sortRange = func(part []T, workers int) {
		if ctx.Err() != nil || len(part) < 2 {
			return
		}
		if workers < 2 || len(part) < 32768 {
			calls, canceled := 0, false
			slices.SortFunc(part, func(a, b T) int {
				calls++
				if calls&4095 == 0 && ctx.Err() != nil {
					canceled = true
				}
				if canceled {
					return 0
				}
				return compare(a, b)
			})
			return
		}
		a, b, c := part[0], part[len(part)/2], part[len(part)-1]
		if compare(a, b) > 0 {
			a, b = b, a
		}
		if compare(b, c) > 0 {
			b = c
			if compare(a, b) > 0 {
				b = a
			}
		}
		pivot := b
		lo, current, hi := 0, 0, len(part)
		for current < hi {
			if current&4095 == 0 && ctx.Err() != nil {
				return
			}
			switch v := compare(part[current], pivot); {
			case v < 0:
				part[lo], part[current] = part[current], part[lo]
				lo++
				current++
			case v > 0:
				hi--
				part[current], part[hi] = part[hi], part[current]
			default:
				current++
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); sortRange(part[:lo], workers/2) }()
		sortRange(part[hi:], workers-workers/2)
		wg.Wait()
	}
	sortRange(values, runtime.GOMAXPROCS(0))
	return ctx.Err()
}
