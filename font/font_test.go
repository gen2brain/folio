package font

import (
	"os"
	"path/filepath"
	"strings"
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
