//go:build windows

package platform

import (
	"golang.org/x/sys/windows"
	"strings"
)

type Drive struct {
	Path, Name, FileSystem string
	Total, Free            uint64
}

func Drives() []Drive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var result []Drive
	for i := uint(0); i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		path := string(rune('A'+i)) + `:\`
		ptr, _ := windows.UTF16PtrFromString(path)
		kind := windows.GetDriveType(ptr)
		if kind != windows.DRIVE_FIXED && kind != windows.DRIVE_REMOVABLE {
			continue
		}
		d := Drive{Path: path, Name: "Local disk"}
		var name, fs [261]uint16
		var serial, maxComponent, flags uint32
		if windows.GetVolumeInformation(ptr, &name[0], 261, &serial, &maxComponent, &flags, &fs[0], 261) == nil {
			if n := windows.UTF16ToString(name[:]); strings.TrimSpace(n) != "" {
				d.Name = n
			}
			d.FileSystem = windows.UTF16ToString(fs[:])
		}
		var available uint64
		if windows.GetDiskFreeSpaceEx(ptr, &available, &d.Total, &d.Free) == nil {
			result = append(result, d)
		}
	}
	return result
}
