package html

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// The PalmDB types this reads.
const (
	mobiHTML = "BOOKMOBI"
	mobiText = "TEXtREAd"
)

// mobiIndex is the part the document itself comes back as.
const mobiIndex = "index.html"

// openMOBI reads a PalmDB database holding a book.
func openMOBI(r io.ReaderAt, size int64) (*Document, error) {
	if size < 78 || size > maxBookBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, size)
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, err
	}
	kind := string(b[60:68])
	if kind != mobiHTML && kind != mobiText {
		return nil, fmt.Errorf("%w: PalmDB type %q", ErrUnsupported, kind)
	}

	recs := mobiRecords(b)
	if len(recs) == 0 {
		return nil, fmt.Errorf("%w: no records", ErrInvalid)
	}

	m := mobiHeader(recs[0])
	text, next := m.text(recs)
	if len(text) == 0 {
		return nil, fmt.Errorf("%w: no text", ErrInvalid)
	}
	if !utf8.Valid(text) {
		text = fromCP1252(text)
	}

	parts := map[string][]byte{mobiIndex: text}
	item := Item{Path: mobiIndex, Type: "text/html"}
	if kind == mobiText {
		item.Type = "text/plain"
	}
	d := &Document{
		kind:     KindMOBI,
		spine:    []Item{item},
		manifest: []Item{item},
		meta:     m.meta,
	}
	// The pictures are numbered from one, which is what a recindex holds.
	n := 1
	for i := next; i < len(recs); i++ {
		t := imageType(recs[i])
		if t == "" {
			continue
		}
		path := fmt.Sprintf("%05d", n)
		parts[path] = recs[i]
		d.manifest = append(d.manifest, Item{Path: path, Type: t})
		n++
	}
	d.read = func(p string) ([]byte, error) {
		if v, ok := parts[p]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d, nil
}

// maxBookBytes bounds a PalmDB, which is read whole.
const maxBookBytes = 1 << 30

// mobiRecords cuts the file into its records, dropping every offset that does
// not move forward and stay inside the file.
func mobiRecords(b []byte) [][]byte {
	n := int(binary.BigEndian.Uint16(b[76:]))
	if n <= 0 || 78+8*n > len(b) {
		return nil
	}
	low := 78 + 8*n - 1
	var offs []int
	for i := 0; i < n; i++ {
		off := int(binary.BigEndian.Uint32(b[78+8*i:]))
		if off <= low || off >= len(b) {
			continue
		}
		low = off
		offs = append(offs, off)
	}
	if len(offs) == 0 {
		return nil
	}
	offs = append(offs, len(b))
	out := make([][]byte, len(offs)-1)
	for i := range out {
		out[i] = b[offs[i]:offs[i+1]]
	}
	return out
}

// mobi is what the first record says about the rest.
type mobi struct {
	compression int
	textLength  int
	records     int
	trailing    uint32
	encoding    int
	meta        Metadata
}

// mobiHeader reads the PalmDOC header and the MOBI header after it.
func mobiHeader(rec []byte) mobi {
	m := mobi{encoding: 65001}
	if len(rec) < 16 {
		return m
	}
	m.compression = int(binary.BigEndian.Uint16(rec[0:]))
	m.textLength = int(binary.BigEndian.Uint32(rec[4:]))
	m.records = int(binary.BigEndian.Uint16(rec[8:]))
	// Every field below is inside the fixed part of the MOBI header, so one
	// bound covers them all.
	if len(rec) < 0x84 || string(rec[16:20]) != "MOBI" {
		return m
	}
	hdr := int(binary.BigEndian.Uint32(rec[20:]))
	if v := int32(binary.BigEndian.Uint32(rec[28:])); v > 0 {
		m.encoding = int(v)
	}
	if hdr >= 0xe4 && len(rec) >= 0xf4 {
		m.trailing = binary.BigEndian.Uint32(rec[0xf0:])
	}
	off := int64(binary.BigEndian.Uint32(rec[0x54:]))
	n := int64(binary.BigEndian.Uint32(rec[0x58:]))
	if n > 0 && off+n <= int64(len(rec)) {
		m.meta.Title = strings.TrimSpace(string(rec[off : off+n]))
	}
	if binary.BigEndian.Uint32(rec[0x80:])&0x40 != 0 {
		m.readEXTH(rec, 16+hdr)
	}
	return m
}

// The EXTH records this reads.
const (
	exthAuthor      = 100
	exthPublisher   = 101
	exthDescription = 103
	exthSubject     = 105
	exthDate        = 106
	exthTitle       = 503
	exthLanguage    = 524
)

// readEXTH reads the extended header: a count, then that many typed records.
func (m *mobi) readEXTH(rec []byte, at int) {
	if at < 0 || at+12 > len(rec) || string(rec[at:at+4]) != "EXTH" {
		return
	}
	n := int(binary.BigEndian.Uint32(rec[at+8:]))
	p := at + 12
	for i := 0; i < n && p+8 <= len(rec); i++ {
		kind := int(binary.BigEndian.Uint32(rec[p:]))
		size := int(binary.BigEndian.Uint32(rec[p+4:]))
		if size < 8 || p+size > len(rec) {
			return
		}
		v := strings.TrimSpace(string(rec[p+8 : p+size]))
		switch kind {
		case exthAuthor:
			m.meta.Author = join(m.meta.Author, v)
		case exthPublisher:
			m.meta.Publisher = first([]string{m.meta.Publisher, v})
		case exthDescription:
			m.meta.Description = first([]string{m.meta.Description, v})
		case exthSubject:
			if v != "" {
				m.meta.Subjects = append(m.meta.Subjects, v)
			}
		case exthDate:
			if t := parseDate(v); !t.IsZero() {
				m.meta.Created = t
			}
		case exthTitle:
			if v != "" {
				m.meta.Title = v
			}
		case exthLanguage:
			m.meta.Language = first([]string{m.meta.Language, v})
		}
		p += size
	}
}

func join(a, b string) string {
	switch {
	case b == "":
		return a
	case a == "":
		return b
	}
	return a + ", " + b
}

// text decompresses the document and returns it with the index of the first
// record after it.
func (m mobi) text(recs [][]byte) ([]byte, int) {
	n := m.records
	if n <= 0 || n >= len(recs) {
		n = len(recs) - 1
	}
	var out []byte
	for i := 1; i <= n; i++ {
		rec := trimTrailing(recs[i], m.trailing)
		switch m.compression {
		case 1:
			out = append(out, rec...)
		case 2:
			out = palmDoc(out, rec)
		default:
			// 17480 is HUFF/CDIC, which is not read yet.
			return out, n + 1
		}
		if m.textLength > 0 && len(out) >= m.textLength {
			break
		}
	}
	if m.textLength > 0 && len(out) > m.textLength {
		out = out[:m.textLength]
	}
	return out, n + 1
}

// trimTrailing removes the entries a record carries after its data: one per
// flag bit above the first, then the multibyte overlap bit zero stands for.
func trimTrailing(rec []byte, flags uint32) []byte {
	for i := uint(15); i >= 1; i-- {
		if flags&(1<<i) == 0 {
			continue
		}
		n := trailingSize(rec)
		if n <= 0 || n > len(rec) {
			return rec
		}
		rec = rec[:len(rec)-n]
	}
	if flags&1 != 0 && len(rec) > 0 {
		n := int(rec[len(rec)-1]&3) + 1
		if n > len(rec) {
			return nil
		}
		rec = rec[:len(rec)-n]
	}
	return rec
}

// trailingSize reads the last trailing entry's own size, written backwards
// seven bits at a time with the top bit marking where it ends.
func trailingSize(rec []byte) int {
	v, shift := 0, 0
	for i := len(rec) - 1; i >= 0 && shift < 28; i-- {
		c := rec[i]
		v |= int(c&0x7f) << shift
		shift += 7
		if c&0x80 != 0 {
			break
		}
	}
	return v
}

// palmDoc appends one record decompressed, which is LZ77 over the output so
// far.
func palmDoc(out, in []byte) []byte {
	for i := 0; i < len(in); {
		c := in[i]
		i++
		switch {
		case c == 0 || (c >= 0x09 && c < 0x80):
			out = append(out, c)
		case c < 0x09:
			n := int(c)
			if i+n > len(in) {
				n = len(in) - i
			}
			out = append(out, in[i:i+n]...)
			i += n
		case c < 0xc0:
			if i >= len(in) {
				return out
			}
			v := int(c)<<8 | int(in[i])
			i++
			dist, n := (v>>3)&0x7ff, (v&7)+3
			if dist == 0 || dist > len(out) {
				continue
			}
			// A run may reach past the end, which is how a repeat is written.
			for k := 0; k < n; k++ {
				out = append(out, out[len(out)-dist])
			}
		default:
			out = append(out, ' ', c^0x80)
		}
	}
	return out
}

// imageType recognizes the pictures a record may hold.
func imageType(rec []byte) string {
	switch {
	case len(rec) < 9:
		return ""
	case rec[0] == 0xff && rec[1] == 0xd8:
		return "image/jpeg"
	case string(rec[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case string(rec[:6]) == "GIF87a" || string(rec[:6]) == "GIF89a":
		return "image/gif"
	case string(rec[:2]) == "BM":
		return "image/bmp"
	}
	return ""
}

// cp1252High is where Windows-1252 and Latin-1 differ.
var cp1252High = [32]rune{
	0x20ac, 0x81, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021,
	0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8d, 0x017d, 0x8f,
	0x90, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
	0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0x9d, 0x017e, 0x0178,
}

// fromCP1252 converts the encoding an older book is written in.
func fromCP1252(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	for _, c := range b {
		switch {
		case c < 0x80:
			out = append(out, c)
		case c < 0xa0:
			out = utf8.AppendRune(out, cp1252High[c-0x80])
		default:
			out = utf8.AppendRune(out, rune(c))
		}
	}
	return out
}
