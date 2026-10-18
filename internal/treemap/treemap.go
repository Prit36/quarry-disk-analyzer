// Package treemap builds a bounded squarified map from immutable scan data.
package treemap

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"quarry/internal/index"
)

type Rect struct{ X, Y, W, H float32 }
type Tile struct {
	ID    index.ID
	Rect  Rect
	Bytes uint64
	Other bool
}
type Item struct {
	ID    index.ID
	Bytes uint64
	Other bool
}

// Items scans a directory once and retains at most limit entries. The remainder
// becomes an honest aggregate tile, rather than disappearing from the map.
func Items(s *index.Snapshot, dir index.ID, allocated bool, limit int) []Item {
	return items(context.Background(), s, dir, allocated, limit, true)
}

func items(ctx context.Context, s *index.Snapshot, dir index.ID, allocated bool, limit int, showMetadata bool) []Item {
	if limit < 2 {
		limit = 2
	}
	h := make(itemHeap, 0, min(limit, 128))
	var other uint64
	visited := 0
	isMFT := strings.Contains(s.Engine, "MFT")
	for id := s.Nodes[dir].FirstChild; id != index.None; id = s.Nodes[id].NextSibling {
		visited++
		if visited&4095 == 0 && ctx.Err() != nil {
			return nil
		}
		if !showMetadata && dir == 0 && isMFT && strings.HasPrefix(s.NameView(id), "$") {
			continue
		}
		n := s.Nodes[id]
		value := n.Size
		if allocated {
			value = n.Allocated
		}
		if value == 0 {
			continue
		}
		item := Item{ID: id, Bytes: value}
		if len(h) < limit-1 {
			h.push(item)
		} else if value > h[0].Bytes {
			other += h[0].Bytes
			h[0] = item
			h.down(0)
		} else {
			other += value
		}
	}
	items := []Item(h)
	if other > 0 {
		items = append(items, Item{ID: dir, Bytes: other, Other: true})
	}
	slices.SortFunc(items, func(a, b Item) int {
		if c := cmp.Compare(b.Bytes, a.Bytes); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return items
}

// itemHeap is a min-heap on Bytes. It mirrors container/heap's sift steps
// without boxing each Item in an interface value.
type itemHeap []Item

func (h *itemHeap) push(item Item) {
	*h = append(*h, item)
	s := *h
	for j := len(s) - 1; j > 0; {
		parent := (j - 1) / 2
		if s[j].Bytes >= s[parent].Bytes {
			break
		}
		s[j], s[parent] = s[parent], s[j]
		j = parent
	}
}

func (h itemHeap) down(i int) {
	for {
		child := 2*i + 1
		if child >= len(h) {
			break
		}
		if right := child + 1; right < len(h) && h[right].Bytes < h[child].Bytes {
			child = right
		}
		if h[child].Bytes >= h[i].Bytes {
			break
		}
		h[i], h[child] = h[child], h[i]
		i = child
	}
}

// Layout creates geometry only; callers cache it until the bounds/items change.
func Layout(items []Item, bounds Rect) []Tile {
	return LayoutAppend(items, bounds, make([]Tile, 0, len(items)))
}

// LayoutAppend appends squarified tiles to dst.
func LayoutAppend(items []Item, bounds Rect, tiles []Tile) []Tile {
	var total float64
	for _, i := range items {
		total += float64(i.Bytes)
	}
	if total <= 0 || bounds.W <= 0 || bounds.H <= 0 {
		return tiles
	}
	scale := float64(bounds.W) * float64(bounds.H) / total
	remaining := bounds
	for start := 0; start < len(items); {
		end := start + 1
		sum := float64(items[start].Bytes) * scale
		side := float64(min(remaining.W, remaining.H))
		worst := aspect(sum, sum, sum, side)
		for end < len(items) {
			next := float64(items[end].Bytes) * scale
			candidate := aspect(sum+next, float64(items[start].Bytes)*scale, next, side)
			if candidate > worst {
				break
			}
			sum += next
			worst = candidate
			end++
		}
		if remaining.W >= remaining.H {
			width := float32(sum / float64(remaining.H))
			y := remaining.Y
			for i := start; i < end; i++ {
				height := float32(float64(items[i].Bytes) * scale / float64(width))
				if i == end-1 {
					height = remaining.Y + remaining.H - y
				}
				tiles = append(tiles, Tile{items[i].ID, Rect{remaining.X, y, width, height}, items[i].Bytes, items[i].Other})
				y += height
			}
			remaining.X += width
			remaining.W = max(0, remaining.W-width)
		} else {
			height := float32(sum / float64(remaining.W))
			x := remaining.X
			for i := start; i < end; i++ {
				width := float32(float64(items[i].Bytes) * scale / float64(height))
				if i == end-1 {
					width = remaining.X + remaining.W - x
				}
				tiles = append(tiles, Tile{items[i].ID, Rect{x, remaining.Y, width, height}, items[i].Bytes, items[i].Other})
				x += width
			}
			remaining.Y += height
			remaining.H = max(0, remaining.H-height)
		}
		start = end
	}
	return tiles
}
func aspect(sum, largest, smallest, side float64) float64 {
	if sum <= 0 || side <= 0 || smallest <= 0 {
		return 1e100
	}
	return max(side*side*largest/(sum*sum), sum*sum/(side*side*smallest))
}
