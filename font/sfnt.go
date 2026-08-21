package font

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

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

	f := &Font{Kind: KindTrueType, sfnt: s, UnitsPerEm: 1000,
		Ascent: defaultAscent, Descent: defaultDescent}
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
		inner.readNames(s)
		if s.numGlyphs > 0 && s.numGlyphs != inner.glyphs {
			s.numGlyphs = inner.glyphs
		}
		inner.readHmtx(s)
		inner.readVertical(s)
		return inner, nil
	}

	f.readNames(s)
	f.glyphs = s.numGlyphs
	if f.glyphs == 0 {
		f.glyphs = s.locaGlyphs()
	}
	f.readHmtx(s)
	f.readVertical(s)
	return f, nil
}

// readVertical reads the em box out of hhea, or the OS/2 typo metrics.
func (f *Font) readVertical(s *sfnt) {
	upem := float32(s.upem)
	if upem <= 0 {
		return
	}
	asc, desc := 0, 0
	if hhea := s.tables["hhea"]; len(hhea) >= 10 {
		asc, desc = int(int16(be16(hhea, 4))), int(int16(be16(hhea, 6)))
	}
	if asc == 0 && desc == 0 {
		if os2 := s.tables["OS/2"]; len(os2) >= 72 {
			asc, desc = int(int16(be16(os2, 68))), int(int16(be16(os2, 70)))
		}
	}
	if asc == 0 && desc == 0 {
		return
	}
	f.Ascent, f.Descent = float32(asc)/upem, float32(desc)/upem
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

// lookupUnicode maps a Unicode value through the best available subtable. A
// character map that answers zero has not mapped the value: glyph zero is
// .notdef in every sfnt, so the two mean the same thing and the caller is told
// so the one way.
func (s *sfnt) lookupUnicode(r rune) int {
	if s == nil || s.unicode == nil {
		return -1
	}
	if gid := s.unicode.lookup(uint32(r)); gid > 0 {
		return gid
	}
	return -1
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
	if gid := s.macRoman.lookup(code); gid > 0 {
		return gid
	}
	return -1
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
		if ranges+2*segs > len(b) {
			return -1
		}
		// The segments are sorted by their end, which the format requires.
		i := sort.Search(segs, func(i int) bool { return uint32(be16(b, ends+2*i)) >= c })
		if i >= segs {
			return -1
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

	case 6:
		first, count := uint32(be16(b, 6)), uint32(be16(b, 8))
		if c < first || c >= first+count {
			return -1
		}
		return be16(b, 10+2*int(c-first))

	case 12:
		n := be32(b, 12)
		if n < 0 {
			return -1
		}
		n = min(n, max(len(b)-16, 0)/12)
		// The groups are sorted by the character they start at.
		i := sort.Search(n, func(i int) bool { return uint32(be32(b, 16+12*i+4)) >= c })
		if i >= n {
			return -1
		}
		g := 16 + 12*i
		if start := uint32(be32(b, g)); c >= start {
			return be32(b, g+8) + int(c-start)
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

// The name table records this reads.
const (
	nameFamily     = 1
	nameSubfamily  = 2
	namePostScript = 6
	nameTypoFamily = 16
	nameTypoSubfam = 17
)

// readNames takes the family, the weight and the slant off a program, which
// is what a face is looked up by.
func (f *Font) readNames(s *sfnt) {
	f.Weight = 400
	if os2 := s.tables["OS/2"]; len(os2) >= 64 {
		if w := be16(os2, 4); w >= 1 && w <= 1000 {
			f.Weight = w
		}
		sel := be16(os2, 62)
		f.Italic = sel&1 != 0
		if sel&(1<<5) != 0 {
			f.Weight = max(f.Weight, 700)
		}
	} else if head := s.tables["head"]; len(head) >= 46 {
		style := be16(head, 44)
		f.Italic = style&2 != 0
		if style&1 != 0 {
			f.Weight = 700
		}
	}
	f.Family = sfntName(s.tables["name"], nameTypoFamily, nameFamily)
	if f.Name == "" {
		f.Name = sfntName(s.tables["name"], namePostScript)
	}
	switch sub := sfntName(s.tables["name"], nameTypoSubfam, nameSubfamily); {
	case containsFold(sub, "italic"), containsFold(sub, "oblique"):
		f.Italic = true
	}
}

// sfntName reads the first of the name records that is there, preferring the
// Windows encoding a modern font writes and falling back to the Macintosh one.
func sfntName(name []byte, want ...int) string {
	if len(name) < 6 {
		return ""
	}
	n, off := be16(name, 2), be16(name, 4)
	best, bestScore := "", -1
	for i := range n {
		e := 6 + 12*i
		if e+12 > len(name) {
			break
		}
		id := be16(name, e+6)
		rank := -1
		for j, w := range want {
			if id == w {
				rank = len(want) - j
			}
		}
		if rank < 0 {
			continue
		}
		plat, enc, lang := be16(name, e), be16(name, e+2), be16(name, e+4)
		score := rank * 8
		switch {
		case plat == 3 && (enc == 1 || enc == 10):
			score += 4
		case plat == 0:
			score += 3
		case plat == 1 && enc == 0:
			score += 2
		default:
			continue
		}
		if plat == 3 && lang != 0x409 || plat == 1 && lang != 0 {
			score--
		}
		if score <= bestScore {
			continue
		}
		start, length := off+be16(name, e+10), be16(name, e+8)
		if start < 0 || length < 0 || start+length > len(name) {
			continue
		}
		v := name[start : start+length]
		if plat == 1 {
			best, bestScore = string(v), score
			continue
		}
		best, bestScore = utf16Name(v), score
	}
	return best
}

// utf16Name decodes the big endian UTF-16 a name record is written in.
func utf16Name(b []byte) string {
	out := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(b[i])<<8 | rune(b[i+1])
		if r >= 0xd800 && r <= 0xdbff && i+3 < len(b) {
			lo := rune(b[i+2])<<8 | rune(b[i+3])
			if lo >= 0xdc00 && lo <= 0xdfff {
				out = append(out, 0x10000+(r-0xd800)<<10+(lo-0xdc00))
				i += 2
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && strings.Contains(strings.ToLower(s), sub)
}
