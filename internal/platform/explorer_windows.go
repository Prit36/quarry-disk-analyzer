//go:build windows

package platform

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

// Foreground permission follows the user's reveal command from Quarry to the
// helper and then to Explorer. A denied grant is harmless: Windows may deny it
// when the user has already switched to another app.
func allowExplorerForeground() {
	var pid uint32
	if shell := windows.GetShellWindow(); shell != 0 {
		if _, err := windows.GetWindowThreadProcessId(shell, &pid); err == nil && pid != 0 {
			procAllowSetForegroundWindow.Call(uintptr(pid))
		}
	}
}

// revealFromShell runs only the reveal command in a copy of this executable
// with Explorer's token. No persistent broker, console, or Quarry window is
// created. The helper rejects elevated execution, preventing recursive launch
// when the desktop itself is elevated.
func revealFromShell(paths []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{exe, "--reveal-in-explorer"}, paths...)
	process, err := startShellProcess(exe, args)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return waitShellProcess(process)
}

// startShellProcess uses the documented Explorer-parent technique to inherit
// the desktop user's token. An explicit application path and Windows argument
// quoting keep filenames out of command interpreters. No handles are inherited.
// https://devblogs.microsoft.com/oldnewthing/20190425-00/?p=102443
func startShellProcess(exe string, args []string) (windows.Handle, error) {
	shell := windows.GetShellWindow()
	if shell == 0 {
		return 0, fmt.Errorf("the desktop shell is unavailable")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(shell, &pid); err != nil {
		return 0, err
	}
	parent, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(parent)
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PARENT_PROCESS, unsafe.Pointer(&parent), unsafe.Sizeof(parent)); err != nil {
		return 0, err
	}
	application, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return 0, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var info windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(application, command, nil, nil, false, flags, nil, nil, &startup.StartupInfo, &info); err != nil {
		return 0, err
	}
	defer windows.CloseHandle(info.Thread)
	// Explorer is the nominated parent, so the helper does not automatically
	// inherit foreground permission from the Quarry window the user clicked.
	// Grant it before resuming, so a fast helper cannot race ahead of the grant.
	procAllowSetForegroundWindow.Call(uintptr(info.ProcessId))
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		// This child has not run any application code. Do not leave it suspended
		// if startup failed; the caller can use the original Explorer fallback.
		windows.TerminateProcess(info.Process, 1)
		windows.CloseHandle(info.Process)
		return 0, err
	}
	return info.Process, nil
}

func waitShellProcess(process windows.Handle) error {
	if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("Explorer helper exited with code %d", code)
	}
	return nil
}
