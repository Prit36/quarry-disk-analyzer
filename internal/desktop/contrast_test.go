package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestContrastMeasures(t *testing.T) {
	// Reference values from the WCAG 2 and APCA 0.0.98G definitions.
	if r := contrastRatio(ui.Hex("#000000"), ui.Hex("#FFFFFF")); r < 20.99 || r > 21.01 {
		t.Errorf("black on white ratio %.2f, want 21", r)
	}
	if r := contrastRatio(ui.Hex("#777777"), ui.Hex("#FFFFFF")); r < 4.47 || r > 4.49 {
		t.Errorf("#777 on white ratio %.2f, want 4.48", r)
	}
	if lc := apcaContrast(ui.Hex("#888888"), ui.Hex("#FFFFFF")); lc < 63 || lc > 64 {
		t.Errorf("#888 on white Lc %.1f, want 63.1", lc)
	}
	if lc := apcaContrast(ui.Hex("#FFFFFF"), ui.Hex("#000000")); lc < 107 || lc > 108.5 {
		t.Errorf("white on black Lc %.1f, want 107.9", lc)
	}
}

// TestThemeContrast holds both themes to the rules in contrast.go: each
// text color against each background it is drawn on.
func TestThemeContrast(t *testing.T) {
	for _, theme := range []string{"Dark", "Light"} {
		t.Run(theme, func(t *testing.T) {
			a := testApp(t)
			a.prefs.Theme = theme
			th, pal := a.appTheme()
			grid := a.gridTheme(th)
			type bg struct {
				name  string
				color ui.Color
			}
			panels := []bg{{"background", th.Background}, {"surface", th.Surface}, {"chrome", pal.chrome}, {"grid", pal.grid}, {"hover", th.SurfaceHover}}
			rows := append(panels[:len(panels):len(panels)], bg{"selected row", pal.selected}, bg{"unfocused selection", pal.unfocused}, bg{"row hover", grid.SurfaceHover})
			text := func(name string, fg ui.Color, on []bg, minRatio, minLc float64) {
				t.Helper()
				for _, b := range on {
					if r := contrastRatio(fg, b.color); r < minRatio {
						t.Errorf("%s on %s: ratio %.2f, want %.1f", name, b.name, r, minRatio)
					}
					if lc := apcaContrast(fg, b.color); lc < minLc {
						t.Errorf("%s on %s: APCA Lc %.0f, want %.0f", name, b.name, lc, minLc)
					}
				}
			}
			text("text", th.Text, rows, minRatioPrimary, minLcPrimary)
			text("secondary text", th.TextMuted, rows, minRatioText, minLcText)
			text("link and status text", th.Focus, panels, minRatioText, minLcText)
			text("warning text", th.Warning, panels, minRatioText, minLcText)
			text("error text", th.Danger, panels, minRatioText, minLcText)
			text("warning icon", th.Warning, rows, minRatioGraphic, 0)
			text("button text", th.AccentText, []bg{{"accent", th.Accent}, {"accent hover", th.AccentHover}, {"accent pressed", th.AccentPressed}}, minRatioText, minLcText)
			text("disabled controls", disabledColor(th), panels[:3], minRatioDisabled, 0)
			text("focus indicator", th.Focus, panels, minRatioGraphic, 0)
			text("share bar", pal.bar, []bg{{"grid", pal.grid}, {"selected row", pal.selected}}, minRatioGraphic, 0)
			if r := contrastRatio(th.TextMuted, disabledColor(th)); r < 1.5 {
				t.Errorf("disabled controls look like enabled ones: ratio %.2f to secondary text", r)
			}
		})
	}
}

// TestTreemapLabelContrast checks the labels on every treemap color: the
// file type colors, aggregated blocks, folder frames and the neutrals.
func TestTreemapLabelContrast(t *testing.T) {
	var colors []ui.Color
	for _, c := range categorical {
		colors = append(colors, c, c.Mix(ui.Hex("#000000"), .22), c.Mix(ui.Hex("#808080"), .45))
		for depth := range 6 {
			colors = append(colors, c.Mix(ui.Hex("#000000"), float32(depth)*.07))
		}
	}
	for _, n := range []ui.Color{ui.Hex("#555555"), ui.Hex("#A9A9A9")} {
		colors = append(colors, n, n.Mix(ui.Hex("#000000"), .2))
		for depth := range 4 {
			colors = append(colors, n.Mix(ui.Hex("#FFFFFF"), float32(depth)*.06))
		}
	}
	colors = append(colors, ui.Hex("#2A2A2A"), ui.Hex("#D6D6D6"))
	white := ui.Hex(treemapLightLabel)
	for _, c := range colors {
		if c := deepen(c); labelOn(c, true) != white || contrastRatio(white, c.Mix(white, .10)) < minRatioTreemapDark {
			t.Errorf("dark theme label on %02X%02X%02X: ratio %.2f, want white at %.1f", c.R, c.G, c.B, contrastRatio(white, c.Mix(white, .10)), minRatioTreemapDark)
		}
		for _, under := range []ui.Color{c.Mix(ui.Hex("#FFFFFF"), .10), c.Mix(ui.Hex("#FFFFFF"), .18).Mix(ui.Hex("#FFFFFF"), .10)} {
			if r := contrastRatio(readableOn(under), under); r < minRatioTreemap {
				t.Errorf("label on %02X%02X%02X: ratio %.2f, want %.1f", under.R, under.G, under.B, r, minRatioTreemap)
			}
		}
	}
}
