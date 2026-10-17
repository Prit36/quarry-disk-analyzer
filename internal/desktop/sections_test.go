package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
)

func TestMillionEntryViewportStaysVirtualized(t *testing.T) {
	app := testApp(t)
	snapshot := index.New(`C:\million-entry-fixture`)
	for position := range 1000000 {
		name := "entry.bin"
		if position == 0 {
			name = "first-entry.bin"
		}
		if position == 999999 {
			name = "last-entry.bin"
		}
		snapshot.Add(0, name, uint64(position+1), uint64(position+1), 0, 0)
	}
	snapshot.Finalize()
	app.snapshot = snapshot
	app.rows = make([]index.Row, len(snapshot.Nodes))
	for position := range app.rows {
		app.rows[position] = index.Row{ID: index.ID(position)}
	}
	app.selected, app.selectedRow, app.focus = index.None, -1, 0
	app.branches = nil
	app.cells = nil
	app.mapTotal = snapshot.Nodes[0].Size
	app.analyticsCollapsed = true
	app.mapCollapsed = true
	tester := ui.NewTester(app.view, 1400, 850)
	if len(tester.Texts()) > 1000 || tester.HasText("last-entry.bin") {
		t.Fatal("initial viewport rendered the full dataset")
	}
	app.list.ScrollToEnd()
	tester.Frame()
	if !tester.HasText("last-entry.bin") || tester.HasText("first-entry.bin") || len(tester.Texts()) > 1000 {
		t.Fatal("virtualized end scrolling is incomplete or unbounded")
	}
	if err := tester.Click("last-entry.bin"); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyHome)
	if app.selectedRow != 0 || !tester.HasText("first-entry.bin") {
		t.Fatal("Home did not return the virtualized table to the first row")
	}
	tester.Key(0, ui.KeyEnd)
	if app.selectedRow != len(app.rows)-1 || !tester.HasText("last-entry.bin") {
		t.Fatal("End did not reach the last indexed row")
	}
	t.Logf("million indexed entries; %d rendered text elements at end", len(tester.Texts()))
}

func TestTreemapViewportZoomAndPan(t *testing.T) {
	app := testApp(t)
	tester := ui.NewTester(app.view, 1400, 850)
	settle(t, app, tester)
	before := app.mapWidth
	if err := tester.Click("Zoom in"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.mapZoom <= 1 || app.mapWidth <= before {
		t.Fatal("zoom did not enlarge the visualization viewport")
	}
	bounds, found := tester.Find("Storage treemap")
	if !found {
		t.Fatal("treemap missing")
	}
	tester.Press(bounds.X+bounds.W/2, bounds.Y+bounds.H/2)
	tester.Move(bounds.X+bounds.W/2+60, bounds.Y+bounds.H/2)
	tester.Release(bounds.X+bounds.W/2+60, bounds.Y+bounds.H/2)
	if app.mapPanX == 0 {
		t.Fatal("zoomed treemap cannot pan")
	}
	if err := tester.Click("Visualization root"); err != nil {
		t.Fatal(err)
	}
	settle(t, app, tester)
	if app.mapZoom != 1 || app.mapPanX != 0 || app.mapPanY != 0 {
		t.Fatal("root did not reset viewport")
	}
}

func TestQuickStatisticsRemainReadableAcrossPaneWidths(t *testing.T) {
	app := testApp(t)
	app.snapshot.Nodes[0].Size = 1 << 40
	app.snapshot.Nodes[0].Allocated = 2 << 40
	app.snapshot.Nodes[0].Files = 958519
	app.snapshot.Nodes[0].Dirs = 335524
	tester := ui.NewTester(app.view, 1672, 941)
	settle(t, app, tester)
	if !tester.HasText("(200.0%)") {
		t.Fatal("allocation/logical ratio was incorrectly clamped to volume utilization")
	}
	for _, size := range [][2]int{{1672, 941}, {1386, 842}, {1000, 600}} {
		tester.SetSize(size[0], size[1])
		tester.Frame()
		for _, metric := range []string{"Total size", "Allocated size", "Total files", "Total folders"} {
			bounds, found := tester.Find("Quick stat: " + metric)
			if !found || bounds.W < 60 || bounds.H < 14 || bounds.X+bounds.W > float32(size[0])-12 || bounds.Y+bounds.H > float32(size[1])-38 {
				t.Fatalf("%s clipped at %v: %+v", metric, size, bounds)
			}
		}
		logical, _ := tester.Find("Quick stat: Total size")
		files, _ := tester.Find("Quick stat: Total files")
		if size[0] < 1500 && files.Y <= logical.Y {
			t.Fatalf("narrow quick statistics did not adapt to two rows at %v", size)
		}
	}
}
