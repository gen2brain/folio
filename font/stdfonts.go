package font

import (
	"strings"
	"sync"
)

// The fourteen fonts every reader must have, ISO 32000-1 9.6.2.2. The
// substitutes are PDFium's, which are metrically compatible with the
// originals.
var stdFontFile = map[string]string{
	"Courier":               "FoxitFixed",
	"Courier-Bold":          "FoxitFixedBold",
	"Courier-Oblique":       "FoxitFixedItalic",
	"Courier-BoldOblique":   "FoxitFixedBoldItalic",
	"Helvetica":             "FoxitSans",
	"Helvetica-Bold":        "FoxitSansBold",
	"Helvetica-Oblique":     "FoxitSansItalic",
	"Helvetica-BoldOblique": "FoxitSansBoldItalic",
	"Times-Roman":           "FoxitSerif",
	"Times-Bold":            "FoxitSerifBold",
	"Times-Italic":          "FoxitSerifItalic",
	"Times-BoldItalic":      "FoxitSerifBoldItalic",
	"Symbol":                "FoxitSymbol",
	"ZapfDingbats":          "FoxitDingbats",
}

var (
	stdMu    sync.Mutex
	stdCache = map[string]*Font{}
)

// Standard returns one of the fourteen fonts by its canonical name, or nil.
// The parsed program is shared, but the caches a font fills as it is used are
// not, so two documents can render at the same time.
func Standard(name string) *Font {
	file, ok := stdFontFile[name]
	if !ok {
		return nil
	}
	stdMu.Lock()
	f, ok := stdCache[file]
	if !ok {
		data := stdFontData[file]
		if data != "" {
			f, _ = Parse([]byte(data))
		}
		stdCache[file] = f
	}
	stdMu.Unlock()
	if f == nil {
		return nil
	}
	g := *f
	g.Name = name
	g.names, g.cache = nil, nil
	return &g
}

// StandardName maps a font name onto one of the fourteen, using the flags of
// the font descriptor to choose when the name says nothing. It returns "" for
// a name that cannot be resolved, which is never: the last resort is
// Helvetica.
func StandardName(name string, serif, fixed, symbolic, bold, italic bool) string {
	base := normalizeName(name)
	if _, ok := stdFontFile[base]; ok {
		return base
	}
	if alias, ok := stdFontAlias[base]; ok {
		return alias
	}

	lower := strings.ToLower(base)
	bold = bold || strings.Contains(lower, "bold") || strings.Contains(lower, "black") ||
		strings.Contains(lower, "heavy") || strings.Contains(lower, "semibold")
	italic = italic || strings.Contains(lower, "italic") || strings.Contains(lower, "oblique")
	fixed = fixed || strings.Contains(lower, "mono") || strings.Contains(lower, "courier")
	serif = serif || strings.Contains(lower, "times") || strings.Contains(lower, "serif") ||
		strings.Contains(lower, "roman") || strings.Contains(lower, "georgia") ||
		strings.Contains(lower, "book")

	switch {
	case symbolic && strings.Contains(lower, "dingbat"):
		return "ZapfDingbats"
	case symbolic && strings.Contains(lower, "symbol"):
		return "Symbol"
	case fixed:
		return "Courier" + suffix(bold, italic, "-Bold", "-Oblique", "-BoldOblique")
	case serif:
		if !bold && !italic {
			return "Times-Roman"
		}
		return "Times" + suffix(bold, italic, "-Bold", "-Italic", "-BoldItalic")
	default:
		return "Helvetica" + suffix(bold, italic, "-Bold", "-Oblique", "-BoldOblique")
	}
}

func suffix(bold, italic bool, b, i, bi string) string {
	switch {
	case bold && italic:
		return bi
	case bold:
		return b
	case italic:
		return i
	}
	return ""
}

// normalizeName strips a subset prefix and the punctuation that separates the
// family from the style.
func normalizeName(name string) string {
	if len(name) > 7 && name[6] == '+' {
		name = name[7:]
	}
	return strings.TrimSpace(name)
}

// stdFontAlias maps the names real files use onto the fourteen.
var stdFontAlias = map[string]string{
	"Arial":                        "Helvetica",
	"Arial,Bold":                   "Helvetica-Bold",
	"Arial,BoldItalic":             "Helvetica-BoldOblique",
	"Arial,Italic":                 "Helvetica-Oblique",
	"Arial-Bold":                   "Helvetica-Bold",
	"Arial-BoldItalic":             "Helvetica-BoldOblique",
	"Arial-BoldItalicMT":           "Helvetica-BoldOblique",
	"Arial-BoldMT":                 "Helvetica-Bold",
	"Arial-Italic":                 "Helvetica-Oblique",
	"Arial-ItalicMT":               "Helvetica-Oblique",
	"ArialMT":                      "Helvetica",
	"ArialNarrow":                  "Helvetica",
	"ArialUnicodeMS":               "Helvetica",
	"Courier-BoldItalic":           "Courier-BoldOblique",
	"Courier-Italic":               "Courier-Oblique",
	"CourierNew":                   "Courier",
	"CourierNew,Bold":              "Courier-Bold",
	"CourierNew,BoldItalic":        "Courier-BoldOblique",
	"CourierNew,Italic":            "Courier-Oblique",
	"CourierNewPS-BoldItalicMT":    "Courier-BoldOblique",
	"CourierNewPS-BoldMT":          "Courier-Bold",
	"CourierNewPS-ItalicMT":        "Courier-Oblique",
	"CourierNewPSMT":               "Courier",
	"CourierStd":                   "Courier",
	"CourierStd-Bold":              "Courier-Bold",
	"CourierStd-BoldOblique":       "Courier-BoldOblique",
	"CourierStd-Oblique":           "Courier-Oblique",
	"Helvetica,Bold":               "Helvetica-Bold",
	"Helvetica,BoldItalic":         "Helvetica-BoldOblique",
	"Helvetica,Italic":             "Helvetica-Oblique",
	"Helvetica-BoldItalic":         "Helvetica-BoldOblique",
	"Helvetica-Italic":             "Helvetica-Oblique",
	"Symbol,Bold":                  "Symbol",
	"Symbol,BoldItalic":            "Symbol",
	"Symbol,Italic":                "Symbol",
	"Times-BoldOblique":            "Times-BoldItalic",
	"Times-Oblique":                "Times-Italic",
	"TimesNewRoman":                "Times-Roman",
	"TimesNewRoman,Bold":           "Times-Bold",
	"TimesNewRoman,BoldItalic":     "Times-BoldItalic",
	"TimesNewRoman,Italic":         "Times-Italic",
	"TimesNewRomanPS":              "Times-Roman",
	"TimesNewRomanPS-Bold":         "Times-Bold",
	"TimesNewRomanPS-BoldItalic":   "Times-BoldItalic",
	"TimesNewRomanPS-BoldItalicMT": "Times-BoldItalic",
	"TimesNewRomanPS-BoldMT":       "Times-Bold",
	"TimesNewRomanPS-Italic":       "Times-Italic",
	"TimesNewRomanPS-ItalicMT":     "Times-Italic",
	"TimesNewRomanPSMT":            "Times-Roman",
	"ZapfDingbats,Bold":            "ZapfDingbats",
}

// StandardWidth returns the advance a base-14 font gives a glyph name, in
// units of a thousandth of an em, or -1.
func StandardWidth(font, glyph string) float64 {
	if w, ok := fixedWidths[font]; ok {
		return float64(w)
	}
	if t, ok := stdWidths[font]; ok {
		if w, ok := t[glyph]; ok {
			return float64(w)
		}
	}
	return -1
}
