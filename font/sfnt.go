package font

import (
	"encoding/binary"
	"fmt"

	"github.com/gen2brain/pdf/raster"
)

// sfnt is the OpenType container: a directory of tables.
type sfnt struct {
	data   []byte
	tables map[string][]byte

	upem       int
	locaLong   bool
	numGlyphs  int
	cmapTables []cmapTable
	unicode    *cmapTable
	symbol     *cmapTable
	macRoman   *cmapTable
	postNames  []string
}

func isSFNT(b []byte) bool {
	switch tag(b, 0) {
	case "\x00\x01\x00\x00", "OTTO", "true", "typ1", "ttcf":
		return true
	}
	return false
}

func tag(b []byte, off int) string {
	if off < 0 || off+4 > len(b) {
		return ""
	}
	return string(b[off : off+4])
}

func be16(b []byte, off int) int {
	if off < 0 || off+2 > len(b) {
		return 0
	}
	return int(binary.BigEndian.Uint16(b[off:]))
}

func be16s(b []byte, off int) int { return int(int16(be16(b, off))) }

func be32(b []byte, off int) int {
	if off < 0 || off+4 > len(b) {
		return 0
	}
	return int(binary.BigEndian.Uint32(b[off:]))
}

// parseSFNT reads the table directory and whichever outline format is inside.
func parseSFNT(data []byte) (*Font, error) {
	off := 0
	if tag(data, 0) == "ttcf" {
		if be32(data, 8) < 1 {
			return nil, fmt.Errorf("%w: empty font collection", ErrInvalid)
		}
		off = be32(data, 12)
		if off < 0 || off >= len(data) {
			return nil, fmt.Errorf("%w: font collection offset %d", ErrInvalid, off)
		}
	}

	n := be16(data, off+4)
	if n <= 0 || n > 512 {
		return nil, fmt.Errorf("%w: %d tables", ErrInvalid, n)
	}
	s := &sfnt{data: data, tables: make(map[string][]byte, n), upem: 1000}
	for i := 0; i < n; i++ {
		e := off + 12 + 16*i
		if e+16 > len(data) {
			break
		}
		name := tag(data, e)
		start, length := be32(data, e+8), be32(data, e+12)
		if start < 0 || length < 0 || start > len(data) {
			continue
		}
		if start+length > len(data) {
			length = len(data) - start
		}
		if _, dup := s.tables[name]; !dup {
			s.tables[name] = data[start : start+length]
		}
	}

	f := &Font{Kind: KindTrueType, sfnt: s, UnitsPerEm: 1000}
	if head := s.tables["head"]; len(head) >= 54 {
		if u := be16(head, 18); u >= 16 && u <= 16384 {
			s.upem = u
		}
		s.locaLong = be16(head, 50) != 0
	}
	f.UnitsPerEm = s.upem
	f.Matrix = raster.Scale(1/float32(s.upem), 1/float32(s.upem))

	if maxp := s.tables["maxp"]; len(maxp) >= 6 {
		s.numGlyphs = be16(maxp, 4)
	}
	s.readCmap()
	s.readPost()

	if cff := s.tables["CFF "]; len(cff) > 4 {
		inner, err := parseCFF(cff)
		if err != nil {
			return nil, err
		}
		inner.sfnt = s
		if s.numGlyphs > 0 && s.numGlyphs != inner.glyphs {
			s.numGlyphs = inner.glyphs
		}
		inner.readHmtx(s)
		return inner, nil
	}

	f.glyphs = s.numGlyphs
	if f.glyphs == 0 {
		f.glyphs = s.locaGlyphs()
	}
	f.readHmtx(s)
	return f, nil
}

// locaGlyphs counts glyphs from the loca table when maxp is unusable.
func (s *sfnt) locaGlyphs() int {
	loca := s.tables["loca"]
	if s.locaLong {
		return max(len(loca)/4-1, 0)
	}
	return max(len(loca)/2-1, 0)
}

// readHmtx reads the horizontal metrics, repeating the last advance for the
// glyphs the table stops short of, as the format requires.
func (f *Font) readHmtx(s *sfnt) {
	hhea, hmtx := s.tables["hhea"], s.tables["hmtx"]
	if len(hhea) < 36 || len(hmtx) < 4 {
		return
	}
	n := be16(hhea, 34)
	if n <= 0 || n > f.glyphs && f.glyphs > 0 {
		n = min(n, f.glyphs)
	}
	if n <= 0 {
		return
	}
	f.advances = make([]int16, 0, n)
	for i := 0; i < n && 4*i+2 <= len(hmtx); i++ {
		f.advances = append(f.advances, int16(be16(hmtx, 4*i)))
	}
}

// cmapTable is one character map subtable.
type cmapTable struct {
	platform, encoding int
	format             int
	data               []byte
}

func (s *sfnt) readCmap() {
	cm := s.tables["cmap"]
	if len(cm) < 4 {
		return
	}
	n := be16(cm, 2)
	for i := 0; i < n; i++ {
		e := 4 + 8*i
		if e+8 > len(cm) {
			break
		}
		off := be32(cm, e+4)
		if off < 0 || off+2 > len(cm) {
			continue
		}
		t := cmapTable{
			platform: be16(cm, e),
			encoding: be16(cm, e+2),
			format:   be16(cm, off),
			data:     cm[off:],
		}
		s.cmapTables = append(s.cmapTables, t)
	}

	for _, want := range [][2]int{{3, 10}, {0, 4}, {0, 6}, {3, 1}, {0, 3}, {0, 2}, {0, 1}, {0, 0}} {
		if t := s.findCmap(want[0], want[1]); t != nil && s.unicode == nil {
			s.unicode = t
		}
	}
	s.symbol = s.findCmap(3, 0)
	s.macRoman = s.findCmap(1, 0)
	if s.unicode == nil && s.symbol == nil && s.macRoman == nil && len(s.cmapTables) > 0 {
		s.unicode = &s.cmapTables[0]
	}
}

func (s *sfnt) findCmap(platform, encoding int) *cmapTable {
	for i := range s.cmapTables {
		if s.cmapTables[i].platform == platform && s.cmapTables[i].encoding == encoding {
			return &s.cmapTables[i]
		}
	}
	return nil
}

// lookupUnicode maps a Unicode value through the best available subtable.
func (s *sfnt) lookupUnicode(r rune) int {
	if s == nil || s.unicode == nil {
		return -1
	}
	return s.unicode.lookup(uint32(r))
}

// lookupSymbol maps a byte code through a (3,0) symbol subtable, which lives
// in the private use area and which files address with or without the F0
// prefix.
func (s *sfnt) lookupSymbol(code uint32) int {
	if s == nil || s.symbol == nil {
		return -1
	}
	for _, c := range [3]uint32{0xF000 + code&0xff, code, 0xF100 + code&0xff} {
		if gid := s.symbol.lookup(c); gid > 0 {
			return gid
		}
	}
	return -1
}

func (s *sfnt) lookupMacRoman(code uint32) int {
	if s == nil || s.macRoman == nil {
		return -1
	}
	return s.macRoman.lookup(code)
}

// lookup reads one code out of a subtable.
func (t *cmapTable) lookup(c uint32) int {
	b := t.data
	switch t.format {
	case 0:
		if c > 255 || 6+int(c) >= len(b) {
			return -1
		}
		return int(b[6+c])

	case 4:
		segs := be16(b, 6) / 2
		if segs == 0 {
			return -1
		}
		if c > 0xffff {
			return -1
		}
		ends := 14
		starts := ends + segs*2 + 2
		deltas := starts + segs*2
		ranges := deltas + segs*2
		for i := 0; i < segs; i++ {
			if uint32(be16(b, ends+2*i)) < c {
				continue
			}
			start := uint32(be16(b, starts+2*i))
			if c < start {
				return -1
			}
			ro := be16(b, ranges+2*i)
			if ro == 0 {
				return int(uint16(int(c) + be16s(b, deltas+2*i)))
			}
			at := ranges + 2*i + ro + 2*int(c-start)
			g := be16(b, at)
			if g == 0 {
				return -1
			}
			return int(uint16(g + be16s(b, deltas+2*i)))
		}
		return -1

	case 6:
		first, count := uint32(be16(b, 6)), uint32(be16(b, 8))
		if c < first || c >= first+count {
			return -1
		}
		return be16(b, 10+2*int(c-first))

	case 12:
		n := be32(b, 12)
		for i := 0; i < n; i++ {
			g := 16 + 12*i
			if g+12 > len(b) {
				break
			}
			start, end := uint32(be32(b, g)), uint32(be32(b, g+4))
			if c < start {
				break
			}
			if c <= end {
				return be32(b, g+8) + int(c-start)
			}
		}
		return -1
	}
	return -1
}

// readPost reads the version 2 glyph names.
func (s *sfnt) readPost() {
	p := s.tables["post"]
	if len(p) < 34 || be32(p, 0) != 0x00020000 {
		return
	}
	n := be16(p, 32)
	if n <= 0 || n > 65535 {
		return
	}
	idx := make([]int, n)
	for i := 0; i < n && 34+2*i+2 <= len(p); i++ {
		idx[i] = be16(p, 34+2*i)
	}
	var extra []string
	for at := 34 + 2*n; at < len(p); {
		l := int(p[at])
		at++
		if at+l > len(p) {
			break
		}
		extra = append(extra, string(p[at:at+l]))
		at += l
	}
	s.postNames = make([]string, n)
	for i, v := range idx {
		switch {
		case v < len(macGlyphNames):
			s.postNames[i] = macGlyphNames[v]
		case v-len(macGlyphNames) < len(extra):
			s.postNames[i] = extra[v-len(macGlyphNames)]
		}
	}
}

func (s *sfnt) glyphName(gid int) string {
	if s == nil || gid < 0 || gid >= len(s.postNames) {
		return ""
	}
	return s.postNames[gid]
}

func (s *sfnt) hasNames() bool { return s != nil && len(s.postNames) > 0 }
