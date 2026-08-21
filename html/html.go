// Package html reads reflowable documents: EPUB, MOBI and plain text.
//
// A reflowable document has no pages until it is laid out, so a container
// hands over the parts a book is made of and the order to read them in:
//
//	doc, err := html.Open("book.epub")
//	defer doc.Close()
//
//	for _, item := range doc.Spine() {
//		b, err := doc.Read(item.Path)
//	}
//
// Read takes the path a spine or manifest item carries, and Resolve turns a
// link inside one part into the path of another.
package html

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

// Errors returned by this package.
var (
	// ErrInvalid means the file is not a book, or is damaged past recovery.
	ErrInvalid = errors.New("html: invalid document")
	// ErrUnsupported means the file is well formed but uses something this
	// package cannot read.
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
}

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
	// tocID is what the spine calls the EPUB 2 table of contents.
	tocID string
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
	var head [72]byte
	n, _ := r.ReadAt(head[:], 0)
	switch {
	case n >= 4 && string(head[:4]) == "PK\x03\x04":
		return openEPUB(r, size)
	case n >= 68 && (string(head[60:68]) == "BOOKMOBI" || string(head[60:68]) == "TEXtREAd"):
		return openMOBI(r, size)
	}
	return openText(r, size)
}

// Load reads a book from a buffer.
func Load(b []byte) (*Document, error) { return NewReader(newByteReader(b), int64(len(b))) }

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

// Manifest returns every part the book carries, and nil for a container that
// lists none.
func (d *Document) Manifest() []Item { return d.manifest }

// Outline returns the table of contents as the tree it is.
func (d *Document) Outline() []Outline { return d.outline }

// Read returns the bytes of one part.
func (d *Document) Read(p string) ([]byte, error) {
	if d.read == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d.read(p)
}

// Resolve turns a link inside one part into the path of another. An absolute
// path stands on its own, a relative one is read against base, and a fragment
// is dropped.
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
