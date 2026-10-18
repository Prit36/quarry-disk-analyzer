package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func flatSearchSnapshot(n int) *Snapshot {
	s := New(`C:\flat`)
	for i := range n {
		s.Add(0, fmt.Sprintf("file-%08d.LOG", i), uint64(i), uint64(i), 0, 0)
	}
	s.Finalize()
	return s
}

func TestNumericSortFullWidthAndCancellation(t *testing.T) {
	s := New(`C:\sort`)
	r := rand.New(rand.NewPCG(21, 42))
	var ids []ID
	for i := range 20000 {
		v := r.Uint64()
		if i%4 == 0 {
			v = ^uint64(0)
		} // ties and the largest possible key
		ids = append(ids, s.Add(0, "file", v, ^v, 0, 0))
	}
	for _, by := range []Sort{{Column: "Size"}, {Column: "Size", Desc: true}, {Column: "Allocated"}, {Column: "Allocated", Desc: true}} {
		want, got := slices.Clone(ids), slices.Clone(ids)
		s.Sort(want, by)
		if err := s.SortContext(context.Background(), got, by); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("numeric sort changed full-width order: %+v", by)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := s.SortByKeys(ctx, ids, func(id ID) uint64 {
		calls++
		if calls == 5000 {
			cancel()
		}
		return uint64(id)
	}, nil, nil)
	if err != context.Canceled || calls >= len(ids) {
		t.Fatalf("key generation ignored cancellation: %v, %d keys", err, calls)
	}
}

func TestBatchedSearchMatchesSequential(t *testing.T) {
	s := flatSearchSnapshot(40000)
	parent := s.Add(0, "matching", 0, 0, 0, Directory)
	for i := range 60 {
		dir := s.Add(parent, fmt.Sprintf("dir-%d", i), 0, 0, 0, Directory)
		for j := range 40 {
			s.Add(dir, fmt.Sprintf("file-%d.log", j), 1, 1, 0, 0)
		}
	}
	s.Finalize()
	s.Remove(200)
	for _, root := range []ID{0, parent} {
		for _, query := range []string{"", "ext:log", "missing", "type:folder", "path:matching", "path:flat ext:log"} {
			f, err := ParseFilter(query)
			if err != nil {
				t.Fatal(err)
			}
			// Mixed directory and leaf batches must retain sequential order,
			// including inherited path matches and detached entries.
			pieces, err := s.searchPieces(context.Background(), root, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if len(pieces) > 100 {
				t.Fatalf("unbounded task count: %d", len(pieces))
			}
			var ancestors []bool
			if f.AncestorTerms() {
				ancestors = make([]bool, len(s.Nodes))
				ancestors[root] = f.MatchPathComponent(s.displayName(root))
				for p := s.Nodes[root].Parent; p != None; p = s.Nodes[p].Parent {
					ancestors[root] = ancestors[root] || f.MatchPathComponent(s.displayName(p))
				}
			}
			want, err := s.searchSubtree(context.Background(), root, root, &f, ancestors, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ancestors != nil {
				clear(ancestors)
				ancestors[root] = f.MatchPathComponent(s.displayName(root))
				for p := s.Nodes[root].Parent; p != None; p = s.Nodes[p].Parent {
					ancestors[root] = ancestors[root] || f.MatchPathComponent(s.displayName(p))
				}
			}
			got, err := s.searchParallel(context.Background(), root, &f, ancestors, 8)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("scope %d query %q changed ordered matches", root, query)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, 0, &Filter{}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func BenchmarkSearchFlat(b *testing.B) {
	s := flatSearchSnapshot(500000)
	for _, query := range []string{"ext:log", "missing", "path:flat ext:log"} {
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
