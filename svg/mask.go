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
	if m == nil || m.name != "mask" || r.depth >= maxNesting {
		return false
	}
	r.depth++
	defer func() { r.depth-- }()

	// A mask is the luminance of what it draws, which is what BeginMask
	// composites the next drawing through.
	r.dev.BeginMask(raster.InfiniteRect, true, gfx.DeviceGray, []float32{0}, gfx.ColorParams{})
	sub := initialState(st.vw, st.vh)
	inner := ctm
	if strings.TrimSpace(r.inherited(m, "maskContentUnits", 0)) == "objectBoundingBox" {
		box := r.shapeBounds(n, st)
		if !box.IsEmpty() {
			inner = raster.Concat(raster.Concat(
				raster.Scale(box.X1-box.X0, box.Y1-box.Y0),
				raster.Translate(box.X0, box.Y0)), ctm)
		}
	}
	r.children(m, inner, sub)
	r.dev.EndMask(nil)
	return true
}
