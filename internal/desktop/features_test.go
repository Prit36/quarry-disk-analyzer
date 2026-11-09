package desktop

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

func TestQueryTermsAndOperatorSuggestions(t *testing.T) {
	q := `report "my file" size:>1GB age:>1y -cat:videos`
	if got := splitQuery(q); !slices.Equal(got, []string{"report", `"my file"`, "size:>1GB", "age:>1y", "-cat:videos"}) {
		t.Fatalf("split %q", got)
	}
	if got := queryTerms(q, sizeFields); got != "size:>1GB" {
		t.Fatalf("size terms %q", got)
	}
	if got := withTerms(q, sizeFields, "size:<1MB"); got != `report "my file" age:>1y -cat:videos size:<1MB` {
		t.Fatalf("replaced %q", got)
	}
	if got := withTerms(q, ageFields, ""); got != `report "my file" size:>1GB -cat:videos` {
		t.Fatalf("removed %q", got)
	}
	// A drive path is not a field.
	if termField(`C:\Users`) != "" || termField("Ext:mp4") != "ext" {
		t.Fatal("termField")
	}

	if s := operatorSuggestions("report ag"); len(s) == 0 || s[0].text != "age:>" {
		t.Fatalf("suggestions for ag: %v", s)
	}
	if s := operatorSuggestions("cat:"); len(s) != len(index.Categories) {
		t.Fatalf("category values: %v", s)
	}
	for _, q := range []string{"", "a", "report ", "age:>1y", `"ty`} {
		if s := operatorSuggestions(q); len(s) != 0 && q != "a" {
			t.Fatalf("%q suggested %v", q, s)
		}
	}
	if s := operatorSuggestions("a"); s != nil {
		t.Fatal("a single letter must not interrupt typing")
	}
	if got := completeOperator("report ty", operatorHint{text: "type:file"}); got != "report type:file" {
		t.Fatalf("completed %q", got)
	}
}

// Every preset and filter control writes a query the parser accepts, in
// both unit systems.
func TestPresetsAndFilterChipsParse(t *testing.T) {
	a := testApp(t)
	for _, units := range []string{"Binary", "Decimal"} {
		a.prefs.Units = units
		for _, p := range a.presets() {
			if _, err := index.ParseFilter(p.query); err != nil {
				t.Fatalf("%s: %q: %v", p.label, p.query, err)
			}
		}
		for _, q := range []string{a.sizeTerm(">", 1, "GB"), a.sizeTerm("<", 1, "MB"), "age:<7d", "age:>3y", "cat:diskimages", "cat:other", "is:cloud", "-is:cloud", "is:hardlink"} {
			if _, err := index.ParseFilter(q); err != nil {
				t.Fatalf("%q: %v", q, err)
			}
		}
	}
	if a.sizeTerm(">", 100, "MB") != "size:>100MB" {
		t.Fatal("decimal units")
	}
	a.prefs.Units = "Binary"
	if a.sizeTerm(">", 100, "MB") != "size:>100MiB" {
		t.Fatal("binary units")
	}
}

func TestShowQueryAndFilterChipsInFileView(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.showQuery("cat:videos type:file", 1)
	settle(tt, func() bool { return !a.files.busy && a.files.ran != "" })
	if a.prefs.View != viewFiles || !a.files.pinned || a.files.scope != 1 || len(a.files.ids) != 2 {
		t.Fatalf("view %d scope %d ids %v err %q", a.prefs.View, a.files.scope, a.files.ids, a.files.err)
	}
	if !tt.HasText("Filters · 1") {
		t.Fatalf("filter chip count missing: %q", tt.Texts())
	}
	// A size chip replaces only the size terms.
	a.setQueryGroup(sizeFields, a.sizeTerm(">", 500, "B"))
	settle(tt, func() bool { return !a.files.busy && strings.Contains(a.files.ran, "size") })
	if !strings.Contains(a.search, "cat:videos") || len(a.files.ids) != 1 || a.files.ids[0] != 2 {
		t.Fatalf("search %q ids %v", a.search, a.files.ids)
	}
	// Category other: everything no category claims (.log is temporary,
	// .pdf documents, .txt documents, .mp4 videos), so nothing here.
	a.showQuery("cat:other type:file", 0)
	settle(tt, func() bool { return !a.files.busy && a.files.ran == "cat:other type:file" })
	if len(a.files.ids) != 0 {
		t.Fatalf("cat:other %v", a.files.ids)
	}
}

// Suggestions appear while typing an operator, Tab takes the first.
func TestSearchSuggestionsTab(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.exec("edit.find")
	tt.Frame()
	tt.Type("siz")
	tt.Frame()
	if !tt.HasText("size:>") {
		t.Fatalf("no suggestion in %q", tt.Texts())
	}
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if a.search != "size:>" {
		t.Fatalf("Tab gave %q", a.search)
	}
	tt.Type("500B")
	settle(tt, func() bool { return !a.files.busy && a.files.ran == "size:>500B" })
	if len(a.files.ids) != 2 {
		t.Fatalf("completed search %q found %v", a.search, a.files.ids)
	}
}

type fakeInfo struct {
	dir  bool
	size int64
}

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

func TestEvaluateBasket(t *testing.T) {
	items := []basketItem{
		{path: `C:\data\media\big.mp4`, size: 900, allocated: 1000},
		{path: `C:\data\media`, dir: true, size: 1050, allocated: 1200, files: 3},
		{path: `C:\data\readme.txt`, size: 10, allocated: 4096},
		{path: `C:\data\docs\report.pdf`, size: 300, allocated: 4096},
		{path: `C:\Windows`, dir: true, size: 5},
		{path: `C:\gone.txt`, size: 1},
		{path: `D:\elsewhere`, dir: true},
	}
	ids := map[string]index.ID{`c:\data\media\big.mp4`: 2, `c:\data\media`: 1, `c:\data\readme.txt`: 8, `c:\data\docs\report.pdf`: 7, `c:\windows`: 20, `c:\gone.txt`: 21}
	find := func(p string) basketFound {
		id, ok := ids[strings.ToLower(p)]
		f := basketFound{id: id, ok: ok}
		if strings.EqualFold(p, `C:\Windows`) {
			f.reason = "a Windows system location"
		}
		if strings.EqualFold(p, `C:\data\readme.txt`) {
			f.risky = "careful"
		}
		return f
	}
	stat := func(p string) (fs.FileInfo, error) {
		switch strings.ToLower(p) {
		case `c:\gone.txt`:
			return nil, errors.New("gone")
		case `c:\data\docs\report.pdf`:
			return fakeInfo{size: 301}, nil // grew after the scan
		case `c:\data\media`:
			return fakeInfo{dir: true}, nil
		}
		return fakeInfo{size: 10}, nil
	}
	rows := evaluateBasket(items, func(i int) basketFound { return find(items[i].path) }, stat)
	want := []basketStatus{basketCovered, basketReady, basketReady, basketChanged, basketBlocked, basketMissing, basketNotFound}
	for i, r := range rows {
		if r.status != want[i] {
			t.Fatalf("row %d (%s): %v, want %v (%s)", i, r.item.path, r.status, want[i], r.note)
		}
	}
	if rows[0].note != `C:\data\media` || !strings.Contains(rows[0].statusText(), "media") {
		t.Fatalf("covered note %q", rows[0].note)
	}
	tot := sumBasket(rows)
	if tot.items != 2 || tot.size != 1060 || tot.allocated != 1200+4096 || tot.files != 4 || tot.excluded != 4 || !tot.risky {
		t.Fatalf("totals %+v", tot)
	}
}

func TestBasketCollectsAndReviews(t *testing.T) {
	a, ids := duplicateApp(t)
	a.prefs.View = viewTree
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.reveal(ids["local-a"])
	a.exec("item.basketAdd")
	a.tree.selectOnly(ids["nested"])
	a.exec("item.basketAdd")
	a.exec("item.basketAdd") // twice: kept once
	if len(a.basket.items) != 2 {
		t.Fatalf("basket %+v", a.basket.items)
	}
	a.exec("tools.basket")
	settle(tt, func() bool { return !a.basket.busy && a.basket.rows != nil })
	tot := sumBasket(a.basket.rows)
	nested := a.base.Nodes[ids["nested"]]
	if tot.items != 1 || tot.size != nested.Size || tot.files != uint64(nested.Files) {
		t.Fatalf("overlap counted twice: %+v rows %+v", tot, a.basket.rows)
	}
	if !tt.HasText("Cleanup Basket") || !tt.HasText("Move to Recycle Bin…") {
		t.Fatalf("review dialog %q", tt.Texts())
	}
	// The scan root is never collected.
	a.tree.selectOnly(0)
	a.exec("item.basketAdd")
	if len(a.basket.items) != 2 || !a.errorMsg {
		t.Fatal("the scan root was added to the basket")
	}
	// Removing from the selection.
	a.clearError()
	a.reveal(ids["local-a"])
	if !a.selectionInBasket() {
		t.Fatal("selectionInBasket")
	}
	a.exec("item.basketRemove")
	if len(a.basket.items) != 1 {
		t.Fatalf("remove left %+v", a.basket.items)
	}
}

func TestCleanupFeedback(t *testing.T) {
	if s := cleanupFeedback(3, 3, "1.0 GiB", false, false); !strings.Contains(s, "Recycle Bin") || !strings.Contains(s, "until the Recycle Bin is emptied") {
		t.Fatal(s)
	}
	if s := cleanupFeedback(2, 3, "1.0 GiB", true, false); !strings.Contains(s, "permanently") || !strings.Contains(s, "1 could not be removed") {
		t.Fatal(s)
	}
	if s := cleanupFeedback(0, 3, "", false, true); !strings.Contains(s, "canceled") {
		t.Fatal(s)
	}
}

// later is fixture with media/big.mp4 grown, docs/report.pdf removed and a
// new folder.
func later() *index.Snapshot {
	s := index.New(`C:\data`)
	media := s.Add(0, "media", 0, 0, 0, index.Directory)
	s.Add(media, "big.mp4", 1900, 1900, 2, 0)
	s.Add(media, "clip.mp4", 100, 100, 1, 0)
	sub := s.Add(media, "sub", 0, 0, 0, index.Directory)
	s.Add(sub, "a.log", 50, 50, 1, 0)
	s.Add(0, "docs", 0, 0, 0, index.Directory)
	s.Add(0, "readme.txt", 10, 10, 1, 0)
	fresh := s.Add(0, "new", 0, 0, 0, index.Directory)
	s.Add(fresh, "n.iso", 70, 70, 2, 0)
	s.Finalize()
	return s
}

func TestComparisonDialog(t *testing.T) {
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	a := newApp()
	a.prefs.Theme = "Dark"
	cur := later()
	a.setBase(cur, "", nil, false)
	cmp, err := index.NewComparison(fixture(), cur, false)
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1280, 800)
	a.showComparison(cmp, "test.quarry")
	v := &a.compareView
	settle(tt, func() bool { return v.lastKey != (compareKey{}) && !v.busy })
	names := func() []string {
		var out []string
		for _, e := range v.rows {
			out = append(out, cmp.Name(e)+":"+e.Status.String())
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"media:Grown", "docs:Shrunk", "new:Added"}) {
		t.Fatalf("root rows %q", got)
	}
	if !tt.HasText("Changes since test.quarry") || !tt.HasText("+770 B") {
		t.Fatalf("summary %q", tt.Texts())
	}
	// Into docs: its file exists only in the previous scan.
	v.stack = append(v.stack, v.rows[1])
	settle(tt, func() bool { return !v.busy && len(v.rows) == 1 })
	if v.rows[0].Status != index.DiffRemoved || a.revealable(v.rows[0]) || !tt.HasText("Only in previous scan") {
		t.Fatalf("removed row %+v", v.rows)
	}
	// Largest changed files, filtered to growth.
	v.stack = v.stack[:1]
	v.mode, v.filter = compareFiles, "Grown"
	settle(tt, func() bool { return !v.busy && v.hasTot })
	if got := names(); !slices.Equal(got, []string{"big.mp4:Grown"}) || v.totals.Added != 1 || v.totals.Removed != 1 {
		t.Fatalf("largest %q totals %+v", got, v.totals)
	}
	if !a.revealable(v.rows[0]) {
		t.Fatal("a current file of the scan shown should be revealable")
	}
	tt.Click("Reveal in Tree")
	v.cursor = 0
	tt.Frame()
	if err := tt.Click("Reveal in Tree"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.open || a.prefs.View != viewTree || a.tree.cursorID() != v.rows[0].Cur {
		t.Fatalf("reveal: open %v view %d cursor %d", v.open, a.prefs.View, a.tree.cursorID())
	}
}

func TestInsightsDialog(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.exec("tools.insights")
	v := &a.insights
	settle(tt, func() bool { return !v.busy && v.data != nil })
	d := v.data
	if d.dominant.Key != "videos" || d.dominant.Bytes != 1000 || d.all.Total.Files != 5 || d.all.Stale.Files != 5 {
		t.Fatalf("overview %+v", d)
	}
	if !tt.HasText("Storage Insights") || !tt.HasText("Videos · 1000 B in 2 files") {
		t.Fatalf("overview texts %q", tt.Texts())
	}
	v.tab = insightsAge
	tt.Frame()
	if !tt.HasText("Over 3 years") || v.age == nil || v.age.Ranges[len(v.age.Ranges)-1].Files != 5 {
		t.Fatalf("age tab %q", tt.Texts())
	}
	v.minSize = ageMinimums[1]
	a.computeAge()
	settle(tt, func() bool { return !v.ageBusy })
	if v.age.Total.Files != 0 {
		t.Fatal("1 MB minimum still counts small files")
	}
	v.tab = insightsCategories
	tt.Frame()
	if !tt.HasText("Documents") || !tt.HasText("Videos") {
		t.Fatalf("categories %q", tt.Texts())
	}
	// A bucket leads to its files in File View, within the scope.
	a.insightQuery(index.AgeRanges[4].Query + " type:file")
	settle(tt, func() bool { return !a.files.busy && a.files.ran != "" })
	if v.open || a.prefs.View != viewFiles || len(a.files.ids) != 5 {
		t.Fatalf("age drill-down %v", a.files.ids)
	}
	// Folder details for one folder.
	a.openInsights(1, insightsOverview)
	settle(tt, func() bool { return !v.busy && v.data != nil })
	if !tt.HasText("Folder Details") || v.data.node.Files != 3 || len(v.data.folders) != 1 {
		t.Fatalf("folder details %q", tt.Texts())
	}
}

func TestTreemapHighlightsMatches(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	settle(tt, func() bool { return !a.tm.busy && a.tm.cells != nil })
	a.showQuery("*.mp4", 0)
	settle(tt, func() bool { return !a.files.busy && a.files.ran == "*.mp4" && !a.tm.busy })
	var lit []bool
	settle(tt, func() bool { lit = a.treemapMatches(a.mapSnapshot(), a.tm.cells); return lit != nil })
	litOf := func(id index.ID) bool {
		i, ok := a.tm.cellOf[id]
		if !ok {
			t.Fatalf("no cell for %d", id)
		}
		return lit[i]
	}
	if !litOf(2) || litOf(7) || litOf(8) {
		t.Fatal("wrong tiles highlighted")
	}
	a.exec("view.mapMatches")
	if a.treemapMatches(a.mapSnapshot(), a.tm.cells) != nil {
		t.Fatal("highlighting stays on when turned off")
	}
}

func TestRefreshFolderAndRetry(t *testing.T) {
	a, ids := duplicateApp(t)
	nested := ids["nested"]
	before := a.base.Nodes[0]
	path := filepath.Join(a.base.Path(nested), "added.bin")
	if err := os.WriteFile(path, make([]byte, 1234), 0600); err != nil {
		t.Fatal(err)
	}
	a.refreshFolder(nested)
	waitModel(t, a, func() bool { return !a.refreshing })
	if a.errorMsg {
		t.Fatal(a.message)
	}
	id, ok := a.findBase(path)
	if !ok || a.base.Nodes[id].Size != 1234 {
		t.Fatal("refreshed folder lacks the new file")
	}
	if a.base.Nodes[0].Size != before.Size+1234 || a.base.Nodes[0].Files != before.Files+1 {
		t.Fatalf("root totals %d/%d, before %d/%d", a.base.Nodes[0].Size, a.base.Nodes[0].Files, before.Size, before.Files)
	}
	// Retrying an issue refreshes its nearest scanned folder.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.retryIssue(index.Issue{Path: filepath.Join(a.base.Path(nested), "deeper", "x"), Message: "denied"})
	waitModel(t, a, func() bool { return !a.refreshing })
	if _, ok := a.findBase(path); ok {
		t.Fatal("retry did not refresh the folder")
	}
}

func TestSkipFolderInFutureScans(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	a.tree.selectOnly(6)
	a.exec("item.skip")
	tt.Frame()
	if !a.skip.open || !tt.HasText(`C:\data\docs`) {
		t.Fatalf("skip dialog %q", tt.Texts())
	}
	if err := tt.Click("Skip in Future Scans"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.skip.open || !slices.Contains(a.prefs.Exclusions, `C:\data\docs`) || !a.prefs.UseExclusions {
		t.Fatalf("exclusions %q", a.prefs.Exclusions)
	}
}

func TestHistoryRecordsAndAlerts(t *testing.T) {
	a, _ := duplicateApp(t)
	a.prefs.KeepHistory, a.prefs.KeepScans = true, 1
	a.recordHistory(a.base)
	dir := historyDir()
	var list []index.ScanSummary
	waitModel(t, a, func() bool { list, _ = index.ListHistory(dir, a.base.Root); return len(list) == 1 })
	if list[0].Scan == "" {
		t.Fatal("the full scan was not kept")
	}
	a.openHistory()
	tt := ui.NewTester(a.view, 1280, 800)
	settle(tt, func() bool { return !a.historyView.busy })
	if len(a.historyView.list) != 1 || !tt.HasText("Scan History") {
		t.Fatalf("history dialog %q", tt.Texts())
	}

	prev := index.ScanSummary{Root: `C:\`, Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Top: []index.FolderSize{{Path: "Users", Size: 1 << 30}}}
	cur := index.ScanSummary{Root: `C:\`, Top: []index.FolderSize{{Path: "Users", Size: 8 << 30}}}
	if msg := growthAlert(prev, cur, false, 5, binaryBytes); !strings.Contains(msg, `C:\Users grew by 7.0 GiB`) {
		t.Fatalf("alert %q", msg)
	}
	if msg := growthAlert(prev, cur, false, 10, binaryBytes); msg != "" {
		t.Fatalf("alert below threshold %q", msg)
	}
}

func TestReportPrivacy(t *testing.T) {
	p := newReportPrivacy(`C:\`, true, false)
	if got := p.path(`C:\Users\Ann\Videos\trip.mp4`); got != `C:\Users\user\Videos\trip.mp4` {
		t.Fatalf("users %q", got)
	}
	p = newReportPrivacy(`C:\data`, true, true)
	if got := p.path(`C:\data\media\big.mp4`); got != `C:\data\•••\•••.mp4` {
		t.Fatalf("names %q", got)
	}
	if replaceFold(`C:\x\ANN\ann`, "ann", "user") != `C:\x\user\user` {
		t.Fatal("replaceFold")
	}
	var b strings.Builder
	s := fixture()
	if err := writeReport(context.Background(), &b, s, 0, false, true, newReportPrivacy(s.Root, true, true), binaryBytes, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, secret := range []string{"big.mp4", "report.pdf", `\media`, `\docs`, ">media<", ">docs<"} {
		if strings.Contains(out, secret) {
			t.Fatalf("report leaks %q", secret)
		}
	}
	for _, want := range []string{"<!doctype html>", "Largest files", "Videos", "Over 3 years", "•••.mp4", "names are hidden"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q", want)
		}
	}
}

func TestDuplicateKeepersAndRecheck(t *testing.T) {
	a, ids := duplicateApp(t)
	a.refreshDuplicates(true)
	waitDuplicates(t, a)
	d := &a.duplicates
	a.selectCopies(0, a.keepFirst)
	if a.files.sel.Len() != len(a.files.ids)-len(d.result.Groups) {
		t.Fatalf("selected %d of %d in %d groups", a.files.sel.Len(), len(a.files.ids), len(d.result.Groups))
	}
	for _, g := range d.result.Groups {
		if a.files.sel.Has(g.IDs[0]) {
			t.Fatal("the kept copy was selected")
		}
	}
	// Every copy of a group selected: refused.
	g := d.groups[ids["original.txt"]]
	ok := false
	a.recheckDuplicates(d.result.Groups[g-1].IDs, func() { ok = true })
	if ok || !a.errorMsg {
		t.Fatal("deleting every copy of a group was allowed")
	}
	a.clearError()
	// One copy with the same contents: passes.
	a.recheckDuplicates([]index.ID{ids["renamed.bin"]}, func() { ok = true })
	waitModel(t, a, func() bool { return ok || a.errorMsg })
	if !ok {
		t.Fatal(a.message)
	}
	// Changed since the check: refused.
	if err := os.WriteFile(a.base.Path(ids["renamed.bin"]), []byte("same contenT"), 0600); err != nil {
		t.Fatal(err)
	}
	ok = false
	a.recheckDuplicates([]index.ID{ids["renamed.bin"]}, func() { ok = true })
	waitModel(t, a, func() bool { return ok || a.errorMsg })
	if ok {
		t.Fatal("a changed copy passed the recheck")
	}
}

// TestTreemapHighlightSteadyWhileSearching follows one search after
// another: while the next search waits for typing to pause, while it runs
// and once its results arrive, the treemap keeps a highlight. Dropping it
// at any step made the map flash on every keystroke.
func TestTreemapHighlightSteadyWhileSearching(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	settle(tt, func() bool { return !a.tm.busy && a.tm.cells != nil })
	a.showQuery("*.mp4", 0)
	s := a.mapSnapshot()
	settle(tt, func() bool {
		return !a.files.busy && a.files.ran == "*.mp4" && a.treemapMatches(s, a.tm.cells) != nil
	})
	f := &a.files
	lit := func(step string) []bool {
		got := a.treemapMatches(s, a.tm.cells)
		if got == nil {
			t.Fatalf("no highlight %s", step)
		}
		return got
	}
	f.queryChanged(time.Now())
	lit("while typing pauses")
	f.busy = true
	f.gen++
	lit("while the search runs")
	f.busy = false
	f.ids, f.idsGen = f.ids[:1], f.idsGen+1
	lit("while the new results are marked")
	settle(tt, func() bool { return !a.tm.matches.busy })
	lit("after the new results are marked")
	a.search = ""
	f.invalidate()
	settle(tt, func() bool { return !f.busy })
	if a.treemapMatches(s, a.tm.cells) != nil {
		t.Fatal("highlight stays after the search is cleared")
	}
}
