package html

import (
	"sort"
	"strings"
)

// layout flows a box tree down a column of a given width. Everything it
// writes into a box is absolute and in CSS pixels, with y counting down.
type layout struct {
	doc  *Document
	path string
	// y is where the next content goes, and pend the collapsing margin that
	// has not been applied to it yet.
	y    float32
	pend float32
	// waiting are the boxes whose top the pending margin will move, which is
	// how a margin collapses through a box that has nothing at its top edge.
	waiting []*box
	// spans are the line boxes in order, what pagination breaks between, and
	// force marks one a page must start at.
	spans []lineSpan
	next  bool
	pics  map[string]*picture
	errs  []error
	// root is the tree run laid out.
	root *box
}

// lineSpan is one line box as pagination sees it.
type lineSpan struct {
	top, bottom float32
	force       bool
}

func (l *layout) collapse(m float32) {
	if m > l.pend {
		l.pend = m
	}
}

// apply spends the collapsing margin, moving with it every box that was
// waiting to find out where its top edge is.
func (l *layout) apply() {
	if l.pend != 0 {
		l.y += l.pend
		for _, b := range l.waiting {
			b.y += l.pend
		}
		l.pend = 0
	}
	l.waiting = l.waiting[:0]
}

// run lays out a whole part into a column of the given width.
func (l *layout) run(b *box, w float32) {
	l.root = b
	if b == nil {
		return
	}
	l.block(b, 0, w)
	l.apply()
}

// block places one block level box and everything under it.
func (l *layout) block(b *box, x, avail float32) {
	s := b.style
	ml, mr := s.MarginLeft.Resolve(avail), s.MarginRight.Resolve(avail)
	pl, pr := s.PaddingLeft.Resolve(avail), s.PaddingRight.Resolve(avail)
	pt, pb := s.PaddingTop.Resolve(avail), s.PaddingBottom.Resolve(avail)
	w := avail - ml - mr - pl - pr
	if !s.Width.Auto() {
		w = s.Width.Resolve(avail)
		if s.MarginLeft.Auto() && s.MarginRight.Auto() {
			ml = max((avail-w-pl-pr)/2, 0)
		}
	}
	w = max(w, 0)

	if s.BreakBefore == BreakAlways {
		l.next = true
	}
	l.collapse(s.MarginTop.Resolve(avail))
	if pt > 0 || s.Background.A > 0 {
		l.apply()
	}
	b.x, b.y, b.w = x+ml, l.y, w+pl+pr
	if l.pend != 0 {
		l.waiting = append(l.waiting, b)
	}
	l.y += pt
	top := l.y

	if len(b.kids) > 0 && b.kids[0].inlineLevel() {
		l.inline(b, b.x+pl, w)
	} else {
		for _, k := range b.kids {
			l.block(k, b.x+pl, w)
		}
	}

	if s.Height.Unit == UnitPx {
		l.apply()
		l.y = max(l.y, top+s.Height.Value)
	}
	if pb > 0 {
		l.apply()
		l.y += pb
	}
	b.h = max(l.y-b.y, 0)
	l.collapse(s.MarginBottom.Resolve(avail))
	if s.BreakAfter == BreakAlways {
		l.next = true
	}
}

// inline fills the line boxes of a block whose children are all inline.
func (l *layout) inline(b *box, x, w float32) {
	c := &inlineCtx{l: l, avail: w}
	for _, k := range b.kids {
		c.gather(k)
	}
	c.str = c.text.String()
	if c.str == "" || len(c.items) == 0 {
		return
	}

	indent := b.style.TextIndent.Resolve(w)
	start, prev := 0, -1
	first := true
	for _, br := range lineBreaks(c.str) {
		avail := w
		if first {
			avail -= indent
		}
		width := c.measure(start, trimEnd(c.str, br.pos))
		if width > avail && prev >= 0 {
			l.emit(b, c, x, w, start, prev, first, false)
			first, start, prev = false, skipSpaces(c.str, prev), -1
			avail = w
			width = c.measure(start, trimEnd(c.str, br.pos))
		}
		if br.mandatory {
			l.emit(b, c, x, w, start, br.pos, first, true)
			first, start, prev = false, skipSpaces(c.str, br.pos), -1
			continue
		}
		if width <= avail || prev < 0 {
			prev = br.pos
		}
	}
}

// emit places one line box, from lo to hi in the text of the context.
func (l *layout) emit(b *box, c *inlineCtx, x, w float32, lo, hi int, first, last bool) {
	hi = trimEnd(c.str, hi)
	l.apply()

	sf := styleFace(b.style)
	above, below := strut(b.style, sf)
	line := lineBox{y: l.y}

	indent := float32(0)
	if first {
		indent = b.style.TextIndent.Resolve(w)
	}
	total, spaces := float32(0), 0
	for _, p := range c.pieces(lo, hi) {
		it := &c.items[p.item]
		if it.img != nil {
			total += it.iw
			above = max(above, it.ih)
			continue
		}
		total += it.face.width(c.str[p.lo:p.hi])
		spaces += strings.Count(c.str[p.lo:p.hi], " ")
		a, d := strut(it.style, it.face)
		above, below = max(above, a), max(below, d)
	}

	extra, off := float32(0), float32(0)
	if leftover := w - indent - total; leftover > 0 {
		switch b.style.TextAlign {
		case AlignRight:
			off = leftover
		case AlignCenter:
			off = leftover / 2
		case AlignJustify:
			if !last && spaces > 0 {
				extra = leftover / float32(spaces)
			}
		}
	}

	cx := x + indent + off
	for _, p := range c.pieces(lo, hi) {
		it := &c.items[p.item]
		f := frag{x: cx, style: it.style, face: it.face}
		if it.img != nil {
			f.img, f.w, f.h = it.img, it.iw, it.ih
			cx += it.iw
			line.frags = append(line.frags, f)
			continue
		}
		s := c.str[p.lo:p.hi]
		f.text, f.extra = s, extra
		f.w = it.face.width(s) + extra*float32(strings.Count(s, " "))
		cx += f.w
		line.frags = append(line.frags, f)
	}

	line.h, line.baseline = above+below, above
	b.lines = append(b.lines, line)
	l.spans = append(l.spans, lineSpan{top: line.y, bottom: line.y + line.h, force: l.next})
	l.next = false
	l.y += line.h
}

// strut is how far a line box reaches above and below its baseline for one
// style, which is the em box of the face with the leading spread around it.
func strut(s *Style, f face) (above, below float32) {
	a, d := f.ascent(), f.descent()
	half := (lineHeight(s, f) - (a + d)) / 2
	return a + half, d + half
}

func lineHeight(s *Style, f face) float32 {
	if s.LineHeight.Unit == UnitScale {
		return s.LineHeight.Value * f.size
	}
	return s.LineHeight.Resolve(f.size)
}

func trimEnd(s string, hi int) int {
	for hi > 0 && (s[hi-1] == ' ' || s[hi-1] == '\n') {
		hi--
	}
	return hi
}

func skipSpaces(s string, lo int) int {
	for lo < len(s) && (s[lo] == ' ' || s[lo] == '\n') {
		lo++
	}
	return lo
}

// paginate cuts a column into pages of a height, never inside a line box and
// never leaving a page with nothing on it.
func paginate(spans []lineSpan, h float32) []float32 {
	tops := []float32{0}
	cur, used := float32(0), false
	for _, s := range spans {
		if used && s.top > cur && (s.force || s.bottom-cur > h) {
			tops = append(tops, s.top)
			cur = s.top
		}
		used = true
	}
	return tops
}

// inlineItem is one run of an inline formatting context: the text of one
// element, or one picture.
type inlineItem struct {
	start  int
	style  *Style
	face   face
	img    *picture
	iw, ih float32
}

// piece is the part of one item that falls inside a line.
type piece struct {
	item   int
	lo, hi int
}

// inlineCtx collects the inline content of a block into one string, over
// which a break opportunity crosses the elements it is written in.
type inlineCtx struct {
	l     *layout
	text  strings.Builder
	str   string
	items []inlineItem
	// avail is the width a picture is scaled down to fit.
	avail float32
	// space is a collapsible space owed before the next character, and begun
	// says whether anything has been written yet.
	space bool
	begun bool
}

func (c *inlineCtx) gather(b *box) {
	switch b.kind {
	case textBox:
		c.addText(b.text, b.style)
	case breakBox:
		c.addBreak(b.style)
	case imageBox:
		c.addImage(b)
	default:
		for _, k := range b.kids {
			c.gather(k)
		}
	}
}

// open starts an item unless the last one is already the same element.
func (c *inlineCtx) open(s *Style, img *picture) {
	if n := len(c.items); img == nil && n > 0 && c.items[n-1].style == s && c.items[n-1].img == nil {
		return
	}
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: s, face: styleFace(s), img: img})
}

func (c *inlineCtx) flushSpace() {
	if c.space {
		c.text.WriteByte(' ')
		c.space = false
	}
}

func (c *inlineCtx) addText(s string, st *Style) {
	if s == "" {
		return
	}
	c.open(st, nil)
	if st.WhiteSpace == WhitePre || st.WhiteSpace == WhitePreWrap {
		c.flushSpace()
		c.text.WriteString(s)
		c.begun = true
		return
	}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isSpaceByte(s[j]) {
			j++
		}
		if j > i {
			c.flushSpace()
			c.text.WriteString(s[i:j])
			c.begun = true
		}
		k := j
		for k < len(s) && isSpaceByte(s[k]) {
			k++
		}
		if k > j && c.begun {
			c.space = true
		}
		i = k
	}
}

func (c *inlineCtx) addBreak(s *Style) {
	c.open(s, nil)
	c.space = false
	c.text.WriteByte('\n')
	c.begun = true
}

// objectChar stands for a picture in the text of a line, and its line break
// class is the one that breaks on both sides.
const objectChar = "￼"

func (c *inlineCtx) addImage(b *box) {
	pic := c.l.picture(b)
	if pic == nil {
		return
	}
	c.flushSpace()
	c.open(b.style, pic)
	c.text.WriteString(objectChar)
	c.begun = true
	n := len(c.items) - 1
	c.items[n].iw, c.items[n].ih = pictureSize(b, pic, c.avail)
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: b.style, face: styleFace(b.style)})
}

// itemAt is which item a byte of the text belongs to.
func (c *inlineCtx) itemAt(off int) int {
	i := sort.Search(len(c.items), func(i int) bool { return c.items[i].start > off })
	return max(i-1, 0)
}

func (c *inlineCtx) itemEnd(i int) int {
	if i+1 < len(c.items) {
		return c.items[i+1].start
	}
	return len(c.str)
}

// pieces cuts a range of the text into the parts of the items it covers.
func (c *inlineCtx) pieces(lo, hi int) []piece {
	var out []piece
	for i := c.itemAt(lo); lo < hi && i < len(c.items); i++ {
		end := min(c.itemEnd(i), hi)
		if end > lo {
			out = append(out, piece{item: i, lo: lo, hi: end})
			lo = end
		}
	}
	return out
}

func (c *inlineCtx) measure(lo, hi int) float32 {
	w := float32(0)
	for _, p := range c.pieces(lo, hi) {
		if it := &c.items[p.item]; it.img != nil {
			w += it.iw
		} else {
			w += it.face.width(c.str[p.lo:p.hi])
		}
	}
	return w
}
