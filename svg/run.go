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
	// ctxFill and ctxStroke are what context-fill and context-stroke stand
	// for: the paint of the element that referred to this one, SVG 2 13.2.
	// ctxCTM and ctxBox are the space that paint is read in, and fillCtx and
	// strokeCtx which of the two the element took.
	ctxFill, ctxStroke             paint
	ctxFillServer, ctxStrokeServer string
	ctxCTM                         raster.Matrix
	ctxBox                         raster.Rect
	fillCtx, strokeCtx             bool
	color                          paint
	fillOpacity                    float32
	strokeOpacity                  float32
	fillEvenOdd                    bool
	width                          float32
	miter                          float32
	cap                            raster.Cap
	join                           raster.Join
	dash                           []float32
	phase                          float32
	// hidden is display: none, which drops the element and everything in it,
	// and invisible visibility: hidden, which a child may turn back on.
	hidden    bool
	invisible bool
	// em is the font size a relative length is measured against, and family,
	// bold, italic and anchor the rest of what a text element is drawn with.
	em     float32
	family string
	bold   bool
	italic bool
	anchor string
	// vertical is writing-mode: the characters run down the page and each one
	// either stands upright or turns a quarter with the line, UAX #50.
	vertical bool
	// rtl is direction, the way the characters of a chunk run when the
	// bidirectional algorithm has nothing to say, and override is
	// unicode-bidi: bidi-override, which lays them out that way whatever they
	// are.
	rtl      bool
	override bool
	// shift is how far baseline-shift has moved the baseline of the
	// characters an element holds, and chain what a tspan inside it shifts
	// from.
	shift, chain float32
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
		ctxFill:       noPaint,
		ctxStroke:     noPaint,
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
	// arts are the drawings an image element referred to, which are
	// documents of their own, and active the elements being drawn through,
	// which one that names itself must not re-enter.
	arts   map[string]*Page
	active map[*node]bool
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
func (p *Page) Run(dev Device, ctm raster.Matrix) error { return p.run(dev, ctm, 0) }

// run is Run with the nesting a drawing reached to get here, which bounds one
// that puts itself in an image element.
func (p *Page) run(dev Device, ctm raster.Matrix, depth int) error {
	if depth >= maxNesting {
		return nil
	}
	r := &runner{doc: p.doc, dev: dev, depth: depth}
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
	// The root is a viewport, so its filter and its opacity apply to what it
	// holds.
	alpha := float32(1)
	if v, ok := opacity(r.prop(d.root, "opacity")); ok {
		alpha = v
	}
	if r.clip(d.root, m, st) {
		defer r.dev.PopClip()
	}
	if r.mask(d.root, m, st) {
		defer r.dev.PopClip()
	}
	if !r.filterWith(d.root, m, st, alpha, r.children) {
		if alpha < 1 {
			r.dev.BeginGroup(raster.InfiniteRect, nil, true, false, gfx.BlendNormal, alpha)
		}
		r.children(d.root, m, st)
		if alpha < 1 {
			r.dev.EndGroup()
		}
	}
	if len(r.errs) > 0 {
		return r.errs[0]
	}
	return nil
}

// fail logs what went wrong while drawing, which a damaged file does rather
// than stop.
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

	if !conditional(n) {
		return
	}
	st = r.style(n, st)
	if st.hidden {
		return
	}
	ctm = raster.Concat(atOrigin(transform(r.prop(n, "transform")),
		r.prop(n, "transform-origin"), st.vw, st.vh, st.em), ctm)
	if r.clip(n, ctm, st) {
		defer r.dev.PopClip()
	}
	if r.mask(n, ctm, st) {
		defer r.dev.PopClip()
	}
	// An element that blends with what is under it, or that isolates what is
	// under it from its children, is a group of its own.
	b, blend := blendMode(r.cssProp(n, "mix-blend-mode"))
	iso := strings.TrimSpace(r.cssProp(n, "isolation")) == "isolate"
	if blend || iso {
		r.dev.BeginGroup(raster.InfiniteRect, nil, iso, false, b, 1)
		defer r.dev.EndGroup()
	}
	// Opacity applies to what the element draws as a whole, after the filter.
	alpha := float32(1)
	if v, ok := opacity(r.prop(n, "opacity")); ok {
		alpha = v
	}
	if r.filtered(n, ctm, st, alpha) {
		return
	}
	if alpha < 1 {
		if isGroup(n.name) {
			r.dev.BeginGroup(raster.InfiniteRect, nil, true, false, gfx.BlendNormal, alpha)
			defer r.dev.EndGroup()
		} else {
			st.fillOpacity *= alpha
			st.strokeOpacity *= alpha
		}
	}
	r.paint(n, ctm, st)
}

// paint draws the element itself, which is what a filter renders off to one
// side rather than onto the page.
func (r *runner) paint(n *node, ctm raster.Matrix, st state) {
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
	case "switch":
		r.chosen(n, ctm, st)
	}
}

// chosen draws the first child of a switch whose conditions all hold,
// SVG 1.1 5.8.
func (r *runner) chosen(n *node, ctm raster.Matrix, st state) {
	for _, k := range n.kids {
		if switchable(k.name) && conditional(k) {
			r.element(k, ctm, st)
			return
		}
	}
}

// switchable is an element a switch may choose between.
func switchable(name string) bool {
	switch name {
	case "a", "foreignObject", "g", "image", "svg", "switch", "text", "use",
		"circle", "ellipse", "line", "path", "polygon", "polyline", "rect":
		return true
	}
	return false
}

// conditional evaluates the three attributes of SVG 1.1 5.8. An empty value
// is false for all of them; an extension is one this has none of, and a
// feature is one it has.
func conditional(n *node) bool {
	if _, ok := n.attr["requiredExtensions"]; ok {
		return false
	}
	if v, ok := n.attr["requiredFeatures"]; ok && strings.TrimSpace(v) == "" {
		return false
	}
	if v, ok := n.attr["systemLanguage"]; ok {
		return speaks(v)
	}
	return true
}

// language is the tag systemLanguage is matched against.
const language = "en"

func speaks(v string) bool {
	for _, tag := range strings.Split(v, ",") {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == language || strings.HasPrefix(tag, language+"-") {
			return true
		}
	}
	return false
}

// inContext hands the element's own paint down as what context-fill and
// context-stroke stand for, which is what a use and a marker do for what they
// draw. A server it names is read in the space of the element that handed it
// down, not of the shape that takes it, so a rotated shape does not rotate the
// gradient it was given.
func (r *runner) inContext(n *node, ctm raster.Matrix, st state) state {
	st.ctxFill, st.ctxFillServer = st.fill, st.fillServer
	st.ctxStroke, st.ctxStrokeServer = st.stroke, st.strokeServer
	st.ctxCTM, st.ctxBox = ctm, raster.Rect{}
	st.fillCtx, st.strokeCtx = false, false
	if st.fillServer != "" || st.strokeServer != "" {
		st.ctxBox = r.shapeBounds(n, st)
	}
	return st
}

// paintSpace is where a paint server is read: the shape's own space, or the
// space the paint came from when it came from context-fill or context-stroke.
func paintSpace(ctx bool, box raster.Rect, ctm raster.Matrix, st state) (raster.Rect, raster.Matrix) {
	if ctx && !st.ctxBox.IsEmpty() {
		return st.ctxBox, st.ctxCTM
	}
	return box, ctm
}

// opened reports an element the runner is inside of.
func (r *runner) opened(n *node) bool {
	for _, o := range r.open {
		if o == n {
			return true
		}
	}
	return false
}

func (r *runner) group(n *node, ctm raster.Matrix, st state) {
	r.children(n, ctm, st)
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
	if r.viewportClip(n, x, y, w, h, ctm) {
		defer r.dev.PopClip()
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

// viewportClip cuts what an element draws down to the viewport it
// establishes, which is what overflow asks for and is hidden by default,
// SVG 1.1 14.3.3.
func (r *runner) viewportClip(n *node, x, y, w, h float32, ctm raster.Matrix) bool {
	switch strings.TrimSpace(r.prop(n, "overflow")) {
	case "visible", "auto":
		return false
	}
	var box raster.Path
	box.Rect(x, y, w, h)
	r.dev.ClipPath(&box, false, ctm, raster.InfiniteRect)
	return true
}

// use draws what an element refers to, in place. A use of a symbol or of an
// svg takes its width and height from the use.
func (r *runner) use(n *node, ctm raster.Matrix, st state) {
	if r.depth >= maxNesting {
		return
	}
	target := r.doc.byID[fragment(n.attr["href"])]
	// An element already being drawn is one this is inside of, so drawing it
	// again would never end.
	if target == nil || r.active[target] || r.opened(target) {
		return
	}
	if r.active == nil {
		r.active = map[*node]bool{}
	}
	r.active[target] = true
	defer delete(r.active, target)
	st = r.inContext(n, ctm, st)
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
		// SVG 1.1 gives neither a symbol nor an svg a transform of its own.
		m := ctm
		if r.viewportClip(target, 0, 0, w, h, m) {
			defer r.dev.PopClip()
		}
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
	if p == nil || p.IsEmpty() || st.invisible {
		return
	}
	fillAlpha := st.fillOpacity * st.fill.alpha
	strokeAlpha := st.strokeOpacity * st.stroke.alpha
	if !st.fill.none || st.fillServer != "" {
		box, at := paintSpace(st.fillCtx, p.Bounds(raster.Identity), ctm, st)
		g, empty := r.server(st.fillServer, box, st)
		switch {
		case g != nil:
			r.dev.ClipPath(p, st.fillEvenOdd, ctm, raster.InfiniteRect)
			r.dev.FillShade(g, at, fillAlpha*g.alpha(), gfx.ColorParams{})
			r.dev.PopClip()
		case empty:
		case st.fillServer != "" && r.faded(fillAlpha, func() bool {
			return r.tiled(st.fillServer, p, st.fillEvenOdd, box, ctm, at, st)
		}):
		case st.fill.none:
		default:
			c := st.fill.color
			r.dev.FillPath(p, st.fillEvenOdd, ctm, gfx.DeviceRGB, c[:], fillAlpha, gfx.ColorParams{})
		}
	}
	if (!st.stroke.none || st.strokeServer != "") && st.width > 0 {
		s := r.strokeOf(st)
		// The box a gradient in objectBoundingBox units is a fraction of is
		// the shape's own, which SVG 1.1 7.11 says the stroke is not part of.
		box, at := paintSpace(st.strokeCtx, p.Bounds(raster.Identity), ctm, st)
		g, empty := r.server(st.strokeServer, box, st)
		switch {
		case g != nil:
			r.dev.ClipStrokePath(p, s, ctm, raster.InfiniteRect)
			r.dev.FillShade(g, at, strokeAlpha*g.alpha(), gfx.ColorParams{})
			r.dev.PopClip()
		case empty:
		case st.strokeServer != "" && r.faded(strokeAlpha, func() bool {
			return r.tiledStroke(st.strokeServer, p, s, box, ctm, at, st)
		}):
		case st.stroke.none:
		default:
			c := st.stroke.color
			r.dev.StrokePath(p, s, ctm, gfx.DeviceRGB, c[:], strokeAlpha, gfx.ColorParams{})
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
	ctm, at raster.Matrix, st state) bool {
	r.dev.ClipPath(p, even, ctm, raster.InfiniteRect)
	ok := r.pattern(server, box, at, st)
	r.dev.PopClip()
	return ok
}

// faded draws through a transparency group when the paint is not opaque,
// which is what an opacity on a pattern means: the tiles fade as a whole.
func (r *runner) faded(alpha float32, draw func() bool) bool {
	if alpha >= 1 {
		return draw()
	}
	r.dev.BeginGroup(raster.InfiniteRect, nil, true, false, gfx.BlendNormal, alpha)
	ok := draw()
	r.dev.EndGroup()
	return ok
}

// tiledStroke paints a stroke with a pattern, clipped to it.
func (r *runner) tiledStroke(server string, p *raster.Path, s *raster.Stroke,
	box raster.Rect, ctm, at raster.Matrix, st state) bool {
	r.dev.ClipStrokePath(p, s, ctm, raster.InfiniteRect)
	ok := r.pattern(server, box, at, st)
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

// cssProp reads a property only a style attribute or a stylesheet may set.
// SVG 2 gives mix-blend-mode and isolation no presentation attribute.
func (r *runner) cssProp(n *node, name string) string {
	if v, ok := styleProp(n.attr["style"], name); ok {
		return v
	}
	if v, ok := r.sheetProp(n, name); ok {
		return v
	}
	return ""
}

// styleProp picks one declaration out of a style attribute.
func styleProp(style, name string) (string, bool) {
	if style == "" {
		return "", false
	}
	if strings.Contains(style, "/*") {
		style = stripComments(style)
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
	switch strings.TrimSpace(r.prop(n, "visibility")) {
	case "hidden", "collapse":
		st.invisible = true
	case "visible":
		st.invisible = false
	}
	// color is read first: currentColor in any other property means this one,
	// and an inherited one follows the color of wherever it is used.
	if c, ok := parseColor(r.prop(n, "color"), st.color); ok {
		st.color = c
	}
	st.fill, st.fillServer, st.fillCtx =
		st.paintOf(r.prop(n, "fill"), st.fill, st.fillServer, st.fillCtx)
	st.stroke, st.strokeServer, st.strokeCtx =
		st.paintOf(r.prop(n, "stroke"), st.stroke, st.strokeServer, st.strokeCtx)
	st.fill = st.fill.follow(st.color)
	st.stroke = st.stroke.follow(st.color)
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
	if v, ok := namedSize(r.prop(n, "font-size")); ok {
		st.em = v
	} else if v, ok := length(r.prop(n, "font-size"), st.em, st.em); ok && v > 0 {
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
	// The shorthand is read before the properties it stands for, so that one
	// written as well as it wins.
	if size, family, ok := fontShorthand(r.prop(n, "font")); ok {
		if v, ok := namedSize(size); ok {
			st.em = v
		} else if v, ok := length(size, st.em, st.em); ok && v > 0 {
			st.em = v
		}
		if family != "" {
			st.family = family
		}
	}
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
	switch strings.TrimSpace(r.prop(n, "direction")) {
	case "rtl":
		st.rtl = true
	case "ltr":
		st.rtl = false
	}
	switch strings.TrimSpace(r.prop(n, "unicode-bidi")) {
	case "bidi-override", "isolate-override":
		st.override = true
	case "normal", "embed", "isolate", "plaintext":
		st.override = false
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
	// baseline-shift moves the characters the element holds itself. One
	// tspan inside another shifts from the baseline the outer one moved to;
	// a text element starts a baseline rather than shifting from one, so
	// what it says reaches its own characters and no tspan of its.
	shift := baselineShift(strings.TrimSpace(r.prop(n, "baseline-shift")), st.em)
	switch n.name {
	case "tspan", "textPath", "tref", "altGlyph":
		st.shift = st.chain + shift
		st.chain = st.shift
	case "text":
		st.shift, st.chain = shift, 0
	default:
		st.shift, st.chain = 0, 0
	}
	switch strings.TrimSpace(r.prop(n, "writing-mode")) {
	case "tb", "tb-rl", "vertical-rl", "vertical-lr":
		st.vertical = true
	case "lr", "lr-tb", "rl", "rl-tb", "horizontal-tb":
		st.vertical = false
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

	return st
}

// paintOf reads a fill or a stroke. A value that names a server keeps what it
// said to fall back to, and falls back to nothing when it said nothing:
// SVG 1.1 11.3 draws neither when the reference resolves to no server. A
// value written but not understood is ignored, so the paint stays what was
// inherited, which is what CSS does with any property it cannot read.
func (st state) paintOf(v string, was paint, wasServer string, wasCtx bool) (paint, string, bool) {
	switch strings.TrimSpace(v) {
	case "":
		return was, wasServer, wasCtx
	case "context-fill":
		return st.ctxFill, st.ctxFillServer, true
	case "context-stroke":
		return st.ctxStroke, st.ctxStrokeServer, true
	}
	p, ok := parsePaint(v, st.color)
	if id := serverID(v); id != "" {
		// A reference with a fallback that is not a paint is not a paint
		// either, however good the reference is.
		if !ok && fallbackOf(v) != "" {
			return was, wasServer, wasCtx
		}
		if !ok {
			p = noPaint
		}
		return p, v, false
	}
	if !ok {
		return was, wasServer, wasCtx
	}
	return p, "", false
}

// baselineShift is how far down the page baseline-shift moves a character.
// A raised baseline is a smaller y, so a positive shift comes back negative.
// The two words are answered from the em rather than the font: the face a
// character lands in is not known this early.
func baselineShift(v string, em float32) float32 {
	switch v {
	case "", "baseline":
		return 0
	case "sub":
		return subShift * em
	case "super":
		return -superShift * em
	}
	if d, ok := length(v, em, em); ok {
		return -d
	}
	return 0
}

// subShift and superShift are what a program that says nothing puts a
// subscript and a superscript at.
const (
	subShift   = 0.2
	superShift = 0.4
)

// fontShorthand splits the font property into the size and the family, which
// are the two of the six it carries that a drawing is laid out by. The size
// is the last word before the family and may be written with a line height
// after a slash, CSS Fonts 3 3.7.
func fontShorthand(v string) (size, family string, ok bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "", false
	}
	f := strings.Fields(v)
	for i, w := range f {
		w, _, _ = strings.Cut(w, "/")
		if _, named := namedSize(w); !named {
			if _, isLen := length(w, 1, 1); !isLen {
				continue
			}
		}
		if i+1 >= len(f) {
			return w, "", true
		}
		return w, strings.Join(f[i+1:], " "), true
	}
	return "", "", false
}

// namedSize is one of the seven absolute sizes CSS Fonts 3 names, on the
// scale a medium of sixteen pixels sets.
func namedSize(v string) (float32, bool) {
	switch strings.TrimSpace(v) {
	case "xx-small":
		return 9, true
	case "x-small":
		return 10, true
	case "small":
		return 13, true
	case "medium":
		return 16, true
	case "large":
		return 18, true
	case "x-large":
		return 24, true
	case "xx-large":
		return 32, true
	}
	return 0, false
}

// fallbackOf is what a paint wrote after the reference, and empty for one
// that wrote nothing.
func fallbackOf(v string) string {
	i := strings.IndexByte(v, ')')
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(v[i+1:])
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
