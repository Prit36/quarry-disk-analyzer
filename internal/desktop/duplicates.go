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
		if f := &a.files; len(f.dupLens) < len(f.ids) {
			open := f.allGroupsOpen()
			label := "Expand All"
			if open {
				label = "Collapse All"
			}
			if textButton(c, "dup-fold", label, "Show or hide the copies under each group's first file", true).Clicked() {
				a.post(func() { f.openAllGroups(!open) })
			}
		}
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

// Reviewing duplicate groups.

// groupMembers returns the files of a group, by one-based number, that
// shown holds (every file when shown is nil).
func (a *App) groupMembers(g int, shown map[index.ID]bool) []index.ID {
	d := &a.duplicates
	if g <= 0 || g > len(d.result.Groups) {
		return nil
	}
	var out []index.ID
	for _, id := range d.result.Groups[g-1].IDs {
		if shown == nil || shown[id] {
			out = append(out, id)
		}
	}
	return out
}

// shownSet is the files File View lists now, or nil for all of them.
func (f *fileModel) shownSet() map[index.ID]bool {
	if strings.TrimSpace(f.ran) == "" {
		return nil
	}
	m := make(map[index.ID]bool, len(f.ids))
	for _, id := range f.ids {
		m[id] = true
	}
	return m
}

// selectCopies selects, in every group or only in group, every file but
// the one keep chooses: the copies to remove, after review.
func (a *App) selectCopies(only int, keep func(ids []index.ID) index.ID) {
	d := &a.duplicates
	f := &a.files
	if !d.verified {
		return
	}
	f.sel = ui.Selection[index.ID]{}
	groups, files := 0, 0
	var size uint64
	shown := f.shownSet() // once: every group consults it
	for g := range d.result.Groups {
		if only > 0 && g+1 != only {
			continue
		}
		ids := a.groupMembers(g+1, shown)
		if len(ids) < 2 {
			continue
		}
		keeper := keep(ids)
		for _, id := range ids {
			if id != keeper {
				f.sel.Add(id)
				files++
				size += d.result.Groups[g].Size
			}
		}
		groups++
	}
	f.selCache.peak = max(f.selCache.peak, f.sel.Len())
	f.selCache.invalidate()
	a.invalidateSelections()
	if files > 0 {
		f.openAllGroups(true)
	}
	a.notify("Selected %s (%s) in %s, keeping one copy of each. Review, then Delete moves them to the Recycle Bin.", plural(uint64(files), "copy", "copies"), a.size(size), plural(uint64(groups), "group", "groups"))
}

// keepers choose the copy to keep in a group.
func (a *App) keepFirst(ids []index.ID) index.ID { return ids[0] }
func (a *App) keepNewest(ids []index.ID) index.ID {
	return slices.MaxFunc(ids, func(x, y index.ID) int { return cmpInt64(a.snap.Nodes[x].ModTime(), a.snap.Nodes[y].ModTime()) })
}
func (a *App) keepOldest(ids []index.ID) index.ID {
	return slices.MinFunc(ids, func(x, y index.ID) int { return cmpInt64(a.snap.Nodes[x].ModTime(), a.snap.Nodes[y].ModTime()) })
}
func (a *App) keepShortest(ids []index.ID) index.ID {
	return slices.MinFunc(ids, func(x, y index.ID) int { return len(a.pathOf(x)) - len(a.pathOf(y)) })
}

func cmpInt64(x, y int64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// duplicateRowItems adds the keeper commands to a file's menu while File
// View lists duplicates.
func (a *App) duplicateRowItems(m *ui.Menu) {
	d := &a.duplicates
	if a.prefs.View != viewFiles || !d.active || d.forSnap == nil || a.source != "" {
		return
	}
	id := a.current()
	g := d.groups[id]
	m.Separator()
	if !d.verified {
		m.Item("Keep This Copy, Select the Others").Disabled(true)
		if m.Item("Verify Contents to Choose Copies…").Chosen() {
			a.post(func() { a.refreshDuplicates(true) })
		}
		return
	}
	if g > 0 && m.Item("Keep This Copy, Select the Others").Disabled(len(a.selected()) != 1).Chosen() {
		keeper := id
		a.post(func() { a.selectCopies(g, func([]index.ID) index.ID { return keeper }) })
	}
	m.Submenu("Select Copies in Every Group", func(sm *ui.Menu) {
		for _, k := range []struct {
			label string
			keep  func([]index.ID) index.ID
		}{{"Keep the First Listed", a.keepFirst}, {"Keep the Newest", a.keepNewest}, {"Keep the Oldest", a.keepOldest}, {"Keep the Shortest Path", a.keepShortest}} {
			if sm.Item(k.label).Chosen() {
				keep := k.keep
				a.post(func() { a.selectCopies(0, keep) })
			}
		}
	})
}

// recheckDuplicates confirms, before verified copies are deleted, that in
// each group at least one file stays and that the files still have the
// keeper's exact contents. ok runs on the UI thread when every group passed.
func (a *App) recheckDuplicates(ids []index.ID, ok func()) {
	d := &a.duplicates
	byGroup := map[int][]index.ID{}
	for _, id := range ids {
		if g := d.groups[id]; g > 0 {
			byGroup[g] = append(byGroup[g], id)
		}
	}
	type check struct {
		keeper index.ID
		others []index.ID
	}
	var checks []check
	for g, sel := range byGroup {
		keeper := index.None
		for _, id := range d.result.Groups[g-1].IDs {
			if !slices.Contains(sel, id) {
				keeper = id
				break
			}
		}
		if keeper == index.None {
			a.fail("Every copy in duplicate group %d is selected. Keep at least one (right-click → Keep This Copy, Select the Others).", g)
			return
		}
		checks = append(checks, check{keeper, sel})
	}
	if len(checks) == 0 {
		ok()
		return
	}
	s := a.snap
	a.notify("Checking that the copies are still identical…")
	go func() {
		var err error
		for _, ch := range checks {
			if err = s.RecheckDuplicates(context.Background(), ch.keeper, ch.others, nil); err != nil {
				break
			}
		}
		a.post(func() {
			if err != nil {
				a.stale = "Some duplicates changed since they were verified."
				a.fail("Not deleted: %v. Verify contents again before removing copies.", err)
				return
			}
			a.message = ""
			ok()
		})
	}()
}
