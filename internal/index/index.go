// Package index stores a scan as flat, pointer-free records and a UTF-8 name arena.
// Paths are reconstructed on demand instead of retained once per file.
package index

import (
	"bufio"
	"cmp"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

type ID uint32

const None ID = ^ID(0)
const (
	Directory uint16 = 1 << iota
	Reparse
	Shared
	Incomplete
	Unplaced
)

// Node is 48 bytes, with no GC-scanned pointers.
type Node struct {
	Size, Allocated uint64
	// Modified is in Unix seconds, 0 when unknown: 32 bits cover 1970 to
	// 2106 and keep the node at 48 bytes instead of 56. Earlier times are
	// stored as the first second of 1970. ModTime returns it as int64.
	Modified                        uint32
	Parent, FirstChild, NextSibling ID
	NameOffset                      uint32
	NameLength                      uint16
	Flags                           uint16
	Files, Dirs                     uint32
}

func (n Node) IsDir() bool { return n.Flags&Directory != 0 }

// ModTime is the modification time in Unix seconds, 0 when unknown.
func (n Node) ModTime() int64 { return int64(n.Modified) }

// unixSeconds stores a Unix time in a Node's 32 bits, clamped.
func unixSeconds(t int64) uint32 {
	switch {
	case t == 0:
		return 0
	case t < 0:
		return 1
	case t > math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(t)
}

type Issue struct{ Path, Message string }
type Snapshot struct {
	Root                                        string
	Nodes                                       []Node
	Names                                       []byte
	Issues                                      []Issue // bounded samples; Errors retains the full count
	Errors, SkippedLinks, HardLinks, Unresolved uint64
	Engine, Fallback                            string
	Started                                     time.Time
	Elapsed                                     time.Duration
	Cancelled                                   bool
	// Exclusions lists the scan exclusion patterns in effect; Excluded counts
	// the entries they kept out. Totals are then incomplete by design.
	Exclusions []string
	Excluded   uint64
}

func New(root string) *Snapshot {
	s := &Snapshot{Root: root, Started: time.Now()}
	s.Add(None, filepath.Base(root), 0, 0, 0, Directory)
	return s
}
func (s *Snapshot) Add(parent ID, name string, size, allocated uint64, modified int64, flags uint16) ID {
	if uint64(len(s.Nodes)) >= uint64(None) || uint64(len(s.Names))+uint64(len(name)) > uint64(^uint32(0)) || len(name) > 65535 {
		panic("scan index exceeds 32-bit arena limits")
	}
	id := ID(len(s.Nodes))
	n := Node{Size: size, Allocated: allocated, Modified: unixSeconds(modified), Parent: parent, FirstChild: None, NextSibling: None, NameOffset: uint32(len(s.Names)), NameLength: uint16(len(name)), Flags: flags}
	if parent != None {
		n.NextSibling = s.Nodes[parent].FirstChild
		s.Nodes[parent].FirstChild = id
	}
	s.Names = append(s.Names, name...)
	s.Nodes = append(s.Nodes, n)
	return id
}
func (s *Snapshot) Name(id ID) string {
	n := s.Nodes[id]
	return string(s.Names[n.NameOffset : uint32(n.NameOffset)+uint32(n.NameLength)])
}

// NameView borrows immutable arena memory. The snapshot must be frozen first.
func (s *Snapshot) NameView(id ID) string {
	n := s.Nodes[id]
	if n.NameLength == 0 {
		return ""
	}
	return unsafe.String(&s.Names[n.NameOffset], int(n.NameLength))
}

// Path builds an entry's full path. Ordinary paths are measured first and
// filled in from the end, in one allocation; anything filepath.Join would
// normalize takes the general route.
func (s *Snapshot) Path(id ID) string {
	if id == 0 {
		return s.Root
	}
	b, ok := s.appendRegularPath(nil, id)
	if !ok {
		return s.joinPath(id)
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// AppendPath appends Path(id) into caller-owned storage. Batch consumers can
// reuse their scratch buffer instead of allocating a string per folder.
func (s *Snapshot) AppendPath(dst []byte, id ID) []byte {
	if id == 0 {
		return append(dst, s.Root...)
	}
	if b, ok := s.appendRegularPath(dst, id); ok {
		return b
	}
	return append(dst, s.joinPath(id)...)
}

func (s *Snapshot) appendRegularPath(dst []byte, id ID) ([]byte, bool) {
	size, depth := 0, 0
	for cur := id; cur != 0; depth++ {
		if cur == None || depth >= len(s.Nodes) || s.Nodes[cur].Flags&Unplaced != 0 {
			return nil, false
		}
		name := s.NameView(cur)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/`) {
			return nil, false
		}
		size += len(name) + 1
		cur = s.Nodes[cur].Parent
	}
	root := s.Root
	if !cleanRoot(root) {
		return nil, false
	}
	if strings.HasSuffix(root, `\`) {
		size--
	}
	start := len(dst)
	dst = append(dst, make([]byte, len(root)+size)...)
	b := dst[start:]
	copy(b, root)
	end := len(b)
	for cur := id; cur != 0; cur = s.Nodes[cur].Parent {
		name := s.NameView(cur)
		end -= len(name)
		copy(b[end:], name)
		if end > len(root) {
			end--
			b[end] = '\\'
		}
	}
	if end != len(root) && end != len(root)+1 {
		return nil, false
	}
	if end == len(root)+1 {
		b[len(root)] = '\\'
	}
	return dst, true
}

// cleanRoot reports whether filepath.Join leaves root and the separator
// after it as they are: a cleaned Windows path, as scans record.
func cleanRoot(root string) bool {
	if len(root) < 3 || root[1] != ':' || root[2] != '\\' || strings.Contains(root, "/") || strings.Contains(root[2:], `\\`) || strings.HasSuffix(root, `\.`) || strings.Contains(root, `\.\`) || strings.Contains(root, `\..`) {
		return false
	}
	return len(root) == 3 || !strings.HasSuffix(root, `\`) && !strings.HasSuffix(root, " ")
}

func (s *Snapshot) joinPath(id ID) string {
	parts := make([]string, 0, 16)
	unplaced := false
	for steps := 0; id != 0 && id != None && steps < len(s.Nodes); steps++ {
		unplaced = unplaced || s.Nodes[id].Flags&Unplaced != 0
		parts = append(parts, s.NameView(id))
		id = s.Nodes[id].Parent
	}
	slices.Reverse(parts)
	if unplaced {
		return filepath.Join("[unresolved metadata]", filepath.Join(parts...))
	}
	return filepath.Join(s.Root, filepath.Join(parts...))
}
func (s *Snapshot) AddIssue(path string, err error) {
	s.Errors++
	if len(s.Issues) < 100 {
		s.Issues = append(s.Issues, Issue{path, err.Error()})
	}
}

// Finalize aggregates in O(n). When every entry comes after its parent, as
// a directory scan adds them, one backward pass does it in place; otherwise
// a leaf queue handles MFT records arriving before their parents. Neither
// recurses on adversarially deep trees.
func (s *Snapshot) Finalize() {
	if !s.aggregateOrdered() {
		s.aggregateQueue()
	}
	s.Elapsed = time.Since(s.Started)
}

// aggregateOrdered sums every entry into its parent walking backwards, so a
// folder is complete before it is added to its own parent. It reports false,
// having changed only the counts aggregateQueue resets, when some entry
// precedes its parent.
func (s *Snapshot) aggregateOrdered() bool {
	nodes := s.Nodes
	for i := range nodes {
		n := &nodes[i]
		if n.Parent != None && n.Parent >= ID(i) {
			return false
		}
		n.Files, n.Dirs = 1, 0
		if n.IsDir() {
			n.Files = 0
		}
	}
	for i := len(nodes) - 1; i > 0; i-- {
		n := &nodes[i]
		if n.Parent == None {
			continue
		}
		p := &nodes[n.Parent]
		p.Size += n.Size
		p.Allocated += n.Allocated
		p.Files += n.Files
		p.Dirs += n.Dirs
		if n.IsDir() {
			p.Dirs++
		}
	}
	return true
}

// FinalizeInOrder aggregates like Finalize, along order: the root first,
// then every placed entry after its parent. Entries left out of it (those
// without a parent) keep their own counts and are not added anywhere.
func (s *Snapshot) FinalizeInOrder(order []ID) {
	nodes := s.Nodes
	for i := range nodes {
		n := &nodes[i]
		n.Files, n.Dirs = 1, 0
		if n.IsDir() {
			n.Files = 0
		}
	}
	for k := len(order) - 1; k > 0; k-- {
		n := &nodes[order[k]]
		if n.Parent == None {
			continue
		}
		p := &nodes[n.Parent]
		p.Size += n.Size
		p.Allocated += n.Allocated
		p.Files += n.Files
		p.Dirs += n.Dirs
		if n.IsDir() {
			p.Dirs++
		}
	}
	s.Elapsed = time.Since(s.Started)
}

func (s *Snapshot) aggregateQueue() {
	remaining := make([]uint32, len(s.Nodes))
	queue := make([]ID, 0, len(s.Nodes))
	for i := range s.Nodes {
		n := &s.Nodes[i]
		n.Files, n.Dirs = 0, 0
		if n.IsDir() {
			n.Dirs = 1
		} else {
			n.Files = 1
		}
		if n.Parent != None {
			remaining[n.Parent]++
		}
	}
	for i, v := range remaining {
		if v == 0 {
			queue = append(queue, ID(i))
		}
	}
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		n := s.Nodes[id]
		if n.Parent == None {
			continue
		}
		p := &s.Nodes[n.Parent]
		p.Size += n.Size
		p.Allocated += n.Allocated
		p.Files += n.Files
		p.Dirs += n.Dirs
		remaining[n.Parent]--
		if remaining[n.Parent] == 0 {
			queue = append(queue, n.Parent)
		}
	}
	if len(queue) != len(s.Nodes) {
		s.AddIssue(s.Root, fmt.Errorf("invalid parent cycle: %d records excluded", len(s.Nodes)-len(queue)))
	}
	for i := range s.Nodes {
		if s.Nodes[i].IsDir() && s.Nodes[i].Dirs > 0 {
			s.Nodes[i].Dirs--
		}
	}
}
func (s *Snapshot) Children(id ID) []ID {
	var ids []ID
	for child := s.Nodes[id].FirstChild; child != None; child = s.Nodes[child].NextSibling {
		ids = append(ids, child)
	}
	return ids
}

type Sort struct {
	Column    string
	Desc      bool
	Allocated bool
}

func (s *Snapshot) Sort(ids []ID, by Sort) {
	slices.SortFunc(ids, func(a, b ID) int { return s.Compare(a, b, by) })
}

func (s *Snapshot) Compare(a, b ID, by Sort) int {
	x, y := s.Nodes[a], s.Nodes[b]
	var v int
	switch by.Column {
	case "Name":
		v = compareFold(s.NameView(a), s.NameView(b))
	case "Modified":
		v = cmp.Compare(x.Modified, y.Modified)
	case "Files":
		v = cmp.Compare(x.Files, y.Files)
	case "Folders":
		v = cmp.Compare(x.Dirs, y.Dirs)
	case "% of Parent":
		share := func(node Node) float64 {
			parent := node.Parent
			if parent == None {
				return 1
			}
			value, total := node.Size, s.Nodes[parent].Size
			if by.Allocated {
				value, total = node.Allocated, s.Nodes[parent].Allocated
			}
			if total == 0 {
				return 0
			}
			return float64(value) / float64(total)
		}
		v = cmp.Compare(share(x), share(y))
	case "Allocated":
		v = cmp.Compare(x.Allocated, y.Allocated)
	case "Size":
		v = cmp.Compare(x.Size, y.Size)
	default:
		if by.Allocated {
			v = cmp.Compare(x.Allocated, y.Allocated)
		} else {
			v = cmp.Compare(x.Size, y.Size)
		}
	}
	if by.Desc {
		v = -v
	}
	if v == 0 {
		v = cmp.Compare(a, b)
	}
	return v
}

func compareFold(a, b string) int {
	// Duplicate filenames are common across package and system folders.
	// The runtime compares equal byte strings in wide chunks, avoiding a
	// byte-by-byte case fold before falling through to the ID tie breaker.
	if a == b {
		return 0
	}
	for i := 0; i < min(len(a), len(b)); i++ {
		x, y := a[i], b[i]
		if x >= 128 || y >= 128 {
			return strings.Compare(strings.ToLower(a), strings.ToLower(b))
		}
		if x >= 'A' && x <= 'Z' {
			x += 32
		}
		if y >= 'A' && y <= 'Z' {
			y += 32
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return cmp.Compare(len(a), len(b))
}
func (s *Snapshot) Bytes() uint64 {
	return uint64(cap(s.Nodes))*uint64(unsafe.Sizeof(Node{})) + uint64(cap(s.Names))
}
func (s *Snapshot) Export(ctx context.Context, w io.Writer) error {
	return s.ExportScope(ctx, w, 0)
}

func (s *Snapshot) ExportScope(ctx context.Context, w io.Writer, root ID) error {
	return s.exportRecords(w, s.newPathWalker().path, func(visit func(ID) error) error { return s.Visit(ctx, root, visit) })
}

func (s *Snapshot) ExportRows(ctx context.Context, w io.Writer, rows []Row) error {
	return s.exportRecords(w, s.Path, func(visit func(ID) error) error {
		for position, row := range rows {
			if position&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if row.ID == None || int(row.ID) >= len(s.Nodes) {
				return fmt.Errorf("invalid export record")
			}
			if err := visit(row.ID); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

// exportRecords writes one CSV row per visited entry. Fields are formatted
// into one reused buffer and passed as views of it (the CSV writer copies
// them), so a row allocates nothing.
func (s *Snapshot) exportRecords(w io.Writer, path func(ID) string, visit func(func(ID) error) error) error {
	buffered := bufio.NewWriterSize(w, 1<<20)
	c := csv.NewWriter(buffered)
	if err := c.Write([]string{"Path", "Type", "Size", "Allocated", "Files", "Folders", "Modified", "Shared", "Incomplete"}); err != nil {
		return err
	}
	row := make([]string, 9)
	scratch := make([]byte, 0, 128)
	field := func(start int) string {
		return unsafe.String(unsafe.SliceData(scratch[start:]), len(scratch)-start)
	}
	err := visit(func(id ID) error {
		n := s.Nodes[id]
		row[0] = path(id)
		row[1] = "file"
		if n.IsDir() {
			row[1] = "folder"
		}
		scratch = scratch[:0]
		start := len(scratch)
		scratch = strconv.AppendUint(scratch, n.Size, 10)
		row[2] = field(start)
		start = len(scratch)
		scratch = strconv.AppendUint(scratch, n.Allocated, 10)
		row[3] = field(start)
		start = len(scratch)
		scratch = strconv.AppendUint(scratch, uint64(n.Files), 10)
		row[4] = field(start)
		start = len(scratch)
		scratch = strconv.AppendUint(scratch, uint64(n.Dirs), 10)
		row[5] = field(start)
		row[6] = ""
		if n.Modified != 0 {
			start = len(scratch)
			scratch = time.Unix(n.ModTime(), 0).UTC().AppendFormat(scratch, time.RFC3339)
			row[6] = field(start)
		}
		row[7] = strconv.FormatBool(n.Flags&Shared != 0)
		row[8] = strconv.FormatBool(n.Flags&Incomplete != 0)
		return c.Write(row)
	})
	if err != nil {
		return err
	}
	c.Flush()
	if err := c.Error(); err != nil {
		return err
	}
	return buffered.Flush()
}
