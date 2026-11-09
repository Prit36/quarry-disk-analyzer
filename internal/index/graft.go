package index

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Graft returns a copy of s in which the folder target holds what fresh,
// a new scan of that same folder, found: target's previous contents are
// detached (as Remove leaves them) and fresh's entries are appended with
// new IDs, so every other ID, and with it selections and history, stays
// valid. Ancestors' totals change by the difference.
//
// Hard links are counted once within fresh only: a file linked from both
// inside and outside target is counted on each side until a full rescan.
func (s *Snapshot) Graft(target ID, fresh *Snapshot) (*Snapshot, error) {
	if target == None || int(target) >= len(s.Nodes) || !s.Reachable(target) {
		return nil, fmt.Errorf("the folder is no longer part of the scan")
	}
	if !s.Nodes[target].IsDir() {
		return nil, fmt.Errorf("only folders can be refreshed")
	}
	if fresh == nil || len(fresh.Nodes) == 0 || !fresh.Nodes[0].IsDir() {
		return nil, fmt.Errorf("the new scan is empty")
	}
	if uint64(len(s.Nodes))+uint64(len(fresh.Nodes)) >= uint64(None) || uint64(len(s.Names))+uint64(len(fresh.Names)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("the scan index would exceed its limits; rescan the whole folder instead")
	}
	// One copy of the node table, sized for both, rather than Clone and
	// then a second, larger copy: at ten million entries each copy is
	// half a gigabyte.
	c := *s
	c.Issues = slices.Clone(s.Issues)
	nodes := make([]Node, len(s.Nodes), len(s.Nodes)+len(fresh.Nodes)-1)
	copy(nodes, s.Nodes)
	old := nodes[target]

	// Detach the previous contents. Their records stay, unreachable.
	for k := old.FirstChild; k != None; k = nodes[k].NextSibling {
		nodes[k].Flags |= Removed
	}
	// Append the new entries below target, renumbered past the end. Fresh
	// IDs are dense from 0 (its root), so new = base + id - 1.
	base := ID(len(nodes))
	remap := func(id ID) ID {
		switch id {
		case None:
			return None
		case 0:
			return target
		}
		return base + id - 1
	}
	names := make([]byte, 0, len(s.Names)+len(fresh.Names))
	names = append(names, s.Names...)
	nameBase := uint32(len(s.Names))
	for i := 1; i < len(fresh.Nodes); i++ {
		n := fresh.Nodes[i]
		if n.Flags&Removed != 0 {
			// Keep numbering dense; the record stays detached.
			n.FirstChild = None
		}
		n.Parent, n.FirstChild, n.NextSibling = remap(n.Parent), remap(n.FirstChild), remap(n.NextSibling)
		n.NameOffset += nameBase
		nodes = append(nodes, n)
	}
	names = append(names, fresh.Names...)
	c.Nodes, c.Names = nodes, names

	t := &c.Nodes[target]
	root := fresh.Nodes[0]
	t.FirstChild = remap(root.FirstChild)
	t.Flags = t.Flags&^Incomplete | root.Flags&Incomplete
	if root.Modified != 0 {
		t.Modified = root.Modified
	}
	t.Size, t.Allocated, t.Files, t.Dirs = root.Size, root.Allocated, root.Files, root.Dirs
	for p := t.Parent; p != None; p = c.Nodes[p].Parent {
		q := &c.Nodes[p]
		q.Size = q.Size - min(q.Size, old.Size) + root.Size
		q.Allocated = q.Allocated - min(q.Allocated, old.Allocated) + root.Allocated
		q.Files = q.Files - min(q.Files, old.Files) + root.Files
		q.Dirs = q.Dirs - min(q.Dirs, old.Dirs) + root.Dirs
	}

	// Issues found below target in the earlier scan are replaced by the
	// new scan's. The sampled list cannot say how many of the counted
	// errors lay below target beyond the sample; those stay counted.
	prefix := strings.TrimSuffix(filepath.Clean(s.Path(target)), `\`) + `\`
	kept := c.Issues[:0]
	dropped := uint64(0)
	for _, is := range c.Issues {
		p := filepath.Clean(is.Path)
		if strings.EqualFold(p, prefix[:len(prefix)-1]) || len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix) {
			dropped++
			continue
		}
		kept = append(kept, is)
	}
	c.Issues = kept
	c.Errors = c.Errors - min(c.Errors, dropped) + fresh.Errors
	for _, is := range fresh.Issues {
		if len(c.Issues) >= 100 {
			break
		}
		c.Issues = append(c.Issues, is)
	}
	return &c, nil
}
