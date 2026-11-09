package index

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// AgeTotals counts files and their sizes.
type AgeTotals struct {
	Files, Bytes, Allocated uint64
}

func (t *AgeTotals) add(n *Node) {
	t.Files++
	t.Bytes += n.Size
	t.Allocated += n.Allocated
}

func (t *AgeTotals) merge(o AgeTotals) {
	t.Files += o.Files
	t.Bytes += o.Bytes
	t.Allocated += o.Allocated
}

// AgeRange is a band of time since files were last modified: From (newer
// end, inclusive) to To (older end, exclusive). To 0 is unbounded.
type AgeRange struct {
	Label    string
	From, To time.Duration
	Query    string // the same files in the search syntax
	Stale    bool   // older than a year: candidates for review
}

const day = 24 * time.Hour

// AgeRanges are the bands of an AgeProfile, newest first. Their age:
// queries select exactly the files counted in each band.
var AgeRanges = []AgeRange{
	{Label: "Last 30 days", From: 0, To: 30 * day, Query: "age:<30d"},
	{Label: "30 to 90 days", From: 30 * day, To: 90 * day, Query: "age:30d..90d"},
	{Label: "90 days to 1 year", From: 90 * day, To: 365 * day, Query: "age:90d..1y"},
	{Label: "1 to 3 years", From: 365 * day, To: 3 * 365 * day, Query: "age:1y..3y", Stale: true},
	{Label: "Over 3 years", From: 3 * 365 * day, Query: "age:>3y", Stale: true},
}

// AgeProfile is the storage of a scope by time since last modification.
type AgeProfile struct {
	Now     time.Time
	MinSize uint64
	Ranges  []AgeTotals // parallel to AgeRanges
	Unknown AgeTotals   // files without a modification time
	Total   AgeTotals   // every file of at least MinSize
	// Stale is the files at least MinSize not modified for over a year
	// (the Stale ranges together).
	Stale AgeTotals
	// Cloud is the online-only placeholders among the counted files.
	Cloud AgeTotals
}

// AgeProfile tallies the files below root of at least minSize by how long
// ago they were last modified, counted from now. It reads only the node
// table, on several cores, and allocates per worker rather than per file.
func (s *Snapshot) AgeProfile(ctx context.Context, root ID, now time.Time, minSize uint64) (AgeProfile, error) {
	p, err := s.AgeProfiles(ctx, root, now, minSize)
	if err != nil {
		return AgeProfile{}, err
	}
	return p[0], nil
}

// AgeProfiles is AgeProfile for several minimum sizes in one walk of the
// index, one profile per minimum, in order.
func (s *Snapshot) AgeProfiles(ctx context.Context, root ID, now time.Time, minSizes ...uint64) ([]AgeProfile, error) {
	// Band boundaries as Unix seconds, newest first: a file belongs to the
	// first band whose older bound it is not older than.
	bounds := make([]int64, len(AgeRanges))
	for i, r := range AgeRanges {
		bounds[i] = -1 << 62
		if r.To > 0 {
			bounds[i] = now.Add(-r.To).Unix()
		}
	}
	type partial struct {
		ranges                []AgeTotals
		unknown, total, cloud AgeTotals
	}
	newParts := func() []partial {
		parts := make([]partial, len(minSizes))
		for i := range parts {
			parts[i].ranges = make([]AgeTotals, len(AgeRanges))
		}
		return parts
	}
	var workers [][]partial
	var mu sync.Mutex
	err := s.walkFilesParallel(ctx, root, func() func(*Node) {
		parts := newParts()
		mu.Lock()
		workers = append(workers, parts)
		mu.Unlock()
		return func(n *Node) {
			band := -1 // unknown date
			if n.Modified != 0 {
				t := n.ModTime()
				for i, b := range bounds {
					if t >= b {
						band = i
						break
					}
				}
			}
			for j := range parts {
				if n.Size < minSizes[j] {
					continue
				}
				part := &parts[j]
				part.total.add(n)
				if n.Flags&CloudOnly != 0 {
					part.cloud.add(n)
				}
				switch {
				case n.Modified == 0:
					part.unknown.add(n)
				case band >= 0:
					part.ranges[band].add(n)
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]AgeProfile, len(minSizes))
	for j, minSize := range minSizes {
		p := AgeProfile{Now: now, MinSize: minSize, Ranges: make([]AgeTotals, len(AgeRanges))}
		for _, parts := range workers {
			part := &parts[j]
			for i := range p.Ranges {
				p.Ranges[i].merge(part.ranges[i])
			}
			p.Unknown.merge(part.unknown)
			p.Total.merge(part.total)
			p.Cloud.merge(part.cloud)
		}
		for i, r := range AgeRanges {
			if r.Stale {
				p.Stale.merge(p.Ranges[i])
			}
		}
		out[j] = p
	}
	return out, nil
}

// walkFilesParallel calls a visitor for every file below root (root itself
// when it is a file). Each worker gets its own visitor from newWorker, so
// tallies need no locks; subtrees are split into pieces of similar weight
// as SummarizeScope does.
func (s *Snapshot) walkFilesParallel(ctx context.Context, root ID, newWorker func() func(*Node)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == None || int(root) >= len(s.Nodes) {
		return fmt.Errorf("invalid scope")
	}
	n := s.Nodes[root]
	if !n.IsDir() {
		newWorker()(&s.Nodes[root])
		return nil
	}
	workers := min(max(1, runtime.GOMAXPROCS(0)-1), max(1, int((uint64(n.Files)+uint64(n.Dirs))/parallelSearchEntries)))
	target := max(uint64(1024), (uint64(n.Files)+uint64(n.Dirs))/uint64(4*workers))
	pieces, err := s.searchPieces(ctx, root, target)
	if err != nil {
		return err
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
			return err
		}
		pieces = slices.Replace(pieces, best, best+1, children...)
	}
	if len(pieces) == 0 {
		return ctx.Err()
	}
	workers = min(workers, len(pieces))
	var next atomic.Int64
	var wg sync.WaitGroup
	work := func() {
		visit := newWorker()
		count := 0
		for {
			i := int(next.Add(1) - 1)
			if i >= len(pieces) {
				return
			}
			p := pieces[i]
			for id := p.id; ; id = s.Nodes[id].NextSibling {
				current := id
				for {
					if count&4095 == 0 && ctx.Err() != nil {
						return
					}
					count++
					node := &s.Nodes[current]
					if !node.IsDir() {
						visit(node)
					}
					if child := node.FirstChild; child != None {
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
	}
	for w := 1; w < workers; w++ {
		wg.Add(1)
		go func() { defer wg.Done(); work() }()
	}
	work()
	wg.Wait()
	return ctx.Err()
}
