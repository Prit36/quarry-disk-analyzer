package desktop

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestDerivedDirectionGroupsPreserveExactOrder(t *testing.T) {
	s := index.New(`C:\data`)
	var ids []index.ID
	for _, name := range []string{"folder", "FOLDER", "Kelvin", "kelvin", "..", `folder\..\folder`, "日本", ""} {
		p := s.Add(0, name, 0, 0, 0, index.Directory)
		ids = append(ids, p)
		for i := range 1000 {
			ext := []string{".DLL", ".dll", ".Σ", ".ς", ".K", ".k", ""}[i%7]
			ids = append(ids, s.Add(p, fmt.Sprintf("file-%d%s", i%13, ext), uint64(i%17), uint64(i%19), 0, 0))
		}
	}
	ids = append(ids, 0, ids[10], ids[28])
	for _, column := range []string{"Folder", "Extension"} {
		for _, allocated := range []bool{false, true} {
			got := slices.Clone(ids)
			var groups []uint32
			order := ui.SortOrder{Column: column}
			if err := sortEntriesGrouped(context.Background(), s, got, order, allocated, &groups); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				oldGroups := slices.Clone(groups)
				previous := groups
				order.Descending = !order.Descending
				want := slices.Clone(ids)
				if err := sortEntries(context.Background(), s, want, order, allocated); err != nil {
					t.Fatal(err)
				}
				var err error
				groups, err = reverseEntryGroups(context.Background(), got, groups)
				if err != nil || !slices.Equal(got, want) || !slices.Equal(previous, oldGroups) {
					t.Fatalf("%s allocated=%v desc=%v: incorrect groups or mutated immutable source: %v", column, allocated, order.Descending, err)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reverseEntryGroups(ctx, ids, []uint32{uint32(len(ids))}); err != context.Canceled {
		t.Fatal("ignored canceled direction change")
	}
}

func TestDerivedModelGroupsFollowMetricAndSnapshot(t *testing.T) {
	a := testApp(t)
	f := &a.files
	for _, column := range []string{"Folder", "Extension"} {
		f.sort = ui.SortOrder{Column: column}
		for _, metric := range []string{"Logical", "Allocated", "Logical"} {
			a.prefs.Metric = metric
			for range 3 {
				f.sort.Descending = !f.sort.Descending
				f.run()
				waitModel(t, a, func() bool { return !f.busy })
				want := slices.Clone(f.ids)
				if err := sortEntries(context.Background(), a.snap, want, f.sort, a.metricAllocated()); err != nil || !slices.Equal(f.ids, want) || len(f.shownGroups) == 0 {
					t.Fatalf("%s metric=%s: %v", column, metric, err)
				}
			}
		}
	}
	a.prefs.View = viewTree
	a.setView(a.snap.Clone())
	if f.shownGroups != nil {
		t.Fatal("hidden view retained obsolete group cache")
	}
}
