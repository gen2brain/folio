package html

import (
	"sync"

	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/gfx"
	"github.com/gen2brain/pdf/raster"
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
		p.rect(b.x, b.y, b.w, b.h, c)
	}
	if len(b.lines) > 0 {
		if b.marker != "" {
			p.marker(b, &b.lines[0])
		}
		for i := range b.lines {
			p.line(&b.lines[i])
		}
		return
	}
	if b.marker != "" {
		if ln := firstLine(b); ln != nil {
			p.marker(b, ln)
		}
	}
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
	f := styleFace(b.style)
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
		} else if gid := f.prog.GIDForRune(r); gid > 0 {
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
