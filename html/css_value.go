package html

import (
	"math"
	"strings"

	"github.com/gen2brain/folio/gfx"
)

// Unit is what a Length is measured in once the cascade has computed it.
type Unit uint8

// The units a computed length is left in. Everything absolute is folded into
// CSS pixels and a percentage is left standing.
const (
	UnitAuto Unit = iota
	UnitPx
	UnitPercent
	UnitScale
)

// Length is a computed length.
type Length struct {
	Value float32
	Unit  Unit
}

// Px is a length in CSS pixels.
func Px(v float32) Length { return Length{Value: v, Unit: UnitPx} }

// Auto reports the absence of a length.
func (l Length) Auto() bool { return l.Unit == UnitAuto }

// Resolve returns the length in CSS pixels against a containing block of the
// given size, and zero for a length there is none of.
func (l Length) Resolve(against float32) float32 {
	switch l.Unit {
	case UnitPx:
		return l.Value
	case UnitPercent:
		return l.Value * against / 100
	case UnitScale:
		return l.Value * against
	}
	return 0
}

// Color is a color as CSS writes one.
type Color struct{ R, G, B, A uint8 }

// Display is the box an element generates.
type Display uint8

// The display values the engine reads. Everything it does not, a table or a
// flex container, becomes a block.
const (
	DisplayInline Display = iota
	DisplayBlock
	DisplayInlineBlock
	DisplayListItem
	DisplayTable
	DisplayTableRowGroup
	DisplayTableRow
	DisplayTableCell
	DisplayTableCaption
	DisplayNone
)

// TextAlign is how a line box sits in its containing block.
type TextAlign uint8

// The alignments. Start is the initial value and End its opposite, which are
// the two edges of the direction the text runs in.
const (
	AlignStart TextAlign = iota
	AlignLeft
	AlignRight
	AlignCenter
	AlignJustify
	AlignEnd
)

// Resolve turns the two alignments that depend on the direction into the two
// that do not.
func (a TextAlign) Resolve(d Direction) TextAlign {
	switch a {
	case AlignStart:
		if d.RTL() {
			return AlignRight
		}
		return AlignLeft
	case AlignEnd:
		if d.RTL() {
			return AlignLeft
		}
		return AlignRight
	}
	return a
}

// FontStyle is the slant of a face.
type FontStyle uint8

// The slants.
const (
	StyleNormal FontStyle = iota
	StyleItalic
	StyleOblique
)

// TextTransform is the case a run of text is drawn in.
type TextTransform uint8

// The text transforms.
const (
	TransformNone TextTransform = iota
	TransformUpper
	TransformLower
	TransformCapitalize
)

// WhiteSpace is how the white space of the source is collapsed.
type WhiteSpace uint8

// The white space values.
const (
	WhiteNormal WhiteSpace = iota
	WhitePre
	WhiteNowrap
	WhitePreWrap
	WhitePreLine
)

// ListStyle is the marker a list item carries.
type ListStyle uint8

// The markers.
const (
	ListNone ListStyle = iota
	ListDisc
	ListCircle
	ListSquare
	ListDecimal
	ListLowerAlpha
	ListUpperAlpha
	ListLowerRoman
	ListUpperRoman
)

// Position is how a box is placed against the flow.
type Position uint8

// The position values. A fixed box is placed like an absolute one: a book has
// no viewport to fix it to.
const (
	PosStatic Position = iota
	PosRelative
	PosAbsolute
)

// Float is the side a box is taken out of the flow to.
type Float uint8

// The float values.
const (
	FloatNone Float = iota
	FloatLeft
	FloatRight
)

// Clear is which floats a box must come below.
type Clear uint8

// The clear values.
const (
	ClearNone Clear = iota
	ClearLeft
	ClearRight
	ClearBoth
)

// PageBreak is what a page break property asks for.
type PageBreak uint8

// The page break values.
const (
	BreakAuto PageBreak = iota
	BreakAlways
	BreakAvoid
)

// VerticalAlign is the baseline an inline box sits on.
type VerticalAlign uint8

// The vertical alignments.
const (
	AlignBaseline VerticalAlign = iota
	AlignSub
	AlignSuper
	AlignTop
	AlignMiddle
	AlignBottom
	AlignTextTop
	AlignTextBottom
	AlignLength
)

// Decoration is a set of text decoration lines.
type Decoration uint8

// The decoration lines.
const (
	Underline Decoration = 1 << iota
	Overline
	LineThrough
)

// BorderStyle is how an edge of a box is drawn.
type BorderStyle uint8

// The border styles. The three dimensional ones are drawn solid.
const (
	BorderNone BorderStyle = iota
	BorderSolid
	BorderDashed
	BorderDotted
	BorderDouble
	// BorderHidden draws nothing, and in a collapsed table it takes the
	// edge away from every other border that meets it.
	BorderHidden
)

// Repeat is how the picture behind a box is tiled.
type Repeat uint8

// The background repeats.
const (
	RepeatBoth Repeat = iota
	RepeatX
	RepeatY
	RepeatNone
)

// Direction is which way the characters of a box run, CSS 2.1 9.10.
type Direction uint8

// The two directions.
const (
	DirLTR Direction = iota
	DirRTL
)

// RTL reports a direction that runs right to left.
func (d Direction) RTL() bool { return d == DirRTL }

// Writing is the direction the lines of a box run in.
type Writing uint8

// The writing modes.
const (
	WritingHorizontal Writing = iota
	WritingVerticalRL
	WritingVerticalLR
)

// Vertical reports a mode whose lines run down the page.
func (w Writing) Vertical() bool { return w != WritingHorizontal }

// Orientation is how a character is turned in vertical text.
type Orientation uint8

// The text orientations.
const (
	OrientMixed Orientation = iota
	OrientUpright
	OrientSideways
)

// Border is one edge of a box.
type Border struct {
	Width float32
	Style BorderStyle
	Color Color
}

// Thickness is how much room the edge takes, which is none when nothing is
// drawn there.
func (b Border) Thickness() float32 {
	if b.Style == BorderNone || b.Style == BorderHidden || b.Width <= 0 {
		return 0
	}
	return b.Width
}

// Style is the computed value of every property the engine reads. Lengths are
// in CSS pixels except where a percentage survived the cascade.
type Style struct {
	Display Display

	MarginTop, MarginRight, MarginBottom, MarginLeft     Length
	PaddingTop, PaddingRight, PaddingBottom, PaddingLeft Length
	BorderTop, BorderRight, BorderBottom, BorderLeft     Border
	Width, Height                                        Length
	MinWidth, MaxWidth, MinHeight, MaxHeight             Length

	Position                    Position
	Top, Right, Bottom, LeftPos Length

	FontFamily []string
	FontSize   float32
	FontStyle  FontStyle
	FontWeight int
	LineHeight Length
	// SmallCaps draws the lower case of a run as capitals of a smaller size.
	SmallCaps bool

	Color      Color
	Background Color
	// BackgroundImage is the picture painted over the colour, with where in
	// the box it sits, how big it is drawn and how it is tiled.
	BackgroundImage          string
	BackgroundRepeat         Repeat
	BackgroundX, BackgroundY Length
	BackgroundW, BackgroundH Length

	TextAlign     TextAlign
	TextIndent    Length
	TextTransform TextTransform
	LetterSpacing float32
	Decoration    Decoration
	WhiteSpace    WhiteSpace
	VerticalAlign VerticalAlign
	// VerticalShift is how far AlignLength raises the box, a percentage of
	// the line height.
	VerticalShift Length
	ListStyle     ListStyle

	Float Float
	Clear Clear

	// Writing is the direction the lines run in, Orient how a character is
	// turned when they run down the page, and Direction which way the
	// characters of a line run.
	Writing   Writing
	Orient    Orientation
	Direction Direction

	// Collapse is whether the cells of a table share the borders between
	// them, and Spacing the gap between them when they do not.
	Collapse           bool
	SpacingX, SpacingY Length

	// Radius is the corner radii of the border box, clockwise from the top
	// left.
	Radius [4]Corner

	// Content is the text a rule generates before or after an element, and
	// HasContent whether it generates a box there at all.
	Content    string
	HasContent bool

	BreakBefore, BreakAfter PageBreak
}

// DefaultFontSize is what a medium font is, and what every relative size is
// worked out from.
const DefaultFontSize = 16

func initialStyle() Style {
	return Style{
		FontSize:     DefaultFontSize,
		FontWeight:   400,
		LineHeight:   Length{Value: 1.2, Unit: UnitScale},
		Color:        Color{A: 255},
		MarginTop:    Px(0),
		MarginRight:  Px(0),
		MarginBottom: Px(0),
		MarginLeft:   Px(0),
	}
}

// inherit returns the style a child starts from: the inherited properties of
// the parent and the initial value of the rest.
func (s *Style) inherit() Style {
	c := initialStyle()
	c.FontFamily = s.FontFamily
	c.FontSize = s.FontSize
	c.FontStyle = s.FontStyle
	c.FontWeight = s.FontWeight
	c.LineHeight = s.LineHeight
	c.SmallCaps = s.SmallCaps
	c.Color = s.Color
	c.TextAlign = s.TextAlign
	c.TextIndent = s.TextIndent
	c.TextTransform = s.TextTransform
	c.LetterSpacing = s.LetterSpacing
	c.Decoration = s.Decoration
	c.WhiteSpace = s.WhiteSpace
	c.ListStyle = s.ListStyle
	c.Writing, c.Orient = s.Writing, s.Orient
	c.Direction = s.Direction
	c.Collapse = s.Collapse
	c.SpacingX, c.SpacingY = s.SpacingX, s.SpacingY
	return c
}

// Corner is the two radii one corner of a border box is rounded by.
type Corner struct {
	X, Y Length
}

// Zero reports a corner that is not rounded at all.
func (c Corner) Zero() bool { return c.X.Value <= 0 || c.Y.Value <= 0 }

// value carries what a declaration's tokens are read against: the element's
// own font size for an em, the root's for a rem, and the parent's style for
// an explicit inherit.
type value struct {
	toks []cssToken
	// em is the element's own font size, rm the root element's and medium the
	// size a font size keyword is a multiple of.
	em, rm, medium float32
	parent         *Style
}

func (v value) first() cssToken {
	if len(v.toks) == 0 {
		return cssToken{}
	}
	return v.toks[0]
}

func (v value) parentSize() float32 {
	if v.parent == nil {
		return DefaultFontSize
	}
	return v.parent.FontSize
}

func (v value) parentWeight() int {
	if v.parent == nil {
		return 400
	}
	return v.parent.FontWeight
}

// The corners of a border box, clockwise from the top left, in the order one
// to four values of the shorthand fill them.
var cornerNames = [4]string{
	"border-top-left-radius", "border-top-right-radius",
	"border-bottom-right-radius", "border-bottom-left-radius",
}

func cornerAt(name string) int {
	for i, n := range cornerNames {
		if n == name {
			return i
		}
	}
	return 0
}

// firstIdent is the name of the first value of a declaration, lower cased.
func firstIdent(toks []cssToken) string {
	parts := splitSpace(splitCommas(toks)[0])
	if len(parts) == 0 || len(parts[0]) != 1 || parts[0][0].kind != cssIdent {
		return ""
	}
	return strings.ToLower(parts[0][0].value)
}

// url reads the address of the first picture a declaration names, which is
// written either as a url token or as a function around a string.
func (v value) url() string {
	toks := splitCommas(v.toks)[0]
	for i, t := range toks {
		switch {
		case t.kind == cssURL:
			return t.value
		case t.kind == cssFunction && strings.EqualFold(t.value, "url"):
			if args, _, ok := parseArgs(toks[i:]); ok {
				if a := skipSpace(args); len(a) == 1 && a[0].kind == cssString {
					return a[0].value
				}
			}
			return ""
		case t.kind == cssIdent && strings.EqualFold(t.value, "none"):
			return ""
		}
	}
	return ""
}

// The offsets the keywords of a background position stand for.
var positionKeywords = map[string]Length{
	"left": {Unit: UnitPercent}, "top": {Unit: UnitPercent},
	"center": {Value: 50, Unit: UnitPercent},
	"right":  {Value: 100, Unit: UnitPercent}, "bottom": {Value: 100, Unit: UnitPercent},
}

// position reads where in a box the picture behind it sits.
func (v value) position() (Length, Length, bool) {
	parts := splitSpace(splitCommas(v.toks)[0])
	if len(parts) == 0 || len(parts) > 2 {
		return Length{}, Length{}, false
	}
	var out [2]Length
	vertical := [2]bool{}
	for i, part := range parts {
		if len(part) != 1 {
			return Length{}, Length{}, false
		}
		if part[0].kind == cssIdent {
			name := strings.ToLower(part[0].value)
			l, ok := positionKeywords[name]
			if !ok {
				return Length{}, Length{}, false
			}
			out[i], vertical[i] = l, name == "top" || name == "bottom"
			continue
		}
		l, ok := length(part[0], v.em, v.rm)
		if !ok {
			return Length{}, Length{}, false
		}
		out[i] = l
	}
	if len(parts) == 1 {
		return out[0], Length{Value: 50, Unit: UnitPercent}, true
	}
	if vertical[0] {
		return out[1], out[0], true
	}
	return out[0], out[1], true
}

// size reads how big the picture behind a box is drawn, in which a length
// there is none of means the size the picture has.
func (v value) size() (Length, Length, bool) {
	parts := splitSpace(splitCommas(v.toks)[0])
	if len(parts) == 0 || len(parts) > 2 {
		return Length{}, Length{}, false
	}
	var out [2]Length
	for i, part := range parts {
		if len(part) != 1 {
			return Length{}, Length{}, false
		}
		if part[0].kind == cssIdent {
			continue
		}
		l, ok := length(part[0], v.em, v.rm)
		if !ok {
			return Length{}, Length{}, false
		}
		out[i] = l
	}
	return out[0], out[1], true
}

// spacing reads the one or two lengths a table leaves between its cells.
func (v value) spacing() (Length, Length, bool) {
	parts := splitSpace(v.toks)
	if len(parts) == 0 || len(parts) > 2 || len(parts[0]) != 1 {
		return Length{}, Length{}, false
	}
	x, ok := length(parts[0][0], v.em, v.rm)
	if !ok || x.Unit != UnitPx {
		return Length{}, Length{}, false
	}
	y := x
	if len(parts) == 2 {
		if len(parts[1]) != 1 {
			return Length{}, Length{}, false
		}
		if y, ok = length(parts[1][0], v.em, v.rm); !ok || y.Unit != UnitPx {
			return Length{}, Length{}, false
		}
	}
	return x, y, true
}

// corner reads the one or two lengths a corner is rounded by, the vertical
// one taking the horizontal when it is left out.
func (v value) corner() (Corner, bool) {
	parts := splitSpace(v.toks)
	if len(parts) == 0 || len(parts) > 2 {
		return Corner{}, false
	}
	x, ok := length(parts[0][0], v.em, v.rm)
	if !ok || len(parts[0]) != 1 {
		return Corner{}, false
	}
	y := x
	if len(parts) == 2 {
		if y, ok = length(parts[1][0], v.em, v.rm); !ok || len(parts[1]) != 1 {
			return Corner{}, false
		}
	}
	return Corner{X: x, Y: y}, true
}

// content reads what a rule generates: the strings it is written as, joined,
// and nothing at all for none, normal and the functions this does not read.
func (v value) content() (string, bool) {
	var b strings.Builder
	found := false
	for _, t := range v.toks {
		switch t.kind {
		case cssSpace:
		case cssString:
			b.WriteString(t.value)
			found = true
		default:
			return "", false
		}
	}
	return b.String(), found
}

func (v value) ident() string {
	if t := v.first(); t.kind == cssIdent && len(v.toks) == 1 {
		return strings.ToLower(t.value)
	}
	return ""
}

// The factors that take an absolute unit to a CSS pixel.
var absoluteUnits = map[string]float32{
	"px": 1,
	"pt": 96.0 / 72,
	"pc": 16,
	"in": 96,
	"cm": 96 / 2.54,
	"mm": 96 / 25.4,
	"q":  96 / 101.6,
}

// length reads one length, resolving everything but a percentage.
func length(t cssToken, em, rm float32) (Length, bool) {
	switch t.kind {
	case cssNumber:
		if t.num == 0 {
			return Px(0), true
		}
	case cssPercentage:
		return Length{Value: float32(t.num), Unit: UnitPercent}, true
	case cssDimension:
		if f, ok := absoluteUnits[t.unit]; ok {
			return Px(float32(t.num) * f), true
		}
		switch t.unit {
		case "em":
			return Px(float32(t.num) * em), true
		case "rem":
			return Px(float32(t.num) * rm), true
		case "ex":
			return Px(float32(t.num) * em / 2), true
		case "ch":
			return Px(float32(t.num) * em / 2), true
		}
	}
	return Length{}, false
}

// lengthAuto reads a length or the keyword that stands for none of one.
func (v value) lengthAuto() (Length, bool) {
	switch v.ident() {
	case "auto", "none":
		return Length{}, true
	}
	if len(v.toks) != 1 {
		return Length{}, false
	}
	return length(v.toks[0], v.em, v.rm)
}

func (v value) length() (Length, bool) {
	if len(v.toks) != 1 {
		return Length{}, false
	}
	return length(v.toks[0], v.em, v.rm)
}

// The relative font size keywords, as multiples of a medium one.
var fontSizeKeywords = map[string]float32{
	"xx-small":  3.0 / 5,
	"x-small":   3.0 / 4,
	"small":     8.0 / 9,
	"medium":    1,
	"large":     6.0 / 5,
	"x-large":   3.0 / 2,
	"xx-large":  2,
	"xxx-large": 3,
}

// fontSize reads a font size, whose em is the parent's and not its own.
func (v value) fontSize(parent float32) (float32, bool) {
	switch k := v.ident(); k {
	case "":
	case "smaller":
		return parent / 1.2, true
	case "larger":
		return parent * 1.2, true
	default:
		if f, ok := fontSizeKeywords[k]; ok {
			return f * orDefault(v.medium, DefaultFontSize), true
		}
		return 0, false
	}
	l, ok := v.length()
	if !ok {
		return 0, false
	}
	switch l.Unit {
	case UnitPx:
		return max(l.Value, 0), true
	case UnitPercent:
		return max(parent*l.Value/100, 0), true
	}
	return 0, false
}

func (v value) fontWeight(parent int) (int, bool) {
	switch v.ident() {
	case "normal":
		return 400, true
	case "bold":
		return 700, true
	case "bolder":
		if parent < 400 {
			return 400, true
		}
		if parent < 600 {
			return 700, true
		}
		return 900, true
	case "lighter":
		if parent > 700 {
			return 700, true
		}
		if parent > 500 {
			return 400, true
		}
		return 100, true
	}
	if t := v.first(); len(v.toks) == 1 && t.kind == cssNumber && t.integer {
		n := int(t.num)
		if n >= 1 && n <= 1000 {
			return n, true
		}
	}
	return 0, false
}

// families reads a font family list, keeping the generic names as written so
// that the font layer decides what they mean.
func (v value) families() ([]string, bool) {
	var out []string
	for _, part := range splitCommas(v.toks) {
		part = skipSpace(part)
		if len(part) == 0 {
			continue
		}
		if part[0].kind == cssString {
			if len(part) != 1 {
				return nil, false
			}
			out = append(out, part[0].value)
			continue
		}
		var b strings.Builder
		for _, t := range part {
			switch t.kind {
			case cssIdent:
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(t.value)
			case cssSpace:
			default:
				return nil, false
			}
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// lineHeight reads a line height, keeping a bare number as the multiple of
// the font size it is.
func (v value) lineHeight() (Length, bool) {
	if v.ident() == "normal" {
		return Length{Value: 1.2, Unit: UnitScale}, true
	}
	if t := v.first(); len(v.toks) == 1 && t.kind == cssNumber {
		return Length{Value: float32(t.num), Unit: UnitScale}, true
	}
	return v.length()
}

// color reads a color: a hash, one of the color functions, or a name.
func (v value) color() (Color, bool) {
	if len(v.toks) == 0 {
		return Color{}, false
	}
	t := v.toks[0]
	switch t.kind {
	case cssHash:
		if len(v.toks) != 1 {
			return Color{}, false
		}
		return hexColor(t.value)
	case cssIdent:
		if len(v.toks) != 1 {
			return Color{}, false
		}
		name := strings.ToLower(t.value)
		if name == "transparent" {
			return Color{}, true
		}
		r, g, b, ok := gfx.NamedColor(name)
		return Color{r, g, b, 255}, ok
	case cssFunction:
		args, rest, ok := parseArgs(v.toks)
		if !ok || len(skipSpace(rest)) != 0 {
			return Color{}, false
		}
		switch strings.ToLower(t.value) {
		case "rgb", "rgba":
			return rgbColor(args)
		case "hsl", "hsla":
			return hslColor(args)
		}
	}
	return Color{}, false
}

func hexColor(s string) (Color, bool) {
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return Color{}, false
		}
	}
	h := func(i int) uint8 { return uint8(hexValue(s[i])) }
	switch len(s) {
	case 3:
		return Color{h(0) * 17, h(1) * 17, h(2) * 17, 255}, true
	case 4:
		return Color{h(0) * 17, h(1) * 17, h(2) * 17, h(3) * 17}, true
	case 6:
		return Color{h(0)<<4 | h(1), h(2)<<4 | h(3), h(4)<<4 | h(5), 255}, true
	case 8:
		return Color{h(0)<<4 | h(1), h(2)<<4 | h(3), h(4)<<4 | h(5), h(6)<<4 | h(7)}, true
	}
	return Color{}, false
}

// colorArg is one argument of a color function, as written: a percentage
// means a different thing in each position.
type colorArg struct {
	v   float64
	pct bool
}

func colorArgs(args []cssToken) ([]colorArg, bool) {
	var out []colorArg
	for _, t := range args {
		switch t.kind {
		case cssSpace, cssComma:
		case cssNumber:
			out = append(out, colorArg{v: t.num})
		case cssPercentage:
			out = append(out, colorArg{v: t.num, pct: true})
		case cssDimension:
			if t.unit != "deg" {
				return nil, false
			}
			out = append(out, colorArg{v: t.num})
		case cssDelim:
			if t.delim != '/' {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	if len(out) < 3 || len(out) > 4 {
		return nil, false
	}
	return out, true
}

func (a colorArg) channel() float64 {
	if a.pct {
		return a.v * 255 / 100
	}
	return a.v
}

func (a colorArg) alpha() float64 {
	if a.pct {
		return a.v * 255 / 100
	}
	return a.v * 255
}

func (a colorArg) fraction() float64 { return min(max(a.v/100, 0), 1) }

func rgbColor(args []cssToken) (Color, bool) {
	n, ok := colorArgs(args)
	if !ok {
		return Color{}, false
	}
	c := Color{clamp255(n[0].channel()), clamp255(n[1].channel()), clamp255(n[2].channel()), 255}
	if len(n) == 4 {
		c.A = clamp255(n[3].alpha())
	}
	return c, true
}

func hslColor(args []cssToken) (Color, bool) {
	n, ok := colorArgs(args)
	if !ok {
		return Color{}, false
	}
	h := math.Mod(math.Mod(n[0].v, 360)+360, 360) / 360
	s, l := n[1].fraction(), n[2].fraction()
	c := Color{
		clamp255(hueToRGB(h+1.0/3, s, l) * 255),
		clamp255(hueToRGB(h, s, l) * 255),
		clamp255(hueToRGB(h-1.0/3, s, l) * 255),
		255,
	}
	if len(n) == 4 {
		c.A = clamp255(n[3].alpha())
	}
	return c, true
}

func hueToRGB(h, s, l float64) float64 {
	q := l * (1 + s)
	if l >= 0.5 {
		q = l + s - l*s
	}
	p := 2*l - q
	h = math.Mod(math.Mod(h, 1)+1, 1)
	switch {
	case h < 1.0/6:
		return p + (q-p)*6*h
	case h < 1.0/2:
		return q
	case h < 2.0/3:
		return p + (q-p)*(2.0/3-h)*6
	}
	return p
}

func clamp255(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// inheritedProps is which properties a child takes from its parent when
// nothing sets them, and what unset means for each.
var inheritedProps = map[string]bool{
	"color":                true,
	"font-family":          true,
	"font-size":            true,
	"font-style":           true,
	"font-weight":          true,
	"line-height":          true,
	"font-variant":         true,
	"text-transform":       true,
	"letter-spacing":       true,
	"list-style-type":      true,
	"text-align":           true,
	"text-indent":          true,
	"text-decoration-line": true,
	"white-space":          true,
}

// applyProp sets one longhand property of a style and reports whether it is
// a property the engine reads at all.
func applyProp(s *Style, name string, v value) bool {
	switch v.ident() {
	case "inherit":
		return copyProp(s, v.parent, name)
	case "initial":
		init := initialStyle()
		return copyProp(s, &init, name)
	case "unset", "revert":
		if inheritedProps[name] {
			return copyProp(s, v.parent, name)
		}
		init := initialStyle()
		return copyProp(s, &init, name)
	}
	switch name {
	case "display":
		if d, ok := v.display(); ok {
			s.Display = d
		}
	case "margin-top":
		setLength(&s.MarginTop, v)
	case "margin-right":
		setLength(&s.MarginRight, v)
	case "margin-bottom":
		setLength(&s.MarginBottom, v)
	case "margin-left":
		setLength(&s.MarginLeft, v)
	case "padding-top":
		setLength(&s.PaddingTop, v)
	case "padding-right":
		setLength(&s.PaddingRight, v)
	case "padding-bottom":
		setLength(&s.PaddingBottom, v)
	case "padding-left":
		setLength(&s.PaddingLeft, v)
	case "border-top-width":
		setBorderWidth(&s.BorderTop, v)
	case "border-right-width":
		setBorderWidth(&s.BorderRight, v)
	case "border-bottom-width":
		setBorderWidth(&s.BorderBottom, v)
	case "border-left-width":
		setBorderWidth(&s.BorderLeft, v)
	case "border-top-style":
		setBorderStyle(&s.BorderTop, v)
	case "border-right-style":
		setBorderStyle(&s.BorderRight, v)
	case "border-bottom-style":
		setBorderStyle(&s.BorderBottom, v)
	case "border-left-style":
		setBorderStyle(&s.BorderLeft, v)
	case "border-top-color":
		setBorderColor(&s.BorderTop, s, v)
	case "border-right-color":
		setBorderColor(&s.BorderRight, s, v)
	case "border-bottom-color":
		setBorderColor(&s.BorderBottom, s, v)
	case "border-left-color":
		setBorderColor(&s.BorderLeft, s, v)
	case "width":
		setLength(&s.Width, v)
	case "height":
		setLength(&s.Height, v)
	case "min-width":
		setLength(&s.MinWidth, v)
	case "max-width":
		setLength(&s.MaxWidth, v)
	case "min-height":
		setLength(&s.MinHeight, v)
	case "max-height":
		setLength(&s.MaxHeight, v)
	case "position":
		switch v.ident() {
		case "static":
			s.Position = PosStatic
		case "relative":
			s.Position = PosRelative
		case "absolute", "fixed":
			s.Position = PosAbsolute
		}
	case "top":
		setLength(&s.Top, v)
	case "right":
		setLength(&s.Right, v)
	case "bottom":
		setLength(&s.Bottom, v)
	case "left":
		setLength(&s.LeftPos, v)
	case "text-indent":
		setLength(&s.TextIndent, v)
	case "font-family":
		if f, ok := v.families(); ok {
			s.FontFamily = f
		}
	case "font-size":
		if size, ok := v.fontSize(v.parentSize()); ok {
			s.FontSize = size
		}
	case "font-style":
		switch v.ident() {
		case "normal":
			s.FontStyle = StyleNormal
		case "italic":
			s.FontStyle = StyleItalic
		case "oblique":
			s.FontStyle = StyleOblique
		}
	case "font-weight":
		if w, ok := v.fontWeight(v.parentWeight()); ok {
			s.FontWeight = w
		}
	case "line-height":
		if l, ok := v.lineHeight(); ok {
			s.LineHeight = l
		}
	case "font-variant":
		switch v.ident() {
		case "small-caps", "all-small-caps":
			s.SmallCaps = true
		case "normal", "none":
			s.SmallCaps = false
		}
	case "text-transform":
		switch v.ident() {
		case "none":
			s.TextTransform = TransformNone
		case "uppercase":
			s.TextTransform = TransformUpper
		case "lowercase":
			s.TextTransform = TransformLower
		case "capitalize":
			s.TextTransform = TransformCapitalize
		}
	case "letter-spacing":
		if v.ident() == "normal" {
			s.LetterSpacing = 0
		} else if l, ok := v.length(); ok && l.Unit == UnitPx {
			s.LetterSpacing = l.Value
		}
	case "color":
		if c, ok := v.color(); ok {
			s.Color = c
		}
	case "background-color":
		if c, ok := v.color(); ok {
			s.Background = c
		}
	case "text-align":
		switch v.ident() {
		case "left":
			s.TextAlign = AlignLeft
		case "start":
			s.TextAlign = AlignStart
		case "right":
			s.TextAlign = AlignRight
		case "end":
			s.TextAlign = AlignEnd
		case "center":
			s.TextAlign = AlignCenter
		case "justify":
			s.TextAlign = AlignJustify
		}
	case "text-decoration-line":
		if d, ok := v.decoration(); ok {
			s.Decoration = d
		}
	case "white-space":
		switch v.ident() {
		case "normal":
			s.WhiteSpace = WhiteNormal
		case "pre":
			s.WhiteSpace = WhitePre
		case "nowrap":
			s.WhiteSpace = WhiteNowrap
		case "pre-wrap":
			s.WhiteSpace = WhitePreWrap
		case "pre-line":
			s.WhiteSpace = WhitePreLine
		}
	case "vertical-align":
		switch v.ident() {
		case "baseline":
			s.VerticalAlign = AlignBaseline
		case "sub":
			s.VerticalAlign = AlignSub
		case "super":
			s.VerticalAlign = AlignSuper
		case "top":
			s.VerticalAlign = AlignTop
		case "middle":
			s.VerticalAlign = AlignMiddle
		case "bottom":
			s.VerticalAlign = AlignBottom
		case "text-top":
			s.VerticalAlign = AlignTextTop
		case "text-bottom":
			s.VerticalAlign = AlignTextBottom
		default:
			if l, ok := v.length(); ok {
				s.VerticalAlign, s.VerticalShift = AlignLength, l
			}
		}
	case "list-style-type":
		if l, ok := v.listStyle(); ok {
			s.ListStyle = l
		}
	case "float":
		switch v.ident() {
		case "none":
			s.Float = FloatNone
		case "left":
			s.Float = FloatLeft
		case "right":
			s.Float = FloatRight
		}
	case "content":
		s.Content, s.HasContent = v.content()
	case "background-image":
		s.BackgroundImage = v.url()
	case "background-repeat":
		switch firstIdent(v.toks) {
		case "repeat":
			s.BackgroundRepeat = RepeatBoth
		case "repeat-x":
			s.BackgroundRepeat = RepeatX
		case "repeat-y":
			s.BackgroundRepeat = RepeatY
		case "no-repeat":
			s.BackgroundRepeat = RepeatNone
		}
	case "background-position":
		if x, y, ok := v.position(); ok {
			s.BackgroundX, s.BackgroundY = x, y
		}
	case "background-size":
		if w, h, ok := v.size(); ok {
			s.BackgroundW, s.BackgroundH = w, h
		}
	case "direction":
		switch v.ident() {
		case "ltr":
			s.Direction = DirLTR
		case "rtl":
			s.Direction = DirRTL
		}
	case "writing-mode", "-epub-writing-mode", "-webkit-writing-mode", "-ms-writing-mode":
		switch v.ident() {
		case "horizontal-tb", "lr-tb", "rl-tb":
			s.Writing = WritingHorizontal
		case "vertical-rl", "tb-rl", "tb":
			s.Writing = WritingVerticalRL
		case "vertical-lr":
			s.Writing = WritingVerticalLR
		}
	case "text-orientation", "-epub-text-orientation", "-webkit-text-orientation":
		switch v.ident() {
		case "mixed":
			s.Orient = OrientMixed
		case "upright":
			s.Orient = OrientUpright
		case "sideways", "sideways-right", "rotate-right":
			s.Orient = OrientSideways
		}
	case "border-collapse":
		switch v.ident() {
		case "collapse":
			s.Collapse = true
		case "separate":
			s.Collapse = false
		}
	case "border-spacing":
		if x, y, ok := v.spacing(); ok {
			s.SpacingX, s.SpacingY = x, y
		}
	case "border-top-left-radius", "border-top-right-radius",
		"border-bottom-right-radius", "border-bottom-left-radius":
		if c, ok := v.corner(); ok {
			s.Radius[cornerAt(name)] = c
		}
	case "clear":
		switch v.ident() {
		case "none":
			s.Clear = ClearNone
		case "left":
			s.Clear = ClearLeft
		case "right":
			s.Clear = ClearRight
		case "both":
			s.Clear = ClearBoth
		}
	case "page-break-before", "break-before":
		if b, ok := v.pageBreak(); ok {
			s.BreakBefore = b
		}
	case "page-break-after", "break-after":
		if b, ok := v.pageBreak(); ok {
			s.BreakAfter = b
		}
	default:
		return false
	}
	return true
}

// copyProp takes one property from another style.
func copyProp(dst, src *Style, name string) bool {
	if src == nil {
		return false
	}
	switch name {
	case "display":
		dst.Display = src.Display
	case "margin-top":
		dst.MarginTop = src.MarginTop
	case "margin-right":
		dst.MarginRight = src.MarginRight
	case "margin-bottom":
		dst.MarginBottom = src.MarginBottom
	case "margin-left":
		dst.MarginLeft = src.MarginLeft
	case "padding-top":
		dst.PaddingTop = src.PaddingTop
	case "padding-right":
		dst.PaddingRight = src.PaddingRight
	case "padding-bottom":
		dst.PaddingBottom = src.PaddingBottom
	case "padding-left":
		dst.PaddingLeft = src.PaddingLeft
	case "border-top-width":
		dst.BorderTop.Width = src.BorderTop.Width
	case "border-right-width":
		dst.BorderRight.Width = src.BorderRight.Width
	case "border-bottom-width":
		dst.BorderBottom.Width = src.BorderBottom.Width
	case "border-left-width":
		dst.BorderLeft.Width = src.BorderLeft.Width
	case "border-top-style":
		dst.BorderTop.Style = src.BorderTop.Style
	case "border-right-style":
		dst.BorderRight.Style = src.BorderRight.Style
	case "border-bottom-style":
		dst.BorderBottom.Style = src.BorderBottom.Style
	case "border-left-style":
		dst.BorderLeft.Style = src.BorderLeft.Style
	case "border-top-color":
		dst.BorderTop.Color = src.BorderTop.Color
	case "border-right-color":
		dst.BorderRight.Color = src.BorderRight.Color
	case "border-bottom-color":
		dst.BorderBottom.Color = src.BorderBottom.Color
	case "border-left-color":
		dst.BorderLeft.Color = src.BorderLeft.Color
	case "width":
		dst.Width = src.Width
	case "height":
		dst.Height = src.Height
	case "min-width":
		dst.MinWidth = src.MinWidth
	case "max-width":
		dst.MaxWidth = src.MaxWidth
	case "min-height":
		dst.MinHeight = src.MinHeight
	case "max-height":
		dst.MaxHeight = src.MaxHeight
	case "position":
		dst.Position = src.Position
	case "top":
		dst.Top = src.Top
	case "right":
		dst.Right = src.Right
	case "bottom":
		dst.Bottom = src.Bottom
	case "left":
		dst.LeftPos = src.LeftPos
	case "text-indent":
		dst.TextIndent = src.TextIndent
	case "font-family":
		dst.FontFamily = src.FontFamily
	case "font-size":
		dst.FontSize = src.FontSize
	case "font-style":
		dst.FontStyle = src.FontStyle
	case "font-weight":
		dst.FontWeight = src.FontWeight
	case "line-height":
		dst.LineHeight = src.LineHeight
	case "font-variant":
		dst.SmallCaps = src.SmallCaps
	case "text-transform":
		dst.TextTransform = src.TextTransform
	case "letter-spacing":
		dst.LetterSpacing = src.LetterSpacing
	case "color":
		dst.Color = src.Color
	case "background-color":
		dst.Background = src.Background
	case "text-align":
		dst.TextAlign = src.TextAlign
	case "text-decoration-line":
		dst.Decoration = src.Decoration
	case "white-space":
		dst.WhiteSpace = src.WhiteSpace
	case "vertical-align":
		dst.VerticalAlign, dst.VerticalShift = src.VerticalAlign, src.VerticalShift
	case "list-style-type":
		dst.ListStyle = src.ListStyle
	case "float":
		dst.Float = src.Float
	case "content":
		dst.Content, dst.HasContent = src.Content, src.HasContent
	case "background-image":
		dst.BackgroundImage = src.BackgroundImage
	case "background-repeat":
		dst.BackgroundRepeat = src.BackgroundRepeat
	case "background-position":
		dst.BackgroundX, dst.BackgroundY = src.BackgroundX, src.BackgroundY
	case "background-size":
		dst.BackgroundW, dst.BackgroundH = src.BackgroundW, src.BackgroundH
	case "direction":
		dst.Direction = src.Direction
	case "writing-mode", "-epub-writing-mode", "-webkit-writing-mode", "-ms-writing-mode":
		dst.Writing = src.Writing
	case "text-orientation", "-epub-text-orientation", "-webkit-text-orientation":
		dst.Orient = src.Orient
	case "border-collapse":
		dst.Collapse = src.Collapse
	case "border-spacing":
		dst.SpacingX, dst.SpacingY = src.SpacingX, src.SpacingY
	case "border-top-left-radius", "border-top-right-radius",
		"border-bottom-right-radius", "border-bottom-left-radius":
		dst.Radius[cornerAt(name)] = src.Radius[cornerAt(name)]
	case "clear":
		dst.Clear = src.Clear
	case "page-break-before", "break-before":
		dst.BreakBefore = src.BreakBefore
	case "page-break-after", "break-after":
		dst.BreakAfter = src.BreakAfter
	default:
		return false
	}
	return true
}

// The named border widths of CSS 2.1.
var borderWidths = map[string]float32{"thin": 1, "medium": 3, "thick": 5}

func setBorderWidth(b *Border, v value) {
	if w, ok := borderWidths[v.ident()]; ok {
		b.Width = w
		return
	}
	if l, ok := v.length(); ok && l.Unit == UnitPx {
		b.Width = max(l.Value, 0)
	}
}

func setBorderStyle(b *Border, v value) {
	switch v.ident() {
	case "none":
		b.Style = BorderNone
	case "hidden":
		b.Style = BorderHidden
	case "solid", "groove", "ridge", "inset", "outset":
		b.Style = BorderSolid
	case "dashed":
		b.Style = BorderDashed
	case "dotted":
		b.Style = BorderDotted
	case "double":
		b.Style = BorderDouble
	}
}

// setBorderColor reads a border colour, which by default is the colour of the
// text and is why the cascade computes that one first.
func setBorderColor(b *Border, s *Style, v value) {
	if v.ident() == "currentcolor" {
		b.Color = s.Color
		return
	}
	if c, ok := v.color(); ok {
		b.Color = c
	}
}

func setLength(dst *Length, v value) {
	if l, ok := v.lengthAuto(); ok {
		*dst = l
	}
}

func (v value) display() (Display, bool) {
	switch k := v.ident(); k {
	case "":
		return 0, false
	case "none":
		return DisplayNone, true
	case "inline":
		return DisplayInline, true
	case "inline-block", "inline-flex", "inline-grid":
		return DisplayInlineBlock, true
	case "list-item":
		return DisplayListItem, true
	case "table", "inline-table":
		return DisplayTable, true
	case "table-row-group", "table-header-group", "table-footer-group":
		return DisplayTableRowGroup, true
	case "table-row":
		return DisplayTableRow, true
	case "table-cell":
		return DisplayTableCell, true
	case "table-caption":
		return DisplayTableCaption, true
	case "table-column", "table-column-group":
		return DisplayNone, true
	}
	return DisplayBlock, true
}

func (v value) decoration() (Decoration, bool) {
	var d Decoration
	for _, part := range splitSpace(v.toks) {
		if len(part) != 1 || part[0].kind != cssIdent {
			continue
		}
		switch strings.ToLower(part[0].value) {
		case "none":
			return 0, true
		case "underline":
			d |= Underline
		case "overline":
			d |= Overline
		case "line-through":
			d |= LineThrough
		}
	}
	return d, true
}

func (v value) listStyle() (ListStyle, bool) {
	for _, part := range splitSpace(v.toks) {
		if len(part) != 1 || part[0].kind != cssIdent {
			continue
		}
		switch strings.ToLower(part[0].value) {
		case "none":
			return ListNone, true
		case "disc":
			return ListDisc, true
		case "circle":
			return ListCircle, true
		case "square":
			return ListSquare, true
		case "decimal", "decimal-leading-zero":
			return ListDecimal, true
		case "lower-alpha", "lower-latin":
			return ListLowerAlpha, true
		case "upper-alpha", "upper-latin":
			return ListUpperAlpha, true
		case "lower-roman":
			return ListLowerRoman, true
		case "upper-roman":
			return ListUpperRoman, true
		}
	}
	return 0, false
}

func (v value) pageBreak() (PageBreak, bool) {
	switch v.ident() {
	case "auto":
		return BreakAuto, true
	case "always", "left", "right", "page", "recto", "verso":
		return BreakAlways, true
	case "avoid", "avoid-page":
		return BreakAvoid, true
	}
	return 0, false
}
