package desktop

import (
	"cmp"
	"context"
	"math/rand/v2"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestParallelSortMatchesLibrary(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 45))
	for _, shape := range []string{"random", "sorted", "reversed", "equal", "duplicates"} {
		values := make([]int, 100000)
		for i := range values {
			switch shape {
			case "random":
				values[i] = r.Int()
			case "sorted":
				values[i] = i
			case "reversed":
				values[i] = -i
			case "equal":
				values[i] = 1
			case "duplicates":
				values[i] = r.IntN(17)
			}
		}
		want := slices.Clone(values)
		slices.Sort(want)
		if err := parallelSort(context.Background(), values, cmp.Compare[int]); err != nil || !slices.Equal(values, want) {
			t.Fatalf("%s: %v", shape, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	values := make([]int, 100000)
	for i := range values {
		values[i] = r.Int()
	}
	err := parallelSort(ctx, values, func(a, b int) int {
		if calls.Add(1) == 10000 {
			cancel()
		}
		return cmp.Compare(a, b)
	})
	if err != context.Canceled {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestSelectionCompactionPreservesCurrentKeys(t *testing.T) {
	var selected ui.Selection[index.ID]
	var cache selectionCache
	for i := range 20000 {
		selected.Add(index.ID(i))
	}
	build := func(out []index.ID) []index.ID {
		for id := range selected.All() {
			out = append(out, id)
		}
		slices.Sort(out)
		return out
	}
	cache.get(&selected, 1, nil, build)
	selected.Clear()
	selected.Add(17)
	selected.Add(9000)
	cache.invalidate()
	got := cache.get(&selected, 1, nil, build)
	if !slices.Equal(got, []index.ID{17, 9000}) || !selected.Has(17) || !selected.Has(9000) || selected.Len() != 2 || cache.peak != 2 {
		t.Fatalf("compaction changed selection: %v", got)
	}
	version := cache.version
	cache.get(&selected, 1, nil, build)
	if cache.version != version {
		t.Fatal("unchanged selection was rebuilt")
	}
}

func TestBorrowedFullSelectionCannotOverwriteResults(t *testing.T) {
	a := testApp(t)
	a.files.run()
	waitModel(t, a, func() bool { return !a.files.busy })
	want := slices.Clone(a.files.ids)
	a.files.selectAll()
	if got := a.files.selectedIDs(); !slices.Equal(got, want) || !a.files.selCache.borrowed {
		t.Fatal("full selection did not borrow immutable results")
	}
	a.files.sel.Remove(want[0])
	a.files.selCache.invalidate()
	a.files.selectedIDs()
	if !slices.Equal(a.files.ids, want) {
		t.Fatal("selection rebuild overwrote file rows")
	}
	a.files.selectOnly(want[len(want)-1])
	a.files.selectedIDs()
	if !slices.Equal(a.files.ids, want) {
		t.Fatal("single selection overwrote file rows")
	}
}

func TestTopLevelEntriesMatchesAncestorReference(t *testing.T) {
	s := syntheticUISnapshot()
	a := &App{snap: s}
	r := rand.New(rand.NewPCG(14, 62))
	for _, n := range []int{0, 1, 16, 10000, 100000} {
		ids := make([]index.ID, n)
		set := map[index.ID]bool{}
		for i := range ids {
			ids[i] = index.ID(r.IntN(len(s.Nodes)))
			if s.Nodes[ids[i]].IsDir() {
				set[ids[i]] = true
			}
		}
		var want []index.ID
		for _, id := range ids {
			covered := false
			for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
				covered = covered || set[p]
			}
			if !covered {
				want = append(want, id)
			}
		}
		if got := a.topLevel(ids); !slices.Equal(got, want) {
			t.Fatalf("%d selected: changed top-level entries", n)
		}
	}
}
