package desktop

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestDuplicatePreparedRowsStayImmutableAcrossSorts(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newAppState()
	s := index.New(`C:\not-opened`)
	for i := range 100 {
		folder := s.Add(0, "folder", 0, 0, 0, index.Directory)
		for _, name := range []string{"copy.LOG", "other.bin"} {
			s.Add(folder, name, uint64(1+i%5), uint64(2+i%5), 0, 0)
		}
	}
	s.Finalize()
	a.snap, a.base, a.prefs.View = s, s, viewFiles
	t.Cleanup(a.shutdown)
	a.refreshDuplicates(false)
	waitDuplicates(t, a)
	prepared := slices.Clone(a.duplicates.ids)
	for _, order := range []ui.SortOrder{{Column: "Size", Descending: true}, {Column: "Group", Descending: true}, {Column: "Name"}, {Column: "Group"}, {Column: "Folder"}, {Column: "Group"}} {
		a.files.sort = order
		a.files.invalidate()
		waitDuplicates(t, a)
		want := slices.Clone(prepared)
		if order.Column == "Group" {
			slices.SortFunc(want, func(x, y index.ID) int {
				gx, gy := a.duplicates.groups[x], a.duplicates.groups[y]
				if gx != gy {
					if (gx < gy) != order.Descending {
						return -1
					}
					return 1
				}
				return s.Compare(x, y, index.Sort{Column: "Name"})
			})
		} else if err := sortEntries(context.Background(), s, want, order, a.metricAllocated()); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(a.files.ids, want) || !slices.Equal(a.duplicates.ids, prepared) {
			t.Fatal("sort changed the prepared source or row ordering", order)
		}
	}
}

func duplicateApp(t *testing.T) (*App, map[string]index.ID) {
	t.Helper()
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	s := index.New(dir)
	scope := s.Add(0, "nested", 0, 0, 0, index.Directory)
	ids := map[string]index.ID{"nested": scope}
	for _, entry := range []struct {
		name, data string
		parent     index.ID
	}{
		{"original.txt", "same content", 0},
		{"renamed.bin", "same content", scope},
		{"different.txt", "other things", 0},
		{"empty-a", "", 0},
		{"empty-b", "", scope},
		{"local-a", strings.Repeat("p", 5000), scope},
		{"local-b", strings.Repeat("p", 5000), scope},
	} {
		path := filepath.Join(s.Path(entry.parent), entry.name)
		if err := os.WriteFile(path, []byte(entry.data), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		ids[entry.name] = s.Add(entry.parent, entry.name, uint64(info.Size()), uint64(info.Size())+4096, info.ModTime().Unix(), 0)
	}
	s.Finalize()
	a := newAppState()
	a.prefs.Theme, a.prefs.ShowTreemap = "Dark", false
	a.setBase(s, "", nil, false)
	t.Cleanup(a.shutdown)
	return a, ids
}

func waitDuplicates(t *testing.T, a *App) {
	t.Helper()
	waitModel(t, a, func() bool { return !a.duplicates.busy && !a.files.busy && a.files.forSnap == a.snap })
	if a.duplicates.err != "" || a.files.err != "" {
		t.Fatalf("duplicates: %q; files: %q", a.duplicates.err, a.files.err)
	}
}

func TestDuplicateResultsUseFilesViewAndFileCommands(t *testing.T) {
	a, ids := duplicateApp(t)
	beforeSort := a.files.sort
	a.files.list.Columns.Order = []string{"Name", "Size", "Modified", "Folder"}
	a.exec("tools.dups")
	waitDuplicates(t, a)
	d := &a.duplicates
	if d.verify || d.verified || d.progress.Bytes.Load() != 0 || len(a.files.ids) != 0 {
		t.Fatal("opening Duplicates read file contents or matched renamed files")
	}
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	if a.prefs.View != viewFiles || !d.active || !d.verified || d.minSize != "0" || len(d.result.Groups) != 3 || len(a.files.ids) != 6 {
		t.Fatalf("wrong default or missing renamed/small/empty duplicates: %+v, files %v", d, a.files.ids)
	}
	if slices.Contains(a.files.ids, ids["different.txt"]) || d.groups[ids["original.txt"]] != d.groups[ids["renamed.bin"]] {
		t.Fatal("incorrect file membership")
	}
	if a.files.list.Columns.Order[0] != "Group" {
		t.Fatal("saved column order hid the new Group column")
	}
	gen := d.gen
	a.files.selectOnly(ids["renamed.bin"])
	if !slices.Equal(a.selected(), []index.ID{ids["renamed.bin"]}) || !a.can("edit.copyPath") || !a.can("edit.rename") || !a.can("export.selection") {
		t.Fatal("duplicate rows do not use normal file actions")
	}
	a.files.sort = ui.SortOrder{Column: "Name", Descending: true}
	a.files.run()
	waitDuplicates(t, a)
	if !a.files.sel.Has(ids["renamed.bin"]) || d.gen != gen {
		t.Fatal("sort rehashed duplicates or lost selection")
	}
	a.prefs.Metric = "Allocated"
	a.files.run()
	waitDuplicates(t, a)
	var total uint64
	for _, id := range a.files.ids {
		total += a.snap.Nodes[id].Allocated
	}
	if a.files.total != total || d.gen != gen {
		t.Fatal("wrong duplicate total or metric rehashed files")
	}
	a.search = "original"
	a.files.run()
	waitDuplicates(t, a)
	if !slices.Equal(a.files.ids, []index.ID{ids["original.txt"]}) || d.gen != gen {
		t.Fatal("search does not filter verified results without rehashing")
	}
	a.search = ""
	a.showDuplicates(false)
	waitModel(t, a, func() bool { return !a.files.busy })
	if a.files.sort != beforeSort || len(a.files.ids) != 7 || d.active {
		t.Fatal("leaving duplicates did not restore all files and their sort")
	}
}

func TestDuplicatesFollowScopeAndDiscardObsoleteResults(t *testing.T) {
	a, ids := duplicateApp(t)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	a.files.pinScope(ids["nested"])
	waitDuplicates(t, a)
	if a.duplicates.verify || a.duplicates.verified || len(a.files.ids) != 0 {
		t.Fatal("changing scope automatically reverified contents")
	}
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	if len(a.duplicates.result.Groups) != 1 || !slices.Equal(a.duplicates.result.Groups[0].IDs, []index.ID{ids["local-a"], ids["local-b"]}) || len(a.files.ids) != 2 {
		t.Fatal("scope includes copies outside the folder", a.files.ids)
	}
	a.files.pinScope(0)
	waitDuplicates(t, a)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	// Queue a completion, change the snapshot, then drain. The older result
	// must never resurrect a removed row or retain the previous scan.
	a.findDuplicates()
	waitQueued(t, a)
	next := a.snap.Clone()
	next.Remove(ids["renamed.bin"])
	a.replaceBase(next)
	waitDuplicates(t, a)
	if a.duplicates.verify || a.duplicates.verified {
		t.Fatal("snapshot change silently restarted full-content verification")
	}
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	if a.duplicates.forSnap != next || len(a.duplicates.result.Groups) != 2 || slices.Contains(a.files.ids, ids["original.txt"]) || slices.Contains(a.files.ids, ids["renamed.bin"]) {
		t.Fatal("obsolete verification leaked into the new snapshot")
	}
	// Restart followed by exit must also reject an already queued completion.
	a.findDuplicates()
	waitQueued(t, a)
	a.showDuplicates(false)
	a.drain()
	waitModel(t, a, func() bool { return !a.files.busy })
	if a.duplicates.active || a.duplicates.forSnap != nil || len(a.files.ids) != 6 {
		t.Fatal("exiting duplicates applied a queued verification")
	}
}

func TestDuplicateSettingsErrorsAndCandidateMode(t *testing.T) {
	a, ids := duplicateApp(t)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	d := &a.duplicates
	d.verify = false
	a.findDuplicates()
	waitDuplicates(t, a)
	if d.verified || len(a.files.ids) != 0 || !strings.Contains(a.duplicateStatus(), "Candidates only") {
		t.Fatal("unverified candidates presented as content duplicates")
	}
	d.verify, d.minSize = true, "1KB"
	a.findDuplicates()
	waitDuplicates(t, a)
	if len(a.files.ids) != 2 || d.groups[ids["local-a"]] == 0 {
		t.Fatal("minimum size not applied")
	}
	d.minSize = "invalid"
	a.findDuplicates()
	if d.err == "" || len(a.files.ids) != 0 || len(d.groups) != 0 {
		t.Fatal("invalid settings retained actionable old results")
	}
	gen := d.gen
	for range 3 {
		a.files.ensure()
	}
	waitModel(t, a, func() bool { return !a.files.busy })
	if d.gen != gen {
		t.Fatal("invalid settings retried every frame")
	}
	d.minSize = "0"
	a.findDuplicates()
	waitDuplicates(t, a)
	if len(a.files.ids) != 6 {
		t.Fatal("corrected settings did not recover all groups")
	}
}

func TestDuplicateMenuKeepsTheTableHeaderCompact(t *testing.T) {
	for _, width := range []int{760, 1280} {
		a, ids := duplicateApp(t)
		s := a.snap.Clone()
		for _, parent := range []index.ID{0, ids["nested"]} {
			data := []byte(strings.Repeat("b", 200))
			if err := os.WriteFile(filepath.Join(s.Path(parent), "backup.dat"), data, 0600); err != nil {
				t.Fatal(err)
			}
			s.Add(parent, "backup.dat", uint64(len(data)), uint64(len(data)), 0, 0)
		}
		s.Reaggregate()
		a.setBase(s, "", nil, false)
		a.setViewMode(viewFiles)
		waitModel(t, a, func() bool { return !a.files.busy })
		tt := ui.NewTester(a.view, width, 480)
		tt.Frame()
		header, _ := tt.Find("Name")
		if err := tt.Click("File display"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Duplicates (name and size)"); err != nil {
			t.Fatal(err)
		}
		settle(tt, func() bool { return !a.duplicates.busy && !a.files.busy && a.files.forSnap == a.snap })
		if a.duplicates.verify || a.duplicates.verified || len(a.files.ids) != 2 || a.duplicates.progress.Bytes.Load() != 0 {
			t.Fatal("the normal Duplicates action read contents instead of using metadata")
		}
		for _, want := range []string{"Duplicates", "2 candidates", "Group", "backup.dat"} {
			if !tt.HasText(want) {
				t.Fatalf("width %v: missing %q in %q", width, want, tt.Texts())
			}
		}
		for _, unwanted := range []string{"Minimum size", "Verify content", "Some files were skipped", "Search filters duplicate results"} {
			if tt.HasText(unwanted) {
				t.Fatalf("advanced controls or helper text clutter the Files view: %q", unwanted)
			}
		}
		for _, label := range []string{"File display", "Scope", "Name"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.X+r.W > float32(width) || r.Y < 0 || r.Y+r.H > 480 {
				t.Fatalf("width %d: %q is outside the window: %+v", width, label, r)
			}
		}
		if compact, _ := tt.Find("Name"); compact.Y != header.Y {
			t.Fatalf("duplicate controls added a row above the table: %v vs %v", compact.Y, header.Y)
		}
		if err := tt.Click("File display"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Minimum size", "1 KB"); err != nil {
			t.Fatal(err)
		}
		settle(tt, func() bool { return !a.duplicates.busy && !a.files.busy && a.files.forSnap == a.snap })
		if a.duplicates.minSize != "1KB" || len(a.files.ids) != 0 || a.duplicates.verify {
			t.Fatal("minimum-size menu failed or started content verification")
		}
		if err := tt.Click("File display"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Minimum size", "All sizes"); err != nil {
			t.Fatal(err)
		}
		settle(tt, func() bool { return !a.duplicates.busy && !a.files.busy && a.files.forSnap == a.snap })
		if err := tt.Click("File display"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("Verify contents (slow)"); err != nil {
			t.Fatal(err)
		}
		settle(tt, func() bool { return !a.duplicates.busy && !a.files.busy && a.files.forSnap == a.snap })
		if !a.duplicates.verified || len(a.files.ids) != 8 {
			t.Fatal("explicit verification did not find renamed copies")
		}
		if err := tt.Click("original.txt"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !slices.Equal(a.selected(), []index.ID{ids["original.txt"]}) {
			t.Fatal("inline duplicate file not selectable")
		}
		if err := tt.Click("File display"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("All files"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		waitModel(t, a, func() bool { return !a.files.busy })
		if a.duplicates.active || len(a.files.ids) != 9 {
			t.Fatal("All files menu failed to restore Files")
		}
	}
}

func TestDuplicateDefaultNeedsNoFilesystemReads(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	s := index.New(filepath.Join(t.TempDir(), "unavailable"))
	left := s.Add(0, "left", 0, 0, 0, index.Directory)
	right := s.Add(0, "right", 0, 0, 0, index.Directory)
	first := s.Add(left, "report.bin", 8192, 8192, 0, 0)
	second := s.Add(right, "report.bin", 8192, 8192, 0, 0)
	s.Finalize()
	a := newAppState()
	t.Cleanup(a.shutdown)
	a.setBase(s, "", nil, false)
	a.showDuplicates(true)
	waitDuplicates(t, a)
	if a.duplicates.verify || a.duplicates.verified || !slices.Equal(a.files.ids, []index.ID{first, second}) || len(a.duplicates.result.Issues) != 0 || a.duplicates.progress.Files.Load() != 0 || a.duplicates.progress.Bytes.Load() != 0 {
		t.Fatal("default duplicate listing accessed unavailable files")
	}
	// Verification remains available, and its choice does not carry over to
	// a new scan or to the next time the user enables Duplicates.
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	if !a.duplicates.verified || a.duplicates.result.Skipped != 2 {
		t.Fatal("explicit verification did not check the files")
	}
	a.replaceBase(s.Clone())
	waitDuplicates(t, a)
	if a.duplicates.verify || len(a.files.ids) != 2 {
		t.Fatal("new scan automatically verified contents")
	}
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	a.showDuplicates(false)
	a.showDuplicates(true)
	waitDuplicates(t, a)
	if a.duplicates.verify || len(a.files.ids) != 2 {
		t.Fatal("enabling Duplicates reused slow verification")
	}
}

func TestExplicitFileDrillDownLeavesDuplicateFilter(t *testing.T) {
	a, ids := duplicateApp(t)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	a.setViewMode(viewExtensions)
	a.showExtensionFiles(".txt")
	waitModel(t, a, func() bool { return !a.files.busy })
	if a.duplicates.active || len(a.files.ids) != 2 || !slices.Contains(a.files.ids, ids["different.txt"]) {
		t.Fatal("extension drill-down silently hid nonduplicate files")
	}
	a.search = ""
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	a.reveal(ids["nested"])
	a.searchHere()
	waitModel(t, a, func() bool { return !a.files.busy })
	if a.duplicates.active || len(a.files.ids) != 4 || !slices.Contains(a.files.ids, ids["renamed.bin"]) {
		t.Fatal("folder search silently retained the duplicate filter")
	}
}

func TestDuplicateCancelRejectsQueuedResult(t *testing.T) {
	a, _ := duplicateApp(t)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	a.findDuplicates()
	waitQueued(t, a)
	// Keep the result queued until the real Cancel control has handled input.
	tt := ui.NewTester(func(c *ui.Context) { a.fileDisplayMenu(c) }, 760, 130)
	tt.Frame()
	if err := tt.Click("File display"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Stop checking"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.drain()
	waitDuplicates(t, a)
	if a.duplicates.busy || len(a.files.ids) != 0 || !strings.Contains(a.duplicateStatus(), "canceled") {
		t.Fatal("cancel applied a queued result or restarted verification")
	}
}
