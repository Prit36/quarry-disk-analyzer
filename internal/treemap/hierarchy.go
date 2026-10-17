package treemap

import (
	"context"
	"stratum/internal/index"
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

func LabelHeight(bounds Rect) float32 {
	if bounds.W >= 80 && bounds.H >= 115 {
		return 68
	}
	if bounds.W >= 65 && bounds.H >= 90 {
		return 48
	}
	return 20
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

func NestedLayout(branches []Branch, bounds Rect) []Cell {
	cells := make([]Cell, 0, 512)
	var layout func([]Branch, Rect, int)
	layout = func(nodes []Branch, r Rect, depth int) {
		items := make([]Item, len(nodes))
		for i, n := range nodes {
			items[i] = n.Item
		}
		tiles := Layout(items, r)
		for i, tile := range tiles {
			r := tile.Rect
			group := len(nodes[i].Children) > 0 && r.W >= 18 && r.H >= 18
			header := float32(0)
			if group {
				header = min(LabelHeight(r), r.H*.45)
				if depth > 0 {
					header = 16
					if r.W >= 80 && r.H >= 50 {
						header = 24
					}
					header = min(header, r.H*.35)
				}
				if header < 16 || r.W <= 60 {
					header = 2
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
