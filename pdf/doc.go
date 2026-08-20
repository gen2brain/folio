package pdf

import (
	"fmt"
	"io"
	"sync"

	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

// Document is an open PDF file.
//
// A Document may be rendered from several goroutines at once, a Page each: the
// object store underneath it is read-only once the file is open, and the caches
// a render fills are locked. Configuration - SetLayers, SetUsage,
// GlyphCacheBytes and ImageCacheBytes - has to happen before those goroutines
// start, and Close after the last of them finishes. Err reads the error log
// whenever.
type Document struct {
	f *syntax.File

	// errs collects everything that went wrong while reading or interpreting.
	// A damaged file still renders what could be read, so this is a log
	// rather than a failure. Err reads it.
	errs []error

	// mu guards the caches a render fills; errMu guards errs, which every
	// layer appends to and which is never read on a hot path.
	mu    sync.Mutex
	errMu sync.Mutex

	fonts  map[any]*Font
	ocOff  map[Ref]bool
	layers []Layer
	usage  Usage
	fbFont *Font
	intent *ColorSpace
	glyphs *raster.GlyphCache

	images     map[imageKey]*imageEntry
	imageHead  *imageEntry
	imageTail  *imageEntry
	imageBytes int

	// GlyphCacheBytes bounds the rendered glyph masks the document keeps and
	// ImageCacheBytes the decoded images. Zero means the default, negative
	// means no cache at all.
	GlyphCacheBytes int
	ImageCacheBytes int
}

// glyphCache returns the document's cache of rendered glyph masks.
func (d *Document) glyphCache() *raster.GlyphCache {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	d.errs = append(d.errs, f.Err()...)
	d.fbFont = &Font{Name: "Helvetica", Dict: Dict{}, defWidth: 1000}
	d.readOutputIntent()
	d.readOptionalContent()
	return d
}

// Close releases the document.
// Close releases the document and everything it has cached. It must not be
// called while a page is still rendering.
func (d *Document) Close() error {
	if d.f != nil {
		d.f.Close()
	}
	d.mu.Lock()
	d.fonts, d.images, d.glyphs = nil, nil, nil
	d.imageHead, d.imageTail, d.imageBytes = nil, nil, 0
	d.layers, d.ocOff, d.fbFont, d.intent = nil, nil, nil, nil
	d.mu.Unlock()

	d.errMu.Lock()
	d.errs = nil
	d.errMu.Unlock()
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
func (d *Document) outputIntent() *ColorSpace { return d.intent }

// readOutputIntent finds the color space of the document's output intent, the
// space a PDF/A file says its device colors are meant for. It is read once, at
// open, so that a render never has to write to the document to find it.
func (d *Document) readOutputIntent() {
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
		return
	}
}

func (d *Document) errorf(format string, a ...any) {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if len(d.errs) < maxErrors {
		d.errs = append(d.errs, fmt.Errorf(format, a...))
	}
}

// Err returns what has gone wrong so far, which a damaged file logs rather
// than fails on. It is safe to call while pages are rendering.
func (d *Document) Err() []error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	return append([]error(nil), d.errs...)
}

func (d *Document) errCount() int {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	return len(d.errs)
}

// errAfter returns the first error logged since the log was n long, which is
// how Options.Strict turns a recorded error into a returned one.
func (d *Document) errAfter(n int) error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if n < len(d.errs) {
		return d.errs[n]
	}
	return nil
}

const maxErrors = 256

// fallbackFont is used when a content stream shows text with no usable font,
// so that the text still lands in the right places.
func (d *Document) fallbackFont() *Font { return d.fbFont }

// readOptionalContent applies the document's default optional content
// configuration, which says which groups start off.
func (d *Document) readOptionalContent() {
	d.layers, d.ocOff = nil, nil
	props := d.f.GetDict(d.f.Catalog()["OCProperties"])
	if props == nil {
		return
	}
	for _, g := range d.f.GetArray(props["OCGs"]) {
		r, ok := g.(Ref)
		if !ok {
			continue
		}
		d.layers = append(d.layers, Layer{Name: d.layerName(g), On: true, ref: r})
	}
	cfg := d.f.GetDict(props["D"])
	if cfg == nil {
		return
	}
	d.ocOff = map[Ref]bool{}
	if d.f.GetName(cfg["BaseState"]) == "OFF" {
		for i := range d.layers {
			d.ocOff[d.layers[i].ref] = true
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
	d.applyUsage(cfg)
	for i := range d.layers {
		d.layers[i].On = !d.ocOff[d.layers[i].ref]
	}
}

// applyUsage runs the usage application dictionaries of a configuration,
// which is how a group says it is meant for the screen but not for paper.
// ISO 32000-1 8.11.4.4.
func (d *Document) applyUsage(cfg Dict) {
	event := Name("View")
	switch d.usage {
	case UsagePrint:
		event = "Print"
	case UsageExport:
		event = "Export"
	}
	for _, a := range d.f.GetArray(cfg["AS"]) {
		use := d.f.GetDict(a)
		if use == nil || d.f.GetName(use["Event"]) != event {
			continue
		}
		for _, g := range d.f.GetArray(use["OCGs"]) {
			r, ok := g.(Ref)
			if !ok {
				continue
			}
			usage := d.f.GetDict(d.f.GetDict(g)["Usage"])
			for _, c := range d.f.GetArray(use["Category"]) {
				cat := d.f.GetName(c)
				st := d.f.GetDict(usage[cat])
				switch d.f.GetName(st[cat+"State"]) {
				case "OFF":
					d.ocOff[r] = true
				case "ON":
					delete(d.ocOff, r)
				}
			}
		}
	}
}

func (d *Document) layerName(obj Object) string {
	switch v := d.f.Resolve(d.f.GetDict(obj)["Name"]).(type) {
	case String:
		return decodeTextString(v)
	case Name:
		return string(v)
	}
	return ""
}

// Layer is one optional content group: a part of a document that can be drawn
// or left out, with the name the file gives it.
type Layer struct {
	Name string
	On   bool
	ref  Ref
}

// Layers returns the document's optional content groups and whether each is
// on, in the order the catalog lists them. It returns nothing for a document
// that has no optional content.
func (d *Document) Layers() []Layer {
	return append([]Layer(nil), d.layers...)
}

// SetLayers turns optional content groups on and off. Only the On field of
// each layer is read, and only layers this document declared are used, so the
// slice Layers returned can be edited and passed back.
func (d *Document) SetLayers(layers []Layer) {
	known := make(map[Ref]bool, len(d.layers))
	for i := range d.layers {
		known[d.layers[i].ref] = true
	}
	if d.ocOff == nil {
		d.ocOff = map[Ref]bool{}
	}
	for _, l := range layers {
		if !known[l.ref] {
			continue
		}
		if l.On {
			delete(d.ocOff, l.ref)
		} else {
			d.ocOff[l.ref] = true
		}
	}
	for i := range d.layers {
		d.layers[i].On = !d.ocOff[d.layers[i].ref]
	}
}

// Usage is what a document is being rendered for. Optional content groups and
// annotations may be meant for the screen, for paper, or for neither; ISO
// 32000-1 calls these the three events of a usage application dictionary.
type Usage int

// The three events.
const (
	UsageView Usage = iota
	UsagePrint
	UsageExport
)

// Usage returns what the document is being rendered for.
func (d *Document) Usage() Usage { return d.usage }

// SetUsage chooses what the document is rendered for, which decides both which
// optional content groups are on and which annotations are drawn. It re-applies
// the default configuration, so it undoes SetLayers. The default is UsageView.
func (d *Document) SetUsage(u Usage) {
	d.usage = u
	d.readOptionalContent()
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
