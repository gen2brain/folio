package svg

import (
	"strings"

	"github.com/gen2brain/folio/raster"
)

// clip pushes what clip-path asks for and reports whether anything was
// pushed, which the caller has to pop.
func (r *runner) clip(n *node, ctm raster.Matrix, st state) bool {
	id := serverID(r.prop(n, "clip-path"))
	if id == "" {
		return false
	}
	c := r.doc.byID[id]
	if c == nil || c.name != "clipPath" {
		return false
	}
	if r.depth >= maxNesting {
		return false
	}
	r.depth++
	defer func() { r.depth-- }()

	m := raster.Concat(transform(c.attr["transform"]), ctm)
	// A clip in the units of the object is written in fractions of the box
	// the shape covers, SVG 1.1 14.3.5.
	if strings.TrimSpace(r.inherited(c, "clipPathUnits", 0)) == "objectBoundingBox" {
		box := r.shapeBounds(n, st)
		if box.IsEmpty() {
			return false
		}
		m = raster.Concat(raster.Concat(raster.Scale(box.X1-box.X0, box.Y1-box.Y0),
			raster.Translate(box.X0, box.Y0)), m)
	}

	// Every shape of the clip counts, so they go into one path: with the
	// nonzero rule the subpaths of one path are the union the spec asks for.
	var p raster.Path
	even := false
	for _, k := range c.kids {
		sub := r.clipShape(k, st)
		if sub == nil {
			continue
		}
		km := transform(k.attr["transform"])
		if km != raster.Identity {
			sub = sub.Transform(km)
		}
		if strings.TrimSpace(r.prop(k, "clip-rule")) == "evenodd" {
			even = true
		}
		p.Append(sub)
	}
	if p.IsEmpty() {
		// A clip with nothing in it clips everything away.
		p.Rect(0, 0, 0, 0)
	}
	r.dev.ClipPath(&p, even, m, raster.InfiniteRect)
	return true
}

// clipShape is the outline one child of a clipPath contributes, which is a
// shape or the shape a use refers to.
func (r *runner) clipShape(n *node, st state) *raster.Path {
	switch n.name {
	case "path":
		return pathData(n.attr["d"])
	case "rect":
		return rectPath(n, st)
	case "circle":
		return circlePath(n, st)
	case "ellipse":
		return ellipsePath(n, st)
	case "line":
		return linePath(n, st)
	case "polyline":
		return polyPath(n, false)
	case "polygon":
		return polyPath(n, true)
	case "use":
		t := r.doc.byID[fragment(n.attr["href"])]
		if t == nil || r.depth >= maxNesting {
			return nil
		}
		r.depth++
		defer func() { r.depth-- }()
		p := r.clipShape(t, st)
		if p == nil {
			return nil
		}
		x, _ := r.length(n, "x", st.vw, st)
		y, _ := r.length(n, "y", st.vh, st)
		if x != 0 || y != 0 {
			p = p.Transform(raster.Translate(x, y))
		}
		return p
	}
	return nil
}

// shapeBounds is the box an element's own geometry covers, which a clip and a
// gradient in the units of the object are fractions of.
func (r *runner) shapeBounds(n *node, st state) raster.Rect {
	var p *raster.Path
	switch n.name {
	case "path":
		p = pathData(n.attr["d"])
	case "rect":
		p = rectPath(n, st)
	case "circle":
		p = circlePath(n, st)
	case "ellipse":
		p = ellipsePath(n, st)
	case "line":
		p = linePath(n, st)
	case "polyline":
		p = polyPath(n, false)
	case "polygon":
		p = polyPath(n, true)
	case "image":
		x, _ := r.length(n, "x", st.vw, st)
		y, _ := r.length(n, "y", st.vh, st)
		w, _ := r.length(n, "width", st.vw, st)
		h, _ := r.length(n, "height", st.vh, st)
		if w <= 0 || h <= 0 {
			return raster.Rect{}
		}
		return raster.Rect{X0: x, Y0: y, X1: x + w, Y1: y + h}
	case "use":
		t := r.doc.byID[fragment(n.attr["href"])]
		if t == nil || t == n || r.depth >= maxNesting {
			return raster.Rect{}
		}
		r.depth++
		b := r.shapeBounds(t, st)
		r.depth--
		if b.IsEmpty() {
			return b
		}
		x, _ := r.length(n, "x", st.vw, st)
		y, _ := r.length(n, "y", st.vh, st)
		return raster.Concat(transform(t.attr["transform"]), raster.Translate(x, y)).ApplyRect(b)
	default:
		// A group's box is everything under it, in the group's own space.
		out := raster.EmptyRect
		for _, k := range n.kids {
			b := r.shapeBounds(k, st)
			if b.IsEmpty() {
				continue
			}
			b = transform(k.attr["transform"]).ApplyRect(b)
			out = out.AddPoint(raster.Point{X: b.X0, Y: b.Y0})
			out = out.AddPoint(raster.Point{X: b.X1, Y: b.Y1})
		}
		return out
	}
	if p == nil {
		return raster.Rect{}
	}
	return p.Bounds(raster.Identity)
}
