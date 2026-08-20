// Package syntax reads the PDF file format: objects, cross references, object
// streams, damaged file recovery, the stream filters and encryption. It knows
// nothing about graphics.
package syntax

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrInvalid is returned for a file that is not a PDF, or is damaged beyond
// what reconstruction can recover. ErrUnsupported is returned for a well formed
// file this package cannot handle.
var (
	ErrInvalid     = errors.New("pdf: invalid file")
	ErrUnsupported = errors.New("pdf: unsupported file")
	ErrPassword    = errors.New("pdf: wrong password")
)

// File is an open PDF file.
type File struct {
	buf   []byte
	xref  xref
	enc   *encrypt
	trail Dict

	// mu guards the object caches, which are the only mutable state a
	// rendering goroutine touches. Everything else a File holds is built
	// while it is being opened and read-only afterwards, which is what lets
	// a document be rendered from several goroutines at once.
	mu    sync.RWMutex
	errMu sync.Mutex

	// hdrOff is where %PDF- was found. It is nonzero only for files with
	// something prepended, whose stored offsets may or may not account for it.
	hdrOff int64

	root  Dict
	pages []Ref

	// errs collects everything that went wrong while parsing or reading. A
	// damaged file still yields the parts that could be read, so this is a
	// log rather than a failure: only Open returning an error means nothing
	// could be recovered. Err reads it.
	errs []error

	repaired bool
}

// Open reads the named file.
func Open(name string) (*File, error) { return OpenPassword(name, "") }

// OpenPassword reads the named file, which is encrypted with password.
func OpenPassword(name, password string) (*File, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return load(b, password)
}

// NewReader reads size bytes from r. The whole file is read into memory:
// resolving a cross-reference means seeking anywhere in it, and every filter
// decodes into memory regardless.
func NewReader(r io.ReaderAt, size int64) (*File, error) {
	return NewReaderPassword(r, size, "")
}

// NewReaderPassword reads size bytes from r, decrypting with password.
func NewReaderPassword(r io.ReaderAt, size int64, password string) (*File, error) {
	if size < 0 || size > 1<<40 {
		return nil, fmt.Errorf("%w: size %d", ErrInvalid, size)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, size), b); err != nil {
		return nil, err
	}
	return load(b, password)
}

// Load reads a document from a buffer, which it takes ownership of.
func Load(buf []byte, password string) (*File, error) { return load(buf, password) }

// Close releases the document. Using it afterwards is undefined.
// Close releases the file. It must not be called while anything is still
// reading the file, which for a document rendered from several goroutines
// means after the last of them has finished.
func (f *File) Close() error {
	f.mu.Lock()
	f.buf, f.root, f.pages, f.trail = nil, nil, nil, nil
	f.xref = xref{}
	f.enc = nil
	f.mu.Unlock()

	f.errMu.Lock()
	f.errs = nil
	f.errMu.Unlock()
	return nil
}

// Repaired reports whether the cross-reference table had to be rebuilt.
func (f *File) Repaired() bool { return f.repaired }

// Trailer returns the document trailer dictionary.
func (f *File) Trailer() Dict { return f.trail }

// Catalog returns the document catalog, the root of the object graph.
func (f *File) Catalog() Dict { return f.root }

func (f *File) errorf(format string, a ...any) {
	if f == nil {
		return
	}
	f.errMu.Lock()
	defer f.errMu.Unlock()
	if len(f.errs) < maxErrors {
		f.errs = append(f.errs, fmt.Errorf(format, a...))
	}
}

// Err returns what went wrong while reading the file, which is a log rather
// than a failure: a damaged file still yields the parts that could be read.
// It is safe to call while other goroutines are reading the same file.
func (f *File) Err() []error {
	if f == nil {
		return nil
	}
	f.errMu.Lock()
	defer f.errMu.Unlock()
	return append([]error(nil), f.errs...)
}

const maxErrors = 256

func errInvalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}
