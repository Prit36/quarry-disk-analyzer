package desktop

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
	"stratum/internal/treemap"
)

func sectorPath(centerX, centerY, inner, outer float32, start, end float64) ui.Path {
	var path ui.Path
	steps := max(2, int(math.Ceil((end-start)*24)))
	for step := 0; step <= steps; step++ {
		angle := start + (end-start)*float64(step)/float64(steps) - math.Pi/2
		pointX, pointY := centerX+outer*float32(math.Cos(angle)), centerY+outer*float32(math.Sin(angle))
		if step == 0 {
			path.MoveTo(pointX, pointY)
		} else {
			path.LineTo(pointX, pointY)
		}
	}
	for step := steps; step >= 0; step-- {
		angle := start + (end-start)*float64(step)/float64(steps) - math.Pi/2
		path.LineTo(centerX+inner*float32(math.Cos(angle)), centerY+inner*float32(math.Sin(angle)))
	}
	path.Close()
	return path
}

func (a *App) sunburstView(c *ui.Context) {
	box := ui.Box(c.Key("sunburst")).Grow(1).MinHeight(0).Clip().Focusable().Label("Storage sunburst")
	bounds := box.Bounds()
	if bounds.W == 0 {
		c.Invalidate()
	}
	if a.sectors == nil && len(a.branches) > 0 {
		a.sectors = treemap.Sunburst(a.branches, 4)
	}
	centerX, centerY := bounds.W/2, bounds.H/2
	radius := max(0, min(bounds.W, bounds.H)/2-8)
	hole := radius * .18
	levels := 1
	for _, sector := range a.sectors {
		levels = max(levels, sector.Depth+1)
	}
	ring := (radius - hole) / float32(levels)
	if a.sunPaths == nil || a.sunWidth != bounds.W || a.sunHeight != bounds.H {
		a.sunWidth, a.sunHeight = bounds.W, bounds.H
		a.sunPaths = make([]ui.Path, len(a.sectors))
		for position, sector := range a.sectors {
			inner := hole + float32(sector.Depth)*ring
			a.sunPaths[position] = sectorPath(centerX, centerY, inner, inner+ring, sector.Start, sector.End)
		}
	}
	pointX, pointY, over := box.PointerPosition()
	hit := -1
	if over && ring > 0 {
		deltaX, deltaY := float64(pointX-centerX), float64(pointY-centerY)
		distance := float32(math.Hypot(deltaX, deltaY))
		depth := int((distance - hole) / ring)
		angle := math.Atan2(deltaY, deltaX) + math.Pi/2
		if angle < 0 {
			angle += 2 * math.Pi
		}
		if distance >= hole && distance <= radius {
			for position, sector := range a.sectors {
				if sector.Depth == depth && angle >= sector.Start && angle < sector.End {
					hit = position
					break
				}
			}
		}
	}
	if hit >= 0 {
		sector := a.sectors[hit]
		box.Tooltip(a.mapTooltip(treemap.Tile{ID: sector.ID, Bytes: sector.Bytes, Other: sector.Other}))
		if box.Clicked() {
			if sector.Other {
				a.focus = sector.ID
				a.tab = 1
				a.query = ""
				a.requestRows()
			} else {
				a.selectMap(sector.ID)
				a.focusMap(sector.ID)
			}
		}
	}
	box.Draw(func(painter *ui.Painter, origin ui.Rect) {
		painter.Transform(ui.Translation(origin.X, origin.Y), 0, 0, func() {
			for position, sector := range a.sectors {
				color := a.tileShade(sector.ID, sector.Other)
				if position == hit {
					color = color.Mix(c.Theme().Text, .18)
				}
				painter.FillPath(&a.sunPaths[position], color)
				painter.StrokePath(&a.sunPaths[position], 1, c.Theme().Surface)
				if sector.ID == a.selected && !sector.Other {
					painter.StrokePath(&a.sunPaths[position], 2, c.Theme().Text)
				}
			}
			if len(a.sectors) == 0 {
				painter.Text(12, 12, "No nonzero data for this metric.", 12, c.Theme().TextMuted)
			}
			painter.Text(12, 12, "Rings = depth · angle = "+a.metricName(), 11, c.Theme().TextMuted)
		})
	})
}

func (a *App) fileTypesChart(c *ui.Context) {
	accent := c.Theme().Accent
	ui.Text(c, "File types · "+a.metricName()+" · "+a.snapshot.Path(a.mapRoot())).SingleLine().FontSize(12).Padding(6, 8)
	dataTable(c.Key("type-chart"), &a.mapTypeList, []ui.TableColumn{{Title: "Extension", Width: 150, MinWidth: 100}, {Title: "Share of visualization scope", MinWidth: 140}, {Title: a.metricName(), Width: 110, Align: ui.End}, {Title: "Files", Width: 90, Align: ui.End}}, len(a.mapTypes), func(row, column int) {
		group := a.mapTypes[row]
		value := group.Bytes
		if a.metric == 1 {
			value = group.Allocated
		}
		switch column {
		case 0:
			ui.Text(c, group.Extension).FontSize(12).SingleLine().Tooltip(group.Extension)
		case 1:
			ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
				proportionBar(c, fraction(value, a.mapTotal), 0, accent).Height(10).Label("Visualization share: " + group.Extension)
				ui.Text(c, fmt.Sprintf("%.1f%%", 100*fraction(value, a.mapTotal))).Width(60).FontSize(12)
			})
		case 2:
			ui.Text(c, a.formatBytes(value)).FontSize(12)
		case 3:
			ui.Text(c, number(group.Count)).FontSize(12)
		}
	}).Grow(1).MinHeight(0)
}
