package desktop

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// prefs is everything Quarry remembers between runs. Window bounds and
// state are kept by mygo (WindowOptions.StateKey).
type prefs struct {
	Version int

	View          int    // 0 tree, 1 files, 2 extensions
	Metric        string // "Logical" or "Allocated"
	Units         string // "Binary" or "Decimal"
	Theme         string // "System", "Dark", "Light"
	Compact       bool
	Zoom          float32
	ShowInfo      bool
	ShowTreemap   bool
	TreemapPct    float32 // height share of the table above the treemap
	TreemapColors string  // "Extension", "Folder", "Mono"
	ShowTypes     bool    // file types panel beside Tree View
	TypesPct      float32 // width share of the tree beside it
	FilterMap     bool    // analysis filter also applies to the treemap
	ShowMetadata  bool    // list NTFS metadata ($MFT, $BadClus, …) of MFT scans

	TreeColumns, FileColumns, ExtColumns ui.TableLayout
	TreeHidden, FileHidden               map[string]bool
	TreeSort, FileSort, ExtSort          ui.SortOrder

	Target         string
	Recent         []string
	SearchHistory  []string
	ScanMode       string // "Automatic" or "Enumerate"
	Exclusions     []string
	UseExclusions  bool
	ConfirmRecycle bool
	FileLimit      int
	LayoutRev      int // bumped when the default column order changes
}

func defaultPrefs() prefs {
	return prefs{
		Version: 3, Metric: "Logical", Units: "Binary", Theme: "System", Compact: true, Zoom: 1,
		ShowInfo: true, ShowTreemap: true, TreemapPct: 62, TreemapColors: "Extension", FilterMap: true, ShowTypes: true, TypesPct: 74,
		TreeHidden: map[string]bool{"% of Scan": true, "Folders": true}, FileHidden: map[string]bool{"Allocated": true},
		TreeSort: ui.SortOrder{Column: "Size", Descending: true}, FileSort: ui.SortOrder{Column: "Size", Descending: true},
		ExtSort: ui.SortOrder{Column: "Size", Descending: true}, ScanMode: "Automatic", ConfirmRecycle: true,
		Exclusions: []string{}, UseExclusions: true, LayoutRev: 1,
	}
}

func configDir() string {
	if d := os.Getenv("QUARRY_CONFIG_DIR"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "Quarry")
}

func loadPrefs() prefs {
	p := defaultPrefs()
	dir := configDir()
	if dir == "" {
		return p
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return p
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // a UTF-8 BOM, as Notepad may write
	saved := defaultPrefs()
	saved.LayoutRev = 0 // absent in settings written before it existed
	if json.Unmarshal(data, &saved) != nil || saved.Version != 3 {
		return p
	}
	p = saved
	if p.LayoutRev < 1 {
		// % of Parent moved beside Name; a saved order would keep it apart.
		p.TreeColumns.Order, p.LayoutRev = nil, 1
	}
	// Defend against hand-edited or corrupt values.
	if p.View < 0 || p.View > 2 {
		p.View = 0
	}
	if !slices.Contains([]string{"Logical", "Allocated"}, p.Metric) {
		p.Metric = "Logical"
	}
	if p.Units != "Decimal" {
		p.Units = "Binary"
	}
	if !slices.Contains([]string{"System", "Dark", "Light"}, p.Theme) {
		p.Theme = "System"
	}
	if !slices.Contains([]string{"Extension", "Folder", "Mono"}, p.TreemapColors) {
		p.TreemapColors = "Extension"
	}
	if bad(p.TypesPct) || p.TypesPct < 30 || p.TypesPct > 90 {
		p.TypesPct = 74
	}
	if bad(p.TreemapPct) || p.TreemapPct < 15 || p.TreemapPct > 90 {
		p.TreemapPct = 62
	}
	if bad(p.Zoom) || p.Zoom < 0.75 || p.Zoom > 2 {
		p.Zoom = 1
	}
	if p.ScanMode != "Enumerate" {
		p.ScanMode = "Automatic"
	}
	for _, l := range []*ui.TableLayout{&p.TreeColumns, &p.FileColumns, &p.ExtColumns} {
		for k, w := range l.Widths {
			if bad(w) || w < 24 || w > 3000 {
				delete(l.Widths, k)
			}
		}
	}
	if p.TreeHidden == nil {
		p.TreeHidden = map[string]bool{}
	}
	if p.FileHidden == nil {
		p.FileHidden = map[string]bool{}
	}
	delete(p.TreeHidden, "Name")
	delete(p.FileHidden, "Name")
	p.Recent = clip(p.Recent, 12)
	p.SearchHistory = clip(p.SearchHistory, 15)
	return p
}

func bad(v float32) bool { return math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) }

func clip(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

func (a *App) savePrefs() error {
	dir := configDir()
	if dir == "" {
		return nil
	}
	a.prefs.TreeColumns = a.tree.list.Columns
	a.prefs.FileColumns = a.files.list.Columns
	a.prefs.ExtColumns = a.ext.list.Columns
	a.prefs.TreeSort, a.prefs.FileSort, a.prefs.ExtSort = a.tree.sort, a.files.sort, a.ext.sort
	if a.duplicates.active && a.files.sort.Column == "Group" {
		a.prefs.FileSort = a.duplicates.fileSort
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(a.prefs)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "settings.json"))
}

// remember moves an entry to the front of a most-recent list.
func remember(list []string, v string, n int) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	list = slices.DeleteFunc(slices.Clone(list), func(s string) bool { return strings.EqualFold(s, v) })
	return clip(append([]string{v}, list...), n)
}
