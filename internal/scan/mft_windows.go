//go:build windows

package scan

import (
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
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
}

func openVolume(root string) (*volumeReader, error) {
	p, err := windows.UTF16PtrFromString(`\\.\` + strings.ToUpper(root[:2]))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("direct NTFS access unavailable (%w); run as administrator for MFT scanning", err)
	}
	f := os.NewFile(uintptr(h), root)
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
	v := &volumeReader{f: f, sector: int64(le.Uint32(data[40:])), cluster: int64(le.Uint32(data[44:])), record: int64(le.Uint32(data[48:])), length: int64(le.Uint64(data[56:])), start: int64(le.Uint64(data[64:]))}
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
	b := make([]byte, size)
	for _, run := range runs {
		if run.LCN < 0 || run.LCN > math.MaxInt64/v.cluster || run.VCN > math.MaxInt64/v.cluster || run.Clusters > math.MaxInt64/v.cluster {
			return nil, ntfs.ErrRecord
		}
		off := run.VCN * v.cluster
		if off >= size {
			break
		}
		n := min(run.Clusters*v.cluster, size-off)
		if _, err := v.f.ReadAt(b[off:off+n], run.LCN*v.cluster); err != nil {
			return nil, err
		}
	}
	return b, nil
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
		if binary.LittleEndian.Uint32(e) == 0x80 && e[6] == 0 {
			number := binary.LittleEndian.Uint64(e[16:]) & refMask
			if !seen[number] {
				seen[number] = true
				extension, err := v.recordFromRuns(runs, number)
				if err != nil {
					extension, err = v.recordByNumber(number)
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

func readMFT(ctx context.Context, root string, p *Progress, v *volumeReader) (*index.Snapshot, error) {
	runs, err := v.mftRuns()
	if err != nil {
		return nil, fmt.Errorf("MFT mapping: %w", err)
	}
	s := index.New(root)
	s.Engine = "NTFS · direct MFT"
	totalRecords := uint64(v.length / v.record)
	capacity := int(min(totalRecords, 1_000_000))
	rootNode := s.Nodes[0]
	s.Nodes = make([]index.Node, 1, max(1, capacity))
	s.Nodes[0] = rootNode
	s.Names = make([]byte, 0, capacity*20)
	parents := make([]uint64, 1, max(1, capacity))
	references := make([]uint64, 1, max(1, capacity))
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
			return id, id != index.None && references[id] == ref
		}
		id, ok := ids[ref]
		return id, ok
	}
	type extension struct{ base, size, allocated uint64 }
	var extensions []extension
	rootRef := uint64(0)
	p.TotalRecords.Store(uint64(v.length / v.record))
	buffer := alignedBuffer(8<<20, int(v.sector))
	processed := int64(0)
	for _, run := range runs {
		left := min(run.Clusters*v.cluster, v.length-processed)
		physical := run.LCN * v.cluster
		for left > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n := min(left, int64(len(buffer)))
			if n%v.record != 0 {
				return nil, fmt.Errorf("MFT record crosses unsupported run boundary")
			}
			if _, err := v.f.ReadAt(buffer[:n], physical); err != nil {
				return nil, err
			}
			for off := int64(0); off < n; off += v.record {
				number := uint64((processed + off) / v.record)
				b := buffer[off : off+v.record]
				if string(b[:4]) != "FILE" {
					continue
				}
				if err := ntfs.Fixup(b, int(v.sector)); err != nil {
					s.AddIssue(fmt.Sprintf("MFT #%d", number), err)
					p.Errors.Add(1)
					continue
				}
				r, inUse, err := ntfs.Parse(b, number)
				if err != nil {
					s.AddIssue(fmt.Sprintf("MFT #%d", number), err)
					p.Errors.Add(1)
					continue
				}
				if !inUse {
					continue
				}
				if r.Base != 0 {
					if r.Size > 0 || r.Allocated > 0 {
						extensions = append(extensions, extension{r.Base, r.Size, r.Allocated})
					}
					continue
				}
				if number == 5 {
					put(r.Reference, 0)
					references[0] = r.Reference
					rootRef = r.Reference
					continue
				}
				if r.Name == "" {
					continue
				}
				flags := uint16(0)
				if r.Directory {
					flags |= index.Directory
					r.Size, r.Allocated = 0, 0
					p.Folders.Add(1)
				} else {
					p.Files.Add(1)
					p.Bytes.Add(r.Size)
				}
				if r.Reparse {
					flags |= index.Reparse
				}
				if r.Links > 1 && !r.Directory {
					s.HardLinks += uint64(r.Links - 1)
				}
				id := s.Add(index.None, r.Name, r.Size, r.Allocated, r.Modified, flags)
				put(r.Reference, id)
				references = append(references, r.Reference)
				parents = append(parents, r.Parent)
			}
			processed += n
			left -= n
			physical += n
			p.Records.Store(uint64(processed / v.record))
		}
		if processed >= v.length {
			break
		}
	}
	if rootRef == 0 {
		return nil, fmt.Errorf("NTFS root record missing")
	}
	for _, e := range extensions {
		if id, ok := get(e.base); ok && !s.Nodes[id].IsDir() {
			s.Nodes[id].Size += e.size
			s.Nodes[id].Allocated += e.allocated
		}
	}
	p.Phase.Store("Building directory index")
	// Validate parents by the full record reference, including sequence number.
	for i := 1; i < len(s.Nodes); i++ {
		if i%8192 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		parent, ok := get(parents[i])
		if !ok || !s.Nodes[parent].IsDir() || parent == index.ID(i) {
			s.Nodes[i].Flags |= index.Incomplete | index.Unplaced
			parent = 0
			s.Unresolved++
		}
		n := &s.Nodes[i]
		n.Parent = parent
		n.NextSibling = s.Nodes[parent].FirstChild
		s.Nodes[parent].FirstChild = index.ID(i)
	}
	// Every node has one parent. Break malformed cycles before aggregation/path use.
	color := make([]byte, len(s.Nodes))
	color[0] = 2
	for i := 1; i < len(s.Nodes); i++ {
		id := index.ID(i)
		for color[id] == 0 {
			color[id] = 1
			id = s.Nodes[id].Parent
		}
		if color[id] == 1 {
			return nil, fmt.Errorf("MFT parent cycle; using directory scan")
		}
		for id = index.ID(i); color[id] == 1; {
			color[id] = 2
			id = s.Nodes[id].Parent
		}
	}
	if s.Unresolved > 0 {
		s.AddIssue(root, fmt.Errorf("%d records have stale or unavailable parents; displayed at root, paths disabled", s.Unresolved))
	}
	p.Phase.Store("Aggregating sizes")
	s.Finalize()
	return s, nil
}

func alignedBuffer(size, alignment int) []byte {
	b := make([]byte, size+alignment)
	offset := int((uintptr(alignment) - uintptr(unsafe.Pointer(&b[0]))%uintptr(alignment)) % uintptr(alignment))
	return b[offset : offset+size]
}
