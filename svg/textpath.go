package svg

import (
	"math"

	"github.com/gen2brain/folio/raster"
)

// pathWalk is a path flattened to line segments with the distance along it at
// each one, which is what puts a character somewhere on it.
type pathWalk struct {
	segs  []pathSeg
	total float32
}

type pathSeg struct {
	p0, p1   raster.Point
	at, span float32
}

// pathFlatness is how far a character's polyline may stray from the curve.
const pathFlatness = 0.1

// flattenPath measures a path into segments. A jump from one subpath to the
// next adds no length: the text carries on where the next one starts.
func flattenPath(p *raster.Path, m raster.Matrix) *pathWalk {
	w := &pathWalk{}
	f := &pathMeasure{w: w}
	p.Flatten(m, pathFlatness, f)
	if len(w.segs) == 0 {
		return nil
	}
	return w
}

type pathMeasure struct {
	w     *pathWalk
	cur   raster.Point
	start raster.Point
	open  bool
}

func (f *pathMeasure) MoveTo(x, y float32) {
	f.cur = raster.Point{X: x, Y: y}
	f.start = f.cur
	f.open = true
}

func (f *pathMeasure) LineTo(x, y float32) {
	p := raster.Point{X: x, Y: y}
	if f.open {
		f.w.add(f.cur, p)
	}
	f.cur = p
}

func (f *pathMeasure) Close() {
	if f.open {
		f.w.add(f.cur, f.start)
		f.cur = f.start
	}
}

func (w *pathWalk) add(p0, p1 raster.Point) {
	dx, dy := float64(p1.X-p0.X), float64(p1.Y-p0.Y)
	n := float32(math.Hypot(dx, dy))
	if n <= 0 {
		return
	}
	w.segs = append(w.segs, pathSeg{p0: p0, p1: p1, at: w.total, span: n})
	w.total += n
}

// at is the point a distance along the path and the direction of travel
// there, and false for a distance that is not on it.
func (w *pathWalk) at(s float32) (raster.Point, float32, bool) {
	if s < 0 || s > w.total {
		return raster.Point{}, 0, false
	}
	lo, hi := 0, len(w.segs)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if w.segs[mid].at <= s {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	g := w.segs[lo]
	t := (s - g.at) / g.span
	dx, dy := g.p1.X-g.p0.X, g.p1.Y-g.p0.Y
	return raster.Point{X: g.p0.X + t*dx, Y: g.p0.Y + t*dy},
		float32(math.Atan2(float64(dy), float64(dx))), true
}

// textPath lays a run of characters along the path it names, SVG 1.1 10.13.
// A reference that resolves to no path draws nothing at all.
func (r *runner) textPath(n *node, st state, c *textCursor, depth int) {
	st = r.style(n, st)
	if st.hidden {
		return
	}
	w := r.pathOf(n, st)
	if w == nil {
		return
	}
	was := c.path
	c.path = w
	c.setX, c.setY = 0, 0
	c.hasX, c.hasY = true, true
	c.chunk++
	if v, ok := length(r.prop(n, "startOffset"), w.total, st.em); ok {
		c.setX = v
	}
	// The characters of a textPath run along it: x and y on the element
	// itself say nothing, and a tspan inside it measures along the path.
	face := r.face(st)
	i := 0
	for _, k := range n.kids {
		switch k.name {
		case "":
			i = r.place(c, k.chars, st, face, nil, nil, nil, nil, i)
		case "tspan", "a":
			r.textRun(k, st, c, depth+1, false)
		}
	}
	c.path = was
}

// pathOf is the path a textPath draws along: the one it refers to, under
// whatever that element was transformed by.
func (r *runner) pathOf(n *node, st state) *pathWalk {
	t := r.doc.byID[fragment(n.attr["href"])]
	if t == nil || t.name != "path" {
		return nil
	}
	p := pathData(t.attr["d"])
	if p == nil || p.IsEmpty() {
		return nil
	}
	return flattenPath(p, atOrigin(transform(t.attr["transform"]),
		r.prop(t, "transform-origin"), st.vw, st.vh, st.em))
}

// onPath moves the characters laid along a path onto it: each one sits where
// its middle falls and turns with the direction of travel there, and one that
// falls off either end is not drawn at all.
func onPath(g []glyph) []glyph {
	out := g[:0]
	for _, one := range g {
		if one.path == nil {
			out = append(out, one)
			continue
		}
		p, ang, ok := one.path.at(one.x + one.adv/2)
		if !ok {
			continue
		}
		cos := float32(math.Cos(float64(ang)))
		sin := float32(math.Sin(float64(ang)))
		off := one.y
		one.x = p.X - cos*one.adv/2 - sin*off
		one.y = p.Y - sin*one.adv/2 + cos*off
		one.turn += ang * 180 / math.Pi
		out = append(out, one)
	}
	return out
}
