package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCategories(t *testing.T) {
	for ext, want := range map[string]string{".mp4": "Videos", ".iso": "Disk images", ".msi": "Installers", ".7z": "Archives", ".ts": "Code", ".xyz": Other, "(no extension)": Other} {
		if got := CategoryOf(ext); got != want {
			t.Errorf("CategoryOf(%s) = %s, want %s", ext, got, want)
		}
	}
	for _, v := range []string{"video", "Videos", "disk images", "diskimage", "ARCH"} {
		if _, ok := FindCategory(v); !ok {
			t.Errorf("FindCategory(%q) failed", v)
		}
	}
	if _, ok := FindCategory("vi"); ok {
		t.Error("two letters must not match a category")
	}
	totals := SummarizeCategories([]FileType{{".mp4", 100, 100, 1}, {".mkv", 50, 60, 2}, {".foo", 500, 500, 1}, {".zip", 70, 70, 1}}, false)
	if totals[0].Name != "Videos" || totals[0].Bytes != 150 || totals[0].Count != 3 || !slices.Equal(totals[0].Top, []string{".mp4", ".mkv"}) {
		t.Fatalf("videos first: %+v", totals)
	}
	if totals[len(totals)-1].Name != Other {
		t.Fatalf("Other comes last whatever its size: %+v", totals)
	}
}

// ageFixture has files modified 10, 60, 200, 800 and 2000 days before now,
// one without a time and one online-only placeholder.
func ageFixture(now time.Time) *Snapshot {
	ago := func(days int) int64 { return now.Add(-time.Duration(days) * 24 * time.Hour).Unix() }
	s := New(`C:\age`)
	d := s.Add(0, "Downloads", 0, 0, 0, Directory)
	s.Add(d, "new.zip", 10, 10, ago(10), 0)
	s.Add(d, "recent.iso", 20, 20, ago(60), 0)
	s.Add(0, "year.mp4", 30, 30, ago(200), 0)
	s.Add(0, "old.log", 40, 40, ago(800), 0)
	s.Add(d, "ancient.MKV", 1000, 1000, ago(2000), 0)
	s.Add(0, "unknown.bin", 5, 5, 0, 0)
	s.Add(0, "cloud.docx", 70, 0, ago(5), CloudOnly)
	s.Finalize()
	return s
}

func TestAgeProfileMatchesAgeQueries(t *testing.T) {
	now := time.Now()
	s := ageFixture(now)
	p, err := s.AgeProfile(context.Background(), 0, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := []uint64{80, 20, 30, 40, 1000}
	for i, r := range AgeRanges {
		if p.Ranges[i].Bytes != wantBytes[i] {
			t.Errorf("%s: %d bytes, want %d", r.Label, p.Ranges[i].Bytes, wantBytes[i])
		}
		// The band's query finds exactly its files.
		f, err := ParseFilterAt(r.Query+" type:file", now)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := s.Search(context.Background(), 0, &f)
		if err != nil {
			t.Fatal(err)
		}
		var sum uint64
		for _, id := range ids {
			sum += s.Nodes[id].Size
		}
		if sum != wantBytes[i] || uint64(len(ids)) != p.Ranges[i].Files {
			t.Errorf("%s: query %q found %d files, %d bytes", r.Label, r.Query, len(ids), sum)
		}
	}
	if p.Unknown.Files != 1 || p.Total.Files != 7 || p.Stale.Bytes != 1040 || p.Cloud.Bytes != 70 {
		t.Fatalf("profile %+v", p)
	}
	big, _ := s.AgeProfile(context.Background(), 0, now, 100)
	if big.Total.Files != 1 || big.Stale.Files != 1 {
		t.Fatalf("minimum size: %+v", big)
	}
	// A scope that is a single file.
	one, err := s.AgeProfile(context.Background(), 3, now, 0)
	if err != nil || one.Total.Files != 1 {
		t.Fatalf("single file scope: %+v %v", one, err)
	}
}

func TestCategoryAndFlagSearch(t *testing.T) {
	now := time.Now()
	s := ageFixture(now)
	count := func(q string) int {
		t.Helper()
		f, err := ParseFilterAt(q, now)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		ids, err := s.Search(context.Background(), 0, &f)
		if err != nil {
			t.Fatal(err)
		}
		return len(ids)
	}
	for q, want := range map[string]int{
		"cat:videos":               2, // .mp4 and .MKV, any case
		"cat:videos;archives":      3,
		"cat:diskimages size:>=20": 1,
		"cat:videos age:>1y":       1,
		"is:cloud":                 1,
		"-is:cloud type:file":      6,
		"age:<1w":                  1,
		"age:>6m":                  3,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d matches, want %d", q, got, want)
		}
	}
	for _, bad := range []string{"cat:nothing", "age:5", "age:5x", "is:blue", "cat:"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	if f, _ := ParseFilter("cat:videos"); f.Empty() {
		t.Error("cat: is a criterion")
	}
}

// compareFixtures: previous and current scans of C:\cmp.
//
//	previous                     current
//	Users/ann/a.txt   100        Users/ANN/a.txt  150   grown
//	Users/ann/b.txt    50        (gone)                 removed
//	Users/ann/same.txt 10        Users/ANN/same.txt 10  unchanged
//	Old/x.iso         300        (gone)                 removed folder
//	                             New/y.zip 400          added folder
//	                             New/deep/z.bin 25
//	big.mp4          1000        big.mp4 900            shrunk
func compareFixtures() (*Snapshot, *Snapshot) {
	p := New(`C:\cmp`)
	u := p.Add(0, "Users", 0, 0, 0, Directory)
	a := p.Add(u, "ann", 0, 0, 0, Directory)
	p.Add(a, "a.txt", 100, 100, 1, 0)
	p.Add(a, "b.txt", 50, 50, 1, 0)
	p.Add(a, "same.txt", 10, 10, 1, 0)
	o := p.Add(0, "Old", 0, 0, 0, Directory)
	p.Add(o, "x.iso", 300, 300, 1, 0)
	p.Add(0, "big.mp4", 1000, 1000, 1, 0)
	p.Finalize()

	c := New(`C:\cmp\`)
	u = c.Add(0, "Users", 0, 0, 0, Directory)
	a = c.Add(u, "ANN", 0, 0, 0, Directory)
	c.Add(a, "a.txt", 150, 150, 2, 0)
	c.Add(a, "same.txt", 10, 10, 1, 0)
	n := c.Add(0, "New", 0, 0, 0, Directory)
	c.Add(n, "y.zip", 400, 400, 2, 0)
	d := c.Add(n, "deep", 0, 0, 0, Directory)
	c.Add(d, "z.bin", 25, 25, 2, 0)
	c.Add(0, "big.mp4", 900, 900, 2, 0)
	c.Finalize()
	return p, c
}

func TestComparisonByFolder(t *testing.T) {
	prev, cur := compareFixtures()
	cmp, err := NewComparison(prev, cur, false)
	if err != nil {
		t.Fatal(err)
	}
	root := cmp.Root()
	if root.Before != 1460 || root.After != 1485 || root.Delta() != 25 || root.Status != DiffGrown {
		t.Fatalf("root %+v", root)
	}
	top, err := cmp.Children(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiffEntry{}
	var order []string
	for _, e := range top {
		got[cmp.Name(e)] = e
		order = append(order, cmp.Name(e))
	}
	// Largest change first: New +425, Old −300, big.mp4 −100, Users 0.
	if !slices.Equal(order, []string{"New", "Old", "big.mp4", "Users"}) {
		t.Fatalf("order %q", order)
	}
	if got["New"].Status != DiffAdded || got["Old"].Status != DiffRemoved || got["big.mp4"].Status != DiffShrunk {
		t.Fatalf("statuses %+v", got)
	}
	// Users has the same size but different contents.
	users := got["Users"]
	if users.Status != DiffModified || users.Prev == None || users.Cur == None {
		t.Fatalf("Users %+v", users)
	}
	inner, _ := cmp.Children(context.Background(), users)
	ann := inner[0]
	if cmp.Name(ann) != "ANN" || ann.Status != DiffModified {
		t.Fatalf("case-insensitive match %+v %s", ann, cmp.Name(ann))
	}
	files, _ := cmp.Children(context.Background(), ann)
	status := map[string]DiffStatus{}
	for _, e := range files {
		status[cmp.Name(e)] = e.Status
	}
	if status["a.txt"] != DiffGrown || status["b.txt"] != DiffRemoved || status["same.txt"] != DiffUnchanged {
		t.Fatalf("files %v", status)
	}
	if cmp.Path(got["Old"]) != `C:\cmp\Old` {
		t.Fatalf("a removed entry's path comes from the previous scan: %s", cmp.Path(got["Old"]))
	}
	if _, err := NewComparison(prev, New(`D:\other`), false); err == nil {
		t.Fatal("different roots must not compare")
	}
}

func TestComparisonLargestChanges(t *testing.T) {
	prev, cur := compareFixtures()
	cmp, _ := NewComparison(prev, cur, false)
	all, totals, err := cmp.LargestChanges(context.Background(), cmp.Root(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range all {
		names = append(names, cmp.Name(e))
	}
	// Equal changes: the larger file first (a.txt is 150, b.txt was 50).
	want := []string{"y.zip", "x.iso", "big.mp4", "a.txt", "b.txt", "z.bin"}
	if !slices.Equal(names, want) {
		t.Fatalf("largest changes %q, want %q", names, want)
	}
	if totals.Added != 2 || totals.AddedBytes != 425 || totals.Removed != 2 || totals.RemovedBytes != 350 || totals.Grown != 1 || totals.Shrunk != 1 || totals.GrownBytes != 50 || totals.ShrunkBytes != 100 {
		t.Fatalf("totals %+v", totals)
	}
	top2, totals2, _ := cmp.LargestChanges(context.Background(), cmp.Root(), 2, func(s DiffStatus) bool { return s == DiffAdded })
	if len(top2) != 2 || cmp.Name(top2[0]) != "y.zip" || totals2 != totals {
		t.Fatalf("limited to added: %v %+v", top2, totals2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := cmp.LargestChanges(ctx, cmp.Root(), 10, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestGraftReplacesOneFolder(t *testing.T) {
	s := New(`C:\g`)
	a := s.Add(0, "a", 0, 0, 0, Directory)
	s.Add(a, "old1", 100, 100, 1, 0)
	s.Add(a, "old2", 50, 50, 1, 0)
	b := s.Add(0, "b", 0, 0, 0, Directory)
	keep := s.Add(b, "keep", 7, 8, 1, 0)
	s.Finalize()
	s.Nodes[a].Flags |= Incomplete
	s.Issues = []Issue{{`C:\g\a\old1`, "denied"}, {`C:\g\b`, "denied"}}
	s.Errors = 2

	fresh := New(`C:\g\a`)
	sub := fresh.Add(0, "sub", 0, 0, 0, Directory)
	fresh.Add(sub, "n1", 30, 32, 2, 0)
	fresh.Add(0, "n2", 5, 8, 2, 0)
	fresh.Finalize()

	g, err := s.Graft(a, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if g.Nodes[0].Size != 42 || g.Nodes[0].Files != 3 || g.Nodes[0].Dirs != 3 {
		t.Fatalf("root totals %+v", g.Nodes[0])
	}
	if g.Nodes[a].Size != 35 || g.Nodes[a].Flags&Incomplete != 0 || g.Nodes[a].Dirs != 1 {
		t.Fatalf("grafted folder %+v", g.Nodes[a])
	}
	if !g.Reachable(keep) || g.Path(keep) != `C:\g\b\keep` {
		t.Fatal("other IDs must stay valid")
	}
	if g.Reachable(2) || g.Reachable(3) {
		t.Fatal("the folder's previous contents must be detached")
	}
	var paths []string
	_ = g.Visit(context.Background(), a, func(id ID) error { paths = append(paths, g.Path(id)); return nil })
	slices.Sort(paths)
	if !slices.Equal(paths, []string{`C:\g\a`, `C:\g\a\n2`, `C:\g\a\sub`, `C:\g\a\sub\n1`}) {
		t.Fatalf("grafted paths %q", paths)
	}
	if g.Errors != 1 || len(g.Issues) != 1 || g.Issues[0].Path != `C:\g\b` {
		t.Fatalf("issues below the folder are replaced: %d %+v", g.Errors, g.Issues)
	}
	if s.Nodes[0].Size != 157 || s.Reachable(2) != true {
		t.Fatal("the original snapshot must be unchanged")
	}
	// The result saves and loads as an ordinary scan.
	path := filepath.Join(t.TempDir(), "g.quarry")
	if err := g.Save(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadSnapshot(context.Background(), path)
	if err != nil || back.Nodes[0].Size != 42 || len(back.Nodes) != 7 {
		t.Fatalf("reload: %v %d", err, len(back.Nodes))
	}
	if _, err := g.Graft(keep, fresh); err == nil {
		t.Fatal("a file cannot be refreshed")
	}
}

func TestHistorySummariesAndPruning(t *testing.T) {
	dir := t.TempDir()
	prev, cur := compareFixtures()
	prev.Started = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	cur.Started = prev.Started.Add(48 * time.Hour)
	save := func(s *Snapshot) func(string) error {
		return func(p string) error { return s.Save(context.Background(), p) }
	}
	if _, err := prev.RecordHistory(dir, true, 5, 10, save(prev)); err != nil {
		t.Fatal(err)
	}
	if _, err := cur.RecordHistory(dir, true, 5, 10, save(cur)); err != nil {
		t.Fatal(err)
	}
	other := New(`D:\x`)
	other.Started = cur.Started
	other.Finalize()
	if _, err := other.RecordHistory(dir, false, 5, 10, nil); err != nil {
		t.Fatal(err)
	}
	list, err := ListHistory(dir, `c:\CMP\`)
	if err != nil || len(list) != 2 || !list[0].Started.Equal(cur.Started) || list[0].Scan == "" {
		t.Fatalf("history %+v %v", list, err)
	}
	growth := CompareSummaries(list[1], list[0], false)
	byPath := map[string]int64{}
	for _, g := range growth {
		byPath[strings.ToLower(g.Path)] = g.Delta()
	}
	if byPath["new"] != 425 || byPath["old"] != -300 || growth[0].Path != "New" {
		t.Fatalf("growth %+v", growth)
	}
	if err := PruneHistory(dir, `C:\cmp`, 1, 1); err != nil {
		t.Fatal(err)
	}
	list, _ = ListHistory(dir, `C:\cmp`)
	if len(list) != 1 || list[0].Scan == "" {
		t.Fatalf("after pruning %+v", list)
	}
	all, _ := ListHistory(dir, "")
	if len(all) != 2 {
		t.Fatalf("other roots are kept: %d", len(all))
	}
	if err := DeleteHistory(list[0]); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("delete leaves %d files", len(entries))
	}
}

func TestRecheckDuplicates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "same contents")
	write("b.txt", "same contents")
	write("c.txt", "diff contents")
	s := New(dir)
	ids := map[string]ID{}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		info, _ := os.Stat(filepath.Join(dir, name))
		ids[name] = s.Add(0, name, uint64(info.Size()), uint64(info.Size()), info.ModTime().Unix(), 0)
	}
	s.Finalize()
	if err := s.RecheckDuplicates(context.Background(), ids["a.txt"], []ID{ids["b.txt"]}, nil); err != nil {
		t.Fatalf("identical copies: %v", err)
	}
	if err := s.RecheckDuplicates(context.Background(), ids["a.txt"], []ID{ids["c.txt"]}, nil); err == nil || !strings.Contains(err.Error(), "same contents") {
		t.Fatalf("different contents must be refused: %v", err)
	}
	write("b.txt", "same contents, longer now")
	if err := s.RecheckDuplicates(context.Background(), ids["a.txt"], []ID{ids["b.txt"]}, nil); err == nil {
		t.Fatal("a file changed since the scan must be refused")
	}
	if err := s.RecheckDuplicates(context.Background(), ids["a.txt"], []ID{ids["a.txt"]}, nil); err == nil {
		t.Fatal("the keeper cannot also be deleted")
	}
}
