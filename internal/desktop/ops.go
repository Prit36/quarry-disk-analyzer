package desktop

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// Selection.

// selected returns the selected entries of the active view, in row order.
func (a *App) selected() []index.ID {
	if a.snap == nil {
		return nil
	}
	switch a.prefs.View {
	case viewTree:
		return a.tree.selectedIDs()
	case viewFiles:
		return a.files.selectedIDs()
	}
	return nil
}

// current is the focused entry of the active view.
func (a *App) current() index.ID {
	ids := a.selected()
	if len(ids) == 0 {
		return index.None
	}
	switch a.prefs.View {
	case viewTree:
		if id := a.tree.cursorID(); id != index.None && a.tree.sel.Has(id) && (len(ids) > 1 || ids[0] == id) {
			return id
		}
	case viewFiles:
		if id := a.files.cursorID(); id != index.None && a.files.sel.Has(id) && (len(ids) > 1 || ids[0] == id) {
			return id
		}
	}
	return ids[0]
}

func (a *App) currentIsDir() bool {
	id := a.current()
	return id != index.None && a.snap.Nodes[id].IsDir()
}

// allPaths reports whether every selected entry is an ordinary file or
// folder that Windows can open. NTFS metadata ($MFT, $BadClus, …) has a
// name in the index but no path the shell accepts, and IsMetadata also
// covers entries without a resolved path. Cached per selection: menus ask
// on every frame.
func (a *App) allPaths() bool { return a.facts().onDisk }

func (a *App) paths(ids []index.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if p := a.pathOf(id); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// topLevel drops entries already covered by a selected ancestor.
func (a *App) topLevel(ids []index.ID) []index.ID {
	if len(ids) < 2 {
		return ids
	}
	// Only folders can cover other entries.
	hasDir := false
	for _, id := range ids {
		if a.snap.Nodes[id].IsDir() {
			hasDir = true
			break
		}
	}
	if !hasDir {
		return ids
	}
	var set []bool
	for _, id := range ids {
		if a.snap.Nodes[id].IsDir() {
			if set == nil {
				set = make([]bool, len(a.snap.Nodes))
			}
			set[id] = true
		}
	}
	out := make([]index.ID, 0, len(ids))
	for _, id := range ids {
		covered := false
		for p := a.snap.Nodes[id].Parent; p != index.None; p = a.snap.Nodes[p].Parent {
			if set[p] {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, id)
		}
	}
	return out
}

// Safety.

// blocked explains why an entry must never be renamed or deleted from
// Quarry, or returns "".
func (a *App) blocked(id index.ID) string {
	if id == 0 {
		return "the scan root"
	}
	if a.snap.IsMetadata(id) {
		return "NTFS filesystem metadata"
	}
	path := a.pathOf(id)
	if path == "" {
		return "an entry without a resolved path"
	}
	dir, name := filepath.Dir(path), strings.ToLower(filepath.Base(path))
	if len(dir) == 3 && dir[1] == ':' {
		if protectedDriveName(name) {
			return "a Windows system location"
		}
	}
	return ""
}

// caution returns a warning for locations that are risky but legitimately
// cleaned up, such as files inside the Windows folder.
func (a *App) caution(id index.ID) string {
	path := strings.ToLower(a.pathOf(id))
	for _, env := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		if root := strings.ToLower(os.Getenv(env)); root != "" && strings.HasPrefix(path, root+`\`) {
			return "Some items are inside " + os.Getenv(env) + ". Removing them can break Windows or installed programs."
		}
	}
	return ""
}

// mutable reports whether file-management commands apply to the selection.
func (a *App) mutable() bool {
	if a.snap == nil || a.source != "" {
		return false
	}
	ids := a.selected()
	return a.mutableSelection(ids)
}

// verify confirms that the entry on disk is still the one in the index, so
// an operation never acts on a different file that took a stale path.
func (a *App) verify(id index.ID) (string, error) {
	path := a.pathOf(id)
	if path == "" {
		return "", fmt.Errorf("this entry has no resolved path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		a.stale = "Some scanned entries no longer exist on disk."
		return "", fmt.Errorf("%s no longer exists. Rescan (F5) to refresh the results", path)
	}
	if info.IsDir() != a.snap.Nodes[id].IsDir() {
		a.stale = "Some scanned entries changed on disk."
		return "", fmt.Errorf("%s changed type since the scan. Rescan (F5) to refresh the results", path)
	}
	return path, nil
}

func (a *App) verifyAll(ids []index.ID) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		p, err := a.verify(id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Explorer integration.

func (a *App) openItems() {
	ids := a.selected()
	if len(ids) > 10 {
		a.fail("Select at most 10 items to open at once.")
		return
	}
	paths, err := a.verifyAll(ids)
	if err != nil {
		a.fail("%v", err)
		return
	}
	for i, p := range paths {
		var err error
		if a.snap.Nodes[ids[i]].IsDir() {
			err = platform.OpenFolder(p)
		} else {
			err = mygo.Shell.OpenPath(p)
		}
		if err != nil {
			a.fail("Windows could not open %s.", p)
			return
		}
	}
}

func (a *App) showInExplorer() {
	paths, err := a.verifyAll(a.topLevel(a.selected()))
	if err != nil {
		a.fail("%v", err)
		return
	}
	a.revealPaths(paths)
}

// revealPaths opens Explorer windows selecting paths, at most a few folders
// at once so a large selection cannot flood the desktop with windows.
func (a *App) revealPaths(paths []string) {
	folders := map[string]bool{}
	for _, p := range paths {
		folders[strings.ToLower(filepath.Dir(p))] = true
	}
	if len(folders) > 8 {
		a.fail("The selection is spread over %d folders. Select items in at most 8 folders to show them in Explorer.", len(folders))
		return
	}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := platform.ShowInExplorer(paths); err != nil {
			a.post(func() { a.fail("%v", err) })
		}
	}()
}

func (a *App) properties() {
	paths, err := a.verifyAll(a.selected())
	if err != nil {
		a.fail("%v", err)
		return
	}
	if err := platform.ShowProperties(a.win.NativeHandle(), paths); err != nil {
		a.fail("%v", err)
	}
}

// clipboardFiles places the selection on the clipboard as shell items, as
// Explorer does: pasting in Explorer copies them, or moves them after Cut.
func (a *App) clipboardFiles(cut bool) {
	ids := a.topLevel(a.selected())
	paths, err := a.verifyAll(ids)
	if err != nil {
		a.fail("%v", err)
		return
	}
	files, err := transfer.FileData(paths...)
	if err != nil {
		a.fail("%v", err)
		return
	}
	effect := make([]byte, 4)
	binary.LittleEndian.PutUint32(effect, 1) // DROPEFFECT_COPY
	if cut {
		binary.LittleEndian.PutUint32(effect, 2) // DROPEFFECT_MOVE
	}
	items := append(files.Items(), transfer.NewItem(transfer.Bytes("Preferred DropEffect", effect)))
	if err := mygo.Clipboard.Write(transfer.New(items...)); err != nil {
		a.fail("Clipboard: %v", err)
		return
	}
	verb := "Copied"
	if cut {
		verb = "Cut"
		a.stale = "Items were cut to the clipboard; pasting them elsewhere moves them. Rescan (F5) afterwards."
	}
	a.notify("%s %s to the clipboard. Paste in File Explorer.", verb, plural(uint64(len(paths)), "item", "items"))
}

func (a *App) copyText(lines []string, what string) {
	mygo.Clipboard.WriteText(strings.Join(lines, "\r\n"))
	a.notify("Copied %s.", what)
}

func (a *App) copyPaths() {
	ps := a.paths(a.selected())
	a.copyText(ps, plural(uint64(len(ps)), "path", "paths"))
}

func (a *App) copyNames() {
	var names []string
	for _, id := range a.selected() {
		names = append(names, a.name(id))
	}
	a.copyText(names, plural(uint64(len(names)), "name", "names"))
}

func (a *App) copySizes() {
	var sizes []string
	for _, id := range a.selected() {
		sizes = append(sizes, strconv.FormatUint(a.metric(a.snap.Nodes[id]), 10))
	}
	a.copyText(sizes, "sizes in bytes")
}

// copyRows copies tab-separated rows with a header, ready for a spreadsheet.
func (a *App) copyRows() {
	lines := []string{"Name\tPath\tSize\tAllocated\tFiles\tFolders\tModified"}
	for _, id := range a.selected() {
		n := a.snap.Nodes[id]
		files, dirs := "", ""
		if n.IsDir() {
			files, dirs = strconv.FormatUint(uint64(n.Files), 10), strconv.FormatUint(uint64(n.Dirs), 10)
		}
		lines = append(lines, strings.Join([]string{a.name(id), a.pathOf(id), strconv.FormatUint(n.Size, 10), strconv.FormatUint(n.Allocated, 10), files, dirs, dateText(n.ModTime())}, "\t"))
	}
	a.copyText(lines, plural(uint64(len(lines)-1), "row", "rows"))
}

func (a *App) selectAll() {
	switch a.prefs.View {
	case viewTree:
		a.tree.selectAll()
	case viewFiles:
		a.files.selectAll()
	}
}

// focusSearch is applied when the search field is next built.
func (a *App) focusSearch() { a.wantSearch = true }

func (a *App) searchHere() {
	id := a.current()
	if id == index.None {
		return
	}
	a.files.setScope(id)
	a.setViewMode(viewFiles)
	a.focusSearch()
}

func (a *App) focusInTreemap() {
	id := a.current()
	if id == index.None {
		return
	}
	a.prefs.ShowTreemap = true
	folder := id
	if !a.snap.Nodes[id].IsDir() {
		folder = a.snap.Nodes[id].Parent
	}
	a.navigate(folder)
	a.tm.highlight = id
	a.tree.reveal(id)
}

// Rename.

func (a *App) beginRename() {
	id := a.current()
	if id == index.None {
		return
	}
	if reason := a.blocked(id); reason != "" {
		a.fail("Quarry does not rename %s.", reason)
		return
	}
	a.dlg.rename = renameDialog{open: true, id: id, name: a.name(id), original: a.name(id)}
}

func (a *App) commitRename(id index.ID, name string) error {
	name = strings.TrimSpace(name)
	if name == a.name(id) {
		return nil
	}
	if err := platform.ValidateName(name); err != nil {
		return err
	}
	path, err := a.verify(id)
	if err != nil {
		return err
	}
	if _, err := platform.RenameItem(path, name); err != nil {
		return err
	}
	next := a.base.Clone()
	if err := next.Rename(id, name); err != nil {
		return err
	}
	a.replaceBase(next)
	a.notify("Renamed to %s.", name)
	return nil
}

// Delete.

func (a *App) deleteItems(permanent bool) {
	ids := a.topLevel(a.selected())
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if reason := a.blocked(id); reason != "" {
			a.fail("Quarry does not delete %s (%s).", a.name(id), reason)
			return
		}
	}
	paths, err := a.verifyAll(ids)
	if err != nil {
		a.fail("%v", err)
		return
	}
	var total uint64
	var files, folders uint64
	warning := ""
	for _, id := range ids {
		n := a.base.Nodes[id]
		total += n.Size
		if n.IsDir() {
			folders++
			files += uint64(n.Files)
		} else {
			files++
		}
		if w := a.caution(id); w != "" {
			warning = w
		}
	}
	what := a.name(ids[0])
	if len(ids) > 1 {
		what = plural(uint64(len(ids)), "item", "items")
	}
	detail := fmt.Sprintf("%s in %s, %s.", plural(files, "file", "files"), plural(folders, "folder", "folders"), a.size(total))
	if folders == 0 {
		detail = fmt.Sprintf("%s, %s.", plural(files, "file", "files"), a.size(total))
	}
	if warning != "" {
		detail += "\n\n" + warning
	}
	run := func() {
		owner := a.win.NativeHandle()
		go func() {
			aborted, err := platform.DeleteItems(owner, paths, !permanent)
			a.post(func() { a.afterDelete(ids, paths, permanent, aborted, err) })
		}()
	}
	if !permanent && !a.prefs.ConfirmRecycle && warning == "" {
		run()
		return
	}
	message, button, kind := "Move "+what+" to the Recycle Bin?", "Move to Recycle Bin", mygo.MessageQuestion
	if permanent {
		message, button, kind = "Permanently delete "+what+"?", "Delete Permanently", mygo.MessageWarning
		detail += "\n\nThis bypasses the Recycle Bin and cannot be undone."
	}
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.win, Type: kind, Title: "Quarry", Message: message, Detail: detail, Buttons: []string{button, "Cancel"}, DefaultButton: 1, CancelButton: 1})
		if err == nil && res.Button == 0 {
			a.post(run)
		}
	}()
}

// afterDelete removes from the index exactly the entries that are gone from
// disk, so totals stay true even when the shell skipped some items.
func (a *App) afterDelete(ids []index.ID, paths []string, permanent, aborted bool, err error) {
	var gone []index.ID
	var freed uint64
	for i, p := range paths {
		if _, statErr := os.Lstat(p); os.IsNotExist(statErr) {
			gone = append(gone, ids[i])
			freed += a.base.Nodes[ids[i]].Size
		}
	}
	if len(gone) > 0 {
		next := a.base.Clone()
		next.Remove(gone...)
		a.replaceBase(next)
		go func() { // the status bar's free space
			drives := platform.Drives()
			a.post(func() { a.drives = drives })
		}()
	}
	verb := "Moved %s (%s) to the Recycle Bin."
	if permanent {
		verb = "Deleted %s (%s) permanently."
	}
	switch {
	case err != nil:
		a.fail("%v", err)
	case aborted && len(gone) < len(ids):
		a.notify("Deletion canceled; %s removed.", plural(uint64(len(gone)), "item was", "items were"))
	case len(gone) < len(ids):
		a.stale = "Some items could not be deleted."
		a.fail("%d of %d items could not be deleted.", len(ids)-len(gone), len(ids))
	default:
		a.notify(verb, plural(uint64(len(gone)), "item", "items"), a.size(freed))
	}
}

// Settings commands.

func (a *App) setMetric(m string) {
	if a.prefs.Metric == m {
		return
	}
	a.prefs.Metric = m
	if a.snap != nil {
		a.tree.dataChanged()
		a.files.invalidate()
		a.ext.invalidate()
		a.tm.invalidate()
	}
}

func (a *App) setZoom(z float32) {
	a.prefs.Zoom = max(0.75, min(2, float32(int(z*10+0.5))/10))
}

func (a *App) setTheme(t string) {
	a.prefs.Theme = t
	switch t {
	case "Dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	case "Light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) resetLayout() {
	d := defaultPrefs()
	a.prefs.ShowTreemap, a.prefs.TreemapPct, a.prefs.ShowInfo, a.prefs.Zoom = d.ShowTreemap, d.TreemapPct, d.ShowInfo, d.Zoom
	a.prefs.TreeHidden, a.prefs.FileHidden = d.TreeHidden, d.FileHidden
	a.tree.list.Columns, a.files.list.Columns, a.ext.list.Columns = d.TreeColumns, d.FileColumns, d.ExtColumns
	a.tm.maximized = false
}

func (a *App) clearFilter() {
	a.filter = analysisFilter{}
	a.applyFilter()
}

func (a *App) toggleExplorerVerb() {
	var err error
	if platform.ExplorerVerbInstalled() {
		err = platform.RemoveExplorerVerb()
		if err == nil {
			a.notify("Removed “Analyze with Quarry” from the Explorer context menu.")
		}
	} else {
		err = platform.InstallExplorerVerb()
		if err == nil {
			a.notify("Added “Analyze with Quarry” to folder and drive context menus (Show more options on Windows 11).")
		}
	}
	if err != nil {
		a.fail("Explorer integration: %v", err)
	}
	a.dlg.explorerVerb = platform.ExplorerVerbInstalled()
}
