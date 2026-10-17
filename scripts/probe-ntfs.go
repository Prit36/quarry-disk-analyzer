//go:build ignore

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

func main() {
	p, _ := windows.UTF16PtrFromString(`\\.\C:`)
	fmt.Println("Elevated", windows.GetCurrentProcessToken().IsElevated())
	for _, access := range []uint32{0, windows.GENERIC_READ} {
		h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		fmt.Printf("access=%x open=%v\n", access, err)
		if err != nil {
			continue
		}
		var output [2048]byte
		var got uint32
		err = windows.DeviceIoControl(h, 0x90064, nil, 0, &output[0], 128, &got, nil)
		fmt.Println("geometry", got, err)
		ref := uint64(5)
		err = windows.DeviceIoControl(h, 0x90068, (*byte)(unsafe.Pointer(&ref)), 8, &output[0], 2048, &got, nil)
		fmt.Println("record", got, err)
		windows.CloseHandle(h)
	}
}
