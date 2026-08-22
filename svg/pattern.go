package svg

import (
	"strings"

	"github.com/gen2brain/folio/raster"
)

// pattern draws what a pattern server paints over the shape a path covers,
// and reports whether it drew anything. The caller has already clipped to the
// shape, which is what bounds the tiles.
func (r *runner) pattern(value string, box raster.Rect, ctm raster.Matrix, st state) bool {
	id := serverID(value)
	if id == "" {
		return false
	}
	n := r.doc.byID[id]
	if n == nil || n.name != "pattern" || r.depth >= maxNesting {
		return false
	}
	// A pattern with no content of its own draws what the one it was written
	// as a variation of draws.
	content := n
	for i := 0; len(content.kids) == 0 && i < maxNesting; i++ {
		p := r.doc.byID[fragment(content.attr["href"])]
		if p == nil || p == content {
			break
		}
		content = p
	}
	if len(content.kids) == 0 {
		return false
	}

	// The tile's own box is in the units of the shape unless it says
	// otherwise, SVG 1.1 13.3.
	units := strings.TrimSpace(r.inherited(n, "patternUnits", 0))
	user := units == "userSpaceOnUse"
	ref := raster.Rect{X1: 1, Y1: 1}
	if user {
		ref = raster.Rect{X1: st.vw, Y1: st.vh}
	}
	num := func(name string, vertical bool) (float32, bool) {
		s := r.inherited(n, name, 0)
		if s == "" {
			return 0, false
		}
		axis := ref.X1
		if vertical {
			axis = ref.Y1
		}
		return length(s, axis, st.em)
	}
	x, _ := num("x", false)
	y, _ := num("y", true)
	w, wok := num("width", false)
	h, hok := num("height", true)
	if !wok || !hok || w <= 0 || h <= 0 {
		return false
	}
	if !user {
		bw, bh := box.X1-box.X0, box.Y1-box.Y0
		x, y = box.X0+x*bw, box.Y0+y*bh
		w, h = w*bw, h*bh
	}

	// The tile is placed where it says, and the drawing inside it is scaled
	// by its own viewBox when it has one.
	tile := raster.Concat(raster.Translate(x, y), ctm)
	tile = raster.Concat(transform(r.inherited(n, "patternTransform", 0)), tile)
	inner := tile
	// The content of a pattern is not inside the shape that referred to it,
	// so it inherits nothing from it: a shape stroked black must not put a
	// black edge around every tile.
	sub := initialState(w, h)
	if vb := numbers(r.inherited(n, "viewBox", 0)); len(vb) == 4 && vb[2] > 0 && vb[3] > 0 {
		al, sl := aspect(r.inherited(n, "preserveAspectRatio", 0))
		inner = raster.Concat(viewport(vb, al, sl, w, h), tile)
		sub.vw, sub.vh = vb[2], vb[3]
	} else if strings.TrimSpace(r.inherited(n, "patternContentUnits", 0)) == "objectBoundingBox" {
		inner = raster.Concat(raster.Scale(box.X1-box.X0, box.Y1-box.Y0), tile)
	}

	area, ok := tile.UnapplyRect(r.clipBox(box, ctm))
	if !ok {
		return false
	}
	view := raster.Rect{X1: w, Y1: h}
	r.depth++
	defer func() { r.depth-- }()

	cached := r.dev.BeginTile(area, view, w, h, tile) != 0
	x0, y0, x1, y1 := 0, 0, 1, 1
	if !cached {
		x0, y0, x1, y1 = tileRange(area, view, w, h)
	}
	var cell raster.Path
	cell.Rect(0, 0, w, h)
	for ty := y0; ty < y1; ty++ {
		for tx := x0; tx < x1; tx++ {
			at := raster.Translate(float32(tx)*w, float32(ty)*h)
			r.dev.ClipPath(&cell, false, raster.Concat(at, tile), raster.InfiniteRect)
			r.children(content, raster.Concat(at, inner), sub)
			r.dev.PopClip()
		}
	}
	r.dev.EndTile()
	return true
}

// clipBox is the area of the drawing a pattern has to cover, which is the
// shape's box in device space.
func (r *runner) clipBox(box raster.Rect, ctm raster.Matrix) raster.Rect {
	return ctm.ApplyRect(box)
}

// tileRange is which repetitions of a cell the area reaches.
func tileRange(area, view raster.Rect, xstep, ystep float32) (x0, y0, x1, y1 int) {
	if xstep <= 0 || ystep <= 0 {
		return 0, 0, 1, 1
	}
	x0 = int(floor32((area.X0 - view.X0) / xstep))
	y0 = int(floor32((area.Y0 - view.Y0) / ystep))
	x1 = int(ceil32((area.X1 - view.X0) / xstep))
	y1 = int(ceil32((area.Y1 - view.Y0) / ystep))
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	// A pattern whose cell is a fraction of a pixel would repeat forever.
	if n := (x1 - x0) * (y1 - y0); n > maxTiles || n < 0 {
		return 0, 0, 1, 1
	}
	return x0, y0, x1, y1
}

// maxTiles bounds how many repetitions one pattern may draw.
const maxTiles = 1 << 14

func floor32(v float32) float32 {
	i := float32(int(v))
	if v < 0 && i != v {
		i--
	}
	return i
}

func ceil32(v float32) float32 {
	i := float32(int(v))
	if v > 0 && i != v {
		i++
	}
	return i
}
