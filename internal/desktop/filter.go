package desktop

import (
	"context"
	"strings"

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
	var ancestors []bool
	if include.AncestorTerms() {
		ancestors = make([]bool, len(base.Nodes))
		ancestors[0] = include.MatchPathComponent(base.Root)
	}
	keep := func(id index.ID) bool {
		name := base.NameView(id)
		if exclude.Match(name, "", false) {
			return false
		}
		if !hasInclude {
			return true
		}
		inherited := ancestors != nil && ancestors[base.Nodes[id].Parent]
		return include.MatchNode(name, base.Nodes[id], func() string { return base.Path(id) }, inherited)
	}
	drop := func(id index.ID) bool {
		name := base.NameView(id)
		if ancestors != nil {
			ancestors[id] = ancestors[base.Nodes[id].Parent] || include.MatchPathComponent(name)
		}
		if exclude.Empty() {
			return false
		}
		path := ""
		if exclude.NeedsPaths() {
			path = base.Path(id)
		}
		return exclude.Match(name, path, true)
	}
	isolate := f.Isolate || hasInclude
	return base.Filtered(ctx, keep, drop, isolate)
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
