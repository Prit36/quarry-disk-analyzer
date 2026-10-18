package platform

import (
	"strings"
	"testing"
	"time"
)

// The exact layout depends on the user's region (for example 10/18/2026 3:04 PM
// or 18/10/2026 15:04), so the test checks what every region shows.
func TestFormatDateTime(t *testing.T) {
	when := time.Date(2026, time.October, 18, 15, 4, 59, 0, time.Local)
	got := FormatDateTime(when)
	if !strings.Contains(got, "2026") {
		t.Fatalf("FormatDateTime = %q, want the year 2026", got)
	}
	if !strings.Contains(got, ":") {
		t.Fatalf("FormatDateTime = %q, want a time with a colon", got)
	}
	if strings.Contains(got, ":59") {
		t.Fatalf("FormatDateTime = %q, seconds should be left out", got)
	}
}
