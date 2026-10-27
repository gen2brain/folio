package svg

import (
	"strings"

	"github.com/gen2brain/folio/raster"
)

// clip pushes the clip clip-path names and reports whether the caller must pop it.
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

	m := raster.Concat(atOrigin(transform(c.attr["transform"]),
		r.prop(c, "transform-origin"), st.vw, st.vh, st.em), ctm)
	// SVG 1.1 14.3.5.
	if strings.TrimSpace(r.inherited(c, "clipPathUnits", 0)) == "objectBoundingBox" {
		box := r.shapeBounds(n, st)
		if box.IsEmpty() {
			return false
		}
		m = raster.Concat(raster.Concat(raster.Scale(box.X1-box.X0, box.Y1-box.Y0),
			raster.Translate(box.X0, box.Y0)), m)
	}

	var p raster.Path
	even := false
	for _, k := range c.kids {
		sub := r.clipShape(k, st)
		if sub == nil {
			continue
		}
		km := atOrigin(transform(k.attr["transform"]),
			r.prop(k, "transform-origin"), st.vw, st.vh, st.em)
		if km != raster.Identity {
			sub = sub.Transform(km)
		}
		if strings.TrimSpace(r.ancestral(k, "clip-rule")) == "evenodd" {
			even = true
		}
		p.Append(sub)
	}
	if p.IsEmpty() {
		p.Rect(0, 0, 0, 0)
	}
	if !r.spend(r.pathWork(&p, true, nil, m)) {
		return false
	}
	r.dev.ClipPath(&p, even, m, raster.InfiniteRect)
	return true
}

// clipShape is the outline one child of a clipPath contributes, through a use too.
func (r *runner) clipShape(n *node, st state) *raster.Path {
	switch strings.TrimSpace(r.prop(n, "display")) {
	case "none":
		return nil
	}
	switch strings.TrimSpace(r.prop(n, "visibility")) {
	case "hidden", "collapse":
		return nil
	}
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

// shapeBounds is the box an element's own geometry covers.
func (r *runner) shapeBounds(n *node, st state) raster.Rect {
	if !r.spend(boundsWork) || strings.TrimSpace(r.prop(n, "display")) == "none" {
		return raster.Rect{}
	}
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
	case "text":
		if r.depth >= maxNesting {
			return raster.Rect{}
		}
		r.depth++
		c := &textCursor{}
		r.textRun(n, st, c, 0, true)
		laid := r.laidOut(c)
		r.depth--
		out := raster.EmptyRect
		for _, g := range laid {
			if g.face == nil {
				continue
			}
			out = out.AddPoint(raster.Point{X: g.x, Y: g.y - g.face.Ascent*g.size})
			out = out.AddPoint(raster.Point{X: g.x + g.adv, Y: g.y - g.face.Descent*g.size})
		}
		if out.IsEmpty() {
			return raster.Rect{}
		}
		return out
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
		return raster.Concat(atOrigin(transform(t.attr["transform"]),
			r.prop(t, "transform-origin"), st.vw, st.vh, st.em),
			raster.Translate(x, y)).ApplyRect(b)
	default:
		out := raster.EmptyRect
		for _, k := range n.kids {
			b := r.shapeBounds(k, st)
			if b.IsEmpty() {
				continue
			}
			b = atOrigin(transform(k.attr["transform"]),
				r.prop(k, "transform-origin"), st.vw, st.vh, st.em).ApplyRect(b)
			out = out.AddPoint(raster.Point{X: b.X0, Y: b.Y0})
			out = out.AddPoint(raster.Point{X: b.X1, Y: b.Y1})
		}
		return out
	}
	if p == nil || !r.spend(segmentWork*int64(p.Len())) {
		return raster.Rect{}
	}
	return p.Bounds(raster.Identity)
}
