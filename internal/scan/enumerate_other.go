//go:build !windows

package scan

import (
	"context"
	"io"
	"os"
	"quarry/internal/index"
	"syscall"
)

func closeDirectory(uintptr) {}

// enumerate lists a folder by its path; dirHandle only satisfies the shared
// walk here, without a handle.
func enumerate(ctx context.Context, t task, emit func(*dirBatch) error) (*dirHandle, error) {
	t.parent.release()
	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return nil, readDirectory(ctx, f, emit)
}

func readDirectory(ctx context.Context, f *os.File, emit func(*dirBatch) error) error {
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
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}
func eligibleMFT(string) bool                                             { return false }
func scanMFT(context.Context, string, *Progress) (*index.Snapshot, error) { panic("unreachable") }
