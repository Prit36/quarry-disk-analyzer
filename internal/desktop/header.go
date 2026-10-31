package desktop

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/platform"
)

// Drive selection and scanning, at the start of the toolbar: a drop-down of
// what to analyze and one Scan button, which is Stop while a scan runs.
// Choosing in the drop-down only selects; Scan (F5) starts.

func driveTitle(d platform.Drive) string {
	name := strings.TrimSuffix(d.Path, `\`)
	if d.Name != "" {
		return d.Name + " (" + name + ")"
	}
	return "Local Disk (" + name + ")"
}

// systemDrive is the default target: the Windows drive when it is listed.
func systemDrive(drives []platform.Drive) string {
	sys := os.Getenv("SystemDrive")
	for _, d := range drives {
		if sys != "" && strings.EqualFold(strings.TrimSuffix(d.Path, `\`), sys) {
			return d.Path
		}
	}
	if len(drives) > 0 {
		return drives[0].Path
	}
	return ""
}

// targetLabel names a.target in the drop-down: a drive by its title, a
// folder by its path.
func (a *App) targetLabel() string {
	if !a.drivesReady && a.target == "" {
		return "Loading drives…"
	}
	if d, ok := a.driveOf(a.target); ok && strings.EqualFold(strings.TrimSuffix(a.target, `\`), strings.TrimSuffix(d.Path, `\`)) {
		return driveTitle(d)
	}
	if a.target == "" {
		return "Choose a drive"
	}
	return a.target
}

func (a *App) drivePicker(c *ui.Context) {
	t := c.Theme()
	icon := "hard_drive"
	if len(strings.TrimSuffix(a.target, `\`)) > 2 {
		icon = "folder"
	}
	b := ui.ButtonBase(c.Key("drive-picker")).Width(200).MinWidth(110).Shrink(1).Height(26).Padding(0, 6, 0, 8).Gap(6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Label("Drive or folder").Tooltip("Drive or folder to scan: " + a.target).Disabled(a.scanning)
	color := t.Text
	switch {
	case a.scanning:
		color = disabledColor(t)
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	default:
		b.Background(t.Surface)
	}
	b.Children(func() {
		ui.Icon(c, icons[icon]).FontSize(14).TextColor(color).Shrink(0)
		ui.Text(c, a.targetLabel()).TextColor(color).SingleLine().MinWidth(0).Shrink(1).Grow(1)
		ui.Icon(c, icons["chevron_down"]).FontSize(10).TextColor(t.TextMuted).Shrink(0)
	})
	if a.scanning {
		return
	}
	b.Menu(func(m *ui.Menu) {
		for _, d := range a.drives {
			d := d
			label := driveTitle(d)
			if d.Total > 0 {
				label += fmt.Sprintf("  —  %s free of %s", a.size(d.Free), a.size(d.Total))
			}
			if m.Item(strings.ReplaceAll(label, "&", "&&")).Checked(strings.EqualFold(a.target, d.Path)).Chosen() {
				a.target = d.Path
			}
		}
		var recent []string
		for _, r := range a.prefs.Recent {
			if len(strings.TrimSuffix(r, `\`)) > 2 { // drive roots are listed above
				recent = append(recent, r)
			}
		}
		if len(recent) > 0 {
			m.Separator()
			for _, r := range recent {
				r := r
				if m.Item(strings.ReplaceAll(r, "&", "&&")).Checked(strings.EqualFold(a.target, r)).Chosen() {
					a.target = r
				}
			}
		}
		m.Separator()
		a.contextItems(m, "scan.folder", "file.openScan")
	})
}

// scanButton is the only scan button: Scan, or Stop while scanning.
func (a *App) scanButton(c *ui.Context) {
	t := c.Theme()
	label, icon, tip := "Scan", "arrow_clockwise", "Scan "+a.target+" (F5)"
	if a.scanning {
		label, icon, tip = "Stop", "stop", "Stop scanning (Esc)"
	}
	enabled := a.scanning || a.can("scan.rescan")
	b := ui.ButtonBase(c.Key("scan")).Height(26).Padding(0, 10, 0, 8).Gap(6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Shrink(0).Label(label).Tooltip(tip).Disabled(!enabled)
	color := t.Text
	switch {
	case !enabled:
		color = disabledColor(t)
	case b.Pressed():
		b.Background(t.SurfacePressed)
	case b.Hovered():
		b.Background(t.SurfaceHover)
	default:
		b.Background(t.Surface)
	}
	b.Children(func() {
		ic := color
		if a.scanning {
			ic = t.Danger
		}
		ui.Icon(c, icons[icon]).FontSize(13).TextColor(ic)
		ui.Text(c, label).TextColor(color).SingleLine()
	})
	if b.Clicked() {
		if a.scanning {
			a.stopScan()
		} else {
			a.exec("scan.rescan")
		}
	}
}

// Info bar.

// infoBar is one line under the toolbar with every fact about the drive
// and the scan, as labeled values in reading order: how full the drive is,
// what the scan holds, its counts and method, then anything that makes the
// results differ from the disk. Nothing here is repeated elsewhere.
func (a *App) infoBar(c *ui.Context, pal palette) {
	t := c.Theme()
	ui.Row(c).Height(28).Shrink(0).Padding(0, 10).Gap(10).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(0, 0, 1, 0).BorderColor(pal.separator).Clip().Children(func() {
		switch {
		case a.scanning:
			a.scanInfo(c, pal)
		case a.snap != nil:
			a.scanResultInfo(c, pal)
		default:
			if d, ok := a.driveOf(a.target); ok {
				a.driveUsage(c, pal, d, 0)
				infoSep(c, pal)
			}
			ui.Text(c, "Not scanned").TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
		}
	})
}

func infoSep(c *ui.Context, pal palette) {
	ui.Box(c).Size(1, 14).Background(pal.separator).Shrink(0)
}

// infoField is a label and its value.
func infoField(c *ui.Context, label, value string) ui.Element {
	t := c.Theme()
	return ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, label).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).SingleLine()
	})
}

// swatchField is a field named by a color of the usage bar.
func swatchField(c *ui.Context, pal palette, color ui.Color, label, value string) ui.Element {
	t := c.Theme()
	return ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Box(c).Size(8, 8).Radius(1).Background(color).Border(1, pal.bar.Mix(pal.barTrack, .3)).Shrink(0)
		ui.Text(c, label).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).SingleLine()
	})
}

// driveUsage is the drive's bar, fill level, free space and file system.
// scanned > 0 splits the used part of the bar into what the scan holds and
// the rest.
func (a *App) driveUsage(c *ui.Context, pal palette, d platform.Drive, scanned uint64) {
	t := c.Theme()
	used := d.Total - d.Free
	usedPct := percent(used, d.Total)
	full := usedPct >= 90
	otherColor := pal.bar.Mix(pal.barTrack, .55)
	if scanned == 0 {
		otherColor = pal.bar
	}
	ui.Box(c).Size(140, 10).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, pal.barTrack, 2)
		if d.Total == 0 {
			return
		}
		seg := r
		seg.W = r.W * float32(float64(used)/float64(d.Total))
		p.Fill(seg, otherColor, 2)
		if scanned > 0 {
			seg.W = max(2, r.W*float32(float64(scanned)/float64(d.Total)))
			p.Fill(seg, pal.bar, 2)
		}
	})
	pct := ui.Text(c, percentText(usedPct)+" used").SingleLine().Shrink(0)
	infoSep(c, pal)
	if full {
		pct.TextColor(t.Warning)
		warnText(c, "Free "+a.size(d.Free)+" of "+a.size(d.Total), "The drive is "+percentText(usedPct)+" full.")
	} else {
		infoField(c, "Free", a.size(d.Free)+" of "+a.size(d.Total))
	}
	if d.FileSystem != "" {
		infoSep(c, pal)
		ui.Text(c, d.FileSystem).TextColor(t.TextMuted).SingleLine().Shrink(0)
	}
}

func (a *App) scanResultInfo(c *ui.Context, pal palette) {
	s := a.snap
	root := s.Nodes[0]
	if a.base != nil {
		root = a.scanTotals()
	}
	d, live := a.driveOf(s.Root)
	live = live && a.source == ""
	if live {
		used := d.Total - d.Free
		scanned := min(s.Nodes[0].Allocated, used) // disk space, as the volume counts it
		a.driveUsage(c, pal, d, scanned)
		infoSep(c, pal)
		wholeDrive := strings.EqualFold(strings.TrimSuffix(s.Root, `\`), strings.TrimSuffix(d.Path, `\`))
		label, other := "Scanned", "Rest of drive"
		otherTip := "Used by everything on " + strings.TrimSuffix(d.Path, `\`) + " outside " + s.Root
		if wholeDrive {
			other = "Not listed"
			otherTip = "Used space the scan does not list: file system structures, shadow copies and anything that could not be read."
		} else {
			label = "This folder"
		}
		swatchField(c, pal, pal.bar, label, a.size(scanned))
		swatchField(c, pal, pal.bar.Mix(pal.barTrack, .55), other, a.size(used-scanned)).Tooltip(otherTip)
	} else {
		label := "Scan size"
		if a.source != "" {
			label = "Saved scan of " + platform.FormatDateTime(s.Started) + ":"
		}
		infoField(c, label, a.size(a.metric(s.Nodes[0])))
	}
	infoSep(c, pal)
	infoField(c, "Files", count(uint64(root.Files)))
	infoField(c, "Folders", count(uint64(root.Dirs)))
	infoSep(c, pal)
	tip := s.Engine
	if s.Fallback != "" {
		tip += "\nDirect MFT reading was not used: " + s.Fallback
		if !a.admin {
			tip += "\nOptions → Restart as Administrator enables it for NTFS drives."
		}
	}
	if bytes, ok := a.metadataBytes(); ok {
		tip += fmt.Sprintf("\nNTFS metadata (%s) is not listed; Options → Show NTFS Metadata Files lists it.", a.size(bytes))
	}
	infoField(c, "Read by", engineName(s)+" in "+duration(s.Elapsed)).Tooltip(tip)
	a.scanConditions(c, pal)
}

func (a *App) scanInfo(c *ui.Context, pal palette) {
	p := a.progress
	value := -1.0
	if p != nil && p.TotalRecords.Load() > 0 {
		value = float64(p.Records.Load()) / float64(p.TotalRecords.Load())
	}
	ui.Progress(c, value).Size(140, 10).Shrink(0)
	what := "Scanning " + a.target + "…"
	if value >= 0 {
		what = fmt.Sprintf("Scanning %s… %.0f %%", a.target, 100*value)
	}
	ui.Text(c, what).SingleLine().MinWidth(0).Shrink(1)
	if p == nil {
		return
	}
	infoSep(c, pal)
	infoField(c, "Files", count(p.Files.Load()))
	infoField(c, "Folders", count(p.Folders.Load()))
	infoSep(c, pal)
	el := time.Since(a.scanStart)
	infoField(c, "Elapsed", fmt.Sprintf("%d:%02d", int(el.Minutes()), int(el.Seconds())%60))
	if e := p.Errors.Load(); e > 0 {
		infoSep(c, pal)
		warnText(c, plural(e, "item could not be read", "items could not be read"), "")
	}
}

func warnText(c *ui.Context, text, tip string) ui.Element {
	t := c.Theme()
	r := ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0)
	if tip != "" {
		r.Tooltip(tip)
	}
	return r.Children(func() {
		ui.Icon(c, icons["warning"]).FontSize(12).TextColor(t.Warning)
		ui.Text(c, text).TextColor(t.Warning).SingleLine()
	})
}

// linkButton is a text link, for an action that belongs to a sentence.
func linkButton(c *ui.Context, key, text, tip string) bool {
	t := c.Theme()
	b := ui.ButtonBase(c.Key(key)).Padding(0, 2).Label(text).Shrink(0)
	if tip != "" {
		b.Tooltip(tip)
	}
	b.Children(func() { ui.Text(c, text).TextColor(t.Focus).Underline().SingleLine() })
	return b.Clicked()
}

// scanConditions name what makes the results differ from the disk as it
// is, only when it applies.
func (a *App) scanConditions(c *ui.Context, pal palette) {
	t := c.Theme()
	s := a.snap
	if s.Cancelled {
		infoSep(c, pal)
		warnText(c, "Partial scan: stopped before it finished", "Totals are incomplete.")
	}
	if a.stale != "" {
		infoSep(c, pal)
		warnText(c, "Results may be outdated", a.stale+"\nPress F5 to rescan.")
	}
	if len(s.Exclusions) > 0 {
		infoSep(c, pal)
		warnText(c, plural(s.Excluded, "entry skipped", "entries skipped"), "Options → Skip During Scan kept these out; totals are incomplete:\n"+strings.Join(s.Exclusions, "\n"))
	}
	if s.Errors > 0 {
		infoSep(c, pal)
		warnText(c, plural(s.Errors, "item could not be read", "items could not be read"), "Totals may be incomplete.")
		if linkButton(c, "issues", "Details", "List the items that could not be read") {
			a.dlg.issues = true
		}
	}
	if a.filter.active() {
		infoSep(c, pal)
		status := "Filtered: " + a.filter.summary()
		if a.filterBusy {
			status = "Applying filter…"
		} else if a.filter.stats.HiddenFiles > 0 {
			status += " · " + plural(a.filter.stats.HiddenFiles, "file", "files") + " hidden"
		}
		ui.Text(c, status).TextColor(t.Focus).SingleLine().MinWidth(0).Shrink(1).Tooltip(status)
		if linkButton(c, "clear-filter", "Clear", "Show all entries again") {
			a.clearFilter()
		}
	}
}

// Before results.

// emptyWorkspace is the workspace before there are results: the same tabs,
// table header and treemap pane, empty, with one line saying what fills
// them. A first scan's results replace it in place.
func (a *App) emptyWorkspace(c *ui.Context, pal palette) {
	t := c.Theme()
	msg := "Nothing scanned yet. Choose a drive or folder in the toolbar and press Scan (F5)."
	if !a.admin {
		msg += "\nScans of NTFS drives are much faster as administrator (Options → Restart as Administrator)."
	}
	if a.scanning {
		msg = "The results appear here when the scan finishes."
	}
	data := func() {
		ui.Column(c).Fill().MinHeight(0).Children(func() {
			ui.Row(c).Height(30).Shrink(0).Padding(0, 6).Gap(2).AlignItems(ui.Stretch).Background(pal.chrome).BorderWidth(0, 0, 1, 0).BorderColor(pal.separator).Children(func() {
				a.tabButtons(c, false)
			})
			prev := c.Theme()
			c.SetTheme(a.gridTheme(prev))
			cols, _ := visibleColumns(treeColumns, a.prefs.TreeHidden)
			ui.Table(c.Key("tree-empty"), &a.tree.list, cols, 0, func(int, int) {}).Grow(1).MinHeight(0).Background(pal.grid).Children(func() {
				ui.Text(c, msg).TextColor(t.TextMuted).Absolute().Top(t.Space(8) + 8).Left(12).MaxWidth(720)
			})
			c.SetTheme(prev)
		})
	}
	mapPane := func() {
		ui.Column(c).Fill().MinHeight(0).Children(func() {
			ui.Row(c).Height(28).Shrink(0).Padding(0, 6, 0, 10).AlignItems(ui.Center).Background(pal.chrome).BorderWidth(1, 0, 1, 0).BorderColor(pal.separator).Children(func() {
				ui.Text(c, "Treemap").TextColor(t.TextMuted).SingleLine()
			})
			ui.Box(c).Grow(1).Background(pal.grid)
		})
	}
	ui.Column(c).Fill().MinHeight(0).Children(func() {
		if a.prefs.ShowTreemap {
			ui.SplitVerticalPercent(c.Key("split"), &a.prefs.TreemapPct, data, mapPane).Grow(1).MinHeight(0)
		} else {
			data()
		}
	})
}
