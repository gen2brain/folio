package raster

import "math"

const (
	polyShift = 8
	polySize  = 1 << polyShift
	polyMask  = polySize - 1
	maxCells  = 1 << 22
)

type cell struct {
	x, y        int32
	cover, area int32
}

type cellBuf struct {
	cells  []cell
	sorted []cell
	starts []int32
	cursor []int32

	cur    cell
	x, y   int32
	minX   int32
	minY   int32
	maxX   int32
	maxY   int32
	isSort bool
	full   bool
}

func (c *cellBuf) reset() {
	c.cells = c.cells[:0]
	c.cur = cell{x: math.MaxInt32, y: math.MaxInt32}
	c.minX, c.minY = math.MaxInt32, math.MaxInt32
	c.maxX, c.maxY = math.MinInt32, math.MinInt32
	c.isSort = false
	c.full = false
}

func (c *cellBuf) addCur() {
	if c.cur.area|c.cur.cover == 0 {
		return
	}
	if len(c.cells) >= maxCells {
		c.full = true
		return
	}
	c.cells = append(c.cells, c.cur)
}

func (c *cellBuf) setCur(x, y int32) {
	if c.cur.x == x && c.cur.y == y {
		return
	}
	c.addCur()
	c.cur = cell{x: x, y: y}
	if x < c.minX {
		c.minX = x
	}
	if x > c.maxX {
		c.maxX = x
	}
	if y < c.minY {
		c.minY = y
	}
	if y > c.maxY {
		c.maxY = y
	}
}

func (c *cellBuf) moveTo(x, y int32) {
	if c.isSort {
		c.reset()
	}
	c.setCur(x>>polyShift, y>>polyShift)
	c.x, c.y = x, y
}

func (c *cellBuf) lineTo(x, y int32) {
	c.renderLine(c.x, c.y, x, y)
	c.x, c.y = x, y
	c.isSort = false
}

func (c *cellBuf) renderHLine(ey, x1, y1, x2, y2 int32) {
	ex1 := x1 >> polyShift
	ex2 := x2 >> polyShift
	fx1 := x1 & polyMask
	fx2 := x2 & polyMask

	if y1 == y2 {
		c.setCur(ex2, ey)
		return
	}
	if ex1 == ex2 {
		delta := y2 - y1
		c.cur.cover += delta
		c.cur.area += (fx1 + fx2) * delta
		return
	}

	p := (polySize - fx1) * (y2 - y1)
	first := int32(polySize)
	incr := int32(1)
	dx := x2 - x1
	if dx < 0 {
		p = fx1 * (y2 - y1)
		first = 0
		incr = -1
		dx = -dx
	}
	delta := p / dx
	mod := p % dx
	if mod < 0 {
		delta--
		mod += dx
	}
	c.cur.cover += delta
	c.cur.area += (fx1 + first) * delta
	ex1 += incr
	c.setCur(ex1, ey)
	y1 += delta

	if ex1 != ex2 {
		p = polySize * (y2 - y1 + delta)
		lift := p / dx
		rem := p % dx
		if rem < 0 {
			lift--
			rem += dx
		}
		mod -= dx
		for ex1 != ex2 {
			delta = lift
			mod += rem
			if mod >= 0 {
				mod -= dx
				delta++
			}
			c.cur.cover += delta
			c.cur.area += polySize * delta
			y1 += delta
			ex1 += incr
			c.setCur(ex1, ey)
		}
	}
	delta = y2 - y1
	c.cur.cover += delta
	c.cur.area += (fx2 + polySize - first) * delta
}

func (c *cellBuf) renderLine(x1, y1, x2, y2 int32) {
	const dxLimit = 16384 << polyShift

	dx := x2 - x1
	if dx >= dxLimit || dx <= -dxLimit {
		cx := (x1 + x2) >> 1
		cy := (y1 + y2) >> 1
		c.renderLine(x1, y1, cx, cy)
		c.renderLine(cx, cy, x2, y2)
		return
	}

	dy := y2 - y1
	ey1 := y1 >> polyShift
	ey2 := y2 >> polyShift
	fy1 := y1 & polyMask
	fy2 := y2 & polyMask

	if ey1 == ey2 {
		c.renderHLine(ey1, x1, fy1, x2, fy2)
		return
	}

	incr := int32(1)
	if dx == 0 {
		ex := x1 >> polyShift
		twoFx := (x1 - (ex << polyShift)) << 1
		first := int32(polySize)
		if dy < 0 {
			first = 0
			incr = -1
		}
		delta := first - fy1
		c.cur.cover += delta
		c.cur.area += twoFx * delta
		ey1 += incr
		c.setCur(ex, ey1)
		delta = first + first - polySize
		area := twoFx * delta
		for ey1 != ey2 {
			c.cur.cover = delta
			c.cur.area = area
			ey1 += incr
			c.setCur(ex, ey1)
		}
		delta = fy2 - polySize + first
		c.cur.cover += delta
		c.cur.area += twoFx * delta
		return
	}

	p := (polySize - fy1) * dx
	first := int32(polySize)
	if dy < 0 {
		p = fy1 * dx
		first = 0
		incr = -1
		dy = -dy
	}
	delta := p / dy
	mod := p % dy
	if mod < 0 {
		delta--
		mod += dy
	}
	xFrom := x1 + delta
	c.renderHLine(ey1, x1, fy1, xFrom, first)
	ey1 += incr
	c.setCur(xFrom>>polyShift, ey1)

	if ey1 != ey2 {
		p = polySize * dx
		lift := p / dy
		rem := p % dy
		if rem < 0 {
			lift--
			rem += dy
		}
		mod -= dy
		for ey1 != ey2 {
			delta = lift
			mod += rem
			if mod >= 0 {
				mod -= dy
				delta++
			}
			xTo := xFrom + delta
			c.renderHLine(ey1, xFrom, polySize-first, xTo, first)
			xFrom = xTo
			ey1 += incr
			c.setCur(xFrom>>polyShift, ey1)
		}
	}
	c.renderHLine(ey1, xFrom, polySize-first, x2, fy2)
}

func (c *cellBuf) sort() {
	if c.isSort {
		return
	}
	c.addCur()
	c.cur = cell{x: math.MaxInt32, y: math.MaxInt32}
	c.isSort = true
	if len(c.cells) == 0 {
		return
	}

	h := int(c.maxY-c.minY) + 1
	c.starts = grow32(c.starts, h+1)
	for i := range c.starts {
		c.starts[i] = 0
	}
	for i := range c.cells {
		c.starts[c.cells[i].y-c.minY+1]++
	}
	for i := 1; i <= h; i++ {
		c.starts[i] += c.starts[i-1]
	}
	c.cursor = grow32(c.cursor, h)
	copy(c.cursor, c.starts[:h])

	c.sorted = growCells(c.sorted, len(c.cells))
	for _, cl := range c.cells {
		i := cl.y - c.minY
		c.sorted[c.cursor[i]] = cl
		c.cursor[i]++
	}
	for i := 0; i < h; i++ {
		sortByX(c.sorted[c.starts[i]:c.starts[i+1]])
	}
}

func (c *cellBuf) scanline(y int32) []cell {
	i := y - c.minY
	return c.sorted[c.starts[i]:c.starts[i+1]]
}

func sortByX(cs []cell) {
	for len(cs) > 12 {
		m := len(cs) / 2
		if cs[m].x < cs[0].x {
			cs[m], cs[0] = cs[0], cs[m]
		}
		if cs[len(cs)-1].x < cs[m].x {
			cs[len(cs)-1], cs[m] = cs[m], cs[len(cs)-1]
			if cs[m].x < cs[0].x {
				cs[m], cs[0] = cs[0], cs[m]
			}
		}
		pivot := cs[m].x
		i, j := 0, len(cs)-1
		for {
			for cs[i].x < pivot {
				i++
			}
			for cs[j].x > pivot {
				j--
			}
			if i >= j {
				break
			}
			cs[i], cs[j] = cs[j], cs[i]
			i++
			j--
		}
		if j+1 < len(cs)-j-1 {
			sortByX(cs[:j+1])
			cs = cs[j+1:]
		} else {
			sortByX(cs[j+1:])
			cs = cs[:j+1]
		}
	}
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].x < cs[j-1].x; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

func grow32(s []int32, n int) []int32 {
	if cap(s) < n {
		return make([]int32, n, n+n/2)
	}
	return s[:n]
}

func growCells(s []cell, n int) []cell {
	if cap(s) < n {
		return make([]cell, n, n+n/2)
	}
	return s[:n]
}
