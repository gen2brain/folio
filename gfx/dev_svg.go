package gfx

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"strconv"

	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/raster"
)

// SVGDevice writes what a page draws as SVG. Paths, text and images come out
// as themselves; a shading and a soft mask are rasterized, because SVG has no
// equivalent of either that survives the round trip.
type SVGDevice struct {
	BaseDevice
	w   io.Writer
	box raster.Rect

	// defs holds the glyph outlines and clip paths, which have to be written
	// before what refers to them, so the body is buffered until Close.
	defs, body bytes.Buffer
	glyphs     map[glyphRef]string
	// open counts the groups a clip, a layer or a transparency group has
	// opened, so that Close balances what a damaged page left open.
	open int
	ids  int
	err  error
}

type glyphRef struct {
	font *font.Font
	gid  int
}

// NewSVGDevice returns a device that writes the page bounded by box as SVG
// into w. Nothing reaches w until Close.
func NewSVGDevice(w io.Writer, box raster.Rect) *SVGDevice {
	return &SVGDevice{w: w, box: box, glyphs: map[glyphRef]string{}}
}

// Close implements Device. It writes the document.
func (d *SVGDevice) Close() error {
	for ; d.open > 0; d.open-- {
		d.body.WriteString("</g>\n")
	}
	if d.err != nil {
		return d.err
	}
	w, h := d.box.X1-d.box.X0, d.box.Y1-d.box.Y0
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"`+
		` version="1.1" width="%s" height="%s" viewBox="%s %s %s %s">`+"\n",
		num(w), num(h), num(d.box.X0), num(d.box.Y0), num(w), num(h))
	if d.defs.Len() > 0 {
		b.WriteString("<defs>\n")
		b.Write(d.defs.Bytes())
		b.WriteString("</defs>\n")
	}
	b.Write(d.body.Bytes())
	b.WriteString("</svg>\n")
	_, err := d.w.Write(b.Bytes())
	return err
}

// FillPath implements Device.
func (d *SVGDevice) FillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.body.WriteString(`<path d="` + pathData(p, ctm) + `"`)
	d.paint("fill", cs, color, alpha)
	if evenOdd {
		d.body.WriteString(` fill-rule="evenodd"`)
	}
	d.body.WriteString("/>\n")
}

// StrokePath implements Device.
func (d *SVGDevice) StrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.body.WriteString(`<path d="` + pathData(p, ctm) + `" fill="none"`)
	d.paint("stroke", cs, color, alpha)
	d.strokeAttrs(s, ctm.Expansion())
	d.body.WriteString("/>\n")
}

// ClipPath implements Device.
func (d *SVGDevice) ClipPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect) {
	rule := ""
	if evenOdd {
		rule = ` clip-rule="evenodd"`
	}
	d.clipGroup(`<path d="` + pathData(p, ctm) + `"` + rule + `/>`)
}

// ClipStrokePath implements Device. A stroke is not a clip shape in SVG, so
// what it bounds is used instead.
func (d *SVGDevice) ClipStrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.clipRect(p.StrokeBounds(s, ctm))
}

// FillText implements Device.
func (d *SVGDevice) FillText(t *Text, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.eachGlyph(t, ctm, func(id string, m raster.Matrix) {
		d.body.WriteString(`<use xlink:href="#` + id + `" transform="` + matrix(m) + `"`)
		d.paint("fill", cs, color, alpha)
		d.body.WriteString("/>\n")
	}, cs, color, alpha)
}

// StrokeText implements Device.
func (d *SVGDevice) StrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.eachGlyph(t, ctm, func(id string, m raster.Matrix) {
		d.body.WriteString(`<use xlink:href="#` + id + `" transform="` + matrix(m) + `" fill="none"`)
		d.paint("stroke", cs, color, alpha)
		d.strokeAttrs(s, ctm.Expansion())
		d.body.WriteString("/>\n")
	}, cs, color, alpha)
}

// ClipText implements Device.
func (d *SVGDevice) ClipText(t *Text, ctm raster.Matrix, scissor raster.Rect) {
	var b bytes.Buffer
	d.eachGlyph(t, ctm, func(id string, m raster.Matrix) {
		b.WriteString(`<use xlink:href="#` + id + `" transform="` + matrix(m) + `"/>`)
	}, nil, nil, 1)
	d.clipGroup(b.String())
}

// ClipStrokeText implements Device.
func (d *SVGDevice) ClipStrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.ClipText(t, ctm, scissor)
}

// FillImage implements Device.
func (d *SVGDevice) FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	px, err := img.Pixels(DeviceRGB, 0)
	if err != nil || px == nil {
		return
	}
	d.image(px, ctm, alpha)
}

// FillImageMask implements Device. A stencil paints one color through its
// samples, which SVG has no form of, so it is filled here.
func (d *SVGDevice) FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	px, err := img.Pixels(nil, 0)
	if err != nil || px == nil {
		return
	}
	var rgb [3]uint8
	cs.Convert(color, rgb[:])
	out := raster.NewPixmap(raster.ModelRGB, px.W, px.H, true)
	if out == nil {
		return
	}
	for y := 0; y < px.H; y++ {
		src, dst := px.Row(y), out.Row(y)
		for x := 0; x < px.W; x++ {
			a := src[x]
			dst[x*4] = mul(rgb[0], a)
			dst[x*4+1] = mul(rgb[1], a)
			dst[x*4+2] = mul(rgb[2], a)
			dst[x*4+3] = a
		}
	}
	d.image(out, ctm, alpha)
}

// ClipImageMask implements Device. What the stencil covers is used instead of
// the stencil, which SVG cannot clip to.
func (d *SVGDevice) ClipImageMask(img Image, ctm raster.Matrix, scissor raster.Rect) {
	d.clipRect(ctm.ApplyRect(raster.Rect{X1: 1, Y1: 1}))
}

// FillShade implements Device. SVG has two of the seven kinds of shading and
// neither of the two under an arbitrary transform, so it is rasterized.
func (d *SVGDevice) FillShade(sh Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	m := raster.Concat(sh.Transform(), ctm)
	box := d.box
	if b := sh.Bounds(); !b.IsEmpty() {
		box = box.Intersect(m.ApplyRect(b))
	}
	x0, y0, x1, y1 := box.Outer()
	if x1 <= x0 || y1 <= y0 {
		return
	}
	s := sh.Shader(raster.ModelRGB, m, box)
	if s == nil {
		return
	}
	px := raster.NewPixmap(raster.ModelRGB, x1-x0, y1-y0, true)
	if px == nil {
		return
	}
	px.X, px.Y = x0, y0
	for y := y0; y < y1; y++ {
		s.Shade(x0, y, x1-x0, px.Row(y-y0))
	}
	d.image(px, raster.Matrix{
		A: float32(x1 - x0), D: float32(y1 - y0),
		E: float32(x0), F: float32(y0),
	}, alpha)
}

// BeginGroup implements Device.
func (d *SVGDevice) BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32) {
	d.body.WriteString(`<g opacity="` + num(alpha) + `"`)
	if blend != BlendNormal {
		d.body.WriteString(` style="mix-blend-mode:` + blend.String() + `"`)
	}
	d.body.WriteString(">\n")
	d.open++
}

// EndGroup implements Device.
func (d *SVGDevice) EndGroup() { d.closeGroup() }

// BeginMask implements Device. What a soft mask group draws is the mask and
// not the page, so nothing of it is written; the group it opens is kept so
// that the clip it becomes still pops.
func (d *SVGDevice) BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams) {
	d.clipRect(area)
}

// EndMask implements Device.
func (d *SVGDevice) EndMask(transfer *[256]uint8) {}

// BeginTile implements Device. Returning zero asks for every repetition to be
// drawn, which is what a file with no pattern element in it needs.
func (d *SVGDevice) BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int {
	d.clipRect(area)
	return 0
}

// EndTile implements Device.
func (d *SVGDevice) EndTile() { d.closeGroup() }

// BeginLayer implements Device.
func (d *SVGDevice) BeginLayer(name string) {
	d.body.WriteString(`<g id="layer_` + strconv.Itoa(d.nextID()) + `">` + "\n")
	d.open++
}

// EndLayer implements Device.
func (d *SVGDevice) EndLayer() { d.closeGroup() }

// PopClip implements Device.
func (d *SVGDevice) PopClip() { d.closeGroup() }

func (d *SVGDevice) closeGroup() {
	if d.open > 0 {
		d.open--
		d.body.WriteString("</g>\n")
	}
}

func (d *SVGDevice) nextID() int { d.ids++; return d.ids }

// clipGroup opens a group clipped to the shape, which the caller has already
// written as SVG elements.
func (d *SVGDevice) clipGroup(shape string) {
	id := "clip_" + strconv.Itoa(d.nextID())
	d.defs.WriteString(`<clipPath id="` + id + `">` + shape + "</clipPath>\n")
	d.body.WriteString(`<g clip-path="url(#` + id + `)">` + "\n")
	d.open++
}

func (d *SVGDevice) clipRect(r raster.Rect) {
	var p raster.Path
	p.Rect(r.X0, r.Y0, r.X1-r.X0, r.Y1-r.Y0)
	d.clipGroup(`<path d="` + pathData(&p, raster.Identity) + `"/>`)
}

// paint writes the fill or stroke color and its opacity.
func (d *SVGDevice) paint(kind string, cs *ColorSpace, color []float32, alpha float32) {
	var rgb [3]uint8
	cs.Convert(color, rgb[:])
	fmt.Fprintf(&d.body, ` %s="#%02x%02x%02x"`, kind, rgb[0], rgb[1], rgb[2])
	if alpha < 1 {
		d.body.WriteString(` ` + kind + `-opacity="` + num(alpha) + `"`)
	}
}

func (d *SVGDevice) strokeAttrs(s *raster.Stroke, scale float32) {
	w := s.Width * scale
	if w <= 0 {
		w = 1
	}
	d.body.WriteString(` stroke-width="` + num(w) + `"`)
	switch s.StartCap {
	case raster.CapRound:
		d.body.WriteString(` stroke-linecap="round"`)
	case raster.CapSquare:
		d.body.WriteString(` stroke-linecap="square"`)
	}
	switch s.Join {
	case raster.JoinRound:
		d.body.WriteString(` stroke-linejoin="round"`)
	case raster.JoinBevel:
		d.body.WriteString(` stroke-linejoin="bevel"`)
	default:
		d.body.WriteString(` stroke-miterlimit="` + num(s.MiterLimit) + `"`)
	}
	if len(s.Dash) > 0 {
		d.body.WriteString(` stroke-dasharray="`)
		for i, v := range s.Dash {
			if i > 0 {
				d.body.WriteByte(' ')
			}
			d.body.WriteString(num(v * scale))
		}
		d.body.WriteString(`" stroke-dashoffset="` + num(s.DashPhase*scale) + `"`)
	}
}

// eachGlyph writes every glyph of a text object through fn, defining the
// outline the first time it is used. A font that has no program draws its own
// glyphs, which for SVG means running them into this device.
func (d *SVGDevice) eachGlyph(t *Text, ctm raster.Matrix, fn func(id string, m raster.Matrix), cs *ColorSpace, color []float32, alpha float32) {
	for i := range t.Spans {
		sp := &t.Spans[i]
		if sp.Font == nil {
			continue
		}
		prog := sp.Font.Program()
		for _, it := range sp.Items {
			if it.GID < 0 {
				continue
			}
			m := raster.Concat(raster.Matrix{
				A: sp.Trm.A, B: sp.Trm.B, C: sp.Trm.C, D: sp.Trm.D,
				E: it.X, F: it.Y,
			}, ctm)
			if prog == nil {
				sp.Font.RunGlyph(d, it.GID, m, cs, color, alpha, 0)
				continue
			}
			id, ok := d.glyph(prog, it.GID)
			if !ok {
				continue
			}
			fn(id, raster.Concat(prog.Matrix, m))
		}
	}
}

// glyph defines a glyph outline once and returns what refers to it.
func (d *SVGDevice) glyph(prog *font.Font, gid int) (string, bool) {
	key := glyphRef{prog, gid}
	if id, ok := d.glyphs[key]; ok {
		return id, id != ""
	}
	p := prog.GlyphPath(gid)
	if p == nil {
		d.glyphs[key] = ""
		return "", false
	}
	id := "glyph_" + strconv.Itoa(d.nextID())
	d.glyphs[key] = id
	d.defs.WriteString(`<path id="` + id + `" d="` + pathData(p, raster.Identity) + `"/>` + "\n")
	return id, true
}

// image writes a pixmap as a PNG under the transform that maps the unit
// square onto the page.
func (d *SVGDevice) image(px *raster.Pixmap, ctm raster.Matrix, alpha float32) {
	img := toNRGBA(px)
	if img == nil {
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		d.err = err
		return
	}
	d.body.WriteString(`<image width="1" height="1" preserveAspectRatio="none" transform="` + matrix(ctm) + `"`)
	if alpha < 1 {
		d.body.WriteString(` opacity="` + num(alpha) + `"`)
	}
	d.body.WriteString(` xlink:href="data:image/png;base64,` +
		base64.StdEncoding.EncodeToString(buf.Bytes()) + `"/>` + "\n")
}

// toNRGBA converts a pixmap to what the PNG encoder takes, undoing the
// premultiplication a pixmap with alpha stores its colors in.
func toNRGBA(px *raster.Pixmap) *image.NRGBA {
	if px.N != 3 {
		return nil
	}
	out := image.NewNRGBA(image.Rect(0, 0, px.W, px.H))
	n := px.Comps()
	for y := 0; y < px.H; y++ {
		src, dst := px.Row(y), out.Pix[y*out.Stride:]
		for x := 0; x < px.W; x++ {
			a := uint8(255)
			if px.Alpha {
				a = src[x*n+3]
			}
			dst[x*4] = unmul(src[x*n], a)
			dst[x*4+1] = unmul(src[x*n+1], a)
			dst[x*4+2] = unmul(src[x*n+2], a)
			dst[x*4+3] = a
		}
	}
	return out
}

func mul(v, a uint8) uint8 {
	t := uint32(v)*uint32(a) + 128
	return uint8((t + t>>8) >> 8)
}

func unmul(v, a uint8) uint8 {
	if a == 0 || a == 255 {
		return v
	}
	if t := uint32(v) * 255 / uint32(a); t < 255 {
		return uint8(t)
	}
	return 255
}

// pathWriter turns a path into the d attribute of an SVG path element.
type pathWriter struct{ b bytes.Buffer }

func (w *pathWriter) MoveTo(x, y float32) { w.b.WriteString("M" + num(x) + " " + num(y)) }
func (w *pathWriter) LineTo(x, y float32) { w.b.WriteString("L" + num(x) + " " + num(y)) }
func (w *pathWriter) Close()              { w.b.WriteString("Z") }

func (w *pathWriter) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	w.b.WriteString("C" + num(x1) + " " + num(y1) + " " + num(x2) + " " + num(y2) +
		" " + num(x3) + " " + num(y3))
}

func pathData(p *raster.Path, m raster.Matrix) string {
	var w pathWriter
	if m == raster.Identity {
		p.Walk(&w)
	} else {
		p.Transform(m).Walk(&w)
	}
	return w.b.String()
}

func matrix(m raster.Matrix) string {
	return "matrix(" + num(m.A) + "," + num(m.B) + "," + num(m.C) + "," +
		num(m.D) + "," + num(m.E) + "," + num(m.F) + ")"
}

func num(v float32) string { return strconv.FormatFloat(float64(v), 'g', 8, 32) }
