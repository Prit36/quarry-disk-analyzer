package index

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
	"unsafe"
)

func fixture() *Snapshot {
	s := New("C:\\test")
	d := s.Add(0, "media", 0, 0, 0, Directory)
	s.Add(d, "video.MP4", 1000, 4096, 1700000000, 0)
	s.Add(d, "empty", 0, 0, 0, 0)
	s.Add(0, "photo.png", 300, 4096, 0, 0)
	s.Finalize()
	return s
}
func TestAggregationAndArena(t *testing.T) {
	s := fixture()
	n := s.Nodes[0]
	if n.Size != 1300 || n.Allocated != 8192 || n.Files != 3 || n.Dirs != 1 {
		t.Fatalf("wrong totals: %+v", n)
	}
	if unsafe.Sizeof(Node{}) != 56 {
		t.Fatalf("record grew to %d bytes", unsafe.Sizeof(Node{}))
	}
	if s.NameView(2) != "video.MP4" || !strings.HasSuffix(s.Path(2), "media\\video.MP4") {
		t.Fatal("path reconstruction failed", s.Path(2))
	}
	if s.Nodes[1].Dirs != 0 {
		t.Fatal("subdirectory count should exclude itself")
	}
}
func TestQueryAndSorting(t *testing.T) {
	s := fixture()
	for _, tc := range []struct {
		q       string
		matches int
	}{{"ext:mp4", 1}, {"ext:none", 1}, {"VIDEO min:900B", 1}, {"min:2KiB", 0}, {"png", 1}, {"min:0B", 3}} {
		r, err := s.QueryRows(context.Background(), Query{Text: tc.q, Mode: "largest", Sort: Sort{Desc: true}})
		if err != nil || r.Matches != tc.matches {
			t.Errorf("query %q: %+v %v", tc.q, r, err)
		}
	}
	r, err := s.QueryRows(context.Background(), Query{Open: map[ID]bool{1: true}, Sort: Sort{Desc: true}})
	if err != nil || len(r.Rows) != 5 || r.Rows[2].Depth != 2 {
		t.Fatal(r, err)
	}
	if _, err := s.QueryRows(context.Background(), Query{Text: "min:potato"}); err == nil {
		t.Fatal("invalid size accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.QueryRows(ctx, Query{Mode: "largest"}); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestSummaryAndCSV(t *testing.T) {
	s := fixture()
	summary, err := s.Summarize(context.Background())
	if err != nil || len(summary.Types) != 3 || summary.Types[0].Extension != ".mp4" {
		t.Fatal(summary, err)
	}
	var b bytes.Buffer
	if err := s.Export(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&b).ReadAll()
	if err != nil || len(rows) != 6 || rows[0][0] != "Path" {
		t.Fatal(rows, err)
	}
}
func TestAllFilesRemainAccessible(t *testing.T) {
	s := New("C:\\test")
	for i := 0; i < 20000; i++ {
		s.Add(0, "file", uint64(i), uint64(i), 0, 0)
	}
	s.Finalize()
	r, err := s.QueryRows(context.Background(), Query{Mode: "largest", Sort: Sort{Desc: true}})
	if err != nil || r.Matches != 20000 || len(r.Rows) != 20000 || s.Nodes[r.Rows[0].ID].Size != 19999 || s.Nodes[r.Rows[19999].ID].Size != 0 {
		t.Fatal("full result incorrect", err)
	}
}
func BenchmarkIndexMillion(b *testing.B) {
	b.ReportAllocs()
	for range b.N {
		s := New("C:\\bench")
		s.Nodes = make([]Node, 1, 1000001)
		s.Nodes[0] = Node{Parent: None, FirstChild: None, NextSibling: None, Flags: Directory}
		s.Names = make([]byte, 0, 20_000_000)
		for i := 0; i < 1_000_000; i++ {
			s.Add(0, "benchmark-file.bin", 4096, 4096, 0, 0)
		}
		s.Finalize()
		b.ReportMetric(float64(s.Bytes())/1e6, "MB/index")
		if s.Nodes[0].Files != 1_000_000 {
			b.Fatal("lost records")
		}
	}
}
func BenchmarkSearchMillion(b *testing.B) {
	s := New("C:\\bench")
	for i := 0; i < 1_000_000; i++ {
		s.Add(0, fmt.Sprintf("file-%d.mp4", i), uint64(i), uint64(i), 0, 0)
	}
	s.Finalize()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, err := s.QueryRows(context.Background(), Query{Text: "ext:mp4 min:900KB", Sort: Sort{Desc: true}})
		if err != nil {
			b.Fatal(err)
		}
	}
}
