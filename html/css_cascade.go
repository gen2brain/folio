package html

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	xhtml "golang.org/x/net/html"
)

// PseudoElement names a ::before or ::after box.
type PseudoElement uint8

// The pseudo-elements a rule may style.
const (
	PseudoNone PseudoElement = iota
	PseudoBefore
	PseudoAfter
)

// Styles is the computed style of every element of a tree and of its ::before and ::after boxes.
type Styles struct {
	of     map[*Node]*Style
	before map[*Node]*Style
	after  map[*Node]*Style
}

// Of is the style of an element, or of the nearest element above a text node.
func (s Styles) Of(n *Node) *Style {
	for ; n != nil; n = n.Parent {
		if st, ok := s.of[n]; ok {
			return st
		}
	}
	st := initialStyle()
	return &st
}

// Has reports whether an element has a style of its own.
func (s Styles) Has(n *Node) bool {
	_, ok := s.of[n]
	return ok
}

// Pseudo returns the style of an element's ::before or ::after box, or nil when it has none.
func (s Styles) Pseudo(n *Node, p PseudoElement) *Style {
	switch p {
	case PseudoBefore:
		return s.before[n]
	case PseudoAfter:
		return s.after[n]
	}
	return nil
}

func (s *Styles) set(n *Node, p PseudoElement, st *Style) {
	switch p {
	case PseudoBefore:
		if s.before == nil {
			s.before = map[*Node]*Style{}
		}
		s.before[n] = st
	case PseudoAfter:
		if s.after == nil {
			s.after = map[*Node]*Style{}
		}
		s.after[n] = st
	default:
		s.of[n] = st
	}
}

// Cascade computes the style of every element of a tree.
// The sheets are given in reading order, the user agent sheet first.
func Cascade(root *Node, media Media, sheets ...*Stylesheet) Styles {
	c := cascader{media: media, out: Styles{of: map[*Node]*Style{}},
		medium: orDefault(media.FontSize, DefaultFontSize)}
	c.rem = c.medium
	for _, s := range sheets {
		if s == nil {
			continue
		}
		for i := range s.Rules {
			if matchMedia(s.Rules[i].Media, media) {
				c.rules = append(c.rules, ruleRef{origin: s.Origin, rule: &s.Rules[i], order: len(c.rules)})
			}
		}
	}
	c.keyed = map[string][]selRef{}
	for ri, r := range c.rules {
		for si := range r.rule.Selectors {
			if sub := &r.rule.Selectors[si].parts[0]; !sub.never {
				k := subjectKey(sub)
				c.keyed[k] = append(c.keyed[k], selRef{int32(ri), int32(si)})
			}
		}
	}
	init := initialStyle()
	init.FontSize = c.medium
	c.walk(root, &init)
	return c.out
}

type ruleRef struct {
	origin Origin
	rule   *Rule
	order  int
}

type cascader struct {
	rules []ruleRef
	// keyed holds rule selectors by the id, class or tag their subject needs; cands those found for one element.
	keyed map[string][]selRef
	cands []selRef
	ctx   matchCtx
	media Media
	out   Styles
	// medium is the base of the font size keywords; rem the computed size of the root element.
	medium, rem float32
}

// selRef is one selector of one rule.
type selRef struct{ rule, sel int32 }

// subjectKey is the id, class or tag a compound requires of its subject.
func subjectKey(c *compound) string {
	switch {
	case c.id != "":
		return "#" + c.id
	case len(c.classes) > 0:
		return "." + c.classes[0]
	case c.tag != "":
		return foldKey(c.tag)
	}
	return ""
}

// foldKey maps each rune to the least of its case-folding orbit, so two names EqualFold matches map alike.
func foldKey(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		ascii = ascii && s[i] < 0x80
	}
	if ascii {
		return strings.ToUpper(s)
	}
	return strings.Map(func(r rune) rune {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
		return m
	}, s)
}

// candidates are the selectors whose subject n may match, in cascade order.
func (c *cascader) candidates(n *Node) []selRef {
	out := append(c.cands[:0], c.keyed[""]...)
	if id := Attr(n, "id"); id != "" {
		out = append(out, c.keyed["#"+id]...)
	}
	for _, a := range n.Attr {
		if a.Key == "class" {
			for f := range strings.FieldsSeq(a.Val) {
				out = append(out, c.keyed["."+f]...)
			}
		}
	}
	if n.Data != "" {
		out = append(out, c.keyed[foldKey(n.Data)]...)
	}
	slices.SortFunc(out, func(a, b selRef) int {
		if a.rule != b.rule {
			return cmp.Compare(a.rule, b.rule)
		}
		return cmp.Compare(a.sel, b.sel)
	})
	c.cands = slices.Compact(out)
	return c.cands
}

func (c *cascader) walk(n *Node, parent *Style) {
	if n.Type == xhtml.ElementNode {
		c.candidates(n)
		s := c.compute(n, parent, PseudoNone)
		c.out.set(n, PseudoNone, s)
		for _, p := range [...]PseudoElement{PseudoBefore, PseudoAfter} {
			if g := c.compute(n, s, p); g != nil {
				c.out.set(n, p, g)
			}
		}
		if parentElement(n) == nil {
			c.rem = s.FontSize
		}
		parent = s
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.walk(ch, parent)
	}
}

// cand is the declaration winning a property so far, and its weight.
type cand struct {
	toks   []cssToken
	weight uint64
}

// weightOf packs origin and importance into three bits, specificity into thirty and source order into the low thirty one.
func weightOf(o Origin, important bool, spec Specificity, order int) uint64 {
	rank := uint64(0)
	switch {
	case important && o == OriginUA:
		rank = 5
	case important && o == OriginUser:
		rank = 4
	case important:
		rank = 3
	case o == OriginAuthor:
		rank = 2
	case o == OriginUser:
		rank = 1
	}
	return rank<<61 | uint64(spec&maxSpecificity)<<31 | uint64(min(order, 1<<31-1))
}

// compute is the cascade over one element, or over its ::before or ::after box; nil when that box has no rule.
func (c *cascader) compute(n *Node, parent *Style, p PseudoElement) *Style {
	win := make(map[string]cand)
	refs := c.cands
	for i := 0; i < len(refs); {
		r := c.rules[refs[i].rule]
		spec, ok := Specificity(0), false
		for ri := refs[i].rule; i < len(refs) && refs[i].rule == ri; i++ {
			sel := &r.rule.Selectors[refs[i].sel]
			if sel.elem == p && sel.spec >= spec {
				if len(c.ctx.fail) > 0 {
					c.ctx.fail = nil
				}
				if matchParts(sel.parts, n, &c.ctx) {
					spec, ok = sel.spec, true
				}
			}
		}
		if !ok {
			continue
		}
		for i := range r.rule.Decls {
			d := &r.rule.Decls[i]
			add(win, d, weightOf(r.origin, d.Important, spec, r.order))
		}
	}
	if p != PseudoNone {
		if len(win) == 0 {
			return nil
		}
	} else if attr := Attr(n, "style"); attr != "" {
		for _, d := range parseInline(attr) {
			add(win, &d, weightOf(OriginAuthor, d.Important, maxSpecificity, 1<<31-1))
		}
	}

	s := parent.inherit()
	if p == PseudoNone {
		switch strings.ToLower(Attr(n, "dir")) {
		case "ltr":
			s.Direction = DirLTR
		case "rtl":
			s.Direction = DirRTL
		}
	}
	v := value{em: parent.FontSize, rm: c.rem, medium: c.medium, parent: parent}
	if w, ok := win["font-size"]; ok {
		v.toks = w.toks
		applyProp(&s, "font-size", v)
	}
	v.em = s.FontSize
	if w, ok := win["color"]; ok {
		v.toks = w.toks
		applyProp(&s, "color", v)
	}
	for name, w := range win {
		if name == "font-size" || name == "color" {
			continue
		}
		v.toks = w.toks
		applyProp(&s, name, v)
	}
	for _, e := range [...]struct {
		name string
		b    *Border
	}{
		{"border-top-color", &s.BorderTop}, {"border-right-color", &s.BorderRight},
		{"border-bottom-color", &s.BorderBottom}, {"border-left-color", &s.BorderLeft},
	} {
		if _, ok := win[e.name]; !ok {
			e.b.Color = s.Color
		}
	}
	return &s
}

func add(win map[string]cand, d *Declaration, w uint64) {
	for _, part := range expand(d) {
		if old, ok := win[part.name]; ok && old.weight > w {
			continue
		}
		win[part.name] = cand{toks: part.toks, weight: w}
	}
}

// parseInline reads the declarations of a style attribute.
func parseInline(s string) []Declaration {
	p := &cssParser{l: newCSSLexer(s), sheet: &Stylesheet{}}
	return p.declarations()
}

// longhand is one property a declaration sets after shorthand expansion.
type longhand struct {
	name string
	toks []cssToken
}

// boxSides are the side suffixes in the order values fill them.
var boxSides = [4]string{"-top", "-right", "-bottom", "-left"}

// expand splits a shorthand into the longhands it sets.
func expand(d *Declaration) []longhand {
	switch d.Name {
	case "margin", "padding":
		parts := splitSpace(d.value)
		if len(parts) == 0 || len(parts) > 4 {
			return nil
		}
		out := make([]longhand, 4)
		for i := range out {
			out[i] = longhand{name: d.Name + boxSides[i], toks: parts[boxIndex(i, len(parts))]}
		}
		return out
	case "background":
		return expandBackground(d.value)
	case "border-radius":
		return expandRadius(d.value)
	case "border":
		w, st, c := borderParts(d.value)
		out := make([]longhand, 0, 12)
		for _, side := range boxSides {
			out = append(out,
				longhand{name: "border" + side + "-width", toks: w},
				longhand{name: "border" + side + "-style", toks: st},
				longhand{name: "border" + side + "-color", toks: c})
		}
		return out
	case "border-top", "border-right", "border-bottom", "border-left":
		w, st, c := borderParts(d.value)
		side := strings.TrimPrefix(d.Name, "border")
		return []longhand{
			{name: "border" + side + "-width", toks: w},
			{name: "border" + side + "-style", toks: st},
			{name: "border" + side + "-color", toks: c},
		}
	case "border-width", "border-style", "border-color":
		parts := splitSpace(d.value)
		if len(parts) == 0 || len(parts) > 4 {
			return nil
		}
		what := strings.TrimPrefix(d.Name, "border")
		out := make([]longhand, 4)
		for i := range out {
			out[i] = longhand{name: "border" + boxSides[i] + what, toks: parts[boxIndex(i, len(parts))]}
		}
		return out
	case "list-style":
		return []longhand{{name: "list-style-type", toks: d.value}}
	case "font":
		return expandFont(d.value)
	case "text-decoration":
		return []longhand{{name: "text-decoration-line", toks: d.value}}
	}
	return []longhand{{name: d.Name, toks: d.value}}
}

// borderParts splits a border shorthand into width, style and colour, defaulting the missing ones.
func borderParts(toks []cssToken) (w, st, c []cssToken) {
	w = []cssToken{{kind: cssIdent, value: "medium"}}
	st = []cssToken{{kind: cssIdent, value: "none"}}
	c = []cssToken{{kind: cssIdent, value: "currentcolor"}}
	for _, p := range splitSpace(toks) {
		switch {
		case isBorderStyle(p):
			st = p
		case isBorderWidth(p):
			w = p
		default:
			c = p
		}
	}
	return w, st, c
}

func isBorderStyle(p []cssToken) bool {
	if len(p) != 1 || p[0].kind != cssIdent {
		return false
	}
	switch strings.ToLower(p[0].value) {
	case "none", "hidden", "solid", "dashed", "dotted", "double",
		"groove", "ridge", "inset", "outset":
		return true
	}
	return false
}

func isBorderWidth(p []cssToken) bool {
	if len(p) != 1 {
		return false
	}
	switch t := p[0]; t.kind {
	case cssDimension:
		return true
	case cssNumber:
		return t.num == 0
	case cssIdent:
		_, ok := borderWidths[strings.ToLower(t.value)]
		return ok
	}
	return false
}

// expandBackground splits the background shorthand into colour, image and position, from the first layer.
func expandBackground(toks []cssToken) []longhand {
	out := []longhand{{name: "background-color", toks: backgroundColor(toks)}}
	var pos []cssToken
	for _, part := range splitSpace(splitCommas(toks)[0]) {
		if len(part) == 0 {
			continue
		}
		if part[0].kind == cssURL ||
			part[0].kind == cssFunction && strings.EqualFold(part[0].value, "url") {
			out = append(out, longhand{name: "background-image", toks: part})
			continue
		}
		if part[0].kind == cssIdent {
			switch strings.ToLower(part[0].value) {
			case "repeat", "repeat-x", "repeat-y", "no-repeat":
				out = append(out, longhand{name: "background-repeat", toks: part})
				continue
			case "scroll", "fixed", "local", "none", "transparent":
				continue
			}
		}
		if _, ok := (value{toks: part}).color(); ok {
			continue
		}
		if len(pos) > 0 {
			pos = append(pos, cssToken{kind: cssSpace})
		}
		pos = append(pos, part...)
	}
	if len(pos) > 0 {
		out = append(out, longhand{name: "background-position", toks: pos})
	}
	return out
}

// backgroundColor picks the colour out of a background shorthand.
func backgroundColor(toks []cssToken) []cssToken {
	parts := splitSpace(toks)
	for i := len(parts) - 1; i >= 0; i-- {
		if v := (value{toks: parts[i]}); len(parts[i]) > 0 {
			if _, ok := v.color(); ok {
				return parts[i]
			}
		}
	}
	return toks
}

// boxIndex is the value of one, two, three or four that fills a side.
func boxIndex(side, n int) int {
	switch n {
	case 1:
		return 0
	case 2:
		return side & 1
	case 3:
		if side == 3 {
			return 1
		}
		return side
	}
	return side
}

// expandRadius splits border-radius into horizontal radii before the slash and vertical after.
func expandRadius(toks []cssToken) []longhand {
	across, down := toks, []cssToken(nil)
	for i, t := range toks {
		if t.kind == cssDelim && t.delim == '/' {
			across, down = toks[:i], toks[i+1:]
			break
		}
	}
	h := splitSpace(across)
	if len(h) == 0 || len(h) > 4 {
		return nil
	}
	v := splitSpace(down)
	if len(v) > 4 {
		return nil
	}
	out := make([]longhand, 4)
	for i := range out {
		part := h[boxIndex(i, len(h))]
		if len(v) > 0 {
			part = append(append([]cssToken{}, part...), cssToken{kind: cssSpace})
			part = append(part, v[boxIndex(i, len(v))]...)
		}
		out[i] = longhand{name: cornerNames[i], toks: part}
	}
	return out
}

// expandFont splits the font shorthand: style, variant, weight, size[/line-height], family.
func expandFont(toks []cssToken) []longhand {
	parts := splitSpace(toks)
	size := -1
	for i, p := range parts {
		if isFontSize(p) {
			size = i
			break
		}
	}
	if size < 0 {
		return nil
	}
	var out []longhand
	for _, p := range parts[:size] {
		if len(p) != 1 {
			continue
		}
		switch t := p[0]; t.kind {
		case cssIdent:
			switch strings.ToLower(t.value) {
			case "italic", "oblique":
				out = append(out, longhand{name: "font-style", toks: p})
			case "bold", "bolder", "lighter":
				out = append(out, longhand{name: "font-weight", toks: p})
			}
		case cssNumber:
			out = append(out, longhand{name: "font-weight", toks: p})
		}
	}
	head, tail, slash := splitSlash(parts[size])
	next := size + 1
	switch {
	case slash && len(tail) == 0 && next < len(parts):
		tail, next = parts[next], next+1
	case !slash && next < len(parts) && isSlash(parts[next][0]):
		if tail = parts[next][1:]; len(tail) == 0 && next+1 < len(parts) {
			tail, next = parts[next+1], next+2
		} else {
			next++
		}
	}
	if next >= len(parts) {
		return nil
	}
	out = append(out, longhand{name: "font-size", toks: head})
	if len(tail) > 0 {
		out = append(out, longhand{name: "line-height", toks: tail})
	}
	return append(out, longhand{name: "font-family", toks: toks[tokenIndex(toks, parts[next]):]})
}

func isFontSize(p []cssToken) bool {
	head, _, _ := splitSlash(p)
	if len(head) != 1 {
		return false
	}
	switch t := head[0]; t.kind {
	case cssDimension, cssPercentage:
		return true
	case cssIdent:
		k := strings.ToLower(t.value)
		if k == "smaller" || k == "larger" {
			return true
		}
		_, ok := fontSizeKeywords[k]
		return ok
	}
	return false
}

// splitSlash cuts a font shorthand's size from its line height, reporting a slash.
func splitSlash(p []cssToken) ([]cssToken, []cssToken, bool) {
	for i, t := range p {
		if isSlash(t) {
			return p[:i], p[i+1:], true
		}
	}
	return p, nil, false
}

func isSlash(t cssToken) bool { return t.kind == cssDelim && t.delim == '/' }

// splitSpace cuts a value at top-level white space.
func splitSpace(toks []cssToken) [][]cssToken {
	var out [][]cssToken
	start, depth := 0, 0
	for i, t := range toks {
		switch t.kind {
		case cssOpenParen, cssOpenSquare, cssFunction:
			depth++
		case cssCloseParen, cssCloseSquare:
			depth = max(0, depth-1)
		case cssSpace:
			if depth == 0 {
				if i > start {
					out = append(out, toks[start:i])
				}
				start = i + 1
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// tokenIndex is the offset of part within all.
func tokenIndex(all, part []cssToken) int {
	if len(part) == 0 {
		return len(all)
	}
	for i := range all {
		if &all[i] == &part[0] {
			return i
		}
	}
	return len(all)
}
