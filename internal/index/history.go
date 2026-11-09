package index

import (
	"cmp"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// History keeps, for scans the user chose to remember, a small summary
// (totals and the larger folders near the root) and, while within limits,
// the full saved scan for detailed comparison. Summaries are what growth
// trends and alerts read: listing them never loads an index.

// FolderSize is a folder's totals in a history summary, by its path
// relative to the scan root ("Users" or "Users\Ann").
type FolderSize struct {
	Path            string
	Size, Allocated uint64
}

// ScanSummary describes one remembered scan.
type ScanSummary struct {
	Root            string
	Started         time.Time
	Engine          string
	Size, Allocated uint64
	Files, Dirs     uint32
	Partial         bool
	// Top lists every folder directly below the root and those one level
	// further down holding at least 0.1% of the scan, largest first.
	Top  []FolderSize
	Scan string `json:"-"` // the saved scan file, "" if not kept
	File string `json:"-"` // this summary's file
}

// maxDirectFolders bounds the folders directly below the root that a
// history summary lists.
const maxDirectFolders = 2000

// SummarizeHistory builds a history summary: totals, every folder
// directly below the root, and the folders one level further down that
// hold at least 0.1% of the scan, at most limit of those.
func (s *Snapshot) SummarizeHistory(limit int) ScanSummary {
	root := s.Nodes[0]
	sum := ScanSummary{Root: s.Root, Started: s.Started, Engine: s.Engine, Size: root.Size, Allocated: root.Allocated, Files: root.Files, Dirs: root.Dirs, Partial: s.Cancelled}
	floor := max(1, root.Size/1000)
	var deeper []FolderSize
	for c := root.FirstChild; c != None; c = s.Nodes[c].NextSibling {
		n := s.Nodes[c]
		if !n.IsDir() {
			continue
		}
		name := s.NameView(c)
		sum.Top = append(sum.Top, FolderSize{name, n.Size, n.Allocated})
		for g := n.FirstChild; g != None; g = s.Nodes[g].NextSibling {
			if m := s.Nodes[g]; m.IsDir() && m.Size >= floor {
				deeper = append(deeper, FolderSize{name + `\` + s.NameView(g), m.Size, m.Allocated})
			}
		}
	}
	bySize := func(a, b FolderSize) int {
		if c := cmp.Compare(b.Size, a.Size); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	}
	// A root with very many folders keeps its largest, so a summary stays
	// small (ListHistory skips files over 4 MB).
	if len(sum.Top) > maxDirectFolders {
		slices.SortFunc(sum.Top, bySize)
		sum.Top = slices.Clip(sum.Top[:maxDirectFolders])
	}
	slices.SortFunc(deeper, bySize)
	if len(deeper) > limit {
		deeper = deeper[:limit]
	}
	sum.Top = append(sum.Top, deeper...)
	slices.SortFunc(sum.Top, bySize)
	return sum
}

// historyStem names a remembered scan's files: its time, and its root so
// that scans of two drives finishing in the same second do not collide.
func historyStem(root string, started time.Time) string {
	return fmt.Sprintf("scan-%s%s", started.UTC().Format("20060102-150405"), rootTag(root))
}

// rootTag ends the file names of a root's records, so listing one root's
// history reads only its own summaries.
func rootTag(root string) string {
	key := strings.ToLower(strings.TrimSuffix(filepath.Clean(root), `\`))
	return fmt.Sprintf("-%08x", crc32.ChecksumIEEE([]byte(key)))
}

// RecordHistory writes s's summary to directory and, when keepScan, the
// saved scan beside it (skipped, not failed, beyond SnapshotLimit). It
// then removes the oldest records of the same root beyond keepScans full
// scans and keepSummaries summaries.
func (s *Snapshot) RecordHistory(directory string, keepScan bool, keepScans, keepSummaries int, save func(path string) error) (ScanSummary, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return ScanSummary{}, err
	}
	sum := s.SummarizeHistory(300)
	stem := filepath.Join(directory, historyStem(s.Root, s.Started))
	if keepScan && save != nil {
		if err := save(stem + ".quarry"); err == nil {
			sum.Scan = stem + ".quarry"
		}
	}
	data, err := json.Marshal(sum)
	if err != nil {
		return sum, err
	}
	tmp, err := os.CreateTemp(directory, ".summary-*")
	if err != nil {
		return sum, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), stem+".json")
	}
	if err != nil {
		return sum, err
	}
	sum.File = stem + ".json"
	return sum, PruneHistory(directory, s.Root, keepScans, keepSummaries)
}

// ListHistory returns the remembered scans of root ("" for all), newest
// first.
func ListHistory(directory, root string) ([]ScanSummary, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	suffix := ".json"
	if root != "" {
		suffix = rootTag(root) + ".json"
	}
	var out []ScanSummary
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "scan-") || !strings.HasSuffix(name, suffix) {
			continue
		}
		path := filepath.Join(directory, name)
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 4<<20 {
			continue
		}
		var sum ScanSummary
		if json.Unmarshal(data, &sum) != nil || sum.Root == "" {
			continue
		}
		if root != "" && !sameRoot(sum.Root, root) {
			continue
		}
		sum.File = path
		if scan := strings.TrimSuffix(path, ".json") + ".quarry"; fileExists(scan) {
			sum.Scan = scan
		}
		out = append(out, sum)
	}
	slices.SortFunc(out, func(a, b ScanSummary) int { return b.Started.Compare(a.Started) })
	return out, nil
}

func sameRoot(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(filepath.Clean(a), `\`), strings.TrimSuffix(filepath.Clean(b), `\`))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// PruneHistory keeps the newest keepScans saved scans and keepSummaries
// summaries of root.
func PruneHistory(directory, root string, keepScans, keepSummaries int) error {
	list, err := ListHistory(directory, root)
	if err != nil {
		return err
	}
	scans := 0
	for i, sum := range list {
		if sum.Scan != "" {
			scans++
			if scans > keepScans {
				if err := os.Remove(sum.Scan); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
		if i >= keepSummaries {
			if err := os.Remove(sum.File); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// DeleteHistory removes one remembered scan.
func DeleteHistory(sum ScanSummary) error {
	if sum.File == "" {
		return fmt.Errorf("not a history record")
	}
	if sum.Scan != "" {
		if err := os.Remove(sum.Scan); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Remove(sum.File)
}

// FolderGrowth is how much a folder of the summaries changed between two
// remembered scans.
type FolderGrowth struct {
	Path          string
	Before, After uint64
}

// Delta is After-Before as a signed number.
func (g FolderGrowth) Delta() int64 { return int64(g.After) - int64(g.Before) }

// CompareSummaries matches the folders of two summaries of the same root
// and returns their changes, largest growth first. Folders directly below
// the root are always summarized, so their changes are exact; a deeper
// folder missing from one summary was absent or below its 0.1% floor there
// and counts as 0, which overstates its change by at most that floor.
func CompareSummaries(older, newer ScanSummary, allocated bool) []FolderGrowth {
	value := func(f FolderSize) uint64 {
		if allocated {
			return f.Allocated
		}
		return f.Size
	}
	byPath := map[string]*FolderGrowth{}
	var order []string
	for _, f := range older.Top {
		k := strings.ToLower(f.Path)
		byPath[k] = &FolderGrowth{Path: f.Path, Before: value(f)}
		order = append(order, k)
	}
	for _, f := range newer.Top {
		k := strings.ToLower(f.Path)
		if g := byPath[k]; g != nil {
			g.After, g.Path = value(f), f.Path
		} else {
			byPath[k] = &FolderGrowth{Path: f.Path, After: value(f)}
			order = append(order, k)
		}
	}
	out := make([]FolderGrowth, 0, len(order))
	for _, k := range order {
		out = append(out, *byPath[k])
	}
	slices.SortStableFunc(out, func(a, b FolderGrowth) int { return cmp.Compare(b.Delta(), a.Delta()) })
	return out
}
