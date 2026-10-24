package index

import (
	"cmp"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// SummarizeScope groups only reachable files below root. Unlike Analyze it
// does not also collect top files/folders. Unlike Summarize it respects removed
// subtrees and an arbitrary scope. Extension View needs just these groups.
func (s *Snapshot) SummarizeScope(ctx context.Context, root ID, allocated bool) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if root == None || int(root) >= len(s.Nodes) {
		return Summary{}, fmt.Errorf("invalid scope")
	}
	n := s.Nodes[root]
	workers := min(max(1, runtime.GOMAXPROCS(0)/2), max(1, int((uint64(n.Files)+uint64(n.Dirs))/parallelSearchEntries)))
	pieces := []searchPiece{{id: root, last: root, walk: true}}
	if n.IsDir() {
		target := max(uint64(1024), (uint64(n.Files)+uint64(n.Dirs))/uint64(4*workers))
		var err error
		pieces, err = s.searchPieces(ctx, root, target)
		if err != nil {
			return Summary{}, err
		}
		for len(pieces) < 4*workers {
			best := -1
			for i, p := range pieces {
				if p.id == p.last && s.Nodes[p.id].IsDir() && s.Nodes[p.id].FirstChild != None && (best < 0 || p.weight > pieces[best].weight) {
					best = i
				}
			}
			if best < 0 {
				break
			}
			children, err := s.searchPieces(ctx, pieces[best].id, target)
			if err != nil {
				return Summary{}, err
			}
			// The split entry is a directory and contributes no file bytes.
			pieces = slices.Replace(pieces, best, best+1, children...)
		}
	}
	if len(pieces) == 0 {
		return Summary{}, ctx.Err()
	}
	workers = min(workers, len(pieces))
	partials := make([]map[string]*FileType, workers)
	errs := make([]error, workers)
	var next atomic.Int64
	var wg sync.WaitGroup
	work := func(w int) {
		types := make(map[string]*FileType)
		bySpelling := make(map[string]*FileType)
		var last *FileType
		var lastExt string
		add := func(id ID) {
			n := &s.Nodes[id]
			if n.IsDir() {
				return
			}
			raw := fastExt(s.NameView(id))
			group := last
			if group == nil || raw != lastExt {
				group = bySpelling[raw]
				if group == nil {
					ext := strings.ToLower(raw)
					if ext == "" {
						ext = "(no extension)"
					}
					group = types[ext]
					if group == nil {
						group = &FileType{Extension: ext}
						types[ext] = group
					}
					bySpelling[raw] = group
				}
				last, lastExt = group, raw
			}
			group.Bytes += n.Size
			group.Allocated += n.Allocated
			group.Count++
		}
		count := 0
		for {
			i := int(next.Add(1) - 1)
			if i >= len(pieces) {
				break
			}
			p := pieces[i]
			for id := p.id; ; id = s.Nodes[id].NextSibling {
				// Walk inline, checking cancellation once per batch instead of
				// starting a callback-based Visit for every leaf in a flat folder.
				current := id
				for {
					if count&4095 == 0 && ctx.Err() != nil {
						errs[w] = ctx.Err()
						return
					}
					count++
					add(current)
					if child := s.Nodes[current].FirstChild; child != None {
						current = child
						continue
					}
					for current != id && s.Nodes[current].NextSibling == None {
						current = s.Nodes[current].Parent
					}
					if current == id {
						break
					}
					current = s.Nodes[current].NextSibling
				}
				if id == p.last {
					break
				}
			}
		}
		partials[w] = types
	}
	for w := 1; w < workers; w++ {
		wg.Add(1)
		go func() { defer wg.Done(); work(w) }()
	}
	work(0)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return Summary{}, err
		}
	}
	types := partials[0]
	for _, part := range partials[1:] {
		for ext, value := range part {
			if group := types[ext]; group != nil {
				group.Bytes += value.Bytes
				group.Allocated += value.Allocated
				group.Count += value.Count
			} else {
				types[ext] = value
			}
		}
	}
	result := Summary{}
	for _, group := range types {
		result.Types = append(result.Types, *group)
	}
	slices.SortFunc(result.Types, func(a, b FileType) int {
		x, y := a.Bytes, b.Bytes
		if allocated {
			x, y = a.Allocated, b.Allocated
		}
		if c := cmp.Compare(y, x); c != 0 {
			return c
		}
		return strings.Compare(a.Extension, b.Extension)
	})
	return result, ctx.Err()
}
