package font

import (
	"bytes"
	"fmt"
	"strconv"
	"sync"

	"github.com/gen2brain/folio/raster"
)

// type1Font is a Type 1 font program, in PFB, PFA or bare form.
type type1Font struct {
	charstrings map[string][]byte
	names       []string
	subrs       [][]byte
	enc         *[256]string
	matrix      raster.Matrix
	bbox        []float32

	// widths is filled by running a charstring, which is the only place a
	// Type 1 glyph's advance is written down, so it is a cache and needs a
	// lock of its own.
	widthMu sync.Mutex
	widths  map[string]float32
}

// parseType1 reads a Type 1 program: a clear text header, then an eexec
// encrypted body holding the charstrings.
func parseType1(data []byte) (*Font, error) {
	data = unwrapPFB(data)
	t := &type1Font{
		charstrings: map[string][]byte{},
		widths:      map[string]float32{},
		matrix:      raster.Scale(0.001, 0.001),
	}

	i := indexOf(data, "eexec")
	if i < 0 {
		return nil, fmt.Errorf("%w: Type1 font has no eexec", ErrInvalid)
	}
	clear := data[:i]
	t.readMatrix(clear)
	t.readEncoding(clear)

	body := data[i+5:]
	for len(body) > 0 && (body[0] == '\r' || body[0] == '\n' || body[0] == ' ' || body[0] == '\t') {
		body = body[1:]
	}
	if isHex(body) {
		body = unhex(body)
	}
	private := eexec(body, 55665, 4)

	lenIV := 4
	if j := indexOf(private, "/lenIV"); j >= 0 {
		if v, ok := readInt(private[j+6:]); ok && v >= 0 && v <= 16 {
			lenIV = v
		}
	}
	t.readSubrs(private, lenIV)
	t.readCharstrings(private, lenIV)
	if len(t.charstrings) == 0 {
		return nil, fmt.Errorf("%w: Type1 font has no charstrings", ErrInvalid)
	}

	f := &Font{
		Kind:       KindType1,
		type1:      t,
		glyphs:     len(t.names),
		UnitsPerEm: 1000,
		Matrix:     t.matrix,
		Ascent:     defaultAscent,
		Descent:    defaultDescent,
	}
	if f.Matrix.A != 0 {
		f.UnitsPerEm = int(1/f.Matrix.A + 0.5)
	}
	f.emBox(t.bbox, f.Matrix.D)
	return f, nil
}

// unwrapPFB strips the segment headers of the binary container.
func unwrapPFB(b []byte) []byte {
	if len(b) < 6 || b[0] != 0x80 {
		return b
	}
	var out []byte
	for len(b) >= 6 && b[0] == 0x80 {
		if b[1] == 3 {
			break
		}
		n := int(b[2]) | int(b[3])<<8 | int(b[4])<<16 | int(b[5])<<24
		b = b[6:]
		if n < 0 || n > len(b) {
			n = len(b)
		}
		out = append(out, b[:n]...)
		b = b[n:]
	}
	return out
}

func isHex(b []byte) bool {
	for i := 0; i < 4 && i < len(b); i++ {
		if hexVal(b[i]) < 0 {
			return false
		}
	}
	return len(b) >= 4
}

func unhex(b []byte) []byte {
	out := make([]byte, 0, len(b)/2)
	hi := -1
	for _, c := range b {
		v := hexVal(c)
		if v < 0 {
			continue
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	return out
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// eexec decrypts the Type 1 stream cipher and drops the leading random bytes.
func eexec(b []byte, key uint16, skip int) []byte {
	const c1, c2 = 52845, 22719
	r := key
	out := make([]byte, 0, len(b))
	for _, c := range b {
		p := c ^ byte(r>>8)
		r = (uint16(c)+r)*c1 + c2
		out = append(out, p)
	}
	if skip < len(out) {
		return out[skip:]
	}
	return nil
}

func (t *type1Font) readMatrix(b []byte) {
	if v := numbersAfter(b, "/FontMatrix"); len(v) == 6 && v[0] != 0 {
		t.matrix = raster.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
	}
	if v := numbersAfter(b, "/FontBBox"); len(v) == 4 {
		t.bbox = v
	}
}

// numbersAfter reads the bracketed list of numbers a key introduces.
func numbersAfter(b []byte, key string) []float32 {
	i := indexOf(b, key)
	if i < 0 {
		return nil
	}
	rest := b[i+len(key):]
	j := bytes.IndexAny(rest, "[{")
	k := bytes.IndexAny(rest, "]}")
	if j < 0 || k < j {
		return nil
	}
	var v []float32
	for _, f := range bytes.Fields(rest[j+1 : k]) {
		n, err := strconv.ParseFloat(string(f), 32)
		if err != nil {
			return nil
		}
		v = append(v, float32(n))
	}
	return v
}

// readEncoding reads the built in encoding, which is either the standard one
// or a list of put statements.
func (t *type1Font) readEncoding(b []byte) {
	i := indexOf(b, "/Encoding")
	if i < 0 {
		return
	}
	rest := b[i+9:]
	if j := indexOf(rest[:min(len(rest), 40)], "StandardEncoding"); j >= 0 {
		var enc [256]string
		copy(enc[:], standardEncoding[:])
		t.enc = &enc
		return
	}
	var enc [256]string
	for at := 0; ; {
		j := indexOf(rest[at:], "dup ")
		if j < 0 {
			break
		}
		at += j + 4
		code, ok := readInt(rest[at:])
		if !ok {
			continue
		}
		k := bytes.IndexByte(rest[at:], '/')
		if k < 0 {
			break
		}
		name := readName(rest[at+k+1:])
		if code >= 0 && code < 256 && name != "" {
			enc[code] = name
		}
		at += k + 1
		if e := indexOf(rest[at:], "readonly def"); e >= 0 && e < 4 {
			break
		}
	}
	t.enc = &enc
}

func readInt(b []byte) (int, bool) {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\r' || b[i] == '\n' || b[i] == '\t') {
		i++
	}
	start := i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == start {
		return 0, false
	}
	v, err := strconv.Atoi(string(b[start:i]))
	return v, err == nil
}

func readName(b []byte) string {
	i := 0
	for i < len(b) && !isPSDelim(b[i]) {
		i++
	}
	return string(b[:i])
}

func isPSDelim(c byte) bool {
	switch c {
	case ' ', '\r', '\n', '\t', '\f', 0, '/', '(', ')', '<', '>', '[', ']', '{', '}', '%':
		return true
	}
	return false
}

// readSubrs reads the local subroutines out of the decrypted private part.
func (t *type1Font) readSubrs(b []byte, lenIV int) {
	i := indexOf(b, "/Subrs")
	if i < 0 {
		return
	}
	n, ok := readInt(b[i+6:])
	if !ok || n < 0 || n > 65536 {
		return
	}
	t.subrs = make([][]byte, n)
	at := i
	for k := 0; k < n; k++ {
		j := indexOf(b[at:], "dup ")
		if j < 0 {
			return
		}
		at += j + 4
		idx, ok := readInt(b[at:])
		if !ok {
			return
		}
		for at < len(b) && b[at] != ' ' {
			at++
		}
		length, ok := readInt(b[at:])
		if !ok || length < 0 {
			return
		}
		start := skipToBinary(b, at)
		if start < 0 || start+length > len(b) {
			return
		}
		if idx >= 0 && idx < n {
			t.subrs[idx] = eexec(b[start:start+length], 4330, lenIV)
		}
		at = start + length
	}
}

// skipToBinary steps over the RD or -| token that introduces binary data.
func skipToBinary(b []byte, at int) int {
	for at < len(b) && b[at] == ' ' {
		at++
	}
	for at < len(b) && b[at] != ' ' {
		at++ // the length was already read; skip it
	}
	for at < len(b) && b[at] == ' ' {
		at++
	}
	for at < len(b) && b[at] != ' ' {
		at++ // RD, -| or whatever this font calls it
	}
	if at < len(b) {
		at++ // exactly one space before the data
	}
	return at
}

// readCharstrings reads the glyph programs, which are named.
func (t *type1Font) readCharstrings(b []byte, lenIV int) {
	i := indexOf(b, "/CharStrings")
	if i < 0 {
		return
	}
	at := i + 12
	for {
		j := bytes.IndexByte(b[at:], '/')
		if j < 0 {
			return
		}
		at += j + 1
		name := readName(b[at:])
		if name == "" {
			continue
		}
		at += len(name)
		length, ok := readInt(b[at:])
		if !ok || length < 0 {
			if indexOf(b[at:min(at+16, len(b))], "end") >= 0 {
				return
			}
			continue
		}
		start := skipToBinary(b, at)
		if start < 0 || start+length > len(b) {
			return
		}
		if _, dup := t.charstrings[name]; !dup {
			t.charstrings[name] = eexec(b[start:start+length], 4330, lenIV)
			t.names = append(t.names, name)
		}
		at = start + length
	}
}

func (t *type1Font) glyphName(gid int) string {
	if gid < 0 || gid >= len(t.names) {
		return ""
	}
	return t.names[gid]
}

func (t *type1Font) encoding() *[256]string { return t.enc }

func (t *type1Font) path(gid int) *raster.Path {
	name := t.glyphName(gid)
	if name == "" {
		return nil
	}
	r := &t1{f: t}
	r.run(t.charstrings[name], 0)
	r.closeContour()
	t.widthMu.Lock()
	t.widths[name] = r.width
	t.widthMu.Unlock()
	return &r.p
}

func (t *type1Font) advance(gid int) float32 {
	name := t.glyphName(gid)
	if name == "" {
		return 0
	}
	t.widthMu.Lock()
	w, ok := t.widths[name]
	t.widthMu.Unlock()
	if ok {
		return w
	}
	t.path(gid)
	t.widthMu.Lock()
	defer t.widthMu.Unlock()
	return t.widths[name]
}
