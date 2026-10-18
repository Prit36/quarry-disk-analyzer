package scan

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"quarry/internal/index"
)

func TestSortIdentitiesGroupsInEntryOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, n := range []int{0, 1, 2, 1000, 200_000} {
		ids := make([]identity, n)
		for i := range ids {
			// Few distinct files, so many share an ID; entries ascend.
			ids[i] = identity{file: r.Uint64N(uint64(n/3 + 1)), id: index.ID(i)}
		}
		want := slices.Clone(ids)
		slices.SortStableFunc(want, func(a, b identity) int { return cmp.Compare(a.file, b.file) })
		got := sortIdentities(slices.Clone(ids))
		// The order between files is free; within a file it is entry order.
		slices.SortStableFunc(got, func(a, b identity) int { return cmp.Compare(a.file, b.file) })
		if !slices.Equal(got, want) {
			t.Fatalf("n=%d: identities lost or reordered", n)
		}
	}
}

// TestWalkMatchesAcrossWorkers scans a tree of nested folders, deep enough
// for relative opens several levels down, with one and with many workers.
func TestWalkMatchesAcrossWorkers(t *testing.T) {
	root := t.TempDir()
	var make func(dir string, depth int)
	make = func(dir string, depth int) {
		for i := range 3 {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d é.txt", i)), []byte(fmt.Sprint(dir, i)), 0600)
		}
		if depth == 0 {
			return
		}
		for i := range 3 {
			sub := filepath.Join(dir, fmt.Sprintf("d%d 😀", i))
			os.Mkdir(sub, 0700)
			make(sub, depth-1)
		}
	}
	make(root, 4)
	var first index.Node
	for _, workers := range []int{1, 8} {
		s, err := Run(context.Background(), root, Options{ForceWalk: true, Workers: workers}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.Errors != 0 {
			t.Fatalf("errors: %+v", s.Issues)
		}
		n := s.Nodes[0]
		if n.Dirs != 120 || n.Files != 363 {
			t.Fatalf("workers %d: %d folders, %d files", workers, n.Dirs, n.Files)
		}
		if workers > 1 && (n.Size != first.Size || n.Files != first.Files) {
			t.Fatal("totals depend on workers")
		}
		first = n
		// Every path is rebuilt from the index exactly as on disk.
		for i := range s.Nodes {
			if _, err := os.Lstat(s.Path(index.ID(i))); err != nil {
				t.Fatal(err)
			}
		}
	}
}
