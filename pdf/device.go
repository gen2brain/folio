package pdf

import "github.com/gen2brain/pdf/raster"

// BlendMode is one of the sixteen blend modes of ISO 32000-1 11.3.5. It is
// raster's, because a blend mode is a function of two colors and nothing about
// it is particular to PDF.
type BlendMode = raster.BlendMode

// The separable blend modes, then the four non-separable ones.
const (
	BlendNormal     = raster.BlendNormal
	BlendMultiply   = raster.BlendMultiply
	BlendScreen     = raster.BlendScreen
	BlendOverlay    = raster.BlendOverlay
	BlendDarken     = raster.BlendDarken
	BlendLighten    = raster.BlendLighten
	BlendColorDodge = raster.BlendColorDodge
	BlendColorBurn  = raster.BlendColorBurn
	BlendHardLight  = raster.BlendHardLight
	BlendSoftLight  = raster.BlendSoftLight
	BlendDifference = raster.BlendDifference
	BlendExclusion  = raster.BlendExclusion
	BlendHue        = raster.BlendHue
	BlendSaturation = raster.BlendSaturation
	BlendColor      = raster.BlendColor
	BlendLuminosity = raster.BlendLuminosity
)

// blendMode reads a /BM name. A name this does not know is Normal, and an
// array of them means the first one that is known.
func blendMode(n Name) (BlendMode, bool) {
	switch n {
	case "Compatible":
		return BlendNormal, true
	case "":
		return BlendNormal, false
	}
	for i := BlendNormal; i <= BlendLuminosity; i++ {
		if string(n) == i.String() {
			return i, true
		}
	}
	return BlendNormal, false
}

// ColorParams are the rendering intent and the four flags that travel with
// every painting operation.
type ColorParams struct {
	Intent          int  // 0 perceptual, 1 relative, 2 saturation, 3 absolute
	BlackPoint      bool // black point compensation
	Overprint       bool
	OverprintMode   int
	OverprintStroke bool
}

// DefaultColorParams is what a page starts with.
var DefaultColorParams = ColorParams{Intent: 1, BlackPoint: true}

func intentValue(n Name) int {
	switch n {
	case "Perceptual":
		return 0
	case "Saturation":
		return 2
	case "AbsoluteColorimetric":
		return 3
	default:
		return 1
	}
}

// DefaultColorSpaces is what the three device spaces mean, and the output
// intent that stands behind them, ISO 32000-1 8.6.5.6.
type DefaultColorSpaces struct {
	Gray, RGB, CMYK *ColorSpace
	OutputIntent    *ColorSpace
}

// Default returns the space a device color space stands for.
func (d *DefaultColorSpaces) Default(cs *ColorSpace) *ColorSpace {
	if d == nil || cs == nil {
		return cs
	}
	switch cs.Kind {
	case KindGray:
		if d.Gray != nil {
			return d.Gray
		}
	case KindRGB:
		if d.RGB != nil {
			return d.RGB
		}
	case KindCMYK:
		if d.CMYK != nil {
			return d.CMYK
		}
	}
	return cs
}

func (d *DefaultColorSpaces) clone() *DefaultColorSpaces {
	if d == nil {
		return &DefaultColorSpaces{}
	}
	c := *d
	return &c
}

// name returns what a trace calls one of the spaces.
func csName(cs *ColorSpace, def string) string {
	if cs == nil {
		return def
	}
	return cs.Name
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
	Font  *Font
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

// Device receives the drawing operations an interpreted content stream
// produces. The interpreter never touches a pixel; everything a renderer, a
// text extractor, a bounding box or a trace needs comes through here.
//
// Embed BaseDevice to pick up no-op implementations of everything.
type Device interface {
	FillPath(path *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	StrokePath(path *raster.Path, stroke *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	ClipPath(path *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect)
	ClipStrokePath(path *raster.Path, stroke *raster.Stroke, ctm raster.Matrix, scissor raster.Rect)

	FillText(text *Text, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	StrokeText(text *Text, stroke *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	ClipText(text *Text, ctm raster.Matrix, scissor raster.Rect)
	ClipStrokeText(text *Text, stroke *raster.Stroke, ctm raster.Matrix, scissor raster.Rect)
	IgnoreText(text *Text, ctm raster.Matrix)

	FillShade(shade *Shade, ctm raster.Matrix, alpha float32, cp ColorParams)
	FillImage(img *Image, ctm raster.Matrix, alpha float32, cp ColorParams)
	FillImageMask(img *Image, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	ClipImageMask(img *Image, ctm raster.Matrix, scissor raster.Rect)

	PopClip()

	// SetDefaultColorSpaces reports the spaces a device color is to be
	// interpreted in, which a resource dictionary may override with
	// /DefaultGray, /DefaultRGB and /DefaultCMYK.
	SetDefaultColorSpaces(d *DefaultColorSpaces)

	// BeginMask opens a soft mask group. What is drawn until EndMask is the
	// mask, not the page: EndMask turns it into a clip that stays in force
	// until the interpreter pops it, reading either the luminosity of each
	// pixel against backdrop or its alpha, through the transfer function.
	BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams)
	EndMask(transfer *Function)
	BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32)
	EndGroup()
	BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int
	EndTile()
	BeginLayer(name string)
	EndLayer()

	// Close is called when the page is finished. A device that writes
	// somewhere flushes here.
	Close() error
}

// BaseDevice implements Device with no-ops. Embed it so a device that cares
// about three operations does not have to spell out twenty.
type BaseDevice struct{}

func (BaseDevice) FillPath(*raster.Path, bool, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}

func (BaseDevice) StrokePath(*raster.Path, *raster.Stroke, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}
func (BaseDevice) ClipPath(*raster.Path, bool, raster.Matrix, raster.Rect)                 {}
func (BaseDevice) ClipStrokePath(*raster.Path, *raster.Stroke, raster.Matrix, raster.Rect) {}
func (BaseDevice) FillText(*Text, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}

func (BaseDevice) StrokeText(*Text, *raster.Stroke, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}
func (BaseDevice) ClipText(*Text, raster.Matrix, raster.Rect)                       {}
func (BaseDevice) ClipStrokeText(*Text, *raster.Stroke, raster.Matrix, raster.Rect) {}
func (BaseDevice) IgnoreText(*Text, raster.Matrix)                                  {}
func (BaseDevice) FillShade(*Shade, raster.Matrix, float32, ColorParams)            {}
func (BaseDevice) FillImage(*Image, raster.Matrix, float32, ColorParams)            {}
func (BaseDevice) FillImageMask(*Image, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}
func (BaseDevice) ClipImageMask(*Image, raster.Matrix, raster.Rect)                        {}
func (BaseDevice) PopClip()                                                                {}
func (BaseDevice) SetDefaultColorSpaces(*DefaultColorSpaces)                               {}
func (BaseDevice) BeginMask(raster.Rect, bool, *ColorSpace, []float32, ColorParams)        {}
func (BaseDevice) EndMask(*Function)                                                       {}
func (BaseDevice) BeginGroup(raster.Rect, *ColorSpace, bool, bool, BlendMode, float32)     {}
func (BaseDevice) EndGroup()                                                               {}
func (BaseDevice) BeginTile(raster.Rect, raster.Rect, float32, float32, raster.Matrix) int { return 0 }
func (BaseDevice) EndTile()                                                                {}
func (BaseDevice) BeginLayer(string)                                                       {}
func (BaseDevice) EndLayer()                                                               {}
func (BaseDevice) Close() error                                                            { return nil }
