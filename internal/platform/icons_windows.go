//go:build windows

package platform

import (
	"image"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var stockIconOnce sync.Once
var stockIconImages map[string]image.Image

func StockIcons() map[string]image.Image {
	stockIconOnce.Do(func() {
		stockIconImages = make(map[string]image.Image)
		for name, identifier := range map[string]uintptr{"file": 0, "folder": 3, "drive": 8} {
			if icon := renderStockIcon(identifier, 48); icon != nil {
				stockIconImages[name] = icon
			}
		}
	})
	return stockIconImages
}

func renderStockIcon(identifier uintptr, size int) *image.RGBA {
	shell := windows.NewLazySystemDLL("shell32.dll")
	user := windows.NewLazySystemDLL("user32.dll")
	gdi := windows.NewLazySystemDLL("gdi32.dll")
	info := struct {
		Size                   uint32
		Icon                   windows.Handle
		SystemIndex, IconIndex int32
		Path                   [260]uint16
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	result, _, _ := shell.NewProc("SHGetStockIconInfo").Call(identifier, 0x100, uintptr(unsafe.Pointer(&info)))
	if result != 0 || info.Icon == 0 {
		return nil
	}
	defer user.NewProc("DestroyIcon").Call(uintptr(info.Icon))
	device, _, _ := user.NewProc("GetDC").Call(0)
	if device == 0 {
		return nil
	}
	defer user.NewProc("ReleaseDC").Call(0, device)
	memory, _, _ := gdi.NewProc("CreateCompatibleDC").Call(device)
	if memory == 0 {
		return nil
	}
	defer gdi.NewProc("DeleteDC").Call(memory)
	header := struct {
		Size                   uint32
		Width, Height          int32
		Planes, Bits           uint16
		Compression, ImageSize uint32
		XPixels, YPixels       int32
		Used, Important        uint32
	}{Size: 40, Width: int32(size), Height: -int32(size), Planes: 1, Bits: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := gdi.NewProc("CreateDIBSection").Call(device, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return nil
	}
	defer gdi.NewProc("DeleteObject").Call(bitmap)
	previous, _, _ := gdi.NewProc("SelectObject").Call(memory, bitmap)
	defer gdi.NewProc("SelectObject").Call(memory, previous)
	rendered, _, _ := user.NewProc("DrawIconEx").Call(memory, 0, 0, uintptr(info.Icon), uintptr(size), uintptr(size), 0, 0, 3)
	if rendered == 0 {
		return nil
	}
	source := unsafe.Slice((*byte)(bits), size*size*4)
	output := image.NewRGBA(image.Rect(0, 0, size, size))
	for offset := 0; offset < len(source); offset += 4 {
		output.Pix[offset], output.Pix[offset+1], output.Pix[offset+2], output.Pix[offset+3] = source[offset+2], source[offset+1], source[offset], source[offset+3]
	}
	return output
}
