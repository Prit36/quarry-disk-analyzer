package desktop

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/treemap"
)

func TestTreemapHitMatchesReverseScan(t *testing.T) {
	s := treemapFixture()
	m := treemapModel{branches: treemap.Hierarchy(context.Background(), s, 0, false, 6000)}
	for _, size := range [][2]float32{{1600, 900}, {640, 360}, {0, 0}, {1900, 1150}} {
		m.layout(size[0], size[1])
		check := func(x, y float32) {
			want := -1
			for i := len(m.cells) - 1; i >= 0; i-- {
				r := m.cells[i].Tile.Rect
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					want = i
					break
				}
			}
			if got := m.hit(x, y); got != want {
				t.Fatalf("size %v at (%g, %g): hit %d, want %d", size, x, y, got, want)
			}
		}
		for x := float32(-1); x <= size[0]+1; x += 13 {
			for y := float32(-1); y <= size[1]+1; y += 17 {
				check(x, y)
			}
		}
		for _, c := range m.cells {
			r := c.Tile.Rect
			check(r.X, r.Y)
			check(r.X+r.W, r.Y+r.H)
		}
	}
}

func awaitQueued(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.queue) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(a.queue) == 0 {
		t.Fatal("background result was not queued")
	}
}

func TestResetRejectsQueuedResults(t *testing.T) {
	a := testApp(t)
	a.files.run()
	awaitQueued(t, a)
	a.files.reset()
	a.drain()
	if a.files.forSnap != nil || len(a.files.ids) != 0 {
		t.Fatal("reset applied an old file query")
	}
	a.ext.run()
	awaitQueued(t, a)
	a.ext.reset()
	a.drain()
	if a.ext.forSnap != nil || len(a.ext.types) != 0 {
		t.Fatal("reset applied old extension groups")
	}
	a.tm.ensure()
	awaitQueued(t, a)
	a.tm.reset()
	a.drain()
	if len(a.tm.branches) != 0 {
		t.Fatal("reset applied an old treemap")
	}
}

func TestTypingRejectsQueuedQuery(t *testing.T) {
	a := testApp(t)
	a.files.run()
	awaitQueued(t, a)
	a.search = "*.log"
	a.files.queryChanged(time.Now())
	a.drain()
	if a.files.forSnap != nil || !a.files.dirty || a.files.busy {
		t.Fatal("a superseded query replaced results during typing")
	}
}

func TestResortReuseTracksScopeAndMetric(t *testing.T) {
	a := testApp(t)
	run := func() {
		a.files.run()
		awaitQueued(t, a)
		a.drain()
	}
	run()
	previous, total := slices.Clone(a.files.ids), a.files.total
	a.files.sort = ui.SortOrder{Column: "Name"}
	run()
	want := slices.Clone(previous)
	a.snap.Sort(want, index.Sort{Column: "Name"})
	if !slices.Equal(a.files.ids, want) || a.files.total != total {
		t.Fatal("resorting changed matches or their total")
	}
	a.files.scope = 6 // docs
	run()
	if !slices.Equal(a.files.ids, []index.ID{7}) || a.files.total != 300 {
		t.Fatal("reused results from another scope")
	}
	a.prefs.Metric = "Allocated"
	next := a.snap.Clone()
	next.Nodes[7].Allocated = 600
	next.Reaggregate()
	a.snap = next
	run()
	if a.files.total != 600 {
		t.Fatal("reused a total from another metric or snapshot")
	}
}

func TestFilterAncestorCacheMatchesReference(t *testing.T) {
	s := fixture()
	for _, query := range []string{"path:data", "path:media", "path:sub ext:log", "path:docs", "path:missing"} {
		f := analysisFilter{Include: query}
		got, stats, err := f.apply(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		include, err := index.ParseFilter(query)
		if err != nil {
			t.Fatal(err)
		}
		include.Kind = index.KindFiles
		want, wantStats, err := s.Filtered(context.Background(), func(id index.ID) bool {
			return include.MatchNode(s.NameView(id), s.Nodes[id], func() string { return s.Path(id) }, ancestorMatchesLocal(s, id, &include))
		}, nil, true)
		if err != nil || stats != wantStats || !slices.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("%q differs from the ancestor-walking reference: %v", query, err)
		}
	}
}

func ancestorMatchesLocal(s *index.Snapshot, id index.ID, f *index.Filter) bool {
	if !f.AncestorTerms() {
		return false
	}
	for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
		name := s.Root
		if p != 0 {
			name = s.NameView(p)
		}
		if f.MatchPathComponent(name) {
			return true
		}
	}
	return false
}

func treemapFixture() *index.Snapshot {
	s := index.New(`C:\performance`)
	for i := range 96 {
		dir := s.Add(0, fmt.Sprintf("folder-%d", i), 0, 0, 0, index.Directory)
		for j := range 96 {
			s.Add(dir, fmt.Sprintf("file-%d.bin", j), uint64(1000+j), uint64(2000+j), 0, 0)
		}
	}
	s.Finalize()
	return s
}

func BenchmarkTreemapHit(b *testing.B) {
	s := treemapFixture()
	m := treemapModel{branches: treemap.Hierarchy(context.Background(), s, 0, false, 6000)}
	m.layout(1600, 900)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		m.hit(float32(i*73%1600), float32(i*31%900))
	}
}

func BenchmarkTreemapHoverFrames(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.prefs.ShowTreemap = true
	a.prefs.Theme = "Dark"
	a.tm.maximized = true
	a.setBase(treemapFixture(), "synthetic.quarry", nil, false)
	tt := ui.NewTester(a.view, 1600, 1000)
	tt.Frame()
	deadline := time.Now().Add(10 * time.Second)
	for a.tm.busy && time.Now().Before(deadline) {
		a.drain()
		time.Sleep(time.Millisecond)
	}
	if a.tm.busy {
		b.Fatal("treemap did not finish")
	}
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		tt.Move(float32(100+i%3), 500)
		tt.Frame()
	}
}

func filePerformanceFixture() *index.Snapshot {
	s := index.New(`C:\performance`)
	for i := range 128 {
		dir := s.Add(0, fmt.Sprintf("folder-%d", i), 0, 0, 0, index.Directory)
		for j := range 1024 {
			s.Add(dir, fmt.Sprintf("file-%d.DLL", j), uint64(1000+j), uint64(2000+j), int64(j), 0)
		}
	}
	s.Finalize()
	return s
}

func BenchmarkFileResortLocal(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.setBase(filePerformanceFixture(), "synthetic.quarry", nil, false)
	a.prefs.View = viewFiles
	run := func() {
		a.files.run()
		for a.files.busy {
			if !a.drain() {
				runtime.Gosched()
			}
		}
		if a.files.err != "" {
			b.Fatal(a.files.err)
		}
	}
	run()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		a.files.sort = ui.SortOrder{Column: "Size", Descending: i%2 == 0}
		run()
	}
}

func BenchmarkAnalysisFilterDeep(b *testing.B) {
	s := index.New(`C:\performance`)
	parent := index.ID(0)
	for i := range 64 {
		parent = s.Add(parent, fmt.Sprintf("level-%d", i), 0, 0, 0, index.Directory)
	}
	for i := range 100000 {
		s.Add(parent, fmt.Sprintf("file-%d.bin", i), uint64(i), uint64(i), 0, 0)
	}
	s.Finalize()
	f := analysisFilter{Include: "path:level-10 ext:bin"}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := f.apply(context.Background(), s); err != nil {
			b.Fatal(err)
		}
	}
}
