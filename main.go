package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"stratum/internal/desktop"
	"stratum/internal/scan"
)

func main() {
	path := flag.String("path", "", "Open the native app and analyze this folder")
	headless := flag.String("scan", "", "Scan without a window and print JSON metrics")
	forceWalk := flag.Bool("walk", false, "Force directory scanning")
	fallback := flag.Bool("fallback", false, "Allow a failed MFT scan to fall back to directory traversal")
	workers := flag.Int("workers", 0, "Directory workers (default: up to 4, maximum: 16)")
	csv := flag.String("csv", "", "Export a headless scan to this CSV file")
	flag.Parse()
	if *headless != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		s, err := scan.Run(ctx, *headless, scan.Options{ForceWalk: *forceWalk, Workers: *workers, AllowFallback: *fallback}, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if *csv != "" {
			f, err := os.Create(*csv)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			err = s.Export(ctx, f)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		runtime.GC()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		n := s.Nodes[0]
		result := map[string]any{"root": s.Root, "engine": s.Engine, "elapsed_ms": float64(s.Elapsed.Microseconds()) / 1000, "files": n.Files, "folders": n.Dirs, "logical_bytes": n.Size, "allocated_bytes": n.Allocated, "index_bytes": s.Bytes(), "heap_bytes": mem.HeapAlloc, "errors": s.Errors, "hard_link_aliases": s.HardLinks, "skipped_directory_links": s.SkippedLinks, "cancelled": s.Cancelled, "fallback": s.Fallback, "issues": s.Issues}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := desktop.Run(*path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
