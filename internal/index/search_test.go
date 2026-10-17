package index

import (
	"context"
	"slices"
	"testing"
	"time"
)

func searchFixture() *Snapshot {
	s := New(`C:\data`)
	day := func(y, m, d int) int64 { return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.Local).Unix() }
	proj := s.Add(0, "project", 0, 0, 0, Directory)
	nm := s.Add(proj, "node_modules", 0, 0, 0, Directory)
	s.Add(nm, "index.js", 2000, 4096, day(2024, 5, 1), 0)
	s.Add(proj, "build.log", 600<<20, 600<<20, day(2024, 12, 31), 0)
	s.Add(proj, "Backup-2023.zip", 3<<30, 3<<30, day(2023, 1, 1), 0)
	media := s.Add(0, "media", 0, 0, 0, Directory)
	s.Add(media, "holiday.MP4", 900<<20, 900<<20, day(2025, 3, 14), 0)
	s.Add(media, "clip.mp4", 10<<20, 10<<20, day(2025, 6, 1), 0)
	s.Add(media, "README", 10, 4096, 0, 0)
	s.Add(media, "IMG_0042.jpg", 5<<20, 5<<20, day(2025, 1, 2), 0)
	s.Finalize()
	return s
}

func names(t *testing.T, s *Snapshot, q string) []string {
	t.Helper()
	f, err := ParseFilter(q)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	ids, err := s.Search(context.Background(), 0, &f)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, id := range ids {
		out = append(out, s.NameView(id))
	}
	slices.Sort(out)
	return out
}

func TestSearchSyntax(t *testing.T) {
	s := searchFixture()
	cases := map[string][]string{
		"*.mp4":                       {"clip.mp4", "holiday.MP4"},
		"MP4":                         {"clip.mp4", "holiday.MP4"},
		"backup*":                     {"Backup-2023.zip"},
		"*cache*":                     nil,
		"img_00??.jpg":                {"IMG_0042.jpg"},
		"node_modules":                {"node_modules"},
		"ext:log size:>500MB":         {"build.log"},
		"ext:log size:>700MB":         nil,
		"ext:mp4;zip size:1GB..4GB":   {"Backup-2023.zip"},
		"type:file size:<1KB":         {"README"},
		"ext:none type:file":          {"README"},
		"modified:<2024-01-01":        {"Backup-2023.zip"},
		"modified:2025-03-14":         {"holiday.MP4"},
		"after:2025-01-01 type:file":  {"IMG_0042.jpg", "clip.mp4", "holiday.MP4"},
		"before:2024-12-31 ext:log":   {"build.log"},
		"path:node_modules type:file": {"index.js"},
		`project\node`:                {"index.js", "node_modules"},
		"type:folder":                 {"media", "node_modules", "project"},
		"-mp4 type:file media":        nil,
		`regex:^img_\d+\.jpe?g$`:      {"IMG_0042.jpg"},
		`"build.log"`:                 {"build.log"},
		"min:1GiB":                    {"Backup-2023.zip", "project"},
	}
	for q, want := range cases {
		if got := names(t, s, q); !slices.Equal(got, want) {
			t.Errorf("%q = %v, want %v", q, got, want)
		}
	}
	// Negation excludes matches.
	if got := names(t, s, "type:file -mp4 -log -js -zip"); !slices.Equal(got, []string{"IMG_0042.jpg", "README"}) {
		t.Errorf("negation: %v", got)
	}
	for _, bad := range []string{"size:>potato", "modified:yesterday", "min:2GB max:1GB", `"open`, "regex:(", "type:socket", "ext:"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestWildcardMatching(t *testing.T) {
	for _, tc := range []struct {
		p, s string
		ok   bool
	}{{"*.mp4", "a.MP4", true}, {"*.mp4", "a.mp4.part", false}, {"a*b*c", "aXXbYYc", true}, {"a*b*c", "aXXbYY", false}, {"?", "é", true}, {"*", "", true}, {"**x", "abx", true}, {"résumé*", "RÉSUMÉ.pdf", true}} {
		if wildcardFold(tc.p, tc.s) != tc.ok {
			t.Errorf("%q vs %q", tc.p, tc.s)
		}
	}
}

func TestSearchScope(t *testing.T) {
	s := searchFixture()
	f, _ := ParseFilter("type:file")
	ids, err := s.Search(context.Background(), 6, &f) // media
	if err != nil || len(ids) != 4 {
		t.Fatalf("scoped search: %v %v", ids, err)
	}
}

func TestRemoveRenameAndFilter(t *testing.T) {
	base := searchFixture()
	total := base.Nodes[0]
	c := base.Clone()
	c.Remove(4) // build.log
	if c.Nodes[0].Size != total.Size-600<<20 || c.Nodes[0].Files != total.Files-1 || c.Nodes[1].Size != base.Nodes[1].Size-600<<20 {
		t.Fatalf("remove did not update totals: %+v", c.Nodes[0])
	}
	if base.Nodes[0].Size != total.Size || !base.Reachable(4) {
		t.Fatal("clone mutated the original snapshot")
	}
	if c.Reachable(4) || slices.Contains(c.Children(1), 4) {
		t.Fatal("removed entry still reachable")
	}
	c.Remove(2) // node_modules folder
	if c.Nodes[0].Dirs != total.Dirs-1 || c.Nodes[0].Files != total.Files-2 {
		t.Fatalf("folder removal totals: %+v", c.Nodes[0])
	}
	if err := c.Rename(10, "renamed.jpg"); err != nil || c.NameView(10) != "renamed.jpg" || base.NameView(10) != "IMG_0042.jpg" {
		t.Fatal("rename")
	}
	// Filtering: only mp4 files, isolate.
	f, _ := ParseFilter("ext:mp4")
	filtered, stats, err := base.Filtered(context.Background(), func(id ID) bool {
		return f.MatchNode(base.NameView(id), base.Nodes[id], nil, false)
	}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Nodes[0].Files != 2 || filtered.Nodes[0].Size != 910<<20 || filtered.Reachable(1) || stats.HiddenFiles != 5 {
		t.Fatalf("filtered totals: %+v stats %+v", filtered.Nodes[0], stats)
	}
	if base.Nodes[0].Files != total.Files {
		t.Fatal("filter mutated base")
	}
	pruned := base.Clone()
	n, err := pruned.Prune(context.Background(), func(id ID) bool { return pruned.NameView(id) == "node_modules" })
	if err != nil || n != 1 || pruned.Nodes[0].Files != total.Files-1 {
		t.Fatalf("prune: %d %v %+v", n, err, pruned.Nodes[0])
	}
}

func TestMetadataDetection(t *testing.T) {
	s := New(`C:\`)
	s.Engine = "NTFS · direct MFT"
	ext := s.Add(0, "$Extend", 0, 0, 0, Directory)
	rm := s.Add(ext, "$RmMetadata", 0, 0, 0, Directory)
	bin := s.Add(0, "$Recycle.Bin", 0, 0, 0, Directory)
	bad := s.Add(0, "$BadClus", 1<<40, 0, 0, 0)
	s.Finalize()
	if !s.IsMetadata(rm) || !s.IsMetadata(bad) || s.IsMetadata(bin) || s.IsMetadata(0) {
		t.Fatal("metadata classification")
	}
}
