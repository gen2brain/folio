package gfx

import "math"

// ICC is what an embedded profile says about the numbers of a color space:
// the curve each channel is encoded with, and the matrix that takes the
// linear channels to the connection space. ISO 15076-1.
//
// Only the matrix/TRC shape is read. A profile that maps through a table is
// left to the alternate space the file names.
type ICC struct {
	n   int
	trc [3]iccCurve
	// m takes the linear channels to linear sRGB, the connection space and
	// the adaptation to D65 already folded in.
	m [9]float32
}

// Components is how many channels the profile takes.
func (p *ICC) Components() int { return p.n }

// xyzD50ToRGB is XYZ relative to D50, which is the connection space, to
// linear sRGB, which is relative to D65: the Bradford adaptation between the
// two whites is folded into it.
var xyzD50ToRGB = [9]float32{
	3.1338561, -1.6168667, -0.4906146,
	-0.9787684, 1.9161415, 0.0334540,
	0.0719453, -0.2289914, 1.4052427,
}

// ParseICC reads a profile, and returns nil for one this cannot use or one
// whose transform is the one sRGB already is.
func ParseICC(data []byte) *ICC {
	if len(data) < 132 || string(data[36:40]) != "acsp" {
		return nil
	}
	if string(data[20:24]) != "XYZ " {
		return nil
	}
	tags := map[string][]byte{}
	n := int(be32(data, 128))
	if n < 0 || n > 1024 || 132+n*12 > len(data) {
		return nil
	}
	for i := 0; i < n; i++ {
		e := 132 + i*12
		off, size := int(be32(data, e+4)), int(be32(data, e+8))
		if off < 0 || size < 0 || off+size > len(data) {
			continue
		}
		tags[string(data[e:e+4])] = data[off : off+size]
	}

	var p ICC
	switch string(data[16:20]) {
	case "RGB ":
		p.n = 3
		cols := [3][3]float32{}
		for i, s := range []string{"rXYZ", "gXYZ", "bXYZ"} {
			v, ok := iccXYZ(tags[s])
			if !ok {
				return nil
			}
			cols[i] = v
		}
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				p.m[r*3+c] = cols[c][r]
			}
		}
		for i, s := range []string{"rTRC", "gTRC", "bTRC"} {
			c, ok := iccTRC(tags[s])
			if !ok {
				return nil
			}
			p.trc[i] = c
		}
	case "GRAY":
		p.n = 1
		c, ok := iccTRC(tags["kTRC"])
		if !ok {
			return nil
		}
		p.trc[0] = c
	default:
		return nil
	}

	if p.n == 3 {
		p.m = matMul(xyzD50ToRGB, p.m)
	}
	if p.isIdentity() {
		return nil
	}
	return &p
}

// isIdentity reports that running a color through the profile leaves it where
// sRGB already puts it, which is what an sRGB profile does and what most files
// embed. Skipping those keeps a page byte for byte what it was.
func (p *ICC) isIdentity() bool {
	const tol = 1.0 / 512
	if p.n == 3 {
		for i, want := range [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1} {
			if abs32(p.m[i]-want) > 8*tol {
				return false
			}
		}
	}
	for i := 0; i < p.n; i++ {
		for s := 0; s <= 16; s++ {
			v := float32(s) / 16
			if abs32(p.trc[i].at(v)-srgbToLinear(v)) > tol {
				return false
			}
		}
	}
	return true
}

// ToRGB converts one color, which is the whole of what a device asks of a
// profile: the curves make the channels linear, the matrix takes them to
// linear sRGB and the transfer function encodes them again.
func (p *ICC) ToRGB(c []float32) (r, g, b float32) {
	if p.n == 1 {
		y := p.trc[0].at(at(c, 0))
		v := linearToSRGB(y)
		return v, v, v
	}
	l0 := p.trc[0].at(at(c, 0))
	l1 := p.trc[1].at(at(c, 1))
	l2 := p.trc[2].at(at(c, 2))
	r = linearToSRGB(p.m[0]*l0 + p.m[1]*l1 + p.m[2]*l2)
	g = linearToSRGB(p.m[3]*l0 + p.m[4]*l1 + p.m[5]*l2)
	b = linearToSRGB(p.m[6]*l0 + p.m[7]*l1 + p.m[8]*l2)
	return r, g, b
}

// iccCurve is one channel's transfer curve: a gamma, a sampled table, or the
// parametric form of ISO 15076-1 10.16.
type iccCurve struct {
	gamma float32
	table []float32
	para  []float32
	kind  int
}

const (
	curveGamma = iota
	curveTable
	curvePara
)

func (c *iccCurve) at(v float32) float32 {
	v = clamp01(v)
	switch c.kind {
	case curveTable:
		n := len(c.table)
		if n == 0 {
			return v
		}
		x := v * float32(n-1)
		i := int(x)
		if i >= n-1 {
			return c.table[n-1]
		}
		f := x - float32(i)
		return c.table[i]*(1-f) + c.table[i+1]*f
	case curvePara:
		return c.parametric(v)
	}
	if c.gamma == 1 {
		return v
	}
	return pow32(v, c.gamma)
}

// parametric is the five functions of ISO 15076-1 10.16, which differ only in
// how many of the seven parameters they use.
func (c *iccCurve) parametric(x float32) float32 {
	p := c.para
	g := p[0]
	var a, b, cc, d, e, f float32
	switch len(p) {
	case 3:
		a, b = p[1], p[2]
		d = -b / a
	case 4:
		a, b, cc = p[1], p[2], p[3]
		d, e = -b/a, cc
	case 5:
		a, b, cc, d = p[1], p[2], p[3], p[4]
	case 7:
		a, b, cc, d, e, f = p[1], p[2], p[3], p[4], p[5], p[6]
	default:
		return pow32(x, g)
	}
	if x >= d {
		return pow32(a*x+b, g) + e
	}
	if len(p) <= 4 {
		return e
	}
	return cc*x + f
}

// iccTRC reads a curveType or a parametricCurveType.
func iccTRC(b []byte) (iccCurve, bool) {
	if len(b) < 12 {
		return iccCurve{}, false
	}
	switch string(b[:4]) {
	case "curv":
		n := int(be32(b, 8))
		if n < 0 || 12+n*2 > len(b) {
			return iccCurve{}, false
		}
		switch n {
		case 0:
			return iccCurve{kind: curveGamma, gamma: 1}, true
		case 1:
			return iccCurve{kind: curveGamma, gamma: float32(be16(b, 12)) / 256}, true
		}
		t := make([]float32, n)
		for i := range t {
			t[i] = float32(be16(b, 12+i*2)) / 65535
		}
		return iccCurve{kind: curveTable, table: t}, true

	case "para":
		want := [...]int{1, 3, 4, 5, 7}
		k := int(be16(b, 8))
		if k >= len(want) || 12+want[k]*4 > len(b) {
			return iccCurve{}, false
		}
		p := make([]float32, want[k])
		for i := range p {
			p[i] = s15Fixed16(b, 12+i*4)
		}
		if p[0] == 0 || (len(p) > 1 && p[1] == 0) {
			return iccCurve{}, false
		}
		if len(p) == 1 {
			return iccCurve{kind: curveGamma, gamma: p[0]}, true
		}
		return iccCurve{kind: curvePara, para: p}, true
	}
	return iccCurve{}, false
}

// iccXYZ reads an XYZType tag, which is three s15Fixed16 numbers.
func iccXYZ(b []byte) ([3]float32, bool) {
	var v [3]float32
	if len(b) < 20 || string(b[:4]) != "XYZ " {
		return v, false
	}
	for i := range v {
		v[i] = s15Fixed16(b, 8+i*4)
	}
	return v, true
}

func matMul(a, b [9]float32) [9]float32 {
	var out [9]float32
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			out[r*3+c] = a[r*3]*b[c] + a[r*3+1]*b[3+c] + a[r*3+2]*b[6+c]
		}
	}
	return out
}

func be16(b []byte, o int) uint16 { return uint16(b[o])<<8 | uint16(b[o+1]) }

func be32(b []byte, o int) uint32 {
	return uint32(b[o])<<24 | uint32(b[o+1])<<16 | uint32(b[o+2])<<8 | uint32(b[o+3])
}

func s15Fixed16(b []byte, o int) float32 {
	return float32(int32(be32(b, o))) / 65536
}

func pow32(v, e float32) float32 {
	if v <= 0 {
		return 0
	}
	return float32(math.Pow(float64(v), float64(e)))
}

func srgbToLinear(v float32) float32 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return pow32((v+0.055)/1.055, 2.4)
}

func linearToSRGB(v float32) float32 {
	if v <= 0.0031308 {
		v *= 12.92
	} else {
		v = 1.055*pow32(v, 1/2.4) - 0.055
	}
	return clamp01(v)
}
