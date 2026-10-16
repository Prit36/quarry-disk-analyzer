//go:build windows

package scan

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"stratum/internal/index"
)

var getFileInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")
var directoryBuffers = sync.Pool{New: func() any { return make([]uint64, 8192) }}

func extendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}
func openDirectory(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// FILE_ID_BOTH_DIR_INFO returns size, allocated size and file identity in one
// directory syscall, avoiding stat/open per file. The OS fills a reusable 64 KiB
// buffer; only bounded batches and retained names cross to the coordinator.
func enumerate(ctx context.Context, path string, emit func([]entry) error) error {
	h, err := openDirectory(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	buffer := directoryBuffers.Get().([]uint64) // aligned as required by Windows
	defer directoryBuffers.Put(buffer)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&buffer[0])), len(buffer)*8)
	class := uintptr(11) // FileIdBothDirectoryRestartInfo
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, _, callErr := getFileInfo.Call(uintptr(h), class, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
		if ok == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_FILES) {
				return nil
			}
			if class == 11 && (errors.Is(callErr, windows.ERROR_INVALID_PARAMETER) || errors.Is(callErr, windows.ERROR_NOT_SUPPORTED) || errors.Is(callErr, windows.ERROR_INVALID_FUNCTION)) {
				return enumerateFallback(ctx, path, emit)
			}
			return callErr
		}
		class = 10
		entries := acquireEntries()
		for off := 0; ; {
			if off+104 > len(b) {
				return fmt.Errorf("truncated directory metadata")
			}
			r := b[off:]
			next := int(binary.LittleEndian.Uint32(r))
			length := int(binary.LittleEndian.Uint32(r[60:]))
			if length%2 != 0 || length > len(r)-104 {
				return fmt.Errorf("invalid directory name length")
			}
			name := string(utf16.Decode(unsafe.Slice((*uint16)(unsafe.Pointer(&r[104])), length/2)))
			if name != "." && name != ".." {
				attrs := binary.LittleEndian.Uint32(r[56:])
				flags := uint16(0)
				if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
					flags |= index.Directory
				}
				if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
					flags |= index.Reparse
				}
				e := entry{name: name, size: binary.LittleEndian.Uint64(r[40:]), allocated: binary.LittleEndian.Uint64(r[48:]), modified: unixTime(binary.LittleEndian.Uint64(r[24:])), fileID: binary.LittleEndian.Uint64(r[96:]), flags: flags}
				if flags&index.Directory != 0 {
					e.size, e.allocated = 0, 0
				}
				entries = append(entries, e)
				if len(entries) == 256 {
					if err := emit(entries); err != nil {
						return err
					}
					entries = acquireEntries()
				}
			}
			if next == 0 {
				break
			}
			if next < 104 || next > len(b)-off {
				return fmt.Errorf("invalid directory record offset")
			}
			off += next
		}
		if len(entries) > 0 {
			if err := emit(entries); err != nil {
				return err
			}
		} else {
			releaseEntries(entries)
		}
	}
}

// Some SMB/FAT providers do not implement file-ID enumeration. Fall back to
// bounded ReadDir plus FileStandardInfo; unknown allocation is disclosed.
func enumerateFallback(ctx context.Context, path string, emit func([]entry) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		dirs, readErr := f.ReadDir(256)
		entries := acquireEntries()
		for _, d := range dirs {
			info, err := d.Info()
			if err != nil {
				return err
			}
			e := entry{name: d.Name(), size: uint64(max(0, info.Size())), modified: info.ModTime().Unix()}
			if d.IsDir() {
				e.flags |= index.Directory
				e.size = 0
			}
			if info.Mode()&os.ModeSymlink != 0 {
				e.flags |= index.Reparse
			}
			p, err := windows.UTF16PtrFromString(extendedPath(filepath.Join(path, d.Name())))
			if err != nil {
				return err
			}
			h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err == nil {
				var standard [3]uint64
				ok, _, _ := getFileInfo.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&standard[0])), 24)
				if ok != 0 {
					e.allocated = standard[0]
				} else {
					e.flags |= index.Incomplete
				}
				var identity windows.ByHandleFileInformation
				if windows.GetFileInformationByHandle(h, &identity) == nil {
					e.fileID = uint64(identity.FileIndexHigh)<<32 | uint64(identity.FileIndexLow)
				}
				windows.CloseHandle(h)
			} else {
				e.flags |= index.Incomplete
			}
			if e.flags&index.Directory != 0 {
				e.allocated = 0
			}
			entries = append(entries, e)
		}
		if len(entries) > 0 {
			if err := emit(entries); err != nil {
				return err
			}
		} else {
			releaseEntries(entries)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
