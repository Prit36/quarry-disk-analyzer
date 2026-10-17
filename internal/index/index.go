// Package index stores a scan as flat, pointer-free records and a UTF-8 name arena.
// Paths are reconstructed on demand instead of retained once per file.
package index

import (
	"cmp"
	"context"
	"encoding/csv"
	"fmt"
	"io"
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

// Node is 56 bytes on 64-bit targets, with no GC-scanned pointers.
type Node struct {
	Size, Allocated                 uint64
	Modified                        int64
	Parent, FirstChild, NextSibling ID
	NameOffset                      uint32
	NameLength                      uint16
	Flags                           uint16
	Files, Dirs                     uint32
}

func (n Node) IsDir() bool { return n.Flags&Directory != 0 }

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
	n := Node{Size: size, Allocated: allocated, Modified: modified, Parent: parent, FirstChild: None, NextSibling: None, NameOffset: uint32(len(s.Names)), NameLength: uint16(len(name)), Flags: flags}
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
func (s *Snapshot) Path(id ID) string {
	if id == 0 {
		return s.Root
	}
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

// Finalize aggregates in O(n) using a leaf queue. It handles MFT records arriving
// before their parents and does not recurse on adversarially deep trees.
func (s *Snapshot) Finalize() {
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
	s.Elapsed = time.Since(s.Started)
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

func (s *Snapshot) SortContext(ctx context.Context, ids []ID, by Sort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	comparisons := 0
	canceled := false
	slices.SortFunc(ids, func(first, second ID) int {
		comparisons++
		if comparisons&4095 == 0 && ctx.Err() != nil {
			canceled = true
		}
		if canceled {
			return 0
		}
		return s.Compare(first, second, by)
	})
	return ctx.Err()
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
	return s.exportRecords(w, func(visit func(ID) error) error { return s.Visit(ctx, root, visit) })
}

func (s *Snapshot) ExportRows(ctx context.Context, w io.Writer, rows []Row) error {
	return s.exportRecords(w, func(visit func(ID) error) error {
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

func (s *Snapshot) exportRecords(w io.Writer, visit func(func(ID) error) error) error {
	c := csv.NewWriter(w)
	if err := c.Write([]string{"Path", "Type", "Size", "Allocated", "Files", "Folders", "Modified", "Shared", "Incomplete"}); err != nil {
		return err
	}
	err := visit(func(id ID) error {
		n := s.Nodes[id]
		kind := "file"
		if n.IsDir() {
			kind = "folder"
		}
		modified := ""
		if n.Modified != 0 {
			modified = time.Unix(n.Modified, 0).UTC().Format(time.RFC3339)
		}
		return c.Write([]string{s.Path(id), kind, strconv.FormatUint(n.Size, 10), strconv.FormatUint(n.Allocated, 10), strconv.FormatUint(uint64(n.Files), 10), strconv.FormatUint(uint64(n.Dirs), 10), modified, strconv.FormatBool(n.Flags&Shared != 0), strconv.FormatBool(n.Flags&Incomplete != 0)})
	})
	if err != nil {
		return err
	}
	c.Flush()
	return c.Error()
}
