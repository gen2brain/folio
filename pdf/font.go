package pdf

import (
	"reflect"
	"strings"
	"sync"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
	"github.com/gen2brain/folio/syntax"
)

// Font is a PDF font dictionary: what the interpreter needs to turn a string
// into positioned glyphs. The font program itself, and therefore the glyph
// outlines, arrive with the font engine.
type Font struct {
	Name  Name // /BaseFont, or /Name for a Type3 font
	Dict  Dict
	Type0 bool
	Type3 bool
	WMode int

	// Simple fonts: one byte per code.
	firstChar    int
	widths       []float64
	missingWidth float64
	encoding     [256]Name

	// Composite fonts: the CMap turns bytes into CIDs.
	cmap     *CMap
	defWidth float64
	cidWidth map[uint32]float64
	cid2gid  []byte

	// Type3 fonts draw their glyphs with content streams.
	FontMatrix raster.Matrix
	CharProcs  Dict
	Resources  Dict

	ascent    float32
	descent   float32
	symbolic  bool
	serif     bool
	fixed     bool
	forceBold bool
	italic    bool

	// t3mu guards t3direct, which is filled the first time a Type3 glyph is
	// shown rather than when the font is read, because reading it means
	// decoding every glyph procedure.
	t3mu     sync.Mutex
	t3direct map[Name]bool

	descriptor   Dict
	baseEncoding *[256]string
	names        [256]string

	// prog is the font program, embedded or substituted.
	prog *font.Font
	// substituted is true when prog is a stand in rather than the font the
	// document asked for.
	substituted bool
	// viaUnicode is set for a substituted CID keyed font: the CIDs address
	// the glyph order of the font the file did not embed, so the stand in is
	// asked for its glyphs through Unicode instead.
	viaUnicode bool
	// gids maps a character code to a glyph in prog, for a simple font.
	gids [256]int32
	// cid2gid is the /CIDToGIDMap stream of a CIDFontType2.
	toUnicode *CMap
	// std is set when the stand in is one of the base fourteen, whose
	// repertoire is Latin-1. A character outside it is looked for in a face
	// the machine has; a font given a face for its collection keeps that face
	// and shows nothing for what it does not have, which is what the other
	// readers do.
	std bool
	// fbMu guards fbFaces, which holds one wrapper per face a character the
	// substitute has no glyph for was drawn out of. The pointer has to be the
	// same one every time, so that a run of them is one span.
	fbMu    sync.Mutex
	fbFaces map[*font.Font]*fallbackFont

	// base is the base-14 name a substituted font resolved to.
	base string
	// ordering is the character collection the CIDs belong to.
	ordering string

	doc *Document
}

// font reads a font dictionary.
func (d *Document) font(obj Object, res Dict) *Font {
	dict := d.f.GetDict(obj)
	if dict == nil {
		return nil
	}
	// The whole build runs under the lock because a font is entered into the
	// cache before it is filled in, and a second goroutine must not be handed
	// a half read one. Nothing inside re-enters this.
	d.mu.Lock()
	defer d.mu.Unlock()
	if ft, ok := d.fonts[dictKey(obj, dict)]; ok {
		return ft
	}
	f := d.f
	ft := &Font{
		doc:          d,
		Name:         f.GetName(dict["BaseFont"]),
		Dict:         dict,
		missingWidth: 0,
		defWidth:     1000,
		FontMatrix:   raster.Matrix{A: 0.001, D: 0.001},
	}
	d.fonts[dictKey(obj, dict)] = ft

	switch f.GetName(dict["Subtype"]) {
	case "Type0":
		ft.Type0 = true
		d.readType0(ft, dict)
	case "Type3":
		ft.Type3 = true
		d.readType3(ft, dict, res)
		d.readSimpleWidths(ft, dict)
	default:
		if cp936Disguised(f, dict) {
			d.readCP936(ft)
		} else {
			d.readSimpleWidths(ft, dict)
		}
	}
	if ft.Name == "" && ft.Type3 {
		ft.Name = f.GetName(dict["Name"])
	}
	if ft.Name == "" {
		ft.Name = "Unnamed"
	}
	ft.readDescriptor(d, dict)
	d.readEncoding(ft, dict)
	d.loadProgram(ft, dict)
	d.readToUnicode(ft, dict)
	d.mapGlyphs(ft)
	return ft
}

// readDescriptor reads the font descriptor of a simple font, or of the
// descendant of a composite one.
func (ft *Font) readDescriptor(d *Document, dict Dict) {
	f := d.f
	desc := f.GetDict(dict["FontDescriptor"])
	if desc == nil && ft.Type0 {
		if df := f.GetArray(dict["DescendantFonts"]); len(df) > 0 {
			desc = f.GetDict(f.GetDict(df[0])["FontDescriptor"])
		}
	}
	if desc == nil {
		return
	}
	ft.descriptor = desc
	ft.missingWidth = f.GetFloat(desc["MissingWidth"], 0)
	ft.ascent = float32(f.GetFloat(desc["Ascent"], 0)) / 1000
	ft.descent = float32(f.GetFloat(desc["Descent"], 0)) / 1000
	flags := int(f.GetInt(desc["Flags"], 0))
	ft.fixed = flags&1 != 0
	ft.serif = flags&2 != 0
	ft.symbolic = flags&4 != 0 && flags&32 == 0
	ft.italic = flags&64 != 0
	ft.forceBold = flags&(1<<18) != 0
	if f.GetFloat(desc["StemV"], 0) > 120 || f.GetFloat(desc["FontWeight"], 400) >= 600 {
		ft.forceBold = true
	}
	if f.GetFloat(desc["ItalicAngle"], 0) != 0 {
		ft.italic = true
	}
}

// loadProgram finds the embedded font program, or substitutes one.
func (d *Document) loadProgram(ft *Font, dict Dict) {
	if ft.Type3 {
		return
	}
	f := d.f
	desc := ft.descriptor
	for _, key := range []Name{"FontFile2", "FontFile3", "FontFile"} {
		st := f.GetStream(desc[key])
		if st == nil {
			continue
		}
		data, err := st.Data()
		if err != nil || len(data) == 0 {
			d.errorf("font /%s: %v", ft.Name, err)
			continue
		}
		prog, err := font.Parse(data)
		if err != nil {
			d.errorf("font /%s: %v", ft.Name, err)
			continue
		}
		ft.prog = prog
		return
	}
	d.substitute(ft)
}

// substitute picks a face for a font the file did not embed: one the machine
// has for a character collection the fourteen cannot draw, one of the fourteen
// otherwise.
func (d *Document) substitute(ft *Font) {
	name := string(ft.Name)
	ft.substituted = true
	if prog := d.systemCJK(name, ft.ordering, ft.forceBold, ft.italic); prog != nil {
		ft.prog, ft.viaUnicode = prog, true
		ft.base = prog.Family
		return
	}
	ft.base = font.StandardName(name, ft.serif, ft.fixed, ft.symbolic, ft.forceBold, ft.italic)
	ft.prog = font.Standard(ft.base)
	ft.viaUnicode, ft.std = ft.Type0, true
	if ft.prog == nil {
		d.errorf("no substitute for font /%s", ft.Name)
	}
}

// cjkSample is a character of each collection, which a face for it is asked
// for by.
var cjkSample = map[string]rune{
	"GB1": '\u4e00', "CNS1": '\u4e00', "Japan1": '\u3042', "Korea1": '\uac00', "KR": '\uac00',
}

// systemCJK finds a face the machine has for a character collection the base
// fourteen have no glyphs for. The document's own name for the font comes
// first: a file naming SimSun and not embedding it means the face, not a
// substitute for it.
func (d *Document) systemCJK(name, ordering string, bold, italic bool) *font.Font {
	sample, ok := cjkSample[ordering]
	if !ok || d.noSystemFonts {
		return nil
	}
	if i := strings.IndexByte(name, '+'); i == 6 {
		name = name[7:]
	}
	if i := strings.IndexByte(name, ','); i >= 0 {
		name = name[:i]
	}
	if f := font.SystemFont(name, bold, italic); f != nil && f.GIDForRune(sample) > 0 {
		return f
	}
	return font.Fallback(sample, bold, italic)
}

// readToUnicode reads the /ToUnicode CMap, which maps codes to text.
func (d *Document) readToUnicode(ft *Font, dict Dict) {
	st := d.f.GetStream(dict["ToUnicode"])
	if st == nil {
		if name := d.f.GetName(dict["ToUnicode"]); name != "" {
			ft.toUnicode = d.predefinedCMap(name, 0)
		}
		return
	}
	data, err := st.Data()
	if err != nil {
		d.errorf("/ToUnicode: %v", err)
		return
	}
	ft.toUnicode = d.parseCMap(data, 0)
}

// mapGlyphs resolves every character code of a simple font to a glyph in the
// font program. ISO 32000-1 9.6.6 describes the inputs; the order they are
// tried in is what real files need rather than what the text says.
func (d *Document) mapGlyphs(ft *Font) {
	for i := range ft.gids {
		ft.gids[i] = -1
	}
	if ft.Type3 {
		ft.names = ft.encodingNames(nil)
		for code := 0; code < 256; code++ {
			ft.gids[code] = int32(code)
		}
		return
	}
	if ft.prog == nil || ft.Type0 {
		return
	}
	p := ft.prog

	ft.names = ft.encodingNames(p.BuiltinEncoding())
	names := ft.names

	for code := 0; code < 256; code++ {
		ft.gids[code] = int32(d.glyphFor(ft, code, names[code]))
	}

	win := font.Encoding("WinAnsiEncoding")
	for code := 0; code < 256; code++ {
		if ft.names[code] != "" || ft.gids[code] <= 0 {
			continue
		}
		if p.HasGlyphNames() {
			ft.names[code] = p.GlyphName(int(ft.gids[code]))
		}
		if ft.names[code] == "" {
			ft.names[code] = win[code]
		}
	}

	if ft.symbolic && ft.prog.Kind == font.KindType1 {
		std := font.StandardEncoding()
		for code := 0; code < 256; code++ {
			if ft.gids[code] > 0 && font.RuneForName(ft.names[code]) == 0 {
				ft.names[code] = std[code]
			}
		}
	}
}

// encodingNames builds the code to glyph name table: the encoding the
// dictionary names, else the one the program carries, else the standard one,
// with /Differences over the top.
func (ft *Font) encodingNames(builtin *[256]string) [256]string {
	var names [256]string
	switch {
	case ft.baseEncoding != nil:
		names = *ft.baseEncoding
	case ft.base == "Symbol" || ft.base == "ZapfDingbats":
		names = *font.Encoding(ft.base)
	case builtin != nil:
		names = *builtin
	default:
		names = *font.StandardEncoding()
	}
	for code, n := range ft.encoding {
		if n != "" {
			names[code] = string(n)
		}
	}
	return names
}

// glyphFor resolves one code. Every lookup it makes answers -1 when it has
// nothing, so zero is a glyph like any other: a Type1 program has no glyph
// order of its own and its CharStrings dictionary puts whichever glyph it
// likes first.
func (d *Document) glyphFor(ft *Font, code int, name string) int {
	p := ft.prog

	if p.HasGlyphNames() {
		if name != "" {
			if gid := p.GIDForName(name); gid >= 0 {
				return gid
			}
			if r := font.RuneForName(name); r > 0 {
				if gid := p.GIDForRune(r); gid >= 0 {
					return gid
				}
			}
			if gid := font.IndexForName(name); gid >= 0 && gid < p.NumGlyphs() {
				return gid
			}
		}
		if r := rune(standardRune(code)); r > 0 {
			if gid := p.GIDForRune(r); gid >= 0 {
				return gid
			}
		}
		if ft.substituted {
			return 0
		}
	}

	if ft.symbolic {
		if gid := p.GIDForSymbolCode(uint32(code)); gid >= 0 {
			return gid
		}
		if gid := p.GIDForMacCode(uint32(code)); gid >= 0 {
			return gid
		}
	}
	if name != "" {
		if r := font.RuneForName(name); r > 0 {
			if gid := p.GIDForRune(r); gid >= 0 {
				return gid
			}
			if mac := font.MacRomanCode(r); mac >= 0 {
				if gid := p.GIDForMacCode(uint32(mac)); gid >= 0 {
					return gid
				}
			}
		}
	}
	if !ft.symbolic {
		if gid := p.GIDForSymbolCode(uint32(code)); gid >= 0 {
			return gid
		}
		if gid := p.GIDForMacCode(uint32(code)); gid >= 0 {
			return gid
		}
	}
	// Reading the character code as a glyph index is a guess, and it is only
	// available to a program that does not name its glyphs: where the encoding
	// asked for a name and the font has no such glyph, the answer is that it
	// has no such glyph. A subset font keeps a few dozen glyphs in whatever
	// order it likes, so the guess lands on an unrelated letter.
	if code < p.NumGlyphs() && !p.HasCmap() && (name == "" || !p.HasGlyphNames()) {
		return code
	}
	if p.HasGlyphNames() {
		if gid := p.GIDForName(".notdef"); gid >= 0 {
			return gid
		}
		return -1
	}
	return 0
}

// standardRune is the character StandardEncoding gives a code.
func standardRune(code int) rune {
	if code < 0 || code > 255 {
		return 0
	}
	return font.RuneForName(font.StandardEncoding()[code])
}

// dictKey identifies a font for the cache, by reference or by its map.
func dictKey(obj Object, dict Dict) any {
	if r, ok := obj.(Ref); ok {
		return r
	}
	return reflect.ValueOf(dict).Pointer()
}

func (d *Document) readSimpleWidths(ft *Font, dict Dict) {
	f := d.f
	ft.firstChar = int(f.GetInt(dict["FirstChar"], 0))
	ft.widths = f.GetFloats(dict["Widths"])
}

func (d *Document) readType3(ft *Font, dict Dict, res Dict) {
	f := d.f
	ft.FontMatrix = d.matrix(dict["FontMatrix"], ft.FontMatrix)
	ft.CharProcs = f.GetDict(dict["CharProcs"])
	ft.Resources = f.GetDict(dict["Resources"])
	if ft.Resources == nil {
		ft.Resources = res
	}
}

// cp936Fonts are the GBK names of the five Chinese fonts one generator writes
// as simple fonts and draws two byte codes through.
var cp936Fonts = []Name{
	"\xCB\xCE\xCC\xE5",        // SimSun
	"\xBA\xDA\xCC\xE5",        // SimHei
	"\xBF\xAC\xCC\xE5_GB2312", // SimKai
	"\xB7\xC2\xCB\xCE_GB2312", // SimFang
	"\xC1\xA5\xCA\xE9",        // SimLi
}

// cp936Disguised reports a simple font that is really a composite one. The
// test is narrow on purpose.
func cp936Disguised(f *syntax.File, dict Dict) bool {
	desc := f.GetDict(dict["FontDescriptor"])
	if desc == nil || f.GetInt(desc["Flags"], 0) != 4 {
		return false
	}
	if n, ok := f.Resolve(dict["Encoding"]).(Name); !ok || n != "WinAnsiEncoding" {
		return false
	}
	base := f.GetName(dict["BaseFont"])
	for _, n := range cp936Fonts {
		if base == n {
			return true
		}
	}
	return false
}

// readCP936 turns such a font into the composite one it is: GBK codes, CIDs
// of Adobe-GB1, and the collection's Unicode mapping behind them.
func (d *Document) readCP936(ft *Font) {
	ft.Type0 = true
	ft.cmap = d.predefinedCMap("GBK-EUC-H", 0)
	if ft.cmap == nil {
		ft.Type0 = false
		return
	}
	ft.WMode = ft.cmap.WMode
	ft.ordering = "GB1"
	ft.defWidth = 1000
	ft.cidWidth = map[uint32]float64{}
}

// readEncoding reads /Encoding: the base encoding it names, and the glyph
// names /Differences assigns.
func (d *Document) readEncoding(ft *Font, dict Dict) {
	enc := d.f.Resolve(dict["Encoding"])
	if n, ok := enc.(Name); ok {
		ft.baseEncoding = font.Encoding(string(n))
		return
	}
	ed, ok := enc.(Dict)
	if !ok {
		return
	}
	if n := d.f.GetName(ed["BaseEncoding"]); n != "" {
		ft.baseEncoding = font.Encoding(string(n))
	}
	code := 0
	for _, e := range d.f.GetArray(ed["Differences"]) {
		switch v := d.f.Resolve(e).(type) {
		case Integer:
			code = int(v)
		case Real:
			code = int(v)
		case Name:
			if code >= 0 && code < 256 {
				ft.encoding[code] = v
			}
			code++
		}
	}
}

func (d *Document) readType0(ft *Font, dict Dict) {
	f := d.f
	switch enc := f.Resolve(dict["Encoding"]).(type) {
	case Name:
		ft.cmap = d.predefinedCMap(enc, 0)
	case *syntax.Stream:
		if data, err := enc.Data(); err == nil {
			ft.cmap = d.parseCMap(data, 0)
		} else {
			d.errorf("CMap stream: %v", err)
		}
	}
	if ft.cmap == nil {
		ft.cmap = identityCMap(0)
	}
	ft.WMode = ft.cmap.WMode

	desc := f.GetArray(dict["DescendantFonts"])
	if len(desc) == 0 {
		d.errorf("Type0 font /%s has no descendant", ft.Name)
		return
	}
	df := f.GetDict(desc[0])
	if df == nil {
		return
	}
	if si := f.GetDict(df["CIDSystemInfo"]); si != nil {
		ft.ordering = string(f.GetBytes(si["Ordering"]))
	}
	ft.defWidth = f.GetFloat(df["DW"], 1000)
	ft.cidWidth = map[uint32]float64{}
	readW(f, f.GetArray(df["W"]), ft.cidWidth)

	if st := f.GetStream(df["CIDToGIDMap"]); st != nil {
		if b, err := st.Data(); err == nil {
			ft.cid2gid = b
		}
	}
}

// BaseFont returns the base-14 name a substituted font resolved to.
func (ft *Font) BaseFont() string { return ft.base }

// Program returns the font program, embedded or substituted, or nil.
func (ft *Font) Program() *font.Font { return ft.prog }

// FontName is the name the font goes by, which is /BaseFont or, for a Type3
// font, /Name.
func (ft *Font) FontName() string { return string(ft.Name) }

// EmBox is how far the font's em box reaches above and below the baseline,
// in text space. The descriptor is preferred over the program.
func (ft *Font) EmBox() (ascent, descent float32) {
	if ft.ascent > 0 && ft.descent < 0 {
		return ft.ascent, ft.descent
	}
	if ft.prog != nil {
		return ft.prog.Ascent, ft.prog.Descent
	}
	return 0.8, -0.2
}

// glyphKey names one glyph procedure, which is what the device holds while it
// is being run.
type glyphKey struct {
	font *Font
	code int
}

// RunGlyph draws one glyph of a Type3 font into dev, by interpreting the
// content stream the glyph is.
func (ft *Font) RunGlyph(dev Device, code int, m raster.Matrix, cs *ColorSpace, col []float32, alpha float32, depth int) {
	if !ft.Type3 || ft.doc == nil || depth >= maxNesting || code < 0 || code > 255 {
		return
	}
	name := ft.GlyphName(uint32(code))
	if name == "" {
		return
	}
	d := ft.doc
	proc := d.f.GetStream(d.f.Lookup(ft.CharProcs, name))
	if proc == nil {
		return
	}
	// The device is what outlives this call, so it is what holds the
	// procedures on the path to here.
	if g, ok := dev.(gfx.GlyphRunner); ok {
		if !g.EnterGlyph(glyphKey{ft, code}) {
			return
		}
		defer g.LeaveGlyph()
	}
	data, err := proc.Data()
	if err != nil {
		d.errorf("Type3 glyph %s: %v", name, err)
		return
	}
	res := ft.Resources
	if res == nil {
		res = Dict{}
	}

	ctm := raster.Concat(ft.FontMatrix, m)
	var ops int64
	ip := &interp{
		doc:      d,
		dev:      dev,
		gs:       newGState(ctm),
		base:     ctm,
		res:      res,
		defaults: &DefaultColorSpaces{},
		scissor:  raster.InfiniteRect,
		ops:      &ops,
		depth:    depth,
	}
	ip.gs.fill.cs = cs
	ip.gs.fill.value = append([]float32(nil), col...)
	ip.gs.strokeColor.cs = cs
	ip.gs.strokeColor.value = append([]float32(nil), col...)
	ip.gs.fillAlpha, ip.gs.strokeAlpha = alpha, alpha
	ip.runStream(proc, data)
	ip.finish()
}

// Substituted reports whether the program is a stand in for a font the file
// did not embed.
func (ft *Font) Substituted() bool { return ft.substituted }

// Glyph returns the index of the glyph a character selects in the font
// program, or -1.
func (ft *Font) Glyph(c Char) int {
	if ft.Type3 {
		if c.Code < 256 {
			return int(c.Code)
		}
		return -1
	}
	if ft.prog == nil {
		return -1
	}
	if ft.viaUnicode {
		if r := ft.textRune(c); r > 0 {
			return ft.prog.GIDForRune(r)
		}
		return ft.standardOrder(c)
	}
	if ft.Type0 {
		return ft.cidToGID(int(c.CID))
	}
	if c.Code < 256 {
		return int(ft.gids[c.Code])
	}
	return -1
}

// standardOrder is the last thing left for a CID keyed font the file did not
// embed and gave no way to read: the CIDs index the glyphs of a font that is
// not here, and a TrueType font that never rearranged its glyphs is in the
// standard Macintosh order. Only what that order covers can be guessed at, so
// a character collection stays undrawn rather than drawn wrongly.
func (ft *Font) standardOrder(c Char) int {
	name := font.MacGlyphName(int(c.CID))
	if name == "" || name == ".notdef" || name == ".null" {
		return -1
	}
	return ft.prog.GIDForName(name)
}

// cidToGID maps a CID onto a glyph, through /CIDToGIDMap or through the
// charset of a CID keyed CFF.
func (ft *Font) cidToGID(cid int) int {
	if len(ft.cid2gid) > 0 {
		if i := 2 * cid; i+1 < len(ft.cid2gid) {
			return int(ft.cid2gid[i])<<8 | int(ft.cid2gid[i+1])
		}
		return 0
	}
	return cid
}

// Text returns the characters a code stands for, which is more than one for a
// ligature.
func (ft *Font) Text(c Char) string {
	if ft.toUnicode == nil {
		return ""
	}
	return ft.toUnicode.Text(c.Code)
}

// Rune returns the character a code stands for, for text extraction. Codes
// that map to nothing, and mappings that land on a control character, become
// the replacement character, which is what MuPDF shows and what a text
// extractor can recognize.
func (ft *Font) Rune(c Char) rune {
	r := rune(0)
	if s := ft.Text(c); s != "" {
		r = []rune(s)[0]
	}
	if r >= 8 && r <= 13 {
		return ' '
	}
	if r < 32 || (r >= 127 && r < 160) {
		r = 0
	}
	if r == 0 && !ft.Type0 && c.Code < 256 {
		r = font.RuneForName(ft.names[c.Code])
	}
	if r == 0 {
		r = ft.textRune(c)
	}
	if r == 0 && !ft.Type3 && ft.prog != nil {
		r = font.RuneForName(ft.prog.GlyphName(ft.Glyph(c)))
	}
	if r == 0 {
		return 0xFFFD
	}
	return r
}

// textRune is what a character stands for as far as the file itself says: the
// ToUnicode map, then the character collection. It does not ask the font
// program, which Glyph needs of it.
func (ft *Font) textRune(c Char) rune {
	if s := ft.Text(c); s != "" {
		if r := []rune(s)[0]; r >= 32 && !(r >= 127 && r < 160) {
			return r
		}
	}
	// The collection comes before the program, which for such a font is a
	// substitute whose glyph names say nothing about the text.
	if ft.Type0 && ft.ordering != "" {
		return uniRuneOf(cidUnicode(ft.ordering), c.CID)
	}
	return 0
}

// GlyphNameOf returns what the font program calls the glyph a character
// selects, which is what a trace of the device calls it too.
func (ft *Font) GlyphNameOf(c Char) string {
	face, gid := ft.GlyphFace(c, nil)
	p := face.Program()
	if gid < 0 || p == nil || ft.Type3 {
		return ""
	}
	return p.GlyphName(gid)
}

// GlyphFace resolves a character to the glyph that draws it and the face it
// comes from, which is one the machine has when the character is outside what
// the stand in for a font the file did not embed can draw. cur is the face the
// run is already in, which keeps a space between two words of a script the
// stand in has no glyphs for from splitting the run in three.
func (ft *Font) GlyphFace(c Char, cur gfx.Font) (gfx.Font, int) {
	if !ft.std {
		return ft, ft.Glyph(c)
	}
	if fb, ok := cur.(*fallbackFont); ok && fb.Font == ft {
		if r := ft.textRune(c); r > 0 {
			if g := fb.prog.GIDForRune(r); g > 0 {
				return fb, g
			}
		}
	}
	gid := ft.Glyph(c)
	if gid > 0 {
		return ft, gid
	}
	r := ft.textRune(c)
	if r <= 0 {
		return ft, gid
	}
	p := font.Fallback(r, ft.forceBold, ft.italic)
	if p == nil || p == ft.prog {
		return ft, gid
	}
	g := p.GIDForRune(r)
	if g <= 0 {
		return ft, gid
	}
	return ft.face(p), g
}

// RunFace is the face a whole shown string is drawn from: the one a character
// the stand in has no glyph for reaches for, so that a word is not half in one
// typeface and half in another. It is nil when the stand in covers the string.
func (ft *Font) RunFace(cs []Char) gfx.Font {
	if !ft.std {
		return nil
	}
	for _, c := range cs {
		if ft.Glyph(c) > 0 {
			continue
		}
		if f, g := ft.GlyphFace(c, nil); g > 0 && f != gfx.Font(ft) {
			return f
		}
	}
	return nil
}

// fallbackFont is the font with one face swapped for another, which is all a
// device needs to draw a glyph out of it.
type fallbackFont struct {
	*Font
	prog *font.Font
}

func (f *fallbackFont) Program() *font.Font { return f.prog }

func (ft *Font) face(p *font.Font) *fallbackFont {
	ft.fbMu.Lock()
	defer ft.fbMu.Unlock()
	if f, ok := ft.fbFaces[p]; ok {
		return f
	}
	if ft.fbFaces == nil {
		ft.fbFaces = map[*font.Font]*fallbackFont{}
	}
	f := &fallbackFont{Font: ft, prog: p}
	ft.fbFaces[p] = f
	return f
}

// readW parses the /W array, which is a mix of "first last width" triples and
// "first [w w w]" pairs.
func readW(f *syntax.File, w Array, out map[uint32]float64) {
	for i := 0; i < len(w); {
		first := f.GetInt(w[i], -1)
		if first < 0 || i+1 >= len(w) {
			return
		}
		switch v := f.Resolve(w[i+1]).(type) {
		case Array:
			for j, e := range v {
				out[uint32(first)+uint32(j)] = f.GetFloat(e, 0)
			}
			i += 2
		default:
			if i+2 >= len(w) {
				return
			}
			last := f.GetInt(w[i+1], -1)
			width := f.GetFloat(w[i+2], 0)
			if last >= first && last-first < 1<<20 {
				for c := first; c <= last; c++ {
					out[uint32(c)] = width
				}
			}
			i += 3
		}
	}
}

// Char is one character code decoded from a string.
type Char struct {
	Code  uint32
	CID   uint32
	Bytes int
	// Width is the glyph advance in text space units of the font size, so
	// 1000 units per em has already been divided out for everything but a
	// Type3 font, whose advance goes through its own font matrix.
	Width float32
	// Space is true for a single byte code 32, which is what the word spacing
	// parameter applies to.
	Space bool
}

// Decode splits a string into character codes.
func (ft *Font) Decode(s []byte) []Char {
	return ft.decode(make([]Char, 0, len(s)), s)
}

// decode is Decode appending into a buffer the caller owns, so that showing a
// string need not allocate one.
func (ft *Font) decode(out []Char, s []byte) []Char {
	for i := 0; i < len(s); {
		var c Char
		if ft.Type0 {
			code, n, cid := ft.cmap.Next(s[i:])
			if n == 0 {
				break
			}
			c = Char{Code: code, CID: cid, Bytes: n, Space: n == 1 && code == 32}
			i += n
		} else {
			c = Char{Code: uint32(s[i]), CID: uint32(s[i]), Bytes: 1, Space: s[i] == 32}
			i++
		}
		c.Width = ft.width(c)
		out = append(out, c)
	}
	return out
}

// width returns the advance for one code, in text space units.
func (ft *Font) width(c Char) float32 {
	if ft.Type0 {
		if w, ok := ft.cidWidth[c.CID]; ok {
			return float32(w) / 1000
		}
		return float32(ft.defWidth) / 1000
	}
	if i := int(c.Code) - ft.firstChar; i >= 0 && i < len(ft.widths) {
		w := float32(ft.widths[i])
		if ft.Type3 {
			return w * ft.FontMatrix.A
		}
		return w / 1000
	}
	if ft.Type3 {
		return 0
	}
	if w := ft.programWidth(c); w >= 0 {
		return w
	}
	if ft.missingWidth != 0 {
		return float32(ft.missingWidth) / 1000
	}
	return 0
}

// uncacheable reports whether a Type3 glyph paints with graphics state it
// never set for itself, and so has to run where the text is shown.
func (ft *Font) uncacheable(name Name, proc *Stream) bool {
	ft.t3mu.Lock()
	defer ft.t3mu.Unlock()
	if v, ok := ft.t3direct[name]; ok {
		return v
	}
	v := true
	if data, err := proc.Data(); err == nil {
		v = scanType3(data)
	}
	if ft.t3direct == nil {
		ft.t3direct = map[Name]bool{}
	}
	ft.t3direct[name] = v
	return v
}

func scanType3(data []byte) bool {
	var fill, stroke, width, join, miter, caps, dash bool
	l := syntax.NewLexer(data)
	for {
		obj, ok := l.Next()
		if !ok {
			return false
		}
		kw, isKw := obj.(syntax.Keyword)
		if !isKw {
			continue
		}
		switch kw {
		case "g", "rg", "k", "sc", "scn", "cs":
			fill = true
		case "G", "RG", "K", "SC", "SCN", "CS":
			stroke = true
		case "w":
			width = true
		case "j":
			join = true
		case "M":
			miter = true
		case "J":
			caps = true
		case "d":
			dash = true
		case "gs":
			fill, stroke, width, join, miter, caps, dash = true, true, true, true, true, true, true

		case "S", "s":
			if !stroke || !width || !dash || !join || !miter || !caps {
				return true
			}
		case "B", "B*", "b", "b*":
			if !fill || !stroke || !width || !dash || !join || !miter || !caps {
				return true
			}
		case "f", "F", "f*", "sh", "Do", "Tj", "TJ", "'", "\"":
			if !fill {
				return true
			}
		}
	}
}

// programWidth asks the font program, or the base-14 metrics when the program
// is one of the fourteen, for the advance of a character.
func (ft *Font) programWidth(c Char) float32 {
	if ft.base != "" && c.Code < 256 {
		if w := font.StandardWidth(ft.base, ft.names[c.Code]); w >= 0 {
			return float32(w) / 1000
		}
	}
	if ft.prog == nil {
		return -1
	}
	gid := ft.Glyph(c)
	if gid < 0 {
		return -1
	}
	upem := float32(ft.prog.UnitsPerEm)
	if upem <= 0 {
		upem = 1000
	}
	if a := ft.prog.Advance(gid); a != 0 {
		return a / upem
	}
	return -1
}

// GlyphName returns the name /Differences gave a code, or "".
func (ft *Font) GlyphName(code uint32) Name {
	if code < 256 {
		return ft.encoding[code]
	}
	return ""
}
