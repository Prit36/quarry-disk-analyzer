package desktop

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"

	"quarry/internal/index"
)

// rootTypeSummary shares the treemap's whole-index tally with Extensions.
// Only extension groups are retained, never a second per-file index. A task
// belongs to its snapshot, so canceling one view cannot cancel another reader.
type rootTypeSummary struct {
	snap   *index.Snapshot
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	all    index.Summary
	err    error

	rootOnce sync.Once
	rootDone chan struct{}
	root     index.Summary // reachable files, logical size then extension
	rootErr  error
}

// Called only on the UI thread. At most the shown snapshot and the separate,
// unfiltered treemap snapshot may be cached.
func (a *App) rootTypes(s *index.Snapshot) *rootTypeSummary {
	a.pruneTypeSummaries()
	if entry := a.cachedRootTypes(s); entry != nil {
		return entry
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &rootTypeSummary{snap: s, ctx: ctx, cancel: cancel, done: make(chan struct{}), rootDone: make(chan struct{})}
	for i, old := range a.typeSummaries {
		if old == nil {
			a.typeSummaries[i] = entry
			go func() {
				defer close(entry.done)
				entry.all, entry.err = s.Summarize(ctx)
			}()
			return entry
		}
	}
	panic("extension summaries outlived their snapshots")
}

func (a *App) cachedRootTypes(s *index.Snapshot) *rootTypeSummary {
	for _, entry := range a.typeSummaries {
		if entry != nil && entry.snap == s {
			return entry
		}
	}
	return nil
}

func (a *App) pruneTypeSummaries() {
	for i, entry := range a.typeSummaries {
		if entry != nil && entry.snap != a.snap && entry.snap != a.mapSnapshot() {
			entry.cancel()
			a.typeSummaries[i] = nil
		}
	}
}

func (a *App) clearTypeSummaries() {
	for i, entry := range a.typeSummaries {
		if entry != nil {
			entry.cancel()
			a.typeSummaries[i] = nil
		}
	}
}

func (e *rootTypeSummary) whole(ctx context.Context) (index.Summary, error) {
	if err := ctx.Err(); err != nil {
		return index.Summary{}, err
	}
	select {
	case <-ctx.Done():
		return index.Summary{}, ctx.Err()
	case <-e.done:
		return e.all, e.err
	}
}

func (e *rootTypeSummary) reachable(ctx context.Context, allocated bool) (index.Summary, error) {
	if err := ctx.Err(); err != nil {
		return index.Summary{}, err
	}
	e.rootOnce.Do(func() {
		go func() {
			defer close(e.rootDone)
			all, err := e.whole(e.ctx)
			if err != nil {
				e.rootErr = err
				return
			}
			// Summarize includes detached subtrees for stable treemap colors.
			// Reuse it only when every record belongs to the live tree. Check
			// structural flags rather than trusting imported aggregate counts.
			for i := 1; i < len(e.snap.Nodes); i++ {
				if i&4095 == 0 && e.ctx.Err() != nil {
					e.rootErr = e.ctx.Err()
					return
				}
				n := &e.snap.Nodes[i]
				if n.Flags&index.Removed != 0 || n.Parent == index.None {
					e.root, e.rootErr = e.snap.SummarizeScope(e.ctx, 0, false)
					return
				}
			}
			e.root.Types = slices.Clone(all.Types)
			sortRootTypes(e.root.Types, false)
		}()
	})
	select {
	case <-ctx.Done():
		return index.Summary{}, ctx.Err()
	case <-e.rootDone:
	}
	if e.rootErr != nil {
		return index.Summary{}, e.rootErr
	}
	result := e.root
	if allocated {
		result.Types = slices.Clone(result.Types)
		sortRootTypes(result.Types, true)
	}
	return result, ctx.Err()
}

func sortRootTypes(types []index.FileType, allocated bool) {
	slices.SortFunc(types, func(a, b index.FileType) int {
		x, y := a.Bytes, b.Bytes
		if allocated {
			x, y = a.Allocated, b.Allocated
		}
		if c := cmp.Compare(y, x); c != 0 {
			return c
		}
		return strings.Compare(a.Extension, b.Extension)
	})
}
