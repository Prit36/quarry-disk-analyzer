package desktop

import (
	"path/filepath"
	"strings"
	"sync"

	"quarry/internal/index"
)

const (
	pathRegular uint8 = 1 << iota
	pathBlocked
)

var mutStatesPool = sync.Pool{
	New: func() any { return new([]uint8) },
}

const stateKnown uint8 = 1 << 7

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
	bufPtr := mutStatesPool.Get().(*[]uint8)
	states := *bufPtr
	if cap(states) < len(s.Nodes) {
		states = make([]uint8, len(s.Nodes))
	} else {
		states = states[:len(s.Nodes)]
		clear(states)
	}
	defer func() {
		*bufPtr = states
		mutStatesPool.Put(bufPtr)
	}()
	states[0] = rootState | stateKnown
	var localStack [64]index.ID
	stack := localStack[:0]
	classify := func(id index.ID, parent uint8) uint8 {
		n := &s.Nodes[id]
		if parent&pathBlocked != 0 || n.Flags&index.Unplaced != 0 || n.Parent == 0 && s.IsMetadata(id) {
			return pathBlocked
		}
		if parent&pathRegular != 0 && n.NameLength > 0 {
			name := s.NameView(id)
			if !(name[0] == '.' && (len(name) == 1 || (len(name) == 2 && name[1] == '.'))) &&
				!hasSlash(name) {
				return pathRegular
			}
		}
		return 0
	}
	for _, id := range ids {
		if id == 0 || id == index.None || int(id) >= len(s.Nodes) {
			return false
		}
		parent := s.Nodes[id].Parent
		var parentState uint8
		if parent == index.None {
			parentState = 0
		} else {
			st := states[parent]
			if st&stateKnown == 0 {
				stack = stack[:0]
				for p := parent; p != index.None; p = s.Nodes[p].Parent {
					if states[p]&stateKnown != 0 {
						break
					}
					stack = append(stack, p)
				}
				for i := len(stack) - 1; i >= 0; i-- {
					p := stack[i]
					var pParState uint8
					if par := s.Nodes[p].Parent; par != index.None {
						pParState = states[par] &^ stateKnown
					}
					states[p] = classify(p, pParState) | stateKnown
				}
				st = states[parent]
			}
			parentState = st &^ stateKnown
		}
		state := classify(id, parentState)
		if s.Nodes[id].IsDir() {
			states[id] = state | stateKnown
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

func hasSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == '\\' {
			return true
		}
	}
	return false
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
