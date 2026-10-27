package svg

import (
	"net/url"
	"slices"
	"sort"
	"strings"
)

// rule is one style rule: its selector chain and declarations.
type rule struct {
	sel   []simple
	decls []decl
	// spec is the selector specificity and order the rule's position.
	spec  [3]int
	order int
}

// simple is one compound selector: a type, classes and ids matched on one element.
type simple struct {
	name    string
	classes []string
	ids     []string
	// attrs are the attribute selectors.
	attrs []attrSel
	// child marks a child combinator before this compound.
	child bool
}

type attrSel struct {
	name, value string
	exact       bool
}

type decl struct {
	name, value string
	// important is the declaration CSS 2.1 6.4.2 puts above the rest.
	important bool
}

// readStyles collects the drawing's stylesheets: xml-stylesheet files first, then style elements.
func (d *Document) readStyles(sheets []string) {
	for _, href := range sheets {
		b := d.sheetBytes(href)
		if len(b) == 0 {
			continue
		}
		rules, faces := parseSheet(string(b), len(d.rules))
		d.rules = append(d.rules, rules...)
		d.faces = append(d.faces, faces...)
	}
	var walk func(n *node, depth int)
	walk = func(n *node, depth int) {
		if depth > maxNesting*4 {
			return
		}
		if n.name == "style" {
			rules, faces := parseSheet(text(n), len(d.rules))
			d.rules = append(d.rules, rules...)
			d.faces = append(d.faces, faces...)
		}
		for _, k := range n.kids {
			walk(k, depth+1)
		}
	}
	walk(d.root, 0)
	sort.SliceStable(d.rules, func(i, j int) bool {
		a, b := d.rules[i], d.rules[j]
		for k := 0; k < 3; k++ {
			if a.spec[k] != b.spec[k] {
				return a.spec[k] < b.spec[k]
			}
		}
		return a.order < b.order
	})
	d.keyed = map[string][]int32{}
	for i := range d.rules {
		k := ruleKey(d.rules[i].sel[len(d.rules[i].sel)-1])
		d.keyed[k] = append(d.keyed[k], int32(i))
	}
}

// ruleKey is the id, class or type name a compound requires, or empty.
func ruleKey(s simple) string {
	switch {
	case len(s.ids) > 0:
		return "#" + s.ids[0]
	case len(s.classes) > 0:
		return "." + s.classes[0]
	}
	return s.name
}

// text is the character data an element holds, the sheet for a style.
func text(n *node) string {
	var b strings.Builder
	for _, k := range n.kids {
		if k.name == "" {
			b.WriteString(k.chars)
		}
	}
	return b.String()
}

// sheetBytes reads a stylesheet beside the drawing or in its container.
func (d *Document) sheetBytes(href string) []byte {
	href = strings.TrimSpace(href)
	if href == "" || d.open == nil || strings.Contains(href, "://") {
		return nil
	}
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	b, err := d.open(href)
	if err != nil {
		return nil
	}
	return b
}

// parseSheet reads the rules and font faces of a sheet, skipping unknown at-rules and selectors.
func parseSheet(s string, order int) ([]rule, []faceRule) {
	s = stripComments(s)
	var out []rule
	var faces []faceRule
	for len(s) > 0 {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			break
		}
		head := strings.TrimSpace(s[:i])
		rest := s[i+1:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			break
		}
		body := rest[:j]
		s = rest[j+1:]
		if strings.HasPrefix(head, "@") {
			if strings.EqualFold(strings.TrimSpace(head[1:]), "font-face") {
				if f, ok := readFaceRule(parseDecls(body)); ok {
					faces = append(faces, f)
				}
			}
			continue
		}
		decls := parseDecls(body)
		if len(decls) == 0 {
			continue
		}
		for _, one := range strings.Split(head, ",") {
			sel, spec, ok := parseSelector(one)
			if !ok {
				continue
			}
			out = append(out, rule{sel: sel, decls: decls, spec: spec, order: order})
			order++
		}
	}
	return out, faces
}

func stripComments(s string) string {
	i := strings.Index(s, "/*")
	if i < 0 {
		return s
	}
	var b strings.Builder
	for i >= 0 {
		b.WriteString(s[:i])
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return b.String()
		}
		b.WriteByte(' ')
		s = s[i+2+j+2:]
		i = strings.Index(s, "/*")
	}
	b.WriteString(s)
	return b.String()
}

func parseDecls(body string) []decl {
	var out []decl
	for _, one := range strings.Split(body, ";") {
		k, v, ok := strings.Cut(one, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			continue
		}
		bang := false
		if i := strings.LastIndexByte(v, '!'); i >= 0 &&
			strings.EqualFold(strings.TrimSpace(v[i+1:]), "important") {
			v, bang = strings.TrimSpace(v[:i]), true
		}
		if v == "" {
			continue
		}
		out = append(out, decl{name: strings.ToLower(k), value: v, important: bang})
	}
	return out
}

// parseSelector reads a selector chain, failing on a combinator or pseudo it cannot match.
func parseSelector(s string) ([]simple, [3]int, bool) {
	var spec [3]int
	var out []simple
	child := false
	for _, part := range strings.Fields(strings.ReplaceAll(s, ">", " > ")) {
		if part == ">" {
			child = true
			continue
		}
		one := simple{child: child}
		child = false
		for len(part) > 0 {
			switch part[0] {
			case '.', '#':
				k := 1
				for k < len(part) && part[k] != '.' && part[k] != '#' {
					k++
				}
				if k == 1 {
					return nil, spec, false
				}
				if part[0] == '.' {
					one.classes = append(one.classes, part[1:k])
					spec[1]++
				} else {
					one.ids = append(one.ids, part[1:k])
					spec[0]++
				}
				part = part[k:]
			case '*':
				part = part[1:]
			case '[':
				k := strings.IndexByte(part, ']')
				if k < 0 {
					return nil, spec, false
				}
				a, ok := parseAttrSel(part[1:k])
				if !ok {
					return nil, spec, false
				}
				one.attrs = append(one.attrs, a)
				spec[1]++
				part = part[k+1:]
			case ':':
				return nil, spec, false
			default:
				k := 0
				for k < len(part) && part[k] != '.' && part[k] != '#' &&
					part[k] != ':' && part[k] != '[' {
					k++
				}
				if k == 0 {
					return nil, spec, false
				}
				one.name = strings.ToLower(part[:k])
				spec[2]++
				part = part[k:]
			}
		}
		out = append(out, one)
	}
	if len(out) == 0 {
		return nil, spec, false
	}
	return out, spec, true
}

// parseAttrSel reads a bracketed attribute selector: a name, or a name and value.
func parseAttrSel(s string) (attrSel, bool) {
	name, value, ok := strings.Cut(s, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		return attrSel{}, false
	}
	if !ok {
		return attrSel{name: name}, true
	}
	return attrSel{name: name, value: strings.Trim(strings.TrimSpace(value), `"'`), exact: true}, true
}

// sheetProp is the stylesheet value of name for n, matched against the open elements.
func (r *runner) sheetProp(n *node, name string) (string, bool) {
	if len(r.doc.rules) == 0 {
		return "", false
	}
	c := r.candidates(n)
	if !r.spend(ruleWork * int64(len(c))) {
		return "", false
	}
	out, found, bang := "", false, false
	for _, i := range c {
		ru := &r.doc.rules[i]
		if !r.matches(ru.sel, n) {
			continue
		}
		for _, d := range ru.decls {
			if d.name == name && (d.important || !bang) {
				out, found, bang = d.value, true, d.important
			}
		}
	}
	return out, found
}

// candidates are the rules whose last compound n can match, in sheet order.
func (r *runner) candidates(n *node) []int32 {
	if r.candOf == n {
		return r.cands
	}
	k := r.doc.keyed
	c := append(r.cands[:0], k[""]...)
	if id := n.attr["id"]; id != "" {
		c = append(c, k["#"+id]...)
	}
	for _, cl := range strings.Fields(n.attr["class"]) {
		c = append(c, k["."+cl]...)
	}
	if name := strings.ToLower(n.name); name != "" {
		c = append(c, k[name]...)
	}
	slices.Sort(c)
	r.cands, r.candOf = slices.Compact(c), n
	return r.cands
}

// matches walks a selector chain backwards through the open elements.
func (r *runner) matches(sel []simple, n *node) bool {
	last := len(sel) - 1
	if !matchOne(sel[last], n) {
		return false
	}
	i := last - 1
	open := r.open
	k := len(open) - 2
	for i >= 0 {
		if k < 0 {
			return false
		}
		if matchOne(sel[i], open[k]) {
			i--
			k--
			continue
		}
		if sel[i+1].child {
			return false
		}
		k--
	}
	return true
}

func matchOne(s simple, n *node) bool {
	if s.name != "" && s.name != strings.ToLower(n.name) {
		return false
	}
	for _, id := range s.ids {
		if n.attr["id"] != id {
			return false
		}
	}
	for _, a := range s.attrs {
		v, ok := n.attr[a.name]
		if !ok || (a.exact && v != a.value) {
			return false
		}
	}
	if len(s.classes) == 0 {
		return true
	}
	have := strings.Fields(n.attr["class"])
	for _, want := range s.classes {
		found := false
		for _, h := range have {
			if h == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
