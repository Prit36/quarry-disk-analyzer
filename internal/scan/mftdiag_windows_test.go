//go:build windows

package scan

import (
	"context"
	"math/bits"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMFTDiagnostics compares MFT read settings on a real volume, as the
// elevated benchmark does: QUARRY_MFT_DIAG=C:\ go test -run TestMFTDiagnostics -v
func TestMFTDiagnostics(t *testing.T) {
	root := os.Getenv("QUARRY_MFT_DIAG")
	if root == "" {
		t.Skip("QUARRY_MFT_DIAG not set")
	}
	defer func(chunk int64, readers int) { mftChunkBytes, mftReaders = chunk, readers }(mftChunkBytes, mftReaders)
	for _, c := range []struct {
		readers int
		chunk   int64
	}{{1, 8 << 20}, {2, 4 << 20}, {2, 8 << 20}, {3, 4 << 20}, {4, 1 << 20}, {4, 4 << 20}, {2, 2 << 20}, {1, 4 << 20}} {
		mftReaders, mftChunkBytes = c.readers, c.chunk
		best := time.Duration(1 << 62)
		var nodes, capacity int
		var unresolved uint64
		var bitmap, used int
		for range 3 {
			v, err := openVolume(root)
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			start := time.Now()
			s, err := readMFT(context.Background(), root, &Progress{}, v)
			elapsed := time.Since(start)
			v.f.Close()
			if err != nil {
				t.Fatal(err)
			}
			best = min(best, elapsed)
			nodes, capacity, unresolved = len(s.Nodes), cap(s.Nodes), s.Unresolved
			bitmap, used = len(v.bitmap), 0
			for _, b := range v.bitmap {
				used += bits.OnesCount8(b)
			}
		}
		t.Logf("%d readers × %2d MiB: %v  (nodes %d, capacity %d, unresolved %d, bitmap %d bytes, %d in use)", c.readers, c.chunk>>20, best.Round(time.Millisecond), nodes, capacity, unresolved, bitmap, used)
	}
}
