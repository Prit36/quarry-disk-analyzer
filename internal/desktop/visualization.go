package desktop

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
	"stratum/internal/treemap"
)

func (a *App) mapRoot() index.ID {
	if a.mapFor == index.None {
		return 0
	}
	return a.mapFor
}

func (a *App) focusMap(id index.ID) {
	if id == index.None {
		id = 0
	}
	if !a.snapshot.Nodes[id].IsDir() {
		id = a.snapshot.Nodes[id].Parent
	}
	if len(a.mapHistory) == 0 {
		a.mapHistory = []index.ID{a.mapRoot()}
		a.mapHistoryPos = 0
	}
	if a.mapRoot() != id {
		a.mapZoom = 1
		a.mapPanX, a.mapPanY = 0, 0
		a.mapHistory = append(a.mapHistory[:a.mapHistoryPos+1], id)
		a.mapHistoryPos++
	}
	a.requestMap(id)
}

func (a *App) mapBack() {
	if a.mapHistoryPos > 0 {
		a.mapHistoryPos--
		a.requestMap(a.mapHistory[a.mapHistoryPos])
	}
}

func (a *App) tileColor(id index.ID) ui.Color {
	for steps := 0; steps < 256 && a.snapshot.Nodes[id].Parent != 0 && a.snapshot.Nodes[id].Parent != index.None; steps++ {
		id = a.snapshot.Nodes[id].Parent
	}
	name := strings.ToLower(a.snapshot.NameView(id))
	switch name {
	case "users":
		return typePalette[1]
	case "program files (x86)":
		return typePalette[2]
	case "windows":
		return typePalette[4]
	case "program files":
		return typePalette[3]
	}
	if strings.HasPrefix(name, "$") && strings.Contains(a.snapshot.Engine, "MFT") {
		return typePalette[0]
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(name))
	return typePalette[hash.Sum32()%uint32(len(typePalette))]
}

func (a *App) mapTooltip(tile treemap.Tile) string {
	if tile.Other {
		return "Other items: " + a.formatBytes(tile.Bytes) + ". Double-click to inspect every indexed file in this directory."
	}
	node := a.snapshot.Nodes[tile.ID]
	parent := node.Parent
	if parent == index.None {
		parent = 0
	}
	text := fmt.Sprintf("%s\nLogical: %s · Allocated: %s\n%.1f%% of parent · %s files", a.snapshot.Path(tile.ID), a.formatBytes(node.Size), a.formatBytes(node.Allocated), 100*fraction(a.metricBytes(tile.ID), a.metricBytes(parent)), number(uint64(node.Files)))
	if strings.HasPrefix(a.snapshot.NameView(tile.ID), "$") && strings.Contains(a.snapshot.Engine, "MFT") {
		text += "\nNTFS metadata: logical size may represent reserved address space, not reclaimable physical storage."
	}
	return text
}

func (a *App) mapView(c *ui.Context) {
	theme := c.Theme()
	ui.Column(c).Fill().Gap(0).Background(theme.Surface).Border(1, theme.Border).Radius(panelRadius).Clip().Children(func() {
		a.visualizationToolbar(c)
		if a.mapBusy && a.mapTab != 3 {
			ui.Text(c, "Updating visualization…").Padding(12).TextColor(theme.TextMuted)
			ui.Box(c).Grow(1)
			return
		}
		if a.mapTab == 1 {
			a.sunburstView(c)
			return
		}
		if a.mapTab == 2 {
			a.fileTypesChart(c)
			return
		}
		if a.mapTab == 3 {
			a.historyView(c)
			return
		}
		box := ui.Box(c.Key("treemap")).Grow(1).MinHeight(0).Margin(8).Clip().Focusable().Label("Storage treemap")
		bounds := box.Bounds()
		if a.mapZoom <= 0 {
			a.mapZoom = 1
		}
		layoutWidth, layoutHeight := bounds.W*float32(a.mapZoom), bounds.H*float32(a.mapZoom)
		if a.mapWidth != layoutWidth || a.mapHeight != layoutHeight {
			a.mapWidth, a.mapHeight = layoutWidth, layoutHeight
			a.cells = treemap.NestedLayout(a.branches, treemap.Rect{X: 1, Y: 1, W: max(0, layoutWidth-2), H: max(0, layoutHeight-2)})
		}
		if bounds.W == 0 {
			c.Invalidate()
		}
		pointerX, pointerY, over := box.PointerPosition()
		if a.mapZoom > 1 {
			box.Cursor(ui.CursorGrab)
			if dx, dy, pressed := box.Dragged(); pressed {
				a.mapPanX += dx
				a.mapPanY += dy
				box.Cursor(ui.CursorGrabbing)
			}
		}
		limitX, limitY := max(0, (layoutWidth-bounds.W)/2), max(0, (layoutHeight-bounds.H)/2)
		a.mapPanX, a.mapPanY = max(-limitX, min(limitX, a.mapPanX)), max(-limitY, min(limitY, a.mapPanY))
		offsetX, offsetY := (bounds.W-layoutWidth)/2+a.mapPanX, (bounds.H-layoutHeight)/2+a.mapPanY
		pointerX, pointerY = pointerX-offsetX, pointerY-offsetY
		hit := -1
		if over {
			for position := len(a.cells) - 1; position >= 0; position-- {
				bounds := a.cells[position].Tile.Rect
				if pointerX >= bounds.X && pointerX < bounds.X+bounds.W && pointerY >= bounds.Y && pointerY < bounds.Y+bounds.H {
					hit = position
					break
				}
			}
		}
		if hit >= 0 {
			tile := a.cells[hit].Tile
			box.Tooltip(a.mapTooltip(tile))
			if box.Clicked() && !tile.Other {
				a.selectMap(tile.ID)
			}
			if box.DoubleClicked() {
				if tile.Other {
					a.focus = tile.ID
					a.address = a.snapshot.Path(tile.ID)
					a.tab = 1
					a.query = ""
					a.requestRows()
				} else {
					a.focusMap(tile.ID)
				}
			}
			box.ContextMenu(func(menu *ui.Menu) {
				if menu.Item("Focus in explorer").Chosen() {
					a.selectMap(tile.ID)
				}
				if menu.Item("Zoom into folder").Chosen() {
					a.focusMap(tile.ID)
				}
				if menu.Item("Show in Explorer").Disabled(tile.Other).Chosen() {
					a.selected = tile.ID
					a.reveal()
				}
				if menu.Item("Copy path").Disabled(tile.Other).Chosen() {
					a.selected = tile.ID
					a.copyPath()
				}
			})
		}
		cells := a.cells
		box.Draw(func(painter *ui.Painter, origin ui.Rect) {
			if len(cells) == 0 {
				painter.Text(origin.X+12, origin.Y+12, "No nonzero data for this metric.", 12, theme.TextMuted)
			}
			for position, cell := range cells {
				tile := cell.Tile
				bounds := tile.Rect
				if bounds.W*bounds.H < 1 {
					continue
				}
				rect := ui.Rect{X: origin.X + offsetX + bounds.X, Y: origin.Y + offsetY + bounds.Y, W: max(0, bounds.W-1), H: max(0, bounds.H-1)}
				name := a.snapshot.NameView(tile.ID)
				color := a.tileColor(tile.ID)
				if tile.Other {
					name, color = "Other items", typePalette[7]
				}
				if cell.Depth > 0 {
					color = color.Mix(theme.Surface, min(.32, float32(cell.Depth)*.045))
				}
				if position == hit {
					color = color.Mix(theme.Text, .14)
				}
				painter.FillGradient(rect, ui.LinearGradient{From: color.Mix(ui.Hex("#FFFFFF"), .045), To: color.Mix(ui.Hex("#000000"), .12), Angle: 180}, 1)
				if tile.ID == a.selected && !tile.Other {
					painter.Stroke(rect, theme.Text, 0, 2)
				}
				textColor := ui.Hex("#FFFFFF")
				painter.Clip(rect, 0, func() {
					if cell.Group && rect.W > 60 {
						header := treemap.LabelHeight(bounds)
						painter.Text(rect.X+10, rect.Y+7, truncate(name, int((rect.W-20)/7)), 13, textColor)
						if header >= 48 {
							painter.Text(rect.X+10, rect.Y+27, a.formatBytes(tile.Bytes), 13, textColor)
						}
						if header >= 68 {
							painter.Text(rect.X+10, rect.Y+47, fmt.Sprintf("%.1f%%", 100*fraction(tile.Bytes, a.mapTotal)), 13, textColor)
						}
					} else if rect.W > 85 && rect.H > 26 {
						painter.Text(rect.X+12, rect.Y+8, truncate(name, int((rect.W-24)/7)), 14, textColor)
						if rect.H > 44 {
							painter.Text(rect.X+12, rect.Y+29, a.formatBytes(tile.Bytes), 14, textColor)
						}
						if rect.H > 62 {
							painter.Text(rect.X+12, rect.Y+50, fmt.Sprintf("%.1f%%", 100*fraction(tile.Bytes, a.mapTotal)), 13, textColor)
						}
					}
				})
			}
		})
	})
}
