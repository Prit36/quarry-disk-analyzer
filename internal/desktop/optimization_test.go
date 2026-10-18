package desktop

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/scan"
)

func waitModel(t *testing.T, a *App, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("background model timed out")
		}
		a.drain()
		time.Sleep(time.Millisecond)
	}
}

func TestFileResortReuseAndInvalidation(t *testing.T) {
	a := testApp(t)
	f := &a.files
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	f.selectAll()
	f.selectOnly(2)
	f.sel.Add(7)
	old := slices.Clone(f.ids)
	f.sort = ui.SortOrder{Column: "Name"}
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if !f.sel.Has(2) || !f.sel.Has(7) || f.cursorID() != 2 || f.total != 1360 {
		t.Fatalf("resort lost selection/cursor/totals: %v %d %d", f.selectedIDs(), f.cursorID(), f.total)
	}
	if slices.Equal(f.ids, old) {
		t.Fatal("sort did not change order")
	}
	f.scope = 1
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if len(f.ids) != 3 || f.total != 1050 || f.sel.Has(7) {
		t.Fatalf("stale scope results: %v", f.ids)
	}
	a.search = "type:folder"
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if !slices.Equal(f.ids, []index.ID{4}) || f.total != 50 {
		t.Fatalf("stale query: %v", f.ids)
	}
	a.search = "size:invalid"
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if f.err == "" {
		t.Fatal("invalid query accepted")
	}
	a.search = ""
	f.scope = 0
	changed := a.snap.Clone()
	changed.Nodes[2].Allocated = 90
	a.snap = changed
	a.prefs.Metric = "Allocated"
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if len(f.ids) != 5 || f.total != 550 {
		t.Fatalf("stale snapshot/metric: %v total %d", f.ids, f.total)
	}
	a.prefs.Metric = "Logical"
	f.run()
	waitModel(t, a, func() bool { return !f.busy })
	if f.total != 1360 {
		t.Fatalf("reused wrong metric total: %d", f.total)
	}
}

// Ctrl-click can leave the focused row unselected. The original refresh
// resets that cursor; reusing matches must preserve the same behavior.
func TestFileResortUnselectedCursor(t *testing.T) {
	for _, otherSelected := range []bool{false, true} {
		a := testApp(t)
		f := &a.files
		f.run()
		waitModel(t, a, func() bool { return !f.busy })
		f.selectOnly(2)
		f.sel.Remove(2)
		if otherSelected {
			f.sel.Add(7)
		}
		f.selCache.invalidate()
		f.sort = ui.SortOrder{Column: "Name"}
		f.run()
		waitModel(t, a, func() bool { return !f.busy })
		if f.cursor != -1 {
			t.Fatalf("unselected cursor retained after resort (other selected=%v): %d", otherSelected, f.cursor)
		}
	}
}

func TestModelResetRejectsQueuedResults(t *testing.T) {
	a := testApp(t)
	a.files.run()
	// Wait until a completion is queued, without applying it.
	waitQueued(t, a)
	a.files.reset()
	a.drain()
	if a.files.forSnap != nil || len(a.files.ids) != 0 {
		t.Fatal("reset applied an old query completion")
	}
	a.tm.ensure()
	waitQueued(t, a)
	a.tm.reset()
	a.drain()
	if len(a.tm.branches) != 0 {
		t.Fatal("reset applied an old treemap completion")
	}
	a.ext.run()
	waitQueued(t, a)
	a.ext.reset()
	a.drain()
	if len(a.ext.types) != 0 {
		t.Fatal("reset applied an old extensions completion")
	}
}

func waitQueued(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.queue) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background completion was not queued")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFitMiddleTextUnicodeAndWidth(t *testing.T) {
	measure := func(s string) float32 { return float32(utf8.RuneCountInString(s)) }
	for _, s := range []string{"", "short", strings.Repeat("résumé-日本-", 25)} {
		for _, width := range []float32{1, 5, 20, 400} {
			got := fitMiddleText(s, width, measure)
			if !utf8.ValidString(got) || measure(got) > width {
				t.Fatalf("invalid fitting text: %q", got)
			}
			if measure(s) <= width && got != s {
				t.Fatal("text that fits was truncated")
			}
		}
	}
	calls := 0
	fitMiddleText(strings.Repeat("a", 1000), 20, func(s string) float32 { calls++; return measure(s) })
	if calls > 12 {
		t.Fatalf("too many text measurements: %d", calls)
	}
}

func referenceAnalysisFilter(ctx context.Context, s *index.Snapshot, f analysisFilter) (*index.Snapshot, index.FilterStats, error) {
	include, err := index.ParseFilter(f.Include)
	if err != nil {
		return nil, index.FilterStats{}, err
	}
	if include.Kind == index.KindAny {
		include.Kind = index.KindFiles
	}
	exclude := scan.NewExclusions(splitPatterns(f.Exclude))
	return s.Filtered(ctx, func(id index.ID) bool {
		return !exclude.Match(s.NameView(id), "", false) && (strings.TrimSpace(f.Include) == "" || include.MatchNode(s.NameView(id), s.Nodes[id], func() string { return s.Path(id) }, ancestorMatches(s, id, &include)))
	}, func(id index.ID) bool {
		return !exclude.Empty() && exclude.Match(s.NameView(id), s.Path(id), true)
	}, f.Isolate || strings.TrimSpace(f.Include) != "")
}

// Original per-file ancestor walk, retained only as a parity reference.
func ancestorMatches(s *index.Snapshot, id index.ID, f *index.Filter) bool {
	if !f.AncestorTerms() {
		return false
	}
	for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
		name := s.Root
		if p != 0 {
			name = s.NameView(p)
		}
		if f.MatchPathComponent(name) {
			return true
		}
	}
	return false
}

func TestFilterReplacementRejectsQueuedResults(t *testing.T) {
	a := testApp(t)
	a.filter = analysisFilter{Include: "ext:mp4"}
	a.applyFilter()
	waitQueued(t, a)
	a.filter = analysisFilter{Include: "ext:log"}
	a.applyFilter()
	waitModel(t, a, func() bool { return !a.filterBusy })
	if a.snap.Nodes[0].Files != 1 || a.snap.Nodes[0].Size != 50 {
		t.Fatal("old filter replaced the latest results")
	}
	a.filter = analysisFilter{}
	a.applyFilter()
	if a.snap != a.base || a.filterBusy {
		t.Fatal("clearing the filter did not restore base")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (analysisFilter{Include: "path:media"}).apply(ctx, a.base); err != context.Canceled {
		t.Fatalf("filter ignored cancellation: %v", err)
	}
}

func TestAnalysisFilterCachedAncestors(t *testing.T) {
	s := fixture()
	s.Remove(7)
	for _, include := range []string{"", "path:data", "path:media", "path:sub", "path:media path:sub", "path:a.log", `path:C:\data\media ext:log`, "ext:mp4", "type:folder"} {
		for _, exclude := range []string{"", "sub", `C:\data\media\sub`, "*.mp4"} {
			f := analysisFilter{Include: include, Exclude: exclude}
			want, wantStats, err := referenceAnalysisFilter(context.Background(), s, f)
			if err != nil {
				t.Fatal(err)
			}
			got, gotStats, err := f.apply(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Nodes, want.Nodes) || gotStats != wantStats {
				t.Fatalf("filter changed results: %+v", f)
			}
		}
	}
}

func BenchmarkAnalysisPathFilter(b *testing.B) {
	s := index.New(`C:\bench`)
	parent := index.ID(0)
	for depth := range 16 {
		parent = s.Add(parent, fmt.Sprintf("level-%02d", depth), 0, 0, 0, index.Directory)
		for i := range 5000 {
			s.Add(parent, fmt.Sprintf("file-%d.log", i), 1, 1, 0, 0)
		}
	}
	s.Finalize()
	f := analysisFilter{Include: "path:level-00"}
	for _, reference := range []bool{true, false} {
		name := "cached"
		if reference {
			name = "reference"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var err error
				if reference {
					_, _, err = referenceAnalysisFilter(context.Background(), s, f)
				} else {
					_, _, err = f.apply(context.Background(), s)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
