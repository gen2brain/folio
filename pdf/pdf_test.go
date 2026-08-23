package pdf

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gen2brain/folio/raster"
)

func open(t *testing.T, name string) *Document {
	t.Helper()
	d, err := Open(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPageGeometry(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Bounds(), (raster.Rect{X1: 200, Y1: 100}); got != want {
		t.Errorf("Bounds = %v, want %v", got, want)
	}
	if got, want := p.Matrix(72), (raster.Matrix{A: 1, D: -1, F: 100}); got != want {
		t.Errorf("Matrix = %v, want %v", got, want)
	}
	if got, want := p.Matrix(144), (raster.Matrix{A: 2, D: -2, F: 200}); got != want {
		t.Errorf("Matrix(144) = %v, want %v", got, want)
	}
	if got, want := p.DeviceBounds(72), (raster.Rect{X1: 200, Y1: 100}); got != want {
		t.Errorf("DeviceBounds = %v, want %v", got, want)
	}
}

func TestRotation(t *testing.T) {
	for _, rot := range []int{0, 90, 180, 270} {
		page := Dict{
			"MediaBox": Array{Integer(0), Integer(0), Integer(200), Integer(100)},
			"Rotate":   Integer(rot),
		}
		p := &Page{doc: &Document{}, dict: page}
		p.doc.f = open(t, "minimal.pdf").f
		b := p.DeviceBounds(72)
		if b.X0 != 0 || b.Y0 != 0 {
			t.Errorf("rotate %d: bounds start at %v", rot, b)
		}
		w, h := b.X1-b.X0, b.Y1-b.Y0
		if rot == 90 || rot == 270 {
			w, h = h, w
		}
		if w != 200 || h != 100 {
			t.Errorf("rotate %d: %gx%g, want 200x100", rot, w, h)
		}
	}
}

// TestTrace runs the bundled files through the trace device and checks what
// the interpreter produced.
func TestTrace(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, _ := d.Page(0)
	var buf bytes.Buffer
	if err := p.Run(NewTraceDevice(&buf), p.Matrix(72)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		`<fill_text colorspace="DeviceGray" color="0" ri="1" bp="1" op="0" opm="0" transform="1 0 0 -1 0 100">`,
		`<span font="Helvetica" wmode="0" bidi="0" trm="24 0 0 24">`,
		`<g unicode="H" glyph="H" x="20" y="40" adv="0.722"/>`,
		`<g unicode="i" glyph="i" x="37.328" y="40" adv="0.222"/>`,
		`<fill_path winding="nonzero" colorspace="DeviceRGB" color="0 0 1"`,
		`<moveto x="10" y="10"/>`,
		`<lineto x="60" y="10"/>`,
		`<closepath/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trace is missing %s\ngot:\n%s", want, got)
		}
	}
}

func TestBBoxDevice(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, _ := d.Page(0)
	dev := NewBBoxDevice()
	p.Run(dev, p.Matrix(72))
	b := dev.Bounds()
	if b.X0 != 10 || b.Y1 != 90 {
		t.Errorf("bounds = %v", b)
	}
	if b.IsEmpty() || b.X1 > 200 || b.Y0 < 0 {
		t.Errorf("bounds = %v, outside the page", b)
	}
}

// TestInterpreter drives one content stream per case and checks what reaches
// the device.
func TestInterpreter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
		absent  []string
	}{
		{
			name:    "rect fill",
			content: "1 0 0 rg 10 20 30 40 re f",
			want:    []string{`fill_path`, `color="1 0 0"`, `<moveto x="10" y="20"/>`, `<closepath/>`},
		},
		{
			name:    "even odd fill",
			content: "0 0 100 100 re 20 20 60 60 re f*",
			want:    []string{`winding="eofill"`},
		},
		{
			name:    "stroke state",
			content: "5 w 1 J 2 j 3 M [2 3] 1 d 0 0 m 10 10 l S",
			want: []string{`linewidth="5"`, `linecap="1,1,1"`, `linejoin="2"`,
				`miterlimit="3"`, `dash_phase="1" dash="2 3"`},
		},
		{
			name:    "cm concatenates",
			content: "2 0 0 2 5 5 cm 0 0 10 10 re f",
			want:    []string{`transform="2 0 0 -2 5 95"`},
		},
		{
			name:    "q restores",
			content: "q 2 0 0 2 0 0 cm Q 0 0 10 10 re f",
			want:    []string{`transform="1 0 0 -1 0 100"`},
		},
		{
			name:    "clip then pop",
			content: "q 0 0 50 50 re W n 0 0 10 10 re f Q",
			want:    []string{`<clip_path winding="nonzero"`, `<pop_clip/>`},
		},
		{
			name:    "curves",
			content: "0 0 m 1 2 3 4 5 6 c 7 8 9 10 v 11 12 13 14 y f",
			want: []string{`<curveto x1="1" y1="2" x2="3" y2="4" x3="5" y3="6"/>`,
				`<curveto x1="5" y1="6" x2="7" y2="8" x3="9" y3="10"/>`,
				`<curveto x1="11" y1="12" x2="13" y2="14" x3="13" y3="14"/>`},
		},
		{
			name:    "alpha and blend",
			content: "/GS1 gs 0 0 10 10 re f",
			want:    []string{`alpha="0.5"`, `<group`, `blendmode="Multiply"`},
		},
		{
			name:    "invisible text is ignored",
			content: "BT /F1 12 Tf 3 Tr (hi) Tj ET",
			want:    []string{`<ignore_text`},
			absent:  []string{`<fill_text`},
		},
		{
			name:    "text clipping",
			content: "BT /F1 12 Tf 7 Tr (hi) Tj ET",
			want:    []string{`<clip_text`},
		},
		{
			name:    "unbalanced q is closed",
			content: "q 0 0 50 50 re W n 0 0 10 10 re f",
			want:    []string{`<pop_clip/>`},
		},
		{
			name:    "inline image",
			content: "BI /W 2 /H 2 /CS /G /BPC 8 ID \x00\x01\x02\x03 EI",
			want:    []string{`<fill_image`, `width="2" height="2"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runContent(t, tc.content)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in\n%s", w, got)
				}
			}
		})
	}
}

// runContent builds a one page document around a content stream and returns
// its trace.
func runContent(t *testing.T, content string) string {
	t.Helper()
	d := open(t, "minimal.pdf")
	page := Dict{
		"Type":     Name("Page"),
		"MediaBox": Array{Integer(0), Integer(0), Integer(200), Integer(100)},
		"Resources": Dict{
			"Font": Dict{"F1": Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica")}},
			"ExtGState": Dict{"GS1": Dict{
				"ca": Real(0.5), "CA": Real(0.5), "BM": Name("Multiply"),
			}},
		},
	}
	p := &Page{doc: d, dict: page}

	var buf bytes.Buffer
	dev := NewTraceDevice(&buf)
	ip := p.newInterp(dev, p.Matrix(72))
	ip.run([]byte(content))
	ip.finish()
	dev.Close()
	return buf.String()
}

// TestMalformedContent feeds the interpreter broken content streams. None may
// panic, hang or leave the device stack unbalanced.
func TestMalformedContent(t *testing.T) {
	for _, content := range []string{
		"", "q", "Q", "Q Q Q Q", "BT", "ET", "f", "S", "W n",
		"1 0 0 1 0 0 cm cm cm", "/Nonexistent Do", "/Nope gs", "/Nope sh",
		"BI ID", "BI /W 5 /H 5 ID abc", "BI /W 1 /H 1 /BPC 8 /CS /G ID",
		"BI /W 1 /H 1 /BPC 8 /CS /G ID ", "BI /W 9 /H 9 /L 9999 ID x", "BT (x) Tj", "BT /F1 Tf (x) Tj ET",
		"0 0 m 1 1 l h h h f", "[ 1 2 3 ] 0 d 0 0 m 1 1 l S",
		strings.Repeat("q ", 200) + strings.Repeat("Q ", 200),
		strings.Repeat("q ", 1000),
		strings.Repeat("BT ", 100) + "(x) Tj",
		"1e400 1e400 m 1e400 1e400 l f",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%q: panic: %v", content, r)
				}
			}()
			runContent(t, content)
		}()
	}
}

func FuzzContent(f *testing.F) {
	f.Add("q 1 0 0 1 5 5 cm 0 0 10 10 re f Q")
	f.Add("BT /F1 12 Tf (hi) Tj ET")
	f.Add("BI /W 2 /H 2 /CS /G /BPC 8 ID \x00\x01\x02\x03 EI")
	f.Fuzz(func(t *testing.T, content string) {
		d, err := Open(filepath.Join("..", "testdata", "minimal.pdf"))
		if err != nil {
			t.Skip()
		}
		defer d.Close()
		p, err := d.Page(0)
		if err != nil {
			t.Skip()
		}
		ip := p.newInterp(NewTraceDevice(&bytes.Buffer{}), p.Matrix(72))
		ip.run([]byte(content))
		ip.finish()
	})
}

func FuzzCMap(f *testing.F) {
	f.Add("1 begincodespacerange <00> <FF> endcodespacerange\n" +
		"2 begincidrange <00> <7f> 1 <80> <ff> 200 endcidrange\n")
	f.Add("1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"2 beginbfchar <41> <00660066> <42> <0042> endbfchar\n")
	f.Add("/WMode 1 def\n1 beginbfrange <00> <ff> [<41> <42>] endbfrange\n")
	f.Fuzz(func(t *testing.T, src string) {
		d, err := Open(filepath.Join("..", "testdata", "minimal.pdf"))
		if err != nil {
			t.Skip()
		}
		defer d.Close()
		cm := d.parseCMap([]byte(src), 0)
		if cm == nil {
			t.Fatal("no CMap")
		}
		for _, code := range []uint32{0, 1, 0x41, 0xff, 0x1234, 0xffff, 0xffffffff} {
			cm.Lookup(code)
			cm.Text(code)
		}
		cm.Next([]byte(src))
	})
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

// TestFontMapping checks that a code reaches the right glyph, character and
// width through each kind of font dictionary.
func TestFontMapping(t *testing.T) {
	tests := []struct {
		name    string
		font    Dict
		text    string
		wantRun string
		wantAdv float32
	}{
		{
			name: "base 14 with no widths",
			font: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica")},
			text: "H", wantRun: "H", wantAdv: 0.722,
		},
		{
			name: "base 14 with widths",
			font: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica"),
				"FirstChar": Integer(72), "Widths": Array{Integer(500)}},
			text: "H", wantRun: "H", wantAdv: 0.5,
		},
		{
			name: "differences rename a code",
			font: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica"),
				"Encoding": Dict{"Differences": Array{Integer(72), Name("bullet")}}},
			text: "H", wantRun: "•",
		},
		{
			name: "winansi encoding",
			font: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica"),
				"Encoding": Name("WinAnsiEncoding")},
			text: "\xa9", wantRun: "©",
		},
		{
			name: "symbol font",
			font: Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Symbol")},
			text: "a", wantRun: "α",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := open(t, "minimal.pdf")
			ft := d.font(tc.font, nil)
			if ft == nil {
				t.Fatal("font did not load")
			}
			cs := ft.Decode([]byte(tc.text))
			if len(cs) != len(tc.text) {
				t.Fatalf("decoded %d characters from %q", len(cs), tc.text)
			}
			c := cs[0]
			if got := string(ft.Rune(c)); got != tc.wantRun {
				t.Errorf("rune = %q, want %q", got, tc.wantRun)
			}
			if ft.Glyph(c) <= 0 {
				t.Errorf("glyph = %d", ft.Glyph(c))
			}
			if tc.wantAdv != 0 && c.Width != tc.wantAdv {
				t.Errorf("width = %g, want %g", c.Width, tc.wantAdv)
			}
		})
	}
}

// TestToUnicodeLigature checks the one to many mapping that a ligature needs.
func TestToUnicodeLigature(t *testing.T) {
	d := open(t, "minimal.pdf")
	cmapData := "/CIDInit /ProcSet findresource begin\n" +
		"1 begincodespacerange <00> <FF> endcodespacerange\n" +
		"2 beginbfchar <41> <00660066> <42> <0042> endbfchar\n" +
		"endcmap\n"
	cm := d.parseCMap([]byte(cmapData), 0)
	if got := cm.Text('A'); got != "ff" {
		t.Errorf("Text('A') = %q, want %q", got, "ff")
	}
	if got := cm.Text('B'); got != "B" {
		t.Errorf("Text('B') = %q, want %q", got, "B")
	}
}

// TestCMapLongSection checks a cidrange section longer than the operand stack
// the parser once kept, which silently dropped every entry past the 21st.
func TestCMapLongSection(t *testing.T) {
	d := open(t, "minimal.pdf")
	var b strings.Builder
	b.WriteString("/CIDInit /ProcSet findresource begin\n")
	b.WriteString("1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	const n = 200
	fmt.Fprintf(&b, "%d begincidrange\n", n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "<%04x> <%04x> %d\n", i, i, 1000+i)
	}
	b.WriteString("endcidrange\nendcmap\n")

	cm := d.parseCMap([]byte(b.String()), 0)
	for _, code := range []uint32{0, 20, 21, 99, n - 1} {
		if got := cm.Lookup(code); got != 1000+code {
			t.Errorf("Lookup(%d) = %d, want %d", code, got, 1000+code)
		}
	}
}

// TestCMapOverlappingRanges checks that ranges which overlap keep the order
// they were written in, where the first match wins, rather than being bisected.
func TestCMapOverlappingRanges(t *testing.T) {
	d := open(t, "minimal.pdf")
	cmapData := "/CIDInit /ProcSet findresource begin\n" +
		"1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"2 begincidrange <0000> <00ff> 100 <0010> <001f> 200 endcidrange\n" +
		"endcmap\n"
	cm := d.parseCMap([]byte(cmapData), 0)
	if cm.sorted {
		t.Error("overlapping ranges were sorted")
	}
	if got := cm.Lookup(0x18); got != 100+0x18 {
		t.Errorf("Lookup(0x18) = %d, want %d", got, 100+0x18)
	}
}

// TestPredefinedCMap checks a built in byte encoding, which decides how many
// bytes a character code takes.
func TestPredefinedCMap(t *testing.T) {
	d := open(t, "minimal.pdf")
	cm := d.predefinedCMap("90ms-RKSJ-H", 0)
	if cm == nil {
		t.Fatal("no CMap")
	}
	// Shift-JIS: one byte for ASCII, two for the kana and kanji ranges.
	for _, tc := range []struct {
		in    string
		bytes int
	}{
		{"A", 1},
		{"\x82\xa0", 2},
		{"\x88\x9f", 2},
	} {
		_, n, cid := cm.Next([]byte(tc.in))
		if n != tc.bytes {
			t.Errorf("%q took %d bytes, want %d", tc.in, n, tc.bytes)
		}
		if cid == 0 {
			t.Errorf("%q mapped to CID 0", tc.in)
		}
	}
}

// renderContent draws a content stream onto the synthetic 200 by 100 page
// runContent uses, so that a test can assert pixels.
func renderContent(t *testing.T, content string, o *Options) *raster.Pixmap {
	t.Helper()
	d := open(t, "minimal.pdf")
	page := Dict{
		"Type":     Name("Page"),
		"MediaBox": Array{Integer(0), Integer(0), Integer(200), Integer(100)},
		"Resources": Dict{
			"Font": Dict{"F1": Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica")}},
			"ExtGState": Dict{
				"GS1": Dict{"ca": Real(0.5), "CA": Real(0.5)},
				"Mul": Dict{"BM": Name("Multiply")},
				"Scr": Dict{"BM": Name("Screen")},
			},
		},
	}
	p := &Page{doc: d, dict: page}

	px := raster.NewPixmap(o.colorSpace().Model(), 200, 100, o.alpha())
	if o.alpha() {
		px.Clear()
	} else {
		px.ClearWhite()
	}
	dev := NewDrawDevice(d, px)
	ip := p.newInterp(dev, p.Matrix(72))
	ip.run([]byte(content))
	ip.finish()
	return px
}

func pixel(px *raster.Pixmap, x, y int) []uint8 {
	n := px.Comps()
	return px.Samples[y*px.Stride+x*n : y*px.Stride+x*n+n]
}

func same(a []uint8, b ...uint8) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDrawFill(t *testing.T) {
	px := renderContent(t, "0 0 1 rg 10 20 30 40 re f", nil)
	if got := pixel(px, 20, 50); !same(got, 0, 0, 255) {
		t.Fatalf("inside the rectangle = %v, want blue", got)
	}
	if got := pixel(px, 20, 30); !same(got, 255, 255, 255) {
		t.Fatalf("above the rectangle = %v, want white", got)
	}
	if got := pixel(px, 5, 50); !same(got, 255, 255, 255) {
		t.Fatalf("left of the rectangle = %v, want white", got)
	}
	if got := pixel(px, 39, 41); !same(got, 0, 0, 255) {
		t.Fatalf("last pixel inside = %v, want blue", got)
	}
}

func TestDrawEvenOdd(t *testing.T) {
	px := renderContent(t, "0 g 0 0 100 100 re 20 20 60 60 re f*", nil)
	if got := pixel(px, 50, 50); !same(got, 255, 255, 255) {
		t.Fatalf("hole = %v, want white", got)
	}
	if got := pixel(px, 10, 50); !same(got, 0, 0, 0) {
		t.Fatalf("ring = %v, want black", got)
	}
}

func TestDrawStroke(t *testing.T) {
	px := renderContent(t, "4 w 0 g 10 50 m 190 50 l S", nil)
	for _, y := range []int{49, 50} {
		if got := pixel(px, 100, y); !same(got, 0, 0, 0) {
			t.Fatalf("on the line at y=%d = %v, want black", y, got)
		}
	}
	if got := pixel(px, 100, 45); !same(got, 255, 255, 255) {
		t.Fatalf("off the line = %v, want white", got)
	}
	if got := pixel(px, 5, 50); !same(got, 255, 255, 255) {
		t.Fatalf("past the end = %v, want white", got)
	}
}

func TestDrawClipRect(t *testing.T) {
	px := renderContent(t, "0 0 100 100 re W n 1 0 0 rg 0 0 200 100 re f", nil)
	if got := pixel(px, 50, 50); !same(got, 255, 0, 0) {
		t.Fatalf("inside the clip = %v, want red", got)
	}
	if got := pixel(px, 150, 50); !same(got, 255, 255, 255) {
		t.Fatalf("outside the clip = %v, want white", got)
	}
}

func TestDrawClipPath(t *testing.T) {
	px := renderContent(t, "0 0 m 100 0 l 0 100 l h W n 0 g 0 0 200 100 re f", nil)
	if got := pixel(px, 10, 90); !same(got, 0, 0, 0) {
		t.Fatalf("inside the triangle = %v, want black", got)
	}
	if got := pixel(px, 90, 10); !same(got, 255, 255, 255) {
		t.Fatalf("outside the triangle = %v, want white", got)
	}
	if got := pixel(px, 150, 50); !same(got, 255, 255, 255) {
		t.Fatalf("outside the clip box = %v, want white", got)
	}
}

func TestDrawNestedClip(t *testing.T) {
	px := renderContent(t, `q 0 0 100 100 re W n
		0 0 m 200 0 l 200 100 l h W n
		0 g 0 0 200 100 re f Q`, nil)
	if got := pixel(px, 80, 90); !same(got, 0, 0, 0) {
		t.Fatalf("inside both clips = %v, want black", got)
	}
	if got := pixel(px, 10, 10); !same(got, 255, 255, 255) {
		t.Fatalf("outside the triangle = %v, want white", got)
	}
	if got := pixel(px, 150, 90); !same(got, 255, 255, 255) {
		t.Fatalf("outside the rectangle = %v, want white", got)
	}
}

func TestDrawAlpha(t *testing.T) {
	px := renderContent(t, "/GS1 gs 0 g 0 0 200 100 re f", nil)
	if got := pixel(px, 100, 50); !same(got, 127, 127, 127) {
		t.Fatalf("half alpha black on white = %v, want 127", got)
	}
}

func TestDrawText(t *testing.T) {
	px := renderContent(t, "BT /F1 48 Tf 10 30 Td (Hi) Tj ET", nil)
	dark := 0
	for y := 0; y < px.H; y++ {
		for x := 0; x < px.W; x++ {
			if pixel(px, x, y)[0] < 128 {
				dark++
			}
		}
	}
	if dark < 100 {
		t.Fatalf("text drew %d dark pixels", dark)
	}
	blank := renderContent(t, "BT /F1 48 Tf 10 30 Td 3 Tr (Hi) Tj ET", nil)
	for i, v := range blank.Samples {
		if v != 255 {
			t.Fatalf("invisible text drew at sample %d: %d", i, v)
		}
	}
}

func TestDrawTextClip(t *testing.T) {
	px := renderContent(t, "BT /F1 48 Tf 10 30 Td 7 Tr (Hi) Tj ET 0 0 1 rg 0 0 200 100 re f", nil)
	blue := 0
	for y := 0; y < px.H; y++ {
		for x := 0; x < px.W; x++ {
			if same(pixel(px, x, y), 0, 0, 255) {
				blue++
			}
		}
	}
	if blue == 0 || blue > 4000 {
		t.Fatalf("text clip let %d pixels through", blue)
	}
}

func TestDrawColorSpaces(t *testing.T) {
	gray := renderContent(t, "0.25 g 0 0 200 100 re f", &Options{ColorSpace: DeviceGray})
	if got := pixel(gray, 100, 50); !same(got, 64) {
		t.Fatalf("gray destination = %v, want 64", got)
	}
	cmyk := renderContent(t, "1 0 0 rg 0 0 200 100 re f", &Options{ColorSpace: DeviceCMYK})
	if got := pixel(cmyk, 100, 50); !same(got, 0, 255, 255, 0) {
		t.Fatalf("cmyk destination = %v, want red", got)
	}
	alpha := renderContent(t, "0 0 1 rg 0 0 100 100 re f", &Options{Alpha: true})
	if got := pixel(alpha, 50, 50); !same(got, 0, 0, 255, 255) {
		t.Fatalf("painted pixel with alpha = %v", got)
	}
	if got := pixel(alpha, 150, 50); !same(got, 0, 0, 0, 0) {
		t.Fatalf("untouched pixel with alpha = %v, want transparent", got)
	}
}

func TestRenderPage(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := p.ImageDPI(72)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.Bounds().Max, (image.Point{X: 200, Y: 100}); got != want {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	if r, g, b, a := img.At(40, 80).RGBA(); r != 0 || g != 0 || b != 0xffff || a != 0xffff {
		t.Fatalf("the blue rectangle is %v %v %v %v", r, g, b, a)
	}
	if r, _, _, _ := img.At(180, 20).RGBA(); r != 0xffff {
		t.Fatalf("the background is not white")
	}
}

func TestRenderLimits(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(p.Matrix(72), &Options{PixelLimit: 100}); err == nil {
		t.Fatal("the pixel limit did not stop a 200x100 page")
	}
	if _, err := p.Render(p.Matrix(72), &Options{ColorSpace: &ColorSpace{Name: "Two", N: 2}}); err == nil {
		t.Fatal("a two component destination was accepted")
	}
	px, err := p.Render(p.Matrix(0.5), nil)
	if err != nil {
		t.Fatalf("a page smaller than a pixel: %v", err)
	}
	if px.W < 1 || px.H < 1 {
		t.Fatalf("a page smaller than a pixel rendered %dx%d", px.W, px.H)
	}
}

func BenchmarkRenderText(b *testing.B) {
	d, err := Open(filepath.Join("..", "testdata", "minimal.pdf"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	page := Dict{
		"Type":     Name("Page"),
		"MediaBox": Array{Integer(0), Integer(0), Integer(612), Integer(792)},
		"Resources": Dict{
			"Font": Dict{"F1": Dict{"Type": Name("Font"), "Subtype": Name("Type1"), "BaseFont": Name("Helvetica")}},
		},
	}
	p := &Page{doc: d, dict: page}
	var content strings.Builder
	content.WriteString("BT /F1 11 Tf 14 TL 36 750 Td\n")
	for i := 0; i < 50; i++ {
		content.WriteString("(The quick brown fox jumps over the lazy dog, 0123456789.) Tj T*\n")
	}
	content.WriteString("ET\n")
	data := []byte(content.String())

	ctm := p.Matrix(150)
	px := raster.NewPixmap(DeviceRGB.Model(), 1275, 1650, false)
	b.ResetTimer()
	for b.Loop() {
		px.ClearWhite()
		dev := NewDrawDevice(d, px)
		ip := p.newInterp(dev, ctm)
		ip.run(data)
		ip.finish()
	}
}

// benchPages builds a document of n identical pages of text and rules, which
// is what a book looks like to the renderer.
func benchPages(b *testing.B, n int) *Document {
	b.Helper()
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", ""}
	var kids strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", 3+i)
	}
	var content strings.Builder
	content.WriteString("BT /F1 11 Tf 14 TL 36 750 Td\n")
	for i := 0; i < 45; i++ {
		content.WriteString("(The quick brown fox jumps over the lazy dog, 0123456789.) Tj T*\n")
	}
	content.WriteString("ET\n0 0 1 RG 2 w\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&content, "36 %d m 576 %d l S\n", 40+i*3, 60+i*3)
	}
	for i := 0; i < n; i++ {
		objs = append(objs, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
			" /Contents %d 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1"+
			" /BaseFont /Helvetica >> >> >> >>", 3+n))
	}
	objs = append(objs, streamObj("", content.String()))
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), n)
	return buildPDFB(b, objs)
}

// BenchmarkRenderPagesSerial and BenchmarkRenderPagesParallel are the same
// work, one page at a time and one page a goroutine, against one Document.
func BenchmarkRenderPagesSerial(b *testing.B) {
	const n = 16
	d := benchPages(b, n)
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < n; i++ {
			p, err := d.Page(i)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := p.Render(p.Matrix(150), nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRenderPagesParallel(b *testing.B) {
	const n = 16
	d := benchPages(b, n)
	b.ResetTimer()
	for b.Loop() {
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				p, err := d.Page(i)
				if err != nil {
					return
				}
				p.Render(p.Matrix(150), nil)
			}(i)
		}
		wg.Wait()
	}
}

// BenchmarkRenderBands is one page drawn whole and then in bands, which is what
// Options.Threads buys on a page too big to draw in one goroutine. Text is the
// case it helps least, because reading the page is most of the work; a shading
// is the case it helps most, because every pixel is computed.
func BenchmarkRenderBands(b *testing.B) {
	shading := "<< /Sh << /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 612 792]" +
		" /Function << /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >> >> >>"
	for _, page := range []struct{ name, content, res string }{
		{"text", "", ""},
		{"shade", "q 0 0 612 792 re W n /Sh sh Q", " /Shading " + shading},
	} {
		for _, n := range []int{1, 2, 4, 8} {
			b.Run(page.name+"/"+fmt.Sprint(n), func(b *testing.B) {
				var d *Document
				if page.content == "" {
					d = benchPages(b, 1)
				} else {
					d = buildPDFB(b, []string{
						"<< /Type /Catalog /Pages 2 0 R >>",
						"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
						"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]" +
							" /Contents 4 0 R /Resources <<" + page.res + " >> >>",
						streamObj("", page.content),
					})
				}
				p, err := d.Page(0)
				if err != nil {
					b.Fatal(err)
				}
				o := &Options{Threads: n}
				ctm := p.Matrix(300)
				b.ResetTimer()
				for b.Loop() {
					if _, err := p.Render(ctm, o); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkRenderPaths(b *testing.B) {
	d, err := Open(filepath.Join("..", "testdata", "minimal.pdf"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	page := Dict{
		"Type":     Name("Page"),
		"MediaBox": Array{Integer(0), Integer(0), Integer(612), Integer(792)},
	}
	p := &Page{doc: d, dict: page}
	var content strings.Builder
	seed := uint32(1)
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for i := 0; i < 400; i++ {
		x, y := next(520), next(700)
		content.WriteString(fmt.Sprintf("0.%d 0.%d 0.%d rg %d %d m %d %d l %d %d %d %d %d %d c %d %d l f\n",
			next(9), next(9), next(9),
			x, y, x+next(80), y+next(80),
			x+next(80), y+next(80), x+next(80), y+next(80), x+next(80), y+next(80),
			x+next(80), y+next(80)))
	}
	data := []byte(content.String())

	ctm := p.Matrix(150)
	px := raster.NewPixmap(DeviceRGB.Model(), 1275, 1650, false)
	b.ResetTimer()
	for b.Loop() {
		px.ClearWhite()
		dev := NewDrawDevice(d, px)
		ip := p.newInterp(dev, ctm)
		ip.run(data)
		ip.finish()
	}
}

func TestImageOptions(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		o    *Options
		want string
	}{
		{nil, "*image.RGBA"},
		{&Options{ColorSpace: DeviceGray}, "*image.Gray"},
		{&Options{ColorSpace: DeviceCMYK}, "*image.CMYK"},
		{&Options{Alpha: true}, "*image.RGBA"},
		{&Options{ColorSpace: DeviceCMYK, Alpha: true}, "*image.RGBA"},
	} {
		img, err := p.ImageOptions(72, tc.o)
		if err != nil {
			t.Fatalf("%v: %v", tc.o, err)
		}
		if got := fmt.Sprintf("%T", img); got != tc.want {
			t.Fatalf("%v: got %s, want %s", tc.o, got, tc.want)
		}
		if got := img.Bounds().Max; got != (image.Point{X: 200, Y: 100}) {
			t.Fatalf("%v: bounds %v", tc.o, got)
		}
		r, g, b, _ := img.At(40, 80).RGBA()
		switch {
		case tc.o != nil && tc.o.ColorSpace == DeviceGray:
			if r != g || g != b || r > 0x4000 {
				t.Fatalf("gray: the blue rectangle came out %v %v %v", r, g, b)
			}
		case tc.o != nil && tc.o.ColorSpace == DeviceCMYK && tc.o.Alpha:
			// Composited in CMYK and converted back through the document's
			// own lattice, which is what blue looks like on paper.
			if b < g+0x4000 || b < r+0x4000 {
				t.Fatalf("cmyk: the blue rectangle came out %v %v %v", r, g, b)
			}
		default:
			if r > 0x2000 || g > 0x2000 || b < 0xd000 {
				t.Fatalf("%v: the blue rectangle came out %v %v %v", tc.o, r, g, b)
			}
		}
	}
}

func TestRenderTo(t *testing.T) {
	d := open(t, "minimal.pdf")
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewRGBA(image.Rect(0, 0, 300, 200))
	if err := p.RenderTo(dst, p.Matrix(72), nil); err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := dst.At(250, 150).RGBA(); r != 0 {
		t.Fatal("RenderTo painted outside the page")
	}
	if _, _, b, _ := dst.At(40, 80).RGBA(); b != 0xffff {
		t.Fatal("RenderTo did not paint the page")
	}
}

// buildPDF assembles a document from object bodies numbered from one, so that
// a test can exercise the things that need a real stream: forms, patterns and
// Type3 glyph procedures.
func buildPDF(t *testing.T, objs []string) *Document {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs)+1)
	for i, o := range objs {
		offs[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)

	d, err := Load(buf.Bytes(), "")
	if err != nil {
		t.Fatalf("building a document: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func streamObj(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

func renderDoc(t *testing.T, d *Document, o *Options) *raster.Pixmap {
	t.Helper()
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	px, err := p.Render(p.Matrix(72), o)
	if err != nil {
		t.Fatal(err)
	}
	return px
}

// TestType3Cycle checks that a Type3 glyph which shows itself stops. A font
// with no program of its own draws a glyph by running content through the
// device, which starts an interpreter of its own, so the depth counter of the
// one that asked does not reach it and the work a cycle does doubles at every
// turn.
func TestType3Cycle(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Font << /T3 5 0 R >> >> >>",
		streamObj("", "BT /T3 10 Tf 10 50 Td (a) Tj ET"),
		"<< /Type /Font /Subtype /Type3 /FontBBox [0 0 10 10]" +
			" /FontMatrix [0.1 0 0 0.1 0 0] /FirstChar 97 /LastChar 97 /Widths [10]" +
			" /Encoding << /Differences [97 /a] >> /CharProcs << /a 6 0 R >>" +
			" /Resources << /Font << /T3 5 0 R >> >> >>",
		streamObj("", "10 0 0 0 10 10 d1 0 0 4 4 re f BT /T3 10 Tf 2 2 Td (a) Tj ET"),
	})
	done := make(chan *raster.Pixmap, 1)
	go func() { done <- renderDoc(t, d, nil) }()
	select {
	case px := <-done:
		if got := pixel(px, 11, 48); same(got, 255, 255, 255) {
			t.Errorf("the glyph drew nothing at all, %v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a glyph that shows itself did not stop")
	}
}

// TestPageHTML covers a page as HTML: the text where it was drawn, in a
// division of its own, escaped.
func TestPageHTML(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R" +
			" /Resources << /Font << /F 5 0 R >> >> >>",
		streamObj("", "BT /F 12 Tf 20 50 Td (a < b & c) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", `<div id="page0"`,
		"width:200pt", "height:100pt", "position:absolute", "a &lt; b &amp; c",
		"font-family:Helvetica"} {
		if !strings.Contains(out, want) {
			t.Errorf("the HTML has no %q: %s", want, out)
		}
	}
}

// TestNestedColorIsolation checks that a form, a pattern or a glyph procedure
// setting a color does not reach back into the state that invoked it. The
// color operators write their operands into the slice the graphics state
// already holds, so a nested context has to be given its own.
func TestNestedColorIsolation(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Fm 5 0 R >> >> >>",
		streamObj("", "/DeviceRGB cs 0 0 1 sc 1 0 0 1 10 10 cm /Fm Do 40 40 50 50 re f"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 20 20]",
			"1 0 0 sc 0 0 20 20 re f"),
	})
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 80); !same(got, 255, 0, 0) {
		t.Fatalf("the form drew %v, want red", got)
	}
	if got := pixel(px, 70, 20); !same(got, 0, 0, 255) {
		t.Fatalf("after the form the fill is %v, want the blue it selected", got)
	}
}

// type1Charstring encodes a Type1 charstring from a tiny operator list: a
// number is pushed and a name is the operator that consumes what is on the
// stack.
func type1Charstring(ops ...any) []byte {
	var out []byte
	for _, o := range ops {
		switch v := o.(type) {
		case int:
			switch {
			case v >= -107 && v <= 107:
				out = append(out, byte(v+139))
			case v >= 108 && v <= 1131:
				v -= 108
				out = append(out, byte(247+v>>8), byte(v))
			case v >= -1131 && v <= -108:
				v = -v - 108
				out = append(out, byte(251+v>>8), byte(v))
			default:
				out = append(out, 255, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			}
		case string:
			out = append(out, map[string]byte{
				"hsbw": 13, "rmoveto": 21, "rlineto": 5,
				"closepath": 9, "endchar": 14,
			}[v])
		}
	}
	return out
}

// type1Crypt is the Type1 stream cipher, used with 55665 for the eexec section
// and 4330 for a charstring.
func type1Crypt(plain []byte, key uint16, pad int) []byte {
	r := key
	out := make([]byte, 0, len(plain)+pad)
	for i := 0; i < pad+len(plain); i++ {
		p := byte(0x55)
		if i >= pad {
			p = plain[i-pad]
		}
		c := p ^ byte(r>>8)
		r = (uint16(c)+r)*52845 + 22719
		out = append(out, c)
	}
	return out
}

// buildType1 assembles a Type1 font program whose CharStrings dictionary lists
// names in the given order, so that the first one is glyph zero. A Type1
// program has no glyph order of its own and nothing says .notdef comes first.
func buildType1(names []string, shapes map[string][]byte) []byte {
	var priv bytes.Buffer
	priv.WriteString("dup /Private 8 dict dup begin\n/lenIV 4 def\n")
	fmt.Fprintf(&priv, "2 index /CharStrings %d dict dup begin\n", len(names))
	for _, n := range names {
		cs := type1Crypt(shapes[n], 4330, 4)
		fmt.Fprintf(&priv, "/%s %d RD ", n, len(cs))
		priv.Write(cs)
		priv.WriteString(" ND\n")
	}
	priv.WriteString("end\nend\nmark currentfile closefile\n")

	head := "%!PS-AdobeFont-1.0: Probe 1.0\n" +
		"/FontMatrix [0.001 0 0 0.001 0 0] readonly def\n" +
		"/Encoding StandardEncoding def\n" +
		"currentfile eexec\n"
	return append([]byte(head), type1Crypt(priv.Bytes(), 55665, 4)...)
}

// TestType1GlyphZero checks that a name resolving to glyph zero is honored. A
// Type1 CharStrings dictionary may put any glyph first, and treating zero as
// "no glyph" sent every such name down the fallback path, which lands on the
// glyph whose index happens to equal the character code.
func TestType1GlyphZero(t *testing.T) {
	big := type1Charstring(0, 500, "hsbw", 100, 100, "rmoveto",
		300, 0, "rlineto", 0, 300, "rlineto", -300, 0, "rlineto", "closepath", "endchar")
	small := type1Charstring(0, 500, "hsbw", 600, 600, "rmoveto",
		200, 0, "rlineto", 0, 200, "rlineto", -200, 0, "rlineto", "closepath", "endchar")
	blank := type1Charstring(0, 500, "hsbw", "endchar")

	names := []string{"A"}
	shapes := map[string][]byte{"A": big, "dollar": small}
	for i := 1; i < 65; i++ {
		n := fmt.Sprintf("uni%04X", 0xE000+i)
		names = append(names, n)
		shapes[n] = blank
	}
	names = append(names, "dollar")

	prog := buildType1(names, shapes)
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Font << /F1 5 0 R >> >> >>",
		streamObj("", "BT /F1 100 Tf 0 0 Td (A) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Probe /FirstChar 65 /LastChar 65" +
			" /Widths [500] /FontDescriptor 6 0 R" +
			" /Encoding << /Type /Encoding /Differences [65 /A] >> >>",
		"<< /Type /FontDescriptor /FontName /Probe /Flags 32 /ItalicAngle 0" +
			" /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80" +
			" /FontBBox [0 0 1000 1000] /FontFile 7 0 R >>",
		streamObj(fmt.Sprintf("/Length1 %d /Length2 0 /Length3 0", len(prog)), string(prog)),
	})
	px := renderDoc(t, d, nil)
	// Glyph "A" is the square from 10,10 to 40,40 at this size; "dollar", the
	// glyph whose index equals the code, would paint from 60,60 to 80,80.
	if got := pixel(px, 25, 75); !same(got, 0, 0, 0) {
		t.Errorf("glyph A drew %v at 25,75, want black", got)
	}
	if got := pixel(px, 70, 30); !same(got, 255, 255, 255) {
		t.Errorf("the glyph at the character code drew %v at 70,30, want white", got)
	}
}

// TestType1MissingName checks that a name the font does not carry resolves to
// nothing rather than to whatever glyph sits at the character code. A subset
// font keeps a few dozen glyphs in its own order, so reading the code as an
// index lands on an unrelated letter.
func TestType1MissingName(t *testing.T) {
	big := type1Charstring(0, 500, "hsbw", 100, 100, "rmoveto",
		300, 0, "rlineto", 0, 300, "rlineto", -300, 0, "rlineto", "closepath", "endchar")
	blank := type1Charstring(0, 500, "hsbw", "endchar")

	// No standard name is in this font, so every lookup fails and only the
	// guess by index is left. "dollar" sits at 65, the code the page draws.
	names := []string{".notdef"}
	shapes := map[string][]byte{".notdef": blank, "dollar": big}
	for i := 1; i < 65; i++ {
		n := fmt.Sprintf("uni%04X", 0xE000+i)
		names = append(names, n)
		shapes[n] = blank
	}
	names = append(names, "dollar")

	prog := buildType1(names, shapes)
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Font << /F1 5 0 R >> >> >>",
		streamObj("", "BT /F1 100 Tf 0 0 Td (A) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Probe /FirstChar 65 /LastChar 65" +
			" /Widths [500] /FontDescriptor 6 0 R" +
			" /Encoding << /Type /Encoding /Differences [65 /nonesuch] >> >>",
		"<< /Type /FontDescriptor /FontName /Probe /Flags 32 /ItalicAngle 0" +
			" /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80" +
			" /FontBBox [0 0 1000 1000] /FontFile 7 0 R >>",
		streamObj(fmt.Sprintf("/Length1 %d /Length2 0 /Length3 0", len(prog)), string(prog)),
	})
	px := renderDoc(t, d, nil)
	// Glyph 65 is "dollar", which paints 10,10 to 40,40 and must not be drawn.
	if got := pixel(px, 25, 75); !same(got, 255, 255, 255) {
		t.Errorf("the glyph at the character code drew %v at 25,75, want white", got)
	}
}

// TestFormCycle checks that two form XObjects that reach each other draw one
// pass and stop at the cycle, rather than running to the nesting limit. The
// form halves the scale each time, so a depth counter alone would paint the
// square sixteen times over.
func TestFormCycle(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /A 5 0 R >> >> >>",
		streamObj("", "0 0 1 rg /A Do"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
			" /Resources << /XObject << /B 6 0 R >> >>",
			"0 0 50 50 re f 0.5 0 0 0.5 50 50 cm /B Do"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
			" /Resources << /XObject << /A 5 0 R >> >>",
			"0 0 50 50 re f 0.5 0 0 0.5 50 50 cm /A Do"),
	})
	px := renderDoc(t, d, nil)
	// A paints the lower left quarter, B the quarter above it at half scale.
	if got := pixel(px, 20, 80); !same(got, 0, 0, 255) {
		t.Errorf("form A drew %v, want blue", got)
	}
	if got := pixel(px, 60, 40); !same(got, 0, 0, 255) {
		t.Errorf("form B drew %v, want blue", got)
	}
	// A reached from B is the cycle and must not paint.
	if got := pixel(px, 80, 20); !same(got, 255, 255, 255) {
		t.Errorf("the cycle drew %v at the third step, want white", got)
	}
	if len(d.Err()) == 0 {
		t.Error("the cycle was not recorded as an error")
	}
}

// TestFormTwiceIsNotACycle checks that the guard unwinds: the same form drawn
// twice side by side is not a cycle and draws both times.
func TestFormTwiceIsNotACycle(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Fm 5 0 R >> >> >>",
		streamObj("", "0 0 1 rg /Fm Do 1 0 0 1 50 50 cm /Fm Do"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100]",
			"0 0 40 40 re f"),
	})
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 80); !same(got, 0, 0, 255) {
		t.Errorf("the first copy drew %v, want blue", got)
	}
	if got := pixel(px, 70, 30); !same(got, 0, 0, 255) {
		t.Errorf("the second copy drew %v, want blue", got)
	}
}

// TestNestedDashIsolation is the same hazard on the dash array, which the d
// operator also rewrites in place.
func TestNestedDashIsolation(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Fm 5 0 R >> >> >>",
		streamObj("", "0 g 4 w [100 100] 0 d /Fm Do 0 50 m 100 50 l S"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 1 1]", "[1 1] 0 d"),
	})
	px := renderDoc(t, d, nil)
	for x := 2; x < 20; x++ {
		if got := pixel(px, x, 50); !same(got, 0, 0, 0) {
			t.Fatalf("the line is dashed at x=%d: %v", x, got)
		}
	}
}

// imagePDF builds a one page document whose content stream draws a single
// image, so that the decoders can be tested a sample at a time.
func imagePDF(t *testing.T, dict, data, content string) *Document {
	t.Helper()
	if content == "" {
		content = "q 100 0 0 100 0 0 cm /Im Do Q"
	}
	return buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Im 5 0 R >> >> >>",
		streamObj("", content),
		streamObj("/Type /XObject /Subtype /Image "+dict, data),
	})
}

// TestImageShrinkCached checks that an image drawn far smaller than it is
// decodes to the size it will be drawn at and that the cache keeps that, so a
// page clipping to the same scan many times decodes it once. The whole size
// pixmap is what the cache used to refuse.
func TestImageShrinkCached(t *testing.T) {
	const w, h = 256, 256
	d := imagePDF(t, fmt.Sprintf("/Width %d /Height %d /ColorSpace /DeviceGray"+
		" /BitsPerComponent 8", w, h), strings.Repeat("\x80", w*h),
		"q 8 0 0 8 0 0 cm /Im Do Q q 8 0 0 8 20 20 cm /Im Do Q")
	renderDoc(t, d, nil)

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.images) != 1 {
		t.Fatalf("the cache holds %d images, want 1", len(d.images))
	}
	for key, e := range d.images {
		if key.shrink == 0 {
			t.Errorf("the image was not shrunk")
		}
		if e.px.W > 16 || e.px.H > 16 {
			t.Errorf("cached %dx%d, want no more than 16x16 for an 8 point box", e.px.W, e.px.H)
		}
	}
}

func TestImageBitDepths(t *testing.T) {
	// Four gray pixels, black to white, at each depth the format allows.
	for _, tc := range []struct {
		bpc  int
		data string
		want [4]uint8
	}{
		{1, "\x10", [4]uint8{0, 0, 0, 255}},
		{2, "\x1b", [4]uint8{0, 85, 170, 255}},
		{4, "\x05\xaf", [4]uint8{0, 85, 170, 255}},
		{8, "\x00\x55\xaa\xff", [4]uint8{0, 85, 170, 255}},
		{16, "\x00\x00\x55\x55\xaa\xaa\xff\xff", [4]uint8{0, 85, 170, 255}},
	} {
		d := imagePDF(t, fmt.Sprintf("/Width 4 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent %d", tc.bpc), tc.data, "")
		px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
		for x, want := range tc.want {
			got := pixel(px, x*25+12, 50)[0]
			if int(got)-int(want) > 1 || int(want)-int(got) > 1 {
				t.Errorf("%d bits: pixel %d = %d, want %d", tc.bpc, x, got, want)
			}
		}
	}
}

func TestImageDecodeArray(t *testing.T) {
	d := imagePDF(t, "/Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Decode [1 0]",
		"\x00\xff", "")
	px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
	if got := pixel(px, 25, 50)[0]; got != 255 {
		t.Fatalf("inverted black = %d, want 255", got)
	}
	if got := pixel(px, 75, 50)[0]; got != 0 {
		t.Fatalf("inverted white = %d, want 0", got)
	}
}

func TestImageIndexed(t *testing.T) {
	d := imagePDF(t, "/Width 2 /Height 1 /BitsPerComponent 8"+
		" /ColorSpace [/Indexed /DeviceRGB 1 <FF000000FF00>]", "\x00\x01", "")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 25, 50); !same(got, 255, 0, 0) {
		t.Fatalf("palette entry 0 = %v, want red", got)
	}
	if got := pixel(px, 75, 50); !same(got, 0, 255, 0) {
		t.Fatalf("palette entry 1 = %v, want green", got)
	}
}

func TestImageMaskPaintsFillColor(t *testing.T) {
	d := imagePDF(t, "/Width 2 /Height 1 /ImageMask true", "\x40",
		"q 0 0 1 rg 100 0 0 100 0 0 cm /Im Do Q")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 25, 50); !same(got, 0, 0, 255) {
		t.Fatalf("a zero bit = %v, want the fill color", got)
	}
	if got := pixel(px, 75, 50); !same(got, 255, 255, 255) {
		t.Fatalf("a one bit = %v, want the page", got)
	}
}

func TestImageSMask(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Im 5 0 R >> >> >>",
		streamObj("", "q 100 0 0 100 0 0 cm /Im Do Q"),
		streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 1"+
			" /ColorSpace /DeviceRGB /BitsPerComponent 8 /SMask 6 0 R",
			"\x00\x00\x00\x00\x00\x00"),
		streamObj("/Type /XObject /Subtype /Image /Width 2 /Height 1"+
			" /ColorSpace /DeviceGray /BitsPerComponent 8", "\xff\x00"),
	})
	px := renderDoc(t, d, nil)
	if got := pixel(px, 25, 50); !same(got, 0, 0, 0) {
		t.Fatalf("opaque half = %v, want black", got)
	}
	if got := pixel(px, 75, 50); !same(got, 255, 255, 255) {
		t.Fatalf("transparent half = %v, want the page", got)
	}
}

func TestImageColorKey(t *testing.T) {
	d := imagePDF(t, "/Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Mask [0 0]",
		"\x00\x00", "")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 25, 50); !same(got, 255, 255, 255) {
		t.Fatalf("a masked sample painted %v", got)
	}
}

func TestImageInline(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
		streamObj("", "q 100 0 0 100 0 0 cm BI /W 2 /H 1 /CS /G /BPC 8 /F /AHx ID 00ff> EI Q"),
	})
	px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
	if got := pixel(px, 25, 50)[0]; got != 0 {
		t.Fatalf("inline black = %d", got)
	}
	if got := pixel(px, 75, 50)[0]; got != 255 {
		t.Fatalf("inline white = %d", got)
	}
}

func BenchmarkRenderImage(b *testing.B) {
	// A 512 by 512 photograph shaped image drawn over a letter page, which is
	// the downscale the sampler is written for.
	pix := make([]byte, 512*512*3)
	seed := uint32(1)
	for i := range pix {
		seed = seed*1664525 + 1013904223
		pix[i] = uint8(seed >> 24)
	}
	var data strings.Builder
	for _, v := range pix {
		fmt.Fprintf(&data, "%02x", v)
	}

	d := buildPDFB(b, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R" +
			" /Resources << /XObject << /Im 5 0 R >> >> >>",
		streamObj("", "q 612 0 0 792 0 0 cm /Im Do Q"),
		streamObj("/Type /XObject /Subtype /Image /Width 512 /Height 512"+
			" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /ASCIIHexDecode",
			data.String()),
	})
	p, err := d.Page(0)
	if err != nil {
		b.Fatal(err)
	}
	ctm := p.Matrix(150)
	px := raster.NewPixmap(DeviceRGB.Model(), 1275, 1650, false)
	b.ResetTimer()
	for b.Loop() {
		px.ClearWhite()
		dev := NewDrawDevice(d, px)
		ip := p.newInterp(dev, ctm)
		ip.run(p.Contents())
		ip.finish()
	}
}

func BenchmarkRenderShading(b *testing.B) {
	// A radial gradient over a letter page, which is the quadratic per pixel
	// and the table lookup and nothing else.
	d := buildPDFB(b, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R" +
			" /Resources << /Shading << /Sh 5 0 R >> >> >>",
		streamObj("", "/Sh sh"),
		"<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [306 396 0 306 396 400]" +
			" /Function 6 0 R /Extend [true true] >>",
		"<< /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >>",
	})
	p, err := d.Page(0)
	if err != nil {
		b.Fatal(err)
	}
	ctm := p.Matrix(150)
	px := raster.NewPixmap(DeviceRGB.Model(), 1275, 1650, false)
	b.ResetTimer()
	for b.Loop() {
		px.ClearWhite()
		dev := NewDrawDevice(d, px)
		ip := p.newInterp(dev, ctm)
		ip.run(p.Contents())
		ip.finish()
	}
}

func BenchmarkRenderTransparency(b *testing.B) {
	// The same curves as RenderPaths, but under a blend mode, so that every
	// one of them opens a group of its own and composites it back.
	var content strings.Builder
	content.WriteString("/GS gs 0.2 0.4 0.8 rg\n")
	seed := uint32(1)
	next := func(m int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>16) % m
	}
	for i := 0; i < 400; i++ {
		x, y := next(500), next(700)
		fmt.Fprintf(&content, "%d %d m %d %d %d %d %d %d c f\n",
			x, y, x+next(80), y+next(80), x+next(80), y+next(80), x+next(80), y+next(80))
	}
	d := buildPDFB(b, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R" +
			" /Resources << /ExtGState << /GS 5 0 R >> >> >>",
		streamObj("", content.String()),
		"<< /Type /ExtGState /BM /Multiply /ca 0.5 >>",
	})
	p, err := d.Page(0)
	if err != nil {
		b.Fatal(err)
	}
	ctm := p.Matrix(150)
	px := raster.NewPixmap(DeviceRGB.Model(), 1275, 1650, false)
	b.ResetTimer()
	for b.Loop() {
		px.ClearWhite()
		dev := NewDrawDevice(d, px)
		ip := p.newInterp(dev, ctm)
		ip.run(p.Contents())
		ip.finish()
	}
}

// buildPDFB is buildPDF for a benchmark.
func buildPDFB(b *testing.B, objs []string) *Document {
	b.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs)+1)
	for i, o := range objs {
		offs[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)

	d, err := Load(buf.Bytes(), "")
	if err != nil {
		b.Fatalf("building a document: %v", err)
	}
	b.Cleanup(func() { d.Close() })
	return d
}

func TestRegisterImageDecoder(t *testing.T) {
	const filter = "TestDecode"
	RegisterImageDecoder(filter, func(data []byte, parms Dict) ([]byte, int, int, int, error) {
		if string(data) != "hello" {
			return nil, 0, 0, 0, fmt.Errorf("got %q", data)
		}
		return []byte{0, 255}, 2, 1, 1, nil
	})
	defer RegisterImageDecoder(filter, nil)

	d := imagePDF(t, "/Width 2 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8"+
		" /Filter /"+filter, "hello", "")
	px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
	if got := pixel(px, 25, 50)[0]; got != 0 {
		t.Fatalf("registered decoder: first sample = %d, want 0", got)
	}
	if got := pixel(px, 75, 50)[0]; got != 255 {
		t.Fatalf("registered decoder: second sample = %d, want 255", got)
	}
}

// fixture reads one of the encoded streams in testdata.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestImageJBIG2 draws testdata/generic.jb2, an MMR coded generic region that
// jbig2dec decodes as a black frame around a white square.
func TestImageJBIG2(t *testing.T) {
	d := imagePDF(t, "/Width 16 /Height 16 /ColorSpace /DeviceGray"+
		" /BitsPerComponent 1 /Filter /JBIG2Decode", fixture(t, "generic.jb2"), "")
	px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
	for _, tc := range []struct {
		x, y int
		want uint8
	}{{6, 6, 0}, {93, 6, 0}, {6, 93, 0}, {93, 93, 0}, {50, 50, 255}, {35, 35, 255}} {
		if got := pixel(px, tc.x, tc.y)[0]; got != tc.want {
			t.Errorf("JBIG2 pixel at %d,%d = %d, want %d", tc.x, tc.y, got, tc.want)
		}
	}
}

// TestImageJBIG2Globals splits the same image so that the page information
// lives in the globals stream, which is the shape a real scanner produces and
// which nothing in any corpus has.
func TestImageJBIG2Globals(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Im 5 0 R >> >> >>",
		streamObj("", "q 100 0 0 100 0 0 cm /Im Do Q"),
		streamObj("/Type /XObject /Subtype /Image /Width 16 /Height 16"+
			" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode"+
			" /DecodeParms << /JBIG2Globals 6 0 R >>", fixture(t, "region.jb2")),
		streamObj("", fixture(t, "globals.jb2")),
	})
	px := renderDoc(t, d, &Options{ColorSpace: DeviceGray})
	if got := pixel(px, 6, 6)[0]; got != 0 {
		t.Errorf("with globals the frame is %d, want 0", got)
	}
	if got := pixel(px, 50, 50)[0]; got != 255 {
		t.Errorf("with globals the middle is %d, want 255", got)
	}
}

// TestImageJPX decodes the three codestreams in testdata, which OpenJPEG made
// from the ramps below without loss, and checks every sample.
func TestImageJPX(t *testing.T) {
	gray := make([]byte, 32*32)
	color := make([]byte, 32*32*3)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			gray[y*32+x] = byte(x*8 + y*4)
			color[(y*32+x)*3] = byte(x * 8)
			color[(y*32+x)*3+1] = byte(y * 8)
			color[(y*32+x)*3+2] = byte((x ^ y) * 8)
		}
	}
	for _, tc := range []struct {
		name  string
		comps int
		want  []byte
	}{
		{"gray.j2k", 1, gray},
		{"rgb.j2k", 3, color},
		{"lazy.j2k", 3, color},
	} {
		img, err := jpxDecode([]byte(fixture(t, tc.name)), false)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if img.width != 32 || img.height != 32 || img.comps != tc.comps {
			t.Fatalf("%s: %dx%d with %d components", tc.name, img.width, img.height, img.comps)
		}
		for i, want := range tc.want {
			if img.pix[i] != want {
				t.Fatalf("%s: sample %d = %d, want %d", tc.name, i, img.pix[i], want)
			}
		}
	}
}

// TestConcurrentRender renders every page of a document from its own goroutine
// against one Document, which is what a caller converting a book wants and
// what the object store and the caches are locked for.
func TestConcurrentRender(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R] /Count 4 >>",
		pageObj(7),
		pageObj(7),
		pageObj(7),
		pageObj(7),
		streamObj("", "BT /F 24 Tf 10 50 Td (concurrent) Tj ET 0 0 1 rg 10 10 40 20 re f"),
	})
	if n := d.NumPages(); n != 4 {
		t.Fatalf("%d pages, want 4", n)
	}

	want := make([][]uint8, 4)
	for i := range want {
		p, err := d.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		px, err := p.Render(p.Matrix(72), nil)
		if err != nil {
			t.Fatal(err)
		}
		want[i] = append([]uint8(nil), px.Samples...)
	}

	var wg sync.WaitGroup
	got := make([][]uint8, 4)
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := d.Page(i)
			if err != nil {
				errs[i] = err
				return
			}
			px, err := p.Render(p.Matrix(72), nil)
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = px.Samples
		}(i)
	}
	wg.Wait()

	for i := range want {
		if errs[i] != nil {
			t.Fatalf("page %d: %v", i, errs[i])
		}
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("page %d rendered differently in a goroutine", i)
		}
	}
}

// TestRenderBands draws a page in horizontal bands from several goroutines and
// requires the result to be the same pixels as drawing it in one.
func TestRenderBands(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 400] /Contents 4 0 R" +
			" /Resources << /Font << /F << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >>" +
			" /Shading << /Sh << /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 300 400]" +
			" /Function << /FunctionType 2 /Domain [0 1] /C0 [1 0 0] /C1 [0 0 1] /N 1 >> >> >> >> >>",
		streamObj("", "q 0 0 300 400 re W n /Sh sh Q\n"+
			"BT /F 18 Tf 20 380 Td 20 TL (bands and bands and bands) Tj T* (and more of them) Tj ET\n"+
			"0.5 0 0.5 rg 3 w 20 20 m 280 380 l S 20 380 m 280 20 l S\n"+
			"q 0.5 g 40 40 220 320 re f Q"),
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := p.Render(p.Matrix(150), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{2, 3, 7} {
		got, err := p.Render(p.Matrix(150), &Options{Threads: n})
		if err != nil {
			t.Fatalf("%d bands: %v", n, err)
		}
		if got.W != want.W || got.H != want.H {
			t.Fatalf("%d bands: %dx%d, want %dx%d", n, got.W, got.H, want.W, want.H)
		}
		// An edge that crosses a band boundary is clipped there, and the
		// crossing point rounds to the rasterizer's 1/256 of a pixel, so
		// a band may differ from the whole page by one coverage unit.
		for i := range got.Samples {
			if d := int(got.Samples[i]) - int(want.Samples[i]); d > 1 || d < -1 {
				t.Fatalf("%d bands: sample %d is %d, want %d",
					n, i, got.Samples[i], want.Samples[i])
			}
		}
	}
}

// pageObj is a page whose contents are the given object and whose resources
// name one standard font, so that every page of the test document shares the
// font cache, the glyph cache and the content stream.
func pageObj(contents int) string {
	return fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents %d 0 R"+
		" /Resources << /Font << /F << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> >>",
		contents)
}

func FuzzJPX(fu *testing.F) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.j2k"))
	for _, name := range names {
		if b, err := os.ReadFile(name); err == nil {
			fu.Add(b)
		}
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		jpxDecode(b, false)
	})
}

// shadingPDF builds a one page document whose content stream paints one
// shading, given as the last object.
func shadingPDF(t *testing.T, content, shade string, extra ...string) *Document {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Shading << /Sh 5 0 R >> /Pattern << /P 5 0 R >>" +
			" /ColorSpace << /Cs [/Pattern /DeviceRGB] >> >> >>",
		streamObj("", content),
		shade,
	}
	return buildPDF(t, append(objs, extra...))
}

// grayRamp is a function from black to white over the unit domain.
const grayRamp = "<< /FunctionType 2 /Domain [0 1] /C0 [0 0 0] /C1 [1 1 1] /N 1 >>"

func TestShadingAxial(t *testing.T) {
	d := shadingPDF(t, "/Sh sh",
		"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [0 0 100 0]"+
			" /Function 6 0 R /Extend [true true] >>", grayRamp)
	px := renderDoc(t, d, nil)
	for _, tc := range []struct{ x, want int }{{2, 6}, {50, 128}, {97, 249}} {
		if got := int(pixel(px, tc.x, 50)[0]); got < tc.want-2 || got > tc.want+2 {
			t.Errorf("x=%d is %d, want about %d", tc.x, got, tc.want)
		}
	}
}

// TestShadingAxialExtend checks that a shading paints past its axis only
// where Extend says it may.
func TestShadingAxialExtend(t *testing.T) {
	d := shadingPDF(t, "/Sh sh",
		"<< /ShadingType 2 /ColorSpace /DeviceRGB /Coords [40 0 60 0]"+
			" /Function 6 0 R /Extend [false true] >>", grayRamp)
	px := renderDoc(t, d, nil)
	if got := pixel(px, 10, 50); !same(got, 255, 255, 255) {
		t.Errorf("before the axis = %v, want the page left alone", got)
	}
	if got := pixel(px, 90, 50); !same(got, 255, 255, 255) {
		t.Errorf("past the extended end = %v, want white", got)
	}
	if got := int(pixel(px, 50, 50)[0]); got < 120 || got > 136 {
		t.Errorf("the middle of the axis is %d, want about 128", got)
	}
}

// TestShadingRadial checks the concentric arrangement, whose quadratic
// degenerates to a distance from one point.
func TestShadingRadial(t *testing.T) {
	d := shadingPDF(t, "/Sh sh",
		"<< /ShadingType 3 /ColorSpace /DeviceRGB /Coords [50 50 0 50 50 40]"+
			" /Function 6 0 R /Extend [true true] >>", grayRamp)
	px := renderDoc(t, d, nil)
	if got := int(pixel(px, 50, 50)[0]); got > 8 {
		t.Errorf("the center is %d, want black", got)
	}
	if got := int(pixel(px, 70, 50)[0]); got < 120 || got > 136 {
		t.Errorf("halfway out is %d, want about 128", got)
	}
	if got := pixel(px, 95, 50); !same(got, 255, 255, 255) {
		t.Errorf("past the outer circle = %v, want white", got)
	}
}

func TestShadingPattern(t *testing.T) {
	d := shadingPDF(t, "/Pattern cs /P scn 0 0 50 100 re f",
		"<< /PatternType 2 /Shading << /ShadingType 2 /ColorSpace /DeviceRGB"+
			" /Coords [0 0 100 0] /Function 6 0 R /Extend [true true] >> >>", grayRamp)
	px := renderDoc(t, d, nil)
	if got := int(pixel(px, 25, 50)[0]); got < 55 || got > 71 {
		t.Errorf("inside the path is %d, want about 63", got)
	}
	if got := pixel(px, 75, 50); !same(got, 255, 255, 255) {
		t.Errorf("outside the path = %v, want white", got)
	}
}

// TestTilingPattern checks that a tile repeats on the XStep lattice and that
// the pattern's own bounding box clips what each repetition draws.
func TestTilingPattern(t *testing.T) {
	d := shadingPDF(t, "/Pattern cs /P scn 0 0 100 100 re f",
		streamObj("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 10 10]"+
			" /XStep 20 /YStep 20 /Resources << >>",
			"1 0 0 rg 0 0 20 20 re f"))
	px := renderDoc(t, d, nil)
	if got := pixel(px, 2, 97); !same(got, 255, 0, 0) {
		t.Errorf("the first cell = %v, want red", got)
	}
	if got := pixel(px, 12, 97); !same(got, 255, 255, 255) {
		t.Errorf("past the cell box = %v, want white", got)
	}
	if got := pixel(px, 22, 97); !same(got, 255, 0, 0) {
		t.Errorf("the next repetition = %v, want red", got)
	}
	if got := pixel(px, 2, 77); !same(got, 255, 0, 0) {
		t.Errorf("the repetition one step up = %v, want red", got)
	}
}

// TestTilingPatternUncolored checks PaintType 2, where the tile has no color
// of its own and takes the one the pattern was selected with.
func TestTilingPatternUncolored(t *testing.T) {
	d := shadingPDF(t, "/Cs cs 0 0 1 /P scn 0 0 100 100 re f",
		streamObj("/PatternType 1 /PaintType 2 /TilingType 1 /BBox [0 0 10 10]"+
			" /XStep 10 /YStep 10 /Resources << >>", "0 0 5 5 re f"))
	px := renderDoc(t, d, nil)
	if got := pixel(px, 2, 97); !same(got, 0, 0, 255) {
		t.Errorf("the uncolored cell = %v, want the blue it was selected with", got)
	}
}

// TestTilingPatternPathIsolation checks that a tile's own path building does not
// reach the path the pattern is filling, which the interpreter shares.
func TestTilingPatternPathIsolation(t *testing.T) {
	d := shadingPDF(t, "/Pattern cs /P scn 20 20 60 60 re f",
		streamObj("/PatternType 1 /PaintType 1 /TilingType 1 /BBox [0 0 10 10]"+
			" /XStep 10 /YStep 10 /Resources << >>",
			"1 0 0 rg 0 0 10 10 re f"))
	px := renderDoc(t, d, nil)
	if got := pixel(px, 50, 50); !same(got, 255, 0, 0) {
		t.Errorf("inside the filled path = %v, want red", got)
	}
	if got := pixel(px, 5, 5); !same(got, 255, 255, 255) {
		t.Errorf("outside the filled path = %v, want white", got)
	}
}

// TestShadingFunctionBased checks a type 1 shading, which is evaluated over
// a grid of its own domain and mapped onto the page by its matrix.
func TestShadingFunctionBased(t *testing.T) {
	d := shadingPDF(t, "/Sh sh",
		"<< /ShadingType 1 /ColorSpace /DeviceRGB /Domain [0 1 0 1]"+
			" /Matrix [100 0 0 100 0 0] /Function 6 0 R >>",
		streamObj("/FunctionType 4 /Domain [0 1 0 1] /Range [0 1 0 1 0 1]",
			"{ pop dup dup }"))
	px := renderDoc(t, d, nil)
	for _, tc := range []struct{ x, want int }{{25, 64}, {75, 191}} {
		if got := int(pixel(px, tc.x, 50)[0]); got < tc.want-3 || got > tc.want+3 {
			t.Errorf("x=%d is %d, want about %d", tc.x, got, tc.want)
		}
	}
}

// TestShadingMesh checks a type 4 free form mesh: one triangle with a color
// at each corner, interpolated across it.
func TestShadingMesh(t *testing.T) {
	data := "\x00\x00\x00\xff\x00\x00" + // (0,0) red
		"\x00\xff\x00\x00\xff\x00" + // (100,0) green
		"\x00\x00\xff\x00\x00\xff" // (0,100) blue
	d := shadingPDF(t, "/Sh sh",
		streamObj("/ShadingType 4 /ColorSpace /DeviceRGB /BitsPerCoordinate 8"+
			" /BitsPerComponent 8 /BitsPerFlag 8 /Decode [0 100 0 100 0 1 0 1 0 1]",
			data))
	px := renderDoc(t, d, nil)
	if got := pixel(px, 4, 95); got[0] < 200 || got[1] > 60 {
		t.Errorf("the corner by the red vertex = %v", got)
	}
	if got := pixel(px, 90, 95); got[1] < 200 || got[0] > 60 {
		t.Errorf("the corner by the green vertex = %v", got)
	}
	if got := pixel(px, 4, 6); got[2] < 200 || got[0] > 60 {
		t.Errorf("the corner by the blue vertex = %v", got)
	}
	if got := pixel(px, 90, 6); !same(got, 255, 255, 255) {
		t.Errorf("outside the triangle = %v, want white", got)
	}
}

// TestOptionalContentOff checks that a marked content section naming a group
// the default configuration turns off draws nothing. The group is named by a
// reference, so the property has to reach the visibility test unresolved.
func TestOptionalContentOff(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs [5 0 R 6 0 R]" +
			" /D << /OFF [6 0 R] >> >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Properties << /On 5 0 R /Off 6 0 R >> >> >>",
		streamObj("", "/OC /On BDC 1 0 0 rg 0 0 40 100 re f EMC "+
			"/OC /Off BDC 0 0 1 rg 60 0 40 100 re f EMC"),
		"<< /Type /OCG /Name (on) >>",
		"<< /Type /OCG /Name (off) >>",
	})
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 50); !same(got, 255, 0, 0) {
		t.Errorf("the visible group = %v, want red", got)
	}
	if got := pixel(px, 80, 50); !same(got, 255, 255, 255) {
		t.Errorf("the hidden group = %v, want white", got)
	}
}

// groupPDF builds a one page document whose content draws a form XObject,
// with the extra objects appended after it.
func groupPDF(t *testing.T, content, formDict, formBody string, extra ...string) *Document {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Fm 5 0 R >> /ExtGState << /GS 6 0 R >> >> >>",
		streamObj("", content),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100] "+formDict, formBody),
	}
	return buildPDF(t, append(objs, extra...))
}

// TestGroupAlpha checks that a transparency group composites as one object:
// two overlapping opaque squares inside a group at half alpha have to come out
// the same shade everywhere, not twice as dark where they overlap.
func TestGroupAlpha(t *testing.T) {
	d := groupPDF(t, "/GS gs /Fm Do",
		"/Group << /S /Transparency /CS /DeviceRGB /I true >>",
		"0 0 1 rg 10 10 40 40 re f 30 30 40 40 re f",
		"<< /Type /ExtGState /ca 0.5 >>")
	px := renderDoc(t, d, nil)
	one := pixel(px, 20, 70)
	both := pixel(px, 40, 50)
	if !same(one, both...) {
		t.Fatalf("the overlap is %v and the rest %v, want one shade", both, one)
	}
	if one[2] < 250 || one[0] < 120 || one[0] > 135 {
		t.Fatalf("half of blue over white = %v, want about 127 127 255", one)
	}
}

// TestBlendMultiply checks a blend mode against what the backdrop under it
// already holds.
func TestBlendMultiply(t *testing.T) {
	px := renderContent(t, "1 0 0 rg 0 0 200 100 re f /Mul gs 0 0 1 rg 0 0 100 100 re f", nil)
	if got := pixel(px, 50, 50); !same(got, 0, 0, 0) {
		t.Errorf("blue multiplied by red = %v, want black", got)
	}
	if got := pixel(px, 150, 50); !same(got, 255, 0, 0) {
		t.Errorf("outside the blend = %v, want red", got)
	}
}

// TestBlendScreen is the same the other way: Screen against white is white,
// which is what says the backdrop reaches the blend at all.
func TestBlendScreen(t *testing.T) {
	px := renderContent(t, "/Scr gs 0 0 1 rg 0 0 100 100 re f", nil)
	if got := pixel(px, 50, 50); !same(got, 255, 255, 255) {
		t.Errorf("blue screened onto white = %v, want white", got)
	}
}

// TestSoftMaskLuminosity checks that a luminosity soft mask lets through what
// its group painted white and stops what it painted black.
func TestSoftMaskLuminosity(t *testing.T) {
	// The mask group paints its left half white and leaves the rest at the
	// black its backdrop defaults to.
	d := groupPDF(t, "/GS gs 1 0 0 rg 0 0 100 100 re f",
		"/Group << /S /Transparency /CS /DeviceGray >>",
		"1 g 0 0 50 100 re f",
		"<< /Type /ExtGState /SMask << /S /Luminosity /G 5 0 R >> >>")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 50); !same(got, 255, 0, 0) {
		t.Errorf("under the white half of the mask = %v, want red", got)
	}
	if got := pixel(px, 80, 50); !same(got, 255, 255, 255) {
		t.Errorf("under the black half = %v, want the page", got)
	}
}

// TestSoftMaskPathIsolation checks that the form a soft mask renders does not
// reach the path it is masking. The mask is rendered from inside the painting
// operator, so both are building into the interpreter's one path.
func TestSoftMaskPathIsolation(t *testing.T) {
	d := groupPDF(t, "/GS gs 1 0 0 rg 20 20 60 60 re f",
		"/Group << /S /Transparency /CS /DeviceGray >>",
		"1 g 0 0 100 100 re f",
		"<< /Type /ExtGState /SMask << /S /Luminosity /G 5 0 R >> >>")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 50, 50); !same(got, 255, 0, 0) {
		t.Errorf("inside the masked path = %v, want red", got)
	}
	if got := pixel(px, 5, 5); !same(got, 255, 255, 255) {
		t.Errorf("outside the masked path = %v, want white", got)
	}
}

// TestSoftMaskAlpha checks the other kind of soft mask, which reads what the
// group painted rather than how bright it is.
func TestSoftMaskAlpha(t *testing.T) {
	d := groupPDF(t, "/GS gs 1 0 0 rg 0 0 100 100 re f",
		"/Group << /S /Transparency /CS /DeviceGray >>",
		"0 g 0 0 50 100 re f",
		"<< /Type /ExtGState /SMask << /S /Alpha /G 5 0 R >> >>")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 50); !same(got, 255, 0, 0) {
		t.Errorf("where the mask group painted = %v, want red", got)
	}
	if got := pixel(px, 80, 50); !same(got, 255, 255, 255) {
		t.Errorf("where it painted nothing = %v, want the page", got)
	}
}

// TestSoftMaskTransfer checks that /TR is applied to the mask, here inverting
// it so that the half the group painted white is the half that is hidden.
func TestSoftMaskTransfer(t *testing.T) {
	d := groupPDF(t, "/GS gs 1 0 0 rg 0 0 100 100 re f",
		"/Group << /S /Transparency /CS /DeviceGray >>",
		"1 g 0 0 50 100 re f",
		"<< /Type /ExtGState /SMask << /S /Luminosity /G 5 0 R /TR 7 0 R >> >>",
		"<< /FunctionType 2 /Domain [0 1] /C0 [1] /C1 [0] /N 1 >>")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 20, 50); !same(got, 255, 255, 255) {
		t.Errorf("under the white half, inverted = %v, want the page", got)
	}
	if got := pixel(px, 80, 50); !same(got, 255, 0, 0) {
		t.Errorf("under the black half, inverted = %v, want red", got)
	}
}

// TestGroupBlendInside checks that a blend mode inside a non-isolated group
// still sees what is under the group, which is what makes such a group worth
// keeping straight rather than turning into a pixmap of its own.
func TestGroupBlendInside(t *testing.T) {
	d := groupPDF(t, "0 1 0 rg 0 0 100 100 re f /Fm Do",
		"/Group << /S /Transparency /CS /DeviceRGB /I false >>",
		"/GS gs 1 1 0 rg 0 0 100 100 re f",
		"<< /Type /ExtGState /BM /Multiply >>")
	px := renderDoc(t, d, nil)
	if got := pixel(px, 50, 50); !same(got, 0, 255, 0) {
		t.Errorf("yellow multiplied by green = %v, want green", got)
	}
}

// TestGroupKnockout checks that an element of a knockout group composites
// against the backdrop the group opened with rather than against the element
// before it. The inner group blends, so it says which backdrop it saw: red
// over nothing, black if it saw the blue underneath.
func TestGroupKnockout(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /XObject << /Outer 5 0 R >> >> >>",
		streamObj("", "/Outer Do"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
			" /Group << /S /Transparency /CS /DeviceRGB /I true /K true >>"+
			" /Resources << /XObject << /Inner 6 0 R >> >>",
			"0 0 1 rg 0 0 100 100 re f /Inner Do"),
		streamObj("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
			" /Group << /S /Transparency /CS /DeviceRGB /I false /K false >>"+
			" /Resources << /ExtGState << /GS 7 0 R >> >>",
			"/GS gs 1 0 0 rg 20 20 60 60 re f"),
		"<< /Type /ExtGState /BM /Multiply >>",
	})
	px := renderDoc(t, d, nil)
	if got := pixel(px, 50, 50); !same(got, 255, 0, 0) {
		t.Errorf("inside the knocked out element = %v, want red", got)
	}
	if got := pixel(px, 5, 5); !same(got, 0, 0, 255) {
		t.Errorf("outside it = %v, want the blue the group put down", got)
	}
}

// layerPDF builds a document with two optional content groups, the second of
// which the default configuration turns off, and a page that paints a
// rectangle under each.
func layerPDF(t *testing.T, config string) *Document {
	t.Helper()
	return buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /OCProperties << /OCGs [5 0 R 6 0 R]" +
			" /D << " + config + " >> >> >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Properties << /L1 5 0 R /L2 6 0 R >> >> >>",
		streamObj("", "/OC /L1 BDC 1 0 0 rg 0 0 40 100 re f EMC "+
			"/OC /L2 BDC 0 0 1 rg 60 0 40 100 re f EMC"),
		"<< /Type /OCG /Name (Red) >>",
		"<< /Type /OCG /Name (Blue) /Usage << /Print << /PrintState /OFF >> >> >>",
	})
}

func TestLayers(t *testing.T) {
	d := layerPDF(t, "/OFF [6 0 R]")
	got := d.Layers()
	if len(got) != 2 || got[0].Name != "Red" || got[1].Name != "Blue" {
		t.Fatalf("Layers() = %v", got)
	}
	if !got[0].On || got[1].On {
		t.Fatalf("states = %v %v, want on then off", got[0].On, got[1].On)
	}
	px := renderDoc(t, d, nil)
	if c := pixel(px, 20, 50); !same(c, 255, 0, 0) {
		t.Errorf("the group that is on = %v, want red", c)
	}
	if c := pixel(px, 80, 50); !same(c, 255, 255, 255) {
		t.Errorf("the group that is off = %v, want white", c)
	}
}

func TestSetLayers(t *testing.T) {
	d := layerPDF(t, "/OFF [6 0 R]")
	l := d.Layers()
	l[0].On, l[1].On = false, true
	d.SetLayers(l)
	if got := d.Layers(); got[0].On || !got[1].On {
		t.Fatalf("after SetLayers: %v", got)
	}
	px := renderDoc(t, d, nil)
	if c := pixel(px, 20, 50); !same(c, 255, 255, 255) {
		t.Errorf("the group turned off = %v, want white", c)
	}
	if c := pixel(px, 80, 50); !same(c, 0, 0, 255) {
		t.Errorf("the group turned on = %v, want blue", c)
	}
}

// TestLayerUsage checks a usage application dictionary, which is how a group
// says it is meant for the screen and not for paper.
func TestLayerUsage(t *testing.T) {
	d := layerPDF(t, "/AS [<< /Event /Print /Category [/Print] /OCGs [6 0 R] >>]")
	if got := d.Layers(); !got[1].On {
		t.Fatalf("for the screen the second group is off: %v", got)
	}
	d.SetUsage(UsagePrint)
	if got := d.Layers(); got[1].On {
		t.Fatalf("for paper the second group is on: %v", got)
	}
	px := renderDoc(t, d, nil)
	if c := pixel(px, 80, 50); !same(c, 255, 255, 255) {
		t.Errorf("the group the print usage turns off = %v, want white", c)
	}
}

// TestBaseStateOff checks the other way a configuration can start, where every
// group is off except the ones it lists.
func TestBaseStateOff(t *testing.T) {
	d := layerPDF(t, "/BaseState /OFF /ON [6 0 R]")
	got := d.Layers()
	if got[0].On || !got[1].On {
		t.Fatalf("with BaseState OFF: %v", got)
	}
}

// annotPDF builds a page with the annotations given, each a dictionary body.
func annotPDF(t *testing.T, annots ...string) *Document {
	t.Helper()
	refs := ""
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"",
		streamObj("", ""),
	}
	for i, a := range annots {
		refs += fmt.Sprintf("%d 0 R ", 5+i*2)
		objs = append(objs, a, streamObj(
			"/Type /XObject /Subtype /Form /BBox [0 0 10 10]",
			fmt.Sprintf("%s rg 0 0 10 10 re f", []string{"1 0 0", "0 1 0", "0 0 1"}[i%3])))
	}
	objs[2] = "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
		" /Annots [" + refs + "] >>"
	return buildPDF(t, objs)
}

// TestAnnotationLink checks that a link is not drawn even when it carries an
// appearance, which is what every renderer this is held to does.
func TestAnnotationLink(t *testing.T) {
	d := annotPDF(t, "<< /Type /Annot /Subtype /Link /Rect [0 0 100 100] /AP << /N 6 0 R >> >>")
	px := renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 255, 255) {
		t.Errorf("a link appearance drew %v, want nothing", c)
	}
}

// TestAnnotationUsage checks the print flag, which says an annotation is for
// paper and not for the screen.
func TestAnnotationUsage(t *testing.T) {
	d := annotPDF(t, "<< /Type /Annot /Subtype /Square /F 4 /Rect [0 0 100 100] /AP << /N 6 0 R >> >>")
	px := renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 0, 0) {
		t.Errorf("on the screen = %v, want red", c)
	}
	d.SetUsage(UsagePrint)
	px = renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 0, 0) {
		t.Errorf("on paper = %v, want red", c)
	}

	d = annotPDF(t, "<< /Type /Annot /Subtype /Square /Rect [0 0 100 100] /AP << /N 6 0 R >> >>")
	d.SetUsage(UsagePrint)
	px = renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 255, 255) {
		t.Errorf("an annotation with no print flag drew %v on paper", c)
	}
}

// TestAnnotationHidden checks the two flags that hide an annotation outright.
func TestAnnotationHidden(t *testing.T) {
	for _, f := range []string{"/F 2", "/F 1"} {
		d := annotPDF(t, "<< /Type /Annot /Subtype /Square "+f+
			" /Rect [0 0 100 100] /AP << /N 6 0 R >> >>")
		px := renderDoc(t, d, nil)
		if c := pixel(px, 50, 50); !same(c, 255, 255, 255) {
			t.Errorf("%s drew %v, want nothing", f, c)
		}
	}
}

// TestAnnotationWidgetsLast checks that a widget is drawn after the
// annotations that come before it in the array, because a form field belongs
// on top of the page it sits on.
func TestAnnotationWidgetsLast(t *testing.T) {
	d := annotPDF(t,
		"<< /Type /Annot /Subtype /Widget /FT /Btn /T (b) /Rect [0 0 100 100] /AP << /N 6 0 R >> >>",
		"<< /Type /Annot /Subtype /Square /Rect [0 0 100 100] /AP << /N 8 0 R >> >>")
	px := renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 0, 0) {
		t.Errorf("the widget is under the square: %v, want the widget's red", c)
	}
}

// TestAnnotationWidgetNoField checks that a widget whose field type and name
// are on the parent field is drawn. Every widget of a page draws, which is
// what MuPDF's own page list holds.
func TestAnnotationWidgetNoField(t *testing.T) {
	d := annotPDF(t,
		"<< /Type /Annot /Subtype /Widget /Parent 9 0 R /Rect [0 0 100 100] /AP << /N 6 0 R >> >>")
	px := renderDoc(t, d, nil)
	if c := pixel(px, 50, 50); !same(c, 255, 0, 0) {
		t.Errorf("the widget drew %v, want its red", c)
	}
}

// TestPatternText checks that text filled with a pattern clips to the glyphs
// and paints the pattern through them, which is the only way a device that
// knows nothing of patterns can draw one.
func TestPatternText(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R" +
			" /Resources << /Font << /F 5 0 R >> /Pattern << /P 6 0 R >> >> >>",
		streamObj("", "/Pattern cs /P scn BT /F 24 Tf 10 40 Td (Hi) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		streamObj("/Type /Pattern /PatternType 1 /PaintType 1 /TilingType 1"+
			" /BBox [0 0 10 10] /XStep 10 /YStep 10 /Resources << >>",
			"1 0 0 rg 0 0 10 10 re f"),
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := p.Run(NewTraceDevice(&buf), p.Matrix(72)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"<clip_text ", "<tile ", "<pop_clip/>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the trace has no %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<fill_text ") {
		t.Errorf("the text was filled with the pattern colour:\n%s", got)
	}
}

// textDoc is a page of Helvetica text: two lines, and a third far enough to
// the right on the second baseline to be a line of its own.
func textDoc(t *testing.T) *Document {
	t.Helper()
	return buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R" +
			" /Resources << /Font << /F 5 0 R >> >> /Annots [6 0 R] >>",
		streamObj("", "BT /F 12 Tf 20 160 Td (Hello world) Tj"+
			" 20 140 Td (Second line) Tj 200 0 Td (far away) Tj ET"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Annot /Subtype /Link /Rect [20 150 120 175]" +
			" /A << /S /URI /URI (https://example.com/) >> >>",
	})
}

func TestPageText(t *testing.T) {
	p, err := textDoc(t).Page(0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Text()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello world", "Second line", "far away"} {
		if !strings.Contains(got, want) {
			t.Fatalf("text %q does not hold %q", got, want)
		}
	}
	if strings.Contains(got, "Second line far away") {
		t.Fatalf("a gap of two hundred points did not break the line: %q", got)
	}
}

func TestStructuredText(t *testing.T) {
	p, err := textDoc(t).Page(0)
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.StructuredText()
	if err != nil {
		t.Fatal(err)
	}
	var lines []*TextLine
	for i := range st.Blocks {
		for j := range st.Blocks[i].Lines {
			lines = append(lines, &st.Blocks[i].Lines[j])
		}
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	first := lines[0]
	if got := len(first.Chars); got != len("Hello world") {
		t.Fatalf("the first line has %d characters, want %d", got, len("Hello world"))
	}
	// The device works in page space, where y counts down from the top, so
	// text drawn at y=160 in user space is near the top of a 200 point page.
	if first.Bounds.Y0 < 20 || first.Bounds.Y0 > 45 {
		t.Fatalf("the first line is at y %v, want it near the top", first.Bounds.Y0)
	}
	if first.Dir != (raster.Point{X: 1}) {
		t.Fatalf("direction = %v, want it along x", first.Dir)
	}
	for _, c := range first.Chars {
		if c.Quad.Bounds().IsEmpty() && c.Rune != ' ' {
			t.Fatalf("character %q has an empty quad", c.Rune)
		}
	}
}

func TestPageLinks(t *testing.T) {
	p, err := textDoc(t).Page(0)
	if err != nil {
		t.Fatal(err)
	}
	links := p.Links()
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if got := links[0].URI; got != "https://example.com/" {
		t.Fatalf("uri = %q", got)
	}
	if got := links[0].Page; got != -1 {
		t.Fatalf("page = %d, want -1 for a link out of the document", got)
	}
	// /Rect is in user space and the link comes back in page space, so the
	// top of the rectangle is 200-175.
	if got := links[0].Rect; got.Y0 != 25 || got.Y1 != 50 || got.X0 != 20 {
		t.Fatalf("rect = %v", got)
	}
}

func TestLinkDestination(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /Names << /Dests 6 0 R >> >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Annots [4 0 R] >>",
		"<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest (target) >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		"<< /Names [(target) [5 0 R /XYZ 30 40 0]] >>",
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	links := p.Links()
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if got := links[0].Page; got != 1 {
		t.Fatalf("page = %d, want 1", got)
	}
	if got := links[0].Point; got.X != 30 || got.Y != 40 {
		t.Fatalf("point = %v", got)
	}
}

func TestPageImageNaturalSize(t *testing.T) {
	// A page of 72 by 72 points carrying nothing but a 144 by 144 image is a
	// scan at 144 dots per inch, and has to come back at that size.
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] /Contents 4 0 R" +
			" /Resources << /XObject << /Im 5 0 R >> >> >>",
		streamObj("", "q 72 0 0 72 0 0 cm /Im Do Q"),
		streamObj("/Type /XObject /Subtype /Image /Width 144 /Height 144"+
			" /ColorSpace /DeviceGray /BitsPerComponent 8", strings.Repeat("\x80", 144*144)),
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := p.Image()
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dx(); got != 144 {
		t.Fatalf("width = %d, want the 144 the image has", got)
	}
}

func TestPageSVG(t *testing.T) {
	p, err := textDoc(t).Page(0)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.SVG()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`width="300" height="200"`,
		"<defs>",
		"<use xlink:href=",
		"</svg>",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("the svg does not hold %q:\n%s", want, s)
		}
	}
}

// TestCIDUnicode checks the character collection standing in for a
// /ToUnicode: a composite font that names one and embeds nothing says what
// its text is only through the collection's own Unicode mapping.
func TestCIDUnicode(t *testing.T) {
	cases := []struct {
		ordering string
		cid      uint32
		want     rune
	}{
		{"Japan1", 34, 'A'},
		{"Japan1", 3284, '日'},
		{"GB1", 34, 'A'},
		{"GB1", 4559, '中'},
		{"CNS1", 34, 'A'},
		{"Korea1", 34, 'A'},
		{"KR", 34, 'A'},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.ordering, tc.cid), func(t *testing.T) {
			d := buildPDF(t, []string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100]" +
					" /Resources << /Font << /F 4 0 R >> >> >>",
				"<< /Type /Font /Subtype /Type0 /BaseFont /Nothing /Encoding /Identity-H" +
					" /DescendantFonts [5 0 R] >>",
				"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /Nothing" +
					" /CIDSystemInfo << /Registry (Adobe) /Ordering (" + tc.ordering + ") /Supplement 0 >> >>",
			})
			ft := d.font(Ref{Num: 4}, nil)
			if ft == nil {
				t.Fatal("no font")
			}
			if got := ft.Rune(Char{Code: tc.cid, CID: tc.cid}); got != tc.want {
				t.Fatalf("CID %d in %s is %q, want %q", tc.cid, tc.ordering, got, tc.want)
			}
		})
	}
}

// TestCIDUnicodePrefersTheIdeograph checks the rule the tables are built
// under: where Unicode has a compatibility form of a character, the character
// itself is what a CID stands for. CID 843 of Adobe-Japan1 is reachable from
// the Kangxi radical as well as from the ideograph.
func TestCIDUnicodePrefersTheIdeograph(t *testing.T) {
	if got := uniRuneOf(cidUnicode("Japan1"), 3284); got == '⽇' {
		t.Fatalf("CID 843 came back as the Kangxi radical")
	}
}

func TestOutline(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /Outlines 5 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		"<< /Type /Outlines /First 6 0 R /Count 2 >>",
		"<< /Title (One) /Dest [3 0 R /XYZ 10 20 0] /First 7 0 R /Count 1 /Next 8 0 R >>",
		"<< /Title (Nested) /Dest [4 0 R /Fit] >>",
		"<< /Title (Two) /A << /S /URI /URI (https://example.com/) >> >>",
	})
	o := d.Outline()
	if len(o) != 2 {
		t.Fatalf("got %d entries, want 2", len(o))
	}
	if o[0].Title != "One" || o[0].Page != 0 || o[0].Point.X != 10 || o[0].Point.Y != 20 {
		t.Fatalf("first = %+v", o[0])
	}
	if !o[0].Open {
		t.Fatalf("an entry with a positive /Count is open")
	}
	if len(o[0].Children) != 1 || o[0].Children[0].Title != "Nested" || o[0].Children[0].Page != 1 {
		t.Fatalf("children = %+v", o[0].Children)
	}
	if o[1].Title != "Two" || o[1].URI != "https://example.com/" || o[1].Page != -1 {
		t.Fatalf("second = %+v", o[1])
	}
}

// TestOutlineCycle checks that an outline pointing back at itself terminates,
// which a damaged file is free to do.
func TestOutlineCycle(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /Outlines 4 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		"<< /Type /Outlines /First 5 0 R >>",
		"<< /Title (Loop) /Next 5 0 R /First 5 0 R >>",
	})
	if o := d.Outline(); len(o) != 1 {
		t.Fatalf("got %d entries, want 1", len(o))
	}
}

func TestMetadata(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
	})
	// The trailer buildPDF writes has no /Info, so put one in by hand.
	d.f.Trailer()["Info"] = Dict{
		"Title":        String("A Title"),
		"Author":       String("An Author"),
		"CreationDate": String("D:20240115103000+01'30'"),
		"ModDate":      String("D:2024"),
	}
	m := d.Metadata()
	if m.Title != "A Title" || m.Author != "An Author" {
		t.Fatalf("metadata = %+v", m)
	}
	if got, want := m.Created.UTC(), time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("created = %v, want %v", got, want)
	}
	if got, want := m.Modified.UTC(), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("modified = %v, want %v", got, want)
	}
}

func TestPageLabels(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R /PageLabels 8 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R 6 0 R 7 0 R] /Count 5 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
		"<< /Nums [0 << /S /r >> 2 << /S /D /St 7 >> 4 << /S /A /P (App ) >>] >>",
	})
	got := d.PageLabels()
	want := []string{"i", "ii", "7", "8", "App A"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestRomanAndLetters(t *testing.T) {
	cases := []struct{ n, style, want string }{
		{"1", "R", "I"}, {"4", "R", "IV"}, {"9", "R", "IX"},
		{"14", "r", "xiv"}, {"1990", "R", "MCMXC"}, {"4000", "R", "4000"},
		{"1", "A", "A"}, {"26", "A", "Z"}, {"27", "a", "aa"}, {"53", "A", "AAA"},
		{"54", "A", "BBB"},
	}
	for _, tc := range cases {
		n, _ := strconv.Atoi(tc.n)
		if got := pageLabel(Name(tc.style), n); got != tc.want {
			t.Fatalf("%s of %d = %q, want %q", tc.style, n, got, tc.want)
		}
	}
}

// FuzzDocument reads a whole file and asks it everything the public API asks:
// the outline, the metadata, the labels, and the text, links and structure of
// the first pages. None of it may panic on any input.
func FuzzDocument(fu *testing.F) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.pdf"))
	for _, name := range names {
		if b, err := os.ReadFile(name); err == nil {
			fu.Add(b)
		}
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		d, err := Load(b, "")
		if err != nil {
			return
		}
		defer d.Close()
		d.Outline()
		d.Metadata()
		d.PageLabels()
		for i := 0; i < d.NumPages() && i < 3; i++ {
			p, err := d.Page(i)
			if err != nil {
				continue
			}
			p.Links()
			p.Text()
			p.WriteSVG(io.Discard)
		}
	})
}

// TestCP936Disguise checks the font that says it is simple and is not: one
// generator writes /WinAnsiEncoding on a Chinese font and then draws two byte
// GBK codes through it.
func TestCP936Disguise(t *testing.T) {
	// The bytes are the GBK name of SimFang, and the string is "全世".
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R" +
			" /Resources << /Font << /F 5 0 R >> >> >>",
		streamObj("", "BT /F 12 Tf 10 50 Td <C8ABCAC0> Tj ET"),
		"<< /Type /Font /Subtype /TrueType /BaseFont /#B7#C2#CB#CE_GB2312" +
			" /Encoding /WinAnsiEncoding /FirstChar 0 /LastChar 255" +
			" /FontDescriptor << /Flags 4 /MissingWidth 1000 >> >>",
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Text()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "全世") {
		t.Fatalf("text = %q, want the two characters the GBK bytes stand for", got)
	}
}

// TestCP936OnlyWhenDisguised checks the test is narrow: a font that is what it
// says it is keeps its own encoding.
func TestCP936OnlyWhenDisguised(t *testing.T) {
	d := buildPDF(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R" +
			" /Resources << /Font << /F 5 0 R >> >> >>",
		streamObj("", "BT /F 12 Tf 10 50 Td (Hi) Tj ET"),
		"<< /Type /Font /Subtype /TrueType /BaseFont /Helvetica" +
			" /Encoding /WinAnsiEncoding /FontDescriptor << /Flags 4 >> >>",
	})
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Text()
	if !strings.Contains(got, "Hi") {
		t.Fatalf("text = %q, want Hi", got)
	}
}
