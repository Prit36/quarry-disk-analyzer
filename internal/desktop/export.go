package desktop

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"stratum/internal/index"
	"stratum/internal/treemap"
)

func (a *App) export() { a.exportAs("csv", a.focus) }

func (a *App) exportRows(rows []index.Row, label string) {
	if a.snapshot == nil || a.exporting {
		return
	}
	a.exporting = true
	snapshot, window := a.snapshot, a.window
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: window, Title: label, DefaultPath: "stratum-results.csv", Filters: []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}}})
		if err == nil && path != "" {
			err = atomicExport(path, func(writer io.Writer) error { return snapshot.ExportRows(context.Background(), writer, rows) })
		}
		a.post(event{kind: "export", path: path, err: err})
	}()
}

func (a *App) exportAs(format string, root index.ID) {
	if a.snapshot == nil || a.exporting {
		return
	}
	a.exporting = true
	snapshot, copyApp := a.snapshot, *a
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: copyApp.window, Title: "Export " + format + " · " + snapshot.Path(root), DefaultPath: "stratum-export." + format, Filters: []mygo.FileFilter{{Name: format, Extensions: []string{format}}}})
		if err == nil && path != "" {
			err = atomicExport(path, func(writer io.Writer) error {
				switch format {
				case "json":
					return snapshot.ExportJSON(context.Background(), writer, root)
				case "txt":
					return snapshot.ExportReport(context.Background(), writer, root)
				case "png":
					return copyApp.exportTreemapPNG(writer)
				default:
					return snapshot.ExportScope(context.Background(), writer, root)
				}
			})
		}
		a.post(event{kind: "export", path: path, err: err})
	}()
}

func atomicExport(path string, write func(io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".stratum-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = write(file)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func exportFile(snapshot *index.Snapshot, path string) error {
	return atomicExport(path, func(writer io.Writer) error { return snapshot.Export(context.Background(), writer) })
}
func exportJSON(snapshot *index.Snapshot, path string) error {
	return atomicExport(path, func(writer io.Writer) error { return snapshot.ExportJSON(context.Background(), writer, 0) })
}

func (a *App) exportTreemapPNG(writer io.Writer) error {
	if a.snapshot == nil {
		return fmt.Errorf("no scan available")
	}
	bitmap := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	draw.Draw(bitmap, bitmap.Bounds(), image.NewUniform(color.RGBA{R: 13, G: 21, B: 32, A: 255}), image.Point{}, draw.Src)
	cells := treemap.NestedLayout(a.branches, treemap.Rect{X: 8, Y: 36, W: 1584, H: 856})
	text := &font.Drawer{Dst: bitmap, Face: basicfont.Face7x13, Src: image.White}
	text.Dot = fixed.P(8, 20)
	text.DrawString(a.snapshot.Path(a.mapRoot()) + " | " + a.metricName() + " | " + a.formatBytes(a.mapTotal))
	for _, cell := range cells {
		tile := cell.Tile
		bounds := tile.Rect
		rectangle := image.Rect(int(bounds.X), int(bounds.Y), int(bounds.X+bounds.W)-1, int(bounds.Y+bounds.H)-1)
		shade := a.tileColor(tile.ID)
		if tile.Other {
			shade = typePalette[7]
		}
		draw.Draw(bitmap, rectangle, image.NewUniform(color.RGBA{R: shade.R, G: shade.G, B: shade.B, A: 255}), image.Point{}, draw.Src)
		foreground := mapTextColor(shade)
		text.Src = image.NewUniform(color.RGBA{R: foreground.R, G: foreground.G, B: foreground.B, A: 255})
		if rectangle.Dx() > 80 && rectangle.Dy() > 16 {
			name := a.snapshot.NameView(tile.ID)
			if tile.Other {
				name = "Other items"
			}
			text.Dot = fixed.P(rectangle.Min.X+4, rectangle.Min.Y+13)
			text.DrawString(truncate(name, (rectangle.Dx()-8)/7))
			if !cell.Group && rectangle.Dy() > 34 {
				text.Dot = fixed.P(rectangle.Min.X+4, rectangle.Min.Y+29)
				text.DrawString(a.formatBytes(tile.Bytes))
			}
		}
	}
	return png.Encode(writer, bitmap)
}
