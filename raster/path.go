package raster

import (
	"math"
	"slices"
)

// Path is a sequence of subpaths built from lines and cubic curves.
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

// Len returns the number of segments, a move and a close included.
func (p *Path) Len() int { return len(p.cmds) }

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

// LineTo adds a straight segment, with an implicit move on an empty path. A line
// to the current point is dropped unless it follows a move.
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

// CurveTo adds a cubic Bezier segment; a degenerate one becomes a line or nothing.
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

// Close closes the current subpath; it does nothing when none is open.
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

// Bounds returns the bounding box of the path and its control points under m.
func (p *Path) Bounds(m Matrix) Rect {
	out := EmptyRect
	for _, pt := range p.pts {
		out = out.AddPoint(m.Apply(pt))
	}
	return out
}

// StrokeBounds returns the bounding box of the stroked path, padded for width,
// miter and square caps.
func (p *Path) StrokeBounds(s *Stroke, m Matrix) Rect {
	r := p.Bounds(m)
	if r.IsEmpty() {
		return r
	}
	w := s.Reach(m)
	return Rect{r.X0 - w, r.Y0 - w, r.X1 + w, r.Y1 + w}
}

// Reach returns the farthest the outline of a stroke lies from the path
// under m, in device units.
func (s *Stroke) Reach(m Matrix) float32 {
	w := s.Width
	if e := m.Expansion(); !(w*e >= 1) && e > 0 {
		w = 1 / e
	}
	f := float32(innerMiterLim)
	if s.StartCap == CapSquare || s.EndCap == CapSquare || s.DashCap == CapSquare {
		f = math.Sqrt2
	}
	if s.Join == JoinMiter && s.MiterLimit > f {
		f = s.MiterLimit
	}
	return max(w, 0) * m.MaxExpansion() / 2 * f
}

// YBounds returns the least and the greatest y of the path's points under m.
func (p *Path) YBounds(m Matrix) (y0, y1 float32) {
	if len(p.pts) == 0 {
		return 0, 0
	}
	y0 = float32(m.B*p.pts[0].X) + float32(m.D*p.pts[0].Y)
	y1 = y0
	for _, q := range p.pts[1:] {
		y := float32(m.B*q.X) + float32(m.D*q.Y)
		y0, y1 = min(y0, y), max(y1, y)
	}
	return y0 + m.F, y1 + m.F
}

// segment is one line or curve of a path and the point it starts from.
type segment struct {
	cmd  byte
	n    int
	from Point
	pts  [3]Point
}

func (g *segment) end() Point { return g.pts[g.n-1] }

// side is -1 when every point of g lies at or above y0 under m, 1 when every
// one lies at or below y1, and 0 otherwise.
func (g *segment) side(m Matrix, y0, y1 float32) int {
	return sideOf(m, g.from, g.pts[:g.n], y0, y1)
}

func sideOf(m Matrix, from Point, pts []Point, y0, y1 float32) int {
	lo := m.Apply(from).Y
	hi := lo
	for _, q := range pts {
		y := m.Apply(q).Y
		lo, hi = min(lo, y), max(hi, y)
	}
	switch {
	case hi <= y0:
		return -1
	case lo >= y1:
		return 1
	}
	return 0
}

// segments calls fn for each subpath: where it starts, its segments, and
// whether it is closed, the closing line among them when it has length.
func (p *Path) segments(fn func(start Point, segs []segment, closed bool)) {
	var segs []segment
	var start, at Point
	open := false
	i := 0
	for _, c := range p.cmds {
		switch c {
		case cmdMove:
			if open {
				fn(start, segs, false)
			}
			segs, open = segs[:0], true
			start, at = p.pts[i], p.pts[i]
			i++
		case cmdLine:
			open = true
			segs = append(segs, segment{cmd: cmdLine, n: 1, from: at, pts: [3]Point{p.pts[i]}})
			at = p.pts[i]
			i++
		case cmdCurve:
			open = true
			segs = append(segs, segment{cmd: cmdCurve, n: 3, from: at, pts: [3]Point{p.pts[i], p.pts[i+1], p.pts[i+2]}})
			at = p.pts[i+2]
			i += 3
		case cmdClose:
			if at != start {
				segs = append(segs, segment{cmd: cmdLine, n: 1, from: at, pts: [3]Point{start}})
			}
			fn(start, segs, true)
			segs, open = segs[:0], false
			at = start
		}
	}
	if open {
		fn(start, segs, false)
	}
}

func (p *Path) appendMove(at Point) {
	p.cmds = append(p.cmds, cmdMove)
	p.pts = append(p.pts, at)
	p.cur, p.start, p.started = at, at, true
}

func (p *Path) appendSeg(g *segment) {
	p.cmds = append(p.cmds, g.cmd)
	p.pts = append(p.pts, g.pts[:g.n]...)
	p.cur = g.end()
}

// CullY writes into dst the path as it fills the rows from y0 to y1 under m:
// each run of segments wholly above y0, or wholly below y1, becomes one line
// between its ends, which covers nothing in those rows either.
func (p *Path) CullY(dst *Path, m Matrix, y0, y1 float32) *Path {
	dst.Reset()
	if !(y0 < y1) {
		dst.Append(p)
		return dst
	}
	run, i := 0, 0
	var at, end Point
	flush := func() {
		if run != 0 {
			dst.cmds = append(dst.cmds, cmdLine)
			dst.pts = append(dst.pts, end)
			dst.cur = end
			run = 0
		}
	}
	for _, c := range p.cmds {
		n := 0
		switch c {
		case cmdMove:
			flush()
			at = p.pts[i]
			dst.appendMove(at)
			i++
			continue
		case cmdClose:
			flush()
			dst.cmds = append(dst.cmds, cmdClose)
			at = dst.start
			dst.cur = at
			continue
		case cmdLine:
			n = 1
		case cmdCurve:
			n = 3
		}
		pts := p.pts[i : i+n]
		sd := sideOf(m, at, pts, y0, y1)
		at = pts[n-1]
		i += n
		if sd != 0 {
			if run != sd {
				flush()
			}
			run, end = sd, at
			continue
		}
		flush()
		dst.cmds = append(dst.cmds, c)
		dst.pts = append(dst.pts, pts...)
		dst.cur = at
	}
	flush()
	return dst
}

// CullStrokeY writes into dst the path as a stroke reaching reach from it
// draws the rows from y0 to y1 under m: a segment wholly beyond reach of
// those rows is cut out unless it meets one that is not, and a closed subpath
// that loses one opens there. The stroke must be undashed.
func (p *Path) CullStrokeY(dst *Path, m Matrix, y0, y1, reach float32) *Path {
	dst.Reset()
	if !(y0 < y1) {
		dst.Append(p)
		return dst
	}
	y0, y1 = y0-reach, y1+reach
	var in, near []bool
	p.segments(func(start Point, segs []segment, closed bool) {
		n := len(segs)
		if n == 0 {
			if y := m.Apply(start).Y; y > y0 && y < y1 {
				dst.appendMove(start)
				if closed {
					dst.cmds = append(dst.cmds, cmdClose)
				}
			}
			return
		}
		in, near = in[:0], near[:0]
		for k := range segs {
			in = append(in, segs[k].side(m, y0, y1) == 0)
			near = append(near, false)
		}
		for k := range n {
			if !in[k] {
				continue
			}
			near[k] = true
			if k > 0 || closed {
				near[(k+n-1)%n] = true
			}
			if k < n-1 || closed {
				near[(k+1)%n] = true
			}
		}
		cut := slices.Index(near, false)
		if cut < 0 {
			dst.appendMove(start)
			for k := range segs {
				dst.appendSeg(&segs[k])
			}
			if closed {
				dst.cmds = append(dst.cmds, cmdClose)
				dst.cur = start
			}
			return
		}
		first := 0
		if closed {
			first = cut + 1
		}
		open := false
		for j := range n {
			k := (first + j) % n
			if !near[k] {
				open = false
				continue
			}
			if !open {
				dst.appendMove(segs[k].from)
				open = true
			}
			dst.appendSeg(&segs[k])
		}
	})
	return dst
}

// AsRect reports whether the path is one subpath that maps to an axis aligned
// rectangle under m, and returns it.
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
