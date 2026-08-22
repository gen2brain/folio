package gfx

import (
	"sync"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/raster"
)

// FontOf wraps a font program as a Font a device can draw glyphs out of,
// which is what a producer that has no font dictionary of its own hands to a
// text span. The wrapper is cached, so that a run of spans out of one program
// compares equal and is one run to a device.
func FontOf(prog *font.Font) Font {
	if v, ok := substCache.Load(prog); ok {
		return v.(*progFont)
	}
	v, _ := substCache.LoadOrStore(prog, &progFont{prog: prog})
	return v.(*progFont)
}

type progFont struct{ prog *font.Font }

// FontName implements Font.
func (f *progFont) FontName() string { return f.prog.Name }

// Program implements Font.
func (f *progFont) Program() *font.Font { return f.prog }

// EmBox implements Font.
func (f *progFont) EmBox() (float32, float32) { return f.prog.Ascent, f.prog.Descent }

// RunGlyph implements Font. A substitute always has a program, so there
// is nothing for it to run.
func (f *progFont) RunGlyph(Device, int, raster.Matrix, *ColorSpace, []float32, float32, int) {}

var substCache sync.Map
