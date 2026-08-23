package svg

import (
	"math"

	"github.com/gen2brain/folio/raster"
)

const sqrt2 = math.Sqrt2

func sqrt64(v float64) float64 { return math.Sqrt(v) }

// kappa is how far a cubic's control point reaches to hold a quarter of a
// circle, which is 4/3 times the tangent of an eighth of a turn.
const kappa = 0.5522847498307933

// rectPath is a rect, with the rounded corners rx and ry ask for. SVG 1.1
// 9.2 says a rounded corner is a quarter of an ellipse and that one radius
// given alone is both of them.
func rectPath(n *node, st state) *raster.Path {
	x, _ := length(n.attr["x"], st.vw, st.em)
	y, _ := length(n.attr["y"], st.vh, st.em)
	w, wok := length(n.attr["width"], st.vw, st.em)
	h, hok := length(n.attr["height"], st.vh, st.em)
	if !wok || !hok || w <= 0 || h <= 0 {
		return nil
	}
	rx, rxok := positive(n.attr["rx"], st.vw, st.em)
	ry, ryok := positive(n.attr["ry"], st.vh, st.em)
	switch {
	case !rxok && !ryok:
		rx, ry = 0, 0
	case !rxok:
		rx = ry
	case !ryok:
		ry = rx
	}
	rx, ry = min(max(rx, 0), w/2), min(max(ry, 0), h/2)

	p := &raster.Path{}
	if rx == 0 || ry == 0 {
		p.Rect(x, y, w, h)
		return p
	}
	cx, cy := rx*kappa, ry*kappa
	p.MoveTo(x+rx, y)
	p.LineTo(x+w-rx, y)
	p.CurveTo(x+w-rx+cx, y, x+w, y+ry-cy, x+w, y+ry)
	p.LineTo(x+w, y+h-ry)
	p.CurveTo(x+w, y+h-ry+cy, x+w-rx+cx, y+h, x+w-rx, y+h)
	p.LineTo(x+rx, y+h)
	p.CurveTo(x+rx-cx, y+h, x, y+h-ry+cy, x, y+h-ry)
	p.LineTo(x, y+ry)
	p.CurveTo(x, y+ry-cy, x+rx-cx, y, x+rx, y)
	p.Close()
	return p
}

func circlePath(n *node, st state) *raster.Path {
	cx, _ := length(n.attr["cx"], st.vw, st.em)
	cy, _ := length(n.attr["cy"], st.vh, st.em)
	r, ok := length(n.attr["r"], diagonal(st.vw, st.vh), st.em)
	if !ok || r <= 0 {
		return nil
	}
	return ellipseAt(cx, cy, r, r)
}

func ellipsePath(n *node, st state) *raster.Path {
	cx, _ := length(n.attr["cx"], st.vw, st.em)
	cy, _ := length(n.attr["cy"], st.vh, st.em)
	rx, xok := positive(n.attr["rx"], st.vw, st.em)
	ry, yok := positive(n.attr["ry"], st.vh, st.em)
	// SVG 2 lets one radius stand for both; with neither, nothing is drawn.
	switch {
	case !xok && !yok:
		return nil
	case !xok:
		rx = ry
	case !yok:
		ry = rx
	}
	if rx <= 0 || ry <= 0 {
		return nil
	}
	return ellipseAt(cx, cy, rx, ry)
}

// positive reads a length that is no length at all when it is negative, which
// an invalid radius comes to: SVG 2 lets the other one stand for it.
func positive(s string, ref, em float32) (float32, bool) {
	v, ok := length(s, ref, em)
	if !ok || v < 0 {
		return 0, false
	}
	return v, true
}

// ellipseAt is four cubics, which is the shape every renderer draws a circle
// as and is within a thousandth of the curve.
func ellipseAt(cx, cy, rx, ry float32) *raster.Path {
	ox, oy := rx*kappa, ry*kappa
	p := &raster.Path{}
	p.MoveTo(cx+rx, cy)
	p.CurveTo(cx+rx, cy+oy, cx+ox, cy+ry, cx, cy+ry)
	p.CurveTo(cx-ox, cy+ry, cx-rx, cy+oy, cx-rx, cy)
	p.CurveTo(cx-rx, cy-oy, cx-ox, cy-ry, cx, cy-ry)
	p.CurveTo(cx+ox, cy-ry, cx+rx, cy-oy, cx+rx, cy)
	p.Close()
	return p
}

func linePath(n *node, st state) *raster.Path {
	x1, _ := length(n.attr["x1"], st.vw, st.em)
	y1, _ := length(n.attr["y1"], st.vh, st.em)
	x2, _ := length(n.attr["x2"], st.vw, st.em)
	y2, _ := length(n.attr["y2"], st.vh, st.em)
	p := &raster.Path{}
	p.MoveTo(x1, y1)
	p.LineTo(x2, y2)
	return p
}

// polyPath is a polyline or a polygon, which differ only in whether the last
// point joins the first. A list with an odd number of coordinates keeps the
// pairs it has, SVG 1.1 9.6.
func polyPath(n *node, closed bool) *raster.Path {
	v := numbers(n.attr["points"])
	if len(v) < 4 {
		return nil
	}
	p := &raster.Path{}
	p.MoveTo(v[0], v[1])
	for i := 2; i+1 < len(v); i += 2 {
		p.LineTo(v[i], v[i+1])
	}
	if closed {
		p.Close()
	}
	return p
}
