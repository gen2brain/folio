package html

import (
	"strings"
	"sync"

	"github.com/gen2brain/folio/font"
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
	// vertical is a face measured down a line rather than across one, and
	// orient how it turns a character there.
	vertical bool
	orient   Orientation
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

// styleFace picks the face a computed style asks for: what the book brings
// with it for one of the families it names, then what the machine has, then
// one of the base fourteen.
func styleFace(s *Style, set *fontSet) face {
	bold, italic := s.FontWeight >= 600, s.FontStyle != StyleNormal
	prog := set.pick(s.FontFamily, bold, italic)
	if prog == nil {
		prog = namedFont(s.FontFamily, bold, italic)
	}
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
		track: s.LetterSpacing, bold: bold, italic: italic,
		vertical: s.Writing.Vertical(), orient: s.Orient}
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
	if gs, ok := f.shape(s, false); ok {
		w := float32(0)
		for _, g := range gs {
			w += float32(g.XAdvance)
		}
		return w*f.prog.Matrix.A*f.size + float32(len(gs))*f.track
	}
	w, n := float32(0), 0
	for _, r := range s {
		w += f.m.advance(r)
		n++
	}
	return w*f.size + float32(n)*f.track
}

// shape runs the text through the font's own layout tables, which is what a
// script whose letters join or carry marks needs. Plain text is measured a
// character at a time, which is the same answer and much less work.
func (f face) shape(s string, rtl bool) ([]font.Glyph, bool) {
	if f.prog == nil || f.vertical || !f.prog.Shaped() || !needsShaping(s) {
		return nil, false
	}
	return f.prog.Shape([]rune(s), rtl), true
}

// needsShaping reports text a font has to lay out itself: anything with a
// combining mark in it, and every script above the Latin range.
func needsShaping(s string) bool {
	for _, r := range s {
		if r >= 0x0300 {
			return true
		}
	}
	return false
}

// advance is what a character takes along its line, which for one that stands
// upright in vertical text is the em rather than its own width.
func (f face) advance(r rune) float32 {
	if f.standsUp(r) {
		return f.size + f.track
	}
	return f.m.advance(r)*f.size + f.track
}

// standsUp reports a character drawn upright rather than turned with the
// line, which only happens in vertical text.
func (f face) standsUp(r rune) bool {
	if !f.vertical {
		return false
	}
	switch f.orient {
	case OrientUpright:
		return true
	case OrientSideways:
		return false
	}
	return lbLookup(r)&lbIsUpright != 0
}

// gid is the glyph a character is drawn with, and -1 when the face has none.
func (f face) gid(r rune) int { return int(f.m.glyph(r).gid) }

// ascent and descent are how far the face reaches above and below the
// baseline at its size.
func (f face) ascent() float32  { return f.m.ascent * f.size }
func (f face) descent() float32 { return -f.m.descent * f.size }

// xHeight, subOffset and superOffset are what the program says about a lower
// case x and about where a script sits, and a plain fraction of the em for a
// program that says nothing, which is every one that is not an SFNT.
func (f face) xHeight() float32 { return f.metric(f.prog.XHeight, 0.5) }

func (f face) subOffset() float32 { return f.metric(f.prog.SubOffset, 0.1) }

func (f face) superOffset() float32 { return f.metric(f.prog.SuperOffset, 0.35) }

func (f face) metric(v, def float32) float32 {
	if f.prog == nil || v <= 0 {
		return def * f.size
	}
	return v * f.size
}

// fontSet are the faces a book brings with it, from the @font-face rules of
// the sheets that style a part.
type fontSet struct {
	doc   *Document
	faces map[string][]FontFace
}

// newFontSet collects what the sheets declare, in the order they are read: a
// family declared twice resolves to the last one that said it.
func newFontSet(d *Document, sheets []*Stylesheet) *fontSet {
	var set *fontSet
	for _, s := range sheets {
		for _, f := range s.Faces {
			key := foldFamily(f.Family)
			if key == "" {
				continue
			}
			if set == nil {
				set = &fontSet{doc: d, faces: map[string][]FontFace{}}
			}
			set.faces[key] = append(set.faces[key], f)
		}
	}
	return set
}

// pick returns the face a book brings for the first family it names that it
// has one for, and nil when it brings none of them.
func (s *fontSet) pick(families []string, bold, italic bool) *font.Font {
	if s == nil {
		return nil
	}
	for _, name := range families {
		if prog := s.find(name, bold, italic); prog != nil {
			return prog
		}
	}
	return nil
}

// find returns the face a book brings for a family at a weight and a slant,
// and nil when it brings none.
func (s *fontSet) find(family string, bold, italic bool) *font.Font {
	list := s.faces[foldFamily(family)]
	if len(list) == 0 {
		return nil
	}
	want := 400
	if bold {
		want = 700
	}
	best, score := -1, -1
	for i, f := range list {
		v := 1000 - abs(f.Weight-want)
		if f.Italic == italic {
			v += 500
		}
		if v > score {
			best, score = i, v
		}
	}
	for _, src := range list[best].Src {
		if prog := s.doc.embeddedFont(src); prog != nil {
			return prog
		}
	}
	return nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// embeddedFont reads and parses one of the font programs a book carries, once
// per book however many parts name it.
func (d *Document) embeddedFont(path string) *font.Font {
	d.fontMu.Lock()
	if prog, ok := d.fonts[path]; ok {
		d.fontMu.Unlock()
		return prog
	}
	d.fontMu.Unlock()

	var prog *font.Font
	if b, err := d.Read(path); err == nil {
		prog, _ = font.Parse(b)
	}
	d.fontMu.Lock()
	if d.fonts == nil {
		d.fonts = map[string]*font.Font{}
	}
	d.fonts[path] = prog
	d.fontMu.Unlock()
	return prog
}
