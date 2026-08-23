package font

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
)

// The bounds a WOFF2 is read inside.
const (
	maxWOFF2Tables = 512
	maxWOFF2Bytes  = 1 << 28
	maxWOFF2Ratio  = 100
)

// The tags a table directory entry may name by number.
var woff2Tags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post",
	"cvt ", "fpgm", "glyf", "loca", "prep", "CFF ", "VORG", "EBDT",
	"EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea",
	"vmtx", "BASE", "GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH",
	"CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar",
	"gvar", "hsty", "just", "lcar", "mort", "morx", "opbd", "prop",
	"trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

// isWOFF2 reports the container a web font compressed with brotli comes in.
func isWOFF2(b []byte) bool { return len(b) >= 4 && string(b[:4]) == "wOF2" }

type woff2Table struct {
	tag       string
	transform bool
	src       int
	srcLen    int
	dstLen    int
	dstOff    int
	checksum  uint32
}

// woff2Buf reads the packed integers of a WOFF2 stream.
type woff2Buf struct {
	b   []byte
	pos int
	bad bool
}

func (r *woff2Buf) u8() int {
	if r.pos+1 > len(r.b) {
		r.bad = true
		return 0
	}
	v := int(r.b[r.pos])
	r.pos++
	return v
}

func (r *woff2Buf) u16() int {
	if r.pos+2 > len(r.b) {
		r.bad = true
		return 0
	}
	v := int(r.b[r.pos])<<8 | int(r.b[r.pos+1])
	r.pos += 2
	return v
}

func (r *woff2Buf) u32() uint32 {
	if r.pos+4 > len(r.b) {
		r.bad = true
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

func (r *woff2Buf) bytes(n int) []byte {
	if n < 0 || r.pos+n > len(r.b) {
		r.bad = true
		return nil
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v
}

// base255 reads the 255UInt16 of the MicroType Express draft.
func (r *woff2Buf) base255() int {
	switch code := r.u8(); code {
	case 253:
		return r.u16()
	case 254:
		return r.u8() + 253*2
	case 255:
		return r.u8() + 253
	default:
		return code
	}
}

// base128 reads the UIntBase128 a table length is stored as.
func (r *woff2Buf) base128() int {
	v := uint32(0)
	for i := range 5 {
		code := r.u8()
		if r.bad || i == 0 && code == 0x80 || v&0xfe000000 != 0 {
			r.bad = true
			return 0
		}
		v = v<<7 | uint32(code&0x7f)
		if code&0x80 == 0 {
			return int(v)
		}
	}
	r.bad = true
	return 0
}

// parseWOFF2 unpacks a web font into the sfnt inside it.
func parseWOFF2(data []byte) (*Font, error) {
	out, err := woff2SFNT(data)
	if err != nil {
		return nil, err
	}
	return parseSFNT(out)
}

// woff2SFNT rebuilds the sfnt a web font holds. The whole font is one brotli
// stream, and the outlines and the horizontal metrics have a form of their own.
func woff2SFNT(data []byte) ([]byte, error) {
	if len(data) < 48 {
		return nil, fmt.Errorf("%w: %d bytes of WOFF2", ErrInvalid, len(data))
	}
	r := &woff2Buf{b: data, pos: 4}
	flavor := r.u32()
	if int(r.u32()) != len(data) {
		return nil, fmt.Errorf("%w: WOFF2 length", ErrInvalid)
	}
	n := r.u16()
	if n <= 0 || n > maxWOFF2Tables {
		return nil, fmt.Errorf("%w: %d tables", ErrInvalid, n)
	}
	r.pos += 6
	compressed := int(r.u32())
	r.pos += 4
	metaOffset, metaLength := int(r.u32()), int(r.u32())
	r.pos += 4
	privOffset, privLength := int(r.u32()), int(r.u32())
	if flavor == 0x74746366 {
		return nil, fmt.Errorf("%w: a WOFF2 font collection", ErrUnsupported)
	}

	tables, err := woff2Directory(r, n)
	if err != nil {
		return nil, err
	}
	last := tables[len(tables)-1]
	size := last.src + last.srcLen
	if size < 1 || size > maxWOFF2Bytes || size/maxWOFF2Ratio > len(data) {
		return nil, fmt.Errorf("%w: %d bytes of sfnt", ErrInvalid, size)
	}
	if compressed < 0 || r.pos+compressed > len(data) {
		return nil, fmt.Errorf("%w: WOFF2 stream runs past the file", ErrInvalid)
	}
	for _, e := range [][2]int{{metaOffset, metaLength}, {privOffset, privLength}} {
		if e[0] != 0 && (e[0] < 0 || e[1] < 0 || e[0] > len(data) || e[0]+e[1] > len(data)) {
			return nil, fmt.Errorf("%w: WOFF2 block runs past the file", ErrInvalid)
		}
	}

	src, err := brotliDecode(data[r.pos:r.pos+compressed], size)
	if err != nil {
		return nil, err
	}
	if len(src) != size {
		return nil, fmt.Errorf("%w: %d bytes of sfnt, want %d", ErrInvalid, len(src), size)
	}
	return woff2Font(src, tables, flavor)
}

// woff2Directory reads the table directory, in which a tag may be a number
// and a length is packed.
func woff2Directory(r *woff2Buf, n int) ([]*woff2Table, error) {
	tables := make([]*woff2Table, n)
	off := 0
	for i := range tables {
		t := &woff2Table{}
		flag := r.u8()
		if flag&0x3f == 0x3f {
			t.tag = tag(r.bytes(4), 0)
		} else {
			t.tag = woff2Tags[flag&0x3f]
		}
		version := flag >> 6 & 3
		if t.tag == "glyf" || t.tag == "loca" {
			t.transform = version == 0
		} else {
			t.transform = version != 0
		}
		t.dstLen = r.base128()
		t.srcLen = t.dstLen
		if t.transform {
			t.srcLen = r.base128()
			if t.tag == "loca" && t.srcLen != 0 {
				return nil, fmt.Errorf("%w: a transformed loca of %d bytes", ErrInvalid, t.srcLen)
			}
		}
		if r.bad || t.dstLen < 0 || t.srcLen < 0 || t.dstLen > maxWOFF2Bytes || off+t.srcLen < off {
			return nil, fmt.Errorf("%w: WOFF2 table directory", ErrInvalid)
		}
		t.src = off
		off += t.srcLen
		tables[i] = t
	}
	if r.bad {
		return nil, fmt.Errorf("%w: WOFF2 table directory", ErrInvalid)
	}
	return tables, nil
}

// woff2Font rebuilds the sfnt from the decompressed tables.
func woff2Font(src []byte, tables []*woff2Table, flavor uint32) ([]byte, error) {
	n := len(tables)
	sorted := slices.Clone(tables)
	slices.SortFunc(sorted, func(a, b *woff2Table) int { return strings.Compare(a.tag, b.tag) })

	out := make([]byte, 12+16*n)
	binary.BigEndian.PutUint32(out, flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	sr, sel := 16, 0
	for sr*2 <= 16*n {
		sr, sel = sr*2, sel+1
	}
	binary.BigEndian.PutUint16(out[6:], uint16(sr))
	binary.BigEndian.PutUint16(out[8:], uint16(sel))
	binary.BigEndian.PutUint16(out[10:], uint16(16*n-sr))
	entry := make(map[string]int, n)
	for i, t := range sorted {
		copy(out[12+16*i:], t.tag)
		entry[t.tag] = 12 + 16*i
	}
	sum := woff2Sum(out)

	var glyf, loca []byte
	var xMins []int16
	numGlyphs, numHMetrics := 0, 0
	for _, t := range tables {
		if t.src+t.srcLen > len(src) {
			return nil, fmt.Errorf("%w: WOFF2 table %s", ErrInvalid, t.tag)
		}
		switch {
		case t.tag == "hhea" && !t.transform:
			if t.srcLen < 36 {
				return nil, fmt.Errorf("%w: %d bytes of hhea", ErrInvalid, t.srcLen)
			}
			numHMetrics = be16(src[t.src:t.src+t.srcLen], 34)
		case t.tag == "glyf" && t.transform:
			var err error
			if glyf, loca, numGlyphs, xMins, err = woff2Glyf(src[t.src : t.src+t.srcLen]); err != nil {
				return nil, err
			}
		}
	}

	for _, t := range tables {
		var content []byte
		switch {
		case !t.transform:
			content = src[t.src : t.src+t.srcLen]
			if t.tag == "head" {
				if len(content) < 12 {
					return nil, fmt.Errorf("%w: %d bytes of head", ErrInvalid, len(content))
				}
				content = append([]byte(nil), content...)
				binary.BigEndian.PutUint32(content[8:], 0)
			}
		case t.tag == "glyf":
			content = glyf
		case t.tag == "loca":
			if glyf == nil || t.dstLen != len(loca) {
				return nil, fmt.Errorf("%w: %d bytes of loca, want %d", ErrInvalid, t.dstLen, len(loca))
			}
			content = loca
		case t.tag == "hmtx":
			var err error
			if content, err = woff2Hmtx(src[t.src:t.src+t.srcLen], numGlyphs, numHMetrics, xMins); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: a transformed %s table", ErrUnsupported, t.tag)
		}
		t.dstOff = len(out)
		t.checksum = woff2Sum(content)
		out = append(out, content...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		at, ok := entry[t.tag]
		if !ok {
			return nil, fmt.Errorf("%w: the table %s twice", ErrInvalid, t.tag)
		}
		delete(entry, t.tag)
		binary.BigEndian.PutUint32(out[at+4:], t.checksum)
		binary.BigEndian.PutUint32(out[at+8:], uint32(t.dstOff))
		binary.BigEndian.PutUint32(out[at+12:], uint32(len(content)))
		sum += t.checksum + woff2Sum(out[at+4:at+16])
		if len(out) > maxWOFF2Bytes {
			return nil, fmt.Errorf("%w: %d bytes of sfnt", ErrInvalid, len(out))
		}
	}

	for _, t := range tables {
		if t.tag == "head" {
			binary.BigEndian.PutUint32(out[t.dstOff+8:], 0xB1B0AFBA-sum)
		}
	}
	return out, nil
}

// woff2Sum is the table checksum of the sfnt format, over a zero padded table.
func woff2Sum(b []byte) uint32 {
	var sum uint32
	i := 0
	for ; i+4 <= len(b); i += 4 {
		sum += binary.BigEndian.Uint32(b[i:])
	}
	if i < len(b) {
		var v uint32
		for ; i < len(b); i++ {
			v |= uint32(b[i]) << (24 - 8*(i&3))
		}
		sum += v
	}
	return sum
}

type woff2Point struct {
	x, y    int
	onCurve bool
}

// woff2Glyf rebuilds the glyph outlines and the index that points at them
// from the seven sub-streams they are taken apart into.
func woff2Glyf(src []byte) (glyf, loca []byte, numGlyphs int, xMins []int16, err error) {
	fail := func(f string, a ...any) ([]byte, []byte, int, []int16, error) {
		return nil, nil, 0, nil, fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...)
	}
	r := &woff2Buf{b: src}
	r.pos += 2
	flags := r.u16()
	numGlyphs = r.u16()
	long := r.u16()
	if r.bad || numGlyphs > 0xFFFF {
		return fail("WOFF2 glyf header")
	}

	var sub [7]*woff2Buf
	off := 9 * 4
	for i := range sub {
		size := int(r.u32())
		if r.bad || size < 0 || off > len(src) || size > len(src)-off {
			return fail("WOFF2 glyf sub-stream %d", i)
		}
		sub[i] = &woff2Buf{b: src[off : off+size]}
		off += size
	}
	contours, points, flagBytes, glyphs, composites, bboxes, instructions :=
		sub[0], sub[1], sub[2], sub[3], sub[4], sub[5], sub[6]

	var overlap []byte
	if flags&1 != 0 {
		size := (numGlyphs + 7) >> 3
		if size > len(src)-off {
			return fail("WOFF2 overlap bitmap")
		}
		overlap = src[off : off+size]
	}

	bitmap := bboxes.bytes((numGlyphs + 31) >> 5 << 2)
	if bboxes.bad {
		return fail("WOFF2 bounding box bitmap")
	}

	xMins = make([]int16, numGlyphs)
	offsets := make([]int, numGlyphs+1)
	glyf = make([]byte, 0, 4*len(src))
	var pts []woff2Point
	for i := range numGlyphs {
		haveBox := bitmap[i>>3]&(0x80>>(i&7)) != 0
		start := len(glyf)
		offsets[i] = start
		switch n := contours.u16(); {
		case contours.bad:
			return fail("WOFF2 contour count")
		case n == 0xFFFF:
			if !haveBox {
				return fail("a composite glyph with no bounding box")
			}
			size, hasInstructions := woff2Composite(composites)
			if size < 0 {
				return fail("WOFF2 composite glyph")
			}
			instrLen := 0
			if hasInstructions {
				instrLen = glyphs.base255()
			}
			glyf = append(glyf, 0xFF, 0xFF)
			glyf = append(glyf, bboxes.bytes(8)...)
			glyf = append(glyf, composites.bytes(size)...)
			if hasInstructions {
				glyf = binary.BigEndian.AppendUint16(glyf, uint16(instrLen))
				glyf = append(glyf, instructions.bytes(instrLen)...)
			}
		case n > 0:
			total := 0
			ends := make([]int, n)
			for j := range n {
				ends[j] = points.base255()
				total += ends[j]
				if points.bad || total > 0xFFFF {
					return fail("WOFF2 point count")
				}
			}
			flagsIn := flagBytes.bytes(total)
			if flagBytes.bad {
				return fail("WOFF2 point flags")
			}
			pts = pts[:0]
			pts, err = woff2Triplets(glyphs, flagsIn, pts)
			if err != nil {
				return nil, nil, 0, nil, err
			}
			instrLen := glyphs.base255()
			if glyphs.bad || instrLen < 0 {
				return fail("WOFF2 instruction length")
			}

			box := make([]byte, 8)
			if haveBox {
				copy(box, bboxes.bytes(8))
			} else {
				woff2Bbox(pts, box)
			}
			glyf = binary.BigEndian.AppendUint16(glyf, uint16(n))
			glyf = append(glyf, box...)
			end := -1
			for _, c := range ends {
				end += c
				glyf = binary.BigEndian.AppendUint16(glyf, uint16(end))
			}
			glyf = binary.BigEndian.AppendUint16(glyf, uint16(instrLen))
			glyf = append(glyf, instructions.bytes(instrLen)...)
			glyf = woff2Points(glyf, pts, overlap != nil && overlap[i>>3]&(0x80>>(i&7)) != 0)
		default:
			if haveBox {
				return fail("an empty glyph with a bounding box")
			}
		}
		if len(glyf) > start+2 {
			xMins[i] = int16(be16(glyf, start+2))
		}
		for len(glyf)%4 != 0 {
			glyf = append(glyf, 0)
		}
		if len(glyf) > maxWOFF2Bytes {
			return fail("%d bytes of glyf", len(glyf))
		}
	}
	for _, b := range sub {
		if b.bad {
			return fail("WOFF2 glyf sub-stream ends early")
		}
	}
	offsets[numGlyphs] = len(glyf)

	if long != 0 {
		loca = make([]byte, 0, 4*(numGlyphs+1))
		for _, v := range offsets {
			loca = binary.BigEndian.AppendUint32(loca, uint32(v))
		}
	} else {
		loca = make([]byte, 0, 2*(numGlyphs+1))
		for _, v := range offsets {
			loca = binary.BigEndian.AppendUint16(loca, uint16(v>>1))
		}
	}
	return glyf, loca, numGlyphs, xMins, nil
}

// woff2Composite measures a composite glyph and reports its instructions.
func woff2Composite(r *woff2Buf) (int, bool) {
	start := r.pos
	instructions := false
	for more := true; more; {
		flags := r.u16()
		more = flags&0x20 != 0
		instructions = instructions || flags&0x100 != 0
		size := 4
		if flags&1 != 0 {
			size = 6
		}
		switch {
		case flags&0x08 != 0:
			size += 2
		case flags&0x40 != 0:
			size += 4
		case flags&0x80 != 0:
			size += 8
		}
		r.pos += size
		if r.bad || r.pos > len(r.b) {
			return -1, false
		}
	}
	n := r.pos - start
	r.pos = start
	return n, instructions
}

// woff2Triplets decodes the points of a simple glyph, each stored as a flag
// and one to four bytes of delta.
func woff2Triplets(r *woff2Buf, flags []byte, out []woff2Point) ([]woff2Point, error) {
	x, y := 0, 0
	for _, flag := range flags {
		onCurve := flag>>7 == 0
		flag &= 0x7f
		var dx, dy int
		switch {
		case flag < 10:
			b0 := r.u8()
			dx, dy = 0, woff2Sign(flag, (int(flag&14)<<7)+b0)
		case flag < 20:
			b0 := r.u8()
			dx, dy = woff2Sign(flag, (int((flag-10)&14)<<7)+b0), 0
		case flag < 84:
			b0, b1 := int(flag)-20, r.u8()
			dx = woff2Sign(flag, 1+(b0&0x30)+(b1>>4))
			dy = woff2Sign(flag>>1, 1+((b0&0x0c)<<2)+(b1&0x0f))
		case flag < 120:
			b0 := int(flag) - 84
			b1, b2 := r.u8(), r.u8()
			dx = woff2Sign(flag, 1+((b0/12)<<8)+b1)
			dy = woff2Sign(flag>>1, 1+(((b0%12)>>2)<<8)+b2)
		case flag < 124:
			b1, b2, b3 := r.u8(), r.u8(), r.u8()
			dx = woff2Sign(flag, (b1<<4)+(b2>>4))
			dy = woff2Sign(flag>>1, ((b2&0x0f)<<8)+b3)
		default:
			b1, b2, b3, b4 := r.u8(), r.u8(), r.u8(), r.u8()
			dx = woff2Sign(flag, (b1<<8)+b2)
			dy = woff2Sign(flag>>1, (b3<<8)+b4)
		}
		if r.bad {
			return nil, fmt.Errorf("%w: WOFF2 point data ends early", ErrInvalid)
		}
		x, y = x+dx, y+dy
		if x < -1<<24 || x > 1<<24 || y < -1<<24 || y > 1<<24 {
			return nil, fmt.Errorf("%w: a point at %d, %d", ErrInvalid, x, y)
		}
		out = append(out, woff2Point{x, y, onCurve})
	}
	return out, nil
}

func woff2Sign(flag uint8, v int) int {
	if flag&1 != 0 {
		return v
	}
	return -v
}

func woff2Bbox(pts []woff2Point, dst []byte) {
	if len(pts) == 0 {
		return
	}
	xMin, yMin, xMax, yMax := pts[0].x, pts[0].y, pts[0].x, pts[0].y
	for _, p := range pts[1:] {
		xMin, xMax = min(xMin, p.x), max(xMax, p.x)
		yMin, yMax = min(yMin, p.y), max(yMax, p.y)
	}
	binary.BigEndian.PutUint16(dst, uint16(int16(xMin)))
	binary.BigEndian.PutUint16(dst[2:], uint16(int16(yMin)))
	binary.BigEndian.PutUint16(dst[4:], uint16(int16(xMax)))
	binary.BigEndian.PutUint16(dst[6:], uint16(int16(yMax)))
}

// woff2Points writes the flags and the coordinates a simple glyph ends with.
func woff2Points(dst []byte, pts []woff2Point, overlap bool) []byte {
	var flags, xs, ys []byte
	last, repeat := -1, 0
	lastX, lastY := 0, 0
	for i, p := range pts {
		flag := 0
		if p.onCurve {
			flag = 1
		}
		if overlap && i == 0 {
			flag |= 0x40
		}
		dx, dy := p.x-lastX, p.y-lastY
		switch {
		case dx == 0:
			flag |= 0x10
		case dx > -256 && dx < 256:
			flag |= 2
			if dx > 0 {
				flag |= 0x10
			}
			xs = append(xs, byte(max(dx, -dx)))
		default:
			xs = binary.BigEndian.AppendUint16(xs, uint16(int16(dx)))
		}
		switch {
		case dy == 0:
			flag |= 0x20
		case dy > -256 && dy < 256:
			flag |= 4
			if dy > 0 {
				flag |= 0x20
			}
			ys = append(ys, byte(max(dy, -dy)))
		default:
			ys = binary.BigEndian.AppendUint16(ys, uint16(int16(dy)))
		}
		if flag == last && repeat != 255 {
			flags[len(flags)-1] |= 8
			repeat++
		} else {
			if repeat != 0 {
				flags = append(flags, byte(repeat))
			}
			flags = append(flags, byte(flag))
			repeat = 0
		}
		lastX, lastY = p.x, p.y
		last = flag
	}
	if repeat != 0 {
		flags = append(flags, byte(repeat))
	}
	dst = append(dst, flags...)
	dst = append(dst, xs...)
	return append(dst, ys...)
}

// woff2Hmtx rebuilds the horizontal metrics, whose left side bearings are
// the left edge of the glyph outline when they were left out.
func woff2Hmtx(src []byte, numGlyphs, numHMetrics int, xMins []int16) ([]byte, error) {
	r := &woff2Buf{b: src}
	flags := r.u8()
	if flags&0xFC != 0 || flags&3 == 3 {
		return nil, fmt.Errorf("%w: hmtx transform flags %#x", ErrInvalid, flags)
	}
	if numHMetrics < 1 || numHMetrics > numGlyphs || len(xMins) != numGlyphs {
		return nil, fmt.Errorf("%w: %d metrics for %d glyphs", ErrInvalid, numHMetrics, numGlyphs)
	}
	widths := make([]int, numHMetrics)
	for i := range widths {
		widths[i] = r.u16()
	}
	lsbs := make([]int16, numGlyphs)
	for i := range numGlyphs {
		fromStream := flags&1 == 0
		if i >= numHMetrics {
			fromStream = flags&2 == 0
		}
		if fromStream {
			lsbs[i] = int16(r.u16())
		} else {
			lsbs[i] = xMins[i]
		}
	}
	if r.bad {
		return nil, fmt.Errorf("%w: hmtx ends early", ErrInvalid)
	}
	out := make([]byte, 0, 2*numGlyphs+2*numHMetrics)
	for i := range numGlyphs {
		if i < numHMetrics {
			out = binary.BigEndian.AppendUint16(out, uint16(widths[i]))
		}
		out = binary.BigEndian.AppendUint16(out, uint16(lsbs[i]))
	}
	return out, nil
}
