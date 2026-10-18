//go:build !windows

package platform

import "time"

// FormatDateTime writes t in local time. Off Windows there is no user date
// format to follow, so it uses ISO 8601 order.
func FormatDateTime(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}
