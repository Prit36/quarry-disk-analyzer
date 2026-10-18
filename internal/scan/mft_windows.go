//go:build windows

package scan

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"quarry/internal/index"
	"quarry/internal/ntfs"
)

const refMask uint64 = 0x0000ffffffffffff

func eligibleMFT(root string) bool {
	return len(root) == 3 && root[1] == ':' && (root[2] == '\\' || root[2] == '/')
}

type volumeReader struct {
	f                       *os.File
	sector, cluster, record int64
	length, start           int64
	// device reopens the volume for parallel reads; "" shares f (tests).
	device string
	// bitmap is the $MFT's own $BITMAP: bit n is set when record n is in
	// use. nil when unavailable.
	bitmap []byte
}

func openDevice(device string, flags uint32) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), device), nil
}

// reader returns a handle for one parallel reader. I/O on a synchronous
// handle is serialized by Windows, so concurrent reads need their own. It
// bypasses the file cache: the MFT is read once, and cached it would push
// a gigabyte or more of other data out of memory for nothing (reads are
// sector-aligned, as unbuffered I/O requires).
func (v *volumeReader) reader() (*os.File, error) {
	if v.device == "" {
		return v.f, nil
	}
	return openDevice(v.device, windows.FILE_FLAG_NO_BUFFERING)
}

func openVolume(root string) (*volumeReader, error) {
	device := `\\.\` + strings.ToUpper(root[:2])
	f, err := openDevice(device, 0)
	if err != nil {
		return nil, fmt.Errorf("direct NTFS access unavailable (%w); run as administrator for MFT scanning", err)
	}
	h := windows.Handle(f.Fd())
	var data [128]byte
	var got uint32
	if err = windows.DeviceIoControl(h, 0x90064, nil, 0, &data[0], uint32(len(data)), &got, nil); err != nil {
		f.Close()
		return nil, fmt.Errorf("NTFS metadata: %w", err)
	}
	if got < 96 {
		f.Close()
		return nil, fmt.Errorf("short NTFS volume response")
	}
	le := binary.LittleEndian
	v := &volumeReader{f: f, device: device, sector: int64(le.Uint32(data[40:])), cluster: int64(le.Uint32(data[44:])), record: int64(le.Uint32(data[48:])), length: int64(le.Uint64(data[56:])), start: int64(le.Uint64(data[64:]))}
	if v.sector < 256 || v.sector > 65536 || v.cluster < v.sector || v.cluster > 2<<20 || v.record < 512 || v.record > 65536 || v.record%v.sector != 0 || v.length <= 0 || v.start <= 0 || v.start > math.MaxInt64/v.cluster {
		f.Close()
		return nil, fmt.Errorf("unsupported NTFS geometry")
	}
	if got >= 104 && (le.Uint16(data[100:]) != 3 || le.Uint16(data[102:]) > 1) {
		f.Close()
		return nil, fmt.Errorf("unsupported NTFS version")
	}
	return v, nil
}
func (v *volumeReader) recordByNumber(number uint64) ([]byte, error) {
	input := number
	out := make([]byte, v.record+16) // SDK structure includes trailing alignment
	var got uint32
	err := windows.DeviceIoControl(windows.Handle(v.f.Fd()), 0x90068, (*byte)(unsafe.Pointer(&input)), 8, &out[0], uint32(len(out)), &got, nil)
	if err != nil {
		return nil, err
	}
	if got < 12 || binary.LittleEndian.Uint64(out)&refMask != number || int64(binary.LittleEndian.Uint32(out[8:])) != v.record || int64(got) < 12+v.record {
		return nil, fmt.Errorf("MFT continuation record unavailable")
	}
	b := out[12 : 12+v.record]
	if err := ntfs.Fixup(b, int(v.sector)); err != nil {
		return nil, err
	}
	return b, nil
}
func (v *volumeReader) readRuns(runs []ntfs.Run, size int64) ([]byte, error) {
	if size < 0 || size > 16<<20 {
		return nil, fmt.Errorf("oversized NTFS attribute list")
	}
	// A volume handle reads whole sectors only: read whole clusters into a
	// buffer of whole clusters, then trim it.
	clusters := (size + v.cluster - 1) / v.cluster
	b := alignedBuffer(int(clusters*v.cluster), int(v.sector))
	for _, run := range runs {
		if run.LCN < 0 || run.LCN > math.MaxInt64/v.cluster || run.VCN > math.MaxInt64/v.cluster || run.Clusters > math.MaxInt64/v.cluster {
			return nil, ntfs.ErrRecord
		}
		off := run.VCN * v.cluster
		if off >= size {
			break
		}
		n := min(run.Clusters*v.cluster, int64(len(b))-off)
		if _, err := v.f.ReadAt(b[off:off+n], run.LCN*v.cluster); err != nil {
			return nil, err
		}
	}
	return b[:size:size], nil
}
func (v *volumeReader) mftRuns() ([]ntfs.Run, error) {
	b := alignedBuffer(int(v.record), int(v.sector))
	if _, err := v.f.ReadAt(b, v.start*v.cluster); err != nil {
		return nil, err
	}
	if err := ntfs.Fixup(b, int(v.sector)); err != nil {
		return nil, err
	}
	var runs []ntfs.Run
	var list []byte
	collect := func(a ntfs.Attribute) error {
		if a.Type == 0x80 && a.NameLength == 0 {
			r, err := ntfs.Runs(a)
			if err != nil {
				return err
			}
			runs = append(runs, r...)
		}
		if a.Type == 0xb0 && a.NameLength == 0 {
			v.bitmap = v.readBitmap(a)
		}
		if a.Type == 0x20 {
			if a.NonResident {
				r, err := ntfs.Runs(a)
				if err != nil {
					return err
				}
				list, err = v.readRuns(r, int64(binary.LittleEndian.Uint64(a.Data[48:])))
				return err
			}
			value, err := a.Value()
			if err != nil {
				return err
			}
			list = append([]byte(nil), value...)
		}
		return nil
	}
	if err := ntfs.Attributes(b, collect); err != nil {
		return nil, err
	}
	seen := map[uint64]bool{0: true}
	for off := 0; off < len(list); {
		if len(list)-off < 26 {
			return nil, ntfs.ErrRecord
		}
		e := list[off:]
		length := int(binary.LittleEndian.Uint16(e[4:]))
		if length < 26 || length > len(e) {
			return nil, ntfs.ErrRecord
		}
		// The $DATA mapping, and on large volumes the $BITMAP too, can live
		// in extension records. The bitmap is optional: failing to read it
		// only costs speed.
		if typ := binary.LittleEndian.Uint32(e); (typ == 0x80 || typ == 0xb0 && v.bitmap == nil) && e[6] == 0 {
			number := binary.LittleEndian.Uint64(e[16:]) & refMask
			if !seen[number] {
				seen[number] = true
				extension, err := v.recordFromRuns(runs, number)
				if err != nil {
					extension, err = v.recordByNumber(number)
				}
				if err != nil && typ == 0xb0 {
					off += length
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := ntfs.Attributes(extension, func(a ntfs.Attribute) error {
					if a.Type == 0x80 && a.NameLength == 0 {
						r, err := ntfs.Runs(a)
						if err != nil {
							return err
						}
						runs = append(runs, r...)
					}
					if a.Type == 0xb0 && a.NameLength == 0 && v.bitmap == nil {
						v.bitmap = v.readBitmap(a)
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
		}
		off += length
	}
	slices.SortFunc(runs, func(a, b ntfs.Run) int { return cmp.Compare(a.VCN, b.VCN) })
	var end int64
	for _, r := range runs {
		if r.VCN != end || r.LCN < 0 || r.LCN > math.MaxInt64/v.cluster || r.VCN > math.MaxInt64/v.cluster || r.Clusters > math.MaxInt64/v.cluster-r.VCN {
			return nil, fmt.Errorf("incomplete or sparse MFT run mapping")
		}
		end += r.Clusters
	}
	if end < v.length/v.cluster+(boolInt(v.length%v.cluster != 0)) {
		return nil, fmt.Errorf("MFT continuation mapping incomplete")
	}
	return runs, nil
}

// readBitmap reads the $MFT's $BITMAP attribute when it is complete in this
// record, and returns nil otherwise: it only sizes buffers and lets
// unused regions be skipped, so its absence costs speed, not accuracy.
func (v *volumeReader) readBitmap(a ntfs.Attribute) []byte {
	if !a.NonResident {
		value, err := a.Value()
		if err != nil {
			return nil
		}
		return append([]byte(nil), value...)
	}
	if len(a.Data) < 64 || binary.LittleEndian.Uint64(a.Data[16:]) != 0 {
		return nil // continues in another record
	}
	runs, err := ntfs.Runs(a)
	if err != nil {
		return nil
	}
	size := int64(binary.LittleEndian.Uint64(a.Data[48:]))
	var mapped int64
	for _, r := range runs {
		if r.LCN < 0 || r.Clusters > (math.MaxInt64-mapped)/v.cluster {
			return nil
		}
		mapped += r.Clusters * v.cluster
	}
	if mapped < size {
		return nil
	}
	b, err := v.readRuns(runs, size)
	if err != nil {
		return nil
	}
	return b
}

// inUse reports whether the bitmap allows record n to be in use.
func (v *volumeReader) inUse(n uint64) bool {
	return v.bitmap == nil || n/8 >= uint64(len(v.bitmap)) || v.bitmap[n/8]&(1<<(n%8)) != 0
}

// Most MFT continuation records are inside the extents already mapped by record
// zero. Read them directly there, avoiding both an ioctl per record and kernel
// fixup representation differences. The ioctl is only a bootstrap fallback.
func (v *volumeReader) recordFromRuns(runs []ntfs.Run, number uint64) ([]byte, error) {
	if number > uint64(math.MaxInt64/v.record) {
		return nil, ntfs.ErrRecord
	}
	logical := int64(number) * v.record
	for _, r := range runs {
		start, end := r.VCN*v.cluster, (r.VCN+r.Clusters)*v.cluster
		if r.LCN >= 0 && logical >= start && logical+v.record <= end {
			b := alignedBuffer(int(v.record), int(v.sector))
			if _, err := v.f.ReadAt(b, r.LCN*v.cluster+logical-start); err != nil {
				return nil, err
			}
			if err := ntfs.Fixup(b, int(v.sector)); err != nil {
				return nil, err
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("MFT continuation not in mapped extents")
}
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func scanMFT(ctx context.Context, root string, p *Progress) (*index.Snapshot, error) {
	v, err := openVolume(root)
	if err != nil {
		return nil, err
	}
	defer v.f.Close()
	return readMFT(ctx, root, p, v)
}

// mftRecord is what the index keeps of a base record; its name is in the
// chunk's arena (nameLen 0: the name is in an extension record, if any).
type mftRecord struct {
	reference, parent uint64
	size, allocated   uint64
	modified          int64
	nameOff           uint32
	nameLen           uint16
	flags             uint16
	rank              uint8
}

type mftExtension struct{ base, size, allocated uint64 }

// mftName is a $FILE_NAME found in an extension record. Records with many
// attributes (large folders such as WinSxS, files with many links) keep
// their names there, not in the base record.
type mftName struct {
	base, parent uint64
	nameOff      uint32
	nameLen      uint16
	rank         uint8
}

type mftIssue struct {
	number uint64
	err    error
}

// mftPiece is a contiguous stretch of the MFT inside one data run.
type mftPiece struct {
	seq      int
	physical int64
	length   int64
	first    uint64 // number of its first record
}

// mftChunk is a piece read and parsed by a worker. Chunks are pooled; the
// coordinator applies them in order, so IDs do not depend on scheduling.
type mftChunk struct {
	mftPiece
	data       []byte
	records    []mftRecord
	names      []byte
	extensions []mftExtension
	extNames   []mftName
	issues     []mftIssue
	root       uint64 // reference of the root folder (record 5), if here
	hardLinks  uint64
	files      uint64
	folders    uint64
	bytes      uint64
	err        error
}

// Read size and parallel readers for the MFT. Volume reads top out near
// 1 GB/s on a fast NVMe drive whatever the parallelism; two readers keep
// the drive busy while the other parses its chunk. (Variables for the
// elevated throughput test, which compares settings on a real volume.)
var (
	mftChunkBytes int64 = 2 << 20
	mftReaders          = 2
)

func (c *mftChunk) name(off uint32, n uint16) []byte { return c.names[off : off+uint32(n)] }

// parse fixes up and parses every record of the chunk. It runs on a worker
// and allocates nothing once the chunk's slices have grown.
func (c *mftChunk) parse(v *volumeReader) {
	c.records, c.names, c.extensions, c.extNames, c.issues = c.records[:0], c.names[:0], c.extensions[:0], c.extNames[:0], c.issues[:0]
	c.root, c.hardLinks, c.files, c.folders, c.bytes = 0, 0, 0, 0, 0
	for off := int64(0); off < c.length; off += v.record {
		number := c.first + uint64(off/v.record)
		b := c.data[off : off+v.record]
		if string(b[:4]) != "FILE" {
			continue
		}
		if err := ntfs.Fixup(b, int(v.sector)); err != nil {
			c.issues = append(c.issues, mftIssue{number, err})
			continue
		}
		r, inUse, err := ntfs.ParseRaw(b, number)
		if err != nil {
			c.issues = append(c.issues, mftIssue{number, err})
			continue
		}
		if !inUse {
			continue
		}
		start := len(c.names)
		c.names = index.AppendUTF16(c.names, r.RawName)
		nameLen := uint16(len(c.names) - start)
		if r.Base != 0 {
			if r.Size > 0 || r.Allocated > 0 {
				c.extensions = append(c.extensions, mftExtension{r.Base, r.Size, r.Allocated})
			}
			if nameLen > 0 {
				c.extNames = append(c.extNames, mftName{r.Base, r.Parent, uint32(start), nameLen, r.NameRank})
			}
			continue
		}
		if number == 5 {
			c.root = r.Reference
			c.names = c.names[:start]
			continue
		}
		flags := uint16(0)
		if r.Directory {
			flags |= index.Directory
			r.Size, r.Allocated = 0, 0
			c.folders++
		} else {
			c.files++
			c.bytes += r.Size
		}
		if r.Reparse {
			flags |= index.Reparse
		}
		if r.Links > 1 && !r.Directory {
			c.hardLinks += uint64(r.Links - 1)
		}
		c.records = append(c.records, mftRecord{r.Reference, r.Parent, r.Size, r.Allocated, r.Modified, uint32(start), nameLen, flags, r.NameRank})
	}
}

// chunkBytes is the read size: whole records, at most mftChunkBytes.
func (v *volumeReader) chunkBytes() int64 { return max(v.record, mftChunkBytes/v.record*v.record) }

// mftPieces splits the MFT's data runs into pieces of at most chunkBytes,
// leaving out pieces whose records the bitmap marks all unused.
func (v *volumeReader) mftPieces(runs []ntfs.Run) ([]mftPiece, error) {
	var pieces []mftPiece
	processed := int64(0)
	for _, run := range runs {
		left := min(run.Clusters*v.cluster, v.length-processed)
		physical := run.LCN * v.cluster
		for left > 0 {
			n := min(left, v.chunkBytes())
			if n%v.record != 0 {
				return nil, fmt.Errorf("MFT record crosses unsupported run boundary")
			}
			first := uint64(processed / v.record)
			used := false
			for r := first; r < first+uint64(n/v.record) && !used; r++ {
				used = v.inUse(r)
			}
			if used {
				pieces = append(pieces, mftPiece{len(pieces), physical, n, first})
			}
			processed += n
			left -= n
			physical += n
		}
		if processed >= v.length {
			break
		}
	}
	return pieces, nil
}

// readMFT reads the MFT with several readers at once (one synchronous read
// stream leaves an NVMe drive mostly idle), parses records on the readers
// and builds the index on this goroutine, in record order.
func readMFT(ctx context.Context, root string, p *Progress, v *volumeReader) (*index.Snapshot, error) {
	runs, err := v.mftRuns()
	if err != nil {
		return nil, fmt.Errorf("MFT mapping: %w", err)
	}
	pieces, err := v.mftPieces(runs)
	if err != nil {
		return nil, err
	}
	s := index.New(root)
	s.Engine = "NTFS · direct MFT"
	totalRecords := uint64(v.length / v.record)
	// The bitmap counts the records in use: an upper bound on the entries.
	capacity := int(min(totalRecords, 1_000_000))
	if v.bitmap != nil {
		used := 0
		for i, b := range v.bitmap {
			if uint64(i)*8 >= totalRecords {
				break
			}
			used += bits.OnesCount8(b)
		}
		capacity = used + 1
	}
	rootNode := s.Nodes[0]
	s.Nodes = make([]index.Node, 1, max(1, capacity))
	s.Nodes[0] = rootNode
	s.Names = make([]byte, 0, capacity*28) // about the average name length
	parents := make([]uint64, 1, max(1, capacity))
	// The sequence number of each entry's record: dense finds the entry by
	// record number, so a reference matches when its sequence does too.
	sequences := make([]uint16, 1, max(1, capacity))
	ranks := make([]uint8, 1, max(1, capacity))
	// Dense references avoid a hash-table lookup for every record and parent.
	// Fall back to a map for exceptionally sparse, very large MFTs.
	var dense []index.ID
	var ids map[uint64]index.ID
	if totalRecords <= 32_000_000 {
		dense = make([]index.ID, int(totalRecords))
		for i := range dense {
			dense[i] = index.None
		}
	} else {
		ids = make(map[uint64]index.ID, capacity)
	}
	put := func(ref uint64, id index.ID) {
		if dense != nil {
			dense[ref&refMask] = id
		} else {
			ids[ref] = id
		}
	}
	get := func(ref uint64) (index.ID, bool) {
		if dense != nil {
			n := ref & refMask
			if n >= uint64(len(dense)) {
				return 0, false
			}
			id := dense[n]
			return id, id != index.None && sequences[id] == uint16(ref>>48)
		}
		id, ok := ids[ref]
		return id, ok
	}
	var extensions []mftExtension
	var extNames []mftName
	var extNameBytes []byte
	rootRef := uint64(0)
	p.TotalRecords.Store(totalRecords)

	// Readers: each has its own handle and up to three chunks in flight.
	workers := max(1, min(len(pieces), mftReaders, runtime.NumCPU()))
	readCtx, cancel := context.WithCancel(ctx)
	free := make(chan *mftChunk, workers*3)
	for range workers * 3 {
		free <- &mftChunk{data: alignedBuffer(int(v.chunkBytes()), max(4096, int(v.sector)))}
	}
	work := make(chan *mftChunk)
	done := make(chan *mftChunk, workers*3)
	var group sync.WaitGroup
	var readers []*os.File
	defer func() {
		cancel()
		group.Wait()
		for _, f := range readers {
			if f != v.f {
				f.Close()
			}
		}
	}()
	for range workers {
		f, err := v.reader()
		if err != nil {
			return nil, err
		}
		readers = append(readers, f)
		group.Add(1)
		go func() {
			defer group.Done()
			for c := range work {
				if _, c.err = f.ReadAt(c.data[:c.length], c.physical); c.err == nil {
					c.parse(v)
				}
				select {
				case done <- c:
				case <-readCtx.Done():
					return
				}
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		defer close(work)
		for _, piece := range pieces {
			var c *mftChunk
			select {
			case c = <-free:
			case <-readCtx.Done():
				return
			}
			c.mftPiece = piece
			select {
			case work <- c:
			case <-readCtx.Done():
				return
			}
		}
	}()
	waiting := map[int]*mftChunk{}
	for next := 0; next < len(pieces); {
		select {
		case c := <-done:
			waiting[c.seq] = c
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		for c := waiting[next]; c != nil; c = waiting[next] {
			delete(waiting, next)
			next++
			if c.err != nil {
				return nil, c.err
			}
			for _, is := range c.issues {
				s.AddIssue(fmt.Sprintf("MFT #%d", is.number), is.err)
				p.Errors.Add(1)
			}
			if c.root != 0 {
				put(c.root, 0)
				sequences[0] = uint16(c.root >> 48)
				rootRef = c.root
			}
			extensions = append(extensions, c.extensions...)
			for _, n := range c.extNames {
				name := c.name(n.nameOff, n.nameLen)
				n.nameOff = uint32(len(extNameBytes))
				extNameBytes = append(extNameBytes, name...)
				extNames = append(extNames, n)
			}
			for i := range c.records {
				r := &c.records[i]
				name := c.name(r.nameOff, r.nameLen)
				id := s.Add(index.None, unsafe.String(unsafe.SliceData(name), len(name)), r.size, r.allocated, r.modified, r.flags)
				put(r.reference, id)
				sequences = append(sequences, uint16(r.reference>>48))
				parents = append(parents, r.parent)
				ranks = append(ranks, r.rank)
			}
			s.HardLinks += c.hardLinks
			p.Files.Add(c.files)
			p.Folders.Add(c.folders)
			p.Bytes.Add(c.bytes)
			p.Records.Store(c.first + uint64(c.length/v.record))
			free <- c
		}
	}
	p.Records.Store(totalRecords)
	if rootRef == 0 {
		return nil, fmt.Errorf("NTFS root record missing")
	}
	// Names kept in extension records, where the base record has none or
	// only a lesser one (a POSIX name where a Win32 name exists).
	for _, n := range extNames {
		if id, ok := get(n.base); ok && id != 0 && n.rank > ranks[id] {
			name := extNameBytes[n.nameOff : n.nameOff+uint32(n.nameLen)]
			if s.Rename(id, string(name)) == nil {
				parents[id], ranks[id] = n.parent, n.rank
			}
		}
	}
	if cap(s.Names) > len(s.Names)+len(s.Names)/8 {
		s.Names = append(make([]byte, 0, len(s.Names)), s.Names...)
	}
	for _, e := range extensions {
		if id, ok := get(e.base); ok && !s.Nodes[id].IsDir() {
			s.Nodes[id].Size += e.size
			s.Nodes[id].Allocated += e.allocated
		}
	}
	p.Phase.Store("Building directory index")
	// Records without any name are not entries (in use, but not listed in
	// any folder); they stay out of the tree. Their former children are
	// unresolved below, as before.
	// The cycle check's colors are set in this pass (2 = done, which includes removed).
	color := make([]byte, len(s.Nodes))
	color[0] = 2
	for i := 1; i < len(s.Nodes); i++ {
		if s.Nodes[i].NameLength == 0 {
			s.Nodes[i].Parent = index.None
			s.Nodes[i].Flags |= index.Removed
		}
		if s.Nodes[i].Flags&index.Removed != 0 {
			color[i] = 2
		}
	}
	// Validate parents by the full record reference, including sequence number.
	for i := 1; i < len(s.Nodes); i++ {
		if i%8192 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		n := &s.Nodes[i]
		if n.Flags&index.Removed != 0 {
			continue
		}
		parent, ok := get(parents[i])
		if !ok || !s.Nodes[parent].IsDir() || parent == index.ID(i) || s.Nodes[parent].Flags&index.Removed != 0 {
			n.Flags |= index.Incomplete | index.Unplaced
			parent = 0
			s.Unresolved++
		}
		n.Parent = parent
		n.NextSibling = s.Nodes[parent].FirstChild
		s.Nodes[parent].FirstChild = index.ID(i)
	}
	// Every node has one parent. Break malformed cycles before aggregation/path use.
	// Each chain walked up to a finished entry is then listed from its top
	// down, so order has every entry after its parent: sizes are then summed
	// in one backward pass along it.
	order := make([]index.ID, 1, len(s.Nodes))
	var chain []index.ID
	for i := 1; i < len(s.Nodes); i++ {
		id := index.ID(i)
		chain = chain[:0]
		for color[id] == 0 {
			color[id] = 1
			chain = append(chain, id)
			id = s.Nodes[id].Parent
		}
		if color[id] == 1 {
			return nil, fmt.Errorf("MFT parent cycle; using directory scan")
		}
		for k := len(chain) - 1; k >= 0; k-- {
			color[chain[k]] = 2
			order = append(order, chain[k])
		}
	}
	if s.Unresolved > 0 {
		s.AddIssue(root, fmt.Errorf("%d records have stale or unavailable parents; displayed at root, paths disabled", s.Unresolved))
	}
	p.Phase.Store("Aggregating sizes")
	s.FinalizeInOrder(order)
	return s, nil
}

func alignedBuffer(size, alignment int) []byte {
	b := make([]byte, size+alignment)
	offset := int((uintptr(alignment) - uintptr(unsafe.Pointer(&b[0]))%uintptr(alignment)) % uintptr(alignment))
	return b[offset : offset+size]
}
