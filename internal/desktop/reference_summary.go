package desktop

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func referenceMetric(c *ui.Context, label, value, explanation string, width float32, strong bool, suffix ...string) {
	column := ui.Column(c).Height(38).Padding(0, 20).Gap(3).Justify(ui.Center).Tooltip(explanation)
	if width > 0 {
		column.Width(width).Shrink(0).BorderWidth(0, 1, 0, 0).BorderColor(c.Theme().Border)
	} else {
		column.Grow(1).MinWidth(0)
	}
	column.Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted).SingleLine()
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			text := ui.Text(c, value).FontSize(14).SingleLine().FontFeatures("tnum")
			if strong {
				text.FontWeight(600)
			}
			if len(suffix) > 0 && len(value) <= 8 {
				ui.Text(c, suffix[0]).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			}
		})
	})
}

func (a *App) scanStrip(c *ui.Context) {
	theme := c.Theme()
	ui.Column(c).Height(summaryHeight).Shrink(0).Padding(0, 12).Children(func() {
		ui.Row(c).Fill().Gap(0).AlignItems(ui.Center).Background(raisedSurface(c)).Border(1, theme.Border).Radius(6).Children(func() {
			ui.Row(c).Width(264).Shrink(0).Height(38).Padding(0, 20).Gap(18).AlignItems(ui.Center).BorderWidth(0, 1, 0, 0).BorderColor(theme.Border).Children(func() {
				filesystemGlyph(c, "drive", 32)
				ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
					path := a.path
					if a.snapshot != nil {
						path = a.snapshot.Root
					}
					ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
						ui.Text(c, path).FontSize(15).SingleLine().MinWidth(0).Tooltip(path)
						if drive, ok := a.summaryDrive(); ok && strings.EqualFold(drive.Path, path) {
							ui.Text(c, drive.Name).FontSize(12).TextColor(theme.TextMuted).SingleLine().MinWidth(0)
						}
					})
					if drive, ok := a.summaryDrive(); ok {
						ui.Progress(c, float64(drive.Total-drive.Free)/float64(drive.Total)).Height(10).Tooltip("Volume used: " + a.formatBytes(drive.Total-drive.Free) + " of " + a.formatBytes(drive.Total) + "; free: " + a.formatBytes(drive.Free) + ". Filesystem capacity, independent of analyzed totals.")
					} else {
						ui.Text(c, "Analyzed scope").FontSize(11).TextColor(theme.TextMuted)
					}
				})
			})
			if a.busy {
				referenceMetric(c, "Files visited", number(a.progress.Files.Load()), "Partial scan progress", 124, false)
				referenceMetric(c, "Folders visited", number(a.progress.Folders.Load()), "Partial scan progress", 154, false)
				referenceMetric(c, "Bytes processed", a.formatBytes(a.progress.Bytes.Load()), "Logical bytes processed so far", 160, true)
				referenceMetric(c, "Scanning", a.progress.Status()+" · "+time.Since(a.started).Round(time.Second).String(), "No percentage is invented for unknown work", 0, false)
				return
			}
			if a.mftBlocked {
				ui.Text(c, "MFT unavailable").Padding(0, 20).TextColor(theme.Warning)
				ui.Text(c, a.scanExplanation()).Grow(1).MinWidth(0).SingleLine()
				if !a.admin {
					ui.Button(c, "Run as administrator").OnClick(a.elevate)
				}
				return
			}
			if snapshot := a.snapshot; snapshot != nil {
				node := snapshot.Nodes[0]
				referenceMetric(c, "Size", a.formatBytes(node.Size), "Sum of logical file sizes in this scan.", 124, true)
				ratio := ""
				if node.Size > 0 {
					ratio = fmt.Sprintf("(%.1f%%)", 100*float64(node.Allocated)/float64(node.Size))
				}
				referenceMetric(c, "Allocated", a.formatBytes(node.Allocated), "Physical allocation attributed to indexed files "+ratio+" of logical size; not volume utilization. Excludes some filesystem overhead.", 154, true, ratio)
				referenceMetric(c, "Files", number(uint64(node.Files)), "Indexed file records; hard-linked content is counted once.", 98, false)
				referenceMetric(c, "Folders", number(uint64(node.Dirs)), "Subdirectories of the scan root.", 108, false)
				referenceMetric(c, "Last scan", snapshot.Started.Format("02 Jan 2006 15:04"), "Scan start time; live filesystem data, not an atomic filesystem snapshot.", 0, false)
			} else {
				ui.Text(c, "Ready to scan").Padding(0, 20).TextColor(theme.TextMuted)
			}
		})
	})
}

func (a *App) status(c *ui.Context) {
	theme := c.Theme()
	ui.Row(c).Height(statusHeight).Shrink(0).Padding(0, 20).Gap(12).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
		text, color, icon := "Ready", theme.TextMuted, "check"
		if a.busy {
			text, icon = "Scanning…", "arrow_counterclockwise"
		} else if a.exporting {
			text, icon = "Exporting…", "export"
		} else if a.notice != "" {
			text = a.notice
		} else if a.snapshot != nil {
			text, color = "Scan completed in "+a.snapshot.Elapsed.Round(time.Millisecond).String(), theme.Success
			if a.snapshot.Cancelled {
				text, color, icon = "Scan canceled · partial results", theme.Warning, "warning"
			} else if a.snapshot.Errors > 0 {
				text, color = "Scan completed with issues", theme.Warning
			}
		}
		glyph(c, icon, 17, color)
		ui.Text(c, text).SingleLine().MinWidth(0).FontSize(13).Tooltip(text)
		if snapshot := a.snapshot; snapshot != nil {
			separator(c, 15)
			ui.Text(c, number(uint64(snapshot.Nodes[0].Files))+" files").FontSize(13).TextColor(theme.TextMuted)
			ui.Text(c, number(uint64(snapshot.Nodes[0].Dirs))+" folders").FontSize(13).TextColor(theme.TextMuted)
			ui.Box(c).Grow(1)
			if snapshot.Errors > 0 {
				label := fmt.Sprintf("%d issues", snapshot.Errors)
				if snapshot.Errors == 1 {
					label = "1 issue"
				}
				ui.Button(c, label).Height(26).TextColor(theme.Warning).OnClick(func() { a.issuesOpen = true }).Children(func() { glyph(c, "warning", 17, theme.Warning) })
				separator(c, 15)
			}
			ui.Text(c, "Index memory: "+a.formatBytes(snapshot.Bytes())).FontSize(12).TextColor(theme.TextMuted)
		} else {
			ui.Box(c).Grow(1)
		}
		if a.admin {
			separator(c, 15)
			glyph(c, "person", 18, theme.TextMuted)
			ui.Text(c, "Administrator").FontSize(13).TextColor(theme.TextMuted)
		}
	})
}
