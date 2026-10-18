package index

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Removed marks an entry detached from the tree by a file operation, an
// exclusion or a filter. Its ID stays valid but it is no longer reachable.
const Removed uint16 = 1 << 5

// Snapshots are shared with background workers and are never mutated in
// place. Every change produces a copy (Clone) that keeps IDs stable, so
// selections and history survive delete, rename and filtering.

// Clone copies the node table. The name arena is shared; its capacity is
// clipped so that renames append into a new array.
func (s *Snapshot) Clone() *Snapshot {
	c := *s
	c.Nodes = slices.Clone(s.Nodes)
	c.Names = s.Names[:len(s.Names):len(s.Names)]
	c.Issues = slices.Clone(s.Issues)
	return &c
}

// Reachable reports whether id is still part of the tree.
func (s *Snapshot) Reachable(id ID) bool {
	if id == None || int(id) >= len(s.Nodes) {
		return false
	}
	for steps := 0; id != None && steps < 4096; steps++ {
		if s.Nodes[id].Flags&Removed != 0 {
			return false
		}
		if id == 0 {
			return true
		}
		id = s.Nodes[id].Parent
	}
	return false
}

// Contains reports whether id is ancestor itself or lies below it.
func (s *Snapshot) Contains(ancestor, id ID) bool {
	for steps := 0; id != None && steps < 4096; steps++ {
		if id == ancestor {
			return true
		}
		id = s.Nodes[id].Parent
	}
	return false
}

// Remove detaches entries and subtracts their totals from every ancestor.
// Call it on a Clone.
func (s *Snapshot) Remove(ids ...ID) {
	for _, id := range ids {
		if id == 0 || !s.Reachable(id) {
			continue
		}
		n := s.Nodes[id]
		s.unlink(id)
		s.Nodes[id].Flags |= Removed
		files, dirs := n.Files, n.Dirs
		if n.IsDir() {
			dirs++
		}
		for p := n.Parent; p != None; p = s.Nodes[p].Parent {
			q := &s.Nodes[p]
			q.Size -= min(q.Size, n.Size)
			q.Allocated -= min(q.Allocated, n.Allocated)
			q.Files -= min(q.Files, files)
			q.Dirs -= min(q.Dirs, dirs)
		}
	}
}

// Restore re-attaches entries that Remove detached, under their original
// parents, and adds their totals back to every ancestor. Entries that are
// not detached, or whose parent is not reachable, are left alone. Call it
// on a Clone.
func (s *Snapshot) Restore(ids ...ID) {
	for _, id := range ids {
		if id == 0 || id == None || int(id) >= len(s.Nodes) || s.Nodes[id].Flags&Removed == 0 {
			continue
		}
		n := &s.Nodes[id]
		if !s.Reachable(n.Parent) {
			continue
		}
		n.Flags &^= Removed
		n.NextSibling = s.Nodes[n.Parent].FirstChild
		s.Nodes[n.Parent].FirstChild = id
		files, dirs := n.Files, n.Dirs
		if n.IsDir() {
			dirs++
		}
		for p := n.Parent; p != None; p = s.Nodes[p].Parent {
			q := &s.Nodes[p]
			q.Size += n.Size
			q.Allocated += n.Allocated
			q.Files += files
			q.Dirs += dirs
		}
	}
}

func (s *Snapshot) unlink(id ID) {
	parent := s.Nodes[id].Parent
	if parent == None {
		return
	}
	if s.Nodes[parent].FirstChild == id {
		s.Nodes[parent].FirstChild = s.Nodes[id].NextSibling
		return
	}
	for c := s.Nodes[parent].FirstChild; c != None; c = s.Nodes[c].NextSibling {
		if s.Nodes[c].NextSibling == id {
			s.Nodes[c].NextSibling = s.Nodes[id].NextSibling
			return
		}
	}
}

// Rename changes an entry's name in place. Call it on a Clone.
func (s *Snapshot) Rename(id ID, name string) error {
	if id == 0 || int(id) >= len(s.Nodes) {
		return fmt.Errorf("the scan root cannot be renamed")
	}
	if len(name) == 0 || len(name) > 65535 || uint64(len(s.Names))+uint64(len(name)) > uint64(^uint32(0)) {
		return fmt.Errorf("invalid name")
	}
	offset := len(s.Names)
	s.Names = append(s.Names, name...)
	s.Nodes[id].NameOffset = uint32(offset)
	s.Nodes[id].NameLength = uint16(len(name))
	return nil
}

// Prune removes every entry for which match returns true, without visiting
// below a removed folder. It returns the number of entries removed.
func (s *Snapshot) Prune(ctx context.Context, match func(id ID) bool) (int, error) {
	var drop []ID
	err := s.walk(ctx, 0, func(id ID) (bool, error) {
		if id != 0 && match(id) {
			drop = append(drop, id)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	s.Remove(drop...)
	return len(drop), nil
}

// FilterStats describes what an analysis filter hid.
type FilterStats struct {
	HiddenFiles, HiddenFolders uint64
	HiddenBytes                uint64
}

// Filtered returns a copy containing only files accepted by keepFile and
// folders not rejected by dropFolder. With isolate, folders left without any
// file are removed too. Totals are recomputed so proportions stay meaningful.
func (s *Snapshot) Filtered(ctx context.Context, keepFile func(ID) bool, dropFolder func(ID) bool, isolate bool) (*Snapshot, FilterStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, FilterStats{}, err
	}
	// Imported trees can put children under files. Keep their historical
	// aggregation semantics; normal scans use the single-pass folder walk.
	for i := range s.Nodes {
		if i&4095 == 0 && ctx.Err() != nil {
			return nil, FilterStats{}, ctx.Err()
		}
		if !s.Nodes[i].IsDir() && (i == 0 || s.Nodes[i].FirstChild != None) {
			return s.filteredGeneral(ctx, keepFile, dropFolder, isolate)
		}
	}
	if len(s.Nodes) == 0 {
		return s.filteredGeneral(ctx, keepFile, dropFolder, isolate)
	}
	c := s.Clone()
	stats := FilterStats{}
	if err := c.filterTree(ctx, 0, keepFile, dropFolder, isolate, nil, &stats); err != nil {
		return nil, stats, err
	}
	return c, stats, ctx.Err()
}

// filterTree rebuilds the subtree of top in c, a copy of the snapshot being
// filtered, and stores top's new totals; it neither drops nor attaches top
// itself. A folder child for which done reports ready was rebuilt (or
// dropped) separately and is attached as it is.
func (c *Snapshot) filterTree(ctx context.Context, root ID, keepFile func(ID) bool, dropFolder func(ID) bool, isolate bool, done func(ID) (ready, dropped bool), stats *FilterStats) error {
	type frame struct {
		id, next, last  ID
		size, allocated uint64
		files, dirs     uint32
		empty           bool
	}
	var local [64]frame
	stack := local[:1]
	stack[0] = frame{id: root, next: c.Nodes[root].FirstChild, last: None}
	visited := 0
	// Attach a retained child and add its already computed totals. The next
	// original sibling is saved in the parent frame before any link changes.
	attach := func(parent *frame, id ID) {
		n := &c.Nodes[id]
		if parent.last == None {
			c.Nodes[parent.id].FirstChild = id
		} else {
			c.Nodes[parent.last].NextSibling = id
		}
		parent.last = id
		parent.size += n.Size
		parent.allocated += n.Allocated
		if n.IsDir() {
			parent.files += n.Files
			parent.dirs += n.Dirs + 1
			parent.empty = parent.empty || n.Files == 0
		} else {
			parent.files++
		}
	}
	for len(stack) > 0 {
		if visited&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		top := &stack[len(stack)-1]
		if top.next != None {
			id := top.next
			n := &c.Nodes[id]
			top.next = n.NextSibling
			if n.IsDir() {
				if done != nil {
					if ready, dropped := done(id); ready {
						if !dropped {
							attach(top, id)
						}
						continue
					}
				}
				if dropFolder != nil && dropFolder(id) {
					n.Flags |= Removed
					stats.HiddenFolders += uint64(n.Dirs) + 1
					stats.HiddenFiles += uint64(n.Files)
					stats.HiddenBytes += n.Size
					continue
				}
				stack = append(stack, frame{id: id, next: n.FirstChild, last: None})
			} else if keepFile != nil && !keepFile(id) {
				n.Flags |= Removed
				stats.HiddenFiles++
				stats.HiddenBytes += n.Size
			} else {
				attach(top, id)
			}
			continue
		}
		if top.last == None {
			c.Nodes[top.id].FirstChild = None
		} else {
			c.Nodes[top.last].NextSibling = None
		}
		// An entirely empty subtree stays intact until its highest empty
		// ancestor is detached. This preserves stable IDs and removal flags.
		if isolate && top.empty && (top.files != 0 || top.id == 0) {
			last := None
			for id := c.Nodes[top.id].FirstChild; id != None; {
				if visited&4095 == 0 && ctx.Err() != nil {
					return ctx.Err()
				}
				visited++
				n := &c.Nodes[id]
				next := n.NextSibling
				if n.IsDir() && n.Files == 0 {
					n.Flags |= Removed
					stats.HiddenFolders += uint64(n.Dirs) + 1
					top.dirs -= n.Dirs + 1
					if last == None {
						c.Nodes[top.id].FirstChild = next
					} else {
						c.Nodes[last].NextSibling = next
					}
				} else {
					last = id
				}
				id = next
			}
		}
		n := &c.Nodes[top.id]
		n.Size, n.Allocated, n.Files, n.Dirs = top.size, top.allocated, top.files, top.dirs
		id := top.id
		stack = stack[:len(stack)-1]
		if len(stack) > 0 {
			attach(&stack[len(stack)-1], id)
		}
	}
	return ctx.Err()
}

func (s *Snapshot) filteredGeneral(ctx context.Context, keepFile func(ID) bool, dropFolder func(ID) bool, isolate bool) (*Snapshot, FilterStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, FilterStats{}, err
	}
	c := s.Clone()
	stats := FilterStats{}
	drop := make([]bool, len(c.Nodes))
	err := c.walk(ctx, 0, func(id ID) (bool, error) {
		if id == 0 {
			return true, nil
		}
		n := c.Nodes[id]
		if n.IsDir() {
			if dropFolder != nil && dropFolder(id) {
				drop[id] = true
				stats.HiddenFolders += uint64(n.Dirs) + 1
				stats.HiddenFiles += uint64(n.Files)
				stats.HiddenBytes += n.Size
				return false, nil
			}
			return true, nil
		}
		if keepFile != nil && !keepFile(id) {
			drop[id] = true
			stats.HiddenFiles++
			stats.HiddenBytes += n.Size
		}
		return true, nil
	})
	if err != nil {
		return nil, stats, err
	}
	if err := c.relink(ctx, drop); err != nil {
		return nil, stats, err
	}
	if err := c.reaggregate(ctx); err != nil {
		return nil, stats, err
	}
	if isolate {
		clear(drop)
		removed := uint64(0)
		err := c.walk(ctx, 0, func(id ID) (bool, error) {
			if id != 0 && c.Nodes[id].IsDir() && c.Nodes[id].Files == 0 {
				drop[id] = true
				removed += uint64(c.Nodes[id].Dirs) + 1
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			return nil, stats, err
		}
		if removed > 0 {
			stats.HiddenFolders += removed
			if err := c.relink(ctx, drop); err != nil {
				return nil, stats, err
			}
			if err := c.reaggregate(ctx); err != nil {
				return nil, stats, err
			}
		}
	}
	return c, stats, ctx.Err()
}

// relink rebuilds the child lists of reachable folders without dropped entries.
func (s *Snapshot) relink(ctx context.Context, drop []bool) error {
	stack := []ID{0}
	visited := 0
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		last := None
		for c := s.Nodes[dir].FirstChild; c != None; {
			if visited&4095 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			visited++
			next := s.Nodes[c].NextSibling
			if drop[c] {
				s.Nodes[c].Flags |= Removed
			} else {
				if last == None {
					s.Nodes[dir].FirstChild = c
				} else {
					s.Nodes[last].NextSibling = c
				}
				last = c
				if s.Nodes[c].IsDir() {
					stack = append(stack, c)
				}
			}
			c = next
		}
		if last == None {
			s.Nodes[dir].FirstChild = None
		} else {
			s.Nodes[last].NextSibling = None
		}
	}
	return ctx.Err()
}

// Reaggregate recomputes folder totals from the reachable files. Folders own
// no bytes in either scan engine; their totals are their children's.
func (s *Snapshot) Reaggregate() {
	_ = s.reaggregate(context.Background())
}

func (s *Snapshot) reaggregate(ctx context.Context) error {
	if len(s.Nodes) == 0 {
		return ctx.Err()
	}
	if !s.Nodes[0].IsDir() {
		return s.reaggregateAllContext(ctx)
	}
	// Accumulate into the current folder's frame, then write its node once.
	// Normal trees need only a small stack, rather than one ID per entry.
	type frame struct {
		id, next        ID
		size, allocated uint64
		files, dirs     uint32
	}
	var local [64]frame
	stack := local[:1]
	stack[0] = frame{id: 0, next: s.Nodes[0].FirstChild}
	visited := 0
	for len(stack) > 0 {
		if visited&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		top := &stack[len(stack)-1]
		if top.next == None {
			value := *top
			n := &s.Nodes[value.id]
			n.Size, n.Allocated, n.Files, n.Dirs = value.size, value.allocated, value.files, value.dirs
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				parent := &stack[len(stack)-1]
				parent.size += value.size
				parent.allocated += value.allocated
				parent.files += value.files
				parent.dirs += value.dirs + 1
			}
			continue
		}
		id := top.next
		n := &s.Nodes[id]
		top.next = n.NextSibling
		if n.IsDir() {
			if len(stack) == 256 {
				return s.reaggregateAllContext(ctx)
			}
			stack = append(stack, frame{id: id, next: n.FirstChild})
		} else if n.FirstChild != None {
			// Preserve pathologically deep and unusual imported trees with
			// the original algorithm. File values have not been modified.
			return s.reaggregateAllContext(ctx)
		} else {
			top.size += n.Size
			top.allocated += n.Allocated
			top.files++
		}
	}
	return ctx.Err()
}

func (s *Snapshot) reaggregateAll() {
	_ = s.reaggregateAllContext(context.Background())
}

func (s *Snapshot) reaggregateAllContext(ctx context.Context) error {
	order := make([]ID, 0, len(s.Nodes))
	err := s.walk(ctx, 0, func(id ID) (bool, error) {
		order = append(order, id)
		if s.Nodes[id].IsDir() {
			n := &s.Nodes[id]
			n.Size, n.Allocated, n.Files, n.Dirs = 0, 0, 0, 0
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	for i := len(order) - 1; i > 0; i-- {
		if i&4095 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		id := order[i]
		n := s.Nodes[id]
		p := &s.Nodes[n.Parent]
		p.Size += n.Size
		p.Allocated += n.Allocated
		if n.IsDir() {
			p.Files += n.Files
			p.Dirs += n.Dirs + 1
		} else {
			p.Files++
		}
	}
	return ctx.Err()
}

var ntfsMetadata = map[string]bool{"$mft": true, "$mftmirr": true, "$logfile": true, "$volume": true, "$attrdef": true, "$bitmap": true, "$boot": true, "$badclus": true, "$secure": true, "$upcase": true, "$extend": true, "$quota": true, "$objid": true, "$reparse": true}

// IsMetadata reports whether an entry is NTFS filesystem metadata (or lies
// inside $Extend), or a record whose real location could not be resolved.
// Such entries are never offered for deletion or renaming.
func (s *Snapshot) IsMetadata(id ID) bool {
	if id == None || int(id) >= len(s.Nodes) {
		return false
	}
	for n := id; n != None && n != 0; n = s.Nodes[n].Parent {
		if s.Nodes[n].Flags&Unplaced != 0 {
			return true
		}
		if s.Nodes[n].Parent == 0 {
			return strings.Contains(s.Engine, "MFT") && isMetadataName(s.NameView(n))
		}
	}
	return false
}

// isMetadataName looks a top-level name up without allocating: menus ask
// for every selected entry on every frame.
func isMetadataName(name string) bool {
	if len(name) == 0 || name[0] != '$' || len(name) > 16 {
		return false
	}
	var lower [16]byte
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		lower[i] = b
	}
	return ntfsMetadata[string(lower[:len(name)])]
}

// Compact returns a copy without removed entries, with renumbered IDs and a
// fresh name arena, suitable for saving.
func (s *Snapshot) Compact() *Snapshot {
	c := *s
	c.Nodes = make([]Node, 0, len(s.Nodes))
	c.Names = make([]byte, 0, len(s.Names))
	remap := make(map[ID]ID, len(s.Nodes))
	_ = s.walk(context.Background(), 0, func(id ID) (bool, error) {
		n := s.Nodes[id]
		name := s.NameView(id)
		parent := None
		if id != 0 {
			parent = remap[n.Parent]
		}
		remap[id] = ID(len(c.Nodes))
		n.Parent, n.FirstChild, n.NextSibling = parent, None, None
		n.NameOffset, n.NameLength = uint32(len(c.Names)), uint16(len(name))
		c.Names = append(c.Names, name...)
		c.Nodes = append(c.Nodes, n)
		return true, nil
	})
	// Relink children in their original order.
	last := make([]ID, len(c.Nodes))
	for i := range last {
		last[i] = None
	}
	for i := 1; i < len(c.Nodes); i++ {
		p := c.Nodes[i].Parent
		if last[p] == None {
			c.Nodes[p].FirstChild = ID(i)
		} else {
			c.Nodes[last[p]].NextSibling = ID(i)
		}
		last[p] = ID(i)
	}
	return &c
}

func (s *Snapshot) hasRemoved() bool {
	for i := range s.Nodes {
		if s.Nodes[i].Flags&Removed != 0 {
			return true
		}
	}
	return false
}
