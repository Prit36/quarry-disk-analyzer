package desktop

import "github.com/egoist/mygo/ui"

const (
	windowBackground         = "#101820"
	panelRadius      float32 = 6
	controlHeight    float32 = 38
	toolbarHeight    float32 = 54
	summaryHeight    float32 = 62
	statusHeight     float32 = 38
	defaultDivider   float32 = 71.5
)

var folderColor = ui.Hex("#F1B544")
var typePalette = []ui.Color{ui.Hex("#246ED1"), ui.Hex("#2C9C45"), ui.Hex("#D4A01C"), ui.Hex("#B84045"), ui.Hex("#714E9E"), ui.Hex("#288EA6"), ui.Hex("#748C9F"), ui.Hex("#526A80")}

func raisedSurface(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#19232F")
	}
	return ui.Hex("#EAF0F6")
}

func designTheme(light, compact bool) *ui.Theme {
	theme := *ui.DarkTheme()
	if light {
		theme = *ui.LightTheme()
		theme.Background = ui.Hex("#F3F5F8")
		theme.Surface = ui.Hex("#FFFFFF")
		theme.SurfaceHover = ui.Hex("#E7EDF5")
		theme.SurfacePressed = ui.Hex("#D8E2EF")
		theme.Border = ui.Hex("#C8D2DF")
		theme.Text = ui.Hex("#182638")
		theme.TextMuted = ui.Hex("#52647A")
		theme.Accent = ui.Hex("#086BC5")
		theme.Selection = ui.Hex("#D1E6FC")
	} else {
		theme.Background = ui.Hex(windowBackground)
		theme.Surface = ui.Hex("#121A23")
		theme.SurfaceHover = ui.Hex("#1D2B3A")
		theme.SurfacePressed = ui.Hex("#1B2937")
		theme.Border = ui.Hex("#293847")
		theme.Text = ui.Hex("#E8EDF5")
		theme.TextMuted = ui.Hex("#ACB9CA")
		theme.Accent = ui.Hex("#0086FA")
		theme.AccentHover = ui.Hex("#3297FF")
		theme.AccentPressed = ui.Hex("#1069C7")
		theme.AccentText = ui.Hex("#FFFFFF")
		theme.Selection = ui.Hex("#0754B0")
		theme.Focus = ui.Hex("#3297FF")
		theme.Danger = ui.Hex("#EA6262")
		theme.Warning = ui.Hex("#F1B544")
		theme.Success = ui.Hex("#33B86D")
	}
	theme.Font = "Segoe UI"
	theme.FontSize = 13
	theme.Spacing = 4
	theme.Radius = 5
	if compact {
		theme.FontSize = 12
		theme.Spacing = 3
	}
	return &theme
}

func dataTable(c *ui.Context, state *ui.ListState, columns []ui.TableColumn, count int, cell func(int, int)) ui.Element {
	previous := c.Theme()
	theme := *previous
	theme.Spacing = 3.5
	if previous.FontSize == 12 {
		theme.Spacing = 3
	}
	theme.Accent = previous.Selection
	if !theme.Dark {
		theme.AccentText = theme.Text
	}
	c.SetTheme(&theme)
	rowHeight, padY, padX := theme.Spacing*8, theme.Spacing*1.5, theme.Spacing*2.5
	table := ui.Table(c, state, columns, count, func(row, column int) {
		wrapper := ui.Row(c).Grow(1).MinWidth(0).Height(rowHeight).Margin(-padY, -padX).Padding(padY, padX).AlignItems(ui.Center).Justify(columns[column].Align)
		selected := state.Selected != nil && *state.Selected == row
		if selected && theme.Dark {
			wrapper.LinearGradient(ui.LinearGradient{From: theme.Accent.Mix(theme.Text, .025), To: theme.Accent.Mix(ui.Hex("#000000"), .10), Angle: 180})
		} else if row%2 == 1 && theme.Dark {
			wrapper.Background(ui.RGBA(36, 52, 69, .35))
		}
		wrapper.Children(func() { cell(row, column) })
	})
	headerColor := raisedSurface(c)
	table.Draw(func(painter *ui.Painter, bounds ui.Rect) {
		painter.Fill(ui.Rect{X: bounds.X, Y: bounds.Y, W: bounds.W, H: min(rowHeight, bounds.H)}, headerColor, 0)
	})
	table.FontFeatures("tnum")
	c.SetTheme(previous)
	return table
}

func proportionBar(c *ui.Context, share float64, width float32, accent ui.Color) ui.Element {
	track := c.Theme().Border
	box := ui.Box(c).Height(4)
	if width > 0 {
		box.Width(width)
	} else {
		box.Grow(1)
	}
	return box.Draw(func(painter *ui.Painter, bounds ui.Rect) {
		painter.Fill(bounds, track, 1)
		bounds.W *= float32(max(0, min(1, share)))
		painter.Fill(bounds, accent, 1)
	})
}
