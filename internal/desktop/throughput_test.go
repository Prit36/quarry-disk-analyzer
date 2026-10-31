package desktop

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestFileDirectionToggleKeepsRowsAndSelection(t *testing.T) {
	a := testApp(t)
	f := &a.files
	for _, metric := range []string{"Logical", "Allocated"} {
		a.prefs.Metric = metric
		for _, column := range []string{"Name", "Size", "Modified", "% of Scan"} {
			for _, desc := range []bool{false, true, false} {
				f.sort = ui.SortOrder{Column: column, Descending: desc}
				f.run()
				waitModel(t, a, func() bool { return !f.busy })
				want := slices.Clone(f.ids)
				if err := sortEntries(context.Background(), a.snap, want, f.sort, a.metricAllocated()); err != nil || !slices.Equal(f.ids, want) {
					t.Fatalf("%s %+v: %v", metric, f.sort, err)
				}
				f.selectOnly(f.ids[0])
			}
		}
	}
}

func TestBorrowedSelectionCheckpointsAndRowChanges(t *testing.T) {
	a := testApp(t)
	f := &a.files
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	f.selectAll()
	want := slices.Clone(f.ids)
	for range 3 {
		f.selCache.checkpoint()
		if got := f.selectedIDs(); !slices.Equal(got, want) || !f.selCache.borrowed {
			t.Fatal("checkpoint changed complete selection")
		}
	}
	f.sel.Remove(want[0])
	f.selCache.checkpoint()
	if got := f.selectedIDs(); len(got) != len(want)-1 || slices.Contains(got, want[0]) {
		t.Fatal("checkpoint lost removal")
	}
	f.selectAll()
	f.selectedIDs()
	a.search = "type:folder"
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	for _, id := range f.selectedIDs() {
		if !a.snap.Nodes[id].IsDir() {
			t.Fatal("borrowed stale file selection")
		}
	}
}

func TestCollapseAllDropsHiddenChildCache(t *testing.T) {
	a := testApp(t)
	a.tree.expandSubtree(0)
	if len(a.tree.kids) < 2 {
		t.Fatal("fixture did not expand child caches")
	}
	a.tree.collapseAll()
	if len(a.tree.kids) != 1 {
		t.Fatalf("retained %d child lists", len(a.tree.kids))
	}
	a.tree.expandSubtree(0)
	if !slices.ContainsFunc(a.tree.rows, func(r treeRow) bool { return r.id == index.ID(2) }) {
		t.Fatal("re-expansion lost rows")
	}
}

func TestSnapshotChangeReleasesHiddenCachesAndQueuedWork(t *testing.T) {
	a := testApp(t)
	old := a.snap
	a.files.run()
	waitModel(t, a, func() bool { return !a.files.busy })
	a.files.selectOnly(7)
	a.files.selectedIDs()
	a.ext.run()
	waitModel(t, a, func() bool { return !a.ext.busy })
	a.tree.cells.snap = old
	a.selFacts.snap, a.mutMemo.snap = old, old
	a.duplicates.forSnap, a.duplicates.minSize = old, "1MB"
	a.tm.forSnap, a.tm.rankFor, a.tm.tipSnap = old, old, old
	// A result posted before the snapshot change must also be rejected.
	a.files.run()
	waitQueued(t, a)
	next := old.Clone()
	next.Remove(2)
	a.base = next
	a.prefs.View = viewTree
	a.setView(next)
	a.drain()
	for label, cached := range map[string]*index.Snapshot{
		"files": a.files.forSnap, "file worker": a.files.runFor,
		"extensions": a.ext.forSnap, "extension worker": a.ext.runFor,
		"file selection": a.files.selCache.key.snap, "tree selection": a.tree.selCache.key.snap,
		"cells": a.tree.cells.snap, "facts": a.selFacts.snap, "operations": a.mutMemo.snap,
		"treemap": a.tm.forSnap, "ranks": a.tm.rankFor, "tooltip": a.tm.tipSnap, "duplicates": a.duplicates.forSnap,
	} {
		if cached == old {
			t.Fatalf("%s retained obsolete snapshot", label)
		}
	}
	if !a.files.sel.Has(7) || a.duplicates.minSize != "1MB" {
		t.Fatal("discarding cache lost view state")
	}
	a.files.run()
	waitModel(t, a, func() bool { return !a.files.busy })
	if a.files.forSnap != next || slices.Contains(a.files.ids, index.ID(2)) || !slices.Equal(a.files.selectedIDs(), []index.ID{7}) {
		t.Fatal("hidden file view failed to refresh with selection retained")
	}
}

func TestSnapshotChangeKeepsValidUnfilteredTreemap(t *testing.T) {
	a := testApp(t)
	a.filter.Include = "*.txt"
	a.prefs.FilterMap = false
	a.tm.forSnap, a.tm.rankFor, a.tm.tipSnap = a.base, a.base, a.base
	gen := a.tm.gen
	a.setView(a.base.Clone())
	if a.tm.forSnap != a.base || a.tm.rankFor != a.base || a.tm.gen != gen {
		t.Fatal("discarded the deliberately unfiltered treemap snapshot")
	}
}
