package desktop

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"quarry/internal/index"
)

// BenchmarkEvaluateBasket reviews a basket of 20,000 items: 200 folders
// and 100 files inside each, as Select All in a large listing adds them.
func BenchmarkEvaluateBasket(b *testing.B) {
	var items []basketItem
	for i := range 200 {
		dir := fmt.Sprintf(`C:\data\area%03d`, i)
		items = append(items, basketItem{path: dir, dir: true, size: 100})
		for j := range 99 {
			items = append(items, basketItem{path: fmt.Sprintf(`%s\file%03d.bin`, dir, j), size: 1})
		}
	}
	find := func(i int) basketFound { return basketFound{id: index.ID(i + 1), ok: true} }
	stat := func(string) (fs.FileInfo, error) { return nil, errors.New("not used") }
	b.ReportAllocs()
	for range b.N {
		evaluateBasket(items, find, stat)
	}
}

// BenchmarkComputeInsights is the Insights dialog over half a million
// entries.
func BenchmarkComputeInsights(b *testing.B) {
	s := index.New(`C:\data`)
	exts := []string{"log", "mp4", "jpg", "txt", "zip", "dll", "tmp", "cs"}
	for i := range 50 {
		top := s.Add(0, fmt.Sprintf("area%02d", i), 0, 0, 0, index.Directory)
		for j := range 100 {
			dir := s.Add(top, fmt.Sprintf("Folder_%03d", j), 0, 0, 0, index.Directory)
			for k := range 99 {
				size := uint64((i*7919+j*104729+k*15485863)%(1<<30)) + 1
				s.Add(dir, fmt.Sprintf("Report_%d.%s", k, exts[(i+j+k)%len(exts)]), size, size, int64(1_600_000_000+k*86400), 0)
			}
		}
	}
	s.Finalize()
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := computeInsights(context.Background(), s, 0, false, now, 5); err != nil {
			b.Fatal(err)
		}
	}
}
