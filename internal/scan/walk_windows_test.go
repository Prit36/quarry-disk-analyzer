//go:build windows

package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func handleCount(t *testing.T) int {
	var n uint32
	if ok, _, err := getProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n))); ok == 0 {
		t.Fatal(err)
	}
	return int(n)
}

// TestWalkClosesFolders checks that the folder handles subfolders are
// opened from are all closed, after complete and after stopped scans.
func TestWalkClosesFolders(t *testing.T) {
	root := t.TempDir()
	for i := range 40 {
		dir := filepath.Join(root, fmt.Sprint("a", i), "b", "c")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "x.txt"), nil, 0600)
	}
	scans := func(n int) {
		for i := range n {
			ctx, cancel := context.WithCancel(context.Background())
			if i%2 == 1 {
				// Stop part way through.
				go func() { time.Sleep(time.Duration(i%20) * 50 * time.Microsecond); cancel() }()
			}
			if _, err := Run(ctx, root, Options{ForceWalk: true, Workers: 4}, nil); err != nil {
				t.Fatal(err)
			}
			cancel()
		}
	}
	// The runtime adds threads (each a handle) as it needs them: those level
	// off, where a leak grows with every scan.
	scans(100)
	before := handleCount(t)
	scans(200)
	if after := handleCount(t); after > before+8 {
		t.Fatalf("handles leaked: %d before, %d after 200 more scans", before, after)
	}
}
