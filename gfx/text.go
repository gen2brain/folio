package gfx

import (
	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/raster"
)

// Font is the glyph source a text span names.
type Font interface {
	// FontName is the name the font goes by.
	FontName() string
	// Program is the font program the glyphs come from, and nil for a font
	// that draws its glyphs by running procedures instead.
	Program() *font.Font
	// EmBox is how far the em box reaches above and below the baseline, in
	// text space where one unit is the font size.
	EmBox() (ascent, descent float32)
	// RunGlyph draws one glyph into dev under ctm, which a font with no
	// program has to do for itself. depth is how deep dev already is inside
	// procedures that re-entered it.
	RunGlyph(dev Device, gid int, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, depth int)
}

// Quad is the four corners of what a character occupies.
type Quad struct{ UL, UR, LL, LR raster.Point }

// Bounds returns the smallest rectangle holding the quad.
func (q Quad) Bounds() raster.Rect {
	return raster.EmptyRect.AddPoint(q.UL).AddPoint(q.UR).AddPoint(q.LL).AddPoint(q.LR)
}

// TextItem is one glyph placed by a text showing operator.
type TextItem struct {
	X, Y float32
	// GID is the glyph index in the font program, or -1 when the font engine
	// has not resolved one.
	GID int
	// Rune is the Unicode value the character code maps to, or -1.
	Rune rune
	// Name is what the font program calls the glyph, when it names it.
	Name string
	// Code is the character code the string held, and CID what the font's
	// encoding turned it into.
	Code, CID uint32
	// Adv is how far the pen moves, in text space units of the font size.
	Adv float32
}

// TextSpan is a run of glyphs from one font under one text matrix.
type TextSpan struct {
	Font  Font
	WMode int
	Trm   raster.Matrix
	Items []TextItem
}

// Text is what one text showing operator, or a run of them, hands to a device.
type Text struct {
	Spans []TextSpan
}

// Bounds returns a conservative bounding box for the text under ctm. An item
// carries its position in text space, so the span matrix supplies only the
// shape; without glyph outlines the extent is an em box around the advance,
// which is wider and taller than any real glyph.
func (t *Text) Bounds(ctm raster.Matrix) raster.Rect {
	out := raster.EmptyRect
	for _, sp := range t.Spans {
		for _, it := range sp.Items {
			m := raster.Concat(raster.Matrix{
				A: sp.Trm.A, B: sp.Trm.B, C: sp.Trm.C, D: sp.Trm.D,
				E: it.X, F: it.Y,
			}, ctm)
			adv := it.Adv
			if adv <= 0 {
				adv = 1
			}
			out = out.Union(m.ApplyRect(raster.Rect{X0: 0, Y0: -0.3, X1: adv, Y1: 1}))
		}
	}
	return out
}
