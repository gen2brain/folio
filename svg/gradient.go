package svg

import (
	"math"
	"sort"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// stop is one entry of a gradient's ramp: where it sits between the two ends,
// what color it is, and how opaque.
type stop struct {
	offset float32
	color  [3]float32
	alpha  float32
}

// gradient is a linearGradient or a radialGradient resolved against the shape
// it paints, which is what gfx.Shade wants: a transform into the space the
// coordinates are in, and a shader for a destination.
type gradient struct {
	matrix raster.Matrix
	c0, c1 raster.Point
	r0, r1 float32
	radial bool
	stops  []stop
	// reps is how many periods of the ramp the table holds and first which
	// one it starts at, which is how reflect and repeat are drawn by a
	// shader that only knows how to pad.
	reps, first int
	mirror      bool
}

// Transform implements gfx.Shade.
func (g *gradient) Transform() raster.Matrix { return g.matrix }

// Bounds implements gfx.Shade. A gradient covers whatever it is clipped to.
func (g *gradient) Bounds() raster.Rect { return raster.Rect{} }

// ColorSpace implements gfx.Shade.
func (g *gradient) ColorSpace() *gfx.ColorSpace { return gfx.DeviceRGB }

// Shader implements gfx.Shade.
func (g *gradient) Shader(model raster.Model, ctm raster.Matrix, box raster.Rect) raster.Shader {
	if len(g.stops) == 0 {
		return nil
	}
	n := model.Components()
	lut := make([]uint8, 256*n)
	rgb := make([]uint8, 3)
	for i := 0; i < 256; i++ {
		c := g.at(g.ramp(float32(i) / 255))
		for k := 0; k < 3; k++ {
			rgb[k] = uint8(min(max(c[k], 0), 1)*255 + 0.5)
		}
		model.FromRGB(lut[i*n:(i+1)*n], rgb)
	}
	return raster.NewGradient(raster.GradientSpec{
		Matrix: ctm,
		LUT:    lut,
		N:      n,
		C0:     g.c0,
		C1:     g.c1,
		R0:     g.r0,
		R1:     g.r1,
		Radial: g.radial,
		// A gradient pads at both ends, which is what spreadMethod says
		// unless it asks to be reflected or repeated.
		Ext0: true,
		Ext1: true,
	})
}

// ramp turns a fraction of the whole table into a fraction of one period,
// which is what makes a repeated or a reflected gradient out of a padded one:
// the table covers every period the shape reaches and the geometry is
// stretched to match.
func (g *gradient) ramp(t float32) float32 {
	if g.reps <= 1 {
		return t
	}
	u := t * float32(g.reps)
	k := int(u)
	if k >= g.reps {
		k = g.reps - 1
	}
	f := u - float32(k)
	if g.mirror && (k+g.first)&1 != 0 {
		return 1 - f
	}
	return f
}

// spread widens a gradient so that its table covers every period the shape
// reaches, which is what reflect and repeat mean. lo and hi are how far along
// the axis the shape runs.
func (g *gradient) spread(mode string, lo, hi float32) {
	if mode != "reflect" && mode != "repeat" {
		return
	}
	first := int(floor32(lo))
	last := int(ceil32(hi))
	if last <= first {
		last = first + 1
	}
	// A gradient whose period is a sliver of the shape would need a table
	// with no room for it, and padding is the better answer then.
	if n := last - first; n < 1 || n > maxPeriods {
		return
	}
	g.first, g.reps, g.mirror = first, last-first, mode == "reflect"
	if g.radial {
		// A radial gradient repeats outwards from its center, so only the
		// far end moves.
		g.r1 *= float32(g.reps)
		return
	}
	d := raster.Point{X: g.c1.X - g.c0.X, Y: g.c1.Y - g.c0.Y}
	o := g.c0
	g.c0 = raster.Point{X: o.X + d.X*float32(first), Y: o.Y + d.Y*float32(first)}
	g.c1 = raster.Point{X: o.X + d.X*float32(last), Y: o.Y + d.Y*float32(last)}
}

// maxPeriods bounds how many times a ramp is written into one table, past
// which the periods are finer than the table can hold anyway.
const maxPeriods = 128

// axisRange is how far along a gradient's axis the corners of a box reach,
// which says how many periods a repeat has to cover.
func (g *gradient) axisRange(box raster.Rect) (lo, hi float32) {
	corners := [4]raster.Point{
		{X: box.X0, Y: box.Y0}, {X: box.X1, Y: box.Y0},
		{X: box.X0, Y: box.Y1}, {X: box.X1, Y: box.Y1},
	}
	first := true
	for _, p := range corners {
		var t float32
		if g.radial {
			if g.r1 <= 0 {
				return 0, 1
			}
			dx, dy := p.X-g.c1.X, p.Y-g.c1.Y
			t = float32(math.Hypot(float64(dx), float64(dy))) / g.r1
		} else {
			dx, dy := g.c1.X-g.c0.X, g.c1.Y-g.c0.Y
			l2 := dx*dx + dy*dy
			if l2 == 0 {
				return 0, 1
			}
			t = ((p.X-g.c0.X)*dx + (p.Y-g.c0.Y)*dy) / l2
		}
		if first || t < lo {
			lo = t
		}
		if first || t > hi {
			hi = t
		}
		first = false
	}
	return lo, hi
}

// at is the color a fraction along the ramp, which is the two stops either
// side of it mixed.
func (g *gradient) at(t float32) [3]float32 {
	s := g.stops
	if t <= s[0].offset {
		return s[0].color
	}
	last := s[len(s)-1]
	if t >= last.offset {
		return last.color
	}
	for i := 1; i < len(s); i++ {
		if t > s[i].offset {
			continue
		}
		lo, hi := s[i-1], s[i]
		span := hi.offset - lo.offset
		if span <= 0 {
			return hi.color
		}
		f := (t - lo.offset) / span
		var out [3]float32
		for k := 0; k < 3; k++ {
			out[k] = lo.color[k] + f*(hi.color[k]-lo.color[k])
		}
		return out
	}
	return last.color
}

// alpha is the one opacity a gradient is drawn at. A ramp whose stops differ
// in opacity needs an alpha per entry of the table, which the shader has no
// room for, so the first stop stands for all of them.
func (g *gradient) alpha() float32 {
	if len(g.stops) == 0 {
		return 1
	}
	return g.stops[0].alpha
}

// server resolves a paint that names a gradient, and is nil for one that
// names anything else. bbox is the shape's own box, which a gradient in
// objectBoundingBox units is a fraction of.
func (r *runner) server(value string, box raster.Rect, st state) *gradient {
	id := serverID(value)
	if id == "" {
		return nil
	}
	n := r.doc.byID[id]
	if n == nil {
		return nil
	}
	switch n.name {
	case "linearGradient", "radialGradient":
	default:
		return nil
	}
	g := &gradient{radial: n.name == "radialGradient"}
	g.stops = r.stops(n, st, 0)
	if len(g.stops) == 0 {
		return nil
	}

	// A gradient in the units of the shape is written in fractions of its
	// box, so the box becomes the transform, SVG 1.1 13.2.2.
	user := strings.TrimSpace(r.inherited(n, "gradientUnits", 0)) == "userSpaceOnUse"
	ref := raster.Rect{X1: 1, Y1: 1}
	if user {
		ref = raster.Rect{X1: st.vw, Y1: st.vh}
	}
	num := func(name string, def float32, vertical bool) float32 {
		s := r.inherited(n, name, 0)
		if s == "" {
			return def
		}
		axis := ref.X1
		if vertical {
			axis = ref.Y1
		}
		v, ok := length(s, axis, st.em)
		if !ok {
			return def
		}
		return v
	}

	if g.radial {
		cx := num("cx", 0.5*ref.X1, false)
		cy := num("cy", 0.5*ref.Y1, true)
		rr := num("r", 0.5*diagonal(ref.X1, ref.Y1), false)
		fx, fy := cx, cy
		if s := r.inherited(n, "fx", 0); s != "" {
			fx = num("fx", cx, false)
		}
		if s := r.inherited(n, "fy", 0); s != "" {
			fy = num("fy", cy, true)
		}
		g.c0, g.r0 = raster.Point{X: fx, Y: fy}, 0
		g.c1, g.r1 = raster.Point{X: cx, Y: cy}, rr
		if rr <= 0 {
			return nil
		}
	} else {
		x1 := num("x1", 0, false)
		y1 := num("y1", 0, true)
		x2 := num("x2", ref.X1, false)
		y2 := num("y2", 0, true)
		g.c0 = raster.Point{X: x1, Y: y1}
		g.c1 = raster.Point{X: x2, Y: y2}
		if g.c0 == g.c1 {
			// A gradient with no length is the color of its last stop.
			last := g.stops[len(g.stops)-1]
			g.stops = []stop{{offset: 0, color: last.color, alpha: last.alpha},
				{offset: 1, color: last.color, alpha: last.alpha}}
			g.c1.X += 1
		}
	}

	// The shape reaches this far along the axis, which is how many periods a
	// reflected or a repeated ramp has to be written out over.
	span := box
	if !user {
		span = raster.Rect{X1: 1, Y1: 1}
	}
	lo, hi := g.axisRange(span)
	g.spread(strings.TrimSpace(r.inherited(n, "spreadMethod", 0)), lo, hi)

	g.matrix = transform(r.inherited(n, "gradientTransform", 0))
	if !user {
		if box.IsEmpty() {
			return nil
		}
		unit := raster.Concat(raster.Scale(box.X1-box.X0, box.Y1-box.Y0),
			raster.Translate(box.X0, box.Y0))
		g.matrix = raster.Concat(g.matrix, unit)
	}
	return g
}

// serverID is the element a url() paint names, and empty for a paint that
// names none.
func serverID(value string) string {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "url(") {
		return ""
	}
	i := strings.IndexByte(v, ')')
	if i < 0 {
		return ""
	}
	inner := strings.Trim(strings.TrimSpace(v[4:i]), `"'`)
	return fragment(inner)
}

// inherited reads an attribute off a gradient, following href to the one it
// was written as a variation of, SVG 1.1 13.2.4.
func (r *runner) inherited(n *node, name string, depth int) string {
	if v, ok := n.attr[name]; ok {
		return v
	}
	if depth >= maxNesting {
		return ""
	}
	if p := r.doc.byID[fragment(n.attr["href"])]; p != nil && p != n {
		return r.inherited(p, name, depth+1)
	}
	return ""
}

// stops reads a gradient's ramp, following href when it declares none of its
// own. The offsets are clamped and never go backwards.
func (r *runner) stops(n *node, st state, depth int) []stop {
	var out []stop
	for _, k := range n.kids {
		if k.name != "stop" {
			continue
		}
		s := stop{alpha: 1}
		if v, ok := opacity(r.prop(k, "offset")); ok {
			s.offset = v
		}
		c, ok := parseColor(r.prop(k, "stop-color"), st.color)
		if !ok {
			c = black
		}
		s.color = c.color
		if v, ok := opacity(r.prop(k, "stop-opacity")); ok {
			s.alpha = v
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		if depth >= maxNesting {
			return nil
		}
		if p := r.doc.byID[fragment(n.attr["href"])]; p != nil && p != n {
			return r.stops(p, st, depth+1)
		}
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	for i := 1; i < len(out); i++ {
		if out[i].offset < out[i-1].offset {
			out[i].offset = out[i-1].offset
		}
	}
	return out
}
