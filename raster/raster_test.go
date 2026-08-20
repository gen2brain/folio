package raster

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type mask struct {
	w, h int
	pix  []uint8
}

func newMask(w, h int) *mask { return &mask{w, h, make([]uint8, w*h)} }

func (m *mask) BlitSolid(x, y, w int, alpha uint8) {
	row := m.pix[y*m.w+x : y*m.w+x+w]
	for i := range row {
		row[i] = alpha
	}
}

func (m *mask) BlitCover(x, y int, cover []uint8) {
	copy(m.pix[y*m.w+x:y*m.w+x+len(cover)], cover)
}

func (m *mask) at(x, y int) uint8 { return m.pix[y*m.w+x] }

func (m *mask) sum() int {
	t := 0
	for _, v := range m.pix {
		t += int(v)
	}
	return t
}

func fill(t *testing.T, w, h int, rule FillRule, f func(r *Rasterizer)) *mask {
	t.Helper()
	r := NewRasterizer(w, h)
	f(r)
	m := newMask(w, h)
	r.Sweep(rule, m)
	return m
}

func rect(r *Rasterizer, x0, y0, x1, y1 float32) {
	r.MoveTo(x0, y0)
	r.LineTo(x1, y0)
	r.LineTo(x1, y1)
	r.LineTo(x0, y1)
	r.Close()
}

func TestRasterizeRect(t *testing.T) {
	m := fill(t, 10, 10, NonZero, func(r *Rasterizer) { rect(r, 2, 3, 6, 7) })
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			want := uint8(0)
			if x >= 2 && x < 6 && y >= 3 && y < 7 {
				want = 255
			}
			if got := m.at(x, y); got != want {
				t.Fatalf("at(%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestRasterizePartialPixel(t *testing.T) {
	m := fill(t, 10, 10, NonZero, func(r *Rasterizer) { rect(r, 2, 0, 5.5, 1) })
	if got := m.at(4, 0); got != 255 {
		t.Fatalf("full pixel = %d", got)
	}
	if got := m.at(5, 0); got != 128 {
		t.Fatalf("half pixel = %d, want 128", got)
	}
	if got := m.at(6, 0); got != 0 {
		t.Fatalf("empty pixel = %d", got)
	}
}

func TestRasterizeTriangleArea(t *testing.T) {
	m := fill(t, 64, 64, NonZero, func(r *Rasterizer) {
		r.MoveTo(4, 4)
		r.LineTo(52, 12)
		r.LineTo(20, 56)
		r.Close()
	})
	area := math.Abs(float64((52-4)*(56-4)-(20-4)*(12-4))) / 2
	if got, want := float64(m.sum())/255, area; math.Abs(got-want) > 1 {
		t.Fatalf("covered area = %.2f, want %.2f", got, want)
	}
}

func TestRasterizeFillRule(t *testing.T) {
	same := func(r *Rasterizer) {
		rect(r, 1, 1, 6, 6)
		rect(r, 3, 3, 8, 8)
	}
	nz := fill(t, 10, 10, NonZero, same)
	eo := fill(t, 10, 10, EvenOdd, same)
	if nz.at(4, 4) != 255 {
		t.Fatalf("non-zero overlap = %d", nz.at(4, 4))
	}
	if eo.at(4, 4) != 0 {
		t.Fatalf("even-odd overlap = %d", eo.at(4, 4))
	}
	if eo.at(2, 2) != 255 || eo.at(7, 7) != 255 {
		t.Fatal("even-odd dropped the non-overlapping part")
	}
}

func TestRasterizeReverseWinding(t *testing.T) {
	m := fill(t, 10, 10, NonZero, func(r *Rasterizer) {
		rect(r, 1, 1, 8, 8)
		r.MoveTo(3, 3)
		r.LineTo(3, 6)
		r.LineTo(6, 6)
		r.LineTo(6, 3)
		r.Close()
	})
	if m.at(4, 4) != 0 {
		t.Fatalf("hole = %d, want 0", m.at(4, 4))
	}
	if m.at(2, 2) != 255 {
		t.Fatalf("ring = %d, want 255", m.at(2, 2))
	}
}

func TestRasterizeClipsToTarget(t *testing.T) {
	m := fill(t, 8, 8, NonZero, func(r *Rasterizer) { rect(r, -100, -100, 100, 100) })
	for i, v := range m.pix {
		if v != 255 {
			t.Fatalf("pixel %d = %d, want 255", i, v)
		}
	}
	m = fill(t, 8, 8, NonZero, func(r *Rasterizer) { rect(r, 20, 20, 30, 30) })
	if m.sum() != 0 {
		t.Fatalf("off target geometry covered %d", m.sum())
	}
	m = fill(t, 8, 8, NonZero, func(r *Rasterizer) { rect(r, -4, -4, 4, 4) })
	if m.at(0, 0) != 255 || m.at(4, 4) != 0 {
		t.Fatal("partly off target rectangle")
	}
}

func TestRasterizeClipBox(t *testing.T) {
	r := NewRasterizer(16, 16)
	r.SetClip(Rect{4, 4, 12, 12})
	rect(r, 0, 0, 16, 16)
	m := newMask(16, 16)
	r.Sweep(NonZero, m)
	if m.at(2, 2) != 0 {
		t.Fatalf("outside clip = %d", m.at(2, 2))
	}
	if m.at(8, 8) != 255 {
		t.Fatalf("inside clip = %d", m.at(8, 8))
	}
	if m.sum() != 8*8*255 {
		t.Fatalf("clipped area = %d", m.sum()/255)
	}
}

func TestRasterizeReuse(t *testing.T) {
	r := NewRasterizer(10, 10)
	rect(r, 0, 0, 4, 4)
	a := newMask(10, 10)
	r.Sweep(NonZero, a)
	r.Reset()
	rect(r, 6, 6, 10, 10)
	b := newMask(10, 10)
	r.Sweep(NonZero, b)
	if b.at(1, 1) != 0 || b.at(7, 7) != 255 || b.sum() != 16*255 {
		t.Fatal("rasterizer kept state across Reset")
	}
}

func TestRasterizeEmpty(t *testing.T) {
	m := fill(t, 8, 8, NonZero, func(r *Rasterizer) {})
	if m.sum() != 0 {
		t.Fatal("empty path covered pixels")
	}
	m = fill(t, 8, 8, NonZero, func(r *Rasterizer) { r.MoveTo(1, 1) })
	if m.sum() != 0 {
		t.Fatal("lone move covered pixels")
	}
	m = fill(t, 8, 8, NonZero, func(r *Rasterizer) {
		r.MoveTo(1, 1)
		r.LineTo(6, 6)
	})
	if m.sum() != 0 {
		t.Fatal("degenerate subpath covered pixels")
	}
}

func TestRasterizeCircleArea(t *testing.T) {
	const k = 0.5522847
	r := NewRasterizer(128, 128)
	p := &Path{}
	cx, cy, rad := float32(64), float32(64), float32(50)
	p.MoveTo(cx+rad, cy)
	p.CurveTo(cx+rad, cy+rad*k, cx+rad*k, cy+rad, cx, cy+rad)
	p.CurveTo(cx-rad*k, cy+rad, cx-rad, cy+rad*k, cx-rad, cy)
	p.CurveTo(cx-rad, cy-rad*k, cx-rad*k, cy-rad, cx, cy-rad)
	p.CurveTo(cx+rad*k, cy-rad, cx+rad, cy-rad*k, cx+rad, cy)
	p.Close()
	r.AddPath(p, Identity)
	m := newMask(128, 128)
	r.Sweep(NonZero, m)
	got := float64(m.sum()) / 255
	want := math.Pi * 50 * 50
	if math.Abs(got-want)/want > 0.001 {
		t.Fatalf("circle area = %.1f, want %.1f", got, want)
	}
}

func TestBoundsContainsCoverage(t *testing.T) {
	r := NewRasterizer(32, 32)
	rect(r, 3.5, 7.25, 20, 19)
	x0, y0, x1, y1 := r.Bounds()
	m := newMask(32, 32)
	r.Sweep(NonZero, m)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if m.at(x, y) != 0 && (x < x0 || x >= x1 || y < y0 || y >= y1) {
				t.Fatalf("covered (%d,%d) outside bounds %d,%d,%d,%d", x, y, x0, y0, x1, y1)
			}
		}
	}
	if x0 != 3 || y0 != 7 || x1 > 21 || y1 > 20 {
		t.Fatalf("bounds = %d,%d,%d,%d", x0, y0, x1, y1)
	}
}

// TestAGGFixtures replays testdata/agg.tsv, the coverage AGG 2.3 produces for
// the same geometry. CORPUS.md §4a is the protocol.
func TestAGGFixtures(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "testdata", "agg.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || text[0] == '#' {
			continue
		}
		field := strings.Split(text, "\t")
		if len(field) != 7 {
			t.Fatalf("line %d has %d fields", line, len(field))
		}
		rule := NonZero
		if field[0] == "e" {
			rule = EvenOdd
		}
		w, _ := strconv.Atoi(field[1])
		h, _ := strconv.Atoi(field[2])

		r := NewRasterizer(w, h)
		for _, sub := range strings.Split(field[3], ";") {
			for i, pt := range strings.Fields(sub) {
				c := strings.IndexByte(pt, ',')
				if c < 0 {
					t.Fatalf("line %d: bad point %q", line, pt)
				}
				x, err1 := strconv.ParseFloat(pt[:c], 32)
				y, err2 := strconv.ParseFloat(pt[c+1:], 32)
				if err1 != nil || err2 != nil {
					t.Fatalf("line %d: bad point %q", line, pt)
				}
				if i == 0 {
					r.MoveTo(float32(x), float32(y))
				} else {
					r.LineTo(float32(x), float32(y))
				}
			}
			r.Close()
		}
		m := newMask(w, h)
		r.Sweep(rule, m)

		var sum, nonzero uint64
		hash := uint64(1469598103934665603)
		for _, v := range m.pix {
			sum += uint64(v)
			if v != 0 {
				nonzero++
			}
			hash = (hash ^ uint64(v)) * 1099511628211
		}
		got := fmt.Sprintf("%d\t%d\t%016x", sum, nonzero, hash)
		if want := strings.Join(field[4:], "\t"); got != want {
			t.Errorf("line %d: coverage %s, AGG says %s\n  %s", line, got, want, field[3])
		}
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("only %d fixtures", n)
	}
}

func benchPath(w, h int) *Path {
	p := &Path{}
	rng := uint32(1)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng>>8) / float32(1<<24)
	}
	for i := 0; i < 64; i++ {
		x, y := next()*float32(w), next()*float32(h)
		p.MoveTo(x, y)
		for j := 0; j < 5; j++ {
			p.CurveTo(x+next()*40-20, y+next()*40-20, x+next()*40-20, y+next()*40-20,
				x+next()*40-20, y+next()*40-20)
		}
		p.Close()
	}
	return p
}

func BenchmarkFillPolygons(b *testing.B) {
	const w, h = 512, 512
	p := benchPath(w, h)
	r := NewRasterizer(w, h)
	m := newMask(w, h)
	b.ResetTimer()
	for b.Loop() {
		r.Reset()
		r.AddPath(p, Identity)
		r.Sweep(NonZero, m)
	}
}

func BenchmarkFillSolid(b *testing.B) {
	const w, h = 512, 512
	r := NewRasterizer(w, h)
	p := NewPixmap(ModelRGB, w, h, false)
	blit := p.Blitter(Paint{Color: []uint8{200, 100, 50}, Alpha: 255})
	b.ResetTimer()
	for b.Loop() {
		r.Reset()
		rect(r, 4.5, 4.5, w-4.5, h-4.5)
		r.Sweep(NonZero, blit)
	}
}

func BenchmarkFillGlyphs(b *testing.B) {
	const w, h = 32, 32
	p := &Path{}
	p.MoveTo(4, 28)
	p.CurveTo(10, 2, 22, 2, 28, 28)
	p.LineTo(22, 28)
	p.CurveTo(18, 12, 14, 12, 10, 28)
	p.Close()
	r := NewRasterizer(w, h)
	m := newMask(w, h)
	b.ResetTimer()
	for b.Loop() {
		r.Reset()
		r.AddPath(p, Identity)
		r.Sweep(NonZero, m)
	}
}

func TestRasterizeHostileCoordinates(t *testing.T) {
	inf := float32(math.Inf(1))
	nan := float32(math.NaN())
	for _, v := range []float32{inf, -inf, nan, 1e30, -1e30, 1e-30} {
		m := fill(t, 8, 8, NonZero, func(r *Rasterizer) {
			r.MoveTo(1, 1)
			r.LineTo(v, 4)
			r.LineTo(4, v)
			r.LineTo(v, v)
			r.Close()
		})
		_ = m.sum()
	}
	p := &Path{}
	p.MoveTo(nan, nan)
	p.CurveTo(inf, 1e30, nan, -inf, 3, 4)
	p.Close()
	r := NewRasterizer(8, 8)
	r.AddPath(p, Matrix{nan, inf, 0, 0, 1e30, -1e30})
	r.Sweep(EvenOdd, newMask(8, 8))

	s := Stroke{Width: nan, MiterLimit: inf, Dash: []float32{nan, inf}}
	s.Outline(p, nan)
	s.Dash = []float32{0, 0}
	s.Outline(p, 1e30)
}
