package pdf

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/folio/raster"
	"github.com/gen2brain/folio/syntax"
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
	// noSystemFonts stops a substitute being looked for outside the file.
	noSystemFonts bool
	fbFont        *Font
	intent        *ColorSpace
	glyphs        *raster.GlyphCache

	images     map[imageKey]*imageEntry
	imageHead  *imageEntry
	imageTail  *imageEntry
	imageBytes int

	// contents holds the parsed form of a content stream run before; a nil
	// entry is a stream seen once.
	contents   map[*syntax.Stream]*content
	contentOps int

	// GlyphCacheBytes bounds the rendered glyph masks the document keeps,
	// ImageCacheBytes the decoded images and ContentCacheOps the operands of
	// parsed content streams. Zero means the default, negative means no cache
	// at all.
	GlyphCacheBytes int
	ImageCacheBytes int
	ContentCacheOps int
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

// NewStream reads a document from a stream that cannot be seeked, which means
// reading all of it into memory first. A caller with an untrusted source
// bounds it with io.LimitReader.
func NewStream(r io.Reader) (*Document, error) { return NewStreamPassword(r, "") }

// NewStreamPassword is NewStream for an encrypted document.
func NewStreamPassword(r io.Reader, password string) (*Document, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Load(b, password)
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
	d.fbFont = d.font(Dict{
		"Subtype":  Name("Type1"),
		"BaseFont": Name("Helvetica"),
		"Encoding": Name("WinAnsiEncoding"),
	}, nil)
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
	d.fail(fmt.Errorf(format, a...))
}

// fail records one error a damaged file caused.
func (d *Document) fail(err error) {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if len(d.errs) < maxErrors {
		d.errs = append(d.errs, err)
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

// SetSystemFonts chooses whether a font the file does not embed may be
// substituted with one the machine has, which is what a page in a script the
// base fourteen cannot draw needs. It is on by default; turning it off makes
// a render depend on nothing outside the file. It has to be called before the
// first page is rendered.
func (d *Document) SetSystemFonts(on bool) { d.noSystemFonts = !on }

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

// Outline is one entry of the document outline, ISO 32000-1 12.3.3.
type Outline struct {
	Title string
	// Page is the page the entry leads to, and -1 otherwise; Point is where
	// on it.
	Page  int
	Point raster.Point
	// URI is where an entry leading out of the document points.
	URI string
	// Open is whether the file asks for the entry to start expanded.
	Open bool
	// Children are the entries nested under this one.
	Children []Outline
}

// maxOutlineDepth bounds an outline that points at itself.
const maxOutlineDepth = 32

// Outline returns the document outline as the tree it is.
func (d *Document) Outline() []Outline {
	f := d.f
	root := f.GetDict(f.Lookup(f.Catalog(), "Outlines"))
	if root == nil {
		return nil
	}
	return d.outlineList(root["First"], 0, map[Ref]bool{})
}

func (d *Document) outlineList(first Object, depth int, seen map[Ref]bool) []Outline {
	if depth > maxOutlineDepth {
		return nil
	}
	f := d.f
	var out []Outline
	for obj := first; obj != nil; {
		if r, ok := obj.(Ref); ok {
			if seen[r] {
				return out
			}
			seen[r] = true
		}
		item := f.GetDict(obj)
		if item == nil {
			return out
		}
		e := Outline{
			Title: decodeTextString(f.GetBytes(item["Title"])),
			Page:  -1,
			Open:  f.GetInt(item["Count"], 0) > 0,
		}
		if act := f.GetDict(item["A"]); act != nil {
			e.URI = d.actionURI(act)
			if e.URI == "" {
				e.Page, e.Point = d.destination(f.Lookup(act, "D"))
			}
		} else {
			e.Page, e.Point = d.destination(item["Dest"])
		}
		e.Children = d.outlineList(item["First"], depth+1, seen)
		out = append(out, e)
		obj = item["Next"]
	}
	return out
}

// Metadata is what the document says about itself, ISO 32000-1 14.3.3.
type Metadata struct {
	Title    string
	Author   string
	Subject  string
	Keywords string
	Creator  string
	Producer string
	// Created and Modified are zero when the file gives no date.
	Created  time.Time
	Modified time.Time
}

// Metadata returns the document information dictionary.
func (d *Document) Metadata() Metadata {
	f := d.f
	info := f.GetDict(f.Trailer()["Info"])
	if info == nil {
		return Metadata{}
	}
	str := func(key Name) string { return decodeTextString(f.GetBytes(info[key])) }
	return Metadata{
		Title:    str("Title"),
		Author:   str("Author"),
		Subject:  str("Subject"),
		Keywords: str("Keywords"),
		Creator:  str("Creator"),
		Producer: str("Producer"),
		Created:  parseDate(str("CreationDate")),
		Modified: parseDate(str("ModDate")),
	}
}

// parseDate reads a PDF date, ISO 32000-1 7.9.4, truncated anywhere after
// the year.
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "D:")
	digits := make([]byte, 0, 14)
	i := 0
	for ; i < len(s) && len(digits) < 14; i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		digits = append(digits, s[i])
	}
	if len(digits) < 4 {
		return time.Time{}
	}
	for len(digits) < 14 {
		switch len(digits) {
		case 4, 6:
			digits = append(digits, '0', '1')
		default:
			digits = append(digits, '0', '0')
		}
	}
	t, err := time.ParseInLocation("20060102150405", string(digits), dateZone(s[i:]))
	if err != nil {
		return time.Time{}
	}
	return t
}

// dateZone reads the offset a date ends with, all of it optional.
func dateZone(s string) *time.Location {
	if s == "" || s[0] == 'Z' {
		return time.UTC
	}
	sign := 1
	switch s[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return time.UTC
	}
	num := func(s string) int {
		if len(s) < 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
			return 0
		}
		return int(s[0]-'0')*10 + int(s[1]-'0')
	}
	hh := num(s[1:])
	mm := 0
	if i := strings.IndexAny(s, "'"); i >= 0 {
		mm = num(s[i+1:])
	} else if len(s) >= 5 {
		mm = num(s[3:])
	}
	if hh > 23 || mm > 59 {
		return time.UTC
	}
	return time.FixedZone("", sign*(hh*3600+mm*60))
}

// PageLabels returns what a viewer numbers each page with, ISO 32000-1
// 12.4.2, and nil for a document with no labels.
func (d *Document) PageLabels() []string {
	f := d.f
	root := f.Lookup(f.Catalog(), "PageLabels")
	if root == nil {
		return nil
	}
	n := f.NumPages()
	out := make([]string, n)

	type span struct {
		first int
		dict  Dict
	}
	var spans []span
	f.NumberTreeEach(root, func(k int64, v Object) bool {
		if k >= 0 && k < int64(n) {
			spans = append(spans, span{int(k), f.GetDict(v)})
		}
		return true
	})
	if len(spans) == 0 {
		return nil
	}
	for i, sp := range spans {
		end := n
		if i+1 < len(spans) {
			end = spans[i+1].first
		}
		style := f.GetName(sp.dict["S"])
		prefix := decodeTextString(f.GetBytes(sp.dict["P"]))
		start := int(f.GetInt(sp.dict["St"], 1))
		if start < 1 {
			start = 1
		}
		for p := sp.first; p < end; p++ {
			out[p] = prefix + pageLabel(style, start+p-sp.first)
		}
	}
	return out
}

// pageLabel numbers one page in the style a label range asks for.
func pageLabel(style Name, n int) string {
	switch style {
	case "D":
		return strconv.Itoa(n)
	case "r":
		return strings.ToLower(roman(n))
	case "R":
		return roman(n)
	case "a":
		return strings.ToLower(letters(n))
	case "A":
		return letters(n)
	}
	return ""
}

// romanDigits are the values and their spellings, largest first.
var romanDigits = []struct {
	v int
	s string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
	{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
	{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

// maxRoman is where a roman numeral stops being one.
const maxRoman = 4000

func roman(n int) string {
	if n <= 0 || n >= maxRoman {
		return strconv.Itoa(n)
	}
	var b strings.Builder
	for _, d := range romanDigits {
		for n >= d.v {
			b.WriteString(d.s)
			n -= d.v
		}
	}
	return b.String()
}

// letters numbers a page A, B ... Z, AA, BB, as the specification says.
func letters(n int) string {
	if n <= 0 {
		return ""
	}
	c := byte('A' + (n-1)%26)
	return strings.Repeat(string(c), (n-1)/26+1)
}
