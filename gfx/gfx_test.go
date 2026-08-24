package gfx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	pngenc "image/png"
	"sort"
	"testing"

	"github.com/gen2brain/folio/font"
	"github.com/gen2brain/folio/raster"
)

// testFont is a glyph source with nothing behind it: the text device needs a
// name and an em box and never asks for an outline.
type testFont struct{}

func (testFont) FontName() string                 { return "Test" }
func (testFont) Program() *font.Font              { return nil }
func (testFont) EmBox() (ascent, descent float32) { return 0.8, -0.2 }
func (testFont) RunGlyph(Device, int, raster.Matrix, *ColorSpace, []float32, float32, int) {
}

// runText builds one span of upright text at size 10, placing each rune at
// the x it is given.
func runText(runes []rune, xs []float32, y float32) *Text {
	sp := TextSpan{Font: testFont{}, Trm: raster.Matrix{A: 10, D: 10}}
	for i, r := range runes {
		sp.Items = append(sp.Items, TextItem{X: xs[i], Y: y, GID: int(r), Rune: r, Adv: 0.5})
	}
	return &Text{Spans: []TextSpan{sp}}
}

func collect(t *testing.T, text *Text) *TextPage {
	t.Helper()
	st := &TextPage{}
	d := NewTextDevice(st, nil)
	d.FillText(text, raster.Identity, DeviceGray, []float32{0}, 1, ColorParams{})
	d.Close()
	return st
}

func lineText(l *TextLine) string {
	var out []rune
	for _, c := range l.Chars {
		out = append(out, c.Rune)
	}
	return string(out)
}

func allLines(st *TextPage) []string {
	var out []string
	for i := range st.Blocks {
		for j := range st.Blocks[i].Lines {
			out = append(out, lineText(&st.Blocks[i].Lines[j]))
		}
	}
	return out
}

func TestTextRuns(t *testing.T) {
	// Each glyph advances five points, so the runs below are a word, a word
	// break of two points, and a gap of forty that is a line of its own.
	cases := []struct {
		name  string
		runes []rune
		xs    []float32
		want  []string
	}{
		{"one word", []rune("abc"), []float32{0, 5, 10}, []string{"abc"}},
		{"word break", []rune("abcd"), []float32{0, 5, 12, 17}, []string{"ab cd"}},
		{"line break", []rune("abcd"), []float32{0, 5, 50, 55}, []string{"ab", "cd"}},
		{"drawn twice", []rune("aabb"), []float32{0, 0, 5, 5}, []string{"ab"}},
		{"ligature", []rune("aﬁb"), []float32{0, 5, 10}, []string{"afib"}},
		{"nothing to read", []rune("   "), []float32{0, 5, 10}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allLines(collect(t, runText(tc.runes, tc.xs, 0)))
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestTextQuad(t *testing.T) {
	st := collect(t, runText([]rune("a"), []float32{0}, 0))
	c := st.Blocks[0].Lines[0].Chars[0]
	// The em box of a size ten font over an advance of five points, in a
	// space where y counts up.
	want := Quad{
		UL: raster.Point{X: 0, Y: 8}, UR: raster.Point{X: 5, Y: 8},
		LL: raster.Point{X: 0, Y: -2}, LR: raster.Point{X: 5, Y: -2},
	}
	if c.Quad != want {
		t.Fatalf("quad = %v, want %v", c.Quad, want)
	}
	if c.Size != 10 {
		t.Fatalf("size = %v, want 10", c.Size)
	}
	if got, want := c.Quad.Bounds(), (raster.Rect{X0: 0, Y0: -2, X1: 5, Y1: 8}); got != want {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
}

func TestTextParagraphs(t *testing.T) {
	// Two lines a step apart are one paragraph; one far below is another.
	st := &TextPage{}
	d := NewTextDevice(st, nil)
	for _, y := range []float32{0, -12, -200} {
		d.FillText(runText([]rune("ab"), []float32{0, 5}, y), raster.Identity,
			DeviceGray, []float32{0}, 1, ColorParams{})
	}
	d.Close()
	if len(st.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(st.Blocks))
	}
	if got := len(st.Blocks[0].Lines); got != 2 {
		t.Fatalf("the first block has %d lines, want 2", got)
	}
}

func TestTextPageText(t *testing.T) {
	st := &TextPage{}
	d := NewTextDevice(st, nil)
	for _, y := range []float32{0, -200} {
		d.FillText(runText([]rune("ab"), []float32{0, 5}, y), raster.Identity,
			DeviceGray, []float32{0}, 1, ColorParams{})
	}
	d.Close()
	if got, want := st.Text(), "ab\n\nab\n\n"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// buildICC assembles a matrix/TRC profile out of the tags a test needs.
func buildICC(t *testing.T, space string, tags map[string][]byte) []byte {
	t.Helper()
	head := make([]byte, 128)
	copy(head[12:], "mntr")
	copy(head[16:], space)
	copy(head[20:], "XYZ ")
	copy(head[36:], "acsp")

	names := make([]string, 0, len(tags))
	for k := range tags {
		names = append(names, k)
	}
	sort.Strings(names)

	out := append([]byte{}, head...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(names)))
	off := len(out) + 12*len(names)
	body := []byte{}
	for _, k := range names {
		out = append(out, k...)
		out = binary.BigEndian.AppendUint32(out, uint32(off+len(body)))
		out = binary.BigEndian.AppendUint32(out, uint32(len(tags[k])))
		body = append(body, tags[k]...)
	}
	return append(out, body...)
}

func iccXYZTag(x, y, z float64) []byte {
	b := append([]byte("XYZ "), 0, 0, 0, 0)
	for _, v := range []float64{x, y, z} {
		b = binary.BigEndian.AppendUint32(b, uint32(int32(v*65536)))
	}
	return b
}

func iccGammaTag(g float64) []byte {
	b := append([]byte("curv"), 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint32(b, 1)
	return binary.BigEndian.AppendUint16(b, uint16(g*256))
}

func TestICCProfile(t *testing.T) {
	// sRGB's own primaries, adapted to D50 as a profile stores them, so that
	// the matrix contributes nothing and the curve is what is under test.
	primaries := map[string][]byte{
		"rXYZ": iccXYZTag(0.4360, 0.2225, 0.0139),
		"gXYZ": iccXYZTag(0.3851, 0.7169, 0.0971),
		"bXYZ": iccXYZTag(0.1431, 0.0606, 0.7141),
	}
	// A gamma of 1.8 is what Apple's Generic RGB profile carries, and the one
	// file of the corpus that needs a profile read at all is written in it.
	generic := map[string][]byte{}
	for k, v := range primaries {
		generic[k] = v
	}
	for _, k := range []string{"rTRC", "gTRC", "bTRC"} {
		generic[k] = iccGammaTag(1.8)
	}
	p := ParseICC(buildICC(t, "RGB ", generic))
	if p == nil {
		t.Fatal("a matrix/TRC profile was not read")
	}
	if p.Components() != 3 {
		t.Fatalf("Components = %d, want 3", p.Components())
	}
	// Half gray through a gamma of 1.8 is lighter than half gray through
	// sRGB's curve: linear 0.5^1.8 encoded again is 0.567, which is the 145
	// to 146 out of 255 that MuPDF, poppler, cairo and Ghostscript all show.
	r, g, b := p.ToRGB([]float32{0.5, 0.5, 0.5})
	want := linearToSRGB(pow32(0.5, 1.8))
	for _, v := range []float32{r, g, b} {
		if abs32(v-want) > 0.002 {
			t.Fatalf("0.5 gray became %.4f, want %.4f", v, want)
		}
	}

	// An sRGB profile says nothing the pipeline does not already do, and is
	// dropped so that a page stays byte for byte what it was.
	srgb := map[string][]byte{}
	for k, v := range primaries {
		srgb[k] = v
	}
	curve := append([]byte("curv"), 0, 0, 0, 0)
	curve = binary.BigEndian.AppendUint32(curve, 256)
	for i := 0; i < 256; i++ {
		v := srgbToLinear(float32(i) / 255)
		curve = binary.BigEndian.AppendUint16(curve, uint16(v*65535+0.5))
	}
	for _, k := range []string{"rTRC", "gTRC", "bTRC"} {
		srgb[k] = curve
	}
	if p := ParseICC(buildICC(t, "RGB ", srgb)); p != nil {
		t.Error("an sRGB profile should be left to the pipeline")
	}

	// A gray profile is one curve, and a profile shaped in a way this cannot
	// read is not guessed at.
	gray := ParseICC(buildICC(t, "GRAY", map[string][]byte{"kTRC": iccGammaTag(2.2)}))
	if gray == nil || gray.Components() != 1 {
		t.Fatal("a gray profile was not read")
	}
	wantGray := linearToSRGB(pow32(0.5, 2.2))
	if v, _, _ := gray.ToRGB([]float32{0.5}); abs32(v-wantGray) > 0.002 {
		t.Errorf("0.5 gray became %.4f, want %.4f", v, wantGray)
	}
	if p := ParseICC(buildICC(t, "CMYK", generic)); p != nil {
		t.Error("a CMYK profile has no matrix and must not be read as one")
	}
	for n := 0; n < 140; n++ {
		ParseICC(buildICC(t, "RGB ", generic)[:n])
	}
}

func TestRegisterPictureDecoder(t *testing.T) {
	var png bytes.Buffer
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	src.Set(1, 0, color.RGBA{R: 40, G: 50, B: 60, A: 255})
	if err := pngenc.Encode(&png, src); err != nil {
		t.Fatal(err)
	}

	p, err := DecodePicture(png.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.pix.Samples[0]; got != 10 {
		t.Errorf("standard decoder gave %d, want 10", got)
	}

	called := 0
	RegisterPictureDecoder("png", "\x89PNG\r\n\x1a\n", func(b []byte) (image.Image, error) {
		called++
		m := image.NewRGBA(image.Rect(0, 0, 2, 1))
		m.Set(0, 0, color.RGBA{R: 99, G: 99, B: 99, A: 255})
		return m, nil
	})
	p, err = DecodePicture(png.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Errorf("the registered decoder ran %d times, want 1", called)
	}
	if got := p.pix.Samples[0]; got != 99 {
		t.Errorf("registered decoder gave %d, want 99", got)
	}

	RegisterPictureDecoder("png", "\x89PNG\r\n\x1a\n", nil)
	p, err = DecodePicture(png.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.pix.Samples[0]; got != 10 {
		t.Errorf("after removal the decoder gave %d, want 10", got)
	}

	RegisterPictureDecoder("huge", "\x89PNG\r\n\x1a\n", func(b []byte) (image.Image, error) {
		return fakeBounds{}, nil
	})
	defer RegisterPictureDecoder("huge", "\x89PNG\r\n\x1a\n", nil)
	if _, err := DecodePicture(png.Bytes()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a decoder returning an unbounded image gave %v, want ErrUnsupported", err)
	}
}

// fakeBounds is an image that claims more pixels than a picture may allocate
// without holding any.
type fakeBounds struct{}

func (fakeBounds) ColorModel() color.Model { return color.RGBAModel }
func (fakeBounds) Bounds() image.Rectangle { return image.Rect(0, 0, 1<<15, 1<<15) }
func (fakeBounds) At(x, y int) color.Color { return color.RGBA{} }

// stubImage is an image of a size.
type stubImage struct{ w, h int }

func (s stubImage) Size() (int, int)                                { return s.w, s.h }
func (s stubImage) ColorSpace() *ColorSpace                         { return nil }
func (s stubImage) Stencil() bool                                   { return false }
func (s stubImage) Smooth() bool                                    { return false }
func (s stubImage) Pixels(*ColorSpace, int) (*raster.Pixmap, error) { return nil, nil }

// TestNaturalDPI covers the resolution a page is rendered at and when it is
// cropped to the picture on it.
func TestNaturalDPI(t *testing.T) {
	page := raster.Rect{X1: 600, Y1: 900}
	block := func(w, h int, x0, y0, x1, y1 float32) TextBlock {
		return TextBlock{Bounds: raster.Rect{X0: x0, Y0: y0, X1: x1, Y1: y1}, Image: stubImage{w, h}}
	}
	text := TextBlock{Bounds: raster.Rect{X1: 600, Y1: 100}, Lines: []TextLine{{}}}

	for _, c := range []struct {
		name   string
		blocks []TextBlock
		dpi    float64
		crop   bool
	}{
		{"nothing at all", nil, DefaultDPI, false},
		{"a page that is one picture", []TextBlock{block(1200, 1800, 0, 0, 600, 900)}, 144, true},
		{"a landscape picture across the page",
			[]TextBlock{block(1400, 1000, 0, 0, 600, 428)}, 168, false},
		{"a tall picture down the page",
			[]TextBlock{block(800, 1600, 0, 0, 450, 900)}, 128, true},
		{"a thin strip across the foot of the page",
			[]TextBlock{block(81915, 1, 0, 791, 4178, 792)}, 1411, false},
		{"a small picture reaching neither edge",
			[]TextBlock{block(100, 100, 10, 10, 110, 110)}, 72, false},
		{"a picture on a page with text",
			[]TextBlock{block(1200, 1800, 0, 0, 600, 900), text}, 144, false},
	} {
		st := &TextPage{Bounds: page, Blocks: c.blocks}
		dpi, _, crop := NaturalDPI(st, page, CropArea)
		if int(dpi) != int(c.dpi) {
			t.Errorf("%s: %v dpi, want %v", c.name, dpi, c.dpi)
		}
		if crop != c.crop {
			t.Errorf("%s: crop %v, want %v", c.name, crop, c.crop)
		}
	}

	if dpi, _, crop := NaturalDPI(nil, page, CropArea); dpi != DefaultDPI || crop {
		t.Errorf("no text page gave %v %v, want %v false", dpi, crop, DefaultDPI)
	}

	// A book crops at a lower share.
	land := &TextPage{Bounds: page, Blocks: []TextBlock{block(1400, 1000, 0, 0, 600, 428)}}
	if _, _, crop := NaturalDPI(land, page, BookCropArea); !crop {
		t.Error("a landscape picture on a book page was not cropped to")
	}
	strip := &TextPage{Bounds: page, Blocks: []TextBlock{block(81915, 1, 0, 791, 4178, 792)}}
	if _, _, crop := NaturalDPI(strip, page, BookCropArea); crop {
		t.Error("a thin strip was cropped to even at the book share")
	}
}
