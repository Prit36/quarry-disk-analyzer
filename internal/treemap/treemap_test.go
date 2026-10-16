package treemap

import (
	"context"
	"math"
	"stratum/internal/index"
	"testing"
)

func TestMapConservesAreaAndBounds(t *testing.T) {
	items := []Item{{ID: 1, Bytes: 600}, {ID: 2, Bytes: 300}, {ID: 3, Bytes: 100}}
	tiles := Layout(items, Rect{W: 1000, H: 300})
	var area float64
	for _, tile := range tiles {
		r := tile.Rect
		if r.X < 0 || r.Y < 0 || r.X+r.W > 1000.01 || r.Y+r.H > 300.01 || r.W <= 0 || r.H <= 0 {
			t.Fatal("outside bounds", r)
		}
		a := float64(r.W * r.H)
		area += a
		if math.Abs(a/300000-float64(tile.Bytes)/1000) > .00001 {
			t.Fatal("incorrect area", tile)
		}
	}
	if math.Abs(area-300000) > 1 {
		t.Fatal("lost area", area)
	}
}

func TestNestedMapBudgetAndFileVisibility(t *testing.T) {
	s := index.New(`C:\test`)
	for d := 0; d < 100; d++ {
		dir := s.Add(0, "folder", 0, 0, 0, index.Directory)
		for i := 0; i < 100; i++ {
			s.Add(dir, "file.bin", uint64(i+1), uint64(i+1), 0, 0)
		}
	}
	s.Finalize()
	branches := Hierarchy(context.Background(), s, 0, false, 1024)
	cells := NestedLayout(branches, Rect{W: 1400, H: 400})
	if len(cells) > 1024 {
		t.Fatal("map budget exceeded", len(cells))
	}
	found := false
	for _, cell := range cells {
		if !s.Nodes[cell.Tile.ID].IsDir() && cell.Depth > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("nested files absent")
	}
}
func TestItemBudgetPreservesSmallFiles(t *testing.T) {
	s := index.New("C:\\test")
	var sum uint64
	for i := 1; i <= 10000; i++ {
		s.Add(0, "file", uint64(i), uint64(i), 0, 0)
		sum += uint64(i)
	}
	s.Finalize()
	items := Items(s, 0, false, 256)
	if len(items) > 256 {
		t.Fatal("tile budget exceeded")
	}
	var actual uint64
	other := false
	for _, i := range items {
		actual += i.Bytes
		other = other || i.Other
	}
	if sum != actual || !other {
		t.Fatal("small files disappeared", sum, actual)
	}
}
func TestEmptyMap(t *testing.T) {
	if len(Layout(nil, Rect{W: 100, H: 100})) != 0 {
		t.Fatal("nonempty")
	}
	if len(Layout([]Item{{Bytes: 1}}, Rect{})) != 0 {
		t.Fatal("zero bounds")
	}
}
