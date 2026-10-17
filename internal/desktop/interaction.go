package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
)

func allResultColumns() []ui.TableColumn {
	return []ui.TableColumn{{Title: "Name", MinWidth: 300, Sortable: true}, {Title: "% of Parent", Width: 120, MinWidth: 100, Align: ui.End, Sortable: true}, {Title: "Size", Width: 122, MinWidth: 80, Align: ui.End, Sortable: true}, {Title: "Allocated", Width: 130, MinWidth: 80, Align: ui.End, Sortable: true}, {Title: "Files", Width: 112, MinWidth: 60, Align: ui.End, Sortable: true}, {Title: "Folders", Width: 108, MinWidth: 60, Align: ui.End, Sortable: true}, {Title: "Modified", Width: 170, MinWidth: 100, Align: ui.End, Sortable: true}}
}

func (a *App) resultColumns() ([]ui.TableColumn, []int) {
	columns := []ui.TableColumn{}
	indices := []int{}
	for position, column := range allResultColumns() {
		if column.Title == "Name" || !a.hiddenColumns[column.Title] {
			columns = append(columns, column)
			indices = append(indices, position)
		}
	}
	return columns, indices
}

func (a *App) columnMenu(menu *ui.Menu) {
	for _, column := range allResultColumns() {
		if menu.Item(column.Title).Checked(!a.hiddenColumns[column.Title]).Disabled(column.Title == "Name").Chosen() {
			a.hiddenColumns[column.Title] = !a.hiddenColumns[column.Title]
		}
	}
	menu.Separator()
	if menu.Item("Reset columns").Chosen() {
		a.hiddenColumns = make(map[string]bool)
		a.list.Columns = ui.TableLayout{}
	}
}

func (a *App) issueExplanation(id index.ID) string {
	path := a.snapshot.Path(id)
	for _, issue := range a.snapshot.Issues {
		if strings.EqualFold(path, issue.Path) {
			return issue.Message
		}
	}
	if a.snapshot.Nodes[id].Flags&index.Unplaced != 0 {
		return "Unresolved NTFS parent; filesystem actions unavailable."
	}
	return "Incomplete metadata. Open Scan issues for the retained error samples."
}

func issueCategory(message string) string {
	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "denied"):
		return "Access denied"
	case strings.Contains(message, "not exist") || strings.Contains(message, "cannot find"):
		return "Missing path"
	case strings.Contains(message, "reparse") || strings.Contains(message, "junction"):
		return "Directory link"
	case strings.Contains(message, "metadata") || strings.Contains(message, "parent"):
		return "Metadata"
	default:
		return "Scan error"
	}
}

func (a *App) advancedFilters(c *ui.Context) {
	ui.Modal(c.Key("advanced-filter"), &a.advancedOpen, func() {
		ui.Column(c).Width(430).Padding(16).Gap(8).Children(func() {
			ui.Text(c, "Advanced filters").Bold().FontSize(14)
			ui.Text(c, "Filters apply to already indexed data.").FontSize(12).TextColor(c.Theme().TextMuted)
			ui.TextInput(c, &a.filterName).Placeholder("Name contains").Label("Name contains")
			ui.TextInput(c, &a.filterExtension).Placeholder("Extension, e.g. zip or none").Label("Extension filter")
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c, &a.filterMinimum).Placeholder("Min, e.g. 1GiB").Label("Minimum size").Grow(1).MinWidth(0)
				ui.TextInput(c, &a.filterMaximum).Placeholder("Max, e.g. 10GiB").Label("Maximum size").Grow(1).MinWidth(0)
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c, &a.filterAfter).Placeholder("Modified after YYYY-MM-DD").Label("Modified after").Grow(1).MinWidth(0)
				ui.TextInput(c, &a.filterBefore).Placeholder("Before YYYY-MM-DD").Label("Modified before").Grow(1).MinWidth(0)
			})
			ui.Select(c, &a.filterKind, []string{"Files", "Folders"}).Label("Entry kind")
			ui.Select(c, &a.searchScope, []string{"Subtree", "Scan"}).Label("Filter scope")
			ui.Row(c).Gap(8).Children(func() {
				ui.PrimaryButton(c, "Apply filters").OnClick(func() {
					terms := []string{strings.TrimSpace(a.filterName)}
					for _, field := range []struct{ prefix, value string }{{"ext:", a.filterExtension}, {"min:", a.filterMinimum}, {"max:", a.filterMaximum}, {"after:", a.filterAfter}, {"before:", a.filterBefore}} {
						if value := strings.TrimSpace(field.value); value != "" {
							terms = append(terms, field.prefix+value)
						}
					}
					if a.filterKind == "Folders" {
						terms = append(terms, "type:folder")
					} else {
						terms = append(terms, "type:file")
					}
					a.query = strings.TrimSpace(strings.Join(terms, " "))
					a.tab = 1
					a.advancedOpen = false
					a.requestRows()
				})
				ui.Button(c, "Clear all").OnClick(func() {
					a.query = ""
					a.filterName = ""
					a.filterExtension = ""
					a.filterMinimum = ""
					a.filterMaximum = ""
					a.filterAfter = ""
					a.filterBefore = ""
					a.advancedOpen = false
					a.requestRows()
				})
				ui.Button(c, "Cancel").OnClick(func() { a.advancedOpen = false })
			})
		})
	})
}
