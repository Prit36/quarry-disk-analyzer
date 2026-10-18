// Package ntfs parses NTFS 3.x records without pointers into reused I/O buffers.
// All offsets, fixup markers, attribute lengths and run boundaries are checked.
package ntfs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var le = binary.LittleEndian
var ErrRecord = errors.New("invalid NTFS record")

type Attribute struct {
	Type        uint32
	NonResident bool
	NameLength  byte
	Flags       uint16
	Data        []byte
}
type Record struct {
	Reference, Parent, Base uint64
	Name                    string
	// RawName is the chosen name as stored, UTF-16LE, borrowed from the
	// record buffer (ParseRaw leaves Name empty and sets only RawName).
	RawName []byte
	// NameRank orders the namespaces of the chosen name: 3 Win32 and DOS,
	// 2 Win32, 1 POSIX, 0 no name. DOS-only short names are never chosen.
	NameRank                          uint8
	Size, Allocated                   uint64
	Modified                          int64
	Links                             uint16
	Directory, Reparse, AttributeList bool
}

func Fixup(b []byte, sector int) error {
	if len(b) < 48 || string(b[:4]) != "FILE" || sector < 256 || len(b)%sector != 0 {
		return ErrRecord
	}
	off, count := int(le.Uint16(b[4:])), int(le.Uint16(b[6:]))
	if off < 8 || count != len(b)/sector+1 || off+count*2 > len(b) {
		return fmt.Errorf("%w: update sequence bounds", ErrRecord)
	}
	// Byte stores rather than copy: a two-byte copy costs a runtime call per sector.
	m0, m1 := b[off], b[off+1]
	for i := 1; i < count; i++ {
		end, src := i*sector-2, off+i*2
		if b[end] != m0 || b[end+1] != m1 {
			return fmt.Errorf("%w: torn sector", ErrRecord)
		}
		b[end], b[end+1] = b[src], b[src+1]
	}
	return nil
}

// Attributes expects an already-fixed record.
func Attributes(b []byte, visit func(Attribute) error) error {
	if len(b) < 48 {
		return ErrRecord
	}
	used := int(le.Uint32(b[24:]))
	off := int(le.Uint16(b[20:]))
	if used > len(b) || used < 48 || off < 48 || off >= used {
		return ErrRecord
	}
	for off+4 <= used {
		typ := le.Uint32(b[off:])
		if typ == 0xffffffff {
			return nil
		}
		if off+16 > used {
			return ErrRecord
		}
		length := int(le.Uint32(b[off+4:]))
		nonresident := b[off+8] != 0
		minimum := 24
		if nonresident {
			minimum = 64
		}
		if length < minimum || length%8 != 0 || length > used-off {
			return fmt.Errorf("%w: attribute bounds", ErrRecord)
		}
		a := Attribute{typ, nonresident, b[off+9], le.Uint16(b[off+12:]), b[off : off+length]}
		if err := visit(a); err != nil {
			return err
		}
		off += length
	}
	return ErrRecord
}
func (a Attribute) Value() ([]byte, error) {
	if a.NonResident || len(a.Data) < 24 {
		return nil, ErrRecord
	}
	length, off := int(le.Uint32(a.Data[16:])), int(le.Uint16(a.Data[20:]))
	if off < 24 || off > len(a.Data) || length > len(a.Data)-off {
		return nil, ErrRecord
	}
	return a.Data[off : off+length], nil
}
func Parse(b []byte, number uint64) (Record, bool, error) {
	r, inUse, err := ParseRaw(b, number)
	if len(r.RawName) > 0 {
		r.Name = decodeName(r.RawName)
	}
	return r, inUse, err
}

// decodeName converts UTF-16LE name bytes to a string. Plain ASCII, nearly
// every name, takes one allocation; anything else takes the general decoder.
func decodeName(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) / 2)
	for i := 0; i+1 < len(b); i += 2 {
		u := le.Uint16(b[i:])
		if u >= utf8.RuneSelf {
			units := make([]uint16, len(b)/2)
			for j := range units {
				units[j] = le.Uint16(b[j*2:])
			}
			return string(utf16.Decode(units))
		}
		sb.WriteByte(byte(u))
	}
	return sb.String()
}

// ParseRaw is Parse without decoding the name: it allocates nothing, and
// Record.RawName points into b.
func ParseRaw(b []byte, number uint64) (Record, bool, error) {
	if len(b) < 48 {
		return Record{}, false, ErrRecord
	}
	flags := le.Uint16(b[22:])
	if flags&1 == 0 {
		return Record{}, false, nil
	}
	r := Record{Reference: number | uint64(le.Uint16(b[16:]))<<48, Base: le.Uint64(b[32:]), Links: le.Uint16(b[18:]), Directory: flags&2 != 0}
	best := -1
	err := Attributes(b, func(a Attribute) error {
		switch a.Type {
		case 0x10:
			v, err := a.Value()
			if err != nil {
				return err
			}
			if len(v) < 36 {
				return ErrRecord
			}
			r.Modified = int64(le.Uint64(v[8:])/10000000) - 11644473600
			r.Reparse = le.Uint32(v[32:])&0x400 != 0
		case 0x20:
			r.AttributeList = true
		case 0x30:
			v, err := a.Value()
			if err != nil {
				return err
			}
			if len(v) < 66 {
				return ErrRecord
			}
			length := int(v[64])
			if length*2 > len(v)-66 {
				return ErrRecord
			}
			ns := v[65]
			rank := 0
			switch ns {
			case 0:
				rank = 1
			case 1:
				rank = 2
			case 3:
				rank = 3
			case 2:
				return nil
			default:
				return ErrRecord
			}
			if rank > best {
				r.RawName = v[66 : 66+length*2]
				r.Parent = le.Uint64(v)
				best = rank
				r.NameRank = uint8(rank)
			}
		case 0x80:
			if !a.NonResident {
				v, err := a.Value()
				if err != nil {
					return err
				}
				r.Size += uint64(len(v))
				return nil
			}
			if le.Uint64(a.Data[16:]) != 0 {
				return nil
			} // continuation of a stream
			size := le.Uint64(a.Data[48:])
			allocated := le.Uint64(a.Data[40:])
			if a.Flags&0x8001 != 0 {
				if len(a.Data) < 72 {
					return ErrRecord
				}
				allocated = le.Uint64(a.Data[64:])
			}
			// A sparse named stream's length describes address space, not
			// data: $BadClus:$Bad spans the whole volume and $UsnJrnl:$J
			// tens of gigabytes. Count what it occupies, as Explorer and
			// WizTree do; sparse main streams keep their logical length.
			if a.NameLength > 0 && a.Flags&0x8000 != 0 {
				size = min(size, allocated)
			}
			r.Size += size
			r.Allocated += allocated
		}
		return nil
	})
	return r, true, err
}

type Run struct{ VCN, LCN, Clusters int64 }

func Runs(a Attribute) ([]Run, error) {
	if !a.NonResident || len(a.Data) < 64 {
		return nil, ErrRecord
	}
	off := int(le.Uint16(a.Data[32:]))
	if off < 64 || off >= len(a.Data) {
		return nil, ErrRecord
	}
	vcn := int64(le.Uint64(a.Data[16:]))
	var lcn int64
	var runs []Run
	for off < len(a.Data) && a.Data[off] != 0 {
		header := a.Data[off]
		off++
		n, m := int(header&15), int(header>>4)
		if n == 0 || n > 8 || m > 8 || n+m > len(a.Data)-off {
			return nil, ErrRecord
		}
		var length, delta uint64
		for i := 0; i < n; i++ {
			length |= uint64(a.Data[off+i]) << uint(i*8)
		}
		off += n
		if length == 0 || length > math.MaxInt64 || vcn > math.MaxInt64-int64(length) {
			return nil, ErrRecord
		}
		for i := 0; i < m; i++ {
			delta |= uint64(a.Data[off+i]) << uint(i*8)
		}
		if m > 0 && a.Data[off+m-1]&128 != 0 && m < 8 {
			delta |= ^uint64(0) << uint(m*8)
		}
		off += m
		if m == 0 {
			runs = append(runs, Run{vcn, -1, int64(length)})
		} else {
			d := int64(delta)
			if (d > 0 && lcn > math.MaxInt64-d) || (d < 0 && lcn < math.MinInt64-d) {
				return nil, ErrRecord
			}
			lcn += d
			if lcn < 0 {
				return nil, ErrRecord
			}
			runs = append(runs, Run{vcn, lcn, int64(length)})
		}
		vcn += int64(length)
	}
	if off >= len(a.Data) || len(runs) == 0 {
		return nil, ErrRecord
	}
	if uint64(vcn-1) != le.Uint64(a.Data[24:]) {
		return nil, fmt.Errorf("%w: run length mismatch", ErrRecord)
	}
	return runs, nil
}
