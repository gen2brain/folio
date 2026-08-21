package font

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

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
	dir := envOr("PDF_REF_DIR", "/temp/pdf")
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
