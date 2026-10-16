//go:build windows

package platform

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// RelaunchElevated is called only by the explicit UI action. Windows owns UAC;
// cancelling it leaves the existing application open.
func RelaunchElevated(path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString("-path " + windows.EscapeArg(path))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, args, dir, windows.SW_SHOWNORMAL)
}
