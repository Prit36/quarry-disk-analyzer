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
	"runtime/debug"
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

	admin          bool
	drives         []platform.Drive
	drivesReady    bool
	startupStarted bool
	shellIcons     map[string]*ui.Bitmap

	// Scanning.
	target     string // the folder the last scan was started on
	scanning   bool
	progress   *scan.Progress
	scanStart  time.Time
	cancelScan context.CancelFunc
	scanGen    uint64

	// Data. base is the authoritative index (updated by file operations);
	// snap is what the views show: base, or a filtered copy with the same IDs.
	base   *index.Snapshot
	snap   *index.Snapshot
	source string // snapshot file the data came from, "" for a live scan
	stale  string // why on-disk state may differ from the index
	// meta lists base's top-level NTFS metadata entries. While metaHidden,
	// they are detached from base itself (index.Remove), so hiding them
	// costs no second copy of the index; Restore brings them back.
	meta         []index.ID
	metaHidden   bool
	filter       analysisFilter
	filterBusy   bool
	filterGen    uint64
	filterCancel context.CancelFunc

	// Navigation: one history shared by the tree and the treemap.
	location index.ID
	history  []index.ID
	histPos  int

	tree          treeModel
	files         fileModel
	ext           extModel
	tm            treemapModel
	typeSummaries [2]*rootTypeSummary

	search         string
	searchLeftTree bool // typing a search switched Tree View to File View
	searchHandle   ui.Handle
	targetHandle   ui.Handle // the location bar while it is a text box
	locText        string
	locEdit        bool // the location bar is a text box
	locArmed       bool // ...and has had the focus, so losing it ends editing
	wantLoc        bool // focus the location text box in the next frame
	focusTable     bool
	wantSearch     bool
	extFilter      string
	focused        map[string]bool

	hover      index.ID // entry under the pointer in a list or the treemap, this frame
	hoverOther bool     // hover is the "Smaller items" block of that folder

	message   string // transient status-bar message
	messageAt time.Time
	errorMsg  bool

	trimPending bool // a scan's transient memory is to be released after display
	trimAt      time.Time

	selFacts selectionFacts // cached facts about the active selection
	mutMemo  mutableMemo    // cached answer of mutable for the active selection

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
	// MyGo uses the GPU by default, with MYGO_GPU=0 as a software fallback.
	// Most of the heap is the scan index: large arrays without pointers,
	// which a collection does not need to scan. Collecting when the heap
	// has grown by a quarter instead of doubling costs little CPU and keeps
	// the working set near the live data instead of up to twice it.
	debug.SetGCPercent(25)
	a := newAppState()
	if initial != "" && !isScanFile(initial) {
		a.target = initial
	}
	mygo.App.WhenReady(func() {
		a.setTheme(a.prefs.Theme) // also themes the native title bar and menus
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Quarry", StateKey: "main", Width: 1280, Height: 800, UseContentSize: true,
			MinWidth: 760, MinHeight: 480, Content: ui.View(a.view),
			Hidden:          true,
			BackgroundColor: "light-dark(#ffffff, #161616)",
		})
		a.menu = a.buildMenu()
		a.win.SetMenu(a.menu.menu)
		a.win.OnClosed(a.shutdown)
		a.win.OnReadyToShow(func() {
			a.win.SetOpacity(1)
			a.win.Show()
			a.loadStartupInfo()
			if initial != "" {
				a.post(func() {
					if isScanFile(initial) {
						a.openSnapshot(initial)
					} else {
						a.startScan()
					}
				})
			}
		})
		// A hidden Windows surface does not paint. Let it draw its first
		// frame without exposing the empty client area or taking focus.
		// Opacity 1 removes this temporary layered-window style afterwards.
		a.win.SetOpacity(0)
		a.win.ShowInactive()
	})
	return mygo.App.Run()
}

func newApp() *App {
	a := newAppState()
	a.setDrives(platform.Drives())
	a.dlg.explorerVerb = platform.ExplorerVerbInstalled()
	a.shellIcons = loadShellIcons()
	return a
}

// newAppState contains only what the first frame needs. Windows shell and
// volume queries run after presentation instead of delaying the window.
func newAppState() *App {
	a := &App{prefs: loadPrefs(), admin: platform.Elevated(), focused: map[string]bool{}, queue: make(chan func(), 64)}
	a.target = a.prefs.Target
	a.cmds = newCommands()
	a.dlg.driveRow = -1
	a.tree.init(a)
	a.files.init(a)
	a.ext.init(a)
	a.tm.a = a
	a.tm.reset()
	return a
}

func (a *App) shutdown() {
	a.closed = true
	a.clearTypeSummaries()
	a.prefs.Target = a.target
	_ = a.savePrefs()
	for _, cancel := range []context.CancelFunc{a.cancelScan, a.filterCancel, a.files.cancel, a.ext.cancel, a.tm.cancel, a.dlg.dup.cancel} {
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

// clearError removes an error from the status bar once it has been seen.
func (a *App) clearError() {
	if a.errorMsg {
		a.message, a.messageAt, a.errorMsg = "", time.Time{}, false
	}
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
	progress, hide := a.progress, !a.prefs.ShowMetadata
	treeOrder := a.tree.order()
	prepareTree := !a.filter.active()
	go func() {
		// scan.Run also validates the target here. A slow or disconnected
		// path must not block the window while handling a startup argument.
		s, err := scan.Run(ctx, target, opts, progress)
		var meta []index.ID
		var tree *initialTree
		if err == nil {
			meta = hideMetadata(s, hide) // off the UI thread
			if prepareTree {
				tree = prepareInitialTree(s, treeOrder)
			}
		}
		a.post(func() {
			if gen != a.scanGen {
				return
			}
			a.scanning, a.cancelScan = false, nil
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
			a.setPreparedBase(s, "", meta, hide, tree)
			a.trimPending = true
			switch {
			case s.Cancelled:
				a.notify("Scan stopped after %s. Results are partial.", duration(s.Elapsed))
			default:
				a.notify("Scan completed in %s.", duration(s.Elapsed))
			}
		})
		// Free space may have changed; the results are shown first.
		drives := platform.Drives()
		a.post(func() { a.drives = drives })
	}()
}

// trimAfterShown leaves a short quiet interval after the ready frame before
// returning transient scan buffers to Windows. Building a view precedes its
// layout and paint: launching a forced GC there competes with the first render.
func (a *App) trimAfterShown(c *ui.Context) {
	if !a.trimPending {
		return
	}
	if a.snap == nil || a.scanning || a.filterBusy || a.tm.busy || a.files.busy || a.ext.busy {
		a.trimAt = time.Time{}
		return
	}
	if a.trimAt.IsZero() {
		a.trimAt = c.Now().Add(200 * time.Millisecond)
	}
	if delay := a.trimAt.Sub(c.Now()); delay > 0 {
		c.After(delay)
		return
	}
	a.trimPending, a.trimAt = false, time.Time{}
	go debug.FreeOSMemory()
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

// setBase installs a new scan or loaded snapshot and resets views. meta
// lists its NTFS metadata entries; hidden says they are already detached.
func (a *App) setBase(s *index.Snapshot, source string, meta []index.ID, hidden bool) {
	a.setPreparedBase(s, source, meta, hidden, nil)
}

func (a *App) setPreparedBase(s *index.Snapshot, source string, meta []index.ID, hidden bool, tree *initialTree) {
	a.clearTypeSummaries()
	a.trimAt = time.Time{}
	a.base, a.source, a.stale = s, source, ""
	a.meta, a.metaHidden = meta, hidden && len(meta) > 0
	a.target = s.Root
	a.location, a.history, a.histPos = 0, []index.ID{0}, 0
	a.tree.reset()
	a.tree.prepared = tree
	a.files.reset()
	a.ext.reset()
	a.tm.reset()
	a.dlg.dup.reset()
	a.selFacts, a.mutMemo = selectionFacts{}, mutableMemo{}
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

// applyFilter derives the view snapshot from base: NTFS metadata detached
// or attached as the option says, then the analysis filter.
func (a *App) applyFilter() {
	if a.filterCancel != nil {
		a.filterCancel()
		a.filterCancel = nil
	}
	a.filterGen++
	if a.base == nil {
		return
	}
	hide := !a.prefs.ShowMetadata
	toggle := len(a.meta) > 0 && a.metaHidden != hide
	if !toggle && !a.filter.active() {
		a.filter.stats = index.FilterStats{}
		a.filterBusy = false
		a.setView(a.base)
		return
	}
	gen, base, f, meta := a.filterGen, a.base, a.filter, a.meta
	ctx, cancel := context.WithCancel(context.Background())
	a.filterCancel = cancel
	a.filterBusy = true
	go func() {
		defer cancel()
		if ctx.Err() != nil {
			return
		}
		if toggle {
			base = base.Clone()
			if hide {
				base.Remove(meta...)
			} else {
				base.Restore(meta...)
			}
		}
		view, stats, err := base, index.FilterStats{}, error(nil)
		if f.active() {
			view, stats, err = f.apply(ctx, base)
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != a.filterGen {
				return
			}
			a.filterBusy = false
			a.filterCancel = nil
			if toggle {
				a.base, a.metaHidden = base, hide
			}
			if err != nil {
				a.fail("Filter: %v", err)
				a.filter = analysisFilter{}
				a.setView(base)
				return
			}
			a.filter.stats = stats
			a.setView(view)
		})
	}()
}

// metadataBytes is the size of the NTFS metadata hidden from the views.
// Detached entries keep their own totals.
func (a *App) metadataBytes() (uint64, bool) {
	if a.base == nil || !a.metaHidden {
		return 0, false
	}
	var total uint64
	for _, id := range a.meta {
		total += a.metric(a.base.Nodes[id])
	}
	return total, true
}

// scanTotals is the scan root's totals including any hidden metadata.
func (a *App) scanTotals() index.Node {
	root := a.base.Nodes[0]
	if a.metaHidden {
		for _, id := range a.meta {
			n := a.base.Nodes[id]
			root.Size += n.Size
			root.Allocated += n.Allocated
			if n.IsDir() {
				root.Files += n.Files
				root.Dirs += n.Dirs + 1
			} else {
				root.Files++
			}
		}
	}
	return root
}

// completeScan returns a function giving the whole scan, NTFS metadata
// included, for saving, exporting and comparing. It may copy the index,
// so it is called off the UI thread.
func (a *App) completeScan() func() *index.Snapshot {
	base, meta, hidden := a.base, a.meta, a.metaHidden
	return func() *index.Snapshot {
		if !hidden {
			return base
		}
		c := base.Clone()
		c.Restore(meta...)
		return c
	}
}

// setView installs the snapshot the views read and refreshes them.
func (a *App) setView(s *index.Snapshot) {
	if a.snap != s {
		a.snap = s
		// Hidden views are refreshed lazily. Their cache identity must not
		// keep a former copy-on-write node table alive until they are shown.
		// IDs remain stable across these changes, so retain rows/selections
		// and cancel/reject only obsolete derived results.
		a.tree.selCache.reset()
		a.tree.cells = cellTextCache{}
		a.files.selCache.reset()
		if a.files.cancel != nil {
			a.files.cancel()
		}
		a.files.gen++
		a.files.cancel, a.files.forSnap, a.files.runFor = nil, nil, nil
		a.files.shownGroups = nil
		a.files.busy = false
		if a.ext.cancel != nil {
			a.ext.cancel()
		}
		a.ext.gen++
		a.ext.cancel, a.ext.forSnap, a.ext.runFor = nil, nil, nil
		a.ext.busy = false
		a.selFacts, a.mutMemo = selectionFacts{}, mutableMemo{}
		if a.tm.forSnap != a.mapSnapshot() {
			a.tm.reset()
		}
		a.dlg.dup.reset()
	}
	a.pruneTypeSummaries()
	if !s.Reachable(a.location) {
		a.location = 0
	}
	if !a.files.pinned {
		a.files.scope = a.location
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
	a.files.follow(id)
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
	a.searchLeftTree = false // a view chosen by the user stays when the search is cleared
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

// shortName is a name for running text: the scan root's last folder (or
// its drive) instead of its full path.
func (a *App) shortName(id index.ID) string {
	if id == 0 {
		if base := filepath.Base(a.snap.Root); base != "" && base != `\` && base != "." && len(a.snap.Root) > 3 {
			return base
		}
	}
	return a.name(id)
}

func (a *App) name(id index.ID) string {
	if id == 0 {
		return a.snap.Root
	}
	return a.snap.NameView(id)
}
