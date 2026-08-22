package svg

import (
	"math"
	"strings"

	"github.com/gen2brain/folio/raster"
)

// markers draws what marker-start, marker-mid and marker-end put at the
// vertices of a path, which is what an arrow head on a diagram is.
func (r *runner) markers(p *raster.Path, ctm raster.Matrix, st state) {
	if st.markStart == "" && st.markMid == "" && st.markEnd == "" {
		return
	}
	v := vertices(p)
	if len(v) == 0 {
		return
	}
	for i, pt := range v {
		which := st.markMid
		switch i {
		case 0:
			which = st.markStart
		case len(v) - 1:
			which = st.markEnd
		}
		if which == "" {
			continue
		}
		r.marker(which, pt, ctm, st)
	}
}

// vertex is a point of the path and the direction the outline is going there,
// which is what a marker turns to face.
type vertex struct {
	at          raster.Point
	in, out     raster.Point
	hasIn, out2 bool
}

// vertices walks a path for the points a marker sits on, with the direction
// of the segment either side of each.
func vertices(p *raster.Path) []vertex {
	w := &vertexWalker{}
	p.Walk(w)
	return w.out
}

type vertexWalker struct {
	out  []vertex
	last raster.Point
	have bool
}

func (w *vertexWalker) add(to raster.Point, from raster.Point, first bool) {
	dir := raster.Point{X: to.X - from.X, Y: to.Y - from.Y}
	if first {
		w.out = append(w.out, vertex{at: from, out: dir, out2: true})
	}
	if n := len(w.out); n > 0 && !first {
		w.out[n-1].out, w.out[n-1].out2 = dir, true
	}
	w.out = append(w.out, vertex{at: to, in: dir, hasIn: true})
}

func (w *vertexWalker) MoveTo(x, y float32) {
	w.last = raster.Point{X: x, Y: y}
	w.out = append(w.out, vertex{at: w.last})
	w.have = true
}

func (w *vertexWalker) LineTo(x, y float32) {
	to := raster.Point{X: x, Y: y}
	w.add(to, w.last, false)
	w.last = to
}

func (w *vertexWalker) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	to := raster.Point{X: x3, Y: y3}
	// The direction at each end of a cubic is toward its own control point,
	// and toward the far end when that point sits on the end.
	in := raster.Point{X: x1 - w.last.X, Y: y1 - w.last.Y}
	if in.X == 0 && in.Y == 0 {
		in = raster.Point{X: x3 - w.last.X, Y: y3 - w.last.Y}
	}
	out := raster.Point{X: x3 - x2, Y: y3 - y2}
	if out.X == 0 && out.Y == 0 {
		out = raster.Point{X: x3 - w.last.X, Y: y3 - w.last.Y}
	}
	if n := len(w.out); n > 0 {
		w.out[n-1].out, w.out[n-1].out2 = in, true
	}
	w.out = append(w.out, vertex{at: to, in: out, hasIn: true})
	w.last = to
}

func (w *vertexWalker) Close() {}

// marker draws one marker at a vertex, turned the way the path runs there.
func (r *runner) marker(value string, v vertex, ctm raster.Matrix, st state) {
	id := serverID(value)
	if id == "" {
		return
	}
	m := r.doc.byID[id]
	if m == nil || m.name != "marker" || r.depth >= maxNesting {
		return
	}
	r.depth++
	defer func() { r.depth-- }()

	w, wok := length(r.inherited(m, "markerWidth", 0), st.vw, st.em)
	h, hok := length(r.inherited(m, "markerHeight", 0), st.vh, st.em)
	if !wok {
		w = 3
	}
	if !hok {
		h = 3
	}
	if w <= 0 || h <= 0 {
		return
	}
	refX, _ := length(r.inherited(m, "refX", 0), st.vw, st.em)
	refY, _ := length(r.inherited(m, "refY", 0), st.vh, st.em)

	scale := float32(1)
	if strings.TrimSpace(r.inherited(m, "markerUnits", 0)) != "userSpaceOnUse" {
		// The default is to scale a marker by the width of the line it sits
		// on, SVG 1.1 11.6.2.
		scale = st.width
	}
	if scale <= 0 {
		return
	}

	at := raster.Concat(raster.Scale(scale, scale), raster.Translate(v.at.X, v.at.Y))
	if a := markerAngle(m, v, r); a != 0 {
		at = raster.Concat(raster.Rotate(float64(a)), at)
	}
	at = raster.Concat(at, ctm)

	sub := initialState(w, h)
	// The marker's viewport is w by h with refX,refY of it sitting on the
	// vertex, so the frame is the viewport moved back by that point.
	ref := raster.Point{X: refX, Y: refY}
	inner := at
	if vb := numbers(r.inherited(m, "viewBox", 0)); len(vb) == 4 && vb[2] > 0 && vb[3] > 0 {
		al, sl := aspect(r.inherited(m, "preserveAspectRatio", 0))
		vp := viewport(vb, al, sl, w, h)
		ref = vp.Apply(ref)
		frame := raster.Concat(raster.Translate(-ref.X, -ref.Y), at)
		inner = raster.Concat(vp, frame)
		at = frame
		sub.vw, sub.vh = vb[2], vb[3]
	} else {
		at = raster.Concat(raster.Translate(-ref.X, -ref.Y), at)
		inner = at
	}

	// A marker clips to its viewport unless it says not to.
	clipped := strings.TrimSpace(r.inherited(m, "overflow", 0)) != "visible"
	if clipped {
		var edge raster.Path
		edge.Rect(0, 0, w, h)
		r.dev.ClipPath(&edge, false, at, raster.InfiniteRect)
	}
	r.children(m, inner, sub)
	if clipped {
		r.dev.PopClip()
	}
}

// markerAngle is how far a marker is turned: what orient asks for, or the way
// the path runs through the vertex.
func markerAngle(m *node, v vertex, r *runner) float32 {
	o := strings.TrimSpace(r.inherited(m, "orient", 0))
	switch o {
	case "", "0":
		return 0
	case "auto", "auto-start-reverse":
		dir := v.in
		if !v.hasIn {
			dir = v.out
		} else if v.out2 {
			// A vertex with a segment either side faces halfway between them.
			dir = raster.Point{X: norm(v.in).X + norm(v.out).X, Y: norm(v.in).Y + norm(v.out).Y}
		}
		if dir.X == 0 && dir.Y == 0 {
			return 0
		}
		a := float32(math.Atan2(float64(dir.Y), float64(dir.X)) * 180 / math.Pi)
		if o == "auto-start-reverse" && !v.hasIn {
			a += 180
		}
		return a
	}
	if v := numbers(o); len(v) == 1 {
		return v[0]
	}
	return 0
}

func norm(p raster.Point) raster.Point {
	l := float32(math.Hypot(float64(p.X), float64(p.Y)))
	if l == 0 {
		return p
	}
	return raster.Point{X: p.X / l, Y: p.Y / l}
}
