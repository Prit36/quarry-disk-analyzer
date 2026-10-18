package desktop

import (
	"bufio"
	"context"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// saveAs asks for a destination, then writes off the UI thread through a
// temporary file, so a failed export never leaves a truncated file behind.
func (a *App) saveAs(title, name, ext, kind string, write func(io.Writer) error) {
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: title, DefaultPath: name, Filters: []mygo.FileFilter{{Name: kind, Extensions: []string{ext}}}})
		if err != nil || path == "" {
			return
		}
		if filepath.Ext(path) == "" {
			path += "." + ext
		}
		a.post(func() { a.notify("Exporting to %s…", filepath.Base(path)) })
		err = atomicWrite(path, write)
		a.post(func() {
			if err != nil {
				a.fail("Export failed: %v", err)
				return
			}
			a.notify("Saved %s.", path)
		})
	}()
}

func atomicWrite(path string, write func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".quarry-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	w := bufio.NewWriterSize(f, 1<<20)
	err = write(w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(f.Name(), path)
}

func (a *App) baseName() string {
	if a.snap == nil {
		return "quarry"
	}
	name := strings.Trim(filepath.Base(a.snap.Root), `\:`)
	if name == "" || name == "." {
		name = strings.Trim(a.snap.Root, `\:`)
	}
	return name + " " + time.Now().Format("2006-01-02")
}

// exportView exports what the active view shows: the File View results, the
// extension summary, or the current location's subtree.
func (a *App) exportView() {
	s := a.snap
	switch a.prefs.View {
	case viewFiles:
		rows := make([]index.Row, len(a.files.ids))
		for i, id := range a.files.ids {
			rows[i] = index.Row{ID: id}
		}
		a.saveAs("Export File View", a.baseName()+" files.csv", "csv", "CSV", func(w io.Writer) error {
			return s.ExportRows(context.Background(), w, rows)
		})
	case viewExtensions:
		a.exportExtensions()
	default:
		loc := a.location
		a.saveAs("Export "+a.name(loc), a.baseName()+" tree.csv", "csv", "CSV", func(w io.Writer) error {
			return s.ExportScope(context.Background(), w, loc)
		})
	}
}

func (a *App) exportSelection() {
	s, ids := a.snap, a.selected()
	rows := make([]index.Row, len(ids))
	for i, id := range ids {
		rows[i] = index.Row{ID: id}
	}
	a.saveAs("Export Selection", a.baseName()+" selection.csv", "csv", "CSV", func(w io.Writer) error {
		return s.ExportRows(context.Background(), w, rows)
	})
}

// exportScan writes every entry of the unfiltered scan.
func (a *App) exportScan() {
	scan := a.completeScan()
	a.saveAs("Export Entire Scan", a.baseName()+".csv", "csv", "CSV", func(w io.Writer) error {
		return scan().Export(context.Background(), w)
	})
}

func (a *App) exportJSON() {
	scan := a.completeScan()
	a.saveAs("Export Scan as JSON", a.baseName()+".json", "json", "JSON", func(w io.Writer) error {
		return scan().ExportJSON(context.Background(), w, 0)
	})
}

func (a *App) exportExtensions() {
	s, scope, allocated, scopeName := a.snap, a.files.scope, a.metricAllocated(), a.files.scopeName()
	a.saveAs("Export Extension Summary", a.baseName()+" extensions.csv", "csv", "CSV", func(w io.Writer) error {
		analysis, err := s.Analyze(context.Background(), scope, allocated, 0)
		if err != nil {
			return err
		}
		var total uint64
		for _, t := range analysis.Types {
			if allocated {
				total += t.Allocated
			} else {
				total += t.Bytes
			}
		}
		c := csv.NewWriter(w)
		_ = c.Write([]string{"Extension", "Type", "Files", "Size", "Allocated", "Percent", "Scope"})
		for _, t := range analysis.Types {
			v := t.Bytes
			if allocated {
				v = t.Allocated
			}
			_ = c.Write([]string{t.Extension, platform.FileTypeName(t.Extension), strconv.FormatUint(t.Count, 10), strconv.FormatUint(t.Bytes, 10), strconv.FormatUint(t.Allocated, 10), strconv.FormatFloat(percent(v, total), 'f', 3, 64), scopeName})
		}
		c.Flush()
		return c.Error()
	})
}

// Snapshots.

var scanFilters = []mygo.FileFilter{{Name: "Quarry scan", Extensions: []string{"quarry", "stratum"}}}

func (a *App) saveSnapshotDialog() {
	scan := a.completeScan()
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: "Save Scan", DefaultPath: a.baseName() + ".quarry", Filters: []mygo.FileFilter{{Name: "Quarry scan", Extensions: []string{"quarry"}}}})
		if err != nil || path == "" {
			return
		}
		if filepath.Ext(path) == "" {
			path += ".quarry"
		}
		err = scan().Save(context.Background(), path)
		a.post(func() {
			if err != nil {
				a.fail("Could not save the scan: %v", err)
				return
			}
			a.notify("Saved scan to %s.", path)
		})
	}()
}

func (a *App) openSnapshotDialog() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Open Saved Scan", Filters: scanFilters})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.openSnapshot(paths[0]) })
	}()
}

func (a *App) openSnapshot(path string) {
	a.notify("Opening %s…", filepath.Base(path))
	hide := !a.prefs.ShowMetadata
	go func() {
		s, err := index.LoadSnapshot(context.Background(), path)
		var meta []index.ID
		if err == nil {
			meta = hideMetadata(s, hide)
		}
		a.post(func() {
			if err != nil {
				a.fail("Could not open %s: %v", filepath.Base(path), err)
				return
			}
			if a.scanning {
				a.stopScan()
				a.scanGen++
				a.scanning = false
			}
			a.setBase(s, path, meta, hide)
			a.notify("Opened saved scan of %s from %s. This is historical data.", s.Root, platform.FormatDateTime(s.Started))
		})
	}()
}

// compareDialog compares the current scan with a saved scan of the same root.
func (a *App) compareDialog() {
	scan, allocated := a.completeScan(), a.metricAllocated()
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Compare with Saved Scan", Filters: scanFilters})
		if err != nil || len(paths) == 0 {
			return
		}
		previous, err := index.LoadSnapshot(context.Background(), paths[0])
		var changes []index.Change
		if err == nil {
			changes, err = index.CompareSnapshots(previous, scan(), allocated)
		}
		a.post(func() {
			if err != nil {
				a.fail("Compare: %v", err)
				return
			}
			a.dlg.compare = compareDialog{open: true, changes: changes, label: filepath.Base(paths[0]) + " (" + platform.FormatDateTime(previous.Started) + ")"}
		})
	}()
}
