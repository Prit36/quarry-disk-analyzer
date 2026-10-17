//go:build !windows

package scan

import (
	"context"
	"io"
	"os"
	"quarry/internal/index"
	"syscall"
)

func enumerate(ctx context.Context, path string, emit func([]entry) error) error {
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
			if info.IsDir() {
				e.flags |= index.Directory
				e.size = 0
			}
			if info.Mode()&os.ModeSymlink != 0 {
				e.flags |= index.Reparse
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				e.allocated = uint64(max(0, st.Blocks)) * 512
				e.fileID = uint64(st.Ino) ^ uint64(st.Dev)*0x9e3779b97f4a7c15
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
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}
func eligibleMFT(string) bool                                             { return false }
func scanMFT(context.Context, string, *Progress) (*index.Snapshot, error) { panic("unreachable") }
