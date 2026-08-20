package raster

// Shader is a source of color for the pixels a Rasterizer covers, in place of
// the single color a Paint carries.
type Shader interface {
	// Shade writes the color of w pixels starting at x, y into span: the
	// destination's color components and then one alpha for each pixel,
	// premultiplied by that alpha.
	Shade(x, y, w int, span []uint8)
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
			copy(row[:n], src[:min(n, sn)])
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
