package index

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Benchmarks for the saving, loading and export paths on a real saved scan.
// Set QUARRY_BENCH_SNAPSHOT to a .quarry file; they skip otherwise.
func maxBenchSnapshot(tb testing.TB) (*Snapshot, string) {
	tb.Helper()
	path := os.Getenv("QUARRY_BENCH_SNAPSHOT")
	if path == "" {
		tb.Skip("set QUARRY_BENCH_SNAPSHOT")
	}
	s, err := LoadSnapshot(context.Background(), path)
	if err != nil {
		tb.Fatal(err)
	}
	return s, path
}

func BenchmarkMaxLoad(b *testing.B) {
	_, path := maxBenchSnapshot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := LoadSnapshot(context.Background(), path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMaxCompact(b *testing.B) {
	s, _ := maxBenchSnapshot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Compact()
	}
}

func BenchmarkMaxExportCSV(b *testing.B) {
	s, _ := maxBenchSnapshot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.Export(context.Background(), io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMaxExportRowsCSV(b *testing.B) {
	s, _ := maxBenchSnapshot(b)
	rows := make([]Row, 0, 200000)
	for i := len(s.Nodes) - 1; i > 0 && len(rows) < cap(rows); i -= 3 {
		rows = append(rows, Row{ID: ID(i)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.ExportRows(context.Background(), io.Discard, rows); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMaxExportJSON(b *testing.B) {
	s, _ := maxBenchSnapshot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.ExportJSON(context.Background(), io.Discard, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// TestMaxParityReport prints digests of every output these benchmarks cover,
// so builds before and after a change can be compared on the same scan.
func TestMaxParityReport(t *testing.T) {
	s, _ := maxBenchSnapshot(t)
	ctx := context.Background()
	digest := func(write func(io.Writer) error) string {
		h := sha256.New()
		if err := write(h); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	rows := make([]Row, 0, 200000)
	for i := len(s.Nodes) - 1; i > 0 && len(rows) < cap(rows); i -= 3 {
		rows = append(rows, Row{ID: ID(i)})
	}
	c := s.Compact()
	saved := filepath.Join(t.TempDir(), "compact.quarry")
	if err := c.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSnapshot(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"nodes":       len(s.Nodes),
		"csv":         digest(func(w io.Writer) error { return s.Export(ctx, w) }),
		"csv_scope":   digest(func(w io.Writer) error { return s.ExportScope(ctx, w, s.Nodes[0].FirstChild) }),
		"csv_rows":    digest(func(w io.Writer) error { return s.ExportRows(ctx, w, rows) }),
		"json":        digest(func(w io.Writer) error { return s.ExportJSON(ctx, w, 0) }),
		"compact_csv": digest(func(w io.Writer) error { return c.Export(ctx, w) }),
		"loaded_csv":  digest(func(w io.Writer) error { return loaded.Export(ctx, w) }),
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(data))
	if out := os.Getenv("QUARRY_PARITY_OUT"); out != "" {
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMaxLoadOnce times one load in a fresh process, as the application
// loads a scan, and reports how much memory the Go runtime obtained.
// QUARRY_LOAD_READER=heap reads the file with os.ReadFile instead.
func TestMaxLoadOnce(t *testing.T) {
	path := os.Getenv("QUARRY_BENCH_SNAPSHOT")
	if path == "" || os.Getenv("QUARRY_LOAD_ONCE") == "" {
		t.Skip("set QUARRY_BENCH_SNAPSHOT and QUARRY_LOAD_ONCE")
	}
	start := time.Now()
	var s *Snapshot
	var err error
	if os.Getenv("QUARRY_LOAD_READER") == "heap" {
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			s, err = loadSnapshot(context.Background(), data, true)
		}
	} else {
		s, err = LoadSnapshot(context.Background(), path)
	}
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("load_ms=%.2f heap_sys_mb=%.1f total_alloc_mb=%.1f gc=%d nodes=%d\n", float64(elapsed.Microseconds())/1000, float64(m.HeapSys)/1e6, float64(m.TotalAlloc)/1e6, m.NumGC, len(s.Nodes))
}
