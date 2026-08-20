package font

import "github.com/gen2brain/pdf/raster"

// Composite glyph flags, OpenType glyf table.
const (
	argsAreWords = 1 << iota
	argsAreXY
	roundXYToGrid
	haveScale
	_
	moreComponents
	haveXYScale
	have2x2
	haveInstructions
	useMyMetrics
)

// Point flags of a simple glyph.
const (
	onCurve = 1 << iota
	xShort
	yShort
	repeatFlag
	xSameOrPos
	ySameOrPos
)

const maxComponentDepth = 8

// glyfSlice returns the outline data of one glyph.
func (s *sfnt) glyfSlice(gid int) []byte {
	loca, glyf := s.tables["loca"], s.tables["glyf"]
	var start, end int
	if s.locaLong {
		if 4*gid+8 > len(loca) {
			return nil
		}
		start, end = be32(loca, 4*gid), be32(loca, 4*gid+4)
	} else {
		if 2*gid+4 > len(loca) {
			return nil
		}
		start, end = 2*be16(loca, 2*gid), 2*be16(loca, 2*gid+2)
	}
	if start >= end || start < 0 || end > len(glyf) {
		return nil
	}
	return glyf[start:end]
}

// glyfPath builds the outline of a TrueType glyph.
func (f *Font) glyfPath(gid, depth int) *raster.Path {
	if depth > maxComponentDepth {
		return nil
	}
	b := f.sfnt.glyfSlice(gid)
	if len(b) < 10 {
		return &raster.Path{}
	}
	if n := be16s(b, 0); n < 0 {
		return f.compositeGlyph(b[10:], depth)
	}
	return simpleGlyph(b)
}

// simpleGlyph reads a contour glyph.
func simpleGlyph(b []byte) *raster.Path {
	n := be16(b, 0)
	if n <= 0 || 10+2*n+2 > len(b) {
		return &raster.Path{}
	}
	ends := make([]int, n)
	for i := range ends {
		ends[i] = be16(b, 10+2*i)
	}
	points := ends[n-1] + 1
	if points <= 0 || points > 10000 {
		return &raster.Path{}
	}

	at := 10 + 2*n
	at += 2 + be16(b, at) // skip the instructions

	flags := make([]byte, 0, points)
	for len(flags) < points && at < len(b) {
		fl := b[at]
		at++
		flags = append(flags, fl)
		if fl&repeatFlag != 0 && at < len(b) {
			r := int(b[at])
			at++
			for i := 0; i < r && len(flags) < points; i++ {
				flags = append(flags, fl)
			}
		}
	}
	if len(flags) < points {
		return &raster.Path{}
	}

	xs := make([]int, points)
	v := 0
	for i, fl := range flags {
		switch {
		case fl&xShort != 0:
			if at >= len(b) {
				break
			}
			d := int(b[at])
			at++
			if fl&xSameOrPos == 0 {
				d = -d
			}
			v += d
		case fl&xSameOrPos == 0:
			v += be16s(b, at)
			at += 2
		}
		xs[i] = v
	}
	ys := make([]int, points)
	v = 0
	for i, fl := range flags {
		switch {
		case fl&yShort != 0:
			if at >= len(b) {
				break
			}
			d := int(b[at])
			at++
			if fl&ySameOrPos == 0 {
				d = -d
			}
			v += d
		case fl&ySameOrPos == 0:
			v += be16s(b, at)
			at += 2
		}
		ys[i] = v
	}

	p := &raster.Path{}
	first := 0
	for _, last := range ends {
		if last < first || last >= points {
			break
		}
		emitContour(p, flags[first:last+1], xs[first:last+1], ys[first:last+1])
		first = last + 1
	}
	return p
}

// emitContour converts one quadratic contour. Where it starts matters: a
// contour whose first point is a control point begins at the last point when
// that one is on the curve, and at the midpoint of the two otherwise, which
// is what FreeType does and therefore what every outline is compared against.
func emitContour(p *raster.Path, flags []byte, xs, ys []int) {
	n := len(flags)
	if n == 0 {
		return
	}
	on := func(i int) bool { return flags[i]&onCurve != 0 }
	at := func(i int) (float32, float32) { return float32(xs[i]), float32(ys[i]) }

	var sx, sy float32
	first, last := 0, n
	switch {
	case on(0):
		sx, sy = at(0)
		first = 1
	case on(n - 1):
		sx, sy = at(n - 1)
		last = n - 1
	default:
		x0, y0 := at(0)
		xn, yn := at(n - 1)
		sx, sy = (x0+xn)/2, (y0+yn)/2
	}
	p.MoveTo(sx, sy)

	var cx, cy float32
	ctrl := false
	for i := first; i < last; i++ {
		x, y := at(i)
		switch {
		case on(i) && ctrl:
			quadTo(p, cx, cy, x, y)
			ctrl = false
		case on(i):
			p.LineTo(x, y)
		case ctrl:
			quadTo(p, cx, cy, (cx+x)/2, (cy+y)/2)
			cx, cy = x, y
		default:
			cx, cy = x, y
			ctrl = true
		}
	}
	if ctrl {
		quadTo(p, cx, cy, sx, sy)
	} else {
		p.LineTo(sx, sy)
	}
	p.Close()
}

// quadTo adds a quadratic segment as the cubic it is equal to.
func quadTo(p *raster.Path, cx, cy, x, y float32) {
	s := p.Current()
	p.CurveTo(
		s.X+2.0/3.0*(cx-s.X), s.Y+2.0/3.0*(cy-s.Y),
		x+2.0/3.0*(cx-x), y+2.0/3.0*(cy-y),
		x, y)
}

// compositeGlyph assembles a glyph out of others.
func (f *Font) compositeGlyph(b []byte, depth int) *raster.Path {
	out := &raster.Path{}
	at := 0
	for at+4 <= len(b) {
		flags := be16(b, at)
		gid := be16(b, at+2)
		at += 4

		var dx, dy float32
		if flags&argsAreWords != 0 {
			if flags&argsAreXY != 0 {
				dx, dy = float32(be16s(b, at)), float32(be16s(b, at+2))
			}
			at += 4
		} else {
			if flags&argsAreXY != 0 && at+2 <= len(b) {
				dx, dy = float32(int8(b[at])), float32(int8(b[at+1]))
			}
			at += 2
		}

		m := raster.Matrix{A: 1, D: 1}
		switch {
		case flags&haveScale != 0:
			s := f2dot14(b, at)
			at += 2
			m.A, m.D = s, s
		case flags&haveXYScale != 0:
			m.A, m.D = f2dot14(b, at), f2dot14(b, at+2)
			at += 4
		case flags&have2x2 != 0:
			m.A, m.B = f2dot14(b, at), f2dot14(b, at+2)
			m.C, m.D = f2dot14(b, at+4), f2dot14(b, at+6)
			at += 8
		}
		m.E, m.F = dx, dy

		if sub := f.glyfPath(gid, depth+1); sub != nil && !sub.IsEmpty() {
			out.Append(sub.Transform(m))
		}
		if flags&moreComponents == 0 {
			break
		}
	}
	return out
}

func f2dot14(b []byte, off int) float32 {
	return float32(be16s(b, off)) / 16384
}
