package index

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// Export rows are formatted straight into one output buffer. The bytes are
// exactly those encoding/csv and encoding/json produced for the same records;
// formatting them directly avoids per-field quoting scans of numbers, time
// formatting through time.Time, and reflection per JSON record.

const exportFlush = 1 << 20

// exportBuffer collects formatted rows and writes them in large blocks.
type exportBuffer struct {
	w   io.Writer
	buf []byte
	err error
}

func newExportBuffer(w io.Writer) *exportBuffer {
	return &exportBuffer{w: w, buf: make([]byte, 0, exportFlush+4096)}
}

// done writes a full block once enough rows are buffered.
func (e *exportBuffer) done() error {
	if len(e.buf) >= exportFlush {
		return e.flush()
	}
	return nil
}

func (e *exportBuffer) flush() error {
	if e.err == nil && len(e.buf) > 0 {
		_, e.err = e.w.Write(e.buf)
	}
	e.buf = e.buf[:0]
	return e.err
}

// csvNeedsQuotes is encoding/csv's rule for a comma-separated field.
func csvNeedsQuotes(field string) bool {
	if field == "" {
		return false
	}
	if field == `\.` {
		return true
	}
	i := 0
	for ; i+8 <= len(field); i += 8 {
		x := load64(field, i)
		if zeroBytes(x^('\n'*swarOnes))|zeroBytes(x^('\r'*swarOnes))|zeroBytes(x^('"'*swarOnes))|zeroBytes(x^(','*swarOnes)) != 0 {
			return true
		}
	}
	for ; i < len(field); i++ {
		if c := field[i]; c == '\n' || c == '\r' || c == '"' || c == ',' {
			return true
		}
	}
	if c := field[0]; c < utf8.RuneSelf {
		return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
	}
	r, _ := utf8.DecodeRuneInString(field)
	return unicode.IsSpace(r)
}

// appendCSVField appends field as encoding/csv writes it (LF line endings).
func appendCSVField(b []byte, field string) []byte {
	if !csvNeedsQuotes(field) {
		return append(b, field...)
	}
	b = append(b, '"')
	for i := 0; i < len(field); i++ {
		if field[i] == '"' {
			b = append(b, '"')
		}
		b = append(b, field[i])
	}
	return append(b, '"')
}

const csvHeader = "Path,Type,Size,Allocated,Files,Folders,Modified,Shared,Incomplete\n"

func appendCSVRecord(b []byte, path string, n *Node) []byte {
	b = appendCSVField(b, path)
	if n.IsDir() {
		b = append(b, ",folder,"...)
	} else {
		b = append(b, ",file,"...)
	}
	b = strconv.AppendUint(b, n.Size, 10)
	b = append(b, ',')
	b = strconv.AppendUint(b, n.Allocated, 10)
	b = append(b, ',')
	b = strconv.AppendUint(b, uint64(n.Files), 10)
	b = append(b, ',')
	b = strconv.AppendUint(b, uint64(n.Dirs), 10)
	b = append(b, ',')
	if n.Modified != 0 {
		b = appendRFC3339UTC(b, uint64(n.Modified))
	}
	b = append(b, ',')
	b = strconv.AppendBool(b, n.Flags&Shared != 0)
	b = append(b, ',')
	b = strconv.AppendBool(b, n.Flags&Incomplete != 0)
	return append(b, '\n')
}

// appendRFC3339UTC formats Unix seconds after 1970 as
// time.Unix(t, 0).UTC().AppendFormat(b, time.RFC3339) does. Node times are
// 32-bit, so the year always has four digits.
func appendRFC3339UTC(b []byte, t uint64) []byte {
	days, secs := t/86400, t%86400
	// Civil date from days since 1970-01-01 (Hinnant's algorithm).
	z := days + 719468
	era := z / 146097
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	year := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	day := doy - (153*mp+2)/5 + 1
	month := mp + 3
	if month > 12 {
		month -= 12
	}
	if month <= 2 {
		year++
	}
	h, m, s := secs/3600, secs/60%60, secs%60
	return append(b,
		byte('0'+year/1000), byte('0'+year/100%10), byte('0'+year/10%10), byte('0'+year%10), '-',
		byte('0'+month/10), byte('0'+month%10), '-',
		byte('0'+day/10), byte('0'+day%10), 'T',
		byte('0'+h/10), byte('0'+h%10), ':',
		byte('0'+m/10), byte('0'+m%10), ':',
		byte('0'+s/10), byte('0'+s%10), 'Z')
}

// appendJSONString appends s as a json.Encoder (with HTML escaping) writes
// it. Printable ASCII other than <, > and &, backslashes, quotes and valid
// UTF-8 outside U+2028/U+2029 are handled here; any other string is encoded
// by encoding/json itself, so rare bytes keep their exact escaping.
func appendJSONString(b []byte, s string) []byte {
	start := len(b)
	b = append(b, '"')
	for i := 0; i < len(s); {
		// Copy a run of bytes that need no escaping in one append.
		j := i
		for j < len(s) && jsonPlain[s[j]] {
			j++
		}
		b = append(b, s[i:j]...)
		if i = j; i == len(s) {
			break
		}
		c := s[i]
		if c < utf8.RuneSelf {
			if c != '\\' && c != '"' {
				return appendJSONStringSlow(b[:start], s) // control bytes, <, > and &
			}
			b = append(b, '\\', c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || r == 0x2028 || r == 0x2029 { // line and paragraph separators
			return appendJSONStringSlow(b[:start], s)
		}
		b = append(b, s[i:i+size]...)
		i += size
	}
	return append(b, '"')
}

// jsonPlain marks the ASCII bytes a JSON string holds unescaped under HTML
// escaping: printable, other than the quote, backslash, <, > and &.
var jsonPlain = func() (t [256]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = c != '"' && c != '\\' && c != '<' && c != '>' && c != '&'
	}
	return t
}()

func appendJSONStringSlow(b []byte, s string) []byte {
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(s); err != nil {
		// A string always encodes; keep the output well formed regardless.
		out.Reset()
		out.WriteString(`""`)
	}
	return append(b, bytes.TrimSuffix(out.Bytes(), []byte{'\n'})...)
}

// appendJSONRecord appends one ExportJSON record and the newline
// json.Encoder.Encode writes after it.
func appendJSONRecord(b []byte, path, name string, n *Node) []byte {
	b = append(b, `{"Path":`...)
	b = appendJSONString(b, path)
	b = append(b, `,"Name":`...)
	b = appendJSONString(b, name)
	b = append(b, `,"Size":`...)
	b = strconv.AppendUint(b, n.Size, 10)
	b = append(b, `,"Allocated":`...)
	b = strconv.AppendUint(b, n.Allocated, 10)
	b = append(b, `,"Files":`...)
	b = strconv.AppendUint(b, uint64(n.Files), 10)
	b = append(b, `,"Folders":`...)
	b = strconv.AppendUint(b, uint64(n.Dirs), 10)
	b = append(b, `,"Modified":`...)
	b = strconv.AppendInt(b, n.ModTime(), 10)
	b = append(b, `,"Flags":`...)
	b = strconv.AppendUint(b, uint64(n.Flags), 10)
	return append(b, "}\n"...)
}
