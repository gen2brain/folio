package doc

import (
	"bytes"
	"io"
	"unicode/utf8"
)

// kindOf reads the head of a file for what it is, and zero for none of the
// three. A file none of the magic numbers claims is a book when it reads as
// text, which is what a plain text document is.
func kindOf(b []byte) Kind {
	if len(b) == 0 {
		return 0
	}
	if bytes.Contains(b, []byte("%PDF-")) {
		return KindPDF
	}
	switch {
	case len(b) >= 4 && string(b[:4]) == "PK\x03\x04",
		len(b) >= 68 && (string(b[60:68]) == "BOOKMOBI" || string(b[60:68]) == "TEXtREAd"),
		len(b) >= 4 && string(b[:4]) == "ITSF",
		bytes.Contains(b, []byte("<FictionBook")):
		return KindBook
	}
	if hasElement(b, "svg") {
		return KindSVG
	}
	if isText(b) {
		return KindBook
	}
	return 0
}

// hasElement reports a start tag of the named element, which for a root is as
// far into the file as the declaration, the doctype and the comments before
// it reach.
func hasElement(b []byte, name string) bool {
	for i := 0; ; {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			return false
		}
		i += j + 1
		if i >= len(b) {
			return false
		}
		if len(b)-i >= len(name) && bytes.EqualFold(b[i:i+len(name)], []byte(name)) {
			if k := i + len(name); k == len(b) || b[k] == '>' || b[k] == '/' ||
				b[k] == ' ' || b[k] == '\t' || b[k] == '\n' || b[k] == '\r' {
				return true
			}
		}
	}
}

// isText reports bytes a plain text document could be made of.
func isText(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	if !utf8.Valid(b) {
		// A truncated rune at the end of the head is not a binary file.
		for n := 1; n < utf8.UTFMax && n < len(b); n++ {
			if utf8.Valid(b[:len(b)-n]) {
				return true
			}
		}
		return false
	}
	return true
}

// byteReader is a buffer read at an offset.
type byteReader struct{ b []byte }

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
