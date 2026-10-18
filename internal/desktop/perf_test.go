package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// TestPerfReport measures the interface on a real saved scan:
//
//	QUARRY_PERF_SNAPSHOT=C.quarry QUARRY_PERF_OUT=ui.json go test ./internal/desktop -run TestPerfReport -v
//
// Frame times are headless (layout, text shaping and painting into the
// display list) and exclude GPU presentation.
func TestPerfReport(t *testing.T) {
	path := os.Getenv("QUARRY_PERF_SNAPSHOT")
	synthetic := os.Getenv("QUARRY_PERF_SYNTHETIC") == "1"
	if path == "" && !synthetic {
		t.Skip("set QUARRY_PERF_SNAPSHOT or QUARRY_PERF_SYNTHETIC=1")
	}
	t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
	report := map[string]any{}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	heap := func() float64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc) / (1 << 20)
	}
	report["heap_before_mb"] = heap()

	start := time.Now()
	var s *index.Snapshot
	if synthetic && path == "" {
		s = syntheticUISnapshot()
		report["source"] = "synthetic 500k nodes (no disk I/O or GPU presentation)"
		report["snapshot_prepare_ms"] = ms(time.Since(start))
	} else {
		var err error
		s, err = index.LoadSnapshot(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		report["load_snapshot_ms"] = ms(time.Since(start))
	}
	report["nodes"] = len(s.Nodes)

	a := newApp()
	testedApp = a
	a.prefs.Theme = "Dark"
	a.prefs.ShowTreemap = true
	tt := ui.NewTester(a.view, 1600, 1000)
	tt.Frame()
	// The scan finishing: the same work the scan goroutine and setBase do.
	start = time.Now()
	a.installScan(s, "")
	until(tt, func() bool { return !a.filterBusy && a.snap != nil })
	report["install_scan_ms"] = ms(time.Since(start))
	start = time.Now()
	tt.Frame()
	until(tt, func() bool { return !a.tm.busy })
	report["treemap_ready_ms"] = ms(time.Since(start))
	report["treemap_cells"] = len(a.tm.cells)
	s = nil
	report["heap_after_install_mb"] = heap()

	// frames renders n frames and returns the median and the worst.
	frames := func(n int, before func(i int)) map[string]float64 {
		var d []float64
		var allocs uint64
		for i := range n {
			if before != nil {
				before(i)
			}
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			st := time.Now()
			tt.Frame()
			d = append(d, ms(time.Since(st)))
			runtime.ReadMemStats(&m1)
			allocs += m1.TotalAlloc - m0.TotalAlloc
		}
		slices.Sort(d)
		return map[string]float64{"median_ms": d[len(d)/2], "max_ms": d[len(d)-1], "kb_alloc_per_frame": float64(allocs) / float64(n) / 1024}
	}
	report["frame_tree_idle"] = frames(20, nil)
	// Moving the pointer over the treemap: every frame hit-tests and repaints.
	report["frame_treemap_hover"] = frames(40, func(i int) { tt.Move(float32(100+i*35), float32(700+(i%8)*30)) })
	tt.Move(5, 5)
	report["menu_sync_ms"] = menuSync(a)
	// The treemap maximized: the most tiles drawn per frame.
	a.exec("view.maxTreemap")
	tt.Frame()
	until(tt, func() bool { return !a.tm.busy })
	tt.Frame()
	report["treemap_max_cells"] = len(a.tm.cells)
	report["frame_treemap_max_hover"] = frames(40, func(i int) { tt.Move(float32(60+i*37), float32(120+(i%9)*90)) })
	a.exec("view.maxTreemap")
	tt.Move(5, 5)
	tt.Frame()

	// Expanding the whole tree below the root (bounded to 200,000 rows).
	start = time.Now()
	a.tree.expandSubtree(0)
	report["tree_expand_subtree_ms"] = ms(time.Since(start))
	report["tree_rows"] = len(a.tree.rows)
	report["frame_tree_200k_rows"] = frames(20, nil)
	report["frame_tree_scroll"] = frames(30, func(i int) { tt.Scroll(400, 300, 0, 600) })
	start = time.Now()
	a.tree.collapseAll()
	report["tree_collapse_all_ms"] = ms(time.Since(start))
	start = time.Now()
	a.tree.sort = ui.SortOrder{Column: "Name"}
	tt.Frame()
	report["tree_resort_ms"] = ms(time.Since(start))
	a.tree.sort = ui.SortOrder{}
	tt.Frame()

	// File View: every file in the scan, largest first.
	start = time.Now()
	a.setViewMode(viewFiles)
	until(tt, func() bool { return !a.files.busy && a.files.forSnap == a.snap })
	report["files_all_ms"] = ms(time.Since(start))
	report["files_rows"] = len(a.files.ids)
	report["frame_files_idle"] = frames(20, nil)
	report["frame_files_scroll"] = frames(30, func(i int) { tt.Scroll(400, 300, 0, 2000) })
	for _, col := range []string{"Name", "Modified", "Extension", "Folder"} {
		start = time.Now()
		a.files.sort = ui.SortOrder{Column: col}
		a.files.invalidate()
		until(tt, func() bool { return !a.files.busy })
		report["files_sort_"+col+"_ms"] = ms(time.Since(start))
	}
	a.files.sort = ui.SortOrder{Column: "Size", Descending: true}
	for _, q := range []string{"*.dll", "microsoft", "path:system32 ext:dll", "type:folder"} {
		a.search = q
		start = time.Now()
		a.files.invalidate()
		until(tt, func() bool { return !a.files.busy && a.files.ran == q })
		report["search_"+q+"_ms"] = ms(time.Since(start))
	}
	a.search = ""
	a.files.invalidate()
	until(tt, func() bool { return !a.files.busy && a.files.ran == "" })
	start = time.Now()
	a.exec("edit.selectAll")
	report["files_select_all_ms"] = ms(time.Since(start))
	report["frame_files_all_selected"] = frames(5, nil)
	report["menu_sync_all_selected_ms"] = menuSync(a)
	a.tree.selCache.checkpoint()
	a.files.selCache.checkpoint()
	start = time.Now()
	for id, cmd := range a.cmds {
		a.can(id)
		if cmd.checked != nil {
			cmd.checked(a)
		}
	}
	report["menu_sync_all_selected_next_frame_ms"] = ms(time.Since(start))
	a.files.sel.Clear()

	// Extensions.
	start = time.Now()
	a.setViewMode(viewExtensions)
	until(tt, func() bool { return !a.ext.busy && a.ext.forSnap == a.snap })
	report["extensions_ms"] = ms(time.Since(start))
	report["frame_extensions_idle"] = frames(20, nil)

	// Treemap for a different location, and a resize.
	a.setViewMode(viewTree)
	tt.Frame()
	biggest := a.snap.Nodes[0].FirstChild
	for c := biggest; c != index.None; c = a.snap.Nodes[c].NextSibling {
		if a.snap.Nodes[c].IsDir() && a.snap.Nodes[c].Size > a.snap.Nodes[biggest].Size {
			biggest = c
		}
	}
	start = time.Now()
	a.navigate(biggest)
	tt.Frame()
	until(tt, func() bool { return !a.tm.busy })
	report["navigate_treemap_ms"] = ms(time.Since(start))
	start = time.Now()
	tt.SetSize(1900, 1150)
	tt.Frame()
	report["resize_frame_ms"] = ms(time.Since(start))

	// Toggling NTFS metadata, then a delete-like mutation (copy-on-write).
	start = time.Now()
	a.exec("opt.showMeta")
	until(tt, func() bool { return !a.filterBusy })
	report["toggle_metadata_ms"] = ms(time.Since(start))
	a.exec("opt.showMeta")
	until(tt, func() bool { return !a.filterBusy })
	start = time.Now()
	next := a.base.Clone()
	next.Remove(biggest)
	a.replaceBase(next)
	until(tt, func() bool { return !a.filterBusy })
	until(tt, func() bool { return !a.tm.busy })
	report["remove_folder_ms"] = ms(time.Since(start))

	report["heap_app_steady_mb"] = heap()
	debug.FreeOSMemory()
	report["index_mb"] = float64(a.base.Bytes()) / (1 << 20)
	out, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(out))
	if p := os.Getenv("QUARRY_PERF_OUT"); p != "" {
		os.WriteFile(p, out, 0o644)
	}
}

// until waits for background work to report back, applying results as
// a frame would but without rendering frames meanwhile.
func until(tt *ui.Tester, done func() bool) {
	a := testedApp
	for i := 0; i < 600000 && !done(); i++ {
		if !a.drain() {
			time.Sleep(100 * time.Microsecond)
		}
	}
	tt.Frame()
}

var testedApp *App

func syntheticUISnapshot() *index.Snapshot {
	s := index.New(`C:\perf`)
	exts := []string{"dll", "TXT", "log", "zip", "jpg", "exe"}
	for i := range 50 {
		top := s.Add(0, fmt.Sprintf("area%02d", i), 0, 0, 0, index.Directory)
		for j := range 100 {
			dir := s.Add(top, fmt.Sprintf("folder%03d", j), 0, 0, 0, index.Directory)
			for k := range 99 {
				size := uint64((i*7919+j*104729+k*15485863)%(1<<30)) + 1
				s.Add(dir, fmt.Sprintf("file-%03d.%s", k, exts[(i+j+k)%len(exts)]), size, size+4096, int64(1600000000+k*3600), 0)
			}
		}
	}
	s.Finalize()
	return s
}

// Exercise real text shaping, including unusually long Unicode filenames.
func BenchmarkTreemapTooltipText(b *testing.B) {
	name := strings.Repeat("résumé-backup-", 18) + ".zip"
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(300, 60).Draw(func(p *ui.Painter, r ui.Rect) {
			p.Text(r.X, r.Y, fitMiddle(p, name, 12.5, 220), 12.5, ui.Hex("#FFFFFF"))
		})
	}, 400, 100)
	tt.Frame()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tt.Frame()
	}
}

func BenchmarkDerivedSorts(b *testing.B) {
	s := syntheticUISnapshot()
	f, _ := index.ParseFilter("type:file")
	base, err := s.Search(context.Background(), 0, &f)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]index.ID, len(base))
	for _, column := range []string{"Folder", "Extension"} {
		b.Run(column, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				copy(ids, base)
				if err := sortEntries(context.Background(), s, ids, ui.SortOrder{Column: column}, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFileResort(b *testing.B) {
	b.Setenv("QUARRY_CONFIG_DIR", b.TempDir())
	a := newApp()
	a.setBase(syntheticUISnapshot(), "", nil, false)
	wait := func() {
		deadline := time.Now().Add(10 * time.Second)
		for a.files.busy {
			if time.Now().After(deadline) {
				b.Fatal("file model timed out")
			}
			a.drain()
			runtime.Gosched()
		}
	}
	a.files.run()
	wait()
	for _, column := range []string{"Size", "Modified"} {
		b.Run(column, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				a.files.sort = ui.SortOrder{Column: column, Descending: true}
				a.files.run()
				wait()
			}
		})
	}
}

// menuSync is the per-frame cost of updating the menu bar's enabled and
// checked states (syncMenu without a native window).
func menuSync(a *App) float64 {
	start := time.Now()
	a.invalidateSelections()
	for id, cmd := range a.cmds {
		a.can(id)
		if cmd.checked != nil {
			cmd.checked(a)
		}
	}
	return float64(time.Since(start).Microseconds()) / 1000
}

// / installScan does what a finished scan does before the views show it.
func (a *App) installScan(s *index.Snapshot, source string) {
	hide := !a.prefs.ShowMetadata
	meta := hideMetadata(s, hide)
	debug.FreeOSMemory()
	a.setBase(s, source, meta, hide)
}
