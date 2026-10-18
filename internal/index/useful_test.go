package index

import (
	"context"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
)

func TestScopedSummaryMatchesAnalysis(t *testing.T) {
	s := benchSnapshot(80000)
	s.Engine = "NTFS MFT"
	meta := s.Add(0, "$Extend", 0, 0, 0, Directory)
	s.Add(meta, "metadata.LOG", 10, 50, 0, Unplaced)
	empty := s.Add(0, "empty", 0, 0, 0, Directory)
	file := s.Add(0, "Résumé.İ", 9, 2, 0, 0)
	unusual := s.Add(0, "file-with-children", 3, 5, 0, 0)
	s.Add(unusual, "child.TXT", 7, 6, 0, 0)
	s.Finalize()
	s.Remove(5, 21, meta)
	previous := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(previous)
	for _, workers := range []int{1, 2, 12} {
		runtime.GOMAXPROCS(workers)
		for _, root := range []ID{0, 1, empty, file, unusual, meta} {
			for _, allocated := range []bool{false, true} {
				want, err := s.Analyze(context.Background(), root, allocated, 0)
				if err != nil {
					t.Fatal(err)
				}
				got, err := s.SummarizeScope(context.Background(), root, allocated)
				if err != nil || !slices.Equal(got.Types, want.Types) {
					t.Fatalf("scope %d, allocated %v, workers %d: groups differ; %v", root, allocated, workers, err)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SummarizeScope(ctx, 0, false); err != context.Canceled {
		t.Fatalf("canceled summary: %v", err)
	}
	for _, root := range []ID{None, ID(len(s.Nodes))} {
		if _, err := s.SummarizeScope(context.Background(), root, false); err == nil {
			t.Fatalf("accepted invalid scope %d", root)
		}
	}
}

func TestAppendPathPreservesPathAndPrefix(t *testing.T) {
	for _, root := range []string{`C:\`, `C:\Data`, `C:\data\..`, `\\server\share`, "relative", ""} {
		s := New(root)
		parent := s.Add(0, "日本-résumé", 0, 0, 0, Directory)
		for _, name := range []string{"plain.bin", "", ".", "..", `a\b`, "Kelvin.TXT"} {
			s.Add(parent, name, 1, 1, 0, 0)
		}
		lost := s.Add(None, "lost", 0, 0, 0, Directory|Unplaced)
		s.Add(lost, "child", 1, 1, 0, 0)
		var buffer []byte
		for i := range s.Nodes {
			buffer = append(buffer[:0], "prefix:"...)
			got := s.AppendPath(buffer, ID(i))
			if string(got) != "prefix:"+s.Path(ID(i)) {
				t.Fatalf("root %q id %d: appended path differs", root, i)
			}
			buffer = got
		}
		if got := string(s.AppendPath(nil, None)); got != s.Path(None) {
			t.Fatalf("None path differs: %q", got)
		}
		if s.Root != root {
			t.Fatal("path construction modified the scan root")
		}
	}
}

func TestParallelPrefixTiesMatchCompare(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previous)
	r := rand.New(rand.NewPCG(71, 91))
	s := New(`C:\names`)
	ids := make([]ID, 196609)
	for i := range ids {
		name := fmt.Sprintf("shared-prefix-%02d-%07d.TXT", i%13, r.IntN(90000))
		if i%19 == 0 {
			name = "shared-prefix-00-identical"
		}
		if i%31 == 0 {
			name = "shared-prefix-00-İstanbul"
		}
		ids[i] = s.Add(0, name, uint64(i), uint64(i), 0, 0)
	}
	r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	for _, desc := range []bool{false, true} {
		by := Sort{Column: "Name", Desc: desc}
		want, got := slices.Clone(ids), slices.Clone(ids)
		s.Sort(want, by)
		if err := s.SortContext(context.Background(), got, by); err != nil || !slices.Equal(got, want) {
			t.Fatalf("parallel prefix ties differ (descending=%v): %v", desc, err)
		}
	}
}
