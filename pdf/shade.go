package pdf

import (
	"math"

	"github.com/gen2brain/pdf/raster"
)

// Shade is a shading dictionary: the sh operator paints one directly, and a
// shading pattern paints one through a path.
type Shade struct {
	Type int // 1 function, 2 axial, 3 radial, 4-7 mesh
	CS   *ColorSpace
	// Matrix is the pattern matrix when the shading came from a pattern, and
	// the identity when it came from sh.
	Matrix raster.Matrix
	// BBox is the shading's own bounding box in its own space, empty when it
	// has none.
	BBox       raster.Rect
	Background []float64
	Coords     []float64
	Domain     []float64
	Extend     [2]bool
	Function   []*Function

	// FuncMatrix, XDivs and YDivs describe a type 1 shading, whose function
	// is sampled over a rectangle of its own space.
	FuncMatrix   raster.Matrix
	XDivs, YDivs int

	dict   Dict
	stream *Stream
}

// Coord6 returns the /Coords of an axial or radial shading, padded to the six
// numbers those two types use.
func (s *Shade) Coord6() [6]float32 {
	var out [6]float32
	if s.Type == 2 {
		for i := 0; i < 4 && i < len(s.Coords); i++ {
			j := i
			if i >= 2 {
				j = i + 1
			}
			out[j] = float32(s.Coords[i])
		}
		return out
	}
	for i := 0; i < 6 && i < len(s.Coords); i++ {
		out[i] = float32(s.Coords[i])
	}
	return out
}

// Domain4 returns the /Domain of a type 1 shading, which defaults to the unit
// square.
func (s *Shade) Domain4() [4]float32 {
	out := [4]float32{0, 1, 0, 1}
	for i := 0; i < 4 && i < len(s.Domain); i++ {
		out[i] = float32(s.Domain[i])
	}
	return out
}

// shade reads a shading dictionary.
func (d *Document) shade(obj Object, res Dict) *Shade {
	dict := d.f.GetDict(obj)
	if dict == nil {
		return nil
	}
	f := d.f
	sh := &Shade{
		Type:       int(f.GetInt(dict["ShadingType"], 0)),
		Matrix:     raster.Identity,
		Background: f.GetFloats(dict["Background"]),
		Coords:     f.GetFloats(dict["Coords"]),
		Domain:     f.GetFloats(dict["Domain"]),
		BBox:       raster.EmptyRect,
		dict:       dict,
	}
	sh.stream = f.GetStream(obj)
	if cs := dict["ColorSpace"]; cs != nil {
		sh.CS = d.colorSpace(cs, res, 0)
	} else {
		sh.CS = DeviceRGB
	}
	if b := f.GetFloats(dict["BBox"]); len(b) == 4 {
		sh.BBox = raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
	}
	if e := f.GetArray(dict["Extend"]); len(e) == 2 {
		sh.Extend[0] = f.GetBool(e[0], false)
		sh.Extend[1] = f.GetBool(e[1], false)
	}
	if sh.Type == 1 {
		sh.FuncMatrix = raster.Identity
		if m := f.GetFloats(dict["Matrix"]); len(m) == 6 {
			sh.FuncMatrix = raster.Matrix{
				A: float32(m[0]), B: float32(m[1]), C: float32(m[2]),
				D: float32(m[3]), E: float32(m[4]), F: float32(m[5]),
			}
		}
		sh.XDivs, sh.YDivs = 32, 32
	}
	switch fn := f.Resolve(dict["Function"]).(type) {
	case Array:
		for _, v := range fn {
			sh.Function = append(sh.Function, d.function(v))
		}
	case nil:
	default:
		sh.Function = append(sh.Function, d.function(dict["Function"]))
	}
	if sh.Type < 1 || sh.Type > 7 {
		d.errorf("shading type %d", sh.Type)
		return nil
	}
	return sh
}

// Dict returns the shading dictionary.
func (s *Shade) Dict() Dict { return s.dict }

// maxShadeGrid bounds the sample grid a type 1 shading is evaluated into.
const maxShadeGrid = 1024

// shadeColor evaluates the functions of a shading into a color of its own
// space. One function gives every component, several give one each.
func shadeColor(sh *Shade, out []float32, in ...float64) []float32 {
	var buf [64]float64
	if len(sh.Function) == 1 {
		v := sh.Function[0].Eval(buf[:0], in...)
		for i := range out {
			out[i] = 0
			if i < len(v) {
				out[i] = float32(v[i])
			}
		}
		return out
	}
	for i := range out {
		out[i] = 0
		if i < len(sh.Function) {
			if v := sh.Function[i].Eval(buf[:0], in...); len(v) > 0 {
				out[i] = float32(v[0])
			}
		}
	}
	return out
}

// shadeComps is how many values a color of the shading's own space has.
func shadeComps(sh *Shade) int {
	if sh.CS == nil {
		return 1
	}
	return sh.CS.N
}

// shadeLUT evaluates a shading over its parametric domain into 256 colors of
// the destination's components.
func shadeLUT(sh *Shade, n int) []uint8 {
	t0, t1 := 0.0, 1.0
	if len(sh.Domain) >= 2 {
		t0, t1 = sh.Domain[0], sh.Domain[1]
	}
	lut := make([]uint8, 256*n)
	col := make([]float32, shadeComps(sh))
	for i := 0; i < 256; i++ {
		shadeColor(sh, col, t0+(t1-t0)*float64(i)/255)
		convertColor(sh.CS, col, lut[i*n:(i+1)*n])
	}
	return lut
}

// gradient shades an axial or radial shading: a parameter per pixel, and the
// color it names in a table the function was evaluated into.
type gradient struct {
	lut []uint8
	n   int
	inv raster.Matrix

	x0, y0, r0 float32
	dx, dy, dr float32
	// a is the quadratic's leading coefficient and invA its reciprocal, r0dr
	// and r0sq the constant terms of the other two, and invLen2 the reciprocal
	// of the axis length squared, which is what an axial shading needs instead.
	a, invA    float32
	r0dr, r0sq float32
	invLen2    float32
	radial     bool
	linear     bool
	ext0, ext1 bool
}

// newGradient prepares a type 2 or 3 shading for drawing under m, which maps
// the shading's own space to the device.
func newGradient(sh *Shade, m raster.Matrix, n int) *gradient {
	if len(sh.Function) == 0 {
		return nil
	}
	inv, ok := m.Invert()
	if !ok {
		return nil
	}
	c := sh.Coord6()
	g := &gradient{
		lut: shadeLUT(sh, n), n: n, inv: inv,
		x0: c[0], y0: c[1], r0: c[2],
		dx: c[3] - c[0], dy: c[4] - c[1], dr: c[5] - c[2],
		radial: sh.Type == 3,
		ext0:   sh.Extend[0], ext1: sh.Extend[1],
	}
	dx2 := float32(g.dx * g.dx)
	dy2 := float32(g.dy * g.dy)
	dr2 := float32(g.dr * g.dr)
	if !g.radial {
		if dx2+dy2 == 0 {
			return nil
		}
		g.invLen2 = 1 / (dx2 + dy2)
		return g
	}
	g.a = dx2 + dy2 - dr2
	g.r0dr = float32(g.r0 * g.dr)
	g.r0sq = float32(g.r0 * g.r0)
	g.linear = absf32(g.a) <= 1e-6*(dx2+dy2+dr2)
	if !g.linear {
		g.invA = 1 / g.a
	}
	return g
}

// Shade implements raster.Shader.
func (g *gradient) Shade(x, y, w int, span []uint8) {
	sn := g.n + 1
	fy := float32(y) + 0.5
	for i := 0; i < w; i++ {
		fx := float32(x+i) + 0.5
		u := float32(g.inv.A*fx) + float32(g.inv.C*fy) + g.inv.E
		v := float32(g.inv.B*fx) + float32(g.inv.D*fy) + g.inv.F
		out := span[i*sn:][:sn:sn]
		s, ok := g.param(u-g.x0, v-g.y0)
		if !ok {
			clear(out)
			continue
		}
		copy(out[:g.n], g.lut[int(s*255+0.5)*g.n:])
		out[g.n] = 255
	}
}

// param is the parameter of the point the gradient covers, in zero to one.
func (g *gradient) param(px, py float32) (float32, bool) {
	if !g.radial {
		return g.clampParam((float32(px*g.dx) + float32(py*g.dy)) * g.invLen2)
	}
	b := float32(px*g.dx) + float32(py*g.dy) + g.r0dr
	c := float32(px*px) + float32(py*py) - g.r0sq
	if g.linear {
		if b == 0 {
			return 0, false
		}
		return g.clampParam(c / (2 * b))
	}
	disc := float32(b*b) - float32(g.a*c)
	if disc < 0 {
		return 0, false
	}
	sq := float32(math.Sqrt(float64(disc)))
	s0, s1 := float32((b+sq)*g.invA), float32((b-sq)*g.invA)
	if g.a < 0 {
		s0, s1 = s1, s0
	}
	if s, ok := g.clampParam(s0); ok {
		return s, true
	}
	return g.clampParam(s1)
}

// clampParam applies the extend rules, and rejects a radius the parameter
// would make negative.
func (g *gradient) clampParam(s float32) (float32, bool) {
	if g.radial && g.r0+float32(s*g.dr) < 0 {
		return 0, false
	}
	if s < 0 {
		return 0, g.ext0
	}
	if s > 1 {
		return 1, g.ext1
	}
	return s, true
}

// pixmapShader reads destination colors from a pixmap sampled through an
// inverse transform, and is transparent outside it. It is how a type 1
// shading, evaluated over a grid of its domain, and a mesh, painted in device
// space, both reach the rasterizer.
type pixmapShader struct {
	px  *raster.Pixmap
	inv raster.Matrix
	n   int
}

// Shade implements raster.Shader.
func (s *pixmapShader) Shade(x, y, w int, span []uint8) {
	sn := s.n + 1
	pn := s.px.Comps()
	fy := float32(y) + 0.5
	for i := 0; i < w; i++ {
		out := span[i*sn:][:sn:sn]
		fx := float32(x+i) + 0.5
		u := float32(s.inv.A*fx) + float32(s.inv.C*fy) + s.inv.E
		v := float32(s.inv.B*fx) + float32(s.inv.D*fy) + s.inv.F
		j, k := int(floor32(u)), int(floor32(v))
		if j < 0 || k < 0 || j >= s.px.W || k >= s.px.H {
			clear(out)
			continue
		}
		src := s.px.Samples[k*s.px.Stride+j*pn:]
		copy(out[:s.n], src)
		out[s.n] = src[s.px.N]
	}
}
