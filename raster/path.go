package raster

// Path is a sequence of subpaths built from lines and cubic curves. The
// commands and the points are kept in two slices, which is compact and lets a
// path be reused across fills without reallocating.
type Path struct {
	cmds []byte
	pts  []Point

	start   Point // where the current subpath began
	cur     Point
	started bool
}

// Path command bytes.
const (
	cmdMove  = 'm'
	cmdLine  = 'l'
	cmdCurve = 'c'
	cmdClose = 'h'
)

// Reset empties the path, keeping the memory for the next one.
func (p *Path) Reset() {
	p.cmds = p.cmds[:0]
	p.pts = p.pts[:0]
	p.started = false
	p.cur, p.start = Point{}, Point{}
}

// IsEmpty reports whether nothing has been added.
func (p *Path) IsEmpty() bool { return len(p.cmds) == 0 }

// Current returns the current point.
func (p *Path) Current() Point { return p.cur }

// Start returns the first point of the current subpath.
func (p *Path) Start() Point { return p.start }

// MoveTo begins a new subpath.
func (p *Path) MoveTo(x, y float32) {
	p.cmds = append(p.cmds, cmdMove)
	p.cur = Point{x, y}
	p.start = p.cur
	p.started = true
	p.pts = append(p.pts, p.cur)
}

// LineTo adds a straight segment. A path that begins with a line has an
// implicit move to its first point. A line to the point the path is already
// at is dropped unless a move put it there, where it is a subpath of its own
// that a round or a square cap paints as a dot.
func (p *Path) LineTo(x, y float32) {
	if !p.started {
		p.MoveTo(x, y)
		return
	}
	if p.cur.X == x && p.cur.Y == y &&
		len(p.cmds) > 0 && p.cmds[len(p.cmds)-1] != cmdMove {
		return
	}
	p.cmds = append(p.cmds, cmdLine)
	p.cur = Point{x, y}
	p.pts = append(p.pts, p.cur)
}

// CurveTo adds a cubic Bezier segment. A cubic whose control points sit on
// its ends is a straight line and is added as one; one that collapses onto a
// single point is nothing at all.
func (p *Path) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	if !p.started {
		p.MoveTo(x1, y1)
	}
	at1 := p.cur.X == x1 && p.cur.Y == y1
	at2 := x1 == x2 && y1 == y2
	at3 := x2 == x3 && y2 == y3
	if at1 && at3 || at1 && at2 || at2 && at3 {
		p.LineTo(x3, y3)
		return
	}
	p.cmds = append(p.cmds, cmdCurve)
	p.pts = append(p.pts, Point{x1, y1}, Point{x2, y2}, Point{x3, y3})
	p.cur = Point{x3, y3}
}

// CurveToV is the PDF v operator, whose first control point is the current one.
func (p *Path) CurveToV(x2, y2, x3, y3 float32) {
	c := p.cur
	p.CurveTo(c.X, c.Y, x2, y2, x3, y3)
}

// CurveToY is the PDF y operator, whose second control point is its end point.
func (p *Path) CurveToY(x1, y1, x3, y3 float32) {
	p.CurveTo(x1, y1, x3, y3, x3, y3)
}

// Close closes the current subpath. Closing an already closed subpath, or one
// that has not begun, does nothing.
func (p *Path) Close() {
	if !p.started || len(p.cmds) == 0 || p.cmds[len(p.cmds)-1] == cmdClose {
		return
	}
	p.cmds = append(p.cmds, cmdClose)
	p.cur = p.start
}

// Rect adds a closed rectangular subpath, the PDF re operator.
func (p *Path) Rect(x, y, w, h float32) {
	p.MoveTo(x, y)
	p.LineTo(x+w, y)
	p.LineTo(x+w, y+h)
	p.LineTo(x, y+h)
	p.Close()
	p.cur = Point{x, y}
	p.start = p.cur
}

// Walker receives the segments of a path in order.
type Walker interface {
	MoveTo(x, y float32)
	LineTo(x, y float32)
	CurveTo(x1, y1, x2, y2, x3, y3 float32)
	Close()
}

// Walk replays the path into w.
func (p *Path) Walk(w Walker) {
	i := 0
	for _, c := range p.cmds {
		switch c {
		case cmdMove:
			w.MoveTo(p.pts[i].X, p.pts[i].Y)
			i++
		case cmdLine:
			w.LineTo(p.pts[i].X, p.pts[i].Y)
			i++
		case cmdCurve:
			w.CurveTo(p.pts[i].X, p.pts[i].Y, p.pts[i+1].X, p.pts[i+1].Y, p.pts[i+2].X, p.pts[i+2].Y)
			i += 3
		case cmdClose:
			w.Close()
		}
	}
}

// Transform returns a copy of the path with every point transformed.
func (p *Path) Transform(m Matrix) *Path {
	out := &Path{
		cmds: append([]byte(nil), p.cmds...),
		pts:  make([]Point, len(p.pts)),
	}
	for i, pt := range p.pts {
		out.pts[i] = m.Apply(pt)
	}
	out.cur = m.Apply(p.cur)
	out.start = m.Apply(p.start)
	out.started = p.started
	return out
}

// Clone returns a copy that shares nothing with the original.
func (p *Path) Clone() *Path {
	return &Path{
		cmds:    append([]byte(nil), p.cmds...),
		pts:     append([]Point(nil), p.pts...),
		cur:     p.cur,
		start:   p.start,
		started: p.started,
	}
}

// Append adds the segments of another path.
func (p *Path) Append(q *Path) {
	if q == nil || len(q.cmds) == 0 {
		return
	}
	p.cmds = append(p.cmds, q.cmds...)
	p.pts = append(p.pts, q.pts...)
	p.cur, p.start, p.started = q.cur, q.start, q.started
}

// Bounds returns the bounding box of the path under m. Control points are
// included rather than the curve solved: the result is conservative, which is
// what every caller of a bounding box wants.
func (p *Path) Bounds(m Matrix) Rect {
	out := EmptyRect
	for _, pt := range p.pts {
		out = out.AddPoint(m.Apply(pt))
	}
	return out
}

// StrokeBounds returns the bounding box of the path when stroked, padded by
// the line width and enough for a miter join.
func (p *Path) StrokeBounds(s *Stroke, m Matrix) Rect {
	r := p.Bounds(m)
	if r.IsEmpty() {
		return r
	}
	w := s.Width * m.MaxExpansion() / 2
	if w < 0 {
		w = 0
	}
	if s.Join == JoinMiter && s.MiterLimit > 1 {
		w *= s.MiterLimit
	}
	return Rect{r.X0 - w, r.Y0 - w, r.X1 + w, r.Y1 + w}
}

// AsRect reports whether the path is one subpath that maps to an axis aligned
// rectangle under m, and returns it. Clipping to a rectangle is exact and
// costs nothing, so the caller wants to know.
func (p *Path) AsRect(m Matrix) (Rect, bool) {
	n := len(p.cmds)
	if n > 0 && p.cmds[n-1] == cmdClose {
		n--
	}
	if n < 4 || n > 5 || p.cmds[0] != cmdMove {
		return EmptyRect, false
	}
	for _, c := range p.cmds[1:n] {
		if c != cmdLine {
			return EmptyRect, false
		}
	}
	var q [5]Point
	for i := 0; i < n; i++ {
		q[i] = m.Apply(p.pts[i])
	}
	if n == 5 && (q[4] != q[0]) {
		return EmptyRect, false
	}
	ok := q[0].X == q[1].X && q[1].Y == q[2].Y && q[2].X == q[3].X && q[3].Y == q[0].Y ||
		q[0].Y == q[1].Y && q[1].X == q[2].X && q[2].Y == q[3].Y && q[3].X == q[0].X
	if !ok {
		return EmptyRect, false
	}
	return Rect{q[0].X, q[0].Y, q[2].X, q[2].Y}.Normalized(), true
}
