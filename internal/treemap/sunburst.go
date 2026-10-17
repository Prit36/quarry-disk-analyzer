package treemap

import (
	"math"
	"stratum/internal/index"
)

type Sector struct {
	ID         index.ID
	Bytes      uint64
	Start, End float64
	Depth      int
	Other      bool
}

func Sunburst(branches []Branch, maxDepth int) []Sector {
	sectors := make([]Sector, 0, 512)
	var build func([]Branch, float64, float64, int)
	build = func(branches []Branch, start, end float64, depth int) {
		if depth >= maxDepth {
			return
		}
		var total float64
		for _, branch := range branches {
			total += float64(branch.Item.Bytes)
		}
		if total == 0 {
			return
		}
		angle := start
		for _, branch := range branches {
			next := angle + (end-start)*float64(branch.Item.Bytes)/total
			sectors = append(sectors, Sector{ID: branch.Item.ID, Bytes: branch.Item.Bytes, Start: angle, End: next, Depth: depth, Other: branch.Item.Other})
			if next-angle > .002 {
				build(branch.Children, angle, next, depth+1)
			}
			angle = next
		}
	}
	build(branches, 0, 2*math.Pi, 0)
	return sectors
}
