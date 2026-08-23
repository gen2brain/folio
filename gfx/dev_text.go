package gfx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/gen2brain/folio/raster"
)

// The gaps that end a word and a line, and the drift that ends a line, as
// fractions of the font size.
const (
	spaceGap  = 0.15
	lineGap   = 0.8
	lineDrift = 0.8
)

// dupTol is how far apart two of the same character may be, as a fraction of
// the narrower advance, and still be one character drawn twice.
const dupTol = 0.5

// blockGap is how far two baselines may be apart and still be one paragraph.
const blockGap = 1.6

// TextPage is a page's text: blocks of lines of characters, each with its
// box, in the order the page drew them.
type TextPage struct {
	Bounds raster.Rect
	Blocks []TextBlock
}

// TextBlock is a paragraph of text, or one image.
type TextBlock struct {
	Bounds raster.Rect
	// Lines are the block's lines, and nil for an image block.
	Lines []TextLine
	// Image is what an image block holds, and nil for a text block; Matrix
	// maps the unit square onto the page.
	Image  Image
	Matrix raster.Matrix
}

// TextLine is a run of characters along one baseline.
type TextLine struct {
	Bounds raster.Rect
	// Dir is the unit vector the baseline runs along.
	Dir   raster.Point
	WMode int
	Chars []TextChar
}

// TextChar is one character where it was drawn.
type TextChar struct {
	Rune rune
	// Origin is where the pen was, and Quad the em box over the advance.
	Origin raster.Point
	Quad   Quad
	Size   float32
	Font   Font
	Color  [3]uint8
}

// HTML writes the page as absolutely positioned text, the words where they
// were drawn. id names the division the page becomes.
func (p *TextPage) HTML(id string) string {
	var b strings.Builder
	w, h := p.Bounds.X1-p.Bounds.X0, p.Bounds.Y1-p.Bounds.Y0
	fmt.Fprintf(&b, `<div id=%q style="position:relative;width:%vpt;height:%vpt">`+"\n",
		id, htmlNum(w), htmlNum(h))
	for i := range p.Blocks {
		bl := &p.Blocks[i]
		if bl.Lines == nil {
			continue
		}
		for j := range bl.Lines {
			ln := &bl.Lines[j]
			if len(ln.Chars) == 0 {
				continue
			}
			c := &ln.Chars[0]
			fmt.Fprintf(&b, `<p style="position:absolute;left:%vpt;top:%vpt;`+
				`font-size:%vpt;white-space:pre">`,
				htmlNum(ln.Bounds.X0-p.Bounds.X0), htmlNum(ln.Bounds.Y0-p.Bounds.Y0),
				htmlNum(c.Size))
			b.WriteString(htmlLine(ln))
			b.WriteString("</p>\n")
		}
	}
	b.WriteString("</div>\n")
	return b.String()
}

// htmlLine writes one line, opening a span wherever the face, the size or the
// colour of the characters changes.
func htmlLine(ln *TextLine) string {
	var b strings.Builder
	open := false
	var face Font
	var size float32
	var col [3]uint8
	for i := range ln.Chars {
		c := &ln.Chars[i]
		if !open || c.Font != face || c.Size != size || c.Color != col {
			if open {
				b.WriteString("</span>")
			}
			face, size, col = c.Font, c.Size, c.Color
			fmt.Fprintf(&b, `<span style="font-size:%vpt;color:#%02x%02x%02x`,
				htmlNum(size), col[0], col[1], col[2])
			if face != nil {
				fmt.Fprintf(&b, ";font-family:%s", htmlFamily(face.FontName()))
			}
			b.WriteString(`">`)
			open = true
		}
		b.WriteString(htmlEscape(c.Rune))
	}
	if open {
		b.WriteString("</span>")
	}
	return b.String()
}

// htmlFamily is the name a font is asked for by, less the subset tag a PDF
// puts in front of it.
func htmlFamily(name string) string {
	if len(name) > 7 && name[6] == '+' {
		name = name[7:]
	}
	if i := strings.IndexAny(name, ",+"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		return "serif"
	}
	if strings.ContainsAny(name, " ;:'\"") {
		return "'" + strings.ReplaceAll(name, "'", "") + "'"
	}
	return name
}

func htmlNum(v float32) string {
	return strconv.FormatFloat(float64(int(v*100+0.5))/100, 'f', -1, 32)
}

func htmlEscape(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	}
	return string(r)
}

// HTMLDocument wraps one page's division in the markup that makes it a page a
// browser will show on its own.
func HTMLDocument(title, body string) string {
	return `<!DOCTYPE html>` + "\n" + `<html><head><meta charset="utf-8"/>` +
		`<title>` + htmlText(title) + `</title></head><body style="margin:0">` + "\n" +
		body + `</body></html>` + "\n"
}

func htmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(htmlEscape(r))
	}
	return b.String()
}

// TextOptions configure a TextDevice.
type TextOptions struct {
	// Images records a block for every image drawn.
	Images bool
	// SkipHidden drops text drawn in a rendering mode that paints nothing.
	SkipHidden bool
}

// TextDevice collects what a page draws into a TextPage.
type TextDevice struct {
	BaseDevice
	opt  TextOptions
	page *TextPage
	// line is held back until a character arrives that does not join it.
	line TextLine
	col  [3]uint8
}

// NewTextDevice returns a device that collects the text of a page into st.
func NewTextDevice(st *TextPage, opt *TextOptions) *TextDevice {
	d := &TextDevice{page: st}
	if opt != nil {
		d.opt = *opt
	}
	return d
}

// FillText implements Device.
func (d *TextDevice) FillText(t *Text, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.addText(t, ctm, cs, color)
}

// StrokeText implements Device.
func (d *TextDevice) StrokeText(t *Text, stroke *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.addText(t, ctm, cs, color)
}

// ClipText implements Device.
func (d *TextDevice) ClipText(t *Text, ctm raster.Matrix, scissor raster.Rect) {
	d.addText(t, ctm, nil, nil)
}

// ClipStrokeText implements Device.
func (d *TextDevice) ClipStrokeText(t *Text, stroke *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.addText(t, ctm, nil, nil)
}

// IgnoreText implements Device. Text that paints nothing is still text.
func (d *TextDevice) IgnoreText(t *Text, ctm raster.Matrix) {
	if !d.opt.SkipHidden {
		d.addText(t, ctm, nil, nil)
	}
}

// FillImage implements Device.
func (d *TextDevice) FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.addImage(img, ctm)
}

// FillImageMask implements Device.
func (d *TextDevice) FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.addImage(img, ctm)
}

// Close implements Device.
func (d *TextDevice) Close() error {
	d.flushLine()
	return nil
}

func (d *TextDevice) addImage(img Image, ctm raster.Matrix) {
	if !d.opt.Images || img == nil {
		return
	}
	d.flushLine()
	d.page.Blocks = append(d.page.Blocks, TextBlock{
		Bounds: ctm.ApplyRect(raster.Rect{X1: 1, Y1: 1}),
		Image:  img,
		Matrix: ctm,
	})
}

func (d *TextDevice) addText(t *Text, ctm raster.Matrix, cs *ColorSpace, color []float32) {
	if t == nil {
		return
	}
	d.col = [3]uint8{}
	if cs != nil {
		var rgb [3]uint8
		cs.Convert(color, rgb[:])
		d.col = rgb
	}
	for i := range t.Spans {
		sp := &t.Spans[i]
		if sp.Font == nil {
			continue
		}
		asc, desc := sp.Font.EmBox()
		for _, it := range sp.Items {
			if it.Rune <= 0 {
				continue
			}
			if unicode.IsSpace(it.Rune) {
				it.Rune = ' '
			}
			m := raster.Concat(raster.Matrix{
				A: sp.Trm.A, B: sp.Trm.B, C: sp.Trm.C, D: sp.Trm.D,
				E: it.X, F: it.Y,
			}, ctm)
			d.addChar(sp, m, it, asc, desc)
		}
	}
}

// addChar places one character, opening a line when it does not join the one
// being built.
func (d *TextDevice) addChar(sp *TextSpan, m raster.Matrix, it TextItem, asc, desc float32) {
	size := float32(math.Sqrt(math.Abs(float64(m.A*m.D - m.B*m.C))))
	if size <= 0 {
		return
	}
	dir := raster.Point{X: m.A, Y: m.B}
	if sp.WMode == 1 {
		dir = raster.Point{X: m.C, Y: m.D}
	}
	dir = normalize(dir)

	adv := it.Adv
	if adv <= 0 {
		adv = float32(asc-desc) / 2
	}
	parts := ligature(it.Rune)
	n := float32(len(parts))
	c := TextChar{
		Rune:   parts[0],
		Origin: raster.Point{X: m.E, Y: m.F},
		Quad: Quad{
			UL: apply(m, 0, asc), UR: apply(m, adv/n, asc),
			LL: apply(m, 0, desc), LR: apply(m, adv/n, desc),
		},
		Size:  size,
		Font:  sp.Font,
		Color: d.col,
	}

	switch d.joins(c, dir, sp.WMode, size) {
	case joinDrop:
		return
	case joinBreak:
		d.flushLine()
		d.line = TextLine{Dir: dir, WMode: sp.WMode, Bounds: raster.EmptyRect}
	case joinSpace:
		d.space()
	}
	for i, r := range parts {
		if i > 0 {
			c.Rune = r
			c.Origin = c.end()
			c.Quad = Quad{
				UL: apply(m, float32(i)*adv/n, asc), UR: apply(m, float32(i+1)*adv/n, asc),
				LL: apply(m, float32(i)*adv/n, desc), LR: apply(m, float32(i+1)*adv/n, desc),
			}
		}
		d.line.Chars = append(d.line.Chars, c)
		d.line.Bounds = d.line.Bounds.Union(c.Quad.Bounds())
	}
}

// ligatures are the Latin ones of the Alphabetic Presentation Forms block.
var ligatures = map[rune]string{
	'\ufb00': "ff", '\ufb01': "fi", '\ufb02': "fl", '\ufb03': "ffi",
	'\ufb04': "ffl", '\ufb05': "st", '\ufb06': "st",
}

// ligature splits a character into what it stands for.
func ligature(r rune) []rune {
	if s, ok := ligatures[r]; ok {
		return []rune(s)
	}
	return []rune{r}
}

// How a character relates to the line being built.
const (
	joinRun = iota
	joinSpace
	joinBreak
	joinDrop
)

func (d *TextDevice) joins(c TextChar, dir raster.Point, wmode int, size float32) int {
	n := len(d.line.Chars)
	if n == 0 {
		return joinBreak
	}
	if wmode != d.line.WMode || dot(dir, d.line.Dir) < 0.999 {
		return joinBreak
	}
	prev := d.line.Chars[n-1]
	// The same character twice in the same place is one emboldened.
	if prev.Rune == c.Rune && near(prev.Origin, c.Origin, dupTol*minAdv(prev, c, size)) {
		return joinDrop
	}
	end := prev.end()
	dx := c.Origin.X - end.X
	dy := c.Origin.Y - end.Y
	along := dx*dir.X + dy*dir.Y
	across := dx*dir.Y - dy*dir.X
	if abs32(across) > lineDrift*size {
		return joinBreak
	}
	switch {
	case along > lineGap*size || along < -lineGap*size:
		return joinBreak
	case along > spaceGap*size && !unicode.IsSpace(c.Rune):
		return joinSpace
	}
	return joinRun
}

// end is where a character's advance leaves the pen.
func (c TextChar) end() raster.Point {
	return raster.Point{
		X: c.Origin.X + c.Quad.LR.X - c.Quad.LL.X,
		Y: c.Origin.Y + c.Quad.LR.Y - c.Quad.LL.Y,
	}
}

// space appends the word break a gap stands for.
func (d *TextDevice) space() {
	n := len(d.line.Chars)
	if n == 0 || d.line.Chars[n-1].Rune == ' ' {
		return
	}
	prev := d.line.Chars[n-1]
	sp := prev
	sp.Rune = ' '
	sp.Origin = prev.end()
	sp.Quad = Quad{UL: sp.Origin, UR: sp.Origin, LL: sp.Origin, LR: sp.Origin}
	d.line.Chars = append(d.line.Chars, sp)
}

// flushLine adds the line to the page, joining it to the block above when
// the two are one paragraph.
func (d *TextDevice) flushLine() {
	line := d.line
	d.line = TextLine{}
	if blank(line.Chars) {
		return
	}
	if b := d.lastBlock(); b != nil && sameParagraph(b, &line) {
		b.Lines = append(b.Lines, line)
		b.Bounds = b.Bounds.Union(line.Bounds)
		return
	}
	d.page.Blocks = append(d.page.Blocks, TextBlock{
		Bounds: line.Bounds,
		Lines:  []TextLine{line},
	})
}

// blank reports a run of characters with nothing to read in it.
func blank(chars []TextChar) bool {
	for _, c := range chars {
		if !unicode.IsSpace(c.Rune) {
			return false
		}
	}
	return true
}

func (d *TextDevice) lastBlock() *TextBlock {
	n := len(d.page.Blocks)
	if n == 0 || d.page.Blocks[n-1].Image != nil {
		return nil
	}
	return &d.page.Blocks[n-1]
}

// sameParagraph reports whether a line continues the block above it: same
// direction, a step no larger than one blank line, and an overlap across it.
func sameParagraph(b *TextBlock, line *TextLine) bool {
	last := &b.Lines[len(b.Lines)-1]
	if last.WMode != line.WMode || dot(last.Dir, line.Dir) < 0.999 {
		return false
	}
	size := max(lineSize(last), lineSize(line))
	if size <= 0 {
		return false
	}
	d := line.Chars[0].Origin
	p := last.Chars[0].Origin
	dx, dy := d.X-p.X, d.Y-p.Y
	step := abs32(dx*line.Dir.Y - dy*line.Dir.X)
	if step <= 0 || step > blockGap*size {
		return false
	}
	return overlaps(last.Bounds, line.Bounds, line.Dir)
}

// overlaps reports whether two boxes share any extent along dir.
func overlaps(a, b raster.Rect, dir raster.Point) bool {
	if abs32(dir.X) >= abs32(dir.Y) {
		return a.X0 < b.X1 && b.X0 < a.X1
	}
	return a.Y0 < b.Y1 && b.Y0 < a.Y1
}

func lineSize(l *TextLine) float32 {
	var s float32
	for _, c := range l.Chars {
		s = max(s, c.Size)
	}
	return s
}

// Text returns the page's text: a newline after every line and a blank line
// after every block.
func (p *TextPage) Text() string {
	var b strings.Builder
	for i := range p.Blocks {
		blk := &p.Blocks[i]
		if blk.Image != nil {
			continue
		}
		for j := range blk.Lines {
			for _, c := range blk.Lines[j].Chars {
				b.WriteRune(c.Rune)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func apply(m raster.Matrix, x, y float32) raster.Point {
	return raster.Point{X: m.A*x + m.C*y + m.E, Y: m.B*x + m.D*y + m.F}
}

func normalize(p raster.Point) raster.Point {
	n := float32(math.Hypot(float64(p.X), float64(p.Y)))
	if n == 0 {
		return raster.Point{X: 1}
	}
	return raster.Point{X: p.X / n, Y: p.Y / n}
}

func dot(a, b raster.Point) float32 { return a.X*b.X + a.Y*b.Y }

// minAdv is the narrower of two advances, or the size when either is zero.
func minAdv(a, b TextChar, size float32) float32 {
	wa, wb := advOf(a), advOf(b)
	if wa <= 0 || wb <= 0 {
		return size
	}
	return min(wa, wb)
}

func advOf(c TextChar) float32 {
	dx, dy := c.Quad.LR.X-c.Quad.LL.X, c.Quad.LR.Y-c.Quad.LL.Y
	return float32(math.Hypot(float64(dx), float64(dy)))
}

func near(a, b raster.Point, tol float32) bool {
	return abs32(a.X-b.X) <= tol && abs32(a.Y-b.Y) <= tol
}
