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
