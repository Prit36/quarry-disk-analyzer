package index

import (
	"cmp"
	"context"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The word-at-a-time helpers must agree with the byte loops they replace on
// every input, including Unicode at every position and every length.

func compareFoldReference(a, b string) int {
	if a == b {
		return 0
	}
	for i := 0; i < min(len(a), len(b)); i++ {
		x, y := a[i], b[i]
		if x >= 128 || y >= 128 {
			return strings.Compare(strings.ToLower(a), strings.ToLower(b))
		}
		if x >= 'A' && x <= 'Z' {
			x += 32
		}
		if y >= 'A' && y <= 'Z' {
			y += 32
		}
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(len(a), len(b))
}

func nameKeyReference(name string, offset int) uint64 {
	for i := 0; i < len(name); i++ {
		if name[i] >= 128 {
			name = strings.ToLower(name)
			break
		}
	}
	var k uint64
	for i := offset; i < offset+8; i++ {
		var b byte
		if i < len(name) {
			b = name[i]
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
		}
		k = k<<8 | uint64(b)
	}
	return k
}

var foldAlphabet = []string{"a", "A", "z", "Z", "@", "[", "`", "{", "m", "M", "_", ".", " ", "\\", "/", "0", "9", "é", "É", "K", "ß", "İ", "~", "\x7f"}

func randomFoldString(r *rand.Rand, maxRunes int) string {
	var b strings.Builder
	for range r.IntN(maxRunes + 1) {
		b.WriteString(foldAlphabet[r.IntN(len(foldAlphabet))])
	}
	return b.String()
}

func TestFoldHelpersMatchByteLoops(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	s := New(`C:\`)
	for range 200000 {
		a := randomFoldString(r, 24)
		b := a
		switch r.IntN(4) {
		case 0:
			b = randomFoldString(r, 24)
		case 1:
			// Shared prefix, then a difference: exercises whole equal words.
			b = a[:r.IntN(len(a)+1)] + randomFoldString(r, 6)
		case 2:
			b = strings.ToUpper(a)
		}
		if got, want := compareFold(a, b), compareFoldReference(a, b); got != want {
			t.Fatalf("compareFold(%q, %q) = %d, want %d", a, b, got, want)
		}
		lower := strings.ToLower(randomFoldString(r, 4))
		if r.IntN(3) == 0 && len(a) > 0 {
			i := r.IntN(len(a))
			lower = strings.ToLower(a[i:min(len(a), i+r.IntN(10))])
		}
		if lower != "" {
			if got, want := containsFold(a, lower), strings.Contains(strings.ToLower(a), lower); got != want {
				t.Fatalf("containsFold(%q, %q) = %v, want %v", a, lower, got, want)
			}
		}
		if got, want := hasSeparator(a), strings.ContainsAny(a, `\/`); got != want {
			t.Fatalf("hasSeparator(%q) = %v", a, got)
		}
		ascii := true
		for i := 0; i < len(a); i++ {
			ascii = ascii && a[i] < 128
		}
		if isASCII(a) != ascii {
			t.Fatalf("isASCII(%q) = %v", a, !ascii)
		}
		if folded, ok := AppendFoldedASCII([]byte("x"), a); ok != ascii || ok && string(folded) != "x"+strings.ToLower(a) {
			t.Fatalf("AppendFoldedASCII(%q) = %q, %v", a, folded, ok)
		}
		if len(a) <= 65535 {
			id := s.Add(0, a, 0, 0, 0, 0)
			for _, offset := range []int{0, 8, 16} {
				if got, want := s.NameKey(id, offset), nameKeyReference(a, offset); got != want {
					t.Fatalf("NameKey(%q, %d) = %x, want %x", a, offset, got, want)
				}
			}
		}
	}
}

func TestSortMatchesCompare(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	s := New(`C:\`)
	ids := make([]ID, 0, 3000)
	for range cap(ids) {
		ids = append(ids, s.Add(0, randomFoldString(r, 6), uint64(r.IntN(50)), uint64(r.IntN(50)), int64(r.IntN(5)), 0))
	}
	for _, column := range []string{"Name", "Size", "Allocated", "Modified", "% of Parent", "Files", ""} {
		for _, desc := range []bool{false, true} {
			by := Sort{Column: column, Desc: desc}
			got := append([]ID(nil), ids...)
			s.Sort(got, by)
			for i := 1; i < len(got); i++ {
				if s.Compare(got[i-1], got[i], by) >= 0 {
					t.Fatalf("%+v: %d before %d", by, got[i-1], got[i])
				}
			}
		}
	}
}

// Plain path terms are matched incrementally per folder; the result must be
// the same as searching every entry's whole path, including occurrences that
// cross folder boundaries and Unicode anywhere in the path.
func TestIncrementalPathTermsMatchWholePaths(t *testing.T) {
	previous := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previous)
	r := rand.New(rand.NewPCG(5, 6))
	parts := []string{"ab", "Ab", "b", "abab", "a", "x", "é", "K", "日本", "", "..", `s\l`, "AB.c"}
	for _, root := range []string{`C:\`, `C:\ab`, `\\server\ab`} {
		s := New(root)
		dirs := []ID{0}
		for i := range 40000 {
			parent := dirs[r.IntN(len(dirs))]
			name := parts[r.IntN(len(parts))] + parts[r.IntN(len(parts))]
			flags := uint16(0)
			if i%5 == 0 {
				flags = Directory
			}
			if i%301 == 0 {
				flags |= Unplaced
			}
			id := s.Add(parent, name, uint64(i), uint64(i), 0, flags)
			if flags&Directory != 0 {
				dirs = append(dirs, id)
			}
		}
		s.Finalize()
		for _, workers := range []int{1, 8} {
			runtime.GOMAXPROCS(workers)
			for _, text := range []string{`path:b\a`, `path:\abab\`, `path:ab\ab\ab`, `-\ab\ path:a\`, `path:é\k`, `path:\日本`, `\b \a path:ab\x`, `path:C:\ab\a`, `path:\\`, `path:ab\`} {
				f, err := ParseFilter(text)
				if err != nil {
					t.Fatal(err)
				}
				for _, scope := range []ID{0, dirs[1], dirs[len(dirs)/2]} {
					var want []ID
					err = s.Visit(context.Background(), scope, func(id ID) error {
						if id != scope && f.MatchNode(s.NameView(id), s.Nodes[id], func() string { return s.Path(id) }, false) {
							want = append(want, id)
						}
						return nil
					})
					got, searchErr := s.Search(context.Background(), scope, &f)
					if err != nil || searchErr != nil || !slices.Equal(got, want) {
						t.Fatalf("root=%q scope=%d workers=%d query=%q: %d results, want %d (%v / %v)", root, scope, workers, text, len(got), len(want), err, searchErr)
					}
				}
			}
		}
	}
}

func TestPathCursorMatchesPath(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	parts := []string{"a", "B", "é", "", "..", ".", `x\y`, "z/w", "日本", "long-name"}
	for _, root := range []string{`C:\`, `C:\scan`, `C:\scan\`, `\\server\share`, "relative", `C:\a\..\b`} {
		s := New(root)
		ids := []ID{0}
		for i := range 3000 {
			flags := uint16(Directory)
			if i%53 == 0 {
				flags |= Unplaced
			}
			ids = append(ids, s.Add(ids[r.IntN(len(ids))], parts[r.IntN(len(parts))]+parts[r.IntN(len(parts))], 0, 0, 0, flags))
		}
		s.Finalize()
		c := s.NewPathCursor()
		shuffled := slices.Clone(ids)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for _, order := range [][]ID{ids, shuffled} {
			for _, id := range order {
				if got, want := c.Path(id), s.Path(id); got != want {
					t.Fatalf("root %q: Path(%d) = %q, want %q", root, id, got, want)
				}
			}
		}
	}
}

func TestParallelNumericSortMatchesCompare(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	s := New(`C:\`)
	ids := make([]ID, 0, 300000)
	for i := range cap(ids) {
		size := uint64(r.IntN(1000))
		if i%3 == 0 {
			size = r.Uint64() // full-width keys: the unpacked path
		}
		ids = append(ids, s.Add(0, "f", size, uint64(r.IntN(50)), int64(r.IntN(1<<30)), 0))
	}
	for _, shuffle := range []bool{false, true} {
		if shuffle {
			r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		}
		for _, by := range []Sort{{Column: "Size"}, {Column: "Size", Desc: true}, {Column: "Allocated"}, {Column: "Modified", Desc: true}} {
			got := slices.Clone(ids)
			if err := s.SortContext(context.Background(), got, by); err != nil {
				t.Fatal(err)
			}
			for i := 1; i < len(got); i++ {
				if s.Compare(got[i-1], got[i], by) >= 0 {
					t.Fatalf("%+v shuffle=%v: %d before %d", by, shuffle, got[i-1], got[i])
				}
			}
		}
	}
}

func TestSortContextNamesMatchCompare(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	s := New(`C:\`)
	prefixes := []string{"", "amd64_microsoft-windows-", "AMD64_Microsoft-Windows-", "api-ms-win-core-", "file", "é-", "abcdefgh"}
	ids := make([]ID, 0, 30000)
	for range cap(ids) {
		ids = append(ids, s.Add(0, prefixes[r.IntN(len(prefixes))]+randomFoldString(r, 5), 0, 0, 0, 0))
	}
	for _, desc := range []bool{false, true} {
		by := Sort{Column: "Name", Desc: desc}
		got := slices.Clone(ids)
		if err := s.SortContext(context.Background(), got, by); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(got); i++ {
			if s.Compare(got[i-1], got[i], by) >= 0 {
				t.Fatalf("desc=%v: %q (%d) before %q (%d)", desc, s.NameView(got[i-1]), got[i-1], s.NameView(got[i]), got[i])
			}
		}
	}
}
