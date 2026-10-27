package html

import (
	"fmt"
	"slices"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Origin is where a stylesheet came from, the first thing the cascade sorts.
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
	// Faces are the fonts the sheet's @font-face rules bring with them.
	Faces  []FontFace
	Origin Origin
	// Errors are the selectors and declarations the sheet dropped.
	Errors []error
}

// FontFace is an @font-face rule: a family the book brings and where its program is.
type FontFace struct {
	Family string
	// Src are the parts the rule names, in order, resolved against the sheet.
	Src    []string
	Weight int
	Italic bool
}

// Import is what an @import rule asks for.
type Import struct {
	// Path is the reference as written, before Resolve.
	Path  string
	Media MediaList
}

// Rule is a selector list and the declarations it applies.
type Rule struct {
	Selectors []Selector
	Decls     []Declaration
	// Media are the conditions of the @media rules around the rule; all match.
	Media []MediaList
	// order is the rule's position in the sheet, breaking a specificity tie.
	order int
}

// Declaration is one property and its value.
type Declaration struct {
	Name      string
	Important bool
	value     []cssToken
}

// MediaList is the comma separated query list of a media rule.
// It matches when any query does, and always when empty.
type MediaList []MediaQuery

// MediaQuery is one query of a media list.
type MediaQuery struct {
	not  bool
	kind string
	// bad marks a query that did not parse and matches nothing.
	bad   bool
	feats []mediaFeature
}

type mediaFeature struct {
	name  string
	value []cssToken
}

// Media is the viewport in CSS pixels and the font size relative lengths in a query resolve against.
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

// Match reports any query of the list matching, true for an empty list.
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

// ParseCSS reads a stylesheet. It never fails; anything it cannot read goes in Errors.
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
	// depth bounds the nesting of at-rules.
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

// rules reads a list of rules up to the end of input or, when nested, the closing brace.
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
	case "font-face":
		if !brace {
			return
		}
		if f, ok := fontFace(p.declarations()); ok {
			p.sheet.Faces = append(p.sheet.Faces, f)
		}
	default:
		if brace {
			p.skipBlock()
		}
	}
}

// fontFace reads the declarations of an @font-face rule.
func fontFace(decls []Declaration) (FontFace, bool) {
	f := FontFace{Weight: 400}
	for i := range decls {
		v := value{toks: decls[i].value}
		switch decls[i].Name {
		case "font-family":
			if names, ok := v.families(); ok {
				f.Family = names[0]
			}
		case "font-weight":
			if w, ok := v.fontWeight(400); ok {
				f.Weight = w
			}
		case "font-style":
			f.Italic = v.ident() == "italic" || v.ident() == "oblique"
		case "src":
			f.Src = fontSrc(decls[i].value)
		}
	}
	return f, f.Family != "" && len(f.Src) > 0
}

// fontSrc reads the src list of an @font-face rule, skipping local faces.
func fontSrc(toks []cssToken) []string {
	var out []string
	for _, part := range splitCommas(toks) {
		part = skipSpace(part)
		if len(part) == 0 {
			continue
		}
		switch t := part[0]; {
		case t.kind == cssURL && t.value != "":
			out = append(out, t.value)
		case t.kind == cssFunction && strings.EqualFold(t.value, "url"):
			args, _, ok := parseArgs(part)
			if !ok {
				continue
			}
			if a := skipSpace(args); len(a) == 1 && a[0].kind == cssString && a[0].value != "" {
				out = append(out, a[0].value)
			}
		}
	}
	return out
}

// prelude reads the tokens before a block and reports whether a block followed.
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

// declarations reads a declaration block from inside its brace.
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

// values reads a declaration value up to its semicolon or closing brace, leaving the brace.
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

// maxImportDepth bounds an @import chain.
const maxImportDepth = 8

// Stylesheets returns the sheets that style one part, in cascade order: the
// user agent sheet, then what the part links or carries and what those import.
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
			resolveURLs(s, path)
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

// StylePart parses one part of a book and styles every element in it.
func (d *Document) StylePart(path string, media Media) (*Node, Styles, error) {
	root, err := d.ParsePart(path)
	if err != nil {
		return nil, Styles{}, err
	}
	return root, Cascade(root, media, d.Stylesheets(path, root)...), nil
}

func (d *Document) importSheet(out []*Stylesheet, path string, media []MediaList, seen map[string]bool, depth int) []*Stylesheet {
	if depth >= maxImportDepth || seen[path] {
		return out
	}
	seen[path] = true
	s := d.sheet(path)
	if s == nil {
		return out
	}
	if len(media) > 0 {
		c := *s
		c.Rules = slices.Clone(s.Rules)
		s = &c
		under(s, media)
	}
	for _, imp := range s.Imports {
		out = d.importSheet(out, Resolve(path, imp.Path), nestMedia(media, imp.Media), seen, depth+1)
	}
	return append(out, s)
}

// sheet is the stylesheet at a path, read and resolved once, nil when there is none.
func (d *Document) sheet(path string) *Stylesheet {
	d.sheetMu.Lock()
	defer d.sheetMu.Unlock()
	if s, ok := d.sheets[path]; ok {
		return s
	}
	var s *Stylesheet
	if b, err := d.Read(path); err == nil {
		s = ParseCSS(b, OriginAuthor)
		resolveURLs(s, path)
	}
	if d.sheets == nil {
		d.sheets = map[string]*Stylesheet{}
	}
	d.sheets[path] = s
	return s
}

// resolveURLs resolves every url in a sheet against the sheet's own path.
func resolveURLs(s *Stylesheet, base string) {
	resolveFaces(s, base)
	for i := range s.Rules {
		for j := range s.Rules[i].Decls {
			toks := s.Rules[i].Decls[j].value
			fn := false
			for k := range toks {
				switch {
				case toks[k].kind == cssURL && toks[k].value != "":
					toks[k].value = Resolve(base, toks[k].value)
				case toks[k].kind == cssFunction:
					fn = strings.EqualFold(toks[k].value, "url")
				case toks[k].kind == cssString && fn && toks[k].value != "":
					toks[k].value, fn = Resolve(base, toks[k].value), false
				case toks[k].kind != cssSpace:
					fn = false
				}
			}
		}
	}
}

// resolveFaces resolves the @font-face sources of a sheet against base.
func resolveFaces(s *Stylesheet, base string) {
	for i := range s.Faces {
		for j, src := range s.Faces[i].Src {
			s.Faces[i].Src[j] = Resolve(base, src)
		}
	}
}

// under puts every rule of a sheet behind the sheet's own conditions.
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

// tokensOf tokenizes a fragment such as a style attribute.
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
