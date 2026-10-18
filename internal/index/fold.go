package index

import (
	"cmp"
	"encoding/binary"
	"math/bits"
	"slices"
	"sync"
	"unsafe"
)

// Eight bytes at a time: names and paths are mostly ASCII, and folding or
// comparing a word costs about as much as one byte in a branchy loop.

const (
	swarOnes = 0x0101010101010101
	swarHigh = 0x8080808080808080
)

// load64 reads s[i:i+8] in memory order (the first byte is the lowest).
// The caller guarantees the eight bytes exist.
func load64(s string, i int) uint64 {
	return binary.LittleEndian.Uint64(unsafe.Slice(unsafe.StringData(s[i:]), 8))
}

// load64BE reads s[i:i+8] with the first byte highest, so words order as
// their bytes do lexicographically.
func load64BE(s string, i int) uint64 {
	return binary.BigEndian.Uint64(unsafe.Slice(unsafe.StringData(s[i:]), 8))
}

// foldASCII8 lower-cases the ASCII letters of eight bytes that are all
// ASCII (no high bits); the additions cannot carry between bytes.
func foldASCII8(x uint64) uint64 {
	atLeastA := x + 0x3f*swarOnes // high bit where byte >= 'A'
	pastZ := x + 0x25*swarOnes    // high bit where byte > 'Z'
	return x | (atLeastA&^pastZ&swarHigh)>>2
}

// zeroBytes has the high bit set in the lowest zero byte of x (and possibly
// in higher bytes); it is zero only when x has no zero byte.
func zeroBytes(x uint64) uint64 {
	return (x - swarOnes) &^ x & swarHigh
}

func isASCII(s string) bool {
	i := 0
	for ; i+8 <= len(s); i += 8 {
		if load64(s, i)&swarHigh != 0 {
			return false
		}
	}
	for ; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// hasSeparator reports whether s contains a backslash or a slash.
func hasSeparator(s string) bool {
	i := 0
	for ; i+8 <= len(s); i += 8 {
		x := load64(s, i)
		if zeroBytes(x^('\\'*swarOnes))|zeroBytes(x^('/'*swarOnes)) != 0 {
			return true
		}
	}
	for ; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '/' {
			return true
		}
	}
	return false
}

// foldedWord returns eight case-folded bytes of name from offset on, as
// NameKey orders them, when they are all present and ASCII.
func foldedWord(name string, offset int) (uint64, bool) {
	if offset+8 > len(name) {
		return 0, false
	}
	x := load64BE(name, offset)
	if x&swarHigh != 0 {
		return 0, false
	}
	return foldASCII8(x), true
}

// containsFoldASCII reports whether the ASCII string s contains lower (lower
// case), folding s's letters. Candidates for the first byte are found a
// word at a time.
func containsFoldASCII(s, lower string) bool {
	n := len(lower)
	last := len(s) - n
	if last < 0 {
		return false
	}
	c0 := lower[0]
	if c0 >= 0x80 {
		return false // folded ASCII never contains other bytes
	}
	pattern := uint64(c0) * swarOnes
	for i := 0; i <= last; {
		if i+8 > len(s) {
			for ; i <= last; i++ {
				b := s[i]
				if b >= 'A' && b <= 'Z' {
					b += 32
				}
				if b == c0 && equalFoldASCII(s[i:i+n], lower) {
					return true
				}
			}
			return false
		}
		m := zeroBytes(foldASCII8(load64(s, i)) ^ pattern)
		if m == 0 {
			i += 8
			continue
		}
		// The lowest marked byte is always a real zero byte.
		j := i + bits.TrailingZeros64(m)/8
		if j > last {
			return false
		}
		if equalFoldASCII(s[j:j+n], lower) {
			return true
		}
		i = j + 1
	}
	return false
}

// keyedNameSort is the length from which Sort compares names by a folded
// eight-byte key first.
const keyedNameSort = 12

type keyedID struct {
	key uint64
	id  ID
}

var keyedPool = sync.Pool{New: func() any { return new([]keyedID) }}

// sortNamesKeyed orders ids as Compare by name does, comparing names from
// byte prefix on (every name has prefix ASCII bytes, equal when folded).
// Keys never order two names opposite to compareFold (see NameKey), and
// differ whenever the folded names do in their next eight bytes, so most
// comparisons are integer comparisons; equal keys fall back to the full
// comparison.
func (s *Snapshot) sortNamesKeyed(ids []ID, desc bool, prefix int) {
	p := keyedPool.Get().(*[]keyedID)
	keyed := slices.Grow((*p)[:0], len(ids))[:len(ids)]
	for i, id := range ids {
		k, ok := foldedWord(s.NameView(id), prefix)
		if !ok {
			k = s.NameKey(id, prefix)
		}
		keyed[i] = keyedID{k, id}
	}
	slices.SortFunc(keyed, func(a, b keyedID) int {
		if a.key != b.key {
			if (a.key < b.key) != desc {
				return -1
			}
			return 1
		}
		v := compareFold(s.NameView(a.id)[prefix:], s.NameView(b.id)[prefix:])
		if v == 0 {
			return cmp.Compare(a.id, b.id)
		}
		if desc {
			return -v
		}
		return v
	})
	for i := range keyed {
		ids[i] = keyed[i].id
	}
	if cap(keyed) <= 1<<14 { // do not retain a large sort's buffer
		*p = keyed
		keyedPool.Put(p)
	}
}

// AppendFoldedASCII appends s with ASCII letters lower-cased and reports
// true when s is all ASCII; otherwise it reports false and dst's contents
// past its original length are unspecified.
func AppendFoldedASCII(dst []byte, s string) ([]byte, bool) {
	i := 0
	for ; i+8 <= len(s); i += 8 {
		x := load64(s, i)
		if x&swarHigh != 0 {
			return dst, false
		}
		dst = binary.LittleEndian.AppendUint64(dst, foldASCII8(x))
	}
	for ; i < len(s); i++ {
		b := s[i]
		if b >= 0x80 {
			return dst, false
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		dst = append(dst, b)
	}
	return dst, true
}
