package font

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// The bounds a WOFF is read inside.
const (
	maxWOFFTables = 512
	maxWOFFBytes  = 1 << 28
)

// isWOFF reports the container a web font is delivered in.
func isWOFF(b []byte) bool { return len(b) >= 4 && string(b[:4]) == "wOFF" }

// parseWOFF unpacks a web font into the sfnt inside it: a header, a table
// directory and the tables, each of them stored or deflated on its own.
func parseWOFF(data []byte) (*Font, error) {
	if len(data) < 44 {
		return nil, fmt.Errorf("%w: %d bytes of WOFF", ErrInvalid, len(data))
	}
	n := be16(data, 12)
	if n <= 0 || n > maxWOFFTables {
		return nil, fmt.Errorf("%w: %d tables", ErrInvalid, n)
	}
	total := be32(data, 16)
	if total < 12 || total > maxWOFFBytes {
		return nil, fmt.Errorf("%w: %d bytes of sfnt", ErrInvalid, total)
	}
	if 44+20*n > len(data) {
		return nil, fmt.Errorf("%w: directory runs past the file", ErrInvalid)
	}

	out := make([]byte, 12+16*n, total)
	copy(out, data[4:8])
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	// The three fields after the count are a binary search hint no reader
	// needs, and are written as the format says they should be.
	sr, sel := 16, 0
	for sr*2 <= 16*n {
		sr, sel = sr*2, sel+1
	}
	binary.BigEndian.PutUint16(out[6:], uint16(sr))
	binary.BigEndian.PutUint16(out[8:], uint16(sel))
	binary.BigEndian.PutUint16(out[10:], uint16(16*n-sr))

	for i := range n {
		e := 44 + 20*i
		off, comp, orig := be32(data, e+4), be32(data, e+8), be32(data, e+12)
		if off < 0 || comp < 0 || orig < 0 || off+comp > len(data) || orig > maxWOFFBytes {
			return nil, fmt.Errorf("%w: table %d runs past the file", ErrInvalid, i)
		}
		var table []byte
		if comp < orig {
			var err error
			if table, err = inflateTable(data[off:off+comp], orig); err != nil {
				return nil, err
			}
		} else {
			table = data[off : off+comp]
		}
		d := 12 + 16*i
		copy(out[d:], data[e:e+4])
		copy(out[d+4:], data[e+16:e+20])
		binary.BigEndian.PutUint32(out[d+8:], uint32(len(out)))
		binary.BigEndian.PutUint32(out[d+12:], uint32(len(table)))
		out = append(out, table...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return parseSFNT(out)
}

// inflateTable decompresses one table, which is deflated with a zlib header.
func inflateTable(b []byte, size int) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: WOFF table: %v", ErrInvalid, err)
	}
	defer r.Close()
	out := make([]byte, size)
	if _, err := io.ReadFull(r, out); err != nil && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("%w: WOFF table: %v", ErrInvalid, err)
	}
	return out, nil
}
