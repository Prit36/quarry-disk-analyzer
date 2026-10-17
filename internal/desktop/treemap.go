package desktop

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
	"quarry/internal/treemap"
)

// treemapModel caches the treemap of the current location. The hierarchy is
// built off the UI thread; the geometry is recomputed only on resize.
type treemapModel struct {
	a         *App
	root      index.ID
	forSnap   *index.Snapshot
	allocated bool
	branches  []treemap.Branch
	total     uint64
	cells     []treemap.Cell
	cellOf    map[index.ID]int
	w, h      float32
	busy      bool
	gen       uint64
	cancel    context.CancelFunc
	dirty     bool
	maximized bool
	highlight index.ID

	// menu is the tile the context menu was opened on, recorded as the
	// secondary button goes down: the menu outlives the hover.
	menu    tileKey
	menuSet bool

	ranks   map[string]int // extension → color slot, by size across the scan
	rankFor *index.Snapshot
	// dominant is the largest extension inside each block that stands for
	// several files (a folder not subdivided further, or "Smaller items"),
	// so even aggregated blocks show what fills them.
	dominant map[tileKey]string
}

type tileKey struct {
	id    index.ID
	other bool
}

// dominantExtensions finds, for every aggregated block, the extension that
// holds the most bytes inside it. Each entry is visited at most once.
func dominantExtensions(ctx context.Context, s *index.Snapshot, branches []treemap.Branch, allocated bool) map[tileKey]string {
	out := map[tileKey]string{}
	tally := map[string]uint64{}
	add := func(root index.ID) {
		_ = s.Visit(ctx, root, func(id index.ID) error {
			n := s.Nodes[id]
			if n.IsDir() {
				return nil
			}
			v := n.Size
			if allocated {
				v = n.Allocated
			}
			tally[strings.ToLower(filepath.Ext(s.NameView(id)))] += v
			return nil
		})
	}
	best := func() string {
		ext, top := "", uint64(0)
		for e, v := range tally {
			if v > top || v == top && e < ext {
				ext, top = e, v
			}
		}
		clear(tally)
		if ext == "" {
			ext = "(no extension)"
		}
		return ext
	}
	var walk func(level []treemap.Branch, parent index.ID)
	walk = func(level []treemap.Branch, parent index.ID) {
		shown := map[index.ID]bool{}
		for _, b := range level {
			if !b.Item.Other {
				shown[b.Item.ID] = true
			}
		}
		for _, b := range level {
			if ctx.Err() != nil {
				return
			}
			switch {
			case b.Item.Other:
				for c := s.Nodes[b.Item.ID].FirstChild; c != index.None; c = s.Nodes[c].NextSibling {
					if !shown[c] {
						add(c)
					}
				}
				out[tileKey{b.Item.ID, true}] = best()
			case len(b.Children) > 0:
				walk(b.Children, b.Item.ID)
			case s.Nodes[b.Item.ID].IsDir():
				add(b.Item.ID)
				out[tileKey{b.Item.ID, false}] = best()
			}
		}
	}
	walk(branches, index.None)
	return out
}

func (m *treemapModel) reset() {
	if m.cancel != nil {
		m.cancel()
	}
	a := m.a
	*m = treemapModel{dirty: true, highlight: index.None, maximized: m.maximized}
	m.a = a
}

func (m *treemapModel) invalidate() { m.dirty = true }

func (m *treemapModel) ensure() {
	a := m.a
	s := a.mapSnapshot()
	if s == nil {
		return
	}
	root := a.location
	if !s.Reachable(root) {
		root = 0
	}
	if !m.dirty && m.forSnap == s && m.root == root && m.allocated == a.metricAllocated() {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.dirty = false
	m.forSnap, m.root, m.allocated = s, root, a.metricAllocated()
	m.gen++
	gen := m.gen
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.busy = true
	needRanks := m.rankFor != s
	allocated := m.allocated
	go func() {
		branches := treemap.Hierarchy(ctx, s, root, allocated, 6000)
		dominant := dominantExtensions(ctx, s, branches, allocated)
		var ranks map[string]int
		if needRanks {
			if summary, err := s.Summarize(ctx); err == nil {
				ranks = map[string]int{}
				for i, t := range summary.Types {
					if i >= len(categorical) {
						break
					}
					ranks[t.Extension] = i
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != m.gen {
				return
			}
			m.busy = false
			m.branches, m.cells, m.w, m.dominant = branches, nil, 0, dominant
			m.total = 0
			for _, b := range branches {
				m.total += b.Item.Bytes
			}
			if ranks != nil {
				m.ranks, m.rankFor = ranks, s
			}
		})
	}()
}

func (m *treemapModel) layout(w, h float32) {
	if w == m.w && h == m.h && m.cells != nil {
		return
	}
	m.w, m.h = w, h
	m.cells = treemap.NestedLayout(m.branches, treemap.Rect{X: 0, Y: 0, W: max(0, w), H: max(0, h)})
	m.cellOf = make(map[index.ID]int, len(m.cells))
	for i, c := range m.cells {
		if !c.Tile.Other {
			m.cellOf[c.Tile.ID] = i
		}
	}
}

func (m *treemapModel) hit(x, y float32) int {
	for i := len(m.cells) - 1; i >= 0; i-- {
		r := m.cells[i].Tile.Rect
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			return i
		}
	}
	return -1
}

// extColor is the color of an extension, shared by the treemap and the
// Extensions view: the largest extensions get distinct colors.
func (a *App) extColor(ext string) ui.Color {
	if rank, ok := a.tm.ranks[ext]; ok {
		return categorical[rank]
	}
	return hashColor(ext)
}

func (m *treemapModel) color(s *index.Snapshot, cell treemap.Cell, dark bool) ui.Color {
	neutral := ui.Hex("#6B6B6B")
	if !dark {
		neutral = ui.Hex("#A9A9A9")
	}
	id := cell.Tile.ID
	if m.a.prefs.TreemapColors == "Extension" {
		if ext, ok := m.dominant[tileKey{id, cell.Tile.Other}]; ok {
			// Aggregated blocks are a little darker than single files.
			return m.a.extColor(ext).Mix(ui.Hex("#000000"), .22)
		}
	}
	if cell.Tile.Other {
		return neutral.Mix(ui.Hex("#000000"), .2)
	}
	n := s.Nodes[id]
	switch m.a.prefs.TreemapColors {
	case "Mono":
		return neutral.Mix(ui.Hex("#FFFFFF"), float32(cell.Depth%4)*.06)
	case "Folder":
		top := id
		for top != index.None && s.Nodes[top].Parent != m.root && top != m.root {
			top = s.Nodes[top].Parent
		}
		slot := 0
		for i, b := range m.branches {
			if b.Item.ID == top {
				slot = i
				break
			}
		}
		c := categorical[slot%len(categorical)]
		return c.Mix(ui.Hex("#000000"), float32(cell.Depth)*.07)
	}
	if n.IsDir() {
		return neutral
	}
	ext := strings.ToLower(filepath.Ext(s.NameView(id)))
	if ext == "" {
		ext = "(no extension)"
	}
	return m.a.extColor(ext)
}

// cellExt is the extension a tile stands for: a file's own, or the largest
// inside an aggregated block. Folders subdivided further have none.
func (m *treemapModel) cellExt(s *index.Snapshot, cell treemap.Cell) string {
	if ext, ok := m.dominant[tileKey{cell.Tile.ID, cell.Tile.Other}]; ok {
		return ext
	}
	if cell.Tile.Other || cell.Group || s.Nodes[cell.Tile.ID].IsDir() {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(s.NameView(cell.Tile.ID)))
	if ext == "" {
		ext = "(no extension)"
	}
	return ext
}

// hoverLines describes a tile for the pointer tip: a title and details.
func (a *App) hoverLines(s *index.Snapshot, cell treemap.Cell) (string, []string) {
	m := &a.tm
	if cell.Tile.Other {
		lines := []string{a.size(cell.Tile.Bytes) + " · " + percentText(percent(cell.Tile.Bytes, m.total)) + " of " + a.shortName(m.root)}
		if ext, ok := m.dominant[tileKey{cell.Tile.ID, true}]; ok {
			lines = append(lines, "Mostly "+ext)
		}
		lines = append(lines, "Too small to draw one by one. Double-click to open the folder.")
		return "Smaller items in " + a.name(cell.Tile.ID), lines
	}
	id := cell.Tile.ID
	n := s.Nodes[id]
	lines := []string{truncateMiddle(a.pathOf(id), 90)}
	size := a.size(n.Size)
	if a.size(n.Allocated) != a.size(n.Size) {
		size += "  (" + a.size(n.Allocated) + " on disk)"
	}
	lines = append(lines, size+" · "+percentText(percent(a.metric(n), m.total))+" of "+a.shortName(m.root))
	if n.IsDir() {
		line := plural(uint64(n.Files), "file", "files") + ", " + plural(uint64(n.Dirs), "folder", "folders")
		if ext, ok := m.dominant[tileKey{id, false}]; ok {
			line += " · mostly " + ext
		}
		lines = append(lines, line)
	} else if ext := m.cellExt(s, cell); ext != "" {
		if tn := platform.FileTypeName(ext); tn != "" {
			ext += " (" + tn + ")"
		}
		lines = append(lines, ext)
	}
	if d := dateText(n.Modified); d != "" {
		lines = append(lines, "Modified "+d)
	}
	if s.IsMetadata(id) {
		lines = append(lines, "NTFS metadata: reserved by the filesystem, not reclaimable.")
	}
	return a.name(id), lines
}

// treemapView draws the map. Click selects the item in Tree View, a double
// click on a folder goes into it, right-click offers the item commands.
// The tile under the pointer is outlined and described in a tip that
// follows the pointer, without the delay of an ordinary tooltip.
func (a *App) treemapView(c *ui.Context, pal palette) {
	m := &a.tm
	m.ensure()
	t := c.Theme()
	s := m.forSnap
	box := ui.Box(c.Key("treemap")).Grow(1).MinHeight(0).Clip().Focusable().Label("Treemap").Background(pal.grid)
	bounds := box.Bounds()
	if s == nil {
		return
	}
	if bounds.W <= 0 {
		c.Invalidate() // laid out in the next frame
		return
	}
	m.layout(bounds.W, bounds.H)
	// The context menu's tile is the one under the pointer as the
	// secondary button goes down: its item is chosen later, with the
	// pointer elsewhere.
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputPointerDown && ev.Button == 1 {
			m.menuSet = false
			if i := m.hit(ev.X, ev.Y); i >= 0 && !m.busy {
				m.menu, m.menuSet = tileKey{m.cells[i].Tile.ID, m.cells[i].Tile.Other}, true
			}
		}
		return false
	})
	px, py, over := box.PointerPosition()
	hover := -1
	if over && !m.busy {
		hover = m.hit(px, py)
	}
	if hover >= 0 {
		cell := m.cells[hover]
		a.hover, a.hoverOther = cell.Tile.ID, cell.Tile.Other
		if box.Clicked() {
			m.highlight = cell.Tile.ID
			a.reveal(cell.Tile.ID)
		}
		if box.DoubleClicked() {
			if id := cell.Tile.ID; s.Nodes[id].IsDir() {
				a.navigate(id)
			}
		}
	}
	box.ContextMenu(func(menu *ui.Menu) {
		if !m.menuSet || int(m.menu.id) >= len(s.Nodes) {
			return
		}
		id, other := m.menu.id, m.menu.other
		title := a.name(id)
		if other {
			title = "Smaller items in " + title
		}
		menu.Item(truncateMiddle(title, 60)).Disabled(true)
		menu.Separator()
		if a.snap == nil || !a.snap.Reachable(id) {
			menu.Item("Hidden by the analysis filter").Disabled(true)
			return
		}
		// Commands act on the selection: make it this tile, once.
		if a.prefs.View != viewTree || a.tree.cursorID() != id || a.tree.sel.Len() != 1 || !a.tree.sel.Has(id) {
			m.highlight = id
			a.reveal(id)
		}
		if s.Nodes[id].IsDir() && id != a.location && menu.Item("Go Into Folder").Chosen() {
			a.post(func() { a.navigate(id) })
		}
		a.contextItems(menu, "-", "item.open", "item.explorer", "-", "edit.copy", "edit.copyPath", "edit.copyName", "-", "nav.up", "item.searchHere", "item.scanHere", "-", "edit.delete", "-", "item.properties")
	})
	if box.Shortcut(0, ui.KeyBackspace) {
		a.up()
	}
	selected := a.current()
	if a.prefs.View == viewExtensions || selected == index.None {
		selected = m.highlight
	}
	mark := -1
	for id := selected; id != index.None && int(id) < len(s.Nodes); id = s.Nodes[id].Parent {
		if i, ok := m.cellOf[id]; ok {
			mark = i
			break
		}
		if id == m.root {
			break
		}
	}
	// In Extensions view the chosen extension stands out; the rest dims.
	focusExt := ""
	if a.prefs.View == viewExtensions {
		focusExt, _ = a.ext.currentExt()
	}
	var tipTitle string
	var tipLines []string
	if hover >= 0 {
		tipTitle, tipLines = a.hoverLines(s, m.cells[hover])
	}
	cells := m.cells
	dark := t.Dark
	busy := m.busy
	metric := strings.ToLower(a.prefs.Metric)
	box.Draw(func(p *ui.Painter, origin ui.Rect) {
		if busy && len(cells) == 0 {
			p.Text(origin.X+8, origin.Y+6, "Building treemap…", 12, t.TextMuted)
			return
		}
		if len(cells) == 0 {
			p.Text(origin.X+8, origin.Y+6, "Nothing to show: this folder has no data for the "+metric+" size metric.", 12, t.TextMuted)
			return
		}
		edge := ui.Hex("#000000").Alpha(.35)
		if !dark {
			edge = ui.Hex("#FFFFFF").Alpha(.7)
		}
		for i, cell := range cells {
			r := cell.Tile.Rect
			if r.W < 1 || r.H < 1 {
				continue
			}
			rect := ui.Rect{X: origin.X + r.X, Y: origin.Y + r.Y, W: r.W, H: r.H}
			col := m.color(s, cell, dark)
			if cell.Group {
				// Folder frame: the header strip carries the name.
				frame := col.Mix(ui.Hex("#000000"), .35)
				if a.prefs.TreemapColors == "Extension" || a.prefs.TreemapColors == "Mono" {
					frame = ui.Hex("#3A3A3A")
					if !dark {
						frame = ui.Hex("#D6D6D6")
					}
				}
				if i == hover {
					frame = frame.Mix(ui.Hex("#FFFFFF"), .15)
				}
				p.Fill(rect, frame, 0)
				if cell.Header >= 14 && rect.W > 40 {
					label := a.name(cell.Tile.ID) + "  " + a.size(cell.Tile.Bytes)
					p.Clip(ui.Rect{X: rect.X + 3, Y: rect.Y, W: rect.W - 6, H: cell.Header}, 0, func() {
						p.Text(rect.X+4, rect.Y+1, label, 11, readableOn(frame))
					})
				}
				continue
			}
			if focusExt != "" && m.cellExt(s, cell) != focusExt {
				col = col.Mix(pal.grid, .75)
			}
			if i == hover {
				col = col.Mix(ui.Hex("#FFFFFF"), .18)
			}
			p.FillGradient(rect, ui.LinearGradient{From: col.Mix(ui.Hex("#FFFFFF"), .10), To: col.Mix(ui.Hex("#000000"), .18), Angle: 135}, 0)
			if rect.W > 2 && rect.H > 2 {
				p.Stroke(rect, edge, 0, 1)
			}
			if rect.W > 48 && rect.H > 16 {
				name := "Smaller items"
				if !cell.Tile.Other {
					name = a.name(cell.Tile.ID)
				}
				fg := readableOn(col)
				p.Clip(ui.Rect{X: rect.X + 2, Y: rect.Y, W: rect.W - 4, H: rect.H}, 0, func() {
					p.Text(rect.X+4, rect.Y+2, name, 11, fg)
					if rect.H > 32 {
						p.Text(rect.X+4, rect.Y+16, a.size(cell.Tile.Bytes), 11, fg.Alpha(.8))
					}
				})
			}
		}
		if mark >= 0 {
			r := cells[mark].Tile.Rect
			rect := ui.Rect{X: origin.X + r.X, Y: origin.Y + r.Y, W: r.W, H: r.H}
			p.Stroke(rect, ui.Hex("#000000"), 0, 3)
			p.Stroke(rect, ui.Hex("#FFFFFF"), 0, 1.5)
		}
		if hover < 0 {
			return
		}
		// Outline the tile under the pointer, then the tip beside the pointer.
		r := cells[hover].Tile.Rect
		rect := ui.Rect{X: origin.X + r.X + 1, Y: origin.Y + r.Y + 1, W: max(1, r.W-2), H: max(1, r.H-2)}
		p.Stroke(rect, t.Focus, 0, 2)
		const pad, line = 8, 16
		titleSpan := ui.Span{Text: tipTitle, Size: 12, Weight: 600, Color: t.Text}
		w, _ := p.MeasureText(0, titleSpan)
		for _, l := range tipLines {
			lw, _ := p.MeasureText(0, ui.Span{Text: l, Size: 11.5})
			w = max(w, lw)
		}
		w = min(w+2*pad, max(120, origin.W-8))
		h := float32(pad*2 + line*(1+len(tipLines)))
		x, y := origin.X+px+16, origin.Y+py+18
		if x+w > origin.X+origin.W-4 {
			x = origin.X + px - w - 12
		}
		if y+h > origin.Y+origin.H-4 {
			y = origin.Y + py - h - 10
		}
		x = max(origin.X+4, x)
		y = max(origin.Y+4, y)
		tip := ui.Rect{X: x, Y: y, W: w, H: h}
		bg, border := ui.Hex("#2B2B2B"), ui.Hex("#454545")
		if !dark {
			bg, border = ui.Hex("#FFFFFF"), ui.Hex("#C8C8C8")
		}
		p.Shadow(tip, 6, 0, 3, 12, 0, ui.RGBA(0, 0, 0, 0.35))
		p.Fill(tip, bg, 6)
		p.Stroke(tip, border, 6, 1)
		p.Clip(ui.Rect{X: x + pad, Y: y, W: w - 2*pad, H: h}, 0, func() {
			p.RichText(x+pad, y+pad-1, 0, titleSpan)
			for i, l := range tipLines {
				color := t.TextMuted
				if i == 1 {
					color = t.Text // the size line
				}
				p.Text(x+pad, y+pad+float32(line*(i+1)), l, 11.5, color)
			}
		})
	})
}
