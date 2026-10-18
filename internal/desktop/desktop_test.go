package desktop

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// fixture: C:\data
//
//	media/            (1)
//	  big.mp4    900  (2)
//	  clip.mp4   100  (3)
//	  sub/            (4)
//	    a.log     50  (5)
//	docs/             (6)
//	  report.pdf 300  (7)
//	readme.txt    10  (8)
func fixture() *index.Snapshot {
	s := index.New(`C:\data`)
	media := s.Add(0, "media", 0, 0, 0, index.Directory)
	s.Add(media, "big.mp4", 900, 900, 1, 0)
	s.Add(media, "clip.mp4", 100, 100, 1, 0)
	sub := s.Add(media, "sub", 0, 0, 0, index.Directory)
	s.Add(sub, "a.log", 50, 50, 1, 0)
	docs := s.Add(0, "docs", 0, 0, 0, index.Directory)
	s.Add(docs, "report.pdf", 300, 300, 1, 0)
	s.Add(0, "readme.txt", 10, 10, 1, 0)
	s.Finalize()
	return s
}

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	a.setBase(fixture(), "", nil, false)
	return a
}

func names(a *App) []string {
	var out []string
	for _, r := range a.tree.rows {
		out = append(out, strings.Repeat(" ", int(r.depth))+a.name(r.id))
	}
	return out
}

func TestTreeRowsSortedBySize(t *testing.T) {
	a := testApp(t)
	want := []string{`C:\data`, " media", " docs", " readme.txt"}
	if got := names(a); !slices.Equal(got, want) {
		t.Fatalf("rows %q", got)
	}
	if a.tree.cursorID() != 0 || !a.tree.sel.Has(0) {
		t.Fatal("the scan root should be selected after loading")
	}
}

func TestTreeExpandCollapseKeepsSelection(t *testing.T) {
	a := testApp(t)
	tr := &a.tree
	tr.selectOnly(1) // media
	tr.keyRight()    // expand
	want := []string{`C:\data`, " media", "  big.mp4", "  clip.mp4", "  sub", " docs", " readme.txt"}
	if got := names(a); !slices.Equal(got, want) {
		t.Fatalf("expanded rows %q", got)
	}
	if tr.cursorID() != 1 {
		t.Fatal("expanding must not move the selection")
	}
	tr.keyRight() // to first child
	if tr.cursorID() != 2 {
		t.Fatalf("Right on an open folder goes to its first child, got %d", tr.cursorID())
	}
	// Collapsing the parent of the selected row moves the selection to it.
	tr.collapseRow(tr.indexOf(1))
	if tr.cursorID() != 1 || !tr.sel.Has(1) || tr.sel.Has(2) {
		t.Fatalf("selection after collapse: cursor %d", tr.cursorID())
	}
	tr.keyLeft() // closed folder: go to parent
	if tr.cursorID() != 0 {
		t.Fatal("Left on a closed folder goes to the parent")
	}
}

func TestTreeExpandSubtreeIsBounded(t *testing.T) {
	a := testApp(t)
	old := expandBudget
	expandBudget = 2
	defer func() { expandBudget = old }()
	a.tree.expandSubtree(0)
	if a.tree.notice == "" {
		t.Fatal("a capped expansion must say so")
	}
	expandBudget = old
	a.tree.expandSubtree(0)
	if len(a.tree.rows) != 9 {
		t.Fatalf("full expansion: %q", names(a))
	}
	a.tree.collapseAll()
	if len(a.tree.rows) != 4 {
		t.Fatalf("collapse all: %q", names(a))
	}
}

func TestRevealAndNavigation(t *testing.T) {
	a := testApp(t)
	a.reveal(5) // media/sub/a.log
	if a.tree.cursorID() != 5 || a.prefs.View != viewTree {
		t.Fatalf("reveal: %q cursor %d", names(a), a.tree.cursorID())
	}
	a.navigate(4) // into sub
	if a.location != 4 || a.tree.rows[0].id != 4 {
		t.Fatal("navigate sets the tree root")
	}
	a.up()
	if a.location != 1 || a.tree.cursorID() != 4 {
		t.Fatal("up selects the folder we came from")
	}
	a.back()
	if a.location != 4 {
		t.Fatal("back")
	}
	a.reveal(7) // outside the location: widens to the scan root
	if a.location != 0 || a.tree.cursorID() != 7 {
		t.Fatal("reveal outside the location")
	}
}

func TestCommandAvailability(t *testing.T) {
	a := testApp(t)
	a.tree.selectOnly(0)
	if a.can("edit.delete") || a.can("edit.rename") {
		t.Fatal("the scan root must not be deletable or renamable")
	}
	a.tree.reveal(7)
	a.tree.selCache.invalidate()
	if !a.can("edit.copyPath") || !a.can("edit.delete") || !a.can("edit.rename") {
		t.Fatal("an ordinary file can be copied, deleted and renamed")
	}
	a.source = "saved.quarry"
	a.tree.selCache.invalidate()
	if a.can("edit.delete") || a.can("edit.cut") || a.mutable() {
		t.Fatal("saved scans are historical: no file operations")
	}
}

func TestMetadataIsProtected(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	s := index.New(`C:\`)
	s.Engine = "NTFS · direct MFT"
	bad := s.Add(0, "$BadClus", 1<<30, 0, 0, 0)
	s.Add(0, "pagefile.sys", 1<<30, 1<<30, 0, 0)
	s.Finalize()
	a := newApp()
	a.prefs.Theme = "Dark"
	a.prefs.ShowMetadata = true
	a.setBase(s, "", metadataEntries(s), false)
	if reason := a.blocked(bad); !strings.Contains(reason, "metadata") {
		t.Fatalf("$BadClus: %q", reason)
	}
	if reason := a.blocked(2); reason == "" {
		t.Fatal("pagefile.sys must be protected")
	}
	a.tree.selectOnly(bad)
	if a.mutable() {
		t.Fatal("metadata selection must hide file-management commands")
	}
	if a.can("item.explorer") || a.can("item.open") || a.can("item.properties") {
		t.Fatal("metadata has no shell path: Open, Show in Explorer and Properties must be unavailable")
	}
}

func TestAnalysisFilterRecomputesTotals(t *testing.T) {
	a := testApp(t)
	f := analysisFilter{Include: "ext:mp4"}
	view, stats, err := f.apply(context.Background(), a.base)
	if err != nil {
		t.Fatal(err)
	}
	if view.Nodes[0].Size != 1000 || view.Nodes[0].Files != 2 || stats.HiddenFiles != 3 || view.Reachable(6) {
		t.Fatalf("filtered: %+v %+v", view.Nodes[0], stats)
	}
	ex := analysisFilter{Exclude: "sub; *.txt"}
	view, _, err = ex.apply(context.Background(), a.base)
	if err != nil || view.Nodes[0].Size != 1300 || view.Reachable(4) || view.Reachable(8) {
		t.Fatalf("exclude: %+v %v", view.Nodes[0], err)
	}
}

func TestAdvancedSearchQuery(t *testing.T) {
	d := searchDialog{name: "*.log", ext: "log;txt", minSize: "100 MB", before: "2025-01-01", kind: "Files", path: "my docs"}
	q := d.query()
	if q != `*.log path:"my docs" ext:log;txt size:>=100MB modified:<=2025-01-01 type:file` {
		t.Fatalf("query %q", q)
	}
	if _, err := index.ParseFilter(q); err != nil {
		t.Fatal(err)
	}
}

// The window, headless: tree, File View search, extensions and dialogs.
func TestViewHeadless(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	for _, want := range []string{"media", "docs", "readme.txt", "Treemap"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	// Typing a search switches to File View and lists matches.
	a.exec("edit.find")
	tt.Frame()
	tt.Type("*.mp4")
	settle(tt, func() bool { return !a.files.busy && a.files.ran == "*.mp4" })
	if a.prefs.View != viewFiles || len(a.files.ids) != 2 || a.files.ids[0] != 2 {
		t.Fatalf("search results %v view %d err %q", a.files.ids, a.prefs.View, a.files.err)
	}
	if !tt.HasText("big.mp4") || !tt.HasText("2 matches · 1000 B") {
		t.Fatalf("file view texts %q", tt.Texts())
	}
	// Extensions view.
	a.exec("view.ext")
	settle(tt, func() bool { return !a.ext.busy })
	if !tt.HasText(".mp4") || !tt.HasText(".pdf") {
		t.Fatalf("extensions %q", tt.Texts())
	}
	a.showExtensionFiles(".pdf")
	settle(tt, func() bool { return !a.files.busy })
	if len(a.files.ids) != 1 || a.files.ids[0] != 7 {
		t.Fatalf("extension drill-down %v", a.files.ids)
	}
	// Help and the rename dialog render.
	a.exec("help.keys")
	tt.Frame()
	if !tt.HasText("Keyboard Shortcuts") {
		t.Fatal("help dialog")
	}
	a.dlg.help = helpNone
	a.exec("view.tree")
	a.tree.selectOnly(8)
	a.exec("edit.rename")
	tt.Frame()
	if !a.dlg.rename.open || !tt.HasText("Rename") {
		t.Fatal("rename dialog")
	}
}

// settle renders frames until background work has reported back.
func settle(tt *ui.Tester, done func() bool) {
	for i := 0; i < 200 && !done(); i++ {
		tt.Advance(200 * time.Millisecond)
		tt.Frame()
		time.Sleep(5 * time.Millisecond) // let the worker goroutine finish
	}
	tt.Frame()
}

func TestStartPageListsDrives(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if !tt.HasText("Choose a drive or folder to analyze") {
		t.Fatalf("start page %q", tt.Texts())
	}
	if len(a.drives) > 0 && !tt.HasText(a.drives[0].Path) {
		t.Fatalf("drive %s not listed", a.drives[0].Path)
	}
}

// The treemap's context menu acts on the tile it was opened on, even after
// the pointer moves elsewhere to choose an item; hovering describes a tile.
func TestTreemapHoverAndContextMenu(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	settle(tt, func() bool { return !a.tm.busy && a.tm.cells != nil })
	i, ok := a.tm.cellOf[7] // docs/report.pdf
	if !ok {
		t.Fatal("report.pdf has no tile")
	}
	// Tile rectangles are relative to the treemap box: find its height by
	// moving down the tile's column until the hover reports it.
	r := a.tm.cells[i].Tile.Rect
	var bx, by float32 = -1, -1
	for y := float32(0); y < 800 && bx < 0; y += 2 {
		tt.Move(r.X+r.W/2, y)
		tt.Frame()
		if a.hover == 7 {
			bx, by = r.X+r.W/2, y
		}
	}
	if bx < 0 {
		t.Fatal("hovering the report.pdf tile never reported it")
	}
	if !tt.HasText(`C:\data\docs\report.pdf`) {
		t.Fatalf("status bar should show the hovered path: %q", tt.Texts())
	}
	a.tree.selectOnly(0)
	tt.RightClickAt(bx, by)
	tt.Frame()
	if menu := tt.Menu(); len(menu) == 0 || menu[0] != "report.pdf" {
		t.Fatalf("treemap menu %q", menu)
	}
	tt.Move(5, 5)
	tt.Frame()
	tt.CloseMenu()
	if a.current() != 7 || !a.can("edit.copyPath") {
		t.Fatalf("menu commands must target the right-clicked tile, current %d", a.current())
	}
}

// NTFS metadata is hidden by default, totals exclude it, and the option
// lists it again.
func TestMetadataHiddenByDefault(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	s := index.New(`C:\`)
	s.Engine = "NTFS · direct MFT"
	bad := s.Add(0, "$BadClus", 1<<30, 1<<30, 0, 0)
	users := s.Add(0, "Users", 0, 0, 0, index.Directory)
	s.Add(users, "$notmeta.txt", 100, 100, 0, 0)
	s.Finalize()
	a := newApp()
	a.prefs.Theme = "Dark"
	tt := ui.NewTester(a.view, 1000, 700)
	a.setBase(s, "", metadataEntries(s), false)
	settle(tt, func() bool { return !a.filterBusy && a.snap != nil })
	if a.snap.Reachable(bad) || a.snap.Nodes[0].Size != 100 || !a.snap.Reachable(3) {
		t.Fatalf("metadata should be hidden: size %d", a.snap.Nodes[0].Size)
	}
	if !tt.HasText("NTFS metadata hidden") {
		t.Fatalf("the information bar should say metadata is hidden: %q", tt.Texts())
	}
	a.exec("opt.showMeta")
	settle(tt, func() bool { return !a.filterBusy && a.snap.Reachable(bad) })
	if !a.snap.Reachable(bad) || a.snap.Nodes[0].Size != 1<<30+100 {
		t.Fatalf("Show NTFS Metadata Files: size %d", a.snap.Nodes[0].Size)
	}
}
