// Package svg renders an SVG drawing. It is a document the way the other
// formats of this module are: a file opens, it has one page, and the page
// runs onto the same gfx.Device a PDF page draws through.
//
// What is drawn is the static picture: shapes and paths, transforms and
// viewports, the three paint servers, clips, masks, markers, filters, and
// text on a path or down the page, shaped and reordered. Scripting, animation
// and interaction are not a picture and are not read.
package svg

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// Errors returned by this package. A file that is not SVG at all, or whose
// XML does not parse, is ErrInvalid; one this cannot draw is ErrUnsupported.
var (
	ErrInvalid     = errors.New("svg: invalid")
	ErrUnsupported = errors.New("svg: unsupported")
)

// Device is the seam a page draws through, which is gfx.Device.
type Device = gfx.Device

// node is one element of the tree, with its attributes and its children. A
// run of character data is a child with no name, which is what keeps the text
// either side of a tspan in order.
type node struct {
	name  string
	attr  map[string]string
	kids  []*node
	up    *node
	chars string
}

// svgNS is the namespace an element has to be in to be part of the drawing.
const svgNS = "http://www.w3.org/2000/svg"

func isXlink(space string) bool {
	return space == "xlink" || space == "http://www.w3.org/1999/xlink"
}

// Document is one SVG file.
type Document struct {
	root *node
	// byID is every element that carries one, which use and the paint
	// servers refer to.
	byID map[string]*node
	// width and height are the size the file asks to be drawn at, in CSS
	// pixels, and box the coordinates it draws in.
	width, height float32
	box           []float32
	align         string
	slice         bool
	// rules are what the style elements declare, in the order a match takes
	// them: least specific first, and faces what the @font-face blocks
	// among them bring with them.
	rules []rule
	faces []faceRule
	// fonts are the programs those faces have been read into, which every
	// element that names one asks for again.
	fontMu sync.Mutex
	fonts  map[string]*font.Font
	// pw and ph are the box a container places the drawing in, which a
	// percentage size on the root element measures against. They are the
	// default page when the caller does not say.
	pw, ph float32
	// open resolves an address the drawing names. It is nil for one read out
	// of memory with no loader, and nothing is then looked up at all.
	open func(name string) ([]byte, error)
}

// maxNesting bounds how deep a use may reach, which a file that refers to
// itself would otherwise follow forever.
const maxNesting = 32

// The size a drawing is when it does not say, and the font size a relative
// length starts out measured against.
const (
	defWidth    = 612
	defHeight   = 792
	defFontSize = 12
)

// title is what the drawing calls itself, which is the title element the root
// carries.
func (d *Document) title() string {
	if d == nil || d.root == nil {
		return ""
	}
	for _, k := range d.root.kids {
		if k.name == "title" {
			var b strings.Builder
			for _, t := range k.kids {
				b.WriteString(t.chars)
			}
			return strings.TrimSpace(b.String() + k.chars)
		}
	}
	return ""
}

// Open reads an SVG file.
func Open(name string) (*Document, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return LoadWith(b, &LoadOptions{Open: dirLoader(filepath.Dir(name))})
}

// LoadOptions are what a caller says about a drawing before it is read. A nil
// pointer, and a zero field, is the default.
type LoadOptions struct {
	// Open resolves an address the drawing names, relative to the drawing
	// itself, which is how one inside a container reaches a picture beside
	// it. A nil Open resolves nothing but a data URL.
	Open func(name string) ([]byte, error)
	// Width and Height are the box the drawing is placed in, which is what a
	// percentage size on the root element measures against.
	Width, Height float32
}

// dirLoader reads a file beside the drawing. An address that leaves the
// directory is not followed, and nothing is fetched over a network.
func dirLoader(dir string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if filepath.IsAbs(name) || strings.Contains(name, "..") {
			return nil, fmt.Errorf("%w: %s leaves the drawing", ErrInvalid, name)
		}
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	}
}

// Load reads an SVG drawing out of memory.
func Load(b []byte) (*Document, error) { return LoadWith(b, nil) }

// NewReader reads a drawing from a reader.
func NewReader(r io.ReaderAt, size int64) (*Document, error) {
	if size < 0 {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, size)
	}
	return NewStream(io.NewSectionReader(r, 0, size))
}

// NewStream reads a drawing from a stream that cannot be seeked, which means
// reading all of it into memory first. A caller with an untrusted source
// bounds it with io.LimitReader.
func NewStream(r io.Reader) (*Document, error) { return NewStreamWith(r, nil) }

// NewStreamWith is NewStream with the options LoadWith takes.
func NewStreamWith(r io.Reader, o *LoadOptions) (*Document, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return LoadWith(b, o)
}

// LoadWith reads an SVG drawing out of memory, resolving what it names
// through a loader and sizing it against the box a container gives it.
func LoadWith(b []byte, o *LoadOptions) (*Document, error) {
	root, sheets, err := parseXML(b)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("%w: no svg element", ErrInvalid)
	}
	d := &Document{root: root, byID: map[string]*node{}}
	if o != nil {
		d.open = o.Open
		d.pw, d.ph = o.Width, o.Height
	}
	d.index(root, 0)
	d.readStyles(sheets)
	d.readSize()
	return d, nil
}

// Close releases what the document holds.
func (d *Document) Close() error { return nil }

// NumPages is one: a drawing is a single page.
func (d *Document) NumPages() int { return 1 }

// Page returns the drawing, which is page zero.
func (d *Document) Page(i int) (*Page, error) {
	if i != 0 {
		return nil, fmt.Errorf("%w: page %d of a drawing", ErrInvalid, i)
	}
	return &Page{doc: d}, nil
}

func (d *Document) index(n *node, depth int) {
	if depth > maxNesting {
		return
	}
	if id := n.attr["id"]; id != "" {
		if _, seen := d.byID[id]; !seen {
			d.byID[id] = n
		}
	}
	for _, k := range n.kids {
		d.index(k, depth+1)
	}
}

// readSize works out how big the drawing is. A file that gives neither a
// width nor a height is the size of its viewBox, and one that gives either is
// measured against a page.
func (d *Document) readSize() {
	if d.pw <= 0 {
		d.pw = defWidth
	}
	if d.ph <= 0 {
		d.ph = defHeight
	}
	d.box = numbers(d.root.attr["viewBox"])
	if len(d.box) != 4 || d.box[2] <= 0 || d.box[3] <= 0 {
		d.box = nil
	}
	d.align, d.slice = aspect(d.root.attr["preserveAspectRatio"])

	wa, ha := d.root.attr["width"], d.root.attr["height"]
	// A file that says neither is the size of its box, and one that says
	// either is measured against a page. There is no right answer here and
	// no two readers agree: this is MuPDF's, which is the oracle.
	if wa == "" && ha == "" && d.box != nil {
		d.width, d.height = d.box[2], d.box[3]
	} else {
		d.width, d.height = d.pw, d.ph
		// A size that is not positive is no size at all.
		if v, ok := length(wa, d.pw, defFontSize); ok && v > 0 {
			d.width = v
		}
		if v, ok := length(ha, d.ph, defFontSize); ok && v > 0 {
			d.height = v
		}
	}
	if d.width <= 0 {
		d.width = 1
	}
	if d.height <= 0 {
		d.height = 1
	}
}

// parseXML reads the tree, keeping only elements in no namespace or in the
// SVG one: a file may carry metadata from any other.
func parseXML(b []byte) (*node, []string, error) {
	b = expandEntities(b)
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var stack []*node
	// skip counts how deep inside an element of another vocabulary the
	// reader is.
	skip := 0
	var root *node
	var sheets []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if root != nil {
				break
			}
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// An element in another vocabulary is not a drawing, and neither
			// is anything inside it.
			if skip > 0 {
				skip++
				continue
			}
			if s := t.Name.Space; s != "" && s != svgNS {
				skip = 1
				continue
			}
			if len(stack) > maxNesting*4 {
				return nil, nil, fmt.Errorf("%w: elements nested too deeply", ErrInvalid)
			}
			n := &node{name: t.Name.Local, attr: make(map[string]string, len(t.Attr))}
			// An attribute keeps its local name. The only namespace that
			// matters is xlink, whose href means the same as href and gives
			// way to one written without a prefix.
			for _, a := range t.Attr {
				if a.Name.Local == "href" && a.Name.Space != "" {
					continue
				}
				n.attr[a.Name.Local] = a.Value
			}
			for _, a := range t.Attr {
				if a.Name.Local != "href" || !isXlink(a.Name.Space) {
					continue
				}
				if _, ok := n.attr["href"]; !ok {
					n.attr["href"] = a.Value
				}
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				n.up = p
				p.kids = append(p.kids, n)
			} else if root == nil && n.name == "svg" {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			// Character data is a child of its own so that the text either
			// side of a tspan keeps its place in the order.
			if len(stack) == 0 {
				continue
			}
			p := stack[len(stack)-1]
			if n := len(p.kids); n > 0 && p.kids[n-1].name == "" {
				p.kids[n-1].chars += string(t)
				continue
			}
			p.kids = append(p.kids, &node{chars: string(t)})
		case xml.ProcInst:
			// An SVG may hold its CSS in a file beside it, which it names
			// with an xml-stylesheet instruction rather than an element.
			if t.Target == "xml-stylesheet" {
				if href := pseudoAttr(string(t.Inst), "href"); href != "" {
					sheets = append(sheets, href)
				}
			}
		}
	}
	return root, sheets, nil
}

// pseudoAttr reads one of the name="value" pairs a processing instruction
// carries, which are not attributes and are not parsed as any.
func pseudoAttr(s, name string) string {
	for {
		i := strings.Index(s, name)
		if i < 0 {
			return ""
		}
		s = strings.TrimSpace(s[i+len(name):])
		if !strings.HasPrefix(s, "=") {
			continue
		}
		s = strings.TrimSpace(s[1:])
		if len(s) == 0 || (s[0] != '"' && s[0] != '\'') {
			continue
		}
		q := s[0]
		if j := strings.IndexByte(s[1:], q); j >= 0 {
			return s[1 : 1+j]
		}
		return ""
	}
}

// Page is the drawing.
type Page struct{ doc *Document }

// Bounds is how big the drawing is, in CSS pixels, which is what a viewer
// draws it at when it is not told otherwise.
func (p *Page) Bounds() raster.Rect {
	return raster.Rect{X1: p.doc.width, Y1: p.doc.height}
}

// Matrix is the transform that puts the drawing on a device at a resolution,
// where 96 is the pixel an SVG length is written in.
func (p *Page) Matrix(dpi float64) raster.Matrix {
	s := float32(dpi / 96)
	return raster.Scale(s, s)
}

// DeviceBounds is the area the page covers at a resolution.
func (p *Page) DeviceBounds(dpi float64) raster.Rect {
	return p.Matrix(dpi).ApplyRect(p.Bounds())
}
