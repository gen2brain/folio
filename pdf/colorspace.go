package pdf

import (
	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

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

// ColorSpace is a PDF color space. Conversion to device colors arrives with
// the rasterizer; what is here is what the interpreter needs to know: how many
// components a color has, and what it is called.
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
	Colorants []Name
	// Range bounds the a and b components of a Lab space.
	Range []float64
	// WhitePoint is the reference white of a CIE based space.
	WhitePoint []float64
	// Gamma and Matrix are the transfer values of a CalRGB or CalGray space.
	Gamma  []float64
	Matrix []float64
	// Tint maps colorant values to the alternate space.
	Tint *Function
}

// The device spaces, which every content stream can use without declaring.
var (
	DeviceGray = &ColorSpace{Name: "DeviceGray", Kind: KindGray, N: 1}
	DeviceRGB  = &ColorSpace{Name: "DeviceRGB", Kind: KindRGB, N: 3}
	DeviceCMYK = &ColorSpace{Name: "DeviceCMYK", Kind: KindCMYK, N: 4}
	patternCS  = &ColorSpace{Name: "Pattern", Kind: KindPattern, N: 1}
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

// IsPattern reports whether colors in this space name a pattern rather than
// carry component values.
func (cs *ColorSpace) IsPattern() bool { return cs != nil && cs.Kind == KindPattern }

// colorSpace resolves a color space object, which is either a name, an array,
// or a name that has to be looked up in the resource dictionary.
func (d *Document) colorSpace(obj Object, res Dict, depth int) *ColorSpace {
	if depth > maxNesting {
		d.errorf("color space nested too deeply")
		return DeviceGray
	}
	switch v := d.f.Resolve(obj).(type) {
	case Name:
		switch v {
		case "DeviceGray", "G", "CalGray":
			return DeviceGray
		case "DeviceRGB", "RGB", "CalRGB":
			return DeviceRGB
		case "DeviceCMYK", "CMYK":
			return DeviceCMYK
		case "Pattern":
			return patternCS
		case "Indexed", "I":
			return DeviceGray
		}
		if sub := d.f.Lookup(d.f.GetDict(res["ColorSpace"]), v); sub != nil {
			return d.colorSpace(sub, nil, depth+1)
		}
		d.errorf("unknown color space /%s", v)
		return DeviceGray

	case Array:
		if len(v) == 0 {
			return DeviceGray
		}
		if len(v) == 1 {
			return d.colorSpace(v[0], res, depth+1)
		}
		return d.colorSpaceArray(v, res, depth)
	}
	return DeviceGray
}

func (d *Document) colorSpaceArray(a Array, res Dict, depth int) *ColorSpace {
	switch d.f.GetName(a[0]) {
	case "ICCBased":
		st := d.f.GetStream(a[1])
		if st == nil {
			return DeviceGray
		}
		if alt := st.Dict["Alternate"]; alt != nil {
			if cs := d.colorSpace(alt, res, depth+1); cs.N == int(d.f.GetInt(st.Dict["N"], 0)) {
				return cs
			}
		}
		switch d.f.GetInt(st.Dict["N"], 0) {
		case 1:
			return DeviceGray
		case 4:
			return DeviceCMYK
		default:
			return DeviceRGB
		}

	case "CalGray":
		cs := &ColorSpace{Name: "CalGray", Kind: KindCalGray, N: 1}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
			if g := d.f.GetFloat(p["Gamma"], 0); g > 0 {
				cs.Gamma = []float64{g}
			}
		}
		return cs
	case "CalRGB":
		cs := &ColorSpace{Name: "CalRGB", Kind: KindCalRGB, N: 3}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
			cs.Gamma = d.f.GetFloats(p["Gamma"])
			cs.Matrix = d.f.GetFloats(p["Matrix"])
		}
		return cs

	case "Lab":
		cs := &ColorSpace{Name: "Lab", Kind: KindLab, N: 3}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.Range = d.f.GetFloats(p["Range"])
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
		}
		return cs

	case "Indexed", "I":
		if len(a) < 4 {
			return DeviceGray
		}
		base := d.colorSpace(a[1], res, depth+1)
		cs := &ColorSpace{
			Name:  "Indexed",
			Kind:  KindIndexed,
			N:     1,
			Base:  base,
			HiVal: int(d.f.GetInt(a[2], 0)),
		}
		switch v := d.f.Resolve(a[3]).(type) {
		case String:
			cs.Lookup = []byte(v)
		case *Stream:
			if b, err := v.Data(); err == nil {
				cs.Lookup = b
			} else {
				d.errorf("indexed palette: %v", err)
			}
		}
		return cs

	case "Separation":
		if len(a) < 3 {
			return DeviceGray
		}
		cs := &ColorSpace{
			Name:      "Separation",
			Kind:      KindSeparation,
			N:         1,
			Colorants: []Name{d.f.GetName(a[1])},
			Alternate: d.colorSpace(a[2], res, depth+1),
		}
		if len(a) > 3 {
			cs.Tint = d.function(a[3])
		}
		if cs.Colorants[0] == "None" {
			cs.Name = "Separation(None)"
		}
		return cs

	case "DeviceN":
		if len(a) < 3 {
			return DeviceGray
		}
		names := d.f.GetArray(a[1])
		cs := &ColorSpace{
			Name:      "DeviceN",
			Kind:      KindDeviceN,
			N:         max(len(names), 1),
			Alternate: d.colorSpace(a[2], res, depth+1),
		}
		for _, n := range names {
			cs.Colorants = append(cs.Colorants, d.f.GetName(n))
		}
		if len(a) > 3 {
			cs.Tint = d.function(a[3])
		}
		return cs

	case "Pattern":
		cs := &ColorSpace{Name: "Pattern", Kind: KindPattern, N: 1}
		if len(a) > 1 {
			cs.Base = d.colorSpace(a[1], res, depth+1)
			cs.N = cs.Base.N
		}
		return cs

	case "DeviceGray", "G":
		return DeviceGray
	case "DeviceRGB", "RGB":
		return DeviceRGB
	case "DeviceCMYK", "CMYK":
		return DeviceCMYK
	}

	d.errorf("unknown color space %v", syntax.Name(d.f.GetName(a[0])))
	return DeviceGray
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
