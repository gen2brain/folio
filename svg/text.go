package svg

import (
	"strings"
	"unicode"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// glyph is one character placed by a text element, with its face and advance.
type glyph struct {
	r    rune
	gid  int
	face *font.Font
	size float32
	adv  float32
	x, y float32
	// chunk is the anchored chunk the glyph belongs to, SVG 1.1 10.5.
	chunk int
	// turn is the rotate attribute's angle, in degrees clockwise.
	turn float32
	// path is the textPath the glyph lies along; x is the distance along it, y the offset.
	path *pathWalk
	// vert says the glyph was placed in vertical writing.
	vert bool
	// at is the index of the character the glyph came from.
	at int
	st state
}

// char is one character of a text element before shaping: its rune, style and requested position.
type char struct {
	r    rune
	st   state
	face *font.Font
	// ax and ay are an absolute position, dx and dy a relative move.
	ax, ay     float32
	hasX, hasY bool
	dx, dy     float32
	turn       float32
	chunk      int
	path       *pathWalk
}

// textCursor collects the characters of a text element in written order and the next position.
type textCursor struct {
	chars []char
	chunk int
	// setX and setY are the position the next character starts at.
	setX, setY float32
	hasX, hasY bool
	// path is the textPath the characters are being laid along, if any.
	path *pathWalk
	// space says a collapsible space is owed before the next character.
	space bool
	begun bool
	// rotate is the rotate list and rotateAt how many are used; the last repeats, SVG 1.1 10.4.
	rotate   []float32
	rotateAt int
	// fits are the ranges of characters a textLength was written for.
	fits []fit
}

// fit is one textLength: the characters it covers and what they must measure.
type fit struct {
	from, to int
	want     float32
}

// text draws a text element and the tspans under it.
func (r *runner) text(n *node, ctm raster.Matrix, st state) {
	if st.invisible {
		return
	}
	c := &textCursor{}
	r.textRun(n, st, c, 0, true)
	g := r.laidOut(c)
	if len(g) == 0 {
		return
	}
	r.drawGlyphs(g, ctm)
	r.decorate(g, ctm)
}

// laidOut turns the collected characters into glyphs in drawing order.
func (r *runner) laidOut(c *textCursor) []glyph {
	c.chars = trimTrailing(c.chars)
	if len(c.chars) == 0 {
		return nil
	}
	g := r.layout(c.chars)
	if len(c.fits) > 0 {
		first, last := glyphsOf(g, len(c.chars))
		for _, f := range c.fits {
			lo, hi := len(g), 0
			for i := max(f.from, 0); i < min(f.to, len(c.chars)); i++ {
				lo, hi = min(lo, first[i]), max(hi, last[i])
			}
			if lo < hi {
				fitLength(g[lo:hi], f.want)
			}
		}
	}
	r.anchorChunks(g)
	shiftBaseline(g)
	return onPath(g)
}

// glyphsOf is the first and the past-last glyph each of n characters produced.
func glyphsOf(g []glyph, n int) ([]int, []int) {
	first, last := make([]int, n), make([]int, n)
	for i := range first {
		first[i] = len(g)
	}
	for i := range g {
		if a := g[i].at; a >= 0 && a < n {
			first[a], last[a] = min(first[a], i), max(last[a], i+1)
		}
	}
	return first, last
}

// trimTrailing drops the trailing space of a text element, SVG 1.1 10.15.
func trimTrailing(cs []char) []char {
	for len(cs) > 0 {
		last := cs[len(cs)-1]
		if last.r != ' ' || last.st.preserve {
			break
		}
		cs = cs[:len(cs)-1]
	}
	return cs
}

// textRun walks one element of a text, placing its characters and its tspans.
func (r *runner) textRun(n *node, st state, c *textCursor, depth int, styled bool) {
	if depth > maxNesting {
		return
	}
	if !styled {
		st = r.style(n, st)
		if st.hidden {
			return
		}
	}
	xs := lengths(r.prop(n, "x"), st.vw, st.em)
	ys := lengths(r.prop(n, "y"), st.vh, st.em)
	dxs := lengths(r.prop(n, "dx"), st.vw, st.em)
	dys := lengths(r.prop(n, "dy"), st.vh, st.em)
	if len(xs) > 0 || len(ys) > 0 {
		if len(xs) > 0 {
			c.setX, c.hasX = xs[0], true
		}
		if len(ys) > 0 {
			c.setY, c.hasY = ys[0], true
		}
		c.chunk++
	}
	if v := numbers(r.prop(n, "rotate")); len(v) > 0 {
		c.rotate = v
		c.rotateAt = 0
	}

	face := r.face(st)
	first := len(c.chars)
	i := 0
	for _, k := range n.kids {
		switch k.name {
		case "":
			i = r.place(c, k.chars, st, face, xs, ys, dxs, dys, i)
		case "tspan", "a":
			r.textRun(k, st, c, depth+1, false)
		case "textPath":
			r.textPath(k, st, c, depth+1)
		}
	}
	if v, ok := length(r.prop(n, "textLength"), st.vw, st.em); ok && v > 0 {
		c.fits = append(c.fits, fit{first, len(c.chars), v})
	}
}

// place records one run of characters from position list index i and returns the next index.
func (r *runner) place(c *textCursor, s string, st state, face *font.Font, xs, ys, dxs, dys []float32, i int) int {
	if face == nil {
		return i
	}
	for _, ch := range collapse(s, st.preserve, &c.space, &c.begun) {
		// SVG 1.1 10.5.
		if i < len(xs) || i < len(ys) {
			if i < len(xs) {
				c.setX, c.hasX = xs[i], true
			}
			if i < len(ys) {
				c.setY, c.hasY = ys[i], true
			}
			c.chunk++
		}
		v := char{r: ch, st: st, face: face, chunk: c.chunk, path: c.path}
		if i < len(dxs) {
			v.dx = dxs[i]
		}
		if i < len(dys) {
			v.dy = dys[i]
		}
		v.ax, v.hasX, c.hasX = c.setX, c.hasX, false
		v.ay, v.hasY, c.hasY = c.setY, c.hasY, false
		if n := len(c.rotate); n > 0 {
			v.turn = c.rotate[min(c.rotateAt, n-1)]
			c.rotateAt++
		}
		c.chars = append(c.chars, v)
		i++
	}
	return i
}

// emBaseline is the baseline's share of the em box from the top, ascent over ascent minus descent.
func emBaseline(f *font.Font) float32 {
	h := f.Ascent - f.Descent
	if h <= 0 {
		return 0.8
	}
	return f.Ascent / h
}

// collapse applies xml:space to the white space of a text element, SVG 1.1 10.15.
func collapse(s string, preserve bool, space, begun *bool) []rune {
	var out []rune
	for _, r := range s {
		if preserve {
			if unicode.IsSpace(r) {
				r = ' '
			}
			out = append(out, r)
			*begun = true
			continue
		}
		if unicode.IsSpace(r) {
			if *begun && !*space {
				out = append(out, ' ')
			}
			*space = true
			continue
		}
		*space = false
		*begun = true
		out = append(out, r)
	}
	return out
}

// anchorChunks moves each chunk to where text-anchor puts it.
func (r *runner) anchorChunks(g []glyph) {
	for i := 0; i < len(g); {
		j := i
		width := float32(0)
		for j < len(g) && g[j].chunk == g[i].chunk {
			width += g[j].adv
			j++
		}
		shift := float32(0)
		switch strings.TrimSpace(g[i].st.anchor) {
		case "middle":
			shift = -width / 2
		case "end":
			shift = -width
		}
		if shift != 0 {
			for k := i; k < j; k++ {
				if g[k].vert {
					g[k].y += shift
					continue
				}
				g[k].x += shift
			}
		}
		i = j
	}
}

// fitLength adjusts the spacing between glyphs to measure want.
func fitLength(g []glyph, want float32) {
	if len(g) == 0 {
		return
	}
	have := float32(0)
	for _, v := range g {
		have += v.adv
	}
	if len(g) < 2 || have <= 0 {
		return
	}
	extra := (want - have) / float32(len(g)-1)
	start := g[0].x
	x := start
	for i := range g {
		g[i].x = x
		x += g[i].adv + extra
	}
}

// decorate draws the text-decoration lines of a run.
func (r *runner) decorate(g []glyph, ctm raster.Matrix) {
	for i := 0; i < len(g); {
		j := i
		for j < len(g) && g[j].st.decoration == g[i].st.decoration &&
			g[j].size == g[i].size && g[j].chunk == g[i].chunk {
			j++
		}
		run, st := g[i:j], g[i].st
		i = j
		kind := strings.TrimSpace(st.decoration)
		if kind == "" || kind == "none" || len(run) == 0 {
			continue
		}
		if run[0].vert || run[0].path != nil {
			continue
		}
		x0, x1 := run[0].x, run[len(run)-1].x+run[len(run)-1].adv
		size := run[0].size
		y := run[0].y
		switch kind {
		case "underline":
			y += size * 0.12
		case "overline":
			y -= size * 0.8
		case "line-through":
			y -= size * 0.26
		default:
			continue
		}
		var p raster.Path
		p.Rect(x0, y, x1-x0, max(size*0.06, 0.5))
		c := st.fill.color
		if st.fill.none || !r.spend(r.pathWork(&p, true, nil, ctm)) {
			continue
		}
		r.dev.FillPath(&p, false, ctm, gfx.DeviceRGB, c[:], st.fillOpacity, gfx.ColorParams{})
	}
}

// shiftBaseline moves glyphs off the baseline by dominant-baseline.
func shiftBaseline(g []glyph) {
	for i := range g {
		f := g[i].face
		if f == nil {
			continue
		}
		var dy float32
		switch strings.TrimSpace(g[i].st.baseline) {
		case "middle", "central":
			dy = xHeight(f) * g[i].size / 2
		case "hanging", "text-before-edge":
			dy = f.Ascent * g[i].size
		case "text-after-edge", "ideographic":
			dy = f.Descent * g[i].size
		default:
			continue
		}
		g[i].y += dy
	}
}

// xHeight is the height of a lower case x in the font, half the em when unknown.
func xHeight(f *font.Font) float32 {
	if f.XHeight > 0 {
		return f.XHeight
	}
	return 0.5
}

// drawGlyphs hands the glyphs to the device, one span per face and size.
func (r *runner) drawGlyphs(g []glyph, ctm raster.Matrix) {
	for i := 0; i < len(g); {
		j := i
		for j < len(g) && g[j].face == g[i].face && g[j].size == g[i].size &&
			g[j].turn == g[i].turn && g[j].st.fill == g[i].st.fill &&
			g[j].st.stroke == g[i].st.stroke {
			j++
		}
		run := g[i:j]
		st := g[i].st
		size := g[i].size
		trm := raster.Matrix{A: size, D: -size}
		if g[i].turn != 0 {
			trm = raster.Concat(trm, raster.Rotate(float64(g[i].turn)))
		}
		span := gfx.TextSpan{Font: gfx.FontOf(g[i].face), Trm: trm}
		for _, v := range run {
			if v.gid <= 0 {
				continue
			}
			span.Items = append(span.Items, gfx.TextItem{
				X: v.x, Y: v.y, GID: v.gid, Rune: v.r, Adv: v.adv / size,
			})
		}
		i = j
		if len(span.Items) == 0 {
			continue
		}
		t := &gfx.Text{Spans: []gfx.TextSpan{span}}
		box := textBounds(run)
		if !r.spend(glyphWork*int64(len(span.Items)) + reach(r.dev, ctm.ApplyRect(box))) {
			return
		}
		if !st.fill.none {
			at, m := paintSpace(st.fillCtx, box, ctm, st)
			sh, empty := r.server(st.fillServer, at, st)
			switch {
			case sh != nil:
				r.dev.ClipText(t, ctm, raster.InfiniteRect)
				r.dev.FillShade(sh, m, st.fillOpacity*sh.alpha(), gfx.ColorParams{})
				r.dev.PopClip()
			case empty:
			default:
				c := st.fill.color
				r.dev.FillText(t, ctm, gfx.DeviceRGB, c[:], st.fillOpacity, gfx.ColorParams{})
			}
		}
		if !st.stroke.none && st.width > 0 {
			s := r.strokeOf(st)
			at, m := paintSpace(st.strokeCtx, box, ctm, st)
			sh, empty := r.server(st.strokeServer, at, st)
			switch {
			case sh != nil:
				r.dev.ClipStrokeText(t, s, ctm, raster.InfiniteRect)
				r.dev.FillShade(sh, m, st.strokeOpacity*sh.alpha(), gfx.ColorParams{})
				r.dev.PopClip()
			case empty:
			default:
				c := st.stroke.color
				r.dev.StrokeText(t, s, ctm, gfx.DeviceRGB, c[:], st.strokeOpacity, gfx.ColorParams{})
			}
		}
	}
}

// textBounds is the box a run of glyphs covers, with the em box for the height.
func textBounds(g []glyph) raster.Rect {
	if len(g) == 0 {
		return raster.Rect{}
	}
	out := raster.EmptyRect
	for _, v := range g {
		asc, desc := float32(0.8), float32(-0.2)
		if v.face != nil {
			asc, desc = v.face.Ascent, v.face.Descent
		}
		out = out.AddPoint(raster.Point{X: v.x, Y: v.y - asc*v.size})
		out = out.AddPoint(raster.Point{X: v.x + v.adv, Y: v.y - desc*v.size})
	}
	return out
}

// face is the font a style asks for: the first family available, else one for the script.
func (r *runner) face(st state) *font.Font {
	key := faceKey{families: st.family, bold: st.bold, italic: st.italic}
	if f, ok := r.faces[key]; ok {
		return f
	}
	var f *font.Font
	for _, name := range strings.Split(st.family, ",") {
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		switch strings.ToLower(name) {
		case "":
			continue
		case "serif", "sans-serif", "monospace", "cursive", "fantasy":
			f = font.SystemFont(generic(name), st.bold, st.italic)
		default:
			if f = r.doc.embedded(name, st.bold, st.italic); f == nil {
				f = font.SystemFont(name, st.bold, st.italic)
			}
		}
		if f != nil {
			break
		}
	}
	if f == nil {
		f = font.Fallback('A', st.bold, st.italic)
	}
	if f == nil {
		f = font.Standard(svgBase14(st))
	}
	if r.faces == nil {
		r.faces = map[faceKey]*font.Font{}
	}
	r.faces[key] = f
	return f
}

// svgBase14 is the base-14 substitute for a style when no face is found.
func svgBase14(st state) string {
	name := "Helvetica"
	switch {
	case st.bold && st.italic:
		name += "-BoldOblique"
	case st.bold:
		name += "-Bold"
	case st.italic:
		name += "-Oblique"
	}
	return name
}

// fallback is a machine face that can draw ch when the requested one cannot.
func (r *runner) fallback(ch rune, st state) (*font.Font, int) {
	if ch == ' ' || ch < 0x20 {
		return nil, 0
	}
	key := fallbackKey{r: ch, bold: st.bold, italic: st.italic}
	if f, ok := r.fbs[key]; ok {
		if f == nil {
			return nil, 0
		}
		return f, f.GIDForRune(ch)
	}
	f := font.Fallback(ch, st.bold, st.italic)
	if f != nil && f.GIDForRune(ch) <= 0 {
		f = nil
	}
	if r.fbs == nil {
		r.fbs = map[fallbackKey]*font.Font{}
	}
	r.fbs[key] = f
	if f == nil {
		return nil, 0
	}
	return f, f.GIDForRune(ch)
}

type fallbackKey struct {
	r            rune
	bold, italic bool
}

// generic is a likely machine face for a CSS generic family name.
func generic(name string) string {
	switch name {
	case "serif":
		return "DejaVu Serif"
	case "monospace":
		return "DejaVu Sans Mono"
	}
	return "DejaVu Sans"
}

type faceKey struct {
	families     string
	bold, italic bool
}

// layout turns the characters of a text element into glyphs, UAX #9 per chunk.
func (r *runner) layout(cs []char) []glyph {
	for i := range cs {
		if f := cs[i].face; f != nil && f.GIDForRune(cs[i].r) <= 0 {
			if alt, _ := r.fallback(cs[i].r, cs[i].st); alt != nil {
				cs[i].face = alt
			}
		}
	}
	var out []glyph
	var pen, saved raster.Point
	along := false
	for i := 0; i < len(cs); {
		j := i
		for j < len(cs) && cs[j].chunk == cs[i].chunk {
			j++
		}
		if here := cs[i].path != nil; here != along {
			if here {
				saved, pen = pen, raster.Point{}
			} else {
				pen = saved
			}
			along = here
		}
		out = r.chunk(cs[i:j], &pen, i, out)
		i = j
	}
	return out
}

// chunk lays out one text-anchor chunk.
func (r *runner) chunk(cs []char, pen *raster.Point, base int, out []glyph) []glyph {
	if cs[0].hasX {
		pen.X = cs[0].ax
	}
	if cs[0].hasY {
		pen.Y = cs[0].ay
	}
	levels := chunkLevels(cs)
	type run struct {
		lo, hi int
		level  byte
	}
	var runs []run
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && levels[j] == levels[i] && sameRun(cs[j], cs[i]) {
			j++
		}
		runs = append(runs, run{i, j, levels[i]})
		i = j
	}
	runLevels := make([]byte, len(runs))
	for i, v := range runs {
		runLevels[i] = v.level
	}
	for _, k := range font.BidiOrder(runLevels) {
		v := runs[k]
		out = r.shaped(cs[v.lo:v.hi], v.level&1 != 0, pen, base+v.lo, out)
	}
	return out
}

// chunkLevels is the UAX #9 embedding level of every character of a chunk.
func chunkLevels(cs []char) []byte {
	rtl := cs[0].st.rtl
	text := make([]rune, len(cs))
	plain := true
	for i := range cs {
		text[i] = cs[i].r
		if cs[i].st.rtl || cs[i].st.override {
			plain = false
		}
	}
	var levels []byte
	if plain && !font.NeedsBidi(string(text)) {
		levels = make([]byte, len(cs))
	} else {
		levels = font.BidiLevels(text, boolInt(rtl))
	}
	for i := range cs {
		if cs[i].st.override {
			levels[i] = byte(boolInt(cs[i].st.rtl))
		}
	}
	return levels
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// sameRun reports whether one shaper call may cover a and b.
func sameRun(a, b char) bool {
	return a.face == b.face && a.st.em == b.st.em && a.st.vertical == b.st.vertical &&
		a.st.letter == b.st.letter && a.st.word == b.st.word && a.path == b.path
}

// placed is one shaped glyph: its character in the run, advance and offsets in text space.
type placed struct {
	gid, at    int
	adv        float32
	xoff, yoff float32
}

// shaped shapes one run and places its glyphs.
func (r *runner) shaped(cs []char, rtl bool, pen *raster.Point, base int, out []glyph) []glyph {
	f, st := cs[0].face, cs[0].st
	if f == nil {
		return out
	}
	text := make([]rune, len(cs))
	for i := range cs {
		text[i] = cs[i].r
		if rtl {
			text[i] = font.BidiMirror(text[i])
		}
	}
	scale := f.Matrix.A * st.em
	var gs []placed
	if !st.vertical && f.Shaped() && font.NeedsShaping(string(text)) {
		for _, sg := range f.Shape(text, rtl) {
			gs = append(gs, placed{gid: sg.GID, at: sg.Cluster,
				adv:  float32(sg.XAdvance) * scale,
				xoff: float32(sg.XOffset) * scale, yoff: float32(sg.YOffset) * scale})
		}
	}
	if gs == nil {
		gs = make([]placed, len(text))
		for i := range text {
			k := i
			if rtl {
				k = len(text) - 1 - i
			}
			gid := f.GIDForRune(text[k])
			gs[i] = placed{gid: gid, at: k, adv: f.Advance(gid) * scale}
		}
	}
	last := -1
	for _, sg := range gs {
		at := min(max(sg.at, 0), len(cs)-1)
		c := cs[at]
		if at != last {
			pen.X += c.dx
			pen.Y += c.dy
			last = at
		}
		adv := sg.adv + st.letter
		if c.r == ' ' {
			adv += st.word
		}
		g := glyph{
			r: c.r, gid: sg.gid, face: f, size: st.em, adv: adv,
			x: pen.X + sg.xoff, y: pen.Y - sg.yoff,
			chunk: c.chunk, turn: c.turn, path: c.path, vert: st.vertical,
			st: c.st, at: base + at,
		}
		if st.vertical {
			if font.Upright(c.r) {
				g.x -= adv / 2
				g.y += emBaseline(f) * st.em
				g.adv = st.em + st.letter
			} else {
				g.turn += 90
				g.x -= (f.Ascent + f.Descent) / 2 * st.em
			}
			g.x += c.st.shift
			pen.Y += g.adv
		} else {
			g.y += c.st.shift
			pen.X += adv
		}
		out = append(out, g)
	}
	return out
}
