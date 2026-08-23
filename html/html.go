// Package html reads reflowable documents: EPUB, MOBI, CHM, FB2, DOCX, PPTX,
// XLSX and plain text.
//
//	doc, err := html.Open("book.epub")
//	defer doc.Close()
//
//	p, err := doc.Page(0)
//	img, err := p.ImageDPI(150)
//
// A document is a spine of parts rather than pages. [Document.Spine] lists
// them, [Document.Read] hands over the markup, [Document.StylePart] parses one
// and computes the style of every element, and [Document.Layout] reflows the
// lot onto pages of a size. [Document.Text] and [Document.Markdown] read the
// parts rather than the pages and need no layout.
//
// A page is measured in CSS pixels, 96 to the inch, and [Page.Bounds] returns
// points so that a page of a book measures the way a page of a PDF does.
// [Page.Run] draws through the same gfx.Device a PDF page draws through.
//
// A face comes from the document when an @font-face rule brings one, from the
// machine when it has one for a family the document names, and from the base
// fourteen otherwise.
//
// Pages may be rendered from several goroutines at once, but not while
// [Document.Layout] is running.
package html

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/folio/font"
)

// Errors returned by this package.
var (
	// ErrInvalid means the file is not a book, or is damaged past recovery.
	ErrInvalid = errors.New("html: invalid document")
	// ErrUnsupported is a well formed file this package cannot read.
	ErrUnsupported = errors.New("html: unsupported document")
	// ErrNotFound means the document has no part by that path.
	ErrNotFound = errors.New("html: no such part")
)

// Kind is the container a document came out of.
type Kind int

// The containers.
const (
	KindEPUB Kind = iota
	KindMOBI
	KindText
	KindCHM
	KindDOCX
	KindPPTX
	KindXLSX
	KindFB2
	KindHTML
)

// String returns the name of the container.
func (k Kind) String() string {
	switch k {
	case KindEPUB:
		return "EPUB"
	case KindMOBI:
		return "MOBI"
	case KindText:
		return "text"
	case KindCHM:
		return "CHM"
	case KindDOCX:
		return "DOCX"
	case KindPPTX:
		return "PPTX"
	case KindXLSX:
		return "XLSX"
	case KindFB2:
		return "FB2"
	case KindHTML:
		return "HTML"
	}
	return "unknown"
}

// Item is one part of a book: a chapter, a stylesheet, an image.
type Item struct {
	// Path is what the container calls it, and what a link resolves against.
	Path string
	// Type is the media type the container declares.
	Type string
	// ID is the manifest identifier.
	ID string
	// Properties are the manifest properties of an EPUB item.
	Properties []string
	// Linear is false for a spine item the package marks as auxiliary, which
	// a reader may leave out of the main flow.
	Linear bool
}

// IsChapter reports whether the item is a part with text in it rather than a
// picture, which a spine may carry as well.
func (i Item) IsChapter() bool { return i.Type == "" || isChapter(i.Type) }

// Metadata is what a book says about itself.
type Metadata struct {
	Title       string
	Author      string
	Language    string
	Identifier  string
	Publisher   string
	Description string
	Subjects    []string
	// Created and Modified are zero when the book gives no date.
	Created  time.Time
	Modified time.Time
}

// Outline is one entry of a book's table of contents.
type Outline struct {
	Title string
	// Path is the part the entry leads to, and Fragment the anchor inside it.
	Path     string
	Fragment string
	Children []Outline
}

// Document is an open book.
type Document struct {
	kind  Kind
	close func() error

	meta     Metadata
	spine    []Item
	manifest []Item
	outline  []Outline
	read     func(path string) ([]byte, error)
	// base is the directory the paths are relative to.
	base string
	// tocID is what the spine calls the EPUB 2 table of contents, and
	// obfuscated the parts a publisher scrambled and the key to each.
	obfuscated map[string][]byte
	tocID      string
	headings   sync.Once
	// filepos are the byte offsets a MOBI names its own places by, which
	// become anchors when the markup is read.
	filepos []int
	// chmHome is the page a CHM opens at.
	chmHome string
	// natural is the page the document itself asks for, which a caller may
	// take by leaving the size out of LayoutOptions.
	natural LayoutOptions

	// fontMu guards the font programs the book carries, read once each.
	fontMu sync.Mutex
	fonts  map[string]*font.Font

	// layoutMu guards what Layout produced, which every page reads, and
	// autoMu keeps two goroutines from laying the book out at once when
	// neither asked for a size.
	layoutMu sync.Mutex
	autoMu   sync.Mutex
	opt      LayoutOptions
	parts    []*laidPart
	pages    []*Page
	laidOut  bool
}

// Open reads the named file, deciding from its contents what it is.
func Open(name string) (*Document, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d, err := NewReader(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	d.close = f.Close
	return d, nil
}

// NewReader reads a book from a reader.
func NewReader(r io.ReaderAt, size int64) (*Document, error) {
	var head [512]byte
	n, _ := r.ReadAt(head[:], 0)
	switch {
	case n >= 4 && string(head[:4]) == "PK\x03\x04":
		return openZip(r, size)
	case n >= 68 && (string(head[60:68]) == "BOOKMOBI" || string(head[60:68]) == "TEXtREAd"):
		return openMOBI(r, size)
	case n >= 4 && string(head[:4]) == "ITSF":
		return openCHM(r, size)
	case isFB2(head[:n]):
		b, err := readAll(r, size)
		if err != nil {
			return nil, err
		}
		return openFB2(b)
	}
	return openText(r, size)
}

// isFB2 reports the head of a file that declares itself a FictionBook, which
// is an XML document rather than a container.
func isFB2(b []byte) bool {
	if len(b) > 512 {
		b = b[:512]
	}
	s := string(b)
	if i := strings.Index(s, "\x00"); i >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.Contains(s, "<FictionBook")
}

func readAll(r io.ReaderAt, size int64) ([]byte, error) {
	if size <= 0 || size > maxPartBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, size)
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, err
	}
	return b, nil
}

// Load reads a book from a buffer.
func Load(b []byte) (*Document, error) { return NewReader(newByteReader(b), int64(len(b))) }

// NewStream reads a document from a stream that cannot be seeked, which means
// reading all of it into memory first. A caller with an untrusted source
// bounds it with io.LimitReader.
func NewStream(r io.Reader) (*Document, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Load(b)
}

// Close releases what the document holds.
func (d *Document) Close() error {
	if d.close == nil {
		return nil
	}
	err := d.close()
	d.close = nil
	return err
}

// Kind returns the container the document came out of.
func (d *Document) Kind() Kind { return d.kind }

// Metadata returns what the book says about itself.
func (d *Document) Metadata() Metadata { return d.meta }

// Spine returns the parts to read, in order.
func (d *Document) Spine() []Item { return d.spine }

// Manifest is every part the book carries, nil for a container listing none.
func (d *Document) Manifest() []Item { return d.manifest }

// Outline returns the table of contents as the tree it is. A book that
// carries none gets one built from the headings of its parts.
func (d *Document) Outline() []Outline {
	d.headings.Do(func() {
		if len(d.outline) == 0 {
			d.outline = d.fromHeadings()
		}
	})
	return d.outline
}

// Read returns the bytes of one part.
func (d *Document) Read(p string) ([]byte, error) {
	if d.read == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d.read(p)
}

// Resolve turns a link inside one part into the path of another. An absolute
// path stands alone, a relative one reads against base, a fragment is cut.
func Resolve(base, ref string) string {
	if i := strings.IndexAny(ref, "#?"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" {
		return base
	}
	if strings.HasPrefix(ref, "/") {
		return path.Clean(ref[1:])
	}
	if i := strings.Index(ref, "://"); i > 0 {
		return ref
	}
	return path.Join(path.Dir(base), ref)
}

// splitFragment cuts a link into the part it names and the anchor inside it.
func splitFragment(ref string) (string, string) {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
