package svg

import (
	"net/url"
	"sort"
	"strings"
)

// A style element carries CSS, and what a drawing needs of it is small: the
// selectors an exporter writes are a type, a class, an id and lists of those.
// The engine in html cascades onto the box tree of a document and knows the
// properties of one, so it is not what an SVG wants; this reads the same
// syntax and answers the one question an element asks, which is what a sheet
// declares for it.
type rule struct {
	sel   []simple
	decls []decl
	// spec is how much the selector counts for and order which rule was
	// written first, which is what settles two that both match.
	spec  [3]int
	order int
}

// simple is one compound selector of a chain: a type name and any number of
// classes and ids, all of which have to match the same element.
type simple struct {
	name    string
	classes []string
	ids     []string
	// child says the element must be the one directly inside the match
	// before it rather than anywhere inside it.
	child bool
}

type decl struct{ name, value string }

// readStyles collects every sheet the drawing names, in the order they are
// read: the files an xml-stylesheet instruction points at first, then every
// style element of the tree.
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
}

// text is the character data an element holds, which for a style element is
// the sheet.
func text(n *node) string {
	var b strings.Builder
	for _, k := range n.kids {
		if k.name == "" {
			b.WriteString(k.chars)
		}
	}
	return b.String()
}

// sheetBytes reads a stylesheet the drawing names, which is a file beside it
// or one the container it came out of holds.
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

// parseSheet reads the rules of a sheet and the faces it brings with it,
// skipping what it does not know: an at-rule other than font-face, and a
// selector with anything in it this cannot match.
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
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + " " + s[i+2+j+2:]
	}
}

func parseDecls(body string) []decl {
	var out []decl
	for _, one := range strings.Split(body, ";") {
		k, v, ok := strings.Cut(one, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			continue
		}
		out = append(out, decl{name: strings.ToLower(k), value: v})
	}
	return out
}

// parseSelector reads a chain of compound selectors, and fails on anything
// with a combinator or a pseudo this does not match, so that a rule it cannot
// honour is left out rather than applied to the wrong elements.
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
			case ':', '[':
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

// sheetProp is what the stylesheets declare for an element, and false when
// none of them declare it. The chain is matched against the ancestors the
// runner is holding, which is how a descendant selector is answered without
// the tree knowing its parents.
func (r *runner) sheetProp(n *node, name string) (string, bool) {
	if len(r.doc.rules) == 0 {
		return "", false
	}
	out, found := "", false
	for i := range r.doc.rules {
		ru := &r.doc.rules[i]
		if !r.matches(ru.sel, n) {
			continue
		}
		for _, d := range ru.decls {
			if d.name == name {
				out, found = d.value, true
			}
		}
	}
	return out, found
}

// matches walks a selector chain from its last compound backwards through the
// open elements.
func (r *runner) matches(sel []simple, n *node) bool {
	last := len(sel) - 1
	if !matchOne(sel[last], n) {
		return false
	}
	i := last - 1
	// open holds the elements this one is inside, outermost first, with the
	// element itself on the end.
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
