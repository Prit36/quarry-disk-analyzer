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

// openRelative opens the folder name inside the open folder parent. Only
// that one component is looked up, under the parent's lock alone, where a
// full path takes every ancestor's again (and contends for the top ones
// with every other worker).
func openRelative(parent windows.Handle, name string) (windows.Handle, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return 0, windows.ERROR_INVALID_NAME
	}
	buffer := pathBuffers.Get().(*[]uint16)
	defer pathBuffers.Put(buffer)
	p := (*buffer)[:0]
	for _, r := range name {
		p = utf16.AppendRune(p, r)
	}
	*buffer = p
	if len(p) == 0 || len(p) > 32767 {
		return 0, windows.ERROR_INVALID_NAME
	}
	us := windows.NTUnicodeString{Length: uint16(len(p) * 2), MaximumLength: uint16(len(p) * 2), Buffer: &p[0]}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: &us, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(&h, directoryAccess|windows.SYNCHRONIZE, &oa, &iosb, nil, 0, shareAll, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		if st, ok := err.(windows.NTStatus); ok {
			return 0, st.Errno()
		}
		return 0, err
	}
	return h, nil
}

// openTask opens a task's folder: relative to its parent when it can,
// otherwise by its full path, as before. A refused relative open is final:
// the path would be refused the same way.
func openTask(t task) (windows.Handle, error) {
	if t.parent != nil && t.nameLen > 0 {
		h, err := openRelative(windows.Handle(t.parent.handle), t.name())
		if err == nil || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return h, err
		}
	}
	return openDirectory(t.path)
}

func closeDirectory(h uintptr) { windows.CloseHandle(windows.Handle(h)) }

// FILE_ID_BOTH_DIR_INFO returns size, allocated size and file identity in one
// directory syscall, avoiding stat/open per file. The OS fills a reusable 256 KiB
// buffer; names are decoded straight into the batch's arena, and only bounded
// batches cross to the coordinator. The folder stays open after it returns
// (the dirHandle), so its subfolders can be opened relative to it; the
// task's reference to its parent is given up once the folder is open.
func enumerate(ctx context.Context, t task, emit func(*dirBatch) error) (*dirHandle, error) {
	h, err := openTask(t)
	t.parent.release()
	if err != nil {
		return nil, err
	}
	dir := &dirHandle{handle: uintptr(h)}
	dir.refs.Store(1)
	subfolders, err := enumerateOpen(ctx, h, t.path, dir, emit)
	if !subfolders {
		// No task will open anything relative to it: close it here, on
		// this worker, rather than on the coordinator.
		windows.CloseHandle(h)
		return nil, err
	}
	return dir, err
}

// enumerateOpen lists the open folder h and reports whether it emitted any
// subfolder (only those can need the handle afterwards).
func enumerateOpen(ctx context.Context, h windows.Handle, path string, dir *dirHandle, emit func(*dirBatch) error) (subfolders bool, err error) {
	buffer := directoryBuffers.Get().(*[directoryBufferWords]uint64) // aligned as required by Windows
	defer directoryBuffers.Put(buffer)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&buffer[0])), len(buffer)*8)
	class := uintptr(11) // FileIdBothDirectoryRestartInfo
	for {
		if err := ctx.Err(); err != nil {
			return subfolders, err
		}
		ok, _, callErr := getFileInfo.Call(uintptr(h), class, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
		if ok == 0 {
			if errors.Is(callErr, windows.ERROR_NO_MORE_FILES) {
				return subfolders, nil
			}
			if class == 11 && (errors.Is(callErr, windows.ERROR_INVALID_PARAMETER) || errors.Is(callErr, windows.ERROR_NOT_SUPPORTED) || errors.Is(callErr, windows.ERROR_INVALID_FUNCTION)) {
				return true, enumerateFallback(ctx, path, dir, emit)
			}
			return subfolders, callErr
		}
		class = 10
		batch := acquireBatch()
		batch.dir = dir
		for off := 0; ; {
			if off+104 > len(b) {
				releaseBatch(batch)
				return subfolders, fmt.Errorf("truncated directory metadata")
			}
			r := b[off:]
			next := int(binary.LittleEndian.Uint32(r))
			length := int(binary.LittleEndian.Uint32(r[60:]))
			if length%2 != 0 || length > len(r)-104 {
				releaseBatch(batch)
				return subfolders, fmt.Errorf("invalid directory name length")
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
					subfolders = subfolders || flags&index.Reparse == 0
				}
				start := len(batch.names)
				batch.names = index.AppendUTF16(batch.names, raw)
				e.nameOff, e.nameLen = uint32(start), uint16(len(batch.names)-start)
				batch.entries = append(batch.entries, e)
				if len(batch.entries) == batchSize {
					if err := emit(batch); err != nil {
						return subfolders, err
					}
					batch = acquireBatch()
					batch.dir = dir
				}
			}
			if next == 0 {
				break
			}
			if next < 104 || next > len(b)-off {
				releaseBatch(batch)
				return subfolders, fmt.Errorf("invalid directory record offset")
			}
			off += next
		}
		if len(batch.entries) > 0 {
			if err := emit(batch); err != nil {
				return subfolders, err
			}
		} else {
			releaseBatch(batch)
		}
	}
}

// Some SMB/FAT providers do not implement file-ID enumeration. Fall back to
// bounded ReadDir plus FileStandardInfo; unknown allocation is disclosed.
func enumerateFallback(ctx context.Context, path string, dir *dirHandle, emit func(*dirBatch) error) error {
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
		batch.dir = dir
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
