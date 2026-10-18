package desktop

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

const (
	helpNone = iota
	helpKeys
	helpSyntax
	helpCLI
	helpAbout
)

type renameDialog struct {
	open           bool
	id             index.ID
	name, original string
	err            string
	selected       bool
}

type filterDialog struct {
	open             bool
	include, exclude string
	isolate, mapToo  bool
	err              string
}

type searchDialog struct {
	open                                             bool
	name, path, ext, minSize, maxSize, after, before string
	kind                                             string
	scope                                            string
	err                                              string
}

type exclusionsDialog struct {
	open bool
	text string
	use  bool
}

type compareDialog struct {
	open    bool
	label   string
	changes []index.Change
	list    ui.ListState
}

type dupRow struct {
	group int
	id    index.ID // index.None for a group header
}

type duplicateDialog struct {
	isOpen   bool
	verify   bool
	minSize  string
	busy     bool
	cancel   context.CancelFunc
	gen      uint64
	progress *index.DuplicateProgress
	result   index.DuplicateResult
	verified bool
	rows     []dupRow
	err      string
	list     ui.ListState
	cursor   int
	forSnap  *index.Snapshot
	scope    index.ID
	started  time.Time
}

type dialogs struct {
	rename       renameDialog
	filter       filterDialog
	search       searchDialog
	exclusions   exclusionsDialog
	compare      compareDialog
	dup          duplicateDialog
	issues, info bool
	issueList    ui.ListState
	help         int
	explorerVerb bool
	driveList    ui.ListState
	driveRow     int
}

func dialogTitle(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(14).Margin(0, 0, 4, 0)
}

func buttons(c *ui.Context, fn func()) {
	ui.Row(c).Gap(8).Justify(ui.End).Margin(8, 0, 0, 0).Children(fn)
}

func field(c *ui.Context, label string, value *string, placeholder string) ui.Element {
	var in ui.Element
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(130).TextColor(c.Theme().TextMuted)
		in = ui.TextInput(c.Key(label), value).Grow(1).Placeholder(placeholder).Label(label).SingleLine()
	})
	return in
}

func (a *App) dialogs(c *ui.Context, pal palette) {
	d := &a.dlg
	t := c.Theme()

	// Rename (F2).
	ui.Modal(c.Key("rename"), &d.rename.open, func() {
		r := &d.rename
		ui.Column(c).Width(420).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Rename")
			in := ui.TextInput(c.Key("rename-input"), &r.name).AutoFocus().Label("New name").SingleLine()
			if !r.selected {
				// Select the name without its extension, as Explorer does.
				stem := len([]rune(r.name))
				if dot := strings.LastIndex(r.name, "."); dot > 0 && !a.snap.Nodes[r.id].IsDir() {
					stem = len([]rune(r.name[:dot]))
				}
				in.SetTextSelection(0, stem)
				r.selected = true
			}
			if r.err != "" {
				ui.Text(c, r.err).TextColor(t.Danger).MaxWidth(390)
			}
			submit := in.Submitted()
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					r.open = false
				}
				if ui.PrimaryButton(c, "Rename").Clicked() {
					submit = true
				}
			})
			if submit {
				if err := a.commitRename(r.id, r.name); err != nil {
					r.err = err.Error()
				} else {
					r.open = false
				}
			}
		})
	})

	// Analysis filter.
	ui.Modal(c.Key("filter"), &d.filter.open, func() {
		f := &d.filter
		ui.Column(c).Width(560).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Analysis Filter")
			ui.Text(c, "Hides entries already in the scan; nothing is rescanned. Totals and percentages are recalculated for what remains.").TextColor(t.TextMuted).MaxWidth(520)
			field(c, "Show only files", &f.include, "e.g. ext:log;txt size:>100MB modified:<2024-01-01")
			field(c, "Hide", &f.exclude, "e.g. node_modules; *.tmp; C:\\Windows\\WinSxS")
			ui.Text(c, "“Show only” uses the search syntax. “Hide” takes names, wildcards or folder paths separated by ;").FontSize(11).TextColor(t.TextMuted)
			ui.Checkbox(c, &f.isolate, "Hide folders that contain no remaining files")
			ui.Checkbox(c, &f.mapToo, "Apply the filter to the treemap")
			if f.err != "" {
				ui.Text(c, f.err).TextColor(t.Danger)
			}
			buttons(c, func() {
				if ui.Button(c, "Clear Filter").Disabled(!a.filter.active()).Clicked() {
					f.open = false
					a.clearFilter()
				}
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Apply").Clicked() {
					if _, err := index.ParseFilter(f.include); err != nil {
						f.err = err.Error()
					} else {
						f.open = false
						a.filter = analysisFilter{Include: f.include, Exclude: f.exclude, Isolate: f.isolate}
						a.prefs.FilterMap = f.mapToo
						a.applyFilter()
					}
				}
			})
		})
	})

	// Advanced search builds a query in the search syntax.
	ui.Modal(c.Key("search"), &d.search.open, func() {
		s := &d.search
		ui.Column(c).Width(560).Padding(8).Gap(6).Children(func() {
			dialogTitle(c, "Advanced Search")
			field(c, "Name", &s.name, "words or wildcards, e.g. *.mp4 or backup*")
			field(c, "Path contains", &s.path, "e.g. node_modules")
			field(c, "Extensions", &s.ext, "e.g. log;txt")
			field(c, "Minimum size", &s.minSize, "e.g. 500MB")
			field(c, "Maximum size", &s.maxSize, "e.g. 4GiB")
			field(c, "Modified after", &s.after, "YYYY-MM-DD")
			field(c, "Modified before", &s.before, "YYYY-MM-DD (inclusive)")
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Type").Width(130).TextColor(t.TextMuted)
				ui.Select(c.Key("kind"), &s.kind, []string{"Files and folders", "Files", "Folders"}).Width(170)
				ui.Text(c, "Scope").TextColor(t.TextMuted)
				opts := []string{"Entire scan"}
				if a.location != 0 {
					opts = append(opts, "Current location")
				}
				ui.Select(c.Key("scope"), &s.scope, opts).Width(150)
			})
			query := s.query()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Query").Width(130).TextColor(t.TextMuted)
				ui.Text(c, query).Selectable().Font("Consolas").MinWidth(0).Shrink(1)
			})
			if s.err != "" {
				ui.Text(c, s.err).TextColor(t.Danger)
			}
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					s.open = false
				}
				if ui.PrimaryButton(c, "Search").Clicked() {
					if _, err := index.ParseFilter(query); err != nil {
						s.err = err.Error()
						return
					}
					s.open = false
					a.search = query
					scope := index.ID(0)
					if s.scope == "Current location" {
						scope = a.location
					}
					a.files.scope = scope
					a.ext.dirty = true
					a.setViewMode(viewFiles)
					a.files.invalidate()
					a.focusTable = true
				}
			})
		})
	})

	// Scan exclusions.
	ui.Modal(c.Key("exclusions"), &d.exclusions.open, func() {
		e := &d.exclusions
		ui.Column(c).Width(560).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Scan Exclusions")
			ui.Text(c, "Entries matching these patterns are skipped by future scans. Use a full folder path (C:\\Users\\me\\AppData\\Local\\Temp) or a name or wildcard (node_modules, *.tmp), one per line.").TextColor(t.TextMuted).MaxWidth(520)
			ui.TextArea(c.Key("excl-text"), &e.text).Height(160).Font("Consolas").Label("Exclusion patterns")
			ui.Checkbox(c, &e.use, "Apply exclusions when scanning")
			ui.Text(c, "Excluded entries are not counted, so scan totals will be incomplete. Quarry shows how many entries were excluded.").FontSize(11).TextColor(t.Warning).MaxWidth(520)
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					e.open = false
				}
				if ui.PrimaryButton(c, "Save").Clicked() {
					e.open = false
					a.prefs.Exclusions = splitPatterns(e.text)
					a.prefs.UseExclusions = e.use
					_ = a.savePrefs()
					if a.base != nil {
						a.notify("Exclusions saved. They apply to the next scan (F5).")
					}
				}
			})
		})
	})

	a.duplicatesDialog(c, pal)

	// Scan issues.
	ui.Modal(c.Key("issues"), &d.issues, func() {
		ui.Column(c).Width(760).MaxWidthPercent(100).Height(440).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Scan Issues")
			if a.snap == nil {
				return
			}
			s := a.snap
			ui.Text(c, fmt.Sprintf("%s could not be read. Totals for their folders may be incomplete. Showing the first %d.", plural(s.Errors, "item", "items"), len(s.Issues))).TextColor(t.TextMuted)
			ui.Table(c.Key("issue-table"), &d.issueList, []ui.TableColumn{{Title: "Path", Width: 420}, {Title: "Problem", MinWidth: 160}}, len(s.Issues), func(row, col int) {
				text := s.Issues[row].Path
				if col == 1 {
					text = s.Issues[row].Message
				}
				ui.Text(c, text).SingleLine().Tooltip(text)
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			buttons(c, func() {
				if ui.Button(c, "Copy List").Clicked() {
					var lines []string
					for _, is := range s.Issues {
						lines = append(lines, is.Path+"\t"+is.Message)
					}
					a.copyText(lines, "issues")
				}
				if !a.admin && ui.Button(c, "Restart as Administrator").Clicked() {
					a.restartElevated()
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					d.issues = false
				}
			})
		})
	})

	// Scan information and space accounting.
	ui.Modal(c.Key("info"), &d.info, func() {
		ui.Column(c).Width(600).Padding(8).Gap(4).Children(func() {
			dialogTitle(c, "Scan Information")
			if a.snap == nil {
				return
			}
			s, root := a.base, a.scanTotals()
			line := func(k, v string) {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, k).Width(170).TextColor(t.TextMuted)
					ui.Text(c, v).Selectable().MinWidth(0).Shrink(1)
				})
			}
			line("Scan root", s.Root)
			if a.source != "" {
				line("Saved scan", a.source)
			}
			line("Method", s.Engine)
			if s.Fallback != "" {
				line("MFT not used", s.Fallback)
			}
			line("Started", platform.FormatDateTime(s.Started))
			line("Duration", duration(s.Elapsed))
			line("Files / folders", count(uint64(root.Files))+" / "+count(uint64(root.Dirs)))
			line("Logical size", a.size(root.Size)+"  ("+count(root.Size)+" bytes)")
			line("Allocated size", a.size(root.Allocated)+"  ("+count(root.Allocated)+" bytes)")
			if dr, ok := a.driveOf(s.Root); ok {
				line("Volume used / free", a.size(dr.Total-dr.Free)+" / "+a.size(dr.Free)+" of "+a.size(dr.Total))
			}
			line("Hard-link names", count(s.HardLinks)+" (counted once)")
			line("Links not followed", count(s.SkippedLinks))
			line("Unreadable items", count(s.Errors))
			if s.Unresolved > 0 {
				line("Unresolved records", count(s.Unresolved))
			}
			if len(s.Exclusions) > 0 {
				line("Excluded entries", count(s.Excluded)+" ("+strings.Join(s.Exclusions, "; ")+")")
			}
			if s.Cancelled {
				line("Status", "Stopped before completion - partial results")
			}
			ui.Text(c, "Logical size is the sum of file lengths. Allocated size is the disk space those files occupy (smaller for compressed or sparse files, larger due to cluster rounding). The volume's used space also includes filesystem structures, shadow copies and files Quarry could not read, so the numbers are not expected to match.").TextColor(t.TextMuted).Margin(8, 0, 0, 0).MaxWidth(560)
			buttons(c, func() {
				if ui.PrimaryButton(c, "Close").Clicked() {
					d.info = false
				}
			})
		})
	})

	// Comparison.
	ui.Modal(c.Key("compare"), &d.compare.open, func() {
		cmpd := &d.compare
		ui.Column(c).Width(640).MaxWidthPercent(100).Height(440).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Changes since "+cmpd.label)
			ui.Text(c, "Top-level folders of the scan root, by "+strings.ToLower(a.prefs.Metric)+" size.").TextColor(t.TextMuted)
			ui.Table(c.Key("compare-table"), &cmpd.list, []ui.TableColumn{{Title: "Name", MinWidth: 160}, {Title: "Before", Width: 100, Align: ui.End}, {Title: "Now", Width: 100, Align: ui.End}, {Title: "Change", Width: 110, Align: ui.End}}, len(cmpd.changes), func(row, col int) {
				ch := cmpd.changes[row]
				switch col {
				case 0:
					ui.Text(c, ch.Name).SingleLine()
				case 1:
					ui.Text(c, a.size(ch.Previous)).TextColor(t.TextMuted)
				case 2:
					ui.Text(c, a.size(ch.Current))
				case 3:
					sign, color := "+", t.Warning
					if !ch.Increase {
						sign, color = "−", t.Focus
					}
					if ch.Difference == 0 {
						sign, color = "", t.TextMuted
					}
					ui.Text(c, sign+a.size(ch.Difference)).TextColor(color)
				}
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			buttons(c, func() {
				if ui.PrimaryButton(c, "Close").Clicked() {
					cmpd.open = false
				}
			})
		})
	})

	// Help.
	open := d.help != helpNone
	ui.Modal(c.Key("help"), &open, func() {
		ui.Column(c).Width(640).MaxHeight(660).Padding(8).Gap(8).Children(func() {
			var title string
			var sections []helpSection
			switch d.help {
			case helpKeys:
				title, sections = "Keyboard Shortcuts", a.shortcutHelp()
			case helpSyntax:
				title, sections = "Search Syntax", searchHelp
			case helpCLI:
				title, sections = "Command Line", cliHelp
			case helpAbout:
				title = "About Quarry"
				sections = []helpSection{{note: "Quarry " + Version + "\nA disk space analyzer for Windows.\n\nReads the NTFS master file table directly when running as administrator, and enumerates folders otherwise. Sizes are computed from the index in memory; nothing is uploaded or sent anywhere.\n\nSettings are stored in " + configDir() + "."}}
			}
			dialogTitle(c, title)
			ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
				helpBody(c, sections)
			})
			buttons(c, func() {
				if d.help == helpCLI && ui.Button(c, "Copy").Clicked() {
					a.copyText(strings.Split(CLIUsage, "\n"), "command line reference")
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					open = false
				}
			})
		})
	})
	if !open {
		d.help = helpNone
	}
}

// Version is set by main.
var Version = "0.2.1"

func (s *searchDialog) query() string {
	var parts []string
	quote := func(v string) string {
		if strings.ContainsAny(v, " \t") {
			return `"` + v + `"`
		}
		return v
	}
	for _, w := range strings.Fields(s.name) {
		parts = append(parts, w)
	}
	if v := strings.TrimSpace(s.path); v != "" {
		parts = append(parts, "path:"+quote(v))
	}
	if v := strings.TrimSpace(s.ext); v != "" {
		parts = append(parts, "ext:"+strings.ReplaceAll(v, " ", ""))
	}
	if v := strings.TrimSpace(s.minSize); v != "" {
		parts = append(parts, "size:>="+strings.ReplaceAll(v, " ", ""))
	}
	if v := strings.TrimSpace(s.maxSize); v != "" {
		parts = append(parts, "size:<="+strings.ReplaceAll(v, " ", ""))
	}
	if v := strings.TrimSpace(s.after); v != "" {
		parts = append(parts, "modified:>="+v)
	}
	if v := strings.TrimSpace(s.before); v != "" {
		parts = append(parts, "modified:<="+v)
	}
	switch s.kind {
	case "Files":
		parts = append(parts, "type:file")
	case "Folders":
		parts = append(parts, "type:folder")
	}
	return strings.Join(parts, " ")
}

func (d *dialogs) openSearch(a *App) {
	if d.search.kind == "" {
		d.search.kind, d.search.scope = "Files", "Entire scan"
	}
	d.search.err = ""
	d.search.open = true
}

func (d *dialogs) openFilter(a *App) {
	d.filter = filterDialog{open: true, include: a.filter.Include, exclude: a.filter.Exclude, isolate: a.filter.Isolate, mapToo: a.prefs.FilterMap}
}

func (d *dialogs) openExclusions(a *App) {
	d.exclusions = exclusionsDialog{open: true, text: strings.Join(a.prefs.Exclusions, "\r\n"), use: a.prefs.UseExclusions}
}

// Duplicates.

func (dd *duplicateDialog) open(a *App) {
	dd.isOpen = true
	if dd.minSize == "" {
		dd.minSize = "1MB"
	}
	if dd.forSnap != a.snap {
		dd.reset()
	}
}

func (dd *duplicateDialog) reset() {
	if dd.cancel != nil {
		dd.cancel()
	}
	dd.busy, dd.rows, dd.result, dd.err, dd.forSnap = false, nil, index.DuplicateResult{}, "", nil
	dd.gen++
}

func (a *App) findDuplicates() {
	dd := &a.dlg.dup
	min, err := index.ParseSize(dd.minSize)
	if err != nil {
		dd.err = err.Error()
		return
	}
	dd.reset()
	dd.err = ""
	gen := dd.gen
	ctx, cancel := context.WithCancel(context.Background())
	dd.cancel, dd.busy, dd.progress, dd.started = cancel, true, &index.DuplicateProgress{}, time.Now()
	s, scope, verify, progress := a.snap, dd.scope, dd.verify, dd.progress
	if !s.Reachable(scope) {
		scope = 0
	}
	go func() {
		var res index.DuplicateResult
		var err error
		if verify {
			res, err = s.FindDuplicatesOver(ctx, scope, min, progress)
		} else {
			res, err = candidates(ctx, s, scope, min)
		}
		a.post(func() {
			if gen != dd.gen {
				return
			}
			dd.busy, dd.forSnap, dd.verified = false, s, verify
			if err != nil {
				if ctx.Err() == nil {
					dd.err = err.Error()
				}
				return
			}
			dd.result = res
			dd.rows = dd.rows[:0]
			for g, group := range res.Groups {
				dd.rows = append(dd.rows, dupRow{group: g, id: index.None})
				for _, id := range group.IDs {
					dd.rows = append(dd.rows, dupRow{group: g, id: id})
				}
			}
		})
	}()
}

// candidates groups files by name and size: likely duplicates, unverified.
func candidates(ctx context.Context, s *index.Snapshot, root index.ID, minSize uint64) (index.DuplicateResult, error) {
	type key struct {
		name string
		size uint64
	}
	groups := map[key][]index.ID{}
	err := s.Visit(ctx, root, func(id index.ID) error {
		n := s.Nodes[id]
		if !n.IsDir() && n.Size >= max(minSize, 1) && n.Flags&(index.Shared|index.Reparse|index.Unplaced) == 0 {
			k := key{strings.ToLower(s.NameView(id)), n.Size}
			groups[k] = append(groups[k], id)
		}
		return nil
	})
	res := index.DuplicateResult{}
	for k, ids := range groups {
		if len(ids) > 1 {
			res.Groups = append(res.Groups, index.DuplicateGroup{IDs: ids, Size: k.size})
		}
	}
	slices.SortFunc(res.Groups, func(x, y index.DuplicateGroup) int {
		return cmp.Compare(y.Size*uint64(len(y.IDs)-1), x.Size*uint64(len(x.IDs)-1))
	})
	return res, err
}

func (a *App) duplicatesDialog(c *ui.Context, pal palette) {
	dd := &a.dlg.dup
	t := c.Theme()
	if dd.busy {
		c.After(200 * time.Millisecond)
	}
	ui.Modal(c.Key("dups"), &dd.isOpen, func() {
		ui.Column(c).Width(860).MaxWidthPercent(100).Height(560).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Find Duplicates")
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Files of at least").TextColor(t.TextMuted)
				ui.TextInput(c.Key("dup-min"), &dd.minSize).Width(80).SingleLine().Label("Minimum size")
				ui.Text(c, "in").TextColor(t.TextMuted)
				scope := "Entire scan"
				if dd.scope != 0 {
					scope = "Current location"
				}
				opts := []string{"Entire scan"}
				if a.location != 0 {
					opts = append(opts, "Current location")
				}
				if ui.Select(c.Key("dup-scope"), &scope, opts).Width(150).Changed() {
					dd.scope = 0
					if scope == "Current location" {
						dd.scope = a.location
					}
				}
				ui.Checkbox(c, &dd.verify, "Verify content (SHA-256)").Tooltip("Reads every candidate file completely. Without it, files are grouped by name and size only, which suggests but does not prove duplication.")
				ui.Spacer(c)
				if dd.busy {
					if ui.Button(c, "Cancel").Clicked() {
						dd.reset()
					}
				} else if ui.PrimaryButton(c, "Find").Clicked() {
					a.findDuplicates()
				}
			})
			switch {
			case dd.busy && dd.verify && dd.progress != nil:
				done, total := dd.progress.Bytes.Load(), dd.progress.TotalBytes.Load()
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					v := -1.0
					if total > 0 {
						v = float64(done) / float64(total)
					}
					ui.Progress(c, v).Width(220)
					ui.Text(c, fmt.Sprintf("Hashing: %s of %s, %s files checked", a.size(done), a.size(total), count(dd.progress.Files.Load()))).TextColor(t.TextMuted)
				})
			case dd.busy:
				ui.Text(c, "Searching…").TextColor(t.TextMuted)
			case dd.err != "":
				ui.Text(c, dd.err).TextColor(t.Danger)
			case dd.forSnap != nil:
				var waste uint64
				for _, g := range dd.result.Groups {
					waste += g.Size * uint64(len(g.IDs)-1)
				}
				status := "Candidates (same name and size, not verified)"
				if dd.verified {
					status = fmt.Sprintf("Verified identical content · %s checked, %s skipped (changed or unreadable)", count(dd.result.Checked), count(dd.result.Skipped))
				}
				ui.Text(c, fmt.Sprintf("%s groups · up to %s in extra copies · %s", count(uint64(len(dd.result.Groups))), a.size(waste), status)).TextColor(t.TextMuted)
			default:
				ui.Text(c, "Quarry never deletes duplicates automatically. Identical files can still be needed in both places; review each group. Hard links are already counted once and are excluded.").TextColor(t.TextMuted).MaxWidth(820)
			}
			dd.list.Selected = &dd.cursor
			s := dd.forSnap
			table := ui.Table(c.Key("dup-table"), &dd.list, []ui.TableColumn{{Title: "Name", Width: 240, MinWidth: 120}, {Title: "Folder", MinWidth: 200}, {Title: "Size", Width: 90, Align: ui.End}, {Title: "Modified", Width: 144}}, len(dd.rows), func(row, col int) {
				r := dd.rows[row]
				g := dd.result.Groups[r.group]
				if r.id == index.None {
					switch col {
					case 0:
						ui.Text(c, fmt.Sprintf("%d copies of %s", len(g.IDs), s.NameView(g.IDs[0]))).Bold().SingleLine()
					case 2:
						ui.Text(c, a.size(g.Size)+" each").TextColor(t.TextMuted)
					}
					return
				}
				id := r.id
				cell := cellBox(c, []ui.Align{ui.Start, ui.Start, ui.End, ui.Start}[col])
				cell.ContextMenu(func(m *ui.Menu) {
					dd.cursor = row
					if m.Item("Reveal in Tree").Chosen() {
						a.post(func() { dd.isOpen = false; a.reveal(id) })
					}
					if m.Item("Show in Explorer").Chosen() {
						if p := s.Path(id); p != "" {
							a.revealPaths([]string{p})
						}
					}
					if m.Item("Copy Full Path").Chosen() {
						mygo.Clipboard.WriteText(s.Path(id))
					}
				})
				cell.Children(func() {
					switch col {
					case 0:
						ui.Text(c, s.NameView(id)).SingleLine().PaddingStart(14)
					case 1:
						folder := s.Path(s.Nodes[id].Parent)
						ui.Text(c, folder).SingleLine().TextColor(t.TextMuted).Tooltip(folder).MinWidth(0).Shrink(1)
					case 2:
						ui.Text(c, a.size(s.Nodes[id].Size)).TextColor(t.TextMuted)
					case 3:
						ui.Text(c, dateText(s.Nodes[id].ModTime())).TextColor(t.TextMuted)
					}
				})
			}).Grow(1).MinHeight(0).Border(1, t.Border)
			if table.Submitted() && dd.cursor >= 0 && dd.cursor < len(dd.rows) && dd.rows[dd.cursor].id != index.None {
				id := dd.rows[dd.cursor].id
				dd.isOpen = false
				a.reveal(id)
			}
			buttons(c, func() {
				ui.Text(c, "Double-click a file to reveal it in Tree View.").TextColor(t.TextMuted)
				ui.Spacer(c)
				if ui.PrimaryButton(c, "Close").Clicked() {
					dd.isOpen = false
				}
			})
		})
	})
}
