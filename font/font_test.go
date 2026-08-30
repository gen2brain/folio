package font

import (
	"bytes"
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const refDir = "/media/adata/temp/corpus/pdf"

// TestStandardFonts parses the fourteen substitutes and checks that every one
// of them has the glyphs and the metrics the standard promises.
func TestStandardFonts(t *testing.T) {
	for name := range stdFontFile {
		f := Standard(name)
		if f == nil {
			t.Fatalf("%s: not loaded", name)
		}
		if f.Kind != KindCFF {
			t.Errorf("%s: kind %d, want CFF", name, f.Kind)
		}
		if f.NumGlyphs() < 100 {
			t.Errorf("%s: %d glyphs", name, f.NumGlyphs())
		}
		if !f.HasGlyphNames() {
			t.Errorf("%s: no glyph names", name)
		}

		glyph := "A"
		if name == "Symbol" {
			glyph = "alpha"
		} else if name == "ZapfDingbats" {
			glyph = "a9"
		}
		gid := f.GIDForName(glyph)
		if gid <= 0 {
			t.Errorf("%s: no glyph %q", name, glyph)
			continue
		}
		p := f.GlyphPath(gid)
		if p == nil || p.IsEmpty() {
			t.Errorf("%s: glyph %q has no outline", name, glyph)
		}
		if w := StandardWidth(name, glyph); w > 0 {
			if adv := f.Advance(gid); adv != float32(w) {
				t.Errorf("%s: %q advance %g, metrics say %g", name, glyph, adv, w)
			}
		}
	}
}

// TestStandardName holds the substitution rules to the names real files use.
func TestStandardName(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		serif, fixed, symbolic bool
		bold, italic           bool
		want                   string
	}{
		{name: "Helvetica", want: "Helvetica"},
		{name: "ABCDEF+Helvetica", want: "Helvetica"},
		{name: "Arial-BoldMT", want: "Helvetica-Bold"},
		{name: "ArialMT", want: "Helvetica"},
		{name: "TimesNewRomanPSMT", want: "Times-Roman"},
		{name: "CourierNewPSMT", want: "Courier"},
		{name: "Unknown", want: "Helvetica"},
		{name: "Unknown", serif: true, want: "Times-Roman"},
		{name: "Unknown", fixed: true, want: "Courier"},
		{name: "Unknown", bold: true, italic: true, want: "Helvetica-BoldOblique"},
		{name: "SomethingBoldItalic", want: "Helvetica-BoldOblique"},
		{name: "MonospaceBold", want: "Courier-Bold"},
		{name: "GeorgiaItalic", want: "Times-Italic"},
	} {
		got := StandardName(tc.name, tc.serif, tc.fixed, tc.symbolic, tc.bold, tc.italic)
		if got != tc.want {
			t.Errorf("StandardName(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if Standard(got) == nil {
			t.Errorf("StandardName(%q) = %q, which is not one of the fourteen", tc.name, got)
		}
	}
}

func TestRuneForName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want rune
	}{
		{"A", 'A'},
		{"space", ' '},
		{"eacute", 'é'},
		{"uni0041", 'A'},
		{"u00C6", 'Æ'},
		{"a.sc", 'a'},
		{"nonexistentglyphname", 0},
		{"", 0},
	} {
		if got := RuneForName(tc.name); got != tc.want {
			t.Errorf("RuneForName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEncodings(t *testing.T) {
	for _, name := range []string{"StandardEncoding", "WinAnsiEncoding", "MacRomanEncoding", "Symbol", "ZapfDingbats"} {
		e := Encoding(name)
		if e == nil {
			t.Fatalf("%s: missing", name)
		}
		if e[65] == "" && name != "Symbol" && name != "ZapfDingbats" {
			t.Errorf("%s: code 65 is unnamed", name)
		}
	}
	if Encoding("NoSuchEncoding") != nil {
		t.Error("an unknown encoding resolved")
	}
	if got := Encoding("WinAnsiEncoding")[0xA9]; got != "copyright" {
		t.Errorf("WinAnsi 0xA9 = %q", got)
	}
	if got := MacRomanCode('é'); got != 0x8E {
		t.Errorf("MacRomanCode(é) = %d, want 142", got)
	}
}

// TestSystemFonts parses whatever the machine has, which is the only test
// here that sees a font this project did not choose.
func TestSystemFonts(t *testing.T) {
	var files []string
	for _, dir := range []string{"/usr/share/fonts", "/System/Library/Fonts", "C:\\Windows\\Fonts"} {
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || len(files) >= 12 {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".ttf", ".otf", ".pfb":
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no system fonts")
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		f, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(name), err)
			continue
		}
		if f.NumGlyphs() == 0 {
			t.Errorf("%s: no glyphs", filepath.Base(name))
			continue
		}
		outlines := 0
		for gid := 0; gid < f.NumGlyphs() && gid < 200; gid++ {
			if p := f.GlyphPath(gid); p != nil && !p.IsEmpty() {
				outlines++
			}
		}
		if outlines == 0 {
			t.Errorf("%s: %d glyphs, none with an outline", filepath.Base(name), f.NumGlyphs())
		}
	}
}

// TestMalformedFonts feeds the parsers truncated and corrupted programs.
func TestMalformedFonts(t *testing.T) {
	seeds := [][]byte{}
	for name := range stdFontFile {
		if f := Standard(name); f != nil {
			seeds = append(seeds, []byte(stdFontData[stdFontFile[name]]))
			break
		}
	}
	if len(seeds) == 0 {
		t.Skip("no seed font")
	}
	orig := seeds[0]
	for _, frac := range []int{2, 3, 4, 8, 16} {
		b := make([]byte, len(orig)*(frac-1)/frac)
		copy(b, orig)
		mustNotPanic(t, b)
	}
	for i := 0; i < len(orig); i += 101 {
		b := make([]byte, len(orig))
		copy(b, orig)
		b[i] ^= 0xff
		mustNotPanic(t, b)
	}
	for _, b := range [][]byte{nil, {}, {0}, {1, 0, 4, 2}, []byte("%!PS-AdobeFont"), []byte("OTTO")} {
		mustNotPanic(t, b)
	}
	// A bare CFF whose charstring index is empty.
	mustNotPanic(t, []byte{
		1, 0, 4, 2,
		0, 0,
		0, 1, 1, 1, 9,
		28, 0, 25, 15, 28, 0, 23, 17,
		0, 0,
		0, 0,
		0, 0,
		0,
	})
}

func mustNotPanic(t *testing.T, b []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on %d bytes: %v", len(b), r)
		}
	}()
	f, err := Parse(b)
	if err != nil {
		return
	}
	for gid := 0; gid < f.NumGlyphs() && gid < 50; gid++ {
		f.GlyphPath(gid)
		f.Advance(gid)
		f.GlyphName(gid)
	}
	f.GIDForName("A")
	f.GIDForRune('A')
}

func FuzzParse(fu *testing.F) {
	for _, file := range stdFontFile {
		fu.Add([]byte(stdFontData[file]))
		break
	}
	if b, err := os.ReadFile(filepath.Join("..", "testdata", "mini.woff2")); err == nil {
		fu.Add(b)
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		f, err := Parse(b)
		if err != nil {
			return
		}
		for gid := 0; gid < f.NumGlyphs() && gid < 20; gid++ {
			f.GlyphPath(gid)
		}
		// The layout tables are offsets into offsets, all of them the file's
		// own, so shaping is where a broken font is most likely to be read
		// off the end of.
		f.Shape([]rune("مها شَدّ שָׁ áb fi"), true)
		f.Shape([]rune("مها fi"), false)
	})
}

func FuzzBrotli(fu *testing.F) {
	if b, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.br")); err == nil {
		fu.Add(b)
	}
	fu.Add([]byte{0x0b})
	fu.Fuzz(func(t *testing.T, b []byte) {
		const limit = 1 << 20
		out, err := brotliDecode(b, limit)
		if err == nil && len(out) > limit {
			t.Fatalf("%d bytes past the limit", len(out))
		}
	})
}

// TestStandardConcurrent checks that two documents rendering at the same time
// can both reach the same standard font. Standard hands out one parsed program
// for all of them, and it fills a glyph outline cache as it is asked.
func TestStandardConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := Standard("Helvetica")
			if f == nil {
				t.Error("no Helvetica")
				return
			}
			for gid := 0; gid < 64; gid++ {
				f.GlyphPath(gid)
			}
		}()
	}
	wg.Wait()
}

// TestSystemIndex asks the machine for what it has. There is nothing to
// compare against on a machine with no fonts, so it skips there.
func TestSystemIndex(t *testing.T) {
	ix := systemIndex()
	if len(ix.all) == 0 {
		t.Skip("the machine has no fonts")
	}
	for _, e := range ix.all {
		if e.family == "" {
			t.Errorf("%s is indexed under no family", e.path)
		}
		if e.weight < 1 || e.weight > 1000 {
			t.Errorf("%s has weight %d", e.path, e.weight)
		}
	}
	// Whatever the machine has, the family it is indexed under is the one it
	// answers to.
	e := ix.all[0]
	f := SystemFont(e.family, e.weight >= 600, e.italic)
	if f == nil {
		t.Fatalf("%q is indexed and not found", e.family)
	}
	if foldName(f.Family) != foldName(e.family) && !hasPrefixFold(foldName(f.Family), foldName(e.family)) {
		t.Errorf("asked for %q and got %q", e.family, f.Family)
	}
	if SystemFont("a family no machine has, surely", false, false) != nil {
		t.Error("a family that is not there was found")
	}
	if SystemFont("", false, false) != nil {
		t.Error("the empty family was found")
	}
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestFallback checks that a character the base fourteen cannot draw finds a
// face that can, when the machine has one.
func TestFallback(t *testing.T) {
	if len(systemIndex().all) == 0 {
		t.Skip("the machine has no fonts")
	}
	for _, r := range []rune{'日', 'あ', '한', 'Ж', 'α'} {
		f := Fallback(r, false, false)
		if f == nil {
			t.Logf("%c: the machine has no face for it", r)
			continue
		}
		if f.GIDForRune(r) <= 0 {
			t.Errorf("%c: %q was chosen and has no glyph for it", r, f.Family)
		}
		if f2 := Fallback(r, false, false); f2 != f {
			t.Errorf("%c: the answer is not remembered", r)
		}
	}
	if Fallback(0x10fffd, false, false) != nil {
		t.Log("a private use character found a face, which is allowed")
	}
}

// TestAddFontDir checks that a caller's own directory is searched, which is
// what a program shipping its own fonts needs.
func TestAddFontDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notafont.ttf"), []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	AddFontDir(dir)
	// The index is built once, so this only checks that describe survives a
	// file that is not a font.
	if e := describe(filepath.Join(dir, "notafont.ttf")); e != nil {
		t.Errorf("nonsense described as %+v", e)
	}
	if e := describe(filepath.Join(dir, "missing.ttf")); e != nil {
		t.Errorf("a missing file described as %+v", e)
	}
}

func TestFoldName(t *testing.T) {
	cases := [][2]string{
		{"Noto Sans CJK JP", "notosanscjkjp"},
		{"Times New Roman", "timesnewroman"},
		{"  DejaVu-Sans  ", "dejavusans"},
		{"", ""},
	}
	for _, c := range cases {
		if got := foldName(c[0]); got != c[1] {
			t.Errorf("foldName(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestScriptOf(t *testing.T) {
	cases := []struct {
		r rune
		s script
	}{
		{'a', scriptLatin}, {'日', scriptHan}, {'あ', scriptKana}, {'한', scriptHangul},
		{'Ж', scriptCyrillic}, {'α', scriptGreek}, {'ع', scriptArabic}, {'א', scriptHebrew},
		{'ก', scriptThai}, {'क', scriptDevanagari},
	}
	for _, c := range cases {
		if got := scriptOf(c.r); got != c.s {
			t.Errorf("scriptOf(%c) = %d, want %d", c.r, got, c.s)
		}
	}
	for k := range scriptOther {
		if len(fallbackFamilies[k]) == 0 {
			t.Errorf("script %d has no families to try", k)
		}
	}
}

// TestWOFF unpacks the container a web font is delivered in, which is a
// deflated table at a time.
func TestWOFF(t *testing.T) {
	src := Standard("Times-Roman")
	if src == nil {
		t.Skip("no substitute to pack")
	}
	for _, name := range []string{"", "wOFF"} {
		if _, err := Parse([]byte(name)); err == nil {
			t.Errorf("%q parsed as a font", name)
		}
	}
	// A truncated container is refused rather than read past.
	b := append([]byte("wOFF"), make([]byte, 60)...)
	if _, err := Parse(b); err == nil {
		t.Error("an empty WOFF parsed")
	}
}

// TestWOFFCorpus reads the web fonts of the corpus, which is the only place
// this has real ones.
func TestWOFFCorpus(t *testing.T) {
	dir := envOr("PDF_REF_DIR", refDir)
	// The obfuscated copies of a book are scrambled until the container
	// unscrambles them, which is the html package's work and not this one's.
	all, _ := filepath.Glob(filepath.Join(dir, "corpus/books/epub3/30/*/EPUB/*.woff"))
	var paths []string
	for _, p := range all {
		if !strings.Contains(p, ".obf.") {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		t.Skip("no web fonts in the corpus")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if f.Family == "" || f.NumGlyphs() == 0 || f.GIDForRune('A') <= 0 {
			t.Errorf("%s: family %q, %d glyphs, A is %d", p, f.Family, f.NumGlyphs(), f.GIDForRune('A'))
		}
		if f.GlyphPath(f.GIDForRune('A')) == nil {
			t.Errorf("%s: the A has no outline", p)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestBrotli decodes what the brotli command line compresses, which is the
// only oracle for RFC 7932 this needs.
func TestBrotli(t *testing.T) {
	if _, err := exec.LookPath("brotli"); err != nil {
		t.Skip("no brotli command")
	}
	var random [1 << 14]byte
	x := uint32(12345)
	for i := range random {
		x = x*1664525 + 1013904223
		random[i] = byte(x >> 24)
	}
	prose := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) +
		"<html><head><title>Time of the day</title></head><body> and \"other\" things.</body></html>"
	cases := map[string][]byte{
		"empty":  nil,
		"one":    []byte("x"),
		"prose":  []byte(prose),
		"font":   []byte(stdFontData["FoxitSans"]),
		"random": random[:],
	}
	for name, want := range cases {
		for _, q := range []string{"0", "5", "9", "11"} {
			for _, w := range []string{"10", "16", "24"} {
				cmd := exec.Command("brotli", "-c", "-q", q, "-w", w)
				cmd.Stdin = bytes.NewReader(want)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					t.Fatalf("brotli -q %s -w %s: %v", q, w, err)
				}
				got, err := brotliDecode(out.Bytes(), len(want)+1)
				if err != nil {
					t.Errorf("%s q%s w%s: %v", name, q, w, err)
					continue
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s q%s w%s: %d bytes, want %d", name, q, w, len(got), len(want))
				}
			}
		}
	}
}

// TestBrotliFile decodes the one stream checked in, which carries enough
// English to reach the static dictionary and the word transforms.
func TestBrotliFile(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.br"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := brotliDecode(src, len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%d bytes, want %d", len(got), len(want))
	}
	if _, err := brotliDecode(src, len(want)-1); err == nil {
		t.Error("a stream decoded past its limit")
	}
	for i := range src {
		b := append([]byte(nil), src[:i]...)
		if _, err := brotliDecode(b, len(want)); err == nil {
			t.Errorf("%d bytes of the stream decoded whole", i)
		}
	}
}

// TestWOFF2 unpacks the web font checked in, whose four glyphs are one of
// each shape the glyph transform has a path for, and holds the result to
// what the reference decoder makes of the same file.
func TestWOFF2(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "mini.woff2"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "testdata", "mini.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := woff2SFNT(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("%d bytes of sfnt, want %d, differing at %d", len(got), len(want), i)
	}

	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Family != "Mini" || f.NumGlyphs() != 4 || f.UnitsPerEm != 1000 {
		t.Errorf("family %q, %d glyphs, %d per em", f.Family, f.NumGlyphs(), f.UnitsPerEm)
	}
	for i, c := range "ABC" {
		g := f.GIDForRune(c)
		if g != i+1 {
			t.Errorf("%c is glyph %d, want %d", c, g, i+1)
		}
		if f.GlyphPath(g) == nil {
			t.Errorf("%c has no outline", c)
		}
	}
	for g, want := range []float32{500, 600, 700, 900} {
		if got := f.Advance(g); got != want {
			t.Errorf("glyph %d is %v wide, want %v", g, got, want)
		}
	}
	if _, err := Parse(append([]byte("wOF2"), make([]byte, 60)...)); err == nil {
		t.Error("an empty WOFF2 parsed")
	}
	for i := range src {
		if _, err := Parse(src[:i]); err == nil {
			t.Errorf("%d bytes of the web font parsed whole", i)
		}
	}
}

// TestJoining covers the form a cursive letter takes from what stands either
// side of it, which is what the four Arabic features pick between.
func TestJoining(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		want joinType
	}{
		{'م', joinDual},           // meem joins both ways
		{'ا', joinRight},          // alef joins only backwards
		{0x0640, joinCausing},     // tatweel
		{0x064E, joinTransparent}, // fatha, a mark
		{'A', joinNone},
	} {
		if got := joinTypeOf(tc.r); got != tc.want {
			t.Errorf("U+%04X is %d, want %d", tc.r, got, tc.want)
		}
	}

	// In "مها" the meem opens, the heh is in the middle and the alef ends.
	text := []rune("مها")
	b := &buffer{items: make([]item, len(text))}
	for i := range b.items {
		b.items[i].mask = 1
	}
	b.joining(text)
	want := []int{formInit, formMedi, formFina}
	for i, w := range want {
		if b.items[i].mask&(1<<uint(w)) == 0 {
			t.Errorf("%q took the wrong form, mask %b", text[i], b.items[i].mask)
		}
	}
	// A letter on its own stands alone.
	one := []rune("م")
	b = &buffer{items: []item{{mask: 1}}}
	b.joining(one)
	if b.items[0].mask&(1<<formIsol) == 0 {
		t.Errorf("a letter on its own took mask %b", b.items[0].mask)
	}
}

// TestMarkOrder covers the order the marks of one cluster are put in, which
// is not the order their combining classes alone would give: the fixed
// position classes of Hebrew and Arabic are permuted first.
func TestMarkOrder(t *testing.T) {
	// A shin dot is written after a qamats and drawn before it.
	runes := []rune{0x05E9, 0x05B8, 0x05C1}
	clusters := []int{0, 0, 0}
	sortMarks(runes, clusters)
	if want := []rune{0x05E9, 0x05C1, 0x05B8}; string(runes) != string(want) {
		t.Errorf("sorted to %04X, want %04X", runes, want)
	}
	// A shadda comes before the vowel it is written after.
	runes = []rune{0x0628, 0x064E, 0x0651}
	clusters = []int{0, 0, 0}
	sortMarks(runes, clusters)
	if want := []rune{0x0628, 0x0651, 0x064E}; string(runes) != string(want) {
		t.Errorf("sorted to %04X, want %04X", runes, want)
	}
	// A mark keeps the cluster it came in with.
	if clusters[0] != 0 || clusters[2] != 0 {
		t.Errorf("clusters came out %v", clusters)
	}
}

// TestDecompose covers what a font with no glyph for a precomposed character
// is handed instead.
func TestDecompose(t *testing.T) {
	a, b, ok := decomposeRune('á')
	if !ok || a != 'a' || b != 0x0301 {
		t.Errorf("decomposed to %04X %04X %v", a, b, ok)
	}
	if _, _, ok := decomposeRune('a'); ok {
		t.Error("a plain letter decomposed")
	}
}

// TestShapeArabic shapes a run through whatever Arabic face the machine has
// and checks the letters joined, which is the whole point of the tables.
func TestShapeArabic(t *testing.T) {
	f := Fallback('م', false, false)
	if f == nil || !f.Shaped() {
		t.Skip("no Arabic face with layout tables")
	}
	nominal := f.GIDForRune('م')
	gs := f.Shape([]rune("مها"), true)
	if len(gs) != 3 {
		t.Fatalf("%d glyphs, want 3", len(gs))
	}
	// The run comes back in the order it is drawn, so the meem is last.
	if gs[2].Cluster != 0 || gs[0].Cluster != 2 {
		t.Errorf("clusters %d %d %d, want the run reversed", gs[0].Cluster, gs[1].Cluster, gs[2].Cluster)
	}
	if gs[2].GID == nominal {
		t.Errorf("the meem is still glyph %d, want the joined form", nominal)
	}
	for _, g := range gs {
		if g.GID <= 0 {
			t.Errorf("glyph %d is not there", g.GID)
		}
	}
}

// TestSyllables covers the grammar a run of an Indic script is cut into
// syllables by, which is what everything else works on.
func TestSyllables(t *testing.T) {
	cats := func(s string) []uint8 {
		out := make([]uint8, len([]rune(s)))
		for i, r := range []rune(s) {
			out[i], _ = indicOf(r)
		}
		return out
	}
	for _, tc := range []struct {
		text string
		want [][2]int
	}{
		// A consonant, a consonant with a halant and a consonant, and a
		// consonant with a matra are each one syllable.
		{"क", [][2]int{{0, 1}}},
		{"क्क", [][2]int{{0, 3}}},
		{"कि", [][2]int{{0, 2}}},
		// A word is as many syllables as it has consonant groups.
		// A consonant, a halant, a consonant and a matra are one syllable.
		{"नमस्ते", [][2]int{{0, 1}, {1, 2}, {2, 6}}},
		{"कर्म", [][2]int{{0, 1}, {1, 4}}},
	} {
		var got [][2]int
		for _, s := range syllables(cats(tc.text)) {
			got = append(got, [2]int{s[0], s[1]})
		}
		if len(got) != len(tc.want) {
			t.Errorf("%q cut into %v, want %v", tc.text, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q cut into %v, want %v", tc.text, got, tc.want)
				break
			}
		}
	}
}

// TestIndicCategories covers what the table says about the characters the
// reordering asks about.
func TestIndicCategories(t *testing.T) {
	for _, tc := range []struct {
		r   rune
		cat uint8
		pos uint8
	}{
		{'क', inC, posBaseC},     // a consonant
		{'र', inRa, posBaseC},    // the one that becomes a reph
		{0x094D, inH, posEnd},    // the virama
		{0x093F, inM, posPreM},   // the matra written before its consonant
		{0x0947, inM, posAboveC}, // and one written above it
		{0x093C, inN, posEnd},    // the nukta
		{0x0902, inSM, posSMVD},  // anusvara
	} {
		cat, pos := indicOf(tc.r)
		if cat != tc.cat || pos != tc.pos {
			t.Errorf("U+%04X is (%d,%d), want (%d,%d)", tc.r, cat, pos, tc.cat, tc.pos)
		}
	}
}

// TestShapeDevanagari shapes a word whose matra is written after the
// consonant it is drawn before, which is the reordering the script needs.
func TestShapeDevanagari(t *testing.T) {
	f := Fallback('क', false, false)
	if f == nil || !f.Shaped() {
		t.Skip("no Devanagari face with layout tables")
	}
	// In "कि" the matra is written second and drawn first, so the consonant
	// is the second glyph and it is the one the letter shapes to on its own.
	gs := f.Shape([]rune("कि"), false)
	if len(gs) != 2 {
		t.Fatalf("%d glyphs, want 2", len(gs))
	}
	alone := f.Shape([]rune("क"), false)
	if len(alone) != 1 {
		t.Fatalf("%d glyphs for the consonant on its own", len(alone))
	}
	if gs[1].GID != alone[0].GID {
		t.Errorf("the consonant is glyph %d and came out %d, so the matra did not move",
			alone[0].GID, gs[1].GID)
	}
	// Both came out of one cluster, so a caret cannot land between them.
	if gs[0].Cluster != 0 || gs[1].Cluster != 0 {
		t.Errorf("clusters %d and %d, want both nought", gs[0].Cluster, gs[1].Cluster)
	}
}

// TestBidiCharacterConformance runs the Unicode bidirectional character test,
// which gives the levels and the visual order of real character sequences. It
// needs the reference directory tools/fetch.sh fills.
func TestBidiCharacterConformance(t *testing.T) {
	name := filepath.Join(cmp.Or(os.Getenv("PDF_REF_DIR"), refDir), "specs/BidiCharacterTest.txt")
	b, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	total, bad := 0, 0
	for n, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		f := strings.Split(line, ";")
		if len(f) != 5 {
			t.Fatalf("line %d: %d fields", n+1, len(f))
		}
		var text []rune
		for _, s := range strings.Fields(f[0]) {
			v, err := strconv.ParseInt(s, 16, 32)
			if err != nil {
				t.Fatalf("line %d: %v", n+1, err)
			}
			text = append(text, rune(v))
		}
		// The second field is the direction asked for: 0 and 1 are the two
		// directions, 2 is the one the text decides.
		base := -1
		switch f[1] {
		case "0":
			base = 0
		case "1":
			base = 1
		}
		total++

		res := bidiResolve(text, base)
		if got, want := int(res.para), atoiOr(f[2], -1); got != want {
			if bad++; bad <= 10 {
				t.Errorf("line %d: paragraph level %d, want %d", n+1, got, want)
			}
			continue
		}
		levels := res.line(0, len(text))
		wantLevels := strings.Fields(f[3])
		if len(wantLevels) != len(levels) {
			t.Fatalf("line %d: %d levels, want %d", n+1, len(levels), len(wantLevels))
		}
		fail := false
		for i, w := range wantLevels {
			// An x is a character the algorithm removed, which has no level.
			if w == "x" {
				if !res.gone[i] {
					fail = true
				}
				continue
			}
			if res.gone[i] || int(levels[i]) != atoiOr(w, -1) {
				fail = true
			}
		}
		if fail {
			if bad++; bad <= 10 {
				t.Errorf("line %d: levels %v, want %v (%q)", n+1, levels, wantLevels, f[0])
			}
			continue
		}
		// The last field is what is drawn, left to right, with the removed
		// characters left out.
		var order []int
		for _, i := range bidiOrder(levels) {
			if !res.gone[i] {
				order = append(order, i)
			}
		}
		var want []int
		for _, s := range strings.Fields(f[4]) {
			want = append(want, atoiOr(s, -1))
		}
		if !slices.Equal(order, want) {
			if bad++; bad <= 10 {
				t.Errorf("line %d: order %v, want %v (%q)", n+1, order, want, f[0])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d bidi cases wrong", bad, total)
	}
	t.Logf("%d bidi character cases", total)
}

func atoiOr(s string, or int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return or
	}
	return v
}

// bidiSample is a character of each class, which the class based conformance
// test is run on. None of them is a bracket, so rule N0 does not fire.
var bidiSample = map[string]rune{
	"L": 'A', "R": 0x05d0, "AL": 0x0627, "EN": '0', "ES": '+', "ET": '#',
	"AN": 0x0660, "CS": ',', "NSM": 0x0300, "BN": 0x00ad, "B": 0x2029,
	"S": '\t', "WS": ' ', "ON": '!', "LRE": 0x202a, "RLE": 0x202b,
	"PDF": 0x202c, "LRO": 0x202d, "RLO": 0x202e, "LRI": 0x2066,
	"RLI": 0x2067, "FSI": 0x2068, "PDI": 0x2069,
}

// TestBidiConformance runs the Unicode bidirectional test, which gives the
// levels and the order of every combination of classes rather than of real
// characters.
func TestBidiConformance(t *testing.T) {
	name := filepath.Join(cmp.Or(os.Getenv("PDF_REF_DIR"), refDir), "specs/BidiTest.txt")
	b, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	// The samples have to be what they claim, or the test proves nothing.
	for name, r := range bidiSample {
		if got := bidiClassOf(r); bidiClasses[got] != name {
			t.Fatalf("U+%04X is %s, want %s", r, bidiClasses[got], name)
		}
	}

	var wantLevels []string
	var wantOrder []int
	total, bad := 0, 0
	for n, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if v, ok := strings.CutPrefix(line, "@Levels:"); ok {
			wantLevels = strings.Fields(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "@Reorder:"); ok {
			wantOrder = nil
			for _, s := range strings.Fields(v) {
				wantOrder = append(wantOrder, atoiOr(s, -1))
			}
			continue
		}
		classes, bits, ok := strings.Cut(line, ";")
		if !ok {
			t.Fatalf("line %d: %q", n+1, line)
		}
		var text []rune
		for _, c := range strings.Fields(classes) {
			r, ok := bidiSample[c]
			if !ok {
				t.Fatalf("line %d: no sample for class %q", n+1, c)
			}
			text = append(text, r)
		}
		set := atoiOr(strings.TrimSpace(bits), 0)
		for _, tc := range []struct {
			bit  int
			base int
		}{{1, -1}, {2, 0}, {4, 1}} {
			if set&tc.bit == 0 {
				continue
			}
			total++
			res := bidiResolve(text, tc.base)
			levels := res.line(0, len(text))
			fail := len(wantLevels) != len(levels)
			for i := 0; !fail && i < len(levels); i++ {
				if wantLevels[i] == "x" {
					fail = !res.gone[i]
					continue
				}
				fail = res.gone[i] || int(levels[i]) != atoiOr(wantLevels[i], -1)
			}
			var order []int
			for _, i := range bidiOrder(levels) {
				if !res.gone[i] {
					order = append(order, i)
				}
			}
			if !fail && !slices.Equal(order, wantOrder) {
				fail = true
			}
			if fail {
				if bad++; bad <= 10 {
					t.Errorf("line %d base %d: levels %v order %v, want %v and %v (%s)",
						n+1, tc.base, levels, order, wantLevels, wantOrder, classes)
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d bidi cases wrong", bad, total)
	}
	t.Logf("%d bidi class cases", total)
}
