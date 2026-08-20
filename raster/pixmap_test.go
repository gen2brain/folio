package raster

import (
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
