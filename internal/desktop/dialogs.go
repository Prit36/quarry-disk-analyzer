package desktop

import (
	"fmt"
	"strings"

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

type dialogs struct {
	rename       renameDialog
	filter       filterDialog
	search       searchDialog
	exclusions   exclusionsDialog
	compare      compareDialog
	issues, info bool
	issueList    ui.ListState
	help         int
	explorerVerb bool
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
			dialogTitle(c, "Show/Hide Filter")
			ui.Text(c, "Narrows the results already scanned; nothing is rescanned. Totals and percentages are recalculated for what remains.").TextColor(t.TextMuted).MaxWidth(520)
			field(c, "Show only", &f.include, "e.g. ext:log;txt size:>100MB modified:<2024-01-01")
			syntaxNote(c, "Files matching a search, written as in the search box.", a)
			field(c, "Hide", &f.exclude, "e.g. node_modules; *.tmp; C:\\Windows\\WinSxS")
			ui.Text(c, patternNote).FontSize(11).TextColor(t.TextMuted).MaxWidth(520)
			ui.Checkbox(c, &f.isolate, "Hide folders that contain no remaining files")
			ui.Checkbox(c, &f.mapToo, "Apply the filter to the treemap")
			// Checked while typing, so a mistake shows before Apply.
			_, perr := index.ParseFilter(f.include)
			if perr != nil {
				ui.Text(c, perr.Error()).TextColor(t.Danger).MaxWidth(520)
			}
			buttons(c, func() {
				if ui.Button(c, "Clear Filter").Disabled(!a.filter.active()).Clicked() {
					f.open = false
					a.clearFilter()
				}
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Apply").Disabled(perr != nil).Clicked() {
					f.open = false
					a.filter = analysisFilter{Include: f.include, Exclude: f.exclude, Isolate: f.isolate}
					a.prefs.FilterMap = f.mapToo
					a.applyFilter()
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
			field(c, "Modified on or after", &s.after, "YYYY-MM-DD")
			field(c, "Modified on or before", &s.before, "YYYY-MM-DD")
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
			_, perr := index.ParseFilter(query)
			if perr != nil {
				ui.Text(c, perr.Error()).TextColor(t.Danger).MaxWidth(520)
			}
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					s.open = false
				}
				if ui.PrimaryButton(c, "Search").Disabled(perr != nil || query == "").Clicked() {
					s.open = false
					if a.prefs.View != viewFiles {
						a.setDuplicateMode(false)
					}
					a.search = query
					if s.scope == "Current location" {
						a.files.followLocation()
					} else {
						a.files.pinScope(0)
					}
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
			dialogTitle(c, "Skip During Scan")
			ui.Text(c, "Scans do not read entries matching these patterns. To hide entries from results you already have, use Tools → Show/Hide Filter instead.").TextColor(t.TextMuted).MaxWidth(520)
			ui.TextArea(c.Key("excl-text"), &e.text).Height(160).Font("Consolas").Label("Patterns to skip")
			ui.Text(c, patternNote).FontSize(11).TextColor(t.TextMuted).MaxWidth(520)
			ui.Checkbox(c, &e.use, "Skip these when scanning")
			ui.Text(c, "Skipped entries are not counted, so scan totals will be incomplete. Quarry shows how many entries were skipped.").FontSize(11).TextColor(t.Warning).MaxWidth(520)
			save := func() {
				e.open = false
				a.prefs.Exclusions = splitPatterns(e.text)
				a.prefs.UseExclusions = e.use
				_ = a.savePrefs()
			}
			// A live scan can be redone right away; otherwise the list waits for the next scan.
			canRescan := a.base != nil && a.source == "" && a.can("scan.rescan")
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					e.open = false
				}
				if canRescan {
					if ui.Button(c, "Save").Clicked() {
						save()
						a.notify("Saved. The list applies to the next scan (F5).")
					}
					if ui.PrimaryButton(c, "Save and Rescan").Clicked() {
						save()
						a.post(a.rescan)
					}
				} else if ui.PrimaryButton(c, "Save").Clicked() {
					save()
				}
			})
		})
	})

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
var Version = "0.2.5"

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
		d.search.kind = "Files"
	}
	// Start from where the user is, as the search box does.
	d.search.scope = "Entire scan"
	if a.location != 0 && (!a.files.pinned || a.files.scope == a.location) {
		d.search.scope = "Current location"
	}
	d.search.open = true
}

// patternNote explains the pattern lists of the Hide field and Skip During
// Scan, which take the same syntax.
const patternNote = "Names, wildcards or full folder paths, separated by ; or one per line."

// syntaxNote is a hint under a search-syntax field with a link to the reference.
func syntaxNote(c *ui.Context, text string, a *App) {
	t := c.Theme()
	ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
		b := ui.ButtonBase(c.Key("syntax-link")).Label("Search syntax").Tooltip("Search Syntax (Help menu)")
		b.Children(func() { ui.Text(c, "Search syntax").FontSize(11).TextColor(t.Focus).Underline() })
		if b.Clicked() {
			a.dlg.help = helpSyntax
		}
	})
}

func (d *dialogs) openFilter(a *App) {
	d.filter = filterDialog{open: true, include: a.filter.Include, exclude: a.filter.Exclude, isolate: a.filter.Isolate, mapToo: a.prefs.FilterMap}
}

func (d *dialogs) openExclusions(a *App) {
	d.exclusions = exclusionsDialog{open: true, text: strings.Join(a.prefs.Exclusions, "\r\n"), use: a.prefs.UseExclusions}
}
