package desktop

import (
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

// The report is one self-contained HTML page summarizing a scan, to share
// with someone who does not have Quarry. Privacy options remove user names
// and every file and folder name before anything is written.

type reportDialog struct {
	open        bool
	scope       index.ID
	redactUsers bool
	hideNames   bool
	files       bool // include the largest files
}

func (a *App) openReport() {
	scope := a.insightScope()
	if a.prefs.View != viewTree {
		scope = a.files.scope
	}
	a.report = reportDialog{open: true, scope: scope, redactUsers: true, files: true}
}

// reportPrivacy rewrites paths for the report.
type reportPrivacy struct {
	users bool
	names bool
	root  string
	who   []string // user names to replace wherever they appear
}

var usersFolder = regexp.MustCompile(`(?i)(\\(?:Users|Documents and Settings)\\)([^\\]+)`)

func newReportPrivacy(root string, users, names bool) reportPrivacy {
	p := reportPrivacy{users: users, names: names, root: root}
	if users {
		for _, k := range []string{"USERNAME", "USER"} {
			if v := os.Getenv(k); len(v) >= 3 {
				p.who = append(p.who, v)
			}
		}
	}
	return p
}

func (p reportPrivacy) path(path string) string {
	if p.names {
		root := strings.TrimSuffix(p.root, `\`)
		rest := path
		if len(path) >= len(root) && strings.EqualFold(path[:len(root)], root) {
			rest = path[len(root):]
		} else {
			root = ""
		}
		parts := strings.Split(strings.Trim(rest, `\`), `\`)
		for i, part := range parts {
			if part == "" {
				continue
			}
			ext := ""
			if i == len(parts)-1 {
				ext = filepath.Ext(part)
			}
			parts[i] = "•••" + ext
		}
		path = root
		if len(parts) > 0 && parts[0] != "" {
			path += `\` + strings.Join(parts, `\`)
		}
		if path == "" {
			path = `\`
		}
	}
	if p.users {
		path = usersFolder.ReplaceAllString(path, "${1}user")
		for _, w := range p.who {
			path = replaceFold(path, w, "user")
		}
	}
	return path
}

// replaceFold replaces old in s ignoring case.
func replaceFold(s, old, repl string) string {
	if old == "" {
		return s
	}
	var b strings.Builder
	lower, lowOld := strings.ToLower(s), strings.ToLower(old)
	if len(lower) != len(s) {
		return strings.ReplaceAll(s, old, repl)
	}
	for {
		i := strings.Index(lower, lowOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

// writeReport writes the report of scope.
func writeReport(ctx context.Context, w io.Writer, s *index.Snapshot, scope index.ID, allocated, files bool, priv reportPrivacy, size func(uint64) string, now time.Time) error {
	d, err := computeInsights(ctx, s, scope, allocated, now, 25)
	if err != nil {
		return err
	}
	an := d.analysis
	metric := func(n index.Node) uint64 {
		if allocated {
			return n.Allocated
		}
		return n.Size
	}
	esc := html.EscapeString
	total := max(1, metric(s.Nodes[scope]))
	var b strings.Builder
	bar := func(v, of uint64) string {
		return fmt.Sprintf(`<span class="bar"><span style="width:%.1f%%"></span></span>`, 100*float64(v)/float64(max(1, of)))
	}
	title := priv.path(s.Path(scope))
	fmt.Fprintf(&b, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Storage report: %s</title><style>
:root{--bg:#fff;--fg:#1b1b1b;--muted:#666;--line:#e3e3e3;--accent:#0f6cbd;--warn:#b35c00}
@media (prefers-color-scheme:dark){:root{--bg:#161616;--fg:#e8e8e8;--muted:#9a9a9a;--line:#333;--accent:#4aa3f0;--warn:#f0a040}}
body{background:var(--bg);color:var(--fg);font:14px/1.45 "Segoe UI",system-ui,sans-serif;margin:0 auto;max-width:960px;padding:24px 16px}
h1{font-size:20px;margin:0 0 4px}h2{font-size:15px;margin:28px 0 8px}p.meta{color:var(--muted);margin:0}
table{border-collapse:collapse;width:100%%}td,th{padding:4px 8px;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}
th{color:var(--muted);font-weight:600}td.n{text-align:right;white-space:nowrap}td.p{word-break:break-all}
.bar{display:inline-block;width:120px;height:8px;background:var(--line);border-radius:2px;vertical-align:middle}.bar span{display:block;height:8px;background:var(--accent);border-radius:2px}
.facts{display:flex;flex-wrap:wrap;gap:8px 28px}.facts div span{color:var(--muted);margin-right:6px}.stale{color:var(--warn)}
footer{color:var(--muted);font-size:12px;margin-top:32px}</style></head><body>`, esc(title))
	fmt.Fprintf(&b, `<h1>Storage report: %s</h1><p class="meta">Scanned %s · %s · sizes by %s</p>`, esc(title), esc(platform.FormatDateTime(s.Started)), esc(engineName(s)), map[bool]string{true: "allocated size (disk space)", false: "logical size"}[allocated])
	n := s.Nodes[scope]
	fmt.Fprintf(&b, `<h2>Summary</h2><div class="facts"><div><span>Size</span>%s</div><div><span>Allocated</span>%s</div><div><span>Files</span>%s</div><div><span>Folders</span>%s</div>`, size(n.Size), size(n.Allocated), count(uint64(n.Files)), count(uint64(n.Dirs)))
	if d.old.Stale.Files > 0 {
		fmt.Fprintf(&b, `<div><span>Large files unchanged for a year</span>%s (%s)</div>`, size(d.old.Stale.Bytes), count(d.old.Stale.Files))
	}
	if d.dupWaste > 0 {
		fmt.Fprintf(&b, `<div><span>Possible duplicates by name and size</span>%s</div>`, size(d.dupWaste))
	}
	if s.Errors > 0 {
		fmt.Fprintf(&b, `<div><span>Unreadable items</span>%s</div>`, count(s.Errors))
	}
	b.WriteString(`</div>`)

	if len(an.Folders) > 0 {
		b.WriteString(`<h2>Largest folders</h2><table><tr><th>Folder</th><th>Size</th><th>Share</th><th>Files</th></tr>`)
		for _, id := range an.Folders {
			m := s.Nodes[id]
			fmt.Fprintf(&b, `<tr><td class="p">%s</td><td class="n">%s</td><td>%s %s</td><td class="n">%s</td></tr>`, esc(priv.path(s.Path(id))), size(metric(m)), bar(metric(m), total), percentText(percent(metric(m), total)), count(uint64(m.Files)))
		}
		b.WriteString(`</table>`)
	}
	if files && len(an.TopFiles) > 0 {
		b.WriteString(`<h2>Largest files</h2><table><tr><th>File</th><th>Size</th><th>Modified</th></tr>`)
		for _, id := range an.TopFiles {
			m := s.Nodes[id]
			fmt.Fprintf(&b, `<tr><td class="p">%s</td><td class="n">%s</td><td class="n">%s</td></tr>`, esc(priv.path(s.Path(id))), size(metric(m)), esc(dateText(m.ModTime())))
		}
		b.WriteString(`</table>`)
	}
	if len(d.categories) > 0 {
		var ctotal uint64
		for _, c := range d.categories {
			ctotal += c.Bytes
		}
		b.WriteString(`<h2>By kind of file</h2><table><tr><th>Category</th><th>Size</th><th>Share</th><th>Files</th></tr>`)
		for _, c := range d.categories {
			fmt.Fprintf(&b, `<tr><td>%s</td><td class="n">%s</td><td>%s %s</td><td class="n">%s</td></tr>`, esc(c.Name), size(c.Bytes), bar(c.Bytes, ctotal), percentText(percent(c.Bytes, max(1, ctotal))), count(c.Count))
		}
		b.WriteString(`</table>`)
	}
	if len(d.all.Ranges) > 0 {
		b.WriteString(`<h2>By time since last change</h2><table><tr><th>Last changed</th><th>Size</th><th>Share</th><th>Files</th></tr>`)
		at := max(1, d.all.Total.Bytes)
		for i, r := range index.AgeRanges {
			t := d.all.Ranges[i]
			cls := ""
			if r.Stale {
				cls = ` class="stale"`
			}
			fmt.Fprintf(&b, `<tr><td%s>%s</td><td class="n">%s</td><td>%s %s</td><td class="n">%s</td></tr>`, cls, esc(r.Label), size(t.Bytes), bar(t.Bytes, at), percentText(percent(t.Bytes, at)), count(t.Files))
		}
		b.WriteString(`</table>`)
	}
	note := "Generated by Quarry " + Version + " on " + platform.FormatDateTime(now) + "."
	switch {
	case priv.names:
		note += " File and folder names are hidden."
	case priv.users:
		note += " User names are replaced."
	}
	fmt.Fprintf(&b, `<footer>%s</footer></body></html>`, esc(note))
	_, err = io.WriteString(w, b.String())
	return err
}

func (a *App) reportDialog(c *ui.Context) {
	d := &a.report
	t := c.Theme()
	if d.open && (a.snap == nil || !a.snap.Reachable(d.scope)) {
		d.scope = 0
		if a.snap == nil {
			d.open = false
		}
	}
	ui.Modal(c.Key("report"), &d.open, func() {
		ui.Column(c).Width(520).Padding(8).Gap(8).Children(func() {
			dialogTitle(c, "Export Report (HTML)")
			ui.Text(c, "A single web page with totals, the largest folders and files, kinds of files and their age, for sharing with someone who does not have Quarry.").TextColor(t.TextMuted).MaxWidth(490)
			infoField(c, "Folder", truncateMiddle(a.pathOf(d.scope), 60))
			ui.Checkbox(c, &d.files, "Include the largest files")
			ui.Checkbox(c, &d.redactUsers, "Replace user names in paths")
			ui.Checkbox(c, &d.hideNames, "Hide every file and folder name (keep sizes and extensions)")
			ui.Text(c, "Nothing leaves this computer: the report is saved where you choose.").FontSize(11).TextColor(t.TextMuted)
			buttons(c, func() {
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Save Report…").Clicked() {
					d.open = false
					s, scope, allocated, files := a.snap, d.scope, a.metricAllocated(), d.files
					priv := newReportPrivacy(s.Root, d.redactUsers, d.hideNames)
					size := a.sizeFunc()
					name := a.baseName() + " report.html"
					if d.hideNames || d.redactUsers {
						name = "Storage report.html"
					}
					a.saveAs("Export Report", name, "html", "Web page", func(w io.Writer) error {
						return writeReport(context.Background(), w, s, scope, allocated, files, priv, size, time.Now())
					})
				}
			})
		})
	})
}

// sizeFunc formats sizes in the units chosen now, for a worker.
func (a *App) sizeFunc() func(uint64) string {
	if a.prefs.Units == "Decimal" {
		return decimalBytes
	}
	return binaryBytes
}
