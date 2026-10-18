package index

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// randomTree adds n entries under random earlier folders, with some detached.
func randomTree(r *rand.Rand, n int) *Snapshot {
	s := New(`C:\t`)
	dirs := []ID{0}
	for i := range n {
		parent := dirs[r.IntN(len(dirs))]
		if r.IntN(5) == 0 {
			dirs = append(dirs, s.Add(parent, fmt.Sprint("d", i), 0, 0, 0, Directory))
			continue
		}
		size := r.Uint64N(1 << 40)
		s.Add(parent, fmt.Sprint("f", i), size, size+uint64(r.IntN(4096)), 0, 0)
	}
	return s
}

func TestFinalizeStrategiesAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 9))
	for _, n := range []int{0, 1, 2, 50, 5000} {
		s := randomTree(r, n)
		ordered := s.Clone()
		if !ordered.aggregateOrdered() {
			t.Fatal("a directory-scan tree must take the ordered path")
		}
		queued := s.Clone()
		queued.aggregateQueue()
		if !slices.Equal(ordered.Nodes, queued.Nodes) {
			t.Fatalf("n=%d: backward pass differs from the leaf queue", n)
		}
		// Any top-down order gives the same totals: breadth first here.
		inOrder := s.Clone()
		order := []ID{0}
		for k := 0; k < len(order); k++ {
			for c := inOrder.Nodes[order[k]].FirstChild; c != None; c = inOrder.Nodes[c].NextSibling {
				order = append(order, c)
			}
		}
		inOrder.FinalizeInOrder(order)
		if !slices.Equal(inOrder.Nodes, queued.Nodes) {
			t.Fatalf("n=%d: FinalizeInOrder differs from the leaf queue", n)
		}
	}
}

func TestFinalizeChildBeforeParent(t *testing.T) {
	// As MFT records may: entry 1 is a file whose folder is entry 2.
	s := &Snapshot{Root: `C:\`}
	s.Add(None, "C:", 0, 0, 0, Directory)
	s.Add(None, "a.txt", 10, 16, 0, 0)
	s.Add(None, "folder", 0, 0, 0, Directory)
	s.Nodes[1].Parent, s.Nodes[2].Parent = 2, 0
	s.Nodes[2].FirstChild, s.Nodes[0].FirstChild = 1, 2
	s.Finalize()
	if root := s.Nodes[0]; root.Size != 10 || root.Allocated != 16 || root.Files != 1 || root.Dirs != 1 {
		t.Fatalf("root totals %+v", root)
	}
	if f := s.Nodes[2]; f.Size != 10 || f.Files != 1 || f.Dirs != 0 {
		t.Fatalf("folder totals %+v", f)
	}
}

func TestBuilderMatchesSnapshot(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	want := New(`C:\b`)
	b := NewBuilder(`C:\b`)
	dirs := []ID{0}
	// Enough entries and long names to span several node and name chunks.
	for i := range 3*nodeChunk + 17 {
		parent := dirs[r.IntN(len(dirs))]
		name := fmt.Sprint(i, "-", string(make([]byte, r.IntN(60))))
		flags := uint16(0)
		if r.IntN(6) == 0 {
			flags = Directory
		}
		id := want.Add(parent, name, uint64(i), uint64(i)*2, int64(i), flags)
		if got := b.Add(parent, name, uint64(i), uint64(i)*2, int64(i), flags); got != id {
			t.Fatal("IDs differ", got, id)
		}
		if b.Name(id) != name {
			t.Fatal("builder name lost", id)
		}
		if flags != 0 {
			dirs = append(dirs, id)
		}
	}
	got := b.Build()
	if !slices.Equal(got.Nodes, want.Nodes) || string(got.Names) != string(want.Names) {
		t.Fatal("built index differs")
	}
}

func TestSummarizeParallelMatchesSequential(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	exts := []string{".dll", ".DLL", ".txt", "", ".Log", ".log", ".tar.gz"}
	s := New(`C:\s`)
	dirs := []ID{0}
	for i := range 300_000 {
		parent := dirs[r.IntN(len(dirs))]
		if r.IntN(8) == 0 {
			dirs = append(dirs, s.Add(parent, fmt.Sprint("d", i), 0, 0, 0, Directory))
			continue
		}
		size := r.Uint64N(1 << 30)
		s.Add(parent, fmt.Sprint("f", i, exts[r.IntN(len(exts))]), size, size+512, 0, 0)
	}
	s.Finalize()
	got, err := s.Summarize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	types, _ := s.summarizeRange(t.Context(), 0, len(s.Nodes))
	var want []FileType
	for _, v := range types {
		want = append(want, *v)
	}
	if len(got.Types) != len(want) {
		t.Fatal("types differ", got.Types, want)
	}
	for _, w := range want {
		if !slices.Contains(got.Types, w) {
			t.Fatal("missing or different", w)
		}
	}
	if !slices.IsSortedFunc(got.Types, func(a, b FileType) int { return -cmp.Compare(a.Allocated, b.Allocated) }) {
		t.Fatal("not ordered by allocated size")
	}
}
