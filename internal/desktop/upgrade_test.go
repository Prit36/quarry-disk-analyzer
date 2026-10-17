package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"stratum/internal/platform"
	"stratum/internal/scan"
)

func TestFoundationDPIAndResize(t *testing.T) {
	for _, scale := range []float32{1, 1.25, 1.5, 2} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			app := testApp(t)
			tester := ui.NewTester(app.view, 1400, 850)
			tester.SetScale(scale)
			for _, size := range [][2]int{{1000, 600}, {1400, 850}, {1920, 1080}} {
				tester.SetSize(size[0], size[1])
				tester.Frame()
				for _, label := range []string{"Scan", "Tree view", "Storage treemap"} {
					bounds, found := tester.Find(label)
					if !found || bounds.W <= 0 || bounds.H <= 0 || bounds.X < 0 || bounds.Y < 0 || bounds.X+bounds.W > float32(size[0])+1 || bounds.Y+bounds.H > float32(size[1])+1 {
						t.Fatalf("%s outside %v at scale %v: %+v", label, size, scale, bounds)
					}
				}
			}
		})
	}
}

func TestCoreNavigationAndSummary(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	target := app.path
	app.navigate(1)
	settle(t, app, tester)
	if app.path != target || app.address != app.snapshot.Path(1) {
		t.Fatal("navigation changed scan target")
	}
	app.goBack()
	settle(t, app, tester)
	if app.focus != 0 || app.busy {
		t.Fatal("history triggered scanning")
	}
	app.goForward()
	settle(t, app, tester)
	if app.focus != 1 {
		t.Fatal("forward history failed")
	}
	app.drives = []platform.Drive{{Path: `C:\`, Total: 1000, Free: 250}}
	if drive, ok := app.summaryDrive(); !ok || drive.Total-drive.Free != 750 {
		t.Fatal("invalid capacity semantics")
	}
	app.snapshot.Errors = 1
	tester.Frame()
	if err := tester.Click("1 issue"); err != nil {
		t.Fatal(err)
	}
	if !app.issuesOpen {
		t.Fatal("issue viewer did not open")
	}
	app.issuesOpen = false
	app.snapshot.Cancelled = true
	tester.Frame()
	if !strings.Contains(strings.Join(tester.Texts(), " "), "Scan canceled") {
		t.Fatal("canceled scan displayed as complete")
	}
}

func TestAnalyticsFollowSelection(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	settle(t, app, tester)
	if len(app.analysis.TopFiles) != 5 {
		t.Fatal("global top files missing")
	}
	app.selected = 1
	app.selectedRow = -1
	tester.Frame()
	settle(t, app, tester)
	if app.analysis.Root != 1 || len(app.analysis.TopFiles) != 2 || len(app.analysis.Types) != 2 {
		t.Fatal("selection scope incorrect", app.analysis)
	}
	app.followSelection = false
	tester.Frame()
	settle(t, app, tester)
	if app.analysis.Root != 0 || len(app.analysis.TopFiles) != 5 {
		t.Fatal("global scope not restored")
	}
	app.typeQuery = ".zip"
	app.filterTypes()
	if len(app.visibleTypes) != 1 || app.visibleTypes[0].Count != 2 {
		t.Fatal("extension filtering failed")
	}
	app.typeTab = 2
	tester.Frame()
	if !tester.HasText("Top 100 files · size") {
		t.Fatal("largest-files table missing")
	}
}

func TestTreemapScopeHistoryAndTooltips(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	app.focusMap(1)
	settle(t, app, tester)
	if app.mapRoot() != 1 || app.focus != 0 || app.mapTotal != app.snapshot.Nodes[1].Size {
		t.Fatal("map scope conflated with navigation")
	}
	app.mapBack()
	settle(t, app, tester)
	if app.mapRoot() != 0 {
		t.Fatal("map history failed")
	}
	before := app.tileColor(4)
	app.focusMap(1)
	settle(t, app, tester)
	if before != app.tileColor(4) {
		t.Fatal("color changed on drilldown")
	}
	if !strings.Contains(app.mapTooltip(app.cells[0].Tile), "Allocated:") {
		t.Fatal("tooltip missing exact metadata")
	}
}

func TestPreferencesRoundTripAndCorruption(t *testing.T) {
	app := testApp(t)
	app.divider = 53
	app.columnSplit = 60
	app.light = true
	app.compact = true
	app.units = "Decimal"
	app.list.Columns = ui.TableLayout{Order: []string{"Name", "Allocated", "Size"}, Widths: map[string]float32{"Name": 333}}
	app.hiddenColumns["Modified"] = true
	app.analyticsCollapsed = true
	app.mapCollapsed = true
	app.followSelection = false
	app.showMetadata = false
	app.tab = 1
	app.metric = 1
	app.rememberLocation(`C:\one`)
	app.rememberLocation(`C:\two`)
	app.rememberLocation(`C:\one`)
	app.savePreferences()
	restored := New("")
	if restored.divider != 53 || restored.columnSplit != 60 || !restored.light || !restored.compact || restored.units != "Decimal" || restored.list.Columns.Widths["Name"] != 333 || !restored.hiddenColumns["Modified"] || !restored.mapCollapsed || !restored.analyticsCollapsed || restored.followSelection || restored.showMetadata || restored.tab != 1 || restored.metric != 1 || len(restored.recentLocations) != 2 {
		t.Fatal("preferences did not round-trip", restored)
	}
	if restored.formatBytes(1000000) != "1.00 MB" {
		t.Fatal("decimal formatting incorrect")
	}
	if err := os.WriteFile(filepath.Join(configDirectory(), "preferences.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if fallback := New(""); fallback.divider != defaultDivider || fallback.units != "Binary" {
		t.Fatal("corrupt preferences changed defaults")
	}
}

func TestLegacyDividerMigrationPreservesCustomSplit(t *testing.T) {
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	for _, split := range []float32{62, 53} {
		if err := writePreferences(filepath.Join(configDirectory(), "preferences.json"), preferences{Version: 1, Divider: split, ColumnSplit: 68}); err != nil {
			t.Fatal(err)
		}
		restored := New("")
		want := split
		if split == 62 {
			want = defaultDivider
		}
		if restored.divider != want {
			t.Fatalf("legacy divider %v restored as %v; want %v", split, restored.divider, want)
		}
	}
}

func TestKeyboardEditingAndPanelCollapse(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	settle(t, app, tester)
	tester.Key(ui.Cmd, ui.KeyL)
	if !tester.Focused("Scan path") {
		t.Fatal("Ctrl+L did not focus address")
	}
	tester.Type("replacement")
	if app.address != "replacement" {
		t.Fatal("Ctrl+L did not select address", app.address)
	}
	tester.Key(ui.Cmd, ui.KeyF)
	if !tester.Focused("Search files") {
		t.Fatal("Ctrl+F did not focus filter")
	}
	tester.Type("zip")
	tester.Key(0, ui.KeyEscape)
	if app.query != "" {
		t.Fatal("Escape did not clear query")
	}
	app.analyticsCollapsed = true
	app.mapCollapsed = true
	tester.Frame()
	if !tester.HasText("Tree view") {
		t.Fatal("collapse lost explorer")
	}
	if _, found := tester.Find("Storage treemap"); found {
		t.Fatal("map collapse failed")
	}
}

func TestAdvancedViewsHistoryAndImageExport(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	settle(t, app, tester)
	app.mapTab = 1
	tester.Frame()
	if _, found := tester.Find("Storage sunburst"); !found || len(app.sunPaths) == 0 {
		t.Fatal("sunburst did not render")
	}
	app.mapTab = 2
	tester.Frame()
	if !tester.HasText("Share of visualization scope") {
		t.Fatal("file type chart missing")
	}
	app.saveSnapshot()
	settle(t, app, tester)
	app.refreshHistory()
	settle(t, app, tester)
	if len(app.savedScans) != 1 {
		t.Fatal("saved scan missing", app.notice)
	}
	app.compareSnapshot(app.savedScans[0].Path)
	settle(t, app, tester)
	if app.comparisonLabel == "" || len(app.changes) == 0 {
		t.Fatal("comparison missing", app.notice)
	}
	previous := app.snapshot
	app.restoreSnapshot(app.savedScans[0].Path)
	settle(t, app, tester)
	if app.snapshot == previous || app.snapshot.Nodes[0].Size != previous.Nodes[0].Size || app.busy {
		t.Fatal("restore rescanned or lost data")
	}
	var buffer bytes.Buffer
	if err := app.exportTreemapPNG(&buffer); err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(&buffer)
	if err != nil || image.Bounds().Dx() != 1600 {
		t.Fatal("PNG export invalid", err)
	}
	tester.SetSize(1000, 600)
	app.settingsOpen = true
	tester.Frame()
	if bounds, found := tester.Find("Done"); !found || bounds.Y+bounds.H > 600 {
		t.Fatal("settings clipped at minimum size", bounds)
	}
	app.settingsOpen = false
	app.advancedOpen = true
	tester.Frame()
	if bounds, found := tester.Find("Apply filters"); !found || bounds.Y+bounds.H > 600 {
		t.Fatal("advanced filters clipped", bounds)
	}
}

func TestNativeDuplicateActionOnRealFiles(t *testing.T) {
	app := testApp(t)
	directory := t.TempDir()
	for _, name := range []string{"one.bin", "two.bin"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("same bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := scan.Run(context.Background(), directory, scan.Options{ForceWalk: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.events <- event{kind: "scan", snapshot: snapshot}
	tester := ui.NewTester(app.view, 1400, 850)
	settle(t, app, tester)
	app.typeTab = 3
	tester.Frame()
	if err := tester.Click("Check content"); err != nil {
		t.Fatal(err)
	}
	settle(t, app, tester)
	if len(app.duplicateResult.Groups) != 1 || len(app.duplicateResult.Groups[0].IDs) != 2 {
		t.Fatal("native duplicate action failed", app.notice, app.duplicateResult)
	}
}

func TestAddressNavigationAfterCommittedSelection(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	app.requestRows()
	settle(t, app, tester)
	if app.selectedRow != 0 {
		t.Fatal("initial selection not committed")
	}
	tester.Key(ui.Cmd, ui.KeyL)
	tester.Type(app.snapshot.Path(2))
	tester.Key(0, ui.KeyEnter)
	settle(t, app, tester)
	if app.selected != 2 || app.analysis.Root != 2 || app.selectedRow != 0 {
		t.Fatal("old table selection replaced navigation selection", app.selected, app.analysis.Root)
	}
}

func TestMapSelectionPreservesCurrentNavigation(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	app.navigate(1)
	settle(t, app, tester)
	app.selectMap(4)
	settle(t, app, tester)
	if app.focus != 1 || app.address != app.snapshot.Path(1) || app.selected != 4 {
		t.Fatal("map selection changed inspection location", app.focus, app.address, app.selected)
	}
}

func TestSearchShortcutFromDetails(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	app.tab = 2
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	if app.tab != 0 || !tester.Focused("Search files") {
		t.Fatal("Ctrl+F did not restore and focus the search field")
	}
}
