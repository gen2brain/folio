package svg

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gen2brain/folio/raster"
)

// walker records a path as the commands it is made of, so that a test can say
// what it expects without depending on how the path stores it.
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
		// A moveto with more than one pair keeps going as a lineto, and a
		// relative one as a relative lineto.
		{"implicit lineto", "M10 20 30 40", []string{"M 10 20", "L 30 40"}},
		{"implicit relative", "m10 20 20 20", []string{"M 10 20", "L 30 40"}},
		{"horizontal", "M10 20H30V40", []string{"M 10 20", "L 30 20", "L 30 40"}},
		{"no separator", "M.5.5L1.5.5", []string{"M 0.5 0.5", "L 1.5 0.5"}},
		{"exponent", "M1e1 2e1", []string{"M 10 20"}},
		{"sign as separator", "M10 20L-5-6", []string{"M 10 20", "L -5 -6"}},
		{"quadratic", "M0 0 Q10 0 10 10", []string{"M 0 0", "C 6.67 0 10 3.33 10 10"}},
		// A smooth curve with nothing to reflect uses the current point.
		{"smooth first", "M0 0 S10 0 10 10", []string{"M 0 0", "C 0 0 10 0 10 10"}},
		{"close then move", "M0 0 L1 0 Z L2 0", []string{"M 0 0", "L 1 0", "Z", "L 2 0"}},
		// Two subpaths, the second starting where the first closed.
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

// TestPathArc checks the endpoint form of an arc against what it must reach,
// which is the only thing about it that is not a matter of how it is cut up.
func TestPathArc(t *testing.T) {
	for _, d := range []string{
		"M0 0 A50 50 0 0 1 100 0",
		"M0 0 A50 50 0 1 0 100 0",
		"M0 0 A50 25 30 1 1 100 40",
		// Radii too small to reach are scaled up until they just do.
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
		// A rotation about a point leaves that point alone.
		{"rotate(90,5,5)", 5, 5, [2]float32{5, 5}},
		// The list applies left to right: the translate happens first.
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
		// A paint server this cannot draw falls back to the color after it.
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

// TestDocumentSize covers the three answers a file can give about how big it
// is, which no two readers agree on and this one follows MuPDF for.
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

// FuzzSVG holds the parser to the rule the rest of the module keeps: nothing
// panics on a file, however damaged, and a drawing that opens also draws.
func FuzzSVG(fu *testing.F) {
	fu.Add([]byte(`<svg width="20" height="20"><rect width="10" height="10"/></svg>`))
	fu.Add([]byte(`<svg viewBox="0 0 8 8"><path d="M0 0A4 4 0 1 1 8 8Z" fill="#abc"/></svg>`))
	fu.Add([]byte(`<svg><defs><g id="a"><circle r="3"/></g></defs><use href="#a" x="4"/></svg>`))
	fu.Add([]byte(`<svg><g transform="rotate(30 1 2) skewX(10)" opacity=".5"><line x2="9"/></g></svg>`))
	fu.Fuzz(func(t *testing.T, b []byte) {
		d, err := Load(b)
		if err != nil {
			return
		}
		defer d.Close()
		p, err := d.Page(0)
		if err != nil {
			return
		}
		// A drawing the size of a page is the most a fuzz case may ask for.
		if bb := p.Bounds(); bb.X1 > 4096 || bb.Y1 > 4096 {
			return
		}
		p.ImageDPI(24)
	})
}

// TestGradientStops covers the ramp: an offset that steps backwards is pulled
// up to the one before it rather than sorted into place, and a gradient with
// no stops of its own takes the ones it was written as a variation of.
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
	// Halfway along a blue to red ramp is half of each.
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

// TestTextLayout covers where the characters of a text element land: one
// after another by their advances, and moved as a whole by text-anchor.
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
		return c.glyphs
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

	// A list of positions places each character, and a tspan carries on from
	// where the text left off.
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

	// The three anchors put the same run in three places.
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

// TestDecode covers the two functions a caller that wants a picture rather
// than a document uses, which are what image.RegisterFormat takes.
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

// TestTextSpacing covers what a text element may put between its characters
// and where dominant-baseline moves them.
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
		if len(c.glyphs) < 2 {
			t.Fatalf("%d glyphs", len(c.glyphs))
		}
		return c.glyphs[1].x - c.glyphs[0].x
	}
	plain := adv(`<svg><text font-size="16">ab</text></svg>`)
	spaced := adv(`<svg><text font-size="16" letter-spacing="5">ab</text></svg>`)
	if !near(spaced-plain, 5) {
		t.Errorf("letter-spacing added %v, want 5", spaced-plain)
	}

	// Preserved white space keeps the run of spaces a collapsing one drops.
	count := func(markup string) int {
		d, _ := Load([]byte(markup))
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		return len(c.glyphs)
	}
	if n := count(`<svg><text font-size="16">a   b</text></svg>`); n != 3 {
		t.Errorf("collapsed to %d glyphs, want 3", n)
	}
	if n := count(`<svg><text font-size="16" xml:space="preserve">a   b</text></svg>`); n != 5 {
		t.Errorf("preserved gave %d glyphs, want 5", n)
	}

	// A middle baseline drops the text by half its x height.
	base := func(v string) float32 {
		d, _ := Load([]byte(`<svg><text y="50" font-size="20" dominant-baseline="` + v + `">a</text></svg>`))
		r := &runner{doc: d}
		c := &textCursor{}
		r.textRun(d.root.kids[0], initialState(300, 300), c, 0, false)
		shiftBaseline(c.glyphs)
		return c.glyphs[0].y
	}
	if b := base("auto"); b != 50 {
		t.Errorf("the default baseline is at %v, want 50", b)
	}
	if b := base("middle"); !(b > 50 && b < 60) {
		t.Errorf("a middle baseline is at %v, want it a little below 50", b)
	}
}

// TestClipAndImage covers the two things a drawing puts around and inside a
// shape: what clip-path leaves of it, and a picture carried in the file.
func TestClipAndImage(t *testing.T) {
	// An eight by eight red PNG, so that the test needs nothing on disk and
	// the middle of it is the color without any of the edge in it.
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
	// Inside the clip the rect is drawn and outside it is not.
	if r, g, b := at(2, 5); r > 8 || g > 8 || b > 8 {
		t.Errorf("inside the clip is %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := at(7, 5); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the clip is %d,%d,%d, want the page", r, g, b)
	}
	// The picture the data URL carried fills its box.
	if r, g, b := at(15, 5); r < 200 || g > 60 || b > 60 {
		t.Errorf("the picture is %d,%d,%d, want red", r, g, b)
	}
}

// TestStylesheet covers what a style element declares: the selectors an
// exporter writes, what wins when two of them match, and the order a style
// attribute, a sheet and a presentation attribute go in.
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
		// A class beats a type, and an id beats a class.
		{"klass", "green"},
		{"one", "blue"},
		// A presentation attribute counts for nothing against a sheet, and a
		// style attribute beats both.
		{"attr", "green"},
		{"inline", "yellow"},
		// A descendant selector matches only inside what it names.
		{"desc", "purple"},
		{"outside", "red"},
	} {
		if got := fill(tc.id); got != tc.want {
			t.Errorf("%s is filled %q, want %q", tc.id, got, tc.want)
		}
	}
}

// TestPattern covers the third paint server: the tile repeats over the shape,
// and what is inside it inherits nothing from the shape that referred to it.
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
	// The tile repeats every ten across: black, then the page, four times.
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

// TestMaskAndMarker covers the two things drawn around a shape rather than in
// it: what a mask lets through, and what a marker puts at a vertex.
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

	// The mask is white over the left half, so only that half is painted.
	at := render(`<svg width="40" height="20">
	  <mask id="m"><rect width="20" height="20" fill="white"/></mask>
	  <rect width="40" height="20" fill="black" mask="url(#m)"/></svg>`)
	if r, g, b := at(10, 10); r > 8 || g > 8 || b > 8 {
		t.Errorf("inside the mask is %d,%d,%d, want black", r, g, b)
	}
	if r, g, b := at(30, 10); r < 248 || g < 248 || b < 248 {
		t.Errorf("outside the mask is %d,%d,%d, want the page", r, g, b)
	}

	// A marker is drawn at each end of the line and nowhere else.
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

// TestGradientSpread covers what spreadMethod does past the ends of a ramp,
// which the shader pads and this writes out as the periods the shape reaches.
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

	// Padding leaves one period, and the other two cover the four the shape
	// reaches across the unit square.
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
	// Repeat starts each period over and reflect turns every other one round.
	rep, ref := build("repeat"), build("reflect")
	if !near(rep.ramp(0.3), rep.ramp(0.05)) {
		t.Errorf("repeat gave %v and %v for the same point of two periods",
			rep.ramp(0.3), rep.ramp(0.05))
	}
	if !near(ref.ramp(0.3), 1-ref.ramp(0.05)) {
		t.Errorf("reflect gave %v, want the mirror of %v", ref.ramp(0.3), ref.ramp(0.05))
	}
}

// TestRootProperties covers the properties the root element carries, which
// every icon written as one outline puts there for the drawing to inherit.
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
	// The rect is stroked and not filled, so its edge is dark and its
	// middle is the page.
	if v := at(10, 5); v > 8 {
		t.Errorf("the edge is %d, want the stroke the root asked for", v)
	}
	if v := at(10, 10); v < 248 {
		t.Errorf("the middle is %d, want fill none to have been inherited", v)
	}
}

// TestImageSize covers rendering at a size the caller has room for rather
// than at the one the drawing asks for, which is what an icon wants.
func TestImageSize(t *testing.T) {
	d, err := Load([]byte(`<svg width="40" height="20"><rect width="40" height="20" fill="black"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, _ := d.Page(0)
	// A square asked of a drawing twice as wide keeps the aspect ratio.
	img, err := p.ImageSize(80, 80)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 40 {
		t.Errorf("the image is %v, want 80x40", b)
	}
	// One side alone takes the other from the drawing.
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

// TestRenderTo covers compositing a drawing onto an image the caller already
// has, which is what putting an icon on something drawn wants.
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
	// The half it was drawn on is the drawing and the other half is untouched.
	if r, _, b, _ := dst.At(15, 5).RGBA(); r>>8 > 8 || b>>8 > 8 {
		t.Errorf("the drawing landed as %v", dst.At(15, 5))
	}
	if _, _, b, _ := dst.At(5, 5).RGBA(); b>>8 < 200 {
		t.Errorf("what was already there became %v", dst.At(5, 5))
	}
}

// TestLoadWith covers what a drawing inside a container needs: a loader for
// the files it names, and a box to measure a percentage size against.
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
	// A percentage on the root is a percentage of the box it was given, not
	// of the default page.
	if b := (&Page{doc: d}).Bounds(); b.X1 != 200 || b.Y1 != 200 {
		t.Errorf("bounds %v, want 200x200", b)
	}
	if opened["sheet.css"] != 1 {
		t.Errorf("the sheet was read %d times, want 1", opened["sheet.css"])
	}
	// The sheet the instruction named is cascaded, and the @font-face in it
	// is a face the drawing brings rather than a rule.
	if len(d.rules) != 1 {
		t.Fatalf("%d rules, want 1", len(d.rules))
	}
	if len(d.faces) != 1 || d.faces[0].family != "Carried" {
		t.Fatalf("faces %+v, want one for Carried", d.faces)
	}
	// The face names a file the loader does not have, so it resolves to
	// nothing rather than failing, and is not read twice.
	if d.embedded("Carried", false, false) != nil {
		t.Error("a face read from nothing came back")
	}
	d.embedded("Carried", false, false)
	if opened["face.ttf"] != 1 {
		t.Errorf("the face was read %d times, want 1", opened["face.ttf"])
	}

	// A drawing with no loader looks nothing up at all.
	plain, err := Load([]byte(markup))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.rules) != 0 || len(plain.faces) != 0 {
		t.Error("a drawing with no loader read a sheet")
	}
}

// TestTspanPositions covers the per character positions an exporter writes,
// and the space between two tspans, which belongs to the element that wrote
// it and must not take the first position the next tspan lists.
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

	// The white space a text element ends with is dropped, and the space
	// between the two tspans sits where the pen was left rather than taking
	// the first position the second tspan lists.
	var got []string
	for _, g := range trimTrailing(c.glyphs) {
		got = append(got, string(g.r))
	}
	if s := strings.Join(got, ""); s != "abc de" {
		t.Errorf("placed %q, want %q", s, "abc de")
	}
	for i, want := range []float32{0, 10, 20, 24.8, 100, 110} {
		if x := c.glyphs[i].x; !near(x, want) {
			t.Errorf("glyph %d %q at %v, want %v", i, c.glyphs[i].r, x, want)
		}
	}
}

// TestPageText reads a drawing back as text, which is what a container asks
// for when the drawing is a page of a book.
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

// TestFilter covers the pipeline: the region a filter renders into, the
// primitives that make a picture out of nothing, the two color spaces and the
// chain one primitive's result reaches the next through.
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

	// A flood fills the whole region, which reaches past the box by a tenth
	// of it at each edge.
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

	// A subregion cuts the flood down, and what it leaves is transparent.
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

	// An offset moves the source and leaves nothing behind it.
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

	// A color matrix works in linear light unless the drawing says otherwise,
	// so the same matrix gives two different answers.
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

	// A chain: the flood is blurred, so the middle stays and the edge softens.
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

	// A filter that names nothing takes the element off the page.
	at = pixels(`<svg width="20" height="20">` +
		`<rect width="20" height="20" fill="red" filter="url(#missing)"/></svg>`)
	if r, g, b, _ := at(10, 10); r < 248 || g < 248 || b < 248 {
		t.Errorf("an element with an unresolved filter is %d,%d,%d, want the page", r, g, b)
	}

	// The shorthand of Filter Effects 1 chains without a filter element.
	at = pixels(`<svg width="20" height="20">` +
		`<rect width="20" height="20" fill="#ff0000" filter="grayscale(1)"/></svg>`)
	r, g, b, _ := at(10, 10)
	if r != g || g != b || r == 0 || r == 255 {
		t.Errorf("grayscale(1) of red is %d,%d,%d, want one grey", r, g, b)
	}

	// A filter on the root element is applied to everything under it.
	at = pixels(`<svg width="20" height="20" filter="url(#f)">` +
		`<filter id="f"><feFlood flood-color="#00ff00"/></filter>` +
		`<rect width="20" height="20" fill="red"/></svg>`)
	if r, g, b, _ := at(10, 10); r > 8 || g < 240 || b > 8 {
		t.Errorf("a filter on the root drew %d,%d,%d, want green", r, g, b)
	}
}

// TestViewportAndConditions covers what an element that establishes a
// viewport or chooses between its children does: the clip a nested svg puts
// on what overflows it, the first child of a switch whose conditions hold,
// and a font size read once rather than once per pass over the element.
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

	// A nested svg is a viewport, and overflow hides what reaches past it.
	px := at(`<svg width="40" height="40">` +
		`<svg x="10" y="10" width="10" height="10">` +
		`<rect width="40" height="40" fill="black"/></svg></svg>`)
	if r, _, _ := px(15, 15); r > 8 {
		t.Errorf("inside the nested viewport is %d, want black", r)
	}
	if r, _, _ := px(25, 15); r < 248 {
		t.Errorf("past the nested viewport is %d, want the page", r)
	}

	// overflow: visible turns the clip off.
	px = at(`<svg width="40" height="40">` +
		`<svg x="10" y="10" width="10" height="10" overflow="visible">` +
		`<rect width="40" height="40" fill="black"/></svg></svg>`)
	if r, _, _ := px(25, 15); r > 8 {
		t.Errorf("past a visible viewport is %d, want black", r)
	}

	// A switch draws the first child it may, and only that one.
	px = at(`<svg width="40" height="40"><switch>` +
		`<rect width="40" height="40" fill="#ff0000" requiredExtensions="urn:x"/>` +
		`<rect width="40" height="40" fill="#00ff00"/>` +
		`<rect width="40" height="40" fill="#ff0000"/></switch></svg>`)
	if r, g, b := px(20, 20); r > 8 || g < 240 || b > 8 {
		t.Errorf("the switch drew %d,%d,%d, want the second child", r, g, b)
	}

	// A relative font size is a fraction of what the element inherited, and
	// the element's own style is read once however many passes reach it.
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
	if len(c.glyphs) != 1 || c.glyphs[0].st.em != 20 {
		t.Errorf("the glyph was placed at %v", c.glyphs)
	}
}

// TestTextPath covers a run of characters laid along a path: each one sits
// where its middle falls, turns with the direction of travel, and one that
// runs off the end is not drawn.
func TestTextPath(t *testing.T) {
	// A quarter circle from (0,50) up to (50,0), so a character a quarter of
	// the way along it is turned by about a quarter turn.
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
		c.glyphs = onPath(c.glyphs)
		if want == nil {
			if len(c.glyphs) != 0 {
				t.Errorf("a textPath naming nothing placed %d glyphs", len(c.glyphs))
			}
			continue
		}
		*want = c.glyphs
	}
	if len(flat) != 2 || len(down) != 2 {
		t.Fatalf("placed %d and %d glyphs, want two each", len(flat), len(down))
	}
	// Along a path that runs to the right, the characters are where ordinary
	// text would put them and are not turned.
	if flat[0].y != 50 || flat[0].turn != 0 || flat[1].x <= flat[0].x {
		t.Errorf("along a horizontal path the glyphs are %v", flat)
	}
	// Along one that runs down the page they are turned a quarter and walk
	// down rather than across.
	if got := down[0].turn; got < 89 || got > 91 {
		t.Errorf("down a vertical path the turn is %v, want a quarter", got)
	}
	if down[1].y <= down[0].y || down[0].x < 49 || down[0].x > 51 {
		t.Errorf("down a vertical path the glyphs are %v", down)
	}
}

// TestFilterUnderRotation holds the coordinate system a filter runs in: the
// user's own, not the device's. A quarter turn between the element and the
// page sends an offset along x down the page instead of across it.
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
