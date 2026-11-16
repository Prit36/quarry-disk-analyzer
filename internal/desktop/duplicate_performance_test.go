package desktop

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"quarry/internal/index"
)

// Includes grouping, the UI-thread handoff, membership indexing, file totals,
// and ordering the default Files view. Excludes scan/loading and GPU frames.
func BenchmarkDuplicateFilesReady(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	for _, source := range []string{"Synthetic1M", "SavedScan"} {
		b.Run(source, func(b *testing.B) {
			var s *index.Snapshot
			if source == "SavedScan" {
				path := os.Getenv("QUARRY_DUP_BENCH_SNAPSHOT")
				if path == "" {
					b.Skip("set QUARRY_DUP_BENCH_SNAPSHOT")
				}
				var err error
				s, err = index.LoadSnapshot(context.Background(), path)
				if err != nil {
					b.Fatal(err)
				}
			} else {
				s = index.New(`C:\duplicate-performance`)
				var parent index.ID
				for i := range 1000000 {
					if i%500 == 0 {
						parent = s.Add(0, fmt.Sprintf("folder-%d", i/500), 0, 0, 0, index.Directory)
					}
					key := i
					if i%10 == 1 {
						key--
					}
					size := uint64(key)*7919%((1<<27)-1) + 1
					s.Add(parent, fmt.Sprintf("File-%08d.LOG", key), size, size, 0, 0)
				}
				s.Finalize()
			}
			a := newAppState()
			a.snap, a.base = s, s
			a.prefs.View, a.prefs.ShowTreemap = viewFiles, false
			b.Cleanup(a.shutdown)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				a.refreshDuplicates(false)
				timeout := time.NewTimer(30 * time.Second)
				for a.duplicates.busy || a.files.busy || a.files.forSnap != s {
					select {
					case apply := <-a.queue:
						apply()
					case <-timeout.C:
						b.Fatal("duplicate results timed out")
					}
				}
				timeout.Stop()
				if a.duplicates.err != "" || a.files.err != "" || a.duplicates.verify {
					b.Fatal("duplicate view failed", a.duplicates.err, a.files.err)
				}
				if source == "Synthetic1M" && len(a.files.ids) != 200000 {
					b.Fatalf("missing duplicate rows: %d", len(a.files.ids))
				}
			}
		})
	}
}
