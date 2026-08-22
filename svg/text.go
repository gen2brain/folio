package svg

import (
	"strings"
	"unicode"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// glyph is one character placed by a text element, with the face it came out
// of and how far the pen moves after it.
type glyph struct {
	r    rune
	gid  int
	face *font.Font
	size float32
	adv  float32
	x, y float32
	// chunk is which run of text the glyph belongs to, which is what
	// text-anchor moves as a whole. A new one starts wherever an absolute
	// position is given, SVG 1.1 10.5.
	chunk int
	// turn is how far the rotate attribute turns the character, in degrees
	// clockwise about where it sits.
	turn float32
	// path is the one a textPath lays the character along, on which x is the
	// distance travelled and y the offset from it.
	path *pathWalk
	// vert says the character was placed down the page rather than across it.
	vert bool
	// at is which character of the element the glyph came out of, which is
	// what textLength is measured over.
	at int
	st state
}

// char is one character of a text element before its glyphs are known: what
// it is, the style it is drawn in, and where the element asked for it. The
// glyphs come later, because a script that joins is shaped a run at a time
// and the bidirectional algorithm orders a whole chunk at once.
type char struct {
	r    rune
	st   state
	face *font.Font
	// ax and ay are an absolute position, which is where the character
	// starts, and dx and dy a move from wherever the pen had reached.
	ax, ay     float32
	hasX, hasY bool
	dx, dy     float32
	turn       float32
	chunk      int
	path       *pathWalk
}

// textCursor is what the walk over a text element collects: the characters in
// the order they were written, and the position the next one takes.
type textCursor struct {
	chars []char
	chunk int
	// setX and setY are a position the next character starts at, which an x
	// or a y attribute and a textPath's start offset give it.
	setX, setY float32
	hasX, hasY bool
	// path is the textPath the characters are being laid along, if any.
	path *pathWalk
	// space says a collapsible space is owed before the next character.
	space bool
	begun bool
	// rotate is the list a text element may turn each of its characters by,
	// and rotateAt how many of them have been used. The last one stands for
	// every character after it, SVG 1.1 10.4.
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
	// The element's own style is already in st: applying it again would read
	// a relative font size twice.
	r.textRun(n, st, c, 0, true)
	g := r.laidOut(c)
	if len(g) == 0 {
		return
	}
	r.drawGlyphs(g, ctm)
	r.decorate(g, ctm)
}

// laidOut turns what the walk collected into the glyphs that draw it, in the
// order they are drawn and where they belong.
func (r *runner) laidOut(c *textCursor) []glyph {
	c.chars = trimTrailing(c.chars)
	if len(c.chars) == 0 {
		return nil
	}
	g := r.layout(c.chars)
	for _, f := range c.fits {
		lo, hi := glyphRange(g, f.from, f.to)
		if lo < hi {
			fitLength(g[lo:hi], f.want)
		}
	}
	r.anchorChunks(g)
	shiftBaseline(g)
	return onPath(g)
}

// glyphRange is the glyphs a range of characters produced. Ordering a chunk
// keeps the characters of one element together, so the range is contiguous.
func glyphRange(g []glyph, from, to int) (int, int) {
	lo, hi := len(g), 0
	for i := range g {
		if g[i].at >= from && g[i].at < to {
			lo, hi = min(lo, i), max(hi, i+1)
		}
	}
	return lo, hi
}

// trimTrailing drops the space a text element ends with, which SVG 1.1 10.15
// removes and the eager collapse below has left with nothing after it.
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

// textRun walks one element of a text, placing its characters and following
// its tspans.
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
	// An absolute position starts a new chunk, and a relative one only moves
	// the pen.
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
	// textLength stretches what the element itself holds, so a tspan with one
	// of its own is fitted before the text around it is.
	if v, ok := length(r.prop(n, "textLength"), st.vw, st.em); ok && v > 0 {
		c.fits = append(c.fits, fit{first, len(c.chars), v})
	}
}

// place records one run of characters, taking the per character positions the
// element carried lists of. i is how many of those have been used.
func (r *runner) place(c *textCursor, s string, st state, face *font.Font, xs, ys, dxs, dys []float32, i int) int {
	if face == nil {
		return i
	}
	for _, ch := range collapse(s, st.preserve, &c.space, &c.begun) {
		// A character given a position of its own starts a chunk, whichever
		// of the two axes it was given, SVG 1.1 10.5.
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

// emBaseline is how far below the top of the em box the baseline sits, which
// is what puts an upright character in the middle of the line it runs down.
// A face whose ascent and descent reach past the em, as a CJK face does, must
// not push the character out of its box.
func emBaseline(f *font.Font) float32 {
	h := f.Ascent - f.Descent
	if h <= 0 {
		return 0.8
	}
	return f.Ascent / h
}

// collapse turns the white space of a text element into the single spaces
// xml:space default asks for, SVG 1.1 10.15. Preserved white space keeps
// every character, with the ones that are not a space becoming one.
//
// The space a run of white space collapses to belongs to the run it was
// written in, not to whatever comes next. Between two tspans that is the text
// element itself, and a space held over until the next one would take the
// first of the positions that tspan lists for its own characters.
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

// anchorChunks moves each run of text so that the point it was given sits
// where text-anchor says: at its start, its middle or its end.
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

// fitLength spreads or squeezes a run so that it measures what textLength
// asks for. Only the spacing between the glyphs is adjusted, which is what
// lengthAdjust asks for by default; the other value stretches the glyphs
// themselves and gets the spacing instead.
func fitLength(g []glyph, want float32) {
	if len(g) == 0 {
		return
	}
	have := float32(0)
	for _, v := range g {
		have += v.adv
	}
	// The length is measured to the end of the last glyph, so what is spread
	// is the gaps between them.
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

// decorate draws the line text-decoration asks for under or through a run,
// which the glyphs themselves carry no mark for.
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
		// A line drawn under a run that goes down the page or along a path
		// would have to follow it, which this does not do.
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
		if st.fill.none {
			continue
		}
		r.dev.FillPath(&p, false, ctm, gfx.DeviceRGB, c[:], st.fillOpacity, gfx.ColorParams{})
	}
}

// shiftBaseline moves a glyph off the baseline the way dominant-baseline
// asks, which is what centers a label on the point it was given.
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

// xHeight is how tall a lower case x is in the program, and half the em for
// one that does not say.
func xHeight(f *font.Font) float32 {
	if f.XHeight > 0 {
		return f.XHeight
	}
	return 0.5
}

// drawGlyphs hands the placed characters to the device, one span per face and
// size, and fills and strokes them the way a shape is.
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

// textBounds is the box a run of glyphs covers, which is what a gradient in
// the units of the object is a fraction of. The em box stands for the height,
// because measuring every outline to paint one gradient is not worth it.
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

// face is the program a style asks for: the first family the machine has, and
// whatever it has for the script otherwise.
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
			// A face the drawing brings with it comes before one the
			// machine has under the same name.
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
	if r.faces == nil {
		r.faces = map[faceKey]*font.Font{}
	}
	r.faces[key] = f
	return f
}

// fallback is a face the machine has that can draw a character the one the
// drawing asked for cannot, which is what keeps a word whole when a book
// brings a font that covers only part of it.
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

// generic is a face the machine is likely to have for one of the five family
// names every document may use.
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

// layout turns the characters of a text element into the glyphs that draw
// them. Each chunk is ordered on its own by the bidirectional algorithm of
// UAX #9, the runs it falls into are shaped by the font that draws them, and
// the pen walks them in the order they are drawn.
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
		// The characters a textPath holds are measured along it, so the pen
		// the text element had reached is put back when the path ends.
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

// chunk lays out one run of text that text-anchor moves as a whole. Only its
// first character carries a position of its own, because one is what starts a
// chunk; the axis it says nothing about carries on from where the pen was.
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

// chunkLevels is the embedding level of every character of a chunk. A run
// that says so is laid out in the direction it names whatever it holds, which
// is what unicode-bidi asks for.
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

// sameRun reports two characters one call to the shaper may cover: the same
// program at the same size, drawn the same way along the same line.
func sameRun(a, b char) bool {
	return a.face == b.face && a.st.em == b.st.em && a.st.vertical == b.st.vertical &&
		a.st.letter == b.st.letter && a.st.word == b.st.word && a.path == b.path
}

// placed is one glyph on its way out of the shaper: which character of the
// run it came from, and its advance and offsets in text space.
type placed struct {
	gid, at    int
	adv        float32
	xoff, yoff float32
}

// shaped turns one run into glyphs and places them. The shaper hands back the
// glyphs of a right to left run in the order they are drawn, and a run it has
// nothing to say about is one glyph per character.
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
			// A line running down the page advances by the em where the
			// character stands upright and by the character's own width
			// where it turns with the line. Either way the em box is
			// centered on the line, SVG 1.1 10.7.
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
