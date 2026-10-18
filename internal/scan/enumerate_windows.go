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
	"quarry/internal/index"
)

var getFileInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")
// directoryBufferWords sizes the query buffer at 256 KiB: a large folder takes a
// quarter of the queries, and small folders fill only what they use. Pooled as a
// pointer to an array, so taking one allocates nothing.
const directoryBufferWords = 32768

var directoryBuffers = sync.Pool{New: func() any { return new([directoryBufferWords]uint64) }}

func extendedPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

const directoryAccess = windows.FILE_LIST_DIRECTORY
const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
const directoryFlags = windows.FILE_FLAG_BACKUP_SEMANTICS | windows.FILE_FLAG_OPEN_REPARSE_POINT

var pathBuffers = sync.Pool{New: func() any { b := make([]uint16, 0, 512); return &b }}

// openDirectory encodes the extended-length path into a pooled UTF-16
// buffer: one folder open allocates nothing.
func openDirectory(path string) (windows.Handle, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return 0, windows.ERROR_INVALID_NAME
	}
	buffer := pathBuffers.Get().(*[]uint16)
	defer pathBuffers.Put(buffer)
	p := (*buffer)[:0]
	switch {
	case strings.HasPrefix(path, `\\?\`):
	case strings.HasPrefix(path, `\\`):
		p = utf16.AppendRune(append(p, '\\', '\\', '?', '\\', 'U', 'N', 'C'), '\\')
		path = path[2:]
	default:
		p = append(p, '\\', '\\', '?', '\\')
	}
	for _, r := range path {
		p = utf16.AppendRune(p, r)
	}
	p = append(p, 0)
	*buffer = p
	return windows.CreateFile(&p[0], directoryAccess, shareAll, nil, windows.OPEN_EXISTING, directoryFlags, 0)
}

// FILE_ID_BOTH_DIR_INFO returns size, allocated size and file identity in one
// directory syscall, avoiding stat/open per file. The OS fills a reusable 64 KiB
// buffer; names are decoded straight into the batch's arena, and only bounded
// batches cross to the coordinator.
func enumerate(ctx context.Context, path string, emit func(*dirBatch) error) error {
	h, err := openDirectory(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	buffer := directoryBuffers.Get().(*[directoryBufferWords]uint64) // aligned as required by Windows
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
		batch := acquireBatch()
		for off := 0; ; {
			if off+104 > len(b) {
				releaseBatch(batch)
				return fmt.Errorf("truncated directory metadata")
			}
			r := b[off:]
			next := int(binary.LittleEndian.Uint32(r))
			length := int(binary.LittleEndian.Uint32(r[60:]))
			if length%2 != 0 || length > len(r)-104 {
				releaseBatch(batch)
				return fmt.Errorf("invalid directory name length")
			}
			raw := r[104 : 104+length]
			// "." and ".." are the only names starting with a dot that are
			// one or two dots long.
			dots := (length == 2 || length == 4) && raw[0] == '.' && raw[1] == 0 && (length == 2 || raw[2] == '.' && raw[3] == 0)
			if !dots {
				attrs := binary.LittleEndian.Uint32(r[56:])
				flags := uint16(0)
				if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
					flags |= index.Directory
				}
				if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
					flags |= index.Reparse
				}
				e := entry{size: binary.LittleEndian.Uint64(r[40:]), allocated: binary.LittleEndian.Uint64(r[48:]), modified: unixTime(binary.LittleEndian.Uint64(r[24:])), fileID: binary.LittleEndian.Uint64(r[96:]), flags: flags}
				if flags&index.Directory != 0 {
					e.size, e.allocated = 0, 0
				}
				start := len(batch.names)
				batch.names = index.AppendUTF16(batch.names, raw)
				e.nameOff, e.nameLen = uint32(start), uint16(len(batch.names)-start)
				batch.entries = append(batch.entries, e)
				if len(batch.entries) == batchSize {
					if err := emit(batch); err != nil {
						return err
					}
					batch = acquireBatch()
				}
			}
			if next == 0 {
				break
			}
			if next < 104 || next > len(b)-off {
				releaseBatch(batch)
				return fmt.Errorf("invalid directory record offset")
			}
			off += next
		}
		if len(batch.entries) > 0 {
			if err := emit(batch); err != nil {
				return err
			}
		} else {
			releaseBatch(batch)
		}
	}
}

// Some SMB/FAT providers do not implement file-ID enumeration. Fall back to
// bounded ReadDir plus FileStandardInfo; unknown allocation is disclosed.
func enumerateFallback(ctx context.Context, path string, emit func(*dirBatch) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		dirs, readErr := f.ReadDir(batchSize)
		batch := acquireBatch()
		for _, d := range dirs {
			info, err := d.Info()
			if err != nil {
				releaseBatch(batch)
				return err
			}
			e := entry{size: uint64(max(0, info.Size())), modified: info.ModTime().Unix()}
			if d.IsDir() {
				e.flags |= index.Directory
				e.size = 0
			}
			if info.Mode()&os.ModeSymlink != 0 {
				e.flags |= index.Reparse
			}
			p, err := windows.UTF16PtrFromString(extendedPath(filepath.Join(path, d.Name())))
			if err != nil {
				releaseBatch(batch)
				return err
			}
			h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, shareAll, nil, windows.OPEN_EXISTING, directoryFlags, 0)
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
			batch.add(e, d.Name())
		}
		if len(batch.entries) > 0 {
			if err := emit(batch); err != nil {
				return err
			}
		} else {
			releaseBatch(batch)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
