package html

import (
	"bytes"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Node is a parsed element of a part, and Parse is what produces one. The
// tree is x/net/html's, which is the HTML5 tree builder the layout engine
// walks rather than a tree of its own.
type Node = xhtml.Node

// Parse reads one part of a book into a DOM. Anything a browser accepts
// parses; nothing about it fails.
func Parse(b []byte) (*Node, error) { return xhtml.Parse(bytes.NewReader(b)) }

// ParsePart reads a part by path.
func (d *Document) ParsePart(path string) (*Node, error) {
	b, err := d.Read(path)
	if err != nil {
		return nil, err
	}
	if d.typeOf(path) == "text/plain" {
		return Parse(plainMarkup(b))
	}
	if d.kind == KindMOBI {
		b = mobiMarkup(b, d.filepos)
	}
	return Parse(b)
}

// typeOf is the media type the container declares for a part.
func (d *Document) typeOf(path string) string {
	for _, it := range d.manifest {
		if it.Path == path {
			return it.Type
		}
	}
	return ""
}

// plainMarkup wraps a part that is text rather than markup, which is what a
// plain file and an older MOBI hold. The user agent sheet gives pre the white
// space handling the lines of a text file need.
func plainMarkup(b []byte) []byte {
	return []byte("<html><body><pre>" + xhtml.EscapeString(string(b)) + "</pre></body></html>")
}

// Text returns what the whole book says, a blank line between blocks, which
// is the text of every part of the spine in order.
func (d *Document) Text() (string, error) {
	var b strings.Builder
	var first error
	for _, item := range d.Spine() {
		var text string
		switch {
		case item.IsChapter():
			root, err := d.ParsePart(item.Path)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			text = NodeText(root)
		default:
			// A drawing in the spine is a page of the book and says what it
			// draws, which no box tree holds.
			s, err := d.drawnText(item)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			text = s
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String(), first
}

// NodeText returns what a subtree says, with a newline where a block element
// ends and nothing where an inline one does.
func NodeText(n *Node) string {
	var w textWriter
	w.walk(n, false)
	return strings.TrimLeft(w.b.String(), "\n")
}

// textWriter collects the text of a tree, holding back the whitespace between
// elements until something is written after it.
type textWriter struct {
	b strings.Builder
	// space and breaks are the whitespace owed before the next character: a
	// space, or that many newlines.
	space  bool
	breaks int
	// last is the character written, which decides whether a space between
	// two wide ones is worth keeping.
	last rune
}

func (w *textWriter) walk(n *Node, pre bool) {
	switch n.Type {
	case xhtml.TextNode:
		w.text(n.Data, pre)
		return
	case xhtml.ElementNode:
		switch n.DataAtom {
		case atom.Head, atom.Script, atom.Style, atom.Title, atom.Rp:
			return
		case atom.Br:
			w.newline(1)
			return
		case atom.Img:
			return
		}
		if n.DataAtom == atom.Pre {
			pre = true
		}
		if block(n.DataAtom) {
			w.newline(1)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, pre)
	}
	if n.Type == xhtml.ElementNode && block(n.DataAtom) {
		w.newline(1)
	}
}

func (w *textWriter) newline(n int) {
	if w.b.Len() > 0 || w.breaks > 0 {
		w.breaks = max(w.breaks, n)
		w.space = false
	}
}

func (w *textWriter) text(s string, pre bool) {
	if pre {
		w.flush(0)
		w.write(s)
		return
	}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isSpaceByte(s[j]) {
			j++
		}
		if j > i {
			r, _ := utf8.DecodeRuneInString(s[i:j])
			w.flush(r)
			w.write(s[i:j])
		}
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		if j > i && w.b.Len() > 0 {
			w.space = true
		}
		i = j
	}
}

func (w *textWriter) flush(next rune) {
	switch {
	case w.breaks > 0:
		w.b.WriteString(strings.Repeat("\n", w.breaks))
		w.last = '\n'
	case w.space && !(wide(w.last) && wide(next)):
		w.b.WriteByte(' ')
		w.last = ' '
	}
	w.breaks, w.space = 0, false
}

func (w *textWriter) write(s string) {
	w.b.WriteString(s)
	if r, n := utf8.DecodeLastRuneInString(s); n > 0 {
		w.last = r
	}
}

// wide reports a character of a script written without spaces between words,
// where the whitespace of the markup is not a word break.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11ff,
		r >= 0x2e80 && r <= 0x303e,
		r >= 0x3041 && r <= 0x33ff,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff,
		r >= 0xa000 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == 0x0b
}

// block reports an element a line ends at.
func block(a atom.Atom) bool {
	switch a {
	case atom.Address, atom.Article, atom.Aside, atom.Blockquote, atom.Body,
		atom.Center, atom.Dd, atom.Details, atom.Dialog, atom.Div, atom.Dl,
		atom.Dt, atom.Fieldset, atom.Figcaption, atom.Figure, atom.Footer,
		atom.Form, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Header, atom.Hgroup, atom.Hr, atom.Li, atom.Main, atom.Nav,
		atom.Ol, atom.P, atom.Pre, atom.Section, atom.Table, atom.Td,
		atom.Tfoot, atom.Th, atom.Thead, atom.Tr, atom.Ul:
		return true
	}
	return false
}

// Attr returns an element's attribute by name, and empty when it has none.
func Attr(n *Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// Walk calls fn for every node of a tree, depth first, and stops descending
// where fn returns false.
func Walk(n *Node, fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		Walk(c, fn)
	}
}

// fromHeadings builds a table of contents out of the headings of every part,
// which is the only one a book with no navigation has.
func (d *Document) fromHeadings() []Outline {
	var flat []Outline
	var level []int
	for _, item := range d.spine {
		if !item.IsChapter() {
			continue
		}
		root, err := d.ParsePart(item.Path)
		if err != nil {
			continue
		}
		Walk(root, func(n *Node) bool {
			if n.Type != xhtml.ElementNode {
				return true
			}
			l := heading(n.DataAtom)
			if l == 0 {
				return true
			}
			if title := squash(NodeText(n)); title != "" {
				flat = append(flat, Outline{Title: title, Path: item.Path, Fragment: Attr(n, "id")})
				level = append(level, l)
			}
			return false
		})
	}
	return nest(flat, level)
}

// heading is what depth an element is a heading at, zero for anything else.
func heading(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 0
}

// nest turns a flat run of headings and their levels into the tree they mean.
func nest(items []Outline, level []int) []Outline {
	var out []Outline
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && level[j] > level[i] {
			j++
		}
		e := items[i]
		e.Children = nest(items[i+1:j], level[i+1:j])
		out = append(out, e)
		i = j
	}
	return out
}
