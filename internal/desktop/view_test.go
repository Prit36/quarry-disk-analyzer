package desktop

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
	"stratum/internal/treemap"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	a := New(`C:\Storage`)
	a.drives = nil
	s := index.New(`C:\Storage`)
	media := s.Add(0, "Media", 0, 0, 0, index.Directory)
	projects := s.Add(0, "Projects", 0, 0, 0, index.Directory)
	downloads := s.Add(0, "Downloads", 0, 0, 0, index.Directory)
	s.Add(media, "Mountain expedition.mp4", 12<<30, 12<<30, 1760000000, 0)
	s.Add(media, "Music collection.flac", 3<<30, 3<<30, 1760000000, 0)
	s.Add(projects, "Design archive.zip", 5<<30, 5<<30, 1760000000, 0)
	s.Add(downloads, "Photo library.zip", 4<<30, 4<<30, 1760000000, 0)
	s.Add(0, "System backup.iso", 9<<30, 9<<30, 1760000000, 0)
	s.Engine = "Directory scan"
	s.Finalize()
	s.Elapsed = 183 * time.Millisecond
	a.snapshot = s
	a.summary, _ = s.Summarize(context.Background())
	a.selected = 0
	a.focus = 0
	a.open[0] = true
	a.mapFor = 0
	a.mapTotal = s.Nodes[0].Size
	a.branches = treemap.Hierarchy(context.Background(), s, 0, false, 4096)
	r, _ := s.QueryRows(context.Background(), index.Query{Open: a.open, Sort: index.Sort{Desc: true}})
	a.rows = r.Rows
	a.matches = r.Matches
	t.Cleanup(func() {
		if a.queryCancel != nil {
			a.queryCancel()
		}
		if a.mapCancel != nil {
			a.mapCancel()
		}
		if a.analysisCancel != nil {
			a.analysisCancel()
		}
		close(a.done)
	})
	return a
}

func TestMFTBlockedStateAndMapSelection(t *testing.T) {
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	a := New(`C:\`)
	a.drives = nil
	a.admin = false
	a.mftBlocked = true
	tt := ui.NewTester(a.view, 1000, 600)
	if !tt.HasText("MFT unavailable") || !tt.HasText("Run as administrator") {
		t.Fatal("MFT failure hidden")
	}
	app := testApp(t)
	uiTest := ui.NewTester(app.view, 1280, 760)
	app.selectMap(4)
	settle(t, app, uiTest)
	if app.selected != 4 || app.selectedRow < 0 || app.rows[app.selectedRow].ID != 4 {
		t.Fatal("map/tree selection not synchronized", app.selected, app.selectedRow)
	}
}

func TestFailedRescanPreservesResults(t *testing.T) {
	a := testApp(t)
	previous := a.snapshot
	rowCount := len(a.rows)
	a.path = filepath.Join(t.TempDir(), "missing")
	a.start()
	tt := ui.NewTester(a.view, 1280, 720)
	deadline := time.After(5 * time.Second)
	for a.busy {
		select {
		case e := <-a.events:
			a.events <- e
			tt.Frame()
		case <-deadline:
			t.Fatal("rescan did not finish")
		}
	}
	if a.snapshot != previous || len(a.rows) != rowCount || a.notice == "" {
		t.Fatal("failed rescan discarded previous results")
	}
}
func settle(t *testing.T, a *App, tt *ui.Tester) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for a.queryBusy || a.analysisBusy || a.mapBusy || a.historyBusy || a.duplicateBusy {
		select {
		case e := <-a.events:
			a.events <- e
			tt.Frame()
		case <-timeout:
			t.Fatal("background query did not complete")
		}
	}
	tt.Frame()
}
func TestNativeNavigationSearchAndResize(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	tt.Frame()
	if !tt.HasText("Tree view") || !tt.HasText("Media") {
		t.Fatal("results not rendered", tt.Texts())
	}
	if err := tt.Click("Expand Media"); err != nil {
		t.Fatal(err)
	}
	settle(t, a, tt)
	if !tt.HasText("Mountain expedition.mp4") {
		t.Fatal("expansion did not show children")
	}
	if err := tt.Click("File view"); err != nil {
		t.Fatal(err)
	}
	settle(t, a, tt)
	if a.tab != 1 || a.matches != 5 {
		t.Fatalf("largest view failed: tab=%d matches=%d busy=%v", a.tab, a.matches, a.queryBusy)
	}
	a.query = "ext:zip"
	a.requestRows()
	settle(t, a, tt)
	if a.matches != 2 {
		t.Fatal("filter did not match archive files")
	}
	if err := tt.Click("Details"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Method: Folders") {
		t.Fatal("notes did not render")
	}
	tt.SetSize(1060, 700)
	if tt.FramePending() {
		tt.Frame()
	}
	if !tt.HasText("Scan") {
		t.Fatal("toolbar disappeared at minimum size")
	}
	tt.SetSize(1440, 900)
	a.tab = 0
	a.query = ""
	a.requestRows()
	settle(t, a, tt)
	if dir := os.Getenv("STRATUM_SCREENSHOT_DIR"); dir != "" {
		os.MkdirAll(dir, 0755)
		f, err := os.Create(filepath.Join(dir, "native-preview.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
func TestEmptyAndTheme(t *testing.T) {
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	a := New("")
	a.drives = nil
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("Select a drive or folder.") {
		t.Fatal("empty state missing")
	}
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Toggle theme"); err != nil {
		t.Fatal(err)
	}
	if !a.light {
		t.Fatal("theme did not change")
	}
}
func TestNativeRender(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	tt.Frame()
	if dir := os.Getenv("STRATUM_SCREENSHOT_DIR"); dir != "" {
		os.MkdirAll(dir, 0755)
		f, err := os.Create(filepath.Join(dir, "native-preview.png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	r, ok := tt.Find("File view")
	t.Log("tabs", r, ok)
}

func TestDesignEvidence(t *testing.T) {
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	dir := os.Getenv("STRATUM_DESIGN_DIR")
	if dir == "" {
		t.Skip("design capture is opt-in")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	a := New(`C:\Storage`)
	a.drives = nil
	s := index.New(a.path)
	for d, name := range []string{"Users", "Program Files", "Windows", "Projects", "Games", "Backups", "Downloads", "Media", "Documents", "Repositories", "Temp", "Logs"} {
		id := s.Add(0, name, 0, 0, 1760000000, index.Directory)
		for i := 0; i < 30; i++ {
			ext := []string{".zip", ".mp4", ".dll", ".png", ".go", ".pdf"}[i%6]
			size := uint64((13-d)*(31-i)) * 4 << 20
			s.Add(id, fmt.Sprintf("%s-%02d%s", strings.ToLower(strings.ReplaceAll(name, " ", "-")), i, ext), size, size, 1760000000, 0)
		}
	}
	s.Finalize()
	s.Engine = "Directory scan"
	s.Elapsed = 183 * time.Millisecond
	a.snapshot = s
	a.summary, _ = s.Summarize(context.Background())
	a.selected = 0
	a.focus = 0
	a.mapFor = 0
	a.open[0] = true
	a.open[1] = true
	a.metric = 1
	a.mapTotal = s.Nodes[0].Allocated
	a.branches = treemap.Hierarchy(context.Background(), s, 0, true, 4096)
	r, _ := s.QueryRows(context.Background(), index.Query{Open: a.open, Sort: index.Sort{Column: "Allocated", Desc: true}})
	a.rows = r.Rows
	a.matches = r.Matches
	save := func(name string, tt *ui.Tester) {
		t.Helper()
		tt.Frame()
		if a.analysisBusy {
			settle(t, a, tt)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err = png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	tt := ui.NewTester(a.view, 1280, 720)
	save("completed-dark", tt)
	a.mapTab = 1
	save("sunburst", tt)
	a.mapTab = 2
	save("file-types-chart", tt)
	a.mapTab = 3
	save("history-empty", tt)
	a.mapTab = 0
	a.typeTab = 0
	save("overview", tt)
	a.typeTab = 2
	save("top-files", tt)
	a.typeTab = 3
	save("duplicates-ready", tt)
	a.typeTab = 1
	a.settingsOpen = true
	save("settings", tt)
	a.settingsOpen = false
	a.advancedOpen = true
	save("advanced-filters", tt)
	a.advancedOpen = false
	tt.SetSize(1000, 600)
	save("minimum-dark", tt)
	a.light = true
	tt.SetSize(1280, 720)
	save("completed-light", tt)
	empty := New(`C:\`)
	empty.drives = nil
	empty.admin = false
	save("empty", ui.NewTester(empty.view, 1280, 720))
	empty.mftBlocked = true
	empty.notice = "MFT scan unavailable: access denied"
	save("mft-unavailable", ui.NewTester(empty.view, 1280, 720))
}
func BenchmarkVirtualizedTable100k(b *testing.B) {
	b.Setenv("STRATUM_CONFIG_DIR", b.TempDir())
	a := New(`C:\bench`)
	a.drives = nil
	s := index.New(a.path)
	for i := 0; i < 100000; i++ {
		s.Add(0, fmt.Sprintf("file-%d.bin", i), 4096, 4096, 0, 0)
	}
	s.Finalize()
	a.snapshot = s
	a.selected = 0
	a.mapFor = 0
	a.branches = treemap.Hierarchy(context.Background(), s, 0, false, 4096)
	a.rows = make([]index.Row, len(s.Nodes))
	for i := range s.Nodes {
		a.rows[i] = index.Row{ID: index.ID(i)}
	}
	tt := ui.NewTester(a.view, 1440, 900)
	tt.Frame()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		tt.Frame()
	}
}
