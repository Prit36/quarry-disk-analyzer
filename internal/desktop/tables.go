package desktop

import (
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// column describes a table column; text renders it for auto-sizing.
type column struct {
	ui.TableColumn
	hideable bool
}

var treeColumns = []column{
	{ui.TableColumn{Title: "Name", MinWidth: 160, Sortable: true}, false},
	{ui.TableColumn{Title: "% of Parent", Width: 150, MinWidth: 80, Sortable: true}, true},
	{ui.TableColumn{Title: "Size", Width: 84, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Allocated", Width: 84, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "% of Scan", Width: 72, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Files", Width: 76, MinWidth: 40, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Folders", Width: 66, MinWidth: 40, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Modified", Width: 144, MinWidth: 60, Sortable: true}, true},
}

var fileColumns = []column{
	{ui.TableColumn{Title: "Name", Width: 260, MinWidth: 120, Sortable: true}, false},
	{ui.TableColumn{Title: "Size", Width: 84, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Allocated", Width: 84, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "% of Scan", Width: 72, MinWidth: 50, Align: ui.End, Sortable: true}, true},
	{ui.TableColumn{Title: "Modified", Width: 144, MinWidth: 60, Sortable: true}, true},
	{ui.TableColumn{Title: "Extension", Width: 72, MinWidth: 40, Sortable: true}, true},
	{ui.TableColumn{Title: "Folder", MinWidth: 120, Sortable: true}, true},
}

var extColumns = []ui.TableColumn{
	{Title: "Extension", Width: 130, MinWidth: 60, Sortable: true},
	{Title: "Type", Width: 200, MinWidth: 60, Sortable: true},
	{Title: "Files", Width: 90, MinWidth: 40, Align: ui.End, Sortable: true},
	{Title: "Size", Width: 90, MinWidth: 50, Align: ui.End, Sortable: true},
	{Title: "Allocated", Width: 90, MinWidth: 50, Align: ui.End, Sortable: true},
	{Title: "Percent", MinWidth: 120, Sortable: true},
}

// visibleColumns returns the shown columns and their titles.
func visibleColumns(all []column, hidden map[string]bool) ([]ui.TableColumn, []string) {
	cols := make([]ui.TableColumn, 0, len(all))
	ids := make([]string, 0, len(all))
	for _, c := range all {
		if c.hideable && hidden[c.Title] {
			continue
		}
		cols = append(cols, c.TableColumn)
		ids = append(ids, c.Title)
	}
	return cols, ids
}

// tableTheme makes selected rows a soft tint of the accent while the table
// has focus, as Windows lists do; unfocused selections are a neutral grey.
// Row text keeps its normal color either way.
func tableTheme(c *ui.Context, state *ui.ListState, focused bool, pal palette) func() {
	prev := c.Theme()
	t := *prev
	t.Accent, t.AccentText = pal.selected, prev.Text
	if !focused {
		t.Accent = pal.unfocused
	}
	c.SetTheme(&t)
	return func() { c.SetTheme(prev) }
}

// trackFocus remembers whether a table holds the keyboard focus, read after
// it is built, to color its selection in the next frame.
func (a *App) trackFocus(c *ui.Context, key string, state *ui.ListState) {
	f := state.FocusWithin(c)
	if a.focused[key] != f {
		a.focused[key] = f
		c.Invalidate()
	}
}

// cellBox fills a table cell so the context menu and drag cover all of it.
func cellBox(c *ui.Context, align ui.Align) ui.Element {
	t := c.Theme()
	padY, padX := t.Space(1.5), t.Space(2.5)
	return ui.Row(c).Grow(1).MinWidth(0).Height(t.Space(8)).Margin(-padY, -padX).Padding(padY, padX).AlignItems(ui.Center).Justify(align).Gap(t.Space(1.5))
}

// shareBar draws a share as a bar; a width of 0 lets it fill the cell, so
// widening the column lengthens the bar.
func shareBar(c *ui.Context, share float64, width float32, pal palette) {
	bar := ui.Box(c).Height(10).Shrink(0)
	if width > 0 {
		bar.Width(width)
	} else {
		bar.Grow(1).MinWidth(24).Shrink(1)
	}
	bar.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, pal.barTrack, 1)
		w := r.W * float32(max(0, min(1, share)))
		if share > 0 && w < 1 {
			w = 1 // keep tiny shares visible
		}
		r.W = w
		p.Fill(r, pal.bar, 1)
	})
}

// headerMenu offers column visibility, auto-size and reset.
func (a *App) headerMenu(c *ui.Context, m *ui.Menu, col int, all []column, ids []string, hidden map[string]bool, state *ui.ListState, text func(row int, id string) string, rows int) {
	if col >= 0 && col < len(ids) {
		id := ids[col]
		if m.Item("Size Column to Fit").Chosen() {
			a.autosize(c, state, []string{id}, text, rows)
		}
	}
	if m.Item("Size All Columns to Fit").Chosen() {
		a.autosize(c, state, ids, text, rows)
	}
	m.Separator()
	for _, cdef := range all {
		if !cdef.hideable {
			continue
		}
		title := cdef.Title
		if m.Item(title).Checked(!hidden[title]).Chosen() {
			hidden[title] = !hidden[title]
		}
	}
	m.Separator()
	// The metric sets the percentages and default sorting of these columns.
	m.Submenu("Size Metric", func(sm *ui.Menu) { a.contextItems(sm, "metric.logical", "metric.allocated") })
	m.Separator()
	if m.Item("Reset Columns").Chosen() {
		clear(hidden)
		for k, v := range defaultHidden(all) {
			hidden[k] = v
		}
		state.Columns = ui.TableLayout{}
	}
}

func defaultHidden(all []column) map[string]bool {
	if len(all) == len(treeColumns) && all[0].Title == treeColumns[0].Title && all[3].Title == treeColumns[3].Title {
		return defaultPrefs().TreeHidden
	}
	return defaultPrefs().FileHidden
}

// autosize fits columns to the widest text among the rows in view.
func (a *App) autosize(c *ui.Context, state *ui.ListState, ids []string, text func(row int, id string) string, rows int) {
	t := c.Theme()
	first, last := state.Visible()
	if last < first {
		first, last = 0, min(rows-1, 50)
	}
	if state.Columns.Widths == nil {
		state.Columns.Widths = map[string]float32{}
	}
	for _, id := range ids {
		w, _ := c.MeasureText(0, ui.Span{Text: id, Size: t.FontSize, Weight: 600})
		w += 24 // sort arrow
		for r := first; r <= last && r < rows; r++ {
			tw, _ := c.MeasureText(0, ui.Span{Text: text(r, id), Size: t.FontSize})
			if id == "Name" {
				tw += 60 // indent, arrow and icon
			}
			w = max(w, tw)
		}
		state.Columns.Widths[id] = min(800, w+t.Space(5)+2)
	}
}

func (a *App) itemIcon(c *ui.Context, id index.ID) {
	n := a.snap.Nodes[id]
	name := "file"
	switch {
	case id == 0 && len(a.snap.Root) <= 3:
		name = "drive"
	case n.IsDir():
		name = "folder"
	}
	if b := a.shellIcons[name]; b != nil {
		ui.Image(c, b).Size(16, 16).Shrink(0)
		return
	}
	icon := "document"
	if n.IsDir() {
		icon = "folder_filled"
	}
	ui.Icon(c, icons[icon]).FontSize(15).Shrink(0)
}

// nameText renders an entry's name with its markers.
func (a *App) nameText(c *ui.Context, id index.ID) {
	t := c.Theme()
	n := a.snap.Nodes[id]
	label := a.name(id)
	txt := ui.Text(c, label).SingleLine().MinWidth(0).Shrink(1)
	switch {
	case a.snap.IsMetadata(id):
		txt.TextColor(t.TextMuted).Tooltip("NTFS filesystem metadata. Not an ordinary file; Quarry never offers it for deletion.")
	case n.Flags&index.Shared != 0:
		ui.Text(c, "hard link").FontSize(t.FontSize - 1).TextColor(t.TextMuted).Tooltip("Another name for a file counted elsewhere; its size is counted once.")
	case n.Flags&index.Reparse != 0 && n.IsDir():
		ui.Text(c, "link").FontSize(t.FontSize - 1).TextColor(t.TextMuted).Tooltip("Junction or symbolic link. Not followed, so its target is not counted here.")
	}
	if n.Flags&index.Incomplete != 0 {
		ui.Icon(c, icons["warning"]).FontSize(12).TextColor(t.Warning).Shrink(0).Tooltip("Some contents could not be read; this total may be incomplete.")
	}
}

// dragFrom makes the name cell a drag source of shell items. Explorer
// performs any copy or move; Quarry only marks its results as stale.
func (a *App) dragFrom(e ui.Element, ids func() []index.ID) {
	if a.source != "" {
		return
	}
	e.DragDataFrom(func() transfer.Data {
		paths := a.paths(a.topLevel(ids()))
		data, err := transfer.FileData(paths...)
		if err != nil {
			return transfer.Data{}
		}
		return data
	}, transfer.DragOptions{Operations: transfer.Copy | transfer.Move, Done: func(r transfer.Result) {
		if r.Operation == transfer.Move {
			a.stale = "Items were moved by drag and drop. Rescan (F5) to refresh."
			a.notify("Moved items with Explorer. Rescan to update the results.")
		}
	}})
}

// clickSelect applies Explorer's click rules to a drag-source cell, which
// receives the press instead of the table: click selects one row, Ctrl-click
// toggles, Shift-click selects the range from the focused row.
func clickSelect(cell ui.Element, sel *ui.Selection[index.ID], cursor *int, row int, key func(int) index.ID) bool {
	if !cell.Clicked() {
		return false
	}
	mods := cell.ClickModifiers()
	id := key(row)
	switch {
	case mods&ui.Shift != 0 && *cursor >= 0:
		if mods&ui.Ctrl == 0 {
			sel.Clear()
		}
		lo, hi := min(*cursor, row), max(*cursor, row)
		for i := lo; i <= hi; i++ {
			sel.Add(key(i))
		}
		return true // keep the anchor
	case mods&ui.Ctrl != 0:
		if sel.Has(id) {
			sel.Remove(id)
		} else {
			sel.Add(id)
		}
	default:
		if sel.Len() > 4096 {
			*sel = ui.Selection[index.ID]{}
		} else {
			sel.Clear()
		}
		sel.Add(id)
	}
	*cursor = row
	return true
}

func (a *App) rowMenu(m *ui.Menu) {
	// Everyday commands first; the rest under More, like Explorer's
	// extended menu. Every command keeps its shortcut and menu bar entry.
	a.contextItems(m,
		"item.open", "item.explorer", "-",
		"edit.cut", "edit.copy", "edit.copyPath", "-",
		"item.goInto", "item.reveal", "item.searchHere", "item.scanHere", "-",
		"edit.rename", "edit.delete", "-",
		"item.properties")
	a.moreItems(m,
		"edit.copyName", "edit.copySize", "edit.copyRows", "-",
		"item.treemap", "item.scanWindow", "export.selection", "-",
		"tree.expandAll", "tree.collapseSub", "-",
		"edit.deletePerm")
}

// Tree View.

func (a *App) treeTable(c *ui.Context, pal palette) {
	tr := &a.tree
	t := c.Theme()
	s := a.snap
	cols, ids := visibleColumns(treeColumns, a.prefs.TreeHidden)
	before := tr.sort
	tr.list.Selected = &tr.cursor
	tr.list.Selection = &tr.sel
	tr.list.Sort = &tr.sort
	tr.list.Key = func(i int) any { return tr.rows[i].id }
	tr.list.Label = func(i int) string { return a.name(tr.rows[i].id) }
	text := func(row int, id string) string { return a.treeCellText(tr.rows[row].id, id) }
	tr.list.HeaderMenu = func(m *ui.Menu, col int) {
		a.headerMenu(c, m, col, treeColumns, ids, a.prefs.TreeHidden, &tr.list, text, len(tr.rows))
	}
	indent := 14 * a.zoom()
	restore := tableTheme(c, &tr.list, a.focused["tree"], pal)
	table := ui.Table(c.Key("tree"), &tr.list, cols, len(tr.rows), func(row, col int) {
		if row >= len(tr.rows) {
			return
		}
		r := tr.rows[row]
		id := r.id
		n := s.Nodes[id]
		cell := cellBox(c, cols[col].Align)
		if cell.Hovered() {
			a.hover = id
		}
		cell.ContextMenu(func(m *ui.Menu) {
			if !tr.sel.Has(id) {
				tr.selectOnly(id)
			}
			a.rowMenu(m)
		})
		switch ids[col] {
		case "Name":
			a.dragFrom(cell, func() []index.ID {
				if tr.sel.Has(id) {
					return tr.selectedIDs()
				}
				return []index.ID{id}
			})
			if clickSelect(cell, &tr.sel, &tr.cursor, row, func(i int) index.ID { return tr.rows[i].id }) {
				tr.selCache.invalidate()
				a.focusTable = true
			}
			if cell.DoubleClicked() {
				row := row
				a.post(func() { a.treeActivate(row) })
			}
			cell.Gap(4)
			cell.Children(func() {
				ui.Box(c).Width(float32(min(r.depth, 64)) * indent).Shrink(0)
				arrow := ui.ButtonBase(c.Key(id)).Size(14, 14).Shrink(0).Center().Label("Expand or collapse")
				if r.kids {
					icon := "chevron_right"
					if tr.open[id] {
						icon = "chevron_down"
					}
					arrow.Children(func() { ui.Icon(c, icons[icon]).FontSize(10).TextColor(t.TextMuted) })
					row := row
					arrow.OnClick(func() { tr.toggle(row) })
				}
				a.itemIcon(c, id)
				a.nameText(c, id)
			})
		case "% of Parent":
			parent := n.Parent
			if parent == index.None {
				parent = id
			}
			share := percent(a.metric(n), a.metric(s.Nodes[parent]))
			// Beside the name, the bar answers "what is big here" before any
			// number is read, as WizTree's does.
			cell.Children(func() {
				shareBar(c, share/100, 0, pal)
				ui.Text(c, percentText(share)).SingleLine().Width(46 * a.zoom()).Shrink(0).TextAlign(ui.End).TextColor(t.TextMuted)
			})
		default:
			txt := a.treeCellText(id, ids[col])
			cell.Children(func() {
				el := ui.Text(c, txt).SingleLine()
				if ids[col] != "Size" && ids[col] != a.sizeColumn() {
					el.TextColor(t.TextMuted)
				}
			})
		}
	}).Grow(1).MinHeight(0).Background(pal.grid)
	restore()
	if table.Changed() {
		tr.selCache.invalidate()
		c.Invalidate() // refresh the native menu after the input was applied
	}
	a.trackFocus(c, "tree", &tr.list)
	if a.focusTable && a.prefs.View == viewTree {
		if tr.list.Focus(c) {
			a.focusTable = false
		}
	}
	if tr.sort != before {
		tr.kids = nil
		tr.rebuild()
	}
	if table.Submitted() {
		a.treeActivate(tr.cursor)
	}
	shiftRight, shiftLeft := tr.list.Shortcut(c, ui.Shift, ui.KeyRight), tr.list.Shortcut(c, ui.Shift, ui.KeyLeft)
	right, left, backspace := tr.list.Shortcut(c, 0, ui.KeyRight), tr.list.Shortcut(c, 0, ui.KeyLeft), tr.list.Shortcut(c, 0, ui.KeyBackspace)
	switch {
	case shiftRight:
		tr.expandSubtree(tr.cursorID())
	case shiftLeft:
		tr.collapseSubtree(tr.cursorID())
	case right:
		tr.keyRight()
	case left:
		tr.keyLeft()
	case backspace:
		a.up()
	}
}

// treeActivate is Enter or a double click in Tree View: folders expand or
// collapse, files open with their default application.
func (a *App) treeActivate(row int) {
	tr := &a.tree
	if row < 0 || row >= len(tr.rows) {
		return
	}
	r := tr.rows[row]
	if r.kids {
		tr.toggle(row)
	} else if !a.snap.Nodes[r.id].IsDir() {
		a.exec("item.open")
	}
}

// fileActivate is Enter or a double click in File View: files open, folders
// are revealed in Tree View.
func (a *App) fileActivate(row int) {
	f := &a.files
	if row < 0 || row >= len(f.ids) {
		return
	}
	if id := f.ids[row]; a.snap.Nodes[id].IsDir() {
		a.reveal(id)
	} else {
		a.exec("item.open")
	}
}

// maxCellText bounds cellTextCache; past it the cache starts again.
const maxCellText = 1 << 15

// cellTextCache holds the formatted text of grid cells for one snapshot and
// the display settings that format them. Snapshots are immutable, so an entry
// stays valid until the snapshot, the size units or the size metric change.
// Both grids share it: a cell's text depends only on those inputs.
type cellTextCache struct {
	snap      *index.Snapshot
	units     string
	allocated bool
	text      map[uint64]string
}

func colCode(col string) uint8 {
	switch col {
	case "Name":
		return 1
	case "Size":
		return 2
	case "Allocated":
		return 3
	case "% of Parent":
		return 4
	case "% of Scan":
		return 5
	case "Files":
		return 6
	case "Folders":
		return 7
	case "Modified":
		return 8
	case "Extension":
		return 9
	case "Folder":
		return 10
	default:
		return 0
	}
}

// treeCellText returns a cell's text, formatting it only once per snapshot
// and setting: visible rows are painted every frame.
func (a *App) treeCellText(id index.ID, col string) string {
	c := &a.tree.cells
	if c.snap != a.snap || c.units != a.prefs.Units || c.allocated != a.metricAllocated() || len(c.text) >= maxCellText {
		c.snap, c.units, c.allocated = a.snap, a.prefs.Units, a.metricAllocated()
		if c.text == nil {
			c.text = map[uint64]string{}
		}
		clear(c.text)
	}
	code := colCode(col)
	if code == 0 {
		return a.formatCellText(id, col)
	}
	k := uint64(id)<<8 | uint64(code)
	if s, ok := c.text[k]; ok {
		return s
	}
	s := a.formatCellText(id, col)
	c.text[k] = s
	return s
}

func (a *App) formatCellText(id index.ID, col string) string {
	s := a.snap
	n := s.Nodes[id]
	switch col {
	case "Name":
		return a.name(id)
	case "Size":
		return a.size(n.Size)
	case "Allocated":
		return a.size(n.Allocated)
	case "% of Parent":
		parent := n.Parent
		if parent == index.None {
			parent = id
		}
		return percentText(percent(a.metric(n), a.metric(s.Nodes[parent])))
	case "% of Scan":
		return percentText(percent(a.metric(n), a.metric(s.Nodes[0])))
	case "Files":
		if n.IsDir() {
			return count(uint64(n.Files))
		}
	case "Folders":
		if n.IsDir() {
			return count(uint64(n.Dirs))
		}
	case "Modified":
		return dateText(n.ModTime())
	case "Extension":
		if !n.IsDir() {
			name := s.NameView(id)
			i := strings.LastIndexByte(name, '.')
			if i >= 0 && i < len(name)-1 {
				return strings.ToLower(name[i+1:])
			}
			return ""
		}
	case "Folder":
		if n.Parent != index.None {
			return a.pathOf(n.Parent)
		}
	}
	return ""
}

// File View.

func (a *App) fileTable(c *ui.Context, pal palette) {
	f := &a.files
	t := c.Theme()
	s := a.snap
	cols, ids := visibleColumns(fileColumns, a.prefs.FileHidden)
	before := f.sort
	f.list.Selected = &f.cursor
	f.list.Selection = &f.sel
	f.list.Sort = &f.sort
	f.list.Key = func(i int) any { return f.ids[i] }
	f.list.Label = func(i int) string { return a.name(f.ids[i]) }
	text := func(row int, id string) string { return a.treeCellText(f.ids[row], id) }
	f.list.HeaderMenu = func(m *ui.Menu, col int) {
		a.headerMenu(c, m, col, fileColumns, ids, a.prefs.FileHidden, &f.list, text, len(f.ids))
	}
	restore := tableTheme(c, &f.list, a.focused["files"], pal)
	table := ui.Table(c.Key("files"), &f.list, cols, len(f.ids), func(row, col int) {
		if row >= len(f.ids) {
			return
		}
		id := f.ids[row]
		cell := cellBox(c, cols[col].Align)
		if cell.Hovered() {
			a.hover = id
		}
		cell.ContextMenu(func(m *ui.Menu) {
			if !f.sel.Has(id) {
				f.selectOnly(id)
			}
			a.rowMenu(m)
		})
		switch ids[col] {
		case "Name":
			a.dragFrom(cell, func() []index.ID {
				if f.sel.Has(id) {
					return f.selectedIDs()
				}
				return []index.ID{id}
			})
			if clickSelect(cell, &f.sel, &f.cursor, row, func(i int) index.ID { return f.ids[i] }) {
				f.selCache.invalidate()
				a.focusTable = true
			}
			if cell.DoubleClicked() {
				row := row
				a.post(func() { a.fileActivate(row) })
			}
			cell.Gap(5).Children(func() {
				a.itemIcon(c, id)
				a.nameText(c, id)
			})
		default:
			txt := a.treeCellText(id, ids[col])
			cell.Children(func() {
				el := ui.Text(c, txt).SingleLine()
				if ids[col] == "Folder" {
					el.TextColor(t.TextMuted).Tooltip(txt).MinWidth(0).Shrink(1)
				} else if ids[col] != "Size" && ids[col] != a.sizeColumn() {
					el.TextColor(t.TextMuted)
				}
			})
		}
	}).Grow(1).MinHeight(0).Background(pal.grid)
	table.Children(func() {
		if len(f.ids) == 0 && !f.busy && f.err == "" && s != nil {
			msg := "No files in this scope."
			if strings.TrimSpace(f.ran) != "" {
				msg = "No matches for “" + f.ran + "” in " + f.scopeName() + ". Search is case-insensitive; use * and ? as wildcards (Help → Search Syntax)."
				if a.filter.active() {
					msg += " An analysis filter is also hiding entries."
				}
			}
			ui.Text(c, msg).TextColor(t.TextMuted).Absolute().Top(t.Space(8) + 8).Left(12).MaxWidth(640)
		}
	})
	restore()
	if table.Changed() {
		f.selCache.invalidate()
		c.Invalidate()
	}
	a.trackFocus(c, "files", &f.list)
	if a.focusTable && a.prefs.View == viewFiles {
		if f.list.Focus(c) {
			a.focusTable = false
		}
	}
	if f.sort != before {
		f.invalidate()
	}
	if table.Submitted() {
		a.fileActivate(f.cursor)
	}
}

// Extensions view.

func (a *App) extTable(c *ui.Context, pal palette) {
	e := &a.ext
	t := c.Theme()
	before := e.sort
	if !e.busy {
		e.refresh(a.extFilter)
	}
	e.list.Selected = &e.cursor
	e.list.Sort = &e.sort
	e.list.Key = func(i int) any { return e.types[e.shown[i]].Extension }
	e.list.Label = func(i int) string { return e.types[e.shown[i]].Extension }
	e.list.HeaderMenu = nil
	totalMetric := e.total.Bytes
	if a.metricAllocated() {
		totalMetric = e.total.Allocated
	}
	restore := tableTheme(c, &e.list, a.focused["ext"], pal)
	table := ui.Table(c.Key("ext"), &e.list, extColumns, len(e.shown), func(row, col int) {
		if row >= len(e.shown) {
			return
		}
		ft := e.types[e.shown[row]]
		cell := cellBox(c, extColumns[col].Align)
		cell.ContextMenu(func(m *ui.Menu) {
			e.cursor = row
			if m.Item("Show Files").Shortcut(0, ui.KeyEnter).Chosen() {
				ext := ft.Extension
				a.post(func() { a.showExtensionFiles(ext) })
			}
			if m.Item("Copy Extension").Chosen() {
				a.copyText([]string{ft.Extension}, "extension")
			}
			m.Separator()
			a.contextItems(m, "export.ext")
		})
		metric := ft.Bytes
		if a.metricAllocated() {
			metric = ft.Allocated
		}
		cell.Children(func() {
			switch col {
			case 0:
				ui.Box(c).Size(10, 10).Radius(2).Background(a.swatchColor(ft.Extension)).Shrink(0)
				ui.Text(c, ft.Extension).SingleLine()
			case 1:
				ui.Text(c, platform.CachedFileTypeName(ft.Extension)).TextColor(t.TextMuted).SingleLine()
			case 2:
				ui.Text(c, count(ft.Count)).TextColor(t.TextMuted)
			case 3:
				ui.Text(c, a.size(ft.Bytes))
			case 4:
				ui.Text(c, a.size(ft.Allocated)).TextColor(t.TextMuted)
			case 5:
				share := percent(metric, totalMetric)
				shareBar(c, share/100, 80*a.zoom(), pal)
				ui.Text(c, percentText(share)).SingleLine()
			}
		})
	}).Grow(1).MinHeight(0).Background(pal.grid).Tooltip("Double-click an extension or press Enter to list its files")
	restore()
	a.trackFocus(c, "ext", &e.list)
	if a.focusTable && a.prefs.View == viewExtensions {
		if e.list.Focus(c) {
			a.focusTable = false
		}
	}
	if e.sort != before {
		e.sortShown()
	}
	if table.Submitted() {
		if ext, ok := e.currentExt(); ok {
			a.showExtensionFiles(ext)
		}
	}
}

// File types panel.

var typeColumns = []ui.TableColumn{
	{Title: "File Type", Width: 120, MinWidth: 70},
	{Title: "Size", Width: 76, MinWidth: 50, Align: ui.End},
	{Title: "% of Files", MinWidth: 80},
}

// typesPanel lists the kinds of files in the scope beside Tree View,
// largest first. Its swatches are the treemap's colors, so it is also the
// treemap's legend.
func (a *App) typesPanel(c *ui.Context, pal palette) {
	e := &a.ext
	t := c.Theme()
	e.panel.Selected = &e.panelCursor
	e.panel.Sort = nil
	e.panel.Key = func(i int) any { return e.types[e.bySize[i]].Extension }
	e.panel.Label = func(i int) string { return e.types[e.bySize[i]].Extension }
	e.panel.HeaderMenu = func(m *ui.Menu, col int) { a.contextItems(m, "view.types") }
	total := e.total.Bytes
	if a.metricAllocated() {
		total = e.total.Allocated
	}
	open := func(row int) {
		if row >= 0 && row < len(e.bySize) {
			ext := e.types[e.bySize[row]].Extension
			a.post(func() { a.showExtensionFiles(ext) })
		}
	}
	restore := tableTheme(c, &e.panel, a.focused["types"], pal)
	table := ui.Table(c.Key("types"), &e.panel, typeColumns, len(e.bySize), func(row, col int) {
		if row >= len(e.bySize) {
			return
		}
		ft := e.types[e.bySize[row]]
		cell := cellBox(c, typeColumns[col].Align)
		if cell.DoubleClicked() {
			open(row)
		}
		cell.ContextMenu(func(m *ui.Menu) {
			e.panelCursor = row
			if m.Item("Show Files").Shortcut(0, ui.KeyEnter).Chosen() {
				open(row)
			}
			if m.Item("Copy Extension").Chosen() {
				a.copyText([]string{ft.Extension}, "extension")
			}
			m.Separator()
			a.contextItems(m, "view.ext", "view.types")
		})
		metric := ft.Bytes
		if a.metricAllocated() {
			metric = ft.Allocated
		}
		cell.Children(func() {
			switch col {
			case 0:
				ui.Box(c).Size(10, 10).Radius(2).Background(a.swatchColor(ft.Extension)).Shrink(0)
				ui.Text(c, ft.Extension).SingleLine().MinWidth(0).Shrink(1)
				cell.Tooltip(platform.CachedFileTypeName(ft.Extension) + "\n" + plural(ft.Count, "file", "files") + " · double-click to list them")
			case 1:
				ui.Text(c, a.size(metric)).SingleLine()
			case 2:
				share := percent(metric, total)
				shareBar(c, share/100, 0, pal)
				ui.Text(c, percentText(share)).SingleLine().Width(46 * a.zoom()).Shrink(0).TextAlign(ui.End).TextColor(t.TextMuted)
			}
		})
	}).Grow(1).MinHeight(0).Background(pal.grid)
	table.Children(func() {
		msg := ""
		switch {
		case e.err != "":
			msg = e.err
		case len(e.bySize) == 0 && e.busy:
			msg = "Analyzing…"
		case len(e.bySize) == 0:
			msg = "No files here."
		}
		if msg != "" {
			ui.Text(c, msg).TextColor(t.TextMuted).Absolute().Top(t.Space(8) + 8).Left(12)
		}
	})
	restore()
	a.trackFocus(c, "types", &e.panel)
	if table.Submitted() {
		open(e.panelCursor)
	}
}
