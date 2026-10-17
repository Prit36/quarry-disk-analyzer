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
	"stratum/internal/platform"
	"stratum/internal/treemap"
)

func TestReferenceMatchEvidence(t *testing.T) {
	directory := os.Getenv("STRATUM_REFERENCE_DIR")
	if directory == "" {
		t.Skip("reference visual capture is opt-in")
	}
	t.Setenv("STRATUM_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	app := testApp(t)
	snapshot := index.New(`C:\`)
	for position, group := range []struct {
		name   string
		gib    float64
		folder bool
	}{{"$BadClus", 700, false}, {"Users", 175, true}, {"Program Files (x86)", 145, true}, {"Windows", 72, true}, {"Program Files", 70, true}, {"System Volume Information", 60, true}, {"ProgramData", 45, true}, {"pagefile.sys", 2, false}, {"$MFT", 1.39, false}, {"Games", .837, true}, {"msedge.dll", .251, false}, {"Locales", .115, true}, {"BucketConfidenceData.cab", .096, false}, {"$Extend", 14.13, true}} {
		size := uint64(group.gib * float64(uint64(1)<<30))
		flags := uint16(0)
		if group.folder {
			flags = index.Directory
		}
		id := snapshot.Add(0, group.name, 0, 0, 1753523760, flags)
		if !group.folder {
			snapshot.Nodes[id].Size = size
			snapshot.Nodes[id].Allocated = size
			continue
		}
		remaining := size
		for child := range 30 {
			part := remaining / uint64(30-child)
			remaining -= part
			extension := []string{".dat", ".dll", ".resources", ".rpack", ".exe", ".img", ".jpg", ".bmp", ".pdb", ".zip", ".mp4"}[(position+child)%11]
			if child < 8 {
				nested := snapshot.Add(id, fmt.Sprintf("Folder %02d", child), 0, 0, 1753523760, index.Directory)
				snapshot.Add(nested, "data"+extension, part, part, 1753523760, 0)
			} else {
				snapshot.Add(id, fmt.Sprintf("file-%02d%s", child, extension), part, part, 1753523760, 0)
			}
		}
	}
	snapshot.Engine = "NTFS MFT"
	snapshot.Finalize()
	snapshot.Started = time.Date(2025, 7, 26, 13, 26, 0, 0, time.Local)
	snapshot.Elapsed = 72 * time.Second
	app.snapshot = snapshot
	app.path = snapshot.Root
	app.address = snapshot.Root
	app.focus = 0
	app.selected = 0
	app.selectedRow = 0
	app.open = map[index.ID]bool{0: true}
	app.mapFor = 0
	app.metric = 0
	app.sort = ui.SortOrder{Column: "Size", Descending: true}
	app.divider = 71.5
	app.columnSplit = 68
	app.drives = []platform.Drive{{Path: `C:\`, Name: "Local Disk", FileSystem: "NTFS", Total: 2 << 40, Free: 800 << 30}}
	app.admin = true
	result, err := snapshot.QueryRows(context.Background(), index.Query{Root: 0, Open: app.open, Sort: index.Sort{Column: "Size", Desc: true}})
	if err != nil {
		t.Fatal(err)
	}
	app.rows = result.Rows
	app.branches = treemap.Hierarchy(context.Background(), snapshot, 0, false, 4096)
	app.mapTotal = 0
	for _, branch := range app.branches {
		app.mapTotal += branch.Item.Bytes
	}
	tester := ui.NewTester(app.view, 1672, 941)
	tester.SetTitleBar(ui.TitleBar{Height: 42, Right: 138})
	settle(t, app, tester)
	for _, family := range ui.FontFamilies() {
		if strings.Contains(family.Name, "Segoe") {
			t.Log("available font:", family.Name)
		}
	}
	save := func(name string) {
		tester.Frame()
		if app.analysisBusy || app.queryBusy || app.mapBusy {
			settle(t, app, tester)
		}
		file, err := os.Create(filepath.Join(directory, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, tester.Image())
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	save("reference-desktop")
	tester.SetSize(1000, 600)
	save("reference-minimum")
	tester.SetSize(1386, 842)
	save("reference-intermediate")
	tester.SetSize(1672, 941)
	app.light = true
	save("reference-light")
}
