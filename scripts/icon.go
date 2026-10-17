//go:build ignore

// Run go run scripts/icon.go to regenerate resources/icon.png.
//
// The Quarry mark: a treemap cut into a dark block like terraces in a
// quarry. One large amber block, a teal block, and two smaller blocks,
// separated by deep grooves, read clearly from 16 to 1024 pixels.
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const size = 1024
const samples = 4 // supersampling per axis

type shape struct {
	x0, y0, x1, y1, r float64
	top, bottom       color.NRGBA // vertical gradient
}

func lerp(a, b uint8, t float64) float64 { return float64(a) + (float64(b)-float64(a))*t }

// inside reports whether (x, y) lies in a rounded rectangle.
func (s shape) inside(x, y float64) bool {
	if x < s.x0 || x > s.x1 || y < s.y0 || y > s.y1 {
		return false
	}
	cx := math.Max(s.x0+s.r, math.Min(x, s.x1-s.r))
	cy := math.Max(s.y0+s.r, math.Min(y, s.y1-s.r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= s.r*s.r
}

func (s shape) color(y float64) (r, g, b float64) {
	t := (y - s.y0) / (s.y1 - s.y0)
	return lerp(s.top.R, s.bottom.R, t), lerp(s.top.G, s.bottom.G, t), lerp(s.top.B, s.bottom.B, t)
}

func main() {
	hex := func(v uint32) color.NRGBA { return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255} }
	base := shape{40, 40, 984, 984, 210, hex(0x26323F), hex(0x10161D)}
	// Treemap inside the base, with grooves 34 px wide.
	const in0, in1, g = 150.0, 874.0, 34.0
	split := in0 + (in1-in0)*0.56
	right := split + g
	mid := in0 + (in1-in0)*0.52
	low := mid + g
	cut := right + (in1-right)*0.55
	blocks := []shape{
		{in0, in0, split, in1, 46, hex(0xF6B44A), hex(0xE08A1E)},   // amber: the biggest item
		{right, in0, in1, mid, 40, hex(0x3CC6B3), hex(0x1F9C8C)},   // teal
		{right, low, cut, in1, 34, hex(0x5B9BF2), hex(0x3A74D0)},   // blue
		{cut + g, low, in1, in1, 30, hex(0xEE6B5E), hex(0xC9473C)}, // coral
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, gg, b, a float64
			for sy := 0; sy < samples; sy++ {
				for sx := 0; sx < samples; sx++ {
					x := float64(px) + (float64(sx)+0.5)/samples
					y := float64(py) + (float64(sy)+0.5)/samples
					if !base.inside(x, y) {
						continue
					}
					cr, cg, cb := base.color(y)
					for _, bl := range blocks {
						if bl.inside(x, y) {
							cr, cg, cb = bl.color(y)
							// Terraces: a lighter top edge on every block.
							if y-bl.y0 < 14 {
								cr, cg, cb = cr+(255-cr)*0.28, cg+(255-cg)*0.28, cb+(255-cb)*0.28
							}
							break
						}
					}
					r, gg, b, a = r+cr, gg+cg, b+cb, a+1
				}
			}
			if a == 0 {
				continue
			}
			n := float64(samples * samples)
			img.SetNRGBA(px, py, color.NRGBA{uint8(r/a + .5), uint8(gg/a + .5), uint8(b/a + .5), uint8(255*a/n + .5)})
		}
	}
	if err := os.MkdirAll("resources", 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create("resources/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
