package index

import (
	"cmp"
	"context"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
)

func TestParallelRadixFullKeysAndTies(t *testing.T) {
	previous := runtime.GOMAXPROCS(12)
	defer runtime.GOMAXPROCS(previous)
	r := rand.New(rand.NewPCG(31, 47))
	base := make([]ID, 500003)
	for i := range base {
		base[i] = ID(r.Uint32())
	}
	s := &Snapshot{}
	for _, tied := range []bool{false, true} {
		key := func(id ID) uint64 {
			if tied {
				return uint64(id) % 17
			}
			return uint64(id) * 0x9e3779b97f4a7c15
		}
		want, got := slices.Clone(base), slices.Clone(base)
		slices.SortFunc(want, func(a, b ID) int {
			if c := cmp.Compare(key(a), key(b)); c != 0 {
				return c
			}
			return cmp.Compare(a, b)
		})
		if err := sortNumeric(context.Background(), got, key); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("parallel numeric keys or ties differ (tied=%v)", tied)
		}
		lo := func(id ID) uint64 {
			if tied {
				return uint64(id) % 3
			}
			return ^(uint64(id) * 0xc2b2ae3d27d4eb4f)
		}
		want, got = slices.Clone(base), slices.Clone(base)
		slices.SortFunc(want, func(a, b ID) int {
			if c := cmp.Compare(key(a), key(b)); c != 0 {
				return c
			}
			if c := cmp.Compare(lo(a), lo(b)); c != 0 {
				return c
			}
			return cmp.Compare(a, b)
		})
		if err := s.SortByKeys(context.Background(), got, key, lo, nil); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("parallel two-word keys or ties differ (tied=%v)", tied)
		}
	}
}

func TestPackedNameWordsMatchIndividualKeys(t *testing.T) {
	s := New(`C:\names`)
	for _, name := range []string{"", "A", "abcdefghijklmnopq", "Ärger", "İSTANBUL", "KELVIN", "日本語", "aéÉıİz", "ABCDEFGHIJKLMNOP"} {
		id := s.Add(0, name, 0, 0, 0, 0)
		key := s.nameWords(id)
		if key.hi != s.NameKey(id, 0) || key.lo != s.NameKey(id, 8) {
			t.Fatalf("packed keys differ for %q", name)
		}
	}
}

func TestReaggregateFolderScratchMatchesOriginal(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		s := benchSnapshot(40000)
		if malformed {
			file := s.Add(0, "unusual", 7, 9, 0, 0)
			s.Add(file, "child", 11, 13, 0, 0)
		}
		// Relocate records so that parent IDs need not precede children.
		r := rand.New(rand.NewPCG(9, 17))
		mapping := r.Perm(len(s.Nodes) - 1)
		remap := func(id ID) ID {
			if id == 0 || id == None {
				return id
			}
			return ID(mapping[int(id)-1] + 1)
		}
		nodes := make([]Node, len(s.Nodes))
		for i, n := range s.Nodes {
			n.Parent, n.FirstChild, n.NextSibling = remap(n.Parent), remap(n.FirstChild), remap(n.NextSibling)
			nodes[remap(ID(i))] = n
		}
		s.Nodes = nodes
		want, got := s.Clone(), s.Clone()
		want.reaggregateAll()
		got.Reaggregate()
		if !slices.Equal(want.Nodes, got.Nodes) {
			t.Fatalf("folder-only aggregation differs (malformed=%v)", malformed)
		}
	}
}

func TestDeepReaggregateFallbackMatchesOriginal(t *testing.T) {
	s := New(`C:\deep`)
	parent := ID(0)
	for range 300 {
		s.Add(parent, "file", 7, 11, 0, 0)
		parent = s.Add(parent, "dir", 0, 0, 0, Directory)
	}
	s.Finalize()
	want, got := s.Clone(), s.Clone()
	want.reaggregateAll()
	got.Reaggregate()
	if !slices.Equal(want.Nodes, got.Nodes) {
		t.Fatal("deep-tree fallback changed totals")
	}
}

func BenchmarkReaggregate(b *testing.B) {
	s := benchSnapshot(500000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Reaggregate()
	}
}

func BenchmarkSummarize(b *testing.B) {
	s := benchSnapshot(500000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Summarize(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
