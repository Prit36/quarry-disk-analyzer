//go:build windows

package scan

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
)

// BenchmarkReadMFTSynthetic parses a synthetic MFT of QUARRY_MFT_BENCH
// records (default 600,000; about 1 KiB of temporary file each) through the
// real reader and index build. The image is read through the file cache, so
// this measures parsing and index construction rather than the disk:
//
//	QUARRY_MFT_BENCH=600000 go test ./internal/scan -run '^$' -bench ReadMFTSynthetic -benchmem
func BenchmarkReadMFTSynthetic(b *testing.B) {
	if os.Getenv("QUARRY_MFT_BENCH") == "" {
		b.Skip("set QUARRY_MFT_BENCH to the record count")
	}
	n, _ := strconv.Atoi(os.Getenv("QUARRY_MFT_BENCH"))
	n = max(n, 64)
	v := syntheticMFT(b, n)
	defer v.f.Close()
	b.ResetTimer()
	for range b.N {
		s, err := readMFT(context.Background(), `C:\`, &Progress{}, v)
		if err != nil {
			b.Fatal(err)
		}
		if len(s.Nodes) < n*9/10 {
			b.Fatal("records lost", len(s.Nodes))
		}
	}
}

// syntheticMFT writes an MFT of n records: one folder in ten, children
// usually after their parents but sometimes before, as on a used volume.
func syntheticMFT(tb testing.TB, n int) *volumeReader {
	const sequence = uint64(2) << 48
	f, err := os.CreateTemp(tb.TempDir(), "mft-*.bin")
	if err != nil {
		tb.Fatal(err)
	}
	length := int64(n) * 1024
	clusters := (length + 4095) / 4096
	mftData := make([]byte, 72)
	testLE.PutUint32(mftData, 0x80)
	testLE.PutUint32(mftData[4:], 72)
	mftData[8] = 1
	testLE.PutUint64(mftData[24:], uint64(clusters-1))
	testLE.PutUint16(mftData[32:], 64)
	testLE.PutUint64(mftData[40:], uint64(clusters*4096))
	testLE.PutUint64(mftData[48:], uint64(length))
	// One run: three bytes of length, one of offset (cluster 1).
	copy(mftData[64:], []byte{0x13, byte(clusters), byte(clusters >> 8), byte(clusters >> 16), 1, 0})
	exts := []string{".dll", ".txt", ".log", ".jpg", ".exe", ".json", ".png", ""}
	dirs := []int{5}
	buf := make([]byte, 0, 4<<20)
	flush := func(force bool) {
		if len(buf) >= 4<<20 || force {
			if _, err := f.Write(buf); err != nil {
				tb.Fatal(err)
			}
			buf = buf[:0]
		}
	}
	buf = append(buf, make([]byte, 4096)...) // cluster 0
	for i := range n {
		var r []byte
		switch {
		case i == 0:
			r = testRecord(false, 0, testName("$MFT", sequence|5), mftData)
		case i == 5:
			r = testRecord(true, 0, testName(".", sequence|5))
		case i < 16:
			r = make([]byte, 1024) // unused system records
		default:
			// Parents are mostly earlier folders; one in sixteen is a
			// folder created later, as reused records produce.
			parent := dirs[(i*7919)%len(dirs)]
			if i%16 == 0 && i+20 < n {
				parent = i + 10 - (i+10)%10
			}
			if i%10 == 0 {
				r = testRecord(true, 0, testName(fmt.Sprintf("folder%d", i), sequence|uint64(parent)))
				dirs = append(dirs, i)
			} else {
				size := make([]byte, 8+(i%200))
				r = testRecord(false, 0, testName(fmt.Sprintf("file%d%s", i, exts[i%len(exts)]), sequence|uint64(parent)), testResident(0x80, size))
			}
		}
		buf = append(buf, r...)
		flush(false)
	}
	flush(true)
	return &volumeReader{f: f, sector: 512, cluster: 4096, record: 1024, length: length, start: 1}
}
