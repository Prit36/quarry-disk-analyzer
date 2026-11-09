package index

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

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
	// shape and literal describe the common wildcard patterns ("*.mp4",
	// "backup*", "*cache*") that match ASCII names without backtracking.
	shape   uint8
	literal string
}

const (
	shapeGeneral = iota
	shapePrefix
	shapeSuffix
	shapeContains
)

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
	// categories holds the extensions of cat: terms; a file matches when
	// its extension is in any of them.
	categories    map[string]bool
	otherCategory bool // cat:other: files whose extension no category lists
	// flags and noFlags are entry flags that must be set or clear (is:).
	flags, noFlags uint16
	kinds          bool // any is: or cat: term, for matchKinds
}

// Empty reports whether the filter has no criteria.
func (f *Filter) Empty() bool {
	return f.Kind == KindAny && len(f.terms) == 0 && len(f.paths) == 0 && len(f.extensions) == 0 && !f.hasMinimum && !f.hasMaximum && !f.hasAfter && !f.hasBefore && f.regex == nil && f.categories == nil && f.flags == 0 && f.noFlags == 0
}

// NeedsPath reports whether a full path must be built per entry.
func (f *Filter) NeedsPath() bool { return f.needsPath }

// AncestorTerms reports whether path: terms without separators are present;
// they match when any ancestor component matches and are evaluated per name.
func (f *Filter) AncestorTerms() bool { return f.needsAncestors }

// ParseFilter compiles a search query. Relative ages (age:) count from now.
func ParseFilter(text string) (Filter, error) {
	return ParseFilterAt(text, time.Now())
}

// ParseFilterAt compiles a search query with age: terms counted from now.
func ParseFilterAt(text string, now time.Time) (Filter, error) {
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
		case "age":
			if err := f.parseAge(value, now); err != nil {
				return f, err
			}
		case "cat", "category":
			if f.categories == nil {
				f.categories = map[string]bool{}
			}
			for _, name := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' || r == '|' }) {
				if strings.EqualFold(strings.TrimSpace(name), "other") {
					f.otherCategory = true
					continue
				}
				c, ok := FindCategory(name)
				if !ok {
					return f, fmt.Errorf("unknown category %q; try %s", name, categoryKeys())
				}
				for _, e := range c.Extensions {
					f.categories[e] = true
				}
			}
			if len(f.categories) == 0 && !f.otherCategory {
				return f, fmt.Errorf("cat: needs a category, e.g. cat:videos")
			}
		case "is", "-is":
			var flag uint16
			switch strings.ToLower(value) {
			case "cloud", "online", "onlineonly", "placeholder":
				flag = CloudOnly
			case "hardlink", "link", "shared":
				flag = Shared
			case "incomplete", "unreadable":
				flag = Incomplete
			default:
				return f, fmt.Errorf("is: must be cloud, hardlink or incomplete")
			}
			if lower == "-is" {
				f.noFlags |= flag
			} else {
				f.flags |= flag
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
	f.kinds = f.categories != nil || f.flags != 0 || f.noFlags != 0
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
	m := textMatcher{text: lower, wildcard: strings.ContainsAny(lower, "*?"), negate: negate}
	if m.wildcard && !strings.Contains(lower, "?") && isASCII(lower) {
		inner := strings.Trim(lower, "*")
		if !strings.Contains(inner, "*") {
			starts, ends := strings.HasPrefix(lower, "*"), strings.HasSuffix(lower, "*")
			switch {
			case starts && ends:
				m.shape = shapeContains
			case starts:
				m.shape = shapeSuffix
			case ends:
				m.shape = shapePrefix
			}
			m.literal = inner
		}
	}
	return m
}

// equalFoldASCII compares s with an ASCII lower-case literal, folding
// s's ASCII upper case; it reports false for non-ASCII s.
func equalFoldASCII(s, lower string) bool {
	if len(s) != len(lower) {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		if b != lower[i] {
			return false
		}
	}
	return true
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

// parseAge reads age:>1y, age:<30d, age:30d..90d: how long ago entries were
// last modified. Units are d (days), w (weeks), m (30-day months) and y
// (365-day years). age:>X is older than X, age:<X newer.
func (f *Filter) parseAge(value string, now time.Time) error {
	at := func(v string) (int64, error) {
		d, err := ParseAge(v)
		if err != nil {
			return 0, err
		}
		return now.Add(-d).Unix(), nil
	}
	if low, high, ok := strings.Cut(value, ".."); ok {
		newest, err := at(low)
		if err != nil {
			return err
		}
		oldest, err := at(high)
		if err != nil {
			return err
		}
		if oldest > newest {
			oldest, newest = newest, oldest
		}
		f.setAfter(oldest)
		f.setBefore(newest)
		return nil
	}
	op, rest := splitOperator(value)
	t, err := at(rest)
	if err != nil {
		return err
	}
	switch op {
	case ">", ">=", "", "=":
		f.setBefore(t)
	case "<", "<=":
		f.setAfter(t)
	}
	return nil
}

// ParseAge reads a duration such as 30d, 2w, 6m or 1y.
func ParseAge(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid age %q; try 30d, 6m or 1y", s)
	}
	days := map[byte]float64{'d': 1, 'w': 7, 'm': 30, 'y': 365}[s[len(s)-1]]
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if days == 0 || err != nil || v < 0 || v > 1000*365 || math.IsNaN(v) {
		return 0, fmt.Errorf("invalid age %q; try 30d, 6m or 1y", s)
	}
	return time.Duration(v * days * float64(24*time.Hour)), nil
}

func categoryKeys() string {
	keys := make([]string, len(Categories))
	for i, c := range Categories {
		keys[i] = c.Key
	}
	return strings.Join(keys, ", ")
}

// matchKinds applies is: and cat: terms. Callers test it after matchNode,
// behind one flag: inside matchNode, even a call that is never reached
// slows the search loop measurably.
//
//go:noinline
func (f *Filter) matchKinds(name string, n *Node) bool {
	if f.flags != 0 && n.Flags&f.flags != f.flags || n.Flags&f.noFlags != 0 {
		return false
	}
	if f.categories != nil {
		if n.IsDir() {
			return false
		}
		ext := ""
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			ext = name[i:]
		}
		return f.matchCategory(ext)
	}
	return true
}

// matchCategory reports whether a file extension (with its dot, any case,
// "" for none) is in the cat: terms' categories. It folds into a stack
// buffer, so matching allocates nothing: every listed extension is short
// ASCII, so a longer or non-ASCII one can only be Other.
func (f *Filter) matchCategory(ext string) bool {
	var buf [16]byte
	if ext == "" || len(ext) > len(buf) {
		return f.otherCategory
	}
	for i := 0; i < len(ext); i++ {
		c := ext[i]
		if c >= 0x80 {
			return f.otherCategory
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	key := buf[:len(ext)]
	if f.categories[string(key)] {
		return true
	}
	if f.otherCategory {
		_, listed := categoryOf[string(key)]
		return !listed
	}
	return false
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
	return f.matchNode(name, &n, path, nil, ancestor) && (!f.kinds || f.matchKinds(name, &n))
}

// matchNode is MatchNode. With paths, the path terms paths tracks are
// decided by its incremental matches for the path that path() returned.
func (f *Filter) matchNode(name string, n *Node, path func() string, paths *pathWalker, ancestor bool) bool {
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
	if (f.hasAfter || f.hasBefore) && (n.Modified == 0 || f.hasAfter && n.ModTime() < f.after || f.hasBefore && n.ModTime() >= f.before) {
		return false
	}
	if len(f.extensions) > 0 {
		ext := ""
		if !n.IsDir() {
			if i := strings.LastIndexByte(name, '.'); i >= 0 {
				ext = name[i:]
			}
		}
		found := false
		for _, want := range f.extensions {
			if equalFoldASCII(ext, want) || strings.EqualFold(ext, want) {
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
	for i, t := range f.terms {
		subject := name
		if t.path {
			if full == "" {
				full = path()
			}
			if paths != nil && i < 64 && paths.plain&(1<<i) != 0 {
				if (paths.termHits()&(1<<i) != 0) == t.negate {
					return false
				}
				continue
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
		// A non-ASCII character can fold to an ASCII one (the Kelvin sign
		// to k), so the shortcuts only decide over ASCII text.
		n := len(t.literal)
		switch t.shape {
		case shapeSuffix:
			if len(s) < n {
				return false
			}
			if tail := s[len(s)-n:]; isASCII(tail) {
				return equalFoldASCII(tail, t.literal)
			}
		case shapePrefix:
			if len(s) < n {
				return false
			}
			if head := s[:n]; isASCII(head) {
				return equalFoldASCII(head, t.literal)
			}
		case shapeContains:
			if isASCII(s) {
				return containsFold(s, t.literal)
			}
		}
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
