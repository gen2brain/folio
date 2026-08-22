package svg

import (
	"math"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// fresult is what one primitive produced, under the name it gave it.
type fresult struct {
	name string
	img  *fimage
}

// pipe is one run of a filter's primitives over one element's rendering.
type pipe struct {
	r      *runner
	src    *raster.Pixmap
	alpha  *raster.Pixmap
	reg    raster.Rect
	box    raster.Rect
	ctm    raster.Matrix
	st     state
	f      *node
	pobj   bool
	sx, sy float32
	full   irect
	res    []fresult
}

// run applies every primitive in turn and hands back the last result in sRGB,
// or nil for a filter with nothing in it.
func (p *pipe) run(kids []*node) *raster.Pixmap {
	var last *fimage
	for _, k := range kids {
		lin := p.space(k)
		var out *fimage
		switch k.name {
		case "feFlood":
			out = p.flood(k)
		case "feGaussianBlur":
			out = p.blur(k, lin)
		case "feOffset":
			out = p.offset(k)
		case "feBlend":
			out = p.blend(k, lin)
		case "feComposite":
			out = p.composite(k, lin)
		case "feMerge":
			out = p.merge(k, lin)
		case "feColorMatrix":
			out = p.colorMatrix(k, lin)
		case "feComponentTransfer":
			out = p.componentTransfer(k, lin)
		case "feDropShadow":
			out = p.dropShadow(k, lin)
		case "feMorphology":
			out = p.morphology(k, lin)
		case "feTile":
			out = p.tile(k)
		case "feTurbulence":
			out = p.turbulence(k, lin)
		case "feConvolveMatrix":
			out = p.convolve(k, lin)
		case "feDisplacementMap":
			out = p.displacement(k, lin)
		case "feImage":
			out = p.feImage(k, p.subregionRaw(k))
		case "feDiffuseLighting":
			out = p.lighting(k, lin, false)
		case "feSpecularLighting":
			out = p.lighting(k, lin, true)
		default:
			if !strings.HasPrefix(k.name, "fe") {
				continue
			}
			out = p.blank(lin)
		}
		if out == nil {
			continue
		}
		if k.name != "feOffset" {
			if sub := p.subregion(k); sub != p.full {
				clearOutside(out.px, sub)
				out.sub = sub
			}
		}
		p.res = append(p.res, fresult{name: strings.TrimSpace(k.attr["result"]), img: out})
		last = out
	}
	if last == nil {
		return nil
	}
	if last.lin {
		px := clonePixmap(last.px)
		convertSpace(px, false)
		return px
	}
	return last.px
}

// primitives are the fe elements a filter draws through, taken from the
// filter it refers to when it declares none of its own.
func (r *runner) primitives(f *node, depth int) []*node {
	var out []*node
	for _, k := range f.kids {
		if strings.HasPrefix(k.name, "fe") {
			out = append(out, k)
		}
	}
	if len(out) > 0 || depth >= maxNesting {
		return out
	}
	if p := r.doc.byID[fragment(f.attr["href"])]; p != nil && p != f && p.name == "filter" {
		return r.primitives(p, depth+1)
	}
	return nil
}

// space is the color space a primitive works in, linear unless it says
// otherwise.
func (p *pipe) space(k *node) bool {
	v := strings.TrimSpace(p.r.prop(k, "color-interpolation-filters"))
	if (v == "" || v == "auto" || v == "inherit") && p.f != nil {
		v = strings.TrimSpace(p.r.prop(p.f, "color-interpolation-filters"))
	}
	return v != "sRGB"
}

// subregion is the part of the region a primitive is allowed to write.
func (p *pipe) subregion(k *node) irect {
	return clampRect(p.subregionRaw(k), p.full)
}

// subregionRaw is the same before it is cut down to the region.
func (p *pipe) subregionRaw(k *node) irect {
	x, xok := p.plen(k, "x")
	y, yok := p.plen(k, "y")
	w, wok := p.plen(k, "width")
	h, hok := p.plen(k, "height")
	if !xok && !yok && !wok && !hok {
		return p.full
	}
	if p.pobj && p.box.IsEmpty() {
		return p.full
	}
	u := p.reg
	if xok {
		u.X0 = x
	}
	if yok {
		u.Y0 = y
	}
	u.X1, u.Y1 = u.X0+(p.reg.X1-p.reg.X0), u.Y0+(p.reg.Y1-p.reg.Y0)
	if wok {
		u.X1 = u.X0 + w
	}
	if hok {
		u.Y1 = u.Y0 + h
	}
	x0, y0, x1, y1 := p.ctm.ApplyRect(u).Outer()
	return irect{x0 - p.src.X, y0 - p.src.Y, x1 - p.src.X, y1 - p.src.Y}
}

func (p *pipe) plen(k *node, name string) (float32, bool) {
	s, ok := k.attr[name]
	if !ok {
		return 0, false
	}
	if p.pobj {
		if p.box.IsEmpty() {
			return 0, false
		}
		v, ok := length(s, 1, p.st.em)
		if !ok {
			return 0, false
		}
		bw, bh := p.box.X1-p.box.X0, p.box.Y1-p.box.Y0
		switch name {
		case "x":
			return p.box.X0 + v*bw, true
		case "y":
			return p.box.Y0 + v*bh, true
		case "width":
			return v * bw, true
		}
		return v * bh, true
	}
	ref := p.st.vh
	if name == "x" || name == "width" {
		ref = p.st.vw
	}
	return length(s, ref, p.st.em)
}

// input is what a primitive reads: the previous result when it names none,
// and the element's own rendering when there is no previous one.
func (p *pipe) input(k *node, attr string) *fimage {
	return p.named(strings.TrimSpace(k.attr[attr]))
}

func (p *pipe) named(v string) *fimage {
	switch v {
	case "SourceGraphic", "BackgroundImage", "FillPaint", "StrokePaint":
		return &fimage{px: p.src, sub: p.full}
	case "SourceAlpha", "BackgroundAlpha":
		if p.alpha == nil {
			p.alpha = clonePixmap(p.src)
			for i := 0; i+4 <= len(p.alpha.Samples); i += 4 {
				p.alpha.Samples[i], p.alpha.Samples[i+1], p.alpha.Samples[i+2] = 0, 0, 0
			}
		}
		return &fimage{px: p.alpha, sub: p.full}
	case "":
	default:
		for i := len(p.res) - 1; i >= 0; i-- {
			if p.res[i].name == v {
				return p.res[i].img
			}
		}
	}
	if len(p.res) > 0 {
		return p.res[len(p.res)-1].img
	}
	return &fimage{px: p.src, sub: p.full}
}

// take is a private copy of an image in the space asked for.
func (i *fimage) take(lin bool) *raster.Pixmap {
	px := clonePixmap(i.px)
	if lin != i.lin {
		convertSpace(px, lin)
	}
	return px
}

func (p *pipe) blank(lin bool) *fimage {
	px := raster.NewPixmap(gfx.DeviceRGB.Model(), p.src.W, p.src.H, true)
	if px == nil {
		return nil
	}
	px.X, px.Y = p.src.X, p.src.Y
	return &fimage{px: px, lin: lin, sub: p.full}
}

func (p *pipe) flood(k *node) *fimage {
	out := p.blank(false)
	if out == nil {
		return nil
	}
	c, a := p.floodOf(k)
	s := out.px.Samples
	r8, g8, b8, a8 := premul(c, a)
	for i := 0; i+4 <= len(s); i += 4 {
		s[i], s[i+1], s[i+2], s[i+3] = r8, g8, b8, a8
	}
	return out
}

// current is what currentColor stands for inside a filter: the colour on the
// primitive, then on the filter element.
func (p *pipe) current(k *node) paint {
	for _, n := range []*node{k, p.f} {
		if n == nil {
			continue
		}
		if v, ok := parseColor(p.r.prop(n, "color"), p.st.color); ok {
			return v
		}
	}
	return p.st.color
}

func (p *pipe) floodOf(k *node) ([3]float32, float32) {
	c := black
	if v, ok := parseColor(p.inherit(k, "flood-color"), p.current(k)); ok {
		c = v
	}
	a := c.alpha
	if v, ok := opacity(p.inherit(k, "flood-opacity")); ok {
		a *= v
	}
	return c.color, a
}

// inherit reads a property off a primitive, and off the filter around it when
// the primitive asks for what it inherits.
func (p *pipe) inherit(k *node, name string) string {
	v := strings.TrimSpace(p.r.prop(k, name))
	if v == "inherit" && p.f != nil {
		return strings.TrimSpace(p.r.prop(p.f, name))
	}
	return v
}

func (p *pipe) blur(k *node, lin bool) *fimage {
	in := p.input(k, "in")
	px := in.take(lin)
	dx, dy := deviation(k.attr["stdDeviation"])
	bx, by := p.unit()
	raster.Blur(px, dx*bx*p.sx, dy*by*p.sy)
	return &fimage{px: px, lin: lin, sub: p.full}
}

func (p *pipe) offset(k *node) *fimage {
	in := p.input(k, "in")
	out := p.blank(in.lin)
	if out == nil {
		return nil
	}
	at := *in.px
	dx, dy := p.shift(k, 0)
	at.X += int(dx)
	at.Y += int(dy)
	out.px.BlendOver(&at, 255, raster.BlendNormal)
	out.sub = in.sub
	return out
}

// shift is a dx and dy pair in samples, which primitiveUnits may make a
// fraction of the element's box.
func (p *pipe) shift(k *node, def float32) (float32, float32) {
	dx, dy := number(k.attr["dx"], def), number(k.attr["dy"], def)
	bx, by := p.unit()
	return dx * bx * p.sx, dy * by * p.sy
}

// unit is what a length in primitiveUnits measures against.
func (p *pipe) unit() (float32, float32) {
	if p.pobj && !p.box.IsEmpty() {
		return p.box.X1 - p.box.X0, p.box.Y1 - p.box.Y0
	}
	return 1, 1
}

func (p *pipe) blend(k *node, lin bool) *fimage {
	over := p.input(k, "in").take(lin)
	under := p.input(k, "in2").take(lin)
	mode, _ := blendMode(k.attr["mode"])
	under.BlendOver(over, 255, mode)
	return &fimage{px: under, lin: lin, sub: p.full}
}

func (p *pipe) merge(k *node, lin bool) *fimage {
	out := p.blank(lin)
	if out == nil {
		return nil
	}
	for _, c := range k.kids {
		if c.name != "feMergeNode" {
			continue
		}
		out.px.BlendOver(p.named(strings.TrimSpace(c.attr["in"])).take(lin), 255, raster.BlendNormal)
	}
	return out
}

func (p *pipe) dropShadow(k *node, lin bool) *fimage {
	in := p.input(k, "in").take(lin)
	shadow := clonePixmap(in)
	sdx, sdy := deviation(k.attr["stdDeviation"])
	bx, by := p.unit()
	raster.Blur(shadow, sdx*bx*p.sx, sdy*by*p.sy)

	c, a := p.floodOf(k)
	r8, g8, b8, a8 := premul(c, a)
	s := shadow.Samples
	for i := 0; i+4 <= len(s); i += 4 {
		cov := uint32(s[i+3])
		s[i] = mul255(r8, cov)
		s[i+1] = mul255(g8, cov)
		s[i+2] = mul255(b8, cov)
		s[i+3] = mul255(a8, cov)
	}
	if lin {
		convertSpace(shadow, true)
	}
	out := p.blank(lin)
	if out == nil {
		return nil
	}
	at := *shadow
	dx, dy := p.shift(k, 2)
	at.X += int(dx)
	at.Y += int(dy)
	out.px.BlendOver(&at, 255, raster.BlendNormal)
	out.px.BlendOver(in, 255, raster.BlendNormal)
	return out
}

func (p *pipe) morphology(k *node, lin bool) *fimage {
	in := p.input(k, "in").take(lin)
	rx, ry := radius(k.attr["radius"])
	bx, by := p.unit()
	rx, ry = rx*bx*p.sx, ry*by*p.sy
	if rx <= 0 || ry <= 0 {
		return &fimage{px: in, lin: lin, sub: p.full}
	}
	erode := strings.TrimSpace(k.attr["operator"]) != "dilate"
	morph(in, rx, ry, erode)
	return &fimage{px: in, lin: lin, sub: p.full}
}

func (p *pipe) tile(k *node) *fimage {
	in := p.input(k, "in")
	s := in.sub
	if s.x0 >= s.x1 || s.y0 >= s.y1 {
		return p.blank(in.lin)
	}
	out := p.blank(in.lin)
	if out == nil {
		return nil
	}
	tw, th := s.x1-s.x0, s.y1-s.y0
	for y := 0; y < out.px.H; y++ {
		sy := s.y0 + wrap(y-s.y0, th)
		row := in.px.Samples[sy*in.px.Stride:]
		dst := out.px.Samples[y*out.px.Stride:]
		for x := 0; x < out.px.W; x++ {
			sx := s.x0 + wrap(x-s.x0, tw)
			copy(dst[x*4:x*4+4], row[sx*4:sx*4+4])
		}
	}
	return out
}

func wrap(v, n int) int {
	v %= n
	if v < 0 {
		v += n
	}
	return v
}

// deviation reads stdDeviation: a number, or one per axis, or nothing.
func deviation(s string) (float32, float32) {
	x, y := pair(s)
	return max(x, 0), max(y, 0)
}

func radius(s string) (float32, float32) { return pair(s) }

func pair(s string) (float32, float32) {
	switch v := numberList(s); len(v) {
	case 1:
		return v[0], v[0]
	case 2:
		return v[0], v[1]
	}
	return 0, 0
}

// number reads an attribute written as a plain number, with no unit and no
// percentage.
func number(s string, def float32) float32 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 32); err == nil {
		return float32(v)
	}
	return def
}

func clampRect(r, to irect) irect {
	r.x0 = max(r.x0, to.x0)
	r.y0 = max(r.y0, to.y0)
	r.x1 = min(r.x1, to.x1)
	r.y1 = min(r.y1, to.y1)
	if r.x1 < r.x0 {
		r.x1 = r.x0
	}
	if r.y1 < r.y0 {
		r.y1 = r.y0
	}
	return r
}

func clearOutside(px *raster.Pixmap, keep irect) {
	for y := 0; y < px.H; y++ {
		row := px.Samples[y*px.Stride : y*px.Stride+px.W*4]
		if y < keep.y0 || y >= keep.y1 {
			clear(row)
			continue
		}
		clear(row[:keep.x0*4])
		clear(row[keep.x1*4:])
	}
}

// px4 is one pixel of a four component pixmap.
func px4(p *raster.Pixmap, x, y int) []uint8 { return p.Samples[y*p.Stride+x*4:] }

func clonePixmap(p *raster.Pixmap) *raster.Pixmap {
	out := *p
	out.Samples = make([]uint8, len(p.Samples))
	copy(out.Samples, p.Samples)
	return &out
}

func premul(c [3]float32, a float32) (uint8, uint8, uint8, uint8) {
	a8 := round8(a)
	return mul255(round8(c[0]), uint32(a8)), mul255(round8(c[1]), uint32(a8)),
		mul255(round8(c[2]), uint32(a8)), a8
}

func round8(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

func mul255(v uint8, a uint32) uint8 {
	x := uint32(v)*a + 128
	return uint8((x + x>>8) >> 8)
}

// srgbOf and linearOf are the two directions of the sRGB transfer function.
var srgbOf, linearOf [256]uint8

func init() {
	for i := range 256 {
		c := float64(i) / 255
		var lin float64
		if c <= 0.04045 {
			lin = c / 12.92
		} else {
			lin = math.Pow((c+0.055)/1.055, 2.4)
		}
		linearOf[i] = uint8(math.Floor(lin*255 + 0.5))
		var srgb float64
		if c <= 0.0031308 {
			srgb = c * 12.92
		} else {
			srgb = 1.055*math.Pow(c, 1/2.4) - 0.055
		}
		srgbOf[i] = uint8(math.Floor(srgb*255 + 0.5))
	}
}

// convertSpace moves a premultiplied pixmap into linear light or back, with
// the transfer function applied outside the multiply.
func convertSpace(px *raster.Pixmap, lin bool) {
	tab := &srgbOf
	if lin {
		tab = &linearOf
	}
	s := px.Samples
	for i := 0; i+4 <= len(s); i += 4 {
		a := s[i+3]
		if a == 0 {
			s[i], s[i+1], s[i+2] = 0, 0, 0
			continue
		}
		if a == 255 {
			s[i], s[i+1], s[i+2] = tab[s[i]], tab[s[i+1]], tab[s[i+2]]
			continue
		}
		s[i] = mul255(tab[unmul(s[i], a)], uint32(a))
		s[i+1] = mul255(tab[unmul(s[i+1], a)], uint32(a))
		s[i+2] = mul255(tab[unmul(s[i+2], a)], uint32(a))
	}
}

func unmul(v, a uint8) uint8 {
	if a == 0 {
		return 0
	}
	x := (uint32(v)*255 + uint32(a)/2) / uint32(a)
	if x > 255 {
		return 255
	}
	return uint8(x)
}

func (p *pipe) composite(k *node, lin bool) *fimage {
	over := p.input(k, "in").take(lin)
	under := p.input(k, "in2").take(lin)
	switch strings.TrimSpace(k.attr["operator"]) {
	case "in":
		porter(over, under, 1)
	case "out":
		porter(over, under, 2)
	case "atop":
		porter(over, under, 3)
	case "xor":
		porter(over, under, 4)
	case "arithmetic":
		arithmetic(over, under, number(k.attr["k1"], 0), number(k.attr["k2"], 0),
			number(k.attr["k3"], 0), number(k.attr["k4"], 0))
	default:
		under.BlendOver(over, 255, raster.BlendNormal)
		return &fimage{px: under, lin: lin, sub: p.full}
	}
	return &fimage{px: over, lin: lin, sub: p.full}
}

// porter composites src onto dst by one of the Porter-Duff operators and
// leaves the answer in src. Both are premultiplied.
func porter(src, dst *raster.Pixmap, op int) {
	s, d := src.Samples, dst.Samples
	for i := 0; i+4 <= len(s) && i+4 <= len(d); i += 4 {
		sa, da := uint32(s[i+3]), uint32(d[i+3])
		var ka, kb uint32
		switch op {
		case 1:
			ka, kb = da, 0
		case 2:
			ka, kb = 255-da, 0
		case 3:
			ka, kb = da, 255-sa
		default:
			ka, kb = 255-da, 255-sa
		}
		for c := range 4 {
			s[i+c] = uint8(mul255(s[i+c], ka)) + mul255(d[i+c], kb)
		}
	}
}

// arithmetic is feComposite's own operator: every channel of the two
// premultiplied inputs through k1*i1*i2 + k2*i1 + k3*i2 + k4.
func arithmetic(src, dst *raster.Pixmap, k1, k2, k3, k4 float32) {
	s, d := src.Samples, dst.Samples
	calc := func(i1, i2 uint8, hi float32) float32 {
		a, b := float32(i1)/255, float32(i2)/255
		v := k1*a*b + k2*a + k3*b + k4
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	for i := 0; i+4 <= len(s) && i+4 <= len(d); i += 4 {
		a := calc(s[i+3], d[i+3], 1)
		if a == 0 {
			s[i], s[i+1], s[i+2], s[i+3] = 0, 0, 0, 0
			continue
		}
		s[i] = uint8(calc(s[i], d[i], a) * 255)
		s[i+1] = uint8(calc(s[i+1], d[i+1], a) * 255)
		s[i+2] = uint8(calc(s[i+2], d[i+2], a) * 255)
		s[i+3] = uint8(a * 255)
	}
}

func (p *pipe) colorMatrix(k *node, lin bool) *fimage {
	px := p.input(k, "in").take(lin)
	m, ok := colorMatrixOf(k)
	if !ok {
		return &fimage{px: px, lin: lin, sub: p.full}
	}
	s := px.Samples
	var c [4]float32
	for i := 0; i+4 <= len(s); i += 4 {
		a := s[i+3]
		c[0] = float32(unmul(s[i], a)) / 255
		c[1] = float32(unmul(s[i+1], a)) / 255
		c[2] = float32(unmul(s[i+2], a)) / 255
		c[3] = float32(a) / 255
		for j := range 4 {
			v := m[j*5]*c[0] + m[j*5+1]*c[1] + m[j*5+2]*c[2] + m[j*5+3]*c[3] + m[j*5+4]
			s[i+j] = round8(v)
		}
		na := uint32(s[i+3])
		s[i] = mul255(s[i], na)
		s[i+1] = mul255(s[i+1], na)
		s[i+2] = mul255(s[i+2], na)
	}
	return &fimage{px: px, lin: lin, sub: p.full}
}

// colorMatrixOf is the twenty numbers a feColorMatrix stands for, in any of
// the four ways it is written.
func colorMatrixOf(k *node) ([20]float32, bool) {
	var m [20]float32
	v := numberList(k.attr["values"])
	switch strings.TrimSpace(k.attr["type"]) {
	case "saturate":
		s := float32(1)
		if len(v) > 0 {
			s = max(v[0], 0)
		}
		return saturateMatrix(s), true
	case "hueRotate":
		a := float64(0)
		if len(v) > 0 {
			a = float64(v[0])
		}
		return hueMatrix(a * math.Pi / 180), true
	case "luminanceToAlpha":
		m[15], m[16], m[17] = 0.2125, 0.7154, 0.0721
		return m, true
	}
	if len(v) != 20 {
		return m, false
	}
	copy(m[:], v)
	return m, true
}

func saturateMatrix(s float32) [20]float32 {
	return [20]float32{
		0.213 + 0.787*s, 0.715 - 0.715*s, 0.072 - 0.072*s, 0, 0,
		0.213 - 0.213*s, 0.715 + 0.285*s, 0.072 - 0.072*s, 0, 0,
		0.213 - 0.213*s, 0.715 - 0.715*s, 0.072 + 0.928*s, 0, 0,
		0, 0, 0, 1, 0,
	}
}

func hueMatrix(rad float64) [20]float32 {
	c := float32(math.Cos(rad))
	s := float32(math.Sin(rad))
	return [20]float32{
		0.213 + 0.787*c - 0.213*s, 0.715 - 0.715*c - 0.715*s, 0.072 - 0.072*c + 0.928*s, 0, 0,
		0.213 - 0.213*c + 0.143*s, 0.715 + 0.285*c + 0.140*s, 0.072 - 0.072*c - 0.283*s, 0, 0,
		0.213 - 0.213*c - 0.787*s, 0.715 - 0.715*c + 0.715*s, 0.072 + 0.928*c + 0.072*s, 0, 0,
		0, 0, 0, 1, 0,
	}
}

func (p *pipe) componentTransfer(k *node, lin bool) *fimage {
	px := p.input(k, "in").take(lin)
	var tab [4]*[256]uint8
	any := false
	for _, c := range k.kids {
		i := -1
		switch c.name {
		case "feFuncR":
			i = 0
		case "feFuncG":
			i = 1
		case "feFuncB":
			i = 2
		case "feFuncA":
			i = 3
		}
		if i < 0 {
			continue
		}
		if t := transferTable(c); t != nil {
			tab[i], any = t, true
		}
	}
	if !any {
		return &fimage{px: px, lin: lin, sub: p.full}
	}
	s := px.Samples
	for i := 0; i+4 <= len(s); i += 4 {
		a := s[i+3]
		var c [4]uint8
		c[0], c[1], c[2], c[3] = unmul(s[i], a), unmul(s[i+1], a), unmul(s[i+2], a), a
		for j := range 4 {
			if tab[j] != nil {
				c[j] = tab[j][c[j]]
			}
		}
		na := uint32(c[3])
		s[i], s[i+1], s[i+2], s[i+3] = mul255(c[0], na), mul255(c[1], na), mul255(c[2], na), c[3]
	}
	return &fimage{px: px, lin: lin, sub: p.full}
}

// transferTable is one feFunc as a lookup, and nil for one that changes
// nothing.
func transferTable(c *node) *[256]uint8 {
	v := numberList(c.attr["tableValues"])
	kind := strings.TrimSpace(c.attr["type"])
	var f func(float32) float32
	switch kind {
	case "table":
		if len(v) == 0 {
			return nil
		}
		if len(v) == 1 {
			f = func(float32) float32 { return v[0] }
			break
		}
		n := len(v) - 1
		f = func(x float32) float32 {
			j := min(int(x*float32(n)), n)
			if j == n {
				return v[n]
			}
			return v[j] + (x-float32(j)/float32(n))*float32(n)*(v[j+1]-v[j])
		}
	case "discrete":
		if len(v) == 0 {
			return nil
		}
		n := len(v)
		f = func(x float32) float32 { return v[min(int(x*float32(n)), n-1)] }
	case "linear":
		slope, inter := number(c.attr["slope"], 1), number(c.attr["intercept"], 0)
		f = func(x float32) float32 { return slope*x + inter }
	case "gamma":
		amp, exp := number(c.attr["amplitude"], 1), number(c.attr["exponent"], 1)
		off := number(c.attr["offset"], 0)
		f = func(x float32) float32 {
			return amp*float32(math.Pow(float64(x), float64(exp))) + off
		}
	default:
		return nil
	}
	var t [256]uint8
	for i := range 256 {
		t[i] = round8(f(float32(i) / 255))
	}
	return &t
}

// morph is feMorphology: every sample becomes the extreme of the box around
// it.
func morph(px *raster.Pixmap, rx, ry float32, erode bool) {
	cols := min(int(math.Ceil(float64(rx)))*2, px.W)
	rows := min(int(math.Ceil(float64(ry)))*2, px.H)
	if cols <= 0 || rows <= 0 {
		return
	}
	tx, ty := cols/2, rows/2
	out := make([]uint8, len(px.Samples))
	for y := 0; y < px.H; y++ {
		dst := out[y*px.Stride:]
		for x := 0; x < px.W; x++ {
			var v [4]uint8
			if erode {
				v = [4]uint8{255, 255, 255, 255}
			}
			for oy := 0; oy < rows; oy++ {
				sy := y - ty + oy
				if sy < 0 || sy >= px.H {
					continue
				}
				row := px.Samples[sy*px.Stride:]
				for ox := 0; ox < cols; ox++ {
					sx := x - tx + ox
					if sx < 0 || sx >= px.W {
						continue
					}
					s := row[sx*4:]
					for c := range 4 {
						if erode {
							v[c] = min(v[c], s[c])
						} else {
							v[c] = max(v[c], s[c])
						}
					}
				}
			}
			copy(dst[x*4:x*4+4], v[:])
		}
	}
	copy(px.Samples, out)
}

func (p *pipe) turbulence(k *node, lin bool) *fimage {
	out := p.blank(lin)
	if out == nil {
		return nil
	}
	fx, fy := pair(k.attr["baseFrequency"])
	oct := int(number(k.attr["numOctaves"], 1) + 0.5)
	if fx < 0 || fy < 0 || oct <= 0 || p.sx == 0 || p.sy == 0 {
		return out
	}
	n := newNoise(int(number(k.attr["seed"], 0)))
	fractal := strings.TrimSpace(k.attr["type"]) == "fractalNoise"
	tiled := strings.TrimSpace(k.attr["stitchTiles"]) == "stitch"
	ox := float64(p.src.X) - float64(p.ctm.E)
	oy := float64(p.src.Y) - float64(p.ctm.F)
	w, h := float64(out.px.W), float64(out.px.H)
	for y := 0; y < out.px.H; y++ {
		row := out.px.Samples[y*out.px.Stride:]
		ty := (float64(y) + oy) / float64(p.sy)
		for x := 0; x < out.px.W; x++ {
			tx := (float64(x) + ox) / float64(p.sx)
			s := row[x*4:]
			for c := range 4 {
				v := n.turbulence(c, tx, ty, float64(x), float64(y), w, h,
					float64(fx), float64(fy), min(oct, maxOctav), fractal, tiled)
				if fractal {
					v = (v*255 + 255) / 2
				} else {
					v *= 255
				}
				s[c] = round8(float32(v) / 255)
			}
			a := uint32(s[3])
			s[0], s[1], s[2] = mul255(s[0], a), mul255(s[1], a), mul255(s[2], a)
		}
	}
	return out
}

func (p *pipe) convolve(k *node, lin bool) *fimage {
	cols, rows := 3, 3
	if v := numberList(k.attr["order"]); len(v) == 1 {
		cols, rows = int(v[0]), int(v[0])
	} else if len(v) == 2 {
		cols, rows = int(v[0]), int(v[1])
	}
	m := numberList(k.attr["kernelMatrix"])
	if cols < 1 || rows < 1 || cols*rows != len(m) {
		return p.blank(lin)
	}
	div := float32(0)
	for _, v := range m {
		div += v
	}
	if div == 0 {
		div = 1
	}
	if v, ok := k.attr["divisor"]; ok {
		div = number(v, div)
		if div == 0 {
			return p.blank(lin)
		}
	}
	tx, ty := cols/2, rows/2
	if v, ok := k.attr["targetX"]; ok {
		tx = int(number(v, float32(tx)))
	}
	if v, ok := k.attr["targetY"]; ok {
		ty = int(number(v, float32(ty)))
	}
	if tx < 0 || tx >= cols || ty < 0 || ty >= rows {
		return p.blank(lin)
	}
	bias := number(k.attr["bias"], 0)
	keep := strings.TrimSpace(k.attr["preserveAlpha"]) == "true"
	edge := strings.TrimSpace(k.attr["edgeMode"])
	if edge == "" {
		edge = "duplicate"
	}

	px := p.input(k, "in").take(lin)
	if keep {
		unpremultiply(px)
	}
	out := clonePixmap(px)
	w, h := px.W, px.H
	at := func(x, y int) []uint8 {
		switch edge {
		case "none":
			if x < 0 || x >= w || y < 0 || y >= h {
				return nil
			}
		case "wrap":
			x, y = wrap(x, w), wrap(y, h)
		default:
			x, y = min(max(x, 0), w-1), min(max(y, 0), h-1)
		}
		return px.Samples[y*px.Stride+x*4:]
	}
	for y := range h {
		for x := range w {
			var sum [4]float32
			for oy := range rows {
				for ox := range cols {
					s := at(x-tx+ox, y-ty+oy)
					if s == nil {
						continue
					}
					kv := m[(rows-oy-1)*cols+(cols-ox-1)]
					for c := range 3 {
						sum[c] += float32(float32(s[c]) / 255 * kv)
					}
					if !keep {
						sum[3] += float32(float32(s[3]) / 255 * kv)
					}
				}
			}
			d := px4(out, x, y)
			a := sum[3]/div + bias
			if keep {
				a = float32(px.Samples[y*px.Stride+x*4+3]) / 255
			}
			ab := min(max(a, 0), 1)
			for c := range 3 {
				v := sum[c]/div + bias*a
				if keep {
					v = min(max(v, 0), 1) * ab
				} else {
					v = min(max(v, 0), ab)
				}
				d[c] = uint8(v*255 + 0.5)
			}
			d[3] = uint8(ab*255 + 0.5)
		}
	}
	return &fimage{px: out, lin: lin, sub: p.full}
}

func (p *pipe) displacement(k *node, lin bool) *fimage {
	src := p.input(k, "in").take(lin)
	m := p.input(k, "in2").take(lin)
	unpremultiply(m)
	out := p.blank(lin)
	if out == nil {
		return nil
	}
	scale := number(k.attr["scale"], 0)
	cx := channelOf(k.attr["xChannelSelector"])
	cy := channelOf(k.attr["yChannelSelector"])
	w, h := src.W, src.H
	for y := range h {
		for x := range w {
			d := m.Samples[y*m.Stride+x*4:]
			dx := float32(d[cx])/255 - 0.5
			dy := float32(d[cy])/255 - 0.5
			ox := x + int(math.Round(float64(dx*p.sx*scale)))
			oy := y + int(math.Round(float64(dy*p.sy*scale)))
			if ox < 0 || ox >= w || oy < 0 || oy >= h {
				continue
			}
			copy(px4(out.px, x, y)[:4], src.Samples[oy*src.Stride+ox*4:][:4])
		}
	}
	return out
}

// channelOf is which byte of a pixel a selector names, alpha by default.
func channelOf(s string) int {
	switch strings.TrimSpace(s) {
	case "R":
		return 0
	case "G":
		return 1
	case "B":
		return 2
	}
	return 3
}

// feImage draws what it refers to into its own subregion: an element of the
// drawing, or a picture beside it.
func (p *pipe) feImage(k *node, sub irect) *fimage {
	out := p.blank(false)
	if out == nil || sub.x0 >= sub.x1 || sub.y0 >= sub.y1 {
		return out
	}
	href := strings.TrimSpace(k.attr["href"])
	x, y := float32(p.src.X+sub.x0), float32(p.src.Y+sub.y0)
	if t := p.r.doc.byID[fragment(href)]; t != nil && p.r.depth < maxNesting {
		dev := gfx.NewDrawDevice(out.px)
		saved := p.r.dev
		p.r.dev = dev
		p.r.depth++
		p.r.element(t, raster.Concat(raster.Scale(p.sx, p.sy), raster.Translate(x, y)),
			initialState(p.st.vw, p.st.vh))
		p.r.depth--
		p.r.dev = saved
		dev.Close()
		return out
	}
	pic := p.r.picture(href)
	if pic == nil {
		return out
	}
	iw, ih := pic.Size()
	if iw <= 0 || ih <= 0 {
		return out
	}
	w, h := float32(sub.x1-sub.x0), float32(sub.y1-sub.y0)
	al, sl := aspect(k.attr["preserveAspectRatio"])
	fit := viewport([]float32{0, 0, float32(iw), float32(ih)}, al, sl, w, h).
		ApplyRect(raster.Rect{X1: float32(iw), Y1: float32(ih)})
	dev := gfx.NewDrawDevice(out.px)
	dev.FillImage(pic, raster.Matrix{
		A: fit.X1 - fit.X0, D: fit.Y1 - fit.Y0,
		E: x + fit.X0, F: y + fit.Y0,
	}, 1, gfx.ColorParams{})
	dev.Close()
	return out
}

func unpremultiply(p *raster.Pixmap) {
	s := p.Samples
	for i := 0; i+4 <= len(s); i += 4 {
		a := s[i+3]
		if a == 0 || a == 255 {
			continue
		}
		s[i], s[i+1], s[i+2] = unmul(s[i], a), unmul(s[i+1], a), unmul(s[i+2], a)
	}
}
