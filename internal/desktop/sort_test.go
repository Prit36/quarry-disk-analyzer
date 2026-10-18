package desktop

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// referenceSort is the comparison sort the ranked radix sort replaced.
func referenceSort(s *index.Snapshot, ids []index.ID, order ui.SortOrder, allocated bool) {
	flip := func(v int) int {
		if order.Descending {
			return -v
		}
		return v
	}
	switch order.Column {
	case "Folder":
		parents := map[index.ID]string{}
		for _, id := range ids {
			if p := s.Nodes[id].Parent; p != index.None {
				parents[p] = strings.ToLower(s.Path(p))
			}
		}
		slices.SortStableFunc(ids, func(x, y index.ID) int {
			if v := strings.Compare(parents[s.Nodes[x].Parent], parents[s.Nodes[y].Parent]); v != 0 {
				return flip(v)
			}
			return s.Compare(x, y, index.Sort{Column: "Name"})
		})
	case "Extension":
		slices.SortStableFunc(ids, func(x, y index.ID) int {
			if v := cmp.Compare(extOf(s, x), extOf(s, y)); v != 0 {
				return flip(v)
			}
			return s.Compare(x, y, index.Sort{Desc: true, Allocated: allocated})
		})
	}
}

func TestRankedSortsMatchComparison(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	s := index.New(`C:\x`)
	dirs := []index.ID{0}
	exts := []string{".dll", ".DLL", ".txt", "", ".tar.gz", ".Txt", ".é", ".zip"}
	for range 30000 {
		parent := dirs[r.IntN(len(dirs))]
		if r.IntN(8) == 0 {
			dirs = append(dirs, s.Add(parent, fmt.Sprintf("Dir%d", r.IntN(300)), 0, 0, 0, index.Directory))
			continue
		}
		size := uint64(r.IntN(100))
		s.Add(parent, fmt.Sprintf("f%d%s", r.IntN(40), exts[r.IntN(len(exts))]), size, size, 0, 0)
	}
	s.Finalize()
	var ids []index.ID
	for i := 1; i < len(s.Nodes); i++ {
		ids = append(ids, index.ID(i))
	}
	for _, column := range []string{"Folder", "Extension"} {
		for _, desc := range []bool{false, true} {
			order := ui.SortOrder{Column: column, Descending: desc}
			want := slices.Clone(ids)
			r.Shuffle(len(want), func(i, j int) { want[i], want[j] = want[j], want[i] })
			got := slices.Clone(want)
			referenceSort(s, want, order, false)
			if err := sortEntries(context.Background(), s, got, order, false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%+v differs from the comparison sort", order)
			}
		}
	}
}
