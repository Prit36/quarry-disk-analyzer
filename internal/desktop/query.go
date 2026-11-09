package desktop

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// showQuery lists what query matches in File View, looking in scope: the
// way every summary in Quarry (age bands, categories, insights, presets)
// leads to the files behind a number, with the usual selection and actions.
func (a *App) showQuery(query string, scope index.ID) {
	if a.snap == nil {
		return
	}
	if !a.snap.Reachable(scope) {
		scope = 0
	}
	fromTree := a.prefs.View == viewTree
	a.setDuplicateMode(false)
	a.search = query
	if scope == a.location {
		a.files.followLocation()
	} else {
		a.files.pinScope(scope)
	}
	a.ext.dirty = true
	a.setViewMode(viewFiles)
	a.searchLeftTree = fromTree || a.searchLeftTree
	a.files.invalidate()
	a.focusTable = true
}

// splitQuery splits a search into its terms, keeping quoted text whole.
func splitQuery(q string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// termField is a term's field name in lower case, "" for a plain term.
func termField(term string) string {
	term = strings.TrimPrefix(term, "-")
	field, _, ok := strings.Cut(term, ":")
	if !ok || len(field) == 1 && strings.HasPrefix(term[len(field)+1:], `\`) {
		return ""
	}
	return strings.ToLower(field)
}

// Search fields that the quick controls set, each group replacing its own.
var (
	sizeFields = []string{"size", "min", "max"}
	ageFields  = []string{"age", "modified", "date", "dm", "after", "before"}
	catFields  = []string{"cat", "category", "ext"}
)

func hasField(fields []string, f string) bool {
	for _, x := range fields {
		if x == f {
			return true
		}
	}
	return false
}

// queryTerms returns the terms of q in the fields group, joined.
func queryTerms(q string, fields []string) string {
	var out []string
	for _, t := range splitQuery(q) {
		if hasField(fields, termField(t)) {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

// withTerms returns q with every term of the fields group replaced by
// terms ("" removes them), keeping the other terms in their order.
func withTerms(q string, fields []string, terms string) string {
	var out []string
	for _, t := range splitQuery(q) {
		if !hasField(fields, termField(t)) {
			out = append(out, t)
		}
	}
	if terms = strings.TrimSpace(terms); terms != "" {
		out = append(out, terms)
	}
	return strings.Join(out, " ")
}

// sizeTerm writes a size in the units the user reads them in.
func (a *App) sizeTerm(op string, n int, unit string) string {
	if a.prefs.Units != "Decimal" && len(unit) == 2 {
		unit = unit[:1] + "iB"
	}
	return "size:" + op + itoa(n) + unit
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

type queryPreset struct{ label, query string }

// presets are one-click searches. They respect the current scope, as the
// search box does.
func (a *App) presets() []queryPreset {
	return []queryPreset{
		{"Files Over 100 MB", a.sizeTerm(">", 100, "MB") + " type:file"},
		{"Files Over 1 GB", a.sizeTerm(">", 1, "GB") + " type:file"},
		{"Files Over 10 GB", a.sizeTerm(">", 10, "GB") + " type:file"},
		{"Large Files Not Modified in a Year", "age:>1y " + a.sizeTerm(">", 500, "MB") + " type:file"},
		{"Old Downloads", "path:Downloads age:>90d type:file"},
		{"Recently Modified (7 Days)", "age:<7d type:file"},
		{"Large Archives", "cat:archives " + a.sizeTerm(">", 100, "MB")},
		{"Installers and Disk Images", "cat:installers;diskimages"},
		{"Videos", "cat:videos"},
		{"Logs and Temporary Files", "cat:temporary"},
		{"Online-Only (Cloud) Files", "is:cloud"},
	}
}

// presetItems adds the quick filters to a menu.
func (a *App) presetItems(m *ui.Menu) {
	for _, p := range a.presets() {
		if m.Item(p.label).Checked(strings.TrimSpace(a.search) == p.query).Disabled(a.snap == nil).Chosen() {
			q := p.query
			a.post(func() { a.showQuery(q, a.files.scope) })
		}
	}
}

// setQueryGroup applies a quick control: the search with that group's
// terms replaced, run in File View.
func (a *App) setQueryGroup(fields []string, terms string) {
	q := withTerms(a.search, fields, terms)
	if strings.TrimSpace(q) == "" {
		a.search = ""
		a.searchCleared(time.Now())
		a.files.invalidate()
		return
	}
	a.showQuery(q, a.files.scope)
}

// filterChips is the Filters button beside the view tabs in File View:
// size, age and category without learning the query syntax. It writes
// ordinary search terms, so the search box shows what it did.
func (a *App) filterChips(c *ui.Context) {
	t := c.Theme()
	// The count changes only with the search text; splitting it three
	// times on every frame is avoidable.
	ch := &a.chips
	if !ch.valid || ch.search != a.search {
		ch.search, ch.valid, ch.active = a.search, true, 0
		for _, g := range [][]string{sizeFields, ageFields, catFields} {
			if queryTerms(a.search, g) != "" {
				ch.active++
			}
		}
		ch.label = "Filters"
		if ch.active > 0 {
			ch.label += " · " + itoa(ch.active)
		}
	}
	active, label := ch.active, ch.label
	b := ui.ButtonBase(c.Key("filter-chips")).Height(22).Padding(0, 6).Radius(4).Border(1, t.Border).AlignItems(ui.Center).Gap(4).Shrink(0).Label("Filters").Tooltip("Narrow the list by size, age or kind of file. These add ordinary search terms.")
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		txt := ui.Text(c, label).SingleLine()
		if active > 0 {
			txt.TextColor(t.Focus)
		}
		ui.Icon(c, icons["chevron_down"]).FontSize(10).TextColor(t.TextMuted)
	}).Menu(func(m *ui.Menu) {
		group := func(title string, fields []string, options []queryPreset) {
			current := queryTerms(a.search, fields)
			m.Submenu(title, func(sm *ui.Menu) {
				for _, o := range options {
					if sm.Item(o.label).Checked(current == o.query).Chosen() {
						q := o.query
						a.post(func() { a.setQueryGroup(fields, q) })
					}
				}
			})
		}
		group("Size", sizeFields, []queryPreset{
			{"Any Size", ""},
			{"Over 1 MB", a.sizeTerm(">", 1, "MB")},
			{"Over 100 MB", a.sizeTerm(">", 100, "MB")},
			{"Over 1 GB", a.sizeTerm(">", 1, "GB")},
			{"Over 10 GB", a.sizeTerm(">", 10, "GB")},
			{"Under 1 MB", a.sizeTerm("<", 1, "MB")},
		})
		group("Modified", ageFields, []queryPreset{
			{"Any Time", ""},
			{"In the Last 7 Days", "age:<7d"},
			{"In the Last 30 Days", "age:<30d"},
			{"Over 90 Days Ago", "age:>90d"},
			{"Over a Year Ago", "age:>1y"},
			{"Over 3 Years Ago", "age:>3y"},
		})
		kinds := []queryPreset{{"Any Kind", ""}}
		for _, cat := range index.Categories {
			kinds = append(kinds, queryPreset{cat.Name, "cat:" + cat.Key})
		}
		group("Kind", catFields, kinds)
		m.Separator()
		m.Submenu("Quick Filters", a.presetItems)
		m.Separator()
		if m.Item("Clear Filters").Disabled(active == 0).Chosen() {
			a.post(func() {
				q := withTerms(withTerms(withTerms(a.search, sizeFields, ""), ageFields, ""), catFields, "")
				a.search = q
				if strings.TrimSpace(q) == "" {
					a.searchCleared(time.Now())
				}
				a.files.invalidate()
			})
		}
	})
}

// Operator suggestions while typing in the search box.

type operatorHint struct{ text, hint string }

var searchOperators = []operatorHint{
	{"ext:", "extension, e.g. ext:mp4;mkv"},
	{"size:>", "size, e.g. size:>1GB or size:100MB..1GB"},
	{"age:>", "time since last change, e.g. age:>1y or age:<30d"},
	{"cat:", "kind of file, e.g. cat:videos"},
	{"modified:<", "changed before a date, e.g. modified:<2025-01-01"},
	{"path:", "any folder in the path, e.g. path:Downloads"},
	{"type:file", "files only"},
	{"type:folder", "folders only"},
	{"is:cloud", "online-only cloud files"},
	{"min:", "at least a size, e.g. min:500MB"},
	{"max:", "at most a size, e.g. max:4GB"},
	{"after:", "changed on or after a date"},
	{"before:", "changed on or before a date"},
	{"name:", "names containing a word"},
	{"regex:", "regular expression on the name"},
}

// valueHints complete a field typed up to its colon.
var valueHints = map[string][]operatorHint{
	"age:":  {{"age:>1y", "not changed for over a year"}, {"age:>90d", "not changed for 90 days"}, {"age:<30d", "changed in the last 30 days"}, {"age:<7d", "changed this week"}},
	"size:": {{"size:>100MB", "larger than 100 MB"}, {"size:>1GB", "larger than 1 GB"}, {"size:<1MB", "smaller than 1 MB"}, {"size:1GB..4GB", "a range"}},
	"type:": {{"type:file", "files only"}, {"type:folder", "folders only"}},
	"is:":   {{"is:cloud", "online-only cloud files"}, {"is:hardlink", "additional hard-link names"}, {"is:incomplete", "folders not read completely"}},
	"cat:": func() []operatorHint {
		var out []operatorHint
		for _, c := range index.Categories {
			out = append(out, operatorHint{"cat:" + c.Key, c.Name})
		}
		return out
	}(),
}

// operatorSuggestions returns what the last, unfinished term of q may
// become. Plain words of a single letter suggest nothing, so ordinary
// searches are not interrupted.
func operatorSuggestions(q string) []operatorHint {
	if q == "" || strings.HasSuffix(q, " ") {
		return nil
	}
	start := strings.LastIndexAny(q, " \t") + 1
	last := q[start:]
	negate := strings.HasPrefix(last, "-")
	word := strings.ToLower(strings.TrimPrefix(last, "-"))
	if hints, ok := valueHints[word]; ok && !negate {
		return hints
	}
	if len(word) < 2 || strings.ContainsAny(word, `:\"*?`) {
		return nil
	}
	var out []operatorHint
	for _, o := range searchOperators {
		if strings.HasPrefix(o.text, word) && o.text != word {
			out = append(out, o)
		}
	}
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// completeOperator replaces the last term of q with the suggestion.
func completeOperator(q string, s operatorHint) string {
	start := strings.LastIndexAny(q, " \t") + 1
	return q[:start] + s.text
}

// searchSuggestions shows operator completions under the search box while
// it has the focus. Tab takes the first; a click takes any. The rows keep
// the focus in the box, so typing simply continues.
func (a *App) searchSuggestions(c *ui.Context, box, field ui.Element) {
	if !field.Focused() || a.prefs.View == viewExtensions {
		return
	}
	hints := operatorSuggestions(a.search)
	if len(hints) == 0 {
		return
	}
	take := func(h operatorHint) {
		a.search = completeOperator(a.search, h)
		a.searchCaretEnd = true
		a.files.queryChanged(c.Now())
		c.After(160 * time.Millisecond)
		if a.prefs.View == viewTree && !strings.HasSuffix(h.text, ":") && !strings.HasSuffix(h.text, ">") && !strings.HasSuffix(h.text, "<") {
			a.setDuplicateMode(false)
			a.setViewMode(viewFiles)
			a.searchLeftTree = true
		}
	}
	if field.Shortcut(0, ui.KeyTab) {
		take(hints[0])
		return
	}
	t := c.Theme()
	ui.Overlay(c, func() {
		panel := ui.Column(c.Key("search-suggest")).AttachTo(box, ui.AnchorBottomStart, ui.AnchorTopStart).Width(360).Padding(4).Gap(0).Radius(6).Background(t.Surface).Border(1, t.Border).Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.25)).KeepFocus()
		panel.Margin(2, 0, 0, 0)
		panel.Children(func() {
			for i, h := range hints {
				row := ui.ButtonBase(c.Key("suggest-"+h.text)).Padding(4, 8).Radius(4).Gap(10).AlignItems(ui.Center).KeepFocus().Label(h.text + ", " + h.hint)
				if row.Hovered() || i == 0 && !row.Hovered() {
					if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
				}
				row.Children(func() {
					ui.Text(c, h.text).Font("Consolas").SingleLine().Shrink(0)
					ui.Text(c, h.hint).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
					if i == 0 {
						ui.Spacer(c)
						ui.Text(c, "Tab").FontSize(t.FontSize - 1).TextColor(t.TextMuted).Shrink(0)
					}
				})
				if row.Clicked() {
					take(h)
				}
			}
		})
	})
}

// chipCache is the Filters button's count of active groups and its label
// for one search text.
type chipCache struct {
	search, label string
	active        int
	valid         bool
}
