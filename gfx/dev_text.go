package gfx

import (
	"math"
	"strings"
	"unicode"

	"github.com/gen2brain/pdf/raster"
)

// How wide a gap between two characters has to be, as a fraction of the font
// size, before it stands for a word break, and how far a character may drift
// off the baseline before it is a line of its own.
const (
	spaceGap  = 0.15
	lineGap   = 0.8
	lineDrift = 0.8
)

// How far apart two baselines may be, as a fraction of the larger font size,
// and still belong to the same paragraph.
const blockGap = 1.6

// TextPage is a page's text: blocks of lines of characters, each with the box
// it occupies, in the order the page drew them.
type TextPage struct {
	Bounds raster.Rect
	Blocks []TextBlock
}

// TextBlock is a paragraph of text, or one image.
type TextBlock struct {
	Bounds raster.Rect
	// Lines are the block's lines, and nil for an image block.
	Lines []TextLine
	// Image is what an image block holds, and nil for a text block. Matrix
	// is what maps the unit square onto the page.
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
	// Origin is where the pen was, and Quad the em box of the font over the
	// character's advance, which is what a selection covers.
	Origin raster.Point
	Quad   Quad
	Size   float32
	Font   Font
	Color  [3]uint8
}

// TextOptions configure a TextDevice.
type TextOptions struct {
	// Images records a block for every image drawn, which is what finding
	// the natural resolution of a scanned page needs.
	Images bool
	// SkipHidden drops text drawn in a rendering mode that paints nothing,
	// which is the layer an OCR program leaves over a scan.
	SkipHidden bool
}

// TextDevice collects what a page draws into a TextPage. It is what text
// extraction, the link and search API and a page's natural resolution are
// all read off.
type TextDevice struct {
	BaseDevice
	opt  TextOptions
	page *TextPage
	// line is the line being built, held back until a character arrives that
	// does not belong to it.
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

// IgnoreText implements Device. Text that paints nothing is still text: it is
// how a scanned page carries what an OCR program made of it.
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
			if it.Rune == 0xa0 {
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

// addChar places one character, opening a line when the character does not
// continue the one being built.
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
	// A ligature is one glyph standing for several characters, and what a
	// reader wants back is the characters, each over its share of the box.
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

// ligatures are the Latin ones of the Alphabetic Presentation Forms block,
// which a font uses as one glyph and a reader wants back as letters.
var ligatures = map[rune]string{
	'\ufb00': "ff", '\ufb01': "fi", '\ufb02': "fl", '\ufb03': "ffi",
	'\ufb04': "ffl", '\ufb05': "st", '\ufb06': "st",
}

// ligature splits a character into what it stands for, which for all but a
// ligature is itself.
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
	// A page that draws the same character twice in the same place is
	// emboldening it, not saying it twice.
	if prev.Rune == c.Rune && near(prev.Origin, c.Origin, 0.05*size) {
		return joinDrop
	}
	// The gap is measured along the baseline from where the previous
	// character's advance ended, and the drift across it from the baseline
	// itself. The advance is the width of the quad, which is why the corners
	// rather than the origins are subtracted.
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

// end is where a character's advance leaves the pen, on the baseline.
func (c TextChar) end() raster.Point {
	return raster.Point{
		X: c.Origin.X + c.Quad.LR.X - c.Quad.LL.X,
		Y: c.Origin.Y + c.Quad.LR.Y - c.Quad.LL.Y,
	}
}

// space appends the word break a gap stands for, unless one is already there.
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

// flushLine adds the line being built to the page, joining it to the last
// block when the two are one paragraph.
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

// blank reports a run of characters with nothing to read in it, which is a
// line of spaces a page drew and meant nothing by.
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

// sameParagraph reports whether a line continues the block above it: the same
// direction, a step no larger than one blank line, and an overlap across the
// baseline so that two columns stay apart.
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

// overlaps reports whether two boxes share any extent along dir, which for an
// upright line is the horizontal one.
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

// Text returns the page's text, a newline after every line and a blank line
// after every block, which is what mutool draw -F txt writes.
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

func near(a, b raster.Point, tol float32) bool {
	return abs32(a.X-b.X) <= tol && abs32(a.Y-b.Y) <= tol
}
