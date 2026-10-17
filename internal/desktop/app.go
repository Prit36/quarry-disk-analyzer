// Package desktop is Quarry's native Windows interface: a menu bar, a
// compact toolbar, one data workspace (Tree, Files, Extensions), an optional
// treemap below it and a status bar. Commands are defined once (commands.go)
// and drive the menus, context menus and keyboard shortcuts alike.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
	"quarry/internal/scan"
)

const (
	viewTree = iota
	viewFiles
	viewExtensions
)

type App struct {
	win    *mygo.Window
	closed bool
	queue  chan func() // results for a windowless view (tests)
	prefs  prefs
	cmds   map[string]*command
	menu   *menuBar

	admin  bool
	drives []platform.Drive

	// Scanning.
	target     string // the scan target box
	scanning   bool
	progress   *scan.Progress
	scanStart  time.Time
	cancelScan context.CancelFunc
	scanGen    uint64

	// Data. base is the authoritative index (updated by file operations);
	// snap is what the views show: base, or a filtered copy with the same IDs.
	base       *index.Snapshot
	snap       *index.Snapshot
	source     string // snapshot file the data came from, "" for a live scan
	stale      string // why on-disk state may differ from the index
	filter     analysisFilter
	filterBusy bool
	filterGen  uint64

	// Navigation: one history shared by the tree and the treemap.
	location index.ID
	history  []index.ID
	histPos  int

	tree  treeModel
	files fileModel
	ext   extModel
	tm    treemapModel

	search       string
	searchHandle ui.Handle
	targetHandle ui.Handle
	focusTable   bool
	wantSearch   bool
	extFilter    string
	focused      map[string]bool

	message   string // transient status-bar message
	messageAt time.Time
	errorMsg  bool

	dlg dialogs
}

// isScanFile reports a saved scan: .quarry, or .stratum from releases made
// under the previous name (same format).
func isScanFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".quarry" || ext == ".stratum"
}

// Run opens the main window. initial is a folder to scan or a .quarry file.
func Run(initial string) error {
	a := newApp()
	mygo.App.WhenReady(func() {
		a.setTheme(a.prefs.Theme) // also themes the native title bar and menus
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Quarry", StateKey: "main", Width: 1280, Height: 800, UseContentSize: true,
			MinWidth: 760, MinHeight: 480, Content: ui.View(a.view),
		})
		a.menu = a.buildMenu()
		a.win.SetMenu(a.menu.menu)
		a.win.Show()
		a.win.OnClosed(a.shutdown)
		switch {
		case isScanFile(initial):
			a.openSnapshot(initial)
		case initial != "":
			a.target = initial
			a.startScan()
		}
	})
	return mygo.App.Run()
}

func newApp() *App {
	a := &App{prefs: loadPrefs(), admin: platform.Elevated(), drives: platform.Drives(), focused: map[string]bool{}, queue: make(chan func(), 64)}
	a.target = a.prefs.Target
	if a.target == "" && len(a.drives) > 0 {
		a.target = a.drives[0].Path
	}
	a.cmds = newCommands()
	a.dlg.explorerVerb = platform.ExplorerVerbInstalled()
	a.dlg.driveRow = -1
	if len(a.drives) > 0 {
		a.dlg.driveRow = 0
	}
	a.tree.init(a)
	a.files.init(a)
	a.ext.init(a)
	a.tm.a = a
	a.tm.reset()
	return a
}

func (a *App) shutdown() {
	a.closed = true
	a.prefs.Target = a.target
	_ = a.savePrefs()
	for _, cancel := range []context.CancelFunc{a.cancelScan, a.files.cancel, a.ext.cancel, a.tm.cancel, a.dlg.dup.cancel} {
		if cancel != nil {
			cancel()
		}
	}
}

// post runs fn on the UI thread and redraws. Safe from any goroutine.
func (a *App) post(fn func()) {
	if a.win == nil {
		a.queue <- fn // drained by the next frame of a windowless view
		return
	}
	a.win.Update(func() {
		if !a.closed {
			fn()
		}
	})
}

// drain runs queued results; only windowless views (tests) queue them.
func (a *App) drain() bool {
	ran := false
	for {
		select {
		case fn := <-a.queue:
			fn()
			ran = true
		default:
			return ran
		}
	}
}

func (a *App) notify(format string, args ...any) {
	a.message, a.messageAt, a.errorMsg = fmt.Sprintf(format, args...), time.Now(), false
}

func (a *App) fail(format string, args ...any) {
	a.message, a.messageAt, a.errorMsg = fmt.Sprintf(format, args...), time.Now(), true
}

func (a *App) metricAllocated() bool { return a.prefs.Metric == "Allocated" }

func (a *App) metric(n index.Node) uint64 {
	if a.metricAllocated() {
		return n.Allocated
	}
	return n.Size
}

// Scanning.

func (a *App) startScan() {
	if a.scanning {
		return
	}
	target := strings.Trim(strings.TrimSpace(a.target), `"`)
	if target == "" {
		a.fail("Choose a drive or folder to scan.")
		return
	}
	if len(target) == 2 && target[1] == ':' {
		target += `\`
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		a.fail("Cannot scan %s: the folder does not exist or is not accessible.", target)
		return
	}
	a.target = target
	a.scanGen++
	gen := a.scanGen
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelScan = cancel
	a.scanning, a.progress, a.scanStart = true, &scan.Progress{}, time.Now()
	a.message = ""
	opts := scan.Options{ForceWalk: a.prefs.ScanMode == "Enumerate", AllowFallback: true}
	if a.prefs.UseExclusions {
		opts.Exclude = append([]string(nil), a.prefs.Exclusions...)
	}
	progress := a.progress
	go func() {
		s, err := scan.Run(ctx, target, opts, progress)
		runtime.GC() // release scan buffers off the UI thread
		drives := platform.Drives()
		a.post(func() {
			if gen != a.scanGen {
				return
			}
			a.scanning, a.cancelScan = false, nil
			a.drives = drives
			if a.win != nil {
				a.win.SetProgressBar(mygo.ProgressBar{})
			}
			if err != nil {
				if errors.Is(err, context.Canceled) {
					a.notify("Scan stopped. The previous results are unchanged.")
				} else {
					a.fail("Scan failed: %v", err)
				}
				return
			}
			a.prefs.Recent = remember(a.prefs.Recent, s.Root, 12)
			a.menu.recentChanged = true
			a.setBase(s, "")
			switch {
			case s.Cancelled:
				a.notify("Scan stopped after %s. Results are partial.", duration(s.Elapsed))
			default:
				a.notify("Scan completed in %s.", duration(s.Elapsed))
			}
		})
	}()
}

func (a *App) stopScan() {
	if a.cancelScan != nil {
		a.cancelScan()
	}
}

func (a *App) rescan() {
	if a.base != nil && a.source == "" {
		a.target = a.base.Root
	}
	a.startScan()
}

func (a *App) scanPath(path string) {
	if a.scanning {
		return
	}
	a.target = path
	a.startScan()
}

func (a *App) chooseFolder() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Choose a folder to analyze", Directory: true, DefaultPath: a.target})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.scanPath(paths[0]) })
	}()
}

func (a *App) newWindow(path string) {
	exe, err := os.Executable()
	if err != nil {
		a.fail("Cannot start a new window: %v", err)
		return
	}
	args := []string{}
	if path != "" {
		args = append(args, path)
	}
	if err := exec.Command(exe, args...).Start(); err != nil {
		a.fail("Cannot start a new window: %v", err)
	}
}

func (a *App) restartElevated() {
	path := a.target
	if a.base != nil && a.source == "" {
		path = a.base.Root
	}
	go func() {
		if err := platform.RelaunchElevated(path); err != nil {
			a.post(func() { a.fail("Administrator restart was canceled or failed: %v", err) })
			return
		}
		a.post(func() { a.win.Close() })
	}()
}

// Data ownership.

// setBase installs a new scan or loaded snapshot and resets views.
func (a *App) setBase(s *index.Snapshot, source string) {
	a.base, a.source, a.stale = s, source, ""
	a.target = s.Root
	a.location, a.history, a.histPos = 0, []index.ID{0}, 0
	a.tree.reset()
	a.files.reset()
	a.ext.reset()
	a.tm.reset()
	a.dlg.dup.reset()
	title := s.Root
	if source != "" {
		title = filepath.Base(source) + " (saved scan)"
	}
	if a.win != nil {
		a.win.SetTitle(title + " - Quarry")
	}
	a.applyFilter()
}

// replaceBase installs a changed copy of the current scan (after a delete or
// rename), keeping the location, expansion and selections that still exist.
func (a *App) replaceBase(s *index.Snapshot) {
	a.base = s
	for !s.Reachable(a.location) && a.location != 0 {
		a.location = s.Nodes[a.location].Parent
	}
	a.applyFilter()
}

// applyFilter derives the view snapshot from base and the analysis filter.
func (a *App) applyFilter() {
	a.filterGen++
	if a.base == nil {
		return
	}
	if !a.filter.active() {
		a.filter.stats = index.FilterStats{}
		a.filterBusy = false
		a.setView(a.base)
		return
	}
	gen, base, f := a.filterGen, a.base, a.filter
	a.filterBusy = true
	go func() {
		view, stats, err := f.apply(context.Background(), base)
		a.post(func() {
			if gen != a.filterGen {
				return
			}
			a.filterBusy = false
			if err != nil {
				a.fail("Filter: %v", err)
				a.filter = analysisFilter{}
				a.setView(a.base)
				return
			}
			a.filter.stats = stats
			a.setView(view)
		})
	}()
}

// setView installs the snapshot the views read and refreshes them.
func (a *App) setView(s *index.Snapshot) {
	a.snap = s
	if !s.Reachable(a.location) {
		a.location = 0
	}
	a.tree.dataChanged()
	if a.tree.sel.Len() == 0 {
		a.tree.selectOnly(a.location)
		a.focusTable = true
	}
	a.files.invalidate()
	a.ext.invalidate()
	a.tm.invalidate()
}

// mapSnapshot is what the treemap draws.
func (a *App) mapSnapshot() *index.Snapshot {
	if a.filter.active() && !a.prefs.FilterMap {
		return a.base
	}
	return a.snap
}

// Navigation.

func (a *App) navigate(id index.ID) { a.navigateTo(id, true) }

func (a *App) navigateTo(id index.ID, record bool) {
	if a.snap == nil || id == index.None || !a.snap.Reachable(id) {
		return
	}
	if !a.snap.Nodes[id].IsDir() {
		id = a.snap.Nodes[id].Parent
	}
	if id == a.location {
		return
	}
	previous := a.location
	a.location = id
	if record {
		a.history = append(a.history[:a.histPos+1], id)
		a.histPos = len(a.history) - 1
	}
	a.tree.locationChanged(previous)
	a.tm.invalidate()
}

func (a *App) canBack() bool    { return a.snap != nil && a.histPos > 0 }
func (a *App) canForward() bool { return a.snap != nil && a.histPos < len(a.history)-1 }
func (a *App) canUp() bool      { return a.snap != nil && a.location != 0 }

func (a *App) back() {
	for a.histPos > 0 {
		a.histPos--
		if a.snap.Reachable(a.history[a.histPos]) {
			a.navigateTo(a.history[a.histPos], false)
			return
		}
	}
}

func (a *App) forward() {
	for a.histPos < len(a.history)-1 {
		a.histPos++
		if a.snap.Reachable(a.history[a.histPos]) {
			a.navigateTo(a.history[a.histPos], false)
			return
		}
	}
}

func (a *App) up() {
	if a.canUp() {
		child := a.location
		a.navigate(a.snap.Nodes[a.location].Parent)
		a.tree.selectOnly(child)
	}
}

// reveal shows an entry in Tree View: expands its ancestors, selects it and
// scrolls it into view. It widens the location when the entry lies outside.
func (a *App) reveal(id index.ID) {
	if a.snap == nil || !a.snap.Reachable(id) {
		a.fail("This entry is hidden by the current filter or no longer exists.")
		return
	}
	if !a.snap.Contains(a.location, id) {
		a.navigate(0)
	}
	a.setViewMode(viewTree)
	a.tree.reveal(id)
}

func (a *App) setViewMode(v int) {
	if a.prefs.View == v {
		return
	}
	a.prefs.View = v
	a.focusTable = true
	if v == viewFiles {
		a.files.ensure()
	}
	if v == viewExtensions {
		a.ext.ensure()
	}
}

// pathOf returns a filesystem path for an entry, or "" for entries that have
// no reliable path (unresolved MFT records).
func (a *App) pathOf(id index.ID) string {
	if a.snap == nil || id == index.None || int(id) >= len(a.snap.Nodes) {
		return ""
	}
	for n := id; n != index.None; n = a.snap.Nodes[n].Parent {
		if a.snap.Nodes[n].Flags&index.Unplaced != 0 {
			return ""
		}
	}
	return a.snap.Path(id)
}

func (a *App) name(id index.ID) string {
	if id == 0 {
		return a.snap.Root
	}
	return a.snap.NameView(id)
}
