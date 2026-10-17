package ntfs

import (
	"testing"
	"unicode/utf16"
)

func resident(typ uint32, value []byte) []byte {
	length := (24 + len(value) + 7) &^ 7
	a := make([]byte, length)
	le.PutUint32(a, typ)
	le.PutUint32(a[4:], uint32(length))
	le.PutUint32(a[16:], uint32(len(value)))
	le.PutUint16(a[20:], 24)
	copy(a[24:], value)
	return a
}
func fileName(name string, namespace byte, parent uint64) []byte {
	u := utf16.Encode([]rune(name))
	v := make([]byte, 66+2*len(u))
	le.PutUint64(v, parent)
	v[64], v[65] = byte(len(u)), namespace
	for i, w := range u {
		le.PutUint16(v[66+2*i:], w)
	}
	return resident(0x30, v)
}
func record(attrs ...[]byte) []byte {
	b := make([]byte, 1024)
	copy(b, "FILE")
	le.PutUint16(b[4:], 48)
	le.PutUint16(b[6:], 3)
	le.PutUint16(b[16:], 7)
	le.PutUint16(b[18:], 1)
	le.PutUint16(b[20:], 56)
	le.PutUint16(b[22:], 1)
	off := 56
	for _, a := range attrs {
		copy(b[off:], a)
		off += len(a)
	}
	le.PutUint32(b[off:], 0xffffffff)
	le.PutUint32(b[24:], uint32(off+4))
	le.PutUint16(b[48:], 0xabcd)
	for i := 1; i <= 2; i++ {
		copy(b[48+i*2:50+i*2], b[i*512-2:i*512])
		le.PutUint16(b[i*512-2:], 0xabcd)
	}
	return b
}
func TestResidentAndNamePreference(t *testing.T) {
	b := record(fileName("LONGFI~1", 2, 5), fileName("Long filename.mp4", 1, 5), resident(0x80, []byte("payload")))
	if err := Fixup(b, 512); err != nil {
		t.Fatal(err)
	}
	r, used, err := Parse(b, 42)
	if err != nil || !used || r.Name != "Long filename.mp4" || r.Size != 7 || r.Allocated != 0 || r.Reference != 42|7<<48 {
		t.Fatalf("%+v %v", r, err)
	}
}

// nonResident builds a $DATA attribute header with no runs, enough for Parse.
func nonResident(name string, flags uint16, size, allocated, compressed uint64) []byte {
	u := utf16.Encode([]rune(name))
	nameOff := 72
	length := (nameOff + 2*len(u) + 8 + 7) &^ 7
	a := make([]byte, length)
	le.PutUint32(a, 0x80)
	le.PutUint32(a[4:], uint32(length))
	a[8] = 1
	a[9] = byte(len(u))
	le.PutUint16(a[10:], uint16(nameOff))
	le.PutUint16(a[12:], flags)
	le.PutUint16(a[32:], uint16(nameOff+2*len(u)))
	le.PutUint64(a[40:], allocated)
	le.PutUint64(a[48:], size)
	le.PutUint64(a[56:], size)
	le.PutUint64(a[64:], compressed)
	for i, w := range u {
		le.PutUint16(a[nameOff+2*i:], w)
	}
	return a
}

func TestSparseNamedStreamCountsAllocation(t *testing.T) {
	// $BadClus: an empty main stream and a sparse $Bad stream as long as the volume.
	b := record(fileName("$BadClus", 3, 5), resident(0x80, nil), nonResident("$Bad", 0x8000, 1<<40, 1<<40, 4096))
	if err := Fixup(b, 512); err != nil {
		t.Fatal(err)
	}
	r, _, err := Parse(b, 8)
	if err != nil || r.Size != 4096 || r.Allocated != 4096 {
		t.Fatalf("$BadClus: %+v %v", r, err)
	}
	// A sparse main stream (a VM disk) keeps its logical length.
	b = record(fileName("disk.vhdx", 1, 5), nonResident("", 0x8000, 1<<34, 1<<34, 1<<20))
	if err := Fixup(b, 512); err != nil {
		t.Fatal(err)
	}
	r, _, err = Parse(b, 9)
	if err != nil || r.Size != 1<<34 || r.Allocated != 1<<20 {
		t.Fatalf("sparse file: %+v %v", r, err)
	}
}

func TestTornAndMalformedRecords(t *testing.T) {
	b := record(fileName("file", 1, 5))
	b[510] = 0
	if err := Fixup(b, 512); err == nil {
		t.Fatal("torn record accepted")
	}
	b = record(fileName("file", 1, 5))
	if err := Fixup(b, 512); err != nil {
		t.Fatal(err)
	}
	le.PutUint32(b[60:], 0)
	if _, _, err := Parse(b, 1); err == nil {
		t.Fatal("zero-length attribute accepted")
	}
	for i := 0; i < 48; i++ {
		if _, _, err := Parse(b[:i], 1); err == nil {
			t.Fatal("short record accepted")
		}
	}
}
func TestRunsAndSparseAllocation(t *testing.T) {
	a := make([]byte, 80)
	le.PutUint32(a, 0x80)
	le.PutUint32(a[4:], 80)
	a[8] = 1
	le.PutUint16(a[12:], 0x8000)
	le.PutUint64(a[24:], 3)
	le.PutUint16(a[32:], 72)
	le.PutUint64(a[40:], 16384)
	le.PutUint64(a[48:], 16000)
	le.PutUint64(a[64:], 8192)
	copy(a[72:], []byte{0x11, 2, 10, 0x01, 2, 0})
	runs, err := Runs(Attribute{NonResident: true, Data: a})
	if err != nil || len(runs) != 2 || runs[0].LCN != 10 || runs[1].LCN != -1 || runs[1].VCN != 2 {
		t.Fatal(runs, err)
	}
	b := record(fileName("sparse.bin", 1, 5), a)
	if err := Fixup(b, 512); err != nil {
		t.Fatal(err)
	}
	r, _, err := Parse(b, 9)
	if err != nil || r.Allocated != 8192 || r.Size != 16000 {
		t.Fatal(r, err)
	}
	a[72] = 0xff
	if _, err := Runs(Attribute{NonResident: true, Data: a}); err == nil {
		t.Fatal("invalid run widths accepted")
	}
}
func TestSignedRunDelta(t *testing.T) {
	a := make([]byte, 80)
	le.PutUint64(a[24:], 3)
	le.PutUint16(a[32:], 64)
	copy(a[64:], []byte{0x11, 2, 20, 0x11, 2, 0xfb, 0})
	r, err := Runs(Attribute{NonResident: true, Data: a})
	if err != nil || r[1].LCN != 15 {
		t.Fatal(r, err)
	}
}
func FuzzRecordParser(f *testing.F) {
	f.Add(record(fileName("sample", 1, 5), resident(0x80, []byte("hello"))))
	f.Add([]byte("FILE"))
	f.Add(make([]byte, 1024))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			return
		}
		copyBytes := append([]byte(nil), b...)
		if Fixup(copyBytes, 512) == nil {
			_, _, _ = Parse(copyBytes, 100)
		}
		_ = Attributes(b, func(a Attribute) error {
			if a.NonResident {
				_, _ = Runs(a)
			} else {
				_, _ = a.Value()
			}
			return nil
		})
	})
}
func BenchmarkRecordParse(b *testing.B) {
	raw := record(fileName("benchmark-file.bin", 1, 5), resident(0x80, make([]byte, 512)))
	buffer := make([]byte, 1024)
	b.ReportAllocs()
	b.SetBytes(1024)
	for range b.N {
		copy(buffer, raw)
		_ = Fixup(buffer, 512)
		r, _, err := Parse(buffer, 100)
		if err != nil || r.Size != 512 {
			b.Fatal(err)
		}
	}
}
