package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures deliberately include mostly unique keys, many duplicate
// groups, and one very common size. Snapshot construction and file creation
// are outside the timed region; every operation starts without result caches.
func duplicatePerformanceSnapshot(n int, kind string) (*Snapshot, int) {
	s := New(`C:\duplicate-performance`)
	s.Nodes = append(make([]Node, 0, n+n/500+1), s.Nodes...)
	s.Names = append(make([]byte, 0, n*25), s.Names...)
	var parent ID
	for i := range n {
		if i%500 == 0 {
			parent = s.Add(0, fmt.Sprintf("folder-%d", i/500), 0, 0, 0, Directory)
		}
		key := i
		if kind != "Unique" && i%10 == 1 {
			key-- // one duplicate pair per ten files
		}
		if kind == "Repeated" {
			key = i % 1000
		}
		size := uint64(key)*7919%((1<<27)-1) + 1
		if kind == "SameSize" {
			size = 65536
		}
		name := fmt.Sprintf("File-%08d.LOG", key)
		if kind == "Unicode" {
			name = fmt.Sprintf("日本-Ärger-%08d.LOG", key)
		}
		s.Add(parent, name, size, size, 0, 0)
	}
	s.Finalize()
	want := n / 10
	if kind == "Unique" {
		want = 0
	} else if kind == "Repeated" {
		want = 1000
	}
	return s, want
}

func BenchmarkDuplicateMetadata(b *testing.B) {
	for _, kind := range []string{"Mixed", "SameSize", "Repeated", "Unique", "Unicode"} {
		b.Run(kind, func(b *testing.B) {
			n := 1000000
			if kind == "Unicode" {
				n = 200000
			}
			s, want := duplicatePerformanceSnapshot(n, kind)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := s.DuplicateCandidatesOver(context.Background(), 0, 0)
				if err != nil || len(got.Groups) != want {
					b.Fatalf("groups=%d want=%d error=%v", len(got.Groups), want, err)
				}
			}
		})
	}
}

func BenchmarkDuplicateRealMetadata(b *testing.B) {
	path := os.Getenv("QUARRY_DUP_BENCH_SNAPSHOT")
	if path == "" {
		b.Skip("set QUARRY_DUP_BENCH_SNAPSHOT")
	}
	s, err := LoadSnapshot(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	want, err := s.DuplicateCandidatesOver(context.Background(), 0, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, err := s.DuplicateCandidatesOver(context.Background(), 0, 0)
		if err != nil || len(got.Groups) != len(want.Groups) {
			b.Fatal("real snapshot results changed", err)
		}
	}
}

// Run in both executables to compare the complete ordered output, beyond
// group counts. Does not open any files from the saved scan.
func TestDuplicateSnapshotParityReport(t *testing.T) {
	path := os.Getenv("QUARRY_DUP_BENCH_SNAPSHOT")
	if path == "" {
		t.Skip("set QUARRY_DUP_BENCH_SNAPSHOT")
	}
	s, err := LoadSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DuplicateCandidatesOver(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	var scratch [8]byte
	files := 0
	for _, group := range result.Groups {
		for _, value := range []uint64{group.Size, uint64(len(group.IDs))} {
			binary.LittleEndian.PutUint64(scratch[:], value)
			hash.Write(scratch[:])
		}
		for _, id := range group.IDs {
			binary.LittleEndian.PutUint64(scratch[:], uint64(id))
			hash.Write(scratch[:])
		}
		files += len(group.IDs)
	}
	report := map[string]any{"root": s.Root, "nodes": len(s.Nodes), "files": s.Nodes[0].Files, "groups": len(result.Groups), "candidates": files, "ordered_sha256": fmt.Sprintf("%x", hash.Sum(nil))}
	data, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(data))
	if out := os.Getenv("QUARRY_DUP_PARITY_OUT"); out != "" {
		if err := os.WriteFile(out, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Keep real files in the same optional directory for before/after executables,
// avoiding changes in path lengths, file placement, and generated content.
func duplicateVerificationFixture(b *testing.B, kind string) (*Snapshot, int, uint64) {
	dir := os.Getenv("QUARRY_DUP_BENCH_DIR")
	if dir == "" {
		dir = b.TempDir()
	}
	dir = filepath.Join(dir, kind)
	if err := os.MkdirAll(dir, 0700); err != nil {
		b.Fatal(err)
	}
	s := New(dir)
	n, want := 512, 256
	if kind != "SmallPairs" {
		n, want = 64, 8
		if kind == "AllCopies" {
			want = 1
		}
	}
	var total uint64
	for i := range n {
		size := 128 + i/2
		key := i / 2
		if kind != "SmallPairs" {
			size = 4 << 20
			key = i
			if i < 16 {
				key = i / 2
			}
			if kind == "AllCopies" {
				key = 0
			}
		}
		data := bytes.Repeat([]byte{'x'}, size)
		copy(data[size/2:], fmt.Sprintf("middle-%08d", key))
		copy(data[size-32:], fmt.Sprintf("tail-%08d", key))
		name := fmt.Sprintf("candidate-%04d.bin", i)
		path := filepath.Join(dir, name)
		if previous, err := os.ReadFile(path); err != nil || !bytes.Equal(previous, data) {
			if err := os.WriteFile(path, data, 0600); err != nil {
				b.Fatal(err)
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			b.Fatal(err)
		}
		s.Add(0, name, uint64(size), uint64(size), info.ModTime().Unix(), 0)
		total += uint64(size)
	}
	s.Finalize()
	return s, want, total
}

func BenchmarkDuplicateVerify(b *testing.B) {
	for _, kind := range []string{"SmallPairs", "SharedHeader", "AllCopies"} {
		b.Run(kind, func(b *testing.B) {
			s, want, _ := duplicateVerificationFixture(b, kind)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := s.FindDuplicates(context.Background(), 0, &DuplicateProgress{})
				if err != nil || len(got.Groups) != want || got.Skipped != 0 {
					b.Fatalf("groups=%d want=%d skipped=%d error=%v", len(got.Groups), want, got.Skipped, err)
				}
			}
		})
	}
}
