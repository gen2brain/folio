// Package doc opens a document without the caller knowing what it is.
//
//	d, err := doc.Open("file")
//	defer d.Close()
//
//	for i := range d.NumPages() {
//		p, err := d.Page(i)
//		img, err := p.ImageDPI(150)
//	}
//
// It sniffs the contents and hands back the pdf, svg or html document behind
// one interface. What the three do not share - a PDF's optional content, a
// book's spine, a drawing's viewBox - is reached by type asserting Underlying.
//
// A caller who knows the format should import that package instead: this one
// pulls all three in.
package doc

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"time"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/html"
	"github.com/gen2brain/folio/pdf"
	"github.com/gen2brain/folio/raster"
	"github.com/gen2brain/folio/svg"
)

// ErrUnsupported is a file none of the three packages reads.
var ErrUnsupported = errors.New("doc: unsupported document")

// Kind is what a file turned out to be.
type Kind int

// The kinds a file may open as.
const (
	KindPDF Kind = iota + 1
	KindSVG
	KindBook
)

func (k Kind) String() string {
	switch k {
	case KindPDF:
		return "PDF"
	case KindSVG:
		return "SVG"
	case KindBook:
		return "book"
	}
	return "unknown"
}

// Metadata is what all three formats say about themselves. The rest is on the
// document Underlying returns.
type Metadata struct {
	Title    string
	Author   string
	Created  time.Time
	Modified time.Time
}

// Link is where a link on a page leads, as much of it as the three formats
// have in common. Rect is in page space at 72 dots per inch.
type Link struct {
	Rect raster.Rect
	URI  string
}

// Document is an open document of any of the three kinds.
type Document interface {
	// Kind is what the file turned out to be.
	Kind() Kind
	// Underlying is the *pdf.Document, *html.Document or *svg.Document behind
	// this one.
	Underlying() any
	// NumPages is how many pages the document has. A book is laid out at its
	// default size the first time it is asked.
	NumPages() int
	// Page returns one page, counting from zero.
	Page(i int) (Page, error)
	// Metadata is the title and author the document gives.
	Metadata() Metadata
	// Close releases the document. It must not be called while a page is
	// still rendering.
	Close() error
}

// Page is one page of a document.
type Page interface {
	// Bounds is the page in points.
	Bounds() raster.Rect
	// Image renders the page at its natural resolution and ImageDPI at the
	// one asked for.
	Image() (*image.RGBA, error)
	ImageDPI(dpi float64) (*image.RGBA, error)
	// Text is what the page says.
	Text() (string, error)
	// StructuredText is the same with the box every character covers.
	StructuredText() (*gfx.TextPage, error)
	// Links is where the page leads.
	Links() []Link
	// SVG and HTML are the page as markup.
	SVG() (string, error)
	HTML() (string, error)
	// Run draws the page through a device.
	Run(dev gfx.Device, ctm raster.Matrix) error
}

// Open reads the named file, deciding from its contents what it is.
func Open(name string) (Document, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d, err := newReader(f, fi.Size(), f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return d, nil
}

// Load reads a document from a buffer.
func Load(b []byte) (Document, error) { return NewReader(newByteReader(b), int64(len(b))) }

// NewStream reads a document from a reader that cannot be seeked, buffering it.
func NewStream(r io.Reader) (Document, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Load(b)
}

// NewReader reads a document of size bytes from r.
func NewReader(r io.ReaderAt, size int64) (Document, error) {
	return newReader(r, size, nil)
}

// newReader is NewReader with a file the document takes ownership of.
func newReader(r io.ReaderAt, size int64, c io.Closer) (Document, error) {
	var head [1024]byte
	n, err := r.ReadAt(head[:], 0)
	if n == 0 && err != nil && err != io.EOF {
		return nil, err
	}
	switch kindOf(head[:n]) {
	case KindPDF:
		d, err := pdf.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		return pdfDoc{d, c}, nil
	case KindSVG:
		d, err := svg.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		return svgDoc{d, c}, nil
	case KindBook:
		d, err := html.NewReader(r, size)
		if err != nil {
			return nil, err
		}
		return bookDoc{d, c}, nil
	}
	return nil, fmt.Errorf("%w", ErrUnsupported)
}

// Detect reports what the head of a file says it is, and zero for none of the
// three.
func Detect(head []byte) Kind { return kindOf(head) }
