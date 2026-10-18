package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// Run this identical harness against both source revisions. Each digest
// includes the complete output, including result order, not just counts.
func TestOriginalOutputParity(t *testing.T) {
	out := os.Getenv("QUARRY_PARITY_OUT")
	if out == "" {
		t.Skip("QUARRY_PARITY_OUT not set")
	}
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	report := map[string]string{}
	record := func(key string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		report[key] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	workersBefore := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(workersBefore)
	for _, flat := range []bool{false, true} {
		r := rand.New(rand.NewPCG(11, 29))
		s := index.New(`C:\parity data`)
		dirs := []index.ID{0}
		names := []string{"Report", "report", "İstanbul", "résumé", "日本", "Kelvin", "cache", "very-long-shared-prefix-backup"}
		exts := []string{".dll", ".DLL", ".log", ".TXT", ".é", "", ".tar.gz"}
		for i := range 70000 {
			parent := dirs[r.IntN(len(dirs))]
			name := fmt.Sprintf("%s-%02d", names[r.IntN(len(names))], r.IntN(50))
			if !flat && i%11 == 0 {
				dirs = append(dirs, s.Add(parent, name, 0, 0, 0, index.Directory))
			} else {
				v := uint64(r.IntN(1 << 30))
				s.Add(parent, name+exts[r.IntN(len(exts))], v, v/2+4096, int64(1600000000+r.IntN(1000000)), 0)
			}
		}
		s.Finalize()
		for id := index.ID(7777); int(id) < len(s.Nodes); id += 7777 {
			s.Remove(id)
		}
		fixtureKey := fmt.Sprintf("flat=%v", flat)
		queries := []string{"", "report", "*.dll", "ext:dll", "ext:none", "-cache", "type:folder", "type:file", "size:>500MB", "modified:>=2020-09-15", "path:report", "path:cache path:report", "path:parity", `path:C:\parity`, `regex:^Report.*`, "résumé", "日本", "missing", "report -cache ext:txt,dll"}
		for _, workers := range []int{1, 2, 8, 12} {
			runtime.GOMAXPROCS(workers)
			for _, root := range []index.ID{0, dirs[len(dirs)-1]} {
				for _, query := range queries {
					f, err := index.ParseFilter(query)
					if err != nil {
						t.Fatal(err)
					}
					ids, err := s.Search(ctx, root, &f)
					if err != nil {
						t.Fatal(err)
					}
					record(fmt.Sprintf("%s/search/%d/%d/%s", fixtureKey, workers, root, query), ids)
				}
			}
		}
		runtime.GOMAXPROCS(workersBefore)
		f, _ := index.ParseFilter("")
		base, err := s.Search(ctx, 0, &f)
		if err != nil {
			t.Fatal(err)
		}
		r.Shuffle(len(base), func(i, j int) { base[i], base[j] = base[j], base[i] })
		for _, col := range []string{"Name", "Size", "Allocated", "Modified", "Files", "Folders", "% of Parent", "% of Scan", "Folder", "Extension"} {
			for _, desc := range []bool{false, true} {
				for _, allocated := range []bool{false, true} {
					ids := slices.Clone(base)
					if err := sortEntries(ctx, s, ids, ui.SortOrder{Column: col, Descending: desc}, allocated); err != nil {
						t.Fatal(err)
					}
					record(fmt.Sprintf("%s/sort/%s/%v/%v", fixtureKey, col, desc, allocated), ids)
				}
			}
		}
		for _, include := range []string{"", "path:parity", "path:report", "path:cache path:report", "path:日本 ext:log", "ext:dll", "type:folder", "size:>500MB"} {
			for _, exclude := range []string{"", "cache-01", "*.log", `C:\parity data\Report-01`} {
				for _, isolate := range []bool{false, true} {
					view, stats, err := (analysisFilter{Include: include, Exclude: exclude, Isolate: isolate}).apply(ctx, s)
					if err != nil {
						t.Fatal(err)
					}
					record(fmt.Sprintf("%s/filter/%s/%s/%v", fixtureKey, include, exclude, isolate), []any{view.Nodes, view.Names, stats})
				}
			}
		}
		a := newApp()
		a.setBase(s, "", nil, false)
		wait := func() {
			deadline := time.Now().Add(10 * time.Second)
			for a.files.busy {
				if time.Now().After(deadline) {
					t.Fatal("file model timed out")
				}
				a.drain()
				runtime.Gosched()
			}
		}
		for _, query := range []string{"", "ext:dll", "type:folder", "size:invalid", "", "path:report"} {
			a.search = query
			for _, col := range []string{"Size", "Name", "Folder", "Extension", "Size"} {
				a.files.sort = ui.SortOrder{Column: col, Descending: col == "Size"}
				a.files.run()
				wait()
				record(fmt.Sprintf("%s/model/%s/%s", fixtureKey, query, col), []any{a.files.ids, a.files.total, a.files.err, a.files.selectedIDs(), a.files.cursorID()})
				if len(a.files.ids) > 0 {
					a.files.selectOnly(a.files.ids[len(a.files.ids)/2])
				}
			}
		}
		a.search = ""
		a.files.run()
		wait()
		for _, otherSelected := range []bool{false, true} {
			id := a.files.ids[0]
			a.files.selectOnly(id)
			a.files.sel.Remove(id)
			if otherSelected {
				a.files.sel.Add(a.files.ids[len(a.files.ids)-1])
			}
			a.files.selCache.invalidate()
			a.files.sort = ui.SortOrder{Column: "Name"}
			a.files.run()
			wait()
			record(fmt.Sprintf("%s/model/deselected/%v", fixtureKey, otherSelected), []any{a.files.ids, a.files.total, a.files.selectedIDs(), a.files.cursorID()})
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d complete-output digests", len(report))
}
