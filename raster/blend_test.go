package raster

import "testing"

func TestBlendComponents(t *testing.T) {
	for _, tc := range []struct {
		mode BlendMode
		b, s uint8
		want uint8
	}{
		{BlendNormal, 100, 200, 200},
		{BlendMultiply, 255, 128, 128},
		{BlendMultiply, 0, 200, 0},
		{BlendScreen, 0, 200, 200},
		{BlendScreen, 255, 10, 255},
		{BlendDarken, 40, 200, 40},
		{BlendLighten, 40, 200, 200},
		{BlendDifference, 40, 200, 160},
		{BlendExclusion, 255, 255, 0},
		{BlendColorDodge, 0, 200, 0},
		{BlendColorDodge, 128, 255, 255},
		{BlendColorBurn, 255, 0, 255},
		{BlendColorBurn, 128, 0, 0},
		{BlendHardLight, 200, 0, 0},
		{BlendHardLight, 200, 255, 255},
		{BlendOverlay, 0, 200, 0},
		{BlendSoftLight, 128, 128, 128},
	} {
		if got := blendComponent(tc.mode, tc.b, tc.s); got != tc.want {
			t.Errorf("%v(%d, %d) = %d, want %d", tc.mode, tc.b, tc.s, got, tc.want)
		}
	}
}

// TestBlendLuminosity checks one of the four that are defined on a color as a
// whole: the result has to carry the source's luminosity and nothing else of
// it.
func TestBlendLuminosity(t *testing.T) {
	var out [3]uint8
	cb := [3]uint8{200, 40, 40}
	cs := [3]uint8{128, 128, 128}
	blendNonSeparable(BlendLuminosity, &cb, &cs, &out)
	if got := grayOf(out[:]); got < 126 || got > 130 {
		t.Errorf("luminosity of the result = %d, want about 128", got)
	}
	blendNonSeparable(BlendColor, &cb, &cs, &out)
	if got := grayOf(out[:]); got < grayOf(cb[:])-2 || got > grayOf(cb[:])+2 {
		t.Errorf("Color changed the luminosity to %d, want %d", got, grayOf(cb[:]))
	}
}

// TestBlendOverNormal checks that compositing a group is exact where nothing
// has to leave premultiplied form.
func TestBlendOverNormal(t *testing.T) {
	dst := NewPixmap(ModelRGB, 4, 1, false)
	dst.ClearWhite()
	src := NewPixmap(ModelRGB, 4, 1, true)
	for x := 0; x < 4; x++ {
		copy(src.Row(0)[x*4:], []uint8{0, 0, 0, 255})
	}
	dst.BlendOver(src, 128, BlendNormal)
	if got := dst.Row(0)[0]; got != 127 {
		t.Fatalf("black at half alpha over white = %d, want 127", got)
	}
	dst.ClearWhite()
	dst.BlendOver(src, 255, BlendNormal)
	if got := dst.Row(0)[0]; got != 0 {
		t.Fatalf("opaque black over white = %d, want 0", got)
	}
}

func TestBlendOverMultiply(t *testing.T) {
	dst := NewPixmap(ModelRGB, 1, 1, false)
	copy(dst.Row(0), []uint8{200, 100, 50})
	src := NewPixmap(ModelRGB, 1, 1, true)
	copy(src.Row(0), []uint8{128, 128, 128, 255})
	dst.BlendOver(src, 255, BlendMultiply)
	want := []uint8{mul255(200, 128), mul255(100, 128), mul255(50, 128)}
	for i, w := range want {
		if got := dst.Row(0)[i]; got != w {
			t.Errorf("component %d = %d, want %d", i, got, w)
		}
	}
}

// TestBlendOverPlaced checks that a group composites where its own X and Y put
// it, which is how a group covers only part of the page.
func TestBlendOverPlaced(t *testing.T) {
	dst := NewPixmap(ModelGray, 8, 4, false)
	dst.ClearWhite()
	src := NewPixmap(ModelGray, 2, 2, true)
	src.X, src.Y = 3, 1
	for i := range src.Samples {
		src.Samples[i] = 0
	}
	for y := 0; y < 2; y++ {
		src.Row(y)[1], src.Row(y)[3] = 255, 255
	}
	dst.BlendOver(src, 255, BlendNormal)
	if got := dst.Row(1)[3]; got != 0 {
		t.Errorf("inside the group = %d, want black", got)
	}
	if got := dst.Row(0)[3]; got != 255 {
		t.Errorf("above the group = %d, want white", got)
	}
	if got := dst.Row(1)[2]; got != 255 {
		t.Errorf("left of the group = %d, want white", got)
	}
}

func TestPixmapMask(t *testing.T) {
	p := NewPixmap(ModelRGB, 2, 1, false)
	copy(p.Row(0), []uint8{255, 255, 255, 0, 0, 0})
	m := p.Mask(true, nil)
	if m.Row(0)[0] != 255 || m.Row(0)[1] != 0 {
		t.Fatalf("luminosity mask = %v, want white then black", m.Row(0))
	}
	var table [256]uint8
	for i := range table {
		table[i] = uint8(255 - i)
	}
	m = p.Mask(true, &table)
	if m.Row(0)[0] != 0 || m.Row(0)[1] != 255 {
		t.Fatalf("through an inverting transfer = %v", m.Row(0))
	}

	a := NewPixmap(ModelRGB, 2, 1, true)
	copy(a.Row(0), []uint8{0, 0, 0, 64, 0, 0, 0, 192})
	m = a.Mask(false, nil)
	if m.Row(0)[0] != 64 || m.Row(0)[1] != 192 {
		t.Fatalf("alpha mask = %v", m.Row(0))
	}
}

// TestBlitterOrigin checks that a pixmap standing for part of the page is
// written where the page says, not where its own samples start.
func TestBlitterOrigin(t *testing.T) {
	p := NewPixmap(ModelGray, 4, 4, false)
	p.ClearWhite()
	p.X, p.Y = 10, 20
	r := NewRasterizer(64, 64)
	rect(r, 10, 20, 12, 22)
	r.Fill(p, NonZero, Paint{Color: []uint8{0}, Alpha: 255})
	if got := p.Row(0)[0]; got != 0 {
		t.Fatalf("the top left of the pixmap = %d, want the fill", got)
	}
	if got := p.Row(2)[2]; got != 255 {
		t.Fatalf("past the fill = %d, want untouched", got)
	}
}
