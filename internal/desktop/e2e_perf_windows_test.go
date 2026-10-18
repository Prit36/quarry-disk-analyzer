package desktop

import (
	"encoding/json"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/egoist/mygo/ui"
	"golang.org/x/sys/windows"
)

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	cb                                                 uint32
	PageFaultCount                                     uint32
	PeakWorkingSetSize, WorkingSetSize                 uintptr
	QuotaPeakPagedPoolUsage, QuotaPagedPoolUsage       uintptr
	QuotaPeakNonPagedPoolUsage, QuotaNonPagedPoolUsage uintptr
	PagefileUsage, PeakPagefileUsage                   uintptr
}

// TestScanToDisplay measures a real scan from the Scan command until the
// first frame that shows the results, treemap included:
//
//	QUARRY_E2E_ROOT=C:\ go test ./internal/desktop -run TestScanToDisplay -v
//
// QUARRY_E2E_WALK=1 forces the directory scan. Frames are headless.
func TestScanToDisplay(t *testing.T) {
	root := os.Getenv("QUARRY_E2E_ROOT")
	if root == "" {
		t.Skip("set QUARRY_E2E_ROOT")
	}
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	a := newApp()
	testedApp = a
	a.prefs.Theme = "Dark"
	a.prefs.ShowTreemap = true
	a.prefs.UseExclusions = false
	if os.Getenv("QUARRY_E2E_WALK") == "1" {
		a.prefs.ScanMode = "Enumerate"
	}
	tt := ui.NewTester(a.view, 1600, 1000)
	tt.Frame()

	var peakHeap atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			runtime.ReadMemStats(&m)
			if v := m.HeapInuse; v > peakHeap.Load() {
				peakHeap.Store(v)
			}
		}
	}()

	if a.menu == nil {
		a.menu = &menuBar{}
	}
	a.target = root
	start := time.Now()
	a.startScan()
	var scanned time.Duration
	for i := 0; ; i++ {
		if !a.drain() {
			time.Sleep(200 * time.Microsecond)
		}
		if scanned == 0 && a.snap != nil {
			scanned = time.Since(start)
		}
		if a.snap != nil && !a.filterBusy {
			tt.Frame()
			if !a.tm.busy && len(a.tm.cells) > 0 {
				break
			}
		}
		if time.Since(start) > 10*time.Minute {
			t.Fatal("timeout")
		}
	}
	total := time.Since(start)
	close(stop)

	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	getProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
	report := map[string]any{
		"root":                 root,
		"engine":               a.base.Engine,
		"nodes":                len(a.base.Nodes),
		"scan_elapsed_ms":      ms(a.base.Elapsed),
		"results_installed_ms": ms(scanned),
		"first_full_frame_ms":  ms(total),
		"treemap_cells":        len(a.tm.cells),
		"peak_heap_inuse_mb":   float64(peakHeap.Load()) / (1 << 20),
		"peak_working_set_mb":  float64(pmc.PeakWorkingSetSize) / (1 << 20),
		"working_set_mb":       float64(pmc.WorkingSetSize) / (1 << 20),
	}
	out, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(out))
	if p := os.Getenv("QUARRY_PERF_OUT"); p != "" {
		os.WriteFile(p, out, 0o644)
	}
}
