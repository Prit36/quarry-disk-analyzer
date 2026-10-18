package desktop

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// helpRow is one line of a help table: a key, flag or command line, and what it does.
type helpRow struct {
	term, desc string
}

// helpSection is one titled part of a help dialog.
type helpSection struct {
	title   string
	stacked bool      // the term sits above its description, for command lines too long for a column
	rows    []helpRow // a table, or stacked terms
	note    string    // a paragraph after the rows
}

// cliHelp is the command line reference. The Command Line dialog and the
// -help output both come from it, so they cannot disagree.
var cliHelp = []helpSection{
	{title: "Usage", stacked: true, rows: []helpRow{
		{"Quarry.exe [folder | drive | file.quarry]", "Open the window and analyze the folder, or open a saved scan."},
		{"Quarry.exe -scan <folder> [-csv out.csv] [-save out.quarry] [-json]\n[-exclude pattern]... [-walk] [-quiet] [-workers n]", "Scan without a window; the options below apply."},
	}},
	{title: "Options", rows: []helpRow{
		{"-scan <folder>", "Scan a folder or drive without opening a window"},
		{"-csv <file>", "Write every entry (path, sizes in bytes, counts, modified time)"},
		{"-save <file>", "Write a Quarry scan that the app can open later"},
		{"-json", "Print a summary as JSON to standard output"},
		{"-exclude <pattern>", "Skip a folder path or a name pattern, e.g. node_modules. Repeat for more."},
		{"-walk", "Enumerate folders even when direct MFT reading is possible"},
		{"-workers <n>", "Folder readers to run in parallel, at most 16 (default: from the CPU count)"},
		{"-quiet", "Print nothing except errors"},
	}, note: "-csv, -save and -json require -scan."},
	{title: "Exit codes", rows: []helpRow{
		{"0", "Success"},
		{"1", "The scan or an output file failed, or the scan was stopped"},
		{"2", "Invalid arguments"},
		{"3", "The scan finished, but some items could not be read; results are incomplete"},
	}},
	{title: "Examples", stacked: true, rows: []helpRow{
		{`Quarry.exe -scan "C:\Projects" -csv projects.csv`, "Save a CSV of everything under C:\\Projects"},
		{`Quarry.exe -scan D:\ -save d-drive.quarry -exclude "$RECYCLE.BIN"`, "Scan D:, keep the result, and skip the Recycle Bin"},
		{"Quarry.exe d-drive.quarry", "Open a saved scan in the window"},
	}},
}

// searchHelp is the search syntax reference (Help → Search Syntax).
var searchHelp = []helpSection{
	{note: "Terms are case-insensitive and must all match (AND)."},
	{title: "Terms", rows: []helpRow{
		{"report", `Names containing "report"`},
		{"*.mp4", "Wildcards over the whole name: * any run of characters, ? one character"},
		{"backup*", `Names starting with "backup"`},
		{`"my file"`, "Quoted text keeps its spaces"},
		{"-cache", `Excludes names containing "cache"`},
		{`a\b`, `A term containing \ matches the full path`},
	}},
	{title: "Fields", rows: []helpRow{
		{"ext:log", "Extension. ext:log;txt for several, ext:none for no extension"},
		{"size:>500MB", "Size comparison: > >= < <=. A range 1GB..4GB, or size:1GB (at least)"},
		{"min:1GiB max:2GiB", "Minimum and maximum size"},
		{"modified:<2025-01-01", "Before a date. modified:> is after it, modified:2025-03-14 one day, modified:2025-01-01..2025-02-01 a range"},
		{"after:2025-01-01", "Modified on or after a date"},
		{"before:2025-12-31", "Modified on or before a date, that day included"},
		{"path:node_modules", "Any part of the full path (wildcards allowed)"},
		{"name:backup", "Same as a bare term"},
		{"type:file", "Files only. type:folder for folders"},
		{`regex:^IMG_\d+\.jpe?g$`, "Go RE2 regular expression on the name (linear time)"},
	}},
	{note: "Units: B, KB, MB, GB, TB (powers of 1000) and KiB, MiB, GiB, TiB (powers of 1024).\nDates are local calendar dates (YYYY-MM-DD). Entries without a modification time never match date filters."},
}

// CLIUsage is the command line reference as plain text (-help and Copy).
var CLIUsage = helpText(cliHelp)

// helpTermWidth is the width of the term column in a table, in theme units.
const helpTermWidth = 52

// helpBody lays out help sections: tables and stacked terms, prose in the
// interface font and preformatted text in the monospace font.
func helpBody(c *ui.Context, sections []helpSection) {
	t := c.Theme()
	ui.Column(c).Gap(18).Children(func() {
		for _, s := range sections {
			ui.Column(c).Gap(6).Children(func() {
				if s.title != "" {
					ui.Text(c, s.title).Bold()
				}
				for _, r := range s.rows {
					if s.stacked {
						ui.Column(c).Gap(2).Children(func() {
							ui.Text(c, r.term).Font("Consolas").Selectable()
							if r.desc != "" {
								ui.Text(c, r.desc).TextColor(t.TextMuted).Selectable()
							}
						})
						continue
					}
					ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
						ui.Text(c, r.term).Font("Consolas").Selectable().Width(t.Space(helpTermWidth))
						ui.Text(c, r.desc).Selectable().Grow(1).MinWidth(0)
					})
				}
				if s.note != "" {
					ui.Text(c, s.note).TextColor(t.TextMuted).Selectable()
				}
			})
		}
	})
}

// helpText renders sections as plain console text, wrapped to 78 columns
// with hanging indents so wrapped lines stay under their term.
func helpText(sections []helpSection) string {
	const width = 78
	var lines []string
	for i, s := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		if s.title != "" {
			lines = append(lines, s.title+":")
		}
		term := 0
		for _, r := range s.rows {
			term = max(term, len(r.term))
		}
		for j, r := range s.rows {
			switch {
			case s.stacked:
				if j > 0 {
					lines = append(lines, "")
				}
				for k, l := range wrapWords(r.term, width-2) {
					indent := "    "
					if k == 0 {
						indent = "  "
					}
					lines = append(lines, indent+l)
				}
				for _, l := range wrapWords(r.desc, width-6) {
					lines = append(lines, "      "+l)
				}
			default:
				desc := wrapWords(r.desc, width-term-4)
				if len(desc) == 0 {
					desc = []string{""}
				}
				for j, l := range desc {
					if j == 0 {
						lines = append(lines, strings.TrimRight(fmt.Sprintf("  %-*s  %s", term, r.term, l), " "))
					} else {
						lines = append(lines, strings.Repeat(" ", term+4)+l)
					}
				}
			}
		}
		if s.note != "" {
			lines = append(lines, wrapWords(s.note, width)...)
		}
	}
	return strings.Join(lines, "\n")
}

// wrapWords breaks s at spaces into lines of at most width bytes. A single
// word longer than width stays whole.
func wrapWords(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// shortcutHelp lists the keyboard shortcuts from the command registry, so the
// dialog always shows the keys that work.
func (a *App) shortcutHelp() []helpSection {
	order := []string{"edit.find", "tools.search", "scan.folder", "scan.rescan", "scan.stop", "view.tree", "view.files", "view.ext", "nav.back", "nav.forward", "nav.up", "nav.root", "item.open", "item.goInto", "item.explorer", "item.properties", "item.reveal", "edit.copy", "edit.cut", "edit.copyPath", "edit.copyRows", "edit.selectAll", "edit.rename", "edit.delete", "edit.deletePerm", "export.view", "file.saveScan", "tools.filter", "tree.expandAll", "tree.collapseSub", "tree.expandLevel", "tree.collapseLvl", "tree.collapseAll", "view.treemap", "view.maxTreemap", "zoom.in", "zoom.out", "zoom.reset", "file.newWindow", "help.keys"}
	keys := helpSection{}
	for _, id := range order {
		cmd := a.cmds[id]
		keys.rows = append(keys.rows, helpRow{cmd.keys, strings.TrimSuffix(cmd.label, "…")})
	}
	mouse := helpSection{title: "Tree View and mouse", rows: []helpRow{
		{"Right", "Expand a folder, or move to its first child"},
		{"Left", "Collapse a folder, or move to its parent"},
		{"Enter, double-click", "Expand a folder or open a file"},
		{"Backspace", "Go up one level"},
		{"Ctrl+wheel", "Change the text size of the lists"},
		{"Mouse back, forward", "Go back and forward"},
		{"Right-click a header", "Show, hide or fit columns"},
	}}
	return []helpSection{keys, mouse}
}
