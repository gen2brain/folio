package gfx

import (
	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/raster"
)

// FontOf wraps a font program as a Font a device can draw glyphs out of; two
// wrappers of one program compare equal.
func FontOf(prog *font.Font) Font { return progFont{prog: prog} }

type progFont struct{ prog *font.Font }

// FontName implements Font.
func (f progFont) FontName() string { return f.prog.Name }

// Program implements Font.
func (f progFont) Program() *font.Font { return f.prog }

// EmBox implements Font.
func (f progFont) EmBox() (float32, float32) { return f.prog.Ascent, f.prog.Descent }

// RunGlyph implements Font.
func (f progFont) RunGlyph(Device, int, raster.Matrix, *ColorSpace, []float32, float32, int) {}
