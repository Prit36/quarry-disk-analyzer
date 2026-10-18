package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
)

func TestFusedFilterMatchesGeneralNodesAndCallbacks(t *testing.T) {
	r := rand.New(rand.NewPCG(29, 73))
	for _, deep := range []bool{false, true} {
		s := New(`C:\filters`)
		dirs := []ID{0}
		for i := range 5000 {
			parent := dirs[r.IntN(len(dirs))]
			if deep {
				parent = dirs[len(dirs)-1]
			}
			flags := uint16(0)
			if i%4 == 0 {
				flags = Directory
			}
			id := s.Add(parent, fmt.Sprint(i), uint64(i%19), uint64(i%23), 0, flags)
			if flags == Directory {
				dirs = append(dirs, id)
			}
		}
		s.Finalize()
		if deep {
			s.Remove(dirs[len(dirs)-1])
		} else {
			s.Remove(dirs[4])
		}
		original := slices.Clone(s.Nodes)
		for _, isolate := range []bool{false, true} {
			for _, mode := range []int{0, 1, 2, 3} {
				var calls []ID
				keep := func(id ID) bool {
					calls = append(calls, id)
					return mode == 0 || mode == 1 && id%7 == 0
				}
				drop := func(id ID) bool {
					calls = append(calls, id)
					return mode == 3 && id%13 == 0
				}
				want, stats, err := s.filteredGeneral(context.Background(), keep, drop, isolate)
				if err != nil {
					t.Fatal(err)
				}
				wantCalls := slices.Clone(calls)
				calls = nil
				got, gotStats, err := s.Filtered(context.Background(), keep, drop, isolate)
				if err != nil || gotStats != stats || !slices.Equal(got.Nodes, want.Nodes) || !slices.Equal(calls, wantCalls) || !slices.Equal(s.Nodes, original) {
					t.Fatalf("deep=%v isolate=%v mode=%d: node/callback/stats/source mismatch: %v", deep, isolate, mode, err)
				}
			}
		}
	}
	s := flatSearchSnapshot(10000)
	ctx, cancel := context.WithCancel(context.Background())
	visits := 0
	got, _, err := s.Filtered(ctx, func(ID) bool {
		visits++
		if visits == 10 {
			cancel()
		}
		return true
	}, nil, false)
	if got != nil || err != context.Canceled || visits >= len(s.Nodes)-1 {
		t.Fatalf("filter ignored cancellation: %v, visits=%d", err, visits)
	}
}

func TestReservedSearchHandlesIncorrectCounts(t *testing.T) {
	s := New(`C:\counts`)
	for d := range 80 {
		p := s.Add(0, fmt.Sprint(d), 0, 0, 0, Directory)
		for i := range 600 {
			s.Add(p, fmt.Sprintf("%d.log", i), uint64(i), uint64(i+4096), 0, 0)
		}
	}
	s.Finalize()
	for _, kind := range []Kind{KindAny, KindFiles, KindFolders} {
		f := Filter{Kind: kind}
		want, err := s.searchSubtree(context.Background(), 0, 0, &f, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, counts := range []uint32{0, 1, ^uint32(0)} {
			for i := 1; i < len(s.Nodes); i++ {
				if s.Nodes[i].IsDir() {
					s.Nodes[i].Files, s.Nodes[i].Dirs = counts, counts
				}
			}
			got, err := s.searchParallel(context.Background(), 0, &f, nil, 8)
			if err != nil || !slices.Equal(got, want) || cap(got) > max(4096, 2*len(want)) {
				t.Fatalf("kind=%d counts=%d: incorrect reserved search: %v", kind, counts, err)
			}
		}
	}
}

func TestRankGroupLengthsFollowSortedRanks(t *testing.T) {
	s := New(`C:\ranks`)
	var ids []ID
	for i := range 5000 {
		ids = append(ids, s.Add(0, fmt.Sprint(i%11), uint64(i%17), uint64(i%29), 0, 0))
	}
	ids = append(ids, ids[10], ids[20])
	for _, sparse := range []bool{false, true} {
		rank := func(id ID) uint32 { return uint32(id%29) * 7 }
		maxRank := uint32(196)
		if sparse {
			maxRank = ^uint32(0)
		}
		for _, desc := range []bool{false, true} {
			got := slices.Clone(ids)
			var groups []uint32
			if err := s.SortByRankGroups(context.Background(), got, rank, maxRank, desc, Sort{Column: "Name"}, &groups); err != nil {
				t.Fatal(err)
			}
			start := 0
			for _, length := range groups {
				if length == 0 {
					t.Fatal("empty rank group")
				}
				end := start + int(length)
				value := rank(got[start])
				for _, id := range got[start:end] {
					if rank(id) != value {
						t.Fatal("group spans different ranks")
					}
				}
				if end < len(got) && rank(got[end]) == value {
					t.Fatal("equal rank split across groups")
				}
				start = end
			}
			if start != len(ids) || len(groups) != 29 {
				t.Fatalf("incomplete groups: %d entries, %d groups", start, len(groups))
			}
		}
	}
}

func TestPathSearchMatchesPerEntryPaths(t *testing.T) {
	previous := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previous)
	for _, root := range []string{`C:\root`, `C:\root\..\root`, `\\server\share`, "relative"} {
		s := New(root)
		dirs := []ID{0}
		for _, name := range []string{"Windows", "wINDOWS", "日本", "Kelvin", "", "..", `nested\path`} {
			p := s.Add(0, name, 0, 0, 0, Directory)
			dirs = append(dirs, p)
			for i := range 5000 {
				flags := uint16(0)
				if i%97 == 0 {
					flags = Unplaced
				}
				s.Add(p, fmt.Sprintf("file-%d.DLL", i), uint64(i), uint64(i), 0, flags)
			}
		}
		s.Finalize()
		for _, workers := range []int{1, 8} {
			runtime.GOMAXPROCS(workers)
			for _, scope := range []ID{0, dirs[1], dirs[4]} {
				for _, text := range []string{`path:C:\root\Windows`, `path:\Windows\ ext:dll`, `path:\日本\ type:file`, `path:\root\ path:\Windows\`, `path:*\*.DLL`, `-nope path:\nested\`, `type:folder path:C:\root`} {
					f, err := ParseFilter(text)
					if err != nil {
						t.Fatal(err)
					}
					var want []ID
					err = s.Visit(context.Background(), scope, func(id ID) error {
						if id != scope && f.MatchNode(s.NameView(id), s.Nodes[id], func() string { return s.Path(id) }, false) {
							want = append(want, id)
						}
						return nil
					})
					got, searchErr := s.Search(context.Background(), scope, &f)
					if err != nil || searchErr != nil || !slices.Equal(got, want) {
						t.Fatalf("root=%q scope=%d workers=%d query=%q: %v / %v", root, scope, workers, text, err, searchErr)
					}
				}
			}
		}
	}
}
