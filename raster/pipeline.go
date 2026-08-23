package raster

// Paint is the source color of a drawing operation and what modulates it.
type Paint struct {
	// Color has the destination's N components, straight, not premultiplied.
	Color []uint8
	// Alpha is the constant alpha of the operation, 255 for opaque.
	Alpha uint8
	// Clip multiplies the coverage. It is an alpha only pixmap positioned by
	// its X and Y, and nil when nothing clips.
	Clip *Pixmap
}

// Blitter returns a blitter that composites the paint into the pixmap under
// the coverage a Rasterizer produces.
func (p *Pixmap) Blitter(paint Paint) Blitter {
	b := &solidBlitter{}
	b.init(p, paint)
	return b
}

// Fill sweeps what the rasterizer has into a pixmap under the paint. It is
// Sweep with the blitter Blitter returns, through one the rasterizer keeps,
// so that filling many paths allocates nothing.
func (r *Rasterizer) Fill(dst *Pixmap, rule FillRule, paint Paint) {
	r.solid.init(dst, paint)
	r.Sweep(rule, &r.solid)
}

// MaskBlitter returns a blitter that writes coverage into an alpha only
// pixmap, replacing what is there. It writes in the pixmap's own coordinates,
// because what fills a mask has already been moved into them.
func (p *Pixmap) MaskBlitter() Blitter { return &maskBlitter{dst: p} }

// DrawMask composites the paint through an alpha mask, which its own X and Y
// place in the destination's coordinates. This is how a glyph is stamped.
func (p *Pixmap) DrawMask(mask *Pixmap, paint Paint) {
	if mask == nil || paint.Alpha == 0 {
		return
	}
	y0, y1 := max(mask.Y, p.Y), min(mask.Y+mask.H, p.Y+p.H)
	x0, x1 := max(mask.X, p.X), min(mask.X+mask.W, p.X+p.W)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	var b solidBlitter
	b.init(p, paint)
	for y := y0; y < y1; y++ {
		row := mask.Samples[(y-mask.Y)*mask.Stride+(x0-mask.X):]
		b.BlitCover(x0, y, row[:x1-x0])
	}
}

// MulMask multiplies an alpha only pixmap by another, over the part they
// share in the coordinates their X and Y are in, and zeroes the rest. It is
// how a nested clip narrows the one it is nested in.
func (p *Pixmap) MulMask(m *Pixmap) {
	if m == nil {
		return
	}
	y0, y1 := max(m.Y, p.Y), min(m.Y+m.H, p.Y+p.H)
	x0, x1 := max(m.X, p.X), min(m.X+m.W, p.X+p.W)
	for y := p.Y; y < p.Y+p.H; y++ {
		row := p.Samples[(y-p.Y)*p.Stride : (y-p.Y)*p.Stride+p.W]
		if y < y0 || y >= y1 || x0 >= x1 {
			clear(row)
			continue
		}
		clear(row[:x0-p.X])
		clear(row[x1-p.X:])
		src := m.Samples[(y-m.Y)*m.Stride+(x0-m.X):]
		mulRow(row[x0-p.X:x1-p.X], src[:x1-x0])
	}
}

type solidBlitter struct {
	dst  *Pixmap
	clip *Pixmap
	// samples, stride and base address the destination directly; base is where
	// the pixmap's own X and Y put its first sample.
	samples []uint8
	stride  int
	base    int
	comps   int
	lo      int
	src     [16]uint8
	alpha   uint8
}

func (b *solidBlitter) init(dst *Pixmap, paint Paint) {
	b.dst, b.clip, b.alpha = dst, paint.Clip, paint.Alpha
	b.samples, b.stride = dst.Samples, dst.Stride
	b.src = [16]uint8{}
	copy(b.src[:min(len(paint.Color), dst.N)], paint.Color)
	b.comps = dst.N
	if dst.Alpha {
		b.src[b.comps] = 255
		b.comps++
	}
	b.lo = coverLimit(b.comps)
	b.base = -dst.Y*dst.Stride - dst.X*b.comps
}

func (b *solidBlitter) BlitSolid(x, y, w int, cover uint8) {
	a := mul255(b.alpha, cover)
	if a == 0 {
		return
	}
	if b.clip != nil {
		b.clipped(x, y, w, a, nil)
		return
	}
	n := b.comps
	row := b.samples[b.base+y*b.stride+x*n:][: w*n : w*n]
	if a == 255 {
		copy(row, b.src[:b.comps])
		for done := n; done < len(row); {
			done += copy(row[done:], row[:done])
		}
		return
	}
	for i := 0; i < len(row); i += n {
		blendSpan(row[i:i+n], b.src[:b.comps], a)
	}
}

func (b *solidBlitter) BlitCover(x, y int, cover []uint8) {
	row := b.samples[b.base+y*b.stride+x*b.comps:]
	if b.clip == nil && b.alpha == 255 {
		if len(cover) >= b.lo && len(row) >= 16 {
			coverBlendVec(row, &b.src, cover, b.comps)
		} else {
			coverBlendScalar(row, &b.src, cover, b.comps)
		}
		return
	}
	if b.clip != nil {
		b.clipped(x, y, len(cover), 0, cover)
		return
	}
	b.blend(row, cover, b.alpha, nil)
}

// blend composites the source into a row of len(cover) pixels, folding the
// constant alpha and the clip mask into the coverage a chunk at a time so
// that the kernel sees one byte per pixel and nothing is allocated.
func (b *solidBlitter) blend(row, cover []uint8, alpha uint8, mask []uint8) {
	n := b.comps
	if alpha == 255 && mask == nil {
		coverBlend(row, &b.src, cover, n)
		return
	}
	if len(cover) < b.lo || len(row) < 16 {
		for i, c := range cover {
			a := mul255(alpha, c)
			if mask != nil {
				a = mul255(a, mask[i])
			}
			if a != 0 {
				blendSpan(row[:n], b.src[:n], a)
			}
			row = row[n:]
		}
		return
	}
	var buf [256]uint8
	for len(cover) > 0 {
		w := min(len(cover), len(buf))
		a := buf[:w]
		if alpha == 255 {
			copy(a, cover[:w])
		} else {
			scaleRow(a, cover[:w], alpha)
		}
		if mask != nil {
			mulRow(a, mask[:w])
			mask = mask[w:]
		}
		coverBlend(row, &b.src, a, n)
		row = row[w*n:]
		cover = cover[w:]
	}
}

// clipped is the same two blits with the clip mask folded in. cover is nil
// for a run of constant coverage, already multiplied into flat.
func (b *solidBlitter) clipped(x, y, w int, flat uint8, cover []uint8) {
	cy := y - b.clip.Y
	if cy < 0 || cy >= b.clip.H {
		return
	}
	cx := x - b.clip.X
	if cx < 0 {
		if cover != nil {
			cover = cover[-cx:]
		}
		w += cx
		x -= cx
		cx = 0
	}
	if cx+w > b.clip.W {
		w = b.clip.W - cx
	}
	if w <= 0 {
		return
	}
	mask := b.clip.Samples[cy*b.clip.Stride+cx:][:w]
	row := b.samples[b.base+y*b.stride+x*b.comps:]
	if cover == nil {
		b.blend(row, mask, flat, nil)
		return
	}
	b.blend(row, cover[:w], b.alpha, mask)
}

type maskBlitter struct {
	dst *Pixmap
}

func (b *maskBlitter) BlitSolid(x, y, w int, cover uint8) {
	row := b.dst.Samples[y*b.dst.Stride+x : y*b.dst.Stride+x+w]
	for i := range row {
		row[i] = cover
	}
}

func (b *maskBlitter) BlitCover(x, y int, cover []uint8) {
	copy(b.dst.Samples[y*b.dst.Stride+x:], cover)
}

func blendSpan(dst, src []uint8, a uint8) {
	inv := 255 - uint32(a)
	for i, s := range src {
		dst[i] = div255(uint32(s)*uint32(a) + uint32(dst[i])*inv)
	}
}

func mul255(a, b uint8) uint8 {
	t := uint32(a)*uint32(b) + 128
	return uint8((t + t>>8) >> 8)
}

func div255(t uint32) uint8 {
	t += 128
	return uint8((t + t>>8) >> 8)
}
