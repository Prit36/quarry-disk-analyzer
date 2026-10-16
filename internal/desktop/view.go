package desktop

import (
	"cmp"
	"embed"
	"fmt"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"math"
	"path/filepath"
	"slices"
	"stratum/internal/index"
	"stratum/internal/treemap"
	"strings"
	"time"
)

//go:embed assets/*.svg
var iconAssets embed.FS
var icons = func() map[string]*ui.SVG {
	m := map[string]*ui.SVG{}
	entries, _ := iconAssets.ReadDir("assets")
	for _, e := range entries {
		b, _ := iconAssets.ReadFile("assets/" + e.Name())
		m[strings.TrimSuffix(e.Name(), ".svg")] = ui.MustParseSVG(b)
	}
	return m
}()
var typePalette = []ui.Color{ui.Hex("#4588bc"), ui.Hex("#9975ba"), ui.Hex("#458f78"), ui.Hex("#bd9446"), ui.Hex("#b56265"), ui.Hex("#4b98a3"), ui.Hex("#879a55"), ui.Hex("#788492")}

func (a *App) view(c *ui.Context) {
	a.drain()
	t := *ui.DarkTheme()
	if a.light {
		t = *ui.LightTheme()
	}
	if !a.light {
		t.Background = ui.Hex("#202020")
		t.Surface = ui.Hex("#292929")
		t.SurfaceHover = ui.Hex("#353535")
		t.SurfacePressed = ui.Hex("#414141")
		t.Border = ui.Hex("#414141")
		t.Text = ui.Hex("#e8e8e8")
		t.TextMuted = ui.Hex("#aaaaaa")
		t.Accent = ui.Hex("#397bac")
		t.AccentHover = ui.Hex("#438bbb")
		t.AccentPressed = ui.Hex("#316b97")
		t.AccentText = ui.Hex("#ffffff")
		t.Selection = ui.Hex("#264c69")
		t.Focus = ui.Hex("#75afd7")
	}
	t.Font = "Segoe UI"
	t.FontSize = 12
	t.Spacing = 2.5
	t.Radius = 2
	c.SetTheme(&t)
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
		a.searchHandle.Focus(c)
	}
	if c.Shortcut(0, ui.KeyF5) {
		a.start()
	}
	if c.Shortcut(0, ui.KeyEscape) && a.busy && a.cancel != nil {
		a.cancel()
	}
	if c.Shortcut(ui.Alt, ui.KeyUp) && a.snapshot != nil && a.focus != 0 {
		a.navigate(a.snapshot.Nodes[a.focus].Parent)
	}
	ui.Column(c).Fill().Gap(0).Children(func() {
		a.toolbar(c)
		a.scanStrip(c)
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			if a.snapshot == nil {
				a.empty(c)
			} else {
				a.results(c)
			}
		})
		a.status(c)
	})
}

func (a *App) toolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(42).Shrink(0).Padding(6, 8).Gap(6).AlignItems(ui.Center).Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Button(c, "Open…").Height(28).Disabled(a.busy).OnClick(a.chooseFolder)
		if len(a.drives) > 0 {
			sel := ui.SelectBase(c.Key("drive"), &a.path)
			sel.Trigger.Size(32, 28).Padding(6).Border(1, t.Border).Label("Select drive").Disabled(a.busy).Children(func() { ui.Icon(c, icons["chevron_down"]).FontSize(12) })
			sel.Popup(func(p ui.Element) {
				p.Padding(3).Background(t.Surface).Border(1, t.Border)
				for _, d := range a.drives {
					item := sel.Item(d.Path).Padding(7, 10)
					item.Children(func() { ui.Text(c, d.Path+"  "+d.Name+"  ·  "+d.FileSystem) })
				}
			})
		}
		if ui.TextInput(c.Key("scan-path"), &a.path).Label("Scan path").Grow(1).MinWidth(100).Height(28).Disabled(a.busy).Submitted() {
			a.start()
		}
		ui.Select(c.Key("method"), &a.scanMode, []string{"Automatic", "Folders"}).Width(108).Height(28).Label("Scan method").Disabled(a.busy).Tooltip("Automatic: MFT for drive roots; folder enumeration for folders.")
		ui.PrimaryButton(c, "Scan").Width(62).Height(28).Disabled(a.busy).OnClick(a.start)
		ui.Button(c, "Stop").Width(48).Height(28).Disabled(!a.busy).OnClick(func() {
			if a.cancel != nil {
				a.cancel()
			}
		})
		separator(c, 20)
		ui.Button(c, "Export…").Height(28).Disabled(a.snapshot == nil || a.exporting).OnClick(a.export)
		if !a.admin {
			ui.Button(c, "Run as administrator").Height(28).Disabled(a.busy).Tooltip("Restart with Windows administrator access for direct MFT scans.").OnClick(a.elevate)
		}
		ui.Button(c, "").Size(28, 28).Label("Toggle theme").Children(func() { ui.Icon(c, icons["dark_theme"]).FontSize(15) }).OnClick(func() {
			a.light = !a.light
			if a.window != nil {
				source := mygo.ThemeDark
				if a.light {
					source = mygo.ThemeLight
				}
				mygo.Theme.SetSource(source)
			}
		})
	})
}
func (a *App) scanStrip(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(30).Shrink(0).Padding(0, 10).Gap(16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		method := "Ready"
		if a.busy {
			method = a.progress.Status()
		} else if a.mftBlocked {
			method = "MFT unavailable"
		} else if a.snapshot != nil {
			method = engineName(a.snapshot.Engine)
		}
		color := t.Text
		if a.mftBlocked {
			color = t.Warning
		}
		ui.Text(c, method).Bold().FontSize(11).TextColor(color)
		if a.mftBlocked {
			ui.Text(c, a.scanExplanation()).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
			return
		}
		if a.busy {
			ui.Text(c, number(a.progress.Files.Load())+" files").TextColor(t.TextMuted)
			ui.Text(c, number(a.progress.Folders.Load())+" folders").TextColor(t.TextMuted)
			ui.Text(c, Bytes(a.progress.Bytes.Load())).TextColor(t.TextMuted)
			if total := a.progress.TotalRecords.Load(); total > 0 {
				ui.Progress(c, float64(a.progress.Records.Load())/float64(total)).Width(160).Height(3)
			}
			ui.Box(c).Grow(1)
			ui.Text(c, time.Since(a.started).Round(100*time.Millisecond).String()).TextColor(t.TextMuted)
		} else if s := a.snapshot; s != nil {
			n := s.Nodes[0]
			ui.Text(c, "Size  "+Bytes(n.Size))
			ui.Text(c, "Allocated  "+Bytes(n.Allocated))
			ui.Text(c, number(uint64(n.Files))+" files").TextColor(t.TextMuted)
			ui.Text(c, number(uint64(n.Dirs))+" folders").TextColor(t.TextMuted)
			ui.Box(c).Grow(1)
			ui.Text(c, s.Elapsed.Round(time.Millisecond).String()).TextColor(t.TextMuted)
		} else {
			ui.Text(c, a.scanExplanation()).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
		}
	})
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
	ui.Column(c).Fill().Center().Children(func() {
		text := "Select a drive or folder."
		color := c.Theme().TextMuted
		if a.busy {
			text = a.progress.Status() + "…"
		}
		if a.mftBlocked {
			text = "Direct MFT scan could not start."
			color = c.Theme().Warning
		}
		ui.Text(c, text).TextColor(color)
	})
}
func (a *App) results(c *ui.Context) {
	ui.SplitVerticalPercent(c, &a.divider, func() {
		ui.SplitPercent(c, &a.columnSplit, func() {
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				a.resultToolbar(c)
				if a.tab == 2 {
					a.notes(c)
				} else {
					a.table(c)
				}
			})
		}, func() { ui.Column(c).Fill().MinHeight(0).Children(func() { a.types(c) }) }).Fill()
	}, func() { a.mapView(c) }).Grow(1).MinHeight(0)
}

func (a *App) resultToolbar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(34).Shrink(0).Padding(3, 6).Gap(5).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		if ui.Segmented(c, &a.tab, "Tree view", "File view", "Details").Label("Results view").Changed() {
			if a.tab != 2 {
				a.requestRows()
			}
		}
		if a.tab != 2 {
			ui.Button(c, "").Size(24, 24).Padding(5).Label("Parent folder").Disabled(a.focus == 0).Children(func() { ui.Icon(c, icons["arrow_up"]) }).OnClick(func() { a.navigate(a.snapshot.Nodes[a.focus].Parent) })
			if ui.TextInput(c.Key("search"), &a.query).Bind(&a.searchHandle).Placeholder("Filter").Label("Search files").Grow(1).MinWidth(30).Height(24).Changed() {
				a.setQuery(c)
			}
			if a.query != "" {
				ui.Button(c, "").Size(23, 23).Padding(5).Label("Clear filter").Children(func() { ui.Icon(c, icons["dismiss"]) }).OnClick(func() { a.query = ""; a.requestRows() })
			}
			if a.queryBusy {
				ui.Text(c, "Updating…").FontSize(10).TextColor(t.TextMuted)
			}
		}
	})
}
func (a *App) table(c *ui.Context) {
	t := c.Theme()
	s := a.snapshot
	cols := []ui.TableColumn{{Title: "Name", MinWidth: 180, Sortable: true}, {Title: "%", Width: 55, Align: ui.End}, {Title: "Size", Width: 88, Align: ui.End, Sortable: true}, {Title: "Allocated", Width: 88, Align: ui.End, Sortable: true}, {Title: "Files", Width: 70, Align: ui.End, Sortable: true}, {Title: "Folders", Width: 62, Align: ui.End}, {Title: "Modified", Width: 112, Align: ui.End, Sortable: true}}
	a.list.Selected = &a.selectedRow
	a.list.Sort = &a.sort
	a.list.Key = func(i int) any { return a.rows[i].ID }
	a.list.Label = func(i int) string { return s.NameView(a.rows[i].ID) }
	before := a.sort
	table := ui.Table(c.Key("results-table"), &a.list, cols, len(a.rows), func(row, col int) {
		r := a.rows[row]
		n := s.Nodes[r.ID]
		switch col {
		case 0:
			cell := ui.Row(c).Height(16).Gap(5).AlignItems(ui.Center)
			cell.ContextMenu(func(m *ui.Menu) {
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
					ui.Box(c).Width(float32(min(r.Depth, 32)) * 12)
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
					color = ui.Hex("#c3aa6b")
				}
				ui.Icon(c, icon).FontSize(14).TextColor(color)
				name := s.NameView(r.ID)
				if r.ID == 0 {
					name = s.Root
				}
				ui.Text(c, name).SingleLine().MinWidth(0).Grow(1)
				if n.Flags&index.Shared != 0 {
					ui.Text(c, "shared").FontSize(10).TextColor(t.TextMuted)
				}
				if n.Flags&index.Incomplete != 0 {
					ui.Text(c, "!").FontSize(10).TextColor(t.Warning)
				}
			})
		case 1:
			base := n.Parent
			if base == index.None || a.tab == 1 || a.query != "" {
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
			ui.Text(c, fmt.Sprintf("%.1f%%", percentage)).FontSize(11).TextColor(t.TextMuted)
		case 2:
			ui.Text(c, Bytes(n.Size)).FontSize(11).SingleLine()
		case 3:
			ui.Text(c, Bytes(n.Allocated)).FontSize(11).SingleLine()
		case 4:
			if n.IsDir() {
				ui.Text(c, number(uint64(n.Files))).FontSize(11).TextColor(t.TextMuted)
			}
		case 5:
			if n.IsDir() {
				ui.Text(c, number(uint64(n.Dirs))).FontSize(11).TextColor(t.TextMuted)
			}
		case 6:
			date := ""
			if n.Modified != 0 {
				date = time.Unix(n.Modified, 0).Format("02/01/2006 15:04")
			}
			ui.Text(c, date).FontSize(10).TextColor(t.TextMuted).SingleLine()
		}
	}).Grow(1).MinHeight(0)
	if a.sort != before {
		a.requestRows()
	}
	if a.selectedRow >= 0 && a.selectedRow < len(a.rows) {
		a.selected = a.rows[a.selectedRow].ID
	}
	if table.Submitted() && a.selected != index.None && s.Nodes[a.selected].IsDir() {
		a.navigate(a.selected)
	}
	if table.Shortcut(0, ui.KeyRight) && a.selected != index.None && s.Nodes[a.selected].IsDir() {
		a.open[a.selected] = true
		a.requestRows()
	}
	if table.Shortcut(0, ui.KeyLeft) && a.selected != index.None {
		a.open[a.selected] = false
		a.requestRows()
	}
}
func (a *App) types(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(34).Shrink(0).Padding(0, 8).AlignItems(ui.Center).Background(t.Surface).Children(func() {
		ui.Text(c, "Extensions").Bold().FontSize(11)
		ui.Box(c).Grow(1)
		ui.Text(c, number(uint64(len(a.summary.Types)))).TextColor(t.TextMuted).FontSize(10)
	})
	cols := []ui.TableColumn{{Title: "Extension", MinWidth: 74, Sortable: true}, {Title: "%", Width: 49, Align: ui.End}, {Title: "Allocated", Width: 87, Align: ui.End, Sortable: true}, {Title: "Files", Width: 72, Align: ui.End, Sortable: true}}
	a.typeList.Selected = &a.typeRow
	a.typeList.Sort = &a.typeSort
	before := a.typeSort
	a.typeList.Key = func(i int) any { return a.summary.Types[i].Extension }
	ui.Table(c.Key("extensions"), &a.typeList, cols, len(a.summary.Types), func(row, col int) {
		v := a.summary.Types[row]
		switch col {
		case 0:
			ui.Row(c).Height(16).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 12).Background(fileColor("a" + v.Extension))
				ui.Text(c, v.Extension).SingleLine().FontSize(11)
			})
		case 1:
			total := a.snapshot.Nodes[0].Allocated
			percent := 0.0
			if total > 0 {
				percent = 100 * float64(v.Allocated) / float64(total)
			}
			ui.Text(c, fmt.Sprintf("%.1f", percent)).FontSize(10).TextColor(t.TextMuted)
		case 2:
			ui.Text(c, Bytes(v.Allocated)).FontSize(11).SingleLine()
		case 3:
			ui.Text(c, number(v.Count)).FontSize(11).TextColor(t.TextMuted)
		}
	}).Grow(1).MinHeight(0)
	if before != a.typeSort {
		slices.SortFunc(a.summary.Types, func(x, y index.FileType) int {
			v := 0
			switch a.typeSort.Column {
			case "Extension":
				v = strings.Compare(x.Extension, y.Extension)
			case "Files":
				v = cmp.Compare(x.Count, y.Count)
			default:
				v = cmp.Compare(x.Allocated, y.Allocated)
			}
			if a.typeSort.Descending {
				v = -v
			}
			return v
		})
	}
	if a.typeRow >= 0 && a.typeRow < len(a.summary.Types) {
		ext := a.summary.Types[a.typeRow].Extension
		a.typeRow = -1
		if ext == "(no extension)" {
			ext = "none"
		}
		a.query = "ext:" + ext
		a.tab = 1
		a.requestRows()
	}
}
func (a *App) notes(c *ui.Context) {
	t := c.Theme()
	s := a.snapshot
	ui.Scroll(c).Grow(1).Padding(10).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
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

func (a *App) mapView(c *ui.Context) {
	t := c.Theme()
	s := a.snapshot
	ui.Column(c).Fill().Gap(0).Children(func() {
		ui.Row(c).Height(28).Shrink(0).Padding(0, 8).Gap(8).AlignItems(ui.Center).Background(t.Surface).BorderWidth(1, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Treemap").Bold().FontSize(11)
			path := s.Root
			if a.mapFor != index.None {
				path = s.Path(a.mapFor)
			}
			ui.Text(c, path).SingleLine().Grow(1).MinWidth(0).FontSize(11).TextColor(t.TextMuted)
			ui.Button(c, "Root").Height(22).Disabled(a.focus == 0 && a.mapFor == 0).OnClick(func() { a.navigate(0) })
			if ui.Segmented(c, &a.metric, "Size", "Allocated").Label("Treemap metric").Changed() {
				a.requestMap(a.mapFor)
			}
		})
		box := ui.Box(c.Key("treemap")).Grow(1).MinHeight(0).Clip().Focusable().Label("Storage treemap")
		bounds := box.Bounds()
		if a.mapWidth != bounds.W || a.mapHeight != bounds.H {
			a.mapWidth, a.mapHeight = bounds.W, bounds.H
			a.cells = treemap.NestedLayout(a.branches, treemap.Rect{X: 2, Y: 2, W: max(0, bounds.W-4), H: max(0, bounds.H-4)})
		}
		if bounds.W == 0 {
			c.Invalidate()
		}
		x, y, over := box.PointerPosition()
		hit := -1
		if over {
			for i := len(a.cells) - 1; i >= 0; i-- {
				r := a.cells[i].Tile.Rect
				if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
					hit = i
					break
				}
			}
		}
		if hit >= 0 {
			cell := a.cells[hit]
			name := s.NameView(cell.Tile.ID)
			if cell.Tile.Other {
				name = "Other items"
			}
			box.Tooltip(name + "  " + Bytes(cell.Tile.Bytes))
			if box.Clicked() && !cell.Tile.Other {
				a.selectMap(cell.Tile.ID)
			}
			if box.DoubleClicked() && !cell.Tile.Other {
				dir := cell.Tile.ID
				if !s.Nodes[dir].IsDir() {
					dir = s.Nodes[dir].Parent
				}
				a.navigate(dir)
			}
		}
		cells := a.cells
		box.Draw(func(p *ui.Painter, origin ui.Rect) {
			for i, cell := range cells {
				tile := cell.Tile
				r := tile.Rect
				q := ui.Rect{X: origin.X + r.X, Y: origin.Y + r.Y, W: max(0, r.W-1), H: max(0, r.H-1)}
				name := s.NameView(tile.ID)
				if cell.Group {
					p.Fill(q, t.SurfaceHover, 0)
					if r.W > 70 {
						p.Clip(q, 0, func() { p.Text(q.X+4, q.Y+2, truncate(name, int((r.W-8)/6)), 10, t.Text) })
					}
					if tile.ID == a.selected {
						p.Stroke(q, t.Text, 0, 1.5)
					}
					continue
				}
				color := fileColor(name)
				if s.Nodes[tile.ID].IsDir() {
					color = typePalette[7]
				}
				if tile.Other {
					color = ui.Hex("#555d66")
					name = "Other items"
				}
				if i == hit {
					color = color.Mix(ui.Hex("#ffffff"), .16)
				}
				p.Fill(q, color, 0)
				if tile.ID == a.selected && !tile.Other {
					p.Stroke(q, t.Text, 0, 1.5)
				}
				if q.W > 85 && q.H > 32 {
					textColor := mapTextColor(color)
					p.Clip(q, 0, func() {
						p.Text(q.X+5, q.Y+4, truncate(name, int((q.W-10)/6)), 10, textColor)
						if q.H > 48 {
							p.Text(q.X+5, q.Y+19, Bytes(tile.Bytes), 10, textColor)
						}
					})
				}
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
	a.focus = 0
	a.tab = 0
	a.query = ""
	a.requestRows()
}
func (a *App) status(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Height(25).Shrink(0).Padding(0, 8).Gap(9).AlignItems(ui.Center).Background(t.Surface).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		text := "Ready"
		if a.notice != "" {
			text = a.notice
		} else if a.busy {
			text = "Scanning"
		} else if a.snapshot != nil && a.selected != index.None {
			text = a.snapshot.Path(a.selected)
		}
		ui.Text(c, text).SingleLine().MinWidth(0).Grow(1).FontSize(10).TextColor(t.TextMuted)
		if a.snapshot != nil {
			ui.Text(c, number(uint64(a.matches))+" rows").FontSize(10).TextColor(t.TextMuted)
			separator(c, 12)
			if a.snapshot.Errors > 0 {
				ui.Text(c, fmt.Sprintf("%d errors", a.snapshot.Errors)).FontSize(10).TextColor(t.Warning)
			}
			ui.Text(c, "Index "+Bytes(a.snapshot.Bytes())).FontSize(10).TextColor(t.TextMuted)
		}
		if a.admin {
			ui.Text(c, "Administrator").FontSize(10).TextColor(t.TextMuted)
		}
	})
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
