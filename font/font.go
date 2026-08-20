// Package font reads font programs and returns glyph outlines and metrics.
// It knows nothing about PDF: a font is bytes in, outlines out.
package font

import (
	"errors"
	"fmt"

	"github.com/gen2brain/pdf/raster"
)

// Errors returned by this package.
var (
	// ErrInvalid means the font program is damaged past use.
	ErrInvalid = errors.New("font: invalid font")
	// ErrUnsupported means the format is known but not handled.
	ErrUnsupported = errors.New("font: unsupported font")
)

// Kind is the format a font program is in.
type Kind int

// Font program formats.
const (
	KindTrueType Kind = iota
	KindCFF
	KindType1
)

// Font is a font program.
type Font struct {
	Kind Kind
	// Name is what the program calls itself, which is not the name the PDF
	// font dictionary uses.
	Name string
	// UnitsPerEm is the glyph space of a TrueType font; a CFF or Type1 font
	// carries a matrix instead.
	UnitsPerEm int
	// Matrix maps glyph space to text space, where one unit is the font size.
	Matrix raster.Matrix
	// CID is true for a CFF font keyed by CID rather than by name.
	CID bool

	sfnt   *sfnt
	cff    *cffFont
	type1  *type1Font
	glyphs int

	advances []int16
	names    map[string]int
	cache    map[int]*raster.Path
}

// Parse reads a font program, sniffing the format from its first bytes.
func Parse(data []byte) (*Font, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, len(data))
	}
	switch {
	case isSFNT(data):
		return parseSFNT(data)
	case data[0] == 1 && data[1] == 0:
		return parseCFF(data)
	case data[0] == '%' || data[0] == 0x80 || hasPrefix(data, "%!"):
		return parseType1(data)
	}
	if i := indexOf(data, "%!PS-AdobeFont"); i >= 0 && i < 1024 {
		return parseType1(data[i:])
	}
	return nil, fmt.Errorf("%w: unrecognized font program", ErrUnsupported)
}

// NumGlyphs returns how many glyphs the program has.
func (f *Font) NumGlyphs() int { return f.glyphs }

// GlyphPath returns the outline of a glyph in glyph space. The path is owned
// by the font and must not be modified.
//
// A CID keyed CFF is addressed by CID rather than by glyph index, which is
// the convention FreeType uses and therefore the one a PDF font dictionary
// carries; the charset maps it to the glyph inside.
func (f *Font) GlyphPath(gid int) *raster.Path {
	if gid < 0 {
		return nil
	}
	if f.CID {
		gid = f.cff.gidForCID(gid)
	}
	if gid >= f.glyphs {
		return nil
	}
	if p, ok := f.cache[gid]; ok {
		return p
	}
	var p *raster.Path
	switch f.Kind {
	case KindTrueType:
		p = f.glyfPath(gid, 0)
	case KindCFF:
		p = f.cff.path(gid)
	case KindType1:
		p = f.type1.path(gid)
	}
	if f.cache == nil {
		f.cache = map[int]*raster.Path{}
	}
	f.cache[gid] = p
	return p
}

// Advance returns the horizontal advance of a glyph in glyph space.
func (f *Font) Advance(gid int) float32 {
	if gid < 0 {
		return 0
	}
	if f.CID {
		gid = f.cff.gidForCID(gid)
	}
	if len(f.advances) > 0 {
		if gid >= len(f.advances) {
			gid = len(f.advances) - 1
		}
		return float32(f.advances[gid])
	}
	switch f.Kind {
	case KindCFF:
		return f.cff.advance(gid)
	case KindType1:
		return f.type1.advance(gid)
	}
	return 0
}

// GlyphName returns the name a glyph has in the program, or "" when it has
// none.
func (f *Font) GlyphName(gid int) string {
	switch f.Kind {
	case KindCFF:
		return f.cff.glyphName(gid)
	case KindType1:
		return f.type1.glyphName(gid)
	default:
		return f.sfnt.glyphName(gid)
	}
}

// GIDForName returns the glyph with a name, or -1.
func (f *Font) GIDForName(name string) int {
	if f.names == nil {
		f.names = map[string]int{}
		for gid := 0; gid < f.glyphs; gid++ {
			if n := f.GlyphName(gid); n != "" {
				if _, seen := f.names[n]; !seen {
					f.names[n] = gid
				}
			}
		}
	}
	if gid, ok := f.names[name]; ok {
		return gid
	}
	return -1
}

// GIDForRune looks a Unicode value up in the font's character map.
func (f *Font) GIDForRune(r rune) int {
	if f.sfnt != nil {
		return f.sfnt.lookupUnicode(r)
	}
	if f.Kind == KindCFF || f.Kind == KindType1 {
		if n, ok := runeToName[r]; ok {
			return f.GIDForName(n)
		}
	}
	return -1
}

// HasGlyphNames reports whether the program names its glyphs, which decides
// whether an encoding can be resolved through them.
func (f *Font) HasGlyphNames() bool {
	switch f.Kind {
	case KindType1:
		return true
	case KindCFF:
		return !f.CID
	default:
		return f.sfnt != nil && f.sfnt.hasNames()
	}
}

// BuiltinEncoding returns the code to glyph name table the program carries,
// or nil. Type1 fonts always have one and CFF fonts usually do.
func (f *Font) BuiltinEncoding() *[256]string {
	switch f.Kind {
	case KindType1:
		return f.type1.encoding()
	case KindCFF:
		return f.cff.encoding()
	}
	return nil
}

// GIDForSymbolCode looks a byte code up in a (3,0) symbol character map,
// which addresses the private use area with and without the F0 prefix.
func (f *Font) GIDForSymbolCode(code uint32) int { return f.sfnt.lookupSymbol(code) }

// GIDForMacCode looks a byte code up in a (1,0) Mac Roman character map.
func (f *Font) GIDForMacCode(code uint32) int { return f.sfnt.lookupMacRoman(code) }

// HasCmap reports whether the program carries a character map at all. One
// that does not is addressed by glyph index.
func (f *Font) HasCmap() bool {
	return f.sfnt != nil && len(f.sfnt.cmapTables) > 0
}

// runeToName is the Adobe Glyph List read backwards, for finding a glyph by
// Unicode value in a font that only names its glyphs. Several names reach the
// same character, so the shortest wins and ties go to the first alphabetically:
// picking whichever the map happened to yield made a page render differently
// from one process to the next.
var runeToName = func() map[rune]string {
	m := make(map[rune]string, len(glyphList))
	for name, s := range glyphList {
		r := []rune(s)
		if len(r) != 1 {
			continue
		}
		old, seen := m[r[0]]
		if !seen || len(name) < len(old) || (len(name) == len(old) && name < old) {
			m[r[0]] = name
		}
	}
	return m
}()

func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}
