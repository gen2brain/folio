package svg

import (
	"strings"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// state is what an element inherits from the one around it. SVG properties
// inherit, so this is copied down the tree and never back up.
type state struct {
	fill, stroke paint
	// fillServer and strokeServer are the paint that named a gradient, kept
	// so that the shape it paints can be measured before it is resolved.
	fillServer, strokeServer string
	color                    paint
	fillOpacity              float32
	strokeOpacity            float32
	fillEvenOdd              bool
	width                    float32
	miter                    float32
	cap                      raster.Cap
	join                     raster.Join
	dash                     []float32
	phase                    float32
	// hidden is display: none, which drops the element and everything in it.
	hidden bool
	// em is the font size a relative length is measured against, and family,
	// bold, italic and anchor the rest of what a text element is drawn with.
	em     float32
	family string
	bold   bool
	italic bool
	anchor string
	// baseline is what dominant-baseline moved the text off, letter and word
	// what is added after a character and after a space, and preserve
	// whether the white space is kept as it was written.
	baseline     string
	letter, word float32
	preserve     bool
	decoration   string
	// markStart, markMid and markEnd are what a path puts at its vertices.
	markStart, markMid, markEnd string
	// vw and vh are the viewport a percentage length is a percentage of.
	vw, vh float32
}

func initialState(w, h float32) state {
	return state{
		fill:          black,
		stroke:        noPaint,
		color:         black,
		fillOpacity:   1,
		strokeOpacity: 1,
		width:         1,
		miter:         4,
		em:            defFontSize,
		vw:            w,
		vh:            h,
	}
}

// runner walks the tree and draws it.
type runner struct {
	doc  *Document
	dev  Device
	errs []error
	// depth counts how deep a use has reached, which bounds a file that
	// refers to itself.
	depth int
	ops   int
	// faces are the programs a family has resolved to, which every text
	// element of a drawing asks for again, and pics the pictures decoded so
	// far, which a use may draw many times.
	faces map[faceKey]*font.Font
	pics  map[string]gfx.Image
	// fbs are the faces a character the chosen one cannot draw has fallen
	// back to, which is the same answer for every element that asks.
	fbs map[fallbackKey]*font.Font
	// open is the elements being drawn, outermost first, which is what a
	// descendant selector is matched against.
	open []*node
}

// maxOps bounds the elements one drawing may draw, so that a use that
// multiplies a group cannot run forever.
const maxOps = 1 << 20

// Run draws the page onto a device under ctm.
func (p *Page) Run(dev Device, ctm raster.Matrix) error {
	r := &runner{doc: p.doc, dev: dev}
	d := p.doc
	// The root's viewBox maps the coordinates the file draws in onto the size
	// it asked to be drawn at.
	m := ctm
	if d.box != nil {
		m = raster.Concat(viewport(d.box, d.align, d.slice, d.width, d.height), ctm)
	}
	st := initialState(d.width, d.height)
	if d.box != nil {
		st.vw, st.vh = d.box[2], d.box[3]
	}
	// The root carries properties of its own, and every icon written as one
	// outline puts fill and stroke there for the whole drawing to inherit.
	r.open = append(r.open, d.root)
	st = r.style(d.root, st)
	if st.hidden {
		return nil
	}
	r.children(d.root, m, st)
	if len(r.errs) > 0 {
		return r.errs[0]
	}
	return nil
}

// Errors are what went wrong while drawing, which a damaged file logs rather
// than fails on.
func (r *runner) fail(err error) {
	if len(r.errs) < 64 {
		r.errs = append(r.errs, err)
	}
}

func (r *runner) children(n *node, ctm raster.Matrix, st state) {
	for _, k := range n.kids {
		r.element(k, ctm, st)
	}
}

// element draws one element and everything under it.
func (r *runner) element(n *node, ctm raster.Matrix, st state) {
	if r.ops++; r.ops > maxOps {
		return
	}
	r.open = append(r.open, n)
	defer func() { r.open = r.open[:len(r.open)-1] }()
	switch n.name {
	case "defs", "symbol", "clipPath", "mask", "pattern", "marker",
		"linearGradient", "radialGradient", "title", "desc", "metadata", "style", "script":
		// A definition is drawn where it is referred to, not where it is
		// written, and the rest of these are not a picture.
		return
	}

	st = r.style(n, st)
	if st.hidden {
		return
	}
	ctm = raster.Concat(transform(n.attr["transform"]), ctm)
	if r.clip(n, ctm, st) {
		defer r.dev.PopClip()
	}
	if r.mask(n, ctm, st) {
		defer r.dev.PopClip()
	}
	// An element that blends with what is under it is a group of its own,
	// which is what carries the mode to the device.
	if b, ok := blendMode(r.prop(n, "mix-blend-mode")); ok {
		r.dev.BeginGroup(raster.InfiniteRect, nil, false, false, b, 1)
		defer r.dev.EndGroup()
	}

	switch n.name {
	case "g", "a":
		r.group(n, ctm, st)
	case "svg":
		r.nested(n, ctm, st)
	case "use":
		r.use(n, ctm, st)
	case "path":
		r.marked(pathData(n.attr["d"]), ctm, st)
	case "rect":
		r.shape(rectPath(n, st), ctm, st)
	case "circle":
		r.shape(circlePath(n, st), ctm, st)
	case "ellipse":
		r.shape(ellipsePath(n, st), ctm, st)
	case "line":
		r.marked(linePath(n, st), ctm, st)
	case "polyline":
		r.marked(polyPath(n, false), ctm, st)
	case "polygon":
		r.marked(polyPath(n, true), ctm, st)
	case "text":
		r.text(n, ctm, st)
	case "image":
		r.image(n, ctm, st)
	}
}

// group draws a group, which is a transparency group of its own when it is
// not fully opaque: opacity on a group applies to what it draws as a whole
// rather than to each shape in it.
func (r *runner) group(n *node, ctm raster.Matrix, st state) {
	alpha := float32(1)
	if v, ok := opacity(r.prop(n, "opacity")); ok {
		alpha = v
	}
	if alpha >= 1 {
		r.children(n, ctm, st)
		return
	}
	r.dev.BeginGroup(raster.InfiniteRect, nil, true, false, gfx.BlendNormal, alpha)
	r.children(n, ctm, st)
	r.dev.EndGroup()
}

// nested is an svg element inside another, which is a viewport of its own.
func (r *runner) nested(n *node, ctm raster.Matrix, st state) {
	x, _ := r.length(n, "x", st.vw, st)
	y, _ := r.length(n, "y", st.vh, st)
	w, wok := r.length(n, "width", st.vw, st)
	h, hok := r.length(n, "height", st.vh, st)
	if !wok {
		w = st.vw
	}
	if !hok {
		h = st.vh
	}
	if w <= 0 || h <= 0 {
		return
	}
	ctm = raster.Concat(raster.Translate(x, y), ctm)
	if box := numbers(n.attr["viewBox"]); len(box) == 4 && box[2] > 0 && box[3] > 0 {
		al, sl := aspect(n.attr["preserveAspectRatio"])
		ctm = raster.Concat(viewport(box, al, sl, w, h), ctm)
		st.vw, st.vh = box[2], box[3]
	} else {
		st.vw, st.vh = w, h
	}
	r.children(n, ctm, st)
}

// use draws what an element refers to, in place. A use of a symbol or of an
// svg takes its width and height from the use.
func (r *runner) use(n *node, ctm raster.Matrix, st state) {
	if r.depth >= maxNesting {
		return
	}
	target := r.doc.byID[fragment(n.attr["href"])]
	if target == nil {
		return
	}
	x, _ := r.length(n, "x", st.vw, st)
	y, _ := r.length(n, "y", st.vh, st)
	ctm = raster.Concat(raster.Translate(x, y), ctm)

	r.depth++
	defer func() { r.depth-- }()

	if target.name == "symbol" || target.name == "svg" {
		w, wok := r.length(n, "width", st.vw, st)
		h, hok := r.length(n, "height", st.vh, st)
		if !wok {
			w = st.vw
		}
		if !hok {
			h = st.vh
		}
		if w <= 0 || h <= 0 {
			return
		}
		sub := r.style(target, st)
		if sub.hidden {
			return
		}
		m := raster.Concat(transform(target.attr["transform"]), ctm)
		if box := numbers(target.attr["viewBox"]); len(box) == 4 && box[2] > 0 && box[3] > 0 {
			al, sl := aspect(target.attr["preserveAspectRatio"])
			m = raster.Concat(viewport(box, al, sl, w, h), m)
			sub.vw, sub.vh = box[2], box[3]
		} else {
			sub.vw, sub.vh = w, h
		}
		r.children(target, m, sub)
		return
	}
	r.element(target, ctm, st)
}

// shape fills and then strokes one path, which is the order SVG 1.1 11.3
// paints them in.
func (r *runner) shape(p *raster.Path, ctm raster.Matrix, st state) {
	if p == nil || p.IsEmpty() {
		return
	}
	if !st.fill.none {
		box := p.Bounds(raster.Identity)
		if g := r.server(st.fillServer, box, st); g != nil {
			r.dev.ClipPath(p, st.fillEvenOdd, ctm, raster.InfiniteRect)
			r.dev.FillShade(g, ctm, st.fillOpacity*g.alpha(), gfx.ColorParams{})
			r.dev.PopClip()
		} else if st.fillServer != "" && r.tiled(st.fillServer, p, st.fillEvenOdd, box, ctm, st) {
		} else {
			c := st.fill.color
			r.dev.FillPath(p, st.fillEvenOdd, ctm, gfx.DeviceRGB, c[:], st.fillOpacity, gfx.ColorParams{})
		}
	}
	if !st.stroke.none && st.width > 0 {
		s := r.strokeOf(st)
		// The box a gradient in objectBoundingBox units is a fraction of is
		// the shape's own, which SVG 1.1 7.11 says the stroke is not part of.
		if g := r.server(st.strokeServer, p.Bounds(raster.Identity), st); g != nil {
			r.dev.ClipStrokePath(p, s, ctm, raster.InfiniteRect)
			r.dev.FillShade(g, ctm, st.strokeOpacity*g.alpha(), gfx.ColorParams{})
			r.dev.PopClip()
		} else {
			c := st.stroke.color
			r.dev.StrokePath(p, s, ctm, gfx.DeviceRGB, c[:], st.strokeOpacity, gfx.ColorParams{})
		}
	}
}

// marked draws a shape and whatever its markers put at its vertices, which
// only the shapes that have vertices may carry.
func (r *runner) marked(p *raster.Path, ctm raster.Matrix, st state) {
	r.shape(p, ctm, st)
	if p != nil {
		r.markers(p, ctm, st)
	}
}

// tiled paints a shape with a pattern, clipped to it.
func (r *runner) tiled(server string, p *raster.Path, even bool, box raster.Rect,
	ctm raster.Matrix, st state) bool {
	r.dev.ClipPath(p, even, ctm, raster.InfiniteRect)
	ok := r.pattern(server, box, ctm, st)
	r.dev.PopClip()
	return ok
}

func (r *runner) strokeOf(st state) *raster.Stroke {
	return &raster.Stroke{
		Width:      st.width,
		MiterLimit: st.miter,
		StartCap:   st.cap,
		DashCap:    st.cap,
		EndCap:     st.cap,
		Join:       st.join,
		Dash:       st.dash,
		DashPhase:  st.phase,
	}
}

// prop reads a property. A style attribute wins over what a sheet declares,
// which wins over an attribute of its own: CSS 2.1 6.4.4 gives a presentation
// attribute no specificity at all.
func (r *runner) prop(n *node, name string) string {
	if v, ok := styleProp(n.attr["style"], name); ok {
		return v
	}
	if v, ok := r.sheetProp(n, name); ok {
		return v
	}
	return n.attr[name]
}

// styleProp picks one declaration out of a style attribute.
func styleProp(style, name string) (string, bool) {
	if style == "" {
		return "", false
	}
	for _, decl := range strings.Split(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// style is the state an element draws with: what it inherited, with whatever
// it says for itself on top.
func (r *runner) style(n *node, st state) state {
	if strings.TrimSpace(r.prop(n, "display")) == "none" {
		st.hidden = true
		return st
	}
	if v := strings.TrimSpace(r.prop(n, "visibility")); v == "hidden" || v == "collapse" {
		st.hidden = true
		return st
	}
	// color is read first: currentColor in any other property means this one.
	if c, ok := parseColor(r.prop(n, "color"), st.color); ok {
		st.color = c
	}
	if p := r.prop(n, "fill"); strings.TrimSpace(p) != "" {
		if v, ok := parsePaint(p, st.color); ok {
			st.fill, st.fillServer = v, ""
		}
		if id := serverID(p); id != "" {
			st.fill, st.fillServer = black, p
		}
	}
	if p := r.prop(n, "stroke"); strings.TrimSpace(p) != "" {
		if v, ok := parsePaint(p, st.color); ok {
			st.stroke, st.strokeServer = v, ""
		}
		if id := serverID(p); id != "" {
			st.stroke, st.strokeServer = black, p
		}
	}
	if v, ok := opacity(r.prop(n, "fill-opacity")); ok {
		st.fillOpacity = v
	}
	if v, ok := opacity(r.prop(n, "stroke-opacity")); ok {
		st.strokeOpacity = v
	}
	switch strings.TrimSpace(r.prop(n, "fill-rule")) {
	case "evenodd":
		st.fillEvenOdd = true
	case "nonzero":
		st.fillEvenOdd = false
	}
	if v, ok := length(r.prop(n, "font-size"), st.em, st.em); ok && v > 0 {
		st.em = v
	}
	if v, ok := length(r.prop(n, "stroke-width"), diagonal(st.vw, st.vh), st.em); ok {
		st.width = max(v, 0)
	}
	if v := numbers(r.prop(n, "stroke-miterlimit")); len(v) == 1 && v[0] >= 1 {
		st.miter = v[0]
	}
	switch strings.TrimSpace(r.prop(n, "stroke-linecap")) {
	case "butt":
		st.cap = raster.CapButt
	case "round":
		st.cap = raster.CapRound
	case "square":
		st.cap = raster.CapSquare
	}
	switch strings.TrimSpace(r.prop(n, "stroke-linejoin")) {
	case "miter":
		st.join = raster.JoinMiter
	case "round":
		st.join = raster.JoinRound
	case "bevel":
		st.join = raster.JoinBevel
	}
	st.dash, st.phase = r.dashes(n, st)
	if v := strings.TrimSpace(r.prop(n, "font-family")); v != "" {
		st.family = v
	}
	switch v := strings.TrimSpace(r.prop(n, "font-weight")); v {
	case "bold", "bolder", "600", "700", "800", "900":
		st.bold = true
	case "normal", "lighter", "100", "200", "300", "400", "500":
		st.bold = false
	}
	switch strings.TrimSpace(r.prop(n, "font-style")) {
	case "italic", "oblique":
		st.italic = true
	case "normal":
		st.italic = false
	}
	if v := strings.TrimSpace(r.prop(n, "text-anchor")); v != "" {
		st.anchor = v
	}
	if v := strings.TrimSpace(r.prop(n, "marker")); v != "" {
		st.markStart, st.markMid, st.markEnd = v, v, v
	}
	for _, m := range [3]struct {
		name string
		to   *string
	}{{"marker-start", &st.markStart}, {"marker-mid", &st.markMid}, {"marker-end", &st.markEnd}} {
		if v := strings.TrimSpace(r.prop(n, m.name)); v != "" {
			*m.to = v
			if v == "none" {
				*m.to = ""
			}
		}
	}
	if v := strings.TrimSpace(r.prop(n, "text-decoration")); v != "" {
		st.decoration = v
	}
	if v := strings.TrimSpace(r.prop(n, "dominant-baseline")); v != "" {
		st.baseline = v
	}
	if v := strings.TrimSpace(r.prop(n, "alignment-baseline")); v != "" {
		st.baseline = v
	}
	if v, ok := length(r.prop(n, "letter-spacing"), 0, st.em); ok {
		st.letter = v
	}
	if v, ok := length(r.prop(n, "word-spacing"), 0, st.em); ok {
		st.word = v
	}
	switch strings.TrimSpace(n.attr["space"]) {
	case "preserve":
		st.preserve = true
	case "default":
		st.preserve = false
	}

	// The opacity of a shape is the opacity of each of its paints; a group
	// takes it as a whole instead, which group handles.
	if v, ok := opacity(r.prop(n, "opacity")); ok && !isGroup(n.name) {
		st.fillOpacity *= v
		st.strokeOpacity *= v
	}
	return st
}

// blendMode is the mode mix-blend-mode names, and false for the one that
// blends nothing.
func blendMode(v string) (gfx.BlendMode, bool) {
	switch strings.TrimSpace(v) {
	case "multiply":
		return gfx.BlendMultiply, true
	case "screen":
		return gfx.BlendScreen, true
	case "overlay":
		return gfx.BlendOverlay, true
	case "darken":
		return gfx.BlendDarken, true
	case "lighten":
		return gfx.BlendLighten, true
	case "color-dodge":
		return gfx.BlendColorDodge, true
	case "color-burn":
		return gfx.BlendColorBurn, true
	case "hard-light":
		return gfx.BlendHardLight, true
	case "soft-light":
		return gfx.BlendSoftLight, true
	case "difference":
		return gfx.BlendDifference, true
	case "exclusion":
		return gfx.BlendExclusion, true
	case "hue":
		return gfx.BlendHue, true
	case "saturation":
		return gfx.BlendSaturation, true
	case "color":
		return gfx.BlendColor, true
	case "luminosity":
		return gfx.BlendLuminosity, true
	}
	return gfx.BlendNormal, false
}

func isGroup(name string) bool {
	switch name {
	case "g", "a", "svg", "use", "symbol":
		return true
	}
	return false
}

// dashes reads stroke-dasharray and stroke-dashoffset. A pattern that is
// negative anywhere, or that adds up to nothing, is not a pattern.
func (r *runner) dashes(n *node, st state) ([]float32, float32) {
	s := strings.TrimSpace(r.prop(n, "stroke-dasharray"))
	if s == "" {
		return st.dash, st.phase
	}
	if s == "none" {
		return nil, 0
	}
	v := numbers(s)
	if len(v) == 0 {
		return nil, 0
	}
	total := float32(0)
	for _, d := range v {
		if d < 0 {
			return nil, 0
		}
		total += d
	}
	if total == 0 {
		return nil, 0
	}
	// An odd list repeats to become even, SVG 1.1 11.4.
	if len(v)%2 == 1 {
		v = append(v, v...)
	}
	phase := float32(0)
	if p, ok := length(r.prop(n, "stroke-dashoffset"), diagonal(st.vw, st.vh), st.em); ok {
		phase = p
	}
	return v, phase
}

// length reads one attribute as a length against a reference.
func (r *runner) length(n *node, name string, ref float32, st state) (float32, bool) {
	return length(r.prop(n, name), ref, st.em)
}

// diagonal is what a percentage length with no axis of its own is measured
// against, SVG 1.1 7.10.
func diagonal(w, h float32) float32 {
	return float32(sqrt64(float64(w)*float64(w)+float64(h)*float64(h)) / sqrt2)
}

// fragment is the part of a reference after the hash, which is the only form
// of reference this follows.
func fragment(s string) string {
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		return s[i+1:]
	}
	return ""
}
