package desktop

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/platform"
)

func TestStartupFrameWithoutShellOrVolumes(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newAppState()
	a.prefs.Theme = "Dark"
	if a.drivesReady || len(a.drives) != 0 || len(a.shellIcons) != 0 {
		t.Fatal("startup performed shell or volume lookups")
	}
	tt := ui.NewTester(a.view, 1280, 800)
	if !tt.HasText("Loading drives…") {
		t.Fatal("initial frame did not show the loading state")
	}
	if a.scanning {
		t.Fatal("startup began an unrequested scan")
	}
	a.setDrives([]platform.Drive{{Path: `C:\`, Name: "Test drive", Total: 100, Free: 50}})
	tt.Frame()
	if tt.HasText("Loading drives…") || !tt.HasText("Test drive (C:)") {
		t.Fatal("drive completion did not update the initial frame")
	}
}

func TestStartupDriveResultPreservesInputAndRefreshesMenus(t *testing.T) {
	a := &App{target: `D:\chosen`, menu: &menuBar{}}
	a.setDrives([]platform.Drive{{Path: `C:\`}})
	if a.target != `D:\chosen` || !a.menu.recentChanged {
		t.Fatal("late drive discovery replaced input or left the menu stale")
	}
	a.setDrives(nil)
	if !a.drivesReady || len(a.drives) != 0 {
		t.Fatal("empty drive result retained a selection")
	}
}

func TestStartupLookupsCompleteWithoutStartingScan(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newAppState()
	a.loadStartupInfo()
	a.loadStartupInfo() // Repeated readiness must not start another pair of workers.
	deadline := time.After(5 * time.Second)
	for range 2 {
		select {
		case fn := <-a.queue:
			fn()
		case <-deadline:
			t.Fatal("startup lookup did not finish")
		}
	}
	if !a.drivesReady || a.shellIcons == nil || a.scanning {
		t.Fatal("startup results were not applied")
	}
	select {
	case <-a.queue:
		t.Fatal("startup lookups ran more than once")
	default:
	}
}

func TestStartupInvalidScanCompletesOnWorker(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newAppState()
	a.target = t.TempDir() + `/missing`
	a.startScan()
	if !a.scanning || a.progress == nil {
		t.Fatal("scan validation did not start on the worker")
	}
	select {
	case fn := <-a.queue:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("invalid scan did not finish")
	}
	if a.scanning || !a.errorMsg || a.snap != nil {
		t.Fatal("invalid target was not reported safely")
	}
}
