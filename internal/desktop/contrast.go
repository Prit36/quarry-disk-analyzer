package desktop

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// Text contrast rules. Every text color of the themes is checked against
// every background it is drawn on (contrast_test.go), with two measures:
//
//   - WCAG 2 contrast ratio, the conformance standard: 4.5:1 for text,
//     7:1 (AAA) for primary text, 3:1 for bars and focus indicators.
//   - APCA lightness contrast (Lc), the perceptual model proposed for
//     WCAG 3. WCAG 2 overrates light grey on dark backgrounds, so dark
//     themes that pass it can still read faintly; at Quarry's 11–12 px
//     sizes APCA asks for Lc 75 for body text and Lc 60 for secondary
//     text, captions and links.
const (
	minRatioPrimary  = 7.0
	minRatioText     = 4.5
	minRatioDisabled = 3.0
	minRatioGraphic  = 3.0
	minLcPrimary     = 75.0
	minLcText        = 60.0
	minRatioTreemap  = 4.5 // light theme: black or white labels
	// The dark theme keeps labels white, with a shadow, on tiles that keep
	// their hue: large-text level plus a margin.
	minRatioTreemapDark = 3.5
	treemapLightLabel   = "#FFFFFF"
	treemapDarkLabel    = "#000000"
)

// luminance is the WCAG 2 relative luminance of an opaque sRGB color.
func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// contrastRatio is the WCAG 2 contrast ratio of two opaque colors, 1 to 21.
func contrastRatio(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// apcaContrast is the APCA 0.0.98G lightness contrast of text on bg, as an
// absolute Lc value (0 to about 106).
func apcaContrast(text, bg ui.Color) float64 {
	y := func(c ui.Color) float64 {
		v := 0.2126729*math.Pow(float64(c.R)/255, 2.4) + 0.7151522*math.Pow(float64(c.G)/255, 2.4) + 0.0721750*math.Pow(float64(c.B)/255, 2.4)
		if v < 0.022 {
			v += math.Pow(0.022-v, 1.414)
		}
		return v
	}
	yt, yb := y(text), y(bg)
	var s float64
	if yb > yt { // dark text on a light background
		s = (math.Pow(yb, 0.56) - math.Pow(yt, 0.57)) * 1.14
		if s < 0.1 {
			return 0
		}
		return (s - 0.027) * 100
	}
	s = (math.Pow(yb, 0.65) - math.Pow(yt, 0.62)) * 1.14
	if s > -0.1 {
		return 0
	}
	return -(s + 0.027) * 100
}
