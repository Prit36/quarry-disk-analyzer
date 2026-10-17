package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
	"stratum/internal/treemap"
)

func nestedDetailApp(t *testing.T) *App {
	t.Helper()
	app := testApp(t)
	snapshot := index.New(`C:\`)
	snapshot.Engine = "NTFS MFT"
	snapshot.Add(0, "$BadClus", 700<<30, 700<<30, 0, 0)
	addFolder := func(parent index.ID, name string, sizes []uint64) index.ID {
		folder := snapshot.Add(parent, name, 0, 0, 0, index.Directory)
		for position, size := range sizes {
			snapshot.Add(folder, fmt.Sprintf("file-%02d.bin", position), size<<30, size<<30, 0, 0)
		}
		return folder
	}
	users := addFolder(0, "Users", nil)
	home := addFolder(users, "My Home", nil)
	addFolder(home, "Downloads", []uint64{45, 18, 9, 5, 3})
	addFolder(home, "Pictures", []uint64{28, 12, 6, 4})
	addFolder(home, "Projects", []uint64{16, 8, 4, 2})
	addFolder(home, "Documents", []uint64{10, 5, 3, 2})
	addFolder(users, "Shared", []uint64{20, 10})
	programs := addFolder(0, "Program Files (x86)", nil)
	addFolder(programs, "Steam", []uint64{45, 24, 12, 6, 3})
	addFolder(programs, "Tools", []uint64{18, 10, 7, 5})
	addFolder(programs, "Browsers", []uint64{9, 5, 3, 1, 1, 1})
	addFolder(0, "Windows", []uint64{20, 15, 10, 6, 4, 3, 2})
	addFolder(0, "ProgramData", []uint64{9, 7, 5, 4, 3, 2})
	snapshot.Finalize()
	app.snapshot, app.focus, app.selected, app.selectedRow = snapshot, 0, index.None, -1
	app.path, app.address = snapshot.Root, snapshot.Root
	app.open = map[index.ID]bool{0: true}
	app.branches = treemap.Hierarchy(context.Background(), snapshot, 0, false, 4096)
	app.mapFor, app.mapTotal = 0, snapshot.Nodes[0].Size
	app.sort = ui.SortOrder{Column: "Size", Descending: true}
	result, err := snapshot.QueryRows(context.Background(), index.Query{Root: 0, Open: app.open, Sort: index.Sort{Column: "Size", Desc: true}})
	if err != nil {
		t.Fatal(err)
	}
	app.rows = result.Rows
	return app
}

func pixelDifference(first, second color.Color) float64 {
	firstRed, firstGreen, firstBlue, _ := first.RGBA()
	secondRed, secondGreen, secondBlue, _ := second.RGBA()
	return (math.Abs(float64(firstRed)-float64(secondRed)) + math.Abs(float64(firstGreen)-float64(secondGreen)) + math.Abs(float64(firstBlue)-float64(secondBlue))) / 257
}

func TestNestedTileEdgesAndSelection(t *testing.T) {
	app := nestedDetailApp(t)
	tester := ui.NewTester(app.view, 1672, 941)
	settle(t, app, tester)
	origin, found := tester.Find("Storage treemap")
	if !found {
		t.Fatal("treemap not rendered")
	}
	for _, cell := range app.cells {
		bounds := cell.Tile.Rect
		if cell.Depth == 0 || cell.Group || bounds.W < 15 || bounds.H < 8 || bounds.H > 25 {
			continue
		}
		centerX, centerY := origin.X+bounds.X+bounds.W/2, origin.Y+bounds.Y+bounds.H/2
		bottom := int(origin.Y + bounds.Y + bounds.H)
		bitmap := tester.Image()
		// The tile's outline is a one-pixel run at its bottom edge, and
		// fractional layout coordinates move it between the last rows of
		// the tile's box.
		difference := float64(0)
		for row := bottom - 2; row <= bottom; row++ {
			if candidate := pixelDifference(bitmap.At(int(centerX), int(centerY)), bitmap.At(int(centerX), row)); candidate > difference {
				difference = candidate
			}
		}
		if difference < 40 {
			t.Fatalf("nested tile edge still blends into its fill: difference %.1f", difference)
		}
		before := app.tileShade(cell.Tile.ID, false)
		tester.ClickAt(centerX, centerY)
		settle(t, app, tester)
		if app.selected != cell.Tile.ID {
			t.Fatal("nested tile selection no longer targets its indexed file", app.selected, cell.Tile.ID)
		}
		app.focusMap(app.snapshot.Nodes[cell.Tile.ID].Parent)
		settle(t, app, tester)
		if app.tileShade(cell.Tile.ID, false) != before {
			t.Fatal("nested shade changed during drilldown")
		}
		return
	}
	t.Fatal("fixture did not exercise small nested tiles")
}

func TestExportedNestedTileEdges(t *testing.T) {
	app := nestedDetailApp(t)
	var buffer bytes.Buffer
	if err := app.exportTreemapPNG(&buffer); err != nil {
		t.Fatal(err)
	}
	bitmap, err := png.Decode(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range treemap.NestedLayout(app.branches, treemap.Rect{X: 8, Y: 36, W: 1584, H: 856}) {
		bounds := cell.Tile.Rect
		if cell.Depth == 0 || cell.Group || bounds.W < 20 || bounds.H < 20 {
			continue
		}
		centerX := int(bounds.X + bounds.W/2)
		edgeY := int(bounds.Y+bounds.H) - 2
		if pixelDifference(bitmap.At(centerX, edgeY-3), bitmap.At(centerX, edgeY)) < 40 {
			t.Fatal("PNG export lost nested tile outlines")
		}
		return
	}
	t.Fatal("export fixture has no nested tiles")
}

func TestFileTypeVisualizationBarsHaveUsableWidth(t *testing.T) {
	app := nestedDetailApp(t)
	tester := ui.NewTester(app.view, 1672, 941)
	settle(t, app, tester)
	app.mapTab = 2
	app.mapTypes = app.analysis.Types
	for _, width := range []int{1672, 1000} {
		tester.SetSize(width, 941)
		tester.Frame()
		bounds, found := tester.Find("Visualization share: .bin")
		if !found || bounds.W < 100 || bounds.H != 10 {
			t.Fatalf("file-type bar collapsed at width %d: %+v", width, bounds)
		}
	}
}

func TestMinimumSettingsActionsStayInsideDialog(t *testing.T) {
	app := nestedDetailApp(t)
	app.admin = false
	app.settingsOpen = true
	tester := ui.NewTester(app.view, 1000, 600)
	for _, light := range []bool{false, true} {
		app.light = light
		tester.Frame()
		for _, label := range []string{"Settings", "Run as administrator", "Done"} {
			bounds, found := tester.Find(label)
			if !found || bounds.Y < 32 || bounds.Y+bounds.H > 568 {
				t.Fatalf("settings action %s escaped the padded dialog (light=%v): %+v", label, light, bounds)
			}
		}
	}
}

func TestZoomedTilesDoNotPaintOverToolbar(t *testing.T) {
	app := nestedDetailApp(t)
	tester := ui.NewTester(app.view, 1672, 941)
	settle(t, app, tester)
	bounds, found := tester.Find("Treemap")
	if !found {
		t.Fatal("treemap tab missing")
	}
	positionX, positionY := int(bounds.X+4), int(bounds.Y+bounds.H/2)
	before := tester.Image().At(positionX, positionY)
	if err := tester.Click("Zoom in"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if app.mapZoom <= 1 {
		t.Fatal("zoom action did not run")
	}
	if difference := pixelDifference(before, tester.Image().At(positionX, positionY)); difference > 1 {
		t.Fatalf("enlarged treemap painted over the toolbar: difference %.1f", difference)
	}
}

func TestNestedVisibilityEvidence(t *testing.T) {
	directory := os.Getenv("STRATUM_POLISH_DIR")
	if directory == "" {
		t.Skip("nested visibility capture is opt-in")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	app := nestedDetailApp(t)
	tester := ui.NewTester(app.view, 1672, 941)
	settle(t, app, tester)
	save := func(name string) {
		tester.Frame()
		file, err := os.Create(filepath.Join(directory, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, tester.Image())
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	save("nested-synthetic")
	app.mapTab = 1
	save("nested-sunburst")
	app.mapTab = 2
	app.mapTypes = app.analysis.Types
	save("file-type-bars")
	app.mapTab = 0
	app.mapZoom = 2
	save("nested-zoomed")
	app.mapZoom = 1
	app.light = true
	save("nested-light")
	tester.SetSize(1000, 600)
	app.settingsOpen = true
	save("minimum-settings")
	app.settingsOpen = false
	app.advancedOpen = true
	save("minimum-filters")
	app.advancedOpen = false
	file, err := os.Create(filepath.Join(directory, "nested-export.png"))
	if err != nil {
		t.Fatal(err)
	}
	err = app.exportTreemapPNG(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}
