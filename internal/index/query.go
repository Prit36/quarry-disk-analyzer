package index

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type FileType struct {
	Extension               string
	Bytes, Allocated, Count uint64
}
type Summary struct {
	Types []FileType
}

func (s *Snapshot) Summarize(ctx context.Context) (Summary, error) {
	types := make(map[string]*FileType)
	byExt := make(map[string]*FileType)
	for i, n := range s.Nodes {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return Summary{}, err
			}
		}
		if n.IsDir() {
			continue
		}
		raw := filepath.Ext(s.NameView(ID(i)))
		v := byExt[raw]
		if v == nil {
			ext := strings.ToLower(raw)
			if ext == "" {
				ext = "(no extension)"
			}
			v = types[ext]
			if v == nil {
				v = &FileType{Extension: ext}
				types[ext] = v
			}
			byExt[raw] = v
		}
		v.Bytes += n.Size
		v.Allocated += n.Allocated
		v.Count++
	}
	result := Summary{}
	for _, v := range types {
		result.Types = append(result.Types, *v)
	}
	slices.SortFunc(result.Types, func(a, b FileType) int {
		if c := cmp.Compare(b.Allocated, a.Allocated); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Bytes, a.Bytes); c != 0 {
			return c
		}
		return strings.Compare(a.Extension, b.Extension)
	})
	return result, nil
}

type Row struct {
	ID    ID
	Depth uint32
}
type Query struct {
	Text, Mode string
	Root       ID
	Open       map[ID]bool
	Sort       Sort
}
type QueryResult struct {
	Rows    []Row
	Matches int
}

// QueryRows runs off the UI thread. All matching entries remain available to
// the virtualized table; sorting applies to the entire result, not a top-k subset.
// Mode "search" matches files and folders, "largest" (and a text query in tree
// mode) files only, unless the query names a type.
func (s *Snapshot) QueryRows(ctx context.Context, q Query) (QueryResult, error) {
	if q.Mode == "largest" || q.Mode == "search" || strings.TrimSpace(q.Text) != "" {
		f, err := ParseFilter(q.Text)
		if err != nil {
			return QueryResult{}, err
		}
		if f.Kind == KindAny && q.Mode != "search" {
			f.Kind = KindFiles
		}
		ids, err := s.Search(ctx, q.Root, &f)
		if err != nil {
			return QueryResult{}, err
		}
		if err := s.SortContext(ctx, ids, q.Sort); err != nil {
			return QueryResult{}, err
		}
		rows := make([]Row, len(ids))
		for i, id := range ids {
			rows[i] = Row{ID: id}
		}
		return QueryResult{rows, len(rows)}, nil
	}
	rows := []Row{{q.Root, 0}}
	stack := []Row{}
	push := func(id ID, depth uint32) error {
		children := s.Children(id)
		if err := s.SortContext(ctx, children, q.Sort); err != nil {
			return err
		}
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, Row{children[i], depth})
		}
		return nil
	}
	if err := push(q.Root, 1); err != nil {
		return QueryResult{}, err
	}
	for len(stack) > 0 {
		if len(rows)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return QueryResult{}, err
			}
		}
		r := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		rows = append(rows, r)
		if q.Open[r.ID] && r.Depth < 256 {
			if err := push(r.ID, r.Depth+1); err != nil {
				return QueryResult{}, err
			}
		}
	}
	return QueryResult{rows, len(rows)}, nil
}

// Search returns every entry below root (excluding root) that matches f, in
// index order. It reads only the immutable snapshot.
func (s *Snapshot) Search(ctx context.Context, root ID, f *Filter) ([]ID, error) {
	if root == None || int(root) >= len(s.Nodes) {
		return nil, fmt.Errorf("invalid search scope")
	}
	var ancestorOK []bool
	if f.AncestorTerms() {
		ancestorOK = make([]bool, len(s.Nodes))
		ok := false
		for p := s.Nodes[root].Parent; p != None; p = s.Nodes[p].Parent {
			if f.MatchPathComponent(s.NameView(p)) {
				ok = true
				break
			}
		}
		if ok || f.MatchPathComponent(s.displayName(root)) {
			ancestorOK[root] = true
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 || uint64(s.Nodes[root].Files)+uint64(s.Nodes[root].Dirs) < parallelSearchEntries {
		return s.searchSubtree(ctx, root, root, f, ancestorOK, make([]ID, 0, 1024))
	}
	return s.searchParallel(ctx, root, f, ancestorOK, workers)
}

// parallelSearchEntries is the scope size from which Search divides the walk
// between goroutines.
const parallelSearchEntries = 1 << 15

// searchSubtree appends the matches in the subtree of top to ids, in index
// order. skip is left out.
func (s *Snapshot) searchSubtree(ctx context.Context, top, skip ID, f *Filter, ancestorOK []bool, ids []ID) ([]ID, error) {
	err := s.Visit(ctx, top, func(id ID) error {
		if id == skip {
			return nil
		}
		if s.matchEntry(id, f, ancestorOK) {
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}

// matchEntry tests one entry, after its parent has been tested. For folders it
// records in ancestorOK whether the folder or an ancestor satisfies every
// path: term, which is what its descendants inherit.
func (s *Snapshot) matchEntry(id ID, f *Filter, ancestorOK []bool) bool {
	n := s.Nodes[id]
	name := s.NameView(id)
	inherited := false
	if ancestorOK != nil {
		inherited = ancestorOK[n.Parent]
		if n.IsDir() {
			ancestorOK[id] = inherited || f.MatchPathComponent(name)
		}
	}
	return f.MatchNode(name, n, func() string { return s.Path(id) }, inherited)
}

// searchPiece is a part of a search, in index order: either the whole subtree
// of id (walk), or the matched entries already collected in ids.
type searchPiece struct {
	id      ID
	last    ID // inclusive end of a consecutive run of siblings
	walk    bool
	weight  uint64
	files   uint64
	reserve int
	ids     []ID
}

// Batch small sibling subtrees together. A flat folder can contain millions
// of files; allocating a task and result slice for each one costs far more
// than matching them. Large subtrees remain individually splittable.
func (s *Snapshot) searchPieces(ctx context.Context, parent ID, target uint64) ([]searchPiece, error) {
	var pieces []searchPiece
	first, last := None, None
	var weight uint64
	var files uint64
	count := 0
	for c := s.Nodes[parent].FirstChild; c != None; c = s.Nodes[c].NextSibling {
		if count&4095 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		count++
		n := &s.Nodes[c]
		w := uint64(1)
		fileCount := uint64(1)
		if n.IsDir() {
			w += uint64(n.Files) + uint64(n.Dirs)
			fileCount = uint64(n.Files)
		}
		if first != None && weight+w > target {
			pieces = append(pieces, searchPiece{id: first, last: last, walk: true, weight: weight, files: files})
			first, weight, files = None, 0, 0
		}
		if first == None {
			first = c
		}
		last, weight = c, weight+w
		files += fileCount
	}
	if first != None {
		pieces = append(pieces, searchPiece{id: first, last: last, walk: true, weight: weight, files: files})
	}
	return pieces, nil
}

// searchParallel walks the children of root on several goroutines. Folders
// are split until there are enough pieces to share out; a split folder is
// matched here and its children become pieces in its place, so the pieces
// concatenated in order are exactly the index order of a single walk. Pieces
// touch disjoint entries of ancestorOK, which keeps the workers race free.
func (s *Snapshot) searchParallel(ctx context.Context, root ID, f *Filter, ancestorOK []bool, workers int) ([]ID, error) {
	target := max(uint64(1024), (uint64(s.Nodes[root].Files)+uint64(s.Nodes[root].Dirs))/uint64(4*workers))
	pieces, err := s.searchPieces(ctx, root, target)
	if err != nil {
		return nil, err
	}
	for len(pieces) < 4*workers {
		best := -1
		for i, p := range pieces {
			if p.walk && p.id == p.last && s.Nodes[p.id].IsDir() && s.Nodes[p.id].FirstChild != None && (best < 0 || p.weight > pieces[best].weight) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		id := pieces[best].id
		split := []searchPiece{{}}
		if s.matchEntry(id, f, ancestorOK) {
			split[0].ids = []ID{id}
		}
		children, err := s.searchPieces(ctx, id, target)
		if err != nil {
			return nil, err
		}
		split = append(split, children...)
		pieces = slices.Replace(pieces, best, best+1, split...)
	}
	errs := make([]error, len(pieces))
	unrestricted := *f
	unrestricted.Kind = KindAny
	reserve := unrestricted.Empty()
	if reserve {
		// Counts are hints, including on imported snapshots. Bound all
		// reservations together so bad totals cannot multiply allocations.
		remaining := uint64(len(s.Nodes))
		for i := range pieces {
			p := &pieces[i]
			if !p.walk {
				continue
			}
			capacity := p.weight
			if f.Kind == KindFiles {
				capacity = p.files
			}
			if f.Kind == KindFolders {
				capacity -= p.files
			}
			p.reserve = int(min(capacity, remaining))
			remaining -= uint64(p.reserve)
		}
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(workers, len(pieces)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(pieces) {
					return
				}
				if pieces[i].walk {
					p := &pieces[i]
					if reserve {
						p.ids = make([]ID, 0, p.reserve)
					}
					count := 0
					for c := p.id; ; c = s.Nodes[c].NextSibling {
						if count&4095 == 0 && ctx.Err() != nil {
							errs[i] = ctx.Err()
							break
						}
						count++
						if s.Nodes[c].FirstChild == None {
							if s.matchEntry(c, f, ancestorOK) {
								p.ids = append(p.ids, c)
							}
						} else {
							p.ids, errs[i] = s.searchSubtree(ctx, c, root, f, ancestorOK, p.ids)
						}
						if errs[i] != nil || c == p.last {
							break
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	total := 0
	for i, p := range pieces {
		if errs[i] != nil {
			return nil, errs[i]
		}
		total += len(p.ids)
	}
	ids := make([]ID, 0, total)
	for _, p := range pieces {
		ids = append(ids, p.ids...)
	}
	return ids, nil
}

// displayName is the name shown for an entry; the scan root shows its path.
func (s *Snapshot) displayName(id ID) string {
	if id == 0 {
		return s.Root
	}
	return s.NameView(id)
}

func containsFold(s, lower string) bool {
	// ASCII names avoid allocating a lowercase copy per file. Unicode retains
	// correct case-insensitive matching through the standard library fallback.
	for i := 0; i < len(s); i++ {
		if s[i] >= 128 {
			return strings.Contains(strings.ToLower(s), lower)
		}
	}
	for i := 0; i+len(lower) <= len(s); i++ {
		match := true
		for j := range len(lower) {
			b := s[i+j]
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if b != lower[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
