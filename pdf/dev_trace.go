package pdf

import (
	"bufio"
	"fmt"
	"io"
	"strconv"

	"github.com/gen2brain/pdf/raster"
)

// TraceDevice writes every device call as XML, in the format mutool trace
// produces. Comparing the two is how the interpreter is held to MuPDF without
// a rasterizer in the way; docs/CORPUS.md has the protocol.
type TraceDevice struct {
	BaseDevice
	w     *bufio.Writer
	depth int
	tile  int
}

// NewTraceDevice returns a device that writes to w.
func NewTraceDevice(w io.Writer) *TraceDevice {
	return &TraceDevice{w: bufio.NewWriter(w)}
}

// Close flushes the output.
func (d *TraceDevice) Close() error { return d.w.Flush() }

func (d *TraceDevice) indent() {
	for i := 0; i < d.depth; i++ {
		d.w.WriteString("    ")
	}
}

// g formats a number the way C's %g does, which is what the reference output
// uses: six significant digits, trailing zeros removed.
func g(v float32) string {
	s := strconv.FormatFloat(float64(v), 'g', 6, 32)
	if s == "-0" {
		return "0"
	}
	return s
}

func (d *TraceDevice) matrix(m raster.Matrix) {
	fmt.Fprintf(d.w, " transform=\"%s %s %s %s %s %s\"", g(m.A), g(m.B), g(m.C), g(m.D), g(m.E), g(m.F))
}

func (d *TraceDevice) color(cs *ColorSpace, c []float32, alpha float32) {
	if cs != nil {
		d.w.WriteString(" colorspace=\"" + cs.Name + "\" color=\"")
		for i := 0; i < cs.N && i < len(c); i++ {
			if i > 0 {
				d.w.WriteByte(' ')
			}
			d.w.WriteString(g(c[i]))
		}
		d.w.WriteByte('"')
	}
	if alpha < 1 {
		fmt.Fprintf(d.w, " alpha=\"%s\"", g(alpha))
	}
}

func (d *TraceDevice) params(cp ColorParams) {
	fmt.Fprintf(d.w, " ri=\"%d\" bp=\"%d\" op=\"%d\" opm=\"%d\"",
		cp.Intent, b2i(cp.BlackPoint), b2i(cp.Overprint), cp.OverprintMode)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (d *TraceDevice) rect(name string, r raster.Rect) {
	fmt.Fprintf(d.w, " %s=\"%s %s %s %s\"", name, g(r.X0), g(r.Y0), g(r.X1), g(r.Y1))
}

// tracePath writes the segments of a path as child elements.
type tracePath struct{ d *TraceDevice }

func (t tracePath) MoveTo(x, y float32) {
	t.d.indent()
	fmt.Fprintf(t.d.w, "<moveto x=\"%s\" y=\"%s\"/>\n", g(x), g(y))
}

func (t tracePath) LineTo(x, y float32) {
	t.d.indent()
	fmt.Fprintf(t.d.w, "<lineto x=\"%s\" y=\"%s\"/>\n", g(x), g(y))
}

func (t tracePath) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	t.d.indent()
	fmt.Fprintf(t.d.w, "<curveto x1=\"%s\" y1=\"%s\" x2=\"%s\" y2=\"%s\" x3=\"%s\" y3=\"%s\"/>\n",
		g(x1), g(y1), g(x2), g(y2), g(x3), g(y3))
}

func (t tracePath) Close() {
	t.d.indent()
	t.d.w.WriteString("<closepath/>\n")
}

func (d *TraceDevice) writePath(p *raster.Path) {
	d.depth++
	p.Walk(tracePath{d})
	d.depth--
}

func (d *TraceDevice) writeText(t *Text) {
	d.depth++
	for _, sp := range t.Spans {
		d.indent()
		fmt.Fprintf(d.w, "<span font=\"%s\" wmode=\"%d\" bidi=\"0\" trm=\"%s %s %s %s\">\n",
			escape(string(sp.Font.Name)), sp.WMode, g(sp.Trm.A), g(sp.Trm.B), g(sp.Trm.C), g(sp.Trm.D))
		d.depth++
		for _, it := range sp.Items {
			d.indent()
			d.w.WriteString("<g")
			if it.Rune >= 0 {
				fmt.Fprintf(d.w, " unicode=\"%s\"", escape(string(it.Rune)))
			}
			if it.Name != "" {
				fmt.Fprintf(d.w, " glyph=%q", escape(it.Name))
			} else if it.GID >= 0 {
				fmt.Fprintf(d.w, " glyph=\"%d\"", it.GID)
			}
			fmt.Fprintf(d.w, " x=\"%s\" y=\"%s\" adv=\"%s\"/>\n", g(it.X), g(it.Y), g(it.Adv))
		}
		d.depth--
		d.indent()
		d.w.WriteString("</span>\n")
	}
	d.depth--
}

func escape(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '&':
			out = append(out, "&amp;"...)
		case '\'':
			out = append(out, "&apos;"...)
		case '"':
			out = append(out, "&quot;"...)
		case '<':
			out = append(out, "&lt;"...)
		case '>':
			out = append(out, "&gt;"...)
		default:
			out = append(out, string(r)...)
		}
	}
	return string(out)
}

// FillPath implements Device.
func (d *TraceDevice) FillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.indent()
	d.w.WriteString("<fill_path")
	d.winding(evenOdd)
	d.color(cs, c, alpha)
	d.params(cp)
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writePath(p)
	d.indent()
	d.w.WriteString("</fill_path>\n")
}

func (d *TraceDevice) winding(evenOdd bool) {
	if evenOdd {
		d.w.WriteString(" winding=\"eofill\"")
	} else {
		d.w.WriteString(" winding=\"nonzero\"")
	}
}

func (d *TraceDevice) stroke(s *raster.Stroke) {
	fmt.Fprintf(d.w, " linewidth=\"%s\" miterlimit=\"%s\" linecap=\"%d,%d,%d\" linejoin=\"%d\"",
		g(s.Width), g(s.MiterLimit), s.StartCap, s.DashCap, s.EndCap, s.Join)
	if len(s.Dash) > 0 {
		fmt.Fprintf(d.w, " dash_phase=\"%s\" dash=\"", g(s.DashPhase))
		for i, v := range s.Dash {
			if i > 0 {
				d.w.WriteByte(' ')
			}
			d.w.WriteString(g(v))
		}
		d.w.WriteByte('"')
	}
}

// StrokePath implements Device.
func (d *TraceDevice) StrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.indent()
	d.w.WriteString("<stroke_path")
	d.stroke(s)
	d.color(cs, c, alpha)
	d.params(cp)
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writePath(p)
	d.indent()
	d.w.WriteString("</stroke_path>\n")
}

// ClipPath implements Device.
func (d *TraceDevice) ClipPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect) {
	d.indent()
	d.w.WriteString("<clip_path")
	d.winding(evenOdd)
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writePath(p)
	d.indent()
	d.w.WriteString("</clip_path>\n")
	d.depth++
}

// ClipStrokePath implements Device.
func (d *TraceDevice) ClipStrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.indent()
	d.w.WriteString("<clip_stroke_path")
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writePath(p)
	d.indent()
	d.w.WriteString("</clip_stroke_path>\n")
	d.depth++
}

// FillText implements Device.
func (d *TraceDevice) FillText(t *Text, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.textElement("fill_text", t, ctm, cs, c, alpha, cp)
}

// StrokeText implements Device.
func (d *TraceDevice) StrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.textElement("stroke_text", t, ctm, cs, c, alpha, cp)
}

func (d *TraceDevice) textElement(name string, t *Text, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.indent()
	d.w.WriteString("<" + name)
	d.color(cs, c, alpha)
	d.params(cp)
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writeText(t)
	d.indent()
	d.w.WriteString("</" + name + ">\n")
}

// ClipText implements Device.
func (d *TraceDevice) ClipText(t *Text, ctm raster.Matrix, scissor raster.Rect) {
	d.indent()
	d.w.WriteString("<clip_text")
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writeText(t)
	d.indent()
	d.w.WriteString("</clip_text>\n")
	d.depth++
}

// ClipStrokeText implements Device.
func (d *TraceDevice) ClipStrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.indent()
	d.w.WriteString("<clip_stroke_text")
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writeText(t)
	d.indent()
	d.w.WriteString("</clip_stroke_text>\n")
	d.depth++
}

// IgnoreText implements Device.
func (d *TraceDevice) IgnoreText(t *Text, ctm raster.Matrix) {
	d.indent()
	d.w.WriteString("<ignore_text")
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.writeText(t)
	d.indent()
	d.w.WriteString("</ignore_text>\n")
}

// FillImage implements Device.
func (d *TraceDevice) FillImage(img *Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.indent()
	fmt.Fprintf(d.w, "<fill_image alpha=\"%s\"", g(alpha))
	if img.CS != nil {
		d.w.WriteString(" colorspace=\"" + img.CS.Name + "\"")
	}
	d.params(cp)
	d.matrix(ctm)
	fmt.Fprintf(d.w, " width=\"%d\" height=\"%d\"/>\n", img.Width, img.Height)
}

// FillImageMask implements Device.
func (d *TraceDevice) FillImageMask(img *Image, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.indent()
	d.w.WriteString("<fill_image_mask")
	d.matrix(ctm)
	d.color(cs, c, alpha)
	d.params(cp)
	fmt.Fprintf(d.w, " width=\"%d\" height=\"%d\"/>\n", img.Width, img.Height)
}

// ClipImageMask implements Device.
func (d *TraceDevice) ClipImageMask(img *Image, ctm raster.Matrix, scissor raster.Rect) {
	d.indent()
	d.w.WriteString("<clip_image_mask")
	d.matrix(ctm)
	fmt.Fprintf(d.w, " width=\"%d\" height=\"%d\"/>\n", img.Width, img.Height)
	d.depth++
}

// FillShade implements Device.
func (d *TraceDevice) FillShade(sh *Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.indent()
	fmt.Fprintf(d.w, "<fill_shade alpha=\"%s\"", g(alpha))
	d.matrix(ctm)
	m := sh.Matrix
	fmt.Fprintf(d.w, " pattern_matrix=\"%s %s %s %s %s %s\"", g(m.A), g(m.B), g(m.C), g(m.D), g(m.E), g(m.F))
	if sh.CS != nil {
		d.w.WriteString(" colorspace=\"" + sh.CS.Name + "\"")
	}
	d.params(cp)
	switch sh.Type {
	case 1:
		d.w.WriteString(" type=\"function\"")
		m := sh.FuncMatrix
		fmt.Fprintf(d.w, " function_matrix=\"%s %s %s %s %s %s\"", g(m.A), g(m.B), g(m.C), g(m.D), g(m.E), g(m.F))
		dom := sh.Domain4()
		fmt.Fprintf(d.w, " domain=\"%s %s %s %s\"", g(dom[0]), g(dom[1]), g(dom[2]), g(dom[3]))
		d.w.WriteString(" samples=\"" + strconv.Itoa(sh.XDivs) + " " + strconv.Itoa(sh.YDivs) + "\"/>\n")
	case 2:
		fmt.Fprintf(d.w, " type=\"linear\" extend=\"%d %d\"", b2i(sh.Extend[0]), b2i(sh.Extend[1]))
		c := sh.Coord6()
		fmt.Fprintf(d.w, " start=\"%s %s\" end=\"%s %s\"/>\n", g(c[0]), g(c[1]), g(c[3]), g(c[4]))
	case 3:
		fmt.Fprintf(d.w, " type=\"radial\" extend=\"%d %d\"", b2i(sh.Extend[0]), b2i(sh.Extend[1]))
		c := sh.Coord6()
		fmt.Fprintf(d.w, " inner=\"%s %s %s\" outer=\"%s %s %s\"/>\n",
			g(c[0]), g(c[1]), g(c[2]), g(c[3]), g(c[4]), g(c[5]))
	default:
		d.w.WriteString(" type=\"mesh\"/>\n")
	}
}

// SetDefaultColorSpaces implements Device.
func (d *TraceDevice) SetDefaultColorSpaces(cs *DefaultColorSpaces) {
	d.indent()
	fmt.Fprintf(d.w, "<set_default_colorspaces gray=%q rgb=%q cmyk=%q oi=%q/>\n",
		csName(cs.Gray, "DeviceGray"), csName(cs.RGB, "DeviceRGB"),
		csName(cs.CMYK, "DeviceCMYK"), csName(cs.OutputIntent, "None"))
}

// PopClip implements Device.
func (d *TraceDevice) PopClip() {
	if d.depth > 0 {
		d.depth--
	}
	d.indent()
	d.w.WriteString("<pop_clip/>\n")
}

// BeginMask implements Device.
func (d *TraceDevice) BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams) {
	d.indent()
	d.w.WriteString("<clip_mask")
	d.rect("bbox", area)
	s := "alpha"
	if luminosity {
		s = "luminosity"
	}
	d.w.WriteString(" s=\"" + s + "\"")
	d.params(cp)
	d.w.WriteString(">\n")
	d.depth++
}

// EndMask implements Device.
func (d *TraceDevice) EndMask() {
	if d.depth > 0 {
		d.depth--
	}
	d.indent()
	d.w.WriteString("</clip_mask>\n")
	d.depth++
}

// BeginGroup implements Device.
func (d *TraceDevice) BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32) {
	d.indent()
	d.w.WriteString("<group")
	d.rect("bbox", area)
	fmt.Fprintf(d.w, " isolated=\"%d\" knockout=\"%d\" blendmode=\"%s\" alpha=\"%s\">\n",
		b2i(isolated), b2i(knockout), blend, g(alpha))
	d.depth++
}

// EndGroup implements Device.
func (d *TraceDevice) EndGroup() {
	if d.depth > 0 {
		d.depth--
	}
	d.indent()
	d.w.WriteString("</group>\n")
}

// BeginTile implements Device.
func (d *TraceDevice) BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int {
	d.indent()
	d.tile++
	fmt.Fprintf(d.w, "<tile id=\"%d\" doc_id=\"0\"", d.tile)
	d.rect("area", area)
	d.rect("view", view)
	fmt.Fprintf(d.w, " xstep=\"%s\" ystep=\"%s\"", g(xstep), g(ystep))
	d.matrix(ctm)
	d.w.WriteString(">\n")
	d.depth++
	return 0
}

// EndTile implements Device.
func (d *TraceDevice) EndTile() {
	if d.depth > 0 {
		d.depth--
	}
	d.indent()
	d.w.WriteString("</tile>\n")
}

// BeginLayer implements Device.
func (d *TraceDevice) BeginLayer(name string) {
	d.indent()
	fmt.Fprintf(d.w, "<layer name=\"%s\">\n", escape(name))
	d.depth++
}

// EndLayer implements Device.
func (d *TraceDevice) EndLayer() {
	if d.depth > 0 {
		d.depth--
	}
	d.indent()
	d.w.WriteString("</layer>\n")
}
