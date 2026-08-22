package raster

import (
	"bytes"
	"math"
	"testing"
)

func TestPixmapShape(t *testing.T) {
	p := NewPixmap(ModelRGB, 4, 3, false)
	if p.N != 3 || p.Comps() != 3 || p.Stride != 12 || len(p.Samples) != 36 {
		t.Fatalf("rgb pixmap = %+v", p)
	}
	p = NewPixmap(ModelCMYK, 4, 3, true)
	if p.Comps() != 5 || p.Stride != 20 || len(p.Samples) != 60 {
		t.Fatalf("cmyk pixmap with alpha = %+v", p)
	}
	m := NewMask(4, 3)
	if m.N != 0 || m.Comps() != 1 || m.Stride != 4 {
		t.Fatalf("mask = %+v", m)
	}
	if NewPixmap(ModelRGB, 1<<30, 1<<30, true) != nil {
		t.Fatal("absurd size allocated")
	}
}

func TestPixmapClearWhite(t *testing.T) {
	for _, tc := range []struct {
		model Model
		alpha bool
		want  []uint8
	}{
		{ModelGray, false, []uint8{255}},
		{ModelRGB, false, []uint8{255, 255, 255}},
		{ModelRGB, true, []uint8{255, 255, 255, 255}},
		{ModelCMYK, false, []uint8{0, 0, 0, 0}},
		{ModelCMYK, true, []uint8{0, 0, 0, 0, 255}},
	} {
		p := NewPixmap(tc.model, 2, 2, tc.alpha)
		p.ClearWhite()
		for i, v := range tc.want {
			if p.Samples[i] != v {
				t.Fatalf("%d components alpha %v: sample %d = %d, want %d",
					tc.model.Components(), tc.alpha, i, p.Samples[i], v)
			}
		}
	}
}

func TestBlitSolidOpaque(t *testing.T) {
	p := NewPixmap(ModelRGB, 8, 2, false)
	p.ClearWhite()
	b := p.Blitter(Paint{Color: []uint8{255, 0, 0}, Alpha: 255})
	b.BlitSolid(2, 0, 3, 255)
	if got := p.Samples[6:9]; got[0] != 255 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("opaque red = %v", got)
	}
	if got := p.Samples[3:6]; got[0] != 255 || got[1] != 255 || got[2] != 255 {
		t.Fatalf("pixel outside the span = %v", got)
	}
}

func TestBlitCoverage(t *testing.T) {
	p := NewPixmap(ModelRGB, 4, 1, false)
	p.ClearWhite()
	p.Blitter(Paint{Color: []uint8{0, 0, 0}, Alpha: 255}).BlitCover(0, 0, []uint8{0, 64, 128, 255})
	want := []uint8{255, 191, 127, 0}
	for i, w := range want {
		if got := p.Samples[i*3]; got != w {
			t.Fatalf("pixel %d = %d, want %d", i, got, w)
		}
	}
}

func TestBlitConstantAlpha(t *testing.T) {
	p := NewPixmap(ModelGray, 2, 1, false)
	p.ClearWhite()
	p.Blitter(Paint{Color: []uint8{0}, Alpha: 128}).BlitSolid(0, 0, 1, 255)
	if got := p.Samples[0]; got != 127 {
		t.Fatalf("half alpha black on white = %d, want 127", got)
	}
	p.Blitter(Paint{Color: []uint8{0}, Alpha: 128}).BlitSolid(0, 0, 1, 128)
	if got := p.Samples[0]; got != 95 {
		t.Fatalf("half alpha at half coverage = %d, want 95", got)
	}
}

func TestBlitPremultiplied(t *testing.T) {
	p := NewPixmap(ModelRGB, 2, 1, true)
	p.Clear()
	p.Blitter(Paint{Color: []uint8{255, 0, 0}, Alpha: 128}).BlitSolid(0, 0, 1, 255)
	got := p.Samples[0:4]
	if got[0] != 128 || got[1] != 0 || got[2] != 0 || got[3] != 128 {
		t.Fatalf("red at half alpha = %v, want [128 0 0 128]", got)
	}
	p.Blitter(Paint{Color: []uint8{255, 0, 0}, Alpha: 128}).BlitSolid(0, 0, 1, 255)
	if got := p.Samples[3]; got != 192 {
		t.Fatalf("alpha after two half covers = %d, want 192", got)
	}
}

func TestMaskBlitter(t *testing.T) {
	m := NewMask(8, 2)
	b := m.MaskBlitter()
	b.BlitSolid(1, 0, 3, 200)
	b.BlitCover(4, 0, []uint8{10, 20})
	want := []uint8{0, 200, 200, 200, 10, 20, 0, 0}
	for i, w := range want {
		if m.Samples[i] != w {
			t.Fatalf("mask %d = %d, want %d", i, m.Samples[i], w)
		}
	}
}

func TestFillRect(t *testing.T) {
	p := NewPixmap(ModelGray, 8, 8, false)
	p.ClearWhite()
	p.FillRect(-4, 2, 4, 6, Paint{Color: []uint8{0}, Alpha: 255})
	if p.Samples[2*8+3] != 0 || p.Samples[2*8+4] != 255 {
		t.Fatal("fill rect did not clip to the pixmap")
	}
	if p.Samples[1*8+0] != 255 {
		t.Fatal("fill rect painted outside its rows")
	}
}

func TestRasterizeIntoPixmap(t *testing.T) {
	p := NewPixmap(ModelRGB, 32, 32, false)
	p.ClearWhite()
	r := NewRasterizer(32, 32)
	rect(r, 8, 8, 24, 24)
	r.Sweep(NonZero, p.Blitter(Paint{Color: []uint8{0, 0, 255}, Alpha: 255}))
	if got := p.Samples[(16*32+16)*3+2]; got != 255 {
		t.Fatalf("blue inside = %d", got)
	}
	if got := p.Samples[(2*32+2)*3+2]; got != 255 {
		t.Fatalf("outside pixel changed")
	}
	if got := p.Samples[(16*32+16)*3]; got != 0 {
		t.Fatalf("red channel inside = %d, want 0", got)
	}
}

func TestDiv255Exact(t *testing.T) {
	for t0 := uint32(0); t0 <= 255*255; t0++ {
		want := uint8(math.Round(float64(t0) / 255))
		if got := div255(t0); got != want {
			t.Fatalf("div255(%d) = %d, want %d", t0, got, want)
		}
	}
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			want := uint8(math.Round(float64(a*b) / 255))
			if got := mul255(uint8(a), uint8(b)); got != want {
				t.Fatalf("mul255(%d,%d) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestModelRoundTrip(t *testing.T) {
	rgb := []uint8{255, 128, 0, 0, 0, 0, 255, 255, 255}
	cmyk := make([]uint8, 12)
	ModelCMYK.FromRGB(cmyk, rgb)
	back := make([]uint8, 9)
	ModelCMYK.ToRGB(back, cmyk)
	for i := range rgb {
		if int(back[i])-int(rgb[i]) > 1 || int(rgb[i])-int(back[i]) > 1 {
			t.Fatalf("cmyk round trip %v -> %v", rgb, back)
		}
	}
	gray := make([]uint8, 3)
	ModelGray.FromRGB(gray, rgb)
	if gray[0] != 152 || gray[1] != 0 || gray[2] != 255 {
		t.Fatalf("gray from rgb = %v", gray)
	}
}

func TestDrawMaskOffTarget(t *testing.T) {
	p := NewPixmap(ModelGray, 8, 8, false)
	p.ClearWhite()
	m := NewMask(4, 4)
	for i := range m.Samples {
		m.Samples[i] = 255
	}
	for _, at := range [][2]int{{-9, 0}, {9, 0}, {0, -9}, {0, 9}, {-2, -2}, {7, 7}} {
		m.X, m.Y = at[0], at[1]
		p.DrawMask(m, Paint{Color: []uint8{0}, Alpha: 255})
	}
	if got, want := p.Samples[0], uint8(0); got != want {
		t.Fatalf("overlap at (-2,-2) did not paint: %d", got)
	}
	if got := p.Samples[4*8+4]; got != 255 {
		t.Fatalf("pixel outside every mask = %d", got)
	}
}

func TestMulMask(t *testing.T) {
	a := NewMask(4, 4)
	for i := range a.Samples {
		a.Samples[i] = 255
	}
	b := NewMask(2, 2)
	b.X, b.Y = 1, 1
	for i := range b.Samples {
		b.Samples[i] = 128
	}
	a.MulMask(b)
	if a.Samples[0] != 0 || a.Samples[1*4+1] != 128 || a.Samples[3*4+3] != 0 {
		t.Fatalf("mul mask = %v", a.Samples)
	}
}

// rampShader paints a gray ramp along x, and nothing at all past a column,
// so that a shader's alpha is exercised as well as its color.
type rampShader struct{ n, stop int }

func (s rampShader) Shade(x, y, w int, span []uint8) {
	for i := 0; i < w; i++ {
		out := span[i*(s.n+1):]
		if x+i >= s.stop {
			clear(out[:s.n+1])
			continue
		}
		for c := 0; c < s.n; c++ {
			out[c] = uint8(x + i)
		}
		out[s.n] = 255
	}
}

func TestFillShader(t *testing.T) {
	p := NewPixmap(ModelRGB, 16, 4, false)
	p.ClearWhite()
	r := NewRasterizer(16, 4)
	rect(r, 0, 0, 16, 4)
	r.FillShader(p, NonZero, rampShader{n: 3, stop: 12}, Paint{Alpha: 255})
	for x := 0; x < 12; x++ {
		if got := p.Samples[(2*16+x)*3]; got != uint8(x) {
			t.Fatalf("x=%d shaded %d, want %d", x, got, x)
		}
	}
	if got := p.Samples[(2*16+13)*3]; got != 255 {
		t.Fatalf("a transparent shader pixel painted %d", got)
	}
}

func TestFillShaderAlpha(t *testing.T) {
	p := NewPixmap(ModelGray, 8, 2, false)
	p.ClearWhite()
	r := NewRasterizer(8, 2)
	rect(r, 0, 0, 8, 2)
	r.FillShader(p, NonZero, rampShader{n: 1, stop: 8}, Paint{Alpha: 128})
	// Half of the way from white to the shader's own value.
	if got, want := p.Samples[4], mul255(128, 4)+mul255(127, 255); got != want {
		t.Fatalf("half alpha over white = %d, want %d", got, want)
	}
}

func TestFillTriangleInterpolates(t *testing.T) {
	p := NewPixmap(ModelRGB, 16, 16, true)
	p.FillTriangle(
		Vertex{X: 0, Y: 0, Color: [4]uint8{255, 0, 0}},
		Vertex{X: 16, Y: 0, Color: [4]uint8{0, 255, 0}},
		Vertex{X: 0, Y: 16, Color: [4]uint8{0, 0, 255}},
	)
	if got := pixelAt(p, 0, 0); got[0] < 200 || got[3] != 255 {
		t.Fatalf("the corner by the red vertex = %v", got)
	}
	if got := pixelAt(p, 14, 0); got[1] < 200 {
		t.Fatalf("the corner by the green vertex = %v", got)
	}
	if got := pixelAt(p, 0, 14); got[2] < 200 {
		t.Fatalf("the corner by the blue vertex = %v", got)
	}
	if got := pixelAt(p, 14, 14); got[3] != 0 {
		t.Fatalf("outside the triangle = %v, want nothing", got)
	}
}

// TestFillTriangleSeam checks that two triangles sharing an edge leave no
// pixel of the square they make up unpainted, which is why they are not
// anti-aliased.
func TestFillTriangleSeam(t *testing.T) {
	p := NewPixmap(ModelGray, 8, 8, true)
	c := [4]uint8{200}
	a := Vertex{X: 1.5, Y: 1.5, Color: c}
	b := Vertex{X: 6.5, Y: 1.5, Color: c}
	d := Vertex{X: 1.5, Y: 6.5, Color: c}
	e := Vertex{X: 6.5, Y: 6.5, Color: c}
	p.FillTriangle(a, b, d)
	p.FillTriangle(b, e, d)
	for y := 2; y < 6; y++ {
		for x := 2; x < 6; x++ {
			if got := pixelAt(p, x, y); got[1] != 255 {
				t.Fatalf("a seam at %d,%d: %v", x, y, got)
			}
		}
	}
}

func pixelAt(p *Pixmap, x, y int) []uint8 {
	n := p.Comps()
	return p.Samples[y*p.Stride+x*n : y*p.Stride+x*n+n]
}

// TestShrinkerMatchesSubsample holds the streaming reduction to the one built
// from the whole pixmap. The two have to agree byte for byte, because the
// point of the streaming one is that nothing downstream can tell which built
// the image.
func TestShrinkerMatchesSubsample(t *testing.T) {
	for _, size := range [][2]int{
		{1, 1}, {1, 8}, {8, 1}, {2, 2}, {3, 3}, {4, 4}, {5, 7}, {7, 5},
		{16, 16}, {17, 33}, {64, 3}, {3, 64}, {100, 100}, {255, 129},
	} {
		w, h := size[0], size[1]
		for n := 0; n < 5; n++ {
			for _, alpha := range []bool{false, true} {
				whole := NewPixmap(ModelRGB, w, h, alpha)
				if whole == nil {
					t.Fatalf("%dx%d: no pixmap", w, h)
				}
				comps := whole.Comps()
				fill := func(row []uint8, y int) {
					for x := 0; x < w*comps; x++ {
						row[x] = uint8((y*7 + x*13 + y*x) % 251)
					}
				}
				for y := 0; y < h; y++ {
					fill(whole.Row(y), y)
				}
				want := whole.Subsample(n)

				s := NewShrinker(ModelRGB, w, h, alpha, n)
				if s == nil {
					t.Fatalf("%dx%d: no shrinker", w, h)
				}
				for y := 0; y < h; y++ {
					fill(s.Row(), y)
					s.Commit()
				}
				got := s.Pixmap()

				if got.W != want.W || got.H != want.H {
					t.Fatalf("%dx%d by %d alpha=%v: shrank to %dx%d, Subsample gives %dx%d",
						w, h, n, alpha, got.W, got.H, want.W, want.H)
				}
				for y := 0; y < want.H; y++ {
					a, b := got.Row(y), want.Row(y)
					for x := range b[:want.W*comps] {
						if a[x] != b[x] {
							t.Fatalf("%dx%d by %d alpha=%v: row %d byte %d is %d, Subsample gives %d",
								w, h, n, alpha, y, x, a[x], b[x])
						}
					}
				}
			}
		}
	}
}

// TestShrinkerShortRow checks that a row the caller does not fill reads as
// zero, which is what writing into a fresh pixmap did.
func TestShrinkerShortRow(t *testing.T) {
	s := NewMaskShrinker(8, 8, 1)
	for y := 0; y < 8; y++ {
		row := s.Row()
		if y == 0 {
			for x := range row {
				row[x] = 255
			}
		}
		s.Commit()
	}
	px := s.Pixmap()
	if got := px.Row(0)[0]; got != 128 {
		t.Errorf("one white row of two averages to %d, want 128", got)
	}
	if got := px.Row(1)[0]; got != 0 {
		t.Errorf("an unfilled row reads %d, want 0", got)
	}
}

// TestBilinearRowMatchesGeneral holds the column blend to the four product
// form it regroups, over every scale that picks one path or the other and
// both directions along the row.
func TestBilinearRowMatchesGeneral(t *testing.T) {
	const sw, sh = 23, 17
	for _, n := range []int{1, 3, 4} {
		src := newPixmap(ModelRGB, n, sw, sh, false)
		seed := uint32(7)
		for i := range src.Samples {
			seed = seed*1664525 + 1013904223
			src.Samples[i] = uint8(seed >> 24)
		}
		for _, scale := range []float32{0.31, 0.5, 0.97, 1, 1.03, 2, 3.7, 11} {
			for _, flip := range []bool{false, true} {
				for _, off := range []float32{0, 0.25, -3.5, 7.125} {
					a := scale
					if flip {
						a = -scale
					}
					w := 40
					b := imageBlitter{src: src, inv: Matrix{A: a, D: scale, E: off, F: 1.5}}
					got := make([]uint8, w*n)
					b.bilinearRow(3, w, 0, 2.5, got)

					want := make([]uint8, w*n)
					for i := range w {
						u := b.ucoord(3+i, 0) + 0.5
						b.bilinear(u, 2.5, want[i*n:])
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("n=%d a=%v off=%v:\n got %v\nwant %v", n, a, off, got, want)
					}
				}
			}
		}
	}
}

// TestGradientRowMatchesSpan holds the row form of a gradient to the span form
// it exists to skip, over both kinds, both extends and a destination with and
// without an alpha channel.
func TestGradientRowMatchesSpan(t *testing.T) {
	lut := make([]uint8, 256*3)
	for i := range 256 {
		lut[i*3], lut[i*3+1], lut[i*3+2] = uint8(i), uint8(255-i), uint8(i/2)
	}
	specs := []GradientSpec{
		{C0: Point{4, 5}, C1: Point{28, 19}},
		{C0: Point{4, 5}, C1: Point{28, 19}, Ext0: true, Ext1: true},
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true},
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true, Ext1: true},
		{C0: Point{16, 12}, R0: 9, C1: Point{16, 12}, R1: 9, Radial: true, Ext0: true},
	}
	for _, alpha := range []bool{false, true} {
		for i, spec := range specs {
			spec.Matrix, spec.LUT, spec.N = Scale(1.3, 0.9), lut, 3
			g := NewGradient(spec)
			if g == nil {
				t.Fatalf("spec %d built nothing", i)
			}
			const w, h = 30, 6
			dst := NewPixmap(ModelRGB, w, h, alpha)
			want := NewPixmap(ModelRGB, w, h, alpha)
			for j := range dst.Samples {
				dst.Samples[j], want.Samples[j] = 0x37, 0x37
			}

			span := make([]uint8, w*4)
			n := want.Comps()
			for y := range h {
				g.ShadeRow(dst, 0, y, w)
				g.Shade(0, y, w, span)
				row := want.Row(y)
				for x := range w {
					src := span[x*4:]
					if src[3] == 0 {
						continue
					}
					if src[3] != 255 {
						t.Fatalf("spec %d: covered pixel has alpha %d, want 255", i, src[3])
					}
					copy(row[x*n:], src[:min(n, 4)])
				}
			}
			if !bytes.Equal(dst.Samples, want.Samples) {
				t.Fatalf("spec %d alpha=%v: row and span disagree", i, alpha)
			}
		}
	}
}

// TestBlurBoxWidths holds the two things the width of the boxes decides: how
// far a deviation spreads, and that three of them are what SVG 1.1 15.17
// asks for. The variance of the cascade is what a Gaussian of that deviation
// would have, to within the coarseness of an integer box.
func TestBlurBoxWidths(t *testing.T) {
	for _, c := range []struct {
		sigma float32
		d     int
		want  float64
	}{
		{2, 4, 2.121},
		{2.5, 5, 2.449},
		{3, 6, 3.136},
		{4, 8, 4.143},
		{5, 9, 4.472},
	} {
		if d := boxWidth(c.sigma); d != c.d {
			t.Errorf("boxWidth(%g) = %d, want %d", c.sigma, d, c.d)
			continue
		}
		v := 0.0
		for _, w := range boxPasses(c.d) {
			n := float64(w[0] + w[1] + 1)
			v += (n*n - 1) / 12
		}
		if got := math.Sqrt(v); math.Abs(got-c.want) > 0.001 {
			t.Errorf("sigma %g spreads by %.3f, want %.3f", c.sigma, got, c.want)
		}
	}
}

// TestBlurEdge blurs a step and checks that the result is what a Gaussian
// does to one: it conserves the whole, it is symmetric about the edge, and
// nothing reaches further than the kernel.
func TestBlurEdge(t *testing.T) {
	for _, sigma := range []float32{0.8, 1.5, 3, 6} {
		// Wide enough that the far side of the step is saturated well before
		// the edge of the pixmap, which the blur erodes.
		const w, h = 128, 3
		p := NewPixmap(ModelRGB, w, h, true)
		for y := range h {
			for x := w / 2; x < w; x++ {
				o := y*p.Stride + x*4
				p.Samples[o], p.Samples[o+1], p.Samples[o+2], p.Samples[o+3] = 0, 0, 0, 255
			}
		}
		Blur(p, sigma, 0)
		var sum, m1, m2 float64
		for x := w/2 - 30; x < w/2+30; x++ {
			k := float64(p.Samples[p.Stride+x*4+3]) - float64(p.Samples[p.Stride+(x-1)*4+3])
			off := float64(x) - w/2
			sum += k
			m1 += k * off
			m2 += k * off * off
		}
		if math.Abs(sum-255) > 1 {
			t.Errorf("sigma %g: the step lost %g of 255", sigma, 255-sum)
		}
		mean := m1 / sum
		if math.Abs(mean) > 0.05 {
			t.Errorf("sigma %g: the edge moved to %.3f", sigma, mean)
		}
		if got := math.Sqrt(m2/sum - mean*mean); math.Abs(got-float64(sigma))/float64(sigma) > 0.12 {
			t.Errorf("sigma %g came out as %.3f", sigma, got)
		}
	}
}

// TestBlurFlat covers what must not change: a deviation of zero, and a field
// with nothing in it to spread.
func TestBlurFlat(t *testing.T) {
	p := NewPixmap(ModelRGB, 64, 64, true)
	for i := range p.Samples {
		p.Samples[i] = 200
	}
	before := append([]uint8(nil), p.Samples...)
	Blur(p, 0, 0)
	for i, v := range p.Samples {
		if v != before[i] {
			t.Fatalf("a deviation of zero changed sample %d from %d to %d", i, before[i], v)
		}
	}
	Blur(p, 4, 4)
	for x := 31; x < 33; x++ {
		if v := p.Samples[32*p.Stride+x*4]; v != 200 {
			t.Errorf("the middle of a flat field blurred to %d, want 200", v)
		}
	}
}

// TestGradientAlphaTable covers a ramp whose stops differ in opacity, which
// is the one case the color table alone cannot carry.
func TestGradientAlphaTable(t *testing.T) {
	lut := make([]uint8, 256*3)
	a := make([]uint8, 256)
	for i := range 256 {
		a[i] = uint8(i)
		for c := range 3 {
			lut[i*3+c] = uint8(uint32(255) * uint32(i) / 255)
		}
	}
	g := NewGradient(GradientSpec{
		Matrix: Identity, LUT: lut, A: a, N: 3,
		C0: Point{}, C1: Point{X: 256}, Ext0: true, Ext1: true,
	})
	if g.Opaque() {
		t.Fatal("a gradient with an alpha table reports itself opaque")
	}
	span := make([]uint8, 4*4)
	g.Shade(0, 0, 4, span)
	for i := range 4 {
		if got := span[i*4+3]; got != a[i] {
			t.Errorf("pixel %d has alpha %d, want %d", i, got, a[i])
		}
	}
	plain := NewGradient(GradientSpec{
		Matrix: Identity, LUT: lut, N: 3,
		C0: Point{}, C1: Point{X: 256}, Ext0: true, Ext1: true,
	})
	if !plain.Opaque() {
		t.Error("a gradient with no alpha table is not opaque")
	}
}
