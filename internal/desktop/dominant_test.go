package desktop

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"quarry/internal/index"
	"quarry/internal/treemap"
)

// dominantSequential is the original single-threaded dominantExtensions.
func dominantSequential(ctx context.Context, s *index.Snapshot, branches []treemap.Branch, allocated bool) map[tileKey]string {
	out := map[tileKey]string{}
	tally := map[string]uint64{}
	add := func(root index.ID) {
		_ = s.Visit(ctx, root, func(id index.ID) error {
			n := s.Nodes[id]
			if n.IsDir() {
				return nil
			}
			v := n.Size
			if allocated {
				v = n.Allocated
			}
			tally[strings.ToLower(filepath.Ext(s.NameView(id)))] += v
			return nil
		})
	}
	best := func() string {
		ext, top := "", uint64(0)
		for e, v := range tally {
			if v > top || v == top && e < ext {
				ext, top = e, v
			}
		}
		clear(tally)
		if ext == "" {
			ext = "(no extension)"
		}
		return ext
	}
	var walk func(level []treemap.Branch)
	walk = func(level []treemap.Branch) {
		shown := map[index.ID]bool{}
		for _, b := range level {
			if !b.Item.Other {
				shown[b.Item.ID] = true
			}
		}
		for _, b := range level {
			switch {
			case b.Item.Other:
				for c := s.Nodes[b.Item.ID].FirstChild; c != index.None; c = s.Nodes[c].NextSibling {
					if !shown[c] {
						add(c)
					}
				}
				out[tileKey{b.Item.ID, true}] = best()
			case len(b.Children) > 0:
				walk(b.Children)
			case s.Nodes[b.Item.ID].IsDir():
				add(b.Item.ID)
				out[tileKey{b.Item.ID, false}] = best()
			}
		}
	}
	walk(branches)
	return out
}

func TestDominantExtensionsMatchSequential(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	exts := []string{".dll", ".DLL", ".txt", "", ".log", ".jpg", ".zip"}
	s := index.New(`C:\x`)
	dirs := []index.ID{0}
	for i := range 400_000 {
		// A few very wide folders, so blocks are split into many units.
		parent := dirs[r.IntN(len(dirs))]
		if i%3 == 0 {
			parent = dirs[r.IntN(min(len(dirs), 4))]
		}
		if r.IntN(12) == 0 {
			dirs = append(dirs, s.Add(parent, fmt.Sprint("d", i), 0, 0, 0, index.Directory))
			continue
		}
		size := r.Uint64N(1 << 24)
		s.Add(parent, fmt.Sprint("f", i, exts[r.IntN(len(exts))]), size, size+4096, 0, 0)
	}
	s.Finalize()
	for _, allocated := range []bool{false, true} {
		for _, budget := range []int{6000, 40} {
			branches := treemap.Hierarchy(t.Context(), s, 0, allocated, budget)
			got := dominantExtensions(t.Context(), s, branches, allocated)
			want := dominantSequential(t.Context(), s, branches, allocated)
			if len(want) == 0 || !maps.Equal(got, want) {
				t.Fatalf("allocated=%v budget=%d: %d blocks differ from the sequential result (%d)", allocated, budget, len(got), len(want))
			}
		}
	}
}
