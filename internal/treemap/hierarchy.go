package treemap

import (
	"context"
	"quarry/internal/index"
)

// Branches keeps nested directory membership without a widget per file.
// Children are built once off-thread; geometry is regenerated only on resize.
type Branch struct {
	Item     Item
	Children []Branch
}
type Cell struct {
	Tile   Tile
	Depth  int
	Group  bool
	Header float32
}

func Hierarchy(ctx context.Context, s *index.Snapshot, root index.ID, allocated bool, budget int) []Branch {
	return HierarchyFiltered(ctx, s, root, allocated, budget, true)
}

func HierarchyFiltered(ctx context.Context, s *index.Snapshot, root index.ID, allocated bool, budget int, showMetadata bool) []Branch {
	if ctx.Err() != nil {
		return nil
	}
	remaining := max(2, budget)
	build := func(dir index.ID, limit int) []Branch {
		entries := items(ctx, s, dir, allocated, limit, showMetadata)
		remaining -= len(entries)
		branches := make([]Branch, len(entries))
		for position, item := range entries {
			branches[position].Item = item
		}
		return branches
	}
	branches := build(root, min(remaining, 96))
	parents := []*Branch{}
	collect := func(nodes []Branch) {
		for position := range nodes {
			if !nodes[position].Item.Other && s.Nodes[nodes[position].Item.ID].IsDir() {
				parents = append(parents, &nodes[position])
			}
		}
	}
	collect(branches)
	for depth := 0; depth < 7 && len(parents) > 0 && remaining >= 2 && ctx.Err() == nil; depth++ {
		level := parents
		parents = nil
		for position, parent := range level {
			if remaining < 2 || ctx.Err() != nil {
				break
			}
			limit := min(96, max(2, remaining/(len(level)-position)))
			parent.Children = build(parent.Item.ID, min(remaining, limit))
			collect(parent.Children)
		}
	}
	return branches
}

// countBranches is the number of cells NestedLayout can produce.
func countBranches(branches []Branch) int {
	n := len(branches)
	for i := range branches {
		n += countBranches(branches[i].Children)
	}
	return n
}

func NestedLayout(branches []Branch, bounds Rect) []Cell {
	cells := make([]Cell, 0, countBranches(branches))
	// items is scratch space: Layout copies what it needs into its tiles, so
	// one buffer serves every folder, including the nested ones.
	var items []Item
	var layout func([]Branch, Rect, int)
	layout = func(nodes []Branch, r Rect, depth int) {
		items = items[:0]
		for _, n := range nodes {
			items = append(items, n.Item)
		}
		tiles := Layout(items, r)
		for i, tile := range tiles {
			r := tile.Rect
			group := len(nodes[i].Children) > 0 && r.W >= 18 && r.H >= 18
			header := float32(0)
			if group {
				// A compact strip carries the folder's name; small groups
				// keep only a thin frame so their contents stay visible.
				header = 2
				if r.W > 60 && r.H >= 40 {
					header = 16
				}
			}
			cells = append(cells, Cell{Tile: tile, Depth: depth, Group: group, Header: header})
			if group {
				layout(nodes[i].Children, Rect{X: r.X + 2, Y: r.Y + header, W: max(0, r.W-4), H: max(0, r.H-header-2)}, depth+1)
			}
		}
	}
	layout(branches, bounds, 0)
	return cells
}
