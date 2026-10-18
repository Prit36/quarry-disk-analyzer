package desktop

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
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
	if len(a.drives) > 0 && !tt.HasText(a.drives[0].Path) {
		t.Fatalf("drive %s not listed", a.drives[0].Path)
	}
	for i := 0; i < 5; i++ {
		tt.Advance(100 * time.Millisecond)
		tt.Frame()
	}
	if a.scanning {
		t.Fatal("the start page began a scan without a click")
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
	if bytes, ok := a.metadataBytes(); !ok || bytes != 1<<30 {
		t.Fatalf("the status bar's engine tip should report %d bytes of hidden metadata, got %d %v", 1<<30, bytes, ok)
	}
	a.exec("opt.showMeta")
	settle(tt, func() bool { return !a.filterBusy && a.snap.Reachable(bad) })
	if !a.snap.Reachable(bad) || a.snap.Nodes[0].Size != 1<<30+100 {
		t.Fatalf("Show NTFS Metadata Files: size %d", a.snap.Nodes[0].Size)
	}
}

// A path typed into the location bar goes to that folder of the scan, case
// insensitively, and reveals files; anything else is not found.
func TestLocationBarPath(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	s := index.New(`C:\`)
	users := s.Add(0, "Users", 0, 0, 0, index.Directory)
	me := s.Add(users, "Me", 0, 0, 0, index.Directory)
	file := s.Add(me, "notes.txt", 10, 10, 0, 0)
	s.Finalize()
	a := newApp()
	a.prefs.Theme = "Dark"
	tt := ui.NewTester(a.view, 1000, 700)
	a.setBase(s, "", nil, false)
	settle(tt, func() bool { return a.snap != nil })
	if id, ok := a.findPath(`c:\users\ME\`); !ok || id != me {
		t.Fatalf("findPath: %d %v, want %d", id, ok, me)
	}
	if id, ok := a.findPath(`C:\`); !ok || id != 0 {
		t.Fatalf("findPath root: %d %v", id, ok)
	}
	if _, ok := a.findPath(`C:\Users\Nobody`); ok {
		t.Fatal("findPath found a missing folder")
	}
	if _, ok := a.findPath(`D:\Users`); ok {
		t.Fatal("findPath found a path outside the scan")
	}
	a.editLocation()
	a.goToLocation(`C:\Users\Me`)
	if a.location != me || a.locEdit {
		t.Fatalf("location %d editing %v, want %d", a.location, a.locEdit, me)
	}
	a.goToLocation(`C:\Users\Me\notes.txt`)
	if a.current() != file {
		t.Fatalf("typing a file path should reveal it, current %d", a.current())
	}
}

// One click on a drive on the start page selects it; a double-click scans it.
func TestStartPageDoubleClickScansDrive(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	dir := t.TempDir()
	a.menu = &menuBar{} // windowless: scan completion marks the recent list
	a.drives = []platform.Drive{{Path: dir, Name: "Test volume", FileSystem: "NTFS", Total: 100, Free: 40}}
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.Click("Test volume"); err != nil {
		t.Fatalf("click: %v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if a.scanning || a.snap != nil || a.dlg.driveRow != 0 {
		t.Fatalf("one click should only select the drive: scanning %v, row %d", a.scanning, a.dlg.driveRow)
	}
	if err := tt.Click("Test volume"); err != nil { // the second click of a double-click
		t.Fatalf("click: %v", err)
	}
	settle(tt, func() bool { return a.snap != nil })
	if a.snap == nil || a.snap.Root != dir {
		t.Fatalf("a double-click should scan %s, scanning %v", dir, a.scanning)
	}
}

// Clearing a search that typing started in Tree View returns to Tree View;
// a view the user chose stays.
func TestClearingSearchReturnsToTree(t *testing.T) {
	a := testApp(t)
	a.tree.selectOnly(6)
	a.search = "report"
	a.setViewMode(viewFiles)
	a.searchLeftTree = true
	a.search = ""
	a.searchCleared(time.Now())
	if a.prefs.View != viewTree || a.tree.cursorID() != 6 {
		t.Fatalf("view %d cursor %d, want Tree View on the same row", a.prefs.View, a.tree.cursorID())
	}
	a.setViewMode(viewFiles) // chosen by the user
	a.search = "report"
	a.search = ""
	a.searchCleared(time.Now())
	if a.prefs.View != viewFiles {
		t.Fatalf("a view chosen by the user should stay, got %d", a.prefs.View)
	}
}

// File View and Extensions follow the location unless a scope was chosen.
func TestScopeFollowsLocation(t *testing.T) {
	a := testApp(t)
	a.navigate(1) // media
	if a.files.scope != 1 {
		t.Fatalf("scope %d, want the location 1", a.files.scope)
	}
	a.files.pinScope(0)
	a.navigate(6) // docs
	if a.files.scope != 0 {
		t.Fatalf("a chosen scope moved with navigation: %d", a.files.scope)
	}
	a.files.followLocation()
	if a.files.scope != 6 || a.files.pinned {
		t.Fatalf("scope %d pinned %v, want the location 6 again", a.files.scope, a.files.pinned)
	}
	a.up()
	if a.files.scope != 0 {
		t.Fatalf("scope %d after going up, want 0", a.files.scope)
	}
}

// Tree View shows the drive header and the file types panel beside the tree.
func TestDriveHeaderAndTypesPanel(t *testing.T) {
	a := testApp(t)
	drives := []platform.Drive{{Path: `C:\`, Name: "Windows", FileSystem: "NTFS", Total: 100 << 30, Free: 5 << 30}}
	a.drives = drives
	tt := ui.NewTester(a.view, 1280, 800)
	settle(tt, func() bool { return !a.ext.busy && len(a.ext.bySize) > 0 })
	a.drives = drives
	tt.Frame()
	for _, want := range []string{"Windows (C:)", "NTFS · 95 % full", "5.0 GiB", "free of 100.0 GiB", "Rest of drive", "Scanned with", "File Type", ".mp4", ".pdf"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if got := a.ext.types[a.ext.bySize[0]].Extension; got != ".mp4" {
		t.Fatalf("largest type %q", got)
	}
	a.exec("view.types")
	tt.Frame()
	if a.prefs.ShowTypes || tt.HasText("File Type") {
		t.Fatal("F8 should hide the panel")
	}
}

// The size metric has no control on screen; it is named only when it is
// not the default, and the treemap bar carries no instructions.
func TestMetricNamedOnlyWhenAllocated(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	if tt.HasText("Allocated size") || tt.HasText("Logical size") || tt.HasText("Click to select") {
		t.Fatalf("unexpected text in %q", tt.Texts())
	}
	a.exec("metric.allocated")
	tt.Frame()
	if !tt.HasText("Allocated size") {
		t.Fatalf("allocated metric not named: %q", tt.Texts())
	}
}
