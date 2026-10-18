package index

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The keyed radix sort must order exactly as the comparison sort does.
func TestKeyedSortMatchesCompare(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	s := New(`C:\sort`)
	names := []string{"a", "A", "ab", "Ab", "abc", "ABCDEFGHI", "abcdefgh", "abcdefghi", "Ärger", "ärger", "éclair", "Éclair", "z", "Z", "_x", "x_", "0", "9", "İstanbul", "istanbul", "api-ms-win-core-file-l1-1-0.dll", "api-ms-win-core-file-l1-2-0.dll", "日本", "日本語"}
	dirs := []ID{0}
	for i := range 20000 {
		parent := dirs[r.IntN(len(dirs))]
		name := names[r.IntN(len(names))]
		if r.IntN(3) == 0 {
			name = fmt.Sprintf("%s%d", name, r.IntN(50))
		}
		if r.IntN(10) == 0 {
			dirs = append(dirs, s.Add(parent, name, 0, 0, int64(r.IntN(1000))-500, Directory))
			continue
		}
		size := uint64(r.IntN(1000))
		s.Add(parent, name, size, size+uint64(r.IntN(4))*4096, int64(r.IntN(1000))-500, 0)
		_ = i
	}
	s.Finalize()
	all := make([]ID, 0, len(s.Nodes))
	for i := range s.Nodes {
		all = append(all, ID(i))
	}
	for _, column := range []string{"", "Name", "Size", "Allocated", "Modified", "Files", "Folders", "% of Parent", "Unknown"} {
		for _, desc := range []bool{false, true} {
			for _, allocated := range []bool{false, true} {
				by := Sort{Column: column, Desc: desc, Allocated: allocated}
				want := slices.Clone(all)
				r.Shuffle(len(want), func(i, j int) { want[i], want[j] = want[j], want[i] })
				got := slices.Clone(want)
				s.Sort(want, by)
				if err := s.SortContext(context.Background(), got, by); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, want) {
					for i := range got {
						if got[i] != want[i] {
							t.Fatalf("%+v: position %d: got %q, want %q", by, i, s.NameView(got[i]), s.NameView(want[i]))
						}
					}
				}
			}
		}
	}
}

// The one-allocation Path must agree with the filepath.Join version.
func TestPathMatchesJoin(t *testing.T) {
	for _, root := range []string{`C:\`, `C:\data`, `C:\data\sub dir`, `\server\share`, `C:\x\`} {
		s := New(root)
		a := s.Add(0, "Users", 0, 0, 0, Directory)
		b := s.Add(a, "résumé", 0, 0, 0, Directory)
		s.Add(b, "file.txt", 1, 1, 0, 0)
		s.Add(a, "trailing. ", 1, 1, 0, 0)
		s.Add(0, "top.bin", 1, 1, 0, 0)
		u := s.Add(0, "lost", 1, 1, 0, Unplaced)
		s.Add(u, "child", 1, 1, 0, 0)
		s.Finalize()
		for i := range s.Nodes {
			id := ID(i)
			want := s.joinPath(id)
			if id == 0 {
				want = s.Root
			}
			if got := s.Path(id); got != want {
				t.Fatalf("root %q, %q: got %q want %q", root, s.NameView(id), got, want)
			}
		}
	}
}

// Wildcard shortcuts must agree with the general matcher.
func TestWildcardShortcutsMatchGeneral(t *testing.T) {
	names := []string{"", "a", "file.DLL", "x.dll", ".dll", "dll", "Backup-1.zip", "backup", "BACKUP", "my cache dir", "Cache", "kelvinK.dll", "Key", "résumé.pdf", "RÉSUMÉ.PDF", "naïve cache", "x.dll.bak", "日本.dll", "ab", "b"}
	patterns := []string{"*.dll", "backup*", "*cache*", "*", "**", "*.", "*a*", "k*", "*k", "*ey", "a*b", "r*", "*.pdf", "*dll*"}
	for _, p := range patterns {
		m := newMatcher(p, false)
		for _, n := range names {
			if got, want := m.match(n), wildcardFold(m.text, n); got != want {
				t.Fatalf("%q on %q: shortcut %v, general %v", p, n, got, want)
			}
		}
	}
}

// Exported paths come from the incremental walker; they must equal Path.
func TestExportPathsMatchPath(t *testing.T) {
	for _, root := range []string{`C:\`, `C:\data`, `\server\share`} {
		s := New(root)
		a := s.Add(0, "Users", 0, 0, 0, Directory)
		b := s.Add(a, "résumé", 0, 0, 0, Directory)
		s.Add(b, "file, with comma.txt", 1, 1, 5, 0)
		c := s.Add(b, "deep", 0, 0, 0, Directory)
		s.Add(c, "x", 1, 1, 0, 0)
		s.Add(a, "trailing. ", 1, 1, 0, 0)
		u := s.Add(0, "lost", 0, 0, 0, Directory|Unplaced)
		s.Add(u, "child", 1, 1, 0, 0)
		s.Finalize()
		for _, scope := range []ID{0, a, b, u} {
			var want []string
			s.Visit(context.Background(), scope, func(id ID) error { want = append(want, s.Path(id)); return nil })
			var out strings.Builder
			if err := s.ExportScope(context.Background(), &out, scope); err != nil {
				t.Fatal(err)
			}
			rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(want)+1 {
				t.Fatalf("%d rows for %d entries", len(rows), len(want))
			}
			for i, w := range want {
				if rows[i+1][0] != w {
					t.Fatalf("root %q scope %d row %d: %q, want %q", root, scope, i, rows[i+1][0], w)
				}
			}
		}
	}
}

// A snapshot larger than one compression block is written as several gzip
// members; loading must read them all back.
func TestLargeSnapshotRoundTrip(t *testing.T) {
	s := New(`C:\big`)
	dir := s.Add(0, "dir", 0, 0, 0, Directory)
	for i := range 120000 {
		s.Add(dir, fmt.Sprintf("file-%06d.bin", i), uint64(i), uint64(i)*2, int64(i), 0)
	}
	s.Finalize()
	path := t.TempDir() + `\big.quarry`
	if err := s.Save(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.Nodes, s.Nodes) || string(loaded.Names) != string(s.Names) {
		t.Fatal("round trip changed the index")
	}
}
