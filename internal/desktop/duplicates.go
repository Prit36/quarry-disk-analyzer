package desktop

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// Duplicate results are a source for the ordinary Files table: selection,
// search, sorting, shell actions and export all work on the same file IDs.
type duplicateModel struct {
	active, initialized bool
	verify              bool
	minSize             string
	busy                bool
	cancel              context.CancelFunc
	gen                 uint64
	progress            *index.DuplicateProgress
	result              index.DuplicateResult
	verified            bool
	err                 string
	notice              string
	forSnap, runFor     *index.Snapshot
	scope               index.ID
	groups              map[index.ID]int // one-based group numbers
	ids                 []index.ID       // immutable default Group/Name order
	waste               uint64
	fileSort            ui.SortOrder
}

func (d *duplicateModel) reset() {
	if d.cancel != nil {
		d.cancel()
	}
	d.gen++ // also reject results already queued on the UI thread
	d.cancel, d.busy, d.progress = nil, false, nil
	d.forSnap, d.runFor, d.groups = nil, nil, nil
	d.ids = nil
	d.result, d.err, d.notice, d.waste, d.verified = index.DuplicateResult{}, "", "", 0, false
}

func (a *App) showDuplicates(active bool) {
	if a.snap == nil {
		return
	}
	a.setDuplicateMode(active)
	a.setViewMode(viewFiles)
	a.searchLeftTree = false
	a.files.invalidate()
}

func (a *App) setDuplicateMode(active bool) {
	d := &a.duplicates
	if !d.initialized {
		d.verify, d.minSize, d.initialized = false, "0", true
	}
	if d.active != active {
		if active {
			d.verify = false
			d.fileSort = a.files.sort
			a.files.sort = ui.SortOrder{Column: "Group"}
			if order := a.files.list.Columns.Order; len(order) > 0 && !slices.Contains(order, "Group") {
				a.files.list.Columns.Order = append([]string{"Group"}, order...)
			}
		} else {
			a.files.sort = d.fileSort
		}
		d.reset()
		d.active = active
		a.files.clearResults()
	}
}

// ensure follows the Files scope with the fast metadata check. Changing sort,
// search or size metric reuses results; content verification requires an action.
func (d *duplicateModel) ensure(a *App) bool {
	if d.forSnap == a.snap && d.scope == a.files.scope {
		return true
	}
	if !d.busy || d.runFor != a.snap || d.scope != a.files.scope {
		// Content verification is an explicit action for this scope. A new
		// folder or scan must never silently start another full-content read.
		d.verify = false
		a.findDuplicates()
	}
	return false
}

func (a *App) refreshDuplicates(verify bool) {
	if a.snap == nil {
		return
	}
	a.setDuplicateMode(true)
	a.duplicates.verify = verify
	a.findDuplicates()
	a.setViewMode(viewFiles)
	a.searchLeftTree = false
	a.files.invalidate()
}

func (a *App) cancelDuplicates() {
	d := &a.duplicates
	d.reset()
	d.verify = false
	// Do not restart the canceled check on the next frame.
	d.forSnap, d.scope = a.snap, a.files.scope
	d.notice = "Duplicate check canceled. Choose Refresh to try again."
	a.files.invalidate()
}

func (a *App) findDuplicates() {
	d := &a.duplicates
	if a.snap == nil {
		return
	}
	minimum, err := index.ParseSize(d.minSize)
	if err != nil {
		d.reset()
		a.files.clearResults()
		d.err = err.Error()
		// Mark this scope as handled so a render does not retry invalid input.
		d.forSnap, d.scope = a.snap, a.files.scope
		return
	}
	d.reset()
	a.files.clearResults()
	s, scope, verify, gen := a.snap, a.files.scope, d.verify, d.gen
	ctx, cancel := context.WithCancel(context.Background())
	progress := &index.DuplicateProgress{}
	d.cancel, d.busy, d.runFor, d.scope, d.progress = cancel, true, s, scope, progress
	go func() {
		defer cancel()
		var result index.DuplicateResult
		var err error
		if verify {
			result, err = s.FindDuplicatesOver(ctx, scope, minimum, progress)
		} else {
			result, err = s.DuplicateCandidatesOver(ctx, scope, minimum)
		}
		entries := 0
		for _, group := range result.Groups {
			entries += len(group.IDs)
		}
		groups := make(map[index.ID]int, entries)
		ids := make([]index.ID, 0, entries)
		var waste uint64
		for g, group := range result.Groups {
			if ctx.Err() != nil {
				return
			}
			waste += group.Size * uint64(len(group.IDs)-1)
			start := len(ids)
			ids = append(ids, group.IDs...)
			// Name/size candidates already have equal folded names and IDs
			// in order. Verified copies may have different names.
			if verify && err == nil {
				err = s.SortContext(ctx, ids[start:], index.Sort{Column: "Name"})
			}
			for i, id := range group.IDs {
				if i&4095 == 0 && ctx.Err() != nil {
					return
				}
				groups[id] = g + 1
			}
		}
		if ctx.Err() != nil {
			return
		}
		a.post(func() {
			if gen != d.gen || a.snap != s || !d.active || a.files.scope != scope {
				return
			}
			d.busy, d.cancel, d.runFor, d.forSnap, d.verified = false, nil, nil, s, verify
			if err != nil {
				d.err = err.Error()
			} else {
				d.result, d.groups, d.ids, d.waste = result, groups, ids, waste
			}
			a.files.invalidate()
		})
	}()
}

func (a *App) duplicateStatus() string {
	d := &a.duplicates
	switch {
	case d.busy && d.verify:
		return fmt.Sprintf("Checking content · %s of %s · %s files checked", a.size(d.progress.Bytes.Load()), a.size(d.progress.TotalBytes.Load()), count(d.progress.Files.Load()))
	case d.busy:
		return "Finding same-name, same-size candidates…"
	case d.err != "":
		return d.err
	case d.notice != "":
		return d.notice
	case d.forSnap == nil:
		return "Choose Refresh to check for duplicate files."
	}
	status := "Candidates only (same name and size; content not verified)"
	if d.verified {
		status = fmt.Sprintf("Verified content · %s checked · %s skipped (changed or unreadable)", count(d.result.Checked), count(d.result.Skipped))
	}
	return fmt.Sprintf("%s · up to %s in extra copies · %s", plural(uint64(len(d.result.Groups)), "group", "groups"), a.size(d.waste), status)
}

func (a *App) duplicateGroupText(id index.ID) string {
	d := &a.duplicates
	g := d.groups[id]
	if g <= 0 || g > len(d.result.Groups) {
		return ""
	}
	return fmt.Sprintf("%d · %d files", g, len(d.result.Groups[g-1].IDs))
}

// Files shares the existing view header. Less common actions stay inside its
// menu, rather than taking table space with another toolbar or helper rows.
func (a *App) fileDisplayMenu(c *ui.Context) {
	d := &a.duplicates
	t := c.Theme()
	label := "All files"
	if d.active {
		label = "Duplicates"
	}
	b := ui.ButtonBase(c.Key("file-display")).Height(22).Padding(0, 6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Gap(5).Shrink(0).Label("File display").Tooltip("Choose all files or duplicate candidates. Duplicates uses names and sizes without reading file contents.")
	b.Children(func() {
		ui.Text(c, label).SingleLine()
		ui.Icon(c, icons["chevron_down"]).FontSize(10).TextColor(t.TextMuted)
	}).Menu(func(m *ui.Menu) {
		if m.Item("All files").Checked(!d.active).Chosen() {
			a.post(func() { a.showDuplicates(false) })
		}
		if m.Item("Duplicates (name and size)").Checked(d.active && !d.verify).Chosen() {
			a.post(func() { a.refreshDuplicates(false) })
		}
		if !d.active {
			return
		}
		m.Separator()
		if d.busy {
			if m.Item("Stop checking").Chosen() {
				a.post(a.cancelDuplicates)
			}
		} else if m.Item("Refresh").Chosen() {
			a.post(func() { a.refreshDuplicates(false) })
		}
		m.Submenu("Minimum size", func(sm *ui.Menu) {
			for _, option := range []struct{ label, size string }{{"All sizes", "0"}, {"1 KB", "1KB"}, {"1 MB", "1MB"}, {"10 MB", "10MB"}, {"100 MB", "100MB"}, {"1 GB", "1GB"}} {
				if sm.Item(option.label).Checked(d.minSize == option.size).Disabled(d.busy).Chosen() {
					a.post(func() { d.minSize = option.size; a.refreshDuplicates(false) })
				}
			}
		})
		m.Separator()
		if m.Item("Verify contents (slow)").Disabled(d.busy).Chosen() {
			a.post(func() { a.refreshDuplicates(true) })
		}
	})
}

func (a *App) duplicateSummary(c *ui.Context) {
	d := &a.duplicates
	t := c.Theme()
	text, color := "", t.TextMuted
	groups := plural(uint64(len(d.result.Groups)), "group", "groups")
	switch {
	case d.busy && d.verify:
		text = "Checking contents…"
	case d.busy:
		text = "Finding duplicates…"
	case d.err != "":
		text, color = "Check failed", t.Danger
	case d.notice != "":
		text = "Check canceled"
	case a.files.busy:
		text = "Filtering…"
	case d.verified:
		text = fmt.Sprintf("%s · %s verified files", groups, count(uint64(len(a.files.ids))))
	default:
		text = groups + " · " + plural(uint64(len(a.files.ids)), "candidate", "candidates")
	}
	var detail strings.Builder
	detail.WriteString(a.duplicateStatus())
	fmt.Fprintf(&detail, "\nMinimum size: %s", d.minSize)
	if strings.TrimSpace(a.search) != "" {
		detail.WriteString("\nSearch filters these results; other files in a group may be hidden.")
	}
	for _, issue := range d.result.Issues {
		fmt.Fprintf(&detail, "\n%s: %s", issue.Path, issue.Message)
	}
	ui.Text(c, text).TextColor(color).SingleLine().MinWidth(0).Shrink(1).Tooltip(detail.String())
	if len(d.result.Issues) > 0 {
		ui.Icon(c, icons["warning"]).FontSize(12).TextColor(t.Warning).Tooltip(detail.String())
	}
	if d.busy {
		c.After(200 * time.Millisecond)
	}
}
