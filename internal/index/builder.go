package index

import (
	"encoding/binary"
	"path/filepath"
	"runtime"
	"slices"
	"time"
	"unicode/utf8"
)

const (
	nodeChunk = 1 << 15 // 32,768 nodes (1.75 MiB) per chunk
	nameChunk = 1 << 20
)

// Builder grows an index whose final size is unknown (a directory scan) in
// fixed-size chunks. Appending never copies or over-allocates what is
// already there, unlike slice growth, which reallocates about every 25%
// and keeps the old and new arrays alive together. Build moves the
// chunks into exactly sized arrays once.
//
// The embedded Snapshot carries the scan's header fields (Root, Engine,
// counters, issues); its Nodes and Names stay empty until Build.
type Builder struct {
	*Snapshot
	nodes [][]Node
	count int
	names [][]byte
	bytes uint64
}

func NewBuilder(root string) *Builder {
	b := &Builder{Snapshot: &Snapshot{Root: root, Started: time.Now()}}
	b.Add(None, filepath.Base(root), 0, 0, 0, Directory)
	return b
}

func (b *Builder) Len() int { return b.count }

// Node returns the stored node; the pointer is valid until Build.
func (b *Builder) Node(id ID) *Node { return &b.nodes[id/nodeChunk][id%nodeChunk] }

func (b *Builder) Add(parent ID, name string, size, allocated uint64, modified int64, flags uint16) ID {
	if uint64(b.count) >= uint64(None) || b.bytes+uint64(len(name)) > uint64(^uint32(0)) || len(name) > 65535 {
		panic("scan index exceeds 32-bit arena limits")
	}
	id := ID(b.count)
	if b.count%nodeChunk == 0 {
		b.nodes = append(b.nodes, make([]Node, 0, nodeChunk))
	}
	n := Node{Size: size, Allocated: allocated, Modified: unixSeconds(modified), Parent: parent, FirstChild: None, NextSibling: None, NameOffset: uint32(b.bytes), NameLength: uint16(len(name)), Flags: flags}
	if parent != None {
		p := b.Node(parent)
		n.NextSibling = p.FirstChild
		p.FirstChild = id
	}
	last := &b.nodes[len(b.nodes)-1]
	*last = append(*last, n)
	b.count++
	for len(name) > 0 {
		if len(b.names) == 0 || len(b.names[len(b.names)-1]) == nameChunk {
			b.names = append(b.names, make([]byte, 0, nameChunk))
		}
		chunk := &b.names[len(b.names)-1]
		k := min(len(name), nameChunk-len(*chunk))
		*chunk = append(*chunk, name[:k]...)
		name = name[k:]
		b.bytes += uint64(k)
	}
	return id
}

// Name copies an entry's name out of the chunked arena.
func (b *Builder) Name(id ID) string {
	n := b.Node(id)
	out := make([]byte, 0, n.NameLength)
	for off, end := uint64(n.NameOffset), uint64(n.NameOffset)+uint64(n.NameLength); off < end; {
		chunk := b.names[off/nameChunk]
		start := off % nameChunk
		k := min(end-off, uint64(len(chunk))-start)
		out = append(out, chunk[start:start+k]...)
		off += k
	}
	return string(out)
}

// Path builds an entry's path while scanning; it is meant for the rare
// entries that need one (errors, path exclusions, fallback opens).
func (b *Builder) Path(id ID) string {
	var parts []string
	for ; id != 0 && id != None; id = b.Node(id).Parent {
		parts = append(parts, b.Name(id))
	}
	slices.Reverse(parts)
	return filepath.Join(b.Root, filepath.Join(parts...))
}

// Build assembles the index into exactly sized arrays and releases the
// chunks. The Builder must not be used afterwards.
func (b *Builder) Build() *Snapshot {
	s := b.Snapshot
	s.Nodes = make([]Node, 0, b.count)
	for i, chunk := range b.nodes {
		s.Nodes = append(s.Nodes, chunk...)
		b.nodes[i] = nil
	}
	// Reclaim the node chunks before the names are copied, so the index
	// never exists three times over. The chunks hold no pointers: the
	// collection only marks the scan's small pointer graph.
	if b.count > nodeChunk {
		runtime.GC()
	}
	s.Names = make([]byte, 0, b.bytes)
	for i, chunk := range b.names {
		s.Names = append(s.Names, chunk...)
		b.names[i] = nil
	}
	b.nodes, b.names, b.Snapshot = nil, nil, nil
	return s
}

// AppendUTF16 appends little-endian UTF-16 text (as stored by NTFS and
// returned by Windows directory queries) to dst as UTF-8, without
// intermediate allocations. Unpaired surrogates become U+FFFD, as with
// unicode/utf16.Decode.
func AppendUTF16(dst []byte, b []byte) []byte {
	needed := len(b) / 2
	if cap(dst)-len(dst) < needed {
		dst = slices.Grow(dst, needed)
	}
	for i := 0; i+1 < len(b); i += 2 {
		low, high := b[i], b[i+1]
		if high == 0 && low < utf8.RuneSelf {
			dst = append(dst, low)
			continue
		}
		u := rune(binary.LittleEndian.Uint16(b[i:]))
		switch {
		case u < utf8.RuneSelf:
			dst = append(dst, byte(u))
		case u < 0xd800 || u >= 0xe000:
			dst = utf8.AppendRune(dst, u)
		case u < 0xdc00 && i+3 < len(b):
			if v := rune(binary.LittleEndian.Uint16(b[i+2:])); v >= 0xdc00 && v < 0xe000 {
				dst = utf8.AppendRune(dst, (u-0xd800)<<10|(v-0xdc00)+0x10000)
				i += 2
			} else {
				dst = utf8.AppendRune(dst, utf8.RuneError)
			}
		default:
			dst = utf8.AppendRune(dst, utf8.RuneError)
		}
	}
	return dst
}
