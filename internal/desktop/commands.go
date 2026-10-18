package desktop

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// command is one user action. The same definition drives the menu bar, the
// context menus and the keyboard shortcut, so their behaviour cannot drift.
type command struct {
	label   string
	keys    string // shortcut as displayed, e.g. "Ctrl+Alt+C"
	mods    ui.Modifiers
	key     ui.Key
	inText  bool              // the shortcut also works while a text field has focus
	visible func(a *App) bool // false hides it from context menus (invalid for the selection)
	enabled func(a *App) bool
	checked func(a *App) bool // non-nil makes it a check or radio item
	radio   bool
	run     func(a *App)
}

func always(*App) bool { return true }

func hasScan(a *App) bool     { return a.snap != nil }
func idle(a *App) bool        { return !a.scanning }
func hasItems(a *App) bool {
	if a.snap == nil {
		return false
	}
	switch a.prefs.View {
	case viewTree:
		return a.tree.sel.Len() > 0
	case viewFiles:
		return a.files.sel.Len() > 0
	}
	return false
}

func singleItem(a *App) bool {
	if a.snap == nil {
		return false
	}
	switch a.prefs.View {
	case viewTree:
		return a.tree.sel.Len() == 1
	case viewFiles:
		return a.files.sel.Len() == 1
	}
	return false
}
func inTree(a *App) bool      { return a.prefs.View == viewTree && a.snap != nil }
func liveScan(a *App) bool    { return a.snap != nil && a.source == "" }
func itemsOnDisk(a *App) bool { return hasItems(a) && liveScan(a) && a.allPaths() }

func newCommands() map[string]*command {
	return map[string]*command{
		// File
		"scan.folder":      {label: "Scan Folder…", keys: "Ctrl+O", mods: ui.Ctrl, key: ui.KeyO, inText: true, enabled: idle, run: (*App).chooseFolder},
		"scan.rescan":      {label: "Rescan", keys: "F5", key: ui.KeyF5, inText: true, enabled: func(a *App) bool { return idle(a) && (a.base != nil || a.target != "") }, run: (*App).rescan},
		"scan.stop":        {label: "Stop Scan", keys: "Esc", enabled: func(a *App) bool { return a.scanning }, run: (*App).stopScan},
		"file.newWindow":   {label: "New Window", keys: "Ctrl+N", mods: ui.Ctrl, key: ui.KeyN, inText: true, run: func(a *App) { a.newWindow("") }},
		"file.openScan":    {label: "Open Saved Scan…", enabled: idle, run: (*App).openSnapshotDialog},
		"file.saveScan":    {label: "Save Scan As…", keys: "Ctrl+S", mods: ui.Ctrl, key: ui.KeyS, inText: true, enabled: hasScan, run: (*App).saveSnapshotDialog},
		"item.open":        {label: "Open", keys: "Enter", enabled: itemsOnDisk, run: (*App).openItems},
		"item.explorer":    {label: "Show in Explorer", keys: "Ctrl+Shift+E", mods: ui.Ctrl | ui.Shift, key: ui.KeyE, enabled: itemsOnDisk, run: (*App).showInExplorer},
		"item.properties":  {label: "Properties", keys: "Alt+Enter", mods: ui.Alt, key: ui.KeyEnter, enabled: itemsOnDisk, run: (*App).properties},
		"export.view":      {label: "Export Current View…", keys: "Ctrl+E", mods: ui.Ctrl, key: ui.KeyE, inText: true, enabled: hasScan, run: (*App).exportView},
		"export.selection": {label: "Export Selection…", enabled: hasItems, run: (*App).exportSelection},
		"export.scan":      {label: "Export Entire Scan…", enabled: hasScan, run: (*App).exportScan},
		"export.ext":       {label: "Export Extension Summary…", enabled: hasScan, run: (*App).exportExtensions},
		"export.json":      {label: "Export Scan as JSON…", enabled: hasScan, run: (*App).exportJSON},
		"file.exit":        {label: "Exit", keys: "Alt+F4", run: func(a *App) { a.win.Close() }},

		// Edit
		"edit.copy":       {label: "Copy", keys: "Ctrl+C", mods: ui.Ctrl, key: ui.KeyC, enabled: itemsOnDisk, run: func(a *App) { a.clipboardFiles(false) }},
		"edit.cut":        {label: "Cut", keys: "Ctrl+X", mods: ui.Ctrl, key: ui.KeyX, enabled: func(a *App) bool { return itemsOnDisk(a) && a.mutableCached() }, run: func(a *App) { a.clipboardFiles(true) }},
		"edit.copyPath":   {label: "Copy Full Path", keys: "Ctrl+Alt+C", mods: ui.Ctrl | ui.Alt, key: ui.KeyC, enabled: hasItems, run: (*App).copyPaths},
		"edit.copyName":   {label: "Copy Name", enabled: hasItems, run: (*App).copyNames},
		"edit.copySize":   {label: "Copy Size", enabled: hasItems, run: (*App).copySizes},
		"edit.copyRows":   {label: "Copy Rows as Text", keys: "Ctrl+Shift+C", mods: ui.Ctrl | ui.Shift, key: ui.KeyC, enabled: hasItems, run: (*App).copyRows},
		"edit.selectAll":  {label: "Select All", keys: "Ctrl+A", mods: ui.Ctrl, key: ui.KeyA, enabled: hasScan, run: (*App).selectAll},
		"edit.find":       {label: "Find", keys: "Ctrl+F", mods: ui.Ctrl, key: ui.KeyF, inText: true, run: (*App).focusSearch},
		"edit.rename":     {label: "Rename…", keys: "F2", key: ui.KeyF2, visible: func(a *App) bool { return a.mutableCached() }, enabled: func(a *App) bool { return singleItem(a) && itemsOnDisk(a) && a.mutableCached() }, run: (*App).beginRename},
		"edit.delete":     {label: "Delete", keys: "Del", key: ui.KeyDelete, visible: func(a *App) bool { return a.mutableCached() }, enabled: func(a *App) bool { return itemsOnDisk(a) && a.mutableCached() }, run: func(a *App) { a.deleteItems(false) }},
		"edit.deletePerm": {label: "Delete Permanently…", keys: "Shift+Del", mods: ui.Shift, key: ui.KeyDelete, visible: func(a *App) bool { return a.mutableCached() }, enabled: func(a *App) bool { return itemsOnDisk(a) && a.mutableCached() }, run: func(a *App) { a.deleteItems(true) }},

		// Item navigation (context menus)
		"item.reveal":      {label: "Reveal in Tree", keys: "Ctrl+Shift+T", mods: ui.Ctrl | ui.Shift, key: ui.KeyT, visible: func(a *App) bool { return a.prefs.View != viewTree }, enabled: singleItem, run: func(a *App) { a.reveal(a.current()) }},
		"item.treemap":     {label: "Focus in Treemap", enabled: singleItem, run: (*App).focusInTreemap},
		"item.goInto":      {label: "Go Into Folder", keys: "Ctrl+Enter", mods: ui.Ctrl, key: ui.KeyEnter, visible: func(a *App) bool { return singleItem(a) && a.currentIsDir() }, enabled: singleItem, run: func(a *App) { a.navigate(a.current()) }},
		"item.searchHere":  {label: "Search in This Folder", visible: func(a *App) bool { return singleItem(a) && a.currentIsDir() }, enabled: singleItem, run: (*App).searchHere},
		"item.scanHere":    {label: "Scan This Folder", visible: func(a *App) bool { return singleItem(a) && a.currentIsDir() && a.current() != 0 }, enabled: func(a *App) bool { return idle(a) && itemsOnDisk(a) }, run: func(a *App) { a.scanPath(a.pathOf(a.current())) }},
		"item.scanWindow":  {label: "Scan This Folder in New Window", visible: func(a *App) bool { return singleItem(a) && a.currentIsDir() && a.current() != 0 }, enabled: itemsOnDisk, run: func(a *App) { a.newWindow(a.pathOf(a.current())) }},
		"tree.expandAll":   {label: "Expand Subtree", keys: "Shift+Right", visible: func(a *App) bool { return a.prefs.View == viewTree && a.currentIsDir() }, enabled: inTree, run: func(a *App) { a.tree.expandSubtree(a.current()) }},
		"tree.collapseSub": {label: "Collapse Subtree", keys: "Shift+Left", visible: func(a *App) bool { return a.prefs.View == viewTree && a.currentIsDir() }, enabled: inTree, run: func(a *App) { a.tree.collapseSubtree(a.current()) }},

		// View
		"view.tree":        {label: "Tree View", keys: "Ctrl+1", mods: ui.Ctrl, key: ui.Key1, inText: true, checked: func(a *App) bool { return a.prefs.View == viewTree }, radio: true, run: func(a *App) { a.setViewMode(viewTree) }},
		"view.files":       {label: "File View", keys: "Ctrl+2", mods: ui.Ctrl, key: ui.Key2, inText: true, checked: func(a *App) bool { return a.prefs.View == viewFiles }, radio: true, run: func(a *App) { a.setViewMode(viewFiles) }},
		"view.ext":         {label: "Extensions", keys: "Ctrl+3", mods: ui.Ctrl, key: ui.Key3, inText: true, checked: func(a *App) bool { return a.prefs.View == viewExtensions }, radio: true, run: func(a *App) { a.setViewMode(viewExtensions) }},
		"nav.back":         {label: "Back", keys: "Alt+Left", mods: ui.Alt, key: ui.KeyLeft, inText: true, enabled: (*App).canBack, run: (*App).back},
		"nav.forward":      {label: "Forward", keys: "Alt+Right", mods: ui.Alt, key: ui.KeyRight, inText: true, enabled: (*App).canForward, run: (*App).forward},
		"nav.up":           {label: "Up One Level", keys: "Alt+Up", mods: ui.Alt, key: ui.KeyUp, inText: true, enabled: (*App).canUp, run: (*App).up},
		"nav.location":     {label: "Edit Location", keys: "Ctrl+L", mods: ui.Ctrl, key: ui.KeyL, inText: true, run: (*App).editLocation},
		"nav.root":         {label: "Go to Scan Root", keys: "Alt+Home", mods: ui.Alt, key: ui.KeyHome, inText: true, enabled: (*App).canUp, run: func(a *App) { a.navigate(0) }},
		"tree.expandLevel": {label: "Expand One Level", keys: "Ctrl+Shift+Right", mods: ui.Ctrl | ui.Shift, key: ui.KeyRight, enabled: inTree, run: func(a *App) { a.tree.expandLevel() }},
		"tree.collapseLvl": {label: "Collapse One Level", keys: "Ctrl+Shift+Left", mods: ui.Ctrl | ui.Shift, key: ui.KeyLeft, enabled: inTree, run: func(a *App) { a.tree.collapseLevel() }},
		"tree.collapseAll": {label: "Collapse All", keys: "Ctrl+Shift+Up", mods: ui.Ctrl | ui.Shift, key: ui.KeyUp, enabled: inTree, run: func(a *App) { a.tree.collapseAll() }},
		"view.treemap":     {label: "Treemap", keys: "Ctrl+T", mods: ui.Ctrl, key: ui.KeyT, inText: true, checked: func(a *App) bool { return a.prefs.ShowTreemap }, run: func(a *App) { a.prefs.ShowTreemap = !a.prefs.ShowTreemap; a.tm.maximized = false }},
		"view.maxTreemap":  {label: "Maximize Treemap", keys: "Ctrl+Shift+M", mods: ui.Ctrl | ui.Shift, key: ui.KeyM, inText: true, checked: func(a *App) bool { return a.tm.maximized }, enabled: hasScan, run: func(a *App) { a.tm.maximized = !a.tm.maximized; a.prefs.ShowTreemap = true }},
		"view.resetLayout": {label: "Restore Default Layout", run: (*App).resetLayout},
		"metric.logical":   {label: "Logical Size", checked: func(a *App) bool { return !a.metricAllocated() }, radio: true, run: func(a *App) { a.setMetric("Logical") }},
		"metric.allocated": {label: "Allocated Size", checked: func(a *App) bool { return a.metricAllocated() }, radio: true, run: func(a *App) { a.setMetric("Allocated") }},
		"color.ext":        {label: "By Extension", checked: func(a *App) bool { return a.prefs.TreemapColors == "Extension" }, radio: true, run: func(a *App) { a.prefs.TreemapColors = "Extension" }},
		"color.folder":     {label: "By Top-Level Folder", checked: func(a *App) bool { return a.prefs.TreemapColors == "Folder" }, radio: true, run: func(a *App) { a.prefs.TreemapColors = "Folder" }},
		"color.mono":       {label: "Monochrome", checked: func(a *App) bool { return a.prefs.TreemapColors == "Mono" }, radio: true, run: func(a *App) { a.prefs.TreemapColors = "Mono" }},
		"units.binary":     {label: "Binary (KiB, MiB, GiB)", checked: func(a *App) bool { return a.prefs.Units != "Decimal" }, radio: true, run: func(a *App) { a.prefs.Units = "Binary" }},
		"units.decimal":    {label: "Decimal (KB, MB, GB)", checked: func(a *App) bool { return a.prefs.Units == "Decimal" }, radio: true, run: func(a *App) { a.prefs.Units = "Decimal" }},
		"zoom.in":          {label: "Larger Text", keys: "Ctrl+=", mods: ui.Ctrl, key: ui.KeyEqual, inText: true, enabled: func(a *App) bool { return a.zoom() < 2 }, run: func(a *App) { a.setZoom(a.zoom() + 0.1) }},
		"zoom.out":         {label: "Smaller Text", keys: "Ctrl+-", mods: ui.Ctrl, key: ui.KeyMinus, inText: true, enabled: func(a *App) bool { return a.zoom() > 0.75 }, run: func(a *App) { a.setZoom(a.zoom() - 0.1) }},
		"zoom.reset":       {label: "Default Text Size", keys: "Ctrl+0", mods: ui.Ctrl, key: ui.Key0, inText: true, run: func(a *App) { a.setZoom(1) }},
		"view.compact":     {label: "Compact Rows", checked: func(a *App) bool { return a.prefs.Compact }, run: func(a *App) { a.prefs.Compact = !a.prefs.Compact }},
		"theme.system":     {label: "Use Windows Setting", checked: func(a *App) bool { return a.prefs.Theme == "System" }, radio: true, run: func(a *App) { a.setTheme("System") }},
		"theme.dark":       {label: "Dark", checked: func(a *App) bool { return a.prefs.Theme == "Dark" }, radio: true, run: func(a *App) { a.setTheme("Dark") }},
		"theme.light":      {label: "Light", checked: func(a *App) bool { return a.prefs.Theme == "Light" }, radio: true, run: func(a *App) { a.setTheme("Light") }},

		// Tools
		"tools.search":      {label: "Advanced Search…", keys: "Ctrl+Shift+F", mods: ui.Ctrl | ui.Shift, key: ui.KeyF, inText: true, enabled: hasScan, run: func(a *App) { a.dlg.openSearch(a) }},
		"tools.filter":      {label: "Analysis Filter…", keys: "Ctrl+Shift+L", mods: ui.Ctrl | ui.Shift, key: ui.KeyL, inText: true, enabled: hasScan, run: func(a *App) { a.dlg.openFilter(a) }},
		"tools.clearFilter": {label: "Clear Filter", enabled: func(a *App) bool { return a.filter.active() }, run: (*App).clearFilter},
		"tools.dups":        {label: "Find Duplicates…", enabled: hasScan, run: func(a *App) { a.dlg.dup.open(a) }},
		"tools.compare":     {label: "Compare with Saved Scan…", enabled: hasScan, run: (*App).compareDialog},
		"tools.issues":      {label: "Scan Issues…", enabled: func(a *App) bool { return a.snap != nil && a.snap.Errors > 0 }, run: func(a *App) { a.dlg.issues = true }},
		"tools.info":        {label: "Scan Information…", enabled: hasScan, run: func(a *App) { a.dlg.info = true }},

		// Options
		"opt.exclusions": {label: "Scan Exclusions…", run: func(a *App) { a.dlg.openExclusions(a) }},
		"opt.useExcl":    {label: "Apply Scan Exclusions", checked: func(a *App) bool { return a.prefs.UseExclusions }, run: func(a *App) { a.prefs.UseExclusions = !a.prefs.UseExclusions }},
		"opt.scanAuto":   {label: "Automatic (direct MFT read when permitted)", checked: func(a *App) bool { return a.prefs.ScanMode != "Enumerate" }, radio: true, run: func(a *App) { a.prefs.ScanMode = "Automatic" }},
		"opt.scanEnum":   {label: "Directory Enumeration Only", checked: func(a *App) bool { return a.prefs.ScanMode == "Enumerate" }, radio: true, run: func(a *App) { a.prefs.ScanMode = "Enumerate" }},
		"opt.admin":      {label: "Restart as Administrator", enabled: func(a *App) bool { return !a.admin && !a.scanning }, run: (*App).restartElevated},
		"opt.explorer":   {label: "Add “Analyze with Quarry” to Explorer", checked: func(a *App) bool { return a.dlg.explorerVerb }, run: (*App).toggleExplorerVerb},
		"opt.showMeta":   {label: "Show NTFS Metadata Files", checked: func(a *App) bool { return a.prefs.ShowMetadata }, run: func(a *App) { a.prefs.ShowMetadata = !a.prefs.ShowMetadata; a.applyFilter() }},
		"opt.confirm":    {label: "Confirm Before Moving to Recycle Bin", checked: func(a *App) bool { return a.prefs.ConfirmRecycle }, run: func(a *App) { a.prefs.ConfirmRecycle = !a.prefs.ConfirmRecycle }},

		// Help
		"help.keys":   {label: "Keyboard Shortcuts", keys: "F1", key: ui.KeyF1, inText: true, run: func(a *App) { a.dlg.help = helpKeys }},
		"help.syntax": {label: "Search Syntax", run: func(a *App) { a.dlg.help = helpSyntax }},
		"help.cli":    {label: "Command Line", run: func(a *App) { a.dlg.help = helpCLI }},
		"help.about":  {label: "About Quarry", run: func(a *App) { a.dlg.help = helpAbout }},
	}
}

func (a *App) can(id string) bool {
	cmd := a.cmds[id]
	return cmd != nil && (cmd.enabled == nil || cmd.enabled(a))
}

// mutableMemo remembers mutable for one selection. mutable checks every
// selected path, and the menu asks about four commands every frame.
type mutableMemo struct {
	valid   bool
	view    int
	version uint64
	snap    *index.Snapshot
	source  string
	ok      bool
}

// mutableCached is mutable, recomputed only when the selection (its
// version) or the scan changes.
func (a *App) mutableCached() bool {
	a.selected() // refreshes the selection version for this frame
	m := &a.mutMemo
	v := a.selectionVersion()
	if m.valid && m.view == a.prefs.View && m.version == v && m.snap == a.snap && m.source == a.source {
		return m.ok
	}
	*m = mutableMemo{valid: true, view: a.prefs.View, version: v, snap: a.snap, source: a.source, ok: a.mutable()}
	return m.ok
}

// exec runs a command if it is currently available.
func (a *App) exec(id string) {
	a.invalidateSelections() // commands act on the exact selection
	if a.can(id) {
		a.cmds[id].run(a)
		if a.win != nil {
			a.win.Invalidate()
		}
	}
}

// shortcuts dispatches keyboard shortcuts for every command, respecting text
// fields: editing keys (Ctrl+C, Delete, F2…) stay with a focused text box.
func (a *App) shortcuts(c *ui.Context) {
	typing := a.searchHandle.Focused(c) || a.targetHandle.Focused(c)
	for id, cmd := range a.cmds {
		if cmd.key == 0 || (typing && !cmd.inText) {
			continue
		}
		if c.Shortcut(cmd.mods, cmd.key) {
			a.exec(id)
		}
	}
}

// contextItems adds commands to a context menu. "-" is a separator; hidden
// commands are omitted and repeated separators collapse.
func (a *App) contextItems(m *ui.Menu, ids ...string) {
	pending := false
	any := false
	for _, id := range ids {
		if id == "-" {
			pending = any
			continue
		}
		cmd := a.cmds[id]
		if cmd == nil || (cmd.visible != nil && !cmd.visible(a)) {
			continue
		}
		if pending {
			m.Separator()
			pending = false
		}
		any = true
		item := m.Item(cmd.label).Disabled(!a.can(id))
		if cmd.key != 0 {
			item.Shortcut(cmd.mods, cmd.key)
		}
		if cmd.checked != nil {
			item.Checked(cmd.checked(a))
		}
		if item.Chosen() {
			// Run after the frame: a command may rebuild the rows being built.
			id := id
			a.post(func() { a.exec(id) })
		}
	}
}

// Native menu bar.

type menuBar struct {
	menu          *mygo.Menu
	items         map[string]*mygo.MenuItem
	enabled       map[string]bool
	checked       map[string]bool
	recentChanged bool
}

func (a *App) buildMenu() *menuBar {
	mb := &menuBar{items: map[string]*mygo.MenuItem{}, enabled: map[string]bool{}, checked: map[string]bool{}}
	item := func(id string) *mygo.MenuItem {
		cmd := a.cmds[id]
		label := cmd.label
		if cmd.keys != "" {
			// The shortcut is shown but not registered as a native
			// accelerator, so text fields keep their editing keys.
			label += "\t" + cmd.keys
		}
		it := &mygo.MenuItem{ID: id, Label: label, Click: func(*mygo.MenuItem, *mygo.Window) { a.exec(id) }}
		if cmd.checked != nil {
			it.Type = mygo.MenuItemCheckbox
			if cmd.radio {
				it.Type = mygo.MenuItemRadio
			}
			it.Checked = cmd.checked(a)
		}
		it.Disabled = !a.can(id)
		mb.items[id] = it
		mb.enabled[id] = !it.Disabled
		mb.checked[id] = it.Checked
		return it
	}
	list := func(ids ...string) []*mygo.MenuItem {
		var out []*mygo.MenuItem
		for _, id := range ids {
			if id == "-" {
				out = append(out, mygo.Separator())
			} else {
				out = append(out, item(id))
			}
		}
		return out
	}
	sub := func(label string, items []*mygo.MenuItem) *mygo.MenuItem {
		return &mygo.MenuItem{Label: label, Submenu: items}
	}
	recent := []*mygo.MenuItem{}
	for _, path := range a.prefs.Recent {
		path := path
		recent = append(recent, &mygo.MenuItem{Label: strings.ReplaceAll(path, "&", "&&"), Click: func(*mygo.MenuItem, *mygo.Window) { a.scanPath(path) }})
	}
	if len(recent) == 0 {
		recent = append(recent, &mygo.MenuItem{Label: "(none)", Disabled: true})
	} else {
		recent = append(recent, mygo.Separator(), &mygo.MenuItem{Label: "Clear Recent Locations", Click: func(*mygo.MenuItem, *mygo.Window) {
			a.prefs.Recent = nil
			a.menu.recentChanged = true
		}})
	}
	drives := []*mygo.MenuItem{}
	for _, d := range a.drives {
		d := d
		drives = append(drives, &mygo.MenuItem{Label: driveLabel(d, a), Click: func(*mygo.MenuItem, *mygo.Window) { a.scanPath(d.Path) }})
	}
	export := list("export.view", "export.selection", "export.scan", "export.ext", "export.json")
	file := append(append([]*mygo.MenuItem{sub("Scan Drive", drives)}, list("scan.folder", "scan.rescan", "scan.stop", "-")...), sub("Recent Locations", recent))
	file = append(file, list("-", "item.open", "item.explorer", "item.properties", "-", "file.openScan", "file.saveScan")...)
	file = append(file, sub("Export", export))
	file = append(file, list("-", "file.newWindow", "-", "file.exit")...)
	view := list("view.tree", "view.files", "view.ext", "-", "nav.back", "nav.forward", "nav.up", "nav.root", "nav.location", "-", "tree.expandLevel", "tree.collapseLvl", "tree.collapseAll", "-", "view.treemap", "view.maxTreemap", "view.resetLayout", "-")
	view = append(view, sub("Size Metric", list("metric.logical", "metric.allocated")), sub("Treemap Colors", list("color.ext", "color.folder", "color.mono")), sub("Size Units", list("units.binary", "units.decimal")))
	view = append(view, list("-", "zoom.in", "zoom.out", "zoom.reset", "view.compact")...)
	view = append(view, sub("Theme", list("theme.system", "theme.dark", "theme.light")))
	mb.menu = mygo.NewMenu([]*mygo.MenuItem{
		sub("&File", file),
		sub("&Edit", list("edit.cut", "edit.copy", "edit.copyPath", "edit.copyName", "edit.copySize", "edit.copyRows", "-", "edit.selectAll", "-", "edit.rename", "edit.delete", "edit.deletePerm", "-", "edit.find")),
		sub("&View", view),
		sub("&Tools", list("tools.search", "tools.filter", "tools.clearFilter", "-", "tools.dups", "tools.compare", "-", "tools.issues", "tools.info")),
		sub("&Options", append(list("opt.exclusions", "opt.useExcl", "-"), append([]*mygo.MenuItem{sub("Scan Method", list("opt.scanAuto", "opt.scanEnum"))}, list("opt.admin", "-", "opt.showMeta", "opt.explorer", "opt.confirm")...)...)),
		sub("&Help", list("help.keys", "help.syntax", "help.cli", "-", "help.about")),
	})
	return mb
}

func driveLabel(d platform.Drive, a *App) string {
	label := d.Path
	if d.Name != "" {
		label += "  " + d.Name
	}
	if d.Total > 0 {
		label += "  (" + a.size(d.Free) + " free of " + a.size(d.Total) + ")"
	}
	return strings.ReplaceAll(label, "&", "&&")
}

// syncMenu updates enabled and check states that changed since the last
// frame. Native updates are posted to the main loop by mygo.
func (a *App) syncMenu() {
	if a.menu == nil || a.win == nil {
		return
	}
	if a.menu.recentChanged {
		a.menu = a.buildMenu()
		a.win.SetMenu(a.menu.menu)
		return
	}
	for id, it := range a.menu.items {
		cmd := a.cmds[id]
		if en := a.can(id); en != a.menu.enabled[id] {
			a.menu.enabled[id] = en
			it.SetEnabled(en)
		}
		if cmd.checked != nil {
			if ch := cmd.checked(a); ch != a.menu.checked[id] {
				a.menu.checked[id] = ch
				it.SetChecked(ch)
			}
		}
	}
}
