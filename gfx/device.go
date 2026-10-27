package gfx

import "github.com/gen2brain/folio/raster"

// BlendMode is one of the sixteen blend modes of ISO 32000-1 11.3.5.
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

// ColorParams are the rendering intent and the flags of every painting operation.
type ColorParams struct {
	Intent          int  // 0 perceptual, 1 relative, 2 saturation, 3 absolute
	BlackPoint      bool // black point compensation
	Overprint       bool
	OverprintMode   int
	OverprintStroke bool
}

// DefaultColorParams is what a page starts with.
var DefaultColorParams = ColorParams{Intent: 1, BlackPoint: true}

// DefaultColorSpaces are the spaces the device spaces stand for and the output intent, ISO 32000-1 8.6.5.6.
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

// Clone returns a copy, and an empty one for a nil receiver.
func (d *DefaultColorSpaces) Clone() *DefaultColorSpaces {
	if d == nil {
		return &DefaultColorSpaces{}
	}
	c := *d
	return &c
}

// Device receives the drawing operations an interpreted document produces.
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

	FillShade(shade Shade, ctm raster.Matrix, alpha float32, cp ColorParams)
	FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams)
	FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams)
	ClipImageMask(img Image, ctm raster.Matrix, scissor raster.Rect)

	PopClip()

	// SetDefaultColorSpaces reports the spaces device colors are interpreted in.
	SetDefaultColorSpaces(d *DefaultColorSpaces)

	// BeginMask opens a soft mask group that EndMask turns into a clip until popped.
	BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams)
	EndMask(transfer *[256]uint8)
	BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32)
	EndGroup()
	BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int
	EndTile()
	BeginLayer(name string)
	EndLayer()

	// Close is called when the page is finished.
	Close() error
}

// BaseDevice implements Device with no-ops.
type BaseDevice struct {
	running  []any
	glyphOps int64
}

// GlyphOps is the count every glyph procedure run through the device adds to.
func (b *BaseDevice) GlyphOps() *int64 { return &b.glyphOps }

// EnterGlyph reports whether a glyph procedure may run and pushes it; LeaveGlyph pops it.
func (b *BaseDevice) EnterGlyph(key any) bool {
	if len(b.running) >= maxGlyphDepth {
		return false
	}
	for _, v := range b.running {
		if v == key {
			return false
		}
	}
	b.running = append(b.running, key)
	return true
}

func (b *BaseDevice) LeaveGlyph() {
	if n := len(b.running); n > 0 {
		b.running = b.running[:n-1]
	}
}

// GlyphRunner is the part of a device a font uses to draw its own glyphs through it.
type GlyphRunner interface {
	EnterGlyph(key any) bool
	LeaveGlyph()
}

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
func (BaseDevice) FillShade(Shade, raster.Matrix, float32, ColorParams)             {}
func (BaseDevice) FillImage(Image, raster.Matrix, float32, ColorParams)             {}
func (BaseDevice) FillImageMask(Image, raster.Matrix, *ColorSpace, []float32, float32, ColorParams) {
}
func (BaseDevice) ClipImageMask(Image, raster.Matrix, raster.Rect)                         {}
func (BaseDevice) PopClip()                                                                {}
func (BaseDevice) SetDefaultColorSpaces(*DefaultColorSpaces)                               {}
func (BaseDevice) BeginMask(raster.Rect, bool, *ColorSpace, []float32, ColorParams)        {}
func (BaseDevice) EndMask(*[256]uint8)                                                     {}
func (BaseDevice) BeginGroup(raster.Rect, *ColorSpace, bool, bool, BlendMode, float32)     {}
func (BaseDevice) EndGroup()                                                               {}
func (BaseDevice) BeginTile(raster.Rect, raster.Rect, float32, float32, raster.Matrix) int { return 0 }
func (BaseDevice) EndTile()                                                                {}
func (BaseDevice) BeginLayer(string)                                                       {}
func (BaseDevice) EndLayer()                                                               {}
func (BaseDevice) Close() error                                                            { return nil }
