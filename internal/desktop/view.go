package desktop

import (
	"embed"
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"math"
	"path/filepath"
	"stratum/internal/index"
	"strings"
	"time"
)

//go:embed assets/*
var iconAssets embed.FS
var icons = func() map[string]*ui.SVG {
	m := map[string]*ui.SVG{}
	entries, _ := iconAssets.ReadDir("assets")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".svg") {
			continue
		}
		b, _ := iconAssets.ReadFile("assets/" + e.Name())
		m[strings.TrimSuffix(e.Name(), ".svg")] = ui.MustParseSVG(b)
	}
	return m
}()

func (a *App) view(c *ui.Context) {
	a.drain()
	c.SetTheme(designTheme(a.light, a.compact))
	if !a.queryDue.IsZero() && !c.Now().Before(a.queryDue) {
		a.requestRows()
	}
	if a.busy {
		c.After(100 * time.Millisecond)
	}
	if c.Shortcut(ui.Cmd, ui.KeyO) {
		a.chooseFolder()
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		if a.tab == 2 {
			a.tab = 0
		}
		a.searchHandle.Focus(c)
	}
	if c.Shortcut(ui.Cmd, ui.KeyL) {
		a.pathHandle.Focus(c)
		a.selectAddress = true
	}
	editing := a.pathHandle.Focused(c) || a.searchHandle.Focused(c) || a.settingsOpen || a.issuesOpen || a.advancedOpen
	if !editing && c.Shortcut(0, ui.KeyF5) {
		a.start()
	}
	if c.Shortcut(0, ui.KeyEscape) && !a.settingsOpen && !a.issuesOpen && !a.advancedOpen {
		if a.query != "" {
			a.query = ""
			a.requestRows()
		} else if a.busy && a.cancel != nil {
			a.cancel()
		}
	}
	if !editing && c.Shortcut(ui.Alt, ui.KeyUp) && a.snapshot != nil && a.focus != 0 {
		a.navigate(a.snapshot.Nodes[a.focus].Parent)
	}
	if !editing && c.Shortcut(ui.Alt, ui.KeyLeft) {
		a.goBack()
	}
	if !editing && c.Shortcut(ui.Alt, ui.KeyRight) {
		a.goForward()
	}
	if c.Shortcut(ui.Cmd, ui.KeyE) {
		a.export()
	}
	ui.Column(c).Fill().Gap(0).FontFeatures("tnum").Children(func() {
		a.titleBar(c)
		a.toolbar(c)
		a.scanStrip(c)
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			if a.snapshot == nil {
				a.empty(c)
			} else {
				a.results(c)
			}
		}).Padding(6, 12, 8, 12)
		a.status(c)
	})
	a.dialogs(c)
}

func (a *App) scanExplanation() string {
	if a.mftBlocked && !a.admin {
		return "Administrator access required. Choose Run as administrator, or select Folders."
	}
	if a.mftBlocked {
		return a.notice
	}
	return ""
}
func (a *App) empty(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(8).Children(func() {
		icon := icons["folder"]
		chips := t.TextMuted
		if a.mftBlocked {
			icon = icons["shield"]
			chips = t.Warning
		}
		ui.Icon(c, icon).FontSize(40).TextColor(chips)
		text := "Select a drive or folder."
		color := t.TextMuted
		if a.busy {
			text = a.progress.Status() + "…"
		}
		if a.mftBlocked {
			text = "Direct MFT scan could not start"
			color = t.Warning
		}
		ui.Text(c, text).FontSize(15).TextColor(color)
		if a.mftBlocked {
			ui.Text(c, a.scanExplanation()).FontSize(12).TextColor(t.TextMuted)
			if !a.admin {
				ui.PrimaryButton(c, "Run as administrator").OnClick(a.elevate)
			}
		} else if !a.busy {
			ui.Text(c, "Choose a drive from the toolbar or browse for a folder.").FontSize(12).TextColor(t.TextMuted)
		}
	})
}
func (a *App) results(c *ui.Context) {
	mainPanes := func() {
		explorer := func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				a.resultToolbar(c)
				ui.Box(c).Height(6).Shrink(0)
				ui.Column(c).Grow(1).MinHeight(0).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(panelRadius).Clip().Children(func() {
					if a.tab == 2 {
						a.notes(c)
					} else {
						a.table(c)
					}
				})
			})
		}
		if a.analyticsCollapsed {
			explorer()
		} else {
			ui.SplitPercent(c, &a.columnSplit, func() { ui.Column(c).Fill().Padding(0, 8, 0, 0).Children(explorer) }, func() {
				ui.Column(c).Fill().MinHeight(0).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(panelRadius).Clip().Children(func() { a.types(c) })
			}).Fill()
		}
	}
	if a.mapCollapsed {
		mainPanes()
	} else {
		ui.SplitVerticalPercent(c, &a.divider, mainPanes, func() { ui.Column(c).Fill().Padding(8, 0, 0, 0).Children(func() { a.mapView(c) }) }).Grow(1).MinHeight(0)
	}
}

func (a *App) table(c *ui.Context) {
	t := c.Theme()
	s := a.snapshot
	if !a.queryBusy && len(a.rows) == 0 {
		ui.Text(c, "No matching entries in the indexed scope.").Padding(12).TextColor(t.TextMuted)
	}
	cols, columnIndices := a.resultColumns()
	a.list.HeaderMenu = func(menu *ui.Menu, column int) { a.columnMenu(menu) }
	a.list.Selected = &a.selectedRow
	a.list.Sort = &a.sort
	a.list.RoundRows = true
	a.list.Key = func(i int) any { return a.rows[i].ID }
	a.list.Label = func(i int) string { return s.NameView(a.rows[i].ID) }
	before := a.sort
	table := dataTable(c.Key("results-table"), &a.list, cols, len(a.rows), func(row, col int) {
		col = columnIndices[col]
		r := a.rows[row]
		n := s.Nodes[r.ID]
		switch col {
		case 0:
			height := float32(16)
			if a.compact {
				height = 14
			}
			cell := ui.Row(c).Height(height).Gap(8).AlignItems(ui.Center).Tooltip(s.Path(r.ID))
			cell.ContextMenu(func(m *ui.Menu) {
				if m.Item("Export subtree as CSV…").Disabled(a.exporting).Chosen() {
					a.exportAs("csv", r.ID)
				}
				if n.IsDir() && m.Item("Rescan selected path").Disabled(a.busy).Chosen() {
					if path := a.safePath(r.ID); path != "" {
						a.path = path
						a.start()
					}
				}
				if m.Item("Copy name").Chosen() {
					mygo.Clipboard.WriteText(s.NameView(r.ID))
				}
				if m.Item("Copy logical size").Chosen() {
					mygo.Clipboard.WriteText(fmt.Sprint(n.Size))
				}
				if m.Item("Focus in treemap").Chosen() {
					a.focusMap(r.ID)
				}
				if m.Item("Analyze selection").Chosen() {
					a.selected = r.ID
					a.selectedRow = row
					a.followSelection = true
				}
				if n.IsDir() && m.Item("Collapse subtree").Chosen() {
					a.open[r.ID] = false
					a.requestRows()
				}
				if m.Item("Show in Explorer").Chosen() {
					a.selected = r.ID
					a.reveal()
				}
				if m.Item("Copy path").Chosen() {
					a.selected = r.ID
					a.copyPath()
				}
				if n.IsDir() && m.Item("Focus folder").Chosen() {
					a.navigate(r.ID)
				}
			})
			cell.Children(func() {
				if a.tab == 0 && a.query == "" {
					ui.Box(c).Width(float32(min(r.Depth, 32)) * 22)
					if n.IsDir() && n.FirstChild != index.None {
						icon := "chevron_right"
						if a.open[r.ID] || r.ID == a.focus {
							icon = "chevron_down"
						}
						ui.Button(c.Key(fmt.Sprint("expand", r.ID)), "").Size(16, 16).Padding(2).Background(ui.Transparent).Border(0, ui.Transparent).Label("Expand " + s.NameView(r.ID)).Children(func() { ui.Icon(c, icons[icon]).FontSize(11) }).OnClick(func() {
							if r.ID != a.focus {
								a.open[r.ID] = !a.open[r.ID]
								a.requestRows()
							}
						})
					} else {
						ui.Box(c).Width(16)
					}
				}
				icon := icons["document"]
				color := t.TextMuted
				if n.IsDir() {
					icon = icons["folder"]
					color = folderColor
				}
				if r.ID == 0 {
					filesystemGlyph(c, "drive", 20)
				} else if n.IsDir() {
					filesystemGlyph(c, "folder", 20)
				} else {
					ui.Icon(c, icon).FontSize(18).TextColor(color)
				}
				name := s.NameView(r.ID)
				if r.ID == 0 {
					name = s.Root
				}
				ui.Text(c, name).FontSize(13).SingleLine().MinWidth(0).Grow(1)
				if n.Flags&index.Shared != 0 {
					ui.Text(c, "shared").FontSize(10).TextColor(t.TextMuted)
				}
				if n.Flags&index.Incomplete != 0 {
					ui.Icon(c, icons["warning"]).FontSize(15).TextColor(t.Warning).Label("Incomplete metadata").Tooltip(a.issueExplanation(r.ID)).OnClick(func() { a.issuesOpen = true })
				}
			})
		case 1:
			base := n.Parent
			if base == index.None {
				base = 0
			}
			value, total := n.Allocated, s.Nodes[base].Allocated
			if a.metric == 0 {
				value, total = n.Size, s.Nodes[base].Size
			}
			percentage := 0.0
			if total > 0 {
				percentage = 100 * float64(value) / float64(total)
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				proportionBar(c, percentage/100, 44, t.Accent).Height(14)
				ui.Text(c, fmt.Sprintf("%.1f%%", percentage)).Width(48).FontSize(12).SingleLine().TextColor(t.TextMuted)
			})
		case 2:
			ui.Text(c, a.formatBytes(n.Size)).FontSize(13).SingleLine()
		case 3:
			ui.Text(c, a.formatBytes(n.Allocated)).FontSize(13).SingleLine()
		case 4:
			if n.IsDir() {
				ui.Text(c, number(uint64(n.Files))).FontSize(13).TextColor(t.TextMuted)
			}
		case 5:
			if n.IsDir() {
				ui.Text(c, number(uint64(n.Dirs))).FontSize(13).TextColor(t.TextMuted)
			}
		case 6:
			date := ""
			if n.Modified != 0 {
				date = time.Unix(n.Modified, 0).Format("02/01/2006 15:04")
			}
			ui.Text(c, date).FontSize(12).TextColor(t.TextMuted).SingleLine()
		}
	}).Grow(1).MinHeight(0)
	if a.sort != before {
		a.requestRows()
	}
	if a.selectedRow >= 0 && a.selectedRow < len(a.rows) {
		a.selected = a.rows[a.selectedRow].ID
		a.listSelection = a.selected
	}
	if table.Submitted() && a.selected != index.None {
		if s.Nodes[a.selected].IsDir() {
			a.navigate(a.selected)
		} else {
			a.tab = 2
		}
	}
	if table.Shortcut(0, ui.KeyRight) && a.selected != index.None && s.Nodes[a.selected].IsDir() {
		a.open[a.selected] = true
		a.requestRows()
	}
	if table.Shortcut(0, ui.KeyLeft) && a.selected != index.None {
		if a.open[a.selected] {
			a.open[a.selected] = false
		} else if parent := s.Nodes[a.selected].Parent; parent != index.None {
			a.selected = parent
			a.selectedRow = -1
		}
		a.requestRows()
	}
	if table.Shortcut(ui.Cmd, ui.KeyC) {
		a.copyPath()
	}
	if table.Shortcut(0, ui.KeySpace) && a.selected != index.None && s.Nodes[a.selected].IsDir() {
		a.open[a.selected] = !a.open[a.selected]
		a.requestRows()
	}
}
func (a *App) notes(c *ui.Context) {
	t := c.Theme()
	s := a.snapshot
	ui.Scroll(c).Grow(1).Padding(10).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
			if a.selected != index.None {
				node := s.Nodes[a.selected]
				ui.Text(c, s.Path(a.selected)).Bold().SingleLine().Tooltip(s.Path(a.selected))
				ui.Text(c, "Logical: "+a.formatBytes(node.Size)+" · allocated: "+a.formatBytes(node.Allocated))
				ui.Text(c, fmt.Sprintf("%s files · %s folders", number(uint64(node.Files)), number(uint64(node.Dirs))))
				if node.Modified != 0 {
					ui.Text(c, "Modified: "+time.Unix(node.Modified, 0).Format("02 Jan 2006 15:04:05"))
				}
			}
			ui.Text(c, "Method: "+engineName(s.Engine)).Bold()
			ui.Text(c, fmt.Sprintf("%d errors   %d directory links skipped   %d hard-link aliases", s.Errors, s.SkippedLinks, s.HardLinks))
			if s.Fallback != "" {
				ui.Text(c, s.Fallback).TextColor(t.Warning)
			}
			ui.Text(c, "Live scan. Directory links are excluded. Allocated totals exclude filesystem overhead.").TextColor(t.TextMuted)
			for _, issue := range s.Issues {
				ui.Text(c, issue.Path+"\n"+issue.Message).FontSize(11).TextColor(t.Warning)
			}
		})
	})
}

func (a *App) selectMap(id index.ID) {
	a.selected = id
	a.selectedRow = -1
	for parent := a.snapshot.Nodes[id].Parent; parent != index.None; parent = a.snapshot.Nodes[parent].Parent {
		a.open[parent] = true
	}
	within := false
	for parent := id; parent != index.None; parent = a.snapshot.Nodes[parent].Parent {
		if parent == a.focus {
			within = true
			break
		}
	}
	if !within {
		a.focus = 0
		a.address = a.snapshot.Root
	}
	a.tab = 0
	a.query = ""
	a.requestRows()
}
func separator(c *ui.Context, height float32) { ui.Box(c).Size(1, height).Background(c.Theme().Border) }
func engineName(name string) string {
	if strings.Contains(name, "MFT") {
		return "NTFS MFT"
	}
	return "Folders"
}
func truncate(text string, limit int) string {
	r := []rune(text)
	if limit > 1 && len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return text
}
func fileColor(name string) ui.Color {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".mov", ".avi", ".webm":
		return typePalette[0]
	case ".zip", ".7z", ".rar", ".tar", ".gz", ".iso":
		return typePalette[1]
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".heic":
		return typePalette[2]
	case ".exe", ".dll", ".msi", ".sys":
		return typePalette[3]
	case ".pdf", ".doc", ".docx", ".txt", ".md":
		return typePalette[4]
	case ".mp3", ".wav", ".flac", ".ogg":
		return typePalette[5]
	case ".go", ".js", ".ts", ".tsx", ".json", ".py", ".rs", ".css":
		return typePalette[6]
	default:
		return typePalette[7]
	}
}

func quickStat(c *ui.Context, t *ui.Theme, icon *ui.SVG, label, value string) {
	ui.Column(c).Grow(1).Background(t.SurfaceHover).Radius(6).Padding(6, 8).Gap(3).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).FontSize(14).TextColor(t.Accent)
			ui.Text(c, label).FontSize(10).TextColor(t.TextMuted)
		})
		ui.Text(c, value).FontSize(12).Bold()
	})
}
func mapTextColor(color ui.Color) ui.Color {
	linear := func(b uint8) float64 {
		v := float64(b) / 255
		if v <= .04045 {
			return v / 12.92
		}
		return math.Pow((v+.055)/1.055, 2.4)
	}
	l := .2126*linear(color.R) + .7152*linear(color.G) + .0722*linear(color.B)
	if l > .179 {
		return ui.Hex("#000000")
	}
	return ui.Hex("#ffffff")
}
