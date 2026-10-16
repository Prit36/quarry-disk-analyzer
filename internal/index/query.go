package index

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type FileType struct {
	Extension               string
	Bytes, Allocated, Count uint64
}
type Summary struct {
	Types []FileType
}

func (s *Snapshot) Summarize(ctx context.Context) (Summary, error) {
	types := make(map[string]*FileType)
	for i, n := range s.Nodes {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return Summary{}, err
			}
		}
		if n.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(s.NameView(ID(i))))
		if ext == "" {
			ext = "(no extension)"
		}
		v := types[ext]
		if v == nil {
			v = &FileType{Extension: ext}
			types[ext] = v
		}
		v.Bytes += n.Size
		v.Allocated += n.Allocated
		v.Count++
	}
	result := Summary{}
	for _, v := range types {
		result.Types = append(result.Types, *v)
	}
	slices.SortFunc(result.Types, func(a, b FileType) int {
		if c := cmp.Compare(b.Allocated, a.Allocated); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Bytes, a.Bytes); c != 0 {
			return c
		}
		return strings.Compare(a.Extension, b.Extension)
	})
	return result, nil
}

type Row struct {
	ID    ID
	Depth uint32
}
type Query struct {
	Text, Mode string
	Root       ID
	Open       map[ID]bool
	Sort       Sort
}
type QueryResult struct {
	Rows    []Row
	Matches int
}

// QueryRows runs off the UI thread. All matching files remain available to the
// virtualized table; sorting must apply to the entire result, not a top-k subset.
func (s *Snapshot) QueryRows(ctx context.Context, q Query) (QueryResult, error) {
	if q.Mode == "largest" || strings.TrimSpace(q.Text) != "" {
		filter, err := parseFilter(q.Text)
		if err != nil {
			return QueryResult{}, err
		}
		rows := make([]Row, 0, min(len(s.Nodes), 16384))
		for i, n := range s.Nodes {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return QueryResult{}, err
				}
			}
			if n.IsDir() || !filter.matches(s.NameView(ID(i)), n.Size) {
				continue
			}
			rows = append(rows, Row{ID: ID(i)})
		}
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		comparisons := 0
		cancelled := false
		slices.SortFunc(rows, func(a, b Row) int {
			comparisons++
			if comparisons&4095 == 0 && ctx.Err() != nil {
				cancelled = true
			}
			if cancelled {
				return 0
			}
			return s.Compare(a.ID, b.ID, q.Sort)
		})
		if err := ctx.Err(); err != nil {
			return QueryResult{}, err
		}
		return QueryResult{rows, len(rows)}, nil
	}
	rows := []Row{{q.Root, 0}}
	stack := []Row{}
	push := func(id ID, depth uint32) {
		children := s.Children(id)
		s.Sort(children, q.Sort)
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, Row{children[i], depth})
		}
	}
	push(q.Root, 1)
	for len(stack) > 0 {
		if len(rows)%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return QueryResult{}, err
			}
		}
		r := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		rows = append(rows, r)
		if q.Open[r.ID] && r.Depth < 256 {
			push(r.ID, r.Depth+1)
		}
	}
	return QueryResult{rows, len(rows)}, nil
}

type filter struct {
	words       []string
	extension   string
	minimum     uint64
	noExtension bool
}

func parseFilter(text string) (filter, error) {
	f := filter{}
	for _, word := range strings.Fields(strings.ToLower(text)) {
		if strings.HasPrefix(word, "ext:") {
			if word == "ext:none" {
				f.noExtension = true
				continue
			}
			f.extension = "." + strings.TrimPrefix(strings.TrimPrefix(word, "ext:"), ".")
			continue
		}
		if strings.HasPrefix(word, "min:") {
			size, err := parseSize(strings.TrimPrefix(word, "min:"))
			if err != nil {
				return f, err
			}
			f.minimum = size
			continue
		}
		f.words = append(f.words, word)
	}
	return f, nil
}
func (f filter) matches(name string, size uint64) bool {
	if size < f.minimum {
		return false
	}
	if f.extension != "" && !strings.EqualFold(filepath.Ext(name), f.extension) {
		return false
	}
	if f.noExtension && filepath.Ext(name) != "" {
		return false
	}
	for _, word := range f.words {
		if !containsFold(name, word) {
			return false
		}
	}
	return true
}
func containsFold(s, lower string) bool {
	// ASCII names avoid allocating a lowercase copy per file. Unicode retains
	// correct case-insensitive matching through the standard library fallback.
	for i := 0; i < len(s); i++ {
		if s[i] >= 128 {
			return strings.Contains(strings.ToLower(s), lower)
		}
	}
	for i := 0; i+len(lower) <= len(s); i++ {
		match := true
		for j := range len(lower) {
			b := s[i+j]
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if b != lower[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
func parseSize(s string) (uint64, error) {
	mult := float64(1)
	for _, u := range []struct {
		suffix string
		scale  float64
	}{{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"tb", 1e12}, {"gb", 1e9}, {"mb", 1e6}, {"kb", 1e3}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s = strings.TrimSuffix(s, u.suffix)
			mult = u.scale
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v*mult >= float64(^uint64(0)) {
		return 0, fmt.Errorf("invalid minimum size; try min:100MB")
	}
	return uint64(v * mult), nil
}
