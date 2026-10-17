package desktop

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
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

func (a *App) treemapTooltip(s *index.Snapshot, cell treemap.Cell) string {
	m := &a.tm
	if cell.Tile.Other {
		tip := "Smaller items in " + a.name(cell.Tile.ID) + ": " + a.size(cell.Tile.Bytes)
		if ext, ok := m.dominant[tileKey{cell.Tile.ID, true}]; ok {
			tip += "\nMostly " + ext
		}
		return tip + "\nDouble-click to open the folder."
	}
	id := cell.Tile.ID
	n := s.Nodes[id]
	lines := []string{a.name(id), a.pathOf(id), "Size: " + a.size(n.Size) + "    Allocated: " + a.size(n.Allocated), percentText(percent(a.metric(n), m.total)) + " of " + a.name(m.root)}
	if !n.IsDir() {
		if ext := filepath.Ext(s.NameView(id)); ext != "" {
			lines = append(lines, "Type: "+strings.ToLower(ext))
		}
	} else {
		line := plural(uint64(n.Files), "file", "files")
		if ext, ok := m.dominant[tileKey{id, false}]; ok {
			line += ", mostly " + ext
		}
		lines = append(lines, line)
	}
	if s.IsMetadata(id) {
		lines = append(lines, "NTFS metadata. Its logical size can describe reserved space rather than data; it is not reclaimable.")
	}
	return strings.Join(lines, "\n")
}

// treemapView draws the map. Click selects the item in Tree View, a double
// click on a folder goes into it, right-click offers the item commands.
func (a *App) treemapView(c *ui.Context, pal palette) {
	m := &a.tm
	m.ensure()
	t := c.Theme()
	s := m.forSnap
	box := ui.Box(c.Key("treemap")).Grow(1).MinHeight(0).Clip().Focusable().Label("Treemap").Background(pal.grid)
	bounds := box.Bounds()
	if bounds.W <= 0 || s == nil {
		c.Invalidate()
		return
	}
	m.layout(bounds.W, bounds.H)
	px, py, over := box.PointerPosition()
	hover := -1
	if over && !m.busy {
		hover = m.hit(px, py)
	}
	if hover >= 0 {
		cell := m.cells[hover]
		box.Tooltip(a.treemapTooltip(s, cell))
		if box.Clicked() {
			if cell.Tile.Other {
				a.reveal(cell.Tile.ID)
			} else {
				m.highlight = cell.Tile.ID
				a.reveal(cell.Tile.ID)
			}
		}
		if box.DoubleClicked() {
			id := cell.Tile.ID
			if s.Nodes[id].IsDir() {
				a.navigate(id)
			}
		}
		box.ContextMenu(func(menu *ui.Menu) {
			if !cell.Tile.Other {
				a.tree.selectOnly(cell.Tile.ID)
			}
			if s.Nodes[cell.Tile.ID].IsDir() && menu.Item("Go Into Folder").Chosen() {
				id := cell.Tile.ID
				a.post(func() { a.navigate(id) })
			}
			a.contextItems(menu, "-", "item.open", "item.explorer", "-", "edit.copy", "edit.copyPath", "-", "item.scanHere", "-", "edit.delete", "-", "item.properties")
		})
	}
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
	cells := m.cells
	dark := t.Dark
	busy := m.busy
	box.Draw(func(p *ui.Painter, origin ui.Rect) {
		if busy && len(cells) == 0 {
			p.Text(origin.X+8, origin.Y+6, "Building treemap…", 12, t.TextMuted)
			return
		}
		if len(cells) == 0 {
			p.Text(origin.X+8, origin.Y+6, "Nothing to show: this folder has no data for the "+strings.ToLower(a.prefs.Metric)+" size metric.", 12, t.TextMuted)
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
					frame = frame.Mix(ui.Hex("#FFFFFF"), .12)
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
			if i == hover {
				col = col.Mix(ui.Hex("#FFFFFF"), .22)
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
	})
}
