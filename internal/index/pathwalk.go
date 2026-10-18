package index

import (
	"unsafe"
)

// pathWalker builds the paths of entries visited depth-first (as Visit
// does) from the path of their parent, kept on a stack, instead of walking
// to the root and allocating for every entry. Paths it cannot build the
// same way as Path (unusual roots or names, unresolved records) come from
// Path itself.
//
// With terms, it also reports which plain (non-wildcard) path terms a path
// contains. A term found in a folder's path is in every descendant's;
// otherwise a new occurrence must end in the part after the folder's path,
// so only the folder path's last len(term)-1 bytes and the rest are
// searched. This holds when the folder's path is ASCII, where folding does
// not change byte positions; other paths are searched in full. Folders are
// decided only once an entry below them asks.
type pathWalker struct {
	s     *Snapshot
	stack []pathFrame
	buf   []byte
	terms []string // lower-case literal per plain term bit, "" for other bits
	plain uint64   // the bits of terms that are tracked

	// The last path returned: built by Path (general), a folder's own frame
	// (own), or a file below frame parent, ending at end in buf.
	general   string
	isGeneral bool
	own       int
	parent    int
	end       int
	hits      uint64
	hitsDone  bool
}

type pathFrame struct {
	id      ID
	end     int    // length of its path in buf
	regular bool   // its path is built in buf
	known   bool   // ascii and hits are decided
	ascii   bool   // its path is ASCII
	hits    uint64 // tracked terms its path contains
}

func (s *Snapshot) newPathWalker() *pathWalker {
	return &pathWalker{s: s, buf: make([]byte, 0, 1024)}
}

// newTermWalker is a pathWalker that also tracks f's plain path terms.
func (s *Snapshot) newTermWalker(f *Filter) *pathWalker {
	w := s.newPathWalker()
	for i, t := range f.terms {
		if i < 64 && t.path && !t.wildcard && t.text != "" {
			if w.terms == nil {
				w.terms = make([]string, min(len(f.terms), 64))
			}
			w.terms[i] = t.text
			w.plain |= 1 << i
		}
	}
	return w
}

func regularName(name string) bool {
	return name != "" && name != "." && name != ".." && !hasSeparator(name)
}

// fullHits searches all of path for the tracked terms.
func (w *pathWalker) fullHits(path string) uint64 {
	var hits uint64
	for i, t := range w.terms {
		if t != "" && containsFold(path, t) {
			hits |= 1 << i
		}
	}
	return hits
}

// path returns id's path. The result is valid until the next call.
func (w *pathWalker) path(id ID) string {
	s := w.s
	w.hitsDone = false
	if id == 0 || id == None || int(id) >= len(s.Nodes) {
		return w.generalPath(s.Path(id))
	}
	parent := s.Nodes[id].Parent
	for len(w.stack) > 0 && w.stack[len(w.stack)-1].id != parent {
		w.stack = w.stack[:len(w.stack)-1]
	}
	if len(w.stack) == 0 {
		// Not reached from its parent (the scope's root, or arbitrary IDs):
		// start from the parent's own path.
		if parent == None {
			return w.generalPath(s.Path(id))
		}
		p := s.Path(parent)
		w.buf = append(w.buf[:0], p...)
		regular := cleanRoot(s.Root) && (parent == 0 || s.Path(parent) == s.joinPath(parent))
		w.stack = append(w.stack, pathFrame{id: parent, end: len(w.buf), regular: regular})
	}
	top := w.stack[len(w.stack)-1]
	n := s.Nodes[id]
	name := s.NameView(id)
	regular := top.regular && n.Flags&Unplaced == 0 && regularName(name)
	if !regular {
		if n.IsDir() {
			// Entries below it are irregular too and use Path.
			w.stack = append(w.stack, pathFrame{id: id})
		}
		return w.generalPath(s.Path(id))
	}
	w.buf = w.buf[:top.end]
	if len(w.buf) == 0 || w.buf[len(w.buf)-1] != '\\' {
		w.buf = append(w.buf, '\\')
	}
	w.buf = append(w.buf, name...)
	w.isGeneral, w.parent, w.end, w.own = false, len(w.stack)-1, len(w.buf), -1
	if n.IsDir() {
		w.own = len(w.stack)
		w.stack = append(w.stack, pathFrame{id: id, end: len(w.buf), regular: true})
	}
	return unsafe.String(unsafe.SliceData(w.buf), len(w.buf))
}

func (w *pathWalker) generalPath(path string) string {
	w.isGeneral, w.general = true, path
	return path
}

// termHits reports the tracked terms that the last path returned contains.
// It must be called before the next call to path.
func (w *pathWalker) termHits() uint64 {
	if !w.hitsDone {
		w.hitsDone = true
		switch {
		case w.isGeneral:
			w.hits = w.fullHits(w.general)
		case w.own >= 0:
			w.decide(w.own)
			w.hits = w.stack[w.own].hits
		default:
			if !w.stack[w.parent].known {
				w.decide(w.parent)
			}
			w.hits, _ = w.extend(w.parent, w.end)
		}
	}
	return w.hits
}

// decide sets the terms of stack[k] and of the undecided frames above it.
// Frames on the stack are ancestors of one another, and buf still begins
// with each one's path; a regular frame's parent frame is regular.
func (w *pathWalker) decide(k int) {
	j := k
	for j >= 0 && !w.stack[j].known {
		j--
	}
	for i := j + 1; i <= k; i++ {
		f := &w.stack[i]
		if i == 0 {
			path := unsafe.String(unsafe.SliceData(w.buf), f.end)
			f.ascii, f.hits = isASCII(path), w.fullHits(path)
		} else {
			f.hits, f.ascii = w.extend(i-1, f.end)
		}
		f.known = true
	}
}

// extend returns the terms and ASCII-ness of buf[:end], a path below the
// decided frame stack[k].
func (w *pathWalker) extend(k, end int) (uint64, bool) {
	top := &w.stack[k]
	full := unsafe.String(unsafe.SliceData(w.buf), end)
	if !top.ascii {
		return w.fullHits(full), false
	}
	// The window's part in top's path is ASCII; check the rest once.
	ascii := isASCII(full[top.end:])
	hits := top.hits
	for i, t := range w.terms {
		if t == "" || hits&(1<<i) != 0 {
			continue
		}
		window := full[max(0, top.end-len(t)+1):]
		if ascii && containsFoldASCII(window, t) || !ascii && containsFold(window, t) {
			hits |= 1 << i
		}
	}
	return hits, ascii
}

// PathCursor builds paths like Path for entries in any order, keeping the
// previous path's ancestors: entries listed near each other (as search
// results are) share most of their path, which is then not rebuilt.
type PathCursor struct {
	s     *Snapshot
	chain []ID  // ancestors below the root of the last regular path, top first
	ends  []int // length of buf after each of them
	buf   []byte
	up    []ID
	clean bool
}

func (s *Snapshot) NewPathCursor() *PathCursor {
	return &PathCursor{s: s, buf: append(make([]byte, 0, 512), s.Root...), clean: cleanRoot(s.Root)}
}

// Path returns Path(id). The result is valid until the next call.
func (c *PathCursor) Path(id ID) string {
	s := c.s
	if id == 0 || !c.clean {
		return s.Path(id)
	}
	c.up = c.up[:0]
	for cur := id; cur != 0; cur = s.Nodes[cur].Parent {
		if cur == None || int(cur) >= len(s.Nodes) || len(c.up) >= len(s.Nodes) || s.Nodes[cur].Flags&Unplaced != 0 {
			return s.Path(id)
		}
		c.up = append(c.up, cur)
	}
	// Keep the ancestors shared with the previous path.
	keep := 0
	for keep < len(c.chain) && keep < len(c.up) && c.chain[keep] == c.up[len(c.up)-1-keep] {
		keep++
	}
	c.chain, c.ends = c.chain[:keep], c.ends[:keep]
	c.buf = c.buf[:len(s.Root)]
	if keep > 0 {
		c.buf = c.buf[:c.ends[keep-1]]
	}
	for i := len(c.up) - 1 - keep; i >= 0; i-- {
		name := s.NameView(c.up[i])
		if !regularName(name) {
			c.chain, c.ends = c.chain[:0], c.ends[:0]
			return s.Path(id)
		}
		if c.buf[len(c.buf)-1] != '\\' {
			c.buf = append(c.buf, '\\')
		}
		c.buf = append(c.buf, name...)
		c.chain = append(c.chain, c.up[i])
		c.ends = append(c.ends, len(c.buf))
	}
	return unsafe.String(unsafe.SliceData(c.buf), len(c.buf))
}
