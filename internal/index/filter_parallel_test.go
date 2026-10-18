package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestFilteredParallelMatchesFiltered(t *testing.T) {
	r := rand.New(rand.NewPCG(41, 7))
	for _, shape := range []string{"random", "deep", "flat", "wide"} {
		s := New(`C:\parallel`)
		dirs := []ID{0}
		for i := range 20000 {
			parent := dirs[r.IntN(len(dirs))]
			switch shape {
			case "deep":
				parent = dirs[len(dirs)-1-r.IntN(min(3, len(dirs)))]
			case "flat":
				parent = dirs[r.IntN(min(4, len(dirs)))]
			}
			flags := uint16(0)
			if i%4 == 0 || shape == "wide" && i%2 == 0 {
				flags = Directory
			}
			id := s.Add(parent, fmt.Sprint(i), uint64(i%19), uint64(i%23), 0, flags)
			if flags == Directory {
				dirs = append(dirs, id)
			}
		}
		s.Finalize()
		s.Remove(dirs[7])
		original := slices.Clone(s.Nodes)
		for _, isolate := range []bool{false, true} {
			for mode := range 5 {
				keep := func(id ID) bool { return mode == 0 || mode == 1 && id%7 == 0 || mode >= 3 && id%3 != 0 }
				drop := func(id ID) bool { return mode == 2 && id%11 == 0 || mode == 4 && id%5 == 0 }
				want, wantStats, err := s.Filtered(context.Background(), keep, drop, isolate)
				if err != nil {
					t.Fatal(err)
				}
				for _, workers := range []int{2, 3, 8} {
					got, gotStats, err := s.filteredParallel(context.Background(), keep, drop, isolate, workers, 0)
					if err != nil || gotStats != wantStats || !slices.Equal(got.Nodes, want.Nodes) || !slices.Equal(s.Nodes, original) {
						t.Fatalf("%s isolate=%v mode=%d workers=%d: mismatch (%v, %+v vs %+v)", shape, isolate, mode, workers, err, gotStats, wantStats)
					}
				}
			}
		}
	}
	// Imported trees with children under files take the general path.
	s := New(`C:\odd`)
	f := s.Add(0, "file", 1, 1, 0, 0)
	s.Add(f, "child", 2, 2, 0, 0)
	s.Finalize()
	want, _, _ := s.Filtered(context.Background(), nil, nil, true)
	got, _, err := s.filteredParallel(context.Background(), nil, nil, true, 4, 0)
	if err != nil || !slices.Equal(got.Nodes, want.Nodes) {
		t.Fatal("irregular tree differs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _, err := flatSearchSnapshot(10000).filteredParallel(ctx, nil, nil, false, 4, 0); got != nil || err == nil {
		t.Fatal("canceled filter returned a result")
	}
}
