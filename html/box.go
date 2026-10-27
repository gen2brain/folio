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
	text  string

	// x, y, w and h are the content box in CSS pixels.
	x, y, w, h float32
	lines      []lineBox
	// marker is the text a list item draws before its first line; back is painted behind the box.
	marker string
	back   *visual
	// natural is the width a table came out at; markerFace draws the marker.
	natural    float32
	markerFace face
	// reach is the bottom of the subtree, past h when something in it is out of flow.
	reach float32
	// collapsed is a collapsed-border table's grid, edges its segments, and inset the border widths a cell lays out with.
	collapsed   *grid
	edges       []edgeLine
	skipBorders bool
	inset       *[4]float32
}

// edgeLine is one segment of the grid a collapsed table draws, in CSS pixels.
type edgeLine struct {
	e          Border
	x, y, w, h float32
	horizontal bool
}

// lineBox is one line of a block.
type lineBox struct {
	y, h, baseline float32
	// natural is the line's width before alignment.
	natural float32
	frags   []frag
}

// frag is a run of text or a picture on a line.
type frag struct {
	x, w  float32
	text  string
	style *Style
	face  face
	// extra is the justification added to each space.
	extra float32
	// dy is the vertical-align raise above the baseline.
	dy float32
	// sub is the box of an inline-block on the line.
	sub *box
	// vis is set for a picture or a drawing.
	vis *visual
	h   float32
	rtl bool
	// link is the href of the enclosing anchor.
	link string
}

// buildBoxes turns a styled tree into the boxes that lay out.
func buildBoxes(root *Node, st Styles) *box {
	b := build(root, st, &matchCtx{})
	if b == nil {
		return nil
	}
	fixup(b)
	return b
}

func build(n *Node, st Styles, m *matchCtx) *box {
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
		case atom.Svg:
			if n.Namespace == "svg" {
				return &box{kind: imageBox, style: s, node: n}
			}
		}
	}
	b := &box{style: s, node: n}
	if n.Type == xhtml.ElementNode && s.Display == DisplayInline &&
		s.Float == FloatNone && s.Position != PosAbsolute {
		b.kind = inlineBox
	}
	if g := generated(n, st, PseudoBefore); g != nil {
		b.kids = append(b.kids, g)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if k := build(c, st, m); k != nil {
			b.kids = append(b.kids, k)
		}
	}
	if g := generated(n, st, PseudoAfter); g != nil {
		b.kids = append(b.kids, g)
	}
	if b.kind == blockBox && s.Display == DisplayListItem {
		b.marker = listMarker(n, s, m)
	}
	return b
}

// generated is the ::before or ::after box of an element, or nil.
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

// inlineLevel reports a box that goes on a line, not in the block flow.
func (b *box) inlineLevel() bool {
	if b.ownFlow() {
		return true
	}
	switch b.kind {
	case inlineBox, textBox, imageBox, breakBox:
		return true
	}
	return false
}

// ownFlow reports a float, an absolutely placed box or an inline-block.
func (b *box) ownFlow() bool {
	if b.floated() || b.placed() {
		return true
	}
	return b.kind != textBox && b.style != nil && b.style.Display == DisplayInlineBlock
}

func (b *box) floated() bool { return b.kind != textBox && b.style.Float != FloatNone }

// placed reports an absolutely positioned box.
func (b *box) placed() bool { return b.kind != textBox && b.style.Position == PosAbsolute }

// blank reports a text box of only collapsible white space.
func (b *box) blank() bool {
	if b.kind != textBox || b.style.WhiteSpace != WhiteNormal {
		return false
	}
	return strings.TrimFunc(b.text, func(r rune) bool { return r < 128 && isSpaceByte(byte(r)) }) == ""
}

// fixup makes every block's children either all inline or all block, CSS 2.1 9.2.1.1.
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

// letters counts in the alphabet from first, as spreadsheet columns do.
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

// listMarker is the marker text of a list item.
func listMarker(n *Node, s *Style, m *matchCtx) string {
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
	i := m.index(n, false, true)
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
