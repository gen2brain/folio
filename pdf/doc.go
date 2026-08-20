package pdf

import (
	"fmt"
	"io"

	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

// Document is an open PDF file.
type Document struct {
	f *syntax.File

	// Errors collects everything that went wrong while reading or
	// interpreting. A damaged file still renders what could be read.
	Errors []error

	fonts  map[any]*Font
	ocOff  map[Ref]bool
	fbFont *Font
	intent *ColorSpace
	glyphs *raster.GlyphCache

	// GlyphCacheBytes bounds the rendered glyph masks the document keeps.
	// Zero means the default; it is read the first time a page is rendered.
	GlyphCacheBytes int
}

// glyphCache returns the document's cache of rendered glyph masks.
func (d *Document) glyphCache() *raster.GlyphCache {
	if d.glyphs == nil {
		d.glyphs = raster.NewGlyphCache(d.GlyphCacheBytes)
	}
	return d.glyphs
}

// Open reads the named file.
func Open(name string) (*Document, error) { return OpenPassword(name, "") }

// OpenPassword reads the named file, which is encrypted with password.
func OpenPassword(name, password string) (*Document, error) {
	f, err := syntax.OpenPassword(name, password)
	if err != nil {
		return nil, err
	}
	return newDocument(f), nil
}

// NewReader reads size bytes from r.
func NewReader(r io.ReaderAt, size int64) (*Document, error) {
	return NewReaderPassword(r, size, "")
}

// NewReaderPassword reads size bytes from r, decrypting with password.
func NewReaderPassword(r io.ReaderAt, size int64, password string) (*Document, error) {
	f, err := syntax.NewReaderPassword(r, size, password)
	if err != nil {
		return nil, err
	}
	return newDocument(f), nil
}

// Load reads a document from a buffer, which it takes ownership of.
func Load(buf []byte, password string) (*Document, error) {
	f, err := syntax.Load(buf, password)
	if err != nil {
		return nil, err
	}
	return newDocument(f), nil
}

// New wraps an already parsed file.
func New(f *syntax.File) *Document { return newDocument(f) }

func newDocument(f *syntax.File) *Document {
	d := &Document{f: f, fonts: map[any]*Font{}}
	d.Errors = append(d.Errors, f.Errors...)
	d.readOptionalContent()
	return d
}

// Close releases the document.
func (d *Document) Close() error {
	if d.f != nil {
		d.f.Close()
	}
	*d = Document{}
	return nil
}

// File returns the object layer underneath, for callers that want to read the
// document structure directly.
func (d *Document) File() *syntax.File { return d.f }

// NumPages returns the number of pages.
func (d *Document) NumPages() int { return d.f.NumPages() }

// Page returns page i, counting from zero.
func (d *Document) Page(i int) (*Page, error) {
	if i < 0 || i >= d.f.NumPages() {
		return nil, fmt.Errorf("%w: page %d of %d", ErrInvalid, i, d.f.NumPages())
	}
	dict := d.f.Page(i)
	if dict == nil {
		d.errorf("page %d has no dictionary", i+1)
		dict = Dict{}
	}
	return &Page{doc: d, dict: dict, num: i}, nil
}

// outputIntent returns the color space of the document's output intent, the
// space a PDF/A file says its device colors are meant for.
func (d *Document) outputIntent() *ColorSpace {
	if d.intent != nil {
		return d.intent
	}
	for _, oi := range d.f.GetArray(d.f.Catalog()["OutputIntents"]) {
		dict := d.f.GetDict(oi)
		if dict == nil {
			continue
		}
		st := d.f.GetStream(dict["DestOutputProfile"])
		if st == nil {
			continue
		}
		switch d.f.GetInt(st.Dict["N"], 0) {
		case 1:
			d.intent = DeviceGray
		case 3:
			d.intent = DeviceRGB
		case 4:
			d.intent = DeviceCMYK
		default:
			continue
		}
		return d.intent
	}
	return nil
}

func (d *Document) errorf(format string, a ...any) {
	if len(d.Errors) < maxErrors {
		d.Errors = append(d.Errors, fmt.Errorf(format, a...))
	}
}

const maxErrors = 256

// fallbackFont is used when a content stream shows text with no usable font,
// so that the text still lands in the right places.
func (d *Document) fallbackFont() *Font {
	if d.fbFont == nil {
		d.fbFont = &Font{Name: "Helvetica", Dict: Dict{}, defWidth: 1000}
	}
	return d.fbFont
}

// readOptionalContent notes which optional content groups are off in the
// default configuration.
func (d *Document) readOptionalContent() {
	props := d.f.GetDict(d.f.Catalog()["OCProperties"])
	if props == nil {
		return
	}
	cfg := d.f.GetDict(props["D"])
	if cfg == nil {
		return
	}
	d.ocOff = map[Ref]bool{}
	if d.f.GetName(cfg["BaseState"]) == "OFF" {
		for _, g := range d.f.GetArray(props["OCGs"]) {
			if r, ok := g.(Ref); ok {
				d.ocOff[r] = true
			}
		}
		for _, g := range d.f.GetArray(cfg["ON"]) {
			if r, ok := g.(Ref); ok {
				delete(d.ocOff, r)
			}
		}
	}
	for _, g := range d.f.GetArray(cfg["OFF"]) {
		if r, ok := g.(Ref); ok {
			d.ocOff[r] = true
		}
	}
}

// optionalContentVisible reports whether content governed by an /OC entry is
// drawn. An unreadable or unknown group is visible.
func (d *Document) optionalContentVisible(obj Object) bool {
	if obj == nil || len(d.ocOff) == 0 {
		return true
	}
	if r, ok := obj.(Ref); ok && d.ocOff[r] {
		return false
	}
	dict := d.f.GetDict(obj)
	if dict == nil {
		return true
	}
	if d.f.GetName(dict["Type"]) != "OCMD" {
		return true
	}

	var groups []Object
	switch g := d.f.Resolve(dict["OCGs"]).(type) {
	case Array:
		groups = g
	case Dict:
		groups = []Object{dict["OCGs"]}
	}
	on := func(o Object) bool {
		r, ok := o.(Ref)
		return !ok || !d.ocOff[r]
	}
	switch d.f.GetName(dict["P"]) {
	case "AllOff":
		for _, g := range groups {
			if on(g) {
				return false
			}
		}
		return true
	case "AnyOff":
		for _, g := range groups {
			if !on(g) {
				return true
			}
		}
		return len(groups) == 0
	case "AllOn":
		for _, g := range groups {
			if !on(g) {
				return false
			}
		}
		return true
	default: // AnyOn
		if len(groups) == 0 {
			return true
		}
		for _, g := range groups {
			if on(g) {
				return true
			}
		}
		return false
	}
}
