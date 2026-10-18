package desktop

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// extModel is the Extensions view: what kinds of files use the space, in
// the same scope as File View. Percentages are of that scope's file total.
type extModel struct {
	a       *App
	types   []index.FileType
	shown   []int
	total   index.FileType
	busy    bool
	err     string
	gen     uint64
	cancel  context.CancelFunc
	dirty   bool
	forSnap *index.Snapshot
	runFor  *index.Snapshot // snapshot being analyzed
	forText string

	list   ui.ListState
	cursor int
	sort   ui.SortOrder
}

func (e *extModel) init(a *App) {
	e.a = a
	e.sort = a.prefs.ExtSort
	e.list.Columns = a.prefs.ExtColumns
	e.reset()
}

func (e *extModel) reset() {
	if e.cancel != nil {
		e.cancel()
	}
	e.gen++
	e.cancel, e.runFor = nil, nil
	cols := e.list.Columns
	e.list = ui.ListState{Columns: cols}
	e.types, e.shown, e.total, e.busy, e.err, e.forSnap = nil, nil, index.FileType{}, false, "", nil
	e.cursor = -1
	e.dirty = true
}

func (e *extModel) invalidate() {
	e.dirty = true
	if e.a.prefs.View == viewExtensions {
		e.run()
	}
}

// ensure analyzes the shown snapshot unless that is already under way.
func (e *extModel) ensure() {
	if e.dirty || e.forSnap != e.a.snap && !(e.busy && e.runFor == e.a.snap) {
		e.run()
	}
}

func (e *extModel) run() {
	a := e.a
	if a.snap == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	e.gen++
	gen := e.gen
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.busy, e.dirty, e.runFor = true, false, a.snap
	s, scope, allocated := a.snap, a.files.scope, a.metricAllocated()
	if !s.Reachable(scope) {
		scope = 0
	}
	go func() {
		defer cancel()
		analysis, err := s.Analyze(ctx, scope, allocated, 0)
		var total index.FileType
		for _, t := range analysis.Types {
			total.Bytes += t.Bytes
			total.Allocated += t.Allocated
			total.Count += t.Count
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != e.gen {
				return
			}
			e.busy, e.forSnap = false, s
			if err != nil {
				e.err = err.Error()
				return
			}
			e.err, e.types, e.total = "", analysis.Types, total
			e.forText = "\x00"
			e.cursor = -1
		})
		if err != nil {
			return
		}
		// Explorer's type names, resolved after the table is shown, off the
		// UI thread and in parallel; the table draws cached names only.
		exts := make([]string, len(analysis.Types))
		for i, t := range analysis.Types {
			exts[i] = t.Extension
		}
		platform.ResolveFileTypeNames(ctx, exts)
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen == e.gen {
				e.forText = "\x00" // filter and sort again with the names
			}
		})
	}()
}

// refresh applies the text filter and sort to the computed groups.
func (e *extModel) refresh(text string) {
	text = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "*")))
	if text == e.forText {
		return
	}
	e.forText = text
	e.shown = e.shown[:0]
	for i, t := range e.types {
		if text == "" || strings.Contains(t.Extension, text) || strings.Contains(strings.ToLower(platform.CachedFileTypeName(t.Extension)), text) {
			e.shown = append(e.shown, i)
		}
	}
	e.sortShown()
}

func (e *extModel) sortShown() {
	allocated := e.a.metricAllocated()
	by := e.sort
	var typeNames []string
	if by.Column == "Type" {
		typeNames = make([]string, len(e.types))
		for _, i := range e.shown {
			typeNames[i] = strings.ToLower(platform.CachedFileTypeName(e.types[i].Extension))
		}
	}
	slices.SortStableFunc(e.shown, func(x, y int) int {
		a, b := e.types[x], e.types[y]
		var v int
		switch by.Column {
		case "Extension":
			v = strings.Compare(a.Extension, b.Extension)
		case "Type":
			v = strings.Compare(typeNames[x], typeNames[y])
		case "Files":
			v = cmp.Compare(a.Count, b.Count)
		case "Allocated":
			v = cmp.Compare(a.Allocated, b.Allocated)
		case "Size":
			v = cmp.Compare(a.Bytes, b.Bytes)
		default:
			if allocated {
				v = cmp.Compare(a.Allocated, b.Allocated)
			} else {
				v = cmp.Compare(a.Bytes, b.Bytes)
			}
		}
		if by.Descending {
			v = -v
		}
		if v == 0 {
			v = strings.Compare(a.Extension, b.Extension)
		}
		return v
	})
}

func (e *extModel) currentExt() (string, bool) {
	if e.cursor >= 0 && e.cursor < len(e.shown) {
		return e.types[e.shown[e.cursor]].Extension, true
	}
	return "", false
}

// showFiles opens File View filtered to an extension, in the same scope.
func (a *App) showExtensionFiles(ext string) {
	q := "ext:" + strings.TrimPrefix(ext, ".")
	if ext == "(no extension)" {
		q = "ext:none"
	}
	a.search = q + " type:file"
	a.files.sort = ui.SortOrder{Column: a.sizeColumn(), Descending: true}
	a.prefs.View = viewFiles
	a.files.dirty = true
	a.files.run()
	a.focusTable = true
}

// sizeColumn is the column that sorts by the active size metric.
func (a *App) sizeColumn() string {
	if a.metricAllocated() {
		return "Allocated"
	}
	return "Size"
}
