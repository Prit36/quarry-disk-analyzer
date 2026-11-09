package index

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var exportStrings = []string{
	"", " ", "\t", "a", `\.`, `\..`, `C:\Users\a b\file.txt`, "comma,name", `quote"name`, `""`,
	"line\nbreak", "cr\rhere", " leading", "trailing ", "\u00a0nbsp", "\u0085next", "\u3000ideo",
	"<html>&amp;", "tab\there", "\x00nul", "\x1fus", "\x7fdel", "caf\u00e9", "\u65e5\u672c\u8a9e.txt",
	"emoji \U0001F600", "sep\u2028x", "para\u2029y", "bad\xffutf8", "\xc3", "\xe2\x80", "\\\\server\\share",
	"a\"b,c\nd", "\u200bzero", "\ufeffbom", "\ufffdrepl",
}

func TestCSVFieldMatchesEncodingCSV(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"a", " ", ",", `"`, "\n", "\r", "\t", `\`, ".", "\u00a0", "\u3000", "\xff", "\u00e9"}
	fields := append([]string(nil), exportStrings...)
	for range 5000 {
		var b strings.Builder
		for range random.IntN(6) {
			b.WriteString(alphabet[random.IntN(len(alphabet))])
		}
		fields = append(fields, b.String())
	}
	for _, field := range fields {
		var want bytes.Buffer
		w := csv.NewWriter(&want)
		w.Write([]string{field, "x"})
		w.Flush()
		got := append(appendCSVField(nil, field), ",x\n"...)
		if string(got) != want.String() {
			t.Fatalf("field %q: got %q, want %q", field, got, want.String())
		}
	}
}

func TestJSONStringMatchesEncoder(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))
	alphabet := []string{"a", `"`, `\`, "<", ">", "&", "\n", "\x01", "\u2028", "\u2029", "\xff", "\u00e9", "\U0001F600", "/", "\x7f"}
	values := append([]string(nil), exportStrings...)
	for range 5000 {
		var b strings.Builder
		for range random.IntN(6) {
			b.WriteString(alphabet[random.IntN(len(alphabet))])
		}
		values = append(values, b.String())
	}
	for _, value := range values {
		var want bytes.Buffer
		json.NewEncoder(&want).Encode(value)
		got := append(appendJSONString(nil, value), '\n')
		if string(got) != want.String() {
			t.Fatalf("string %q: got %q, want %q", value, got, want.String())
		}
	}
}

func TestRFC3339MatchesTime(t *testing.T) {
	check := func(v uint64) {
		want := time.Unix(int64(v), 0).UTC().Format(time.RFC3339)
		if got := string(appendRFC3339UTC(nil, v)); got != want {
			t.Fatalf("%d: got %s, want %s", v, got, want)
		}
	}
	for _, v := range []uint64{1, 59, 60, 86399, 86400, 951782400, 951868800, 4107542400, 4107628800, math.MaxUint32 - 1, math.MaxUint32} {
		check(v)
	}
	// Every day boundary across the representable range, and random seconds.
	for day := uint64(0); day*86400 <= math.MaxUint32; day++ {
		check(day*86400 + 1)
		check(min(day*86400+86399, math.MaxUint32))
	}
	random := rand.New(rand.NewPCG(5, 6))
	for range 200000 {
		check(uint64(random.Uint32N(math.MaxUint32)) + 1)
	}
}

// exportFixture is a small tree with names that exercise quoting and escaping.
func exportFixture() *Snapshot {
	s := New(`C:\export`)
	random := rand.New(rand.NewPCG(7, 8))
	folders := []ID{0}
	for i := range 3000 {
		parent := folders[random.IntN(len(folders))]
		name := exportStrings[i%len(exportStrings)] + strconv.Itoa(i)
		if i%5 == 0 {
			folders = append(folders, s.Add(parent, name, 0, 0, int64(random.Uint32()), Directory))
			continue
		}
		modified := int64(random.Uint32())
		if i%7 == 0 {
			modified = 0
		}
		flags := uint16(0)
		if i%11 == 0 {
			flags |= Shared
		}
		if i%13 == 0 {
			flags |= Incomplete
		}
		s.Add(parent, name, random.Uint64(), random.Uint64N(1<<40), modified, flags)
	}
	s.Finalize()
	return s
}

// The pre-formatting implementation, kept here as the reference.
func referenceCSV(s *Snapshot, ids []ID, path func(ID) string) string {
	var out bytes.Buffer
	c := csv.NewWriter(&out)
	c.Write([]string{"Path", "Type", "Size", "Allocated", "Files", "Folders", "Modified", "Shared", "Incomplete"})
	for _, id := range ids {
		n := s.Nodes[id]
		kind := "file"
		if n.IsDir() {
			kind = "folder"
		}
		modified := ""
		if n.Modified != 0 {
			modified = time.Unix(n.ModTime(), 0).UTC().Format(time.RFC3339)
		}
		c.Write([]string{path(id), kind, strconv.FormatUint(n.Size, 10), strconv.FormatUint(n.Allocated, 10),
			strconv.FormatUint(uint64(n.Files), 10), strconv.FormatUint(uint64(n.Dirs), 10), modified,
			strconv.FormatBool(n.Flags&Shared != 0), strconv.FormatBool(n.Flags&Incomplete != 0)})
	}
	c.Flush()
	return out.String()
}

func referenceJSON(s *Snapshot, ids []ID) string {
	var out bytes.Buffer
	metadata, _ := json.Marshal(struct {
		Schema             int
		ScanRoot, Scope    string
		Partial            bool
		Logical, Allocated uint64
	}{1, s.Root, s.Path(ids[0]), s.Cancelled, s.Nodes[ids[0]].Size, s.Nodes[ids[0]].Allocated})
	out.WriteString("{\"scan\":" + string(metadata) + ",\"nodes\":[")
	encoder := json.NewEncoder(&out)
	for i, id := range ids {
		if i > 0 {
			out.WriteByte(',')
		}
		n := s.Nodes[id]
		encoder.Encode(struct {
			Path, Name      string
			Size, Allocated uint64
			Files, Folders  uint32
			Modified        int64
			Flags           uint16
		}{s.Path(id), s.Name(id), n.Size, n.Allocated, n.Files, n.Dirs, n.ModTime(), n.Flags})
	}
	out.WriteString("]}\n")
	return out.String()
}

func TestExportsMatchReference(t *testing.T) {
	s := exportFixture()
	ctx := context.Background()
	var visited []ID
	s.Visit(ctx, 0, func(id ID) error { visited = append(visited, id); return nil })

	var got bytes.Buffer
	if err := s.Export(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if want := referenceCSV(s, visited, s.Path); got.String() != want {
		t.Fatal("CSV export differs from encoding/csv")
	}

	var rows []Row
	for i := len(s.Nodes) - 1; i >= 0; i -= 2 {
		rows = append(rows, Row{ID: ID(i)})
	}
	ids := make([]ID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	got.Reset()
	if err := s.ExportRows(ctx, &got, rows); err != nil {
		t.Fatal(err)
	}
	if want := referenceCSV(s, ids, s.Path); got.String() != want {
		t.Fatal("row CSV export differs from encoding/csv")
	}
	if err := s.ExportRows(ctx, &got, []Row{{ID: None}}); err == nil {
		t.Fatal("invalid row accepted")
	}

	got.Reset()
	if err := s.ExportJSON(ctx, &got, 0); err != nil {
		t.Fatal(err)
	}
	if want := referenceJSON(s, visited); got.String() != want {
		t.Fatal("JSON export differs from encoding/json")
	}
	if !json.Valid(got.Bytes()) {
		t.Fatal("JSON export is not valid JSON")
	}

	scope := s.Nodes[0].FirstChild
	for scope != None && !s.Nodes[scope].IsDir() {
		scope = s.Nodes[scope].NextSibling
	}
	var inScope []ID
	s.Visit(ctx, scope, func(id ID) error { inScope = append(inScope, id); return nil })
	got.Reset()
	if err := s.ExportJSON(ctx, &got, scope); err != nil {
		t.Fatal(err)
	}
	if want := referenceJSON(s, inScope); got.String() != want {
		t.Fatal("scoped JSON export differs from encoding/json")
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, os.ErrClosed
	}
	w.after--
	return len(p), nil
}

func TestExportReportsWriteErrors(t *testing.T) {
	s := exportFixture()
	ctx := context.Background()
	if err := s.Export(ctx, &failingWriter{}); err == nil {
		t.Fatal("CSV write error not reported")
	}
	if err := s.ExportJSON(ctx, &failingWriter{}, 0); err == nil {
		t.Fatal("JSON write error not reported")
	}
}

func TestCompactMatchesSavedOrder(t *testing.T) {
	s := exportFixture()
	c := s.Clone()
	// Remove a spread of files and folders, then compact.
	for i := len(c.Nodes) - 1; i > 0; i -= 9 {
		if c.Reachable(ID(i)) {
			c.Remove(ID(i))
		}
	}
	compact := c.Compact()
	ctx := context.Background()
	var before, after bytes.Buffer
	if err := c.Export(ctx, &before); err != nil {
		t.Fatal(err)
	}
	if err := compact.Export(ctx, &after); err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("compacted snapshot lists different entries")
	}
	if compact.hasRemoved() || compact.validate(ctx) != nil {
		t.Fatal("compacted snapshot is not a valid saved tree")
	}
	path := filepath.Join(t.TempDir(), "c.quarry")
	if err := c.Save(ctx, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSnapshot(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded bytes.Buffer
	loaded.Export(ctx, &reloaded)
	if reloaded.String() != before.String() {
		t.Fatal("saved and loaded snapshot differs")
	}
}

func TestLoadSnapshotFileEdges(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.quarry")
	os.WriteFile(empty, nil, 0o644)
	if _, err := LoadSnapshot(context.Background(), empty); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, err := LoadSnapshot(context.Background(), filepath.Join(dir, "missing.quarry")); err == nil {
		t.Fatal("missing file accepted")
	}
	garbage := filepath.Join(dir, "garbage.quarry")
	os.WriteFile(garbage, []byte("not a snapshot at all"), 0o644)
	if _, err := LoadSnapshot(context.Background(), garbage); err == nil {
		t.Fatal("garbage accepted")
	}
}
