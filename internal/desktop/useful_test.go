package desktop

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestFolderPathArenasMatchReferenceAcrossChunks(t *testing.T) {
	for _, root := range []string{`C:\Data`, `\\server\share`} {
		s := index.New(root)
		parent := index.ID(0)
		for range 20 {
			parent = s.Add(parent, strings.Repeat("Deep-Folder-", 9), 0, 0, 0, index.Directory)
		}
		for i := range 2000 {
			name := fmt.Sprintf("Folder-%d", i)
			if i%7 == 0 {
				name = "日本-İ-" + name
			}
			if i%127 == 0 {
				name = "." // normalized paths can produce equal derived keys
			}
			dir := s.Add(parent, name, 0, 0, 0, index.Directory)
			s.Add(dir, "file.bin", uint64(i), uint64(i)*2, 0, 0)
		}
		s.Finalize()
		f := index.Filter{Kind: index.KindFiles}
		base, err := s.Search(context.Background(), 0, &f)
		if err != nil {
			t.Fatal(err)
		}
		for _, desc := range []bool{false, true} {
			order := ui.SortOrder{Column: "Folder", Descending: desc}
			want, got := slices.Clone(base), slices.Clone(base)
			referenceSort(s, want, order, false)
			if err := sortEntries(context.Background(), s, got, order, false); err != nil || !slices.Equal(got, want) {
				t.Fatalf("folder arena order differs for root %q descending %v: %v", root, desc, err)
			}
		}
	}
}

func TestSelectionOnDiskMatchesMetadataChecks(t *testing.T) {
	for _, engine := range []string{"Directory scan", "NTFS MFT"} {
		s := index.New(`C:\`)
		s.Engine = engine
		ordinary := s.Add(0, "Users", 0, 0, 0, index.Directory)
		unplaced := s.Add(ordinary, "lost", 0, 0, 0, index.Directory|index.Unplaced)
		meta := s.Add(0, "$Extend", 0, 0, 0, index.Directory)
		detached := s.Add(index.None, "detached", 0, 0, 0, index.Directory)
		parents := []index.ID{ordinary, unplaced, meta, detached}
		for i := range 4000 {
			flags := uint16(0)
			if i%127 == 0 {
				flags = index.Unplaced
			}
			s.Add(parents[i%len(parents)], "file.bin", 1, 1, 0, flags)
		}
		a := &App{snap: s}
		for _, rootUnplaced := range []bool{false, true} {
			if rootUnplaced {
				s.Nodes[0].Flags |= index.Unplaced
			}
			for i := range s.Nodes {
				ids := make([]index.ID, 64)
				for j := range ids {
					ids[j] = index.ID(i)
				}
				if got, want := a.selectionOnDisk(ids), !s.IsMetadata(index.ID(i)); got != want {
					t.Fatalf("engine %q root unplaced %v id %d: on-disk %v, want %v", engine, rootUnplaced, i, got, want)
				}
			}
		}
		r := rand.New(rand.NewPCG(23, 25))
		for range 100 {
			ids := make([]index.ID, 1000)
			want := true
			for i := range ids {
				ids[i] = index.ID(r.IntN(len(s.Nodes)))
				want = want && !s.IsMetadata(ids[i])
			}
			if a.selectionOnDisk(ids) != want {
				t.Fatal("mixed selection changed metadata availability")
			}
		}
	}
}

func TestAllResultsFactsReuseWorkerTotal(t *testing.T) {
	a := testApp(t)
	a.prefs.View = viewFiles
	for _, allocated := range []bool{false, true} {
		if allocated {
			a.prefs.Metric = "Allocated"
		}
		for _, query := range []string{"", "media", "type:folder", "ext:log", "absent"} {
			for _, scope := range []index.ID{0, 1, 6} {
				a.search, a.files.scope = query, scope
				a.files.run()
				waitModel(t, a, func() bool { return !a.files.busy })
				a.files.selectAll()
				ids := a.selected()
				want, onDisk := uint64(0), true
				for _, id := range a.topLevel(ids) {
					want += a.metric(a.snap.Nodes[id])
				}
				for _, id := range ids {
					onDisk = onDisk && !a.snap.IsMetadata(id)
				}
				if facts := a.facts(); facts.total != want || facts.onDisk != onDisk {
					t.Fatalf("query %q scope %d metric %v: selection facts changed", query, scope, allocated)
				}
				a.files.sel.Clear()
				a.files.selCache.invalidate()
			}
		}
	}
}
