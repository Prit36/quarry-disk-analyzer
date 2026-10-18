package treemap

import (
	"context"
	"math"
	"quarry/internal/index"
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

func TestHierarchySharesDetailBudgetAcrossRootFolders(t *testing.T) {
	snapshot := index.New(`C:\nested`)
	large := snapshot.Add(0, "Large", 0, 0, 0, index.Directory)
	for folder := range 30 {
		parent := snapshot.Add(large, "nested", 0, 0, 0, index.Directory)
		for file := range 30 {
			snapshot.Add(parent, "file", uint64(10000+folder+file), uint64(10000+folder+file), 0, 0)
		}
	}
	for _, name := range []string{"Second", "Third"} {
		parent := snapshot.Add(0, name, 0, 0, 0, index.Directory)
		snapshot.Add(parent, "data", 100, 100, 0, 0)
	}
	snapshot.Finalize()
	branches := Hierarchy(context.Background(), snapshot, 0, false, 48)
	count := 0
	var verify func([]Branch)
	verify = func(nodes []Branch) {
		for _, branch := range nodes {
			count++
			if len(branch.Children) > 0 {
				var total uint64
				for _, child := range branch.Children {
					total += child.Item.Bytes
				}
				if total != branch.Item.Bytes {
					t.Fatal("detail budget lost bytes", total, branch.Item.Bytes)
				}
				verify(branch.Children)
			}
		}
	}
	verify(branches)
	if count > 48 {
		t.Fatal("detail budget exceeded", count)
	}
	for _, branch := range branches {
		if len(branch.Children) == 0 {
			t.Fatal("a root folder was starved by a larger sibling", snapshot.NameView(branch.Item.ID))
		}
	}
}

func TestNestedHeadersPreserveChildSpace(t *testing.T) {
	branches := []Branch{{Item: Item{ID: 1, Bytes: 100}, Children: []Branch{{Item: Item{ID: 2, Bytes: 100}, Children: []Branch{{Item: Item{ID: 3, Bytes: 60}}, {Item: Item{ID: 4, Bytes: 40}}}}}}}
	for _, height := range []float32{70, 145, 400} {
		cells := NestedLayout(branches, Rect{W: 300, H: height})
		if len(cells) != 4 {
			t.Fatal("visible nested folder lost descendants", height, cells)
		}
		for position, cell := range cells {
			if !cell.Group {
				continue
			}
			if cell.Header > cell.Tile.Rect.H*.45 || (cell.Depth > 0 && cell.Header > 34) {
				t.Fatal("directory label consumes child detail", cell)
			}
			child := cells[position+1].Tile.Rect
			parent := cell.Tile.Rect
			if child.X < parent.X+2 || child.Y < parent.Y+cell.Header || child.X+child.W > parent.X+parent.W-1.99 || child.Y+child.H > parent.Y+parent.H-1.99 {
				t.Fatal("child outside directory body", parent, child)
			}
		}
	}
}

func TestHierarchyHonorsCancellationForSmallDirectories(t *testing.T) {
	snapshot := index.New(`C:\cancelled`)
	snapshot.Add(0, "file", 100, 100, 0, 0)
	snapshot.Finalize()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if branches := Hierarchy(ctx, snapshot, 0, false, 4096); len(branches) != 0 {
		t.Fatal("cancelled hierarchy still enumerated a small directory")
	}
}

func TestMetadataVisibilityPreservesSnapshot(t *testing.T) {
	snapshot := index.New(`C:\`)
	snapshot.Engine = "NTFS MFT"
	snapshot.Add(0, "$BadClus", 1000, 0, 0, 0)
	snapshot.Add(0, "ordinary.bin", 100, 100, 0, 0)
	snapshot.Finalize()
	branches := HierarchyFiltered(context.Background(), snapshot, 0, false, 100, false)
	if len(branches) != 1 || branches[0].Item.Bytes != 100 || snapshot.Nodes[0].Size != 1100 {
		t.Fatal("metadata scope or global totals wrong")
	}
}

// benchSnapshot is a tree of folders holding many files of varied sizes, large
// enough that the hierarchy reaches its 6,000-tile budget.
func benchSnapshot() *index.Snapshot {
	s := index.New(`C:\bench`)
	for d := 0; d < 300; d++ {
		dir := s.Add(0, "folder", 0, 0, 0, index.Directory)
		for i := 0; i < 400; i++ {
			size := uint64((d*7919+i*104729)%100000 + 1)
			s.Add(dir, "file.bin", size, size, 0, 0)
		}
		for i := 0; i < 8; i++ {
			sub := s.Add(dir, "sub", 0, 0, 0, index.Directory)
			for j := 0; j < 50; j++ {
				size := uint64((i*31+j*17+d)%5000 + 1)
				s.Add(sub, "leaf.txt", size, size, 0, 0)
			}
		}
	}
	s.Finalize()
	return s
}

func BenchmarkHierarchy(b *testing.B) {
	s := benchSnapshot()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Hierarchy(context.Background(), s, 0, false, 6000)
	}
}

func BenchmarkNestedLayout(b *testing.B) {
	s := benchSnapshot()
	branches := Hierarchy(context.Background(), s, 0, false, 6000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NestedLayout(branches, Rect{W: 1400, H: 800})
	}
}
