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
	Tile  Tile
	Depth int
	Group bool
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
	left := max(2, budget)
	var build func(index.ID, int) []Branch
	build = func(dir index.ID, depth int) []Branch {
		if ctx.Err() != nil || left < 2 {
			return nil
		}
		items := items(ctx, s, dir, allocated, min(left, 96), showMetadata)
		left -= len(items)
		branches := make([]Branch, len(items))
		for i, item := range items {
			branches[i].Item = item
			if !item.Other && s.Nodes[item.ID].IsDir() && depth < 7 && left >= 2 {
				branches[i].Children = build(item.ID, depth+1)
			}
		}
		return branches
	}
	return build(root, 0)
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
			group := len(nodes[i].Children) > 0 && r.W > 55 && r.H > 42
			cells = append(cells, Cell{Tile: tile, Depth: depth, Group: group})
			if group {
				header := LabelHeight(r)
				layout(nodes[i].Children, Rect{X: r.X + 1, Y: r.Y + header, W: max(0, r.W-2), H: max(0, r.H-header-1)}, depth+1)
			}
		}
	}
	layout(branches, bounds, 0)
	return cells
}
