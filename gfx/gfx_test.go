package gfx

import (
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
