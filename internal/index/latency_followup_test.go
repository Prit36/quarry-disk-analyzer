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

func TestSearchResultsMatchesIndependentTotals(t *testing.T) {
	s := New(`C:\root`)
	s.Engine = "NTFS MFT"
	dirs := []ID{0}
	r := rand.New(rand.NewPCG(58, 91))
	for i := range 70000 {
		flags := uint16(0)
		if i%8 == 0 {
			flags |= Directory
		}
		if i%137 == 0 {
			flags |= Unplaced
		}
		name := fmt.Sprintf("entry-%d.dll", i)
		if i%7 == 0 {
			name = "Microsoft-" + name
		}
		id := s.Add(dirs[r.IntN(len(dirs))], name, uint64(i%5000), uint64(i%5000+4096), int64(1700000000+i), flags)
		if flags&Directory != 0 {
			dirs = append(dirs, id)
		}
	}
	meta := s.Add(0, "$Extend", 0, 0, 0, Directory)
	s.Add(meta, "metadata.dll", 27, 4096, 0, 0)
	s.Finalize()
	s.Remove(dirs[10])
	previous := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previous)
	for _, workers := range []int{1, 2, 12} {
		runtime.GOMAXPROCS(workers)
		for _, root := range []ID{0, dirs[1], dirs[10], meta, ID(len(s.Nodes) - 1)} {
			for _, text := range []string{"", "type:file", "type:folder", "Microsoft", "*.dll", "path:root ext:dll", "min:1KB", "missing"} {
				filter, err := ParseFilter(text)
				if err != nil {
					t.Fatal(err)
				}
				ids, err := s.Search(context.Background(), root, &filter)
				if err != nil {
					t.Fatal(err)
				}
				selected := map[ID]bool{}
				metadata := false
				for _, id := range ids {
					if s.Nodes[id].IsDir() {
						selected[id] = true
					}
					metadata = metadata || s.IsMetadata(id)
				}
				for _, allocated := range []bool{false, true} {
					var total uint64
					for _, id := range ids {
						covered := false
						for p := s.Nodes[id].Parent; p != None; p = s.Nodes[p].Parent {
							covered = covered || selected[p]
						}
						if !covered {
							if allocated {
								total += s.Nodes[id].Allocated
							} else {
								total += s.Nodes[id].Size
							}
						}
					}
					got, err := s.SearchResults(context.Background(), root, &filter, allocated)
					if err != nil || !slices.Equal(got.IDs, ids) || got.Total != total || got.HasMetadata != metadata {
						t.Fatalf("workers %d root %d query %q allocated %v: total %d/%d metadata %v/%v err %v", workers, root, text, allocated, got.Total, total, got.HasMetadata, metadata, err)
					}
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SearchResults(ctx, 0, &Filter{}, false); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := s.SearchResults(context.Background(), None, &Filter{}, false); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestNamePrefixTiesKeepUnicodeAndLengthOrder(t *testing.T) {
	s := New(`C:\names`)
	var ids []ID
	names := []string{
		strings.Repeat("A", 16), strings.Repeat("a", 16) + "x",
		strings.Repeat("A", 16) + "İstanbul", strings.Repeat("a", 16) + "iSTANBUL",
		strings.Repeat("A", 16) + "Kelvin", strings.Repeat("a", 16) + "kelvin",
		strings.Repeat("A", 15) + "Kelvin", strings.Repeat("a", 15) + "kelvin",
		"short", "SHORT", "short\x00", strings.Repeat("a", 16) + "\x00",
	}
	for i := range 20000 {
		ids = append(ids, s.Add(0, names[i%len(names)], 0, 0, 0, 0))
	}
	slices.Reverse(ids)
	for _, desc := range []bool{false, true} {
		by := Sort{Column: "Name", Desc: desc}
		want, got := slices.Clone(ids), slices.Clone(ids)
		s.Sort(want, by)
		if err := s.SortContext(context.Background(), got, by); err != nil || !slices.Equal(got, want) {
			t.Fatalf("descending %v changed Unicode/length ordering: %v", desc, err)
		}
	}
}

func TestSearchResultsMetadataScopeAndRootFlags(t *testing.T) {
	for _, engine := range []string{"directory enumeration", "NTFS MFT"} {
		s := New(`C:\`)
		s.Engine = engine
		s.Nodes[0].Flags |= Unplaced // IsMetadata intentionally ignores the scan root.
		ordinary := s.Add(0, "ordinary", 0, 0, 0, Directory)
		s.Add(ordinary, "file", 1, 4096, 0, 0)
		meta := s.Add(0, "$Extend", 0, 0, 0, Directory)
		s.Add(meta, "data", 1, 4096, 0, 0)
		lost := s.Add(ordinary, "unresolved", 0, 0, 0, Directory|Unplaced)
		s.Add(lost, "file", 1, 4096, 0, 0)
		s.Finalize()
		for _, root := range []ID{0, ordinary, meta, lost} {
			got, err := s.SearchResults(context.Background(), root, &Filter{}, false)
			metadata := false
			for _, id := range got.IDs {
				metadata = metadata || s.IsMetadata(id)
			}
			if err != nil || metadata != got.HasMetadata {
				t.Fatalf("%s root %d: %v", engine, root, err)
			}
		}
	}
}

func TestPackedNumericSortBoundaries(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 32))
	for _, width := range []uint{0, 1, 31, 32, 40, 43, 44, 63, 64} {
		for _, highIDs := range []bool{false, true} {
			keys := map[ID]uint64{}
			var ids []ID
			mask := ^uint64(0)
			if width < 64 {
				mask = (uint64(1) << width) - 1
			}
			for i := range 5000 {
				id := ID(i)
				if highIDs {
					id |= 1 << 31
				}
				keys[id] = ^mask | r.Uint64()&mask
				if i%9 == 0 {
					keys[id] = ^mask
				}
				ids = append(ids, id)
			}
			ids = append(ids, ids[0], ids[32])
			r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
			for _, descending := range []bool{false, true} {
				key := func(id ID) uint64 {
					if descending {
						return ^keys[id]
					}
					return keys[id]
				}
				want, got := slices.Clone(ids), slices.Clone(ids)
				slices.SortFunc(want, func(a, b ID) int {
					if c := cmp.Compare(key(a), key(b)); c != 0 {
						return c
					}
					return cmp.Compare(a, b)
				})
				if err := sortNumeric(context.Background(), got, key); err != nil || !slices.Equal(got, want) {
					t.Fatalf("width %d high IDs %v descending %v: %v", width, highIDs, descending, err)
				}
			}
		}
	}
}
