package desktop

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"quarry/internal/index"
	"quarry/internal/platform"
)

const shotGiB = 1 << 30

// showcaseSnapshot is a made-up drive, so no real names reach the README.
func showcaseSnapshot() *index.Snapshot {
	s := index.New(`C:\`)
	mod := time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC).Unix()
	dir := func(p index.ID, name string) index.ID { return s.Add(p, name, 0, 0, mod, index.Directory) }
	files := func(p index.ID, base, ext string, n int, total float64) {
		for i := 0; i < n; i++ {
			w := float64(n-i) / float64(n*(n+1)/2)
			size := uint64(total * shotGiB * w)
			s.Add(p, fmt.Sprintf("%s%02d.%s", base, i+1, ext), size, size+4096, mod-int64(i)*86400*3, 0)
		}
	}
	users := dir(0, "Users")
	me := dir(users, "Alex")
	videos := dir(me, "Videos")
	files(videos, "Screen Recording ", "mp4", 14, 61)
	files(videos, "Project Export ", "mov", 4, 18)
	dl := dir(me, "Downloads")
	files(dl, "setup-", "exe", 9, 7.5)
	files(dl, "photos-backup-", "zip", 11, 14)
	files(dl, "ubuntu-26.04-desktop-amd64-", "iso", 2, 11)
	local := dir(dir(me, "AppData"), "Local")
	files(dir(local, "BrowserCache"), "cache_", "dat", 40, 19)
	files(dir(local, "Logs"), "session-", "log", 25, 3.2)
	docs := dir(me, "Documents")
	files(docs, "Report ", "pdf", 30, 2.4)
	files(docs, "Notes ", "docx", 20, 0.6)
	files(dir(me, "Pictures"), "IMG_", "jpg", 60, 9.5)
	win := dir(0, "Windows")
	sys32 := dir(win, "System32")
	files(sys32, "lib", "dll", 80, 9)
	files(sys32, "drv", "sys", 20, 1.1)
	files(dir(win, "WinSxS"), "amd64_component_", "dll", 60, 11)
	files(dir(win, "Installer"), "pkg", "msi", 15, 4.2)
	pf := dir(0, "Program Files")
	files(dir(pf, "Games"), "data", "pak", 12, 46)
	files(dir(pf, "Studio"), "module", "dll", 30, 5.4)
	files(dir(dir(0, "dev"), "node_modules"), "chunk", "js", 50, 2.8)
	s.Add(0, "hiberfil.sys", 13636000000, 13636000000, mod, 0)
	s.Add(0, "pagefile.sys", 8*shotGiB, 8*shotGiB, mod, 0)
	s.Finalize()
	s.Engine = "NTFS MFT"
	s.Elapsed = 2140 * time.Millisecond
	return s
}

// TestShowcaseScreenshots renders the README's screenshots from sample data
// into the folder QUARRY_SHOTS names; it is skipped otherwise:
//
//	QUARRY_SHOTS=docs/screenshots go test ./internal/desktop -run Showcase
func TestShowcaseScreenshots(t *testing.T) {
	out := os.Getenv("QUARRY_SHOTS")
	if out == "" {
		t.Skip("QUARRY_SHOTS not set")
	}
	shot := func(tt *ui.Tester, name string) {
		f, err := os.Create(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, theme := range []string{"dark", "light"} {
		t.Setenv("QUARRY_CONFIG_DIR", t.TempDir())
		a := newApp()
		a.prefs.Theme = map[string]string{"dark": "Dark", "light": "Light"}[theme]
		a.setBase(showcaseSnapshot(), "", nil, false)
		tt := ui.NewTester(a.view, 1280, 800)
		tt.SetDark(theme == "dark")
		tt.SetScale(1.5)
		drives := []platform.Drive{{Path: `C:\`, Name: "Windows", FileSystem: "NTFS", Total: 298 * shotGiB, Free: 46 * shotGiB}}
		a.drives = drives
		tt.Frame()
		a.tree.selectOnly(1)
		a.tree.keyRight() // expand Users
		a.tree.keyRight() // to Alex
		a.tree.keyRight() // expand Alex
		a.focused["tree"] = true
		settle(tt, func() bool { return !a.ext.busy && len(a.ext.bySize) > 0 && !a.tm.busy })
		a.drives = drives
		tt.Frame()
		shot(tt, "tree-"+theme+".png")

		if theme == "dark" {
			a.search = "*.mp4"
			a.setViewMode(viewFiles)
			a.files.invalidate()
			settle(tt, func() bool { return !a.files.busy && len(a.files.ids) > 0 && !a.tm.busy })
			a.focused["files"] = true
			a.drives = drives
			tt.Frame()
			shot(tt, "search-dark.png")
		}
	}
}
