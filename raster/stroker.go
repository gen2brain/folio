package raster

import "math"

const (
	strokeTheta     = 1.0 / 1000
	innerMiterLim   = 1.01
	vertexEpsilon   = 1e-14
	intersectionEps = 1e-30
	arcTolerance    = 0.125
)

// Outline returns the path whose non-zero fill is the stroke of p. The path is
// stroked in its own space, so the caller transforms the result; scale is how
// much that transform magnifies lengths, and sets the flattening tolerance,
// the number of segments in an arc, and the width of a hairline.
func (s *Stroke) Outline(p *Path, scale float32) *Path {
	return s.OutlineInto(&Path{}, p, scale)
}

// OutlineInto is Outline writing into dst, which it empties first, so that a
// caller stroking many paths can keep the memory.
func (s *Stroke) OutlineInto(dst *Path, p *Path, scale float32) *Path {
	if scale <= 0 || scale != scale {
		scale = 1
	}
	w := s.Width
	if !(w*scale >= 1) {
		w = 1 / scale
	}
	tol := DefaultFlatness / scale

	dst.Reset()
	st := &stroker{
		out:        dst,
		width:      w / 2,
		miterLimit: s.MiterLimit,
		join:       s.Join,
		arcTol:     arcTolerance / scale,
		eps:        vertexEpsilon,
	}
	if st.miterLimit < 1 {
		st.miterLimit = 1
	}

	var c collector
	p.Flatten(Identity, tol, &c)
	c.flush()

	d := newDasher(s, scale)
	var v []vertexDist
	for _, sub := range c.subs {
		v = vertices(v[:0], c.pts[sub.start:sub.end], sub.closed, st.eps)
		if len(v) == 0 {
			continue
		}
		if d == nil {
			st.startCap, st.endCap = s.StartCap, s.EndCap
			st.polyline(v, sub.closed)
			continue
		}
		st.startCap, st.endCap = s.DashCap, s.DashCap
		d.reset()
		d.run(v, sub.closed, func(pts []Point, whole bool) {
			if whole {
				st.startCap, st.endCap = s.StartCap, s.EndCap
				st.polyline(vertices(st.run[:0], pts, true, st.eps), true)
				return
			}
			st.run = vertices(st.run[:0], pts, false, st.eps)
			st.polyline(st.run, false)
		})
	}
	return st.out
}

// subpath is a range of the collector's points, which are all in one slice so
// that a path with many subpaths does not allocate per subpath.
type subpath struct {
	start, end int
	closed     bool
}

type collector struct {
	pts   []Point
	subs  []subpath
	start int
	open  bool
	last  Point
}

func (c *collector) MoveTo(x, y float32) {
	c.flush()
	c.start = len(c.pts)
	c.pts = append(c.pts, Point{x, y})
	c.open = true
	c.last = Point{x, y}
}

func (c *collector) LineTo(x, y float32) {
	if !c.open {
		c.start = len(c.pts)
		c.pts = append(c.pts, c.last)
		c.open = true
	}
	c.pts = append(c.pts, Point{x, y})
}

func (c *collector) Close() {
	if !c.open {
		return
	}
	c.subs = append(c.subs, subpath{c.start, len(c.pts), true})
	c.last = c.pts[c.start]
	c.open = false
}

func (c *collector) flush() {
	if c.open {
		c.subs = append(c.subs, subpath{c.start, len(c.pts), false})
		c.open = false
	}
}

type vertexDist struct{ x, y, dist float32 }

func vertices(v []vertexDist, pts []Point, closed bool, eps float32) []vertexDist {
	for _, p := range pts {
		if n := len(v); n > 0 {
			d := dist(v[n-1].x, v[n-1].y, p.X, p.Y)
			if d <= eps {
				continue
			}
			v[n-1].dist = d
		}
		v = append(v, vertexDist{x: p.X, y: p.Y})
	}
	if closed {
		for len(v) > 1 {
			d := dist(v[len(v)-1].x, v[len(v)-1].y, v[0].x, v[0].y)
			if d > eps {
				v[len(v)-1].dist = d
				break
			}
			v = v[:len(v)-1]
		}
	}
	return v
}

type stroker struct {
	out        *Path
	width      float32
	miterLimit float32
	arcTol     float32
	eps        float32
	join       Join
	startCap   Cap
	endCap     Cap
	tmp        []Point
	run        []vertexDist
	first      bool
}

func (s *stroker) add(x, y float32) { s.tmp = append(s.tmp, Point{x, y}) }

func (s *stroker) emit() {
	for _, p := range s.tmp {
		if s.first {
			s.out.MoveTo(p.X, p.Y)
			s.first = false
		} else {
			s.out.LineTo(p.X, p.Y)
		}
	}
}

func (s *stroker) polyline(v []vertexDist, closed bool) {
	n := len(v)
	if n == 0 {
		return
	}
	if closed && n >= 3 {
		s.first = true
		for i := 0; i < n; i++ {
			p, q := (i+n-1)%n, (i+1)%n
			s.join3(v[p], v[i], v[q], v[p].dist, v[i].dist)
			s.emit()
		}
		s.out.Close()
		s.first = true
		for i := n - 1; i >= 0; i-- {
			p, q := (i+n-1)%n, (i+1)%n
			s.join3(v[q], v[i], v[p], v[i].dist, v[p].dist)
			s.emit()
		}
		s.out.Close()
		return
	}
	if n >= 2 {
		s.first = true
		s.cap2(v[0], v[1], v[0].dist, s.startCap)
		s.emit()
		for i := 1; i <= n-2; i++ {
			s.join3(v[i-1], v[i], v[i+1], v[i-1].dist, v[i].dist)
			s.emit()
		}
		s.cap2(v[n-1], v[n-2], v[n-2].dist, s.endCap)
		s.emit()
		for i := n - 2; i >= 1; i-- {
			s.join3(v[i+1], v[i], v[i-1], v[i].dist, v[i-1].dist)
			s.emit()
		}
		s.out.Close()
		return
	}
	s.dot(v[0], closed)
}

func (s *stroker) dot(v vertexDist, closed bool) {
	w := s.width
	switch {
	case s.startCap == CapRound:
		da := s.arcStep()
		s.first = true
		for a := float32(0); a < 2*math.Pi; a += da {
			sin, cos := math.Sincos(float64(a))
			x := float32(float64(v.x) + float64(float64(w)*cos))
			y := float32(float64(v.y) + float64(float64(w)*sin))
			if s.first {
				s.out.MoveTo(x, y)
				s.first = false
			} else {
				s.out.LineTo(x, y)
			}
		}
		s.out.Close()
	case closed || s.startCap != CapSquare:
	default:
		s.out.MoveTo(v.x-w, v.y-w)
		s.out.LineTo(v.x+w, v.y-w)
		s.out.LineTo(v.x+w, v.y+w)
		s.out.LineTo(v.x-w, v.y+w)
		s.out.Close()
	}
}

func (s *stroker) arcStep() float32 {
	w := s.width
	if w <= 0 {
		return strokeTheta
	}
	da := float32(math.Acos(float64(w/(w+s.arcTol))) * 2)
	if !(da > strokeTheta) {
		da = strokeTheta
	}
	return da
}

func (s *stroker) arc(x, y, dx1, dy1, dx2, dy2 float32) {
	a1 := float32(math.Atan2(float64(dy1), float64(dx1)))
	a2 := float32(math.Atan2(float64(dy2), float64(dx2)))
	da := a1 - a2
	ccw := da > 0 && da < math.Pi
	w := s.width
	da = s.arcStep()

	s.add(x+dx1, y+dy1)
	if !ccw {
		if a1 > a2 {
			a2 += 2 * math.Pi
		}
		a2 -= da / 4
		for a1 += da; a1 < a2; a1 += da {
			sin, cos := math.Sincos(float64(a1))
			s.add(float32(float64(x)+float64(float64(w)*cos)), float32(float64(y)+float64(float64(w)*sin)))
		}
	} else {
		if a1 < a2 {
			a2 -= 2 * math.Pi
		}
		a2 += da / 4
		for a1 -= da; a1 > a2; a1 -= da {
			sin, cos := math.Sincos(float64(a1))
			s.add(float32(float64(x)+float64(float64(w)*cos)), float32(float64(y)+float64(float64(w)*sin)))
		}
	}
	s.add(x+dx2, y+dy2)
}

func (s *stroker) miter(v0, v1, v2 vertexDist, dx1, dy1, dx2, dy2, limit float32) {
	xi, yi, ok := intersection(
		v0.x+dx1, v0.y-dy1,
		v1.x+dx1, v1.y-dy1,
		v1.x+dx2, v1.y-dy2,
		v2.x+dx2, v2.y-dy2)
	if ok {
		if dist(v1.x, v1.y, xi, yi) <= s.width*limit {
			s.add(xi, yi)
			return
		}
	} else {
		x2 := v1.x + dx1
		y2 := v1.y - dy1
		if float32((x2-v0.x)*dy1)-float32((v0.y-y2)*dx1) < 0 !=
			(float32((x2-v2.x)*dy1)-float32((v2.y-y2)*dx1) < 0) {
			s.add(v1.x+dx1, v1.y-dy1)
			return
		}
	}
	s.add(v1.x+dx1, v1.y-dy1)
	s.add(v1.x+dx2, v1.y-dy2)
}

func (s *stroker) cap2(v0, v1 vertexDist, length float32, c Cap) {
	s.tmp = s.tmp[:0]
	if length <= 0 {
		return
	}
	w := s.width
	dx1 := (v1.y - v0.y) / length * w
	dy1 := (v1.x - v0.x) / length * w
	if c == CapTriangle {
		s.add(v0.x-dx1, v0.y+dy1)
		s.add(v0.x-dy1, v0.y-dx1)
		s.add(v0.x+dx1, v0.y-dy1)
		return
	}
	if c != CapRound {
		var dx2, dy2 float32
		if c == CapSquare {
			dx2, dy2 = dy1, dx1
		}
		s.add(v0.x-dx1-dx2, v0.y+dy1-dy2)
		s.add(v0.x+dx1-dx2, v0.y-dy1-dy2)
		return
	}
	a1 := float32(math.Atan2(float64(dy1), float64(-dx1)))
	a2 := a1 + math.Pi
	da := s.arcStep()
	s.add(v0.x-dx1, v0.y+dy1)
	a2 -= da / 4
	for a1 += da; a1 < a2; a1 += da {
		sin, cos := math.Sincos(float64(a1))
		s.add(float32(float64(v0.x)+float64(float64(w)*cos)), float32(float64(v0.y)+float64(float64(w)*sin)))
	}
	s.add(v0.x+dx1, v0.y-dy1)
}

func (s *stroker) join3(v0, v1, v2 vertexDist, len1, len2 float32) {
	s.tmp = s.tmp[:0]
	if len1 <= 0 || len2 <= 0 {
		return
	}
	w := s.width
	dx1 := w * (v1.y - v0.y) / len1
	dy1 := w * (v1.x - v0.x) / len1
	dx2 := w * (v2.y - v1.y) / len2
	dy2 := w * (v2.x - v1.x) / len2

	if pointLocation(v0.x, v0.y, v1.x, v1.y, v2.x, v2.y) > 0 {
		s.miter(v0, v1, v2, dx1, dy1, dx2, dy2, innerMiterLim)
		return
	}
	switch s.join {
	case JoinMiter:
		s.miter(v0, v1, v2, dx1, dy1, dx2, dy2, s.miterLimit)
	case JoinRound:
		s.arc(v1.x, v1.y, dx1, -dy1, dx2, -dy2)
	default:
		s.add(v1.x+dx1, v1.y-dy1)
		s.add(v1.x+dx2, v1.y-dy2)
	}
}

func pointLocation(x1, y1, x2, y2, x, y float32) float32 {
	return float32((x-x2)*(y2-y1)) - float32((y-y2)*(x2-x1))
}

func dist(x1, y1, x2, y2 float32) float32 {
	dx, dy := x2-x1, y2-y1
	return float32(math.Sqrt(float64(float32(dx*dx) + float32(dy*dy))))
}

func intersection(ax, ay, bx, by, cx, cy, dx, dy float32) (x, y float32, ok bool) {
	num := float32((ay-cy)*(dx-cx)) - float32((ax-cx)*(dy-cy))
	den := float32((bx-ax)*(dy-cy)) - float32((by-ay)*(dx-cx))
	if absf(den) < intersectionEps {
		return 0, 0, false
	}
	return ax + (bx-ax)*num/den, ay + (by-ay)*num/den, true
}
