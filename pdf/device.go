package pdf

import (
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// The graphics model comes from the gfx package, aliased here so that a
// caller needs one import.
type (
	// Device receives the drawing operations an interpreted content stream
	// produces. Embed BaseDevice to pick up no-op implementations.
	Device = gfx.Device
	// BaseDevice implements Device with no-ops.
	BaseDevice = gfx.BaseDevice
	// DrawDevice renders into a raster.Pixmap.
	DrawDevice = gfx.DrawDevice
	// BBoxDevice accumulates the bounding box of everything drawn.
	BBoxDevice = gfx.BBoxDevice
	// ListDevice records what a page draws so that it can be drawn again.
	ListDevice = gfx.ListDevice
	// ColorSpace is a color space, ISO 32000-1 8.6.
	ColorSpace = gfx.ColorSpace
	// Kind is the family a color space belongs to.
	Kind = gfx.Kind
	// ColorParams are the rendering intent and the four flags that travel
	// with every painting operation.
	ColorParams = gfx.ColorParams
	// DefaultColorSpaces is what the three device spaces mean.
	DefaultColorSpaces = gfx.DefaultColorSpaces
	// Text is what one text showing operator, or a run of them, hands to a
	// device.
	Text = gfx.Text
	// TextSpan is a run of glyphs from one font under one text matrix.
	TextSpan = gfx.TextSpan
	// TextItem is one glyph placed by a text showing operator.
	TextItem = gfx.TextItem
	// BlendMode is one of the sixteen blend modes of ISO 32000-1 11.3.5.
	BlendMode = gfx.BlendMode
)

// Color space families, ISO 32000-1 8.6.
const (
	KindGray       = gfx.KindGray
	KindRGB        = gfx.KindRGB
	KindCMYK       = gfx.KindCMYK
	KindLab        = gfx.KindLab
	KindCalRGB     = gfx.KindCalRGB
	KindCalGray    = gfx.KindCalGray
	KindIndexed    = gfx.KindIndexed
	KindSeparation = gfx.KindSeparation
	KindDeviceN    = gfx.KindDeviceN
	KindPattern    = gfx.KindPattern
)

// The separable blend modes, then the four non-separable ones.
const (
	BlendNormal     = gfx.BlendNormal
	BlendMultiply   = gfx.BlendMultiply
	BlendScreen     = gfx.BlendScreen
	BlendOverlay    = gfx.BlendOverlay
	BlendDarken     = gfx.BlendDarken
	BlendLighten    = gfx.BlendLighten
	BlendColorDodge = gfx.BlendColorDodge
	BlendColorBurn  = gfx.BlendColorBurn
	BlendHardLight  = gfx.BlendHardLight
	BlendSoftLight  = gfx.BlendSoftLight
	BlendDifference = gfx.BlendDifference
	BlendExclusion  = gfx.BlendExclusion
	BlendHue        = gfx.BlendHue
	BlendSaturation = gfx.BlendSaturation
	BlendColor      = gfx.BlendColor
	BlendLuminosity = gfx.BlendLuminosity
)

// The device spaces, which every content stream can use without declaring.
var (
	DeviceGray = gfx.DeviceGray
	DeviceRGB  = gfx.DeviceRGB
	DeviceCMYK = gfx.DeviceCMYK
	patternCS  = gfx.DevicePattern
)

// DefaultColorParams is what a page starts with.
var DefaultColorParams = gfx.DefaultColorParams

// NewDrawDevice returns a device that renders a page of doc into dst. The
// document is needed for the glyph cache and for the errors a damaged file
// records on the way.
func NewDrawDevice(doc *Document, dst *raster.Pixmap) *DrawDevice {
	d := gfx.NewDrawDevice(dst)
	if doc != nil {
		d.SetGlyphCache(doc.glyphCache())
		d.SetErrorFunc(doc.fail)
	}
	return d
}

// NewBBoxDevice returns a device that measures a page.
func NewBBoxDevice() *BBoxDevice { return gfx.NewBBoxDevice() }

// NewListDevice returns an empty display list.
func NewListDevice() *ListDevice { return gfx.NewListDevice() }

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

// csName returns what a trace calls one of the spaces.
func csName(cs *ColorSpace, def string) string {
	if cs == nil {
		return def
	}
	return cs.Name
}

func clamp01(v float32) float32 { return clampf(v, 0, 1) }

func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// transferTable evaluates a soft mask's /TR into the 256 values a sample can
// take.
func transferTable(fn *Function) *[256]uint8 {
	if fn == nil {
		return nil
	}
	var t [256]uint8
	var out [1]float32
	for i := range t {
		fn.Eval1(out[:], float64(i)/255)
		t[i] = clamp8(out[0])
	}
	return &t
}

func clamp8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 255
	}
	return uint8(v*255 + 0.5)
}
