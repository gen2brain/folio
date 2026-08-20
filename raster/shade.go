package raster

import "math"

// Shader is a source of color for the pixels a Rasterizer covers, in place of
// the single color a Paint carries.
type Shader interface {
	// Shade writes the color of w pixels starting at x, y into span: the
	// destination's color components and then one alpha for each pixel,
	// premultiplied by that alpha.
	Shade(x, y, w int, span []uint8)
}

// A RowShader can write its color straight into a destination row, leaving
// the pixels it does not cover as it found them. It is what a shader whose
// coverage is all or nothing can do instead of filling a span that is then
// copied out of, which for a gradient is half the work of shading a page.
type RowShader interface {
	Shader
	ShadeRow(dst *Pixmap, x, y, w int)
}

// FillShader sweeps what the rasterizer has into a pixmap, taking color from
// sh and everything else from the paint.
func (r *Rasterizer) FillShader(dst *Pixmap, rule FillRule, sh Shader, paint Paint) {
	r.shader.init(dst, sh, paint)
	r.Sweep(rule, &r.shader)
}

type shaderBlitter struct {
	dst    *Pixmap
	clip   *Pixmap
	ox, oy int
	sh     Shader
	alpha  uint8
	span   []uint8
}

func (b *shaderBlitter) init(dst *Pixmap, sh Shader, paint Paint) {
	b.dst, b.clip, b.sh, b.alpha = dst, paint.Clip, sh, paint.Alpha
	b.ox, b.oy = dst.X, dst.Y
	if n := dst.W * (dst.N + 1); cap(b.span) < n {
		b.span = make([]uint8, n)
	} else {
		b.span = b.span[:n]
	}
}

func (b *shaderBlitter) BlitSolid(x, y, w int, cover uint8) {
	if a := mul255(b.alpha, cover); a != 0 {
		b.blit(x, y, w, a, nil)
	}
}

func (b *shaderBlitter) BlitCover(x, y int, cover []uint8) {
	b.blit(x, y, len(cover), 0, cover)
}

func (b *shaderBlitter) blit(x, y, w int, flat uint8, cover []uint8) {
	if cover == nil && flat == 255 && b.clip == nil {
		if rs, ok := b.sh.(RowShader); ok {
			rs.ShadeRow(b.dst, x, y, w)
			return
		}
	}
	sn := b.dst.N + 1
	span := b.span[:w*sn]
	b.sh.Shade(x, y, w, span)

	n := b.dst.Comps()
	row := b.dst.Samples[(y-b.oy)*b.dst.Stride+(x-b.ox)*n:]
	if cover == nil && flat == 255 && b.clip == nil {
		b.opaque(row, span, w, sn, n)
		return
	}
	for i := 0; i < w; i++ {
		a := flat
		if cover != nil {
			a = mul255(b.alpha, cover[i])
		}
		src := span[i*sn:][:sn:sn]
		if a == 0 || src[b.dst.N] == 0 {
			row = row[n:]
			continue
		}
		if b.clip != nil {
			cx, cy := x+i-b.clip.X, y-b.clip.Y
			if cx < 0 || cy < 0 || cx >= b.clip.W || cy >= b.clip.H {
				row = row[n:]
				continue
			}
			if a = mul255(a, b.clip.Samples[cy*b.clip.Stride+cx]); a == 0 {
				row = row[n:]
				continue
			}
		}
		inv := 255 - uint32(mul255(src[b.dst.N], a))
		for c := 0; c < b.dst.N; c++ {
			row[c] = div255(uint32(src[c])*uint32(a) + uint32(row[c])*inv)
		}
		if b.dst.Alpha {
			row[b.dst.N] = div255(uint32(src[b.dst.N])*uint32(a) + uint32(row[b.dst.N])*inv)
		}
		row = row[n:]
	}
}

// opaque is blit with full coverage, no clip and no constant alpha.
func (b *shaderBlitter) opaque(row, span []uint8, w, sn, n int) {
	for i := 0; i < w; i++ {
		src := span[i*sn:][:sn:sn]
		sa := src[b.dst.N]
		switch sa {
		case 0:
		case 255:
			for c, v := range src[:min(n, sn)] {
				row[c] = v
			}
		default:
			inv := 255 - uint32(sa)
			for c := 0; c < b.dst.N; c++ {
				row[c] = div255(uint32(src[c])*255 + uint32(row[c])*inv)
			}
			if b.dst.Alpha {
				row[b.dst.N] = div255(uint32(sa)*255 + uint32(row[b.dst.N])*inv)
			}
		}
		row = row[n:]
	}
}

// GradientSpec describes an axial or radial gradient: the two circles it runs
// between — an axial gradient ignores the radii and runs between the two
// points — the table of 256 colors of N components it takes its color from,
// the transform from its own space to the device, and whether it paints on
// past each end.
type GradientSpec struct {
	Matrix     Matrix
	LUT        []uint8
	N          int
	C0, C1     Point
	R0, R1     float32
	Radial     bool
	Ext0, Ext1 bool
}

// Gradient is a Shader over one of those. It is what a PDF type 2 or type 3
// shading and an SVG linearGradient or radialGradient all evaluate to, which
// is why it is here and not in the document layer.
type Gradient struct {
	lut []uint8
	n   int
	inv Matrix

	x0, y0, r0 float32
	dx, dy, dr float32
	// a is the quadratic's leading coefficient and invA its reciprocal, r0dr
	// and r0sq the constant terms of the other two, and invLen2 the reciprocal
	// of the axis length squared, which is what an axial gradient needs
	// instead.
	a, invA    float32
	r0dr, r0sq float32
	invLen2    float32
	radial     bool
	linear     bool
	ext0, ext1 bool
}

// NewGradient prepares a gradient for drawing, nil if it degenerates to
// nothing: an axial gradient of no length, or a transform that does not
// invert.
func NewGradient(s GradientSpec) *Gradient {
	inv, ok := s.Matrix.Invert()
	if !ok || s.N < 1 || len(s.LUT) < 256*s.N {
		return nil
	}
	g := &Gradient{
		lut: s.LUT, n: s.N, inv: inv,
		x0: s.C0.X, y0: s.C0.Y, r0: s.R0,
		dx: s.C1.X - s.C0.X, dy: s.C1.Y - s.C0.Y, dr: s.R1 - s.R0,
		radial: s.Radial,
		ext0:   s.Ext0, ext1: s.Ext1,
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
	g.linear = abs32(g.a) <= 1e-6*(dx2+dy2+dr2)
	if !g.linear {
		g.invA = 1 / g.a
	}
	return g
}

// Shade writes one color per pixel of the span, opaque where the gradient
// covers the point and clear where it does not.
func (g *Gradient) Shade(x, y, w int, span []uint8) {
	g.shade(x, y, w, span, g.n+1, g.n, g.n, true)
}

// ShadeRow writes the same colors straight into a destination row, in that
// pixmap's own shape, and leaves a pixel it does not cover alone.
func (g *Gradient) ShadeRow(dst *Pixmap, x, y, w int) {
	n := dst.Comps()
	ai := -1
	if dst.Alpha {
		ai = dst.N
	}
	row := dst.Samples[(y-dst.Y)*dst.Stride+(x-dst.X)*n:]
	g.shade(x, y, w, row, n, min(g.n, dst.N), ai, false)
}

// shade walks a span, writing m color components and an opaque alpha at ai
// into every n bytes. blank says what an uncovered pixel gets: the span form
// zeroes it, the row form leaves it. The two kinds of gradient are separate
// loops because which one it is does not change along a span, and the axial
// one is a dot product where the radial one is a quadratic.
func (g *Gradient) shade(x, y, w int, out []uint8, n, m, ai int, blank bool) {
	fy := float32(y) + 0.5
	if g.radial {
		for i := 0; i < w; i++ {
			fx := float32(x+i) + 0.5
			u := float32(g.inv.A*fx) + float32(g.inv.C*fy) + g.inv.E
			v := float32(g.inv.B*fx) + float32(g.inv.D*fy) + g.inv.F
			s, ok := g.radialParam(u-g.x0, v-g.y0)
			g.put(out[:n:n], s, m, ai, ok, blank)
			out = out[n:]
		}
		return
	}
	for i := 0; i < w; i++ {
		fx := float32(x+i) + 0.5
		u := float32(g.inv.A*fx) + float32(g.inv.C*fy) + g.inv.E
		v := float32(g.inv.B*fx) + float32(g.inv.D*fy) + g.inv.F
		s, ok := g.axialParam(u-g.x0, v-g.y0)
		g.put(out[:n:n], s, m, ai, ok, blank)
		out = out[n:]
	}
}

func (g *Gradient) put(out []uint8, s float32, m, ai int, ok, blank bool) {
	if !ok {
		if blank {
			for c := range out {
				out[c] = 0
			}
		}
		return
	}
	k := int(s*255+0.5) * g.n
	for c, v := range g.lut[k : k+m : k+m] {
		out[c] = v
	}
	if ai >= 0 {
		out[ai] = 255
	}
}

// axialParam is where the point falls along the axis, in zero to one.
func (g *Gradient) axialParam(px, py float32) (float32, bool) {
	s := (float32(px*g.dx) + float32(py*g.dy)) * g.invLen2
	if s < 0 {
		return 0, g.ext0
	}
	if s > 1 {
		return 1, g.ext1
	}
	return s, true
}

// radialParam solves the quadratic for the larger of the two circles that
// reach the point, and takes the smaller one where the larger is off the end.
func (g *Gradient) radialParam(px, py float32) (float32, bool) {
	b := float32(px*g.dx) + float32(py*g.dy) + g.r0dr
	c := float32(px*px) + float32(py*py) - g.r0sq
	if g.linear {
		if b == 0 {
			return 0, false
		}
		return g.clampRadial(c / (2 * b))
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
	if s, ok := g.clampRadial(s0); ok {
		return s, true
	}
	return g.clampRadial(s1)
}

// clampRadial applies the extend rules, and rejects a radius the parameter
// would make negative.
func (g *Gradient) clampRadial(s float32) (float32, bool) {
	if g.r0+float32(s*g.dr) < 0 {
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

// Vertex is a corner of a Gouraud shaded triangle: a point in the pixmap's
// own coordinates and a color of its components.
type Vertex struct {
	X, Y  float32
	Color [4]uint8
}

// FillTriangle fills a triangle, interpolating color linearly between its
// corners, and leaves the pixels it covers opaque. It does not anti-alias,
// because two triangles that share an edge have to meet without a seam; a
// mesh is anti-aliased at its own boundary instead, when it is composited.
func (p *Pixmap) FillTriangle(v0, v1, v2 Vertex) {
	area := float32((v1.X-v0.X)*(v2.Y-v0.Y)) - float32((v2.X-v0.X)*(v1.Y-v0.Y))
	if area == 0 {
		return
	}
	if area < 0 {
		v1, v2 = v2, v1
		area = -area
	}
	scale := 1 / area

	x0 := clampInt(int(floor32(min32(v0.X, min32(v1.X, v2.X)))), 0, p.W)
	x1 := clampInt(int(floor32(max32(v0.X, max32(v1.X, v2.X))))+1, 0, p.W)
	y0 := clampInt(int(floor32(min32(v0.Y, min32(v1.Y, v2.Y)))), 0, p.H)
	y1 := clampInt(int(floor32(max32(v0.Y, max32(v1.Y, v2.Y))))+1, 0, p.H)

	n := p.Comps()
	for y := y0; y < y1; y++ {
		py := float32(y) + 0.5
		row := p.Row(y)
		for x := x0; x < x1; x++ {
			px := float32(x) + 0.5
			w0 := float32((v2.X-v1.X)*(py-v1.Y)) - float32((v2.Y-v1.Y)*(px-v1.X))
			if w0 < 0 {
				continue
			}
			w1 := float32((v0.X-v2.X)*(py-v2.Y)) - float32((v0.Y-v2.Y)*(px-v2.X))
			if w1 < 0 {
				continue
			}
			w2 := float32((v1.X-v0.X)*(py-v0.Y)) - float32((v1.Y-v0.Y)*(px-v0.X))
			if w2 < 0 {
				continue
			}
			w0, w1, w2 = float32(w0*scale), float32(w1*scale), float32(w2*scale)
			out := row[x*n:][:n:n]
			for c := 0; c < p.N && c < len(v0.Color); c++ {
				v := float32(w0*float32(v0.Color[c])) +
					float32(w1*float32(v1.Color[c])) +
					float32(w2*float32(v2.Color[c]))
				out[c] = clampByte(v)
			}
			if p.Alpha {
				out[p.N] = 255
			}
		}
	}
}

func clampByte(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
