//go:build !windows

package platform

import "syscall"

type Drive struct {
	Path, Name, FileSystem string
	Total, Free            uint64
}

func Drives() []Drive {
	var stat syscall.Statfs_t
	if syscall.Statfs("/", &stat) != nil {
		return nil
	}
	return []Drive{{Path: "/", Name: "System", FileSystem: "Local", Total: uint64(stat.Blocks) * uint64(stat.Bsize), Free: uint64(stat.Bavail) * uint64(stat.Bsize)}}
}
