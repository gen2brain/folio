package svg

import (
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// mask draws what a mask element paints into a soft mask and reports whether
// one was opened, which the caller has to close.
func (r *runner) mask(n *node, ctm raster.Matrix, st state) bool {
	id := serverID(r.prop(n, "mask"))
	if id == "" {
		return false
	}
	m := r.doc.byID[id]
	if m == nil || m.name != "mask" || r.depth >= maxNesting || r.active[m] {
		return false
	}
	r.depth++
	if r.active == nil {
		r.active = map[*node]bool{}
	}
	r.active[m] = true
	defer func() { r.depth--; delete(r.active, m) }()

	// A mask is the luminance of what it draws, or its alpha where SVG 2's
	// mask-type asks for that, which is what BeginMask composites the next
	// drawing through.
	lum := strings.TrimSpace(r.prop(m, "mask-type")) != "alpha"
	r.dev.BeginMask(raster.InfiniteRect, lum, gfx.DeviceGray, []float32{0}, gfx.ColorParams{})
	sub := initialState(st.vw, st.vh)
	box := r.shapeBounds(n, st)
	inner := ctm
	if strings.TrimSpace(r.inherited(m, "maskContentUnits", 0)) == "objectBoundingBox" && !box.IsEmpty() {
		inner = raster.Concat(raster.Concat(
			raster.Scale(box.X1-box.X0, box.Y1-box.Y0),
			raster.Translate(box.X0, box.Y0)), ctm)
	}
	pops := 0
	if r.maskRegion(m, box, ctm, st) {
		pops++
	}
	// A mask of its own is drawn through the one it names, SVG 1.1 14.4.
	if r.mask(m, inner, sub) {
		pops++
	}
	r.children(m, inner, sub)
	for range pops {
		r.dev.PopClip()
	}
	r.dev.EndMask(nil)
	return true
}

// maskRegion clips a mask to the box it declares, which is a fraction of the
// masked element's own unless maskUnits says otherwise.
func (r *runner) maskRegion(m *node, box raster.Rect, ctm raster.Matrix, st state) bool {
	obj := strings.TrimSpace(r.inherited(m, "maskUnits", 0)) != "userSpaceOnUse"
	var reg raster.Rect
	if obj {
		if box.IsEmpty() {
			return false
		}
		bw, bh := box.X1-box.X0, box.Y1-box.Y0
		x := r.flen(m, "x", 1, st, -0.1)
		y := r.flen(m, "y", 1, st, -0.1)
		w := r.flen(m, "width", 1, st, 1.2)
		h := r.flen(m, "height", 1, st, 1.2)
		if w <= 0 || h <= 0 {
			return false
		}
		reg = raster.Rect{
			X0: box.X0 + x*bw, Y0: box.Y0 + y*bh,
			X1: box.X0 + (x+w)*bw, Y1: box.Y0 + (y+h)*bh,
		}
	} else {
		x := r.flen(m, "x", st.vw, st, -0.1*st.vw)
		y := r.flen(m, "y", st.vh, st, -0.1*st.vh)
		w := r.flen(m, "width", st.vw, st, 1.2*st.vw)
		h := r.flen(m, "height", st.vh, st, 1.2*st.vh)
		if w <= 0 || h <= 0 {
			return false
		}
		reg = raster.Rect{X0: x, Y0: y, X1: x + w, Y1: y + h}
	}
	var p raster.Path
	p.Rect(reg.X0, reg.Y0, reg.X1-reg.X0, reg.Y1-reg.Y0)
	r.dev.ClipPath(&p, false, ctm, raster.InfiniteRect)
	return true
}
