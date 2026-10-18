package desktop

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestSelectionCacheFollowsTableInput(t *testing.T) {
	a := testApp(t)
	a.prefs.ShowTreemap = false
	a.setViewMode(viewFiles)
	waitModel(t, a, func() bool { return !a.files.busy })
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	if err := tt.Click("big.mp4"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.selected(); !slices.Equal(got, []index.ID{2}) {
		t.Fatalf("click selection: %v", got)
	}
	version := a.files.selCache.version
	for range 5 {
		tt.Frame()
	}
	if a.files.selCache.version != version {
		t.Fatal("idle frames rebuild an unchanged selection")
	}
	// Replacing one selected item with another keeps the same cardinality;
	// the table's change notification must invalidate its cached IDs.
	if err := tt.Click("clip.mp4"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.selected(); !slices.Equal(got, []index.ID{3}) {
		t.Fatalf("same-count selection change: %v", got)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Frame()
	if len(a.selected()) != 5 {
		t.Fatalf("select all: %v", a.selected())
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if len(a.selected()) != 1 {
		t.Fatalf("keyboard changed selection: %v", a.selected())
	}
}

func TestMutableSelectionMatchesPathChecks(t *testing.T) {
	for _, root := range []string{`C:\`, `C:\data`, `C:\data\..`, `C:\data\`, `\\server\share`, "relative"} {
		for _, engine := range []string{"directory enumeration", "NTFS MFT"} {
			s := index.New(root)
			s.Engine = engine
			for _, name := range []string{"Windows", "BOOT", "pagefile.sys", "ordinary", "$Extend", "$MFT", "System Volume Information", "..", `.\Windows`, ""} {
				dir := s.Add(0, name, 0, 0, 0, index.Directory)
				s.Add(dir, "child", 1, 1, 0, 0)
				s.Add(dir, "lost", 1, 1, 0, index.Unplaced)
			}
			detached := s.Add(index.None, "Windows", 1, 1, 0, 0)
			s.Add(detached, "file", 1, 1, 0, 0)
			a := &App{snap: s}
			for _, rootUnplaced := range []bool{false, true} {
				if rootUnplaced {
					s.Nodes[0].Flags |= index.Unplaced
				}
				for i := range s.Nodes {
					id := index.ID(i)
					ids := make([]index.ID, 64)
					for j := range ids {
						ids[j] = id
					}
					want := a.blocked(id) == ""
					if got := a.mutableSelection(ids); got != want {
						t.Fatalf("root %q engine %q root unplaced %v id %d name %q: got %v want %v", root, engine, rootUnplaced, id, s.NameView(id), got, want)
					}
				}
			}
		}
	}
}

func BenchmarkSelectionAvailability(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	a.setBase(syntheticUISnapshot(), "", nil, false)
	a.setViewMode(viewFiles)
	for a.files.busy {
		a.drain()
	}
	a.files.selectAll()
	a.selected()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !a.mutable() {
			b.Fatal("ordinary selection was blocked")
		}
	}
}

func BenchmarkUnrestrictedSearch(b *testing.B) {
	s := syntheticUISnapshot()
	f, _ := index.ParseFilter("type:file")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Search(context.Background(), 0, &f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelectedFrames(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	a.prefs.ShowTreemap = false
	a.setBase(syntheticUISnapshot(), "", nil, false)
	a.setViewMode(viewFiles)
	for a.files.busy {
		a.drain()
	}
	a.files.selectAll()
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tt.Frame()
	}
}
