package desktop

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
)

func (a *App) analyticsScope() index.ID {
	root := index.ID(0)
	if a.followSelection && a.selected != index.None {
		root = a.selected
		if !a.snapshot.Nodes[root].IsDir() {
			root = a.snapshot.Nodes[root].Parent
		}
	}
	return root
}

func (a *App) prepareAnalysis() {
	root := a.analyticsScope()
	if a.analysisSource == a.snapshot && a.analysisRoot == root && a.analysisMetric == a.metric {
		return
	}
	if a.analysisCancel != nil {
		a.analysisCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.analysisCancel = cancel
	a.analysisSource, a.analysisRoot, a.analysisMetric = a.snapshot, root, a.metric
	a.analysisGeneration++
	generation := a.analysisGeneration
	a.analysisBusy = true
	a.analysis = index.Analysis{Root: root}
	a.visibleTypes = nil
	a.topRow = -1
	snapshot, allocated := a.snapshot, a.metric == 1
	go func() {
		analysis, err := snapshot.Analyze(ctx, root, allocated, 100)
		if ctx.Err() == nil {
			a.post(event{kind: "analysis", generation: generation, snapshot: snapshot, analysis: analysis, err: err})
		}
	}()
}

func (a *App) filterTypes() {
	a.visibleTypes = a.visibleTypes[:0]
	for _, group := range a.analysis.Types {
		if strings.Contains(strings.ToLower(group.Extension), strings.ToLower(a.typeQuery)) {
			a.visibleTypes = append(a.visibleTypes, group)
		}
	}
	slices.SortFunc(a.visibleTypes, func(first, second index.FileType) int {
		comparison := 0
		switch a.typeSort.Column {
		case "Extension":
			comparison = strings.Compare(first.Extension, second.Extension)
		case "Files":
			comparison = cmp.Compare(first.Count, second.Count)
		default:
			if a.metric == 1 {
				comparison = cmp.Compare(first.Allocated, second.Allocated)
			} else {
				comparison = cmp.Compare(first.Bytes, second.Bytes)
			}
		}
		if a.typeSort.Descending {
			comparison = -comparison
		}
		return comparison
	})
}

func (a *App) metricBytes(id index.ID) uint64 {
	if a.metric == 1 {
		return a.snapshot.Nodes[id].Allocated
	}
	return a.snapshot.Nodes[id].Size
}
func (a *App) metricName() string {
	if a.metric == 1 {
		return "Allocated"
	}
	return "Size"
}

func fraction(value, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return min(1, float64(value)/float64(total))
}

func (a *App) inspectType(extension string) {
	if extension == "(no extension)" {
		extension = "none"
	}
	a.focus = a.analysis.Root
	a.searchScope = "Subtree"
	a.query = "ext:" + extension
	a.tab = 1
	a.requestRows()
}

func (a *App) topFilesTable(c *ui.Context) {
	ui.Row(c).Height(34).Shrink(0).Padding(0, 8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Top 100 files · "+strings.ToLower(a.metricName())).Bold().FontSize(12).Grow(1).MinWidth(0).SingleLine()
		ui.Button(c, "Export").Height(26).Label("Export top files").Disabled(a.exporting).OnClick(func() {
			rows := make([]index.Row, len(a.analysis.TopFiles))
			for position, id := range a.analysis.TopFiles {
				rows[position] = index.Row{ID: id}
			}
			a.exportRows(rows, "Export top 100 files")
		})
	})
	columns := []ui.TableColumn{{Title: "File", MinWidth: 130}, {Title: a.metricName(), Width: 90, MinWidth: 72, Align: ui.End}, {Title: "Parent folder", Width: 160, MinWidth: 100}, {Title: "Modified", Width: 116}, {Title: "Extension", Width: 78}}
	a.topList.Selected = &a.topRow
	a.topList.Key = func(row int) any { return a.analysis.TopFiles[row] }
	table := dataTable(c.Key("topfiles"), &a.topList, columns, len(a.analysis.TopFiles), func(row, column int) {
		id := a.analysis.TopFiles[row]
		path := a.snapshot.Path(id)
		switch column {
		case 0:
			ui.Text(c, a.snapshot.NameView(id)).SingleLine().FontSize(12).Tooltip(path).ContextMenu(func(menu *ui.Menu) {
				if menu.Item("Show in Explorer").Chosen() {
					a.selected = id
					a.reveal()
				}
				if menu.Item("Copy path").Chosen() {
					mygo.Clipboard.WriteText(path)
				}
				if menu.Item("Inspect file").Chosen() {
					a.selectMap(id)
				}
				if menu.Item("Filter this extension").Chosen() {
					a.inspectType(filepath.Ext(a.snapshot.NameView(id)))
				}
			})
		case 1:
			ui.Text(c, a.formatBytes(a.metricBytes(id))).FontSize(12)
		case 2:
			ui.Text(c, filepath.Dir(path)).SingleLine().FontSize(11).Tooltip(filepath.Dir(path))
		case 3:
			text := "—"
			if modified := a.snapshot.Nodes[id].Modified; modified != 0 {
				text = time.Unix(modified, 0).Format("02 Jan 2006 15:04")
			}
			ui.Text(c, text).FontSize(11)
		case 4:
			ui.Text(c, filepath.Ext(a.snapshot.NameView(id))).SingleLine().FontSize(11)
		}
	}).Grow(1).MinHeight(0)
	if table.Submitted() && a.topRow >= 0 && a.topRow < len(a.analysis.TopFiles) {
		a.selectMap(a.analysis.TopFiles[a.topRow])
	}
}
