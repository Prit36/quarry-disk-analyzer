package desktop

import (
	"context"
	"runtime"
	"strings"
	"sync"

	"quarry/internal/index"
	"quarry/internal/scan"
)

// analysisFilter hides or isolates entries that are already in the index.
// It never changes what is scanned (that is scan exclusions) and never
// touches the disk. Views read a filtered copy whose totals are recomputed,
// so sizes and percentages describe exactly what is shown.
type analysisFilter struct {
	Include string // search syntax; only files matching it are kept
	Exclude string // names, wildcards or folder paths, separated by ;
	Isolate bool   // also hide folders left without any file
	stats   index.FilterStats
}

func (f analysisFilter) active() bool {
	return strings.TrimSpace(f.Include) != "" || strings.TrimSpace(f.Exclude) != ""
}

func splitPatterns(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '\n' || r == '\r' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (f analysisFilter) apply(ctx context.Context, base *index.Snapshot) (*index.Snapshot, index.FilterStats, error) {
	include, err := index.ParseFilter(f.Include)
	if err != nil {
		return nil, index.FilterStats{}, err
	}
	if include.Kind == index.KindAny {
		include.Kind = index.KindFiles
	}
	exclude := scan.NewExclusions(splitPatterns(f.Exclude))
	hasInclude := strings.TrimSpace(f.Include) != ""
	// Decide every file up front on all cores; the tree rebuild then only
	// reads bits. The included files are exactly a search of the whole scan
	// (inherited path: terms, full paths and all), and name exclusions need
	// nothing but the name.
	var matched, excluded []uint64
	if hasInclude && len(base.Nodes) > 0 {
		ids, err := base.Search(ctx, 0, &include)
		if err != nil {
			return nil, index.FilterStats{}, err
		}
		matched = make([]uint64, (len(base.Nodes)+63)/64)
		for _, id := range ids {
			matched[id>>6] |= 1 << (id & 63)
		}
	}
	if !exclude.Empty() {
		var err error
		if excluded, err = excludedFiles(ctx, base, exclude); err != nil {
			return nil, index.FilterStats{}, err
		}
	}
	keep := func(id index.ID) bool {
		if excluded != nil && excluded[id>>6]&(1<<(id&63)) != 0 {
			return false
		}
		return matched == nil || matched[id>>6]&(1<<(id&63)) != 0
	}
	var drop func(index.ID) bool
	if !exclude.Empty() {
		drop = func(id index.ID) bool {
			path := ""
			if exclude.NeedsPaths() {
				path = base.Path(id)
			}
			return exclude.Match(base.NameView(id), path, true)
		}
	}
	isolate := f.Isolate || hasInclude
	return base.FilteredParallel(ctx, keep, drop, isolate)
}

// excludedFiles marks the files whose names match exclude, splitting the
// index into word-aligned ranges matched in parallel.
func excludedFiles(ctx context.Context, s *index.Snapshot, exclude scan.Exclusions) ([]uint64, error) {
	bits := make([]uint64, (len(s.Nodes)+63)/64)
	workers := min(runtime.GOMAXPROCS(0), max(1, len(bits)/1024))
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for word := len(bits) * w / workers; word < len(bits)*(w+1)/workers; word++ {
				if word&63 == 0 && ctx.Err() != nil {
					return
				}
				var v uint64
				for i := word * 64; i < min(len(s.Nodes), word*64+64); i++ {
					if !s.Nodes[i].IsDir() && exclude.Match(s.NameView(index.ID(i)), "", false) {
						v |= 1 << (i & 63)
					}
				}
				bits[word] = v
			}
		}()
	}
	wg.Wait()
	return bits, ctx.Err()
}

func (f analysisFilter) summary() string {
	var parts []string
	if s := strings.TrimSpace(f.Include); s != "" {
		parts = append(parts, "showing "+s)
	}
	if s := strings.TrimSpace(f.Exclude); s != "" {
		parts = append(parts, "hiding "+s)
	}
	return strings.Join(parts, "; ")
}

// hasMetadata reports whether a scan can contain NTFS metadata entries:
// direct MFT scans list $MFT, $BadClus, $Extend… and unresolved records.
func hasMetadata(s *index.Snapshot) bool {
	return strings.Contains(s.Engine, "MFT") || s.Unresolved > 0
}

// metadataEntries lists a scan's NTFS metadata entries. Metadata (and
// unresolved records) only sits directly below the scan root.
func metadataEntries(s *index.Snapshot) []index.ID {
	if !hasMetadata(s) {
		return nil
	}
	var ids []index.ID
	for c := s.Nodes[0].FirstChild; c != index.None; c = s.Nodes[c].NextSibling {
		if s.IsMetadata(c) {
			ids = append(ids, c)
		}
	}
	return ids
}

// hideMetadata finds a new scan's metadata entries and, with hide, detaches
// them in place: s must not be shared yet.
func hideMetadata(s *index.Snapshot, hide bool) []index.ID {
	meta := metadataEntries(s)
	if hide {
		s.Remove(meta...)
	}
	return meta
}
