package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// view builds the window for every frame.
func (a *App) view(c *ui.Context) {
	if a.drain() {
		c.Invalidate()
	}
	t, pal := a.appTheme()
	c.SetTheme(t)
	a.hover, a.hoverOther = index.None, false
	a.syncMenu()
	a.shortcuts(c)
	if c.Shortcut(0, ui.KeyEscape) && a.scanning {
		a.stopScan()
	}
	if c.Shortcut(0, ui.KeyBack) {
		a.exec("nav.back")
	}
	if c.Shortcut(0, ui.KeyForward) {
		a.exec("nav.forward")
	}
	if a.scanning {
		a.scanProgress()
		c.After(150 * time.Millisecond)
	}
	if a.prefs.View == viewFiles {
		a.files.tick(c)
		a.files.ensure()
	}
	if a.prefs.View == viewExtensions {
		a.ext.ensure()
	}
	if !a.messageAt.IsZero() {
		if age := time.Since(a.messageAt); age > 12*time.Second && !a.errorMsg {
			a.message, a.messageAt = "", time.Time{}
		} else if age < 5*time.Second && !a.errorMsg {
			c.After(5*time.Second - age) // the status bar stops showing it then
		} else {
			c.After(12*time.Second - age)
		}
	}

	root := ui.Column(c).Fill().Gap(0).Background(pal.grid).FontFeatures("tnum")
	if files := root.DroppedFiles(); len(files) > 0 {
		a.dropped(files)
	}
	root.Children(func() {
		a.toolbar(c, pal)
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			switch {
			case a.snap == nil && a.scanning:
				a.scanningPage(c)
			case a.snap == nil:
				a.startPage(c, pal)
			default:
				a.workspace(c, pal)
			}
		})
		a.statusBar(c, pal)
	})
	a.dialogs(c, pal)
	a.trimAfterShown()
}

func (a *App) scanProgress() {
	p := a.progress
	if a.win == nil || p == nil {
		return
	}
	if total := p.TotalRecords.Load(); total > 0 {
		a.win.SetProgressBar(mygo.ProgressBar{Value: float64(p.Records.Load()) / float64(total)})
	} else {
		a.win.SetProgressBar(mygo.ProgressBar{State: mygo.ProgressIndeterminate})
	}
}

// dropped handles folders dragged in from Explorer. Replacing an existing
// scan asks first, offering a separate window instead.
func (a *App) dropped(files []string) {
	path := files[0]
	if isScanFile(path) {
		a.openSnapshot(path)
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		a.fail("Drop a folder or drive to analyze it.")
		return
	}
	if a.snap == nil && !a.scanning {
		a.scanPath(path)
		return
	}
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.win, Type: mygo.MessageQuestion, Title: "Quarry", Message: "Analyze " + path + "?", Detail: "Scanning here replaces the current results. You can open it in a new window instead.", Buttons: []string{"Scan Here", "New Window", "Cancel"}, CancelButton: 2})
		if err != nil {
			return
		}
		a.post(func() {
			switch res.Button {
			case 0:
				a.stopScan()
				a.scanGen++
				a.scanning = false
				a.scanPath(path)
			case 1:
				a.newWindow(path)
			}
		})
	}()
}

// Toolbar.

func toolButton(c *ui.Context, key string, icon string, tip string, enabled bool) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Size(26, 26).Radius(4).Center().Label(tip).Tooltip(tip).Disabled(!enabled)
	color := t.Text
	if !enabled {
		color = t.TextMuted.Alpha(.5)
	} else if b.Pressed() {
		b.Background(t.SurfacePressed)
	} else if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() { ui.Icon(c, icons[icon]).FontSize(15).TextColor(color) })
	return enabled && b.Clicked()
}

func textButton(c *ui.Context, key, label, tip string, enabled bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Height(26).Padding(0, 10).Radius(4).Border(1, t.Border).Center().Label(label).Disabled(!enabled)
	if tip != "" {
		b.Tooltip(tip)
	}
	color := t.Text
	switch {
	case !enabled:
		color = t.TextMuted.Alpha(.5)
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	default:
		b.Background(t.Surface)
	}
	b.Children(func() { ui.Text(c, label).TextColor(color).SingleLine() })
	return b
}

func vsep(c *ui.Context, pal palette) {
	ui.Box(c).Size(1, 18).Background(pal.separator).Margin(0, 4)
}

func (a *App) toolbar(c *ui.Context, pal palette) {
	ui.Row(c).Height(36).Shrink(0).Padding(0, 6).Gap(4).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(0, 0, 1, 0).BorderColor(pal.separator).Children(func() {
		a.scanMenu(c)
		// One button that rescans and, while a scan runs, stops it.
		switch {
		case a.scanning:
			if textButton(c, "stop", "Stop", "Stop scanning (Esc)", true).Clicked() {
				a.stopScan()
			}
		case a.base != nil:
			if textButton(c, "rescan", "Rescan", "Scan "+a.base.Root+" again (F5)", true).Clicked() {
				a.rescan()
			}
		}
		vsep(c, pal)
		if toolButton(c, "back", "arrow_left", "Back (Alt+Left)", a.canBack()) {
			a.back()
		}
		if toolButton(c, "forward", "arrow_right", "Forward (Alt+Right)", a.canForward()) {
			a.forward()
		}
		if toolButton(c, "up", "arrow_up", "Up one level (Alt+Up)", a.canUp()) {
			a.up()
		}
		a.locationBar(c, pal)
		vsep(c, pal)
		a.searchBox(c)
	})
}

// scanMenu is the toolbar's entry point for new scans: drives with their
// free space, recent locations, a folder picker and saved scans.
func (a *App) scanMenu(c *ui.Context) {
	t := c.Theme()
	b := ui.ButtonBase(c.Key("scan-menu")).Height(26).Padding(0, 6, 0, 8).Gap(6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Label("Scan").Tooltip("Scan a drive or folder").Disabled(a.scanning)
	color := t.Text
	switch {
	case a.scanning:
		color = t.TextMuted.Alpha(.5)
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	default:
		b.Background(t.Surface)
	}
	b.Children(func() {
		ui.Icon(c, icons["hard_drive"]).FontSize(14).TextColor(color)
		ui.Text(c, "Scan").TextColor(color).SingleLine()
		ui.Icon(c, icons["chevron_down"]).FontSize(10).TextColor(t.TextMuted)
	})
	if a.scanning {
		return
	}
	b.Menu(func(m *ui.Menu) {
		for _, d := range a.drives {
			if m.Item(strings.ReplaceAll(driveLabel(d, a), "&&", "&")).Chosen() {
				a.scanPath(d.Path)
			}
		}
		if len(a.prefs.Recent) > 0 {
			m.Separator()
			for _, r := range a.prefs.Recent {
				if m.Item(r).Chosen() {
					a.scanPath(r)
				}
			}
		}
		m.Separator()
		a.contextItems(m, "scan.folder", "file.openScan")
	})
}

// locationBar shows where Tree View and the treemap are rooted as
// breadcrumbs. Like Explorer's address bar, a click on its empty part or
// Ctrl+L turns it into a text box: a path inside the scan goes there, any
// other folder is scanned.
func (a *App) locationBar(c *ui.Context, pal palette) {
	t := c.Theme()
	editing := a.locEdit || a.snap == nil
	bar := ui.Row(c).Grow(1).MinWidth(60).Height(26).Radius(4).Border(1, t.Border).Background(pal.grid).AlignItems(ui.Center).Clip()
	bar.Children(func() {
		if editing {
			placeholder := "Type a folder path and press Enter to scan it"
			field := ui.TextInput(c.Key("location"), &a.locText).Bind(&a.targetHandle).Grow(1).MinWidth(0).Height(24).Background(ui.Transparent).Border(0, ui.Transparent).Padding(0, 6).Placeholder(placeholder).Label("Location").SingleLine()
			if field.Focused() {
				bar.Border(1, t.Focus)
				a.locArmed = true
			} else if a.locArmed && a.snap != nil {
				a.endLocationEdit() // focus moved elsewhere, as in Explorer
			}
			if a.wantLoc {
				a.wantLoc = false
				field.Focus()
				field.SetTextSelection(0, len([]rune(a.locText)))
				c.Invalidate()
			}
			if field.Shortcut(0, ui.KeyEscape) {
				a.endLocationEdit()
				a.focusTable = a.snap != nil
			}
			if field.Submitted() {
				a.goToLocation(a.locText)
			}
			return
		}
		var ids []index.ID
		for id := a.location; id != index.None; id = a.snap.Nodes[id].Parent {
			ids = append([]index.ID{id}, ids...)
		}
		names := make([]string, len(ids))
		for i, id := range ids {
			names[i] = strings.TrimSuffix(a.name(id), `\`)
		}
		chosen := len(ids) - 1
		ui.Box(c).Width(6).Shrink(0)
		if ui.Breadcrumbs(c.Key("crumbs"), names, &chosen).Label("Location").Shrink(1).MinWidth(0).Changed() && chosen >= 0 && chosen < len(ids) {
			a.navigate(ids[chosen])
		}
		edit := ui.ButtonBase(c.Key("location-edit")).Grow(1).Height(24).Label("Edit location").Tooltip(a.pathOf(a.location) + "\nClick to type a path (Ctrl+L)")
		if edit.Clicked() {
			a.editLocation()
		}
	})
}

func (a *App) editLocation() {
	a.locText = a.target
	if a.snap != nil {
		a.locText = a.pathOf(a.location)
	}
	a.locEdit, a.locArmed, a.wantLoc = true, false, true
}

func (a *App) endLocationEdit() {
	a.locEdit, a.locArmed, a.wantLoc = false, false, false
}

// goToLocation handles a path typed into the location bar.
func (a *App) goToLocation(text string) {
	path := strings.Trim(strings.TrimSpace(text), `"`)
	if path == "" {
		return
	}
	if len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	if a.snap != nil {
		if id, ok := a.findPath(path); ok {
			a.endLocationEdit()
			if a.snap.Nodes[id].IsDir() {
				a.navigate(id)
			} else {
				a.reveal(id)
			}
			a.focusTable = true
			return
		}
	}
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		if a.scanning {
			a.fail("Wait for the current scan to finish, or stop it (Esc).")
			return
		}
		a.endLocationEdit()
		a.scanPath(path)
	case err == nil && isScanFile(path):
		a.endLocationEdit()
		a.openSnapshot(path)
	default:
		a.fail("Cannot find %s.", path)
	}
}

// findPath returns the entry of the scan at path, if the scan contains it.
func (a *App) findPath(path string) (index.ID, bool) {
	s := a.snap
	root := strings.TrimSuffix(filepath.Clean(s.Root), `\`)
	p := strings.TrimSuffix(filepath.Clean(path), `\`)
	if len(p) < len(root) || !strings.EqualFold(p[:len(root)], root) || (len(p) > len(root) && p[len(root)] != '\\') {
		return index.None, false
	}
	id := index.ID(0)
	for _, part := range strings.Split(p[len(root):], `\`) {
		if part == "" {
			continue
		}
		next := index.None
		for _, k := range s.Children(id) {
			if strings.EqualFold(s.NameView(k), part) {
				next = k
				break
			}
		}
		if next == index.None {
			return index.None, false
		}
		id = next
	}
	return id, true
}

func (a *App) searchBox(c *ui.Context) {
	t := c.Theme()
	ext := a.prefs.View == viewExtensions
	text, placeholder := &a.search, "Search files and folders (Ctrl+F)"
	if ext {
		text, placeholder = &a.extFilter, "Filter extensions (Ctrl+F)"
	}
	ui.Row(c).Gap(0).AlignItems(ui.Center).Children(func() {
		var field ui.Element
		box := ui.Row(c).Width(280).Height(26).Padding(0, 4, 0, 6).Gap(4).Radius(4).Border(1, t.Border).Background(t.Background).AlignItems(ui.Center)
		box.Children(func() {
			ui.Icon(c, icons["search"]).FontSize(13).TextColor(t.TextMuted).Shrink(0)
			field = ui.TextInput(c.Key("search"), text).Bind(&a.searchHandle).Grow(1).MinWidth(0).Background(ui.Transparent).Border(0, ui.Transparent).Padding(0).Placeholder(placeholder).Label("Search").SingleLine().Disabled(a.snap == nil)
			if *text != "" {
				clearBtn := ui.ButtonBase(c.Key("search-clear")).Size(18, 18).Center().Radius(3).Label("Clear search").Tooltip("Clear (Esc)")
				if clearBtn.Hovered() {
					clearBtn.Background(t.SurfaceHover)
				}
				clearBtn.Children(func() { ui.Icon(c, icons["dismiss"]).FontSize(11).TextColor(t.TextMuted) })
				if clearBtn.Clicked() {
					*text = ""
					a.files.queryChanged(c.Now())
				}
			}
		})
		if field.Focused() {
			box.Border(1, t.Focus)
		}
		if field.Shortcut(0, ui.KeyEscape) {
			if *text != "" {
				*text = ""
				if !ext {
					a.files.queryChanged(c.Now())
				}
			} else {
				a.focusTable = true
			}
		}
		if field.Shortcut(0, ui.KeyDown) {
			a.focusTable = true // continue into the results
		}
		if a.wantSearch && a.snap != nil {
			// Select the query so typing replaces it, as in Explorer.
			a.wantSearch, a.focusTable = false, false
			field.Focus()
			field.SetTextSelection(0, len([]rune(*text)))
			c.Invalidate()
		}
		if field.Changed() && !ext {
			if a.prefs.View == viewTree && strings.TrimSpace(a.search) != "" {
				a.setViewMode(viewFiles)
				a.focusTable = false
			}
			a.files.queryChanged(c.Now())
			c.After(160 * time.Millisecond)
		}
		if field.Submitted() && a.prefs.View == viewFiles {
			a.files.run()
			a.focusTable = true
		}
		ui.ButtonBase(c.Key("search-menu")).Size(20, 26).Center().Label("Search options").Tooltip("Recent searches and search help").Children(func() {
			ui.Icon(c, icons["chevron_down"]).FontSize(11).TextColor(t.TextMuted)
		}).Menu(func(m *ui.Menu) {
			for _, q := range a.prefs.SearchHistory {
				if m.Item(q).Chosen() {
					a.search = q
					a.setViewMode(viewFiles)
					a.files.invalidate()
				}
			}
			if len(a.prefs.SearchHistory) > 0 {
				if m.Item("Clear Search History").Chosen() {
					a.prefs.SearchHistory = nil
				}
				m.Separator()
			}
			a.contextItems(m, "tools.search", "tools.filter", "help.syntax")
		})
	})
}

// progressText is the live line of a running scan.
func (a *App) progressText() string {
	p := a.progress
	if p == nil {
		return "Scanning " + a.target + "…"
	}
	text := fmt.Sprintf("Scanning %s - %s files, %s folders · %s · %s", a.target, count(p.Files.Load()), count(p.Folders.Load()), duration(time.Since(a.scanStart)), p.Status())
	if total := p.TotalRecords.Load(); total > 0 {
		text += fmt.Sprintf(" %.0f%%", 100*float64(p.Records.Load())/float64(total))
	}
	if e := p.Errors.Load(); e > 0 {
		text += " · " + plural(e, "inaccessible item", "inaccessible items")
	}
	return text
}

// scanFlags names what makes the results differ from the disk as it is:
// only shown when it applies, beside the view tabs.
func (a *App) scanFlags(c *ui.Context) {
	t := c.Theme()
	s := a.snap
	warn := func(text, tip string) {
		ui.Text(c, text).TextColor(t.Warning).SingleLine().Tooltip(tip)
	}
	if a.source != "" {
		warn("Saved scan from "+platform.FormatDateTime(s.Started), a.source+"\nHistorical data: files may have changed since.")
	}
	if s.Cancelled {
		warn("Partial scan", "The scan was stopped before it finished; totals are incomplete.")
	}
	if len(s.Exclusions) > 0 {
		warn(plural(s.Excluded, "entry excluded", "entries excluded"), "Scan exclusions kept these out; totals are incomplete:\n"+strings.Join(s.Exclusions, "\n"))
	}
	if a.stale != "" {
		warn("Results may be outdated", a.stale+"\nPress F5 to rescan.")
	}
	if a.filter.active() {
		status := "Filtered: " + a.filter.summary()
		if a.filterBusy {
			status = "Applying filter…"
		} else if a.filter.stats.HiddenFiles > 0 {
			status += " · " + plural(a.filter.stats.HiddenFiles, "file", "files") + " hidden"
		}
		ui.Text(c, status).TextColor(t.Focus).SingleLine().MinWidth(0).Shrink(1).MaxWidth(360).Tooltip(status)
		b := ui.ButtonBase(c.Key("clear-filter")).Padding(0, 4).Label("Clear filter").Tooltip("Show all entries again")
		b.Children(func() { ui.Text(c, "Clear").TextColor(t.Focus).Underline() })
		if b.Clicked() {
			a.clearFilter()
		}
	}
}

func engineName(s *index.Snapshot) string {
	if strings.Contains(s.Engine, "MFT") {
		return "NTFS MFT"
	}
	return "Directory scan"
}

// Workspace.

func (a *App) workspace(c *ui.Context, pal palette) {
	grid := a.gridTheme(c.Theme())
	area := ui.Column(c).Grow(1).MinHeight(0)
	area.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputScroll && ev.Mods&ui.Ctrl != 0 && ev.DY != 0 {
			if ev.DY < 0 {
				a.setZoom(a.zoom() + 0.1)
			} else {
				a.setZoom(a.zoom() - 0.1)
			}
			a.win.Invalidate()
			return true
		}
		return false
	})
	area.Children(func() {
		data := func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				a.viewTabs(c, pal)
				prev := c.Theme()
				c.SetTheme(grid)
				switch a.prefs.View {
				case viewFiles:
					a.fileTable(c, pal)
				case viewExtensions:
					a.extTable(c, pal)
				default:
					a.treeTable(c, pal)
				}
				c.SetTheme(prev)
			})
		}
		mapPane := func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				a.treemapBar(c, pal)
				a.treemapView(c, pal)
			})
		}
		switch {
		case a.tm.maximized:
			mapPane()
		case a.prefs.ShowTreemap:
			ui.SplitVerticalPercent(c.Key("split"), &a.prefs.TreemapPct, data, mapPane).Grow(1).MinHeight(0)
		default:
			data()
		}
	})
}

func (a *App) viewTabs(c *ui.Context, pal palette) {
	t := c.Theme()
	ui.Row(c).Height(28).Shrink(0).Padding(0, 6).Gap(2).AlignItems(ui.Stretch).Background(pal.chrome).BorderWidth(0, 0, 1, 0).BorderColor(pal.separator).Children(func() {
		for i, name := range []string{"Tree", "Files", "Extensions"} {
			active := a.prefs.View == i
			tab := ui.ButtonBase(c.Key("tab"+name)).Padding(0, 10).Center().Label(name + " view").Tooltip(fmt.Sprintf("%s (Ctrl+%d)", name, i+1))
			if active {
				tab.BorderWidth(0, 0, 2, 0).BorderColor(t.Focus)
			} else if tab.Hovered() {
				tab.Background(t.SurfaceHover)
			}
			i := i
			tab.Children(func() {
				txt := ui.Text(c, name).SingleLine()
				if !active {
					txt.TextColor(t.TextMuted)
				}
			})
			if tab.Clicked() {
				a.setViewMode(i)
			}
		}
		ui.Spacer(c)
		ui.Row(c).Gap(10).AlignItems(ui.Center).MinWidth(0).Shrink(1).Clip().Children(func() {
			a.scanFlags(c)
			a.viewContext(c)
		})
	})
}

// viewContext shows what the active view contains and its scope.
func (a *App) viewContext(c *ui.Context) {
	t := c.Theme()
	muted := func(s string) ui.Element { return ui.Text(c, s).TextColor(t.TextMuted).SingleLine() }
	switch a.prefs.View {
	case viewTree:
		if a.tree.notice != "" {
			ui.Text(c, "Expansion limited").TextColor(t.Warning).Tooltip(a.tree.notice)
		}
	case viewFiles, viewExtensions:
		if a.prefs.View == viewFiles {
			f := &a.files
			switch {
			case f.err != "":
				ui.Text(c, f.err).TextColor(t.Danger).SingleLine().MaxWidth(420)
			case f.busy:
				muted("Searching…")
			default:
				n := uint64(len(f.ids))
				what := plural(n, "file", "files")
				if strings.TrimSpace(f.ran) != "" {
					what = plural(n, "match", "matches")
				}
				muted(what + " · " + a.size(f.total))
			}
		} else {
			e := &a.ext
			if e.busy {
				muted("Analyzing…")
			} else {
				muted(fmt.Sprintf("%s of %s extensions · %% of %s files in scope", count(uint64(len(e.shown))), count(uint64(len(e.types))), strings.ToLower(a.prefs.Metric)))
			}
		}
		scope := a.files.scopeName()
		b := ui.ButtonBase(c.Key("scope")).Height(22).Padding(0, 6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Gap(4).Label("Scope").Tooltip("Where File View and Extensions look")
		b.Children(func() {
			muted("Scope:")
			ui.Text(c, truncateMiddle(scope, 48)).SingleLine()
			ui.Icon(c, icons["chevron_down"]).FontSize(10).TextColor(t.TextMuted)
		}).Menu(func(m *ui.Menu) {
			if m.Item("Entire Scan").Checked(a.files.scope == 0).Chosen() {
				a.files.setScope(0)
			}
			if a.location != 0 && m.Item("Current Location: "+a.name(a.location)).Checked(a.files.scope == a.location).Chosen() {
				a.files.setScope(a.location)
			}
			if id := a.tree.cursorID(); id != index.None && id != 0 && id != a.location && a.snap.Nodes[id].IsDir() {
				if m.Item("Selected Folder: " + a.name(id)).Checked(a.files.scope == id).Chosen() {
					a.files.setScope(id)
				}
			}
		})
	}
}

func truncateMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	half := (n - 1) / 2
	return string(r[:half]) + "…" + string(r[len(r)-half:])
}

func (a *App) treemapBar(c *ui.Context, pal palette) {
	t := c.Theme()
	ui.Row(c).Height(28).Shrink(0).Padding(0, 6, 0, 10).Gap(6).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(1, 0, 1, 0).BorderColor(pal.separator).Children(func() {
		// The location and its size are in the toolbar and the tree already.
		ui.Text(c, "Treemap").SingleLine().Tooltip("Click selects in the tree; double-click a folder to go into it, Backspace or Alt+Up goes back up.")
		if a.tm.busy {
			ui.Text(c, "Updating…").TextColor(t.TextMuted)
		}
		ui.Spacer(c)
		metric := a.prefs.Metric + " size"
		if ui.Select(c.Key("tm-metric"), &metric, []string{"Logical size", "Allocated size"}).Label("Size metric").Tooltip("Size metric for the treemap, percentages and default sorting").Width(124).Changed() {
			a.setMetric(strings.TrimSuffix(metric, " size"))
		}
		colors := map[string]string{"Extension": "Color by extension", "Folder": "Color by folder", "Mono": "Monochrome"}
		choice := colors[a.prefs.TreemapColors]
		if ui.Select(c.Key("tm-colors"), &choice, []string{"Color by extension", "Color by folder", "Monochrome"}).Label("Treemap colors").Width(150).Changed() {
			for k, v := range colors {
				if v == choice {
					a.prefs.TreemapColors = k
				}
			}
		}
		maxTip := "Maximize treemap (Ctrl+Shift+M)"
		if a.tm.maximized {
			maxTip = "Restore treemap (Ctrl+Shift+M)"
		}
		icon := "arrow_up"
		if a.tm.maximized {
			icon = "chevron_down"
		}
		if toolButton(c, "tm-max", icon, maxTip, true) {
			a.exec("view.maxTreemap")
		}
		if toolButton(c, "tm-hide", "dismiss", "Hide treemap (Ctrl+T)", true) {
			a.prefs.ShowTreemap, a.tm.maximized = false, false
		}
	})
}

// Status bar.

func (a *App) statusBar(c *ui.Context, pal palette) {
	t := c.Theme()
	ui.Row(c).Height(24).Shrink(0).Padding(0, 10).Gap(16).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(1, 0, 0, 0).BorderColor(pal.separator).Clip().Children(func() {
		// A fresh message or an error first, then what the pointer is over,
		// then the focused entry: the full path the columns cannot show.
		msg := a.message
		color := t.Text
		if a.errorMsg {
			color = t.Danger
		} else if a.scanning || !a.messageAt.IsZero() && time.Since(a.messageAt) > 5*time.Second {
			msg = ""
		}
		if msg == "" && a.scanning && a.snap != nil { // a first scan shows it on the page
			ui.Spinner(c).Size(12, 12).Shrink(0)
			msg = a.progressText()
		}
		if msg == "" {
			color = t.TextMuted
			switch {
			case a.snap == nil:
				msg = "Ready"
			case a.hover != index.None && int(a.hover) < len(a.snap.Nodes):
				msg = a.itemStatus(a.hover, a.hoverOther)
			case a.current() != index.None:
				msg = a.itemStatus(a.current(), false)
			default:
				msg = "Ready"
			}
		}
		m := ui.Text(c, msg).TextColor(color).SingleLine().Shrink(1).MinWidth(0)
		if a.errorMsg {
			m.Tooltip(msg)
		}
		ui.Spacer(c)
		// One selected item is already on the left; a count helps for more.
		if ids := a.selected(); len(ids) > 1 && a.snap != nil {
			ui.Text(c, fmt.Sprintf("%s selected · %s", count(uint64(len(ids))), a.size(a.facts().total))).TextColor(t.TextMuted).SingleLine()
		}
		if a.snap != nil {
			s := a.snap
			engine := ui.Text(c, engineName(s)+" · "+duration(s.Elapsed)).TextColor(t.TextMuted).SingleLine()
			tip := s.Engine
			if s.Fallback != "" {
				tip += "\nDirect MFT reading was not used: " + s.Fallback
				if !a.admin {
					tip += "\nOptions → Restart as Administrator enables it for NTFS drives."
				}
			}
			if bytes, ok := a.metadataBytes(); ok {
				tip += fmt.Sprintf("\nNTFS metadata ($MFT, $LogFile, $BadClus, $Extend…, %s) is not listed or counted; Options → Show NTFS Metadata Files lists it.", a.size(bytes))
			}
			engine.Tooltip(tip)
			if s.Errors > 0 {
				b := ui.ButtonBase(c.Key("issues")).Padding(0, 2).Gap(4).AlignItems(ui.Center).Label("Scan issues").Tooltip("Some items could not be read; totals may be incomplete. Click for details.")
				b.Children(func() {
					ui.Icon(c, icons["warning"]).FontSize(12).TextColor(t.Warning)
					ui.Text(c, plural(s.Errors, "issue", "issues")).TextColor(t.Warning)
				})
				if b.Clicked() {
					a.dlg.issues = true
				}
			}
			// Free space is what a cleanup is for; it updates after deletions.
			if d, ok := a.driveOf(s.Root); ok && a.source == "" {
				ui.Text(c, fmt.Sprintf("%s %s free", strings.TrimSuffix(d.Path, `\`), a.size(d.Free))).TextColor(t.TextMuted).SingleLine().Tooltip(fmt.Sprintf("Volume %s: %s used of %s (%s)", d.Path, a.size(d.Total-d.Free), a.size(d.Total), percentText(percent(d.Total-d.Free, d.Total))))
			}
		}
	})
}

// itemStatus is the status-bar line for an entry: its full path and size.
func (a *App) itemStatus(id index.ID, other bool) string {
	n := a.snap.Nodes[id]
	path := a.pathOf(id)
	if path == "" {
		path = a.name(id)
	}
	if other {
		return "Smaller items in " + path
	}
	line := path + "  ·  " + a.size(a.metric(n))
	if n.IsDir() {
		line += "  ·  " + plural(uint64(n.Files), "file", "files")
	}
	return line
}

// Pages without data.

func (a *App) scanningPage(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(10).Children(func() {
		ui.Text(c, "Scanning "+a.target+"…").FontSize(14)
		value := -1.0
		if p := a.progress; p != nil && p.TotalRecords.Load() > 0 {
			value = float64(p.Records.Load()) / float64(p.TotalRecords.Load())
		}
		ui.Progress(c, value).Width(280)
		if p := a.progress; p != nil {
			line := fmt.Sprintf("%s files, %s folders · %s", count(p.Files.Load()), count(p.Folders.Load()), duration(time.Since(a.scanStart)))
			if e := p.Errors.Load(); e > 0 {
				line += " · " + plural(e, "inaccessible item", "inaccessible items")
			}
			ui.Text(c, line).TextColor(t.TextMuted)
		}
		ui.Text(c, "Esc stops the scan.").TextColor(t.TextMuted)
	})
}

// startPage lists what can be scanned: the drives, then recent folders.
// The toolbar's Scan menu and location bar cover everything else.
func (a *App) startPage(c *ui.Context, pal palette) {
	t := c.Theme()
	row := func(key, label string, children func()) bool {
		b := ui.ButtonBase(c.Key(key)).Height(30).Padding(0, 8).Radius(4).Gap(8).AlignItems(ui.Center).Justify(ui.Start).Label(label).Tooltip(label)
		if b.Pressed() {
			b.Background(t.SurfacePressed)
		} else if b.Hovered() {
			b.Background(t.SurfaceHover)
		}
		b.Children(children)
		return b.Clicked()
	}
	ui.Column(c).Fill().Padding(14, 16).Gap(2).Children(func() {
		cols := []ui.TableColumn{{Title: "Drive", Width: 70}, {Title: "Label", Width: 180}, {Title: "File system", Width: 90}, {Title: "Capacity", Width: 90, Align: ui.End}, {Title: "Used", Width: 160}, {Title: "Free", Width: 90, Align: ui.End}}
		a.dlg.driveList.Selected = &a.dlg.driveRow
		table := ui.Table(c.Key("drives"), &a.dlg.driveList, cols, len(a.drives), func(row, col int) {
			d := a.drives[row]
			// One click scans, like the recent folders below.
			cell := cellBox(c, cols[col].Align)
			if cell.Clicked() {
				a.post(func() { a.scanPath(d.Path) })
			}
			cell.Children(func() {
				switch col {
				case 0:
					if b := shellIcons["drive"]; b != nil {
						ui.Image(c, b).Size(16, 16)
					}
					ui.Text(c, d.Path).SingleLine()
				case 1:
					ui.Text(c, d.Name).SingleLine()
				case 2:
					ui.Text(c, d.FileSystem).TextColor(t.TextMuted)
				case 3:
					ui.Text(c, a.size(d.Total))
				case 4:
					used := percent(d.Total-d.Free, d.Total)
					shareBar(c, used/100, 90, pal)
					ui.Text(c, percentText(used)).TextColor(t.TextMuted)
				case 5:
					ui.Text(c, a.size(d.Free))
				}
			})
		}).Height(float32(28 + 26*max(1, len(a.drives)))).MaxHeight(260).MaxWidth(720).Tooltip("Click or press Enter to scan")
		if table.Submitted() && a.dlg.driveRow >= 0 && a.dlg.driveRow < len(a.drives) {
			a.scanPath(a.drives[a.dlg.driveRow].Path)
		}
		var recent []string
		for _, r := range a.prefs.Recent {
			if len(filepath.Clean(r)) > 3 { // drive roots are listed above
				recent = append(recent, r)
			}
		}
		if len(recent) > 0 {
			ui.Text(c, "Recent").TextColor(t.TextMuted).Margin(12, 8, 4, 8)
			ui.Column(c).Gap(2).MaxWidth(720).Children(func() {
				for _, r := range recent {
					r := r
					if row("recent"+r, "Scan "+r, func() {
						if bm := shellIcons["folder"]; bm != nil {
							ui.Image(c, bm).Size(16, 16).Shrink(0)
						}
						ui.Text(c, r).SingleLine().MinWidth(0).Shrink(1)
					}) {
						a.scanPath(r)
					}
				}
			})
		}
		if !a.admin {
			ui.Row(c).Margin(14, 8, 0, 8).Gap(4).AlignItems(ui.Center).Children(func() {
				b := ui.ButtonBase(c.Key("start-admin")).Label("Restart as administrator").Tooltip("Reads the NTFS master file table directly instead of enumerating folders")
				b.Children(func() { ui.Text(c, "Restart as administrator").TextColor(t.Focus).Underline() })
				if b.Clicked() {
					a.restartElevated()
				}
				ui.Text(c, "for much faster scans of NTFS drives.").TextColor(t.TextMuted).SingleLine()
			})
		}
	})
}

func (a *App) driveOf(path string) (d struct {
	Path        string
	Total, Free uint64
}, ok bool) {
	for _, dr := range a.drives {
		if len(path) >= len(dr.Path) && strings.EqualFold(path[:len(dr.Path)], dr.Path) && dr.Total > 0 {
			d.Path, d.Total, d.Free = dr.Path, dr.Total, dr.Free
			return d, true
		}
	}
	return d, false
}
