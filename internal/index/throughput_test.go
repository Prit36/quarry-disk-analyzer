package index

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestAdaptiveNameSortShapes(t *testing.T) {
	before := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(before)
	r := rand.New(rand.NewPCG(91, 72))
	for _, workers := range []int{1, 2, 12} {
		runtime.GOMAXPROCS(workers)
		for _, prefix := range []string{"", strings.Repeat("sHARED", 12), strings.Repeat("a", 256)} {
			s := New(`C:\sort`)
			var ids []ID
			names := []string{"", "a", "A", "a\x00", "a\x00b", "résumé", "RÉSUMÉ", "Ⱥ", "ⱥ", "İ", "i", "K", "k", "日本語"}
			for i := range 8000 {
				name := prefix + names[r.IntN(len(names))]
				if i%3 == 0 {
					name += fmt.Sprint(r.IntN(1000))
				}
				ids = append(ids, s.Add(0, name, 0, 0, 0, 0))
			}
			ids = append(ids, ids[17], ids[180])
			for _, desc := range []bool{false, true} {
				by := Sort{Column: "Name", Desc: desc}
				want, got := slices.Clone(ids), slices.Clone(ids)
				r.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
				s.Sort(want, by)
				// Force each partition count without enlarging the fixture to
				// the production parallel-sort threshold for every test case.
				if err := s.sortNameChunks(context.Background(), got, make([]ID, len(got)), make([]uint64, len(got)), make([]uint64, len(got)), make([]uint32, workers*2048), 0, workers, by); err != nil || !slices.Equal(got, want) {
					t.Fatalf("workers %d prefix length %d descending %v: %v", workers, len(prefix), desc, err)
				}
			}
		}
	}
}

func TestRankedSortMatchesFullComparator(t *testing.T) {
	r := rand.New(rand.NewPCG(51, 79))
	s := New(`C:\rank`)
	var ids []ID
	var ranks []uint32
	ranks = append(ranks, 0)
	for range 100000 {
		ids = append(ids, s.Add(0, fmt.Sprintf("shared-prefix-%d", r.IntN(100)), uint64(r.IntN(500)), uint64(r.IntN(500)), 0, 0))
		ranks = append(ranks, uint32(r.IntN(200)))
	}
	for _, sparse := range []bool{false, true} {
		maxRank := uint32(199)
		rank := func(id ID) uint32 {
			if sparse {
				return ranks[id] << 24
			}
			return ranks[id]
		}
		if sparse {
			maxRank = ^uint32(0)
		}
		for _, desc := range []bool{false, true} {
			for _, tie := range []Sort{{Column: "Name"}, {Desc: true}, {Desc: true, Allocated: true}} {
				want, got := slices.Clone(ids), slices.Clone(ids)
				slices.SortFunc(want, func(a, b ID) int {
					c := cmp.Compare(rank(a), rank(b))
					if desc {
						c = -c
					}
					if c == 0 {
						c = s.Compare(a, b, tie)
					}
					return c
				})
				if err := s.SortByRank(context.Background(), got, rank, maxRank, desc, tie); err != nil || !slices.Equal(got, want) {
					t.Fatalf("sparse %v descending %v tie %+v: %v", sparse, desc, tie, err)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	if err := s.SortByRank(ctx, ids, func(id ID) uint32 {
		calls++
		if calls == 1000 {
			cancel()
		}
		return ranks[id]
	}, 199, false, Sort{}); err != context.Canceled {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestReverseSortedPreservesTies(t *testing.T) {
	s := New(`C:\reverse`)
	var ids []ID
	names := []string{"a", "A", "K", "k", "Σ", "ς", "Ⱥ", "ⱥ", "日本"}
	for i := range 5000 {
		ids = append(ids, s.Add(0, names[i%len(names)], uint64(i%13), uint64(i%19), int64(i%17), 0))
	}
	for _, column := range []string{"Name", "Size", "Allocated", "Modified", "Files", "Folders", "% of Parent", ""} {
		for _, allocated := range []bool{false, true} {
			by := Sort{Column: column, Allocated: allocated}
			got := slices.Clone(ids)
			s.Sort(got, by)
			original := slices.Clone(got)
			by.Desc = true
			want := slices.Clone(ids)
			s.Sort(want, by)
			if err := s.ReverseSortContext(context.Background(), got, by); err != nil || !slices.Equal(got, want) {
				t.Fatalf("%+v: %v", by, err)
			}
			if err := s.ReverseSortContext(context.Background(), got, by); err != nil || !slices.Equal(got, original) {
				t.Fatalf("round trip %+v: %v", by, err)
			}
		}
	}
}

func TestContainsFoldProgressiveMatchesFullFold(t *testing.T) {
	r := rand.New(rand.NewPCG(83, 91))
	alphabet := []rune("abcABC_-résumé日本ȺⱥİiKkΣςσ\x00")
	for range 10000 {
		var name, term strings.Builder
		for range r.IntN(50) {
			name.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		for range r.IntN(10) {
			term.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		lower := strings.ToLower(term.String())
		if got, want := containsFold(name.String(), lower), strings.Contains(strings.ToLower(name.String()), lower); got != want {
			t.Fatalf("%q contains %q: %v != %v", name.String(), lower, got, want)
		}
	}
}

func TestReverseSortParallelBoundaries(t *testing.T) {
	s := New(`C:\parallel`)
	ids := make([]ID, 196609)
	for i := range ids {
		ids[i] = s.Add(0, fmt.Sprintf("same-name-%02d", i%5), uint64(i%3), uint64(i%7), int64(i%11), 0)
	}
	for _, by := range []Sort{{Column: "Name"}, {Column: "Size"}, {Column: "Modified"}, {Column: "Folders"}} {
		s.Sort(ids, by)
		got, want := slices.Clone(ids), slices.Clone(ids)
		by.Desc = true
		s.Sort(want, by)
		if err := s.ReverseSortContext(context.Background(), got, by); err != nil || !slices.Equal(got, want) {
			t.Fatalf("parallel %+v: %v", by, err)
		}
	}
}
