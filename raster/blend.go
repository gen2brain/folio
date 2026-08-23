package raster

import (
	"encoding/binary"
	"math"
)

// BlendMode is one of the sixteen blend functions of ISO 32000-1 11.3.5, the
// same set SVG and CSS use. The first twelve are separable and run one
// component at a time; the last four take a color as a whole, through RGB.
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
// the blend function against what p already holds.
func (p *Pixmap) BlendOver(src *Pixmap, alpha uint8, mode BlendMode) {
	if src == nil || alpha == 0 || !src.Alpha {
		return
	}
	y0, y1 := max(src.Y, p.Y), min(src.Y+src.H, p.Y+p.H)
	x0, x1 := max(src.X, p.X), min(src.X+src.W, p.X+p.W)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	b := blender{
		model:     p.Model,
		mode:      mode,
		alpha:     alpha,
		separable: mode.Separable(),
		n:         min(p.N, src.N),
		pn:        p.N,
		sn:        src.Comps(),
		dn:        p.Comps(),
		sa:        src.N,
		dalpha:    p.Alpha,
	}
	for i := range b.scale {
		b.scale[i] = mul255(uint8(i), alpha)
	}
	// Over a backdrop with no alpha the destination is already the
	// unpremultiplied colour, laid out as the scratch is, so the mode runs
	// over a whole run in one call.
	b.flat = b.separable && mode != BlendNormal && !p.Alpha && b.n == b.dn

	sa, dn, sn := b.sa, b.dn, b.sn
	w := x1 - x0
	for y := y0; y < y1; y++ {
		dst := p.Samples[(y-p.Y)*p.Stride+(x0-p.X)*dn:]
		row := src.Samples[(y-src.Y)*src.Stride+(x0-src.X)*sn:]
		for i := 0; i < w; {
			if row[sa] == 0 {
				if k := clearPixels(row[:(w-i)*sn], sn); k > 0 {
					dst, row = dst[k*dn:], row[k*sn:]
					i += k
					continue
				}
			}
			k := 1
			if b.flat {
				for k < min(w-i, blendChunk) && row[k*sn+sa] != 0 {
					k++
				}
				b.span(dst, row, k)
			} else if a := b.scale[row[sa]]; a != 0 {
				b.pixel(dst[:dn:dn], row[:sn:sn], a)
			}
			dst, row = dst[k*dn:], row[k*sn:]
			i += k
		}
	}
}

// blendChunk is how many pixels of a run go through the scratch at a time.
const blendChunk = 64

// span is the blend of a run of pixels whose backdrop is opaque and laid out
// as the scratch is. A pixel of no coverage comes out of it unchanged.
func (b *blender) span(dst, src []uint8, w int) {
	n := b.n
	cs := b.cs[: w*n : w*n]
	cr := b.cr[: w*n : w*n]

	for i := range w {
		s := src[i*b.sn:]
		unpremultiply(cs[i*n:i*n+n:i*n+n], s, s[b.sa])
	}
	blendSeparable(b.mode, dst[:w*n:w*n], cs, cr)
	for i := range w {
		a := uint32(b.scale[src[i*b.sn+b.sa]])
		isa := 255 - a
		p := dst[i*n : i*n+n : i*n+n]
		for c, v := range cr[i*n : i*n+n : i*n+n] {
			p[c] = div255(uint32(v)*a + uint32(p[c])*isa)
		}
	}
}

// clearPixels is how many whole pixels at the front of a group's row are every
// byte zero, which is how many the blend has nothing to do for. The test is
// every byte rather than the alpha alone so that it stands on its own: a pixel
// of all zeroes has a zero alpha whatever else is true.
func clearPixels(row []uint8, sn int) int {
	z := 0
	for z+8 <= len(row) && binary.NativeEndian.Uint64(row[z:]) == 0 {
		z += 8
	}
	for z < len(row) && row[z] == 0 {
		z++
	}
	return z / sn
}

// blender is what one call to BlendOver knows before it starts, so that the
// pixel loop reads none of it out of a Pixmap again.
type blender struct {
	model         Model
	buf           blendBuf
	scale         [256]uint8
	cs, cr        [blendChunk * 4]uint8
	mode          BlendMode
	alpha         uint8
	n, pn, sn, dn int
	sa            int
	separable     bool
	flat          bool
	dalpha        bool
}

func (b *blender) pixel(dst, src []uint8, a uint8) {
	if b.mode == BlendNormal {
		b.normal(dst, src, a)
		return
	}
	if !b.dalpha || dst[b.pn] == 255 {
		b.opaque(dst, src, a)
		return
	}
	b.general(dst, src, a)
}

// normal composites one premultiplied pixel over another.
func (b *blender) normal(dst, src []uint8, a uint8) {
	isa := 255 - uint32(a)
	for c := range b.n {
		dst[c] = div255(uint32(mul255(src[c], b.alpha))*255 + uint32(dst[c])*isa)
	}
	if b.dalpha {
		dst[b.pn] = div255(uint32(a)*255 + uint32(dst[b.pn])*isa)
	}
}

// opaque is the blend against a backdrop that is already opaque, which is
// every page and most of a group. Nothing has to leave premultiplied form on
// the destination side, and the union of the two alphas is opaque as well.
func (b *blender) opaque(dst, src []uint8, a uint8) {
	cs, cr := &b.buf.cs, &b.buf.cr
	unpremultiply(cs[:b.n], src, src[b.sa])
	if b.separable {
		blendSeparable(b.mode, dst[:b.n], cs[:b.n], cr[:b.n])
	} else {
		blend(b.mode, b.model, dst[:b.n], cs[:b.n], cr[:b.n], &b.buf)
	}

	isa := 255 - uint32(a)
	for c := range b.n {
		dst[c] = div255(uint32(cr[c])*uint32(a) + uint32(dst[c])*isa)
	}
	if b.dalpha {
		dst[b.pn] = 255
	}
}

// general is the blend against a backdrop that is neither clear nor opaque,
// where both sides have to be divided by their own alpha and the result
// weighted back toward the source by how much backdrop there was.
func (b *blender) general(dst, src []uint8, a uint8) {
	cs, cb, cr := &b.buf.cs, &b.buf.cb, &b.buf.cr
	ba := dst[b.pn]
	unpremultiply(cs[:b.n], src, src[b.sa])
	if ba == 0 {
		for c := range b.n {
			dst[c] = mul255(cs[c], a)
		}
		dst[b.pn] = a
		return
	}
	unpremultiply(cb[:b.n], dst, ba)
	blend(b.mode, b.model, cb[:b.n], cs[:b.n], cr[:b.n], &b.buf)

	inv := 255 - uint32(ba)
	for c := range b.n {
		cr[c] = div255(uint32(cs[c])*inv + uint32(cr[c])*uint32(ba))
	}
	isa := 255 - uint32(a)
	for c := range b.n {
		dst[c] = div255(uint32(cr[c])*uint32(a) + uint32(dst[c])*isa)
	}
	dst[b.pn] = uint8(uint32(a) + uint32(mul255(ba, 255-a)))
}

// unpremultiply divides a premultiplied pixel by its own alpha.
func unpremultiply(out, src []uint8, a uint8) {
	scale := unpremulScale[a]
	for c := range out {
		v := uint32(src[c]) * scale >> 8
		if v > 255 {
			v = 255
		}
		out[c] = uint8(v)
	}
}

var unpremulScale [256]uint32

func init() {
	for a := 1; a < 256; a++ {
		unpremulScale[a] = 255*256/uint32(a) + 1
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
		blendSeparable(mode, cb, cs, out)
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

// blendSeparable applies one of the twelve component-at-a-time modes to a
// whole color. The mode is chosen once and the loop is inside the choice.
func blendSeparable(mode BlendMode, cb, cs, out []uint8) {
	switch mode {
	case BlendMultiply:
		for c, s := range cs {
			out[c] = mul255(cb[c], s)
		}
	case BlendScreen:
		for c, s := range cs {
			out[c] = screen(cb[c], s)
		}
	case BlendOverlay:
		for c, s := range cs {
			out[c] = hardLight(s, cb[c])
		}
	case BlendDarken:
		for c, s := range cs {
			out[c] = min(cb[c], s)
		}
	case BlendLighten:
		for c, s := range cs {
			out[c] = max(cb[c], s)
		}
	case BlendColorDodge:
		for c, s := range cs {
			out[c] = colorDodge(cb[c], s)
		}
	case BlendColorBurn:
		for c, s := range cs {
			out[c] = colorBurn(cb[c], s)
		}
	case BlendHardLight:
		for c, s := range cs {
			out[c] = hardLight(cb[c], s)
		}
	case BlendSoftLight:
		for c, s := range cs {
			out[c] = softLight(cb[c], s)
		}
	case BlendDifference:
		for c, s := range cs {
			out[c] = difference(cb[c], s)
		}
	case BlendExclusion:
		for c, s := range cs {
			out[c] = exclusion(cb[c], s)
		}
	default:
		copy(out, cs)
	}
}

func screen(b, s uint8) uint8 {
	return uint8(uint32(b) + uint32(s) - uint32(mul255(b, s)))
}

func colorDodge(b, s uint8) uint8 {
	switch {
	case b == 0:
		return 0
	case s == 255:
		return 255
	}
	return uint8(min(255, uint32(b)*255/uint32(255-s)))
}

func colorBurn(b, s uint8) uint8 {
	switch {
	case b == 255:
		return 255
	case s == 0:
		return 0
	}
	return uint8(255 - min(255, uint32(255-b)*255/uint32(s)))
}

func difference(b, s uint8) uint8 {
	if b > s {
		return b - s
	}
	return s - b
}

func exclusion(b, s uint8) uint8 {
	return uint8(uint32(b) + uint32(s) - 2*uint32(mul255(b, s)))
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

// luminosityOf is the gray a color carries, which a luminosity soft mask reads.
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
