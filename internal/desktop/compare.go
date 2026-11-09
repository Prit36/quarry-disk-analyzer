package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// The comparison shows what changed between a previous scan and the
// current one, folder by folder or as the files that changed most. Folder
// totals are in both indexes, so opening a folder only matches its
// children by name; the walk for the largest changes runs on a worker.

const (
	compareFolders = iota
	compareFiles
)

var compareFilters = []string{"All Changes", "Added", "Removed", "Grown", "Shrunk"}

type comparisonView struct {
	open      bool
	cmp       *index.Comparison
	label     string // the previous scan, for the title
	stack     []index.DiffEntry
	mode      int
	filter    string
	unchanged bool

	rows    []index.DiffEntry
	totals  index.DiffTotals
	hasTot  bool
	busy    bool
	err     string
	gen     uint64
	cancel  context.CancelFunc
	list    ui.ListState
	cursor  int
	crumb   int
	lastKey compareKey // what rows were computed for
	largest uint64     // the largest change among rows, for the bars
}

// compareKey is what the rows depend on.
type compareKey struct {
	depth     int
	mode      int
	filter    string
	unchanged bool
	prev, cur index.ID
}

// compareDialog asks for a saved scan and compares it with the current one.
func (a *App) compareDialog() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "Compare with Saved Scan", Filters: scanFilters})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.compareWith(paths[0], filepath.Base(paths[0])) })
	}()
}

// compareWith loads a saved scan and opens the comparison with it.
func (a *App) compareWith(path, label string) {
	if a.base == nil {
		return
	}
	scan, allocated := a.completeScan(), a.metricAllocated()
	a.notify("Loading %s for comparison…", label)
	go func() {
		previous, err := index.LoadSnapshot(context.Background(), path)
		var cmp *index.Comparison
		if err == nil {
			cmp, err = index.NewComparison(previous, scan(), allocated)
		}
		a.post(func() {
			if err != nil {
				a.fail("Compare: %v", err)
				return
			}
			a.clearError()
			a.message = ""
			a.showComparison(cmp, label+" ("+platform.FormatDateTime(previous.Started)+")")
		})
	}()
}

func (a *App) showComparison(cmp *index.Comparison, label string) {
	v := &a.compareView
	if v.cancel != nil {
		v.cancel()
	}
	*v = comparisonView{open: true, cmp: cmp, label: label, stack: []index.DiffEntry{cmp.Root()}, filter: compareFilters[0], gen: v.gen + 1}
}

func (v *comparisonView) here() index.DiffEntry { return v.stack[len(v.stack)-1] }

// keep reports whether the filter shows a status.
func (v *comparisonView) keep(s index.DiffStatus) bool {
	switch v.filter {
	case "Added":
		return s == index.DiffAdded
	case "Removed":
		return s == index.DiffRemoved
	case "Grown":
		return s == index.DiffGrown
	case "Shrunk":
		return s == index.DiffShrunk
	}
	return s != index.DiffUnchanged || v.unchanged
}

// refresh computes the rows for the folder, mode and filter shown, when
// they changed.
func (a *App) refreshComparison() {
	v := &a.compareView
	here := v.here()
	key := compareKey{len(v.stack), v.mode, v.filter, v.unchanged, here.Prev, here.Cur}
	if key == v.lastKey {
		return
	}
	v.lastKey = key
	if v.cancel != nil {
		v.cancel()
	}
	v.gen++
	gen, cmp, mode := v.gen, v.cmp, v.mode
	probe := comparisonView{filter: v.filter, unchanged: v.unchanged} // a copy for the worker
	keep := probe.keep
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel, v.busy, v.err = cancel, true, ""
	go func() {
		var rows []index.DiffEntry
		var totals index.DiffTotals
		var err error
		hasTot := false
		if mode == compareFiles {
			rows, totals, err = cmp.LargestChanges(ctx, here, 1000, keep)
			hasTot = true
		} else {
			var all []index.DiffEntry
			all, err = cmp.Children(ctx, here)
			for _, e := range all {
				if keep(e.Status) {
					rows = append(rows, e)
				}
			}
		}
		a.post(func() {
			if v.gen != gen {
				return
			}
			v.busy, v.cancel = false, nil
			if err != nil {
				if ctx.Err() == nil {
					v.err = err.Error()
				}
				return
			}
			v.rows, v.cursor = rows, -1
			v.largest = 1
			for _, e := range rows {
				v.largest = max(v.largest, e.Magnitude())
			}
			if hasTot {
				v.totals, v.hasTot = totals, true
			}
		})
	}()
}

// revealable reports whether an entry of the comparison's current scan is
// the same entry in the scan now shown, so Reveal can go to it.
func (a *App) revealable(e index.DiffEntry) bool {
	v := &a.compareView
	if e.Cur == index.None || a.base == nil || a.snap == nil || v.cmp == nil {
		return false
	}
	id := e.Cur
	if int(id) >= len(a.base.Nodes) || !a.snap.Reachable(id) || !strings.EqualFold(a.base.Root, v.cmp.Cur.Root) {
		return false
	}
	if id == 0 {
		return true
	}
	return a.base.NameView(id) == v.cmp.Cur.NameView(id) && a.base.Nodes[id].IsDir() == e.Dir
}

func (a *App) comparisonDialog(c *ui.Context) {
	v := &a.compareView
	if !v.open || v.cmp == nil {
		if !v.open && v.cancel != nil {
			v.cancel()
			v.cancel = nil
		}
		return
	}
	a.refreshComparison()
	t := c.Theme()
	ui.Modal(c.Key("comparison"), &v.open, func() {
		ui.Column(c).Width(900).MaxWidthPercent(100).Height(600).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Changes since "+v.label)
			root := v.cmp.Root()
			metric := "logical size"
			if v.cmp.Allocated {
				metric = "allocated size"
			}
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				infoField(c, "Previous", a.size(root.Before))
				infoField(c, "Now", a.size(root.After))
				d := root.Delta()
				ui.Row(c).Gap(4).Shrink(0).Children(func() {
					ui.Text(c, "Net change").TextColor(t.TextMuted).SingleLine()
					ui.Text(c, signedSize(a, d)).TextColor(deltaColor(t, d)).SingleLine()
				})
				ui.Spacer(c)
				ui.Text(c, "By "+metric).TextColor(t.TextMuted).SingleLine()
			})
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				if ui.Segmented(c, &v.mode, "By Folder", "Largest Changed Files").Label("Show").Changed() {
					v.cursor = -1
				}
				ui.Select(c.Key("cmp-filter"), &v.filter, compareFilters).Label("Show changes").Width(140)
				if v.mode == compareFolders {
					ui.Checkbox(c, &v.unchanged, "Show unchanged")
				}
				ui.Spacer(c)
				if v.busy {
					ui.Text(c, "Comparing…").TextColor(t.TextMuted).SingleLine()
				}
			})
			// Where in the comparison: the scan root down to the folder open.
			crumbs := make([]string, len(v.stack))
			for i, e := range v.stack {
				crumbs[i] = v.cmp.Name(e)
			}
			v.crumb = len(crumbs) - 1
			if ui.Breadcrumbs(c, crumbs, &v.crumb).Label("Folder").Changed() && v.crumb < len(v.stack)-1 {
				v.stack = v.stack[:v.crumb+1]
				v.cursor = -1
			}
			if v.mode == compareFiles && v.hasTot {
				tt := v.totals
				ui.Text(c, fmt.Sprintf("Files below this folder: %s added (%s) · %s removed (%s) · %s grew (+%s) · %s shrank (−%s)",
					count(tt.Added), a.size(tt.AddedBytes), count(tt.Removed), a.size(tt.RemovedBytes),
					count(tt.Grown), a.size(tt.GrownBytes), count(tt.Shrunk), a.size(tt.ShrunkBytes))).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
			}
			if v.err != "" {
				ui.Text(c, v.err).TextColor(t.Danger)
			}
			largest := max(v.largest, 1)
			v.list.Selected = &v.cursor
			name := "Name"
			if v.mode == compareFiles {
				name = "Path"
			}
			table := ui.Table(c.Key("cmp-table"), &v.list, []ui.TableColumn{{Title: name, MinWidth: 220}, {Title: "Previous", Width: 96, Align: ui.End}, {Title: "Now", Width: 96, Align: ui.End}, {Title: "Change", Width: 200}, {Title: "Status", Width: 150}}, len(v.rows), func(row, col int) {
				e := v.rows[row]
				switch col {
				case 0:
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						icon := "document"
						if e.Dir {
							icon = "folder"
						}
						ui.Icon(c, icons[icon]).FontSize(13).TextColor(t.TextMuted).Shrink(0)
						text := v.cmp.Name(e)
						if v.mode == compareFiles {
							text = v.cmp.Path(e)
						}
						txt := ui.Text(c, text).SingleLine().MinWidth(0).Shrink(1).Tooltip(v.cmp.Path(e))
						if e.Cur == index.None {
							txt.TextColor(t.TextMuted)
						}
					})
				case 1:
					if e.Prev != index.None {
						ui.Text(c, a.size(e.Before)).TextColor(t.TextMuted).SingleLine()
					}
				case 2:
					if e.Cur != index.None {
						ui.Text(c, a.size(e.After)).SingleLine()
					}
				case 3:
					d := e.Delta()
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						frac := float32(float64(e.Magnitude()) / float64(largest))
						color := deltaColor(t, d)
						ui.Box(c).Size(70, 8).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
							mid := r.X + r.W/2
							w := r.W / 2 * frac
							if d != 0 && w < 1 {
								w = 1
							}
							seg := ui.Rect{X: mid, Y: r.Y, W: w, H: r.H}
							if d < 0 {
								seg.X = mid - w
							}
							p.Fill(ui.Rect{X: mid, Y: r.Y - 1, W: 1, H: r.H + 2}, t.Border, 0)
							p.Fill(seg, color, 1)
						})
						ui.Text(c, signedSize(a, d)).TextColor(deltaColor(t, d)).SingleLine()
					})
				case 4:
					status := e.Status.String()
					switch e.Status {
					case index.DiffRemoved:
						status = "Only in previous scan"
					case index.DiffAdded:
						status = "New"
					}
					txt := ui.Text(c, status).SingleLine()
					if e.Status == index.DiffRemoved {
						txt.TextColor(t.TextMuted).Tooltip("No longer on disk as of the current scan; it cannot be revealed or opened.")
					}
				}
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			if len(v.rows) == 0 && !v.busy {
				ui.Text(c, "No changes here with this filter.").TextColor(t.TextMuted)
			}
			sel := index.DiffEntry{Prev: index.None, Cur: index.None}
			if v.cursor >= 0 && v.cursor < len(v.rows) {
				sel = v.rows[v.cursor]
			}
			open := func(e index.DiffEntry) {
				if e.Dir {
					if v.mode == compareFiles {
						// Show it among its siblings, as a folder.
						return
					}
					v.stack = append(v.stack, e)
					v.cursor = -1
				}
			}
			if table.Submitted() && sel.Dir {
				open(sel)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(8, 0, 0, 0).Children(func() {
				if ui.Button(c, "Up").Disabled(len(v.stack) < 2).Clicked() {
					v.stack = v.stack[:len(v.stack)-1]
					v.cursor = -1
				}
				if v.mode == compareFolders && ui.Button(c, "Open Folder").Disabled(!sel.Dir).Clicked() {
					open(sel)
				}
				can := a.revealable(sel)
				tip := "Show it in Tree View of the current scan"
				if sel.Cur == index.None && sel.Prev != index.None {
					tip = "It exists only in the previous scan"
				}
				if ui.Button(c, "Reveal in Tree").Disabled(!can).Tooltip(tip).Clicked() {
					v.open = false
					a.reveal(sel.Cur)
				}
				if ui.Button(c, "List Files Here").Disabled(!can || !sel.Dir).Tooltip("Open this folder's files in File View").Clicked() {
					v.open = false
					a.showQuery("type:file", sel.Cur)
				}
				ui.Spacer(c)
				if ui.Button(c, "Copy List").Disabled(len(v.rows) == 0).Clicked() {
					lines := []string{"Path\tPrevious\tNow\tChange\tStatus"}
					for _, e := range v.rows {
						lines = append(lines, fmt.Sprintf("%s\t%d\t%d\t%d\t%s", v.cmp.Path(e), e.Before, e.After, e.Delta(), e.Status))
					}
					a.copyText(lines, "changes")
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					v.open = false
				}
			})
		})
	})
}

func signedSize(a *App, d int64) string {
	switch {
	case d > 0:
		return "+" + a.size(uint64(d))
	case d < 0:
		return "−" + a.size(uint64(-d))
	}
	return "0"
}

func deltaColor(t *ui.Theme, d int64) ui.Color {
	switch {
	case d > 0:
		return t.Warning
	case d < 0:
		return t.Success
	}
	return t.TextMuted
}
