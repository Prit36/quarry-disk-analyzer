package desktop

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// expandBudget bounds how many rows an "expand subtree" may add, so a
// request on C:\ cannot materialize millions of rows and stall the UI.
var expandBudget = 200_000

type treeRow struct {
	id    index.ID
	depth int32
	kids  bool
}

// treeModel is Tree View's state: the flattened rows of the expanded tree
// below the current location. Rows are spliced on expand/collapse instead of
// being rebuilt, and everything is keyed by stable index IDs.
type treeModel struct {
	a      *App
	rows   []treeRow
	open   map[index.ID]bool
	kids   map[index.ID][]index.ID // children sorted by the current order
	list   ui.ListState
	sel    ui.Selection[index.ID]
	cursor int
	sort   ui.SortOrder
	notice string

	selCache selectionCache
	cells    cellTextCache // formatted cell text, shared by both grids
	rowsGen  uint64        // changes whenever rows change
}

func (t *treeModel) init(a *App) {
	t.a = a
	t.sort = a.prefs.TreeSort
	t.list.Columns = a.prefs.TreeColumns
	t.reset()
}

func (t *treeModel) reset() {
	cols := t.list.Columns
	t.list = ui.ListState{Columns: cols}
	t.rows, t.kids = nil, nil
	t.rowsGen++
	t.open = map[index.ID]bool{0: true}
	t.sel = ui.Selection[index.ID]{}
	t.cursor = -1
	t.notice = ""
	t.selCache.reset()
	t.cells = cellTextCache{} // drops the old snapshot too
}

func (t *treeModel) snap() *index.Snapshot { return t.a.snap }

func (t *treeModel) order() index.Sort {
	s := index.Sort{Column: t.sort.Column, Desc: t.sort.Descending, Allocated: t.a.metricAllocated()}
	if t.sort.Column == "% of Scan" || t.sort.Column == "" {
		s.Column = "" // the active size metric
	}
	return s
}

func (t *treeModel) children(id index.ID) []index.ID {
	if t.kids == nil {
		t.kids = map[index.ID][]index.ID{}
	}
	if k, ok := t.kids[id]; ok {
		return k
	}
	s := t.snap()
	k := s.Children(id)
	_ = s.SortContext(context.Background(), k, t.order())
	t.kids[id] = k
	return k
}

func (t *treeModel) hasKids(id index.ID) bool {
	n := t.snap().Nodes[id]
	return n.IsDir() && n.FirstChild != index.None
}

// descendants appends the visible rows below id (open folders only).
func (t *treeModel) descendants(id index.ID, depth int32, out []treeRow, budget int) []treeRow {
	type frame struct {
		kids  []index.ID
		depth int32
	}
	stack := []frame{{t.children(id), depth}}
	for len(stack) > 0 && budget > 0 {
		top := &stack[len(stack)-1]
		if len(top.kids) == 0 {
			stack = stack[:len(stack)-1]
			continue
		}
		k := top.kids[0]
		top.kids = top.kids[1:]
		d := top.depth
		row := treeRow{id: k, depth: d, kids: t.hasKids(k)}
		out = append(out, row)
		budget--
		if row.kids && t.open[k] {
			stack = append(stack, frame{t.children(k), d + 1})
		}
	}
	return out
}

func (t *treeModel) rebuild() {
	if t.snap() == nil {
		t.rows = nil
		t.rowsGen++
		return
	}
	cid := t.cursorID()
	loc := t.a.location
	rows := []treeRow{{id: loc, depth: 0, kids: t.hasKids(loc)}}
	if t.open[loc] {
		rows = t.descendants(loc, 1, rows, 1<<30)
	}
	t.rows = rows
	t.rowsGen++
	t.restoreCursor(cid)
	t.selCache.invalidate()
}

func (t *treeModel) dataChanged() {
	t.kids = nil
	s := t.snap()
	for id := range t.sel.All() {
		if !s.Reachable(id) {
			t.sel.Remove(id)
		}
	}
	t.rebuild()
}

func (t *treeModel) locationChanged(previous index.ID) {
	t.open[t.a.location] = true
	t.rebuild()
	if !t.selectOnly(previous) {
		t.selectOnly(t.a.location)
	}
	t.list.ScrollTo(max(0, t.cursor), ui.Center)
}

func (t *treeModel) indexOf(id index.ID) int {
	for i, r := range t.rows {
		if r.id == id {
			return i
		}
	}
	return -1
}

func (t *treeModel) cursorID() index.ID {
	if t.cursor >= 0 && t.cursor < len(t.rows) {
		return t.rows[t.cursor].id
	}
	return index.None
}

func (t *treeModel) restoreCursor(id index.ID) {
	if id != index.None && t.cursor >= 0 && t.cursor < len(t.rows) && t.rows[t.cursor].id == id {
		return // unchanged row: IDs are unique among rows, so no scan is needed
	}
	t.cursor = -1
	if id != index.None {
		t.cursor = t.indexOf(id)
	}
}

func (t *treeModel) selectOnly(id index.ID) bool {
	i := t.indexOf(id)
	if i < 0 {
		return false
	}
	t.sel.Clear()
	t.sel.Add(id)
	t.cursor = i
	t.selCache.invalidate()
	t.list.ScrollIntoView(i)
	return true
}

func (t *treeModel) selectAll() {
	for _, r := range t.rows {
		t.sel.Add(r.id)
	}
	t.selCache.invalidate()
}

// selectedIDs lists selected rows in display order (cached, see selectionCache).
func (t *treeModel) selectedIDs() []index.ID {
	return t.selCache.get(&t.sel, t.rowsGen, t.snap(), func(out []index.ID) []index.ID {
		if n := t.sel.Len(); n == 1 {
			for id := range t.sel.All() {
				if t.snap().Reachable(id) {
					out = append(out, id)
				}
			}
		} else if n > 1 {
			if n == len(t.rows) {
				for _, r := range t.rows {
					out = append(out, r.id)
				}
			} else {
				for _, r := range t.rows {
					if t.sel.Has(r.id) {
						out = append(out, r.id)
					}
				}
			}
		}
		return out
	})
}

// Expand and collapse.

func (t *treeModel) toggle(i int) {
	if i < 0 || i >= len(t.rows) || !t.rows[i].kids {
		return
	}
	if t.open[t.rows[i].id] {
		t.collapseRow(i)
	} else {
		t.expandRow(i)
	}
}

func (t *treeModel) expandRow(i int) {
	r := t.rows[i]
	if !r.kids || t.open[r.id] {
		return
	}
	cid := t.cursorID()
	t.open[r.id] = true
	sub := t.descendants(r.id, r.depth+1, nil, 1<<30)
	t.rows = slices.Insert(t.rows, i+1, sub...)
	t.rowsGen++
	t.restoreCursor(cid)
	t.selCache.invalidate()
}

// collapseRow hides a folder's rows. A selected row that disappears moves
// the selection to the collapsed folder, as Explorer does.
func (t *treeModel) collapseRow(i int) {
	r := t.rows[i]
	if !t.open[r.id] {
		return
	}
	delete(t.open, r.id)
	end := i + 1
	for end < len(t.rows) && t.rows[end].depth > r.depth {
		end++
	}
	hidden := false
	for _, h := range t.rows[i+1 : end] {
		if t.sel.Has(h.id) {
			t.sel.Remove(h.id)
			hidden = true
		}
	}
	cid := t.cursorID()
	if t.cursor > i && t.cursor < end {
		cid, hidden = r.id, true
	}
	t.rows = slices.Delete(t.rows, i+1, end)
	t.rowsGen++
	if hidden {
		t.sel.Add(r.id)
	}
	t.restoreCursor(cid)
	t.selCache.invalidate()
}

func (t *treeModel) expandSubtree(id index.ID) {
	if id == index.None || !t.snap().Nodes[id].IsDir() {
		return
	}
	budget := expandBudget
	queue := []index.ID{id}
	for len(queue) > 0 && budget > 0 {
		d := queue[0]
		queue = queue[1:]
		t.open[d] = true
		kids := t.children(d)
		budget -= len(kids)
		for _, k := range kids {
			if t.hasKids(k) {
				queue = append(queue, k)
			}
		}
	}
	t.notice = ""
	if len(queue) > 0 {
		t.notice = "Expansion stopped at about " + count(uint64(expandBudget)) + " rows to keep Quarry responsive. Expand deeper folders individually."
		t.a.notify("%s", t.notice)
	}
	t.rebuild()
}

func (t *treeModel) collapseSubtree(id index.ID) {
	if id == index.None {
		return
	}
	s := t.snap()
	for o := range t.open {
		if o != id && s.Contains(id, o) {
			delete(t.open, o)
		}
	}
	if id != t.a.location {
		delete(t.open, id)
		if t.sel.Len() > 0 {
			// Selections inside collapse to the folder.
			moved := false
			for sid := range t.sel.All() {
				if sid != id && s.Contains(id, sid) {
					t.sel.Remove(sid)
					moved = true
				}
			}
			if moved {
				t.sel.Add(id)
			}
		}
	}
	cid := t.cursorID()
	if cid != index.None && cid != id && s.Contains(id, cid) {
		cid = id
	}
	t.rebuild()
	t.restoreCursor(cid)
}

// scope is the folder that level commands act on: the focused folder, or
// the location.
func (t *treeModel) scope() int {
	if t.cursor >= 0 && t.cursor < len(t.rows) && t.rows[t.cursor].kids && t.sel.Has(t.rows[t.cursor].id) {
		return t.cursor
	}
	return 0
}

func (t *treeModel) subtreeEnd(i int) int {
	end := i + 1
	for end < len(t.rows) && t.rows[end].depth > t.rows[i].depth {
		end++
	}
	return end
}

func (t *treeModel) expandLevel() {
	if len(t.rows) == 0 {
		return
	}
	i := t.scope()
	if !t.open[t.rows[i].id] {
		t.expandRow(i)
		return
	}
	budget := expandBudget
	for _, r := range t.rows[i+1 : t.subtreeEnd(i)] {
		if r.kids && !t.open[r.id] && budget > 0 {
			t.open[r.id] = true
			budget -= len(t.children(r.id))
		}
	}
	t.rebuild()
}

func (t *treeModel) collapseLevel() {
	if len(t.rows) == 0 {
		return
	}
	i := t.scope()
	end := t.subtreeEnd(i)
	deepest := int32(-1)
	for _, r := range t.rows[i:end] {
		if r.kids && t.open[r.id] && r.depth > deepest {
			deepest = r.depth
		}
	}
	if deepest < 0 {
		return
	}
	if deepest == t.rows[i].depth && t.rows[i].id == t.a.location {
		return
	}
	cid := t.cursorID()
	for _, r := range t.rows[i:end] {
		if r.depth == deepest && t.open[r.id] {
			delete(t.open, r.id)
			if cid != index.None && cid != r.id && t.snap().Contains(r.id, cid) {
				cid = r.id
			}
		}
	}
	t.rebuild()
	t.restoreCursor(cid)
	if cid != index.None && !t.sel.Has(cid) {
		t.selectOnly(cid)
	}
}

func (t *treeModel) collapseAll() {
	t.open = map[index.ID]bool{t.a.location: true}
	t.rebuild()
	if t.cursor < 0 {
		t.selectOnly(t.a.location)
	}
	t.list.ScrollTo(0, ui.Start)
}

// reveal expands the ancestors of id below the location, selects it and
// scrolls it into view.
func (t *treeModel) reveal(id index.ID) {
	s := t.snap()
	for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
		t.open[p] = true
		if p == t.a.location {
			break
		}
	}
	t.rebuild()
	if t.selectOnly(id) {
		t.list.ScrollTo(t.cursor, ui.Center)
	}
	t.a.focusTable = true
}

// Keyboard behaviour matching Explorer's navigation pane.
func (t *treeModel) keyRight() {
	i := t.cursor
	if i < 0 || i >= len(t.rows) || !t.rows[i].kids {
		return
	}
	if !t.open[t.rows[i].id] {
		t.expandRow(i)
	} else if i+1 < len(t.rows) && t.rows[i+1].depth > t.rows[i].depth {
		t.selectOnly(t.rows[i+1].id)
	}
}

func (t *treeModel) keyLeft() {
	i := t.cursor
	if i < 0 || i >= len(t.rows) {
		return
	}
	if t.rows[i].kids && t.open[t.rows[i].id] && i > 0 {
		t.collapseRow(i)
		return
	}
	for p := i - 1; p >= 0; p-- {
		if t.rows[p].depth < t.rows[i].depth {
			t.selectOnly(t.rows[p].id)
			return
		}
	}
}
