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

// duplicatesToggle sits beside the view tabs: Files lists every file, or
// with it checked only those that appear to be copies of each other.
func (a *App) duplicatesToggle(c *ui.Context) {
	on := a.duplicates.active
	if ui.Checkbox(c.Key("dups"), &on, "Duplicates only").Label("Duplicates").Tooltip("List only files with the same name and size, in groups").Shrink(0).Changed() {
		if on {
			a.refreshDuplicates(false)
		} else {
			a.showDuplicates(false)
		}
	}
}

var minSizes = []struct{ label, size string }{{"Any size", "0"}, {"1 KB", "1KB"}, {"1 MB", "1MB"}, {"10 MB", "10MB"}, {"100 MB", "100MB"}, {"1 GB", "1GB"}}

// duplicateBar is the duplicate check's line under the tabs: what it found,
// how it matched, and its options.
func (a *App) duplicateBar(c *ui.Context, pal palette) {
	d := &a.duplicates
	t := c.Theme()
	ui.Row(c).Height(30).Shrink(0).Padding(0, 10).Gap(10).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(0, 0, 1, 0).BorderColor(pal.separator).Clip().Children(func() {
		switch {
		case d.busy:
			value, label := -1.0, "Finding files with the same name and size…"
			if d.verify {
				label = "Comparing contents…"
				if total := d.progress.TotalBytes.Load(); total > 0 {
					value = float64(d.progress.Bytes.Load()) / float64(total)
					label = fmt.Sprintf("Comparing contents: %s of %s, %s files", a.size(d.progress.Bytes.Load()), a.size(total), count(d.progress.Files.Load()))
				}
			}
			ui.Progress(c, value).Size(140, 10).Shrink(0)
			ui.Text(c, label).SingleLine().MinWidth(0).Shrink(1)
			ui.Spacer(c)
			if textButton(c, "dup-stop", "Stop checking", "", true).Clicked() {
				a.post(a.cancelDuplicates)
			}
			c.After(200 * time.Millisecond)
			return
		case d.err != "":
			ui.Text(c, d.err).TextColor(t.Danger).SingleLine().MinWidth(0).Shrink(1).Tooltip(d.err)
		case d.notice != "":
			ui.Text(c, "Check canceled.").TextColor(t.TextMuted).SingleLine()
			if linkButton(c, "dup-retry", "Check again", "") {
				a.post(func() { a.refreshDuplicates(false) })
			}
		case d.forSnap != nil:
			infoField(c, "Groups", count(uint64(len(d.result.Groups))))
			infoField(c, "Files", count(uint64(len(a.files.ids))))
			infoField(c, "In extra copies", a.size(d.waste)).Tooltip("Space taken by all but one file of each group")
			infoSep(c, pal)
			if d.verified {
				ui.Text(c, fmt.Sprintf("Contents verified: %s compared, %s skipped", count(d.result.Checked), count(d.result.Skipped))).TextColor(t.Success).SingleLine().MinWidth(0).Shrink(1)
			} else {
				ui.Text(c, "Matched by name and size; contents not compared").TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
			}
			if len(d.result.Issues) > 0 {
				var detail strings.Builder
				for i, issue := range d.result.Issues {
					if i > 0 {
						detail.WriteString("\n")
					}
					fmt.Fprintf(&detail, "%s: %s", issue.Path, issue.Message)
				}
				warnText(c, plural(uint64(len(d.result.Issues)), "file skipped", "files skipped"), detail.String())
			}
			if strings.TrimSpace(a.search) != "" {
				ui.Text(c, "· filtered by the search").TextColor(t.Focus).SingleLine().Shrink(0)
			}
		}
		ui.Spacer(c)
		ui.Text(c, "Minimum size:").TextColor(t.TextMuted).SingleLine().Shrink(0)
		choice := minSizes[0].label
		labels := make([]string, len(minSizes))
		for i, o := range minSizes {
			labels[i] = o.label
			if o.size == d.minSize {
				choice = o.label
			}
		}
		if ui.Select(c.Key("dup-min"), &choice, labels).Label("Minimum size").Width(96).Height(24).Shrink(0).Changed() {
			for _, o := range minSizes {
				if o.label == choice {
					size := o.size
					a.post(func() { d.minSize = size; a.refreshDuplicates(false) })
				}
			}
		}
		if !d.verified && textButton(c, "dup-verify", "Verify Contents", "Read the files and keep only true copies. This can take a while.", true).Clicked() {
			a.post(func() { a.refreshDuplicates(true) })
		}
	})
}
