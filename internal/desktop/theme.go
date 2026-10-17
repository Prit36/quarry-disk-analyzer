package desktop

import (
	"embed"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"quarry/internal/platform"
)

//go:embed assets/*.svg
var iconFiles embed.FS

var icons = func() map[string]*ui.SVG {
	m := map[string]*ui.SVG{}
	entries, _ := iconFiles.ReadDir("assets")
	for _, e := range entries {
		b, _ := iconFiles.ReadFile("assets/" + e.Name())
		m[strings.TrimSuffix(e.Name(), ".svg")] = ui.MustParseSVG(b)
	}
	return m
}()

// Shell stock icons (folder, file, drive) so rows look like Explorer's.
var shellIcons = func() map[string]*ui.Bitmap {
	m := map[string]*ui.Bitmap{}
	for name, img := range platform.StockIcons() {
		m[name] = ui.NewBitmap(img)
	}
	return m
}()

// palette holds the colors that are not part of ui.Theme.
type palette struct {
	chrome    ui.Color // toolbar, headers, status bar
	grid      ui.Color // table background
	separator ui.Color
	bar       ui.Color // percentage bars
	barTrack  ui.Color
	unfocused ui.Color // selection while the table is not focused
	folder    ui.Color
}

func (a *App) dark() bool {
	switch a.prefs.Theme {
	case "Dark":
		return true
	case "Light":
		return false
	}
	return mygo.Theme.IsDark()
}

// appTheme is a restrained Windows 11 look: neutral greys, the system accent
// only for selection and focus, compact metrics (24 DIP rows at 100% zoom).
func (a *App) appTheme() (*ui.Theme, palette) {
	var t ui.Theme
	var p palette
	if a.dark() {
		t = *ui.DarkTheme()
		t.Background = ui.Hex("#1C1C1C")
		t.Surface = ui.Hex("#2D2D2D")
		t.SurfaceHover = ui.Hex("#2A2D31")
		t.SurfacePressed = ui.Hex("#333333")
		t.Border = ui.Hex("#3C3C3C")
		t.Text = ui.Hex("#E8E8E8")
		t.TextMuted = ui.Hex("#A0A0A0")
		t.Accent = ui.Hex("#2F6FB3")
		t.AccentHover = ui.Hex("#3A7CC2")
		t.AccentPressed = ui.Hex("#28609C")
		t.AccentText = ui.Hex("#FFFFFF")
		t.Selection = ui.Hex("#264F78")
		t.Focus = ui.Hex("#60A5FA")
		t.Warning = ui.Hex("#E3B341")
		t.Danger = ui.Hex("#F07070")
		t.Scrollbar = ui.Hex("#5A5A5A")
		p = palette{chrome: ui.Hex("#252526"), grid: ui.Hex("#1C1C1C"), separator: ui.Hex("#333333"), bar: ui.Hex("#5B8DC9"), barTrack: ui.Hex("#2E2E2E"), unfocused: ui.Hex("#37414D"), folder: ui.Hex("#E8B64C")}
	} else {
		t = *ui.LightTheme()
		t.Background = ui.Hex("#FFFFFF")
		t.Surface = ui.Hex("#FBFBFB")
		t.SurfaceHover = ui.Hex("#E5F3FF")
		t.SurfacePressed = ui.Hex("#CCE8FF")
		t.Border = ui.Hex("#D5D5D5")
		t.Text = ui.Hex("#1A1A1A")
		t.TextMuted = ui.Hex("#5E5E5E")
		t.Accent = ui.Hex("#CCE8FF")
		t.AccentHover = ui.Hex("#B8DEFF")
		t.AccentPressed = ui.Hex("#99D1FF")
		t.AccentText = ui.Hex("#000000")
		t.Selection = ui.Hex("#99C9F5")
		t.Focus = ui.Hex("#0067C0")
		t.Warning = ui.Hex("#9D5D00")
		t.Danger = ui.Hex("#C42B1C")
		t.Scrollbar = ui.Hex("#B0B0B0")
		p = palette{chrome: ui.Hex("#F3F3F3"), grid: ui.Hex("#FFFFFF"), separator: ui.Hex("#E0E0E0"), bar: ui.Hex("#4F8BD6"), barTrack: ui.Hex("#E8E8E8"), unfocused: ui.Hex("#E3E3E3"), folder: ui.Hex("#DDA028")}
	}
	t.Font = "Segoe UI"
	t.FontSize = 12
	t.Spacing = 3
	t.Radius = 4
	t.ScrollbarWidth = 8
	if !a.prefs.Compact {
		t.Spacing = 3.5
	}
	return &t, p
}

// gridTheme scales the data workspace only (Ctrl+wheel, Ctrl+=/-/0).
func (a *App) gridTheme(base *ui.Theme) *ui.Theme {
	t := *base
	z := a.zoom()
	t.FontSize = base.FontSize * z
	t.Spacing = base.Spacing * z
	return &t
}

func (a *App) zoom() float32 {
	z := a.prefs.Zoom
	if z < 0.75 || z > 2 {
		return 1
	}
	return z
}

// Formatting.

func (a *App) size(n uint64) string {
	if a.prefs.Units == "Decimal" {
		return decimalBytes(n)
	}
	return binaryBytes(n)
}

func binaryBytes(n uint64) string {
	if n < 1024 {
		return strconv.FormatUint(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	v := float64(n) / 1024
	u := 0
	for v >= 1024 && u < len(units)-1 {
		v /= 1024
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

func decimalBytes(n uint64) string {
	if n < 1000 {
		return strconv.FormatUint(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	v := float64(n) / 1000
	u := 0
	for v >= 1000 && u < len(units)-1 {
		v /= 1000
		u++
	}
	return fmt.Sprintf("%.1f %s", v, units[u])
}

func count(n uint64) string {
	s := strconv.FormatUint(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n uint64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return count(n) + " " + many
}

func percent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}

func percentText(v float64) string {
	switch {
	case v <= 0:
		return "0 %"
	case v < 0.1:
		return "<0.1 %"
	case v >= 99.95 && v < 100:
		return ">99.9 %"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " %"
}

func dateText(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}

func duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// Stable categorical colors for the treemap and the extension list.
var categorical = []ui.Color{
	ui.Hex("#4E79A7"), ui.Hex("#F28E2B"), ui.Hex("#59A14F"), ui.Hex("#E15759"),
	ui.Hex("#76B7B2"), ui.Hex("#EDC948"), ui.Hex("#B07AA1"), ui.Hex("#FF9DA7"),
	ui.Hex("#9C755F"), ui.Hex("#6B9AC4"), ui.Hex("#8CD17D"), ui.Hex("#D37295"),
}

func hashColor(s string) ui.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	c := categorical[h.Sum32()%uint32(len(categorical))]
	return c.Mix(ui.Hex("#808080"), .45)
}

func readableOn(c ui.Color) ui.Color {
	l := .299*float32(c.R) + .587*float32(c.G) + .114*float32(c.B)
	if l > 150 {
		return ui.Hex("#111111")
	}
	return ui.Hex("#FFFFFF")
}
