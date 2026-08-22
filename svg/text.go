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
	st   state
}

// textCursor is where the next glyph goes and which chunk it is in.
type textCursor struct {
	x, y   float32
	chunk  int
	glyphs []glyph
	// space says a collapsible space is owed before the next character.
	space bool
	begun bool
	// rotate is the list a text element may turn each of its characters by,
	// and rotateAt how many of them have been used. The last one stands for
	// every character after it, SVG 1.1 10.4.
	rotate   []float32
	rotateAt int
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
	c.glyphs = trimTrailing(c.glyphs)
	if len(c.glyphs) == 0 {
		return
	}
	if v, ok := length(r.prop(n, "textLength"), st.vw, st.em); ok && v > 0 {
		fitLength(c.glyphs, v)
	}
	r.anchorChunks(c.glyphs)
	shiftBaseline(c.glyphs)
	r.drawGlyphs(c.glyphs, ctm)
	r.decorate(c.glyphs, ctm)
}

// trimTrailing drops the space a text element ends with, which SVG 1.1 10.15
// removes and the eager collapse below has left with nothing after it.
func trimTrailing(g []glyph) []glyph {
	for len(g) > 0 {
		last := g[len(g)-1]
		if last.r != ' ' || last.st.preserve {
			break
		}
		g = g[:len(g)-1]
	}
	return g
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
	xs := numbers(r.prop(n, "x"))
	ys := numbers(r.prop(n, "y"))
	dxs := numbers(r.prop(n, "dx"))
	dys := numbers(r.prop(n, "dy"))
	if len(xs) > 0 || len(ys) > 0 {
		if len(xs) > 0 {
			c.x = xs[0]
		}
		if len(ys) > 0 {
			c.y = ys[0]
		}
		c.chunk++
	}
	if len(dxs) > 0 {
		c.x += dxs[0]
	}
	if len(dys) > 0 {
		c.y += dys[0]
	}
	if v := numbers(r.prop(n, "rotate")); len(v) > 0 {
		c.rotate = v
		c.rotateAt = 0
	}

	face := r.face(st)
	i := 0
	for _, k := range n.kids {
		switch k.name {
		case "":
			i = r.place(c, k.chars, st, face, xs, ys, dxs, dys, i)
		case "tspan", "textPath", "a":
			r.textRun(k, st, c, depth+1, false)
		}
	}
}

// place lays out one run of characters, taking the per glyph positions the
// element carried lists of. i is how many of those have been used.
func (r *runner) place(c *textCursor, s string, st state, face *font.Font, xs, ys, dxs, dys []float32, i int) int {
	if face == nil {
		return i
	}
	for _, ch := range collapse(s, st.preserve, &c.space, &c.begun) {
		if i < len(xs) {
			c.x = xs[i]
			c.chunk++
		}
		if i < len(ys) {
			c.y = ys[i]
		}
		if i < len(dxs) {
			c.x += dxs[i]
		}
		if i < len(dys) {
			c.y += dys[i]
		}
		f := face
		gid := f.GIDForRune(ch)
		if gid <= 0 {
			if alt, g := r.fallback(ch, st); alt != nil {
				f, gid = alt, g
			}
		}
		// Advance is in the glyph space of the program, which its matrix
		// takes to the text space one unit of the font size is.
		adv := f.Advance(gid)*f.Matrix.A*st.em + st.letter
		if ch == ' ' {
			adv += st.word
		}
		turn := float32(0)
		if n := len(c.rotate); n > 0 {
			turn = c.rotate[min(c.rotateAt, n-1)]
			c.rotateAt++
		}
		c.glyphs = append(c.glyphs, glyph{
			r: ch, gid: gid, face: f, size: st.em, adv: adv,
			x: c.x, y: c.y, chunk: c.chunk, turn: turn, st: st,
		})
		c.x += adv
		i++
	}
	return i
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
			sh, empty := r.server(st.fillServer, box, st)
			switch {
			case sh != nil:
				r.dev.ClipText(t, ctm, raster.InfiniteRect)
				r.dev.FillShade(sh, ctm, st.fillOpacity*sh.alpha(), gfx.ColorParams{})
				r.dev.PopClip()
			case empty:
			default:
				c := st.fill.color
				r.dev.FillText(t, ctm, gfx.DeviceRGB, c[:], st.fillOpacity, gfx.ColorParams{})
			}
		}
		if !st.stroke.none && st.width > 0 {
			s := r.strokeOf(st)
			sh, empty := r.server(st.strokeServer, box, st)
			switch {
			case sh != nil:
				r.dev.ClipStrokeText(t, s, ctm, raster.InfiniteRect)
				r.dev.FillShade(sh, ctm, st.strokeOpacity*sh.alpha(), gfx.ColorParams{})
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
