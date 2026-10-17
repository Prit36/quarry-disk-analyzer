package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"
	"stratum/internal/index"
)

func (a *App) startDuplicates() {
	if a.snapshot == nil || a.busy || a.duplicateBusy {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.duplicateCancel = cancel
	a.duplicateGeneration++
	generation := a.duplicateGeneration
	a.duplicateSource, a.duplicateRoot = a.snapshot, a.analysis.Root
	a.duplicateResult = index.DuplicateResult{}
	a.duplicateBusy = true
	a.duplicateProgress = &index.DuplicateProgress{}
	snapshot, root, progress := a.snapshot, a.duplicateRoot, a.duplicateProgress
	go func() {
		result, err := snapshot.FindDuplicates(ctx, root, progress)
		a.post(event{kind: "duplicates", generation: generation, snapshot: snapshot, duplicates: result, err: err})
	}()
}

func (a *App) duplicatesView(c *ui.Context) {
	ui.Row(c).Padding(8).Gap(8).Children(func() {
		ui.Button(c, "Check content").Disabled(a.busy || a.duplicateBusy).OnClick(a.startDuplicates)
		ui.Button(c, "Cancel").Disabled(!a.duplicateBusy).OnClick(func() {
			if a.duplicateCancel != nil {
				a.duplicateCancel()
			}
		})
	})
	ui.Text(c, "SHA-256 verified content. Hard-link aliases are excluded; physical reclaimability is not estimated.").FontSize(11).Padding(0, 8).TextColor(c.Theme().TextMuted)
	if a.duplicateBusy {
		c.After(100 * time.Millisecond)
		ui.Text(c, fmt.Sprintf("Checking… %d files · %s read", a.duplicateProgress.Files.Load(), a.formatBytes(a.duplicateProgress.Bytes.Load()))).Padding(8).FontSize(12)
	} else if a.duplicateSource == a.snapshot {
		ui.Text(c, fmt.Sprintf("%d groups · %d checked · %d skipped", len(a.duplicateResult.Groups), a.duplicateResult.Checked, a.duplicateResult.Skipped)).Padding(8).FontSize(12)
		ui.Text(c, "Checked scope: "+a.snapshot.Path(a.duplicateRoot)).SingleLine().Padding(0, 8).FontSize(11)
	} else {
		ui.Text(c, "Start a content check for this analytics scope.").Padding(8).FontSize(12)
	}
	columns := []ui.TableColumn{{Title: "Representative file", MinWidth: 160}, {Title: "Each size", Width: 94}, {Title: "Copies", Width: 60}}
	a.duplicateList.Selected = &a.duplicateRow
	dataTable(c.Key("duplicates"), &a.duplicateList, columns, len(a.duplicateResult.Groups), func(row, column int) {
		group := a.duplicateResult.Groups[row]
		switch column {
		case 0:
			ui.Text(c, a.snapshot.NameView(group.IDs[0])).SingleLine().FontSize(12).Tooltip(a.snapshot.Path(group.IDs[0]))
		case 1:
			ui.Text(c, a.formatBytes(group.Size)).FontSize(12)
		case 2:
			ui.Text(c, fmt.Sprint(len(group.IDs))).FontSize(12)
		}
	}).Grow(1).MinHeight(0)
	if a.duplicateRow >= 0 && a.duplicateRow < len(a.duplicateResult.Groups) {
		ui.Scroll(c).Height(100).Shrink(0).Padding(8).Children(func() {
			for _, id := range a.duplicateResult.Groups[a.duplicateRow].IDs {
				ui.Button(c.Key(id), a.snapshot.Path(id)).OnClick(func() { a.selectMap(id) }).FontSize(11)
			}
		})
	}
}

func historyDirectory() string { return filepath.Join(configDirectory(), "history") }

func (a *App) refreshHistory() {
	if a.historyBusy {
		return
	}
	a.historyBusy = true
	go func() {
		entries, err := index.ListSnapshots(historyDirectory())
		a.post(event{kind: "history", savedScans: entries, err: err})
	}()
}

func (a *App) saveSnapshot() {
	if a.snapshot == nil || a.historyBusy {
		return
	}
	a.historyBusy = true
	snapshot := a.snapshot
	go func() {
		directory := historyDirectory()
		err := os.MkdirAll(directory, 0755)
		if err == nil {
			path := filepath.Join(directory, "scan-"+time.Now().Format("20060102-150405.000000000")+".stratum")
			err = snapshot.Save(context.Background(), path)
			if err == nil {
				err = index.PruneSnapshots(directory)
			}
		}
		a.post(event{kind: "saved", err: err})
	}()
}

func (a *App) restoreSnapshot(path string) {
	if a.historyBusy || a.busy {
		return
	}
	a.historyBusy = true
	go func() {
		snapshot, err := index.LoadSnapshot(context.Background(), path)
		if err != nil {
			a.post(event{kind: "restore-error", err: err})
			return
		}
		summary, err := snapshot.Summarize(context.Background())
		if err != nil {
			a.post(event{kind: "restore-error", err: err})
			return
		}
		a.post(event{kind: "restore", snapshot: snapshot, summary: summary})
	}()
}

func (a *App) compareSnapshot(path string) {
	if a.historyBusy || a.snapshot == nil {
		return
	}
	a.historyBusy = true
	snapshot, allocated := a.snapshot, a.metric == 1
	go func() {
		previous, err := index.LoadSnapshot(context.Background(), path)
		var changes []index.Change
		if err == nil {
			changes, err = index.CompareSnapshots(previous, snapshot, allocated)
		}
		label := ""
		if previous != nil {
			label = "Previous: " + previous.Started.Format("02 Jan 2006 15:04") + " · current: " + snapshot.Started.Format("02 Jan 2006 15:04")
			if allocated {
				label += " · allocated"
			} else {
				label += " · logical"
			}
		}
		a.post(event{kind: "compare", snapshot: snapshot, changes: changes, path: label, err: err})
	}()
}

func (a *App) historyView(c *ui.Context) {
	if !a.historyLoaded && !a.historyBusy {
		a.refreshHistory()
	}
	ui.Row(c).Height(36).Shrink(0).Padding(0, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Button(c, "Save current scan").Height(28).Disabled(a.snapshot == nil || a.historyBusy || a.busy).OnClick(a.saveSnapshot)
		ui.Button(c, "Refresh").Height(28).Disabled(a.historyBusy).OnClick(a.refreshHistory)
		selected := a.savedRow >= 0 && a.savedRow < len(a.savedScans)
		ui.Button(c, "Restore").Height(28).Disabled(!selected || a.historyBusy || a.busy).OnClick(func() { a.restoreSnapshot(a.savedScans[a.savedRow].Path) })
		ui.Button(c, "Compare with current").Height(28).Disabled(!selected || a.historyBusy || a.snapshot == nil).OnClick(func() { a.compareSnapshot(a.savedScans[a.savedRow].Path) })
		ui.Text(c, "Retains 5 saves / 256 MiB; 128 MiB per uncompressed index.").SingleLine().Grow(1).MinWidth(0).FontSize(11).TextColor(c.Theme().TextMuted)
	})
	if a.historyBusy {
		ui.Text(c, "Preparing saved scans…").Padding(8).FontSize(12)
	}
	if a.comparisonLabel != "" {
		ui.Row(c).Height(28).Shrink(0).Padding(0, 8).Children(func() {
			ui.Text(c, a.comparisonLabel).SingleLine().Grow(1).MinWidth(0).FontSize(11)
			ui.Button(c, "Back to saved scans").Height(24).OnClick(func() { a.comparisonLabel = ""; a.changes = nil })
		})
		columns := []ui.TableColumn{{Title: "Root child", MinWidth: 180}, {Title: "Previous", Width: 110, Align: ui.End}, {Title: "Current", Width: 110, Align: ui.End}, {Title: "Change", Width: 110, Align: ui.End}}
		dataTable(c.Key("changes"), &a.changesList, columns, len(a.changes), func(row, column int) {
			change := a.changes[row]
			switch column {
			case 0:
				ui.Text(c, change.Name).SingleLine()
			case 1:
				ui.Text(c, a.formatBytes(change.Previous)).FontSize(12)
			case 2:
				ui.Text(c, a.formatBytes(change.Current)).FontSize(12)
			case 3:
				sign := "−"
				if change.Increase {
					sign = "+"
				}
				ui.Text(c, sign+a.formatBytes(change.Difference)).FontSize(12)
			}
		}).Grow(1).MinHeight(0)
		return
	}
	if len(a.savedScans) == 0 && !a.historyBusy {
		ui.Text(c, "No saved scans. Save the current index to restore or compare it later.").Padding(8).TextColor(c.Theme().TextMuted)
	}
	columns := []ui.TableColumn{{Title: "Saved scan", Width: 180}, {Title: "Root", MinWidth: 180}, {Title: "Logical size", Width: 110}, {Title: "Files", Width: 80}, {Title: "Status", Width: 100}}
	a.savedList.Selected = &a.savedRow
	dataTable(c.Key("saved-scans"), &a.savedList, columns, len(a.savedScans), func(row, column int) {
		entry := a.savedScans[row]
		switch column {
		case 0:
			ui.Text(c, entry.Started.Format("02 Jan 2006 15:04")).FontSize(12)
		case 1:
			ui.Text(c, entry.Root).SingleLine().FontSize(12).Tooltip(entry.Root)
		case 2:
			ui.Text(c, a.formatBytes(entry.Total.Size)).FontSize(12)
		case 3:
			ui.Text(c, number(uint64(entry.Total.Files))).FontSize(12)
		case 4:
			label := "Complete"
			if entry.Cancelled {
				label = "Partial"
			} else if entry.Errors > 0 {
				label = "With issues"
			}
			ui.Text(c, label).FontSize(12)
		}
	}).Grow(1).MinHeight(0)
}
