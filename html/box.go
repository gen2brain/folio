package html

import (
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// boxKind is what a box lays out as.
type boxKind uint8

// The boxes the restricted engine generates.
const (
	blockBox boxKind = iota
	inlineBox
	textBox
	imageBox
	breakBox
)

// box is a node of the layout tree.
type box struct {
	kind  boxKind
	style *Style
	node  *Node
	kids  []*box
	// text is what a text box says, as the source wrote it.
	text string

	// x, y, w and h are the content box in CSS pixels, which layout fills in.
	x, y, w, h float32
	// lines are the line boxes of a block whose children are inline.
	lines []lineBox
	// marker is what a list item draws before its first line, and img the
	// picture an image box draws.
	marker string
	img    *picture
	// natural is how wide a table came out, which its lines do not say, and
	// markerFace what a list item draws its marker with.
	natural    float32
	markerFace face
	// reach is how far down the subtree of a box goes, which is past its own
	// height when something in it is out of the flow.
	reach float32
	// collapsed is the grid a table whose cells share their borders
	// resolved, edges the segments it paints, skipBorders a box whose own
	// borders they replace, and inset the widths such a box lays out with.
	collapsed   *grid
	edges       []edgeLine
	skipBorders bool
	inset       *[4]float32
}

// edgeLine is one segment of the grid a collapsed table draws, in absolute
// CSS pixels.
type edgeLine struct {
	e          Border
	x, y, w, h float32
	horizontal bool
}

// lineBox is one line of a block.
type lineBox struct {
	y, h, baseline float32
	// natural is what the line measures before it is aligned, which a float
	// shrinking to fit is measured by.
	natural float32
	frags   []frag
}

// frag is a run of text or a picture on a line.
type frag struct {
	x, w  float32
	text  string
	style *Style
	face  face
	// extra is what justification adds to each space of the run.
	extra float32
	// img is set for a picture, and h its height.
	img *picture
	h   float32
}

// buildBoxes turns a styled tree into the boxes that lay out.
func buildBoxes(root *Node, st Styles) *box {
	b := build(root, st)
	if b == nil {
		return nil
	}
	fixup(b)
	return b
}

func build(n *Node, st Styles) *box {
	switch n.Type {
	case xhtml.TextNode:
		if n.Data == "" || n.Parent == nil {
			return nil
		}
		s := st.Of(n.Parent)
		if s.Display == DisplayNone {
			return nil
		}
		return &box{kind: textBox, style: s, text: n.Data, node: n}
	case xhtml.ElementNode, xhtml.DocumentNode:
	default:
		return nil
	}
	s := st.Of(n)
	if n.Type == xhtml.ElementNode {
		if s.Display == DisplayNone {
			return nil
		}
		switch n.DataAtom {
		case atom.Br:
			return &box{kind: breakBox, style: s, node: n}
		case atom.Img:
			return &box{kind: imageBox, style: s, node: n}
		}
	}
	b := &box{style: s, node: n}
	// A float and an absolutely placed box are block level whatever display
	// says, so an inline image can be floated out of the line it was written
	// in.
	if n.Type == xhtml.ElementNode && s.Display == DisplayInline &&
		s.Float == FloatNone && s.Position != PosAbsolute {
		b.kind = inlineBox
	}
	if g := generated(n, st, PseudoBefore); g != nil {
		b.kids = append(b.kids, g)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if k := build(c, st); k != nil {
			b.kids = append(b.kids, k)
		}
	}
	if g := generated(n, st, PseudoAfter); g != nil {
		b.kids = append(b.kids, g)
	}
	if b.kind == blockBox && s.Display == DisplayListItem {
		b.marker = listMarker(n, s)
	}
	return b
}

// generated is the box a rule asks for before or after the content of an
// element, and nil when no rule asks for one.
func generated(n *Node, st Styles, p PseudoElement) *box {
	s := st.Pseudo(n, p)
	if s == nil || !s.HasContent || s.Display == DisplayNone {
		return nil
	}
	b := &box{style: s}
	if s.Display == DisplayInline && s.Float == FloatNone && s.Position != PosAbsolute {
		b.kind = inlineBox
	}
	if s.Content != "" {
		b.kids = []*box{{kind: textBox, style: s, text: s.Content}}
	}
	return b
}

// inlineLevel reports a box that belongs on a line rather than in the block
// flow of its parent. A float belongs to neither: it is placed where it is
// written and the lines beside it work around it.
func (b *box) inlineLevel() bool {
	if b.floated() || b.placed() {
		return true
	}
	switch b.kind {
	case inlineBox, textBox, imageBox, breakBox:
		return true
	}
	return false
}

// floated reports a box taken out of the flow. A text box carries the style
// of the element around it, so only a box of its own can be one.
func (b *box) floated() bool { return b.kind != textBox && b.style.Float != FloatNone }

// placed reports a box put somewhere of its own rather than in the flow.
func (b *box) placed() bool { return b.kind != textBox && b.style.Position == PosAbsolute }

// blank reports a text box that is nothing but collapsible white space, which
// generates no box between two blocks.
func (b *box) blank() bool {
	if b.kind != textBox || b.style.WhiteSpace != WhiteNormal {
		return false
	}
	return strings.TrimFunc(b.text, func(r rune) bool { return r < 128 && isSpaceByte(byte(r)) }) == ""
}

// fixup makes every block's children either all inline or all block. An
// inline box that turns out to hold a block becomes one, and runs of inline
// children beside a block are wrapped in a block of their own.
func fixup(b *box) {
	for _, k := range b.kids {
		fixup(k)
	}
	if b.style.Display == DisplayTable && b.style.Collapse {
		collapse(b)
	}
	if b.kind == inlineBox {
		for _, k := range b.kids {
			if !k.inlineLevel() {
				b.kind = blockBox
				break
			}
		}
	}
	if b.kind != blockBox {
		return
	}
	blocks := false
	for _, k := range b.kids {
		if !k.inlineLevel() {
			blocks = true
			break
		}
	}
	if !blocks {
		return
	}
	var out []*box
	var run []*box
	flush := func() {
		if len(run) == 0 {
			return
		}
		empty := true
		for _, k := range run {
			if !k.blank() {
				empty = false
				break
			}
		}
		if !empty {
			// An anonymous block takes the inherited properties of the box
			// it is inside and the initial value of the rest: it has no
			// margin, no padding and no background of its own.
			s := b.style.inherit()
			out = append(out, &box{style: &s, kids: run})
		}
		run = nil
	}
	for _, k := range b.kids {
		if k.inlineLevel() {
			run = append(run, k)
			continue
		}
		flush()
		out = append(out, k)
	}
	flush()
	b.kids = out
}

// romanNumeral writes a number the way an ordered list may ask for.
func romanNumeral(n int) string {
	if n < 1 || n > 3999 {
		return itoa(n)
	}
	var b strings.Builder
	for i, v := range [...]int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1} {
		for n >= v {
			b.WriteString([...]string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}[i])
			n -= v
		}
	}
	return b.String()
}

// letters counts in the alphabet, as a spreadsheet numbers its columns.
func letters(n int, first rune) string {
	if n < 1 {
		return itoa(n)
	}
	var b []rune
	for n > 0 {
		n--
		b = append([]rune{first + rune(n%26)}, b...)
		n /= 26
	}
	return string(b)
}

func itoa(n int) string { return strconv.Itoa(n) }

// listMarker is what a list item draws before its first line, which for an
// ordered list counts its position among its siblings.
func listMarker(n *Node, s *Style) string {
	switch s.ListStyle {
	case ListNone:
		return ""
	case ListDisc:
		return "•"
	case ListCircle:
		return "◦"
	case ListSquare:
		return "▪"
	}
	i := index(n, false, true)
	switch s.ListStyle {
	case ListDecimal:
		return itoa(i) + "."
	case ListLowerAlpha:
		return letters(i, 'a') + "."
	case ListUpperAlpha:
		return letters(i, 'A') + "."
	case ListLowerRoman:
		return strings.ToLower(romanNumeral(i)) + "."
	case ListUpperRoman:
		return romanNumeral(i) + "."
	}
	return "•"
}
