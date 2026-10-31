package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"quarry/internal/desktop"
	"quarry/internal/platform"
	"quarry/internal/scan"
)

const version = "0.2.6"

type patterns []string

func (p *patterns) String() string     { return strings.Join(*p, ";") }
func (p *patterns) Set(v string) error { *p = append(*p, v); return nil }

func main() {
	// An elevated window delegates Explorer selection to an unelevated copy
	// of Quarry. Handle it before console attachment and GUI initialization.
	if len(os.Args) > 1 && os.Args[1] == "--reveal-in-explorer" {
		os.Exit(revealInExplorer(os.Args[2:]))
	}
	desktop.Version = version
	// The executable is a GUI program; attach to the console it was started
	// from only for command-line use, so double-clicking opens no console.
	cli := len(os.Args) > 1 && strings.HasPrefix(os.Args[1], "-")
	if cli {
		platform.AttachParentConsole()
	}
	os.Exit(run(os.Args[1:], cli))
}

func revealInExplorer(paths []string) int {
	if len(paths) == 0 || platform.Elevated() {
		return 2
	}
	if err := platform.ShowInExplorer(paths); err != nil {
		return 1
	}
	return 0
}

func run(args []string, cli bool) int {
	fs := flag.NewFlagSet("Quarry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	target := fs.String("scan", "", "")
	csvPath := fs.String("csv", "", "")
	savePath := fs.String("save", "", "")
	asJSON := fs.Bool("json", false, "")
	walk := fs.Bool("walk", false, "")
	quiet := fs.Bool("quiet", false, "")
	path := fs.String("path", "", "") // compatibility with earlier releases
	workers := fs.Int("workers", 0, "")
	_ = fs.Bool("fallback", true, "") // earlier releases; falling back is now the default
	var exclude patterns
	fs.Var(&exclude, "exclude", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(desktop.CLIUsage)
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, desktop.CLIUsage)
		return 2
	}
	if *target == "" {
		if *csvPath != "" || *savePath != "" || *asJSON {
			fmt.Fprintln(os.Stderr, "-csv, -save and -json require -scan <folder>")
			return 2
		}
		initial := *path
		if fs.NArg() > 0 {
			initial = fs.Arg(0)
		}
		if err := desktop.Run(initial); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	return headless(*target, *csvPath, *savePath, *asJSON, *walk, *quiet, *workers, exclude)
}

func headless(target, csvPath, savePath string, asJSON, walk, quiet bool, workers int, exclude []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logf := func(format string, a ...any) {
		if !quiet && !asJSON {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}
	logf("Scanning %s...", target)
	s, err := scan.Run(ctx, target, scan.Options{ForceWalk: walk, AllowFallback: true, Exclude: exclude, Workers: workers}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan failed:", err)
		return 1
	}
	if s.Excluded > 0 {
		logf("%d entries excluded by %s; totals exclude them.", s.Excluded, strings.Join(s.Exclusions, ", "))
	}
	if s.Fallback != "" {
		logf("Direct MFT reading unavailable (%s); folders were enumerated.", s.Fallback)
	}
	root := s.Nodes[0]
	logf("%s files, %s folders, %d bytes logical, %d bytes allocated, in %s (%s)", fmtCount(root.Files), fmtCount(root.Dirs), root.Size, root.Allocated, s.Elapsed.Round(1e6), s.Engine)
	if csvPath != "" {
		if err := writeFile(csvPath, func(w io.Writer) error { return s.Export(ctx, w) }); err != nil {
			fmt.Fprintln(os.Stderr, "CSV export failed:", err)
			return 1
		}
		logf("Wrote %s", csvPath)
	}
	if savePath != "" {
		if err := s.Save(ctx, savePath); err != nil {
			fmt.Fprintln(os.Stderr, "saving the scan failed:", err)
			return 1
		}
		logf("Saved %s", savePath)
	}
	if asJSON {
		runtime.GC()
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"root": s.Root, "engine": s.Engine, "fallback": s.Fallback, "elapsed_ms": s.Elapsed.Milliseconds(),
			"files": root.Files, "folders": root.Dirs, "logical_bytes": root.Size, "allocated_bytes": root.Allocated,
			"errors": s.Errors, "hard_link_aliases": s.HardLinks, "skipped_links": s.SkippedLinks,
			"excluded": s.Excluded, "exclusions": s.Exclusions, "cancelled": s.Cancelled, "issues": s.Issues,
		})
	}
	if s.Cancelled {
		fmt.Fprintln(os.Stderr, "scan interrupted; results are partial")
		return 1
	}
	if s.Errors > 0 {
		logf("%d items could not be read; totals are incomplete.", s.Errors)
		return 3
	}
	return 0
}

func writeFile(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

func fmtCount(n uint32) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
