package raster

import "math"

// BlendMode is one of the sixteen blend functions of ISO 32000-1 11.3.5, the
// same set SVG and CSS use. The first twelve are separable, computed one
// component at a time; the last four are defined on a color as a whole and go
// through RGB.
type BlendMode int

// The separable blend modes, then the four non-separable ones.
const (
	BlendNormal BlendMode = iota
	BlendMultiply
	BlendScreen
	BlendOverlay
	BlendDarken
	BlendLighten
	BlendColorDodge
	BlendColorBurn
	BlendHardLight
	BlendSoftLight
	BlendDifference
	BlendExclusion
	BlendHue
	BlendSaturation
	BlendColor
	BlendLuminosity
)

var blendNames = [...]string{
	"Normal", "Multiply", "Screen", "Overlay", "Darken", "Lighten",
	"ColorDodge", "ColorBurn", "HardLight", "SoftLight", "Difference",
	"Exclusion", "Hue", "Saturation", "Color", "Luminosity",
}

// String returns the mode's name.
func (b BlendMode) String() string {
	if b < 0 || int(b) >= len(blendNames) {
		return "Normal"
	}
	return blendNames[b]
}

// Separable reports whether the mode is a function of one component at a time.
func (b BlendMode) Separable() bool { return b < BlendHue }

// BlendOver composites src onto p. src is premultiplied, carries an alpha
// channel and is positioned by its own X and Y; alpha scales it, and mode is
// the blend function against what p already holds. This is what ends a
// transparency group.
func (p *Pixmap) BlendOver(src *Pixmap, alpha uint8, mode BlendMode) {
	if src == nil || alpha == 0 || !src.Alpha {
		return
	}
	y0, y1 := max(src.Y, p.Y), min(src.Y+src.H, p.Y+p.H)
	x0, x1 := max(src.X, p.X), min(src.X+src.W, p.X+p.W)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	n := min(p.N, src.N)
	dn, sn := p.Comps(), src.Comps()
	var buf blendBuf
	for y := y0; y < y1; y++ {
		dst := p.Samples[(y-p.Y)*p.Stride+(x0-p.X)*dn:]
		row := src.Samples[(y-src.Y)*src.Stride+(x0-src.X)*sn:]
		for x := x0; x < x1; x++ {
			if sa := mul255(row[src.N], alpha); sa != 0 {
				if mode == BlendNormal {
					normalPixel(p, dst[:dn:dn], row[:sn:sn], alpha, sa, n)
				} else {
					blendPixel(p, dst[:dn:dn], row[:sn:sn], sa, n, mode, &buf)
				}
			}
			dst, row = dst[dn:], row[sn:]
		}
	}
}

// normalPixel composites one premultiplied pixel over another.
func normalPixel(p *Pixmap, dst, src []uint8, alpha, sa uint8, n int) {
	isa := 255 - uint32(sa)
	for c := 0; c < n; c++ {
		dst[c] = div255(uint32(mul255(src[c], alpha))*255 + uint32(dst[c])*isa)
	}
	if p.Alpha {
		dst[p.N] = div255(uint32(sa)*255 + uint32(dst[p.N])*isa)
	}
}

// blendPixel composites one source pixel over one destination pixel. src is
// premultiplied by its own alpha, which sa has already scaled; dst is
// premultiplied by its own, or opaque when the pixmap has no alpha channel.
func blendPixel(p *Pixmap, dst, src []uint8, sa uint8, n int, mode BlendMode, buf *blendBuf) {
	cs, cb, cr := &buf.cs, &buf.cb, &buf.cr
	ba := uint8(255)
	if p.Alpha {
		ba = dst[p.N]
	}
	unpremultiply(cs[:n], src, src[len(src)-1])
	if ba == 0 {
		for c := 0; c < n; c++ {
			dst[c] = mul255(cs[c], sa)
		}
		dst[p.N] = sa
		return
	}
	unpremultiply(cb[:n], dst, ba)

	blend(mode, p.Model, cb[:n], cs[:n], cr[:n], buf)
	if ba != 255 {
		inv := 255 - uint32(ba)
		for c := 0; c < n; c++ {
			cr[c] = div255(uint32(cs[c])*inv + uint32(cr[c])*uint32(ba))
		}
	}

	isa := 255 - uint32(sa)
	for c := 0; c < n; c++ {
		dst[c] = div255(uint32(cr[c])*uint32(sa) + uint32(dst[c])*isa)
	}
	if p.Alpha {
		dst[p.N] = uint8(uint32(sa) + uint32(mul255(ba, 255-sa)))
	}
}

// unpremultiply divides a premultiplied pixel by its own alpha.
func unpremultiply(out, src []uint8, a uint8) {
	if a == 255 {
		copy(out, src)
		return
	}
	if a == 0 {
		clear(out)
		return
	}
	scale := 255*256/uint32(a) + 1
	for c := range out {
		v := uint32(src[c]) * scale >> 8
		if v > 255 {
			v = 255
		}
		out[c] = uint8(v)
	}
}

// blendBuf is the scratch one call to BlendOver reuses, so that going through
// RGB for a non separable mode costs no allocation per pixel.
type blendBuf struct {
	cs, cb, cr [4]uint8
	rb, rs, ro [3]uint8
}

// blend applies one blend mode to a whole color. The non-separable four are
// defined on RGB, so a destination in any other model goes through it.
func blend(mode BlendMode, model Model, cb, cs, out []uint8, buf *blendBuf) {
	if mode.Separable() {
		for c := range out {
			out[c] = blendComponent(mode, cb[c], cs[c])
		}
		return
	}
	toRGB(model, buf.rb[:], cb)
	toRGB(model, buf.rs[:], cs)
	blendNonSeparable(mode, &buf.rb, &buf.rs, &buf.ro)
	fromRGB(model, out, buf.ro[:])
}

func toRGB(model Model, dst, src []uint8) {
	switch {
	case len(src) == 3:
		copy(dst, src)
	case model != nil:
		model.ToRGB(dst, src)
	case len(src) == 1:
		dst[0], dst[1], dst[2] = src[0], src[0], src[0]
	default:
		clear(dst)
	}
}

func fromRGB(model Model, dst, src []uint8) {
	switch {
	case len(dst) == 3:
		copy(dst, src)
	case model != nil:
		model.FromRGB(dst, src)
	case len(dst) == 1:
		dst[0] = grayOf(src)
	default:
		clear(dst)
	}
}

func blendComponent(mode BlendMode, b, s uint8) uint8 {
	switch mode {
	case BlendMultiply:
		return mul255(b, s)
	case BlendScreen:
		return uint8(uint32(b) + uint32(s) - uint32(mul255(b, s)))
	case BlendOverlay:
		return hardLight(s, b)
	case BlendDarken:
		return min(b, s)
	case BlendLighten:
		return max(b, s)
	case BlendColorDodge:
		switch {
		case b == 0:
			return 0
		case s == 255:
			return 255
		}
		return uint8(min(255, uint32(b)*255/uint32(255-s)))
	case BlendColorBurn:
		switch {
		case b == 255:
			return 255
		case s == 0:
			return 0
		}
		return uint8(255 - min(255, uint32(255-b)*255/uint32(s)))
	case BlendHardLight:
		return hardLight(b, s)
	case BlendSoftLight:
		return softLight(b, s)
	case BlendDifference:
		if b > s {
			return b - s
		}
		return s - b
	case BlendExclusion:
		return uint8(uint32(b) + uint32(s) - 2*uint32(mul255(b, s)))
	}
	return s
}

func hardLight(b, s uint8) uint8 {
	if s <= 127 {
		return mul255(b, uint8(2*uint32(s)))
	}
	d := uint8(2*uint32(s) - 255)
	return uint8(uint32(b) + uint32(d) - uint32(mul255(b, d)))
}

func softLight(b, s uint8) uint8 {
	cb := float64(b) / 255
	cs := float64(s) / 255
	var v float64
	if cs <= 0.5 {
		v = cb - (1-2*cs)*cb*(1-cb)
	} else {
		d := math.Sqrt(cb)
		if cb <= 0.25 {
			d = ((16*cb-12)*cb + 4) * cb
		}
		v = cb + (2*cs-1)*(d-cb)
	}
	return clamp255(v * 255)
}

// blendNonSeparable is the four modes of ISO 32000-1 11.3.5.3, which move the
// hue, the saturation or the luminosity of one color onto the other.
func blendNonSeparable(mode BlendMode, cb, cs, out *[3]uint8) {
	b := [3]float64{float64(cb[0]) / 255, float64(cb[1]) / 255, float64(cb[2]) / 255}
	s := [3]float64{float64(cs[0]) / 255, float64(cs[1]) / 255, float64(cs[2]) / 255}
	var v [3]float64
	switch mode {
	case BlendHue:
		v = setLum(setSat(s, sat(b)), lum(b))
	case BlendSaturation:
		v = setLum(setSat(b, sat(s)), lum(b))
	case BlendColor:
		v = setLum(s, lum(b))
	default: // BlendLuminosity
		v = setLum(b, lum(s))
	}
	for i := range out {
		out[i] = clamp255(v[i] * 255)
	}
}

func lum(c [3]float64) float64 { return 0.3*c[0] + 0.59*c[1] + 0.11*c[2] }

func sat(c [3]float64) float64 {
	return max(c[0], max(c[1], c[2])) - min(c[0], min(c[1], c[2]))
}

func setLum(c [3]float64, l float64) [3]float64 {
	d := l - lum(c)
	c[0] += d
	c[1] += d
	c[2] += d
	return clipColor(c)
}

func clipColor(c [3]float64) [3]float64 {
	l := lum(c)
	n := min(c[0], min(c[1], c[2]))
	x := max(c[0], max(c[1], c[2]))
	if n < 0 && l != n {
		for i := range c {
			c[i] = l + (c[i]-l)*l/(l-n)
		}
	}
	if x > 1 && x != l {
		for i := range c {
			c[i] = l + (c[i]-l)*(1-l)/(x-l)
		}
	}
	return c
}

// setSat rewrites a color to have the given saturation, keeping the order of
// its components and putting the smallest at zero.
func setSat(c [3]float64, s float64) [3]float64 {
	lo, mid, hi := 0, 1, 2
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	if c[mid] > c[hi] {
		mid, hi = hi, mid
	}
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	if c[hi] > c[lo] {
		c[mid] = (c[mid] - c[lo]) * s / (c[hi] - c[lo])
		c[hi] = s
	} else {
		c[mid] = 0
		c[hi] = 0
	}
	c[lo] = 0
	return c
}

func clamp255(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// Mask turns what a soft mask group drew into the alpha only pixmap a clip
// takes: the luminosity of each pixel, or the pixel's own alpha. table, when
// it is not nil, is a transfer function the result is read through.
func (p *Pixmap) Mask(luminosity bool, table *[256]uint8) *Pixmap {
	if !luminosity && !p.Alpha {
		return nil
	}
	m := NewMask(p.W, p.H)
	if m == nil {
		return nil
	}
	m.X, m.Y = p.X, p.Y
	n := p.Comps()
	var rgb [3]uint8
	for y := 0; y < p.H; y++ {
		src := p.Row(y)
		dst := m.Row(y)
		for x := 0; x < p.W; x++ {
			var v uint8
			if luminosity {
				v = luminosityOf(p.Model, src[x*n:x*n+p.N], &rgb)
			} else {
				v = src[x*n+p.N]
			}
			if table != nil {
				v = table[v]
			}
			dst[x] = v
		}
	}
	return m
}

// luminosityOf is the gray a color carries, which is what a luminosity soft
// mask reads.
func luminosityOf(model Model, px []uint8, rgb *[3]uint8) uint8 {
	switch len(px) {
	case 0:
		return 0
	case 1:
		return px[0]
	case 3:
		return grayOf(px)
	}
	toRGB(model, rgb[:], px)
	return grayOf(rgb[:])
}

func grayOf(rgb []uint8) uint8 {
	return uint8((uint32(rgb[0])*77 + uint32(rgb[1])*151 + uint32(rgb[2])*28) >> 8)
}

// KnockoutOver composites src onto p the way an element of a knockout group
// does: where src covers a pixel it replaces what is there rather than
// layering over it, however little alpha it carries. alpha is the constant
// alpha the element was drawn with, which scales what it contributes but not
// how much of the backdrop it takes away.
func (p *Pixmap) KnockoutOver(src *Pixmap, alpha uint8) {
	if src == nil || alpha == 0 || !src.Alpha {
		return
	}
	y0, y1 := max(src.Y, p.Y), min(src.Y+src.H, p.Y+p.H)
	x0, x1 := max(src.X, p.X), min(src.X+src.W, p.X+p.W)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	n := min(p.N, src.N)
	dn, sn := p.Comps(), src.Comps()
	for y := y0; y < y1; y++ {
		dst := p.Samples[(y-p.Y)*p.Stride+(x0-p.X)*dn:]
		row := src.Samples[(y-src.Y)*src.Stride+(x0-src.X)*sn:]
		for x := x0; x < x1; x++ {
			if f := row[src.N]; f != 0 {
				inv := 255 - uint32(f)
				for c := 0; c < n; c++ {
					dst[c] = div255(uint32(mul255(row[c], alpha))*255 + uint32(dst[c])*inv)
				}
				if p.Alpha {
					dst[p.N] = div255(uint32(mul255(f, alpha))*255 + uint32(dst[p.N])*inv)
				}
			}
			dst, row = dst[dn:], row[sn:]
		}
	}
}
