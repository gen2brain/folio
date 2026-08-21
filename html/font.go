package html

import (
	"strings"
	"sync"

	"github.com/gen2brain/pdf/font"
)

// The three kinds of face the base fourteen can stand in for.
const (
	familySerif = iota
	familySans
	familyMono
)

// genericFamilies maps what a stylesheet names onto the three kinds of face
// there are substitutes for.
var genericFamilies = map[string]int{
	"serif":              familySerif,
	"ui-serif":           familySerif,
	"cursive":            familySerif,
	"fantasy":            familySerif,
	"times":              familySerif,
	"times new roman":    familySerif,
	"timesnewroman":      familySerif,
	"georgia":            familySerif,
	"garamond":           familySerif,
	"palatino":           familySerif,
	"palatino linotype":  familySerif,
	"book antiqua":       familySerif,
	"century schoolbook": familySerif,
	"cambria":            familySerif,
	"constantia":         familySerif,
	"minion pro":         familySerif,
	"charter":            familySerif,
	"caslon":             familySerif,
	"baskerville":        familySerif,
	"bookman":            familySerif,
	"new york":           familySerif,

	"sans-serif":       familySans,
	"sans serif":       familySans,
	"ui-sans-serif":    familySans,
	"system-ui":        familySans,
	"-apple-system":    familySans,
	"helvetica":        familySans,
	"helvetica neue":   familySans,
	"arial":            familySans,
	"arial unicode ms": familySans,
	"verdana":          familySans,
	"tahoma":           familySans,
	"calibri":          familySans,
	"candara":          familySans,
	"segoe ui":         familySans,
	"trebuchet ms":     familySans,
	"lucida sans":      familySans,
	"lucida grande":    familySans,
	"gill sans":        familySans,
	"futura":           familySans,
	"optima":           familySans,
	"avenir":           familySans,
	"roboto":           familySans,
	"open sans":        familySans,

	"monospace":        familyMono,
	"ui-monospace":     familyMono,
	"courier":          familyMono,
	"courier new":      familyMono,
	"consolas":         familyMono,
	"menlo":            familyMono,
	"monaco":           familyMono,
	"andale mono":      familyMono,
	"lucida console":   familyMono,
	"dejavu sans mono": familyMono,
	"liberation mono":  familyMono,
}

// base14 names the substitute for a kind of face at a weight and a slant.
var base14 = [3][4]string{
	familySerif: {"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic"},
	familySans:  {"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique"},
	familyMono:  {"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique"},
}

// face is a font program at a size: what a run of text is measured and drawn
// with.
type face struct {
	prog *font.Font
	m    *faceMetrics
	size float32
	// track is what letter-spacing adds after every character.
	track float32
	// bold and italic are what was asked for, which a fallback for a script
	// this face cannot draw is looked up by.
	bold, italic bool
}

// faceMetrics is what a program says about itself, worked out once: the em
// box, and the advance of every character a Latin book is mostly made of.
type faceMetrics struct {
	ascent, descent float32
	ascii           [128]glyph
	space           float32
	upem            float32
	once            sync.Once
	prog            *font.Font
	// mu guards the glyphs a face has been asked about above ASCII. Looking
	// one up in a font of sixty thousand glyphs is what measuring a page of
	// ideographs costs, and it is the same answer every time.
	mu     sync.RWMutex
	glyphs map[rune]glyph
}

// glyph is what a character maps to in one program: the glyph index, and how
// far the pen moves in text space.
type glyph struct {
	gid int32
	adv float32
}

var faceCache sync.Map

func metricsOf(prog *font.Font) *faceMetrics {
	if v, ok := faceCache.Load(prog); ok {
		return v.(*faceMetrics)
	}
	m := &faceMetrics{prog: prog}
	v, _ := faceCache.LoadOrStore(prog, m)
	m = v.(*faceMetrics)
	m.once.Do(m.fill)
	return m
}

func (m *faceMetrics) fill() {
	m.upem = float32(m.prog.UnitsPerEm)
	if m.upem <= 0 {
		m.upem = 1000
	}
	m.ascent, m.descent = m.prog.Ascent, m.prog.Descent
	if m.ascent <= 0 {
		m.ascent = 0.88
	}
	if m.descent >= 0 {
		m.descent = -0.25
	}
	for r := range rune(128) {
		m.ascii[r] = m.read(r)
	}
	m.space = m.ascii[' '].adv
}

// read looks a character up in the program, in text space where one unit is
// the font size.
func (m *faceMetrics) read(r rune) glyph {
	gid := m.prog.GIDForRune(r)
	if gid <= 0 {
		return glyph{gid: -1}
	}
	return glyph{gid: int32(gid), adv: m.prog.Advance(gid) / m.upem}
}

// glyph is what a character maps to, remembered after the first look-up.
func (m *faceMetrics) glyph(r rune) glyph {
	if r >= 0 && r < 128 {
		return m.ascii[r]
	}
	m.mu.RLock()
	g, ok := m.glyphs[r]
	m.mu.RUnlock()
	if ok {
		return g
	}
	g = m.read(r)
	m.mu.Lock()
	if m.glyphs == nil {
		m.glyphs = make(map[rune]glyph)
	}
	m.glyphs[r] = g
	m.mu.Unlock()
	return g
}

// has reports whether the program can draw a character at all.
func (m *faceMetrics) has(r rune) bool { return m.glyph(r).gid > 0 }

func (m *faceMetrics) advance(r rune) float32 {
	if g := m.glyph(r); g.gid > 0 {
		return g.adv
	}
	// A character the substitute has no glyph for still takes room, or the
	// text of a script it cannot draw collapses into nothing.
	if wide(r) {
		return 1
	}
	return m.space
}

// styleFace picks the face a computed style asks for: what the machine has
// for one of the families it names, and one of the base fourteen otherwise.
func styleFace(s *Style) face {
	bold, italic := s.FontWeight >= 600, s.FontStyle != StyleNormal
	prog := namedFont(s.FontFamily, bold, italic)
	if prog == nil {
		kind := familySerif
		for _, name := range s.FontFamily {
			if k, ok := genericFamilies[foldFamily(name)]; ok {
				kind = k
				break
			}
		}
		slot := 0
		if bold {
			slot |= 1
		}
		if italic {
			slot |= 2
		}
		prog = font.Standard(base14[kind][slot])
	}
	return face{prog: prog, m: metricsOf(prog), size: s.FontSize,
		track: s.LetterSpacing, bold: bold, italic: italic}
}

// namedKey is what a resolved family is remembered by.
type namedKey struct {
	families     string
	bold, italic bool
}

var namedCache sync.Map

// namedFont is the first of the families a style names that the machine has,
// and nil when it has none of them. A generic family is left to the base
// fourteen, which every machine has and which draw the same everywhere.
func namedFont(families []string, bold, italic bool) *font.Font {
	if len(families) == 0 {
		return nil
	}
	key := namedKey{strings.Join(families, ","), bold, italic}
	if v, ok := namedCache.Load(key); ok {
		f, _ := v.(*font.Font)
		return f
	}
	var found *font.Font
	for _, name := range families {
		if _, generic := genericFamilies[foldFamily(name)]; generic {
			break
		}
		if f := font.SystemFont(name, bold, italic); f != nil {
			found = f
			break
		}
	}
	namedCache.Store(key, found)
	return found
}

func foldFamily(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// fallbackFace is a face for a character the one in hand cannot draw, which
// is what a page in a script the base fourteen have no glyphs for needs.
func fallbackFace(f face, r rune) face {
	prog := font.Fallback(r, f.bold, f.italic)
	if prog == nil {
		return face{}
	}
	f.prog, f.m = prog, metricsOf(prog)
	return f
}

// smallCapsFace is the face the lower case of a small caps run is drawn with.
func smallCapsFace(f face) face {
	f.size *= smallCapsScale
	return f
}

// smallCapsScale is how much smaller a synthesised small capital is than the
// capitals around it.
const smallCapsScale = 0.8

// width is how much room a run of text takes.
func (f face) width(s string) float32 {
	w, n := float32(0), 0
	for _, r := range s {
		w += f.m.advance(r)
		n++
	}
	return w*f.size + float32(n)*f.track
}

func (f face) advance(r rune) float32 { return f.m.advance(r)*f.size + f.track }

// gid is the glyph a character is drawn with, and -1 when the face has none.
func (f face) gid(r rune) int { return int(f.m.glyph(r).gid) }

// ascent and descent are how far the face reaches above and below the
// baseline at its size.
func (f face) ascent() float32  { return f.m.ascent * f.size }
func (f face) descent() float32 { return -f.m.descent * f.size }
