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
}

// faceMetrics is what a program says about itself, worked out once: the em
// box, and the advance of every character a Latin book is mostly made of.
type faceMetrics struct {
	ascent, descent float32
	ascii           [128]float32
	space           float32
	upem            float32
	once            sync.Once
	prog            *font.Font
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
		m.ascii[r] = m.raw(r)
	}
	m.space = m.ascii[' ']
}

// raw is the advance of one character in text space, where one unit is the
// font size.
func (m *faceMetrics) raw(r rune) float32 {
	gid := m.prog.GIDForRune(r)
	if gid <= 0 {
		return 0
	}
	return m.prog.Advance(gid) / m.upem
}

func (m *faceMetrics) advance(r rune) float32 {
	if r < 128 {
		return m.ascii[r]
	}
	if a := m.raw(r); a != 0 {
		return a
	}
	// A character the substitute has no glyph for still takes room, or the
	// text of a script it cannot draw collapses into nothing.
	if wide(r) {
		return 1
	}
	return m.space
}

// styleFace picks the substitute a computed style asks for.
func styleFace(s *Style) face {
	kind := familySerif
	for _, name := range s.FontFamily {
		if k, ok := genericFamilies[strings.ToLower(strings.TrimSpace(name))]; ok {
			kind = k
			break
		}
	}
	slot := 0
	if s.FontWeight >= 600 {
		slot |= 1
	}
	if s.FontStyle != StyleNormal {
		slot |= 2
	}
	prog := font.Standard(base14[kind][slot])
	return face{prog: prog, m: metricsOf(prog), size: s.FontSize, track: s.LetterSpacing}
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

// ascent and descent are how far the face reaches above and below the
// baseline at its size.
func (f face) ascent() float32  { return f.m.ascent * f.size }
func (f face) descent() float32 { return -f.m.descent * f.size }
