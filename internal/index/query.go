package index

import (
	"cmp"
	"context"
	"fmt"
	"math/bits"
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
	// Ranges of the index are tallied in parallel, then merged: the sums,
	// and so the result, do not depend on the split. Half the cores are
	// left to the caller, which runs it beside other work (the treemap's).
	parts := min(max(1, runtime.GOMAXPROCS(0)/2), max(1, len(s.Nodes)/65536))
	partial := make([]map[string]*FileType, parts)
	errs := make([]error, parts)
	var group sync.WaitGroup
	for w := range parts {
		group.Add(1)
		go func() {
			defer group.Done()
			partial[w], errs[w] = s.summarizeRange(ctx, len(s.Nodes)*w/parts, len(s.Nodes)*(w+1)/parts)
		}()
	}
	group.Wait()
	for _, err := range errs {
		if err != nil {
			return Summary{}, err
		}
	}
	types := partial[0]
	for _, part := range partial[1:] {
		for ext, v := range part {
			if t := types[ext]; t != nil {
				t.Bytes += v.Bytes
				t.Allocated += v.Allocated
				t.Count += v.Count
			} else {
				types[ext] = v
			}
		}
	}
	result := Summary{Types: make([]FileType, 0, len(types))}
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

// summarizeRange tallies the files among s.Nodes[lo:hi] by lower-case
// extension.
func (s *Snapshot) summarizeRange(ctx context.Context, lo, hi int) (map[string]*FileType, error) {
	types := make(map[string]*FileType)
	byExt := make(map[string]*FileType)
	var last *FileType
	var lastExt string
	for i := lo; i < hi; i++ {
		if (i-lo)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		n := &s.Nodes[i]
		if n.IsDir() {
			continue
		}
		raw := fastExt(s.NameView(ID(i)))
		v := last
		if v == nil || raw != lastExt {
			v = byExt[raw]
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
			last, lastExt = v, raw
		}
		v.Bytes += n.Size
		v.Allocated += n.Allocated
		v.Count++
	}
	return types, nil
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
	return s.search(ctx, root, f, nil)
}

// SearchResult describes all matches and the facts needed by bulk selection.
type SearchResult struct {
	IDs         []ID
	Total       uint64
	HasMetadata bool
}

// SearchResults returns the same ordered matches as Search and their size,
// counting bytes inside matched folders once. Tallying during the walk avoids
// rereading every result and walking its ancestors after sorting. Metadata
// classification shares the temporary ancestry bits for selection validation.
func (s *Snapshot) SearchResults(ctx context.Context, root ID, f *Filter, allocated bool) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	if root == None || int(root) >= len(s.Nodes) {
		return SearchResult{}, fmt.Errorf("invalid search scope")
	}
	tally := &searchTally{allocated: allocated, states: make([]atomic.Uint32, (len(s.Nodes)+15)/16)}
	if s.IsMetadata(root) {
		tally.states[root>>4].Store(uint32(2) << (2 * (root & 15)))
	}
	ids, err := s.search(ctx, root, f, tally)
	return SearchResult{IDs: ids, Total: tally.total, HasMetadata: tally.metadata}, err
}

type searchTally struct {
	states    []atomic.Uint32 // two bits per entry; adjacent subtrees can share a word
	allocated bool
	total     uint64 // private to the coordinator or one worker's current piece
	metadata  bool
}

func (s *Snapshot) search(ctx context.Context, root ID, f *Filter, tally *searchTally) ([]ID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
		return s.searchSubtree(ctx, root, root, f, ancestorOK, make([]ID, 0, 1024), tally)
	}
	return s.searchParallel(ctx, root, f, ancestorOK, workers, tally)
}

// parallelSearchEntries is the scope size from which Search divides the walk
// between goroutines.
const parallelSearchEntries = 1 << 15

// searchSubtree appends the matches in the subtree of top to ids, in index
// order. skip is left out.
func (s *Snapshot) searchSubtree(ctx context.Context, top, skip ID, f *Filter, ancestorOK []bool, ids []ID, tallies ...*searchTally) ([]ID, error) {
	var tally *searchTally
	if len(tallies) > 0 {
		tally = tallies[0]
	}
	var paths *pathWalker
	if f.NeedsPath() {
		paths = s.newTermWalker(f)
	}
	return s.searchSubtreeWalk(ctx, top, skip, f, ancestorOK, ids, tally, paths)
}

func (s *Snapshot) searchSubtreeWalk(ctx context.Context, top, skip ID, f *Filter, ancestorOK []bool, ids []ID, tally *searchTally, paths *pathWalker) ([]ID, error) {
	// The same depth-first order as Visit, without two indirect calls per
	// entry.
	if top == None || int(top) >= len(s.Nodes) {
		return ids, fmt.Errorf("invalid scope")
	}
	nodes := s.Nodes
	current, count := top, 0
	for {
		if count&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return ids, err
			}
		}
		count++
		if current != skip && s.matchEntry(current, f, ancestorOK, tally, paths) {
			ids = append(ids, current)
		}
		if child := nodes[current].FirstChild; child != None {
			current = child
			continue
		}
		for current != top && nodes[current].NextSibling == None {
			current = nodes[current].Parent
		}
		if current == top {
			return ids, ctx.Err()
		}
		current = nodes[current].NextSibling
	}
}

// matchEntry tests one entry, after its parent has been tested. For folders it
// records in ancestorOK whether the folder or an ancestor satisfies every
// path: term, which is what its descendants inherit.
func (s *Snapshot) matchEntry(id ID, f *Filter, ancestorOK []bool, tally *searchTally, paths *pathWalker) bool {
	n := &s.Nodes[id]
	name := s.NameView(id)
	inherited := false
	if ancestorOK != nil {
		inherited = ancestorOK[n.Parent]
		if n.IsDir() {
			ancestorOK[id] = inherited || f.MatchPathComponent(name)
		}
	}
	var full string
	if paths != nil && n.IsDir() {
		// Feed every directory to the walker even when another criterion
		// rejects it: descendants reuse its path without ancestor walks.
		full = paths.path(id)
	}
	matched := f.matchNode(name, n, func() string {
		if paths == nil {
			return s.Path(id)
		}
		if full == "" {
			full = paths.path(id)
		}
		return full
	}, paths, inherited)
	if tally != nil && (matched || n.FirstChild != None) {
		const covered, metadata = 1, 2
		var state uint32
		if n.Parent != None {
			state = tally.states[n.Parent>>4].Load() >> (2 * (n.Parent & 15)) & 3
		}
		if n.Flags&Unplaced != 0 || n.Parent == 0 && s.IsMetadata(id) {
			state |= metadata
		}
		if matched && state&metadata != 0 {
			tally.metadata = true
		}
		parentCovered := state&covered != 0
		if matched && n.IsDir() {
			state |= covered
		}
		if n.FirstChild != None && state != 0 {
			tally.states[id>>4].Or(state << (2 * (id & 15)))
		}
		if matched && !parentCovered {
			if tally.allocated {
				tally.total += n.Allocated
			} else {
				tally.total += n.Size
			}
		}
	}
	return matched
}

// searchPiece is a part of a search, in index order: either the whole subtree
// of id (walk), or the matched entries already collected in ids.
type searchPiece struct {
	id       ID
	last     ID // inclusive end of a consecutive run of siblings
	walk     bool
	weight   uint64
	files    uint64
	reserve  int
	ids      []ID
	total    uint64
	metadata bool
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
func (s *Snapshot) searchParallel(ctx context.Context, root ID, f *Filter, ancestorOK []bool, workers int, tallies ...*searchTally) ([]ID, error) {
	var tally *searchTally
	if len(tallies) > 0 {
		tally = tallies[0]
	}
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
		if s.matchEntry(id, f, ancestorOK, tally, nil) {
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
	var reserved []ID
	if reserve {
		// Counts are hints, including on imported snapshots. Bound all
		// reservations together so bad totals cannot multiply allocations.
		remaining := uint64(len(s.Nodes))
		capacityTotal := 0
		for i := range pieces {
			p := &pieces[i]
			if !p.walk {
				capacityTotal += len(p.ids)
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
			capacityTotal += p.reserve
		}
		// Give workers disjoint, capacity-clipped ranges of the output array.
		// Accurate counts avoid a second full result array. Incorrect saved
		// counts can still grow individual ranges without touching neighbours.
		reserved = make([]ID, capacityTotal)
		offset := 0
		for i := range pieces {
			p := &pieces[i]
			if p.walk {
				p.ids = reserved[offset : offset : offset+p.reserve]
				offset += p.reserve
			} else {
				offset += len(p.ids)
			}
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
					var local searchTally
					var pieceTally *searchTally
					if tally != nil {
						local = *tally
						local.total = 0
						local.metadata = false
						pieceTally = &local
					}
					var paths *pathWalker
					if f.NeedsPath() {
						paths = s.newTermWalker(f)
					}
					count := 0
					for c := p.id; ; c = s.Nodes[c].NextSibling {
						if count&4095 == 0 && ctx.Err() != nil {
							errs[i] = ctx.Err()
							break
						}
						count++
						if s.Nodes[c].FirstChild == None {
							if s.matchEntry(c, f, ancestorOK, pieceTally, paths) {
								if p.ids == nil {
									p.ids = make([]ID, 0, min(1024, int(p.weight)))
								}
								p.ids = append(p.ids, c)
							}
						} else {
							if p.ids == nil {
								p.ids = make([]ID, 0, min(1024, int(p.weight)))
							}
							p.ids, errs[i] = s.searchSubtreeWalk(ctx, c, root, f, ancestorOK, p.ids, pieceTally, paths)
						}
						if errs[i] != nil || c == p.last {
							break
						}
					}
					p.total = local.total
					p.metadata = local.metadata
				}
			}
		}()
	}
	wg.Wait()
	total := 0
	grew := false
	for i, p := range pieces {
		if errs[i] != nil {
			return nil, errs[i]
		}
		total += len(p.ids)
		grew = grew || p.walk && len(p.ids) > p.reserve
	}
	ids := reserved[:0]
	// Compact leftward only when no range outgrew its reservation. Otherwise
	// a copy could overwrite a later worker's results before reading them.
	if !reserve || grew || cap(ids) > 2*total+4096 {
		ids = make([]ID, 0, total)
	}
	for _, p := range pieces {
		ids = append(ids, p.ids...)
		if tally != nil {
			tally.total += p.total
			tally.metadata = tally.metadata || p.metadata
		}
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
	n := len(lower)
	if n == 0 {
		return true
	}
	last := len(s) - n
	if last < 0 {
		for i := 0; i < len(s); i++ {
			if s[i] >= 128 {
				return strings.Contains(strings.ToLower(s), lower)
			}
		}
		return false
	}
	c0 := lower[0]
	if c0 >= 0x80 {
		return strings.Contains(strings.ToLower(s), lower)
	}
	pattern := uint64(c0) * swarOnes
	for i := 0; i <= last; {
		if i+8 > len(s) {
			for ; i <= last; i++ {
				b := s[i]
				if b >= 128 {
					return strings.Contains(strings.ToLower(s), lower)
				}
				if b >= 'A' && b <= 'Z' {
					b += 32
				}
				if b == c0 && equalFoldASCII(s[i:i+n], lower) {
					return true
				}
			}
			break
		}
		x := load64(s, i)
		if x&swarHigh != 0 {
			return strings.Contains(strings.ToLower(s), lower)
		}
		m := zeroBytes(foldASCII8(x) ^ pattern)
		if m == 0 {
			i += 8
			continue
		}
		j := i + bits.TrailingZeros64(m)/8
		if j > last {
			break
		}
		if equalFoldASCII(s[j:j+n], lower) {
			return true
		}
		i = j + 1
	}
	for i := max(0, last+1); i < len(s); i++ {
		if s[i] >= 128 {
			return strings.Contains(strings.ToLower(s), lower)
		}
	}
	return false
}
