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

// A RowShader can write its color straight into a destination row, leaving the
// pixels it does not cover as it found them, which is what a shader whose
// coverage is all or nothing can do instead of filling a span to be copied
// out of.
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
		// The row form overwrites, so only an opaque shader may take it.
		if o, ok := b.sh.(interface{ Opaque() bool }); !ok || o.Opaque() {
			if rs, ok := b.sh.(RowShader); ok {
				rs.ShadeRow(b.dst, x, y, w)
				return
			}
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
	Matrix Matrix
	LUT    []uint8
	// A is one opacity per entry of the table, and nil for a gradient that
	// is opaque throughout. LUT is premultiplied by it.
	A          []uint8
	N          int
	C0, C1     Point
	R0, R1     float32
	Radial     bool
	Ext0, Ext1 bool
}

// Gradient is a Shader over one of those: what a PDF type 2 or type 3 shading
// and an SVG linearGradient or radialGradient all evaluate to.
type Gradient struct {
	lut    []uint8
	a      []uint8
	n      int
	p      gradParams
	radial bool
	linear bool
	ext0   bool
	ext1   bool
}

// gradParams is everything the parameter of a point depends on, in one block
// of four byte fields so that a kernel can read it by offset. a is the
// quadratic's leading coefficient and invA its reciprocal, r0dr and r0sq the
// constant terms of the other two, invLen2 the reciprocal of the axis length
// squared, which is what an axial gradient needs instead, and sgn which root
// of the quadratic comes first. lo and hi are what the parameter is tested
// against: zero and one, or an infinity where the gradient extends past that
// end, which is the extend rule written as a comparison rather than a flag.
type gradParams struct {
	ia, ib, ic, id float32
	ie, if_        float32
	x0, y0         float32
	dx, dy         float32
	r0, dr         float32
	a, invA        float32
	r0dr, r0sq     float32
	invLen2        float32
	sgn            float32
	lo, hi         float32
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
		lut: s.LUT, a: s.A, n: s.N,
		radial: s.Radial,
		ext0:   s.Ext0, ext1: s.Ext1,
		p: gradParams{
			ia: inv.A, ib: inv.B, ic: inv.C, id: inv.D,
			ie: inv.E, if_: inv.F,
			x0: s.C0.X, y0: s.C0.Y,
			dx: s.C1.X - s.C0.X, dy: s.C1.Y - s.C0.Y,
			r0: s.R0, dr: s.R1 - s.R0,
			sgn: 1,
			lo:  0, hi: 1,
		},
	}
	if s.Ext0 {
		g.p.lo = float32(math.Inf(-1))
	}
	if s.Ext1 {
		g.p.hi = float32(math.Inf(1))
	}
	dx2 := float32(g.p.dx * g.p.dx)
	dy2 := float32(g.p.dy * g.p.dy)
	dr2 := float32(g.p.dr * g.p.dr)
	if !g.radial {
		if dx2+dy2 == 0 {
			return nil
		}
		g.p.invLen2 = 1 / (dx2 + dy2)
		return g
	}
	g.p.a = dx2 + dy2 - dr2
	g.p.r0dr = float32(g.p.r0 * g.p.dr)
	g.p.r0sq = float32(g.p.r0 * g.p.r0)
	g.linear = abs32(g.p.a) <= 1e-6*(dx2+dy2+dr2)
	if !g.linear {
		g.p.invA = 1 / g.p.a
		if g.p.a < 0 {
			g.p.sgn = -1
		}
	}
	return g
}

// Opaque reports that every entry of the table is fully opaque.
func (g *Gradient) Opaque() bool { return g.a == nil }

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
// zeroes it, the row form leaves it.
func (g *Gradient) shade(x, y, w int, out []uint8, n, m, ai int, blank bool) {
	var buf [64]int32
	for w > 0 {
		k := min(w, len(buf))
		g.index(x, y, k, buf[:k])
		for _, e := range buf[:k] {
			p := out[:n:n]
			switch {
			case e >= 0:
				j := int(e) * g.n
				for c, v := range g.lut[j : j+m : j+m] {
					p[c] = v
				}
				if ai >= 0 {
					if g.a != nil {
						p[ai] = g.a[e]
					} else {
						p[ai] = 255
					}
				}
			case blank:
				for c := range p {
					p[c] = 0
				}
			}
			out = out[n:]
		}
		x += k
		w -= k
	}
}

// indexScalar writes the entry of the color table each pixel of the span
// takes, and -1 where the gradient does not reach it. It is what the kernels
// are checked against.
func (g *Gradient) indexScalar(x, y, w int, idx []int32) {
	p := &g.p
	fy := float32(y) + 0.5
	cu := float32(p.ic * fy)
	cv := float32(p.id * fy)
	if g.radial {
		for i := range idx[:w] {
			fx := float32(x+i) + 0.5
			u := float32(p.ia*fx) + cu + p.ie
			v := float32(p.ib*fx) + cv + p.if_
			if s, ok := g.radialParam(u-p.x0, v-p.y0); ok {
				idx[i] = int32(s*255 + 0.5)
			} else {
				idx[i] = -1
			}
		}
		return
	}
	for i := range idx[:w] {
		fx := float32(x+i) + 0.5
		u := float32(p.ia*fx) + cu + p.ie
		v := float32(p.ib*fx) + cv + p.if_
		if s, ok := g.axialParam(u-p.x0, v-p.y0); ok {
			idx[i] = int32(s*255 + 0.5)
		} else {
			idx[i] = -1
		}
	}
}

// axialParam is where the point falls along the axis, in zero to one.
func (g *Gradient) axialParam(px, py float32) (float32, bool) {
	s := (float32(px*g.p.dx) + float32(py*g.p.dy)) * g.p.invLen2
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
	b := float32(px*g.p.dx) + float32(py*g.p.dy) + g.p.r0dr
	c := float32(px*px) + float32(py*py) - g.p.r0sq
	if g.linear {
		if b == 0 {
			return 0, false
		}
		return g.clampRadial(c / (2 * b))
	}
	disc := float32(b*b) - float32(g.p.a*c)
	if disc < 0 {
		return 0, false
	}
	sq := float32(math.Sqrt(float64(disc))) * g.p.sgn
	s0, s1 := float32((b+sq)*g.p.invA), float32((b-sq)*g.p.invA)
	if s, ok := g.clampRadial(s0); ok {
		return s, true
	}
	return g.clampRadial(s1)
}

// clampRadial applies the extend rules, and rejects a radius the parameter
// would make negative.
func (g *Gradient) clampRadial(s float32) (float32, bool) {
	if g.p.r0+float32(s*g.p.dr) < 0 {
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
