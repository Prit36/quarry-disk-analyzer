package index

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// SearchSyntax documents the matching rules shown in Help → Search Syntax.
const SearchSyntax = `Terms are case-insensitive and must all match (AND).

  report          name contains "report"
  *.mp4           wildcard over the whole name: * any run of characters, ? one character
  backup*         names starting with "backup"
  "my file"       quoted text keeps spaces
  -cache          excludes names containing "cache"
  a\b             a term containing \ matches the full path

Fields:
  ext:log         extension (ext:log;txt for several, ext:none for no extension)
  size:>500MB     size comparisons: > >= < <=, a range 1GB..4GB, or size:1GB (at least)
  min:1GiB max:2GiB
  modified:<2025-01-01   modified:>2025-06-01   modified:2025-03-14   modified:2025-01-01..2025-02-01
  after:2025-01-01 before:2025-12-31   (before includes that day)
  path:node_modules     any part of the full path (wildcards allowed)
  name:backup     same as a bare term
  type:file   type:folder
  regex:^IMG_\d+\.jpe?g$   Go RE2 regular expression on the name (linear time)

Units: B, KB, MB, GB, TB (powers of 1000) and KiB, MiB, GiB, TiB (powers of 1024).
Dates are local calendar dates (YYYY-MM-DD). Entries without a modification time never match date filters.`

// Kind restricts matches to files, folders or both.
type Kind uint8

const (
	KindAny Kind = iota
	KindFiles
	KindFolders
)

type textMatcher struct {
	text     string // lower case
	wildcard bool
	path     bool // matched against the full path
	negate   bool
}

// Filter is a compiled search. The zero Filter matches everything.
type Filter struct {
	Kind                      Kind
	terms                     []textMatcher
	paths                     []textMatcher
	extensions                []string // lower case, with dot; "" for none
	minimum, maximum          uint64
	hasMinimum, hasMaximum    bool
	after, before             int64
	hasAfter, hasBefore       bool
	regex                     *regexp.Regexp
	needsPath, needsAncestors bool
}

// Empty reports whether the filter has no criteria.
func (f *Filter) Empty() bool {
	return f.Kind == KindAny && len(f.terms) == 0 && len(f.paths) == 0 && len(f.extensions) == 0 && !f.hasMinimum && !f.hasMaximum && !f.hasAfter && !f.hasBefore && f.regex == nil
}

// NeedsPath reports whether a full path must be built per entry.
func (f *Filter) NeedsPath() bool { return f.needsPath }

// AncestorTerms reports whether path: terms without separators are present;
// they match when any ancestor component matches and are evaluated per name.
func (f *Filter) AncestorTerms() bool { return f.needsAncestors }

// ParseFilter compiles a search query.
func ParseFilter(text string) (Filter, error) {
	f := Filter{}
	tokens, err := tokenize(text)
	if err != nil {
		return f, err
	}
	for _, token := range tokens {
		negate := false
		if len(token) > 1 && token[0] == '-' && !strings.Contains(token, ":") {
			negate, token = true, token[1:]
		}
		field, value, hasField := strings.Cut(token, ":")
		lower := strings.ToLower(field)
		if !hasField || (len(field) == 1 && unicode.IsLetter(rune(field[0])) && strings.HasPrefix(value, `\`)) {
			// Plain term, or a drive-qualified path such as C:\Users.
			f.addTerm(token, negate)
			continue
		}
		switch lower {
		case "name":
			f.addTerm(value, negate)
		case "path":
			m := newMatcher(value, negate)
			if strings.ContainsAny(value, `\/`) {
				m.path = true
				f.needsPath = true
				f.terms = append(f.terms, m)
			} else {
				f.paths = append(f.paths, m)
				f.needsAncestors = true
			}
		case "ext":
			for _, ext := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return r == ';' || r == ',' || r == '|' }) {
				if ext == "none" {
					f.extensions = append(f.extensions, "")
					continue
				}
				f.extensions = append(f.extensions, "."+strings.TrimLeft(ext, ".*"))
			}
			if len(f.extensions) == 0 {
				return f, fmt.Errorf("ext: needs an extension, e.g. ext:log")
			}
		case "size":
			if err := f.parseSizeRange(value); err != nil {
				return f, err
			}
		case "min":
			v, err := ParseSize(value)
			if err != nil {
				return f, err
			}
			f.setMinimum(v)
		case "max":
			v, err := ParseSize(value)
			if err != nil {
				return f, err
			}
			f.setMaximum(v)
		case "modified", "date", "dm":
			if err := f.parseDateRange(value); err != nil {
				return f, err
			}
		case "after":
			d, err := parseDate(value)
			if err != nil {
				return f, err
			}
			f.setAfter(d.Unix())
		case "before":
			d, err := parseDate(value)
			if err != nil {
				return f, err
			}
			f.setBefore(d.AddDate(0, 0, 1).Unix())
		case "type", "kind":
			k := KindAny
			switch strings.ToLower(value) {
			case "file", "files":
				k = KindFiles
			case "folder", "folders", "dir", "directory":
				k = KindFolders
			default:
				return f, fmt.Errorf("type: must be file or folder")
			}
			if err := f.setKind(k); err != nil {
				return f, err
			}
		case "file", "files":
			if err := f.setKind(KindFiles); err != nil {
				return f, err
			}
		case "folder", "folders":
			if err := f.setKind(KindFolders); err != nil {
				return f, err
			}
		case "regex":
			if len(value) > 1000 {
				return f, fmt.Errorf("regular expression is too long")
			}
			re, err := regexp.Compile("(?i)" + value)
			if err != nil {
				return f, fmt.Errorf("invalid regular expression: %v", err)
			}
			f.regex = re
		default:
			f.addTerm(token, negate)
		}
	}
	if f.hasMinimum && f.hasMaximum && f.minimum > f.maximum {
		return f, fmt.Errorf("minimum size exceeds maximum size")
	}
	if f.hasAfter && f.hasBefore && f.after >= f.before {
		return f, fmt.Errorf("modified date range is reversed")
	}
	return f, nil
}

func tokenize(text string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	quoted, any := false, false
	for _, r := range text {
		switch {
		case r == '"':
			quoted = !quoted
			any = true
		case unicode.IsSpace(r) && !quoted:
			if any {
				tokens = append(tokens, current.String())
			}
			current.Reset()
			any = false
		default:
			current.WriteRune(r)
			any = true
		}
	}
	if quoted {
		return nil, fmt.Errorf("unclosed quotation mark")
	}
	if any && current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

func newMatcher(value string, negate bool) textMatcher {
	lower := strings.ToLower(value)
	return textMatcher{text: lower, wildcard: strings.ContainsAny(lower, "*?"), negate: negate}
}

func (f *Filter) addTerm(value string, negate bool) {
	if value == "" {
		return
	}
	m := newMatcher(value, negate)
	if strings.ContainsAny(value, `\/`) {
		m.path = true
		m.text = strings.ReplaceAll(m.text, "/", `\`)
		f.needsPath = true
	}
	f.terms = append(f.terms, m)
}

func (f *Filter) setKind(k Kind) error {
	if f.Kind != KindAny && f.Kind != k {
		return fmt.Errorf("choose either files or folders")
	}
	f.Kind = k
	return nil
}

func (f *Filter) setMinimum(v uint64) {
	if !f.hasMinimum || v > f.minimum {
		f.minimum, f.hasMinimum = v, true
	}
}
func (f *Filter) setMaximum(v uint64) {
	if !f.hasMaximum || v < f.maximum {
		f.maximum, f.hasMaximum = v, true
	}
}
func (f *Filter) setAfter(v int64) {
	if !f.hasAfter || v > f.after {
		f.after, f.hasAfter = v, true
	}
}
func (f *Filter) setBefore(v int64) {
	if !f.hasBefore || v < f.before {
		f.before, f.hasBefore = v, true
	}
}

func (f *Filter) parseSizeRange(value string) error {
	if low, high, ok := strings.Cut(value, ".."); ok {
		a, err := ParseSize(low)
		if err != nil {
			return err
		}
		b, err := ParseSize(high)
		if err != nil {
			return err
		}
		f.setMinimum(a)
		f.setMaximum(b)
		return nil
	}
	op, rest := splitOperator(value)
	v, err := ParseSize(rest)
	if err != nil {
		return err
	}
	switch op {
	case ">":
		if v == math.MaxUint64 {
			return fmt.Errorf("size is too large")
		}
		f.setMinimum(v + 1)
	case ">=", "", "=":
		f.setMinimum(v)
	case "<":
		if v == 0 {
			return fmt.Errorf("size:<0 matches nothing")
		}
		f.setMaximum(v - 1)
	case "<=":
		f.setMaximum(v)
	}
	return nil
}

func (f *Filter) parseDateRange(value string) error {
	if low, high, ok := strings.Cut(value, ".."); ok {
		a, err := parseDate(low)
		if err != nil {
			return err
		}
		b, err := parseDate(high)
		if err != nil {
			return err
		}
		f.setAfter(a.Unix())
		f.setBefore(b.AddDate(0, 0, 1).Unix())
		return nil
	}
	op, rest := splitOperator(value)
	d, err := parseDate(rest)
	if err != nil {
		return err
	}
	switch op {
	case ">":
		f.setAfter(d.AddDate(0, 0, 1).Unix())
	case ">=":
		f.setAfter(d.Unix())
	case "<":
		f.setBefore(d.Unix())
	case "<=":
		f.setBefore(d.AddDate(0, 0, 1).Unix())
	default:
		f.setAfter(d.Unix())
		f.setBefore(d.AddDate(0, 0, 1).Unix())
	}
	return nil
}

func splitOperator(value string) (string, string) {
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(value, op) {
			return op, value[len(op):]
		}
	}
	return "", value
}

func parseDate(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006-1-2"} {
		if d, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return d, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q; use YYYY-MM-DD", value)
}

// ParseSize parses "500MB", "1.5GiB", "100k" or a plain byte count.
func ParseSize(s string) (uint64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := float64(1)
	for _, u := range []struct {
		suffix string
		scale  float64
	}{{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"tb", 1e12}, {"gb", 1e9}, {"mb", 1e6}, {"kb", 1e3}, {"t", 1e12}, {"g", 1e9}, {"m", 1e6}, {"k", 1e3}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			mult = u.scale
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v*mult >= float64(math.MaxUint64) {
		return 0, fmt.Errorf("invalid size %q; try 500MB or 1GiB", s)
	}
	return uint64(v * mult), nil
}

// MatchNode tests an entry. path is only consulted when NeedsPath; ancestor
// reports whether an ancestor already satisfied every path: term.
func (f *Filter) MatchNode(name string, n Node, path func() string, ancestor bool) bool {
	switch f.Kind {
	case KindFiles:
		if n.IsDir() {
			return false
		}
	case KindFolders:
		if !n.IsDir() {
			return false
		}
	}
	if f.hasMinimum && n.Size < f.minimum || f.hasMaximum && n.Size > f.maximum {
		return false
	}
	if (f.hasAfter || f.hasBefore) && (n.Modified == 0 || f.hasAfter && n.Modified < f.after || f.hasBefore && n.Modified >= f.before) {
		return false
	}
	if len(f.extensions) > 0 {
		ext := ""
		if !n.IsDir() {
			ext = filepath.Ext(name)
		}
		found := false
		for _, want := range f.extensions {
			if strings.EqualFold(ext, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.paths) > 0 && !ancestor && !f.MatchPathComponent(name) {
		return false
	}
	var full string
	for _, t := range f.terms {
		subject := name
		if t.path {
			if full == "" {
				full = path()
			}
			subject = full
		}
		if t.match(subject) == t.negate {
			return false
		}
	}
	if f.regex != nil && !f.regex.MatchString(name) {
		return false
	}
	return true
}

// MatchPathComponent reports whether a single name satisfies every path: term.
func (f *Filter) MatchPathComponent(name string) bool {
	for _, t := range f.paths {
		if t.match(name) == t.negate {
			return false
		}
	}
	return true
}

func (t textMatcher) match(s string) bool {
	if t.wildcard {
		return wildcardFold(t.text, s)
	}
	return containsFold(s, t.text)
}

// wildcardFold matches pattern (lower case, * and ?) against the whole of s,
// case-insensitively, in O(len(s)*len(pattern)) worst case without recursion.
func wildcardFold(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if p < len(pattern) {
			pr, psize := utf8.DecodeRuneInString(pattern[p:])
			if pr == '?' || (pr != '*' && foldEqual(pr, r)) {
				p += psize
				i += size
				continue
			}
			if pr == '*' {
				star, mark = p, i
				p += psize
				continue
			}
		}
		if star >= 0 {
			p = star + 1
			_, size := utf8.DecodeRuneInString(s[mark:])
			mark += size
			i = mark
			continue
		}
		return false
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func foldEqual(lowerPattern, r rune) bool {
	if lowerPattern == r {
		return true
	}
	if r < 128 {
		if r >= 'A' && r <= 'Z' {
			r += 32
		}
		return r == lowerPattern
	}
	return unicode.ToLower(r) == lowerPattern
}
