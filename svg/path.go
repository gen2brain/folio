package svg

import (
	"math"

	"github.com/gen2brain/folio/raster"
)

// pathData reads the d attribute, SVG 1.1 8.3. A command may be followed by
// several sets of operands, an omitted command repeats the last one, and the
// one after a moveto is a lineto.
func pathData(d string) *raster.Path {
	p := &raster.Path{}
	s := &scanner{s: d}
	var cur, start raster.Point
	// prevC and prevQ are the reflected control points a smooth curve takes
	// from the one before it, and cmd the command an operand set repeats.
	var prevC, prevQ raster.Point
	var smoothC, smoothQ bool
	cmd := byte(0)
	open := false

	for {
		s.sep()
		if s.i >= len(s.s) {
			return p
		}
		c := s.s[s.i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			cmd = c
			s.i++
		case cmd == 0:
			return p
		case cmd == 'M':
			cmd = 'L'
		case cmd == 'm':
			cmd = 'l'
		}
		rel := cmd >= 'a' && cmd <= 'z'
		off := raster.Point{}
		if rel {
			off = cur
		}

		switch cmd | 0x20 {
		case 'm':
			v, ok := s.coords(2)
			if !ok {
				return p
			}
			cur = raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			p.MoveTo(cur.X, cur.Y)
			start, open = cur, true
			smoothC, smoothQ = false, false

		case 'l':
			v, ok := s.coords(2)
			if !ok {
				return p
			}
			cur = raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			p.LineTo(cur.X, cur.Y)
			smoothC, smoothQ = false, false

		case 'h':
			v, ok := s.coords(1)
			if !ok {
				return p
			}
			cur.X = off.X + v[0]
			p.LineTo(cur.X, cur.Y)
			smoothC, smoothQ = false, false

		case 'v':
			v, ok := s.coords(1)
			if !ok {
				return p
			}
			cur.Y = off.Y + v[0]
			p.LineTo(cur.X, cur.Y)
			smoothC, smoothQ = false, false

		case 'c':
			v, ok := s.coords(6)
			if !ok {
				return p
			}
			c1 := raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			c2 := raster.Point{X: off.X + v[2], Y: off.Y + v[3]}
			cur = raster.Point{X: off.X + v[4], Y: off.Y + v[5]}
			p.CurveTo(c1.X, c1.Y, c2.X, c2.Y, cur.X, cur.Y)
			prevC, smoothC, smoothQ = c2, true, false

		case 's':
			v, ok := s.coords(4)
			if !ok {
				return p
			}
			c1 := cur
			if smoothC {
				c1 = raster.Point{X: 2*cur.X - prevC.X, Y: 2*cur.Y - prevC.Y}
			}
			c2 := raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			cur = raster.Point{X: off.X + v[2], Y: off.Y + v[3]}
			p.CurveTo(c1.X, c1.Y, c2.X, c2.Y, cur.X, cur.Y)
			prevC, smoothC, smoothQ = c2, true, false

		case 'q':
			v, ok := s.coords(4)
			if !ok {
				return p
			}
			q := raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			end := raster.Point{X: off.X + v[2], Y: off.Y + v[3]}
			quadTo(p, cur, q, end)
			cur, prevQ, smoothQ, smoothC = end, q, true, false

		case 't':
			v, ok := s.coords(2)
			if !ok {
				return p
			}
			q := cur
			if smoothQ {
				q = raster.Point{X: 2*cur.X - prevQ.X, Y: 2*cur.Y - prevQ.Y}
			}
			end := raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			quadTo(p, cur, q, end)
			cur, prevQ, smoothQ, smoothC = end, q, true, false

		case 'a':
			r, ok := s.coords(3)
			if !ok {
				return p
			}
			large, ok := s.flag()
			if !ok {
				return p
			}
			sweep, ok := s.flag()
			if !ok {
				return p
			}
			v, ok := s.coords(2)
			if !ok {
				return p
			}
			end := raster.Point{X: off.X + v[0], Y: off.Y + v[1]}
			arcTo(p, cur, r[0], r[1], r[2], large, sweep, end)
			cur = end
			smoothC, smoothQ = false, false

		case 'z':
			if open {
				p.Close()
				cur = start
			}
			smoothC, smoothQ = false, false

		default:
			return p
		}
	}
}

// coords reads n numbers, and fails without moving on when the list runs out
// part way through a command's operands.
func (p *scanner) coords(n int) ([]float32, bool) {
	var v [7]float32
	mark := p.i
	for i := 0; i < n; i++ {
		p.sep()
		f, ok := p.number()
		if !ok {
			p.i = mark
			return nil, false
		}
		v[i] = f
	}
	return v[:n], true
}

// quadTo raises a quadratic to the cubic the path holds.
func quadTo(p *raster.Path, from, q, to raster.Point) {
	c1 := raster.Point{
		X: from.X + float32(2.0/3.0)*(q.X-from.X),
		Y: from.Y + float32(2.0/3.0)*(q.Y-from.Y),
	}
	c2 := raster.Point{
		X: to.X + float32(2.0/3.0)*(q.X-to.X),
		Y: to.Y + float32(2.0/3.0)*(q.Y-to.Y),
	}
	p.CurveTo(c1.X, c1.Y, c2.X, c2.Y, to.X, to.Y)
}

// arcTo turns the endpoint form of an elliptical arc into the center form and
// draws it as cubics, SVG 1.1 F.6.
func arcTo(p *raster.Path, from raster.Point, rx, ry, rot float32, large, sweep bool, to raster.Point) {
	if from == to {
		return
	}
	rx, ry = float32(math.Abs(float64(rx))), float32(math.Abs(float64(ry)))
	if rx == 0 || ry == 0 {
		p.LineTo(to.X, to.Y)
		return
	}
	phi := float64(rot) * math.Pi / 180
	cosp, sinp := math.Cos(phi), math.Sin(phi)

	// F.6.5.1, the endpoint halfway between the two in the ellipse's own frame.
	dx, dy := float64(from.X-to.X)/2, float64(from.Y-to.Y)/2
	x1 := cosp*dx + sinp*dy
	y1 := -sinp*dx + cosp*dy

	rxf, ryf := float64(rx), float64(ry)
	// F.6.6.2, an ellipse too small to reach is scaled up until it just does.
	if l := x1*x1/(rxf*rxf) + y1*y1/(ryf*ryf); l > 1 {
		s := math.Sqrt(l)
		rxf, ryf = rxf*s, ryf*s
	}

	// F.6.5.2, the center in that frame.
	num := rxf*rxf*ryf*ryf - rxf*rxf*y1*y1 - ryf*ryf*x1*x1
	den := rxf*rxf*y1*y1 + ryf*ryf*x1*x1
	f := 0.0
	if den > 0 && num > 0 {
		f = math.Sqrt(num / den)
	}
	if large == sweep {
		f = -f
	}
	cx1 := f * rxf * y1 / ryf
	cy1 := -f * ryf * x1 / rxf

	// F.6.5.3 and F.6.5.5, the center back in user space and the two angles.
	cx := cosp*cx1 - sinp*cy1 + float64(from.X+to.X)/2
	cy := sinp*cx1 + cosp*cy1 + float64(from.Y+to.Y)/2
	theta := angleTo(1, 0, (x1-cx1)/rxf, (y1-cy1)/ryf)
	delta := angleTo((x1-cx1)/rxf, (y1-cy1)/ryf, (-x1-cx1)/rxf, (-y1-cy1)/ryf)
	switch {
	case !sweep && delta > 0:
		delta -= 2 * math.Pi
	case sweep && delta < 0:
		delta += 2 * math.Pi
	}

	// A cubic holds a quarter turn to a rounding error; the arc is cut so.
	n := int(math.Ceil(math.Abs(delta) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	step := delta / float64(n)
	// The control points of one segment, F.6a, where 4/3 tan(t/4) comes from.
	k := 4.0 / 3.0 * math.Tan(step/4)
	for i := 0; i < n; i++ {
		a0 := theta + float64(i)*step
		a1 := a0 + step
		c0, s0 := math.Cos(a0), math.Sin(a0)
		c1, s1 := math.Cos(a1), math.Sin(a1)
		p0x, p0y := ellipse(cx, cy, rxf, ryf, cosp, sinp, c0, s0)
		p1x, p1y := ellipse(cx, cy, rxf, ryf, cosp, sinp, c1, s1)
		d0x, d0y := tangent(rxf, ryf, cosp, sinp, c0, s0)
		d1x, d1y := tangent(rxf, ryf, cosp, sinp, c1, s1)
		p.CurveTo(
			float32(p0x+k*d0x), float32(p0y+k*d0y),
			float32(p1x-k*d1x), float32(p1y-k*d1y),
			float32(p1x), float32(p1y))
	}
}

func ellipse(cx, cy, rx, ry, cosp, sinp, cos, sin float64) (x, y float64) {
	ex, ey := rx*cos, ry*sin
	return cx + cosp*ex - sinp*ey, cy + sinp*ex + cosp*ey
}

func tangent(rx, ry, cosp, sinp, cos, sin float64) (x, y float64) {
	ex, ey := -rx*sin, ry*cos
	return cosp*ex - sinp*ey, sinp*ex + cosp*ey
}

// angleTo is the signed angle from one vector to another.
func angleTo(ux, uy, vx, vy float64) float64 {
	dot := ux*vx + uy*vy
	l := math.Hypot(ux, uy) * math.Hypot(vx, vy)
	if l == 0 {
		return 0
	}
	a := math.Acos(math.Min(math.Max(dot/l, -1), 1))
	if ux*vy-uy*vx < 0 {
		return -a
	}
	return a
}
