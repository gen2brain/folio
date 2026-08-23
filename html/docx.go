package html

import (
	"fmt"
	"strconv"
	"strings"
)

// The parts a generated document is served as.
const (
	docxBody  = "document.xhtml"
	docxSheet = "document.css"
)

const docxMaxLists = 1 << 12

// docxStyle is one entry of the style sheet a document carries: what it is
// based on, and the paragraph and run properties it sets.
type docxStyle struct {
	kind    string
	base    string
	name    string
	outline int
	pPr     *xnode
	rPr     *xnode
}

// docxLevel is one level of a list, and how it is numbered.
type docxLevel struct {
	format string
	start  int
}

type docx struct {
	o      *ooxml
	styles map[string]docxStyle
	// nums maps a numbering identifier onto the levels of the list it names.
	nums map[string][]docxLevel
	// dpPr and drPr are the properties every element starts from.
	dpPr, drPr *xnode

	body  strings.Builder
	sheet strings.Builder
	// lists is the stack of list elements open at the moment, by level.
	lists []int
	// media is the pictures the body refers to, by the path it names them by.
	media map[string]string
	// notes are the footnotes and endnotes the body refers to, by identifier.
	notes map[string]*xnode
	// inNote guards a note that refers to itself.
	inNote bool
}

func openDOCX(o *ooxml) (*Document, error) {
	root, err := o.part("word/document.xml")
	if err != nil {
		return nil, err
	}
	c := &docx{o: o, styles: map[string]docxStyle{}, nums: map[string][]docxLevel{},
		media: map[string]string{}}
	c.readStyles()
	c.readNumbering()
	c.readNotes()

	body := root.child("body")
	if body == nil {
		return nil, fmt.Errorf("%w: the document has no body", ErrInvalid)
	}
	c.sheet.WriteString("body{margin:0}\ntable{border-collapse:collapse}\n" +
		"td,th{border:1px solid #999;padding:2px 4px;vertical-align:top}\n")
	c.writeStyles()
	c.body.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml"><head>` +
		`<link rel="stylesheet" type="text/css" href="` + docxSheet + `"/>` +
		`<title>` + esc(c.title()) + `</title></head><body>`)
	c.block(body, 0)
	c.closeLists(0)
	c.body.WriteString("</body></html>")

	d := &Document{kind: KindDOCX}
	d.meta = c.metadata()
	d.natural = c.pageBox(body)
	html, sheet := []byte(c.body.String()), []byte(c.sheet.String())
	d.read = func(p string) ([]byte, error) {
		switch p {
		case docxBody:
			return html, nil
		case docxSheet:
			return sheet, nil
		}
		if _, ok := c.media[p]; ok {
			return o.read(p)
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	d.spine = []Item{{Path: docxBody, Type: "application/xhtml+xml", Linear: true}}
	d.manifest = append([]Item{
		{Path: docxBody, Type: "application/xhtml+xml", Linear: true},
		{Path: docxSheet, Type: "text/css"},
	}, c.mediaItems()...)
	return d, nil
}

func (c *docx) mediaItems() []Item {
	out := make([]Item, 0, len(c.media))
	for p, t := range c.media {
		out = append(out, Item{Path: p, Type: t})
	}
	return out
}

func (c *docx) title() string {
	if v := c.metadata().Title; v != "" {
		return v
	}
	return "Document"
}

// metadata reads what the document says about itself, which lives outside the
// document part.
func (c *docx) metadata() Metadata {
	var m Metadata
	root, err := c.o.part("docProps/core.xml")
	if err != nil {
		return m
	}
	for _, k := range root.kids {
		v := strings.TrimSpace(k.text)
		switch k.name {
		case "title":
			m.Title = v
		case "creator":
			m.Author = v
		case "description":
			m.Description = v
		case "language":
			m.Language = v
		case "identifier":
			m.Identifier = v
		}
	}
	return m
}

// pageBox is the page the section asks for, which a caller may take instead
// of choosing one.
func (c *docx) pageBox(body *xnode) LayoutOptions {
	var o LayoutOptions
	sect := body.child("sectPr")
	if sect == nil {
		return o
	}
	if sz := sect.child("pgSz"); sz != nil {
		o.Width = twips(sz.at("w"))
		o.Height = twips(sz.at("h"))
	}
	if m := sect.child("pgMar"); m != nil {
		o.Margin = twips(m.at("left"))
	}
	return o
}

func twips(v string) float32 {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return float32(round2(n / twipsPerPixel))
}

func (c *docx) readStyles() {
	root, err := c.o.part("word/styles.xml")
	if err != nil {
		return
	}
	if def := root.child("docDefaults"); def != nil {
		if p := def.child("pPrDefault"); p != nil {
			c.dpPr = p.child("pPr")
		}
		if r := def.child("rPrDefault"); r != nil {
			c.drPr = r.child("rPr")
		}
	}
	for _, s := range root.all("style") {
		id := s.at("styleId")
		if id == "" {
			continue
		}
		v := docxStyle{kind: s.at("type"), pPr: s.child("pPr"), rPr: s.child("rPr")}
		v.base, _ = s.val("basedOn")
		v.name, _ = s.val("name")
		v.outline = -1
		if p := v.pPr; p != nil {
			if n, ok := p.num("outlineLvl"); ok {
				v.outline = int(n)
			}
		}
		c.styles[id] = v
	}
}

// readNotes holds the footnotes and endnotes, which are written inline where
// they are referred to.
func (c *docx) readNotes() {
	c.notes = map[string]*xnode{}
	for _, part := range []string{"word/footnotes.xml", "word/endnotes.xml"} {
		root, err := c.o.part(part)
		if err != nil {
			continue
		}
		for _, n := range root.kids {
			switch n.name {
			case "footnote", "endnote":
			default:
				continue
			}
			switch n.at("type") {
			case "separator", "continuationSeparator", "continuationNotice":
				continue
			}
			if id := n.at("id"); id != "" {
				c.notes[n.name+id] = n
			}
		}
	}
}

func (c *docx) readNumbering() {
	root, err := c.o.part("word/numbering.xml")
	if err != nil {
		return
	}
	abstract := map[string][]docxLevel{}
	for _, a := range root.all("abstractNum") {
		id := a.at("abstractNumId")
		var levels []docxLevel
		for _, l := range a.all("lvl") {
			at, _ := strconv.Atoi(l.at("ilvl"))
			for len(levels) <= at && len(levels) < 16 {
				levels = append(levels, docxLevel{format: "bullet", start: 1})
			}
			if at >= len(levels) {
				continue
			}
			v := docxLevel{format: "bullet", start: 1}
			if f, ok := l.val("numFmt"); ok {
				v.format = f
			}
			if n, ok := l.num("start"); ok {
				v.start = int(n)
			}
			levels[at] = v
		}
		abstract[id] = levels
	}
	for _, n := range root.all("num") {
		id := n.at("numId")
		if a, ok := n.val("abstractNumId"); ok {
			if levels, ok := abstract[a]; ok && len(c.nums) < docxMaxLists {
				c.nums[id] = levels
			}
		}
	}
}

// writeStyles turns the document's own styles into the classes the generated
// markup names.
func (c *docx) writeStyles() {
	if c.dpPr != nil || c.drPr != nil {
		if v := c.rules(c.dpPr, c.drPr); v != "" {
			fmt.Fprintf(&c.sheet, "body{%s}\n", v)
		}
	}
	for id, s := range c.styles {
		if s.kind != "paragraph" && s.kind != "character" && s.kind != "table" {
			continue
		}
		if v := c.rules(s.pPr, s.rPr); v != "" {
			fmt.Fprintf(&c.sheet, ".%s{%s}\n", cssClass(id), v)
		}
	}
}

// rules is the CSS one pair of paragraph and run properties comes to.
func (c *docx) rules(pPr, rPr *xnode) string {
	var out []string
	if pPr != nil {
		switch v, _ := pPr.val("jc"); v {
		case "center":
			out = append(out, "text-align:center")
		case "right", "end":
			out = append(out, "text-align:right")
		case "both", "distribute":
			out = append(out, "text-align:justify")
		case "left", "start":
			out = append(out, "text-align:left")
		}
		if s := pPr.child("spacing"); s != nil {
			if v := twips(s.at("before")); v > 0 {
				out = append(out, "margin-top:"+px(float64(v))+"px")
			}
			if v := twips(s.at("after")); v > 0 {
				out = append(out, "margin-bottom:"+px(float64(v))+"px")
			}
			if s.at("lineRule") == "auto" {
				if n, err := strconv.ParseFloat(s.at("line"), 64); err == nil && n > 0 {
					out = append(out, "line-height:"+px(round2(n/240)))
				}
			}
		}
		if i := pPr.child("ind"); i != nil {
			if v := twips(i.at("left")); v > 0 {
				out = append(out, "margin-left:"+px(float64(v))+"px")
			}
			if v := twips(i.at("firstLine")); v > 0 {
				out = append(out, "text-indent:"+px(float64(v))+"px")
			}
		}
	}
	if rPr != nil {
		if rPr.on("b") {
			out = append(out, "font-weight:bold")
		}
		if rPr.on("i") {
			out = append(out, "font-style:italic")
		}
		switch v, ok := rPr.val("u"); {
		case ok && v != "none":
			out = append(out, "text-decoration:underline")
		}
		if rPr.on("strike") {
			out = append(out, "text-decoration:line-through")
		}
		if v, ok := rPr.val("color"); ok {
			if col := ooxmlColor(v); col != "" {
				out = append(out, "color:"+col)
			}
		}
		if v, ok := rPr.val("highlight"); ok && v != "none" {
			out = append(out, "background-color:"+v)
		}
		if n, ok := rPr.num("sz"); ok && n > 0 {
			out = append(out, "font-size:"+px(round2(n/2*4/3))+"px")
		}
		if f := rPr.child("rFonts"); f != nil {
			if v := f.at("ascii"); v != "" {
				out = append(out, "font-family:"+cssFamily(v))
			}
		}
		if v, ok := rPr.val("vertAlign"); ok {
			switch v {
			case "superscript":
				out = append(out, "vertical-align:super", "font-size:smaller")
			case "subscript":
				out = append(out, "vertical-align:sub", "font-size:smaller")
			}
		}
		if rPr.on("caps") {
			out = append(out, "text-transform:uppercase")
		}
		if rPr.on("smallCaps") {
			out = append(out, "font-variant:small-caps")
		}
	}
	return strings.Join(out, ";")
}

// block writes the paragraphs and tables of one container.
func (c *docx) block(n *xnode, depth int) {
	if depth > ooxmlMaxDepth {
		return
	}
	for _, k := range n.kids {
		switch k.name {
		case "p":
			c.para(k, depth)
		case "tbl":
			c.closeLists(0)
			c.table(k, depth)
		case "sdt":
			if v := k.child("sdtContent"); v != nil {
				c.block(v, depth+1)
			}
		}
	}
}

func (c *docx) table(n *xnode, depth int) {
	c.body.WriteString("<table")
	if v, ok := n.child("tblPr").val("tblStyle"); ok {
		c.body.WriteString(` class="` + cssClass(v) + `"`)
	}
	c.body.WriteString(">")
	for _, r := range n.all("tr") {
		c.body.WriteString("<tr>")
		for _, cell := range r.all("tc") {
			pr := cell.child("tcPr")
			c.body.WriteString("<td")
			if span, ok := pr.val("gridSpan"); ok && span != "1" {
				c.body.WriteString(` colspan="` + esc(span) + `"`)
			}
			if v := c.cellStyle(pr); v != "" {
				c.body.WriteString(` style="` + v + `"`)
			}
			c.body.WriteString(">")
			c.block(cell, depth+1)
			c.closeLists(0)
			c.body.WriteString("</td>")
		}
		c.body.WriteString("</tr>")
	}
	c.body.WriteString("</table>")
}

func (c *docx) cellStyle(pr *xnode) string {
	if pr == nil {
		return ""
	}
	var out []string
	if s := pr.child("shd"); s != nil {
		if v := ooxmlColor(s.at("fill")); v != "" {
			out = append(out, "background-color:"+v)
		}
	}
	if w := pr.child("tcW"); w != nil && w.at("type") == "dxa" {
		if v := twips(w.at("w")); v > 0 {
			out = append(out, "width:"+px(float64(v))+"px")
		}
	}
	if v, ok := pr.val("vAlign"); ok {
		switch v {
		case "center":
			out = append(out, "vertical-align:middle")
		case "bottom":
			out = append(out, "vertical-align:bottom")
		}
	}
	return strings.Join(out, ";")
}

// para writes one paragraph, opening and closing the lists it belongs to.
func (c *docx) para(n *xnode, depth int) {
	pPr := n.child("pPr")
	style, _ := pPr.val("pStyle")
	level, list := c.listOf(pPr)
	if list {
		c.openLists(level, c.nums[numID(pPr)])
		c.body.WriteString("<li")
	} else {
		c.closeLists(0)
		c.body.WriteString("<" + c.tagOf(style))
	}
	if style != "" {
		c.body.WriteString(` class="` + cssClass(style) + `"`)
	}
	if v := c.rules(pPr, nil); v != "" {
		c.body.WriteString(` style="` + v + `"`)
	}
	c.body.WriteString(">")
	c.runs(n, depth)
	if list {
		c.body.WriteString("</li>")
		return
	}
	c.body.WriteString("</" + c.tagOf(style) + ">")
}

func numID(pPr *xnode) string {
	if pPr == nil {
		return ""
	}
	if p := pPr.child("numPr"); p != nil {
		v, _ := p.val("numId")
		return v
	}
	return ""
}

// listOf is which level of a list a paragraph sits at, and whether it is in
// one at all.
func (c *docx) listOf(pPr *xnode) (int, bool) {
	if pPr == nil {
		return 0, false
	}
	p := pPr.child("numPr")
	if p == nil {
		return 0, false
	}
	id, ok := p.val("numId")
	if !ok || id == "0" {
		return 0, false
	}
	if _, known := c.nums[id]; !known {
		return 0, false
	}
	level := 0
	if n, ok := p.num("ilvl"); ok {
		level = int(n)
	}
	if level < 0 || level > 8 {
		level = 0
	}
	return level, true
}

func (c *docx) openLists(level int, levels []docxLevel) {
	c.closeLists(level + 1)
	for len(c.lists) <= level {
		at := len(c.lists)
		tag, start := "ul", 0
		if at < len(levels) && levels[at].format != "bullet" && levels[at].format != "none" {
			tag = "ol"
			start = levels[at].start
		}
		c.body.WriteString("<" + tag)
		if tag == "ol" {
			if s := listStyle(levels[at].format); s != "" {
				c.body.WriteString(` style="list-style-type:` + s + `"`)
			}
			if start > 1 {
				c.body.WriteString(` start="` + strconv.Itoa(start) + `"`)
			}
		}
		c.body.WriteString(">")
		c.lists = append(c.lists, boolToTag(tag))
	}
}

func (c *docx) closeLists(to int) {
	for len(c.lists) > to {
		tag := "ul"
		if c.lists[len(c.lists)-1] == 1 {
			tag = "ol"
		}
		c.body.WriteString("</" + tag + ">")
		c.lists = c.lists[:len(c.lists)-1]
	}
}

func boolToTag(tag string) int {
	if tag == "ol" {
		return 1
	}
	return 0
}

func listStyle(format string) string {
	switch format {
	case "decimal":
		return "decimal"
	case "lowerLetter":
		return "lower-alpha"
	case "upperLetter":
		return "upper-alpha"
	case "lowerRoman":
		return "lower-roman"
	case "upperRoman":
		return "upper-roman"
	}
	return ""
}

// tagOf is the element a paragraph style is written as.
func (c *docx) tagOf(style string) string {
	for i, s := 0, style; s != "" && i < ooxmlMaxDepth; i++ {
		v, ok := c.styles[s]
		if !ok {
			break
		}
		if n := headingLevel(v.name, v.outline); n > 0 {
			return "h" + strconv.Itoa(n)
		}
		s = v.base
	}
	return "p"
}

func headingLevel(name string, outline int) int {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "heading ") {
		if n, err := strconv.Atoi(strings.TrimSpace(name[8:])); err == nil && n >= 1 && n <= 6 {
			return n
		}
	}
	if outline >= 0 && outline <= 5 {
		return outline + 1
	}
	return 0
}

// runs writes the content of one paragraph.
func (c *docx) runs(n *xnode, depth int) {
	if depth > ooxmlMaxDepth {
		return
	}
	for _, k := range n.kids {
		switch k.name {
		case "r":
			c.run(k)
		case "hyperlink":
			href := c.o.external("word/document.xml", k.rel("id"))
			if href == "" {
				if v := k.at("anchor"); v != "" {
					href = "#" + v
				}
			}
			if href == "" {
				c.runs(k, depth+1)
				continue
			}
			c.body.WriteString(`<a href="` + esc(href) + `">`)
			c.runs(k, depth+1)
			c.body.WriteString("</a>")
		case "bookmarkStart":
			if v := k.at("name"); v != "" && v != "_GoBack" {
				c.body.WriteString(`<a id="` + esc(v) + `"></a>`)
			}
		case "smartTag", "ins", "sdt", "sdtContent":
			c.runs(k, depth+1)
		}
	}
}

// note writes what a footnote or an endnote says, where it is referred to.
func (c *docx) note(kind, id string, depth int) {
	n := c.notes[kind+id]
	if n == nil || c.inNote || depth > ooxmlMaxDepth {
		return
	}
	c.inNote = true
	defer func() { c.inNote = false }()
	c.body.WriteString(`<span class="note">`)
	for _, p := range n.all("p") {
		c.runs(p, depth+1)
	}
	c.body.WriteString("</span>")
}

func (c *docx) run(r *xnode) {
	rPr := r.child("rPr")
	style, _ := rPr.val("rStyle")
	open, shut := "", ""
	if style != "" || c.rules(nil, rPr) != "" {
		open = "<span"
		if style != "" {
			open += ` class="` + cssClass(style) + `"`
		}
		if v := c.rules(nil, rPr); v != "" {
			open += ` style="` + v + `"`
		}
		open += ">"
		shut = "</span>"
	}
	// A run the file marks up is written as the element that means it, so
	// that what comes back out is a document rather than a colour.
	for _, e := range c.emphasis(rPr, style) {
		open += "<" + e + ">"
		shut = "</" + e + ">" + shut
	}
	var body strings.Builder
	var notes [][2]string
	for _, k := range r.kids {
		switch k.name {
		case "t":
			body.WriteString(esc(k.text))
		case "delText", "rPr":
		case "tab":
			body.WriteString("&#9;")
		case "br":
			if k.at("type") == "page" {
				body.WriteString(`<span style="page-break-after:always"></span>`)
				continue
			}
			body.WriteString("<br/>")
		case "cr":
			body.WriteString("<br/>")
		case "noBreakHyphen":
			body.WriteString("-")
		case "softHyphen":
			body.WriteString("&#173;")
		case "sym":
			body.WriteString(symbol(k.at("char")))
		case "lastRenderedPageBreak":
		case "drawing", "pict", "object":
			body.WriteString(c.picture(k, 0))
		case "footnoteReference":
			notes = append(notes, [2]string{"footnote", k.at("id")})
		case "endnoteReference":
			notes = append(notes, [2]string{"endnote", k.at("id")})
		}
	}
	if body.Len() > 0 {
		c.body.WriteString(open + body.String() + shut)
	}
	for _, v := range notes {
		c.note(v[0], v[1], 0)
	}
}

// monoStyles are the character styles a generator marks code with, and
// monoFamilies the faces it sets it in.
var (
	monoStyles = map[string]bool{
		"verbatimchar": true, "sourcecode": true, "code": true, "htmlcode": true,
	}
	monoFamilies = map[string]bool{
		"courier": true, "courier new": true, "consolas": true, "menlo": true,
		"monaco": true, "dejavu sans mono": true, "liberation mono": true,
		"lucida console": true, "andale mono": true,
	}
)

// emphasis is the elements a run's properties and its style mean, innermost
// last.
func (c *docx) emphasis(rPr *xnode, style string) []string {
	var out []string
	if c.monospaced(rPr, style, 0) {
		out = append(out, "code")
	}
	if rPr == nil {
		return out
	}
	if rPr.on("b") {
		out = append(out, "strong")
	}
	if rPr.on("i") {
		out = append(out, "em")
	}
	if rPr.on("strike") || rPr.on("dstrike") {
		out = append(out, "s")
	}
	if v, ok := rPr.val("vertAlign"); ok {
		switch v {
		case "superscript":
			out = append(out, "sup")
		case "subscript":
			out = append(out, "sub")
		}
	}
	return out
}

// monospaced reports a run written as code, which a style names or a face
// says.
func (c *docx) monospaced(rPr *xnode, style string, depth int) bool {
	if depth > ooxmlMaxDepth {
		return false
	}
	if monoStyles[strings.ToLower(style)] {
		return true
	}
	if rPr != nil {
		if f := rPr.child("rFonts"); f != nil {
			if monoFamilies[strings.ToLower(f.at("ascii"))] {
				return true
			}
		}
	}
	if style == "" {
		return false
	}
	v, ok := c.styles[style]
	if !ok {
		return false
	}
	return c.monospaced(v.rPr, v.base, depth+1)
}

func symbol(code string) string {
	n, err := strconv.ParseInt(strings.TrimPrefix(code, "F0"), 16, 32)
	if err != nil || n <= 0 {
		return ""
	}
	return "&#" + strconv.FormatInt(n, 10) + ";"
}

// picture writes the image a drawing holds, at the size it declares.
func (c *docx) picture(n *xnode, depth int) string {
	id := findRel(n, 0)
	path := c.o.target("word/document.xml", id)
	if path == "" || !c.o.has(path) {
		return ""
	}
	c.media[path] = chmType(path)
	return `<img src="` + esc(path) + `"` + extent(n, 0) + "/>"
}

// findRel is the picture a drawing holds, which sits some way inside it.
func findRel(n *xnode, depth int) string {
	if depth > ooxmlMaxDepth {
		return ""
	}
	if n.name == "blip" || n.name == "imagedata" {
		if id := n.rel("embed"); id != "" {
			return id
		}
		return n.rel("id")
	}
	for _, k := range n.kids {
		if v := findRel(k, depth+1); v != "" {
			return v
		}
	}
	return ""
}

// extent is the size a drawing declares, which is written on the anchor
// around the picture rather than on the picture itself.
func extent(n *xnode, depth int) string {
	if depth > ooxmlMaxDepth {
		return ""
	}
	if n.name == "extent" {
		w, h := emu(n.at("cx")), emu(n.at("cy"))
		if w > 0 && h > 0 {
			return ` width="` + px(w) + `" height="` + px(h) + `"`
		}
		return ""
	}
	for _, k := range n.kids {
		if v := extent(k, depth+1); v != "" {
			return v
		}
	}
	return ""
}

func emu(v string) float64 {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return round2(n / emuPerPixel)
}

// cssClass is the name a style identifier is written as, which has to be one
// a selector may carry.
func cssClass(id string) string {
	var b strings.Builder
	b.WriteString("s-")
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func cssFamily(name string) string {
	if strings.ContainsAny(name, " ;:'\"") {
		return "'" + strings.ReplaceAll(name, "'", "") + "'"
	}
	return name
}
