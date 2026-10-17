package index

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
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

// QueryRows runs off the UI thread. All matching entries remain available to
// the virtualized table; sorting applies to the entire result, not a top-k subset.
// Mode "search" matches files and folders, "largest" (and a text query in tree
// mode) files only, unless the query names a type.
func (s *Snapshot) QueryRows(ctx context.Context, q Query) (QueryResult, error) {
	if q.Mode == "largest" || q.Mode == "search" || strings.TrimSpace(q.Text) != "" {
		f, err := ParseFilter(q.Text)
		if err != nil {
			return QueryResult{}, err
		}
		if f.Kind == KindAny && q.Mode != "search" {
			f.Kind = KindFiles
		}
		ids, err := s.Search(ctx, q.Root, &f)
		if err != nil {
			return QueryResult{}, err
		}
		if err := s.SortContext(ctx, ids, q.Sort); err != nil {
			return QueryResult{}, err
		}
		rows := make([]Row, len(ids))
		for i, id := range ids {
			rows[i] = Row{ID: id}
		}
		return QueryResult{rows, len(rows)}, nil
	}
	rows := []Row{{q.Root, 0}}
	stack := []Row{}
	push := func(id ID, depth uint32) error {
		children := s.Children(id)
		if err := s.SortContext(ctx, children, q.Sort); err != nil {
			return err
		}
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, Row{children[i], depth})
		}
		return nil
	}
	if err := push(q.Root, 1); err != nil {
		return QueryResult{}, err
	}
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
			if err := push(r.ID, r.Depth+1); err != nil {
				return QueryResult{}, err
			}
		}
	}
	return QueryResult{rows, len(rows)}, nil
}

// Search returns every entry below root (excluding root) that matches f, in
// index order. It reads only the immutable snapshot.
func (s *Snapshot) Search(ctx context.Context, root ID, f *Filter) ([]ID, error) {
	if root == None || int(root) >= len(s.Nodes) {
		return nil, fmt.Errorf("invalid search scope")
	}
	ids := make([]ID, 0, 1024)
	var ancestorOK []bool
	if f.AncestorTerms() {
		ancestorOK = make([]bool, len(s.Nodes))
		ok := false
		for p := s.Nodes[root].Parent; p != None; p = s.Nodes[p].Parent {
			if f.MatchPathComponent(s.NameView(p)) {
				ok = true
				break
			}
		}
		if ok || f.MatchPathComponent(s.displayName(root)) {
			ancestorOK[root] = true
		}
	}
	err := s.Visit(ctx, root, func(id ID) error {
		if id == root {
			return nil
		}
		n := s.Nodes[id]
		name := s.NameView(id)
		inherited := false
		if ancestorOK != nil {
			inherited = ancestorOK[n.Parent]
			if n.IsDir() {
				ancestorOK[id] = inherited || f.MatchPathComponent(name)
			}
		}
		if f.MatchNode(name, n, func() string { return s.Path(id) }, inherited) {
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}

// displayName is the name shown for an entry; the scan root shows its path.
func (s *Snapshot) displayName(id ID) string {
	if id == 0 {
		return s.Root
	}
	return s.NameView(id)
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
