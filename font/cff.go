package font

import (
	"fmt"

	"github.com/gen2brain/folio/raster"
)

// cffFont is a Compact Font Format program, bare or inside an sfnt.
type cffFont struct {
	data []byte

	charStrings [][]byte
	globalSubrs [][]byte
	localSubrs  [][]byte
	strings     [][]byte

	// charset maps a glyph to its name identifier, or to its CID in a CID
	// keyed font.
	charset []uint16
	enc     *[256]string

	defaultWidth float32
	nominalWidth float32

	// A CID keyed font has one private dictionary per font dict, and may
	// have one font matrix per font dict as well.
	isCID    bool
	fdSelect []uint8
	fdPriv   []privateDict
	fdMatrix []raster.Matrix

	matrix raster.Matrix
	upem   int
}

type privateDict struct {
	subrs        [][]byte
	defaultWidth float32
	nominalWidth float32
}

// parseCFF reads a CFF program.
func parseCFF(data []byte) (*Font, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: CFF is %d bytes", ErrInvalid, len(data))
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil, fmt.Errorf("%w: CFF header size %d", ErrInvalid, hdrSize)
	}
	c := &cffFont{data: data, matrix: raster.Scale(0.001, 0.001), upem: 1000}

	at := hdrSize
	_, at, err := readIndex(data, at) // name index
	if err != nil {
		return nil, err
	}
	tops, at, err := readIndex(data, at)
	if err != nil {
		return nil, err
	}
	c.strings, at, err = readIndex(data, at)
	if err != nil {
		return nil, err
	}
	c.globalSubrs, _, err = readIndex(data, at)
	if err != nil {
		return nil, err
	}
	if len(tops) == 0 {
		return nil, fmt.Errorf("%w: CFF has no top dictionary", ErrInvalid)
	}

	top := parseDict(tops[0])
	if m := top[key12(7)]; len(m) == 6 {
		c.matrix = raster.Matrix{
			A: float32(m[0]), B: float32(m[1]), C: float32(m[2]),
			D: float32(m[3]), E: float32(m[4]), F: float32(m[5]),
		}
		if c.matrix.A != 0 {
			c.upem = int(1/c.matrix.A + 0.5)
		}
	}
	c.isCID = top[key12(30)] != nil

	csOff := dictInt(top, 17, -1)
	if csOff <= 0 || csOff >= len(data) {
		return nil, fmt.Errorf("%w: CFF has no charstrings", ErrInvalid)
	}
	c.charStrings, _, err = readIndex(data, csOff)
	if err != nil {
		return nil, err
	}

	if p := top[18]; len(p) == 2 {
		pd := c.readPrivate(int(p[1]), int(p[0]))
		c.localSubrs, c.defaultWidth, c.nominalWidth = pd.subrs, pd.defaultWidth, pd.nominalWidth
	}
	c.readCharset(dictInt(top, 15, 0))
	if !c.isCID {
		c.readEncoding(dictInt(top, 16, 0))
	} else {
		c.readCIDPrivates(top, top[key12(7)] != nil)
	}

	f := &Font{
		Kind:       KindCFF,
		cff:        c,
		glyphs:     len(c.charStrings),
		UnitsPerEm: c.upem,
		Matrix:     c.matrix,
		CID:        c.isCID,
		Ascent:     defaultAscent,
		Descent:    defaultDescent,
	}
	if b := top[5]; len(b) == 4 {
		f.emBox([]float32{float32(b[0]), float32(b[1]), float32(b[2]), float32(b[3])}, c.matrix.D)
	}
	return f, nil
}

// useFDMatrix moves the font dict matrices into the font's own, which is what
// FreeType does and therefore what a PDF font dictionary is written against.
func (c *cffFont) useFDMatrix() {
	if len(c.fdMatrix) == 0 {
		return
	}
	c.matrix = c.fdMatrix[0]
	if c.matrix.A != 0 {
		c.upem = int(1/c.matrix.A + 0.5)
	}
	same := true
	for _, m := range c.fdMatrix[1:] {
		if m != c.matrix {
			same = false
			break
		}
	}
	if same {
		c.fdMatrix = nil
	}
}

// matrixFor returns the transform from the outline of a glyph to the space
// the font's own matrix expects, which is the identity unless the font dicts
// disagree about their matrices.
func (c *cffFont) matrixFor(gid int) (raster.Matrix, bool) {
	if len(c.fdMatrix) == 0 || gid < 0 || gid >= len(c.fdSelect) {
		return raster.Identity, false
	}
	fd := int(c.fdSelect[gid])
	if fd >= len(c.fdMatrix) || c.fdMatrix[fd] == c.matrix {
		return raster.Identity, false
	}
	inv, ok := c.matrix.Invert()
	if !ok {
		return raster.Identity, false
	}
	return raster.Concat(c.fdMatrix[fd], inv), true
}

// readIndex reads a CFF INDEX and returns its elements and the offset past it.
func readIndex(b []byte, at int) ([][]byte, int, error) {
	if at < 0 || at+2 > len(b) {
		return nil, at, fmt.Errorf("%w: CFF index at %d", ErrInvalid, at)
	}
	count := be16(b, at)
	if count == 0 {
		return nil, at + 2, nil
	}
	if at+3 > len(b) {
		return nil, at, fmt.Errorf("%w: CFF index at %d", ErrInvalid, at)
	}
	offSize := int(b[at+2])
	if offSize < 1 || offSize > 4 {
		return nil, at, fmt.Errorf("%w: CFF offset size %d", ErrInvalid, offSize)
	}
	base := at + 3
	end := base + (count+1)*offSize
	if end > len(b) {
		return nil, at, fmt.Errorf("%w: CFF index runs past the end", ErrInvalid)
	}
	readOff := func(i int) int {
		v := 0
		for j := 0; j < offSize; j++ {
			v = v<<8 | int(b[base+i*offSize+j])
		}
		return v
	}
	dataStart := end - 1
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		lo, hi := dataStart+readOff(i), dataStart+readOff(i+1)
		if lo < dataStart || hi < lo || hi > len(b) {
			out = append(out, nil)
			continue
		}
		out = append(out, b[lo:hi])
	}
	last := dataStart + readOff(count)
	if last < end || last > len(b) {
		last = len(b)
	}
	return out, last, nil
}

// key12 is how a two byte operator is keyed in a parsed dictionary.
func key12(op int) int { return 1200 + op }

// parseDict reads a CFF DICT into operator to operand lists.
func parseDict(b []byte) map[int][]float64 {
	out := map[int][]float64{}
	var operands []float64
	for i := 0; i < len(b); {
		v := int(b[i])
		switch {
		case v <= 21:
			op := v
			i++
			if v == 12 && i < len(b) {
				op = key12(int(b[i]))
				i++
			}
			out[op] = append([]float64(nil), operands...)
			operands = operands[:0]
		case v == 28:
			if i+3 > len(b) {
				return out
			}
			operands = append(operands, float64(int16(be16(b, i+1))))
			i += 3
		case v == 29:
			if i+5 > len(b) {
				return out
			}
			operands = append(operands, float64(int32(be32(b, i+1))))
			i += 5
		case v == 30:
			f, n := parseRealDict(b[i+1:])
			operands = append(operands, f)
			i += 1 + n
		case v >= 32 && v <= 246:
			operands = append(operands, float64(v-139))
			i++
		case v >= 247 && v <= 250:
			if i+2 > len(b) {
				return out
			}
			operands = append(operands, float64((v-247)*256+int(b[i+1])+108))
			i += 2
		case v >= 251 && v <= 254:
			if i+2 > len(b) {
				return out
			}
			operands = append(operands, float64(-(v-251)*256-int(b[i+1])-108))
			i += 2
		default:
			i++
		}
		if len(operands) > 48 {
			operands = operands[:48]
		}
	}
	return out
}

// parseRealDict reads the nibble encoded real of a DICT.
func parseRealDict(b []byte) (float64, int) {
	var s []byte
	for i := 0; i < len(b); i++ {
		for _, nib := range [2]byte{b[i] >> 4, b[i] & 0xf} {
			switch {
			case nib <= 9:
				s = append(s, '0'+nib)
			case nib == 0xa:
				s = append(s, '.')
			case nib == 0xb:
				s = append(s, 'E')
			case nib == 0xc:
				s = append(s, 'E', '-')
			case nib == 0xe:
				s = append(s, '-')
			case nib == 0xf:
				return parseFloat(string(s)), i + 1
			}
		}
	}
	return parseFloat(string(s)), len(b)
}

func dictInt(d map[int][]float64, op int, def int) int {
	if v := d[op]; len(v) > 0 {
		return int(v[len(v)-1])
	}
	return def
}

// readPrivate reads a private dictionary and the local subroutines it names.
func (c *cffFont) readPrivate(off, size int) privateDict {
	var pd privateDict
	if off <= 0 || size <= 0 || off >= len(c.data) {
		return pd
	}
	if off+size > len(c.data) {
		size = len(c.data) - off
	}
	d := parseDict(c.data[off : off+size])
	pd.defaultWidth = float32(dictFloat(d, 20, 0))
	pd.nominalWidth = float32(dictFloat(d, 21, 0))
	if s := dictInt(d, 19, 0); s > 0 && off+s < len(c.data) {
		pd.subrs, _, _ = readIndex(c.data, off+s)
	}
	return pd
}

func dictFloat(d map[int][]float64, op int, def float64) float64 {
	if v := d[op]; len(v) > 0 {
		return v[len(v)-1]
	}
	return def
}

// readCharset reads the glyph name identifiers, or the CIDs of a CID font.
func (c *cffFont) readCharset(off int) {
	n := len(c.charStrings)
	c.charset = make([]uint16, n)
	switch off {
	case 0: // ISOAdobe: identity
		for i := range c.charset {
			c.charset[i] = uint16(i)
		}
		return
	case 1, 2: // Expert sets, which no real font uses
		for i := range c.charset {
			c.charset[i] = uint16(i)
		}
		return
	}
	b := c.data
	if off < 0 || off >= len(b) {
		return
	}
	format := b[off]
	at := off + 1
	c.charset[0] = 0
	gid := 1
	switch format {
	case 0:
		for gid < n && at+2 <= len(b) {
			c.charset[gid] = uint16(be16(b, at))
			at += 2
			gid++
		}
	case 1, 2:
		step := 3
		if format == 2 {
			step = 4
		}
		for gid < n && at+step <= len(b) {
			first := be16(b, at)
			left := int(b[at+2])
			if format == 2 {
				left = be16(b, at+2)
			}
			for i := 0; i <= left && gid < n; i++ {
				c.charset[gid] = uint16(first + i)
				gid++
			}
			at += step
		}
	}
}

// readEncoding reads the built in code to glyph table of a non-CID font.
func (c *cffFont) readEncoding(off int) {
	var enc [256]string
	switch off {
	case 0:
		enc = standardEncoding
		c.enc = &enc
		return
	case 1:
		enc = expertEncoding
		c.enc = &enc
		return
	}
	b := c.data
	if off < 0 || off >= len(b) {
		return
	}
	format := b[off]
	at := off + 1
	switch format &^ 0x80 {
	case 0:
		n := int(b[at])
		at++
		for i := 1; i <= n && at < len(b); i++ {
			enc[b[at]] = c.glyphName(i)
			at++
		}
	case 1:
		n := int(b[at])
		at++
		gid := 1
		for i := 0; i < n && at+2 <= len(b); i++ {
			first := int(b[at])
			left := int(b[at+1])
			at += 2
			for j := 0; j <= left && first+j < 256; j++ {
				enc[first+j] = c.glyphName(gid)
				gid++
			}
		}
	}
	if format&0x80 != 0 && at < len(b) {
		n := int(b[at])
		at++
		for i := 0; i < n && at+3 <= len(b); i++ {
			code := int(b[at])
			sid := be16(b, at+1)
			at += 3
			enc[code] = c.sidName(sid)
		}
	}
	c.enc = &enc
}

// readCIDPrivates reads the per font dictionaries of a CID keyed font.
func (c *cffFont) readCIDPrivates(top map[int][]float64, topMatrix bool) {
	fdaOff := dictInt(top, key12(36), 0)
	if fdaOff > 0 && fdaOff < len(c.data) {
		fds, _, err := readIndex(c.data, fdaOff)
		if err == nil {
			for _, fd := range fds {
				d := parseDict(fd)
				var pd privateDict
				if p := d[18]; len(p) == 2 {
					pd = c.readPrivate(int(p[1]), int(p[0]))
				}
				c.fdPriv = append(c.fdPriv, pd)

				m := c.matrix
				if v := d[key12(7)]; len(v) == 6 {
					sub := raster.Matrix{
						A: float32(v[0]), B: float32(v[1]), C: float32(v[2]),
						D: float32(v[3]), E: float32(v[4]), F: float32(v[5]),
					}
					m = sub
					if topMatrix {
						m = raster.Concat(sub, c.matrix)
					}
				}
				c.fdMatrix = append(c.fdMatrix, m)
			}
		}
	}
	c.useFDMatrix()

	c.fdSelect = make([]uint8, len(c.charStrings))
	off := dictInt(top, key12(37), 0)
	b := c.data
	if off <= 0 || off >= len(b) {
		return
	}
	switch b[off] {
	case 0:
		for i := range c.fdSelect {
			if off+1+i < len(b) {
				c.fdSelect[i] = b[off+1+i]
			}
		}
	case 3:
		n := be16(b, off+1)
		at := off + 3
		for i := 0; i < n && at+5 <= len(b); i++ {
			first := be16(b, at)
			fd := b[at+2]
			next := be16(b, at+3)
			for g := first; g < next && g < len(c.fdSelect); g++ {
				c.fdSelect[g] = fd
			}
			at += 3
		}
	}
}

// sidName resolves a string identifier.
func (c *cffFont) sidName(sid int) string {
	if sid < len(cffStandardStrings) {
		return cffStandardStrings[sid]
	}
	if i := sid - len(cffStandardStrings); i >= 0 && i < len(c.strings) {
		return string(c.strings[i])
	}
	return ""
}

func (c *cffFont) glyphName(gid int) string {
	if c.isCID || gid < 0 || gid >= len(c.charset) {
		return ""
	}
	return c.sidName(int(c.charset[gid]))
}

// gidForCID finds the glyph a CID names, which in a CID keyed font is the
// charset read backwards.
func (c *cffFont) gidForCID(cid int) int {
	for gid, v := range c.charset {
		if int(v) == cid {
			return gid
		}
	}
	if cid < len(c.charStrings) {
		return cid
	}
	return 0
}

func (c *cffFont) encoding() *[256]string { return c.enc }

// privateFor returns the private dictionary that governs a glyph.
func (c *cffFont) privateFor(gid int) ([][]byte, float32, float32) {
	if c.isCID && gid >= 0 && gid < len(c.fdSelect) {
		if fd := int(c.fdSelect[gid]); fd < len(c.fdPriv) {
			p := c.fdPriv[fd]
			return p.subrs, p.defaultWidth, p.nominalWidth
		}
	}
	return c.localSubrs, c.defaultWidth, c.nominalWidth
}
