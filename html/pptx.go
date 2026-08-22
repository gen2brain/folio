package html

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	pptxSheet    = "slides.css"
	pptxMaxSlide = 1 << 12
	// pptxPoint is how many hundredths of a point one CSS pixel is.
	pptxPoint = 75
)

// pptxFrame is where a shape sits on the slide and how big it is.
type pptxFrame struct {
	x, y, w, h float64
	ok         bool
}

// pptxPlace is what a layout or a master says about one kind of placeholder.
type pptxPlace struct {
	frame pptxFrame
	size  float64
	align string
	bold  bool
}

type pptx struct {
	o *ooxml
	// slides are the parts the presentation lists, in order.
	slides []string
	w, h   float64
	// place is the geometry a placeholder inherits, by the layout part it
	// belongs to and the kind and index it carries.
	place map[string]map[string]pptxPlace
	media map[string]string
	pages map[string][]byte
	sheet []byte
}

func openPPTX(o *ooxml) (*Document, error) {
	root, err := o.part("ppt/presentation.xml")
	if err != nil {
		return nil, err
	}
	c := &pptx{o: o, w: 960, h: 720, place: map[string]map[string]pptxPlace{},
		media: map[string]string{}, pages: map[string][]byte{}}
	if sz := root.child("sldSz"); sz != nil {
		if v := emu(sz.at("cx")); v > 0 {
			c.w = v
		}
		if v := emu(sz.at("cy")); v > 0 {
			c.h = v
		}
	}
	if lst := root.child("sldIdLst"); lst != nil {
		for _, s := range lst.all("sldId") {
			if p := o.target("ppt/presentation.xml", s.rel("id")); p != "" && o.has(p) {
				c.slides = append(c.slides, p)
			}
			if len(c.slides) >= pptxMaxSlide {
				break
			}
		}
	}
	if len(c.slides) == 0 {
		return nil, fmt.Errorf("%w: the presentation has no slides", ErrInvalid)
	}

	c.sheet = []byte(fmt.Sprintf("body{margin:0;padding:0}\n"+
		".slide{position:relative;width:%spx;height:%spx;overflow:hidden;"+
		"page-break-after:always}\n"+
		".shape{position:absolute}\n.shape p{margin:0}\n", px(c.w), px(c.h)))

	d := &Document{kind: KindPPTX}
	d.natural = LayoutOptions{Width: float32(c.w), Height: float32(c.h)}
	d.meta = c.metadata()
	for i, part := range c.slides {
		name := slideName(i)
		c.pages[name] = c.slide(part)
		item := Item{Path: name, Type: "application/xhtml+xml", Linear: true}
		d.spine = append(d.spine, item)
		d.manifest = append(d.manifest, item)
		d.outline = append(d.outline, Outline{Title: c.titleOf(part, i), Path: name})
	}
	d.manifest = append(d.manifest, Item{Path: pptxSheet, Type: "text/css"})
	for p, t := range c.media {
		d.manifest = append(d.manifest, Item{Path: p, Type: t})
	}
	d.read = func(p string) ([]byte, error) {
		if b, ok := c.pages[p]; ok {
			return b, nil
		}
		if p == pptxSheet {
			return c.sheet, nil
		}
		if _, ok := c.media[p]; ok {
			return o.read(p)
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d, nil
}

func slideName(i int) string { return fmt.Sprintf("slide%d.xhtml", i+1) }

func (c *pptx) metadata() Metadata {
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
		}
	}
	return m
}

// titleOf is what the slide calls itself, and its number otherwise.
func (c *pptx) titleOf(part string, i int) string {
	root, err := c.o.part(part)
	if err == nil {
		if tree := shapeTree(root); tree != nil {
			for _, sp := range tree.all("sp") {
				if kind, _ := placeholder(sp); strings.Contains(kind, "title") {
					if v := strings.TrimSpace(shapeText(sp)); v != "" {
						return v
					}
				}
			}
		}
	}
	return "Slide " + strconv.Itoa(i+1)
}

// slide writes one slide as a page of boxes placed where the shape tree says.
func (c *pptx) slide(part string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml"><head>` +
		`<link rel="stylesheet" type="text/css" href="` + pptxSheet + `"/>` +
		`</head><body><div class="slide">`)
	root, err := c.o.part(part)
	if err == nil {
		if tree := shapeTree(root); tree != nil {
			c.shapes(&b, tree, part, 0)
		}
	}
	b.WriteString("</div></body></html>")
	return []byte(b.String())
}

func shapeTree(root *xnode) *xnode {
	if s := root.child("cSld"); s != nil {
		return s.child("spTree")
	}
	return nil
}

func (c *pptx) shapes(b *strings.Builder, tree *xnode, part string, depth int) {
	if depth > ooxmlMaxDepth {
		return
	}
	for _, sp := range tree.kids {
		switch sp.name {
		case "sp":
			c.shape(b, sp, part)
		case "pic":
			c.pic(b, sp, part)
		case "grpSp":
			c.shapes(b, sp, part, depth+1)
		case "graphicFrame":
			c.graphic(b, sp, part)
		}
	}
}

func (c *pptx) shape(b *strings.Builder, sp *xnode, part string) {
	body := sp.child("txBody")
	if body == nil {
		return
	}
	kind, idx := placeholder(sp)
	from := c.inherited(part, kind, idx)
	f := frameOf(sp)
	if !f.ok {
		f = from.frame
	}
	if !f.ok {
		return
	}
	var text strings.Builder
	for _, p := range body.all("p") {
		c.para(&text, p, from)
	}
	if strings.TrimSpace(stripTags(text.String())) == "" {
		return
	}
	fmt.Fprintf(b, `<div class="shape" style="left:%spx;top:%spx;width:%spx;height:%spx">`,
		px(f.x), px(f.y), px(f.w), px(f.h))
	b.WriteString(text.String())
	b.WriteString("</div>")
}

func (c *pptx) pic(b *strings.Builder, sp *xnode, part string) {
	id := findRel(sp, 0)
	path := c.o.target(part, id)
	if path == "" || !c.o.has(path) {
		return
	}
	f := frameOf(sp)
	if !f.ok {
		return
	}
	c.media[path] = chmType(path)
	fmt.Fprintf(b, `<div class="shape" style="left:%spx;top:%spx;width:%spx;height:%spx">`+
		`<img src="%s" width="%s" height="%s"/></div>`,
		px(f.x), px(f.y), px(f.w), px(f.h), esc(path), px(f.w), px(f.h))
}

// graphic writes the table a graphic frame may hold, which is the one part of
// DrawingML a page of text has any use for.
func (c *pptx) graphic(b *strings.Builder, sp *xnode, part string) {
	f := frameOf(sp)
	tbl := findChild(sp, "tbl", 0)
	if !f.ok || tbl == nil {
		return
	}
	fmt.Fprintf(b, `<div class="shape" style="left:%spx;top:%spx;width:%spx">`+
		`<table style="border-collapse:collapse;width:100%%">`, px(f.x), px(f.y), px(f.w))
	for _, tr := range tbl.all("tr") {
		b.WriteString("<tr>")
		for _, tc := range tr.all("tc") {
			b.WriteString(`<td style="border:1px solid #999;padding:2px 4px">`)
			if body := tc.child("txBody"); body != nil {
				for _, p := range body.all("p") {
					c.para(b, p, pptxPlace{})
				}
			}
			b.WriteString("</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table></div>")
}

// para writes one paragraph of a text body, at the level it is nested to.
func (c *pptx) para(b *strings.Builder, p *xnode, from pptxPlace) {
	pPr := p.child("pPr")
	var style []string
	size, align, bold := from.size, from.align, from.bold
	if pPr != nil {
		if v := pPr.at("algn"); v != "" {
			align = v
		}
		if n, err := strconv.ParseFloat(pPr.at("marL"), 64); err == nil && n > 0 {
			style = append(style, "margin-left:"+px(round2(n/emuPerPixel))+"px")
		}
		if n, err := strconv.Atoi(pPr.at("lvl")); err == nil && n > 0 {
			style = append(style, "margin-left:"+px(float64(n*24))+"px")
		}
	}
	switch align {
	case "ctr":
		style = append(style, "text-align:center")
	case "r":
		style = append(style, "text-align:right")
	case "just":
		style = append(style, "text-align:justify")
	}
	if size > 0 {
		style = append(style, "font-size:"+px(size)+"px")
	}
	if bold {
		style = append(style, "font-weight:bold")
	}
	b.WriteString("<p")
	if len(style) > 0 {
		b.WriteString(` style="` + strings.Join(style, ";") + `"`)
	}
	b.WriteString(">")
	empty := true
	for _, r := range p.kids {
		switch r.name {
		case "r":
			b.WriteString(runSpan(r))
			empty = false
		case "br":
			b.WriteString("<br/>")
		case "fld":
			if t := r.child("t"); t != nil {
				b.WriteString(esc(t.text))
				empty = false
			}
		}
	}
	if empty {
		b.WriteString("&#160;")
	}
	b.WriteString("</p>")
}

func runSpan(r *xnode) string {
	t := r.child("t")
	if t == nil || t.text == "" {
		return ""
	}
	var style []string
	if pr := r.child("rPr"); pr != nil {
		if n, err := strconv.ParseFloat(pr.at("sz"), 64); err == nil && n > 0 {
			style = append(style, "font-size:"+px(round2(n/pptxPoint))+"px")
		}
		if pr.at("b") == "1" {
			style = append(style, "font-weight:bold")
		}
		if pr.at("i") == "1" {
			style = append(style, "font-style:italic")
		}
		if pr.at("u") != "" && pr.at("u") != "none" {
			style = append(style, "text-decoration:underline")
		}
		if fill := pr.child("solidFill"); fill != nil {
			if s := fill.child("srgbClr"); s != nil {
				if v := ooxmlColor(s.at("val")); v != "" {
					style = append(style, "color:"+v)
				}
			}
		}
		if f := pr.child("latin"); f != nil {
			if v := f.at("typeface"); v != "" {
				style = append(style, "font-family:"+cssFamily(v))
			}
		}
	}
	if len(style) == 0 {
		return esc(t.text)
	}
	return `<span style="` + strings.Join(style, ";") + `">` + esc(t.text) + "</span>"
}

// placeholder is the kind and the index a shape stands in for, which is how
// the layout beneath it is looked up.
func placeholder(sp *xnode) (string, string) {
	nv := sp.child("nvSpPr")
	if nv == nil {
		return "", ""
	}
	pr := nv.child("nvPr")
	if pr == nil {
		return "", ""
	}
	ph := pr.child("ph")
	if ph == nil {
		return "", ""
	}
	kind := ph.at("type")
	if kind == "" {
		kind = "body"
	}
	return kind, ph.at("idx")
}

// inherited is what the layout and the master under a slide say about a
// placeholder, which is where most of a real slide's geometry lives.
func (c *pptx) inherited(part, kind, idx string) pptxPlace {
	var out pptxPlace
	if kind == "" {
		return out
	}
	for i, at := 0, part; i < 3 && at != ""; i++ {
		at = c.parentOf(at)
		if at == "" {
			break
		}
		m := c.layout(at)
		v, ok := m[kind+"/"+idx]
		if !ok {
			v, ok = m[kind+"/"]
		}
		if !ok {
			continue
		}
		if !out.frame.ok {
			out.frame = v.frame
		}
		if out.size == 0 {
			out.size = v.size
		}
		if out.align == "" {
			out.align = v.align
		}
		out.bold = out.bold || v.bold
	}
	return out
}

// parentOf is the layout a slide is drawn on, or the master a layout is.
func (c *pptx) parentOf(part string) string {
	dir, base := splitPart(part)
	root, err := c.o.part(dir + "_rels/" + base + ".rels")
	if err != nil {
		return ""
	}
	for _, r := range root.all("Relationship") {
		t := r.at("Type")
		if strings.HasSuffix(t, "/slideLayout") || strings.HasSuffix(t, "/slideMaster") {
			return joinPart(dir, r.at("Target"))
		}
	}
	return ""
}

// layout is what one layout or master says about each placeholder on it.
func (c *pptx) layout(part string) map[string]pptxPlace {
	if v, ok := c.place[part]; ok {
		return v
	}
	m := map[string]pptxPlace{}
	if root, err := c.o.part(part); err == nil {
		if tree := shapeTree(root); tree != nil {
			for _, sp := range tree.all("sp") {
				kind, idx := placeholder(sp)
				if kind == "" {
					continue
				}
				v := pptxPlace{frame: frameOf(sp)}
				if body := sp.child("txBody"); body != nil {
					v.size, v.align, v.bold = defaultRun(body)
				}
				m[kind+"/"+idx] = v
			}
		}
	}
	c.place[part] = m
	return m
}

// defaultRun is the type a placeholder sets for the text put into it.
func defaultRun(body *xnode) (size float64, align string, bold bool) {
	lst := body.child("lstStyle")
	if lst == nil {
		return 0, "", false
	}
	lvl := lst.child("lvl1pPr")
	if lvl == nil {
		return 0, "", false
	}
	align = lvl.at("algn")
	if d := lvl.child("defRPr"); d != nil {
		if n, err := strconv.ParseFloat(d.at("sz"), 64); err == nil && n > 0 {
			size = round2(n / pptxPoint)
		}
		bold = d.at("b") == "1"
	}
	return size, align, bold
}

// frameOf is the rectangle a shape declares, and nothing when it declares
// none and takes the one its placeholder has.
func frameOf(sp *xnode) pptxFrame {
	x := findChild(sp, "xfrm", 0)
	if x == nil {
		return pptxFrame{}
	}
	off, ext := x.child("off"), x.child("ext")
	if off == nil || ext == nil {
		return pptxFrame{}
	}
	w, h := emu(ext.at("cx")), emu(ext.at("cy"))
	if w <= 0 || h <= 0 {
		return pptxFrame{}
	}
	return pptxFrame{x: emuSigned(off.at("x")), y: emuSigned(off.at("y")), w: w, h: h, ok: true}
}

func emuSigned(v string) float64 {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return round2(n / emuPerPixel)
}

func findChild(n *xnode, name string, depth int) *xnode {
	if depth > ooxmlMaxDepth {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
		if v := findChild(k, name, depth+1); v != nil {
			return v
		}
	}
	return nil
}

// shapeText is everything one shape says, which the outline reads.
func shapeText(sp *xnode) string {
	body := sp.child("txBody")
	if body == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range body.all("p") {
		for _, r := range p.all("r") {
			if t := r.child("t"); t != nil {
				b.WriteString(t.text)
			}
		}
		b.WriteString(" ")
	}
	return b.String()
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}
