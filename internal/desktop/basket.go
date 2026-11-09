package desktop

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// The cleanup basket collects files and folders from anywhere in the scan
// for one reviewed cleanup. It lives only as long as the window: nothing is
// remembered, and nothing is removed until the user confirms in its review.

// basketItem is what was added, as the scan saw it.
type basketItem struct {
	path            string
	dir             bool
	size, allocated uint64
	files           uint32
	id              index.ID // in the scan it was added from; checked by path before use
}

// basketKey is the case-folded path the basket compares items by.
func basketKey(path string) string {
	return strings.ToLower(strings.TrimSuffix(path, `\`))
}

type basketStatus uint8

const (
	basketReady    basketStatus = iota
	basketCovered               // inside another item of the basket
	basketBlocked               // never deleted from Quarry
	basketMissing               // gone from disk
	basketChanged               // a different kind of entry, or a file of another size
	basketNotFound              // not part of the current scan
)

// basketRow is an item and what the review found about it.
type basketRow struct {
	item   basketItem
	id     index.ID
	status basketStatus
	note   string // the reason, or the folder that covers it
	risky  string // a caution about its location
}

func (r basketRow) statusText() string {
	switch r.status {
	case basketReady:
		if r.risky != "" {
			return "Ready · caution"
		}
		return "Ready"
	case basketCovered:
		return "Included in " + filepath.Base(r.note)
	case basketBlocked:
		return "Protected"
	case basketMissing:
		return "No longer on disk"
	case basketChanged:
		return "Changed since the scan"
	}
	return "Not in the current scan"
}

type basketModel struct {
	items  []basketItem
	keys   map[string]struct{} // basketKey of every item
	open   bool
	rows   []basketRow
	busy   bool
	gen    uint64
	list   ui.ListState
	cursor int
	result string // what the last cleanup did, shown in the review
}

// has, add and remove keep the items free of duplicates by path; each is
// a set lookup, so adding thousands of items stays linear.
func (b *basketModel) has(path string) bool {
	_, ok := b.keys[basketKey(path)]
	return ok
}

func (b *basketModel) add(it basketItem) bool {
	key := basketKey(it.path)
	if _, ok := b.keys[key]; ok {
		return false
	}
	if b.keys == nil {
		b.keys = map[string]struct{}{}
	}
	b.keys[key] = struct{}{}
	b.items = append(b.items, it)
	return true
}

func (b *basketModel) remove(paths ...string) {
	gone := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		gone[basketKey(p)] = struct{}{}
	}
	b.items = slices.DeleteFunc(b.items, func(it basketItem) bool {
		key := basketKey(it.path)
		if _, ok := gone[key]; ok {
			delete(b.keys, key)
			return true
		}
		return false
	})
}

// basketTotals sums what a cleanup would remove: ready items only, each
// counted once even when a folder and something inside it were both added.
type basketTotals struct {
	items, files    uint64
	size, allocated uint64
	excluded        int
	risky           bool
}

func sumBasket(rows []basketRow) basketTotals {
	var t basketTotals
	for _, r := range rows {
		switch r.status {
		case basketReady:
			t.items++
			t.size += r.item.size
			t.allocated += r.item.allocated
			if r.item.dir {
				t.files += uint64(r.item.files)
			} else {
				t.files++
			}
			if r.risky != "" {
				t.risky = true
			}
		case basketCovered:
		default:
			t.excluded++
		}
	}
	return t
}

// basketFound is a basket path resolved in the scan: its entry, why it
// must not be deleted and a caution about its location.
type basketFound struct {
	id            index.ID
	ok            bool
	reason, risky string
}

// evaluateBasket checks every item against the scan and the disk. find
// resolves an item in the scan and stat reads the disk. A folder covers
// what was added from inside it, which is then counted once. Each item
// looks up its own ancestors among the folders, so the check is linear in
// the number of items rather than quadratic.
func evaluateBasket(items []basketItem, find func(int) basketFound, stat func(string) (fs.FileInfo, error)) []basketRow {
	rows := make([]basketRow, len(items))
	// Shorter paths first, so a folder is seen before what it contains.
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int { return len(items[x].path) - len(items[y].path) })
	folders := map[string]string{} // basketKey → the folder's path as added
	for _, i := range order {
		it := items[i]
		r := basketRow{item: it, id: index.None}
		key := basketKey(it.path)
		for j := strings.LastIndexByte(key, '\\'); j > 0; j = strings.LastIndexByte(key[:j], '\\') {
			if f, ok := folders[key[:j]]; ok {
				r.status, r.note = basketCovered, f
				break
			}
		}
		if r.status != basketCovered {
			f := find(i)
			switch {
			case !f.ok:
				r.status, r.note = basketNotFound, "The current scan does not contain it."
			default:
				r.id = f.id
				if f.reason != "" {
					r.status, r.note = basketBlocked, "Quarry does not delete "+f.reason+"."
				} else {
					r.risky = f.risky
					info, err := stat(it.path)
					switch {
					case err != nil:
						r.status, r.note = basketMissing, "It was moved or deleted after the scan."
					case info.IsDir() != it.dir:
						r.status, r.note = basketChanged, "Something else now has this path. Rescan to include it."
					case !it.dir && uint64(info.Size()) != it.size:
						r.status, r.note = basketChanged, "Its size changed after the scan. Rescan to include it."
					}
				}
			}
			if it.dir && r.status == basketReady {
				folders[key] = it.path
			}
		}
		rows[i] = r // in the order the items were added
	}
	return rows
}

// findBase finds a path in the full scan, NTFS metadata aside.
func (a *App) findBase(path string) (index.ID, bool) {
	if a.base == nil {
		return index.None, false
	}
	snap := a.snap
	a.snap = a.base
	id, ok := a.findPath(path)
	a.snap = snap
	if ok && !a.base.Reachable(id) {
		return index.None, false
	}
	return id, ok
}

// addToBasket adds the selection.
func (a *App) addToBasket() {
	ids := a.selected()
	added, skipped := 0, ""
	for _, id := range ids {
		if reason := a.blocked(id); reason != "" {
			skipped = a.name(id) + " (" + reason + ")"
			continue
		}
		path := a.pathOf(id)
		if path == "" {
			continue
		}
		n := a.base.Nodes[id]
		if a.basket.add(basketItem{path: path, dir: n.IsDir(), size: n.Size, allocated: n.Allocated, files: n.Files, id: id}) {
			added++
		}
	}
	switch {
	case added == 0 && skipped != "":
		a.fail("Quarry does not delete %s.", skipped)
	case added == 0:
		a.notify("Already in the cleanup basket.")
	default:
		msg := fmt.Sprintf("Added %s to the cleanup basket (%s in all). Review it with Tools → Cleanup Basket (Ctrl+Shift+B).", plural(uint64(added), "item", "items"), plural(uint64(len(a.basket.items)), "item", "items"))
		if skipped != "" {
			msg += " Skipped " + skipped + "."
		}
		a.notify("%s", msg)
	}
	a.basket.rows = nil
}

func (a *App) removeFromBasket() {
	var paths []string
	for _, id := range a.selected() {
		paths = append(paths, a.pathOf(id))
	}
	before := len(a.basket.items)
	a.basket.remove(paths...)
	a.basket.rows = nil
	a.notify("Removed %s from the cleanup basket.", plural(uint64(before-len(a.basket.items)), "item", "items"))
}

// selectionInBasket reports whether anything selected is in the basket.
func (a *App) selectionInBasket() bool {
	if len(a.basket.items) == 0 || !hasItems(a) {
		return false
	}
	ids := a.selected()
	if len(ids) > 2000 {
		return true
	}
	for _, id := range ids {
		if a.basket.has(a.pathOf(id)) {
			return true
		}
	}
	return false
}

// reviewBasket opens the review and checks every item on disk.
func (a *App) reviewBasket() {
	a.basket.open = true
	a.basket.result = ""
	a.checkBasket()
}

func (a *App) checkBasket() {
	b := &a.basket
	b.gen++
	gen, items := b.gen, slices.Clone(b.items)
	// Resolving paths reads the index on this thread; the disk is read
	// by a worker, as a network drive can be slow.
	// An item's entry from when it was added is still right while the
	// scan only had things removed or folders refreshed, which keep IDs;
	// confirming it by path is far cheaper than finding the path again.
	resolved := make([]basketFound, len(items))
	if a.base != nil && a.source == "" {
		for i, it := range items {
			id, ok := it.id, false
			if int(id) < len(a.base.Nodes) && a.base.Reachable(id) && strings.EqualFold(a.base.Path(id), it.path) {
				ok = true
			} else {
				id, ok = a.findBase(it.path)
			}
			f := basketFound{id: id, ok: ok}
			if ok {
				f.reason, f.risky = a.blocked(id), a.caution(id)
			}
			resolved[i] = f
		}
	}
	b.busy = true
	go func() {
		rows := evaluateBasket(items, func(i int) basketFound { return resolved[i] }, func(p string) (fs.FileInfo, error) { return os.Lstat(p) })
		a.post(func() {
			if b.gen != gen {
				return
			}
			b.rows, b.busy = rows, false
		})
	}()
}

// cleanBasket removes the ready items, after confirmation.
func (a *App) cleanBasket(permanent bool) {
	b := &a.basket
	tot := sumBasket(b.rows)
	if tot.items == 0 || a.base == nil || a.source != "" {
		return
	}
	var ids []index.ID
	var paths []string
	warning := ""
	for _, r := range b.rows {
		if r.status == basketReady {
			ids = append(ids, r.id)
			paths = append(paths, r.item.path)
			if r.risky != "" {
				warning = r.risky
			}
		}
	}
	what := plural(tot.items, "item", "items")
	detail := fmt.Sprintf("%s, %s (about %s of disk space).", plural(tot.files, "file", "files"), a.size(tot.size), a.size(tot.allocated))
	if tot.excluded > 0 {
		detail += fmt.Sprintf("\n%s in the basket will be left alone; the review says why.", plural(uint64(tot.excluded), "item", "items"))
	}
	if warning != "" {
		detail += "\n\n" + warning
	}
	message, button, kind := "Move "+what+" to the Recycle Bin?", "Move to Recycle Bin", mygo.MessageQuestion
	if permanent {
		message, button, kind = "Permanently delete "+what+"?", "Delete Permanently", mygo.MessageWarning
		detail += "\n\nThis bypasses the Recycle Bin and cannot be undone."
	} else {
		detail += "\n\nThe Recycle Bin keeps them, and their disk space, until it is emptied."
	}
	run := func() {
		owner := a.win.NativeHandle()
		go func() {
			aborted, err := platform.DeleteItems(owner, paths, !permanent)
			a.post(func() { a.afterBasketClean(ids, paths, permanent, aborted, err) })
		}()
	}
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.win, Type: kind, Title: "Cleanup Basket", Message: message, Detail: detail, Buttons: []string{button, "Cancel"}, DefaultButton: 1, CancelButton: 1})
		if err == nil && res.Button == 0 {
			a.post(run)
		}
	}()
}

func (a *App) afterBasketClean(ids []index.ID, paths []string, permanent, aborted bool, err error) {
	var gone []index.ID
	var gonePaths []string
	var freed uint64
	for i, p := range paths {
		if _, statErr := os.Lstat(p); os.IsNotExist(statErr) {
			gone = append(gone, ids[i])
			gonePaths = append(gonePaths, p)
			if a.base != nil && int(ids[i]) < len(a.base.Nodes) {
				freed += a.base.Nodes[ids[i]].Size
			}
		}
	}
	if len(gone) > 0 && a.base != nil {
		next := a.base.Clone()
		next.Remove(gone...)
		a.replaceBase(next)
		go func() {
			drives := platform.Drives()
			a.post(func() { a.drives = drives })
		}()
	}
	a.basket.remove(gonePaths...)
	a.basket.result = cleanupFeedback(len(gone), len(paths), a.size(freed), permanent, aborted)
	if err != nil {
		a.basket.result += " " + err.Error()
		a.fail("%v", err)
	} else {
		a.notify("%s", a.basket.result)
	}
	a.checkBasket()
}

// cleanupFeedback says what a deletion did and where the space went.
func cleanupFeedback(done, asked int, size string, permanent, aborted bool) string {
	var s string
	switch {
	case done == 0 && aborted:
		return "Cleanup canceled; nothing was removed."
	case done == 0:
		return "Nothing was removed."
	case permanent:
		s = fmt.Sprintf("Deleted %s (%s) permanently. The space is free now.", plural(uint64(done), "item", "items"), size)
	default:
		s = fmt.Sprintf("Moved %s (%s) to the Recycle Bin. They still take disk space until the Recycle Bin is emptied.", plural(uint64(done), "item", "items"), size)
	}
	if done < asked {
		s += fmt.Sprintf(" %d could not be removed.", asked-done)
	}
	return s
}

// basketDialog is the review: every item with what will happen to it.
func (a *App) basketDialog(c *ui.Context) {
	b := &a.basket
	t := c.Theme()
	a.modal(c.Key("basket"), &b.open, func() {
		ui.Column(c).Width(820).MaxWidthPercent(100).Height(500).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Cleanup Basket")
			tot := sumBasket(b.rows)
			switch {
			case len(b.items) == 0:
				ui.Text(c, "The basket is empty. Right-click files or folders anywhere in the scan and choose Add to Cleanup Basket (Ctrl+B) to collect them here for one reviewed cleanup.").TextColor(t.TextMuted).MaxWidth(780)
			case b.busy && b.rows == nil:
				ui.Text(c, "Checking the items on disk…").TextColor(t.TextMuted)
			default:
				ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
					infoField(c, "Will remove", plural(tot.items, "item", "items")+" · "+plural(tot.files, "file", "files"))
					infoField(c, "Size", a.size(tot.size))
					infoField(c, "Disk space (estimated)", a.size(tot.allocated)).Tooltip("Allocated size from the scan: what the drive gains once the files are gone for good.")
					if tot.excluded > 0 {
						warnText(c, plural(uint64(tot.excluded), "item left alone", "items left alone"), "Protected, missing or changed items are not removed. Select one to see why.")
					}
				})
				if tot.risky {
					warnText(c, "Some items are in system or program folders", "Removing them can break Windows or installed programs.")
				}
			}
			b.list.Selected = &b.cursor
			rows := b.rows
			if rows == nil {
				for _, it := range b.items {
					rows = append(rows, basketRow{item: it, id: index.None, status: basketNotFound})
				}
			}
			table := ui.Table(c.Key("basket-table"), &b.list, []ui.TableColumn{{Title: "Path", MinWidth: 260}, {Title: "Size", Width: 90, Align: ui.End}, {Title: "Allocated", Width: 90, Align: ui.End}, {Title: "Status", Width: 210}}, len(rows), func(row, col int) {
				r := rows[row]
				switch col {
				case 0:
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						icon := "document"
						if r.item.dir {
							icon = "folder"
						}
						ui.Icon(c, icons[icon]).FontSize(13).TextColor(t.TextMuted).Shrink(0)
						ui.Text(c, r.item.path).SingleLine().MinWidth(0).Shrink(1).Tooltip(r.item.path)
					})
				case 1:
					txt := ui.Text(c, a.size(r.item.size)).SingleLine()
					if r.status != basketReady {
						txt.TextColor(t.TextMuted)
					}
				case 2:
					ui.Text(c, a.size(r.item.allocated)).TextColor(t.TextMuted).SingleLine()
				case 3:
					txt := ui.Text(c, r.statusText()).SingleLine()
					switch {
					case b.rows == nil:
						txt.TextColor(t.TextMuted)
					case r.status == basketReady && r.risky != "":
						txt.TextColor(t.Warning).Tooltip(r.risky)
					case r.status == basketReady:
						txt.TextColor(t.Success)
					case r.status == basketCovered:
						txt.TextColor(t.TextMuted).Tooltip("Counted once, as part of " + r.note)
					default:
						txt.TextColor(t.Warning).Tooltip(r.note)
					}
				}
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			sel := index.None
			selPath := ""
			if b.cursor >= 0 && b.cursor < len(rows) {
				sel, selPath = rows[b.cursor].id, rows[b.cursor].item.path
				if note := rows[b.cursor].note; note != "" && rows[b.cursor].status != basketCovered {
					ui.Text(c, note).TextColor(t.TextMuted).SingleLine()
				}
			}
			if table.Submitted() && sel != index.None {
				b.open = false
				a.reveal(sel)
			}
			if b.result != "" {
				ui.Text(c, b.result).TextColor(t.Success).MaxWidth(780)
			}
			ui.Text(c, "Nothing is removed until you choose below. Moving to the Recycle Bin can be undone there; the space returns when it is emptied.").FontSize(11).TextColor(t.TextMuted).MaxWidth(780)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(8, 0, 0, 0).Children(func() {
				if ui.Button(c, "Remove from Basket").Disabled(selPath == "").Clicked() {
					b.remove(selPath)
					a.checkBasket()
				}
				if ui.Button(c, "Clear").Disabled(len(b.items) == 0).Clicked() {
					b.items, b.rows = nil, nil
				}
				if ui.Button(c, "Reveal in Tree").Disabled(sel == index.None || a.snap == nil || !a.snap.Reachable(sel)).Clicked() {
					b.open = false
					a.reveal(sel)
				}
				ui.Spacer(c)
				ready := tot.items > 0 && !b.busy && a.source == "" && a.win != nil
				if ui.Button(c, "Delete Permanently…").Disabled(!ready).Clicked() {
					a.cleanBasket(true)
				}
				if ui.PrimaryButton(c, "Move to Recycle Bin…").Disabled(!ready).Clicked() {
					a.cleanBasket(false)
				}
				if ui.Button(c, "Close").Clicked() {
					b.open = false
				}
			})
		})
	})
}
