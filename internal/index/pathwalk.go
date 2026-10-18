package index

import (
	"strings"
	"unsafe"
)

// pathWalker builds the paths of entries visited depth-first (as Visit
// does) from the path of their parent, kept on a stack, instead of walking
// to the root and allocating for every entry. Paths it cannot build the
// same way as Path (unusual roots or names, unresolved records) come from
// Path itself.
type pathWalker struct {
	s     *Snapshot
	stack []pathFrame
	buf   []byte
}

type pathFrame struct {
	id      ID
	end     int  // length of its path in buf
	regular bool // its path is built in buf
}

func (s *Snapshot) newPathWalker() *pathWalker {
	return &pathWalker{s: s, buf: make([]byte, 0, 1024)}
}

func regularName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `\/`)
}

// path returns id's path. The result is valid until the next call.
func (w *pathWalker) path(id ID) string {
	s := w.s
	if id == 0 || id == None || int(id) >= len(s.Nodes) {
		return s.Path(id)
	}
	parent := s.Nodes[id].Parent
	for len(w.stack) > 0 && w.stack[len(w.stack)-1].id != parent {
		w.stack = w.stack[:len(w.stack)-1]
	}
	if len(w.stack) == 0 {
		// Not reached from its parent (the scope's root, or arbitrary IDs):
		// start from the parent's own path.
		if parent == None {
			return s.Path(id)
		}
		p := s.Path(parent)
		w.buf = append(w.buf[:0], p...)
		regular := cleanRoot(s.Root) && (parent == 0 || s.Path(parent) == s.joinPath(parent))
		w.stack = append(w.stack, pathFrame{parent, len(w.buf), regular})
	}
	top := w.stack[len(w.stack)-1]
	n := s.Nodes[id]
	name := s.NameView(id)
	regular := top.regular && n.Flags&Unplaced == 0 && regularName(name)
	if !regular {
		if n.IsDir() {
			w.stack = append(w.stack, pathFrame{id, 0, false})
		}
		return s.Path(id)
	}
	w.buf = w.buf[:top.end]
	if len(w.buf) == 0 || w.buf[len(w.buf)-1] != '\\' {
		w.buf = append(w.buf, '\\')
	}
	w.buf = append(w.buf, name...)
	if n.IsDir() {
		w.stack = append(w.stack, pathFrame{id, len(w.buf), true})
	}
	return unsafe.String(unsafe.SliceData(w.buf), len(w.buf))
}
