package desktop

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
	"stratum/internal/platform"
)

func (a *App) visitAddress() {
	path := strings.Trim(strings.TrimSpace(a.address), "\"")
	if a.snapshot != nil {
		for id := a.focus; id != index.None; id = a.snapshot.Nodes[id].Parent {
			if strings.EqualFold(filepath.Clean(path), filepath.Clean(a.snapshot.Path(id))) {
				a.navigate(id)
				return
			}
		}
		id := index.ID(0)
		relative, err := filepath.Rel(a.snapshot.Root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			found := true
			if relative != "." {
				for _, part := range strings.Split(relative, string(filepath.Separator)) {
					next := index.None
					for child := a.snapshot.Nodes[id].FirstChild; child != index.None; child = a.snapshot.Nodes[child].NextSibling {
						if a.snapshot.Nodes[child].IsDir() && strings.EqualFold(a.snapshot.NameView(child), part) {
							next = child
							break
						}
					}
					if next == index.None {
						found = false
						break
					}
					id = next
				}
			}
			if found {
				a.navigate(id)
				return
			}
		}
	}
	a.path = path
	a.start()
}

func (a *App) summaryDrive() (platform.Drive, bool) {
	if a.snapshot == nil {
		return platform.Drive{}, false
	}
	for _, drive := range a.drives {
		if strings.HasPrefix(strings.ToLower(a.snapshot.Root), strings.ToLower(drive.Path)) && drive.Total > 0 && drive.Free <= drive.Total {
			return drive, true
		}
	}
	return platform.Drive{}, false
}

func (a *App) dialogs(c *ui.Context) {
	a.advancedFilters(c)
	ui.Modal(c.Key("history-dialog"), &a.historyOpen, func() {
		ui.Column(c).Width(850).Height(450).Padding(12).Gap(8).Children(func() {
			ui.Text(c, "Saved scans").Bold()
			a.historyView(c)
			ui.Button(c, "Close history").OnClick(func() { a.historyOpen = false })
		})
	})
	ui.Modal(c.Key("settings"), &a.settingsOpen, func() {
		_, height := c.Size()
		padding, gap := float32(16), float32(12)
		if height < 700 {
			padding, gap = 12, 6
		}
		ui.Column(c).Width(380).Padding(padding).Gap(gap).Children(func() {
			ui.Text(c, "Settings").Bold().FontSize(14)
			if ui.Checkbox(c, &a.light, "Light theme").Label("Toggle theme").Changed() && a.window != nil {
				source := mygo.ThemeDark
				if a.light {
					source = mygo.ThemeLight
				}
				mygo.Theme.SetSource(source)
			}
			ui.Checkbox(c, &a.compact, "Compact table density")
			ui.Checkbox(c, &a.followSelection, "Analytics follows selection")
			if a.snapshot != nil {
				ui.Row(c).Gap(8).Children(func() {
					ui.Button(c, "Advanced filters").OnClick(func() { a.settingsOpen = false; a.advancedOpen = true })
					ui.Button(c, "Saved scans").OnClick(func() { a.settingsOpen = false; a.historyOpen = true })
				})
			}
			ui.Select(c, &a.units, []string{"Binary", "Decimal"}).Label("Size units")
			ui.Checkbox(c, &a.analyticsCollapsed, "Collapse analytics panel")
			ui.Checkbox(c, &a.mapCollapsed, "Collapse visualization")
			if ui.Checkbox(c, &a.showMetadata, "Show NTFS metadata in visualizations").Changed() && a.snapshot != nil {
				a.requestMap(a.mapRoot())
			}
			ui.Text(c, "NTFS reserved records may have special logical-size semantics. Hiding them changes only visualization scope; scan totals still include them.").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Text(c, "Scan method").TextColor(c.Theme().TextMuted)
			ui.Select(c, &a.scanMode, []string{"Automatic", "Folders"}).Label("Scan method").Disabled(a.busy)
			ui.Text(c, "Automatic uses NTFS MFT for drive roots. Folders explicitly enumerates directories.").FontSize(12)
			if !a.admin {
				ui.Button(c, "Run as administrator").Disabled(a.busy).OnClick(a.elevate)
			}
			ui.Button(c, "Done").OnClick(func() { a.settingsOpen = false; a.savePreferences() })
		})
	})
	ui.Modal(c.Key("issues"), &a.issuesOpen, func() {
		ui.Column(c).Width(700).Height(380).Padding(16).Gap(12).Children(func() {
			ui.Text(c, "Scan issues").Bold()
			if a.snapshot != nil {
				ui.Text(c, fmt.Sprintf("Showing %d retained samples of %d issues", len(a.snapshot.Issues), a.snapshot.Errors)).FontSize(12)
				dataTable(c.Key("issue-table"), &a.issueList, []ui.TableColumn{{Title: "Path", Width: 220, MinWidth: 120}, {Title: "Category", Width: 110}, {Title: "Explanation", MinWidth: 150}, {Title: "Operation", Width: 80}}, len(a.snapshot.Issues), func(row, column int) {
					issue := a.snapshot.Issues[row]
					text := issue.Path
					switch column {
					case 1:
						text = issueCategory(issue.Message)
					case 2:
						text = issue.Message
					case 3:
						text = "Scan"
					}
					ui.Text(c, text).SingleLine().FontSize(12).Tooltip(text)
				}).Grow(1).MinHeight(0)
				ui.Button(c, "Copy issues").OnClick(func() {
					var lines []string
					for _, issue := range a.snapshot.Issues {
						lines = append(lines, issue.Path+"\t"+issue.Message)
					}
					mygo.Clipboard.WriteText(strings.Join(lines, "\n"))
				})
			}
			ui.Button(c, "Close").OnClick(func() { a.issuesOpen = false })
		})
	})
}
