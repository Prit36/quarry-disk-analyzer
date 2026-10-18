package desktop

import (
	"path/filepath"
	"strings"

	"quarry/internal/index"
)

const (
	pathRegular uint8 = 1 << iota
	pathBlocked
)

// Classify ancestors once per selection. Ordinary entries need no full path
// allocation to determine whether blocked would accept them. Unusual roots
// or components use blocked itself, retaining its path-cleaning semantics.
func (a *App) mutableSelection(ids []index.ID) bool {
	if len(ids) < 64 {
		for _, id := range ids {
			if a.blocked(id) != "" {
				return false
			}
		}
		return len(ids) > 0
	}
	s := a.snap
	root := filepath.Clean(s.Root)
	rootState := uint8(0)
	if root == s.Root {
		rootState = pathRegular
	}
	if s.Nodes[0].Flags&index.Unplaced != 0 {
		rootState |= pathBlocked
	}
	isDrive := len(root) == 3 && root[1] == ':' && root[2] == '\\'
	states := map[index.ID]uint8{0: rootState, index.None: 0}
	var stack []index.ID
	classify := func(id index.ID, parent uint8) uint8 {
		n := s.Nodes[id]
		if parent&pathBlocked != 0 || n.Flags&index.Unplaced != 0 || n.Parent == 0 && s.IsMetadata(id) {
			return pathBlocked
		}
		name := s.NameView(id)
		if parent&pathRegular != 0 && name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `\/`) {
			return pathRegular
		}
		return 0
	}
	for _, id := range ids {
		if id == 0 || id == index.None || int(id) >= len(s.Nodes) {
			return false
		}
		parent := s.Nodes[id].Parent
		parentState, known := states[parent]
		if !known {
			stack = stack[:0]
			for p := parent; ; p = s.Nodes[p].Parent {
				if _, ok := states[p]; ok {
					break
				}
				stack = append(stack, p)
			}
			for i := len(stack) - 1; i >= 0; i-- {
				p := stack[i]
				states[p] = classify(p, states[s.Nodes[p].Parent])
			}
			parentState = states[parent]
		}
		state := classify(id, parentState)
		if s.Nodes[id].IsDir() {
			states[id] = state
		}
		if state&pathBlocked != 0 {
			return false
		}
		if state&pathRegular == 0 {
			if a.blocked(id) != "" {
				return false
			}
		} else if isDrive && parent == 0 && protectedDriveName(s.NameView(id)) {
			return false
		}
	}
	return len(ids) > 0
}

func protectedDriveName(name string) bool {
	if len(name) == 0 {
		return false
	}
	b := name[0]
	if b >= 'A' && b <= 'Z' {
		b += 32
	}
	if b < 128 {
		switch b {
		case 's', 'p', 'h', '$', 'w', 'b', 'r':
		default:
			return false
		}
	}
	switch strings.ToLower(name) {
	case "system volume information", "pagefile.sys", "hiberfil.sys", "swapfile.sys", "$recycle.bin", "windows", "boot", "recovery", "$windows.~bt", "$winreagent":
		return true
	}
	return false
}
