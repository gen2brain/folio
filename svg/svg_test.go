package svg

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gen2brain/folio/raster"
)

// walker records a path as the commands it is made of.
type walker struct{ out []string }

func (w *walker) MoveTo(x, y float32) { w.out = append(w.out, fm("M", x, y)) }
func (w *walker) LineTo(x, y float32) { w.out = append(w.out, fm("L", x, y)) }
func (w *walker) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	w.out = append(w.out, fm("C", x1, y1, x2, y2, x3, y3))
}
func (w *walker) Close() { w.out = append(w.out, "Z") }

func fm(op string, v ...float32) string {
	var b strings.Builder
	b.WriteString(op)
	for _, f := range v {
		n := math.Round(float64(f)*100) / 100
		if n == 0 {
			n = 0
		}
		b.WriteString(" ")
		b.WriteString(strconv.FormatFloat(n, 'g', -1, 64))
	}
	return b.String()
}

func path(t *testing.T, d string) []string {
	t.Helper()
	w := &walker{}
	pathData(d).Walk(w)
	return w.out
}

func TestPathData(t *testing.T) {
	for _, tc := range []struct {
		name, d string
		want    []string
	}{
		{"absolute", "M10 20 L30 40 Z", []string{"M 10 20", "L 30 40", "Z"}},
		{"relative", "m10 20 l20 20 z", []string{"M 10 20", "L 30 40", "Z"}},
		{"implicit lineto", "M10 20 30 40", []string{"M 10 20", "L 30 40"}},
		{"implicit relative", "m10 20 20 20", []string{"M 10 20", "L 30 40"}},
		{"horizontal", "M10 20H30V40", []string{"M 10 20", "L 30 20", "L 30 40"}},
		{"no separator", "M.5.5L1.5.5", []string{"M 0.5 0.5", "L 1.5 0.5"}},
		{"exponent", "M1e1 2e1", []string{"M 10 20"}},
		{"sign as separator", "M10 20L-5-6", []string{"M 10 20", "L -5 -6"}},
		{"quadratic", "M0 0 Q10 0 10 10", []string{"M 0 0", "C 6.67 0 10 3.33 10 10"}},
		{"smooth first", "M0 0 S10 0 10 10", []string{"M 0 0", "C 0 0 10 0 10 10"}},
		{"close then move", "M0 0 L1 0 Z L2 0", []string{"M 0 0", "L 1 0", "Z", "L 2 0"}},
		{"repeat command", "M0 0 L1 0 L2 0", []string{"M 0 0", "L 1 0", "L 2 0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := path(t, tc.d)
			if len(got) != len(tc.want) {
				t.Fatalf("%q gave %v, want %v", tc.d, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("%q gave %v, want %v", tc.d, got, tc.want)
					return
				}
			}
		})
	}
}

// TestPathArc checks that an endpoint arc reaches its endpoint.
func TestPathArc(t *testing.T) {
	for _, d := range []string{
		"M0 0 A50 50 0 0 1 100 0",
		"M0 0 A50 50 0 1 0 100 0",
		"M0 0 A50 25 30 1 1 100 40",
		"M0 0 A1 1 0 0 1 100 0",
	} {
		p := pathData(d)
		w := &walker{}
		p.Walk(w)
		if len(w.out) < 2 {
			t.Fatalf("%q drew %v", d, w.out)
		}
		last := w.out[len(w.out)-1]
		var want string
		switch {
		case strings.HasSuffix(d, "100 0"):
			want = "100 0"
		default:
			want = "100 40"
		}
		if !strings.HasSuffix(last, want) {
			t.Errorf("%q ended at %q, want it to reach %s", d, last, want)
		}
	}
	// An arc with a zero radius is a line, SVG 1.1 F.6.6.
	if got := path(t, "M0 0 A0 50 0 0 1 100 0"); len(got) != 2 || got[1] != "L 100 0" {
		t.Errorf("a zero radius gave %v, want a line", got)
	}
}

func TestTransform(t *testing.T) {
	for _, tc := range []struct {
		in   string
		x, y float32
		want [2]float32
	}{
		{"translate(10,20)", 1, 2, [2]float32{11, 22}},
		{"translate(10)", 1, 2, [2]float32{11, 2}},
		{"scale(2)", 3, 4, [2]float32{6, 8}},
		{"scale(2,3)", 3, 4, [2]float32{6, 12}},
		{"matrix(1,0,0,1,5,6)", 1, 1, [2]float32{6, 7}},
		{"rotate(90)", 1, 0, [2]float32{0, 1}},
		{"rotate(90,5,5)", 5, 5, [2]float32{5, 5}},
		{"translate(10,0) scale(2)", 1, 0, [2]float32{12, 0}},
		{"scale(2) translate(10,0)", 1, 0, [2]float32{22, 0}},
	} {
		m := transform(tc.in)
		p := m.Apply(raster.Point{X: tc.x, Y: tc.y})
		if !near(p.X, tc.want[0]) || !near(p.Y, tc.want[1]) {
			t.Errorf("%q took (%g,%g) to (%g,%g), want (%g,%g)",
				tc.in, tc.x, tc.y, p.X, p.Y, tc.want[0], tc.want[1])
		}
	}
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

func TestLength(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float32
		ok   bool
	}{
		{"10", 10, true},
		{"10px", 10, true},
		{"50%", 100, true},
		{"1in", 96, true},
		{"72pt", 96, true},
		{"2em", 24, true},
		{"", 0, false},
		{"auto", 0, false},
	} {
		v, ok := length(tc.in, 200, 12)
		if ok != tc.ok || (ok && !near(v, tc.want)) {
			t.Errorf("%q gave %v %v, want %v %v", tc.in, v, ok, tc.want, tc.ok)
		}
	}
}

func TestColor(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want [3]float32
		none bool
		ok   bool
	}{
		{in: "red", want: [3]float32{1, 0, 0}, ok: true},
		{in: "#0f0", want: [3]float32{0, 1, 0}, ok: true},
		{in: "#0000ff", want: [3]float32{0, 0, 1}, ok: true},
		{in: "rgb(255,0,0)", want: [3]float32{1, 0, 0}, ok: true},
		{in: "rgb(100%,0%,0%)", want: [3]float32{1, 0, 0}, ok: true},
		{in: "none", none: true, ok: true},
		{in: "url(#g) blue", want: [3]float32{0, 0, 1}, ok: true},
		{in: "url(#g)", ok: false},
		{in: "notacolor", ok: false},
	} {
		p, ok := parsePaint(tc.in, black)
		if ok != tc.ok {
			t.Errorf("%q gave ok %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if p.none != tc.none {
			t.Errorf("%q gave none %v, want %v", tc.in, p.none, tc.none)
			continue
		}
		if !tc.none && p.color != tc.want {
			t.Errorf("%q gave %v, want %v", tc.in, p.color, tc.want)
		}
	}
}

// TestDocumentSize covers the size a drawing reads from its root element.
func TestDocumentSize(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		w, h     float32
	}{
		{"width and height", `<svg width="100" height="50"/>`, 100, 50},
		{"viewBox alone", `<svg viewBox="0 0 40 30"/>`, 40, 30},
		{"neither", `<svg/>`, defWidth, defHeight},
		{"units", `<svg width="1in" height="72pt"/>`, 96, 96},
		{"percent of the page", `<svg width="50%" height="50%"/>`, defWidth / 2, defHeight / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Load([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			b := d.Page0().Bounds()
			if !near(b.X1, tc.w) || !near(b.Y1, tc.h) {
				t.Errorf("%s is %gx%g, want %gx%g", tc.in, b.X1, b.Y1, tc.w, tc.h)
			}
		})
	}
	if _, err := Load([]byte(`<html><body>no</body></html>`)); err == nil {
		t.Error("a file with no svg element opened")
	}
}

// Page0 is the page, for a test that knows there is one.
func (d *Document) Page0() *Page {
	p, _ := d.Page(0)
	return p
}

// FuzzSVG checks that no file panics and a drawing that opens also draws.

func FuzzSVG(fu *testing.F) {
	fu.Add([]byte(`<svg width="20" height="20"><rect width="10" height="10"/></svg>`))
	fu.Add([]byte(`<svg viewBox="0 0 8 8"><path d="M0 0A4 4 0 1 1 8 8Z" fill="#abc"/></svg>`))
	fu.Add([]byte(`<svg><defs><g id="a"><circle r="3"/></g></defs><use href="#a" x="4"/></svg>`))
	fu.Add([]byte(`<svg><g transform="rotate(30 1 2) skewX(10)" opacity=".5"><line x2="9"/></g></svg>`))
	fu.Fuzz(func(t *testing.T, b []byte) {
		defer slowFuzz(t)()
		d, err := Load(b)
		if err != nil {
			return
		}
		defer d.Close()
		p, err := d.Page(0)
		if err != nil {
			return
		}
		if bb := p.Bounds(); bb.X1 > 4096 || bb.Y1 > 4096 {
			return
		}
		p.ImageDPI(24)
	})
}

// TestGradientStops covers stop offsets and stops inherited through href.
func TestGradientStops(t *testing.T) {
	d, err := Load([]byte(`<svg>
	  <linearGradient id="base">
	    <stop offset="0" stop-color="#00f"/>
	    <stop offset="1" stop-color="red"/>
	  </linearGradient>
	  <linearGradient id="heir" href="#base" x1="0" x2="10"/>
	  <linearGradient id="back">
	    <stop offset="0" stop-color="#00f"/>
	    <stop offset="0.7" stop-color="red"/>
	    <stop offset="0.1" stop-color="#0f0"/>
	  </linearGradient>
	  <rect width="10" height="10" fill="url(#heir)"/>
	</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{doc: d}
	st := initialState(100, 100)
	g, _ := r.server("url(#heir)", raster.Rect{X1: 10, Y1: 10}, st)
	if g == nil {
		t.Fatal("the gradient did not resolve")
	}
	if len(g.stops) != 2 {
		t.Fatalf("%d stops, want the two it inherited", len(g.stops))
	}
	if g.stops[0].offset != 0 || g.stops[1].offset != 1 {
		t.Errorf("stops at %v and %v, want them in order",
			g.stops[0].offset, g.stops[1].offset)
	}
	if g.stops[0].color != ([3]float32{0, 0, 1}) {
		t.Errorf("the first stop is %v, want blue", g.stops[0].color)
	}
	if c := g.at(0.5); !near(c[0], 0.5) || !near(c[2], 0.5) {
		t.Errorf("the middle of the ramp is %v, want half of each end", c)
	}
	if g, ok := r.server("url(#nothing)", raster.Rect{X1: 1, Y1: 1}, st); g != nil || ok {
		t.Error("a reference to nothing resolved")
	}

	b, _ := r.server("url(#back)", raster.Rect{X1: 10, Y1: 10}, st)
	if b == nil {
		t.Fatal("the second gradient did not resolve")
	}
	if got := []float32{b.stops[0].offset, b.stops[1].offset, b.stops[2].offset}; got[2] != 0.7 {
		t.Errorf("stops at %v, want the last pulled up to 0.7", got)
	}
	if b.stops[2].color != ([3]float32{0, 1, 0}) {
		t.Errorf("the last stop is %v, want it kept where it was written", b.stops[2].color)
	}
}

// TestTextLayout covers character advances, positions and text-anchor.
func TestTextLayout(t *testing.T) {
	run := func(markup string) []glyph {
		t.Helper()
		d, err := Load([]byte(markup))
		if err != nil {
			t.Fatal(err)
		}
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		return r.layout(c.chars)
	}

	g := run(`<svg><text x="10" y="20" font-size="16">abc</text></svg>`)
	if len(g) != 3 {
		t.Fatalf("%d glyphs, want 3", len(g))
	}
	if g[0].x != 10 || g[0].y != 20 {
		t.Errorf("the first glyph is at %v,%v, want 10,20", g[0].x, g[0].y)
	}
	for i := 1; i < 3; i++ {
		if g[i].x <= g[i-1].x {
			t.Errorf("glyph %d is at %v, not past %v", i, g[i].x, g[i-1].x)
		}
		if g[i].y != 20 {
			t.Errorf("glyph %d left the baseline, at %v", i, g[i].y)
		}
	}

	g = run(`<svg><text x="0 50 100" y="9" font-size="16">abc</text></svg>`)
	if len(g) != 3 || g[1].x != 50 || g[2].x != 100 {
		t.Errorf("a list of x placed them at %v", xs(g))
	}
	g = run(`<svg><text x="5" y="9" font-size="16">ab<tspan dx="20">c</tspan></text></svg>`)
	if len(g) != 3 {
		t.Fatalf("%d glyphs across a tspan, want 3", len(g))
	}
	if g[2].x < g[1].x+20 {
		t.Errorf("the tspan is at %v, want it 20 past %v", g[2].x, g[1].x)
	}

	pos := func(anchor string) float32 {
		gs := run(`<svg><text x="100" y="9" font-size="16" text-anchor="` + anchor + `">abc</text></svg>`)
		r := &runner{}
		r.anchorChunks(gs)
		return gs[0].x
	}
	start, middle, end := pos("start"), pos("middle"), pos("end")
	if start != 100 {
		t.Errorf("start anchored at %v, want 100", start)
	}
	if !(end < middle && middle < start) {
		t.Errorf("the anchors gave %v, %v, %v, want end left of middle left of start",
			end, middle, start)
	}
}

func xs(g []glyph) []float32 {
	out := make([]float32, len(g))
	for i := range g {
		out[i] = g[i].x
	}
	return out
}

// TestDecode covers Decode and DecodeConfig.
func TestDecode(t *testing.T) {
	const src = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20">` +
		`<rect width="40" height="20" fill="lime"/></svg>`
	cfg, err := DecodeConfig(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 40 || cfg.Height != 20 {
		t.Errorf("config is %dx%d, want 40x20", cfg.Width, cfg.Height)
	}
	img, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 20 {
		t.Fatalf("the image is %v, want 40x20", b)
	}
	r, g, b, _ := img.At(20, 10).RGBA()
	if r>>8 > 8 || g>>8 < 240 || b>>8 > 8 {
		t.Errorf("the middle is %d,%d,%d, want lime", r>>8, g>>8, b>>8)
	}
	if _, err := Decode(strings.NewReader("not svg at all")); err == nil {
		t.Error("a file that is not a drawing decoded")
	}
}

// TestTextSpacing covers letter and word spacing, white space and dominant-baseline.
func TestTextSpacing(t *testing.T) {
	adv := func(markup string) float32 {
		t.Helper()
		d, err := Load([]byte(markup))
		if err != nil {
			t.Fatal(err)
		}
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		g := r.layout(c.chars)
		if len(g) < 2 {
			t.Fatalf("%d glyphs", len(g))
		}
		return g[1].x - g[0].x
	}
	plain := adv(`<svg><text font-size="16">ab</text></svg>`)
	spaced := adv(`<svg><text font-size="16" letter-spacing="5">ab</text></svg>`)
	if !near(spaced-plain, 5) {
		t.Errorf("letter-spacing added %v, want 5", spaced-plain)
	}

	count := func(markup string) int {
		d, _ := Load([]byte(markup))
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		return len(r.layout(c.chars))
	}
	if n := count(`<svg><text font-size="16">a   b</text></svg>`); n != 3 {
		t.Errorf("collapsed to %d glyphs, want 3", n)
	}
	if n := count(`<svg><text font-size="16" xml:space="preserve">a   b</text></svg>`); n != 5 {
		t.Errorf("preserved gave %d glyphs, want 5", n)
	}

	base := func(v string) float32 {
		d, _ := Load([]byte(`<svg><text y="50" font-size="20" dominant-baseline="` + v + `">a</text></svg>`))
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		g := r.layout(c.chars)
		shiftBaseline(g)
		return g[0].y
	}
	if b := base("auto"); b != 50 {
		t.Errorf("the default baseline is at %v, want 50", b)
	}
	if b := base("middle"); !(b > 50 && b < 60) {
		t.Errorf("a middle baseline is at %v, want it a little below 50", b)
	}
}

// TestClipAndImage covers clip-path and an image carried in a data URL.
func TestClipAndImage(t *testing.T) {
	const red = "data:image/png;base64," +
		"iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAMAAADz0U65AAAAIGNIUk0AAHomAACAhAAA+gAA" +
		"AIDoAAB1MAAA6mAAADqYAAAXcJy6UTwAAAADUExURf8AABniCTcAAAAHdElNRQfqCBYEEQEd" +
		"3mqcAAAAJXRFWHRkYXRlOmNyZWF0ZQAyMDI2LTA4LTIyVDA0OjE3OjAxKzAwOjAwYcwxyAAA" +
		"ACV0RVh0ZGF0ZTptb2RpZnkAMjAyNi0wOC0yMlQwNDoxNzowMSswMDowMBCRiXQAAAAodEVY" +
		"dGRhdGU6dGltZXN0YW1wADIwMjYtMDgtMjJUMDQ6MTc6MDErMDA6MDBHhKirAAAADElEQVQI" +
		"12NgoA4AAABIAAH/myMhAAAAAElFTkSuQmCC"
	d, err := Load([]byte(`<svg width="20" height="10">` +
		`<clipPath id="c"><rect x="0" y="0" width="5" height="10"/></clipPath>` +
		`<rect width="10" height="10" fill="black" clip-path="url(#c)"/>` +
		`<image x="10" y="0" width="10" height="10" href="` + red + `"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) (uint8, uint8, uint8) {
		o := y*img.Stride + x*4
		return img.Pix[o], img.Pix[o+1], img.Pix[o+2]
	}
	if r, g, b := at(2, 5); r > 8 || g > 8 || b > 8 {
		t.Errorf("inside the clip is %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := at(7, 5); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the clip is %d,%d,%d, want the page", r, g, b)
	}
	if r, g, b := at(15, 5); r < 200 || g > 60 || b > 60 {
		t.Errorf("the picture is %d,%d,%d, want red", r, g, b)
	}
}

// TestStylesheet covers selectors, specificity and cascade order.
func TestStylesheet(t *testing.T) {
	d, err := Load([]byte(`<svg>
	  <style>
	    /* a comment */
	    rect { fill: red }
	    .a { fill: green }
	    #one { fill: blue }
	    g .b { fill: purple }
	    @media print { rect { fill: black } }
	  </style>
	  <rect id="plain"/>
	  <rect id="klass" class="a"/>
	  <rect id="one" class="a"/>
	  <rect id="attr" class="a" fill="orange"/>
	  <rect id="inline" class="a" style="fill: yellow"/>
	  <g><rect id="desc" class="b"/></g>
	  <rect id="outside" class="b"/>
	</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	fill := func(id string) string {
		r := &runner{doc: d}
		var found string
		var walk func(n *node)
		walk = func(n *node) {
			r.open = append(r.open, n)
			if n.attr["id"] == id {
				found = r.prop(n, "fill")
			}
			for _, k := range n.kids {
				walk(k)
			}
			r.open = r.open[:len(r.open)-1]
		}
		walk(d.root)
		return found
	}
	for _, tc := range []struct{ id, want string }{
		{"plain", "red"},
		{"klass", "green"},
		{"one", "blue"},
		{"attr", "green"},
		{"inline", "yellow"},
		{"desc", "purple"},
		{"outside", "red"},
	} {
		if got := fill(tc.id); got != tc.want {
			t.Errorf("%s is filled %q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestPattern covers tile repetition and pattern content inheritance.
func TestPattern(t *testing.T) {
	d, err := Load([]byte(`<svg width="40" height="20">
	  <pattern id="p" width="10" height="20" patternUnits="userSpaceOnUse">
	    <rect width="5" height="20" fill="black"/>
	  </pattern>
	  <rect width="40" height="20" fill="url(#p)" stroke="red" stroke-width="4"/>
	</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x int) (uint8, uint8, uint8) {
		o := 10*img.Stride + x*4
		return img.Pix[o], img.Pix[o+1], img.Pix[o+2]
	}
	for _, x := range []int{2, 12, 22, 32} {
		if r, g, b := at(x); r > 8 || g > 8 || b > 8 {
			t.Errorf("at %d the tile is %d,%d,%d, want black", x, r, g, b)
		}
	}
	for _, x := range []int{7, 17, 27, 37} {
		r, g, b := at(x)
		if r < 200 || g < 200 || b < 200 {
			t.Errorf("at %d the gap is %d,%d,%d, want the page", x, r, g, b)
		}
	}
}

// TestMaskAndMarker covers a mask and markers at the ends of a line.
func TestMaskAndMarker(t *testing.T) {
	render := func(markup string) func(x, y int) (uint8, uint8, uint8) {
		t.Helper()
		d, err := Load([]byte(markup))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		p, _ := d.Page(0)
		img, err := p.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		return func(x, y int) (uint8, uint8, uint8) {
			o := y*img.Stride + x*4
			return img.Pix[o], img.Pix[o+1], img.Pix[o+2]
		}
	}

	at := render(`<svg width="40" height="20">
	  <mask id="m"><rect width="20" height="20" fill="white"/></mask>
	  <rect width="40" height="20" fill="black" mask="url(#m)"/></svg>`)
	if r, g, b := at(10, 10); r > 8 || g > 8 || b > 8 {
		t.Errorf("inside the mask is %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := at(30, 10); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the mask is %d,%d,%d, want the page", r, g, b)
	}

	at = render(`<svg width="60" height="20">
	  <marker id="k" markerWidth="4" markerHeight="4" refX="2" refY="2" markerUnits="userSpaceOnUse">
	    <rect width="4" height="4" fill="black"/>
	  </marker>
	  <line x1="10" y1="10" x2="50" y2="10" stroke="none"
	        marker-start="url(#k)" marker-end="url(#k)"/></svg>`)
	for _, x := range []int{10, 50} {
		if r, g, b := at(x, 10); r > 8 || g > 8 || b > 8 {
			t.Errorf("the marker at %d is %d,%d,%d, want black", x, r, g, b)
		}
	}
	if r, g, b := at(30, 10); r < 248 || g < 248 || b < 248 {
		t.Errorf("between the markers is %d,%d,%d, want the page", r, g, b)
	}
}

// TestHugeDrawing checks that a drawing too large to allocate is refused.
func TestHugeDrawing(t *testing.T) {
	for _, size := range []string{"1e30", "1e38", "4294967296", "99999999999999999999"} {
		d, err := Load([]byte(`<svg width="` + size + `" height="` + size + `"><rect width="10" height="10"/></svg>`))
		if err != nil {
			continue
		}
		p, _ := d.Page(0)
		if _, err := p.ImageDPI(72); err == nil {
			t.Errorf("%s: a drawing of %s was rendered", size, size)
		}
	}
}

// TestGradientSingular checks a gradient whose transform has no inverse.
func TestGradientSingular(t *testing.T) {
	for _, kind := range []string{"linearGradient", "radialGradient"} {
		d, err := Load([]byte(`<svg viewBox="0 0 200 200">
		  <` + kind + ` id="g" gradientTransform="matrix(0 0 0 0 0 0)">
		    <stop offset="0" stop-color="white"/><stop offset="1" stop-color="green"/>
		  </` + kind + `>
		  <rect x="20" y="20" width="160" height="160" fill="url(#g)"/></svg>`))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := d.Page(0)
		if _, err := p.ImageDPI(72); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// TestGradientSpread covers spreadMethod past the ends of a ramp.
func TestGradientSpread(t *testing.T) {
	build := func(mode string) *gradient {
		t.Helper()
		d, err := Load([]byte(`<svg width="100" height="10">
		  <linearGradient id="g" x1="0" x2="0.25" spreadMethod="` + mode + `">
		    <stop offset="0" stop-color="black"/><stop offset="1" stop-color="white"/>
		  </linearGradient>
		  <rect width="100" height="10" fill="url(#g)"/></svg>`))
		if err != nil {
			t.Fatal(err)
		}
		r := &runner{doc: d}
		g, _ := r.server("url(#g)", raster.Rect{X1: 100, Y1: 10}, initialState(100, 10))
		if g == nil {
			t.Fatal("the gradient did not resolve")
		}
		return g
	}

	if g := build("pad"); g.reps != 0 {
		t.Errorf("pad wrote %d periods, want the one the ramp is", g.reps)
	}
	for _, mode := range []string{"repeat", "reflect"} {
		g := build(mode)
		if g.reps != 4 {
			t.Errorf("%s wrote %d periods, want 4", mode, g.reps)
		}
		if !near(g.c1.X-g.c0.X, 1) {
			t.Errorf("%s left the axis %v long, want the whole box", mode, g.c1.X-g.c0.X)
		}
	}
	rep, ref := build("repeat"), build("reflect")
	if !near(rep.ramp(0.3), rep.ramp(0.05)) {
		t.Errorf("repeat gave %v and %v for the same point of two periods",
			rep.ramp(0.3), rep.ramp(0.05))
	}
	if !near(ref.ramp(0.3), 1-ref.ramp(0.05)) {
		t.Errorf("reflect gave %v, want the mirror of %v", ref.ramp(0.3), ref.ramp(0.05))
	}
}

// TestRootProperties covers fill and stroke inherited from the root element.
func TestRootProperties(t *testing.T) {
	d, err := Load([]byte(`<svg width="20" height="20" fill="none" stroke="black" stroke-width="2">` +
		`<rect x="5" y="5" width="10" height="10"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) uint8 { return img.Pix[y*img.Stride+x*4] }
	if v := at(10, 5); v > 8 {
		t.Errorf("the edge is %d, want the stroke the root asked for", v)
	}
	if v := at(10, 10); v < 248 {
		t.Errorf("the middle is %d, want fill none to have been inherited", v)
	}
}

// TestImageSize covers rendering at a size the caller asks for.
func TestImageSize(t *testing.T) {
	d, err := Load([]byte(`<svg width="40" height="20"><rect width="40" height="20" fill="black"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageSize(80, 80)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 40 {
		t.Errorf("the image is %v, want 80x40", b)
	}
	img, err = p.ImageSize(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("the image is %v, want 20x10", b)
	}
	if _, err := p.ImageSize(0, 0); err == nil {
		t.Error("no size at all rendered")
	}
}

// TestRenderTo covers compositing a drawing onto an existing image.
func TestRenderTo(t *testing.T) {
	d, err := Load([]byte(`<svg width="10" height="10"><rect width="10" height="10" fill="black"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	dst := image.NewRGBA(image.Rect(0, 0, 20, 10))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	if err := p.RenderTo(dst, raster.Translate(10, 0), &Options{Alpha: true}); err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := dst.At(15, 5).RGBA(); r>>8 > 8 || b>>8 > 8 {
		t.Errorf("the drawing landed as %v", dst.At(15, 5))
	}
	if _, _, b, _ := dst.At(5, 5).RGBA(); b>>8 < 200 {
		t.Errorf("what was already there became %v", dst.At(5, 5))
	}
}

// TestPictureAliases checks that one picture named three ways is read once.
func TestPictureAliases(t *testing.T) {
	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	reads := 0
	load := func(name string) ([]byte, error) {
		reads++
		return pic.Bytes(), nil
	}
	d, err := LoadWith([]byte(`<svg width="30" height="10">`+
		`<image href="a.png" width="10" height="10"/>`+
		`<image href="./a.png" x="10" width="10" height="10"/>`+
		`<image href=".//a.png" x="20" width="10" height="10"/></svg>`), &LoadOptions{Open: load})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := d.Page(0)
	if _, err := p.ImageDPI(72); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("a.png was read %d times", reads)
	}
}

// TestLoadWith covers a loader for named files and a box for percentage sizes.
func TestLoadWith(t *testing.T) {
	files := map[string][]byte{
		"sheet.css": []byte(`.big { font-size: 40px; } @font-face { font-family: Carried; src: url(face.ttf); }`),
	}
	opened := map[string]int{}
	load := func(name string) ([]byte, error) {
		opened[name]++
		b, ok := files[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return b, nil
	}

	const markup = `<?xml version="1.0"?>` +
		`<?xml-stylesheet href="sheet.css" type="text/css"?>` +
		`<svg width="50%" height="25%"><text class="big">a</text></svg>`
	d, err := LoadWith([]byte(markup), &LoadOptions{Open: load, Width: 400, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	if b := (&Page{doc: d}).Bounds(); b.X1 != 200 || b.Y1 != 200 {
		t.Errorf("bounds %v, want 200x200", b)
	}
	if opened["sheet.css"] != 1 {
		t.Errorf("the sheet was read %d times, want 1", opened["sheet.css"])
	}
	if len(d.rules) != 1 {
		t.Fatalf("%d rules, want 1", len(d.rules))
	}
	if len(d.faces) != 1 || d.faces[0].family != "Carried" {
		t.Fatalf("faces %+v, want one for Carried", d.faces)
	}
	if d.embedded("Carried", false, false) != nil {
		t.Error("a face read from nothing came back")
	}
	d.embedded("Carried", false, false)
	if opened["face.ttf"] != 1 {
		t.Errorf("the face was read %d times, want 1", opened["face.ttf"])
	}

	plain, err := Load([]byte(markup))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.rules) != 0 || len(plain.faces) != 0 {
		t.Error("a drawing with no loader read a sheet")
	}
}

// TestTspanPositions covers per character positions and the space between tspans.
func TestTspanPositions(t *testing.T) {
	const markup = `<svg><text font-size="10">
		<tspan x="0,10,20">abc</tspan>
		<tspan x="100,110">de</tspan>
	</text></svg>`
	d, err := Load([]byte(markup))
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{doc: d}
	c := &textCursor{}
	r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)

	g := r.laidOut(c)
	var got []string
	for _, v := range g {
		got = append(got, string(v.r))
	}
	if s := strings.Join(got, ""); s != "abc de" {
		t.Errorf("placed %q, want %q", s, "abc de")
	}
	for _, c := range []struct {
		i    int
		want float32
	}{{0, 0}, {1, 10}, {2, 20}, {4, 100}, {5, 110}} {
		if x := g[c.i].x; !near(x, c.want) {
			t.Errorf("glyph %d %q at %v, want %v", c.i, g[c.i].r, x, c.want)
		}
	}
	if x := g[3].x; x <= 20 || x >= 100 {
		t.Errorf("the space is at %v, want it after the c at 20 and before the next tspan at 100", x)
	}
}

// TestPageText covers reading a drawing back as text.
func TestPageText(t *testing.T) {
	d, err := Load([]byte(`<svg width="200" height="60"><text x="10" y="30" font-size="20">Hello</text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Text()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "Hello") {
		t.Errorf("the drawing reads %q, want Hello in it", s)
	}
}

// TestFilter covers filter regions, primitives, color spaces and chaining.
func TestFilter(t *testing.T) {
	pixels := func(src string) func(x, y int) (uint8, uint8, uint8, uint8) {
		t.Helper()
		d, err := Load([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		p, _ := d.Page(0)
		img, err := p.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			o := y*img.Stride + x*4
			return img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]
		}
	}

	at := pixels(`<svg width="40" height="40">` +
		`<filter id="f"><feFlood flood-color="#00ff00"/></filter>` +
		`<rect x="10" y="10" width="20" height="20" fill="red" filter="url(#f)"/></svg>`)
	if r, g, b, _ := at(20, 20); r > 8 || g < 240 || b > 8 {
		t.Errorf("the flood is %d,%d,%d, want green", r, g, b)
	}
	if r, g, b, _ := at(9, 20); r > 8 || g < 240 || b > 8 {
		t.Errorf("two units outside the box is %d,%d,%d, want the region", r, g, b)
	}
	if r, g, b, _ := at(5, 20); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the region is %d,%d,%d, want the page", r, g, b)
	}

	at = pixels(`<svg width="40" height="40">` +
		`<filter id="f" filterUnits="userSpaceOnUse" x="0" y="0" width="40" height="40">` +
		`<feFlood flood-color="#00ff00" x="0" y="0" width="20" height="40"/></filter>` +
		`<rect x="10" y="10" width="20" height="20" fill="red" filter="url(#f)"/></svg>`)
	if r, g, b, _ := at(5, 20); r > 8 || g < 240 || b > 8 {
		t.Errorf("inside the subregion is %d,%d,%d, want green", r, g, b)
	}
	if r, g, b, _ := at(30, 20); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the subregion is %d,%d,%d, want the page", r, g, b)
	}

	at = pixels(`<svg width="40" height="40">` +
		`<filter id="f" filterUnits="userSpaceOnUse" x="0" y="0" width="40" height="40">` +
		`<feOffset dx="10" dy="0"/></filter>` +
		`<rect x="5" y="5" width="10" height="30" fill="black" filter="url(#f)"/></svg>`)
	if r, _, _, _ := at(20, 20); r > 8 {
		t.Errorf("where the offset put it is %d, want black", r)
	}
	if r, _, _, _ := at(8, 20); r < 248 {
		t.Errorf("where it came from is %d, want the page", r)
	}

	const half = `<svg width="20" height="20"><filter id="f" %s>` +
		`<feColorMatrix values="0.5 0 0 0 0  0 0.5 0 0 0  0 0 0.5 0 0  0 0 0 1 0"/></filter>` +
		`<rect width="20" height="20" fill="white" filter="url(#f)"/></svg>`
	lin, _, _, _ := pixels(fmt.Sprintf(half, ""))(10, 10)
	srgb, _, _, _ := pixels(fmt.Sprintf(half, `color-interpolation-filters="sRGB"`))(10, 10)
	if srgb != 128 {
		t.Errorf("half of white in sRGB is %d, want 128", srgb)
	}
	if lin <= srgb+20 {
		t.Errorf("half of white in linear light is %d, want well above %d", lin, srgb)
	}

	at = pixels(`<svg width="60" height="60">` +
		`<filter id="f" filterUnits="userSpaceOnUse" x="0" y="0" width="60" height="60">` +
		`<feFlood flood-color="black" x="20" y="20" width="20" height="20" result="a"/>` +
		`<feGaussianBlur in="a" stdDeviation="3"/></filter>` +
		`<rect width="1" height="1" fill="red" filter="url(#f)"/></svg>`)
	mid, _, _, _ := at(30, 30)
	edge, _, _, _ := at(20, 30)
	out, _, _, _ := at(14, 30)
	if mid > 8 {
		t.Errorf("the middle of the blur is %d, want black", mid)
	}
	if edge < 60 || edge > 200 {
		t.Errorf("the edge of the blur is %d, want it part way", edge)
	}
	if out < 240 {
		t.Errorf("well outside the blur is %d, want the page", out)
	}

	at = pixels(`<svg width="20" height="20">` +
		`<rect width="20" height="20" fill="red" filter="url(#missing)"/></svg>`)
	if r, g, b, _ := at(10, 10); r < 248 || g < 248 || b < 248 {
		t.Errorf("an element with an unresolved filter is %d,%d,%d, want the page", r, g, b)
	}

	at = pixels(`<svg width="20" height="20">` +
		`<rect width="20" height="20" fill="#ff0000" filter="grayscale(1)"/></svg>`)
	r, g, b, _ := at(10, 10)
	if r != g || g != b || r == 0 || r == 255 {
		t.Errorf("grayscale(1) of red is %d,%d,%d, want one grey", r, g, b)
	}

	at = pixels(`<svg width="20" height="20" filter="url(#f)">` +
		`<filter id="f"><feFlood flood-color="#00ff00"/></filter>` +
		`<rect width="20" height="20" fill="red"/></svg>`)
	if r, g, b, _ := at(10, 10); r > 8 || g < 240 || b > 8 {
		t.Errorf("a filter on the root drew %d,%d,%d, want green", r, g, b)
	}
}

// TestViewportAndConditions covers nested svg overflow, switch and relative font size.
func TestViewportAndConditions(t *testing.T) {
	at := func(src string) func(x, y int) (uint8, uint8, uint8) {
		t.Helper()
		d, err := Load([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		p, _ := d.Page(0)
		img, err := p.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		return func(x, y int) (uint8, uint8, uint8) {
			o := y*img.Stride + x*4
			return img.Pix[o], img.Pix[o+1], img.Pix[o+2]
		}
	}

	px := at(`<svg width="40" height="40">` +
		`<svg x="10" y="10" width="10" height="10">` +
		`<rect width="40" height="40" fill="black"/></svg></svg>`)
	if r, _, _ := px(15, 15); r > 8 {
		t.Errorf("inside the nested viewport is %d, want black", r)
	}
	if r, _, _ := px(25, 15); r < 248 {
		t.Errorf("past the nested viewport is %d, want the page", r)
	}

	px = at(`<svg width="40" height="40">` +
		`<svg x="10" y="10" width="10" height="10" overflow="visible">` +
		`<rect width="40" height="40" fill="black"/></svg></svg>`)
	if r, _, _ := px(25, 15); r > 8 {
		t.Errorf("past a visible viewport is %d, want black", r)
	}

	px = at(`<svg width="40" height="40"><switch>` +
		`<rect width="40" height="40" fill="#ff0000" requiredExtensions="urn:x"/>` +
		`<rect width="40" height="40" fill="#00ff00"/>` +
		`<rect width="40" height="40" fill="#ff0000"/></switch></svg>`)
	if r, g, b := px(20, 20); r > 8 || g < 240 || b > 8 {
		t.Errorf("the switch drew %d,%d,%d, want the second child", r, g, b)
	}

	d, err := Load([]byte(`<svg width="100" height="40">` +
		`<g font-size="10"><text id="t" font-size="200%">M</text></g></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := &runner{doc: d}
	c := &textCursor{}
	st := r.style(d.root.kids[0], initialState(100, 40))
	st = r.style(d.root.kids[0].kids[0], st)
	r.textRun(d.root.kids[0].kids[0], st, c, 0, true)
	if st.em != 20 {
		t.Errorf("the font size is %v, want twice the ten it inherited", st.em)
	}
	if g := r.layout(c.chars); len(g) != 1 || g[0].st.em != 20 {
		t.Errorf("the glyph was placed at %v", g)
	}
}

// TestTextPath covers characters laid along a path.
func TestTextPath(t *testing.T) {
	d, err := Load([]byte(`<svg width="60" height="60" font-size="10">` +
		`<path id="p" d="M 0 50 L 50 50"/>` +
		`<path id="q" d="M 50 0 L 50 50"/>` +
		`<text><textPath href="#p">ab</textPath></text>` +
		`<text><textPath href="#q">cd</textPath></text>` +
		`<text><textPath href="#missing">ef</textPath></text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := &runner{doc: d}
	st := initialState(60, 60)
	var flat, down []glyph
	for i, want := range []*[]glyph{&flat, &down, nil} {
		c := &textCursor{}
		r.textRun(d.root.kids[i+2], st, c, 0, false)
		g := onPath(r.layout(c.chars))
		if want == nil {
			if len(g) != 0 {
				t.Errorf("a textPath naming nothing placed %d glyphs", len(g))
			}
			continue
		}
		*want = g
	}
	if len(flat) != 2 || len(down) != 2 {
		t.Fatalf("placed %d and %d glyphs, want two each", len(flat), len(down))
	}
	if flat[0].y != 50 || flat[0].turn != 0 || flat[1].x <= flat[0].x {
		t.Errorf("along a horizontal path the glyphs are %v", flat)
	}
	if got := down[0].turn; got < 89 || got > 91 {
		t.Errorf("down a vertical path the turn is %v, want a quarter", got)
	}
	if down[1].y <= down[0].y || down[0].x < 49 || down[0].x > 51 {
		t.Errorf("down a vertical path the glyphs are %v", down)
	}
}

// TestVerticalText covers upright and rotated characters in vertical text.
func TestVerticalText(t *testing.T) {
	d, err := Load([]byte(`<svg width="200" height="200" font-size="20">` +
		`<text x="100" y="30" writing-mode="tb">A日</text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := &runner{doc: d}
	st := r.style(d.root, initialState(200, 200))
	c := &textCursor{}
	r.textRun(d.root.kids[0], st, c, 0, false)
	g := r.layout(c.chars)
	if len(g) != 2 {
		t.Fatalf("placed %d glyphs, want two", len(g))
	}
	latin, cjk := g[0], g[1]
	if !latin.vert || latin.turn != 90 {
		t.Errorf("the latin character is turned %v, want a quarter", latin.turn)
	}
	if cjk.turn != 0 {
		t.Errorf("the ideograph is turned %v, want upright", cjk.turn)
	}
	if cjk.y <= latin.y {
		t.Errorf("the second character is at %v, want it below the first", cjk.y)
	}
	if cjk.adv != 20 {
		t.Errorf("an upright character advances %v, want the em", cjk.adv)
	}
	for _, v := range g {
		if v.x < 100-v.size || v.x > 100+v.size {
			t.Errorf("%q sits at %v, want it about the line at 100", v.r, v.x)
		}
	}
}

// TestBidiText covers bidirectional ordering, direction and unicode-bidi.
func TestBidiText(t *testing.T) {
	const heb = "\u05e9\u05dc\u05d5\u05dd"
	drawn := func(markup string) string {
		t.Helper()
		d, err := Load([]byte(markup))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		out := []rune{}
		for _, g := range r.layout(c.chars) {
			out = append(out, g.r)
		}
		return string(out)
	}

	if got, want := drawn(`<svg><text>ab`+heb+`</text></svg>`), "ab\u05dd\u05d5\u05dc\u05e9"; got != want {
		t.Errorf("drew %q, want the hebrew backwards", got)
	}
	if got, want := drawn(`<svg><text direction="rtl">`+heb+` 12</text></svg>`),
		"12 \u05dd\u05d5\u05dc\u05e9"; got != want {
		t.Errorf("drew %q, want the run right to left", got)
	}
	if got, want := drawn(`<svg><text direction="rtl" unicode-bidi="bidi-override">abc</text></svg>`),
		"cba"; got != want {
		t.Errorf("drew %q, want it overridden", got)
	}

	d, err := Load([]byte(`<svg><text x="10" y="20 30 40">` + heb + `</text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := &runner{doc: d}
	c := &textCursor{}
	r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
	g := r.layout(c.chars)
	if len(g) != 4 {
		t.Fatalf("placed %d glyphs, want four", len(g))
	}
	for i, want := range []float32{20, 30, 40, 40} {
		if g[i].y != want {
			t.Errorf("glyph %d sits at %v, want %v", i, g[i].y, want)
		}
	}
}

// TestNewStream covers reading a drawing from a stream and from a reader.
func TestNewStream(t *testing.T) {
	const src = `<svg width="40" height="20" xmlns="http://www.w3.org/2000/svg">` +
		`<rect width="40" height="20" fill="#00ff00"/></svg>`
	d, err := NewStream(iotest.OneByteReader(strings.NewReader(src)))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if b := d.Page0().Bounds(); b.X1 != 40 || b.Y1 != 20 {
		t.Errorf("the drawing is %v", b)
	}
	r, err := NewReader(strings.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if b := r.Page0().Bounds(); b.X1 != 40 {
		t.Errorf("the drawing read from a reader is %v", b)
	}
	if _, err := NewStream(strings.NewReader("")); err == nil {
		t.Error("an empty stream opened")
	}
}

// TestDrawingLinks covers the bounds and targets of a drawing's anchors.
func TestDrawingLinks(t *testing.T) {
	d, err := Load([]byte(`<svg viewBox="0 0 100 100" width="200" height="200"` +
		` xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">` +
		`<a href="https://example.com"><rect x="10" y="10" width="20" height="20"/></a>` +
		`<g transform="translate(50 0)">` +
		`<a xlink:href="#target"><rect x="0" y="40" width="10" height="10"/></a></g>` +
		`<rect id="target" x="0" y="90" width="5" height="5"/>` +
		`</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	links := p.Links()
	if len(links) != 2 {
		t.Fatalf("%d links, want two: %+v", len(links), links)
	}
	if l := links[0]; l.URI != "https://example.com" ||
		!near(l.Rect.X0, 20) || !near(l.Rect.Y0, 20) ||
		!near(l.Rect.X1, 60) || !near(l.Rect.Y1, 60) {
		t.Errorf("the outward link is %+v", l)
	}
	if l := links[1]; l.URI != "" || l.Fragment != "target" ||
		!near(l.Rect.X0, 100) || !near(l.Rect.Y0, 80) {
		t.Errorf("the inward link is %+v", l)
	}
}

// TestDrawingHTML covers the drawing's text as HTML.
func TestDrawingHTML(t *testing.T) {
	d, err := Load([]byte(`<svg width="200" height="100" xmlns="http://www.w3.org/2000/svg">` +
		`<title>A Drawing</title><text x="10" y="50" font-size="20" fill="#0000ff">Hi &amp; bye</text>` +
		`</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	out, err := p.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "<title>A Drawing</title>",
		`<div id="drawing"`, "position:absolute", "Hi &amp; bye", "color:#0000ff"} {
		if !strings.Contains(out, want) {
			t.Errorf("the HTML has no %q: %s", want, out)
		}
	}
	svg, err := p.SVG()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "</svg>") {
		t.Errorf("the SVG is %.60q", svg)
	}
}

// TestTransformOrigin covers transform-origin keywords and lengths.
func TestTransformOrigin(t *testing.T) {
	render := func(origin, turn string) *image.RGBA {
		d, err := Load([]byte(`<svg viewBox="0 0 200 200" width="200" height="200">` +
			`<rect x="80" y="80" width="40" height="40" fill="#00ff00"` +
			` transform="` + turn + `" transform-origin="` + origin + `"/></svg>`))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		pg, _ := d.Page(0)
		img, err := pg.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	differs := func(a, b *image.RGBA) int {
		n := 0
		for i := range a.Pix {
			if v := int(a.Pix[i]) - int(b.Pix[i]); v > 2 || v < -2 {
				n++
			}
		}
		return n
	}
	for _, c := range []struct{ origin, same string }{
		{"center", "translate(100 100) rotate(45) translate(-100 -100)"},
		{"50% 50%", "translate(100 100) rotate(45) translate(-100 -100)"},
		{"100 100", "translate(100 100) rotate(45) translate(-100 -100)"},
		{"right bottom", "translate(200 200) rotate(45) translate(-200 -200)"},
		{"top", "translate(100 0) rotate(45) translate(-100 0)"},
		{"25% top", "translate(50 0) rotate(45) translate(-50 0)"},
	} {
		if n := differs(render(c.origin, "rotate(45)"), render("", c.same)); n > 64 {
			t.Errorf("transform-origin %q turned about the wrong point, %d bytes differ", c.origin, n)
		}
	}

	img := render("top 3em bottom left", "rotate(45)")
	o := 100*img.Stride + 100*4
	if img.Pix[o] < 248 {
		t.Error("an unreadable transform-origin was not ignored")
	}
}

// TestContextPaint covers context-fill and context-stroke.
func TestContextPaint(t *testing.T) {
	d, err := Load([]byte(`<svg width="30" height="30">` +
		`<path id="p" fill="context-stroke" d="M 0 0 h 30 v 30 h -30 Z"/>` +
		`<use href="#p" fill="#ff0000" stroke="#00ff00"/>` +
		`</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	o := 15*img.Stride + 15*4
	if r, g, b := img.Pix[o], img.Pix[o+1], img.Pix[o+2]; r > 8 || g < 240 || b > 8 {
		t.Errorf("context-stroke drew %d,%d,%d, want the use's stroke", r, g, b)
	}

	d2, err := Load([]byte(`<svg width="30" height="30">` +
		`<path fill="context-fill" d="M 0 0 h 30 v 30 h -30 Z"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	p2, _ := d2.Page(0)
	img2, err := p2.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	if v := img2.Pix[o]; v < 248 {
		t.Errorf("context-fill with no context drew %d, want the page", v)
	}
}

// TestTextPieces checks text split into a million pieces by comments.
func TestTextPieces(t *testing.T) {
	src := []byte(`<svg><text>` + strings.Repeat(`x<!---->`, 1<<20) + `</text></svg>`)
	done := make(chan error, 1)
	go func() {
		_, err := Load(src)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a text of a million pieces did not parse")
	}
}

// TestEntityBomb checks an entity referenced far more often than it is long.
func TestEntityBomb(t *testing.T) {
	value := strings.Repeat("v", 1<<16)
	refs := strings.Repeat("&a;", 1<<12)
	big := []byte(`<!DOCTYPE svg [<!ENTITY a "` + value + `">]><svg><text>` + refs + `</text></svg>`)
	if got := expandEntities(big); len(got) > maxExpanded {
		t.Errorf("4096 references to a 64 KB entity expanded to %d bytes", len(got))
	}
}

// TestDocumentRules covers foreign namespaces, internal entities and length positions.
func TestDocumentRules(t *testing.T) {
	d, err := Load([]byte(`<!DOCTYPE svg [<!ENTITY Green "<rect id='r' width='10' height='10' fill='#00ff00'/>">]>` +
		`<svg width="20" height="20" xmlns="http://www.w3.org/2000/svg"` +
		` xmlns:s="http://www.w3.org/2000/svg" xmlns:x="http://example.org/x">` +
		`<g>&Green;</g>` +
		`<x:g><rect width="20" height="20" fill="#ff0000"/></x:g>` +
		`<s:rect x="10" y="10" width="10" height="10" fill="#0000ff"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) (uint8, uint8, uint8) {
		o := y*img.Stride + x*4
		return img.Pix[o], img.Pix[o+1], img.Pix[o+2]
	}
	if r, g, b := at(5, 5); r > 8 || g < 240 || b > 8 {
		t.Errorf("the entity drew %d,%d,%d, want the green rect it stands for", r, g, b)
	}
	if r, g, b := at(15, 15); r > 8 || g > 8 || b < 240 {
		t.Errorf("the prefixed svg element drew %d,%d,%d, want blue", r, g, b)
	}
	if r, g, b := at(5, 15); r < 248 || g < 248 || b < 248 {
		t.Errorf("an element of another vocabulary drew %d,%d,%d", r, g, b)
	}

	if v := lengths("10% 20", 200, 16); len(v) != 2 || v[0] != 20 || v[1] != 20 {
		t.Errorf("lengths read %v, want a percentage of the viewport and a number", v)
	}
}

// TestBaselineShift covers baseline-shift on text and nested tspans.
func TestBaselineShift(t *testing.T) {
	d, err := Load([]byte(`<svg width="200" height="60" font-size="10">` +
		`<text x="10" y="40"><tspan baseline-shift="20%"><tspan baseline-shift="20%">a</tspan></tspan></text>` +
		`<text x="10" y="40" baseline-shift="20%"><tspan>b</tspan></text>` +
		`<text x="10" y="40" baseline-shift="20%">c</text></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := &runner{doc: d}
	base := r.style(d.root, initialState(200, 60))
	ys := make([]float32, 0, 3)
	for _, n := range d.root.kids {
		c := &textCursor{}
		r.textRun(n, r.style(n, base), c, 0, true)
		g := r.layout(c.chars)
		if len(g) != 1 {
			t.Fatalf("placed %d glyphs, want one", len(g))
		}
		ys = append(ys, g[0].y)
	}
	if ys[0] != 40-4 {
		t.Errorf("two nested shifts put the character at %v, want 36", ys[0])
	}
	if ys[1] != 40 {
		t.Errorf("a text's shift reached its tspan, putting it at %v", ys[1])
	}
	if ys[2] != 40-2 {
		t.Errorf("a text's shift missed its own character, putting it at %v", ys[2])
	}
}

// TestFilterUnderRotation checks that a filter runs in user space, not device space.
func TestFilterUnderRotation(t *testing.T) {
	d, err := Load([]byte(`<svg width="50" height="50">` +
		`<filter id="f" filterUnits="userSpaceOnUse" x="0" y="0" width="50" height="50">` +
		`<feOffset dx="10" dy="0"/></filter>` +
		`<rect x="15" y="15" width="10" height="10" fill="black"` +
		` transform="rotate(90 20 20)" filter="url(#f)"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	dark := func(x, y int) bool { return img.Pix[y*img.Stride+x*4] < 128 }
	if !dark(20, 30) {
		t.Error("the offset did not follow the element's own x axis")
	}
	if dark(30, 20) {
		t.Error("the offset followed the device x axis")
	}
	if dark(20, 20) {
		t.Error("the element is still where it was drawn")
	}
}

func TestMetadata(t *testing.T) {
	d, err := Load([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">` +
		`<title>A drawing</title><desc>What it shows</desc><rect width="10" height="10"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	m := d.Metadata()
	if m.Title != "A drawing" {
		t.Errorf("title %q", m.Title)
	}
	if m.Description != "What it shows" {
		t.Errorf("description %q", m.Description)
	}
	if m := (&Document{}).Metadata(); m.Title != "" || m.Description != "" {
		t.Errorf("an empty drawing says %+v", m)
	}
}

// slowFuzz fails a fuzz input that runs past five seconds.
func slowFuzz(t *testing.T) func() {
	start := time.Now()
	return func() {
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("took %v", d)
		}
	}
}
