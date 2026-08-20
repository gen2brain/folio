package pdf

import (
	"reflect"
	"sync"

	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
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
	// gids maps a character code to a glyph in prog, for a simple font.
	gids [256]int32
	// cid2gid is the /CIDToGIDMap stream of a CIDFontType2.
	toUnicode *CMap
	// base is the base-14 name a substituted font resolved to.
	base string
}

// font reads a font dictionary.
func (d *Document) font(obj Object) *Font {
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
		d.readType3(ft, dict)
		d.readSimpleWidths(ft, dict)
	default:
		d.readSimpleWidths(ft, dict)
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

// substitute picks one of the fourteen for a font the file did not embed.
func (d *Document) substitute(ft *Font) {
	name := string(ft.Name)
	if ft.Type0 {
		ft.substituted = true
	}
	ft.base = font.StandardName(name, ft.serif, ft.fixed, ft.symbolic, ft.forceBold, ft.italic)
	ft.prog = font.Standard(ft.base)
	ft.substituted = true
	if ft.prog == nil {
		d.errorf("no substitute for font /%s", ft.Name)
	}
}

// readToUnicode reads the /ToUnicode CMap, which maps codes to text.
func (d *Document) readToUnicode(ft *Font, dict Dict) {
	st := d.f.GetStream(dict["ToUnicode"])
	if st == nil {
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
	if code < p.NumGlyphs() && !p.HasCmap() {
		return code
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

func (d *Document) readType3(ft *Font, dict Dict) {
	f := d.f
	ft.FontMatrix = d.matrix(dict["FontMatrix"], ft.FontMatrix)
	ft.CharProcs = f.GetDict(dict["CharProcs"])
	ft.Resources = f.GetDict(dict["Resources"])
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
	if ft.Type0 {
		return ft.cidToGID(int(c.CID))
	}
	if c.Code < 256 {
		return int(ft.gids[c.Code])
	}
	return -1
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
	if r == 0 && !ft.Type3 && ft.prog != nil {
		r = font.RuneForName(ft.prog.GlyphName(ft.Glyph(c)))
	}
	if r == 0 {
		return 0xFFFD
	}
	return r
}

// GlyphNameOf returns what the font program calls the glyph a character
// selects, which is what a trace of the device calls it too.
func (ft *Font) GlyphNameOf(c Char) string {
	gid := ft.Glyph(c)
	if gid < 0 || ft.prog == nil || ft.Type3 {
		return ""
	}
	return ft.prog.GlyphName(gid)
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
	out := make([]Char, 0, len(s))
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
