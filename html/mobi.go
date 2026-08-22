package html

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strconv"
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

	all := recs
	m := mobiHeader(recs[0])
	first := m.image
	recs, m = kf8(recs, m)
	if m.encryption != 0 {
		return nil, fmt.Errorf("%w: the book is encrypted", ErrUnsupported)
	}
	text, next := m.text(recs)
	if len(text) == 0 {
		return nil, fmt.Errorf("%w: no text", ErrInvalid)
	}
	if !utf8.Valid(text) {
		text = fromCP1252(text)
	}

	parts := map[string][]byte{mobiIndex: text}
	item := Item{Path: mobiIndex, Type: "text/html", Linear: true}
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
	// They belong to the file rather than to either half of a hybrid one, so
	// they are counted from the record the first header names and over every
	// record there is.
	if first <= 0 || first >= len(all) {
		first, all = next, recs
	}
	n := 1
	for i := first; i < len(all); i++ {
		t := imageType(all[i])
		if t == "" {
			continue
		}
		path := mobiPicture(n)
		parts[path] = all[i]
		d.manifest = append(d.manifest, Item{Path: path, Type: t})
		n++
	}
	if m.ncx > 0 {
		d.outline, d.filepos = mobiOutline(recs, m.ncx)
	}
	d.read = func(p string) ([]byte, error) {
		if v, ok := parts[p]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d, nil
}

// kf8 picks the KF8 half of a hybrid file, which is a whole second book after
// a record saying BOUNDARY. The record numbers a KF8 header holds count from
// there, so the records before it are dropped rather than skipped.
func kf8(recs [][]byte, m mobi) ([][]byte, mobi) {
	b := m.boundary
	if b <= 1 || b >= len(recs) || string(recs[b-1][:min(8, len(recs[b-1]))]) != "BOUNDARY" {
		return recs, m
	}
	k := mobiHeader(recs[b])
	if k.textLength <= 0 {
		return recs, m
	}
	return recs[b:], k
}

// maxBookBytes bounds a PalmDB, which is read whole.
const maxBookBytes = 1 << 30

// notSet is what a MOBI header holds where it names no record.
const notSet = 0xffffffff

// mobiRecords cuts the file into its records. An offset that does not stay
// inside the file leaves that record empty rather than dropping it: the
// header names the records it cares about by number, so the numbering has to
// survive a broken entry.
func mobiRecords(b []byte) [][]byte {
	n := int(binary.BigEndian.Uint16(b[76:]))
	if n <= 0 || 78+8*n > len(b) {
		return nil
	}
	offs := make([]int, n)
	for i := range offs {
		offs[i] = int(binary.BigEndian.Uint32(b[78+8*i:]))
	}
	// The list ends before the first record, which is what bounds it when
	// the count is larger than the file can hold.
	if m := (offs[0] - 78) / 8; m > 0 && m < n {
		n, offs = m, offs[:m]
	}
	low := 78 + 8*n
	ends := make([]int, n)
	end := len(b)
	for i := n - 1; i >= 0; i-- {
		ends[i] = end
		if offs[i] >= low && offs[i] <= len(b) {
			end = offs[i]
		}
	}
	out := make([][]byte, n)
	for i := range out {
		if offs[i] >= low && offs[i] <= ends[i] && ends[i] <= len(b) {
			out[i] = b[offs[i]:ends[i]]
		}
	}
	if len(out[0]) == 0 {
		return nil
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
	ncx         int
	image       int
	huff, nhuf  int
	encryption  int
	// boundary is the record the KF8 half of a hybrid file starts at.
	boundary int
	meta     Metadata
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
	m.encryption = int(binary.BigEndian.Uint16(rec[12:]))
	// Every field below is inside the fixed part of the MOBI header, so one
	// bound covers them all.
	if len(rec) < 0x84 || string(rec[16:20]) != "MOBI" {
		return m
	}
	hdr := int(binary.BigEndian.Uint32(rec[20:]))
	if v := int32(binary.BigEndian.Uint32(rec[28:])); v > 0 {
		m.encoding = int(v)
	}
	if hdr >= 0x6c && len(rec) >= 0x70 {
		if v := binary.BigEndian.Uint32(rec[0x6c:]); v != notSet {
			m.image = int(v)
		}
	}
	if hdr >= 0x74 && len(rec) >= 0x78 {
		m.huff = int(binary.BigEndian.Uint32(rec[0x70:]))
		m.nhuf = int(binary.BigEndian.Uint32(rec[0x74:]))
	}
	// The extra flags are two bytes at 242, the NCX index the four at 244,
	// and a header may stop between them.
	if hdr >= 0xe4 && len(rec) >= 0xf4 {
		m.trailing = uint32(binary.BigEndian.Uint16(rec[0xf2:]))
	}
	if hdr >= 0xe8 && len(rec) >= 0xf8 {
		if v := binary.BigEndian.Uint32(rec[0xf4:]); v != notSet {
			m.ncx = int(v)
		}
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
	exthBoundary    = 121
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
		case exthBoundary:
			m.boundary = int(exthNumber(rec[p+8 : p+size]))
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
	var h *huffcdic
	if m.compression == 17480 {
		if h = readHuffcdic(recs, m.huff, m.nhuf); h == nil {
			return nil, n + 1
		}
	}
	var out []byte
	for i := 1; i <= n; i++ {
		rec := trimTrailing(recs[i], m.trailing)
		switch m.compression {
		case 1:
			out = append(out, rec...)
		case 2:
			out = palmDoc(out, rec)
		case 17480:
			if h == nil {
				return out, n + 1
			}
			out = h.unpack(out, rec, 0)
		default:
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

// exthNumber reads a numeric EXTH record, written in as many bytes as it
// needs.
func exthNumber(b []byte) uint32 {
	if len(b) == 0 || len(b) > 4 {
		return 0
	}
	var v uint32
	for _, c := range b {
		v = v<<8 | uint32(c)
	}
	return v
}

// mobiMarkup rewrites the markup MOBI carries into HTML. A page break is the
// only one of its own tags that means anything to layout; the rest are
// dropped because HTML5 closes no tag it does not know, so a self closing one
// would hold the tree open to the end of the book. A place named by a byte
// offset becomes an anchor, and a link to one an href.
func mobiMarkup(b []byte, want []int) []byte {
	marks := mobiMarks(b, want)
	if len(marks) == 0 && !bytes.Contains(b, []byte("<mbp:")) && !bytes.Contains(b, []byte("<idx:")) {
		return b
	}
	out := make([]byte, 0, len(b)+len(b)/8)
	for i := 0; i < len(b); {
		for len(marks) > 0 && marks[0] <= i {
			if marks[0] == i {
				out = append(out, `<a id="`...)
				out = append(out, filepos(i)...)
				out = append(out, `"></a>`...)
			}
			marks = marks[1:]
		}
		if b[i] != '<' {
			out = append(out, b[i])
			i++
			continue
		}
		end := bytes.IndexByte(b[i:], '>')
		if end < 0 {
			return append(out, b[i:]...)
		}
		tag := b[i : i+end+1]
		i += end + 1
		switch {
		case mobiOwnTag(tag):
			if bytes.HasPrefix(tag, []byte("<mbp:pagebreak")) {
				out = append(out, `<div style="page-break-after:always"></div>`...)
			} else if !bytes.HasSuffix(tag, []byte("/>")) {
				out = append(out, tag...)
			}
		default:
			out = append(out, mobiLink(tag)...)
		}
	}
	return out
}

// mobiMarks is where an anchor has to go: every offset the index named, and
// every one a link in the markup points at, in order and without repeats.
func mobiMarks(b []byte, want []int) []int {
	marks := append([]int(nil), want...)
	for i := 0; i+7 < len(b); {
		j := bytes.Index(b[i:], []byte("filepos="))
		if j < 0 {
			break
		}
		i += j + 8
		v, n := 0, 0
		for ; i+n < len(b) && b[i+n] >= '0' && b[i+n] <= '9' && n < 10; n++ {
			v = v*10 + int(b[i+n]-'0')
		}
		if n > 0 && v < len(b) {
			marks = append(marks, v)
		}
	}
	slices.Sort(marks)
	return slices.Compact(marks)
}

// mobiLink rewrites the three ways MOBI names something into the two HTML
// has: the offset an anchor points at becomes a fragment, and a picture named
// by its record number, either way the format writes one, becomes a source.
func mobiLink(tag []byte) []byte {
	if i := bytes.Index(tag, []byte("filepos=")); i >= 0 {
		j := i + 8
		for j < len(tag) && tag[j] >= '0' && tag[j] <= '9' {
			j++
		}
		if v, err := strconv.Atoi(string(tag[i+8 : j])); err == nil {
			out := append([]byte(nil), tag[:i]...)
			out = append(out, `href="#`...)
			out = append(out, filepos(v)...)
			out = append(out, '"')
			tag = append(out, tag[j:]...)
		}
	}
	if i := bytes.Index(tag, []byte("recindex=")); i >= 0 {
		j := i + 9
		for j < len(tag) && (tag[j] == '"' || tag[j] == '\'') {
			j++
		}
		k := j
		for k < len(tag) && tag[k] >= '0' && tag[k] <= '9' {
			k++
		}
		if v, err := strconv.Atoi(string(tag[j:k])); err == nil && v > 0 {
			for k < len(tag) && (tag[k] == '"' || tag[k] == '\'') {
				k++
			}
			out := append([]byte(nil), tag[:i]...)
			out = append(out, `src="`...)
			out = append(out, mobiPicture(v)...)
			out = append(out, '"')
			tag = append(out, tag[k:]...)
		}
	}
	if i := bytes.Index(tag, []byte("kindle:embed:")); i >= 0 {
		j := i + 13
		k := j
		for k < len(tag) && tag[k] != '"' && tag[k] != '\'' && tag[k] != '?' {
			k++
		}
		e := k
		for e < len(tag) && tag[e] != '"' && tag[e] != '\'' {
			e++
		}
		if v, ok := base32Number(tag[j:k]); ok && v > 0 {
			out := append([]byte(nil), tag[:i]...)
			out = append(out, mobiPicture(int(v))...)
			tag = append(out, tag[e:]...)
		}
	}
	return tag
}

// base32Number reads the number KF8 names a resource by, whose digits run 0
// to 9 and then A to V.
func base32Number(b []byte) (uint32, bool) {
	if len(b) == 0 || len(b) > 6 {
		return 0, false
	}
	var v uint32
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
			v = v*32 + uint32(c-'0')
		case c >= 'A' && c <= 'V':
			v = v*32 + uint32(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// mobiOwnTag reports a tag in one of the namespaces MOBI puts its own
// elements in.
func mobiOwnTag(tag []byte) bool {
	name := tag[1:]
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	for _, ns := range [][]byte{[]byte("mbp:"), []byte("idx:")} {
		if bytes.HasPrefix(name, ns) {
			return true
		}
	}
	return false
}

// mobiOutline builds the table of contents from the index the header names,
// and returns the places it points at.
func mobiOutline(recs [][]byte, at int) ([]Outline, []int) {
	entries, cncx := readIndex(recs, at)
	if len(entries) == 0 {
		return nil, nil
	}
	var flat []Outline
	var level []int
	var pos []int
	for i := range entries {
		e := &entries[i]
		title := e.label
		if v, ok := e.value(ncxLabel, 0); ok {
			if s := cncxString(cncx, v); s != "" {
				title = s
			}
		}
		title = squash(title)
		if title == "" {
			continue
		}
		out := Outline{Title: title, Path: mobiIndex}
		if v, ok := e.value(ncxFilepos, 0); ok {
			out.Fragment = filepos(int(v))
			pos = append(pos, int(v))
		}
		l := 0
		if v, ok := e.value(ncxLevel, 0); ok && v < 16 {
			l = int(v)
		}
		flat = append(flat, out)
		level = append(level, l+1)
	}
	return nest(flat, level), pos
}

// filepos is the anchor a byte offset into the text is named by, which is how
// MOBI writes a link to a place in the book.
func filepos(at int) string { return fmt.Sprintf("filepos%010d", at) }

// mobiPicture is what a picture record is called, which is its number among
// them counting from one.
func mobiPicture(n int) string { return fmt.Sprintf("%05d", n) }
