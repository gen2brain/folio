package pdf

import "github.com/gen2brain/folio/raster"

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

	doc    *Document
	dict   Dict
	stream *Stream
}

// Transform returns the pattern matrix, which maps the shading's own space
// into the space the ctm a device is given starts from.
func (s *Shade) Transform() raster.Matrix { return s.Matrix }

// Bounds returns the shading's own bounding box, empty when it has none.
func (s *Shade) Bounds() raster.Rect { return s.BBox }

// ColorSpace returns the space the shading's colors are in.
func (s *Shade) ColorSpace() *ColorSpace { return s.CS }

// Shader prepares the shading to be painted into a destination of the given
// model, under ctm and over box, and returns nil when nothing is painted.
func (s *Shade) Shader(model raster.Model, ctm raster.Matrix, box raster.Rect) raster.Shader {
	switch s.Type {
	case 1:
		if p := s.functionShader(model, ctm, box); p != nil {
			return p
		}
	case 2, 3:
		if g := newGradient(s, ctm, model.Components()); g != nil {
			return g
		}
	case 4, 5, 6, 7:
		if p := s.meshShader(model, ctm, box); p != nil {
			return p
		}
	}
	return nil
}

// functionShader evaluates a type 1 shading over a grid of its domain, sized
// to how much of the plane the domain covers. The size deliberately does not
// depend on the clip: a shading drawn in two halves has to sample the same
// grid in both, or the halves do not meet.
func (s *Shade) functionShader(model raster.Model, m raster.Matrix, box raster.Rect) *pixmapShader {
	if len(s.Function) == 0 {
		return nil
	}
	full := raster.Concat(s.FuncMatrix, m)
	inv, ok := full.Invert()
	if !ok {
		return nil
	}
	dom := s.Domain4()
	dx, dy := dom[1]-dom[0], dom[3]-dom[2]
	if dx <= 0 || dy <= 0 {
		return nil
	}
	on := full.ApplyRect(raster.Rect{X0: dom[0], Y0: dom[2], X1: dom[1], Y1: dom[3]})
	if on.IsEmpty() || on.Intersect(box).IsEmpty() {
		return nil
	}
	w := clampInt(int(on.X1-on.X0)+1, 1, maxShadeGrid)
	h := clampInt(int(on.Y1-on.Y0)+1, 1, maxShadeGrid)
	px := raster.NewPixmap(model, w, h, true)
	if px == nil {
		return nil
	}

	n := px.Comps()
	col := make([]float32, shadeComps(s))
	for j := 0; j < h; j++ {
		y := float64(dom[2]) + (float64(j)+0.5)/float64(h)*float64(dy)
		row := px.Row(j)
		for i := 0; i < w; i++ {
			x := float64(dom[0]) + (float64(i)+0.5)/float64(w)*float64(dx)
			shadeColor(s, col, x, y)
			s.CS.Convert(col, row[i*n:i*n+px.N])
			row[i*n+px.N] = 255
		}
	}
	grid := raster.Matrix{
		A: float32(w) / dx, D: float32(h) / dy,
		E: -dom[0] * float32(w) / dx, F: -dom[2] * float32(h) / dy,
	}
	return &pixmapShader{px: px, inv: raster.Concat(inv, grid), n: model.Components()}
}

// meshShader paints a type 4 to 7 shading into a pixmap of the part of the
// page it covers. The triangles meet without anti-aliasing, so that the mesh
// is smooth inside and its own edge is the only one the rasterizer sees.
func (s *Shade) meshShader(model raster.Model, m raster.Matrix, box raster.Rect) *pixmapShader {
	x0, y0, x1, y1 := box.Outer()
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	px := raster.NewPixmap(model, x1-x0, y1-y0, true)
	if px == nil {
		return nil
	}
	if !s.paintMesh(raster.Concat(m, raster.Translate(float32(-x0), float32(-y0))), px) {
		return nil
	}
	return &pixmapShader{
		px: px, inv: raster.Translate(float32(-x0), float32(-y0)), n: model.Components(),
	}
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
		doc:        d,
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
		sh.FuncMatrix = d.matrix(dict["Matrix"], raster.Identity)
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
		sh.CS.Convert(col, lut[i*n:(i+1)*n])
	}
	return lut
}

// newGradient prepares a type 2 or 3 shading for drawing under m, which maps
// the shading's own space to the device. Everything after the table is
// geometry an SVG gradient has too, so it lives in raster.
func newGradient(sh *Shade, m raster.Matrix, n int) *raster.Gradient {
	if len(sh.Function) == 0 {
		return nil
	}
	c := sh.Coord6()
	return raster.NewGradient(raster.GradientSpec{
		Matrix: m,
		LUT:    shadeLUT(sh, n),
		N:      n,
		C0:     raster.Point{X: c[0], Y: c[1]},
		C1:     raster.Point{X: c[3], Y: c[4]},
		R0:     c[2],
		R1:     c[5],
		Radial: sh.Type == 3,
		Ext0:   sh.Extend[0],
		Ext1:   sh.Extend[1],
	})
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
