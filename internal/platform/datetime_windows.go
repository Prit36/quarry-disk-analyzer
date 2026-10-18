//go:build windows

package platform

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetDateFormatEx = kernel32.NewProc("GetDateFormatEx")
	procGetTimeFormatEx = kernel32.NewProc("GetTimeFormatEx")
)

// systemTime matches the Win32 SYSTEMTIME structure.
type systemTime struct {
	Year, Month, DayOfWeek, Day, Hour, Minute, Second, Milliseconds uint16
}

const (
	dateShortDate = 0x1 // DATE_SHORTDATE
	timeNoSeconds = 0x2 // TIME_NOSECONDS
)

// FormatDateTime writes t in local time with the user's Windows short date and
// short time formats, the same ones Explorer shows, such as "10/18/2026 3:04 PM"
// or "18/10/2026 15:04". The formats follow the region settings.
func FormatDateTime(t time.Time) string {
	t = t.Local()
	st := systemTime{
		Year:      uint16(t.Year()),
		Month:     uint16(t.Month()),
		DayOfWeek: uint16(t.Weekday()),
		Day:       uint16(t.Day()),
		Hour:      uint16(t.Hour()),
		Minute:    uint16(t.Minute()),
		Second:    uint16(t.Second()),
	}
	var date, clock [100]uint16
	// A NULL locale means the user's default locale.
	n, _, _ := procGetDateFormatEx.Call(0, dateShortDate, uintptr(unsafe.Pointer(&st)), 0, uintptr(unsafe.Pointer(&date[0])), uintptr(len(date)), 0)
	m, _, _ := procGetTimeFormatEx.Call(0, timeNoSeconds, uintptr(unsafe.Pointer(&st)), 0, uintptr(unsafe.Pointer(&clock[0])), uintptr(len(clock)), 0)
	if n == 0 || m == 0 {
		return t.Format("2006-01-02 15:04")
	}
	return windows.UTF16ToString(date[:]) + " " + windows.UTF16ToString(clock[:])
}
