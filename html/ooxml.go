package html

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// relNS is the namespace a part names another part through.
	relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	// emuPerPixel is how many English metric units one CSS pixel is.
	emuPerPixel = 9525
	// twipsPerPixel is how many twentieths of a point one CSS pixel is.
	twipsPerPixel = 15
	ooxmlMaxNodes = 1 << 21
	ooxmlMaxDepth = 256
)

// xnode is one element of an OOXML part. The parts are small enough to hold
// whole and irregular enough that a token walk reads worse than a tree does.
type xnode struct {
	name string
	attr []xml.Attr
	kids []*xnode
	text string
}

func parseXML(b []byte) (*xnode, error) { return parseTree(b, false) }

// parseMixed keeps the character data as children in the order it was
// written, which a document with text beside its elements needs.
func parseMixed(b []byte) (*xnode, error) { return parseTree(b, true) }

func parseTree(b []byte, mixed bool) (*xnode, error) {
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root := &xnode{}
	stack := []*xnode{root}
	nodes := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		switch e := t.(type) {
		case xml.StartElement:
			if nodes++; nodes > ooxmlMaxNodes || len(stack) > ooxmlMaxDepth {
				return nil, fmt.Errorf("%w: the part is too deep or too large", ErrUnsupported)
			}
			n := &xnode{name: e.Name.Local, attr: e.Attr}
			top := stack[len(stack)-1]
			top.kids = append(top.kids, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			top := stack[len(stack)-1]
			top.text += string(e)
			if mixed && len(e) > 0 {
				top.kids = append(top.kids, &xnode{text: string(e)})
			}
		}
	}
	for _, k := range root.kids {
		if k.name != "" {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: the part has no root element", ErrInvalid)
}

// at is the value of an attribute of any namespace but the relationship one,
// which rel reads instead.
func (n *xnode) at(name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.attr {
		if a.Name.Local == name && a.Name.Space != relNS {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) rel(name string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.attr {
		if a.Name.Local == name && (a.Name.Space == relNS || a.Name.Space == "r") {
			return a.Value
		}
	}
	return ""
}

// val is the w:val of a child element, and "" when there is no such child. An
// element written for its presence alone answers "on".
func (n *xnode) val(name string) (string, bool) {
	c := n.child(name)
	if c == nil {
		return "", false
	}
	if v := c.at("val"); v != "" {
		return v, true
	}
	return "on", true
}

func (n *xnode) child(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *xnode) all(name string) []*xnode {
	if n == nil {
		return nil
	}
	var out []*xnode
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

// on reports a toggle, which is set unless it says otherwise.
func (n *xnode) on(name string) bool {
	v, ok := n.val(name)
	if !ok {
		return false
	}
	switch v {
	case "0", "false", "off":
		return false
	}
	return true
}

func (n *xnode) num(name string) (float64, bool) {
	v, ok := n.val(name)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// ooxml is the archive an office document is, with the parts it has been
// asked for held.
type ooxml struct {
	files map[string]*zip.File
	rels  map[string]map[string]string
}

func (o *ooxml) has(name string) bool { return o.files[name] != nil }

func (o *ooxml) read(name string) ([]byte, error) {
	f := o.files[name]
	if f == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxPartBytes))
}

func (o *ooxml) part(name string) (*xnode, error) {
	b, err := o.read(name)
	if err != nil {
		return nil, err
	}
	return parseXML(b)
}

// target is the part a relationship identifier names, read against the
// directory the part naming it lives in.
func (o *ooxml) target(part, id string) string {
	if id == "" {
		return ""
	}
	m, ok := o.rels[part]
	if !ok {
		m = map[string]string{}
		dir, base := splitPart(part)
		if root, err := o.part(dir + "_rels/" + base + ".rels"); err == nil {
			for _, r := range root.all("Relationship") {
				if t := r.at("Target"); t != "" && r.at("TargetMode") != "External" {
					m[r.at("Id")] = joinPart(dir, t)
				}
			}
		}
		if o.rels == nil {
			o.rels = map[string]map[string]string{}
		}
		o.rels[part] = m
	}
	return m[id]
}

// external is the address a relationship points outside the archive at.
func (o *ooxml) external(part, id string) string {
	dir, base := splitPart(part)
	root, err := o.part(dir + "_rels/" + base + ".rels")
	if err != nil {
		return ""
	}
	for _, r := range root.all("Relationship") {
		if r.at("Id") == id && r.at("TargetMode") == "External" {
			return r.at("Target")
		}
	}
	return ""
}

func pathExt(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[i:]
	}
	return ""
}

func splitPart(p string) (dir, base string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1], p[i+1:]
	}
	return "", p
}

func joinPart(dir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return cleanPart(dir + target)
}

func cleanPart(p string) string {
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, v := range parts {
		switch v {
		case ".", "":
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, v)
		}
	}
	return strings.Join(out, "/")
}

// openZip decides which of the four containers a zip is and opens it.
func openZip(r io.ReaderAt, size int64) (*Document, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	files := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		if _, dup := files[f.Name]; !dup {
			files[f.Name] = f
		}
	}
	o := &ooxml{files: files}
	switch {
	case o.has("word/document.xml"):
		return openDOCX(o)
	case o.has("ppt/presentation.xml"):
		return openPPTX(o)
	case o.has("xl/workbook.xml"):
		return openXLSX(o)
	}
	// A FictionBook is sometimes zipped, one book to an archive.
	if !o.has("META-INF/container.xml") {
		for _, f := range z.File {
			if !strings.EqualFold(pathExt(f.Name), ".fb2") {
				continue
			}
			b, err := o.read(f.Name)
			if err != nil {
				break
			}
			return openFB2(b)
		}
	}
	return openEPUB(z, files)
}

// esc writes text as the markup a generated part carries it as.
func esc(s string) string {
	if !strings.ContainsAny(s, "&<>\"") {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// px turns a length into the CSS one, rounded to a hundredth.
func px(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// ooxmlColor is a colour an office part writes as six hex digits, and "" for
// the one that means the reader chooses.
func ooxmlColor(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "#")
	if len(v) != 6 || strings.EqualFold(v, "auto") {
		return ""
	}
	for _, r := range v {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return ""
		}
	}
	return "#" + strings.ToLower(v)
}
