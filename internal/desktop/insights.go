package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
)

// Insights are computed on demand for one folder (or the whole scan) and
// shown in a dialog that goes away when closed: a few facts worth acting
// on, file age and categories. Every number leads to the files behind it
// in File View, where the usual commands apply.

const (
	insightsOverview = iota
	insightsAge
	insightsCategories
)

// ageMinimums are the sizes the age table can be limited to.
var ageMinimums = []string{"All files", "1 MB or larger", "100 MB or larger", "1 GB or larger"}

func ageMinimum(label string) (uint64, string) {
	switch label {
	case ageMinimums[1]:
		return 1 << 20, "1MB"
	case ageMinimums[2]:
		return 100 << 20, "100MB"
	case ageMinimums[3]:
		return 1 << 30, "1GB"
	}
	return 0, ""
}

type insightOverview struct {
	node        index.Node
	folders     []index.ID // largest folders directly inside
	topFiles    []index.ID // largest files anywhere below
	dominant    index.CategoryTotal
	categories  []index.CategoryTotal
	old         index.AgeProfile // files of 100 MB or more, by age
	all         index.AgeProfile // every file, by age
	dupGroups   int
	dupWaste    uint64 // bytes beyond one copy, by name and size
	temporary   index.CategoryTotal
	issues      int // unreadable items under the folder, from the sample
	newestFile  int64
	computedFor *index.Snapshot
	analysis    index.Analysis // untrimmed: the report lists more
}

type insightsView struct {
	open   bool
	scope  index.ID
	tab    int
	gen    uint64
	cancel context.CancelFunc
	busy   bool
	err    string

	data *insightOverview

	minSize string
	age     *index.AgeProfile
	ageBusy bool
	ageGen  uint64

	ageList, catList     ui.ListState
	ageCursor, catCursor int
}

// insightScope is the folder the insight commands look at: the selected
// folder, or the current location.
func (a *App) insightScope() index.ID {
	if a.snap == nil {
		return 0
	}
	if id := a.current(); id != index.None && a.snap.Nodes[id].IsDir() && a.prefs.View == viewTree {
		return id
	}
	return a.location
}

// openInsights shows the insights of scope, computing them in the
// background.
func (a *App) openInsights(scope index.ID, tab int) {
	if a.snap == nil {
		return
	}
	if !a.snap.Reachable(scope) || !a.snap.Nodes[scope].IsDir() {
		scope = 0
	}
	v := &a.insights
	if v.cancel != nil {
		v.cancel()
	}
	minSize := v.minSize
	if minSize == "" {
		minSize = ageMinimums[0]
	}
	*v = insightsView{open: true, scope: scope, tab: tab, gen: v.gen + 1, ageGen: v.ageGen + 1, minSize: minSize, ageCursor: -1, catCursor: -1}
	gen, s, allocated := v.gen, a.snap, a.metricAllocated()
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel, v.busy = cancel, true
	go func() {
		d, err := computeInsights(ctx, s, scope, allocated, now, 5)
		a.post(func() {
			if v.gen != gen {
				return
			}
			v.busy, v.cancel = false, nil
			if err != nil {
				if ctx.Err() == nil {
					v.err = err.Error()
				}
				return
			}
			v.data = d
			if v.age == nil && v.minSize == ageMinimums[0] {
				all := d.all
				v.age = &all
			}
		})
	}()
	if minSize != ageMinimums[0] {
		a.computeAge()
	}
}

// computeInsights gathers the facts of one folder, with the top largest
// files. Every part is a walk over the index that is already in memory, on
// several cores; no file is opened. There are three walks: the extension
// totals with the largest files, the age bands at two minimum sizes
// together, and the same-name-and-size candidates.
func computeInsights(ctx context.Context, s *index.Snapshot, scope index.ID, allocated bool, now time.Time, top int) (*insightOverview, error) {
	d := &insightOverview{node: s.Nodes[scope], computedFor: s}
	an, err := s.Analyze(ctx, scope, allocated, max(top, 5))
	if err != nil {
		return nil, err
	}
	d.analysis = an
	d.folders = an.Folders
	if len(d.folders) > 5 {
		d.folders = d.folders[:5]
	}
	d.topFiles = an.TopFiles
	if len(d.topFiles) > 5 {
		d.topFiles = d.topFiles[:5]
	}
	d.categories = index.SummarizeCategories(an.Types, allocated)
	for _, c := range d.categories {
		if c.Key != "" && (d.dominant.Name == "" || c.Bytes > d.dominant.Bytes) {
			d.dominant = c
		}
		if c.Key == "temporary" {
			d.temporary = c
		}
	}
	ages, err := s.AgeProfiles(ctx, scope, now, 0, 100<<20)
	if err != nil {
		return nil, err
	}
	d.all, d.old = ages[0], ages[1]
	dups, err := s.DuplicateCandidatesOver(ctx, scope, 1<<20)
	if err != nil {
		return nil, err
	}
	d.dupGroups = len(dups.Groups)
	for _, g := range dups.Groups {
		if len(g.IDs) > 1 {
			d.dupWaste += uint64(len(g.IDs)-1) * g.Size
		}
	}
	if scope == 0 {
		d.issues = len(s.Issues)
	} else {
		prefix := strings.ToLower(strings.TrimSuffix(s.Path(scope), `\`)) + `\`
		for _, is := range s.Issues {
			if strings.HasPrefix(strings.ToLower(is.Path), prefix) {
				d.issues++
			}
		}
	}
	return d, nil
}

// computeAge recomputes the age table for the chosen minimum size.
func (a *App) computeAge() {
	v := &a.insights
	if a.snap == nil {
		return
	}
	v.ageGen++
	gen, s, scope := v.ageGen, a.snap, v.scope
	minSize, _ := ageMinimum(v.minSize)
	v.ageBusy = true
	go func() {
		p, err := s.AgeProfile(context.Background(), scope, time.Now(), minSize)
		a.post(func() {
			if v.ageGen != gen {
				return
			}
			v.ageBusy = false
			if err == nil {
				v.age = &p
			}
		})
	}()
}

// insightQuery lists the files behind a number and closes the dialog.
func (a *App) insightQuery(query string) {
	v := &a.insights
	v.open = false
	a.showQuery(query, v.scope)
}

func (a *App) insightsDialog(c *ui.Context) {
	v := &a.insights
	if !v.open {
		if v.cancel != nil {
			v.cancel()
			v.cancel = nil
		}
		return
	}
	if a.snap == nil || !a.snap.Reachable(v.scope) {
		v.open = false
		return
	}
	t := c.Theme()
	a.modal(c.Key("insights"), &v.open, func() {
		ui.Column(c).Width(720).MaxWidthPercent(100).Height(560).MaxHeightPercent(100).Padding(8).Gap(8).Children(func() {
			title := "Storage Insights"
			if v.scope != 0 {
				title = "Folder Details"
			}
			dialogTitle(c, title)
			path := a.pathOf(v.scope)
			if path == "" {
				path = a.name(v.scope)
			}
			ui.Text(c, path).TextColor(t.TextMuted).SingleLine().MinWidth(0).Tooltip(path)
			ui.Tabs(c, &v.tab, "Overview", "File Age", "Categories").Label("Insights")
			switch {
			case v.err != "":
				ui.Text(c, v.err).TextColor(t.Danger)
			case v.busy && v.data == nil:
				ui.Text(c, "Looking through the scan…").TextColor(t.TextMuted)
			default:
				switch v.tab {
				case insightsOverview:
					a.insightsOverview(c)
				case insightsAge:
					a.insightsAge(c)
				case insightsCategories:
					a.insightsCategories(c)
				}
			}
			if v.data == nil || v.err != "" {
				ui.Spacer(c) // the tabs fill the dialog themselves
			}
			buttons(c, func() {
				if v.scope != 0 && ui.Button(c, "Reveal in Tree").Clicked() {
					v.open = false
					a.reveal(v.scope)
				}
				if ui.PrimaryButton(c, "Close").Clicked() {
					v.open = false
				}
			})
		})
	})
}

// insightLine is one fact, with a link to what is behind it.
func (a *App) insightLine(c *ui.Context, key, label, value, link string, act func()) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(24).Children(func() {
		ui.Text(c, label).Width(190).TextColor(t.TextMuted).SingleLine().Shrink(0)
		ui.Text(c, value).SingleLine().MinWidth(0).Shrink(1)
		if link != "" && act != nil {
			ui.Spacer(c)
			if linkButton(c, "insight-"+key, link, "Open them in File View") {
				a.post(act)
			}
		}
	})
}

// listing returns an action listing what query matches in the insights'
// folder.
func (a *App) listing(query string) func() {
	return func() { a.insightQuery(query) }
}

// findDuplicatesHere lists duplicate candidates in the insights' folder.
func (a *App) findDuplicatesHere() {
	v := &a.insights
	v.open = false
	scope := v.scope
	a.search = ""
	if scope == a.location {
		a.files.followLocation()
	} else {
		a.files.pinScope(scope)
	}
	a.showDuplicates(true)
}

func (a *App) insightsOverview(c *ui.Context) {
	v := &a.insights
	d := v.data
	t := c.Theme()
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			n := d.node
			a.insightLine(c, "size", "Size", fmt.Sprintf("%s (%s allocated)", a.size(n.Size), a.size(n.Allocated)), "", nil)
			a.insightLine(c, "count", "Contents", plural(uint64(n.Files), "file", "files")+" in "+plural(uint64(n.Dirs), "folder", "folders"), "List All Files", a.listing("type:file"))
			if len(d.folders) > 0 {
				f := d.folders[0]
				a.insightLine(c, "child", "Largest folder inside", fmt.Sprintf("%s · %s (%s)", a.name(f), a.size(a.metric(a.snap.Nodes[f])), percentText(percent(a.metric(a.snap.Nodes[f]), max(1, a.metric(n))))), "", nil)
			}
			if d.dominant.Name != "" {
				a.insightLine(c, "dominant", "Mostly", fmt.Sprintf("%s · %s in %s", d.dominant.Name, a.size(d.dominant.Bytes), plural(d.dominant.Count, "file", "files")), "Show", a.listing("cat:"+d.dominant.Key+" type:file"))
			}
			if len(d.all.Ranges) > 0 {
				recent := d.all.Ranges[0]
				a.insightLine(c, "recent", "Changed in the last 30 days", fmt.Sprintf("%s · %s", plural(recent.Files, "file", "files"), a.size(recent.Bytes)), "Show", a.listing(index.AgeRanges[0].Query+" type:file"))
			}
			ui.Box(c).Height(1).Background(t.Border).Margin(6, 0)
			ui.Text(c, "Worth a look").FontSize(13).Margin(0, 0, 2, 0)
			any := false
			if d.old.Stale.Files > 0 {
				any = true
				a.insightLine(c, "old", "Large files not changed in a year", fmt.Sprintf("%s · %s", plural(d.old.Stale.Files, "file", "files"), a.size(d.old.Stale.Bytes)), "Show", a.listing("age:>1y "+a.sizeTerm(">=", 100, "MB")+" type:file"))
			}
			if d.dupWaste > 0 {
				any = true
				a.insightLine(c, "dups", "Possible duplicates (1 MB+)", fmt.Sprintf("%s in %s with the same name and size · contents not compared", a.size(d.dupWaste), plural(uint64(d.dupGroups), "group", "groups")), "Find Duplicates", a.findDuplicatesHere)
			}
			if d.temporary.Count > 0 && d.temporary.Bytes >= 1<<20 {
				any = true
				a.insightLine(c, "temp", "Logs and temporary files", fmt.Sprintf("%s · %s", plural(d.temporary.Count, "file", "files"), a.size(d.temporary.Bytes)), "Show", a.listing("cat:temporary type:file"))
			}
			for _, cat := range d.categories {
				if (cat.Key == "installers" || cat.Key == "diskimages" || cat.Key == "archives") && cat.Bytes >= 100<<20 {
					any = true
					a.insightLine(c, "cat-"+cat.Key, cat.Name, fmt.Sprintf("%s · %s, often kept after use", plural(cat.Count, "file", "files"), a.size(cat.Bytes)), "Show", a.listing("cat:"+cat.Key+" type:file"))
				}
			}
			if d.all.Cloud.Files > 0 {
				any = true
				a.insightLine(c, "cloud", "Online-only cloud files", fmt.Sprintf("%s · %s listed, taking almost no space on this drive", plural(d.all.Cloud.Files, "file", "files"), a.size(d.all.Cloud.Bytes)), "Show", a.listing("is:cloud"))
			}
			if d.issues > 0 {
				any = true
				a.insightLine(c, "issues", "Unreadable items", plural(uint64(d.issues), "item", "items")+" · totals may be low", "", nil)
				ui.Row(c).Gap(8).Children(func() {
					ui.Box(c).Width(190).Shrink(0)
					if linkButton(c, "insight-issues-open", "Open Scan Issues", "See which items could not be read and retry them") {
						a.post(func() { v.open = false; a.dlg.issues = true })
					}
				})
			}
			if !any {
				ui.Text(c, "Nothing stands out: no large old files, duplicates by name, or temporary files of note.").TextColor(t.TextMuted)
			}
			if len(d.topFiles) > 0 {
				ui.Box(c).Height(1).Background(t.Border).Margin(6, 0)
				ui.Text(c, "Largest files").FontSize(13).Margin(0, 0, 2, 0)
				for i, id := range d.topFiles {
					if int(id) >= len(a.snap.Nodes) || !a.snap.Reachable(id) {
						continue
					}
					id := id
					ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(22).Children(func() {
						ui.Text(c, a.size(a.metric(a.snap.Nodes[id]))).Width(90).SingleLine().Shrink(0)
						p := a.pathOf(id)
						ui.Text(c, p).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1).Tooltip(p)
						ui.Spacer(c)
						if linkButton(c, fmt.Sprintf("insight-file-%d", i), "Reveal", "Show it in Tree View") {
							a.post(func() { v.open = false; a.reveal(id) })
						}
					})
				}
			}
		})
	})
}

func (a *App) insightsAge(c *ui.Context) {
	v := &a.insights
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Count").TextColor(t.TextMuted)
		if ui.Select(c.Key("age-min"), &v.minSize, ageMinimums).Label("Minimum file size").Width(170).Changed() {
			a.computeAge()
		}
		ui.Spacer(c)
		if v.ageBusy {
			ui.Text(c, "Counting…").TextColor(t.TextMuted)
		}
	})
	p := v.age
	if p == nil {
		return
	}
	ui.Text(c, "By the time since each file last changed. Double-click a row to list its files.").TextColor(t.TextMuted).FontSize(11)
	type row struct {
		label string
		tot   index.AgeTotals
		query string
		stale bool
	}
	var rows []row
	for i, r := range index.AgeRanges {
		rows = append(rows, row{r.Label, p.Ranges[i], r.Query, r.Stale})
	}
	if p.Unknown.Files > 0 {
		rows = append(rows, row{"Unknown date", p.Unknown, "", false})
	}
	total := max(1, p.Total.Bytes)
	v.ageList.Selected = &v.ageCursor
	table := ui.Table(c.Key("age-table"), &v.ageList, []ui.TableColumn{{Title: "Last changed", MinWidth: 150}, {Title: "Files", Width: 90, Align: ui.End}, {Title: "Size", Width: 100, Align: ui.End}, {Title: "Share", Width: 200}}, len(rows), func(i, col int) {
		r := rows[i]
		switch col {
		case 0:
			txt := ui.Text(c, r.label).SingleLine()
			if r.stale {
				txt.TextColor(t.Warning).Tooltip("Not changed for over a year: often safe to archive or remove, but check first")
			}
		case 1:
			ui.Text(c, count(r.tot.Files)).SingleLine()
		case 2:
			ui.Text(c, a.size(r.tot.Bytes)).SingleLine()
		case 3:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				frac := float32(float64(r.tot.Bytes) / float64(total))
				ui.Box(c).Size(110, 8).Shrink(0).Draw(func(pt *ui.Painter, rc ui.Rect) {
					pt.Fill(rc, t.Border, 2)
					rc.W *= frac
					color := t.Focus
					if r.stale {
						color = t.Warning
					}
					pt.Fill(rc, color, 2)
				})
				ui.Text(c, percentText(percent(r.tot.Bytes, total))).TextColor(t.TextMuted).SingleLine()
			})
		}
	}).Grow(1).MinHeight(0).Border(1, t.Border)
	ui.Text(c, fmt.Sprintf("%s · %s in all. %s not changed for over a year (%s).", plural(p.Total.Files, "file", "files"), a.size(p.Total.Bytes), plural(p.Stale.Files, "file", "files"), a.size(p.Stale.Bytes))).TextColor(t.TextMuted).SingleLine().MinWidth(0)
	open := v.ageCursor >= 0 && v.ageCursor < len(rows) && rows[v.ageCursor].query != ""
	if table.Submitted() && open || open && ui.Button(c, "List These Files").Clicked() {
		r := rows[v.ageCursor]
		q := r.query
		if _, term := ageMinimum(v.minSize); term != "" {
			q += " size:>=" + term
		}
		a.post(func() { a.insightQuery(q + " type:file") })
	}
}

func (a *App) insightsCategories(c *ui.Context) {
	v := &a.insights
	t := c.Theme()
	cats := v.data.categories
	var total uint64
	for _, cat := range cats {
		total += cat.Bytes
	}
	total = max(1, total)
	ui.Text(c, "Files grouped by kind, from their extensions. Double-click a row to list its files.").TextColor(t.TextMuted).FontSize(11)
	v.catList.Selected = &v.catCursor
	table := ui.Table(c.Key("cat-table"), &v.catList, []ui.TableColumn{{Title: "Category", MinWidth: 150}, {Title: "Files", Width: 90, Align: ui.End}, {Title: "Size", Width: 100, Align: ui.End}, {Title: "Share", Width: 170}, {Title: "Mostly", MinWidth: 120}}, len(cats), func(i, col int) {
		cat := cats[i]
		switch col {
		case 0:
			ui.Text(c, cat.Name).SingleLine()
		case 1:
			ui.Text(c, count(cat.Count)).SingleLine()
		case 2:
			ui.Text(c, a.size(cat.Bytes)).SingleLine()
		case 3:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				frac := float32(float64(cat.Bytes) / float64(total))
				ui.Box(c).Size(90, 8).Shrink(0).Draw(func(pt *ui.Painter, rc ui.Rect) {
					pt.Fill(rc, t.Border, 2)
					rc.W *= frac
					pt.Fill(rc, t.Focus, 2)
				})
				ui.Text(c, percentText(percent(cat.Bytes, total))).TextColor(t.TextMuted).SingleLine()
			})
		case 4:
			top := cat.Top
			if len(top) > 4 {
				top = top[:4]
			}
			ui.Text(c, strings.Join(top, ", ")).TextColor(t.TextMuted).SingleLine().MinWidth(0).Shrink(1)
		}
	}).Grow(1).MinHeight(0).Border(1, t.Border)
	open := v.catCursor >= 0 && v.catCursor < len(cats)
	if table.Submitted() && open || open && ui.Button(c, "List These Files").Clicked() {
		cat := cats[v.catCursor]
		q := "cat:" + cat.Key + " type:file"
		if cat.Key == "" || cat.Name == index.Other {
			q = "cat:other type:file" // the extensions no category claims
		}
		a.post(func() { a.insightQuery(q) })
	}
}
