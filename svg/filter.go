package svg

import (
	"math"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// maxFilterArea bounds the samples one filter region may allocate.
const maxFilterArea = 1 << 24

// irect is a rectangle of samples inside a filter region.
type irect struct{ x0, y0, x1, y1 int }

// fimage is one primitive's result: a pixmap the size of the filter region,
// the space its samples are in, and the part of it that holds anything.
type fimage struct {
	px  *raster.Pixmap
	lin bool
	sub irect
}

// filtered draws an element through the filters it names and reports whether
// it dealt with the element. A reference that resolves to nothing takes the
// element off the page, SVG 1.1 15.7.
func (r *runner) filtered(n *node, ctm raster.Matrix, st state, alpha float32) bool {
	return r.filterWith(n, ctm, st, alpha, r.paint)
}

// filterWith is filtered with the drawing named. The root element is a
// viewport rather than a shape and passes its children.
func (r *runner) filterWith(n *node, ctm raster.Matrix, st state, alpha float32,
	draw func(*node, raster.Matrix, state)) bool {

	v := strings.TrimSpace(r.prop(n, "filter"))
	if v == "" || v == "none" || r.depth >= maxNesting {
		return v != "" && v != "none"
	}
	r.depth++
	defer func() { r.depth-- }()

	box := r.shapeBounds(n, st)
	chain, ok := r.chain(v, box, st)
	if !ok {
		return true
	}
	if len(chain) == 0 {
		return false
	}
	reg := raster.EmptyRect
	for _, l := range chain {
		reg = reg.Union(l.reg)
	}
	// A filter is a grid of samples over the user's own axes. Where the
	// element is only scaled and moved those are the device's; a rotation or
	// a skew is split off and put back afterwards.
	inner, back := filterSpace(ctm)
	x0, y0, x1, y1 := inner.ApplyRect(reg).Outer()
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return true
	}
	if w*h > maxFilterArea {
		return false
	}
	src := raster.NewPixmap(gfx.DeviceRGB.Model(), w, h, true)
	if src == nil {
		return false
	}
	src.X, src.Y = x0, y0

	dev := gfx.NewDrawDevice(src)
	outer := r.dev
	r.dev = dev
	draw(n, inner, st)
	r.dev = outer
	dev.Close()

	var out *raster.Pixmap
	for _, l := range chain {
		p := &pipe{r: r, f: l.f, src: src, reg: l.reg, box: box, ctm: inner, st: st, pobj: l.pobj}
		p.sx, p.sy = scaleOf(inner)
		p.full = irect{0, 0, w, h}
		if out = p.run(l.prims); out == nil {
			return true
		}
		src = out
	}
	place := raster.Matrix{A: float32(w), D: float32(h), E: float32(x0), F: float32(y0)}
	r.dev.FillImage(gfx.NewPicture(out), raster.Concat(place, back), alpha, gfx.ColorParams{})
	return true
}

// filterSpace splits a transform into the scale the filter's samples are laid
// out on and whatever turn is left over. One with no rotation and no skew in it
// keeps the whole thing, and nothing is resampled.
func filterSpace(ctm raster.Matrix) (inner, back raster.Matrix) {
	if ctm.B == 0 && ctm.C == 0 {
		return ctm, raster.Identity
	}
	sx, sy := scaleOf(ctm)
	if sx <= 0 || sy <= 0 {
		return ctm, raster.Identity
	}
	scale := raster.Scale(sx, sy)
	inv, ok := scale.Invert()
	if !ok {
		return ctm, raster.Identity
	}
	return scale, raster.Concat(inv, ctm)
}

// link is one entry of a filter property: a filter element, or the primitives
// one of the shorthand functions stands for.
type link struct {
	f     *node
	prims []*node
	reg   raster.Rect
	pobj  bool
}

// chain reads the filter property: false for a reference that names nothing,
// and an empty chain for a value that is no filter at all.
func (r *runner) chain(v string, box raster.Rect, st state) ([]link, bool) {
	var out []link
	bad := false
	for _, e := range filterEntries(v) {
		if id := serverID(e); id != "" {
			l, ok := r.filterLink(id, box, st)
			if !ok {
				bad = true
				continue
			}
			out = append(out, l)
			continue
		}
		prims := filterFuncs(e, st)
		if len(prims) == 0 {
			// Filter Effects 1 4.1: one malformed function drops the list.
			return nil, true
		}
		reg, ok := r.filterRegion(nil, box, true, st)
		if !ok {
			return nil, false
		}
		if m := funcMargin(prims); m > 0 {
			reg = raster.Rect{X0: reg.X0 - m, Y0: reg.Y0 - m, X1: reg.X1 + m, Y1: reg.Y1 + m}
		}
		out = append(out, link{prims: prims, reg: reg})
	}
	if len(out) == 0 && bad {
		return nil, false
	}
	return out, true
}

func (r *runner) filterLink(id string, box raster.Rect, st state) (link, bool) {
	f := r.doc.byID[id]
	if f == nil || f.name != "filter" {
		return link{}, false
	}
	l := link{f: f, prims: r.primitives(f, 0)}
	if len(l.prims) == 0 {
		return link{}, false
	}
	obj := true
	if u, ok := r.fattr(f, "filterUnits", 0); ok {
		obj = strings.TrimSpace(u) != "userSpaceOnUse"
	}
	if u, ok := r.fattr(f, "primitiveUnits", 0); ok {
		l.pobj = strings.TrimSpace(u) == "objectBoundingBox"
	}
	reg, ok := r.filterRegion(f, box, obj, st)
	if !ok {
		return link{}, false
	}
	l.reg = reg
	return l, true
}

// funcMargin is how far past the element the shorthand spreads. A filter
// element declares a region and a function does not.
func funcMargin(prims []*node) float32 {
	m := float32(0)
	for _, k := range prims {
		dx, dy := deviation(k.attr["stdDeviation"])
		switch k.name {
		case "feGaussianBlur":
			m += 3 * max(dx, dy)
		case "feDropShadow":
			m += 3*max(dx, dy) + max(absf32(number(k.attr["dx"], 2)), absf32(number(k.attr["dy"], 2)))
		}
	}
	return m
}

func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// filterEntries splits a filter property into the references and the
// functions it lists, keeping each one's parentheses whole.
func filterEntries(v string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				out = append(out, strings.TrimSpace(v[start:i+1]))
				start = i + 1
			}
		}
	}
	return out
}

// filterRegion is the box the filter runs over, in user space.
func (r *runner) filterRegion(f *node, box raster.Rect, obj bool, st state) (raster.Rect, bool) {
	if obj {
		if box.IsEmpty() {
			return raster.Rect{}, false
		}
		bw, bh := box.X1-box.X0, box.Y1-box.Y0
		x := r.flen(f, "x", 1, st, -0.1)
		y := r.flen(f, "y", 1, st, -0.1)
		w := r.flen(f, "width", 1, st, 1.2)
		h := r.flen(f, "height", 1, st, 1.2)
		if w <= 0 || h <= 0 {
			return raster.Rect{}, false
		}
		return raster.Rect{
			X0: box.X0 + x*bw, Y0: box.Y0 + y*bh,
			X1: box.X0 + (x+w)*bw, Y1: box.Y0 + (y+h)*bh,
		}, true
	}
	x := r.flen(f, "x", st.vw, st, -0.1*st.vw)
	y := r.flen(f, "y", st.vh, st, -0.1*st.vh)
	w := r.flen(f, "width", st.vw, st, 1.2*st.vw)
	h := r.flen(f, "height", st.vh, st, 1.2*st.vh)
	if w <= 0 || h <= 0 {
		return raster.Rect{}, false
	}
	return raster.Rect{X0: x, Y0: y, X1: x + w, Y1: y + h}, true
}

// fattr reads an attribute off a filter, following href to the filter it was
// written as a variation of, SVG 1.1 15.7.2.
func (r *runner) fattr(n *node, name string, depth int) (string, bool) {
	if n == nil {
		return "", false
	}
	if v, ok := n.attr[name]; ok {
		return v, true
	}
	if depth >= maxNesting {
		return "", false
	}
	if p := r.doc.byID[fragment(n.attr["href"])]; p != nil && p != n && p.name == "filter" {
		return r.fattr(p, name, depth+1)
	}
	return "", false
}

func (r *runner) flen(f *node, name string, ref float32, st state, def float32) float32 {
	if s, ok := r.fattr(f, name, 0); ok {
		if v, ok := length(s, ref, st.em); ok {
			return v
		}
	}
	return def
}

// scaleOf is how much a matrix stretches each axis.
func scaleOf(m raster.Matrix) (float32, float32) {
	return float32(math.Hypot(float64(m.A), float64(m.B))),
		float32(math.Hypot(float64(m.C), float64(m.D)))
}

// filterFuncs turns the shorthand of Filter Effects 1 4.1 into the primitives
// each function stands for. They chain, and they work in sRGB.
func filterFuncs(v string, st state) []*node {
	var out []*node
	for _, e := range filterEntries(v) {
		i := strings.IndexByte(e, '(')
		if i < 0 || !strings.HasSuffix(e, ")") {
			return nil
		}
		k := filterFunc(strings.ToLower(strings.TrimSpace(e[:i])),
			strings.TrimSpace(e[i+1:len(e)-1]), st)
		if k == nil {
			return nil
		}
		k.attr["color-interpolation-filters"] = "sRGB"
		out = append(out, k)
	}
	return out
}

func filterFunc(name, args string, st state) *node {
	fe := func(n string, kv ...string) *node {
		a := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			a[kv[i]] = kv[i+1]
		}
		return &node{name: n, attr: a}
	}
	transfer := func(kind string, kv ...string) *node {
		n := fe("feComponentTransfer")
		for _, c := range []string{"feFuncR", "feFuncG", "feFuncB"} {
			n.kids = append(n.kids, fe(c, append([]string{"type", kind}, kv...)...))
		}
		return n
	}
	if badAmount(args) && name != "drop-shadow" && name != "hue-rotate" && name != "blur" {
		return nil
	}
	switch name {
	case "blur":
		if strings.Contains(args, "%") {
			return nil
		}
		d, ok := length(args, st.vw, st.em)
		if !ok && strings.TrimSpace(args) != "" {
			return nil
		}
		return fe("feGaussianBlur", "stdDeviation", fnum(max(d, 0)))
	case "brightness":
		return transfer("linear", "slope", fnum(amount(args, false)))
	case "contrast":
		v := amount(args, false)
		return transfer("linear", "slope", fnum(v), "intercept", fnum(0.5-0.5*v))
	case "invert":
		v := amount(args, true)
		return transfer("table", "tableValues", fnum(v)+" "+fnum(1-v))
	case "opacity":
		v := amount(args, true)
		n := fe("feComponentTransfer")
		n.kids = append(n.kids, fe("feFuncA", "type", "table", "tableValues", "0 "+fnum(v)))
		return n
	case "grayscale":
		return fe("feColorMatrix", "values", mix(grayMatrix, 1-amount(args, true)))
	case "sepia":
		return fe("feColorMatrix", "values", mix(sepiaMatrix, 1-amount(args, true)))
	case "saturate":
		return fe("feColorMatrix", "type", "saturate", "values", fnum(max(amount(args, false), 0)))
	case "hue-rotate":
		a, ok := angle(args)
		if !ok {
			return nil
		}
		return fe("feColorMatrix", "type", "hueRotate", "values", fnum(a))
	case "drop-shadow":
		return dropShadowFunc(args, st)
	}
	return nil
}

// grayMatrix and sepiaMatrix are interpolated towards the identity by the
// amount the function asks for.
var (
	grayMatrix = [3][4]float32{
		{0.2126, 0.7152, 0.0722, 0.7874},
		{0.2126, 0.7152, 0.0722, 0.2848},
		{0.2126, 0.7152, 0.0722, 0.9278},
	}
	sepiaMatrix = [3][4]float32{
		{0.393, 0.769, 0.189, 0.607},
		{0.349, 0.686, 0.168, 0.314},
		{0.272, 0.534, 0.131, 0.869},
	}
)

func mix(m [3][4]float32, t float32) string {
	var b strings.Builder
	for i, row := range m {
		for j := range 3 {
			v := row[j]
			if i == j {
				v += row[3] * t
			} else {
				v -= row[j] * t
			}
			b.WriteString(fnum(v))
			b.WriteByte(' ')
		}
		b.WriteString("0 0 ")
	}
	b.WriteString("0 0 0 1 0")
	return b.String()
}

func dropShadowFunc(args string, st state) *node {
	if strings.ContainsAny(args, ",") {
		return nil
	}
	var nums []string
	var col string
	for _, f := range splitArgs(args) {
		if _, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(f), "px"), 32); err == nil {
			nums = append(nums, strings.TrimSpace(f))
			continue
		}
		if _, ok := length(f, st.vw, st.em); ok && len(nums) < 3 {
			nums = append(nums, strings.TrimSpace(f))
			continue
		}
		col = strings.TrimSpace(f)
	}
	if len(nums) < 2 || len(nums) > 3 {
		return nil
	}
	dx, _ := length(nums[0], st.vw, st.em)
	dy, _ := length(nums[1], st.vh, st.em)
	sd := float32(0)
	if len(nums) > 2 {
		sd, _ = length(nums[2], st.vw, st.em)
	}
	a := map[string]string{"dx": fnum(dx), "dy": fnum(dy), "stdDeviation": fnum(max(sd, 0))}
	if col != "" {
		a["flood-color"] = col
	} else {
		a["flood-color"] = "currentColor"
	}
	return &node{name: "feDropShadow", attr: a}
}

// amount reads a number or a percentage, clamped above at one for the
// functions that say so. No argument means one.
func amount(s string, unit bool) float32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1
	}
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
	if err != nil {
		return 1
	}
	out := float32(v)
	if pct {
		out /= 100
	}
	if unit {
		out = min(out, 1)
	}
	return out
}

// badAmount is an argument that is not a number at all, or is negative, which
// none of these functions takes.
func badAmount(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
	return err != nil || v < 0
}

// angle reads the four forms CSS writes a rotation in, in degrees. Only zero
// may be written without a unit.
func angle(s string) (float32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	scale, unit := float32(1), true
	switch {
	case strings.HasSuffix(s, "deg"):
		s = s[:len(s)-3]
	case strings.HasSuffix(s, "grad"):
		s, scale = s[:len(s)-4], 0.9
	case strings.HasSuffix(s, "rad"):
		s, scale = s[:len(s)-3], 180/math.Pi
	case strings.HasSuffix(s, "turn"):
		s, scale = s[:len(s)-4], 360
	default:
		unit = false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 32)
	if err != nil || (!unit && v != 0) {
		return 0, false
	}
	return float32(v) * scale, true
}

// splitArgs breaks arguments at the spaces and commas between them, leaving a
// colour written as a function of its own whole.
func splitArgs(v string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) {
			if start < i {
				out = append(out, v[start:i])
			}
			break
		}
		switch c := v[i]; {
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == ' ' || c == ',' || c == '\t' || c == '\n' || c == '\r'):
			if start < i {
				out = append(out, v[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func fnum(v float32) string {
	return strconv.FormatFloat(float64(v), 'g', -1, 32)
}
