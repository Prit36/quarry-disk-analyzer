package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestWideSearchMatchesSequential(t *testing.T) {
	s := wideSnapshot(100000)
	dir := s.Add(0, "nested", 0, 0, 0, Directory)
	s.Add(dir, "needle.bin", 7, 8, 0, 0)
	s.Reaggregate()
	for _, query := range []string{"type:file", "absent", "path:wide ext:bin", "nested", "path:nested"} {
		f, err := ParseFilter(query)
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []ID{0, dir} {
			var ancestors []bool
			if f.AncestorTerms() {
				ancestors = make([]bool, len(s.Nodes))
				ancestors[scope] = f.MatchPathComponent(s.Root) || f.MatchPathComponent(s.NameView(scope))
			}
			want, err := s.searchSubtree(context.Background(), scope, scope, &f, ancestors, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Search(context.Background(), scope, &f)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("query %q scope %d: %d matches, want %d; %v", query, scope, len(got), len(want), err)
			}
		}
	}
}

func TestNumericSortFullWidthKeys(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	s := wideSnapshot(10000)
	ids := make([]ID, len(s.Nodes))
	for i := range ids {
		ids[i] = ID(i)
		s.Nodes[i].Size, s.Nodes[i].Allocated = r.Uint64(), r.Uint64()
		s.Nodes[i].Modified = r.Uint32()
	}
	for _, column := range []string{"Size", "Allocated", "Modified"} {
		for _, desc := range []bool{false, true} {
			by := Sort{Column: column, Desc: desc}
			want, got := slices.Clone(ids), slices.Clone(ids)
			s.Sort(want, by)
			if err := s.SortContext(context.Background(), got, by); err != nil || !slices.Equal(got, want) {
				t.Fatalf("order %+v: result differs; %v", by, err)
			}
		}
	}
}

func TestCanceledWorkStops(t *testing.T) {
	s := wideSnapshot(10000)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ids := s.Children(0)
	err := s.SortByKeys(ctx, ids, func(id ID) uint64 {
		calls++
		if calls == 100 {
			cancel()
		}
		return s.Nodes[id].Size
	}, nil, nil)
	if err != context.Canceled || calls >= len(ids) {
		t.Fatalf("sort continued after cancellation: %d calls, %v", calls, err)
	}
	f := Filter{}
	if _, err := s.Search(ctx, 0, &f); err != context.Canceled {
		t.Fatalf("canceled search: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	calls = 0
	view, _, err := s.Filtered(ctx, func(ID) bool {
		calls++
		if calls == 100 {
			cancel()
		}
		return false
	}, nil, true)
	if err != context.Canceled || view != nil || calls >= len(ids) {
		t.Fatalf("filter continued after cancellation: %d calls, %v", calls, err)
	}
	if s.Nodes[0].Files != 10000 {
		t.Fatal("canceled filtering mutated the original snapshot")
	}
}

func TestReaggregateMatchesPostorderReference(t *testing.T) {
	s := benchSnapshot(15000)
	// Reorder IDs so parents can follow children, as in MFT scans.
	r := rand.New(rand.NewPCG(17, 18))
	perm := make([]ID, len(s.Nodes))
	for i := range perm {
		perm[i] = ID(i)
	}
	r.Shuffle(len(perm)-1, func(i, j int) { perm[i+1], perm[j+1] = perm[j+1], perm[i+1] })
	remap := func(id ID) ID {
		if id == None {
			return None
		}
		return perm[id]
	}
	nodes := slices.Clone(s.Nodes)
	for i, n := range nodes {
		n.Parent, n.FirstChild, n.NextSibling = remap(n.Parent), remap(n.FirstChild), remap(n.NextSibling)
		s.Nodes[perm[i]] = n
	}
	s.Remove(perm[5], perm[13], perm[21])
	want := s.Clone()
	var order []ID
	want.Visit(context.Background(), 0, func(id ID) error {
		order = append(order, id)
		if want.Nodes[id].IsDir() {
			n := &want.Nodes[id]
			n.Size, n.Allocated, n.Files, n.Dirs = 0, 0, 0, 0
		}
		return nil
	})
	for i := len(order) - 1; i > 0; i-- {
		n := want.Nodes[order[i]]
		p := &want.Nodes[n.Parent]
		p.Size += n.Size
		p.Allocated += n.Allocated
		if n.IsDir() {
			p.Files += n.Files
			p.Dirs += n.Dirs + 1
		} else {
			p.Files++
		}
	}
	s.Reaggregate()
	if !slices.Equal(s.Nodes, want.Nodes) {
		t.Fatal("folder totals differ from the postorder reference")
	}
}

func wideSnapshot(n int) *Snapshot {
	s := New(`C:\wide`)
	for i := range n {
		s.Add(0, fmt.Sprintf("file-%08d.bin", i), uint64(i), uint64(i), 0, 0)
	}
	s.Finalize()
	return s
}

func BenchmarkSearchWide(b *testing.B) {
	s := wideSnapshot(100000)
	for _, query := range []string{"type:file", "absent", "path:wide ext:bin"} {
		b.Run(query, func(b *testing.B) {
			f, err := ParseFilter(query)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := s.Search(context.Background(), 0, &f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReaggregateWide(b *testing.B) {
	s := wideSnapshot(100000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Reaggregate()
	}
}
