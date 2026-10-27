package html

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"github.com/gen2brain/folio/font"
)

// layout flows a box tree down a column of a given width, in absolute CSS pixels, y down.
type layout struct {
	doc  *Document
	path string
	// y is the top of the next content; pend the collapsing margin not yet applied.
	y    float32
	pend float32
	// waiting are the boxes whose top the pending margin will move.
	waiting []*box
	// spans are the line boxes for pagination; next forces a page break before the next one.
	spans []lineSpan
	next  bool
	// floats are the out-of-flow boxes by bottom; clearL and clearR the lowest left and right bottoms.
	floats         []exclusion
	clearL, clearR float32
	// floatTop is the top of the last float, the highest a later one may rise, CSS 2.1 9.5.1.
	floatTop float32
	pics     map[string]*visual
	arts     map[*xhtml.Node]*visual
	errs     []error
	// budget bounds the boxes the trial layouts of tables and floats lay out.
	budget *int
	// posX, posY, posW and posH are the containing block of an absolutely placed child; posH is zero while unknown.
	posX, posY, posW, posH float32
	// cbh is the containing block height for percentage heights, negative when auto, CSS 2.1 10.5.
	cbh float32
	// pw and ph are the page box, against percentage sizes on a drawing's root.
	pw, ph float32
	// fonts are the faces the book brings for this part.
	fonts *fontSet
	root  *box
	// vertical is a part whose lines run down the page.
	vertical bool
}

func (l *layout) face(s *Style) face { return styleFace(s, l.fonts) }

// layoutWork bounds the boxes the trial layouts of one part lay out.
const layoutWork = 1 << 19

// sub is a layout for a float, a table cell, or a trial of either, measured on its own.
func (l *layout) sub(y float32) *layout {
	return &layout{doc: l.doc, path: l.path, pics: l.pics, budget: l.budget, y: y,
		fonts: l.fonts, posX: l.posX, posY: l.posY, posW: l.posW, posH: l.posH,
		cbh: l.cbh, vertical: l.vertical}
}

// spend takes a trial layout of b out of the budget.
func (l *layout) spend(b *box) bool {
	if l.budget == nil || *l.budget <= 0 {
		return false
	}
	*l.budget -= boxCount(b)
	return true
}

func boxCount(b *box) int {
	n := 1
	for _, k := range b.kids {
		n += boxCount(k)
	}
	return n
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

// band is the free horizontal range within x and x+avail over top to bottom, past the floats.
func (l *layout) band(x, avail, top, bottom float32) (float32, float32) {
	x0, x1 := x, x+avail
	for _, f := range l.floats[l.past(top):] {
		if f.top >= bottom {
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

// nextBottom is the next float bottom below top, or top when none.
func (l *layout) nextBottom(top float32) float32 {
	if i := l.past(top); i < len(l.floats) {
		return l.floats[i].bottom
	}
	return top
}

// past is the first float that ends below top.
func (l *layout) past(top float32) int {
	i, _ := slices.BinarySearchFunc(l.floats, top, func(f exclusion, y float32) int {
		if f.bottom <= y {
			return -1
		}
		return 1
	})
	return i
}

// clearance is the lowest bottom of the floats c clears.
func (l *layout) clearance(c Clear) float32 {
	switch c {
	case ClearLeft:
		return l.clearL
	case ClearRight:
		return l.clearR
	}
	return max(l.clearL, l.clearR)
}

// placeFloat takes a box out of the flow and puts it against an edge, below floats it does not fit beside.
func (l *layout) placeFloat(b *box, x, avail float32) {
	l.apply()
	w := min(l.floatWidth(b, avail), avail)
	top := l.y
	if len(l.floats) > 0 {
		top = max(top, l.floatTop)
	}
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
	sub.flow(b, left, w)
	sub.apply()
	l.spans = append(l.spans, sub.spans...)
	l.errs = append(l.errs, sub.errs...)
	f := exclusion{
		top: top, bottom: max(sub.y, top), x0: left, x1: left + w,
		right: b.style.Float == FloatRight,
	}
	l.floats = slices.Insert(l.floats, l.past(f.bottom), f)
	l.floatTop = top
	if f.right {
		l.clearR = max(l.clearR, f.bottom)
	} else {
		l.clearL = max(l.clearL, f.bottom)
	}
}

// floatWidth is a float's width: its own, or shrink-to-fit.
func (l *layout) floatWidth(b *box, avail float32) float32 {
	s := b.style
	frame := s.MarginLeft.Resolve(avail) + s.MarginRight.Resolve(avail) +
		s.PaddingLeft.Resolve(avail) + s.PaddingRight.Resolve(avail) +
		s.BorderLeft.Thickness() + s.BorderRight.Thickness()
	if !s.Width.Auto() {
		return s.Width.Resolve(avail) + frame
	}
	if !l.spend(b) {
		return avail
	}
	probe := l.sub(0)
	probe.flow(b, 0, avail)
	w := widest(b)
	reset(b)
	return min(w+frame, avail)
}

// reset clears the layout results in a subtree.
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

// apply spends the collapsing margin, moving every box waiting on it.
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
		n := layoutWork
		l.budget = &n
	}
	if b == nil {
		return
	}
	l.block(b, 0, w)
	l.apply()
	sort.Slice(l.spans, func(i, j int) bool { return l.spans[i].top < l.spans[j].top })
	for i := range l.spans {
		l.y = max(l.y, l.spans[i].bottom)
	}
	reachOf(b)
}

// block places one block level box and its subtree, in flow or out of it.
func (l *layout) block(b *box, x, avail float32) {
	if b.style.Position == PosAbsolute {
		l.absolute(b, x, avail)
		return
	}
	l.flow(b, x, avail)
}

// flow places a box in the block flow.
func (l *layout) flow(b *box, x, avail float32) {
	s := b.style
	ml, mr := s.MarginLeft.Resolve(avail), s.MarginRight.Resolve(avail)
	pl, pr := s.PaddingLeft.Resolve(avail), s.PaddingRight.Resolve(avail)
	pt, pb := s.PaddingTop.Resolve(avail), s.PaddingBottom.Resolve(avail)
	bt, br, bb, bl := borderInset(b)
	frame := pl + pr + bl + br
	w := avail - ml - mr - frame
	if !s.Width.Auto() && s.Display != DisplayTableCell {
		w = s.Width.Resolve(avail)
		if s.MarginLeft.Auto() && s.MarginRight.Auto() {
			ml = max((avail-w-frame)/2, 0)
		}
	}
	w = clampLength(w, s.MinWidth, s.MaxWidth, avail)

	if s.BackgroundImage != "" {
		b.back = l.pictureAt(s.BackgroundImage)
	}
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
	if b.marker != "" {
		b.markerFace = l.face(s)
	}
	l.y += bt + pt
	top := l.y
	savedX, savedY, savedW, savedH := l.posX, l.posY, l.posW, l.posH
	own, definite := definiteHeight(s.Height, l.cbh)
	if s.Position != PosStatic {
		l.posX, l.posY, l.posW, l.posH = b.x+bl+pl, top, w, 0
		if definite {
			l.posH = own
		}
	}
	savedCB := l.cbh
	l.cbh = -1
	switch {
	case definite:
		l.cbh = own
	case b == l.root:
		l.cbh = savedCB
	}

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

	l.posX, l.posY, l.posW, l.posH = savedX, savedY, savedW, savedH
	l.cbh = savedCB
	content := l.y - top
	h := content
	if definite {
		h = own
	}
	if v, ok := definiteHeight(s.MaxHeight, savedCB); ok {
		h = min(h, v)
	}
	if v, ok := definiteHeight(s.MinHeight, savedCB); ok {
		h = max(h, v)
	}
	if h > content {
		l.apply()
		l.y = top + h
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
	if s.Position == PosRelative {
		dx, dy := offsets(s, avail)
		if dx != 0 || dy != 0 {
			shift(b, dx, dy)
		}
	}
}

// reachOf fills in the reach of every subtree.
func reachOf(b *box) float32 {
	r := b.y + b.h
	for _, k := range b.kids {
		r = max(r, reachOf(k))
	}
	b.reach = r
	return r
}

// borderInset is the room a box gives its borders, halved for a collapsed table cell.
func borderInset(b *box) (top, right, bottom, left float32) {
	if b.inset != nil {
		return b.inset[0], b.inset[1], b.inset[2], b.inset[3]
	}
	s := b.style
	return s.BorderTop.Thickness(), s.BorderRight.Thickness(),
		s.BorderBottom.Thickness(), s.BorderLeft.Thickness()
}

// definiteHeight resolves a length or a percentage of a definite containing block height.
func definiteHeight(v Length, cb float32) (float32, bool) {
	switch v.Unit {
	case UnitPx:
		return max(v.Value, 0), true
	case UnitPercent:
		if cb >= 0 {
			return max(v.Value*cb/100, 0), true
		}
	}
	return 0, false
}

// clampLength clamps v to min and max, ignoring an absent bound.
func clampLength(v float32, lo, hi Length, against float32) float32 {
	if !hi.Auto() {
		v = min(v, hi.Resolve(against))
	}
	if !lo.Auto() {
		v = max(v, lo.Resolve(against))
	}
	return max(v, 0)
}

// offsets is the displacement of a relatively positioned box.
func offsets(s *Style, avail float32) (float32, float32) {
	dx, dy := float32(0), float32(0)
	switch {
	case !s.LeftPos.Auto():
		dx = s.LeftPos.Resolve(avail)
	case !s.Right.Auto():
		dx = -s.Right.Resolve(avail)
	}
	switch {
	case !s.Top.Auto():
		dy = s.Top.Resolve(avail)
	case !s.Bottom.Auto():
		dy = -s.Bottom.Resolve(avail)
	}
	return dx, dy
}

// absolute places a box against its positioned ancestor, leaving the flow.
func (l *layout) absolute(b *box, x, avail float32) {
	s := b.style
	cx, cy, cw := l.posX, l.posY, l.posW
	if cw <= 0 {
		cx, cy, cw = x, l.y, avail
	}
	w := cw
	switch {
	case !s.Width.Auto():
		w = s.Width.Resolve(cw) + frameWidth(s)
	case !s.LeftPos.Auto() && !s.Right.Auto():
		w = cw - s.LeftPos.Resolve(cw) - s.Right.Resolve(cw)
	default:
		w = l.floatWidth(b, cw)
	}
	w = clampLength(w, s.MinWidth, s.MaxWidth, cw)

	bx, by := cx, l.y
	if !s.LeftPos.Auto() {
		bx = cx + s.LeftPos.Resolve(cw)
	} else if !s.Right.Auto() {
		bx = cx + cw - s.Right.Resolve(cw) - w
	}
	if !s.Top.Auto() {
		by = cy + s.Top.Resolve(cw)
	}
	sub := l.sub(by)
	sub.posX, sub.posY, sub.posW = bx, by, w
	sub.flow(b, bx, w)
	sub.apply()
	if s.Top.Auto() && !s.Bottom.Auto() && l.posH > 0 {
		if dy := cy + l.posH - s.Bottom.Resolve(cw) - (sub.y - by) - by; dy != 0 {
			shift(b, 0, dy)
			for i := range sub.spans {
				sub.spans[i].top += dy
				sub.spans[i].bottom += dy
			}
		}
	}
	l.spans = append(l.spans, sub.spans...)
	l.errs = append(l.errs, sub.errs...)
}

// inline fills the line boxes of a block whose children are all inline.
func (l *layout) inline(b *box, x, w float32) {
	c := &inlineCtx{l: l, avail: w}
	for _, k := range b.kids {
		c.gather(k)
	}
	c.str = c.text.String()
	c.resolveBidi(b.style.Direction)
	defer func() {
		for _, k := range c.placed {
			l.absolute(k, x, w)
		}
	}()
	if c.str == "" || len(c.items) == 0 {
		l.release(c, x, w, 0)
		return
	}

	sf := l.face(b.style)
	above, below := strut(b.style, sf)
	indent := b.style.TextIndent.Resolve(w)
	start, prev, tried := 0, -1, 0
	first := true
	for _, br := range c.breaks() {
		l.release(c, x, w, start)
		x0, x1 := l.lineBand(x, w, above+below)
		avail := x1 - x0
		if first {
			avail -= indent
		}
		width := c.measure(start, trimEnd(c.str, br.pos))
		if tried++; (width > avail || tried > maxLineBreaks) && prev >= 0 {
			l.emit(b, c, x0, x1-x0, lineIndent(indent, first), start, prev, false)
			first, start, prev, tried = false, skipSpaces(c.str, prev), -1, 1
			l.release(c, x, w, start)
			x0, x1 = l.lineBand(x, w, above+below)
			avail = x1 - x0
			width = c.measure(start, trimEnd(c.str, br.pos))
		}
		if br.mandatory {
			l.emit(b, c, x0, x1-x0, lineIndent(indent, first), start, br.pos, true)
			first, start, prev, tried = false, skipSpaces(c.str, br.pos), -1, 0
			continue
		}
		if width <= avail || prev < 0 {
			prev = br.pos
		}
	}
	l.release(c, x, w, len(c.str))
}

// maxLineBreaks bounds the break opportunities one line is measured at.
const maxLineBreaks = 1024

func lineIndent(indent float32, first bool) float32 {
	if first {
		return indent
	}
	return 0
}

// lineBand is the room a line has beside the floats, moving down past any that leave none.
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

// release places the floats written before upto, beside the current line.
func (l *layout) release(c *inlineCtx, x, w float32, upto int) {
	for len(c.floats) > 0 && c.floats[0].at <= upto {
		l.placeFloat(c.floats[0].box, x, w)
		c.floats = c.floats[1:]
	}
}

// imageBlock lays out a floated or block level picture as a block of its own.
func (l *layout) imageBlock(b *box, x, w float32) {
	vis := l.picture(b)
	if vis == nil {
		return
	}
	iw, ih := pictureSize(b, vis, w, l.cbh, l.ph)
	if iw <= 0 || ih <= 0 {
		return
	}
	l.apply()
	line := lineBox{y: l.y, h: ih, baseline: ih, natural: iw,
		frags: []frag{{x: x, w: iw, h: ih, vis: vis, style: b.style}}}
	b.lines = append(b.lines, line)
	l.spans = append(l.spans, lineSpan{top: line.y, bottom: line.y + line.h, force: l.next})
	l.next = false
	l.y += ih
}

// emit places one line box, from lo to hi in the text of the context.
func (l *layout) emit(b *box, c *inlineCtx, x, w, indent float32, lo, hi int, last bool) {
	hi = trimEnd(c.str, hi)
	l.apply()

	sf := l.face(b.style)
	above, below := strut(b.style, sf)
	line := lineBox{y: l.y}

	total, spaces := float32(0), 0
	edge := false
	for _, p := range c.pieces(lo, hi) {
		it := &c.items[p.item]
		a, d := itemBox(it)
		if it.sub != nil || it.vis != nil {
			total += it.iw
		} else {
			total += it.face.width(c.str[p.lo:p.hi])
			spaces += strings.Count(c.str[p.lo:p.hi], " ")
		}
		switch it.style.VerticalAlign {
		case AlignTop, AlignBottom:
			edge = true
			continue
		}
		dy := alignShift(it.style, it.face, sf, a, d)
		above, below = max(above, a+dy), max(below, d-dy)
	}
	// CSS 2.1 10.8.1.
	if edge {
		for _, p := range c.pieces(lo, hi) {
			it := &c.items[p.item]
			switch it.style.VerticalAlign {
			case AlignTop, AlignBottom:
			default:
				continue
			}
			a, d := itemBox(it)
			if h := a + d - (above + below); h > 0 {
				if it.style.VerticalAlign == AlignTop {
					below += h
				} else {
					above += h
				}
			}
		}
	}

	extra, off := float32(0), float32(0)
	if leftover := w - indent - total; leftover > 0 {
		switch b.style.TextAlign.Resolve(b.style.Direction) {
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

	type placed struct {
		f  frag
		at int
	}
	var laid []placed
	cx := x + indent + off
	for _, p := range c.visual(c.pieces(lo, hi)) {
		it := &c.items[p.item]
		f := frag{x: cx, style: it.style, face: it.face, link: it.link,
			rtl: c.rightToLeft(p.lo)}
		a, d := itemBox(it)
		switch it.style.VerticalAlign {
		case AlignTop:
			f.dy = above - a
		case AlignBottom:
			f.dy = d - below
		default:
			f.dy = alignShift(it.style, it.face, sf, a, d)
		}
		if it.sub != nil {
			f.sub, f.w, f.h = it.sub, it.iw, it.ih
			cx += it.iw
			laid = append(laid, placed{f, p.lo})
			continue
		}
		if it.vis != nil {
			f.vis, f.w, f.h = it.vis, it.iw, it.ih
			cx += it.iw
			laid = append(laid, placed{f, p.lo})
			continue
		}
		s := c.str[p.lo:p.hi]
		f.text, f.extra = s, extra
		f.w = it.face.width(s) + extra*float32(strings.Count(s, " "))
		cx += f.w
		laid = append(laid, placed{f, p.lo})
	}
	if c.levels != nil {
		slices.SortFunc(laid, func(a, b placed) int { return a.at - b.at })
	}
	for _, pl := range laid {
		line.frags = append(line.frags, pl.f)
	}

	line.h, line.baseline, line.natural = above+below, above, total+indent
	for i := range line.frags {
		f := &line.frags[i]
		if f.sub == nil {
			continue
		}
		ml := f.style.MarginLeft.Resolve(w)
		shift(f.sub, f.x+ml-f.sub.x, line.y+above-f.dy-inlineBaseline(f.sub)-f.sub.y)
	}
	b.lines = append(b.lines, line)
	l.spans = append(l.spans, lineSpan{top: line.y, bottom: line.y + line.h, force: l.next})
	l.next = false
	l.y += line.h
}

// itemBox is the extent of an inline item above and below its baseline.
func itemBox(it *inlineItem) (above, below float32) {
	switch {
	case it.sub != nil:
		return it.ib, it.ih - it.ib
	case it.vis != nil:
		return it.ih, 0
	}
	return strut(it.style, it.face)
}

// alignShift is the vertical-align raise of a box above its line's baseline, edge alignments excluded.
func alignShift(s *Style, f, parent face, above, below float32) float32 {
	switch s.VerticalAlign {
	case AlignSub:
		return -parent.subOffset()
	case AlignSuper:
		return parent.superOffset()
	case AlignTextTop:
		return parent.ascent() - above
	case AlignTextBottom:
		return below - parent.descent()
	case AlignMiddle:
		return parent.xHeight()/2 - (above-below)/2
	case AlignLength:
		if s.VerticalShift.Unit == UnitPercent {
			return s.VerticalShift.Value / 100 * lineHeight(s, f)
		}
		return s.VerticalShift.Resolve(f.size)
	}
	return 0
}

// strut is the extent of a line box above and below its baseline for one style: the em box plus half-leading.
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

// paginate cuts a column into pages of a height, never inside a line box and never leaving a page empty.
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

// inlineItem is one run of an inline formatting context: one element's text or one picture.
type inlineItem struct {
	start int
	style *Style
	face  face
	vis   *visual
	// link is the href of the enclosing anchor.
	link   string
	iw, ih float32
	// sub is the box of an inline-block; ib its baseline below its top.
	sub *box
	ib  float32
}

// breaks are the UAX #14 line break opportunities, less those inside nowrap ranges.
func (c *inlineCtx) breaks() []lineBreak {
	brs := lineBreaks(c.str)
	if len(c.nowrap) == 0 {
		return brs
	}
	rs := slices.Clone(c.nowrap)
	slices.SortFunc(rs, func(a, b [2]int) int { return a[0] - b[0] })
	merged := rs[:1]
	for _, r := range rs[1:] {
		if last := &merged[len(merged)-1]; r[0] < last[1] {
			last[1] = max(last[1], r[1])
		} else {
			merged = append(merged, r)
		}
	}
	out := brs[:0]
	j := 0
	for _, br := range brs {
		for j < len(merged) && merged[j][1] <= br.pos {
			j++
		}
		if !br.mandatory && j < len(merged) && br.pos > merged[j][0] {
			continue
		}
		out = append(out, br)
	}
	return out
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

// inlineCtx collects the inline content of a block into one string for line breaking.
type inlineCtx struct {
	l     *layout
	text  strings.Builder
	str   string
	items []inlineItem
	// link is the href of the enclosing anchor.
	link string
	// avail is the width a picture scales down to fit, floats the pending floats, nowrap the unbreakable ranges.
	avail  float32
	floats []pendingFloat
	placed []*box
	nowrap [][2]int
	// space is a collapsible space owed before the next character; begun reports any text yet.
	space bool
	begun bool
	// levels is the bidi level of every byte, nil when all left to right; rtl the paragraph direction.
	levels []byte
	rtl    bool
}

func (c *inlineCtx) gather(b *box) {
	switch {
	case b.placed():
		c.placed = append(c.placed, b)
		return
	case b.floated():
		c.floats = append(c.floats, pendingFloat{box: b, at: c.text.Len()})
		return
	}
	if href := hrefOf(b.node); href != "" {
		was := c.link
		c.link = href
		defer func() { c.link = was }()
	}
	switch b.kind {
	case textBox:
		c.addText(b.text, b.style)
	case breakBox:
		c.addBreak(b.style)
	case imageBox:
		c.addImage(b)
	default:
		if b.style.Display == DisplayInlineBlock {
			c.addInlineBlock(b)
			return
		}
		for _, k := range b.kids {
			c.gather(k)
		}
	}
}

// open starts an item unless the last is the same element in the same face.
func (c *inlineCtx) open(s *Style, f face, vis *visual) {
	if n := len(c.items); vis == nil && n > 0 &&
		c.items[n-1].style == s && c.items[n-1].face == f &&
		c.items[n-1].vis == nil && c.items[n-1].link == c.link {
		return
	}
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: s, face: f,
		vis: vis, link: c.link})
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
	s = transform(s, st.TextTransform)
	f := c.l.face(st)
	first := -1
	put := func(run string) {
		c.open(st, f, nil)
		c.flushSpace()
		if first < 0 {
			first = c.text.Len()
		}
		c.write(run, st, f)
	}
	if st.WhiteSpace == WhitePre || st.WhiteSpace == WhitePreWrap {
		put(s)
	} else {
		for i := 0; i < len(s); {
			j := i
			for j < len(s) && !isSpaceByte(s[j]) {
				j++
			}
			if j > i {
				put(s[i:j])
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
	if st.WhiteSpace == WhiteNowrap && first >= 0 {
		c.nowrap = append(c.nowrap, [2]int{first, c.text.Len()})
	}
}

// write appends a run of text, splitting it for small capitals and for characters base cannot draw.
func (c *inlineCtx) write(s string, st *Style, base face) {
	c.begun = true
	small := smallCapsFace(base)
	var run []rune
	cur := base
	flush := func() {
		if len(run) == 0 {
			return
		}
		c.open(st, cur, nil)
		c.text.WriteString(string(run))
		run = run[:0]
	}
	for _, r := range s {
		f, out := base, r
		if st.SmallCaps && unicode.IsLower(r) {
			f, out = small, unicode.ToUpper(r)
		}
		if f.m != nil && !f.m.has(out) {
			if fb := fallbackFace(f, out); fb.prog != nil {
				f = fb
			}
		}
		if f != cur {
			flush()
			cur = f
		}
		run = append(run, out)
	}
	flush()
}

// transform applies text-transform.
func transform(s string, t TextTransform) string {
	switch t {
	case TransformUpper:
		return strings.ToUpper(s)
	case TransformLower:
		return strings.ToLower(s)
	case TransformCapitalize:
		prev := ' '
		return strings.Map(func(r rune) rune {
			was := prev
			prev = r
			if unicode.IsSpace(was) {
				return unicode.ToTitle(r)
			}
			return r
		}, s)
	}
	return s
}

func (c *inlineCtx) addBreak(s *Style) {
	c.open(s, c.l.face(s), nil)
	c.space = false
	c.text.WriteByte('\n')
	c.begun = true
}

// objectChar stands for a picture on a line; its UAX #14 class breaks on both sides.
const objectChar = "￼"

func (c *inlineCtx) addImage(b *box) {
	vis := c.l.picture(b)
	if vis == nil {
		return
	}
	c.flushSpace()
	c.open(b.style, c.l.face(b.style), vis)
	c.text.WriteString(objectChar)
	c.begun = true
	n := len(c.items) - 1
	c.items[n].iw, c.items[n].ih = pictureSize(b, vis, c.avail, c.l.cbh, c.l.ph)
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: b.style, face: c.l.face(b.style)})
}

// addInlineBlock lays a box out on its own and puts it on the line as one character, CSS 2.1 10.3.9.
func (c *inlineCtx) addInlineBlock(b *box) {
	l := c.l
	w := c.avail
	if !b.style.Width.Auto() {
		w = b.style.Width.Resolve(c.avail)
	} else if mx := l.probe(b, maxContentWidth); mx > 0 && mx < c.avail {
		w = mx
	}
	sub := l.sub(0)
	sub.cbh = l.cbh
	sub.flow(b, 0, max(w, 0))
	sub.apply()
	l.errs = append(l.errs, sub.errs...)
	if b.w <= 0 && b.h <= 0 {
		return
	}
	c.flushSpace()
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: b.style, face: l.face(b.style)})
	c.text.WriteString(objectChar)
	c.begun = true
	n := len(c.items) - 1
	c.items[n].sub = b
	c.items[n].iw = b.w + b.style.MarginLeft.Resolve(c.avail) + b.style.MarginRight.Resolve(c.avail)
	c.items[n].ih = b.h
	c.items[n].ib = inlineBaseline(b)
	c.items = append(c.items, inlineItem{start: c.text.Len(), style: b.style, face: l.face(b.style)})
}

// inlineBaseline is an inline-block's last line box baseline, or its bottom edge, CSS 2.1 10.8.1.
func inlineBaseline(b *box) float32 {
	if v, ok := lastLineBaseline(b); ok {
		return v - b.y
	}
	return b.h
}

func lastLineBaseline(b *box) (float32, bool) {
	for i := len(b.kids) - 1; i >= 0; i-- {
		if v, ok := lastLineBaseline(b.kids[i]); ok {
			return v, true
		}
	}
	if n := len(b.lines); n > 0 {
		return b.lines[n-1].y + b.lines[n-1].baseline, true
	}
	return 0, false
}

// itemAt is the index of the item holding byte off.
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

// pieces cuts a range of the text at item boundaries.
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
		if it := &c.items[p.item]; it.vis != nil || it.sub != nil {
			w += it.iw
		} else {
			w += it.face.width(c.str[p.lo:p.hi])
		}
	}
	return w
}

// resolveBidi levels every byte of the paragraph; levels stays nil when all is left to right.
func (c *inlineCtx) resolveBidi(dir Direction) {
	c.rtl = dir.RTL()
	if !c.rtl && !font.NeedsBidi(c.str) {
		return
	}
	text := []rune(c.str)
	base := 0
	if c.rtl {
		base = 1
	}
	levels := font.BidiLevels(text, base)
	c.levels = make([]byte, len(c.str))
	at := 0
	for i, r := range text {
		n := utf8.RuneLen(r)
		for k := 0; k < n; k++ {
			c.levels[at+k] = levels[i]
		}
		at += n
	}
}

// visual splits the pieces of a line at level changes and orders them by rule L2.
func (c *inlineCtx) visual(in []piece) []piece {
	if c.levels == nil {
		return in
	}
	var out []piece
	for _, p := range in {
		if c.items[p.item].vis != nil || c.items[p.item].sub != nil {
			out = append(out, p)
			continue
		}
		for lo := p.lo; lo < p.hi; {
			hi := lo + 1
			for hi < p.hi && c.levels[hi] == c.levels[lo] {
				hi++
			}
			out = append(out, piece{item: p.item, lo: lo, hi: hi})
			lo = hi
		}
	}
	levels := make([]byte, len(out))
	for i, p := range out {
		levels[i] = c.levels[p.lo]
	}
	order := font.BidiOrder(levels)
	res := make([]piece, len(out))
	for i, j := range order {
		res[i] = out[j]
	}
	return res
}

// rightToLeft reports a byte drawn right to left.
func (c *inlineCtx) rightToLeft(at int) bool {
	return c.levels != nil && c.levels[at]&1 != 0
}
