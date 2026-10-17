package desktop

import (
	"context"
	"errors"
	"fmt"
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
	drives        []platform.Drive
	selected      index.ID
	mapTypes      []index.FileType
	duplicates    index.DuplicateResult
	savedScans    []index.SnapshotInfo
	changes       []index.Change
	analysis      index.Analysis
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
	mapZoom                           float64
	mapPanX, mapPanY                  float32
	typeFilterOpen, allTypes          bool
	listSelection                     index.ID
	issueList                         ui.ListState
	historyOpen                       bool
	mapTypes                          []index.FileType
	mapTypeList                       ui.ListState
	sunPaths                          []ui.Path
	sunWidth, sunHeight               float32
	duplicateResult                   index.DuplicateResult
	duplicateBusy                     bool
	duplicateCancel                   context.CancelFunc
	duplicateGeneration               uint64
	duplicateSource                   *index.Snapshot
	duplicateRoot                     index.ID
	duplicateProgress                 *index.DuplicateProgress
	duplicateList                     ui.ListState
	duplicateRow                      int
	savedScans                        []index.SnapshotInfo
	historyLoaded, historyBusy        bool
	savedList, changesList            ui.ListState
	savedRow                          int
	changes                           []index.Change
	comparisonLabel                   string
	sectors                           []treemap.Sector
	selectAddress                     bool
	advancedOpen                      bool
	filterName                        string
	filterExtension                   string
	filterMinimum                     string
	filterMaximum                     string
	filterKind                        string
	filterAfter                       string
	filterBefore                      string
	hiddenColumns                     map[string]bool
	recentLocations                   []string
	units                             string
	analyticsCollapsed, mapCollapsed  bool
	analysis                          index.Analysis
	analysisSource                    *index.Snapshot
	analysisRoot                      index.ID
	analysisMetric                    int
	analysisGeneration                uint64
	analysisCancel                    context.CancelFunc
	analysisBusy                      bool
	followSelection                   bool
	typeQuery                         string
	visibleTypes                      []index.FileType
	topList                           ui.ListState
	topRow                            int
	window                            *mygo.Window
	path, query, notice               string
	address                           string
	settingsOpen, issuesOpen          bool
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
	searchScope                       string
	compact                           bool
	mapTab, typeTab                   int
	matches, selectedRow, tab, metric int
	selected, focus                   index.ID
	open                              map[index.ID]bool
	history                           []index.ID
	historyPos                        int
	list                              ui.ListState
	typeList                          ui.ListState
	typeRow                           int
	typeSort                          ui.SortOrder
	columnSplit                       float32
	sort                              ui.SortOrder
	queryDue                          time.Time
	searchHandle, pathHandle          ui.Handle
	divider                           float32
	branches                          []treemap.Branch
	cells                             []treemap.Cell
	mapCancel                         context.CancelFunc
	mapWidth, mapHeight               float32
	mapFor                            index.ID
	showMetadata                      bool
	mapTotal                          uint64
	mapBusy                           bool
	mapHistory                        []index.ID
	mapHistoryPos                     int
}

func New(path string) *App {
	if path == "" {
		path = `C:\`
	}
	a := &App{path: path, events: make(chan event, 16), done: make(chan struct{}), drives: platform.Drives(), open: make(map[index.ID]bool), history: []index.ID{0}, historyPos: 0, sort: ui.SortOrder{Column: "Size", Descending: true}, typeSort: ui.SortOrder{Column: "Bytes", Descending: true}, divider: defaultDivider, columnSplit: 68, typeTab: 1, mapTab: 0, searchScope: "Subtree", selectedRow: -1, typeRow: -1, selected: index.None, mapFor: index.None, admin: platform.Elevated(), scanMode: "Automatic"}
	a.followSelection = true
	a.mapZoom = 1
	a.showMetadata = true
	a.hiddenColumns = make(map[string]bool)
	a.units = "Binary"
	a.filterKind = "Files"
	a.loadPreferences()
	a.address = a.path
	a.analysisRoot = index.None
	a.topRow = -1
	a.listSelection = index.None
	a.duplicateRow = -1
	a.savedRow = -1
	return a
}
func Run(initial string) error {
	app := New(initial)
	mygo.Theme.SetSource(mygo.ThemeDark)
	mygo.App.WhenReady(func() {
		app.window = mygo.NewWindow(mygo.WindowOptions{Title: "Stratum — Disk Space Analyzer", StateKey: "main", TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 42, Width: 1672, Height: 941, UseContentSize: true, MinWidth: 1000, MinHeight: 600, BackgroundColor: windowBackground, Content: ui.View(app.view)})
		// Win32 can consume the first ShowWindow using the launcher's SW_HIDE.
		app.window.Show()
		app.window.OnClosed(func() {
			app.savePreferences()
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
			if app.analysisCancel != nil {
				app.analysisCancel()
			}
			if app.duplicateCancel != nil {
				app.duplicateCancel()
			}
		})
		if initial != "" {
			app.start()
		}
	})
	return mygo.App.Run()
}
func (a *App) start() {
	if a.busy || a.historyBusy {
		return
	}
	if a.duplicateCancel != nil {
		a.duplicateCancel()
		a.duplicateGeneration++
		a.duplicateBusy = false
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
	a.mapBusy = false
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
		var drives []platform.Drive
		if err == nil {
			drives = platform.Drives()
		}
		a.post(event{kind: "scan", snapshot: s, summary: summary, err: err, drives: drives})
	}()
}
func (a *App) drain() {
	for {
		select {
		case e := <-a.events:
			switch e.kind {
			case "duplicates":
				if e.generation == a.duplicateGeneration && e.snapshot == a.snapshot {
					a.duplicateBusy = false
					a.duplicateResult = e.duplicates
					if e.err != nil {
						a.notice = "Duplicate check: " + e.err.Error()
					}
				}
			case "history":
				a.historyBusy = false
				a.historyLoaded = true
				a.savedScans = e.savedScans
				a.savedRow = -1
				if e.err != nil {
					a.notice = e.err.Error()
				}
			case "saved":
				a.historyBusy = false
				a.historyLoaded = false
				if e.err != nil {
					a.notice = "Snapshot save failed: " + e.err.Error()
				} else {
					a.notice = "Snapshot saved"
				}
			case "compare":
				a.historyBusy = false
				if e.snapshot == a.snapshot {
					if e.err != nil {
						a.notice = e.err.Error()
					} else {
						a.changes = e.changes
						a.comparisonLabel = e.path
					}
				}
			case "restore-error":
				a.historyBusy = false
				a.notice = "Snapshot restore failed: " + e.err.Error()
			case "analysis":
				if e.generation == a.analysisGeneration && e.snapshot == a.snapshot {
					a.analysisBusy = false
					if e.err != nil {
						a.notice = e.err.Error()
					} else {
						a.analysis = e.analysis
						a.filterTypes()
					}
				}
			case "scan", "restore":
				if e.kind == "restore" {
					a.historyOpen = false
				}
				a.historyBusy = false
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
				a.open = make(map[index.ID]bool)
				a.snapshot = e.snapshot
				if e.kind == "scan" {
					a.drives = e.drives
				}
				a.changes = nil
				a.comparisonLabel = ""
				if a.duplicateCancel != nil {
					a.duplicateCancel()
				}
				a.duplicateGeneration++
				a.duplicateBusy = false
				a.duplicateSource = nil
				a.duplicateResult = index.DuplicateResult{}
				a.summary = e.summary
				a.path = e.snapshot.Root
				a.address = a.path
				a.rememberLocation(a.path)
				a.selected = 0
				a.focus = 0
				a.open[0] = true
				a.history = []index.ID{0}
				a.historyPos = 0
				a.requestRows()
				a.requestMap(0)
				a.mapHistory = []index.ID{0}
				a.mapHistoryPos = 0
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
						if a.selected != a.listSelection {
							a.list = ui.ListState{Columns: a.list.Columns}
							a.listSelection = a.selected
						}
						a.rows = e.query.Rows
						a.matches = e.query.Matches
						a.selectedRow = e.selectedIndex
						if e.selected != a.selected {
							a.selectedRow = -1
						}
						if a.selectedRow >= 0 {
							a.list.ScrollIntoView(a.selectedRow)
						}
					}
				}
			case "map":
				if e.generation == a.mapGeneration {
					a.mapBusy = false
					a.branches = e.branches
					a.mapTotal = 0
					for _, branch := range e.branches {
						a.mapTotal += branch.Item.Bytes
					}
					a.sectors = treemap.Sunburst(e.branches, 4)
					a.mapTypes = e.mapTypes
					a.sunPaths = nil
					a.mapWidth = 0
				}
			case "export":
				a.exporting = false
				if e.err != nil {
					a.notice = "Export failed: " + e.err.Error()
				} else if e.path != "" {
					a.notice = "Export complete: " + e.path
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
	root := a.focus
	if (a.query != "" || a.tab == 1) && a.searchScope == "Scan" {
		root = 0
	}
	q := index.Query{Text: a.query, Mode: mode, Root: root, Open: open, Sort: index.Sort{Column: a.sort.Column, Desc: a.sort.Descending, Allocated: a.metric == 1}}
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
			a.post(event{kind: "query", generation: generation, query: result, err: err, selectedIndex: selectedIndex, selected: selected})
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
	a.mapBusy = true
	if a.mapCancel != nil {
		a.mapCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mapCancel = cancel
	a.mapGeneration++
	generation := a.mapGeneration
	s, allocated, showMetadata := a.snapshot, a.metric == 1, a.showMetadata
	go func() {
		branches := treemap.HierarchyFiltered(ctx, s, id, allocated, 4096, showMetadata)
		analysis, err := s.Analyze(ctx, id, allocated, 0, showMetadata)
		if ctx.Err() == nil {
			a.post(event{kind: "map", generation: generation, branches: branches, mapTypes: analysis.Types, err: err})
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
func (a *App) navigate(id index.ID) {
	a.navigateTo(id, true)
}
func (a *App) navigateTo(id index.ID, record bool) {
	if a.snapshot == nil || id == index.None || !a.snapshot.Nodes[id].IsDir() {
		return
	}
	a.focus = id
	a.address = a.snapshot.Path(id)
	a.selected = id
	a.selectedRow = -1
	a.open[id] = true
	a.query = ""
	a.tab = 0
	if record {
		// Truncate any forward history and record the new location.
		if a.historyPos < len(a.history)-1 {
			a.history = a.history[:a.historyPos+1]
		}
		if len(a.history) == 0 || a.history[len(a.history)-1] != id {
			a.history = append(a.history, id)
			a.historyPos = len(a.history) - 1
		}
	}
	a.requestRows()
	a.focusMap(id)
}

func (a *App) goBack() {
	if a.historyPos > 0 {
		a.historyPos--
		a.navigateTo(a.history[a.historyPos], false)
	}
}

func (a *App) goForward() {
	if a.historyPos < len(a.history)-1 {
		a.historyPos++
		a.navigateTo(a.history[a.historyPos], false)
	}
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
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
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
