package html

import "io"

// byteReader is a buffer read the way a file is.
type byteReader struct{ b []byte }

func newByteReader(b []byte) *byteReader { return &byteReader{b} }

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
