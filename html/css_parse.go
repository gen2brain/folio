package html

import (
	"fmt"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Origin is where a stylesheet came from, and the first thing the cascade
// sorts on.
type Origin uint8

// The origins, in the order they lose to one another.
const (
	OriginUA Origin = iota
	OriginUser
	OriginAuthor
)

// Stylesheet is a parsed sheet.
type Stylesheet struct {
	Rules   []Rule
	Imports []Import
	Origin  Origin
	// Errors are what the sheet dropped: a selector that did not parse, a
	// declaration with no colon.
	Errors []error
}

// Import is what an @import rule asks for.
type Import struct {
	// Path is the reference as written, which Resolve turns into a part.
	Path  string
	Media MediaList
}

// Rule is a selector list and the declarations it applies.
type Rule struct {
	Selectors []Selector
	Decls     []Declaration
	// Media are the conditions of the @media rules the rule sits in, all of
	// which must match.
	Media []MediaList
	// order is the position of the rule in the sheet, which breaks a tie the
	// specificity did not.
	order int
}

// Declaration is one property and its value.
type Declaration struct {
	Name      string
	Important bool
	value     []cssToken
}

// MediaList is the comma separated list of a media rule, which matches when
// any one of its queries does. An empty list matches everything.
type MediaList []MediaQuery

// MediaQuery is one query of a media list.
type MediaQuery struct {
	not  bool
	kind string
	// bad marks a query that did not parse, which matches nothing.
	bad   bool
	feats []mediaFeature
}

type mediaFeature struct {
	name  string
	value []cssToken
}

// Media is what a media query is asked about: the viewport, in CSS pixels,
// and the font size a relative length in a query resolves against.
type Media struct {
	Width, Height float32
	FontSize      float32
}

// matchMedia reports whether every condition a rule sits under matches.
func matchMedia(ms []MediaList, m Media) bool {
	for _, l := range ms {
		if !l.Match(m) {
			return false
		}
	}
	return true
}

// Match reports whether any query of the list matches, and true for a list
// with no query in it.
func (l MediaList) Match(m Media) bool {
	if len(l) == 0 {
		return true
	}
	for _, q := range l {
		if q.match(m) {
			return true
		}
	}
	return false
}

func (q MediaQuery) match(m Media) bool {
	if q.bad {
		return false
	}
	r := true
	switch q.kind {
	case "", "all", "screen":
	default:
		r = false
	}
	for _, f := range q.feats {
		if !f.match(m) {
			r = false
			break
		}
	}
	if q.not {
		return !r
	}
	return r
}

func (f mediaFeature) match(m Media) bool {
	name, _ := strings.CutPrefix(f.name, "min-")
	if name == f.name {
		name, _ = strings.CutPrefix(f.name, "max-")
	}
	var have float32
	switch name {
	case "width", "device-width":
		have = m.Width
	case "height", "device-height":
		have = m.Height
	case "orientation":
		want := ""
		if len(f.value) == 1 && f.value[0].kind == cssIdent {
			want = strings.ToLower(f.value[0].value)
		}
		if m.Height >= m.Width {
			return want == "portrait"
		}
		return want == "landscape"
	default:
		return false
	}
	if len(f.value) == 0 {
		return have > 0
	}
	em := orDefault(m.FontSize, DefaultFontSize)
	l, ok := length(f.value[0], em, em)
	if !ok || len(f.value) != 1 {
		return false
	}
	want := l.Resolve(have)
	switch {
	case strings.HasPrefix(f.name, "min-"):
		return have >= want
	case strings.HasPrefix(f.name, "max-"):
		return have <= want
	}
	return have == want
}

func orDefault(v, alt float32) float32 {
	if v > 0 {
		return v
	}
	return alt
}

// ParseCSS reads a stylesheet. It never fails: what it cannot read it records
// in Errors and drops.
func ParseCSS(b []byte, origin Origin) *Stylesheet {
	p := &cssParser{l: newCSSLexer(string(b)), sheet: &Stylesheet{Origin: origin}}
	p.rules(nil, false)
	return p.sheet
}

type cssParser struct {
	l     *cssLexer
	saved cssToken
	has   bool
	sheet *Stylesheet
	// depth bounds the nesting of at-rules, which a hostile sheet writes
	// without end.
	depth int
}

const maxCSSNesting = 16

func (p *cssParser) next() cssToken {
	if p.has {
		p.has = false
		return p.saved
	}
	return p.l.next()
}

func (p *cssParser) back(t cssToken) { p.saved, p.has = t, true }

func (p *cssParser) fail(format string, a ...any) {
	if len(p.sheet.Errors) < 64 {
		p.sheet.Errors = append(p.sheet.Errors, fmt.Errorf(format, a...))
	}
}

// rules reads a list of rules, stopping at the end of the input or, when
// nested, at the closing brace of the block it is in.
func (p *cssParser) rules(media []MediaList, nested bool) {
	for {
		switch t := p.next(); t.kind {
		case cssEOF:
			return
		case cssSpace, cssCDO, cssCDC:
		case cssCloseCurly:
			if nested {
				return
			}
		case cssAtKeyword:
			p.atRule(strings.ToLower(t.value), media)
		default:
			p.back(t)
			p.qualified(media)
		}
	}
}

// qualified reads a selector list and the declarations it applies.
func (p *cssParser) qualified(media []MediaList) {
	prelude, brace := p.prelude()
	if !brace {
		p.fail("css: rule with no block")
		return
	}
	sels, ok := parseSelectors(skipSpace(prelude))
	if !ok {
		p.skipBlock()
		p.fail("css: cannot read selector %q", tokensText(prelude))
		return
	}
	decls := p.declarations()
	if len(decls) == 0 {
		return
	}
	p.sheet.Rules = append(p.sheet.Rules, Rule{
		Selectors: sels,
		Decls:     decls,
		Media:     media,
		order:     len(p.sheet.Rules),
	})
}

func (p *cssParser) atRule(name string, media []MediaList) {
	prelude, brace := p.prelude()
	switch name {
	case "media":
		if !brace {
			return
		}
		if p.depth >= maxCSSNesting {
			p.skipBlock()
			return
		}
		inner := media
		if q := parseMediaList(skipSpace(prelude)); len(q) > 0 {
			inner = append(append([]MediaList(nil), media...), q)
		}
		p.depth++
		p.rules(inner, true)
		p.depth--
	case "import":
		if brace {
			p.skipBlock()
			return
		}
		toks := skipSpace(prelude)
		if len(toks) == 0 {
			return
		}
		var path string
		var rest []cssToken
		switch t := toks[0]; t.kind {
		case cssURL, cssString:
			path, rest = t.value, toks[1:]
		case cssFunction:
			args, after, ok := parseArgs(toks)
			args = skipSpace(args)
			if ok && strings.EqualFold(t.value, "url") && len(args) == 1 && args[0].kind == cssString {
				path, rest = args[0].value, after
			}
		}
		if path == "" {
			p.fail("css: cannot read @import %q", tokensText(prelude))
			return
		}
		p.sheet.Imports = append(p.sheet.Imports, Import{
			Path:  path,
			Media: parseMediaList(skipSpace(rest)),
		})
	default:
		if brace {
			p.skipBlock()
		}
	}
}

// prelude reads what comes before a block, and reports whether a block
// followed rather than a semicolon or the end of the input.
func (p *cssParser) prelude() ([]cssToken, bool) {
	var out []cssToken
	depth := 0
	for {
		t := p.next()
		switch t.kind {
		case cssEOF:
			return out, false
		case cssSemicolon:
			if depth == 0 {
				return out, false
			}
		case cssOpenCurly:
			if depth == 0 {
				return out, true
			}
			depth++
		case cssOpenParen, cssOpenSquare, cssFunction:
			depth++
		case cssCloseParen, cssCloseSquare, cssCloseCurly:
			depth = max(0, depth-1)
		}
		out = append(out, t)
	}
}

// declarations reads a declaration block, which the cursor is inside of.
func (p *cssParser) declarations() []Declaration {
	var out []Declaration
	for {
		switch t := p.next(); t.kind {
		case cssEOF, cssCloseCurly:
			return out
		case cssSpace, cssSemicolon:
		case cssAtKeyword:
			if _, brace := p.prelude(); brace {
				p.skipBlock()
			}
		case cssIdent:
			if d, ok := declaration(t.value, p.values()); ok {
				out = append(out, d)
			} else {
				p.fail("css: cannot read declaration %q", t.value)
			}
		default:
			p.back(t)
			p.values()
			p.fail("css: declaration does not start with a property")
		}
	}
}

// values reads what a declaration is worth, up to the semicolon or the brace
// that ends it, leaving the brace for the caller.
func (p *cssParser) values() []cssToken {
	var out []cssToken
	depth := 0
	for {
		t := p.next()
		switch t.kind {
		case cssEOF:
			return out
		case cssSemicolon:
			if depth == 0 {
				return out
			}
		case cssCloseCurly:
			if depth == 0 {
				p.back(t)
				return out
			}
			depth--
		case cssOpenCurly, cssOpenParen, cssOpenSquare, cssFunction:
			depth++
		case cssCloseParen, cssCloseSquare:
			depth = max(0, depth-1)
		}
		out = append(out, t)
	}
}

func (p *cssParser) skipBlock() {
	depth := 1
	for depth > 0 {
		switch t := p.next(); t.kind {
		case cssEOF:
			return
		case cssOpenCurly:
			depth++
		case cssCloseCurly:
			depth--
		}
	}
}

// declaration reads what follows a property name.
func declaration(name string, toks []cssToken) (Declaration, bool) {
	toks = skipSpace(toks)
	if len(toks) == 0 || toks[0].kind != cssColon {
		return Declaration{}, false
	}
	toks = skipSpace(toks[1:])
	d := Declaration{Name: strings.ToLower(name)}
	if n := len(toks); n >= 2 && toks[n-1].kind == cssIdent &&
		strings.EqualFold(toks[n-1].value, "important") {
		if k := skipSpace(toks[:n-1]); len(k) > 0 &&
			k[len(k)-1].kind == cssDelim && k[len(k)-1].delim == '!' {
			d.Important = true
			toks = skipSpace(k[:len(k)-1])
		}
	}
	if len(toks) == 0 {
		return Declaration{}, false
	}
	for _, t := range toks {
		if t.kind == cssBadString || t.kind == cssBadURL {
			return Declaration{}, false
		}
	}
	d.value = toks
	return d, true
}

func parseMediaList(toks []cssToken) MediaList {
	if len(toks) == 0 {
		return nil
	}
	var out MediaList
	for _, part := range splitCommas(toks) {
		out = append(out, parseMediaQuery(skipSpace(part)))
	}
	return out
}

func parseMediaQuery(toks []cssToken) MediaQuery {
	var q MediaQuery
	if len(toks) == 0 {
		return MediaQuery{bad: true}
	}
	if toks[0].kind == cssIdent {
		switch strings.ToLower(toks[0].value) {
		case "not":
			q.not, toks = true, skipSpace(toks[1:])
		case "only":
			toks = skipSpace(toks[1:])
		}
	}
	if len(toks) > 0 && toks[0].kind == cssIdent {
		q.kind, toks = strings.ToLower(toks[0].value), skipSpace(toks[1:])
	}
	for len(toks) > 0 {
		if toks[0].kind == cssIdent {
			if !strings.EqualFold(toks[0].value, "and") {
				return MediaQuery{bad: true}
			}
			toks = skipSpace(toks[1:])
			continue
		}
		if toks[0].kind != cssOpenParen {
			return MediaQuery{bad: true}
		}
		args, rest, ok := parseParens(toks)
		if !ok {
			return MediaQuery{bad: true}
		}
		f, ok := parseMediaFeature(skipSpace(args))
		if !ok {
			return MediaQuery{bad: true}
		}
		q.feats, toks = append(q.feats, f), skipSpace(rest)
	}
	return q
}

func parseMediaFeature(toks []cssToken) (mediaFeature, bool) {
	if len(toks) == 0 || toks[0].kind != cssIdent {
		return mediaFeature{}, false
	}
	f := mediaFeature{name: strings.ToLower(toks[0].value)}
	toks = skipSpace(toks[1:])
	if len(toks) == 0 {
		return f, true
	}
	if toks[0].kind != cssColon {
		return mediaFeature{}, false
	}
	f.value = skipSpace(toks[1:])
	return f, len(f.value) > 0
}

// parseParens returns what a parenthesis encloses and what follows it.
func parseParens(toks []cssToken) ([]cssToken, []cssToken, bool) {
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

// tokensText renders a token run back for an error message.
func tokensText(toks []cssToken) string {
	var b strings.Builder
	for _, t := range toks {
		if b.Len() > 64 {
			b.WriteString("...")
			break
		}
		switch t.kind {
		case cssIdent, cssAtKeyword, cssString, cssURL, cssFunction:
			b.WriteString(t.value)
		case cssHash:
			b.WriteByte('#')
			b.WriteString(t.value)
		case cssDelim:
			b.WriteByte(t.delim)
		case cssSpace:
			b.WriteByte(' ')
		case cssColon:
			b.WriteByte(':')
		case cssComma:
			b.WriteByte(',')
		case cssOpenSquare:
			b.WriteByte('[')
		case cssCloseSquare:
			b.WriteByte(']')
		case cssOpenParen:
			b.WriteByte('(')
		case cssCloseParen:
			b.WriteByte(')')
		}
	}
	return strings.TrimSpace(b.String())
}

// Coverage returns how many of a sheet's declarations the engine reads and
// how many it has no use for.
func (s *Stylesheet) Coverage() (read, ignored int) {
	parent := initialStyle()
	for i := range s.Rules {
		for j := range s.Rules[i].Decls {
			hit := false
			for _, lh := range expand(&s.Rules[i].Decls[j]) {
				scratch := initialStyle()
				if applyProp(&scratch, lh.name, value{toks: lh.toks, em: DefaultFontSize, rm: DefaultFontSize, medium: DefaultFontSize, parent: &parent}) {
					hit = true
				}
			}
			if hit {
				read++
			} else {
				ignored++
			}
		}
	}
	return read, ignored
}

// maxImportDepth bounds an @import chain, which a damaged book writes as a
// cycle.
const maxImportDepth = 8

// Stylesheets returns the sheets that style one part, in cascade order: the
// user agent sheet, then every sheet the part links or carries and everything
// those import.
func (d *Document) Stylesheets(path string, root *Node) []*Stylesheet {
	out := []*Stylesheet{UserAgent()}
	seen := map[string]bool{}
	Walk(root, func(n *Node) bool {
		if n.Type != xhtml.ElementNode {
			return true
		}
		switch n.DataAtom {
		case atom.Link:
			if href := Attr(n, "href"); href != "" && isStylesheetLink(n) {
				out = d.importSheet(out, Resolve(path, href), mediaOf(n), seen, 0)
			}
		case atom.Style:
			if t := Attr(n, "type"); t != "" && !strings.EqualFold(t, "text/css") {
				return false
			}
			s := ParseCSS([]byte(rawText(n)), OriginAuthor)
			under(s, mediaOf(n))
			for _, imp := range s.Imports {
				out = d.importSheet(out, Resolve(path, imp.Path), nestMedia(mediaOf(n), imp.Media), seen, 1)
			}
			out = append(out, s)
			return false
		}
		return true
	})
	return out
}

// StylePart parses one part of a book and computes the style of every element
// in it.
func (d *Document) StylePart(path string, media Media) (*Node, Styles, error) {
	root, err := d.ParsePart(path)
	if err != nil {
		return nil, nil, err
	}
	return root, Cascade(root, media, d.Stylesheets(path, root)...), nil
}

func (d *Document) importSheet(out []*Stylesheet, path string, media []MediaList, seen map[string]bool, depth int) []*Stylesheet {
	if depth >= maxImportDepth || seen[path] {
		return out
	}
	seen[path] = true
	b, err := d.Read(path)
	if err != nil {
		return out
	}
	s := ParseCSS(b, OriginAuthor)
	under(s, media)
	for _, imp := range s.Imports {
		out = d.importSheet(out, Resolve(path, imp.Path), nestMedia(media, imp.Media), seen, depth+1)
	}
	return append(out, s)
}

// under puts every rule of a sheet behind the conditions the sheet itself was
// brought in under.
func under(s *Stylesheet, media []MediaList) {
	if len(media) == 0 {
		return
	}
	for i := range s.Rules {
		s.Rules[i].Media = append(append([]MediaList(nil), media...), s.Rules[i].Media...)
	}
}

func nestMedia(media []MediaList, inner MediaList) []MediaList {
	if len(inner) == 0 {
		return media
	}
	return append(append([]MediaList(nil), media...), inner)
}

func mediaOf(n *Node) []MediaList {
	q := parseMediaList(skipSpace(tokensOf(Attr(n, "media"))))
	if len(q) == 0 {
		return nil
	}
	return []MediaList{q}
}

func isStylesheetLink(n *Node) bool {
	for f := range strings.FieldsSeq(Attr(n, "rel")) {
		if strings.EqualFold(f, "stylesheet") {
			return true
		}
	}
	return false
}

// tokensOf tokenizes a fragment that stands on its own, an attribute rather
// than a sheet.
func tokensOf(s string) []cssToken {
	l := newCSSLexer(s)
	var out []cssToken
	for {
		t := l.next()
		if t.kind == cssEOF {
			return out
		}
		out = append(out, t)
	}
}

// rawText returns what an element encloses, uncollapsed.
func rawText(n *Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}
