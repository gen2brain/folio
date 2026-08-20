package raster

// FillImage sweeps what the rasterizer has, reading color from src rather
// than from the paint. inv maps a destination pixel to a source pixel, and
// smooth chooses bilinear sampling over nearest.
//
// A src with no color components is a stencil: the paint's color is painted
// through its alpha, which is how a one bit image mask draws.
func (r *Rasterizer) FillImage(dst, src *Pixmap, inv Matrix, paint Paint, smooth bool) {
	if src == nil || src.W == 0 || src.H == 0 {
		return
	}
	r.image.init(dst, src, inv, paint, smooth)
	r.Sweep(NonZero, &r.image)
}

// Subsample halves the pixmap n times with a box filter. Bringing a scan near
// its destination size before sampling it is both better looking and faster
// than point sampling the original.
func (p *Pixmap) Subsample(n int) *Pixmap {
	out := p
	for ; n > 0 && out.W > 1 && out.H > 1; n-- {
		out = out.halve()
	}
	return out
}

func (p *Pixmap) halve() *Pixmap {
	w, h := max(p.W/2, 1), max(p.H/2, 1)
	out := newPixmap(p.Model, p.N, w, h, p.Alpha)
	if out == nil {
		return p
	}
	out.X, out.Y = p.X, p.Y
	n := p.Comps()
	for y := 0; y < h; y++ {
		r0 := p.Row(min(y*2, p.H-1))
		r1 := p.Row(min(y*2+1, p.H-1))
		dst := out.Row(y)
		for x := 0; x < w; x++ {
			x0 := min(x*2, p.W-1) * n
			x1 := min(x*2+1, p.W-1) * n
			for c := 0; c < n; c++ {
				sum := uint32(r0[x0+c]) + uint32(r0[x1+c]) + uint32(r1[x0+c]) + uint32(r1[x1+c])
				dst[x*n+c] = uint8((sum + 2) >> 2)
			}
		}
	}
	return out
}

type imageBlitter struct {
	dst, src *Pixmap
	clip     *Pixmap
	ox, oy   int
	inv      Matrix
	color    [5]uint8
	alpha    uint8
	smooth   bool
	stencil  bool
}

func (b *imageBlitter) init(dst, src *Pixmap, inv Matrix, paint Paint, smooth bool) {
	b.dst, b.src, b.clip = dst, src, paint.Clip
	b.ox, b.oy = dst.X, dst.Y
	b.inv, b.alpha, b.smooth = inv, paint.Alpha, smooth
	b.stencil = src.N == 0
	b.color = [5]uint8{}
	copy(b.color[:min(len(paint.Color), dst.N)], paint.Color)
	if dst.Alpha {
		b.color[dst.N] = 255
	}
}

func (b *imageBlitter) BlitSolid(x, y, w int, cover uint8) {
	for i := 0; i < w; i++ {
		b.pixel(x+i, y, cover)
	}
}

func (b *imageBlitter) BlitCover(x, y int, cover []uint8) {
	for i, c := range cover {
		b.pixel(x+i, y, c)
	}
}

func (b *imageBlitter) pixel(x, y int, cover uint8) {
	a := mul255(b.alpha, cover)
	if a == 0 {
		return
	}
	if b.clip != nil {
		cx, cy := x-b.clip.X, y-b.clip.Y
		if cx < 0 || cy < 0 || cx >= b.clip.W || cy >= b.clip.H {
			return
		}
		if a = mul255(a, b.clip.Samples[cy*b.clip.Stride+cx]); a == 0 {
			return
		}
	}

	fx := float32(x) + 0.5
	fy := float32(y) + 0.5
	u := float32(b.inv.A*fx) + float32(b.inv.C*fy) + b.inv.E
	v := float32(b.inv.B*fx) + float32(b.inv.D*fy) + b.inv.F

	var src [5]uint8
	if b.smooth {
		b.bilinear(u, v, &src)
	} else {
		b.nearest(u, v, &src)
	}

	n := b.dst.Comps()
	row := b.dst.Samples[(y-b.oy)*b.dst.Stride+(x-b.ox)*n:][:n:n]

	if b.stencil {
		if sa := mul255(src[0], a); sa != 0 {
			blendSpan(row, b.color[:n], sa)
		}
		return
	}

	sa := a
	if b.src.Alpha {
		sa = mul255(src[b.src.N], a)
		if sa == 0 {
			return
		}
	}
	inv := 255 - uint32(sa)
	for c := 0; c < b.dst.N && c < b.src.N; c++ {
		row[c] = div255(uint32(src[c])*uint32(a) + uint32(row[c])*inv)
	}
	if b.dst.Alpha {
		s := uint32(255)
		if b.src.Alpha {
			s = uint32(src[b.src.N])
		}
		row[b.dst.N] = div255(s*uint32(a) + uint32(row[b.dst.N])*inv)
	}
}

// nearest reads the sample the point lands in, clamped to the edge.
func (b *imageBlitter) nearest(u, v float32, out *[5]uint8) {
	n := b.src.Comps()
	i := clampInt(int(floor32(u)), 0, b.src.W-1)
	j := clampInt(int(floor32(v)), 0, b.src.H-1)
	copy(out[:n], b.src.Samples[j*b.src.Stride+i*n:])
}

// bilinear weights the four samples around the point. The source is
// premultiplied, which is what keeps a transparent edge from bleeding color.
func (b *imageBlitter) bilinear(u, v float32, out *[5]uint8) {
	n := b.src.Comps()
	u -= 0.5
	v -= 0.5
	i0 := int(floor32(u))
	j0 := int(floor32(v))
	fu := uint32((u - float32(i0)) * 256)
	fv := uint32((v - float32(j0)) * 256)
	i1, j1 := clampInt(i0+1, 0, b.src.W-1), clampInt(j0+1, 0, b.src.H-1)
	i0, j0 = clampInt(i0, 0, b.src.W-1), clampInt(j0, 0, b.src.H-1)

	r0 := b.src.Samples[j0*b.src.Stride:]
	r1 := b.src.Samples[j1*b.src.Stride:]
	p00 := r0[i0*n:][:n:n]
	p01 := r0[i1*n:][:n:n]
	p10 := r1[i0*n:][:n:n]
	p11 := r1[i1*n:][:n:n]
	iu, iv := 256-fu, 256-fv
	for c := 0; c < n; c++ {
		top := uint32(p00[c])*iu + uint32(p01[c])*fu
		bot := uint32(p10[c])*iu + uint32(p11[c])*fu
		out[c] = uint8((top*iv + bot*fv + 1<<15) >> 16)
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func floor32(v float32) float32 {
	i := float32(int32(v))
	if v < i {
		i--
	}
	return i
}

// MulImage multiplies an alpha only pixmap by the alpha of src, sampled
// through inv, which maps a pixel of p to a pixel of src. It is how a stencil
// mask narrows a clip.
func (p *Pixmap) MulImage(src *Pixmap, inv Matrix) {
	if src == nil || src.W == 0 || src.H == 0 {
		return
	}
	n := src.Comps()
	off := 0
	if src.Alpha {
		off = src.N
	}
	for y := 0; y < p.H; y++ {
		row := p.Row(y)
		fy := float32(y+p.Y) + 0.5
		for x := 0; x < p.W; x++ {
			if row[x] == 0 {
				continue
			}
			fx := float32(x+p.X) + 0.5
			u := float32(inv.A*fx) + float32(inv.C*fy) + inv.E
			v := float32(inv.B*fx) + float32(inv.D*fy) + inv.F
			i := clampInt(int(floor32(u)), 0, src.W-1)
			j := clampInt(int(floor32(v)), 0, src.H-1)
			row[x] = mul255(row[x], src.Samples[j*src.Stride+i*n+off])
		}
	}
}
