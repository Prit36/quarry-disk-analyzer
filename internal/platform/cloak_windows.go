//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Cloak hides or reveals a window through the compositor. A cloaked window
// is shown as far as Windows is concerned, so it paints its first frame,
// but none of it reaches the screen until it is uncloaked.
func Cloak(hwnd uintptr, on bool) {
	var v int32
	if on {
		v = 1
	}
	_ = windows.DwmSetWindowAttribute(windows.HWND(hwnd), windows.DWMWA_CLOAK, unsafe.Pointer(&v), uint32(unsafe.Sizeof(v)))
}
