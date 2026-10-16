package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
	"stratum/internal/platform"
	"stratum/internal/scan"
	"stratum/internal/treemap"
)

type event struct {
	kind          string
	generation    uint64
	snapshot      *index.Snapshot
	summary       index.Summary
	query         index.QueryResult
	branches      []treemap.Branch
	path          string
	err           error
	selectedIndex int
}
type App struct {
	window                            *mygo.Window
	path, query, notice               string
	busy, queryBusy, exporting, light bool
	admin, mftBlocked                 bool
	scanMode                          string
	progress                          *scan.Progress
	started                           time.Time
	cancel, queryCancel               context.CancelFunc
	events                            chan event
	done                              chan struct{}
	generation, mapGeneration         uint64
	snapshot                          *index.Snapshot
	summary                           index.Summary
	drives                            []platform.Drive
	rows                              []index.Row
	matches, selectedRow, tab, metric int
	selected, focus                   index.ID
	open                              map[index.ID]bool
	list                              ui.ListState
	typeList                          ui.ListState
	typeRow                           int
	typeSort                          ui.SortOrder
	columnSplit                       float32
	sort                              ui.SortOrder
	queryDue                          time.Time
	searchHandle                      ui.Handle
	divider                           float32
	branches                          []treemap.Branch
	cells                             []treemap.Cell
	mapCancel                         context.CancelFunc
	mapWidth, mapHeight               float32
	mapFor                            index.ID
}

func New(path string) *App {
	if path == "" {
		path = `C:\`
	}
	return &App{path: path, events: make(chan event, 16), done: make(chan struct{}), drives: platform.Drives(), open: make(map[index.ID]bool), sort: ui.SortOrder{Column: "Allocated", Descending: true}, typeSort: ui.SortOrder{Column: "Allocated", Descending: true}, divider: 62, columnSplit: 68, selectedRow: -1, typeRow: -1, selected: index.None, mapFor: index.None, admin: platform.Elevated(), scanMode: "Automatic"}
}
func Run(initial string) error {
	app := New(initial)
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		app.window = mygo.NewWindow(mygo.WindowOptions{Title: "Stratum", Width: 1400, Height: 850, MinWidth: 1000, MinHeight: 600, BackgroundColor: "#202020", Content: ui.View(app.view)})
		// Win32 can consume the first ShowWindow using the launcher's SW_HIDE.
		app.window.Show()
		app.window.OnClosed(func() {
			close(app.done)
			if app.cancel != nil {
				app.cancel()
			}
			if app.queryCancel != nil {
				app.queryCancel()
			}
			if app.mapCancel != nil {
				app.mapCancel()
			}
		})
		if initial != "" {
			app.start()
		}
	})
	return mygo.App.Run()
}
func (a *App) start() {
	if a.busy {
		return
	}
	if strings.TrimSpace(a.path) == "" {
		a.notice = "Choose a drive or enter a folder path."
		return
	}
	if a.queryCancel != nil {
		a.queryCancel()
	}
	a.queryBusy = false
	a.queryDue = time.Time{}
	a.generation++
	a.progress = &scan.Progress{}
	a.started = time.Now()
	a.busy = true
	a.mftBlocked = false
	a.notice = ""
	a.mapGeneration++
	if a.mapCancel != nil {
		a.mapCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	root, forceWalk, progress := a.path, a.scanMode == "Folders", a.progress
	go func() {
		s, err := scan.Run(ctx, root, scan.Options{ForceWalk: forceWalk}, progress)
		var summary index.Summary
		if err == nil && s != nil {
			progress.Phase.Store("Indexing file types")
			summary, err = s.Summarize(context.Background())
		}
		// Release the transient identity map, aggregation queue and scan buffers
		// once, off the UI thread. Idle frames then retain the compact index.
		runtime.GC()
		a.post(event{kind: "scan", snapshot: s, summary: summary, err: err})
	}()
}
func (a *App) drain() {
	for {
		select {
		case e := <-a.events:
			switch e.kind {
			case "scan":
				a.busy = false
				a.cancel = nil
				if e.err != nil {
					var mft *scan.MFTError
					a.mftBlocked = errors.As(e.err, &mft)
					a.notice = e.err.Error()
					continue
				}
				// Commit the replacement together; a failed scan preserves the
				// previous immutable result and its usable table/map state.
				a.rows = nil
				a.branches = nil
				a.cells = nil
				a.mapWidth = 0
				a.selectedRow = -1
				a.query = ""
				a.tab = 0
				a.open = make(map[index.ID]bool)
				a.snapshot = e.snapshot
				a.summary = e.summary
				a.path = e.snapshot.Root
				a.selected = 0
				a.focus = 0
				a.open[0] = true
				a.requestRows()
				a.requestMap(0)
				if e.snapshot.Cancelled {
					a.notice = "Scan stopped. Results show only the files visited before cancellation."
				}
			case "path":
				if e.err != nil {
					a.notice = e.err.Error()
				} else if e.path != "" {
					a.path = e.path
					a.start()
				}
			case "query":
				if e.generation == a.generation {
					a.queryBusy = false
					if e.err != nil {
						a.notice = e.err.Error()
					} else {
						a.rows = e.query.Rows
						a.matches = e.query.Matches
						a.selectedRow = e.selectedIndex
						if a.selectedRow >= 0 {
							a.list.ScrollIntoView(a.selectedRow)
						}
					}
				}
			case "map":
				if e.generation == a.mapGeneration {
					a.branches = e.branches
					a.mapWidth = 0
				}
			case "export":
				a.exporting = false
				if e.err != nil {
					a.notice = "Export failed: " + e.err.Error()
				} else if e.path != "" {
					a.notice = "CSV exported to " + e.path
				}
			case "elevate":
				if e.err != nil {
					a.notice = e.err.Error()
				} else if a.window != nil {
					a.window.Close()
				}
			}
		default:
			return
		}
	}
}
func (a *App) requestRows() {
	if a.snapshot == nil {
		return
	}
	if a.queryCancel != nil {
		a.queryCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.queryCancel = cancel
	a.generation++
	generation := a.generation
	a.queryBusy = true
	a.queryDue = time.Time{}
	open := make(map[index.ID]bool, len(a.open))
	for id, v := range a.open {
		open[id] = v
	}
	mode := "tree"
	if a.tab == 1 {
		mode = "largest"
	}
	q := index.Query{Text: a.query, Mode: mode, Root: a.focus, Open: open, Sort: index.Sort{Column: a.sort.Column, Desc: a.sort.Descending, Allocated: a.metric == 1}}
	s := a.snapshot
	selected := a.selected
	go func() {
		result, err := s.QueryRows(ctx, q)
		selectedIndex := -1
		for i, r := range result.Rows {
			if r.ID == selected {
				selectedIndex = i
				break
			}
		}
		if ctx.Err() == nil {
			a.post(event{kind: "query", generation: generation, query: result, err: err, selectedIndex: selectedIndex})
		}
	}()
}
func (a *App) requestMap(id index.ID) {
	if a.snapshot == nil {
		return
	}
	if !a.snapshot.Nodes[id].IsDir() {
		id = a.snapshot.Nodes[id].Parent
	}
	if id == index.None {
		id = 0
	}
	a.mapFor = id
	if a.mapCancel != nil {
		a.mapCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mapCancel = cancel
	a.mapGeneration++
	generation := a.mapGeneration
	s, allocated := a.snapshot, a.metric == 1
	go func() {
		branches := treemap.Hierarchy(ctx, s, id, allocated, 4096)
		if ctx.Err() == nil {
			a.post(event{kind: "map", generation: generation, branches: branches})
		}
	}()
}
func (a *App) chooseFolder() {
	if a.busy {
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.window, Title: "Choose a folder to analyze", Directory: true, DefaultPath: a.path})
		e := event{kind: "path", err: err}
		if len(paths) > 0 {
			e.path = paths[0]
		}
		a.post(e)
	}()
}
func (a *App) export() {
	if a.snapshot == nil || a.exporting {
		return
	}
	a.exporting = true
	s := a.snapshot
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.window, Title: "Export scan as CSV", DefaultPath: "stratum-scan.csv", Filters: []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}}})
		if err == nil && path != "" {
			err = exportFile(s, path)
		}
		a.post(event{kind: "export", path: path, err: err})
	}()
}
func exportFile(s *index.Snapshot, path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".stratum-*.csv")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	err = s.Export(context.Background(), f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (a *App) navigate(id index.ID) {
	if a.snapshot == nil || !a.snapshot.Nodes[id].IsDir() {
		return
	}
	a.focus = id
	a.selected = id
	a.open[id] = true
	a.query = ""
	a.tab = 0
	a.requestRows()
	a.requestMap(id)
}
func (a *App) safePath(id index.ID) string {
	if a.snapshot == nil || id == index.None {
		return ""
	}
	for n := id; n != index.None; n = a.snapshot.Nodes[n].Parent {
		if a.snapshot.Nodes[n].Flags&index.Unplaced != 0 {
			a.notice = "This record has an unresolved metadata path."
			return ""
		}
	}
	return a.snapshot.Path(id)
}
func (a *App) reveal() {
	if path := a.safePath(a.selected); path != "" {
		mygo.Shell.ShowItemInFolder(path)
	}
}
func (a *App) copyPath() {
	if path := a.safePath(a.selected); path != "" {
		mygo.Clipboard.WriteText(path)
		a.notice = "Path copied to clipboard."
	}
}
func (a *App) setQuery(c *ui.Context) {
	a.queryDue = c.Now().Add(180 * time.Millisecond)
	c.After(180 * time.Millisecond)
}

func (a *App) elevate() {
	if a.busy || a.admin {
		return
	}
	path := a.path
	go func() { a.post(event{kind: "elevate", err: platform.RelaunchElevated(path)}) }()
}

func (a *App) post(e event) {
	select {
	case a.events <- e:
		if a.window != nil {
			a.window.Invalidate()
		}
	case <-a.done:
	}
}

func Bytes(n uint64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(n) / 1024
	u := 0
	for value >= 1024 && u < len(units)-1 {
		value /= 1024
		u++
	}
	return fmt.Sprintf("%.2f %s", value, units[u])
}
func number(n uint64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
