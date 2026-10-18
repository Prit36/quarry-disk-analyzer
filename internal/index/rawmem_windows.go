//go:build windows

package index

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// rawAlloc returns n zeroed bytes outside the Go heap, for builder chunks:
// they hold no pointers, and rawFree returns them to Windows the moment
// their contents are copied, without waiting for a collection.
func rawAlloc(n int) []byte {
	addr, err := windows.VirtualAlloc(0, uintptr(n), windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil || addr == 0 {
		panic("index: cannot allocate a builder chunk: out of memory")
	}
	return unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), n)
}

func rawFree(b []byte) {
	if cap(b) > 0 {
		windows.VirtualFree(uintptr(unsafe.Pointer(unsafe.SliceData(b))), 0, windows.MEM_RELEASE)
	}
}
