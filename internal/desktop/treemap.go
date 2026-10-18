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

	// styles holds each cell's colors and texts, derived once per layout
	// and setting rather than on every frame the pointer moves.
	styles   []cellStyle
	styleFor styleKey
	styleGen uint64 // changes with the cells, ranks or dominant extensions
	// dominant is the largest extension inside each block that stands for
	// several files (a folder not subdivided further, or "Smaller items"),
	// so even aggregated blocks show what fills them.
	dominant map[tileKey]string
}

type tileKey struct {
	id    index.ID
	other bool
}

type cellStyle struct {
	color ui.Color // tile fill, or a group's frame
	text  ui.Color
	// lightEnd and darkEnd are the tile's gradient ends for color, so the
	// paint loop does not derive them for every tile on every frame.
	lightEnd, darkEnd ui.Color
	name              string // the label: a group's includes its size
	size              string
	ext               string // cellExt
}

type styleKey struct {
	gen           uint64 // cells, ranks and dominant extensions
	colors, units string
	dark          bool
}

// ensureStyles derives the cells' colors and labels when the layout or a
// setting they depend on changed.
func (m *treemapModel) ensureStyles(s *index.Snapshot, dark bool) {
	a := m.a
	key := styleKey{gen: m.styleGen, colors: a.prefs.TreemapColors, units: a.prefs.Units, dark: dark}
	if m.styles != nil && key == m.styleFor {
		return
	}
	m.styleFor = key
	m.styles = make([]cellStyle, len(m.cells))
	for i, cell := range m.cells {
		st := &m.styles[i]
		col := m.color(s, cell, dark)
		if cell.Group {
			frame := col.Mix(ui.Hex("#000000"), .35)
			if a.prefs.TreemapColors == "Extension" || a.prefs.TreemapColors == "Mono" {
				frame = ui.Hex("#3A3A3A")
				if !dark {
					frame = ui.Hex("#D6D6D6")
				}
			}
			st.color, st.text = frame, readableOn(frame)
			st.name = a.name(cell.Tile.ID) + "  " + a.size(cell.Tile.Bytes)
			continue
		}
		st.color, st.text = col, readableOn(col)
		st.lightEnd, st.darkEnd = col.Mix(ui.Hex("#FFFFFF"), .10), col.Mix(ui.Hex("#000000"), .18)
		st.name = "Smaller items"
		if !cell.Tile.Other {
			st.name = a.name(cell.Tile.ID)
		}
		st.size = a.size(cell.Tile.Bytes)
		st.ext = m.cellExt(s, cell)
	}
}

// dominantExtensions finds, for every aggregated block, the extension that
// holds the most bytes inside it. Each entry is visited at most once.
func dominantExtensions(ctx context.Context, s *index.Snapshot, branches []treemap.Branch, allocated bool) map[tileKey]string {
	out := map[tileKey]string{}
	tally := map[string]uint64{}
	extensions := map[string]string{}
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
			raw := filepath.Ext(s.NameView(id))
			ext, ok := extensions[raw]
			if !ok {
				ext = strings.ToLower(raw)
				extensions[raw] = ext
			}
			tally[ext] += v
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
	*m = treemapModel{dirty: true, highlight: index.None, maximized: m.maximized, gen: m.gen + 1}
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
				m.styleGen++
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
	m.styleGen++
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

// tipInfo is what the pointer tip shows for a tile: a title, what kind of
// item it is with its share of the map on the right, labelled facts in
// reading order, and an optional note.
type tipInfo struct {
	title, kind string
	aside       string
	rows        [][2]string
	hint        string
}

// hoverInfo describes a tile for the pointer tip.
func (a *App) hoverInfo(s *index.Snapshot, cell treemap.Cell) tipInfo {
	m := &a.tm
	of := " of " + a.shortName(m.root)
	if cell.Tile.Other {
		info := tipInfo{
			title: "Smaller items",
			kind:  "Grouped in " + a.name(cell.Tile.ID),
			aside: percentText(percent(cell.Tile.Bytes, m.total)) + of,
			hint:  "Too small to draw one by one.\nDouble-click to open the folder.",
		}
		info.rows = append(info.rows, [2]string{"Size", a.size(cell.Tile.Bytes)})
		if ext, ok := m.dominant[tileKey{cell.Tile.ID, true}]; ok {
			info.rows = append(info.rows, [2]string{"Mostly", ext})
		}
		return info
	}
	id := cell.Tile.ID
	n := s.Nodes[id]
	info := tipInfo{title: a.name(id), aside: percentText(percent(a.metric(n), m.total)) + of}
	info.rows = append(info.rows, [2]string{"Size", a.size(n.Size)})
	if a.size(n.Allocated) != a.size(n.Size) {
		info.rows = append(info.rows, [2]string{"On disk", a.size(n.Allocated)})
	}
	if n.IsDir() {
		info.kind = "Folder"
		info.rows = append(info.rows, [2]string{"Contains", plural(uint64(n.Files), "file", "files") + ", " + plural(uint64(n.Dirs), "folder", "folders")})
		if ext, ok := m.dominant[tileKey{id, false}]; ok {
			info.rows = append(info.rows, [2]string{"Mostly", ext})
		}
	} else {
		info.kind = "File"
		if ext := m.cellExt(s, cell); ext != "" {
			if tn := platform.FileTypeName(ext); tn != "" {
				info.kind = tn
			}
			info.rows = append(info.rows, [2]string{"Type", ext})
		}
	}
	if d := dateText(n.ModTime()); d != "" {
		info.rows = append(info.rows, [2]string{"Modified", d})
	}
	if s.IsMetadata(id) {
		info.hint = "NTFS metadata: reserved by the\nfilesystem, not reclaimable."
	}
	return info
}

// fitMiddle shortens s with an ellipsis in the middle until it measures at
// most maxW at the given size, so a path keeps its start and its end.
func fitMiddle(p *ui.Painter, s string, size, maxW float32) string {
	return fitMiddleText(s, maxW, func(text string) float32 {
		w, _ := p.MeasureText(0, ui.Span{Text: text, Size: size})
		return w
	})
}

// Measure logarithmically many candidates and decode Unicode only once.
// The previous loop shaped and allocated every shorter version of a long
// filename on every pointer frame. Keep a measured, fitting candidate even
// when kerning makes widths slightly non-monotonic.
func fitMiddleText(s string, maxW float32, measure func(string) float32) string {
	if measure(s) <= maxW {
		return s
	}
	runes := []rune(s)
	best := "…"
	lo, hi := 1, (len(runes)-1)/2
	for lo <= hi {
		half := lo + (hi-lo)/2
		candidate := string(runes[:half]) + "…" + string(runes[len(runes)-half:])
		if measure(candidate) <= maxW {
			best, lo = candidate, half+1
		} else {
			hi = half - 1
		}
	}
	return best
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
	var tip tipInfo
	if hover >= 0 {
		tip = a.hoverInfo(s, m.cells[hover])
	}
	cells := m.cells
	m.ensureStyles(s, t.Dark)
	styles := m.styles
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
			st := &styles[i]
			col, fg := st.color, st.text
			if cell.Group {
				// Folder frame: the header strip carries the name.
				if i == hover {
					col = col.Mix(ui.Hex("#FFFFFF"), .15)
					fg = readableOn(col)
				}
				p.Fill(rect, col, 0)
				if cell.Header >= 14 && rect.W > 40 {
					p.Clip(ui.Rect{X: rect.X + 3, Y: rect.Y, W: rect.W - 6, H: cell.Header}, 0, func() {
						p.Text(rect.X+4, rect.Y+1, st.name, 11, fg)
					})
				}
				continue
			}
			lightEnd, darkEnd := st.lightEnd, st.darkEnd
			if focusExt != "" && st.ext != focusExt {
				col = col.Mix(pal.grid, .75)
				fg = readableOn(col)
				lightEnd, darkEnd = col.Mix(ui.Hex("#FFFFFF"), .10), col.Mix(ui.Hex("#000000"), .18)
			}
			if i == hover {
				col = col.Mix(ui.Hex("#FFFFFF"), .18)
				fg = readableOn(col)
				lightEnd, darkEnd = col.Mix(ui.Hex("#FFFFFF"), .10), col.Mix(ui.Hex("#000000"), .18)
			}
			p.FillGradient(rect, ui.LinearGradient{From: lightEnd, To: darkEnd, Angle: 135}, 0)
			if rect.W > 2 && rect.H > 2 {
				p.Stroke(rect, edge, 0, 1)
			}
			if rect.W > 48 && rect.H > 16 {
				p.Clip(ui.Rect{X: rect.X + 2, Y: rect.Y, W: rect.W - 4, H: rect.H}, 0, func() {
					p.Text(rect.X+4, rect.Y+2, st.name, 11, fg)
					if rect.H > 32 {
						p.Text(rect.X+4, rect.Y+16, st.size, 11, fg.Alpha(.8))
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
		// A plain card: title, kind, a rule, then aligned label and value rows.
		const pad, rowH, labelW, maxW = 10, 17, 60, 300
		titleSpan := ui.Span{Text: tip.title, Size: 12.5, Weight: 600, Color: t.Text}
		w, _ := p.MeasureText(0, titleSpan)
		kindW, _ := p.MeasureText(0, ui.Span{Text: tip.kind, Size: 11.5})
		asideW, _ := p.MeasureText(0, ui.Span{Text: tip.aside, Size: 11.5})
		w = max(w, kindW+asideW+12, 200)
		for _, r := range tip.rows {
			vw, _ := p.MeasureText(0, ui.Span{Text: r[1], Size: 11.5})
			w = max(w, labelW+min(vw, maxW-2*pad-labelW))
		}
		hint := strings.Split(tip.hint, "\n")
		if tip.hint == "" {
			hint = nil
		}
		for _, l := range hint {
			lw, _ := p.MeasureText(0, ui.Span{Text: l, Size: 11})
			w = max(w, lw)
		}
		w = min(w+2*pad, min(maxW, max(180, origin.W-8)))
		h := float32(pad*2 + 17 + 15)
		if len(tip.rows) > 0 {
			h += float32(9 + len(tip.rows)*rowH)
		}
		if len(hint) > 0 {
			h += float32(8 + 14*len(hint))
		}
		x, y := origin.X+px+16, origin.Y+py+18
		if x+w > origin.X+origin.W-4 {
			x = origin.X + px - w - 12
		}
		if y+h > origin.Y+origin.H-4 {
			y = origin.Y + py - h - 10
		}
		x = max(origin.X+4, x)
		y = max(origin.Y+4, y)
		card := ui.Rect{X: x, Y: y, W: w, H: h}
		bg, border := ui.Hex("#2B2B2B"), ui.Hex("#4A4A4A")
		if !dark {
			bg, border = ui.Hex("#FFFFFF"), ui.Hex("#C8C8C8")
		}
		p.Shadow(card, 4, 0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.25))
		p.Fill(card, bg, 4)
		p.Stroke(card, border, 4, 1)
		cx, cy, cw := x+pad, y+pad, w-2*pad
		valueW := cw - labelW
		title := titleSpan
		title.Text = fitMiddle(p, tip.title, 12.5, cw)
		p.Clip(card, 0, func() {
			p.Clip(ui.Rect{X: cx, Y: cy - 2, W: cw, H: 20}, 0, func() {
				p.RichText(cx, cy-1, 0, title)
			})
			cy += 17
			p.Clip(ui.Rect{X: cx, Y: cy, W: cw - asideW - 12, H: 16}, 0, func() {
				p.Text(cx, cy, tip.kind, 11.5, t.TextMuted)
			})
			p.Text(cx+cw-asideW, cy, tip.aside, 11.5, t.TextMuted)
			cy += 15
			if len(tip.rows) > 0 {
				p.Fill(ui.Rect{X: cx, Y: cy + 3, W: cw, H: 1}, border, 0)
				cy += 9
			}
			for _, r := range tip.rows {
				p.Text(cx, cy, r[0], 11.5, t.TextMuted)
				p.Clip(ui.Rect{X: cx + labelW, Y: cy, W: cw - labelW, H: rowH}, 0, func() {
					p.Text(cx+labelW, cy, fitMiddle(p, r[1], 11.5, valueW), 11.5, t.Text)
				})
				cy += rowH
			}
			if len(hint) > 0 {
				cy += 8
				for _, l := range hint {
					p.Text(cx, cy, l, 11, t.TextMuted)
					cy += 14
				}
			}
		})
	})
}
