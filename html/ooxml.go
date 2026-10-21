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
	// emuPerPixel is the English metric units in one CSS pixel.
	emuPerPixel = 9525
	// twipsPerPixel is the twentieths of a point in one CSS pixel.
	twipsPerPixel = 15
	ooxmlMaxNodes = 1 << 21
	ooxmlMaxDepth = 256
	// ooxmlMaxOutput bounds the HTML a whole document is turned into.
	ooxmlMaxOutput = 1 << 28
)

// xnode is one element of an OOXML part.
type xnode struct {
	name string
	attr []xml.Attr
	kids []*xnode
	text string
	buf  []byte
}

func parseXML(b []byte) (*xnode, error) { return parseTree(b, false) }

// parseMixed parses a part keeping character data as children in order.
func parseMixed(b []byte) (*xnode, error) { return parseTree(b, true) }

func parseTree(b []byte, mixed bool) (*xnode, error) {
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	root := &xnode{}
	stack := []*xnode{root}
	nodes := 0
	var texts []*xnode
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
			if top.buf == nil {
				texts = append(texts, top)
			}
			top.buf = append(top.buf, e...)
			if mixed && len(e) > 0 {
				top.kids = append(top.kids, &xnode{text: string(e)})
			}
		}
	}
	for _, n := range texts {
		n.text, n.buf = string(n.buf), nil
	}
	for _, k := range root.kids {
		if k.name != "" {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: the part has no root element", ErrInvalid)
}

// at is the value of an attribute outside the relationship namespace.
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

// val is the w:val of a child element, "on" for one with no w:val.
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

// on reports whether a toggle is set.
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

// ooxml is the archive an office document is, holding the parts asked for.
type ooxml struct {
	files map[string]*zip.File
	rels  map[string]map[string]string
	ext   map[string]map[string]string
	// spent is the HTML the parts written so far came to.
	spent int
}

// full reports whether n bytes reach the part or the document limit.
func (o *ooxml) full(n int) bool { return n >= maxPartBytes || o.spent+n >= ooxmlMaxOutput }

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

// target is the part a relationship identifier names, relative to part.
func (o *ooxml) target(part, id string) string {
	if id == "" {
		return ""
	}
	o.loadRels(part)
	return o.rels[part][id]
}

// external is the address a relationship points outside the archive at.
func (o *ooxml) external(part, id string) string {
	o.loadRels(part)
	return o.ext[part][id]
}

// loadRels reads the relationships of a part, once.
func (o *ooxml) loadRels(part string) {
	if _, ok := o.rels[part]; ok {
		return
	}
	in, ex := map[string]string{}, map[string]string{}
	dir, base := splitPart(part)
	if root, err := o.part(dir + "_rels/" + base + ".rels"); err == nil {
		for _, r := range root.all("Relationship") {
			t := r.at("Target")
			switch {
			case t == "":
			case r.at("TargetMode") == "External":
				ex[r.at("Id")] = t
			default:
				in[r.at("Id")] = joinPart(dir, t)
			}
		}
	}
	if o.rels == nil {
		o.rels, o.ext = map[string]map[string]string{}, map[string]map[string]string{}
	}
	o.rels[part], o.ext[part] = in, ex
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

// ooxmlColor converts a six hex digit colour, "" for auto.
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
