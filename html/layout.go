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
	// floats are the boxes taken out of the flow, which the line boxes
	// beside them work around.
	floats []exclusion
	pics   map[string]*picture
	errs   []error
	// budget bounds the trial layouts a table and a float measure with,
	// which nest and would otherwise multiply.
	budget *int
	// root is the tree run laid out.
	root *box
}

// layoutProbes is how many trial layouts one part may measure with.
const layoutProbes = 1 << 16

// sub is a layout for a box measured or placed on its own: a float, a table
// cell, or a trial of either.
func (l *layout) sub(y float32) *layout {
	return &layout{doc: l.doc, path: l.path, pics: l.pics, budget: l.budget, y: y}
}

// spend takes one trial layout out of the budget.
func (l *layout) spend() bool {
	if l.budget == nil || *l.budget <= 0 {
		return false
	}
	*l.budget--
	return true
}

// lineSpan is one line box as pagination sees it.
type lineSpan struct {
	top, bottom float32
	force       bool
}

// exclusion is a float the lines beside it must avoid.
type exclusion struct {
	top, bottom float32
	x0, x1      float32
	right       bool
}

// band is the free horizontal range between x and x+avail over a vertical
// range, once the floats that overlap it are taken out.
func (l *layout) band(x, avail, top, bottom float32) (float32, float32) {
	x0, x1 := x, x+avail
	for _, f := range l.floats {
		if f.bottom <= top || f.top >= bottom {
			continue
		}
		if f.right {
			x1 = min(x1, f.x0)
		} else {
			x0 = max(x0, f.x1)
		}
	}
	return x0, x1
}

// nextBottom is the next vertical position at which a float ends, and top
// itself when none does.
func (l *layout) nextBottom(top float32) float32 {
	next := top
	for _, f := range l.floats {
		if f.bottom > top && (next == top || f.bottom < next) {
			next = f.bottom
		}
	}
	return next
}

// clearance is how far down a box must go to be past the floats it clears.
func (l *layout) clearance(c Clear) float32 {
	y := float32(0)
	for _, f := range l.floats {
		if c == ClearLeft && f.right || c == ClearRight && !f.right {
			continue
		}
		y = max(y, f.bottom)
	}
	return y
}

// placeFloat takes a box out of the flow and puts it against one edge, below
// whatever is already there that it does not fit beside.
func (l *layout) placeFloat(b *box, x, avail float32) {
	l.apply()
	w := min(l.floatWidth(b, avail), avail)
	top := l.y
	for {
		x0, x1 := l.band(x, avail, top, top+1)
		if x1-x0 >= w {
			break
		}
		next := l.nextBottom(top)
		if next <= top {
			break
		}
		top = next
	}
	x0, x1 := l.band(x, avail, top, top+1)
	left := x0
	if b.style.Float == FloatRight {
		left = max(x1-w, x)
	}
	sub := l.sub(top)
	sub.block(b, left, w)
	sub.apply()
	l.spans = append(l.spans, sub.spans...)
	l.errs = append(l.errs, sub.errs...)
	l.floats = append(l.floats, exclusion{
		top: top, bottom: max(sub.y, top), x0: left, x1: left + w,
		right: b.style.Float == FloatRight,
	})
}

// floatWidth is how wide a float ends up: what it asks for, or the widest
// line it makes when it is left to shrink to fit.
func (l *layout) floatWidth(b *box, avail float32) float32 {
	s := b.style
	frame := s.MarginLeft.Resolve(avail) + s.MarginRight.Resolve(avail) +
		s.PaddingLeft.Resolve(avail) + s.PaddingRight.Resolve(avail) +
		s.BorderLeft.Thickness() + s.BorderRight.Thickness()
	if !s.Width.Auto() {
		return s.Width.Resolve(avail) + frame
	}
	if !l.spend() {
		return avail
	}
	probe := l.sub(0)
	probe.block(b, 0, avail)
	w := widest(b)
	reset(b)
	return min(w+frame, avail)
}

// reset clears what a layout wrote into a box: measuring a float and then
// placing it must not leave the tree with both.
func reset(b *box) {
	b.lines, b.natural = nil, 0
	b.x, b.y, b.w, b.h = 0, 0, 0, 0
	for _, k := range b.kids {
		reset(k)
	}
}

// widest is the natural width of the widest line under a box.
func widest(b *box) float32 {
	w := b.natural
	for i := range b.lines {
		w = max(w, b.lines[i].natural)
	}
	for _, k := range b.kids {
		w = max(w, widest(k))
	}
	return w
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
	if l.budget == nil {
		n := layoutProbes
		l.budget = &n
	}
	if b == nil {
		return
	}
	l.block(b, 0, w)
	l.apply()
	// A float's lines are laid out where the float was met, so the spans
	// reach pagination out of order.
	sort.Slice(l.spans, func(i, j int) bool { return l.spans[i].top < l.spans[j].top })
}

// block places one block level box and everything under it.
func (l *layout) block(b *box, x, avail float32) {
	s := b.style
	ml, mr := s.MarginLeft.Resolve(avail), s.MarginRight.Resolve(avail)
	pl, pr := s.PaddingLeft.Resolve(avail), s.PaddingRight.Resolve(avail)
	pt, pb := s.PaddingTop.Resolve(avail), s.PaddingBottom.Resolve(avail)
	bt, br := s.BorderTop.Thickness(), s.BorderRight.Thickness()
	bb, bl := s.BorderBottom.Thickness(), s.BorderLeft.Thickness()
	frame := pl + pr + bl + br
	w := avail - ml - mr - frame
	// A cell is as wide as its column, which the table worked out from what
	// every cell in it asked for.
	if !s.Width.Auto() && s.Display != DisplayTableCell {
		w = s.Width.Resolve(avail)
		if s.MarginLeft.Auto() && s.MarginRight.Auto() {
			ml = max((avail-w-frame)/2, 0)
		}
	}
	w = max(w, 0)

	if s.BreakBefore == BreakAlways {
		l.next = true
	}
	if s.Clear != ClearNone {
		l.apply()
		l.y = max(l.y, l.clearance(s.Clear))
	}
	l.collapse(s.MarginTop.Resolve(avail))
	if pt+bt > 0 || s.Background.A > 0 {
		l.apply()
	}
	b.x, b.y, b.w = x+ml, l.y, w+frame
	if l.pend != 0 {
		l.waiting = append(l.waiting, b)
	}
	l.y += bt + pt
	top := l.y

	switch {
	case s.Display == DisplayTable:
		l.table(b, b.x+bl+pl, w)
	case b.kind == imageBox:
		l.imageBlock(b, b.x+bl+pl, w)
	case len(b.kids) > 0 && b.kids[0].inlineLevel():
		l.inline(b, b.x+bl+pl, w)
	default:
		for _, k := range b.kids {
			l.block(k, b.x+bl+pl, w)
		}
	}

	if s.Height.Unit == UnitPx {
		l.apply()
		l.y = max(l.y, top+s.Height.Value)
	}
	if pb+bb > 0 {
		l.apply()
		l.y += pb + bb
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
		l.release(c, x, w, 0)
		return
	}

	sf := styleFace(b.style)
	above, below := strut(b.style, sf)
	indent := b.style.TextIndent.Resolve(w)
	start, prev := 0, -1
	first := true
	for _, br := range lineBreaks(c.str) {
		l.release(c, x, w, start)
		x0, x1 := l.lineBand(x, w, above+below)
		avail := x1 - x0
		if first {
			avail -= indent
		}
		width := c.measure(start, trimEnd(c.str, br.pos))
		if width > avail && prev >= 0 {
			l.emit(b, c, x0, x1-x0, lineIndent(indent, first), start, prev, false)
			first, start, prev = false, skipSpaces(c.str, prev), -1
			l.release(c, x, w, start)
			x0, x1 = l.lineBand(x, w, above+below)
			avail = x1 - x0
			width = c.measure(start, trimEnd(c.str, br.pos))
		}
		if br.mandatory {
			l.emit(b, c, x0, x1-x0, lineIndent(indent, first), start, br.pos, true)
			first, start, prev = false, skipSpaces(c.str, br.pos), -1
			continue
		}
		if width <= avail || prev < 0 {
			prev = br.pos
		}
	}
	l.release(c, x, w, len(c.str))
}

func lineIndent(indent float32, first bool) float32 {
	if first {
		return indent
	}
	return 0
}

// lineBand is the room a line has once the floats beside it are taken out,
// moving down past any that leave it none.
func (l *layout) lineBand(x, w, h float32) (float32, float32) {
	h = max(h, 1)
	x0, x1 := l.band(x, w, l.y, l.y+h)
	for x1 <= x0 {
		next := l.nextBottom(l.y)
		if next <= l.y {
			return x, x + w
		}
		l.y = next
		x0, x1 = l.band(x, w, l.y, l.y+h)
	}
	return x0, x1
}

// release places the floats written before a point in the text, which is
// where they belong: beside the line they were written in.
func (l *layout) release(c *inlineCtx, x, w float32, upto int) {
	for len(c.floats) > 0 && c.floats[0].at <= upto {
		l.placeFloat(c.floats[0].box, x, w)
		c.floats = c.floats[1:]
	}
}

// imageBlock lays a picture out as a block of its own, which an image that is
// floated or block level is.
func (l *layout) imageBlock(b *box, x, w float32) {
	pic := l.picture(b)
	if pic == nil {
		return
	}
	iw, ih := pictureSize(b, pic, w)
	if iw <= 0 || ih <= 0 {
		return
	}
	l.apply()
	line := lineBox{y: l.y, h: ih, baseline: ih, natural: iw,
		frags: []frag{{x: x, w: iw, h: ih, img: pic, style: b.style}}}
	b.lines = append(b.lines, line)
	l.spans = append(l.spans, lineSpan{top: line.y, bottom: line.y + line.h, force: l.next})
	l.next = false
	l.y += ih
}

// emit places one line box, from lo to hi in the text of the context.
func (l *layout) emit(b *box, c *inlineCtx, x, w, indent float32, lo, hi int, last bool) {
	hi = trimEnd(c.str, hi)
	l.apply()

	sf := styleFace(b.style)
	above, below := strut(b.style, sf)
	line := lineBox{y: l.y}

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

	line.h, line.baseline, line.natural = above+below, above, total+indent
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

// pendingFloat is a float and the point in the text it was written at.
type pendingFloat struct {
	box *box
	at  int
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
	// avail is the width a picture is scaled down to fit, and floats are the
	// boxes met so far that belong beside a line rather than on one.
	avail  float32
	floats []pendingFloat
	// space is a collapsible space owed before the next character, and begun
	// says whether anything has been written yet.
	space bool
	begun bool
}

func (c *inlineCtx) gather(b *box) {
	if b.floated() {
		c.floats = append(c.floats, pendingFloat{box: b, at: c.text.Len()})
		return
	}
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
