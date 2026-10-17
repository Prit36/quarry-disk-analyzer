//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32                       = windows.NewLazySystemDLL("shell32.dll")
	ole32                         = windows.NewLazySystemDLL("ole32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	procILCreateFromPath          = shell32.NewProc("ILCreateFromPathW")
	procILFree                    = shell32.NewProc("ILFree")
	procILFindLastID              = shell32.NewProc("ILFindLastID")
	procSHOpenFolderAndSelectItem = shell32.NewProc("SHOpenFolderAndSelectItems")
	procSHObjectProperties        = shell32.NewProc("SHObjectProperties")
	procSHCreateDataObject        = shell32.NewProc("SHCreateDataObject")
	procSHMultiFileProperties     = shell32.NewProc("SHMultiFileProperties")
	procSHFileOperation           = shell32.NewProc("SHFileOperationW")
	procSHGetFileInfo             = shell32.NewProc("SHGetFileInfoW")
	procSHChangeNotify            = shell32.NewProc("SHChangeNotify")
	procCoInitializeEx            = ole32.NewProc("CoInitializeEx")
	procAttachConsole             = kernel32.NewProc("AttachConsole")
)

func coInit() { procCoInitializeEx.Call(0, 0x2) } // COINIT_APARTMENTTHREADED; S_FALSE when already initialized

func pidl(path string) (uintptr, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	r, _, _ := procILCreateFromPath.Call(uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return 0, fmt.Errorf("%s: not found", path)
	}
	return r, nil
}

// ShowInExplorer opens the folder containing the paths and selects them.
// Paths in different folders open one window per folder; a drive root opens
// the drive itself.
//
// An elevated process cannot hand SHOpenFolderAndSelectItems to the user's
// (unelevated) Explorer, so the shell opens the window inside this process,
// on a thread without a message loop, where it never appears. There, and
// whenever the shell call fails, Explorer is started as its own process.
func ShowInExplorer(paths []string) error {
	coInit()
	groups := map[string][]string{}
	var order []string
	for _, p := range paths {
		dir := filepath.Dir(p)
		if dir == p {
			// A drive root has no parent folder to select it in.
			if err := explorerProcess(p, false); err != nil {
				return err
			}
			continue
		}
		if _, ok := groups[dir]; !ok {
			order = append(order, dir)
		}
		groups[dir] = append(groups[dir], p)
	}
	elevated := Elevated()
	var firstErr error
	for _, dir := range order {
		var err error
		if !elevated {
			err = selectIn(dir, groups[dir])
		}
		if elevated || err != nil {
			err = explorerProcess(groups[dir][0], true)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OpenFolder opens a folder in a File Explorer window of its own process, so
// the window belongs to the user's shell even when Quarry is elevated.
func OpenFolder(path string) error { return explorerProcess(path, false) }

// explorerProcess starts explorer.exe on path, selecting it in its folder.
// Explorer passes the request to the running shell, at the user's level.
func explorerProcess(path string, sel bool) error {
	exe := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	if os.Getenv("SystemRoot") == "" {
		exe = "explorer.exe"
	}
	args := `"` + path + `"`
	if sel {
		args = `/select,"` + path + `"`
	}
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + exe + `" ` + args}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Explorer could not be started: %w", err)
	}
	go cmd.Wait() // explorer.exe exits at once with code 1; reap it
	return nil
}

func selectIn(dir string, items []string) error {
	folder, err := pidl(dir)
	if err != nil {
		return err
	}
	defer procILFree.Call(folder)
	var full, children []uintptr
	defer func() {
		for _, p := range full {
			procILFree.Call(p)
		}
	}()
	for _, item := range items {
		p, err := pidl(item)
		if err != nil {
			continue
		}
		full = append(full, p)
		last, _, _ := procILFindLastID.Call(p)
		children = append(children, last)
	}
	if len(children) == 0 {
		return fmt.Errorf("%s no longer exists", items[0])
	}
	hr, _, _ := procSHOpenFolderAndSelectItem.Call(folder, uintptr(len(children)), uintptr(unsafe.Pointer(&children[0])), 0)
	if int32(hr) < 0 {
		return fmt.Errorf("Explorer could not open %s (0x%x)", dir, uint32(hr))
	}
	return nil
}

var iidDataObject = windows.GUID{Data1: 0x0000010e, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

// ShowProperties opens the Windows Properties sheet. Several items in one
// folder share a single multi-item sheet. It must run on the UI thread,
// whose message loop drives the modeless sheet.
func ShowProperties(owner uintptr, paths []string) error {
	coInit()
	if len(paths) == 0 {
		return nil
	}
	dir := filepath.Dir(paths[0])
	same := len(paths) > 1
	for _, p := range paths[1:] {
		if !strings.EqualFold(filepath.Dir(p), dir) {
			same = false
		}
	}
	if same {
		if err := multiProperties(dir, paths); err == nil {
			return nil
		}
	}
	p, err := windows.UTF16PtrFromString(paths[0])
	if err != nil {
		return err
	}
	ok, _, _ := procSHObjectProperties.Call(owner, 0x2 /* SHOP_FILEPATH */, uintptr(unsafe.Pointer(p)), 0)
	if ok == 0 {
		return fmt.Errorf("Properties are unavailable for %s", paths[0])
	}
	return nil
}

func multiProperties(dir string, paths []string) error {
	folder, err := pidl(dir)
	if err != nil {
		return err
	}
	defer procILFree.Call(folder)
	var full, children []uintptr
	defer func() {
		for _, p := range full {
			procILFree.Call(p)
		}
	}()
	for _, item := range paths {
		p, err := pidl(item)
		if err != nil {
			return err
		}
		full = append(full, p)
		last, _, _ := procILFindLastID.Call(p)
		children = append(children, last)
	}
	var object unsafe.Pointer
	hr, _, _ := procSHCreateDataObject.Call(folder, uintptr(len(children)), uintptr(unsafe.Pointer(&children[0])), 0, uintptr(unsafe.Pointer(&iidDataObject)), uintptr(unsafe.Pointer(&object)))
	if int32(hr) < 0 || object == nil {
		return fmt.Errorf("data object: 0x%x", uint32(hr))
	}
	defer func() {
		vtbl := *(**[3]uintptr)(object)
		syscall.SyscallN(vtbl[2], uintptr(object)) // IUnknown::Release
	}()
	hr, _, _ = procSHMultiFileProperties.Call(uintptr(object), 0)
	if int32(hr) < 0 {
		return fmt.Errorf("properties: 0x%x", uint32(hr))
	}
	return nil
}

// DeleteItems deletes paths through the Windows shell. With recycle, items
// go to the Recycle Bin (Windows warns before deleting permanently anything
// the bin cannot hold); otherwise they are removed permanently. Quarry asks
// for confirmation itself beforehand. It runs on its own OS thread because
// the shell shows a progress window for long operations.
func DeleteItems(owner uintptr, paths []string, recycle bool) (aborted bool, err error) {
	if len(paths) == 0 {
		return false, nil
	}
	type shFileOp struct {
		Hwnd                 uintptr
		Func                 uint32
		From                 *uint16
		To                   *uint16
		Flags                uint16
		AnyOperationsAborted int32
		NameMappings         uintptr
		ProgressTitle        *uint16
	}
	const (
		foDelete          = 3
		fofNoConfirmation = 0x0010
		fofAllowUndo      = 0x0040
		fofWantNukeWarn   = 0x4000
	)
	var from []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return false, err
		}
		from = append(from, u...) // includes the terminating NUL
	}
	from = append(from, 0)
	flags := uint16(fofNoConfirmation)
	if recycle {
		flags |= fofAllowUndo | fofWantNukeWarn
	}
	var wg sync.WaitGroup
	var result uintptr
	op := shFileOp{Hwnd: owner, Func: foDelete, From: &from[0], Flags: flags}
	wg.Add(1)
	go func() {
		defer wg.Done()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		coInit()
		result, _, _ = procSHFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	}()
	wg.Wait()
	if op.AnyOperationsAborted != 0 {
		return true, nil
	}
	if result != 0 {
		return false, fmt.Errorf("Windows could not delete the selection (error 0x%x)", result)
	}
	return false, nil
}

// ValidateName checks a new file or folder name against Windows rules.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("A name is required.")
	}
	if strings.ContainsAny(name, `\/:*?"<>|`) {
		return errors.New(`A file name can't contain any of the following characters: \ / : * ? " < > |`)
	}
	for _, r := range name {
		if r < 32 {
			return errors.New("A file name can't contain control characters.")
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return errors.New("A file name can't end with a period or a space.")
	}
	base := strings.ToUpper(strings.TrimSpace(strings.SplitN(name, ".", 2)[0]))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("%s is a reserved device name.", base)
	}
	if len(name) > 255 {
		return errors.New("The name is too long.")
	}
	return nil
}

// RenameItem renames path within its folder, never replacing an existing
// entry, and notifies Explorer.
func RenameItem(path, newName string) (string, error) {
	if err := ValidateName(newName); err != nil {
		return "", err
	}
	target := filepath.Join(filepath.Dir(path), newName)
	from, err := windows.UTF16PtrFromString(extended(path))
	if err != nil {
		return "", err
	}
	to, err := windows.UTF16PtrFromString(extended(target))
	if err != nil {
		return "", err
	}
	// Renaming only the case of a name targets the same entry.
	if !strings.EqualFold(path, target) {
		if _, err := os.Lstat(target); err == nil {
			return "", fmt.Errorf("%s already exists in this folder.", newName)
		}
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return "", err
	}
	notifyRename(path, target)
	return target, nil
}

func notifyRename(from, to string) {
	a, _ := windows.UTF16PtrFromString(from)
	b, _ := windows.UTF16PtrFromString(to)
	procSHChangeNotify.Call(0x00000001 /* SHCNE_RENAMEITEM */, 0x0005 /* SHCNF_PATHW */, uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)))
}

func extended(path string) string {
	if strings.HasPrefix(path, `\\?\`) || len(path) < 248 {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + path[2:]
	}
	return `\\?\` + path
}

var typeNames sync.Map

// FileTypeName returns Explorer's description of an extension, such as
// "PNG File" or "Text Document". It reads the registry through the shell and
// never touches a file.
func FileTypeName(ext string) string {
	if ext == "" || ext[0] != '.' {
		return ""
	}
	if v, ok := typeNames.Load(strings.ToLower(ext)); ok {
		return v.(string)
	}
	var info struct {
		Icon        windows.Handle
		IconIndex   int32
		Attributes  uint32
		DisplayName [260]uint16
		TypeName    [80]uint16
	}
	p, _ := windows.UTF16PtrFromString(ext)
	name := ""
	r, _, _ := procSHGetFileInfo.Call(uintptr(unsafe.Pointer(p)), 0x80 /* FILE_ATTRIBUTE_NORMAL */, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0x400|0x10 /* SHGFI_TYPENAME|SHGFI_USEFILEATTRIBUTES */)
	if r != 0 {
		name = windows.UTF16ToString(info.TypeName[:])
	}
	typeNames.Store(strings.ToLower(ext), name)
	return name
}

const explorerVerb = `Quarry.Analyze`

var verbKeys = []string{`Software\Classes\Directory\shell\` + explorerVerb, `Software\Classes\Drive\shell\` + explorerVerb}

// ExplorerVerbInstalled reports whether "Analyze with Quarry" is registered
// for the current user.
func ExplorerVerbInstalled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, verbKeys[0]+`\command`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("")
	exe, _ := os.Executable()
	return err == nil && strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// InstallExplorerVerb registers "Analyze with Quarry" on folders and drives
// for the current user only (HKCU). Windows 11 lists it under Show more options.
func InstallExplorerVerb() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for _, key := range verbKeys {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue("", "Analyze with Quarry")
		if err == nil {
			err = k.SetStringValue("Icon", exe)
		}
		k.Close()
		if err != nil {
			return err
		}
		c, _, err := registry.CreateKey(registry.CURRENT_USER, key+`\command`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = c.SetStringValue("", `"`+exe+`" "%V"`)
		c.Close()
		if err != nil {
			return err
		}
	}
	procSHChangeNotify.Call(0x08000000 /* SHCNE_ASSOCCHANGED */, 0, 0, 0)
	return nil
}

// RemoveExplorerVerb deletes only the keys InstallExplorerVerb created.
func RemoveExplorerVerb() error {
	for _, key := range verbKeys {
		for _, k := range []string{key + `\command`, key} {
			if err := registry.DeleteKey(registry.CURRENT_USER, k); err != nil && !errors.Is(err, registry.ErrNotExist) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				return err
			}
		}
	}
	procSHChangeNotify.Call(0x08000000, 0, 0, 0)
	return nil
}

// AttachParentConsole connects a GUI-subsystem executable to the console it
// was started from, so command-line output is visible. It reports success.
// Output already redirected to a file or pipe is left alone.
func AttachParentConsole() bool {
	redirected := func(std uint32) bool {
		h, err := windows.GetStdHandle(std)
		if err != nil || h == 0 || h == windows.InvalidHandle {
			return false
		}
		t, _ := windows.GetFileType(h)
		return t == windows.FILE_TYPE_DISK || t == windows.FILE_TYPE_PIPE
	}
	outRedirected, errRedirected := redirected(windows.STD_OUTPUT_HANDLE), redirected(windows.STD_ERROR_HANDLE)
	r, _, _ := procAttachConsole.Call(^uintptr(0)) // ATTACH_PARENT_PROCESS
	if r == 0 {
		return outRedirected
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		if !outRedirected {
			os.Stdout = out
		}
		if !errRedirected {
			os.Stderr = out
		}
	}
	return true
}
