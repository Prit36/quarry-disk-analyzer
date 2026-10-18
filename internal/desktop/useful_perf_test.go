package desktop

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func realPerfSnapshot(b *testing.B) *index.Snapshot {
	b.Helper()
	path := os.Getenv("QUARRY_PERF_SNAPSHOT")
	if path == "" {
		b.Skip("QUARRY_PERF_SNAPSHOT not set")
	}
	s, err := index.LoadSnapshot(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkRealSorts(b *testing.B) {
	s := realPerfSnapshot(b)
	f := index.Filter{Kind: index.KindFiles}
	base, err := s.Search(context.Background(), 0, &f)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]index.ID, len(base))
	for _, column := range []string{"Name", "Folder", "Size", "Modified", "Extension"} {
		b.Run(column, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				copy(ids, base)
				if err := sortEntries(context.Background(), s, ids, ui.SortOrder{Column: column}, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRealFileQueries(b *testing.B) {
	s := realPerfSnapshot(b)
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	for _, query := range []string{"", "*.dll", "microsoft", "path:system32 ext:dll", "type:folder", `path:C:\Windows\System32`, `type:file path:C:\Windows ext:dll`} {
		b.Run(query, func(b *testing.B) {
			a := newApp()
			a.snap, a.search = s, query
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				a.files.forSnap = nil // measure a new query, not a resort
				a.files.run()
				for a.files.busy {
					a.drain()
					runtime.Gosched()
				}
				if a.files.err != "" {
					b.Fatal(a.files.err)
				}
			}
		})
	}
}

func BenchmarkRealExtensions(b *testing.B) {
	s := realPerfSnapshot(b)
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.snap = s
	b.Cleanup(func() {
		if a.ext.cancel != nil {
			a.ext.cancel()
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		a.ext.run()
		deadline := time.Now().Add(10 * time.Second)
		for a.ext.busy {
			if time.Now().After(deadline) {
				b.Fatal("extension query timed out")
			}
			a.drain()
			runtime.Gosched()
		}
		if a.ext.err != "" {
			b.Fatal(a.ext.err)
		}
		// Measure grouping/application; shell type descriptions are a separate
		// asynchronous stage and are canceled before the next iteration.
		if a.ext.cancel != nil {
			a.ext.cancel()
		}
	}
}

func BenchmarkRealSelectionFacts(b *testing.B) {
	s := realPerfSnapshot(b)
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.snap = s
	a.prefs.View = viewFiles
	a.files.run()
	for a.files.busy {
		a.drain()
		runtime.Gosched()
	}
	a.files.selectAll()
	a.selected()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		a.selFacts.ok = false
		a.facts()
	}
}

// Clicking the active heading changes direction while retaining the same
// query, scope and metric. Include result application and selection retention.
func BenchmarkRealSortToggle(b *testing.B) {
	s := realPerfSnapshot(b)
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	for _, column := range []string{"Name", "Size", "Modified", "Folder", "Extension"} {
		b.Run(column, func(b *testing.B) {
			a := newApp()
			a.snap = s
			a.files.sort = ui.SortOrder{Column: column}
			wait := func() {
				for a.files.busy {
					a.drain()
					runtime.Gosched()
				}
				if a.files.err != "" {
					b.Fatal(a.files.err)
				}
			}
			a.files.run()
			wait()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				a.files.sort.Descending = !a.files.sort.Descending
				a.files.run()
				wait()
			}
		})
	}
}

func BenchmarkRealAnalysisFilters(b *testing.B) {
	s := realPerfSnapshot(b)
	for _, f := range []analysisFilter{
		{Include: "ext:dll"},
		{Include: "path:system32 ext:dll"},
		{Exclude: "node_modules;*.tmp"},
		{Include: "no-such-quarry-file-982731"},
	} {
		b.Run(f.summary(), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, _, err := f.apply(context.Background(), s); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
