//go:build windows

package scan

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"stratum/internal/index"
)

var testLE = binary.LittleEndian

func testResident(typ uint32, value []byte) []byte {
	length := (24 + len(value) + 7) &^ 7
	a := make([]byte, length)
	testLE.PutUint32(a, typ)
	testLE.PutUint32(a[4:], uint32(length))
	testLE.PutUint32(a[16:], uint32(len(value)))
	testLE.PutUint16(a[20:], 24)
	copy(a[24:], value)
	return a
}
func testName(name string, parent uint64) []byte {
	u := utf16.Encode([]rune(name))
	v := make([]byte, 66+2*len(u))
	testLE.PutUint64(v, parent)
	v[64], v[65] = byte(len(u)), 1
	for i, w := range u {
		testLE.PutUint16(v[66+i*2:], w)
	}
	return testResident(0x30, v)
}
func testRecord(directory bool, base uint64, attrs ...[]byte) []byte {
	b := make([]byte, 1024)
	copy(b, "FILE")
	testLE.PutUint16(b[4:], 48)
	testLE.PutUint16(b[6:], 3)
	testLE.PutUint16(b[16:], 2)
	testLE.PutUint16(b[18:], 1)
	testLE.PutUint16(b[20:], 56)
	flags := uint16(1)
	if directory {
		flags |= 2
	}
	testLE.PutUint16(b[22:], flags)
	testLE.PutUint64(b[32:], base)
	off := 56
	for _, a := range attrs {
		copy(b[off:], a)
		off += len(a)
	}
	testLE.PutUint32(b[off:], 0xffffffff)
	testLE.PutUint32(b[24:], uint32(off+4))
	testLE.PutUint16(b[48:], 0xabcd)
	for i := 1; i <= 2; i++ {
		copy(b[48+i*2:50+i*2], b[i*512-2:i*512])
		testLE.PutUint16(b[i*512-2:], 0xabcd)
	}
	return b
}
func TestMFTReaderOnSyntheticVolume(t *testing.T) {
	const sequence = uint64(2) << 48
	image := make([]byte, 4096+16*1024)
	mftData := make([]byte, 72)
	testLE.PutUint32(mftData, 0x80)
	testLE.PutUint32(mftData[4:], 72)
	mftData[8] = 1
	testLE.PutUint64(mftData[24:], 3)
	testLE.PutUint16(mftData[32:], 64)
	testLE.PutUint64(mftData[40:], 16*1024)
	testLE.PutUint64(mftData[48:], 16*1024)
	copy(mftData[64:], []byte{0x11, 4, 1, 0})
	records := map[int][]byte{
		0:  testRecord(false, 0, testName("$MFT", sequence|5), mftData),
		2:  testRecord(false, 0, testName("résumé.txt", sequence|7), testResident(0x80, make([]byte, 100))),
		5:  testRecord(true, 0, testName(".", sequence|5)),
		7:  testRecord(true, 0, testName("Documents", sequence|5)),
		9:  testRecord(false, sequence|2, testResident(0x80, make([]byte, 10))),
		10: testRecord(false, 0, testName("orphan.txt", 3<<48|7), testResident(0x80, make([]byte, 5))),
	}
	for n, r := range records {
		copy(image[4096+n*1024:], r)
	}
	file, err := os.CreateTemp(t.TempDir(), "volume-*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(image); err != nil {
		t.Fatal(err)
	}
	v := &volumeReader{f: file, sector: 512, cluster: 4096, record: 1024, length: 16 * 1024, start: 1}
	s, err := readMFT(context.Background(), `C:\`, &Progress{}, v)
	if err != nil {
		t.Fatal(err)
	}
	if s.Engine != "NTFS · direct MFT" || s.Unresolved != 1 || s.Nodes[0].Size != 16*1024+115 || s.Nodes[0].Allocated != 16*1024 {
		t.Fatalf("incorrect MFT scan: %+v root %+v", s, s.Nodes[0])
	}
	found := false
	for i, n := range s.Nodes {
		if s.NameView(index.ID(i)) == "résumé.txt" {
			found = true
			if n.Size != 110 || s.Path(index.ID(i)) != `C:\Documents\résumé.txt` {
				t.Fatal("extension data or parent reference lost", n, s.Path(index.ID(i)))
			}
		}
	}
	if !found {
		t.Fatal("Unicode name lost")
	}
	// The MFT's data mapping continues in a separate record, and its final
	// cluster is fragmented. Bootstrap the continuation from an already mapped
	// extent, exercising the actual raw reader without an elevated disk handle.
	fragmented := make([]byte, 8*4096)
	baseData := append([]byte(nil), mftData...)
	testLE.PutUint64(baseData[24:], 2)
	copy(baseData[64:], []byte{0x11, 3, 1, 0})
	continuation := append([]byte(nil), mftData...)
	testLE.PutUint64(continuation[16:], 3)
	testLE.PutUint64(continuation[24:], 3)
	copy(continuation[64:], []byte{0x11, 1, 7, 0})
	list := make([]byte, 64)
	for i, ref := range []uint64{sequence, sequence | 3} {
		offset := i * 32
		testLE.PutUint32(list[offset:], 0x80)
		testLE.PutUint16(list[offset+4:], 32)
		testLE.PutUint64(list[offset+8:], uint64(i*3))
		testLE.PutUint64(list[offset+16:], ref)
	}
	records[0] = testRecord(false, 0, testName("$MFT", sequence|5), baseData, testResident(0x20, list))
	records[3] = testRecord(false, sequence, continuation)
	for number, record := range records {
		offset := 4096 + number*1024
		if number >= 12 {
			offset = 7*4096 + (number-12)*1024
		}
		copy(fragmented[offset:], record)
	}
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(fragmented, 0); err != nil {
		t.Fatal(err)
	}
	fragmentedScan, err := readMFT(context.Background(), `C:\`, &Progress{}, v)
	if err != nil {
		t.Fatal("fragmented MFT failed", err)
	}
	if fragmentedScan.Nodes[0].Size != s.Nodes[0].Size || fragmentedScan.Nodes[0].Allocated != s.Nodes[0].Allocated {
		t.Fatal("fragmented mapping lost data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readMFT(ctx, `C:\`, &Progress{}, v); err != context.Canceled {
		t.Fatal("MFT cancellation ignored", err)
	}
}
func TestSparseFileAllocatedSize(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(file.Fd()), 0x900c4, nil, 0, nil, 0, &returned, nil); err != nil {
		file.Close()
		t.Skip("sparse files unsupported", err)
	}
	if err := file.Truncate(32 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	s, err := Run(context.Background(), root, Options{ForceWalk: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Nodes[0].Size != 32<<20 || s.Nodes[0].Allocated != 0 {
		t.Fatal("sparse allocation incorrectly rounded", s.Nodes[0])
	}
}

func TestJunctionIsNotFollowed(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "outside.bin"), make([]byte, 1024), 0600)
	link := filepath.Join(root, "junction")
	if err := os.Mkdir(link, 0700); err != nil {
		t.Fatal(err)
	}
	sub, print := utf16.Encode([]rune(`\??\`+outside)), utf16.Encode([]rune(outside))
	data := make([]byte, 16+2*(len(sub)+len(print)+2))
	testLE.PutUint32(data, 0xa0000003)
	testLE.PutUint16(data[4:], uint16(len(data)-8))
	testLE.PutUint16(data[10:], uint16(2*len(sub)))
	testLE.PutUint16(data[12:], uint16(2*(len(sub)+1)))
	testLE.PutUint16(data[14:], uint16(2*len(print)))
	for i, w := range sub {
		testLE.PutUint16(data[16+i*2:], w)
	}
	for i, w := range print {
		testLE.PutUint16(data[16+2*(len(sub)+1+i):], w)
	}
	ptr, _ := windows.UTF16PtrFromString(link)
	h, err := windows.CreateFile(ptr, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	var returned uint32
	err = windows.DeviceIoControl(h, 0x900a4, (*byte)(unsafe.Pointer(&data[0])), uint32(len(data)), nil, 0, &returned, nil)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Run(context.Background(), root, Options{ForceWalk: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Nodes[0].Size != 0 || s.SkippedLinks != 1 {
		t.Fatal("junction followed", s.Nodes[0], s.SkippedLinks)
	}
}
