package index

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
)

// FilteredParallel returns exactly what Filtered returns, rebuilding
// separate subtrees on several goroutines. keepFile and dropFolder are then
// called concurrently and in no particular order (a folder can be asked
// twice), so they must be safe for concurrent use and deterministic.
func (s *Snapshot) FilteredParallel(ctx context.Context, keepFile func(ID) bool, dropFolder func(ID) bool, isolate bool) (*Snapshot, FilterStats, error) {
	return s.filteredParallel(ctx, keepFile, dropFolder, isolate, runtime.GOMAXPROCS(0), 1<<16)
}

func (s *Snapshot) filteredParallel(ctx context.Context, keepFile func(ID) bool, dropFolder func(ID) bool, isolate bool, workers, minNodes int) (*Snapshot, FilterStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, FilterStats{}, err
	}
	if workers < 2 || len(s.Nodes) < minNodes || !s.Nodes[0].IsDir() {
		return s.Filtered(ctx, keepFile, dropFolder, isolate)
	}
	c, regular := s.cloneRegular(workers)
	if !regular {
		return s.Filtered(ctx, keepFile, dropFolder, isolate)
	}
	if err := ctx.Err(); err != nil {
		return nil, FilterStats{}, err
	}
	// Split the heaviest folders until there are enough pieces to share out.
	// A split folder is rebuilt last, here, from its finished children; it
	// must not be one dropFolder rejects, whose subtree is never visited.
	split := map[ID]bool{0: true}
	unsplittable := map[ID]bool{}
	var pieces []ID
	for c := s.Nodes[0].FirstChild; c != None; c = s.Nodes[c].NextSibling {
		if s.Nodes[c].IsDir() {
			pieces = append(pieces, c)
		}
	}
	weight := func(id ID) uint64 { return uint64(s.Nodes[id].Files) + uint64(s.Nodes[id].Dirs) }
	target := uint64(len(s.Nodes) / (4 * workers))
	for len(pieces) < 4*workers {
		best := -1
		for i, p := range pieces {
			if !unsplittable[p] && s.Nodes[p].FirstChild != None && weight(p) > target && (best < 0 || weight(p) > weight(pieces[best])) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		p := pieces[best]
		if dropFolder != nil && dropFolder(p) {
			unsplittable[p] = true // a piece of its own, dropped by its worker
			continue
		}
		var children []ID
		for c := s.Nodes[p].FirstChild; c != None; c = s.Nodes[c].NextSibling {
			if s.Nodes[c].IsDir() {
				children = append(children, c)
			}
		}
		split[p] = true
		pieces = slices.Replace(pieces, best, best+1, children...)
	}
	slices.SortFunc(pieces, func(a, b ID) int { return cmp.Compare(weight(b), weight(a)) })
	index := make(map[ID]int, len(pieces))
	for i, p := range pieces {
		index[p] = i
	}

	dropped := make([]bool, len(pieces))
	stats := make([]FilterStats, workers)
	errs := make([]error, workers)
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(pieces) || errs[w] != nil {
					return
				}
				p := pieces[i]
				if dropFolder != nil && dropFolder(p) {
					n := &c.Nodes[p]
					n.Flags |= Removed
					stats[w].HiddenFolders += uint64(n.Dirs) + 1
					stats[w].HiddenFiles += uint64(n.Files)
					stats[w].HiddenBytes += n.Size
					dropped[i] = true
					continue
				}
				errs[w] = c.filterTree(ctx, p, keepFile, dropFolder, isolate, nil, &stats[w])
			}
		}()
	}
	wg.Wait()
	var total FilterStats
	for w := range workers {
		if errs[w] != nil {
			return nil, FilterStats{}, errs[w]
		}
		total.HiddenFiles += stats[w].HiddenFiles
		total.HiddenFolders += stats[w].HiddenFolders
		total.HiddenBytes += stats[w].HiddenBytes
	}
	// The split folders, in the same order as one walk: their files are
	// decided here and their finished folders attached in place.
	err := c.filterTree(ctx, 0, keepFile, dropFolder, isolate, func(id ID) (bool, bool) {
		if split[id] {
			return false, false
		}
		i, ok := index[id]
		return ok, ok && dropped[i]
	}, &total)
	if err != nil {
		return nil, FilterStats{}, err
	}
	return c, total, ctx.Err()
}

// cloneRegular is Clone, copying the node table in parallel ranges. It
// also reports whether no file has children, the case filterTree handles
// (imported trees may differ), checking each block while it is in cache.
func (s *Snapshot) cloneRegular(workers int) (*Snapshot, bool) {
	c := *s
	c.Nodes = make([]Node, len(s.Nodes))
	var irregular atomic.Bool
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			const block = 1024
			for lo := len(s.Nodes) * w / workers; lo < len(s.Nodes)*(w+1)/workers; lo += block {
				hi := min(lo+block, len(s.Nodes)*(w+1)/workers)
				copy(c.Nodes[lo:hi], s.Nodes[lo:hi])
				for i := range c.Nodes[lo:hi] {
					if n := &c.Nodes[lo+i]; !n.IsDir() && n.FirstChild != None {
						irregular.Store(true)
					}
				}
			}
		}()
	}
	wg.Wait()
	c.Names = s.Names[:len(s.Names):len(s.Names)]
	c.Issues = slices.Clone(s.Issues)
	return &c, !irregular.Load()
}
