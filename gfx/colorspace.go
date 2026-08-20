package gfx

import "github.com/gen2brain/pdf/raster"

// Kind is the family a color space belongs to.
type Kind int

// Color space families, ISO 32000-1 8.6.
const (
	KindGray Kind = iota
	KindRGB
	KindCMYK
	KindLab
	KindCalRGB
	KindCalGray
	KindIndexed
	KindSeparation
	KindDeviceN
	KindPattern
)

// ColorSpace is a color space in the model of ISO 32000-1 8.6. What is here
// is what a device needs to know about one: how many components a color has,
// what it is called, and how to reach RGB.
type ColorSpace struct {
	Name string
	Kind Kind
	N    int

	// Base is the underlying space of an Indexed space and the underlying
	// space of a Pattern space, when it has one.
	Base *ColorSpace
	// Alternate is the space a Separation or DeviceN falls back to.
	Alternate *ColorSpace
	// Lookup is the palette of an Indexed space, HiVal+1 entries of Base.N
	// bytes each.
	Lookup []byte
	// HiVal is the largest index of an Indexed space.
	HiVal int
	// Colorants names the inks of a Separation or DeviceN space.
	Colorants []string
	// Range bounds the a and b components of a Lab space.
	Range []float64
	// WhitePoint is the reference white of a CIE based space.
	WhitePoint []float64
	// Gamma and Matrix are the transfer values of a CalRGB or CalGray space.
	Gamma  []float64
	Matrix []float64
	// Tint maps colorant values to the alternate space.
	Tint Tint
}

// The device spaces, which every content stream can use without declaring.
var (
	DeviceGray    = &ColorSpace{Name: "DeviceGray", Kind: KindGray, N: 1}
	DeviceRGB     = &ColorSpace{Name: "DeviceRGB", Kind: KindRGB, N: 3}
	DeviceCMYK    = &ColorSpace{Name: "DeviceCMYK", Kind: KindCMYK, N: 4}
	DevicePattern = &ColorSpace{Name: "Pattern", Kind: KindPattern, N: 1}
)

// Initial returns the color a space starts at, ISO 32000-1 8.6.8.
func (cs *ColorSpace) Initial() []float32 {
	c := make([]float32, cs.N)
	switch cs.Kind {
	case KindCMYK:
		c[3] = 1
	case KindSeparation, KindDeviceN:
		for i := range c {
			c[i] = 1
		}
	}
	return c
}

// Clamp holds a color to the range its space allows.
func (cs *ColorSpace) Clamp(c []float32) {
	switch cs.Kind {
	case KindIndexed:
		if len(c) > 0 {
			c[0] = float32(int(clampf(c[0], 0, float32(cs.HiVal)) + 0.5))
		}
	case KindLab:
		r := labRange(cs.Range)
		if len(c) > 0 {
			c[0] = clampf(c[0], 0, 100)
		}
		if len(c) > 2 {
			c[1] = clampf(c[1], r[0], r[1])
			c[2] = clampf(c[2], r[2], r[3])
		}
	default:
		for i := range c {
			c[i] = clamp01(c[i])
		}
	}
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

// LabRange bounds the a and b components of a Lab space, from its /Range or
// from the default when it has none.
func (cs *ColorSpace) LabRange() [4]float32 { return labRange(cs.Range) }

// IsPattern reports whether colors in this space name a pattern rather than
// carry component values.
func (cs *ColorSpace) IsPattern() bool { return cs != nil && cs.Kind == KindPattern }

// Tint is the transform a Separation or DeviceN space runs its colorant
// values through to reach the space it falls back to.
type Tint interface {
	Eval(out []float64, in ...float64) []float64
}

// Convert writes a color of this space into out, in the space out's length
// implies: gray for one component, CMYK for four, RGB otherwise. A nil space
// converts black.
func (cs *ColorSpace) Convert(c []float32, out []uint8) {
	if cs == nil {
		cs = DeviceGray
		c = nil
	}
	switch len(out) {
	case 1:
		out[0] = clamp8(cs.Gray(c))
	case 4:
		cy, m, y, k := cs.CMYK(c)
		out[0], out[1], out[2], out[3] = clamp8(cy), clamp8(m), clamp8(y), clamp8(k)
	default:
		r, g, b := cs.RGB(c)
		if len(out) >= 3 {
			out[0], out[1], out[2] = clamp8(r), clamp8(g), clamp8(b)
		}
	}
}

// Model returns the space as a raster.Model, which is all a pixmap needs to
// know about it: how many components it has, and how to reach RGB. The space
// has to be one a pixmap can hold, so one, three or four components whose
// values run from zero to one.
func (cs *ColorSpace) Model() raster.Model {
	if cs == nil {
		return raster.ModelRGB
	}
	switch cs.Kind {
	case KindGray:
		return raster.ModelGray
	case KindRGB:
		return raster.ModelRGB
	}
	return colorModel{cs}
}

// colorModel converts through the color space itself, which is what an
// ICCBased or CalRGB destination needs.
type colorModel struct{ cs *ColorSpace }

func (m colorModel) Components() int { return m.cs.N }

func (m colorModel) ToRGB(dst, src []uint8) {
	var comps [maxComponents]float32
	n := min(m.cs.N, len(comps))
	buf := comps[:n]
	for i, o := 0, 0; i+n <= len(src) && o+3 <= len(dst); i, o = i+n, o+3 {
		for j := range buf {
			buf[j] = float32(src[i+j]) / 255
		}
		r, g, b := m.cs.RGB(buf)
		dst[o], dst[o+1], dst[o+2] = clamp8(r), clamp8(g), clamp8(b)
	}
}

func (m colorModel) FromRGB(dst, src []uint8) {
	switch m.cs.N {
	case 1:
		raster.ModelGray.FromRGB(dst, src)
	case 4:
		raster.ModelCMYK.FromRGB(dst, src)
	case 3:
		copy(dst, src)
	default:
		clear(dst)
	}
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
