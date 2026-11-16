package index

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

// Unique sizes never reach disk verification; this isolates candidate grouping.
func BenchmarkDuplicateCandidatesUnique(b *testing.B) {
	s := New(`C:\duplicate-benchmark`)
	for i := range 100000 {
		s.Add(0, "file.bin", uint64(i+1), uint64(i+1), 0, 0)
	}
	s.Finalize()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := s.FindDuplicates(context.Background(), 0, nil)
		if err != nil || len(result.Groups) != 0 || result.Checked != 0 {
			b.Fatal(result, err)
		}
	}
}

func BenchmarkSnapshotLoadMemory(b *testing.B) {
	s := benchSnapshot(500000)
	path := b.TempDir() + "/scan.quarry"
	if err := s.Save(context.Background(), path); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := LoadSnapshot(context.Background(), path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSegmentStreamMemory(b *testing.B) {
	var compressed bytes.Buffer
	z := newParallelGzip(&compressed)
	block := make([]byte, gzipBlockSize)
	for i := range 64 {
		copy(block, fmt.Sprintf("segment-%d", i))
		if _, err := z.Write(block); err != nil {
			b.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		b.Fatal(err)
	}
	data := compressed.Bytes()
	starts := gzipMemberStarts(data)
	b.ReportAllocs()
	b.SetBytes(64 * gzipBlockSize)
	b.ResetTimer()
	for range b.N {
		s := newSegmentStream(data, starts)
		n, err := io.Copy(io.Discard, s)
		s.Close()
		if err != nil || n != 64*gzipBlockSize {
			b.Fatal(n, err)
		}
	}
}
