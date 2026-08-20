package raster

// FillRule decides which parts of a self intersecting path are inside.
type FillRule int

// The two fill rules of ISO 32000-1 8.5.3.3.
const (
	NonZero FillRule = iota
	EvenOdd
)

// Blitter consumes the coverage a Rasterizer produces, one run at a time.
// The cover slice passed to BlitCover is reused and must not be retained.
type Blitter interface {
	BlitSolid(x, y, w int, alpha uint8)
	BlitCover(x, y int, cover []uint8)
}

const (
	fixedMax      = 1 << 29
	statusInitial = iota
	statusLine
	statusClosed
)

type fixRect struct{ x0, y0, x1, y1 int32 }

// Rasterizer turns paths into per pixel coverage. It is reused across paths:
// Reset, add geometry, Sweep.
type Rasterizer struct {
	cells cellBuf

	width, height int
	clip          fixRect
	box           fixRect

	startX, startY int32
	prevX, prevY   int32
	clipX, clipY   int32
	prevFlags      uint32
	status         int

	flatness float32
	cover    []uint8
	solid    solidBlitter
	image    imageBlitter
	shader   shaderBlitter
}

// NewRasterizer returns a rasterizer for a w by h pixel target.
func NewRasterizer(w, h int) *Rasterizer {
	r := &Rasterizer{}
	r.SetSize(w, h)
	return r
}

// SetSize sets the target size and clears any clip box.
func (r *Rasterizer) SetSize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	r.width, r.height = w, h
	r.box = fixRect{0, 0, int32(w) << polyShift, int32(h) << polyShift}
	r.clip = r.box
	if cap(r.cover) < w {
		r.cover = make([]uint8, 0, w)
	}
	r.cover = r.cover[:0]
	r.Reset()
}

// SetClip restricts rasterization to the intersection of the target and box.
// It is not a clip mask: geometry outside is projected onto the boundary,
// which is what a bounding box clip means for coverage inside it.
func (r *Rasterizer) SetClip(box Rect) {
	c := fixRect{
		toFixed(box.X0), toFixed(box.Y0),
		toFixed(box.X1), toFixed(box.Y1),
	}
	if c.x0 > c.x1 {
		c.x0, c.x1 = c.x1, c.x0
	}
	if c.y0 > c.y1 {
		c.y0, c.y1 = c.y1, c.y0
	}
	if c.x0 < r.box.x0 {
		c.x0 = r.box.x0
	}
	if c.y0 < r.box.y0 {
		c.y0 = r.box.y0
	}
	if c.x1 > r.box.x1 {
		c.x1 = r.box.x1
	}
	if c.y1 > r.box.y1 {
		c.y1 = r.box.y1
	}
	if c.x1 < c.x0 {
		c.x1 = c.x0
	}
	if c.y1 < c.y0 {
		c.y1 = c.y0
	}
	r.clip = c
}

// SetFlatness sets how far a flattened curve may stray from the true one, in
// pixels. Zero restores the default.
func (r *Rasterizer) SetFlatness(tol float32) { r.flatness = tol }

// Reset drops the accumulated geometry, keeping the memory.
func (r *Rasterizer) Reset() {
	r.cells.reset()
	r.status = statusInitial
}

// MoveTo begins a subpath at a device space point.
func (r *Rasterizer) MoveTo(x, y float32) { r.moveTo(toFixed(x), toFixed(y)) }

// LineTo adds a segment to a device space point.
func (r *Rasterizer) LineTo(x, y float32) { r.lineTo(toFixed(x), toFixed(y)) }

// Close closes the current subpath. Sweep closes it anyway; an explicit close
// only matters when more geometry follows.
func (r *Rasterizer) Close() {
	if r.status != statusLine {
		return
	}
	r.clipSegment(r.startX, r.startY)
	r.closeNoClip()
}

// AddPath flattens the path under m and adds it.
func (r *Rasterizer) AddPath(p *Path, m Matrix) {
	p.Flatten(m, r.flatness, r)
}

func (r *Rasterizer) moveTo(x, y int32) {
	if r.cells.isSort {
		r.Reset()
	}
	if r.status == statusLine {
		r.Close()
	}
	r.prevX, r.startX = x, x
	r.prevY, r.startY = y, y
	r.status = statusInitial
	r.prevFlags = clipFlags(x, y, r.clip)
	if r.prevFlags == 0 {
		r.moveToNoClip(x, y)
	}
}

func (r *Rasterizer) lineTo(x, y int32) { r.clipSegment(x, y) }

func (r *Rasterizer) moveToNoClip(x, y int32) {
	if r.status == statusLine {
		r.closeNoClip()
	}
	r.cells.moveTo(x, y)
	r.clipX, r.clipY = x, y
	r.status = statusLine
}

func (r *Rasterizer) lineToNoClip(x, y int32) {
	if r.status != statusInitial {
		r.cells.lineTo(x, y)
		r.status = statusLine
	}
}

func (r *Rasterizer) closeNoClip() {
	if r.status == statusLine {
		r.cells.lineTo(r.clipX, r.clipY)
		r.status = statusClosed
	}
}

func (r *Rasterizer) clipSegment(x, y int32) {
	flags := clipFlags(x, y, r.clip)
	if r.prevFlags == flags {
		if flags == 0 {
			if r.status == statusInitial {
				r.moveToNoClip(x, y)
			} else {
				r.lineToNoClip(x, y)
			}
		}
	} else {
		var cx, cy [4]int32
		n := clipLine(r.prevX, r.prevY, x, y, r.clip, &cx, &cy)
		for i := 0; i < n; i++ {
			if r.status == statusInitial {
				r.moveToNoClip(cx[i], cy[i])
			} else {
				r.lineToNoClip(cx[i], cy[i])
			}
		}
	}
	r.prevFlags = flags
	r.prevX, r.prevY = x, y
}

// Bounds returns the pixel rectangle the accumulated geometry touches,
// clamped to the target. It is conservative: an edge landing exactly on a
// pixel boundary counts.
func (r *Rasterizer) Bounds() (x0, y0, x1, y1 int) {
	r.Close()
	r.cells.sort()
	if len(r.cells.cells) == 0 {
		return 0, 0, 0, 0
	}
	x0, y0 = int(r.cells.minX), int(r.cells.minY)
	x1, y1 = int(r.cells.maxX)+1, int(r.cells.maxY)+1
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > r.width {
		x1 = r.width
	}
	if y1 > r.height {
		y1 = r.height
	}
	if x1 < x0 || y1 < y0 {
		return 0, 0, 0, 0
	}
	return
}

// Sweep applies the fill rule and hands the coverage to b, scanline by
// scanline, top to bottom.
func (r *Rasterizer) Sweep(rule FillRule, b Blitter) {
	r.Close()
	r.cells.sort()
	if len(r.cells.cells) == 0 {
		return
	}
	y0, y1 := r.cells.minY, r.cells.maxY
	if y0 < 0 {
		y0 = 0
	}
	if y1 > int32(r.height)-1 {
		y1 = int32(r.height) - 1
	}
	e := emitter{b: b, cover: r.cover[:0], width: r.width}
	for y := y0; y <= y1; y++ {
		e.y = int(y)
		r.sweepLine(r.cells.scanline(y), rule, &e)
		e.flush()
	}
}

func (r *Rasterizer) sweepLine(cells []cell, rule FillRule, e *emitter) {
	cover := int32(0)
	for len(cells) > 0 {
		x := cells[0].x
		area := cells[0].area
		cover += cells[0].cover
		cells = cells[1:]
		for len(cells) > 0 && cells[0].x == x {
			area += cells[0].area
			cover += cells[0].cover
			cells = cells[1:]
		}
		if area != 0 {
			if a := calcAlpha(cover<<(polyShift+1)-area, rule); a != 0 {
				e.cell(int(x), a)
			}
			x++
		}
		if len(cells) > 0 && cells[0].x > x {
			if a := calcAlpha(cover<<(polyShift+1), rule); a != 0 {
				e.span(int(x), int(cells[0].x-x), a)
			}
		}
	}
}

func calcAlpha(area int32, rule FillRule) uint8 {
	cover := area >> (polyShift*2 + 1 - 8)
	if cover < 0 {
		cover = -cover
	}
	if rule == EvenOdd {
		cover &= 2*polySize - 1
		if cover > polySize {
			cover = 2*polySize - cover
		}
	}
	if cover > polySize-1 {
		cover = polySize - 1
	}
	return uint8(cover)
}

type emitter struct {
	b     Blitter
	y     int
	x0    int
	width int
	cover []uint8
}

func (e *emitter) cell(x int, a uint8) {
	if x < 0 || x >= e.width {
		return
	}
	if len(e.cover) > 0 && x != e.x0+len(e.cover) {
		e.flush()
	}
	if len(e.cover) == 0 {
		e.x0 = x
	}
	e.cover = append(e.cover, a)
}

func (e *emitter) span(x, w int, a uint8) {
	if x < 0 {
		w += x
		x = 0
	}
	if x+w > e.width {
		w = e.width - x
	}
	if w <= 0 {
		return
	}
	if w <= 4 {
		for i := 0; i < w; i++ {
			e.cell(x+i, a)
		}
		return
	}
	e.flush()
	e.b.BlitSolid(x, e.y, w, a)
}

func (e *emitter) flush() {
	if len(e.cover) > 0 {
		e.b.BlitCover(e.x0, e.y, e.cover)
		e.cover = e.cover[:0]
	}
}

func toFixed(v float32) int32 {
	const lim = fixedMax / polySize
	switch {
	case v != v:
		return 0
	case v <= -lim:
		return -fixedMax
	case v >= lim:
		return fixedMax
	}
	return int32(v * polySize)
}

func clipFlags(x, y int32, box fixRect) uint32 {
	return b2u(x > box.x1) | b2u(y > box.y1)<<1 | b2u(x < box.x0)<<2 | b2u(y < box.y0)<<3
}

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func clipLine(x1, y1, x2, y2 int32, box fixRect, cx, cy *[4]int32) int {
	const nearZero = 1e-30

	deltax := float32(x2) - float32(x1)
	deltay := float32(y2) - float32(y1)
	np := 0
	if deltax == 0 {
		deltax = nearZero
		if x1 > box.x0 {
			deltax = -nearZero
		}
	}
	var xin, xout float32
	if deltax > 0 {
		xin, xout = float32(box.x0), float32(box.x1)
	} else {
		xin, xout = float32(box.x1), float32(box.x0)
	}
	tinx := (xin - float32(x1)) / deltax
	if deltay == 0 {
		deltay = nearZero
		if y1 > box.y0 {
			deltay = -nearZero
		}
	}
	var yin, yout float32
	if deltay > 0 {
		yin, yout = float32(box.y0), float32(box.y1)
	} else {
		yin, yout = float32(box.y1), float32(box.y0)
	}
	tiny := (yin - float32(y1)) / deltay

	tin1, tin2 := tinx, tiny
	if tiny < tinx {
		tin1, tin2 = tiny, tinx
	}
	if tin1 > 1 {
		return 0
	}
	if tin1 > 0 {
		cx[np], cy[np] = int32(xin), int32(yin)
		np++
	}
	if tin2 > 1 {
		return np
	}
	toutx := (xout - float32(x1)) / deltax
	touty := (yout - float32(y1)) / deltay
	tout1 := toutx
	if touty < toutx {
		tout1 = touty
	}
	if tin2 <= 0 && tout1 <= 0 {
		return np
	}
	if tin2 > tout1 {
		if tinx > tiny {
			cx[np], cy[np] = int32(xin), int32(yout)
		} else {
			cx[np], cy[np] = int32(xout), int32(yin)
		}
		np++
		return np
	}
	if tin2 > 0 {
		if tinx > tiny {
			cx[np], cy[np] = int32(xin), int32(float32(y1)+float32(deltay*tinx))
		} else {
			cx[np], cy[np] = int32(float32(x1)+float32(deltax*tiny)), int32(yin)
		}
		np++
	}
	if tout1 < 1 {
		if toutx < touty {
			cx[np], cy[np] = int32(xout), int32(float32(y1)+float32(deltay*toutx))
		} else {
			cx[np], cy[np] = int32(float32(x1)+float32(deltax*touty)), int32(yout)
		}
	} else {
		cx[np], cy[np] = x2, y2
	}
	np++
	return np
}
