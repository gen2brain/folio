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
	span     []uint8
	col      []uint16
	color    [5]uint8
	alpha    uint8
	smooth   bool
	stencil  bool
	direct   bool
}

func (b *imageBlitter) init(dst, src *Pixmap, inv Matrix, paint Paint, smooth bool) {
	b.dst, b.src, b.clip = dst, src, paint.Clip
	b.ox, b.oy = dst.X, dst.Y
	b.inv, b.alpha, b.smooth = inv, paint.Alpha, smooth
	b.stencil = src.N == 0
	b.direct = !b.stencil && !src.Alpha && !dst.Alpha && dst.N == src.N
	b.color = [5]uint8{}
	copy(b.color[:min(len(paint.Color), dst.N)], paint.Color)
	if dst.Alpha {
		b.color[dst.N] = 255
	}
}

func (b *imageBlitter) BlitSolid(x, y, w int, cover uint8) {
	if a := mul255(b.alpha, cover); a != 0 {
		b.blit(x, y, w, a, nil)
	}
}

func (b *imageBlitter) BlitCover(x, y int, cover []uint8) {
	b.blit(x, y, len(cover), 0, cover)
}

// blit samples the source over a whole span before compositing it.
func (b *imageBlitter) blit(x, y, w int, flat uint8, cover []uint8) {
	var mask []uint8
	if b.clip != nil {
		cy := y - b.clip.Y
		if cy < 0 || cy >= b.clip.H {
			return
		}
		lo := max(b.clip.X-x, 0)
		hi := min(b.clip.X+b.clip.W-x, w)
		if lo >= hi {
			return
		}
		mask = b.clip.Samples[cy*b.clip.Stride+(x+lo-b.clip.X):][: hi-lo : hi-lo]
		if cover != nil {
			cover = cover[lo:hi]
		}
		x += lo
		w = hi - lo
	}

	sn := b.src.Comps()
	n := b.dst.Comps()
	row := b.dst.Samples[(y-b.oy)*b.dst.Stride+(x-b.ox)*n:]

	// An opaque image at full coverage over a destination of the same shape
	// composites to itself, so it is sampled straight into the page.
	if b.direct && flat == 255 && cover == nil && mask == nil {
		b.sample(x, y, w, row)
		return
	}

	if cap(b.span) < w*sn {
		b.span = make([]uint8, w*sn)
	}
	span := b.span[:w*sn]
	b.sample(x, y, w, span)

	for i := range w {
		a := flat
		if cover != nil {
			a = mul255(b.alpha, cover[i])
		}
		if mask != nil {
			a = mul255(a, mask[i])
		}
		if a != 0 {
			b.compose(row[:n:n], span[:sn:sn], a)
		}
		row = row[n:]
		span = span[sn:]
	}
}

// compose puts one sampled pixel down. The source is premultiplied, so the
// color takes the operation's alpha and only the destination takes the
// source's.
func (b *imageBlitter) compose(row, src []uint8, a uint8) {
	n := b.dst.Comps()
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
	m := min(b.dst.N, b.src.N)
	for c, s := range src[:m:m] {
		row[c] = div255(uint32(s)*uint32(a) + uint32(row[c])*inv)
	}
	if b.dst.Alpha {
		s := uint32(255)
		if b.src.Alpha {
			s = uint32(src[b.src.N])
		}
		row[b.dst.N] = div255(s*uint32(a) + uint32(row[b.dst.N])*inv)
	}
}

// sample reads one source pixel per destination pixel of the span. The sums
// keep their association: a float32 sum that reassociates lands on a different
// sample at the seams.
func (b *imageBlitter) sample(x, y, w int, out []uint8) {
	n := b.src.Comps()
	fy := float32(y) + 0.5
	cu := float32(b.inv.C * fy)
	cv := float32(b.inv.D * fy)
	if b.smooth {
		if b.inv.B == 0 {
			b.bilinearRow(x, w, cu, cv+b.inv.F, out)
			return
		}
		for i := range w {
			fx := float32(x+i) + 0.5
			u := float32(b.inv.A*fx) + cu + b.inv.E
			v := float32(b.inv.B*fx) + cv + b.inv.F
			b.bilinear(u, v, out[i*n:])
		}
		return
	}
	for i := range w {
		fx := float32(x+i) + 0.5
		u := float32(b.inv.A*fx) + cu + b.inv.E
		v := float32(b.inv.B*fx) + cv + b.inv.F
		b.nearest(u, v, out[i*n:])
	}
}

// bilinearRow is the same weighted sum for a span whose source row does not
// change along it, which is what an unrotated image is.
//
// The four products regroup exactly into iu*(r0*iv + r1*fv) + fu*(r0'*iv +
// r1'*fv), so the vertical blend belongs to the source column rather than to
// the pixel that reads it. The rounding still happens once, at the end.
func (b *imageBlitter) bilinearRow(x, w int, cu, v float32, out []uint8) {
	n := b.src.Comps()
	v -= 0.5
	j0 := ifloor32(v)
	fv := uint32((v - float32(j0)) * 256)
	j1 := clampInt(j0+1, 0, b.src.H-1)
	j0 = clampInt(j0, 0, b.src.H-1)
	r0 := b.src.Samples[j0*b.src.Stride:]
	r1 := b.src.Samples[j1*b.src.Stride:]
	iv := 256 - fv
	hi := b.src.W - 1

	e0 := ifloor32(b.ucoord(x, cu))
	e1 := ifloor32(b.ucoord(x+w-1, cu))
	k0 := min(e0, e1)
	if cols := max(e0, e1) - k0 + 2; cols < w {
		col := b.column(r0, r1, k0, cols, n, iv, fv)
		bilinearSpan(out, col, w, n, k0, x, b.inv.A, cu, b.inv.E)
		return
	}

	for i := range w {
		u := b.ucoord(x+i, cu)
		i0 := ifloor32(u)
		fu := uint32((u - float32(i0)) * 256)
		i1 := clampInt(i0+1, 0, hi) * n
		i0 = clampInt(i0, 0, hi) * n
		iu := 256 - fu
		a0 := r0[i0 : i0+n : i0+n]
		a1 := r0[i1 : i1+n : i1+n]
		c0 := r1[i0 : i0+n : i0+n]
		c1 := r1[i1 : i1+n : i1+n]
		p := out[i*n : i*n+n : i*n+n]
		for c, s := range a0 {
			top := uint32(s)*iu + uint32(a1[c])*fu
			bot := uint32(c0[c])*iu + uint32(c1[c])*fu
			p[c] = uint8((top*iv + bot*fv + 1<<15) >> 16)
		}
	}
}

// ucoord is where a destination pixel lands along the source row, less the
// half pixel the sample grid is offset by. It is the sum bilinearSpanScalar
// makes, in the association the general path uses.
func (b *imageBlitter) ucoord(x int, cu float32) float32 {
	return float32(b.inv.A*(float32(x)+0.5)) + cu + b.inv.E - 0.5
}

// column blends the two source rows into the one the span reads, over every
// column the span reaches whether or not the image has it: an index outside
// the image repeats the edge here rather than being clamped per pixel. The
// weights sum to 256 and the samples are bytes, so a blended column is at most
// 65280 and the sum is exact.
func (b *imageBlitter) column(r0, r1 []uint8, k0, cols, n int, iv, fv uint32) []uint16 {
	m := cols * n
	if cap(b.col) < m+bilinearSlack {
		b.col = make([]uint16, m+bilinearSlack)
	}
	col := b.col[:m:m]
	hi := b.src.W - 1

	// The columns the image has are contiguous in both rows and blend as one
	// run of bytes; only the ones off either end repeat an edge.
	head := clampInt(-k0, 0, cols)
	tail := max(clampInt(hi-k0+1, 0, cols), head)
	for k := range head {
		blendRows(col[k*n:k*n+n:k*n+n], r0[:n:n], r1[:n:n], iv, fv)
	}
	if tail > head {
		s := (k0 + head) * n
		w := (tail - head) * n
		blendRows(col[head*n:head*n+w], r0[s:s+w], r1[s:s+w], iv, fv)
	}
	for k := tail; k < cols; k++ {
		s := hi * n
		blendRows(col[k*n:k*n+n:k*n+n], r0[s:s+n:s+n], r1[s:s+n:s+n], iv, fv)
	}
	return col
}

// nearest reads the sample the point lands in, clamped to the edge.
func (b *imageBlitter) nearest(u, v float32, out []uint8) {
	n := b.src.Comps()
	i := clampInt(ifloor32(u), 0, b.src.W-1)
	j := clampInt(ifloor32(v), 0, b.src.H-1)
	copy(out[:n], b.src.Samples[j*b.src.Stride+i*n:])
}

// bilinear weights the four samples around the point. The source is
// premultiplied, which is what keeps a transparent edge from bleeding color.
func (b *imageBlitter) bilinear(u, v float32, out []uint8) {
	n := b.src.Comps()
	u -= 0.5
	v -= 0.5
	i0 := ifloor32(u)
	j0 := ifloor32(v)
	fu := uint32((u - float32(i0)) * 256)
	fv := uint32((v - float32(j0)) * 256)
	i1, j1 := clampInt(i0+1, 0, b.src.W-1), clampInt(j0+1, 0, b.src.H-1)
	i0, j0 = clampInt(i0, 0, b.src.W-1), clampInt(j0, 0, b.src.H-1)

	r0 := b.src.Samples[j0*b.src.Stride:]
	r1 := b.src.Samples[j1*b.src.Stride:]
	i0 *= n
	i1 *= n
	p00 := r0[i0 : i0+n : i0+n]
	p01 := r0[i1 : i1+n : i1+n]
	p10 := r1[i0 : i0+n : i0+n]
	p11 := r1[i1 : i1+n : i1+n]
	p := out[:n:n]
	iu, iv := 256-fu, 256-fv
	for c, s := range p00 {
		top := uint32(s)*iu + uint32(p01[c])*fu
		bot := uint32(p10[c])*iu + uint32(p11[c])*fu
		p[c] = uint8((top*iv + bot*fv + 1<<15) >> 16)
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

func ifloor32(v float32) int {
	i := int(int32(v))
	if v < float32(i) {
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

// A Shrinker halves rows as they arrive and cascades them through as many
// levels as Subsample would have used, so that an image may be reduced to the
// size it will be drawn at without the full size pixmap ever existing. What it
// returns is what Subsample would have built from that pixmap, byte for byte.
//
// The caller fills the slice Row returns and calls Commit, once per row of the
// source, then takes the result from Pixmap.
type Shrinker struct {
	out  *Pixmap
	n    int
	w    []int
	src  []uint8
	pend [][]uint8
	held []bool
	rows [][]uint8
	y    int
}

// NewShrinker reduces a w by h image of the model's components n times.
func NewShrinker(model Model, w, h int, alpha bool, n int) *Shrinker {
	comps := 0
	if model != nil {
		comps = model.Components()
	}
	return newShrinker(model, comps, w, h, alpha, n)
}

// NewMaskShrinker is NewShrinker for the one byte coverage NewMask returns.
func NewMaskShrinker(w, h, n int) *Shrinker { return newShrinker(nil, 0, w, h, true, n) }

func newShrinker(model Model, comps, w, h int, alpha bool, n int) *Shrinker {
	widths := []int{w}
	for sw, sh := w, h; n > 0 && sw > 1 && sh > 1; n-- {
		sw, sh = max(sw/2, 1), max(sh/2, 1)
		widths = append(widths, sw)
	}
	last := len(widths) - 1
	out := newPixmap(model, comps, widths[last], heightAfter(h, last), alpha)
	if out == nil {
		return nil
	}
	s := &Shrinker{out: out, n: out.Comps(), w: widths}
	if last == 0 {
		return s
	}
	s.src = make([]uint8, w*s.n)
	s.pend = make([][]uint8, last)
	s.held = make([]bool, last)
	s.rows = make([][]uint8, last+1)
	for k := 0; k < last; k++ {
		s.pend[k] = make([]uint8, widths[k]*s.n)
		s.rows[k+1] = make([]uint8, widths[k+1]*s.n)
	}
	return s
}

// heightAfter is the height h becomes after k halvings.
func heightAfter(h, k int) int {
	for ; k > 0; k-- {
		h = max(h/2, 1)
	}
	return h
}

// Row returns the buffer for the next source row, zeroed, which the caller
// fills and then commits. A row the caller leaves short reads as zero, which
// is what writing into a fresh pixmap did.
func (s *Shrinker) Row() []uint8 {
	if len(s.w) == 1 {
		if s.y >= s.out.H {
			return nil
		}
		return s.out.Row(s.y)
	}
	clear(s.src)
	return s.src
}

// Commit folds the row Row returned into the result.
func (s *Shrinker) Commit() {
	if len(s.w) == 1 {
		s.y++
		return
	}
	s.feed(0, s.src)
}

func (s *Shrinker) feed(k int, row []uint8) {
	if k == len(s.w)-1 {
		if s.y < s.out.H {
			copy(s.out.Row(s.y), row)
			s.y++
		}
		return
	}
	if !s.held[k] {
		copy(s.pend[k], row)
		s.held[k] = true
		return
	}
	s.held[k] = false
	dst := s.rows[k+1]
	halveRow(dst, s.pend[k], row, s.w[k+1], s.n)
	s.feed(k+1, dst)
}

// halveRow averages two source rows into one of half the width, which is what
// halve does to a pair of rows away from the edges it clamps.
func halveRow(dst, a, b []uint8, w, n int) {
	for x := 0; x < w; x++ {
		x0, x1 := 2*x*n, (2*x+1)*n
		for c := 0; c < n; c++ {
			sum := uint32(a[x0+c]) + uint32(a[x1+c]) + uint32(b[x0+c]) + uint32(b[x1+c])
			dst[x*n+c] = uint8((sum + 2) >> 2)
		}
	}
}

// Pixmap is the reduced image.
func (s *Shrinker) Pixmap() *Pixmap { return s.out }
