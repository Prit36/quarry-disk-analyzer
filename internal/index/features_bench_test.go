package index

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"
)

// changedTree is searchTree after a month of use: one file in ten grew,
// one in fifty is gone and every folder gained a file.
func changedTree() *Snapshot {
	s := New(`C:\data`)
	exts := []string{"log", "mp4", "jpg", "txt", "zip", "dll", "tmp", "cs"}
	for i := range 50 {
		top := s.Add(0, fmt.Sprintf("area%02d", i), 0, 0, 0, Directory)
		for j := range 100 {
			dir := s.Add(top, fmt.Sprintf("Folder_%03d", j), 0, 0, 0, Directory)
			for k := range 99 {
				if k%50 == 49 {
					continue
				}
				ext := exts[(i+j+k)%len(exts)]
				name := fmt.Sprintf("Report_%d_%d.%s", j, k, ext)
				if k%7 == 0 {
					name = fmt.Sprintf("backup-cache-%d.%s", k, ext)
				}
				size := uint64((i*7919+j*104729+k*15485863)%(1<<30)) + 1
				if k%10 == 3 {
					size += 4096
				}
				s.Add(dir, name, size, size, int64(1_600_000_000+k*3600), 0)
			}
			s.Add(dir, "new.bin", 1<<20, 1<<20, 1_700_000_000, 0)
		}
	}
	s.Finalize()
	return s
}

func BenchmarkComparison(b *testing.B) {
	c, err := NewComparison(searchTree(), changedTree(), false)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("largest", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, _, err := c.LargestChanges(context.Background(), c.Root(), 1000, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("children", func(b *testing.B) {
		// A folder of 99 files: what opening one folder costs.
		rows, err := c.Children(context.Background(), c.Root())
		if err != nil {
			b.Fatal(err)
		}
		inner, err := c.Children(context.Background(), rows[0])
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := c.Children(context.Background(), inner[0]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkAgeProfiles(b *testing.B) {
	s := searchTree()
	now := time.Unix(1_700_000_000, 0)
	b.Run("two-walks", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.AgeProfile(context.Background(), 0, now, 0); err != nil {
				b.Fatal(err)
			}
			if _, err := s.AgeProfile(context.Background(), 0, now, 100<<20); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("one-walk", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := s.AgeProfiles(context.Background(), 0, now, 0, 100<<20); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkGraft(b *testing.B) {
	s := searchTree()
	fresh := New(`C:\data\area00\Folder_000`)
	for k := range 5000 {
		fresh.Add(0, fmt.Sprintf("f%d.bin", k), 100, 4096, 0, 0)
	}
	fresh.Finalize()
	target := s.Nodes[s.Nodes[0].FirstChild].FirstChild
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Graft(target, fresh); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCategorySearch(b *testing.B) {
	s := searchTree()
	for _, q := range []string{"cat:videos", "cat:other", "cat:code;temporary type:file", "is:cloud"} {
		b.Run(q, func(b *testing.B) {
			f, err := ParseFilter(q)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := s.Search(context.Background(), 0, &f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestLargestChangesSplit checks that splitting the comparison among
// workers returns what one walk does: the same totals, entries and order.
func TestLargestChangesSplit(t *testing.T) {
	c, err := NewComparison(searchTree(), changedTree(), false)
	if err != nil {
		t.Fatal(err)
	}
	run := func(procs, limit int, keep func(DiffStatus) bool) ([]DiffEntry, DiffTotals) {
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
		rows, totals, err := c.LargestChanges(context.Background(), c.Root(), limit, keep)
		if err != nil {
			t.Fatal(err)
		}
		return rows, totals
	}
	for _, limit := range []int{0, 10, 1000, 1 << 20} {
		for _, keep := range []func(DiffStatus) bool{nil, func(s DiffStatus) bool { return s == DiffRemoved }} {
			one, oneTotals := run(1, limit, keep)
			many, manyTotals := run(8, limit, keep)
			if oneTotals != manyTotals || !slices.Equal(one, many) {
				t.Fatalf("limit %d: one walk %d rows %+v, split %d rows %+v", limit, len(one), oneTotals, len(many), manyTotals)
			}
		}
	}
	_, totals := run(8, 10, nil)
	// 50*100 folders: 1 file in 50 removed (k=49, 98 of 99 kept), 1 added,
	// every k%10==3 file grew.
	if totals.Added != 5000 || totals.Removed != 5000 || totals.Grown != 50000 {
		t.Fatalf("totals %+v", totals)
	}
}
