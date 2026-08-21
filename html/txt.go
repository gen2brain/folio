package html

import (
	"fmt"
	"io"
	"unicode/utf8"
)

// textPath is what a plain text document calls its one part.
const textPath = "index.txt"

// openText reads a file that is not a container: the bytes are the book.
func openText(r io.ReaderAt, size int64) (*Document, error) {
	if size <= 0 || size > maxPartBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, size)
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if !readsAsText(b) {
		return nil, fmt.Errorf("%w: not a book", ErrInvalid)
	}
	item := Item{Path: textPath, Type: "text/plain", Linear: true}
	return &Document{
		kind:     KindText,
		spine:    []Item{item},
		manifest: []Item{item},
		read: func(p string) ([]byte, error) {
			if p != textPath {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
			}
			return b, nil
		},
	}, nil
}

// readsAsText reports valid UTF-8 with no control characters in it.
func readsAsText(b []byte) bool {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return true
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			return false
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' {
			return false
		}
		b = b[n:]
	}
	return true
}
