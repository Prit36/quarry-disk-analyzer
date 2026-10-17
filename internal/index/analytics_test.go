package index

import (
	"context"
	"fmt"
	"testing"
)

func TestScopedAnalyticsAndBoundedTopFiles(t *testing.T) {
	snapshot := New("fixture")
	folder := snapshot.Add(0, "scope", 0, 0, 0, Directory)
	for number := 1; number <= 1200; number++ {
		snapshot.Add(folder, fmt.Sprintf("%d.bin", number), uint64(number), uint64(1201-number), 0, 0)
	}
	snapshot.Add(0, "outside.txt", 1<<30, 1<<30, 0, 0)
	snapshot.Finalize()
	for _, allocated := range []bool{false, true} {
		analysis, err := snapshot.Analyze(context.Background(), folder, allocated, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(analysis.Types) != 1 || analysis.Types[0].Count != 1200 || len(analysis.TopFiles) != 100 {
			t.Fatalf("wrong scope/limit: %+v", analysis)
		}
		first := snapshot.Nodes[analysis.TopFiles[0]]
		if (!allocated && first.Size != 1200) || (allocated && first.Allocated != 1200) {
			t.Fatal("wrong metric ordering")
		}
	}
	rows, err := snapshot.QueryRows(context.Background(), Query{Mode: "largest", Root: folder})
	if err != nil || len(rows.Rows) != 1200 {
		t.Fatal("file view escaped scope", len(rows.Rows), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot.Analyze(ctx, folder, false, 100); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
}

func BenchmarkAnalyzeMillion(b *testing.B) {
	snapshot := New("fixture")
	for number := range 1000000 {
		snapshot.Add(0, "file.bin", uint64(number+1), uint64(number+1), 0, 0)
	}
	snapshot.Finalize()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := snapshot.Analyze(context.Background(), 0, false, 100); err != nil {
			b.Fatal(err)
		}
	}
}
