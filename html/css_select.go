package html

import (
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// Specificity is CSS 2.1's (a, b, c) triple packed so that it compares as one
// number: identifiers, then classes and attributes, then element names.
type Specificity uint32

// maxSpecificity is what the style attribute carries, above any selector.
const maxSpecificity = Specificity(1<<30 - 1)

func specificity(a, b, c int) Specificity {
	return Specificity(min(a, 1023)<<20 | min(b, 1023)<<10 | min(c, 1023))
}

// Selector is one complex selector. Its compounds are held right to left,
// subject first: the order it matches in.
type Selector struct {
	parts []compound
	spec  Specificity
	elem  PseudoElement
}

// Spec returns the specificity of the selector.
func (s Selector) Spec() Specificity { return s.spec }

// compound is a run of simple selectors with no combinator between them, and
// comb is how it relates to the compound to its right in the source.
type compound struct {
	comb    byte
	tag     string
	id      string
	classes []string
	attrs   []attrSel
	pseudo  []pseudoSel
	// elem is the pseudo-element the compound names, which only the subject
	// of a selector may carry.
	elem PseudoElement
	// never marks a compound nothing matches: a pseudo-element layout has no
	// notion of, or a pseudo class about a state a book reader is never in.
	never bool
}

type attrSel struct {
	name string
	// ns is the namespace prefix written before the name, which an XHTML
	// document parsed as HTML keeps in the attribute name itself.
	ns string
	// op is 0 for a bare presence test, otherwise one of = ~ | ^ $ *.
	op   byte
	val  string
	fold bool
}

type pseudoSel struct {
	name string
	a, b int
	not  []compound
}

// The combinators, and the space that stands for a descendant.
const (
	combDescendant = ' '
	combChild      = '>'
	combNext       = '+'
	combLater      = '~'
)

// parseSelectors reads a comma separated selector list. It returns nothing
// when any one of them fails: a rule with a selector this cannot read is
// dropped whole.
func parseSelectors(toks []cssToken) ([]Selector, bool) {
	if len(toks) == 0 {
		return nil, false
	}
	var out []Selector
	for _, part := range splitCommas(toks) {
		s, ok := parseSelector(skipSpace(part))
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// parseSelector reads one complex selector.
func parseSelector(toks []cssToken) (Selector, bool) {
	var s Selector
	var a, b, c int
	comb := byte(0)
	for {
		for len(toks) > 0 && toks[0].kind == cssSpace {
			toks = toks[1:]
		}
		if len(toks) == 0 {
			return Selector{}, false
		}
		var cur compound
		var ok bool
		if cur, toks, ok = parseCompound(toks, &a, &b, &c); !ok {
			return Selector{}, false
		}
		cur.comb = comb
		s.parts = append(s.parts, cur)

		comb = 0
		space := false
		for len(toks) > 0 {
			t := toks[0]
			if t.kind == cssSpace {
				space, toks = true, toks[1:]
				continue
			}
			if t.kind == cssDelim && (t.delim == combChild || t.delim == combNext || t.delim == combLater) {
				comb, toks = t.delim, toks[1:]
				continue
			}
			break
		}
		switch {
		case comb != 0:
		case space && len(toks) > 0:
			comb = combDescendant
		default:
			if len(toks) > 0 {
				return Selector{}, false
			}
			reverse(s.parts)
			s.spec = specificity(a, b, c)
			s.elem = s.parts[0].elem
			for i := 1; i < len(s.parts); i++ {
				if s.parts[i].elem != PseudoNone {
					s.parts[0].never = true
				}
			}
			return s, true
		}
	}
}

func reverse(p []compound) {
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
}

// parseCompound reads one compound selector and counts it into the
// specificity.
func parseCompound(toks []cssToken, a, b, c *int) (compound, []cssToken, bool) {
	var cur compound
	first, n := true, 0
	for len(toks) > 0 {
		t := toks[0]
		switch {
		case t.kind == cssIdent || (t.kind == cssDelim && t.delim == '*'):
			if !first {
				return cur, toks, false
			}
			_, name, rest, ok := parseQualified(toks)
			if !ok {
				return cur, toks, false
			}
			if name != "" {
				cur.tag, toks = name, rest
				*c++
			} else {
				toks = rest
			}
		case t.kind == cssHash && t.id:
			cur.id, toks = t.value, toks[1:]
			*a++
		case t.kind == cssDelim && t.delim == '.':
			if len(toks) < 2 || toks[1].kind != cssIdent {
				return cur, toks, false
			}
			cur.classes, toks = append(cur.classes, toks[1].value), toks[2:]
			*b++
		case t.kind == cssOpenSquare:
			at, rest, ok := parseAttr(toks)
			if !ok {
				return cur, toks, false
			}
			cur.attrs, toks = append(cur.attrs, at), rest
			*b++
		case t.kind == cssColon:
			rest, ok := parsePseudo(toks, &cur, a, b, c)
			if !ok {
				return cur, toks, false
			}
			toks = rest
		default:
			return cur, toks, n > 0
		}
		first, n = false, n+1
	}
	return cur, toks, n > 0
}

// parseQualified reads a name and the namespace prefix a sheet with an
// @namespace writes before it, and returns empty for the universal selector.
func parseQualified(toks []cssToken) (string, string, []cssToken, bool) {
	name := ""
	if toks[0].kind == cssIdent {
		name = toks[0].value
	}
	toks = toks[1:]
	if len(toks) >= 2 && toks[0].kind == cssDelim && toks[0].delim == '|' {
		switch t := toks[1]; {
		case t.kind == cssIdent:
			return name, t.value, toks[2:], true
		case t.kind == cssDelim && t.delim == '*':
			return name, "", toks[2:], true
		}
	}
	return "", name, toks, true
}

func parseAttr(toks []cssToken) (attrSel, []cssToken, bool) {
	var at attrSel
	toks = skipSpace(toks[1:])
	if len(toks) == 0 {
		return at, toks, false
	}
	ns, name, toks, ok := parseQualified(toks)
	if !ok || name == "" {
		return at, toks, false
	}
	at.name, at.ns = strings.ToLower(name), strings.ToLower(ns)
	toks = skipSpace(toks)
	if len(toks) == 0 {
		return at, toks, false
	}
	if toks[0].kind == cssDelim && toks[0].delim != '=' {
		switch toks[0].delim {
		case '~', '|', '^', '$', '*':
			at.op = toks[0].delim
			toks = toks[1:]
		default:
			return at, toks, false
		}
	}
	if len(toks) > 0 && toks[0].kind == cssDelim && toks[0].delim == '=' {
		if at.op == 0 {
			at.op = '='
		}
		toks = skipSpace(toks[1:])
		if len(toks) == 0 {
			return at, toks, false
		}
		switch toks[0].kind {
		case cssIdent, cssString:
			at.val, toks = toks[0].value, skipSpace(toks[1:])
		default:
			return at, toks, false
		}
	} else if at.op != 0 {
		return at, toks, false
	}
	if len(toks) > 0 && toks[0].kind == cssIdent && strings.EqualFold(toks[0].value, "i") {
		at.fold, toks = true, skipSpace(toks[1:])
	}
	if len(toks) == 0 || toks[0].kind != cssCloseSquare {
		return at, toks, false
	}
	return at, toks[1:], true
}

// parsePseudo reads a pseudo class or a pseudo element. A pseudo element and
// every state a book reader has no notion of make the compound unmatchable.
func parsePseudo(toks []cssToken, cur *compound, a, b, c *int) ([]cssToken, bool) {
	toks = toks[1:]
	double := false
	if len(toks) > 0 && toks[0].kind == cssColon {
		double, toks = true, toks[1:]
	}
	if len(toks) == 0 {
		return toks, false
	}
	if double {
		if toks[0].kind != cssIdent {
			return toks, false
		}
		switch strings.ToLower(toks[0].value) {
		case "before":
			cur.elem = PseudoBefore
		case "after":
			cur.elem = PseudoAfter
		default:
			cur.never = true
		}
		*c++
		return toks[1:], true
	}
	switch t := toks[0]; t.kind {
	case cssIdent:
		name := strings.ToLower(t.value)
		switch name {
		case "before", "after":
			cur.elem = PseudoBefore
			if name == "after" {
				cur.elem = PseudoAfter
			}
			*c++
		case "first-line", "first-letter":
			cur.never = true
			*c++
		case "root", "empty", "first-child", "last-child", "only-child",
			"first-of-type", "last-of-type", "only-of-type":
			cur.pseudo = append(cur.pseudo, pseudoSel{name: name})
			*b++
		default:
			cur.never = true
			*b++
		}
		return toks[1:], true
	case cssFunction:
		name := strings.ToLower(t.value)
		arg, rest, ok := parseArgs(toks)
		if !ok {
			return toks, false
		}
		switch name {
		case "not":
			var not []compound
			for _, part := range splitCommas(arg) {
				one, left, ok := parseCompound(skipSpace(part), a, b, c)
				if !ok || len(skipSpace(left)) > 0 {
					return toks, false
				}
				not = append(not, one)
			}
			if len(not) == 0 {
				return toks, false
			}
			cur.pseudo = append(cur.pseudo, pseudoSel{name: name, not: not})
		case "nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type":
			n, m, ok := parseNth(arg)
			if !ok {
				return toks, false
			}
			cur.pseudo = append(cur.pseudo, pseudoSel{name: name, a: n, b: m})
			*b++
		default:
			cur.never = true
			*b++
		}
		return rest, true
	}
	return toks, false
}

// parseArgs returns what a function token encloses and what follows it.
func parseArgs(toks []cssToken) ([]cssToken, []cssToken, bool) {
	depth := 1
	for i := 1; i < len(toks); i++ {
		switch toks[i].kind {
		case cssOpenParen, cssFunction:
			depth++
		case cssCloseParen:
			if depth--; depth == 0 {
				return toks[1:i], toks[i+1:], true
			}
		}
	}
	return nil, toks, false
}

func splitCommas(toks []cssToken) [][]cssToken {
	var out [][]cssToken
	start, depth := 0, 0
	for i, t := range toks {
		switch t.kind {
		case cssOpenParen, cssOpenSquare, cssFunction:
			depth++
		case cssCloseParen, cssCloseSquare:
			depth--
		case cssComma:
			if depth == 0 {
				out = append(out, toks[start:i])
				start = i + 1
			}
		}
	}
	return append(out, toks[start:])
}

// parseNth reads the an+b of an nth-child argument back out of the tokens it
// was split into, which depends on where the signs fell.
func parseNth(toks []cssToken) (int, int, bool) {
	var b strings.Builder
	for _, t := range skipSpace(toks) {
		switch t.kind {
		case cssSpace:
		case cssIdent:
			b.WriteString(t.value)
		case cssDelim:
			b.WriteByte(t.delim)
		case cssNumber, cssDimension:
			if !t.integer {
				return 0, 0, false
			}
			if t.signed && t.num >= 0 {
				b.WriteByte('+')
			}
			b.WriteString(strconv.FormatInt(int64(t.num), 10))
			b.WriteString(t.unit)
		default:
			return 0, 0, false
		}
	}
	s := strings.ToLower(strings.TrimSpace(b.String()))
	switch s {
	case "odd":
		return 2, 1, true
	case "even":
		return 2, 0, true
	}
	i := strings.IndexByte(s, 'n')
	if i < 0 {
		v, err := strconv.Atoi(s)
		return 0, v, err == nil
	}
	a := 1
	switch head := s[:i]; head {
	case "", "+":
	case "-":
		a = -1
	default:
		v, err := strconv.Atoi(head)
		if err != nil {
			return 0, 0, false
		}
		a = v
	}
	tail := s[i+1:]
	if tail == "" {
		return a, 0, true
	}
	if tail[0] != '+' && tail[0] != '-' {
		return 0, 0, false
	}
	v, err := strconv.Atoi(tail)
	return a, v, err == nil
}

func skipSpace(toks []cssToken) []cssToken {
	for len(toks) > 0 && toks[0].kind == cssSpace {
		toks = toks[1:]
	}
	for len(toks) > 0 && toks[len(toks)-1].kind == cssSpace {
		toks = toks[:len(toks)-1]
	}
	return toks
}

// Match reports whether an element matches the selector. A selector that
// names a pseudo-element matches no element: what it styles is a box the
// element generates, which Pseudo names.
func (s Selector) Match(n *Node) bool {
	return s.elem == PseudoNone && matchParts(s.parts, n)
}

// Pseudo is the pseudo-element the selector styles, if it names one.
func (s Selector) Pseudo() PseudoElement { return s.elem }

func matchParts(parts []compound, n *Node) bool {
	if !matchCompound(&parts[0], n) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	rest := parts[1:]
	switch parts[0].comb {
	case combChild:
		p := parentElement(n)
		return p != nil && matchParts(rest, p)
	case combDescendant:
		for p := parentElement(n); p != nil; p = parentElement(p) {
			if matchParts(rest, p) {
				return true
			}
		}
	case combNext:
		p := prevElement(n)
		return p != nil && matchParts(rest, p)
	case combLater:
		for p := prevElement(n); p != nil; p = prevElement(p) {
			if matchParts(rest, p) {
				return true
			}
		}
	}
	return false
}

func matchCompound(c *compound, n *Node) bool {
	if c.never || n.Type != xhtml.ElementNode {
		return false
	}
	if c.tag != "" && !strings.EqualFold(c.tag, n.Data) {
		return false
	}
	if c.id != "" && Attr(n, "id") != c.id {
		return false
	}
	for _, cl := range c.classes {
		if !hasClass(n, cl) {
			return false
		}
	}
	for i := range c.attrs {
		if !matchAttr(&c.attrs[i], n) {
			return false
		}
	}
	for i := range c.pseudo {
		if !matchPseudo(&c.pseudo[i], n) {
			return false
		}
	}
	return true
}

func hasClass(n *Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for f := range strings.FieldsSeq(a.Val) {
			if f == want {
				return true
			}
		}
	}
	return false
}

func matchAttr(a *attrSel, n *Node) bool {
	v, ok := "", false
	for _, at := range n.Attr {
		if at.Key == a.name || a.ns != "" && at.Key == a.ns+":"+a.name {
			v, ok = at.Val, true
			break
		}
	}
	if !ok {
		return false
	}
	want := a.val
	if a.fold {
		v, want = strings.ToLower(v), strings.ToLower(want)
	}
	switch a.op {
	case 0:
		return true
	case '=':
		return v == want
	case '~':
		if want == "" {
			return false
		}
		for f := range strings.FieldsSeq(v) {
			if f == want {
				return true
			}
		}
		return false
	case '|':
		return v == want || strings.HasPrefix(v, want+"-")
	case '^':
		return want != "" && strings.HasPrefix(v, want)
	case '$':
		return want != "" && strings.HasSuffix(v, want)
	case '*':
		return want != "" && strings.Contains(v, want)
	}
	return false
}

func matchPseudo(p *pseudoSel, n *Node) bool {
	switch p.name {
	case "not":
		for i := range p.not {
			if matchCompound(&p.not[i], n) {
				return false
			}
		}
		return true
	case "root":
		return parentElement(n) == nil
	case "empty":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.ElementNode {
				return false
			}
			if c.Type == xhtml.TextNode && c.Data != "" {
				return false
			}
		}
		return true
	case "first-child":
		return prevElement(n) == nil
	case "last-child":
		return nextElement(n) == nil
	case "only-child":
		return prevElement(n) == nil && nextElement(n) == nil
	case "first-of-type":
		return countOfType(n, false) == 1
	case "last-of-type":
		return countOfType(n, true) == 1
	case "only-of-type":
		return countOfType(n, false) == 1 && countOfType(n, true) == 1
	case "nth-child":
		return nthMatch(p.a, p.b, index(n, false, false))
	case "nth-last-child":
		return nthMatch(p.a, p.b, index(n, true, false))
	case "nth-of-type":
		return nthMatch(p.a, p.b, index(n, false, true))
	case "nth-last-of-type":
		return nthMatch(p.a, p.b, index(n, true, true))
	}
	return false
}

// index is what an nth-child argument counts against: the element's position
// among its siblings, from one, from either end and either of every element
// or of those with its own name.
func index(n *Node, last, typed bool) int {
	i := 1
	step := prevElement
	if last {
		step = nextElement
	}
	for s := step(n); s != nil; s = step(s) {
		if !typed || strings.EqualFold(s.Data, n.Data) {
			i++
		}
	}
	return i
}

func countOfType(n *Node, last bool) int { return index(n, last, true) }

func nthMatch(a, b, i int) bool {
	if a == 0 {
		return i == b
	}
	d := i - b
	return d%a == 0 && d/a >= 0
}

func parentElement(n *Node) *Node {
	p := n.Parent
	if p == nil || p.Type != xhtml.ElementNode {
		return nil
	}
	return p
}

func prevElement(n *Node) *Node {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == xhtml.ElementNode {
			return s
		}
	}
	return nil
}

func nextElement(n *Node) *Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == xhtml.ElementNode {
			return s
		}
	}
	return nil
}
