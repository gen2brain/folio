package html

import (
	"sync"

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
}

func (p *painter) walk(b *box) {
	if b == nil {
		return
	}
	if b.h > 0 && (b.y >= p.bottom || b.y+b.h <= p.top) {
		return
	}
	if c := b.style.Background; c.A > 0 && b.w > 0 && b.h > 0 {
		if r, round := radii(b.style, b.w, b.h); round {
			p.fillRound(b.x, b.y, b.w, b.h, r, c)
		} else {
			p.rect(b.x, b.y, b.w, b.h, c)
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
	for i := range ln.frags {
		f := &ln.frags[i]
		if f.img != nil {
			p.image(f.img, f.x, base-f.h, f.w, f.h)
			continue
		}
		p.text(f.text, f.face, f.style, f.x, base, f.extra)
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
	p.text(b.marker, f, b.style, b.x-w-f.size*0.4, ln.y+ln.baseline, 0)
}

func (p *painter) text(s string, f face, st *Style, x, y, extra float32) {
	if s == "" || f.prog == nil {
		return
	}
	span := gfx.TextSpan{
		Font: substFont(f.prog),
		Trm:  raster.Matrix{A: f.size, D: -f.size},
	}
	start := x
	for _, r := range s {
		adv := f.advance(r)
		if r == ' ' {
			adv += extra
		} else if gid := f.gid(r); gid > 0 {
			span.Items = append(span.Items, gfx.TextItem{
				X: x, Y: y, GID: gid, Rune: r, Adv: adv / f.size,
			})
		}
		x += adv
	}
	if len(span.Items) > 0 {
		t := &gfx.Text{Spans: []gfx.TextSpan{span}}
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
	if b.w <= 0 || b.h <= 0 {
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

func (p *painter) image(pic *picture, x, y, w, h float32) {
	if w <= 0 || h <= 0 {
		return
	}
	m := raster.Concat(raster.Matrix{A: w, D: h, E: x, F: y}, p.ctm)
	p.dev.FillImage(pic, m, 1, gfx.ColorParams{})
}

func colorOf(c Color) ([]float32, float32) {
	return []float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255}, float32(c.A) / 255
}

// subst is a font program a device draws glyphs from, which is all a
// substitute face needs to be.
type subst struct{ prog *font.Font }

// FontName implements gfx.Font.
func (f *subst) FontName() string { return f.prog.Name }

// Program implements gfx.Font.
func (f *subst) Program() *font.Font { return f.prog }

// EmBox implements gfx.Font.
func (f *subst) EmBox() (float32, float32) { return f.prog.Ascent, f.prog.Descent }

// RunGlyph implements gfx.Font. A substitute always has a program, so there
// is nothing for it to run.
func (f *subst) RunGlyph(gfx.Device, int, raster.Matrix, *gfx.ColorSpace, []float32, float32, int) {}

var substCache sync.Map

func substFont(prog *font.Font) gfx.Font {
	if v, ok := substCache.Load(prog); ok {
		return v.(*subst)
	}
	v, _ := substCache.LoadOrStore(prog, &subst{prog: prog})
	return v.(*subst)
}
