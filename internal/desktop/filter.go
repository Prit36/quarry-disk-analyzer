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
	keep := func(id index.ID) bool {
		name := base.NameView(id)
		if exclude.Match(name, "", false) {
			return false
		}
		if strings.TrimSpace(f.Include) == "" {
			return true
		}
		return include.MatchNode(name, base.Nodes[id], func() string { return base.Path(id) }, ancestorMatches(base, id, &include))
	}
	drop := func(id index.ID) bool {
		if exclude.Empty() {
			return false
		}
		return exclude.Match(base.NameView(id), base.Path(id), true)
	}
	isolate := f.Isolate || strings.TrimSpace(f.Include) != ""
	return base.Filtered(ctx, keep, drop, isolate)
}

// ancestorMatches evaluates path: terms for a single entry.
func ancestorMatches(s *index.Snapshot, id index.ID, f *index.Filter) bool {
	if !f.AncestorTerms() {
		return false
	}
	for p := s.Nodes[id].Parent; p != index.None; p = s.Nodes[p].Parent {
		name := s.Root
		if p != 0 {
			name = s.NameView(p)
		}
		if f.MatchPathComponent(name) {
			return true
		}
	}
	return false
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

// withoutMetadata returns s without its NTFS metadata, with totals
// recomputed, or s itself when hide is false or there is none. Metadata
// only sits directly below the scan root, so only those entries are tested.
func withoutMetadata(ctx context.Context, s *index.Snapshot, hide bool) *index.Snapshot {
	if !hide || !hasMetadata(s) {
		return s
	}
	meta := func(id index.ID) bool { return s.Nodes[id].Parent == 0 && s.IsMetadata(id) }
	view, _, err := s.Filtered(ctx, func(id index.ID) bool { return !meta(id) }, meta, false)
	if err != nil {
		return s
	}
	return view
}
