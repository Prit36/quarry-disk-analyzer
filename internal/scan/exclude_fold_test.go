package scan

import (
	"strings"
	"testing"
)

func TestEqualFoldWordMatchesEqualFold(t *testing.T) {
	words := []string{"node_modules", "Cache", "k", "s.tmp", "a-b_C.9", "x"}
	names := []string{"node_modules", "NODE_MODULES", "Node_Modules ", "node_module", "cache", "CACHE", "cachf", "K", "K", "k", "ſ.tmp", "S.TMP", "s.tmK", "A-B_c.9", "a-b_c.8", "a_b-c.9", "X", "", "é", "\xff", "x\x00"}
	for _, w := range words {
		for _, n := range names {
			if got, want := equalFoldWord(n, w), strings.EqualFold(n, w); got != want {
				t.Errorf("equalFoldWord(%q, %q) = %v, want %v", n, w, got, want)
			}
		}
	}
}
