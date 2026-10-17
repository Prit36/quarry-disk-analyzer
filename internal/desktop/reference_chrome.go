package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"stratum/internal/platform"
)

var brandMark = func() *ui.Bitmap {
	data, _ := iconAssets.ReadFile("assets/brand.png")
	bitmap, _ := ui.DecodeBitmap(data)
	return bitmap
}()

var systemIcons = func() map[string]*ui.Bitmap {
	icons := make(map[string]*ui.Bitmap)
	for name, source := range platform.StockIcons() {
		icons[name] = ui.NewBitmap(source)
	}
	return icons
}()

func filesystemGlyph(c *ui.Context, name string, size float32) {
	if bitmap := systemIcons[name]; bitmap != nil {
		ui.Image(c, bitmap).Size(size, size)
		return
	}
	icon, color := "document", c.Theme().TextMuted
	if name == "folder" {
		icon, color = "folder_filled", folderColor
	} else if name == "drive" {
		icon, color = "hard_drive", ui.Hex("#86A9CB")
	}
	glyph(c, icon, size, color)
}

func glyph(c *ui.Context, name string, size float32, color ui.Color) {
	ui.Icon(c, icons[name]).FontSize(size).TextColor(color)
}
func coloredGlyph(c *ui.Context, name string, size float32) {
	ui.Image(c, icons[name]).Size(size, size)
}

func (a *App) titleBar(c *ui.Context) {
	bar := c.TitleBar()
	background := raisedSurface(c)
	if c.Theme().Dark {
		background = ui.Hex("#141E2B")
	}
	ui.Row(c).Height(max(42, bar.Height)).Shrink(0).Padding(0, max(18, bar.Right), 0, max(18, bar.Left)).Gap(16).AlignItems(ui.Center).Background(background).DragWindow().Children(func() {
		ui.Image(c, brandMark).Size(20, 20)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Stratum").FontWeight(600).FontSize(16)
			ui.Text(c, "–").FontSize(16).TextColor(c.Theme().TextMuted)
			ui.Text(c, "Disk Space Analyzer").FontSize(16).TextColor(c.Theme().TextMuted)
		})
	})
}

func (a *App) toolbar(c *ui.Context) {
	theme := c.Theme()
	width, _ := c.Size()
	ui.Toolbar(c, func() {
		ui.Row(c).Gap(2).Background(raisedSurface(c)).Border(1, theme.Border).Radius(5).Children(func() {
			ui.Button(c, "").Size(40, controlHeight).Label("Back").Tooltip("Back (Alt+Left)").Disabled(a.historyPos <= 0).Children(func() { glyph(c, "arrow_left", 20, theme.TextMuted) }).OnClick(a.goBack)
			ui.Button(c, "").Size(40, controlHeight).Label("Forward").Tooltip("Forward (Alt+Right)").Disabled(a.historyPos >= len(a.history)-1).Children(func() { glyph(c, "arrow_right", 20, theme.TextMuted) }).OnClick(a.goForward)
		})
		separator(c, 32)
		ui.Button(c, "").Size(40, controlHeight).Background(raisedSurface(c)).Border(1, theme.Border).Label("Choose folder").Tooltip("Choose a folder (Ctrl+O)").Disabled(a.busy).Children(func() { filesystemGlyph(c, "folder", 20) }).OnClick(a.chooseFolder)
		ui.Row(c).Height(controlHeight).Grow(1).MinWidth(100).Background(theme.SurfacePressed).Border(1, theme.Border).Radius(5).AlignItems(ui.Center).Children(func() {
			address := ui.TextInput(c.Key("address"), &a.address).Bind(&a.pathHandle).Label("Scan path").Grow(1).MinWidth(40).Height(controlHeight-2).Background(ui.Transparent).Border(0, ui.Transparent).FontSize(14).Disabled(a.busy)
			if a.selectAddress {
				address.SetTextSelection(0, len([]rune(a.address)))
				a.selectAddress = false
			}
			if address.Submitted() {
				a.visitAddress()
			}
			ui.Button(c, "").Menu(func(menu *ui.Menu) {
				if menu.Item("Parent folder").Disabled(a.snapshot == nil || a.focus == 0).Chosen() {
					a.navigate(a.snapshot.Nodes[a.focus].Parent)
				}
				for _, location := range a.recentLocations {
					if menu.Item(location).Chosen() {
						a.address = location
						a.visitAddress()
					}
				}
			}).Size(30, controlHeight-2).Background(ui.Transparent).Border(0, ui.Transparent).Label("Recent locations").Children(func() { glyph(c, "chevron_down", 13, theme.Text) })
		})
		if width >= 1300 {
			ui.Text(c, "Scan target:").FontSize(14).Padding(0, 4)
		}
		selector := ui.SelectBase(c.Key("target"), &a.path)
		targetWidth, scanWidth, scanMenuWidth, stopWidth := float32(180), float32(96), float32(38), float32(90)
		if width < 1300 {
			targetWidth, scanWidth, scanMenuWidth, stopWidth = 130, 76, 28, 64
		}
		selector.Trigger.Width(targetWidth).Height(controlHeight).Padding(8, 12).Background(theme.SurfacePressed).Border(1, theme.Border).Radius(5).Label("Scan target").Tooltip(a.path).Disabled(a.busy).Children(func() {
			filesystemGlyph(c, "drive", 18)
			label := a.path
			for _, drive := range a.drives {
				if strings.EqualFold(drive.Path, a.path) {
					label = drive.Name + " (" + strings.TrimSuffix(drive.Path, `\`) + ")"
					break
				}
			}
			ui.Text(c, label).SingleLine().Grow(1).MinWidth(0).FontSize(14)
			glyph(c, "chevron_down", 12, theme.Text)
		})
		selector.Popup(func(panel ui.Element) {
			panel.Padding(4).Background(theme.SurfacePressed).Border(1, theme.Border).Radius(6)
			for _, drive := range a.drives {
				selector.Item(drive.Path).Padding(8, 12).Children(func() { glyph(c, "hard_drive", 16, theme.TextMuted); ui.Text(c, drive.Name+" ("+drive.Path+")") })
			}
			ui.Button(c, "Choose folder…").OnClick(a.chooseFolder)
		})
		ui.Row(c).Gap(0).Radius(5).Clip().Children(func() {
			ui.PrimaryButton(c, "").Width(scanWidth).Height(controlHeight).Radius(0).Label("Scan").Disabled(a.busy || a.historyBusy).OnClick(a.start).Children(func() { glyph(c, "play", 17, theme.AccentText); ui.Text(c, "Scan").FontSize(14).FontWeight(600) })
			ui.PrimaryButton(c, "").Menu(func(menu *ui.Menu) {
				if menu.Item("Scan selected target").Chosen() {
					a.start()
				}
				if menu.Item("Scan inspected location").Disabled(a.snapshot == nil).Chosen() {
					a.path = a.snapshot.Path(a.focus)
					a.start()
				}
				if menu.Item("Choose another folder…").Chosen() {
					a.chooseFolder()
				}
				if menu.Item("Scan options…").Chosen() {
					a.settingsOpen = true
				}
			}).Size(scanMenuWidth, controlHeight).Radius(0).BorderWidth(0, 0, 0, 1).BorderColor(theme.AccentPressed).Label("Scan options").Disabled(a.busy || a.historyBusy).Children(func() { glyph(c, "chevron_down", 13, theme.AccentText) })
		})
		ui.Button(c, "").Width(stopWidth).Height(controlHeight).Background(raisedSurface(c)).Border(1, theme.Border).Label("Stop").Disabled(!a.busy).OnClick(func() {
			if a.cancel != nil {
				a.cancel()
			}
		}).Children(func() { glyph(c, "stop", 16, theme.TextMuted); ui.Text(c, "Stop").FontSize(14) })
		separator(c, 32)
		ui.Button(c, "").Menu(a.exportMenu).Height(controlHeight).Padding(8, 12).Background(ui.Transparent).Border(0, ui.Transparent).Label("Export").Disabled(a.snapshot == nil || a.exporting).Children(func() {
			glyph(c, "document", 19, theme.Text)
			ui.Text(c, "Export").FontSize(14)
			glyph(c, "chevron_down", 11, theme.Text)
		})
		ui.Button(c, "").Height(controlHeight).Padding(8, 12).Background(ui.Transparent).Border(0, ui.Transparent).Label("Settings").Tooltip("Settings").OnClick(func() { a.settingsOpen = true }).Children(func() {
			glyph(c, "settings", 20, theme.Text)
			if width >= 1150 {
				ui.Text(c, "Settings").FontSize(14)
			}
		})
	}).Height(toolbarHeight).Shrink(0).Padding(0, 12).Gap(8).Background(theme.Background)
}

func (a *App) exportMenu(menu *ui.Menu) {
	if menu.Item("Current table results · CSV…").Disabled(a.queryBusy || a.tab == 2).Chosen() {
		a.exportRows(a.rows, "Export current table results")
	}
	if menu.Item("Current subtree · CSV…").Chosen() {
		a.exportAs("csv", a.focus)
	}
	if menu.Item("Current subtree · JSON…").Chosen() {
		a.exportAs("json", a.focus)
	}
	if menu.Item("Current subtree · summary report…").Chosen() {
		a.exportAs("txt", a.focus)
	}
	if menu.Item("Entire scan · CSV…").Chosen() {
		a.exportAs("csv", 0)
	}
	if menu.Item("Treemap · PNG…").Disabled(a.mapBusy).Chosen() {
		a.exportAs("png", a.mapRoot())
	}
}
