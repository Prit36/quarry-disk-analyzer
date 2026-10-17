package desktop

import (
	"math"

	"github.com/egoist/mygo/ui"
)

func (a *App) visualizationToolbar(c *ui.Context) {
	theme := c.Theme()
	width, _ := c.Size()
	root := a.mapRoot()
	ui.Row(c).Height(38).Shrink(0).Gap(8).Padding(0, 4, 0, 0).AlignItems(ui.Center).Background(raisedSurface(c)).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		ui.Row(c).Height(38).Gap(0).Children(func() {
			for tab, label := range []string{"Treemap", "Sunburst", "File types", "History"} {
				background, border, foreground := ui.Transparent, ui.Transparent, theme.TextMuted
				if a.mapTab == tab {
					background, border, foreground = ui.Hex("#0B345C"), theme.Accent, theme.Text
					if !theme.Dark {
						foreground = ui.Hex("#FFFFFF")
					}
				}
				button := ui.Button(c, "").Label(label).Height(38).Padding(8, 18).Background(background).BorderWidth(0, 0, 2, 0).BorderColor(border).TextColor(foreground).FontSize(13)
				button.Children(func() {
					if tab == 0 {
						glyph(c, "treemap", 16, theme.Accent)
					} else if tab == 3 {
						glyph(c, "history", 15, theme.TextMuted)
					}
					ui.Text(c, label)
				})
				button.OnClick(func() {
					if a.mapTab != tab {
						a.mapTab = tab
						if tab != 3 {
							a.requestMap(root)
						}
					}
				})
			}
		})
		ui.Box(c).Grow(1)
		ui.Text(c, a.snapshot.Path(root)).SingleLine().MinWidth(0).MaxWidth(200).FontSize(13).Tooltip(a.snapshot.Path(root))
		value := a.formatBytes(a.mapTotal)
		if width >= 1300 {
			value += " (" + number(a.mapTotal) + " bytes)"
		}
		ui.Text(c, value).FontSize(11).SingleLine().TextColor(theme.TextMuted).Tooltip("Visible visualization scope. Scan totals remain global.")
		separator(c, 18)
		ui.Button(c, "").Size(24, 26).Padding(4).Background(ui.Transparent).Border(0, ui.Transparent).Label("Treemap back").Tooltip("Previous visualization root").Disabled(a.mapHistoryPos == 0).OnClick(a.mapBack).Children(func() { glyph(c, "arrow_left", 16, theme.TextMuted) })
		ui.Button(c, "").Size(24, 26).Padding(4).Background(ui.Transparent).Border(0, ui.Transparent).Label("Zoom out").Tooltip("Zoom out; drag the treemap to pan when enlarged").Disabled(a.mapTab != 0 || a.mapZoom <= .25).OnClick(func() { a.mapZoom = max(.25, a.mapZoom/math.Sqrt2) }).Children(func() { glyph(c, "subtract", 16, theme.Text) })
		zoom := math.Log2(max(.25, a.mapZoom))
		sliderWidth := float32(112)
		if width < 1300 {
			sliderWidth = 70
		}
		slider := ui.SliderBase(c.Key("map-zoom"), &zoom, -2, 2).Width(sliderWidth).Height(24).Padding(0, 6).Label("Treemap zoom").Tooltip("Zoom the viewport. Drag the map to pan; double-click a directory to drill down.").Disabled(a.mapTab != 0)
		slider.Draw(func(painter *ui.Painter, bounds ui.Rect) {
			track := ui.Rect{X: bounds.X + 6, Y: bounds.Y + 10, W: bounds.W - 12, H: 4}
			painter.Fill(track, theme.Border, 2)
			track.W *= float32((zoom + 2) / 4)
			painter.Fill(track, theme.Accent, 2)
			painter.Fill(ui.Rect{X: track.X + track.W - 6, Y: bounds.Y + 6, W: 12, H: 12}, theme.Accent, 6)
		})
		if slider.Changed() {
			a.mapZoom = math.Exp2(zoom)
		}
		ui.Button(c, "").Size(24, 26).Padding(4).Background(ui.Transparent).Border(0, ui.Transparent).Label("Zoom in").Disabled(a.mapTab != 0 || a.mapZoom >= 4).OnClick(func() { a.mapZoom = min(4, a.mapZoom*math.Sqrt2) }).Children(func() { glyph(c, "add", 16, theme.Text) })
		separator(c, 18)
		ui.Button(c, "").Size(24, 26).Padding(4).Background(ui.Transparent).Border(0, ui.Transparent).Label("Visualization root").Tooltip("Return to the scan root and reset zoom").OnClick(func() { a.mapZoom = 1; a.mapPanX, a.mapPanY = 0, 0; a.focusMap(0) }).Children(func() { glyph(c, "home", 16, theme.TextMuted) })
		metric := a.metricName()
		if ui.Select(c.Key("map-metric"), &metric, []string{"Size", "Allocated"}).Width(124).Height(28).Label("Treemap metric").Changed() {
			a.metric = 0
			if metric == "Allocated" {
				a.metric = 1
			}
			a.requestMap(root)
		}
	})
}
