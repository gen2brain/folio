package html

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	fb2Body  = "book.xhtml"
	fb2Sheet = "book.css"
	// fb2MaxDepth bounds how deep the sections of a book may nest.
	fb2MaxDepth = 32
)

// fb2Sheets is the type a book is set in, which the format leaves to the
// reader the way MuPDF's own sheet does.
const fb2Sheets = `body{margin:0}
h1,h2,h3,h4,h5,h6{text-align:center}
p{margin-top:1em;text-align:justify}
p+p{margin-top:0;text-indent:1.5em}
.subtitle{text-align:center;font-weight:bold;margin:1em 0}
.epigraph,.cite{margin:1em 2em;font-style:italic}
.epigraph .author,.cite .author{text-align:right;font-style:normal}
.poem{margin:1em 2em}
.stanza{margin:1em 0}
.v{margin:0;text-indent:0;text-align:left}
.empty{margin:0;padding-top:1em}
.annotation{margin:1em 2em;font-size:smaller}
.note{font-size:smaller;vertical-align:super}
img{margin:1em 0}
table{border-collapse:collapse}
th,td{border:1px solid #999;padding:2px 4px}
`

// The elements a run of text may be written inside.
var fb2Inline = map[string]string{
	"strong":        "strong",
	"emphasis":      "em",
	"strikethrough": "s",
	"sub":           "sub",
	"sup":           "sup",
	"code":          "code",
	"style":         "span",
}

// fb2Binary is one of the pictures a book carries at its end.
type fb2Binary struct {
	kind string
	data string
}

type fb2 struct {
	root   *xnode
	body   strings.Builder
	bins   map[string]fb2Binary
	titles []Outline
	// ids counts the anchors written for a section that carries none.
	ids int
}

func openFB2(b []byte) (*Document, error) {
	root, err := parseMixed(fb2Decode(b))
	if err != nil {
		return nil, err
	}
	if root.name != "FictionBook" {
		return nil, fmt.Errorf("%w: not a FictionBook", ErrInvalid)
	}
	c := &fb2{root: root, bins: map[string]fb2Binary{}}
	for _, k := range root.all("binary") {
		if id := k.at("id"); id != "" {
			c.bins[id] = fb2Binary{kind: k.at("content-type"), data: k.text}
		}
	}

	c.body.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml"><head>` +
		`<link rel="stylesheet" type="text/css" href="` + fb2Sheet + `"/>` +
		`<title>` + esc(c.title()) + `</title></head><body>`)
	c.cover()
	for _, body := range root.all("body") {
		c.section(body, 0)
	}
	c.body.WriteString("</body></html>")

	d := &Document{kind: KindFB2}
	d.meta = c.metadata()
	d.outline = c.titles
	page := []byte(c.body.String())
	d.spine = []Item{{Path: fb2Body, Type: "application/xhtml+xml", Linear: true}}
	d.manifest = append([]Item{
		{Path: fb2Body, Type: "application/xhtml+xml", Linear: true},
		{Path: fb2Sheet, Type: "text/css"},
	}, c.items()...)
	d.read = func(p string) ([]byte, error) {
		switch p {
		case fb2Body:
			return page, nil
		case fb2Sheet:
			return []byte(fb2Sheets), nil
		}
		v, ok := c.bins[p]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
		}
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v.data), ""))
	}
	return d, nil
}

func (c *fb2) items() []Item {
	out := make([]Item, 0, len(c.bins))
	for p, v := range c.bins {
		kind := v.kind
		if kind == "" {
			kind = chmType(p)
		}
		out = append(out, Item{Path: p, Type: kind})
	}
	return out
}

// info is one element of what the book says about itself.
func (c *fb2) info(name string) *xnode {
	d := c.root.child("description")
	if d == nil {
		return nil
	}
	t := d.child("title-info")
	if t == nil {
		return nil
	}
	return t.child(name)
}

func (c *fb2) title() string {
	if n := c.info("book-title"); n != nil {
		return strings.TrimSpace(n.text)
	}
	return ""
}

func (c *fb2) metadata() Metadata {
	m := Metadata{Title: c.title()}
	d := c.root.child("description")
	if d == nil {
		return m
	}
	t := d.child("title-info")
	if t == nil {
		return m
	}
	var authors []string
	for _, a := range t.all("author") {
		var parts []string
		for _, f := range []string{"first-name", "middle-name", "last-name", "nickname"} {
			if v := strings.TrimSpace(a.child(f).textOf()); v != "" {
				parts = append(parts, v)
			}
		}
		if len(parts) > 0 {
			authors = append(authors, strings.Join(parts, " "))
		}
	}
	m.Author = strings.Join(authors, ", ")
	m.Language = strings.TrimSpace(t.child("lang").textOf())
	if a := t.child("annotation"); a != nil {
		m.Description = strings.TrimSpace(flatten(a))
	}
	for _, g := range t.all("genre") {
		if v := strings.TrimSpace(g.text); v != "" {
			m.Subjects = append(m.Subjects, v)
		}
	}
	if p := d.child("publish-info"); p != nil {
		m.Publisher = strings.TrimSpace(p.child("publisher").textOf())
	}
	if id := d.child("document-info"); id != nil {
		m.Identifier = strings.TrimSpace(id.child("id").textOf())
	}
	return m
}

func (n *xnode) textOf() string {
	if n == nil {
		return ""
	}
	return n.text
}

// flatten is everything an element says, however deep.
func flatten(n *xnode) string {
	var b strings.Builder
	var walk func(*xnode, int)
	walk = func(v *xnode, depth int) {
		if depth > fb2MaxDepth {
			return
		}
		if v.name == "" {
			b.WriteString(v.text)
			return
		}
		for _, k := range v.kids {
			walk(k, depth+1)
		}
	}
	walk(n, 0)
	return strings.Join(strings.Fields(b.String()), " ")
}

// cover writes the picture the book opens with, which the description names.
func (c *fb2) cover() {
	page := c.info("coverpage")
	if page == nil {
		return
	}
	for _, img := range page.all("image") {
		c.image(img, true)
	}
}

// section writes one body or section and everything under it.
func (c *fb2) section(n *xnode, depth int) {
	if depth > fb2MaxDepth {
		return
	}
	id := n.at("id")
	if id == "" {
		c.ids++
		id = "fb2-" + strconv.Itoa(c.ids)
	}
	c.body.WriteString(`<section id="` + esc(id) + `">`)
	c.contents(n, id, depth, true)
	c.body.WriteString("</section>")
}

// contents writes what one section or block holds. heads says a title here
// is a heading of the book rather than the name of a poem or a quotation.
func (c *fb2) contents(n *xnode, id string, depth int, heads bool) {
	for _, k := range n.kids {
		switch k.name {
		case "":
			// A book that writes its text straight into a section.
			if v := strings.TrimSpace(k.text); v != "" {
				c.body.WriteString("<p>" + esc(v) + "</p>")
			}
		case "section":
			c.section(k, depth+1)
		case "title":
			if heads {
				c.heading(k, id, depth)
			} else {
				c.para(k, "p", "subtitle")
			}
		case "subtitle":
			c.para(k, "p", "subtitle")
		case "epigraph":
			c.block(k, "blockquote", "epigraph", depth)
		case "cite":
			c.block(k, "blockquote", "cite", depth)
		case "annotation":
			c.block(k, "div", "annotation", depth)
		case "poem":
			c.block(k, "div", "poem", depth)
		case "stanza":
			c.block(k, "div", "stanza", depth)
		case "p":
			c.para(k, "p", "")
		case "v":
			c.para(k, "p", "v")
		case "text-author":
			c.para(k, "p", "author")
		case "date":
			c.para(k, "p", "date")
		case "empty-line":
			c.body.WriteString(`<p class="empty"><br/></p>`)
		case "image":
			c.image(k, true)
		case "table":
			c.table(k)
		default:
			c.contents(k, id, depth+1, heads)
		}
	}
}

// heading writes the title of a section at the depth it sits, whose lines the
// format writes as paragraphs of their own.
func (c *fb2) heading(n *xnode, id string, depth int) {
	tag := "h" + strconv.Itoa(min(depth+1, 6))
	var b strings.Builder
	for _, k := range n.kids {
		switch k.name {
		case "p", "v", "":
			if b.Len() > 0 {
				b.WriteString("<br/>")
			}
			b.WriteString(c.runs(k))
		case "empty-line":
			b.WriteString("<br/>")
		}
	}
	line := strings.TrimSpace(b.String())
	if line == "" {
		return
	}
	c.body.WriteString("<" + tag + ">" + line + "</" + tag + ">")
	if title := strings.TrimSpace(stripTags(line)); title != "" {
		c.titles = append(c.titles, Outline{Title: title, Path: fb2Body, Fragment: id})
	}
}

func (c *fb2) block(n *xnode, tag, class string, depth int) {
	c.body.WriteString("<" + tag + ` class="` + class + `">`)
	c.contents(n, "", depth+1, false)
	c.body.WriteString("</" + tag + ">")
}

func (c *fb2) para(n *xnode, tag, class string) {
	c.body.WriteString("<" + tag)
	if class != "" {
		c.body.WriteString(` class="` + class + `"`)
	}
	if id := n.at("id"); id != "" {
		c.body.WriteString(` id="` + esc(id) + `"`)
	}
	c.body.WriteString(">" + c.runs(n) + "</" + tag + ">")
}

// runs writes what one paragraph says, with the text and the elements in the
// order they were written.
func (c *fb2) runs(n *xnode) string {
	var b strings.Builder
	c.inline(&b, n, 0)
	return b.String()
}

func (c *fb2) inline(b *strings.Builder, n *xnode, depth int) {
	if depth > fb2MaxDepth {
		return
	}
	if n.name == "" {
		b.WriteString(esc(n.text))
		return
	}
	for _, k := range n.kids {
		switch {
		case k.name == "":
			b.WriteString(esc(k.text))
		case k.name == "a":
			href := k.rel("href")
			if href == "" {
				href = k.at("href")
			}
			class := ""
			if k.at("type") == "note" {
				class = ` class="note"`
			}
			b.WriteString(`<a href="` + esc(href) + `"` + class + ">")
			c.inline(b, k, depth+1)
			b.WriteString("</a>")
		case k.name == "image":
			var img strings.Builder
			c.write(&img, k, false)
			b.WriteString(img.String())
		case fb2Inline[k.name] != "":
			t := fb2Inline[k.name]
			b.WriteString("<" + t + ">")
			c.inline(b, k, depth+1)
			b.WriteString("</" + t + ">")
		default:
			c.inline(b, k, depth+1)
		}
	}
}

func (c *fb2) image(n *xnode, block bool) { c.write(&c.body, n, block) }

// write puts the picture an image element names, one of the book's binaries.
func (c *fb2) write(b *strings.Builder, n *xnode, block bool) {
	href := n.rel("href")
	if href == "" {
		href = n.at("href")
	}
	id := strings.TrimPrefix(href, "#")
	if id == "" {
		return
	}
	if _, ok := c.bins[id]; !ok {
		return
	}
	alt := n.at("alt")
	img := `<img src="` + esc(id) + `"`
	if alt != "" {
		img += ` alt="` + esc(alt) + `"`
	}
	img += "/>"
	if block {
		img = "<p>" + img + "</p>"
	}
	b.WriteString(img)
}

func (c *fb2) table(n *xnode) {
	c.body.WriteString("<table>")
	for _, r := range n.all("tr") {
		c.body.WriteString("<tr>")
		for _, cell := range r.kids {
			tag := ""
			switch cell.name {
			case "th":
				tag = "th"
			case "td":
				tag = "td"
			default:
				continue
			}
			c.body.WriteString("<" + tag)
			if v := cell.at("colspan"); v != "" {
				c.body.WriteString(` colspan="` + esc(v) + `"`)
			}
			if v := cell.at("rowspan"); v != "" {
				c.body.WriteString(` rowspan="` + esc(v) + `"`)
			}
			switch cell.at("align") {
			case "center":
				c.body.WriteString(` style="text-align:center"`)
			case "right":
				c.body.WriteString(` style="text-align:right"`)
			}
			c.body.WriteString(">" + c.runs(cell) + "</" + tag + ">")
		}
		c.body.WriteString("</tr>")
	}
	c.body.WriteString("</table>")
}

// fb2Decode turns the bytes into the UTF-8 the parser reads, which a book
// written in a Cyrillic or a Latin code page is not.
func fb2Decode(b []byte) []byte {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return fromUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return fromUTF16(b[2:], true)
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		b = b[3:]
	}
	switch strings.ToLower(xmlEncoding(b)) {
	case "windows-1251", "cp1251", "x-cp1251":
		return fromTable(b, cp1251High[:])
	case "koi8-r", "koi8r", "koi8-u":
		return fromTable(b, koi8High[:])
	case "windows-1252", "cp1252":
		return fromCP1252(b)
	case "iso-8859-1", "latin1", "iso8859-1":
		return fromTable(b, latin1High[:])
	}
	if utf8.Valid(b) {
		return b
	}
	return fromTable(b, cp1251High[:])
}

// xmlEncoding is what the declaration at the head of the file says it is.
func xmlEncoding(b []byte) string {
	if len(b) > 256 {
		b = b[:256]
	}
	s := string(b)
	i := strings.Index(s, "encoding=")
	if i < 0 {
		return ""
	}
	s = s[i+len("encoding="):]
	if s == "" {
		return ""
	}
	q := s[0]
	if q != '"' && q != '\'' {
		return ""
	}
	if j := strings.IndexByte(s[1:], q); j >= 0 {
		return s[1 : 1+j]
	}
	return ""
}

// fromTable converts a single byte encoding whose upper half the table gives.
func fromTable(b []byte, high []rune) []byte {
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		if c < 0x80 {
			out = append(out, c)
			continue
		}
		out = utf8.AppendRune(out, high[c-0x80])
	}
	return replaceEncoding(out)
}

func fromUTF16(b []byte, big bool) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i+1 < len(b); i += 2 {
		v := rune(b[i]) | rune(b[i+1])<<8
		if big {
			v = rune(b[i])<<8 | rune(b[i+1])
		}
		out = utf8.AppendRune(out, v)
	}
	return replaceEncoding(out)
}

// replaceEncoding rewrites the declaration, which now says the wrong thing
// about bytes that have been converted.
func replaceEncoding(b []byte) []byte {
	s := string(b)
	i := strings.Index(s, "?>")
	if i < 0 || i > 256 {
		return b
	}
	head := s[:i]
	j := strings.Index(head, "encoding=")
	if j < 0 {
		return b
	}
	rest := head[j+len("encoding="):]
	if rest == "" {
		return b
	}
	q := rest[0]
	k := strings.IndexByte(rest[1:], q)
	if k < 0 {
		return b
	}
	return []byte(head[:j] + `encoding="utf-8"` + rest[1+k+1:] + s[i:])
}

// latin1High is the upper half of ISO 8859-1, the code points themselves.
var latin1High = func() [128]rune {
	var t [128]rune
	for i := range t {
		t[i] = rune(0x80 + i)
	}
	return t
}()

// cp1251High is the upper half of Windows-1251, the Cyrillic code page a
// Russian book is usually written in.
var cp1251High = [128]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021,
	0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0xFFFD, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7,
	0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7,
	0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
	0x0410, 0x0411, 0x0412, 0x0413, 0x0414, 0x0415, 0x0416, 0x0417,
	0x0418, 0x0419, 0x041A, 0x041B, 0x041C, 0x041D, 0x041E, 0x041F,
	0x0420, 0x0421, 0x0422, 0x0423, 0x0424, 0x0425, 0x0426, 0x0427,
	0x0428, 0x0429, 0x042A, 0x042B, 0x042C, 0x042D, 0x042E, 0x042F,
	0x0430, 0x0431, 0x0432, 0x0433, 0x0434, 0x0435, 0x0436, 0x0437,
	0x0438, 0x0439, 0x043A, 0x043B, 0x043C, 0x043D, 0x043E, 0x043F,
	0x0440, 0x0441, 0x0442, 0x0443, 0x0444, 0x0445, 0x0446, 0x0447,
	0x0448, 0x0449, 0x044A, 0x044B, 0x044C, 0x044D, 0x044E, 0x044F,
}

// koi8High is the upper half of KOI8-R, the other Cyrillic encoding.
var koi8High = [128]rune{
	0x2500, 0x2502, 0x250C, 0x2510, 0x2514, 0x2518, 0x251C, 0x2524,
	0x252C, 0x2534, 0x253C, 0x2580, 0x2584, 0x2588, 0x258C, 0x2590,
	0x2591, 0x2592, 0x2593, 0x2320, 0x25A0, 0x2219, 0x221A, 0x2248,
	0x2264, 0x2265, 0x00A0, 0x2321, 0x00B0, 0x00B2, 0x00B7, 0x00F7,
	0x2550, 0x2551, 0x2552, 0x0451, 0x2553, 0x2554, 0x2555, 0x2556,
	0x2557, 0x2558, 0x2559, 0x255A, 0x255B, 0x255C, 0x255D, 0x255E,
	0x255F, 0x2560, 0x2561, 0x0401, 0x2562, 0x2563, 0x2564, 0x2565,
	0x2566, 0x2567, 0x2568, 0x2569, 0x256A, 0x256B, 0x256C, 0x00A9,
	0x044E, 0x0430, 0x0431, 0x0446, 0x0434, 0x0435, 0x0444, 0x0433,
	0x0445, 0x0438, 0x0439, 0x043A, 0x043B, 0x043C, 0x043D, 0x043E,
	0x043F, 0x044F, 0x0440, 0x0441, 0x0442, 0x0443, 0x0436, 0x0432,
	0x044C, 0x044B, 0x0437, 0x0448, 0x044D, 0x0449, 0x0447, 0x044A,
	0x042E, 0x0410, 0x0411, 0x0426, 0x0414, 0x0415, 0x0424, 0x0413,
	0x0425, 0x0418, 0x0419, 0x041A, 0x041B, 0x041C, 0x041D, 0x041E,
	0x041F, 0x042F, 0x0420, 0x0421, 0x0422, 0x0423, 0x0416, 0x0412,
	0x042C, 0x042B, 0x0417, 0x0428, 0x042D, 0x0429, 0x0427, 0x042A,
}
