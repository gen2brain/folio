// Package font reads font programs and returns glyph outlines and metrics.
// It knows nothing about PDF: a font is bytes in, outlines out.
package font

import (
	"errors"
	"fmt"
	"sync"

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
	// Family is the family the program belongs to, which is the name a
	// stylesheet asks for a font by.
	Family string
	// Weight is the usWeightClass of the program, 400 when it declares none,
	// and Italic whether it is slanted.
	Weight int
	Italic bool
	// UnitsPerEm is the glyph space of a TrueType font; a CFF or Type1 font
	// carries a matrix instead.
	UnitsPerEm int
	// Matrix maps glyph space to text space, where one unit is the font size.
	Matrix raster.Matrix
	// Ascent and Descent are how far the em box reaches above and below the
	// baseline, in text space, and default when the program declares neither.
	Ascent, Descent float32
	// CID is true for a CFF font keyed by CID rather than by name.
	CID bool

	sfnt   *sfnt
	cff    *cffFont
	type1  *type1Font
	glyphs int

	advances []int16

	// mu guards the two caches a Font fills as it is used. The program
	// itself is read-only once parsed, so a document rendered from several
	// goroutines shares one Font and locks only these.
	mu    sync.RWMutex
	names map[string]int
	cache map[int]*raster.Path
}

// alias returns the same program under another name, with caches of its own.
// A Font holds a lock, so it cannot be copied whole.
func (f *Font) alias(name string) *Font {
	return &Font{
		Kind:       f.Kind,
		Name:       name,
		UnitsPerEm: f.UnitsPerEm,
		Matrix:     f.Matrix,
		Ascent:     f.Ascent,
		Descent:    f.Descent,
		CID:        f.CID,
		sfnt:       f.sfnt,
		cff:        f.cff,
		type1:      f.type1,
		glyphs:     f.glyphs,
		advances:   f.advances,
	}
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

// The em box of a font that declares no vertical metrics.
const (
	defaultAscent  = 0.8
	defaultDescent = -0.2
)

// emBox takes the em box from a font bounding box in glyph space.
func (f *Font) emBox(bbox []float32, scale float32) {
	if len(bbox) != 4 || scale == 0 {
		return
	}
	asc, desc := bbox[3]*scale, bbox[1]*scale
	if asc <= 0 || desc >= 0 {
		return
	}
	f.Ascent, f.Descent = asc, desc
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
	f.mu.RLock()
	p, ok := f.cache[gid]
	f.mu.RUnlock()
	if ok {
		return p
	}
	switch f.Kind {
	case KindTrueType:
		p = f.glyfPath(gid, 0)
	case KindCFF:
		p = f.cff.path(gid)
	case KindType1:
		p = f.type1.path(gid)
	}
	f.mu.Lock()
	if f.cache == nil {
		f.cache = map[int]*raster.Path{}
	}
	if won, ok := f.cache[gid]; ok {
		p = won
	} else {
		f.cache[gid] = p
	}
	f.mu.Unlock()
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
	f.mu.Lock()
	if f.names == nil {
		names := map[string]int{}
		for gid := 0; gid < f.glyphs; gid++ {
			if n := f.GlyphName(gid); n != "" {
				if _, seen := names[n]; !seen {
					names[n] = gid
				}
			}
		}
		f.names = names
	}
	gid, ok := f.names[name]
	f.mu.Unlock()
	if ok {
		return gid
	}
	return -1
}

// GIDForRune looks a Unicode value up in the font's character map, or -1.
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
