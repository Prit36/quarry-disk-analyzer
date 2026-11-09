package desktop

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// History is opt-in (Options → Remember Scans for Trends). After each
// completed scan Quarry writes a small summary, and the last few full scans
// of each root for detailed comparison, to its settings folder. There is no
// background service: history grows only when the user scans.

const historySummaries = 60

func historyDir() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "history")
}

func (a *App) toggleHistory() {
	a.prefs.KeepHistory = !a.prefs.KeepHistory
	if a.prefs.KeepHistory {
		a.notify("Quarry will remember a summary of each completed scan (and the last %d full scans) in %s.", a.prefs.KeepScans, historyDir())
	} else {
		a.notify("Scans are no longer remembered. Tools → Scan History can delete those kept so far.")
	}
}

// recordHistory remembers a completed live scan, then compares it with the
// previous one of the same root for the growth alert.
func (a *App) recordHistory(s *index.Snapshot) {
	dir := historyDir()
	if !a.prefs.KeepHistory || dir == "" || s == nil || s.Cancelled {
		return
	}
	scan, keep, threshold, allocated := a.completeScan(), a.prefs.KeepScans, a.prefs.GrowthAlert, a.metricAllocated()
	go func() {
		previous, _ := index.ListHistory(dir, s.Root)
		sum, err := s.RecordHistory(dir, keep > 0, keep, historySummaries, func(path string) error {
			return scan().Save(context.Background(), path)
		})
		a.post(func() {
			if err != nil {
				a.fail("Could not remember this scan: %v", err)
				return
			}
			if a.historyView.open {
				a.loadHistory()
			}
			if len(previous) == 0 || threshold <= 0 {
				return
			}
			if msg := growthAlert(previous[0], sum, allocated, threshold, a.size); msg != "" {
				a.notify("%s", msg)
			}
		})
	}()
}

// growthAlert names the folder that grew most since the previous scan, if
// by more than threshold GB.
func growthAlert(prev, cur index.ScanSummary, allocated bool, threshold float64, size func(uint64) string) string {
	limit := int64(threshold * (1 << 30))
	growth := index.CompareSummaries(prev, cur, allocated)
	if len(growth) == 0 || growth[0].Delta() < limit {
		return ""
	}
	g := growth[0]
	return fmt.Sprintf("%s grew by %s since the scan of %s. Tools → Scan History shows the trend.", filepath.Join(cur.Root, g.Path), size(uint64(g.Delta())), platform.FormatDateTime(prev.Started))
}

type historyView struct {
	open    bool
	root    string
	list    []index.ScanSummary
	busy    bool
	err     string
	gen     uint64
	cursor  int
	lst     ui.ListState
	glist   ui.ListState
	scans   string // option labels
	alert   string
	confirm bool // asking before deleting every record of this root
}

var keepScanOptions = []string{"No full scans", "Last full scan", "Last 3 full scans", "Last 5 full scans", "Last 10 full scans"}
var keepScanCounts = []int{0, 1, 3, 5, 10}
var alertOptions = []string{"Never", "Over 1 GB", "Over 5 GB", "Over 10 GB", "Over 50 GB"}
var alertValues = []float64{0, 1, 5, 10, 50}

func (a *App) openHistory() {
	v := &a.historyView
	root := a.target
	if a.base != nil {
		root = a.base.Root
	}
	*v = historyView{open: true, root: root, gen: v.gen + 1}
	for i, n := range keepScanCounts {
		if n == a.prefs.KeepScans {
			v.scans = keepScanOptions[i]
		}
	}
	if v.scans == "" {
		v.scans = keepScanOptions[2]
	}
	v.alert = alertOptions[0]
	for i, n := range alertValues {
		if n == a.prefs.GrowthAlert {
			v.alert = alertOptions[i]
		}
	}
	a.loadHistory()
}

func (a *App) loadHistory() {
	v := &a.historyView
	v.gen++
	gen, root, dir := v.gen, v.root, historyDir()
	v.busy = true
	go func() {
		list, err := index.ListHistory(dir, root)
		a.post(func() {
			if v.gen != gen {
				return
			}
			v.busy, v.list, v.err = false, list, ""
			if err != nil {
				v.err = err.Error()
			}
			if v.cursor >= len(list) {
				v.cursor = 0
			}
		})
	}()
}

func (a *App) historyValue(s index.ScanSummary) uint64 {
	if a.metricAllocated() {
		return s.Allocated
	}
	return s.Size
}

func (a *App) historyDialog(c *ui.Context) {
	v := &a.historyView
	if !v.open {
		return
	}
	t := c.Theme()
	ui.Modal(c.Key("history"), &v.open, func() {
		ui.Column(c).Width(860).MaxWidthPercent(100).Height(600).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Scan History")
			root := v.root
			if root == "" {
				root = "(no scan yet)"
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, root).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
				ui.Spacer(c)
				ui.Checkbox(c, &a.prefs.KeepHistory, "Remember scans")
				if ui.Select(c.Key("hist-keep"), &v.scans, keepScanOptions).Label("Full scans kept").Width(150).Disabled(!a.prefs.KeepHistory).Changed() {
					for i, o := range keepScanOptions {
						if o == v.scans {
							a.prefs.KeepScans = keepScanCounts[i]
						}
					}
					if dir := historyDir(); dir != "" && v.root != "" {
						keep := a.prefs.KeepScans
						go func() {
							index.PruneHistory(dir, v.root, keep, historySummaries)
							a.post(a.loadHistory)
						}()
					}
				}
				ui.Text(c, "Alert when a folder grows").TextColor(t.TextMuted).SingleLine()
				if ui.Select(c.Key("hist-alert"), &v.alert, alertOptions).Label("Growth alert").Width(110).Disabled(!a.prefs.KeepHistory).Changed() {
					for i, o := range alertOptions {
						if o == v.alert {
							a.prefs.GrowthAlert = alertValues[i]
						}
					}
				}
			})
			switch {
			case v.err != "":
				ui.Text(c, v.err).TextColor(t.Danger)
			case v.busy && v.list == nil:
				ui.Text(c, "Reading history…").TextColor(t.TextMuted)
			case len(v.list) == 0:
				msg := "No scans of this folder are remembered yet."
				if !a.prefs.KeepHistory {
					msg += " Turn on Remember scans: each completed scan then keeps a small summary (and, as chosen, the full scan) in " + historyDir() + ", so growth can be followed over time. Nothing runs in the background."
				} else {
					msg += " The next completed scan will be the first."
				}
				ui.Text(c, msg).TextColor(t.TextMuted).MaxWidth(820)
			default:
				a.historyBody(c)
			}
			buttons(c, func() {
				if len(v.list) > 0 {
					if v.confirm {
						ui.Text(c, "Delete every remembered scan of this folder?").TextColor(t.Warning).SingleLine()
						if ui.Button(c, "Delete All").Clicked() {
							v.confirm = false
							list := v.list
							go func() {
								for _, s := range list {
									index.DeleteHistory(s)
								}
								a.post(a.loadHistory)
							}()
						}
						if ui.Button(c, "Keep").Clicked() {
							v.confirm = false
						}
					} else if ui.Button(c, "Delete All…").Clicked() {
						v.confirm = true
					}
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					v.open = false
				}
			})
		})
	})
}

func (a *App) historyBody(c *ui.Context) {
	v := &a.historyView
	t := c.Theme()
	list := v.list
	// The trend, oldest to newest.
	var hi uint64 = 1
	for _, s := range list {
		hi = max(hi, a.historyValue(s))
	}
	newest, oldest := list[0], list[len(list)-1]
	ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(260, 40).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(r, t.Border.Alpha(0.35), 3)
			n := len(list)
			if n == 0 {
				return
			}
			w := r.W / float32(max(n, 2))
			for i := range n {
				s := list[n-1-i]
				h := r.H * float32(float64(a.historyValue(s))/float64(hi))
				bar := ui.Rect{X: r.X + float32(i)*w + 1, Y: r.Y + r.H - h, W: max(1, w-2), H: h}
				color := t.Focus.Alpha(0.6)
				if n-1-i == v.cursor {
					color = t.Focus
				}
				p.Fill(bar, color, 1)
			}
		}).Tooltip("Size of each remembered scan, oldest on the left")
		infoField(c, "Now", a.size(a.historyValue(newest)))
		d := int64(a.historyValue(newest)) - int64(a.historyValue(oldest))
		ui.Row(c).Gap(4).Shrink(0).Children(func() {
			ui.Text(c, "Since "+platform.FormatDateTime(oldest.Started)).TextColor(t.TextMuted).SingleLine()
			ui.Text(c, signedSize(a, d)).TextColor(deltaColor(t, d)).SingleLine()
		})
		ui.Spacer(c)
		ui.Text(c, plural(uint64(len(list)), "scan", "scans")).TextColor(t.TextMuted).SingleLine()
	})
	ui.Row(c).Gap(8).Grow(1).MinHeight(0).Children(func() {
		v.lst.Selected = &v.cursor
		ui.Table(c.Key("hist-table"), &v.lst, []ui.TableColumn{{Title: "Scanned", MinWidth: 150}, {Title: "Size", Width: 90, Align: ui.End}, {Title: "Change", Width: 90, Align: ui.End}, {Title: "Saved", Width: 60}}, len(list), func(row, col int) {
			s := list[row]
			switch col {
			case 0:
				txt := platform.FormatDateTime(s.Started)
				if s.Partial {
					txt += " (partial)"
				}
				ui.Text(c, txt).SingleLine()
			case 1:
				ui.Text(c, a.size(a.historyValue(s))).SingleLine()
			case 2:
				if row+1 < len(list) {
					d := int64(a.historyValue(s)) - int64(a.historyValue(list[row+1]))
					ui.Text(c, signedSize(a, d)).TextColor(deltaColor(t, d)).SingleLine()
				}
			case 3:
				if s.Scan != "" {
					ui.Text(c, "Full").TextColor(t.TextMuted).SingleLine().Tooltip("The full scan is kept: it can be opened or compared in detail")
				}
			}
		}).Width(390).Shrink(0).MinHeight(0).Border(1, t.Border)
		ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Gap(6).Children(func() {
			if v.cursor < 0 || v.cursor >= len(list) {
				return
			}
			sel := list[v.cursor]
			if v.cursor+1 >= len(list) {
				ui.Text(c, "The oldest remembered scan; select a later one to see what grew since the scan before it.").TextColor(t.TextMuted)
				return
			}
			prev := list[v.cursor+1]
			ui.Text(c, "Folders that changed since "+platform.FormatDateTime(prev.Started)).TextColor(t.TextMuted).SingleLine()
			growth := index.CompareSummaries(prev, sel, a.metricAllocated())
			shown := growth[:0:0]
			for _, g := range growth {
				if g.Delta() != 0 {
					shown = append(shown, g)
				}
			}
			ui.Table(c.Key("hist-growth"), &v.glist, []ui.TableColumn{{Title: "Folder", MinWidth: 160}, {Title: "Before", Width: 86, Align: ui.End}, {Title: "After", Width: 86, Align: ui.End}, {Title: "Change", Width: 90, Align: ui.End}}, len(shown), func(row, col int) {
				g := shown[row]
				switch col {
				case 0:
					ui.Text(c, g.Path).SingleLine().MinWidth(0).Tooltip(filepath.Join(sel.Root, g.Path))
				case 1:
					ui.Text(c, a.size(g.Before)).TextColor(t.TextMuted).SingleLine()
				case 2:
					ui.Text(c, a.size(g.After)).SingleLine()
				case 3:
					ui.Text(c, signedSize(a, g.Delta())).TextColor(deltaColor(t, g.Delta())).SingleLine()
				}
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			if len(shown) == 0 {
				ui.Text(c, "No folder near the root changed size.").TextColor(t.TextMuted)
			}
		})
	})
	if v.cursor < 0 || v.cursor >= len(list) {
		return
	}
	sel := list[v.cursor]
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		sameRoot := a.base != nil && strings.EqualFold(filepath.Clean(a.base.Root), filepath.Clean(sel.Root))
		if ui.Button(c, "Compare with Current…").Disabled(sel.Scan == "" || !sameRoot).Tooltip("Open the detailed comparison of this remembered scan with the scan shown now").Clicked() {
			v.open = false
			a.compareWith(sel.Scan, "remembered scan")
		}
		if ui.Button(c, "Open").Disabled(sel.Scan == "" || a.scanning).Tooltip("Open the remembered scan as historical data").Clicked() {
			v.open = false
			a.openSnapshot(sel.Scan)
		}
		if ui.Button(c, "Delete").Clicked() {
			go func() {
				index.DeleteHistory(sel)
				a.post(a.loadHistory)
			}()
		}
		if sel.Scan == "" {
			ui.Text(c, "Only the summary of this scan is kept.").TextColor(t.TextMuted).SingleLine()
		}
	})
}
