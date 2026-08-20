package raster

// Flattener receives a path whose curves have been replaced by line segments.
type Flattener interface {
	MoveTo(x, y float32)
	LineTo(x, y float32)
	Close()
}

// DefaultFlatness is how far a flattened curve may stray from the true one,
// in the units the path is flattened into.
const DefaultFlatness = 0.25

const (
	flattenLimit        = 16
	flattenCollinearity = 1e-30
)

// Flatten walks the path under m, replacing every curve by line segments that
// stay within tol of it. A tol of zero means DefaultFlatness.
func (p *Path) Flatten(m Matrix, tol float32, f Flattener) {
	if tol <= 0 {
		tol = DefaultFlatness
	}
	fl := flattener{f: f, distSq: tol * tol, manhattan: tol * 8}
	var cur, start Point
	i := 0
	for _, c := range p.cmds {
		switch c {
		case cmdMove:
			cur = m.Apply(p.pts[i])
			i++
			start = cur
			f.MoveTo(cur.X, cur.Y)
		case cmdLine:
			cur = m.Apply(p.pts[i])
			i++
			f.LineTo(cur.X, cur.Y)
		case cmdCurve:
			c1 := m.Apply(p.pts[i])
			c2 := m.Apply(p.pts[i+1])
			c3 := m.Apply(p.pts[i+2])
			i += 3
			fl.curve(cur, c1, c2, c3)
			f.LineTo(c3.X, c3.Y)
			cur = c3
		case cmdClose:
			f.Close()
			cur = start
		}
	}
}

type flattener struct {
	f         Flattener
	distSq    float32
	manhattan float32
}

func (fl *flattener) curve(p0, p1, p2, p3 Point) {
	fl.recurse(p0.X, p0.Y, p1.X, p1.Y, p2.X, p2.Y, p3.X, p3.Y, 0)
}

func (fl *flattener) recurse(x1, y1, x2, y2, x3, y3, x4, y4 float32, level int) {
	if level > flattenLimit {
		return
	}
	x12, y12 := (x1+x2)/2, (y1+y2)/2
	x23, y23 := (x2+x3)/2, (y2+y3)/2
	x34, y34 := (x3+x4)/2, (y3+y4)/2
	x123, y123 := (x12+x23)/2, (y12+y23)/2
	x234, y234 := (x23+x34)/2, (y23+y34)/2
	x1234, y1234 := (x123+x234)/2, (y123+y234)/2

	dx, dy := x4-x1, y4-y1
	d2 := absf(float32((x2-x4)*dy) - float32((y2-y4)*dx))
	d3 := absf(float32((x3-x4)*dy) - float32((y3-y4)*dx))

	switch b2i(d2 > flattenCollinearity)<<1 | b2i(d3 > flattenCollinearity) {
	case 0:
		if absf(x1+x3-x2-x2)+absf(y1+y3-y2-y2)+absf(x2+x4-x3-x3)+absf(y2+y4-y3-y3) <= fl.manhattan {
			fl.f.LineTo(x1234, y1234)
			return
		}
	case 1:
		if d3*d3 <= fl.distSq*(float32(dx*dx)+float32(dy*dy)) {
			fl.f.LineTo(x23, y23)
			return
		}
	case 2:
		if d2*d2 <= fl.distSq*(float32(dx*dx)+float32(dy*dy)) {
			fl.f.LineTo(x23, y23)
			return
		}
	case 3:
		if (d2+d3)*(d2+d3) <= fl.distSq*(float32(dx*dx)+float32(dy*dy)) {
			fl.f.LineTo(x23, y23)
			return
		}
	}
	fl.recurse(x1, y1, x12, y12, x123, y123, x1234, y1234, level+1)
	fl.recurse(x1234, y1234, x234, y234, x34, y34, x4, y4, level+1)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
