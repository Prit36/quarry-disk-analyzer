//go:build windows

package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

type shellChildResult struct {
	Args     []string
	SID      string
	Elevated bool
}

// This subprocess has no UI. It checks the real Windows token and command-line
// boundary used by the reveal helper, including filenames needing quoting.
func TestShellChild(t *testing.T) {
	result := os.Getenv("QUARRY_SHELL_CHILD_RESULT")
	if result == "" {
		t.Skip("subprocess only")
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		os.Exit(3)
	}
	data, _ := json.Marshal(shellChildResult{Args: os.Args[3:], SID: user.User.Sid.String(), Elevated: token.IsElevated()})
	if err := os.WriteFile(result, data, 0600); err != nil {
		os.Exit(4)
	}
	if len(os.Args) > 2 && os.Args[2] == "fail" {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestShellProcessIntegration(t *testing.T) {
	if os.Getenv("QUARRY_SHELL_INTEGRATION") != "1" {
		t.Skip("set QUARRY_SHELL_INTEGRATION=1 to test against the desktop shell")
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.GetShellWindow(), &pid); err != nil {
		t.Fatal(err)
	}
	parent, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(parent)
	var token windows.Token
	if err := windows.OpenProcessToken(parent, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result.json")
	t.Setenv("QUARRY_SHELL_CHILD_RESULT", result)
	wantArgs := []string{`C:\folder with spaces\a,b.txt`, `C:\日本語\notes.txt`, `C:\trailing slash\`}
	for _, mode := range []string{"ok", "fail"} {
		args := append([]string{exe, "-test.run=^TestShellChild$", mode}, wantArgs...)
		child, err := startShellProcess(exe, args)
		if err != nil {
			t.Fatal(err)
		}
		err = waitShellProcess(child)
		windows.CloseHandle(child)
		if (err != nil) != (mode == "fail") {
			t.Fatalf("%s: exit error = %v", mode, err)
		}
		data, err := os.ReadFile(result)
		if err != nil {
			t.Fatal(err)
		}
		var got shellChildResult
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Args, wantArgs) {
			t.Fatalf("arguments = %#v", got.Args)
		}
		if got.SID != user.User.Sid.String() || got.Elevated != token.IsElevated() {
			t.Fatalf("helper identity differs from desktop shell: %+v", got)
		}
	}
}

func TestShowInExplorerEmpty(t *testing.T) {
	if err := ShowInExplorer(nil); err != nil {
		t.Fatal(err)
	}
}
