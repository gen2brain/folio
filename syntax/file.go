// Package syntax reads the PDF file format: objects, cross references, object
// streams, damaged file recovery, the stream filters and encryption.
package syntax

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// ErrInvalid is returned for a file that is not a PDF or cannot be recovered,
// ErrUnsupported for a well formed file this package cannot handle.
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

	// mu guards the object caches.
	mu    sync.RWMutex
	errMu sync.Mutex

	// hdrOff is the offset of %PDF-.
	hdrOff int64

	root  Dict
	pages []Ref

	pageMu  sync.Mutex
	pageIdx map[Ref]int

	errs []error

	repaired bool

	// StreamCacheBytes bounds the decoded stream data kept: zero is
	// DefaultStreamCacheBytes, negative is none.
	StreamCacheBytes int
	decMu            sync.Mutex
	decLRU           list.List
	decHeld          int
}

// DefaultStreamCacheBytes is the default bound of a file's decoded stream cache.
const DefaultStreamCacheBytes = 1 << 26

// keep puts a decoded stream in the cache and drops the oldest past its bound.
func (f *File) keep(s *Stream, n, gen int) {
	limit := f.StreamCacheBytes
	if limit == 0 {
		limit = DefaultStreamCacheBytes
	}
	type victim struct {
		s   *Stream
		gen int
	}
	var drop []victim
	f.decMu.Lock()
	if s.elem == nil {
		s.elem, s.held = f.decLRU.PushFront(victim{s, gen}), n
		f.decHeld += n
	}
	for f.decHeld > max(limit, 0) {
		e := f.decLRU.Back()
		if e == nil {
			break
		}
		v := e.Value.(victim)
		f.decLRU.Remove(e)
		f.decHeld -= v.s.held
		v.s.elem, v.s.held = nil, 0
		drop = append(drop, v)
	}
	f.decMu.Unlock()
	for _, v := range drop {
		v.s.mu.Lock()
		if v.s.gen == v.gen {
			v.s.dec, v.s.decoded = nil, false
		}
		v.s.mu.Unlock()
	}
}

// touch marks a cached stream as just used.
func (f *File) touch(s *Stream) {
	f.decMu.Lock()
	if s.elem != nil {
		f.decLRU.MoveToFront(s.elem)
	}
	f.decMu.Unlock()
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

// NewReader reads size bytes from r into memory.
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

// Close releases the file. It must not be called while anything is still reading it.
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

// Err returns the errors recorded while reading the file.
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
