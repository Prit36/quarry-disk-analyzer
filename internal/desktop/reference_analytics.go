package desktop

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
)

func (a *App) types(c *ui.Context) {
	a.prepareAnalysis()
	theme := c.Theme()
	windowWidth, _ := c.Size()
	compact := windowWidth*(100-a.columnSplit)/100 < 500
	ui.Row(c).Height(40).Shrink(0).Gap(0).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		for tab, label := range []string{"Overview", "File types", "Top files", "Duplicate files"} {
			display, padding, fontSize := label, float32(20), float32(13)
			if compact {
				padding, fontSize = 6, 12
				if tab == 3 {
					display = "Duplicates"
				}
			}
			background, border := ui.Transparent, ui.Transparent
			if a.typeTab == tab {
				background, border = raisedSurface(c), theme.Accent
			}
			ui.Button(c, display).Label(label).Height(39).Padding(8, padding).FontSize(fontSize).Background(background).BorderWidth(0, 0, 2, 0).BorderColor(border).OnClick(func() { a.typeTab = tab })
		}
	})
	if a.analysisBusy {
		ui.Text(c, "Preparing analytics…").Padding(16).TextColor(theme.TextMuted)
		ui.Box(c).Grow(1)
		return
	}
	switch a.typeTab {
	case 0:
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(16).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "Largest folders · "+strings.ToLower(a.metricName())).FontSize(14).FontWeight(600)
				for _, id := range a.analysis.Folders {
					ui.Button(c.Key(id), "").Height(28).OnClick(func() { a.navigate(id) }).Children(func() {
						glyph(c, "folder_filled", 18, folderColor)
						ui.Text(c, a.snapshot.NameView(id)).Grow(1).MinWidth(0).SingleLine()
						ui.Text(c, a.formatBytes(a.metricBytes(id))).FontSize(13)
					})
					proportionBar(c, fraction(a.metricBytes(id), a.metricBytes(a.analysis.Root)), 0, theme.Accent)
				}
				if len(a.analysis.Folders) == 0 {
					ui.Text(c, "No subdirectories in this scope.").TextColor(theme.TextMuted)
				}
				ui.Text(c, "Scan information").FontSize(14).FontWeight(600).Padding(8, 0, 0, 0)
				ui.Text(c, "Method: "+engineName(a.snapshot.Engine)).FontSize(12)
				ui.Text(c, "Started: "+a.snapshot.Started.Format("02 Jan 2006 15:04")+" · "+a.snapshot.Elapsed.Round(time.Millisecond).String()).FontSize(12)
				ui.Text(c, fmt.Sprintf("%d issues · live filesystem scan", a.snapshot.Errors)).FontSize(12).TextColor(theme.TextMuted)
			})
		})
	case 1:
		a.extensionTable(c)
	case 2:
		a.topFilesTable(c)
	case 3:
		a.duplicatesView(c)
	}
	a.referenceQuickStats(c)
}

func (a *App) referenceQuickStats(c *ui.Context) {
	selected := a.selected
	if selected == index.None {
		selected = a.analysis.Root
	}
	node := a.snapshot.Nodes[selected]
	windowWidth, windowHeight := c.Size()
	compact := windowWidth*(100-a.columnSplit)/100 < 500
	height := float32(98)
	padding, gap := float32(12), float32(12)
	labelSize, valueSize, metricGap, iconSize := float32(11), float32(14), float32(3), float32(26)
	if compact {
		height = 132
		if windowHeight < 720 {
			height, padding, gap = 106, 6, 6
			labelSize, valueSize, metricGap, iconSize = 10, 13, 1, 22
		}
	}
	ui.Column(c).Height(height).Shrink(0).Padding(padding, 16).Gap(gap).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		title := "Quick stats"
		if selected != 0 {
			title += " — " + a.snapshot.NameView(selected)
		}
		ui.Text(c, title).FontSize(14).FontWeight(600).SingleLine().Tooltip("Selected scope: " + a.snapshot.Path(selected))
		metrics := []struct {
			icon, label, value string
			color              ui.Color
		}{{"hard_drive", "Total size", a.formatBytes(node.Size), ui.Hex("#1683F4")}, {"database_color", "Allocated size", a.formatBytes(node.Allocated), ui.Hex("#1683F4")}, {"document_text_color", "Total files", number(uint64(node.Files)), ui.Hex("#AE7DE7")}, {"folder_filled", "Total folders", number(uint64(node.Dirs)), folderColor}}
		columns := 4
		if compact {
			columns = 2
		}
		for start := 0; start < len(metrics); start += columns {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				for position, metric := range metrics[start:min(start+columns, len(metrics))] {
					if position > 0 {
						separator(c, 32)
					}
					ui.Row(c).Grow(1).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
						if metric.icon == "folder_filled" {
							filesystemGlyph(c, "folder", iconSize)
						} else if strings.HasSuffix(metric.icon, "_color") {
							coloredGlyph(c, metric.icon, iconSize)
						} else {
							glyph(c, metric.icon, iconSize, metric.color)
						}
						ui.Column(c).Grow(1).MinWidth(0).Gap(metricGap).Children(func() {
							ui.Text(c, metric.label).FontSize(labelSize).TextColor(c.Theme().TextMuted).SingleLine()
							ui.Text(c, metric.value).Label("Quick stat: " + metric.label).FontSize(valueSize).FontWeight(600).SingleLine().Tooltip(metric.value)
						})
					})
				}
			})
		}
	})
}

func (a *App) extensionTable(c *ui.Context) {
	theme := c.Theme()
	ui.Row(c).Height(46).Shrink(0).Padding(0, 16).Gap(8).AlignItems(ui.Center).ContextMenu(func(menu *ui.Menu) {
		if menu.Item("Follow selection").Checked(a.followSelection).Chosen() {
			a.followSelection = !a.followSelection
		}
		if menu.Item("Filter extensions…").Chosen() {
			a.typeFilterOpen = true
		}
	}).Children(func() {
		ui.Text(c, "File type distribution").FontSize(14).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Tooltip(a.metricName() + " · % of analyzed scope: " + a.snapshot.Path(a.analysis.Root))
		ui.Text(c, number(uint64(len(a.analysis.Types)))+" extensions").FontSize(12).TextColor(theme.TextMuted)
	})
	if a.typeFilterOpen || a.typeQuery != "" {
		ui.Row(c).Height(30).Shrink(0).Padding(0, 12).Gap(6).Children(func() {
			if ui.TextInput(c, &a.typeQuery).Placeholder("Filter extensions").Label("Filter extensions").Grow(1).Height(26).Changed() {
				a.filterTypes()
			}
			ui.Button(c, "").Size(24, 24).Padding(4).Label("Clear extension filter").OnClick(func() { a.typeQuery = ""; a.typeFilterOpen = false; a.filterTypes() }).Children(func() { glyph(c, "dismiss", 14, theme.TextMuted) })
		})
	}
	count := len(a.visibleTypes)
	if !a.allTypes {
		count = min(10, count)
	}
	ui.Column(c).Grow(1).MinHeight(0).Padding(0, 12).Children(func() {
		columns := []ui.TableColumn{{Title: "Extension", MinWidth: 120, Sortable: true}, {Title: "% of Scope", Width: 156, MinWidth: 110, Align: ui.End, Sortable: true}, {Title: a.metricName(), ID: "Bytes", Width: 110, MinWidth: 80, Align: ui.End, Sortable: true}, {Title: "Files", Width: 88, MinWidth: 60, Align: ui.End, Sortable: true}}
		a.typeList.Selected, a.typeList.Sort = &a.typeRow, &a.typeSort
		a.typeList.Key = func(row int) any { return a.visibleTypes[row].Extension }
		before := a.typeSort
		dataTable(c.Key("extensions"), &a.typeList, columns, count, func(row, column int) {
			group := a.visibleTypes[row]
			value := group.Bytes
			if a.metric == 1 {
				value = group.Allocated
			}
			share := fraction(value, a.metricBytes(a.analysis.Root))
			switch column {
			case 0:
				ui.Row(c).Height(17).Gap(8).AlignItems(ui.Center).ContextMenu(func(menu *ui.Menu) {
					if menu.Item("Inspect this extension").Chosen() {
						a.inspectType(group.Extension)
					}
				}).Children(func() {
					coloredGlyph(c, extensionIconName(group.Extension), 23)
					ui.Text(c, group.Extension).FontSize(13).SingleLine().Grow(1).MinWidth(0).Tooltip(group.Extension)
				})
			case 1:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("%.1f%%", share*100)).Width(48).FontSize(12).TextColor(theme.TextMuted)
					proportionBar(c, share, 82, theme.Accent).Height(14)
				})
			case 2:
				ui.Text(c, a.formatBytes(value)).FontSize(13).SingleLine()
			case 3:
				ui.Text(c, number(group.Count)).FontSize(13).SingleLine().TextColor(theme.TextMuted)
			}
		}).Grow(1).MinHeight(0)
		if before != a.typeSort {
			a.filterTypes()
		}
	})
	ui.Row(c).Height(26).Shrink(0).Padding(0, 12).Justify(ui.End).Children(func() {
		if len(a.visibleTypes) > 10 {
			label := fmt.Sprintf("Show more (%s)", number(uint64(len(a.visibleTypes)-10)))
			if a.allTypes {
				label = "Show fewer"
			}
			ui.Button(c, label).Height(24).Padding(2, 8).Background(ui.Transparent).Border(0, ui.Transparent).FontSize(11).TextColor(theme.AccentHover).OnClick(func() { a.allTypes = !a.allTypes }).Children(func() { glyph(c, "chevron_down", 11, theme.AccentHover) })
		}
	})
	if a.typeRow >= 0 && a.typeRow < count {
		a.inspectType(a.visibleTypes[a.typeRow].Extension)
		a.typeRow = -1
	}
}

func extensionIconName(extension string) string {
	switch strings.ToLower(extension) {
	case ".dll", ".resources", ".zip", ".7z", ".rar", ".tar", ".gz":
		return "document_yellow"
	case ".rpack", ".doc", ".docx", ".txt", ".md":
		return "document_purple"
	case ".exe", ".msi":
		return "document_green"
	case ".img", ".bmp", ".pdf":
		return "document_red"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdb":
		return "document_cyan"
	default:
		return "document_gray"
	}
}
