package html

import (
	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// painter draws a laid out tree through a device. Everything it hands over is
// in CSS pixels; ctm is what takes those to the device.
type painter struct {
	dev gfx.Device
	ctm raster.Matrix
	// top and bottom bound the part of the column the page shows.
	top, bottom float32
	path        raster.Path
	// vertical is a page whose lines run down it.
	vertical bool
	errs     []error
}

func (p *painter) walk(b *box) {
	if b == nil {
		return
	}
	if b.reach > b.y && (b.y >= p.bottom || b.reach <= p.top) {
		return
	}
	if b.w > 0 && b.h > 0 {
		r, round := radii(b.style, b.w, b.h)
		if c := b.style.Background; c.A > 0 {
			if round {
				p.fillRound(b.x, b.y, b.w, b.h, r, c)
			} else {
				p.rect(b.x, b.y, b.w, b.h, c)
			}
		}
		if b.back != nil {
			p.backdrop(b, r, round)
		}
	}
	p.borders(b)
	if b.marker != "" {
		if ln := firstLine(b); ln != nil {
			p.marker(b, ln)
		}
	}
	for i := range b.lines {
		p.line(&b.lines[i])
	}
	// The children of a block with lines are the inline boxes the lines were
	// made of and draw nothing of their own, except a float, which was laid
	// out as a block beside them.
	for _, k := range b.kids {
		p.walk(k)
	}
}

// firstLine is the line a list item hangs its marker beside.
func firstLine(b *box) *lineBox {
	if len(b.lines) > 0 {
		return &b.lines[0]
	}
	for _, k := range b.kids {
		if ln := firstLine(k); ln != nil {
			return ln
		}
	}
	return nil
}

func (p *painter) line(ln *lineBox) {
	if ln.y >= p.bottom || ln.y+ln.h <= p.top {
		return
	}
	base := ln.y + ln.baseline
	mid := ln.y + ln.h/2
	for i := range ln.frags {
		f := &ln.frags[i]
		if f.vis != nil {
			p.visual(f.vis, f.x, base-f.dy-f.h, f.w, f.h)
			continue
		}
		p.text(f.text, f.face, f.style, f.x, base-f.dy, mid, f.extra)
	}
}

// marker draws what a list item carries before its first line, hanging in the
// padding the user agent sheet leaves for it.
func (p *painter) marker(b *box, ln *lineBox) {
	f := b.markerFace
	if f.prog == nil {
		return
	}
	w := f.width(b.marker)
	p.text(b.marker, f, b.style, b.x-w-f.size*0.4, ln.y+ln.baseline, ln.y+ln.h/2, 0)
}

func (p *painter) text(s string, f face, st *Style, x, y, mid, extra float32) {
	if s == "" || f.prog == nil {
		return
	}
	prog := substFont(f.prog)
	var spans []gfx.TextSpan
	span := gfx.TextSpan{Font: prog, Trm: raster.Matrix{A: f.size, D: -f.size}}
	up := false
	start := x
	for _, r := range s {
		adv := f.advance(r)
		if f.standsUp(r) != up && len(span.Items) > 0 {
			spans = append(spans, span)
			span = gfx.TextSpan{Font: prog, Trm: raster.Matrix{A: f.size, D: -f.size}}
		}
		up = f.standsUp(r)
		gx, gy := x, y
		if p.vertical {
			gy = mid + f.size/2
			if up {
				span.Trm = raster.Matrix{B: -f.size, C: -f.size}
				gx = x + f.ascent()
			} else {
				gy -= f.descent()
			}
		}
		if r == ' ' {
			adv += extra
		} else if gid := f.gid(r); gid > 0 {
			span.Items = append(span.Items, gfx.TextItem{
				X: gx, Y: gy, GID: gid, Rune: r, Adv: adv / f.size,
			})
		}
		x += adv
	}
	if len(span.Items) > 0 {
		spans = append(spans, span)
	}
	if len(spans) > 0 {
		t := &gfx.Text{Spans: spans}
		col, alpha := colorOf(st.Color)
		p.dev.FillText(t, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
	}
	if st.Decoration != 0 {
		p.decorate(st, f, start, x, y)
	}
}

// decorate draws the lines a style asks for through a run of text.
func (p *painter) decorate(st *Style, f face, x0, x1, y float32) {
	t := max(f.size*0.05, 0.5)
	if st.Decoration&Underline != 0 {
		p.rect(x0, y+f.size*0.12, x1-x0, t, st.Color)
	}
	if st.Decoration&Overline != 0 {
		p.rect(x0, y-f.ascent(), x1-x0, t, st.Color)
	}
	if st.Decoration&LineThrough != 0 {
		p.rect(x0, y-f.size*0.26, x1-x0, t, st.Color)
	}
}

// borders paints the four edges of a box: a ring when a corner is rounded,
// and otherwise rectangles that overlap at the corners rather than mitred,
// which shows only where two edges of different colours meet.
func (p *painter) borders(b *box) {
	if len(b.edges) > 0 {
		for _, e := range b.edges {
			p.edge(e.e, e.x, e.y, e.w, e.h, e.horizontal)
		}
		return
	}
	if b.skipBorders || b.w <= 0 || b.h <= 0 {
		return
	}
	s := b.style
	t, r := s.BorderTop.Thickness(), s.BorderRight.Thickness()
	bo, l := s.BorderBottom.Thickness(), s.BorderLeft.Thickness()
	if rad, round := radii(s, b.w, b.h); round && plainBorder(s) {
		p.roundBorder(b, rad)
		return
	}
	if t > 0 {
		p.edge(s.BorderTop, b.x, b.y, b.w, t, true)
	}
	if bo > 0 {
		p.edge(s.BorderBottom, b.x, b.y+b.h-bo, b.w, bo, true)
	}
	if l > 0 {
		p.edge(s.BorderLeft, b.x, b.y, l, b.h, false)
	}
	if r > 0 {
		p.edge(s.BorderRight, b.x+b.w-r, b.y, r, b.h, false)
	}
}

func (p *painter) edge(e Border, x, y, w, h float32, horizontal bool) {
	switch e.Style {
	case BorderDashed, BorderDotted:
		p.dashedEdge(e, x, y, w, h, horizontal)
	case BorderDouble:
		u := e.Thickness() / 3
		if horizontal {
			p.rect(x, y, w, u, e.Color)
			p.rect(x, y+h-u, w, u, e.Color)
			return
		}
		p.rect(x, y, u, h, e.Color)
		p.rect(x+w-u, y, u, h, e.Color)
	default:
		p.rect(x, y, w, h, e.Color)
	}
}

func (p *painter) dashedEdge(e Border, x, y, w, h float32, horizontal bool) {
	t := e.Thickness()
	on := t * 3
	if e.Style == BorderDotted {
		on = t
	}
	p.path.Reset()
	if horizontal {
		p.path.MoveTo(x, y+h/2)
		p.path.LineTo(x+w, y+h/2)
	} else {
		p.path.MoveTo(x+w/2, y)
		p.path.LineTo(x+w/2, y+h)
	}
	st := raster.DefaultStroke()
	st.Width, st.Dash = t, []float32{on, on}
	col, alpha := colorOf(e.Color)
	p.dev.StrokePath(&p.path, &st, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
}

// radius is one corner of a border box in CSS pixels, once it is resolved.
type radius struct{ X, Y float32 }

// radii resolves the four corner radii against a box and scales them down so
// that no edge is asked for more than its length, which is the rule of CSS
// backgrounds and borders. It reports whether any corner is rounded at all.
func radii(s *Style, w, h float32) ([4]radius, bool) {
	var out [4]radius
	round := false
	for i, c := range s.Radius {
		if c.Zero() {
			continue
		}
		out[i] = radius{max(c.X.Resolve(w), 0), max(c.Y.Resolve(h), 0)}
		round = round || out[i].X > 0 && out[i].Y > 0
	}
	if !round {
		return out, false
	}
	f := float32(1)
	for _, e := range [4][3]float32{
		{w, out[0].X, out[1].X}, {h, out[1].Y, out[2].Y},
		{w, out[3].X, out[2].X}, {h, out[0].Y, out[3].Y},
	} {
		if sum := e[1] + e[2]; sum > e[0] && sum > 0 {
			f = min(f, e[0]/sum)
		}
	}
	if f < 1 {
		for i := range out {
			out[i].X *= f
			out[i].Y *= f
		}
	}
	return out, true
}

// plainBorder reports the borders a rounded ring can be drawn for: every side
// solid or absent, which is what a book that rounds a corner writes.
func plainBorder(s *Style) bool {
	for _, e := range [4]Border{s.BorderTop, s.BorderRight, s.BorderBottom, s.BorderLeft} {
		switch e.Style {
		case BorderNone, BorderSolid:
		default:
			return false
		}
	}
	return true
}

// The distance along a tangent that turns a cubic into a quarter ellipse.
const kappa = 0.5522847498307936

// roundRect adds a rectangle with rounded corners to a path, clockwise from
// the top left.
func roundRect(path *raster.Path, x, y, w, h float32, r [4]radius) {
	x1, y1 := x+w, y+h
	path.MoveTo(x+r[0].X, y)
	path.LineTo(x1-r[1].X, y)
	if r[1].X > 0 && r[1].Y > 0 {
		path.CurveTo(x1-r[1].X+r[1].X*kappa, y, x1, y+r[1].Y-r[1].Y*kappa, x1, y+r[1].Y)
	}
	path.LineTo(x1, y1-r[2].Y)
	if r[2].X > 0 && r[2].Y > 0 {
		path.CurveTo(x1, y1-r[2].Y+r[2].Y*kappa, x1-r[2].X+r[2].X*kappa, y1, x1-r[2].X, y1)
	}
	path.LineTo(x+r[3].X, y1)
	if r[3].X > 0 && r[3].Y > 0 {
		path.CurveTo(x+r[3].X-r[3].X*kappa, y1, x, y1-r[3].Y+r[3].Y*kappa, x, y1-r[3].Y)
	}
	path.LineTo(x, y+r[0].Y)
	if r[0].X > 0 && r[0].Y > 0 {
		path.CurveTo(x, y+r[0].Y-r[0].Y*kappa, x+r[0].X-r[0].X*kappa, y, x+r[0].X, y)
	}
	path.Close()
}

// fillRound fills a rounded rectangle.
func (p *painter) fillRound(x, y, w, h float32, r [4]radius, c Color) {
	p.path.Reset()
	roundRect(&p.path, x, y, w, h, r)
	col, alpha := colorOf(c)
	p.dev.FillPath(&p.path, false, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
}

// roundBorder draws the ring between the border box and the padding box. Each
// side is clipped to the quadrilateral that runs between its two corners, so
// that four colours meet on the diagonals the way a browser draws them.
func (p *painter) roundBorder(b *box, r [4]radius) {
	s := b.style
	t, rt := s.BorderTop.Thickness(), s.BorderRight.Thickness()
	bo, l := s.BorderBottom.Thickness(), s.BorderLeft.Thickness()
	if t <= 0 && rt <= 0 && bo <= 0 && l <= 0 {
		return
	}
	x0, y0, x1, y1 := b.x, b.y, b.x+b.w, b.y+b.h
	inner := [4]radius{
		{max(r[0].X-l, 0), max(r[0].Y-t, 0)},
		{max(r[1].X-rt, 0), max(r[1].Y-t, 0)},
		{max(r[2].X-rt, 0), max(r[2].Y-bo, 0)},
		{max(r[3].X-l, 0), max(r[3].Y-bo, 0)},
	}
	ring := func() {
		p.path.Reset()
		roundRect(&p.path, x0, y0, b.w, b.h, r)
		if w, h := b.w-l-rt, b.h-t-bo; w > 0 && h > 0 {
			roundRect(&p.path, x0+l, y0+t, w, h, inner)
		}
	}

	sides := [4]struct {
		e    Border
		quad [4][2]float32
	}{
		{s.BorderTop, [4][2]float32{{x0, y0}, {x1, y0}, {x1 - rt, y0 + t}, {x0 + l, y0 + t}}},
		{s.BorderRight, [4][2]float32{{x1, y0}, {x1, y1}, {x1 - rt, y1 - bo}, {x1 - rt, y0 + t}}},
		{s.BorderBottom, [4][2]float32{{x1, y1}, {x0, y1}, {x0 + l, y1 - bo}, {x1 - rt, y1 - bo}}},
		{s.BorderLeft, [4][2]float32{{x0, y1}, {x0, y0}, {x0 + l, y0 + t}, {x0 + l, y1 - bo}}},
	}
	same := true
	for _, sd := range sides[1:] {
		same = same && sd.e.Color == sides[0].e.Color && sd.e.Thickness() == sides[0].e.Thickness()
	}
	if same {
		ring()
		col, alpha := colorOf(sides[0].e.Color)
		p.dev.FillPath(&p.path, true, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
		return
	}
	for _, sd := range sides {
		if sd.e.Thickness() <= 0 || sd.e.Color.A == 0 {
			continue
		}
		p.path.Reset()
		p.path.MoveTo(sd.quad[0][0], sd.quad[0][1])
		for _, q := range sd.quad[1:] {
			p.path.LineTo(q[0], q[1])
		}
		p.path.Close()
		p.dev.ClipPath(&p.path, false, p.ctm, raster.InfiniteRect)
		ring()
		col, alpha := colorOf(sd.e.Color)
		p.dev.FillPath(&p.path, true, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
		p.dev.PopClip()
	}
}

func (p *painter) rect(x, y, w, h float32, c Color) {
	if w <= 0 || h <= 0 || c.A == 0 {
		return
	}
	p.path.Reset()
	p.path.Rect(x, y, w, h)
	col, alpha := colorOf(c)
	p.dev.FillPath(&p.path, false, p.ctm, gfx.DeviceRGB, col, alpha, gfx.ColorParams{})
}

// backdrop paints the picture behind a box, tiled the way the style asks and
// clipped to the box itself.
func (p *painter) backdrop(b *box, r [4]radius, round bool) {
	s := b.style
	iw, ih := b.back.w, b.back.h
	if iw <= 0 || ih <= 0 {
		return
	}
	w, h := iw, ih
	switch {
	case !s.BackgroundW.Auto() && !s.BackgroundH.Auto():
		w, h = s.BackgroundW.Resolve(b.w), s.BackgroundH.Resolve(b.h)
	case !s.BackgroundW.Auto():
		w = s.BackgroundW.Resolve(b.w)
		h = ih * w / iw
	case !s.BackgroundH.Auto():
		h = s.BackgroundH.Resolve(b.h)
		w = iw * h / ih
	}
	if w <= 0 || h <= 0 {
		return
	}
	x := b.x + s.BackgroundX.Resolve(b.w-w)
	y := b.y + s.BackgroundY.Resolve(b.h-h)
	nx, ny := 1, 1
	if s.BackgroundRepeat == RepeatBoth || s.BackgroundRepeat == RepeatX {
		for x > b.x {
			x -= w
		}
		nx = int((b.x+b.w-x)/w) + 1
	}
	if s.BackgroundRepeat == RepeatBoth || s.BackgroundRepeat == RepeatY {
		for y > b.y {
			y -= h
		}
		ny = int((b.y+b.h-y)/h) + 1
	}
	if nx*ny > maxTiles || nx <= 0 || ny <= 0 {
		return
	}

	p.path.Reset()
	if round {
		roundRect(&p.path, b.x, b.y, b.w, b.h, r)
	} else {
		p.path.Rect(b.x, b.y, b.w, b.h)
	}
	p.dev.ClipPath(&p.path, false, p.ctm, raster.InfiniteRect)
	for i := range ny {
		for j := range nx {
			p.visual(b.back, x+float32(j)*w, y+float32(i)*h, w, h)
		}
	}
	p.dev.PopClip()
}

// maxTiles is how many copies of a picture one background may be painted
// with, which a picture a fraction of a pixel wide would otherwise run to.
const maxTiles = 1 << 14

// visual draws a picture or a drawing into a rectangle of the page. A
// drawing runs onto the device under a matrix that fits it to the rectangle,
// so it stays a drawing all the way down rather than becoming pixels here.
func (p *painter) visual(v *visual, x, y, w, h float32) {
	if w <= 0 || h <= 0 {
		return
	}
	if v.art != nil {
		if v.w <= 0 || v.h <= 0 {
			return
		}
		m := raster.Concat(raster.Matrix{A: w / v.w, D: h / v.h, E: x, F: y}, p.ctm)
		p.dev.ClipPath(p.box(x, y, w, h), false, p.ctm, raster.InfiniteRect)
		p.err(v.art.Run(p.dev, m))
		p.dev.PopClip()
		return
	}
	if v.pic == nil {
		return
	}
	m := raster.Concat(raster.Matrix{A: w, D: h, E: x, F: y}, p.ctm)
	p.dev.FillImage(v.pic, m, 1, gfx.ColorParams{})
}

// box is a rectangle of the page as a path, reusing the painter's own so a
// clip costs no allocation.
func (p *painter) box(x, y, w, h float32) *raster.Path {
	p.path.Reset()
	p.path.Rect(x, y, w, h)
	return &p.path
}

func (p *painter) err(err error) {
	if err != nil && len(p.errs) < 32 {
		p.errs = append(p.errs, err)
	}
}

func colorOf(c Color) ([]float32, float32) {
	return []float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255}, float32(c.A) / 255
}

// subst is a font program a device draws glyphs from, which is all a
// substitute face needs to be.

// substFont is gfx.FontOf, which wraps a program as a font a device can draw
// glyphs out of.
func substFont(prog *font.Font) gfx.Font { return gfx.FontOf(prog) }
