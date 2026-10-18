package scan

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"quarry/internal/index"
)

// Exclusions keeps entries out of a scan. A pattern containing a path
// separator excludes that folder (and everything below it) by full path;
// any other pattern is a wildcard matched against entry names, e.g.
// "node_modules" or "*.tmp". Matching is case-insensitive.
type Exclusions struct {
	paths []string // cleaned, lower case, without trailing separator
	names []index.Filter
	raw   []string
}

func NewExclusions(patterns []string) Exclusions {
	e := Exclusions{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		e.raw = append(e.raw, p)
		if strings.ContainsAny(p, `\/`) {
			e.paths = append(e.paths, strings.ToLower(strings.TrimRight(filepath.Clean(p), `\`)))
			continue
		}
		query := `regex:"^` + regexp.QuoteMeta(p) + `$"` // a plain word must match the whole name
		if strings.ContainsAny(p, "*?") {
			query = `name:"` + strings.ReplaceAll(p, `"`, "") + `"`
		}
		if f, err := index.ParseFilter(query); err == nil {
			e.names = append(e.names, f)
		}
	}
	return e
}

func (e Exclusions) Empty() bool        { return len(e.raw) == 0 }
func (e Exclusions) Patterns() []string { return e.raw }

// needsPaths reports whether folder paths take part in matching.
func (e Exclusions) needsPaths() bool { return len(e.paths) > 0 }

// Match reports whether an entry with this name and full path is excluded.
func (e Exclusions) Match(name, path string, dir bool) bool {
	for _, f := range e.names {
		if f.MatchNode(name, index.Node{}, nil, false) {
			return true
		}
	}
	if dir && len(e.paths) > 0 {
		lower := strings.ToLower(path)
		for _, p := range e.paths {
			if lower == p {
				return true
			}
		}
	}
	return false
}

// apply prunes excluded entries from a completed index (the MFT engine reads
// every record first).
func (e Exclusions) apply(ctx context.Context, s *index.Snapshot) error {
	if e.Empty() {
		return nil
	}
	n, err := s.Prune(ctx, func(id index.ID) bool {
		node := s.Nodes[id]
		path := ""
		if node.IsDir() && len(e.paths) > 0 {
			path = s.Path(id)
		}
		return e.Match(s.NameView(id), path, node.IsDir())
	})
	s.Excluded += uint64(n)
	return err
}
