package desktop

import "github.com/egoist/mygo/ui"

func (a *App) resultToolbar(c *ui.Context) {
	theme := c.Theme()
	width, _ := c.Size()
	buttonWidth := float32(120)
	if width < 1300 {
		buttonWidth = 96
	}
	ui.Row(c).Height(44).Shrink(0).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Height(40).Padding(2).Gap(2).Border(1, theme.Border).Radius(5).Background(theme.Surface).Children(func() {
			for tab, label := range []string{"Tree view", "File view", "Details"} {
				icon := []string{"tree", "list", "details"}[tab]
				background, foreground := ui.Transparent, theme.TextMuted
				if a.tab == tab {
					background, foreground = ui.Hex("#0B345C"), theme.Text
					if !theme.Dark {
						foreground = ui.Hex("#FFFFFF")
					}
				}
				padding := float32(12)
				if width < 1300 {
					padding = 6
				}
				ui.Button(c, "").Label(label).Width(buttonWidth).Height(34).Padding(4, padding).Background(background).Border(0, ui.Transparent).Radius(4).FontSize(13).Children(func() { glyph(c, icon, 17, foreground); ui.Text(c, label).SingleLine() }).TextColor(foreground).OnClick(func() {
					if a.tab != tab {
						a.tab = tab
						if tab != 2 {
							a.requestRows()
						}
					}
				})
			}
		})
		if a.tab != 2 {
			ui.Row(c).Height(40).Grow(1).MinWidth(50).Padding(3, 10).Gap(8).Background(theme.Surface).Border(1, theme.Border).Radius(5).AlignItems(ui.Center).ContextMenu(func(menu *ui.Menu) {
				if menu.Item("Filter current subtree").Checked(a.searchScope != "Scan").Chosen() {
					a.searchScope = "Subtree"
					a.requestRows()
				}
				if menu.Item("Filter entire scan").Checked(a.searchScope == "Scan").Chosen() {
					a.searchScope = "Scan"
					a.requestRows()
				}
				if menu.Item("Advanced filters…").Chosen() {
					a.advancedOpen = true
				}
			}).Children(func() {
				glyph(c, "search", 18, theme.TextMuted)
				if ui.TextInput(c.Key("search"), &a.query).Bind(&a.searchHandle).Placeholder("Filter by name (Ctrl+F)…").Label("Search files").Tooltip("Filter indexed "+a.searchScope+" data. Right-click for scope or use Advanced filters in Settings.").Grow(1).MinWidth(20).Height(32).Padding(0).Background(ui.Transparent).Border(0, ui.Transparent).FontSize(13).Changed() {
					a.setQuery(c)
				}
				if a.query != "" {
					if ui.Select(c.Key("scope"), &a.searchScope, []string{"Subtree", "Scan"}).Width(84).Height(26).Label("Search scope").Changed() {
						a.requestRows()
					}
					ui.Button(c, "").Size(24, 24).Padding(4).Background(ui.Transparent).Border(0, ui.Transparent).Label("Clear filter").OnClick(func() { a.query = ""; a.requestRows() }).Children(func() { glyph(c, "dismiss", 14, theme.TextMuted) })
				}
			})
		}
	})
}
