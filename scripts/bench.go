//go:build ignore

// bench measures a real scan and the index operations behind every view.
// One scan per process, so peak memory figures belong to that scan alone:
//
//	go run scripts/bench.go -scan C:\ [-walk] [-workers N] [-save C.quarry] [-ops] > result.json
//	go run scripts/bench.go -load C.quarry -ops > result.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"quarry/internal/index"
	"quarry/internal/scan"
	"quarry/internal/treemap"
)

var getMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

type memoryCounters struct {
	cb                                                           uint32
	pageFaults                                                   uint32
	peakWorkingSet, workingSet                                   uintptr
	quotaPeakPaged, quotaPaged, quotaPeakNonPaged, quotaNonPaged uintptr
	pagefile, peakPagefile, private                              uintptr
}

func process() memoryCounters {
	var m memoryCounters
	m.cb = uint32(unsafe.Sizeof(m))
	getMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&m)), uintptr(m.cb))
	return m
}

func cpuTime() time.Duration {
	var c, e, k, u windows.Filetime
	windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u)
	return time.Duration(k.Nanoseconds() + u.Nanoseconds())
}

func mb(v uint64) float64 { return float64(v) / (1 << 20) }

// heapSampler records the peak of live heap objects while work runs.
type heapSampler struct {
	stop chan struct{}
	done sync.WaitGroup
	peak uint64
}

func sampleHeap() *heapSampler {
	h := &heapSampler{stop: make(chan struct{})}
	h.done.Add(1)
	go func() {
		defer h.done.Done()
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		for {
			metrics.Read(sample)
			h.peak = max(h.peak, sample[0].Value.Uint64())
			select {
			case <-h.stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return h
}
func (h *heapSampler) end() uint64 { close(h.stop); h.done.Wait(); return h.peak }

type op struct {
	Name       string  `json:"name"`
	Ms         float64 `json:"ms"`
	AllocMB    float64 `json:"alloc_mb"`
	Allocs     uint64  `json:"allocs"`
	PeakHeapMB float64 `json:"peak_heap_mb"`
	Note       string  `json:"note,omitempty"`
}

// measure runs fn several times and keeps the fastest run.
func measure(name string, runs int, fn func() string) op {
	best := op{Name: name, Ms: 1e18}
	for range runs {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		h := sampleHeap()
		start := time.Now()
		note := fn()
		elapsed := time.Since(start)
		peak := h.end()
		runtime.ReadMemStats(&after)
		if ms := float64(elapsed.Microseconds()) / 1000; ms < best.Ms {
			best = op{name, ms, mb(after.TotalAlloc - before.TotalAlloc), after.Mallocs - before.Mallocs, mb(peak), note}
		}
	}
	return best
}

func main() {
	target := flag.String("scan", "", "")
	load := flag.String("load", "", "")
	walk := flag.Bool("walk", false, "")
	workers := flag.Int("workers", 0, "")
	save := flag.String("save", "", "")
	ops := flag.Bool("ops", false, "")
	runs := flag.Int("runs", 3, "")
	cpuProfile := flag.String("cpuprofile", "", "")
	memProfile := flag.String("memprofile", "", "")
	flag.Parse()
	if *memProfile != "" {
		runtime.MemProfileRate = 64 << 10
		defer func() {
			f, _ := os.Create(*memProfile)
			pprof.Lookup("allocs").WriteTo(f, 0)
			f.Close()
		}()
	}
	if *cpuProfile != "" {
		f, _ := os.Create(*cpuProfile)
		pprof.StartCPUProfile(f)
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	result := map[string]any{"go": runtime.Version(), "cpus": runtime.NumCPU(), "elevated": windows.GetCurrentProcessToken().IsElevated()}
	var s *index.Snapshot
	var err error
	if *target != "" {
		p := &scan.Progress{}
		phases := []map[string]any{}
		stopPhases := make(chan struct{})
		var phaseDone sync.WaitGroup
		phaseDone.Add(1)
		start := time.Now()
		go func() {
			defer phaseDone.Done()
			last, at := "", start
			for {
				if cur := p.Status(); cur != last {
					if last != "" {
						phases = append(phases, map[string]any{"phase": last, "ms": float64(time.Since(at).Microseconds()) / 1000})
					}
					last, at = cur, time.Now()
				}
				select {
				case <-stopPhases:
					phases = append(phases, map[string]any{"phase": last, "ms": float64(time.Since(at).Microseconds()) / 1000})
					return
				case <-time.After(500 * time.Microsecond):
				}
			}
		}()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu0 := cpuTime()
		h := sampleHeap()
		s, err = scan.Run(context.Background(), *target, scan.Options{ForceWalk: *walk, AllowFallback: true, Workers: *workers}, p)
		elapsed := time.Since(start)
		peakHeap := h.end()
		cpu := cpuTime() - cpu0
		close(stopPhases)
		phaseDone.Wait()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		peak := process()
		runtime.GC()
		var live runtime.MemStats
		runtime.ReadMemStats(&live)
		debug.FreeOSMemory()
		settled := process()
		root := s.Nodes[0]
		result["scan"] = map[string]any{
			"root": s.Root, "engine": s.Engine, "fallback": s.Fallback, "elapsed_ms": float64(elapsed.Microseconds()) / 1000,
			"cpu_ms": cpu.Milliseconds(), "phases": phases,
			"files": root.Files, "folders": root.Dirs, "nodes": len(s.Nodes), "name_bytes": len(s.Names), "logical_bytes": root.Size, "allocated_bytes": root.Allocated,
			"errors": s.Errors, "hard_links": s.HardLinks, "unresolved": s.Unresolved,
			"index_mb": mb(s.Bytes()), "nodes_cap": cap(s.Nodes), "names_cap": cap(s.Names),
			"alloc_during_scan_mb": mb(after.TotalAlloc - before.TotalAlloc), "mallocs_during_scan": after.Mallocs - before.Mallocs, "gc_cycles": after.NumGC - before.NumGC,
			"gc_pause_ms":          float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6,
			"peak_heap_objects_mb": mb(peakHeap), "peak_working_set_mb": mb(uint64(peak.peakWorkingSet)), "peak_private_mb": mb(uint64(peak.peakPagefile)),
			"heap_live_after_gc_mb": mb(live.HeapAlloc), "working_set_after_free_mb": mb(uint64(settled.workingSet)), "private_after_free_mb": mb(uint64(settled.private)),
		}
		if *save != "" {
			if err := s.Save(context.Background(), *save); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	} else if *load != "" {
		start := time.Now()
		s, err = index.LoadSnapshot(context.Background(), *load)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		result["load_ms"] = float64(time.Since(start).Microseconds()) / 1000
		result["nodes"] = len(s.Nodes)
	} else {
		fmt.Fprintln(os.Stderr, "-scan or -load")
		os.Exit(2)
	}
	if *ops {
		result["ops"] = indexOps(s, *runs)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

func indexOps(s *index.Snapshot, runs int) []op {
	ctx := context.Background()
	var out []op
	search := func(name, query string, kind index.Kind, sort bool) {
		out = append(out, measure(name, runs, func() string {
			f, err := index.ParseFilter(query)
			if err != nil {
				return err.Error()
			}
			if kind != index.KindAny && f.Kind == index.KindAny {
				f.Kind = kind
			}
			ids, _ := s.Search(ctx, 0, &f)
			if sort {
				s.SortContext(ctx, ids, index.Sort{Desc: true})
			}
			return fmt.Sprint(len(ids), " matches")
		}))
	}
	search("files: all files sorted by size", "", index.KindFiles, true)
	search("search: ext:dll sorted", "ext:dll", index.KindAny, true)
	search("search: *.log", "*.log", index.KindAny, true)
	search("search: substring 'microsoft'", "microsoft", index.KindAny, true)
	search("search: path:node_modules", "path:node_modules", index.KindAny, true)
	search("search: size:>100MB", "size:>100MB", index.KindAny, true)
	out = append(out, measure("sort all files by name", runs, func() string {
		f := index.Filter{Kind: index.KindFiles}
		ids, _ := s.Search(ctx, 0, &f)
		s.SortContext(ctx, ids, index.Sort{Column: "Name"})
		return ""
	}))
	out = append(out, measure("sort all files by modified", runs, func() string {
		f := index.Filter{Kind: index.KindFiles}
		ids, _ := s.Search(ctx, 0, &f)
		s.SortContext(ctx, ids, index.Sort{Column: "Modified", Desc: true})
		return ""
	}))
	out = append(out, measure("extensions: Analyze(root)", runs, func() string {
		a, _ := s.Analyze(ctx, 0, false, 0)
		return fmt.Sprint(len(a.Types), " types")
	}))
	out = append(out, measure("extensions: Summarize", runs, func() string {
		a, _ := s.Summarize(ctx)
		return fmt.Sprint(len(a.Types), " types")
	}))
	out = append(out, measure("treemap: Hierarchy(root, 6000)", runs, func() string {
		b := treemap.Hierarchy(ctx, s, 0, false, 6000)
		cells := treemap.NestedLayout(b, treemap.Rect{W: 1600, H: 420})
		return fmt.Sprint(len(cells), " cells")
	}))
	out = append(out, measure("treemap: NestedLayout only", runs, func() string {
		return ""
	}))
	b := treemap.Hierarchy(ctx, s, 0, false, 6000)
	out[len(out)-1] = measure("treemap: NestedLayout only", runs, func() string {
		return fmt.Sprint(len(treemap.NestedLayout(b, treemap.Rect{W: 1600, H: 420})), " cells")
	})
	out = append(out, measure("tree: root children sorted", runs, func() string {
		k := s.Children(0)
		s.Sort(k, index.Sort{Desc: true})
		return fmt.Sprint(len(k))
	}))
	out = append(out, measure("Clone", runs, func() string { s.Clone(); return "" }))
	out = append(out, measure("Filtered (hide metadata)", runs, func() string {
		meta := func(id index.ID) bool { return s.Nodes[id].Parent == 0 && s.IsMetadata(id) }
		v, _, _ := s.Filtered(ctx, func(id index.ID) bool { return !meta(id) }, meta, false)
		return fmt.Sprint(v.Nodes[0].Files, " files")
	}))
	out = append(out, measure("Filtered (isolate *.dll)", runs, func() string {
		f, _ := index.ParseFilter("ext:dll")
		v, _, _ := s.Filtered(ctx, func(id index.ID) bool { return f.MatchNode(s.NameView(id), s.Nodes[id], nil, false) }, nil, true)
		return fmt.Sprint(v.Nodes[0].Files, " files")
	}))
	out = append(out, measure("Compact", runs, func() string { s.Compact(); return "" }))
	dir, _ := os.MkdirTemp("", "quarry-bench")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "scan.quarry")
	out = append(out, measure("snapshot Save", runs, func() string {
		s.Save(ctx, path)
		st, _ := os.Stat(path)
		return fmt.Sprintf("%.1f MB on disk", mb(uint64(st.Size())))
	}))
	out = append(out, measure("snapshot Load", runs, func() string {
		_, err := index.LoadSnapshot(ctx, path)
		if err != nil {
			return err.Error()
		}
		return ""
	}))
	out = append(out, measure("export CSV (discard)", 1, func() string { s.Export(ctx, io.Discard); return "" }))
	out = append(out, measure("Path() for 100k files", runs, func() string {
		n := 0
		for i := len(s.Nodes) - 1; i > 0 && n < 100000; i -= 7 {
			_ = s.Path(index.ID(i))
			n++
		}
		return ""
	}))
	out = append(out, measure("Finalize (re-aggregate whole index)", runs, func() string {
		c := s.Clone()
		c.Finalize()
		return ""
	}))
	return out
}
