package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
	"quarry/internal/scan"
)

// Refreshing a folder rescans only that folder and puts the result in place
// of its previous contents: quicker than a full scan after cleaning up in
// one place. The scan root itself is refreshed by a full scan (F5).

func (a *App) refreshFolder(id index.ID) {
	if a.base == nil || a.source != "" || a.scanning || a.refreshing {
		return
	}
	if id == 0 {
		a.startScan()
		return
	}
	path := a.pathOf(id)
	if path == "" || !a.base.Nodes[id].IsDir() {
		a.fail("Only folders with a path on disk can be refreshed.")
		return
	}
	opts := scan.Options{ForceWalk: true, AllowFallback: true}
	if a.prefs.UseExclusions {
		opts.Exclude = append([]string(nil), a.prefs.Exclusions...)
	}
	a.refreshing = true
	a.notify("Refreshing %s…", path)
	root := a.base.Root
	started := time.Now()
	go func() {
		fresh, err := scan.Run(context.Background(), path, opts, &scan.Progress{})
		a.post(func() {
			a.refreshing = false
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.fail("Could not refresh %s: %v", path, err)
				return
			}
			if a.base == nil || a.source != "" || !strings.EqualFold(a.base.Root, root) {
				return // another scan replaced this one meanwhile
			}
			target, ok := a.findBase(path)
			if !ok {
				a.fail("%s is no longer part of the scan.", path)
				return
			}
			before := a.base.Nodes[target]
			next, err := a.base.Graft(target, fresh)
			if err != nil {
				a.fail("Could not refresh %s: %v", path, err)
				return
			}
			a.replaceBase(next)
			a.stale = ""
			after := next.Nodes[target]
			a.notify("Refreshed %s in %s: %s now (%s).", filepath.Base(path), duration(time.Since(started)), a.size(a.metric(after)), signedSize(a, int64(a.metric(after))-int64(a.metric(before))))
			go func() {
				drives := platform.Drives()
				a.post(func() { a.drives = drives })
			}()
		})
	}()
}

// retryIssue refreshes the folder of an unreadable entry, to read it again
// after its permissions changed or a device came back.
func (a *App) retryIssue(issue index.Issue) {
	if a.base == nil || a.source != "" {
		return
	}
	for p := filepath.Clean(issue.Path); ; p = filepath.Dir(p) {
		if id, ok := a.findBase(p); ok && a.base.Nodes[id].IsDir() {
			a.refreshFolder(id)
			return
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	a.fail("%s is not part of the current scan.", issue.Path)
}

// Skipping a folder in future scans.

type skipDialog struct {
	open bool
	path string
	size uint64
}

func (a *App) skipFolder(id index.ID) {
	path := a.pathOf(id)
	if path == "" || id == 0 {
		return
	}
	a.skip = skipDialog{open: true, path: path, size: a.metric(a.snap.Nodes[id])}
}

func (a *App) skipPath(path string) {
	a.skip = skipDialog{open: true, path: filepath.Clean(path)}
}

func (a *App) skipDialog(c *ui.Context) {
	d := &a.skip
	t := c.Theme()
	a.modal(c.Key("skip"), &d.open, func() {
		ui.Column(c).Width(520).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Skip This Folder in Future Scans")
			ui.Text(c, d.path).Selectable().MaxWidth(490)
			text := "Future scans will not read this folder or count its size. This scan's results stay as they are; nothing on disk changes."
			if d.size > 0 {
				text += " It holds " + a.size(d.size) + " now."
			}
			ui.Text(c, text).TextColor(t.TextMuted).MaxWidth(490)
			ui.Text(c, "Edit or turn off the list in Options → Skip During Scan.").TextColor(t.TextMuted).MaxWidth(490)
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Skip in Future Scans").Clicked() {
					d.open = false
					if !slices.ContainsFunc(a.prefs.Exclusions, func(e string) bool { return strings.EqualFold(e, d.path) }) {
						a.prefs.Exclusions = append(a.prefs.Exclusions, d.path)
					}
					a.prefs.UseExclusions = true
					a.savePrefs()
					a.notify("Future scans will skip %s.", d.path)
				}
			})
		})
	})
}
