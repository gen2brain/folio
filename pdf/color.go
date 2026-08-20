package pdf

import "math"

// RGB converts a color in this space to RGB, each component in 0 to 1.
func (cs *ColorSpace) RGB(c []float32) (r, g, b float32) {
	var out [3]float32
	cs.convert(c, out[:], kindRGB, 0)
	return out[0], out[1], out[2]
}

// Gray converts a color in this space to a single grey level.
func (cs *ColorSpace) Gray(c []float32) float32 {
	var out [1]float32
	cs.convert(c, out[:], kindGrayOut, 0)
	return out[0]
}

// CMYK converts a color in this space to CMYK.
func (cs *ColorSpace) CMYK(c []float32) (cyan, magenta, yellow, black float32) {
	var out [4]float32
	cs.convert(c, out[:], kindCMYKOut, 0)
	return out[0], out[1], out[2], out[3]
}

// The destination of a conversion. Only the three device spaces can be one.
type outKind int

const (
	kindGrayOut outKind = iota
	kindRGB
	kindCMYKOut
)

const maxColorDepth = 8

// convert writes the color into out, which is 1, 3 or 4 components wide.
func (cs *ColorSpace) convert(c []float32, out []float32, want outKind, depth int) {
	if cs == nil || depth > maxColorDepth {
		fill(out, 0)
		return
	}

	switch cs.Kind {
	case KindGray:
		fromGray(at(c, 0), out, want)
	case KindRGB:
		fromRGB(at(c, 0), at(c, 1), at(c, 2), out, want)
	case KindCMYK:
		fromCMYK(at(c, 0), at(c, 1), at(c, 2), at(c, 3), out, want)
	case KindLab:
		r, g, b := labToRGB(at(c, 0), at(c, 1), at(c, 2), cs.WhitePoint)
		fromRGB(r, g, b, out, want)
	case KindCalRGB:
		r, g, b := calRGBToRGB(at(c, 0), at(c, 1), at(c, 2), cs)
		fromRGB(r, g, b, out, want)
	case KindCalGray:
		r, g, b := calGrayToRGB(at(c, 0), cs)
		fromRGB(r, g, b, out, want)

	case KindIndexed:
		base := cs.Base
		if base == nil {
			fill(out, 0)
			return
		}
		var comps [maxComponents]float32
		n := min(base.N, len(comps))
		i := int(at(c, 0) + 0.5)
		if i < 0 {
			i = 0
		} else if i > cs.HiVal {
			i = cs.HiVal
		}
		for j := 0; j < n; j++ {
			if p := i*base.N + j; p < len(cs.Lookup) {
				comps[j] = float32(cs.Lookup[p]) / 255
			}
		}
		base.decodeInto(comps[:n])
		base.convert(comps[:n], out, want, depth+1)

	case KindSeparation, KindDeviceN:
		cs.convertTint(c, out, want, depth)

	case KindPattern:
		if cs.Base != nil {
			cs.Base.convert(c, out, want, depth+1)
			return
		}
		fill(out, 0)

	default:
		fill(out, 0)
	}
}

const maxComponents = 32

// convertTint runs a Separation or DeviceN color through its tint transform.
func (cs *ColorSpace) convertTint(c []float32, out []float32, want outKind, depth int) {
	if len(cs.Colorants) == 1 {
		switch cs.Colorants[0] {
		case "None":
			fill(out, 1)
			return
		case "All":
			v := 1 - clamp01(at(c, 0))
			fromGray(v, out, want)
			return
		}
	}
	if cs.Tint == nil || cs.Alternate == nil {
		v := 1 - clamp01(at(c, 0))
		fromGray(v, out, want)
		return
	}

	var in [maxComponents]float64
	n := min(cs.N, len(in))
	for i := 0; i < n; i++ {
		in[i] = float64(at(c, i))
	}
	var buf [maxComponents]float64
	v := cs.Tint.Eval(buf[:0], in[:n]...)

	var alt [maxComponents]float32
	m := min(cs.Alternate.N, len(alt))
	for i := 0; i < m && i < len(v); i++ {
		alt[i] = float32(v[i])
	}
	cs.Alternate.convert(alt[:m], out, want, depth+1)
}

// decodeInto maps the components an indexed palette stores as bytes onto the
// range the base space actually uses, which only Lab differs on.
func (cs *ColorSpace) decodeInto(c []float32) {
	if cs.Kind != KindLab {
		return
	}
	r := labRange(cs.Range)
	c[0] *= 100
	c[1] = r[0] + c[1]*(r[1]-r[0])
	c[2] = r[2] + c[2]*(r[3]-r[2])
}

func at(c []float32, i int) float32 {
	if i < len(c) {
		return c[i]
	}
	return 0
}

func fill(out []float32, v float32) {
	for i := range out {
		out[i] = v
	}
}

func fromGray(v float32, out []float32, want outKind) {
	v = clamp01(v)
	switch want {
	case kindGrayOut:
		out[0] = v
	case kindRGB:
		out[0], out[1], out[2] = v, v, v
	case kindCMYKOut:
		out[0], out[1], out[2], out[3] = 0, 0, 0, 1-v
	}
}

func fromRGB(r, g, b float32, out []float32, want outKind) {
	r, g, b = clamp01(r), clamp01(g), clamp01(b)
	switch want {
	case kindGrayOut:
		out[0] = r*0.3 + g*0.59 + b*0.11
	case kindRGB:
		out[0], out[1], out[2] = r, g, b
	case kindCMYKOut:
		c, m, y := 1-r, 1-g, 1-b
		k := min(c, min(m, y))
		out[0], out[1], out[2], out[3] = c-k, m-k, y-k, k
	}
}

func fromCMYK(c, m, y, k float32, out []float32, want outKind) {
	c, m, y, k = clamp01(c), clamp01(m), clamp01(y), clamp01(k)
	if want == kindCMYKOut {
		out[0], out[1], out[2], out[3] = c, m, y, k
		return
	}
	r, g, b := adobeCMYK(c, m, y, k)
	fromRGB(r, g, b, out, want)
}

// adobeCMYK converts CMYK to sRGB through the lattice PDFium carries, which
// is what Acrobat produces and what both reference renderers agree on. The
// subtractive formula is nowhere close: pure cyan is not 0,1,1.
func adobeCMYK(c, m, y, k float32) (float32, float32, float32) {
	ci := int(c*255 + 0.49999997)
	mi := int(m*255 + 0.49999997)
	yi := int(y*255 + 0.49999997)
	ki := int(k*255 + 0.49999997)

	fixC, fixM, fixY, fixK := ci<<8, mi<<8, yi<<8, ki<<8
	cIdx := (fixC + 4096) >> 13
	mIdx := (fixM + 4096) >> 13
	yIdx := (fixY + 4096) >> 13
	kIdx := (fixK + 4096) >> 13

	sr, sg, sb := cmykLattice(cIdx, mIdx, yIdx, kIdx)
	fr, fg, fb := int(sr)<<8, int(sg)<<8, int(sb)<<8

	neighbour := func(fix, idx int) int {
		n := fix >> 13
		if n == idx {
			if n == 8 {
				return n - 1
			}
			return n + 1
		}
		return n
	}
	mix := func(nr, ng, nb byte, rate int) {
		fr += (int(sr) - int(nr)) * rate / 32
		fg += (int(sg) - int(ng)) * rate / 32
		fb += (int(sb) - int(nb)) * rate / 32
	}

	c1 := neighbour(fixC, cIdx)
	nr, ng, nb := cmykLattice(c1, mIdx, yIdx, kIdx)
	mix(nr, ng, nb, (fixC-(cIdx<<13))*(cIdx-c1))

	m1 := neighbour(fixM, mIdx)
	nr, ng, nb = cmykLattice(cIdx, m1, yIdx, kIdx)
	mix(nr, ng, nb, (fixM-(mIdx<<13))*(mIdx-m1))

	y1 := neighbour(fixY, yIdx)
	nr, ng, nb = cmykLattice(cIdx, mIdx, y1, kIdx)
	mix(nr, ng, nb, (fixY-(yIdx<<13))*(yIdx-y1))

	k1 := neighbour(fixK, kIdx)
	nr, ng, nb = cmykLattice(cIdx, mIdx, yIdx, k1)
	mix(nr, ng, nb, (fixK-(kIdx<<13))*(kIdx-k1))

	return float32(max(fr, 0)>>8) / 255, float32(max(fg, 0)>>8) / 255, float32(max(fb, 0)>>8) / 255
}

func cmykLattice(c, m, y, k int) (byte, byte, byte) {
	i := 3 * (9*9*9*c + 9*9*m + 9*y + k)
	if i < 0 || i+2 >= len(cmykToRGB) {
		return 0, 0, 0
	}
	return cmykToRGB[i], cmykToRGB[i+1], cmykToRGB[i+2]
}

// labToRGB converts CIE L*a*b* to sRGB through XYZ, adapting from the space's
// own white point.
func labToRGB(lstar, astar, bstar float32, wp []float64) (r, g, b float32) {
	fy := float64(lstar+16) / 116
	fx := fy + float64(astar)/500
	fz := fy - float64(bstar)/200
	w := whitePoint(wp, d50)
	return xyzToRGB(labFun(fx)*w[0], labFun(fy)*w[1], labFun(fz)*w[2], w)
}

func labFun(x float64) float64 {
	if x >= 6.0/29.0 {
		return x * x * x
	}
	return (108.0 / 841.0) * (x - 4.0/29.0)
}

// calRGBToRGB converts a CalRGB color: gamma, then the matrix that gives XYZ,
// ISO 32000-1 8.6.5.7.
func calRGBToRGB(a, b, c float32, cs *ColorSpace) (float32, float32, float32) {
	g := [3]float64{1, 1, 1}
	for i := 0; i < 3 && i < len(cs.Gamma); i++ {
		if cs.Gamma[i] > 0 {
			g[i] = cs.Gamma[i]
		}
	}
	av := pow(float64(clamp01(a)), g[0])
	bv := pow(float64(clamp01(b)), g[1])
	cv := pow(float64(clamp01(c)), g[2])

	m := [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	if len(cs.Matrix) == 9 {
		copy(m[:], cs.Matrix)
	}
	x := m[0]*av + m[3]*bv + m[6]*cv
	y := m[1]*av + m[4]*bv + m[7]*cv
	z := m[2]*av + m[5]*bv + m[8]*cv

	w := whitePoint(cs.WhitePoint, d65)
	return xyzToRGB(x, y, z, w)
}

// calGrayToRGB converts a CalGray color, which is a grey level raised to the
// space's gamma.
func calGrayToRGB(v float32, cs *ColorSpace) (float32, float32, float32) {
	g := 1.0
	if len(cs.Gamma) > 0 && cs.Gamma[0] > 0 {
		g = cs.Gamma[0]
	}
	y := pow(float64(clamp01(v)), g)
	w := whitePoint(cs.WhitePoint, d65)
	return xyzToRGB(y*w[0], y*w[1], y*w[2], w)
}

// labRange returns the a and b bounds of a Lab space, ISO 32000-1 table 65.
func labRange(r []float64) [4]float32 {
	out := [4]float32{-100, 100, -100, 100}
	for i := 0; i < 4 && i < len(r); i++ {
		out[i] = float32(r[i])
	}
	return out
}

var (
	d50 = [3]float64{0.9642, 1, 0.8249}
	d65 = [3]float64{0.9505, 1, 1.089}
)

func whitePoint(wp []float64, def [3]float64) [3]float64 {
	if len(wp) < 3 || wp[1] <= 0 {
		return def
	}
	return [3]float64{wp[0], wp[1], wp[2]}
}

// xyzToRGB adapts XYZ from a white point to D65 by the Bradford transform and
// converts it to sRGB.
func xyzToRGB(x, y, z float64, wp [3]float64) (float32, float32, float32) {
	x, y, z = adaptToD65(x, y, z, wp)

	r := 3.2404542*x - 1.5371385*y - 0.4985314*z
	g := -0.9692660*x + 1.8760108*y + 0.0415560*z
	b := 0.0556434*x - 0.2040259*y + 1.0572252*z
	return srgb(r), srgb(g), srgb(b)
}

// bradford is the cone response matrix, and its inverse.
var (
	bradford    = [9]float64{0.8951, 0.2664, -0.1614, -0.7502, 1.7135, 0.0367, 0.0389, -0.0685, 1.0296}
	bradfordInv = [9]float64{0.9869929, -0.1470543, 0.1599627, 0.4323053, 0.5183603, 0.0492912, -0.0085287, 0.0400428, 0.9684867}
)

func adaptToD65(x, y, z float64, wp [3]float64) (float64, float64, float64) {
	if wp == d65 {
		return x, y, z
	}
	sr, sg, sb := mul3(bradford, wp[0], wp[1], wp[2])
	dr, dg, db := mul3(bradford, d65[0], d65[1], d65[2])
	if sr == 0 || sg == 0 || sb == 0 {
		return x, y, z
	}
	cr, cg, cb := mul3(bradford, x, y, z)
	return mul3(bradfordInv, cr*dr/sr, cg*dg/sg, cb*db/sb)
}

func mul3(m [9]float64, x, y, z float64) (float64, float64, float64) {
	return m[0]*x + m[1]*y + m[2]*z,
		m[3]*x + m[4]*y + m[5]*z,
		m[6]*x + m[7]*y + m[8]*z
}

// srgb applies the sRGB transfer function.
func srgb(v float64) float32 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	if v <= 0.0031308 {
		return float32(12.92 * v)
	}
	return float32(1.055*pow(v, 1/2.4) - 0.055)
}

func pow(v, e float64) float64 {
	if v <= 0 {
		return 0
	}
	if e == 1 {
		return v
	}
	return math.Pow(v, e)
}
