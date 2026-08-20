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

func strokeMask(w, h int, s *Stroke, p *Path) *mask {
	r := NewRasterizer(w, h)
	r.AddPath(s.Outline(p, 1), Identity)
	m := newMask(w, h)
	r.Sweep(NonZero, m)
	return m
}

func line(x0, y0, x1, y1 float32) *Path {
	p := &Path{}
	p.MoveTo(x0, y0)
	p.LineTo(x1, y1)
	return p
}

func TestStrokeLineButt(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10}
	m := strokeMask(16, 16, &s, line(2, 4, 10, 4))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			want := uint8(0)
			if x >= 2 && x < 10 && y >= 3 && y < 5 {
				want = 255
			}
			if got := m.at(x, y); got != want {
				t.Fatalf("at(%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestStrokeCaps(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10}
	s.SetCaps(CapSquare)
	m := strokeMask(16, 16, &s, line(2, 4, 10, 4))
	if m.at(1, 4) != 255 || m.at(10, 4) != 255 {
		t.Fatal("square cap does not extend by half the width")
	}
	if m.sum() != 10*2*255 {
		t.Fatalf("square capped area = %d, want %d", m.sum()/255, 20)
	}

	s.SetCaps(CapRound)
	m = strokeMask(16, 16, &s, line(2, 4, 10, 4))
	got := float64(m.sum())/255 - 16
	if got > math.Pi || got < math.Pi*0.8 {
		t.Fatalf("round cap area = %.2f, want just under %.2f", got, math.Pi)
	}
}

func TestStrokeArcTolerance(t *testing.T) {
	p := &Path{}
	p.MoveTo(20, 20)
	p.LineTo(20, 20)
	s := Stroke{Width: 8, MiterLimit: 10}
	s.SetCaps(CapRound)
	o := s.Outline(p, 1)
	if len(o.pts) < 8 {
		t.Fatalf("round dot has %d points", len(o.pts))
	}
	for i, q := range o.pts {
		if r := dist(20, 20, q.X, q.Y); math.Abs(float64(r)-4) > 1e-3 {
			t.Fatalf("point %d at radius %v, want 4", i, r)
		}
		n := o.pts[(i+1)%len(o.pts)]
		mid := Point{(q.X + n.X) / 2, (q.Y + n.Y) / 2}
		if sag := 4 - dist(20, 20, mid.X, mid.Y); sag > arcTolerance {
			t.Fatalf("chord %d strays %v from the arc, tolerance %v", i, sag, arcTolerance)
		}
	}
}

func TestStrokeClosedRect(t *testing.T) {
	p := &Path{}
	p.Rect(4, 4, 8, 8)
	s := Stroke{Width: 2, MiterLimit: 10}
	m := strokeMask(24, 24, &s, p)
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			in := x >= 3 && x < 13 && y >= 3 && y < 13
			hole := x >= 5 && x < 11 && y >= 5 && y < 11
			want := uint8(0)
			if in && !hole {
				want = 255
			}
			if got := m.at(x, y); got != want {
				t.Fatalf("at(%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestStrokeJoins(t *testing.T) {
	p := &Path{}
	p.MoveTo(4, 30)
	p.LineTo(32, 30)
	p.LineTo(4, 20)

	s := Stroke{Width: 4, MiterLimit: 10, Join: JoinMiter}
	miter := s.Outline(p, 1).Bounds(Identity)
	if ratio := (miter.X1 - 32) / 2; ratio < 5 || ratio > 6 {
		t.Fatalf("miter reaches %v past the vertex, want about 5.8 half-widths", ratio)
	}

	s.Join = JoinBevel
	bevel := s.Outline(p, 1).Bounds(Identity)
	if bevel.X1 >= miter.X1 {
		t.Fatalf("bevel reaches %v, miter %v", bevel.X1, miter.X1)
	}

	s.Join = JoinMiter
	s.MiterLimit = 2
	if limited := s.Outline(p, 1).Bounds(Identity); limited != bevel {
		t.Fatalf("miter over the limit gave %+v, want the bevel %+v", limited, bevel)
	}

	s.Join = JoinRound
	s.MiterLimit = 10
	round := s.Outline(p, 1).Bounds(Identity)
	if round.X1 > 34 || round.X1 < 34-arcTolerance {
		t.Fatalf("round join reaches %v, want just under 34", round.X1)
	}
}

func TestStrokeHairline(t *testing.T) {
	for _, w := range []float32{0, 0.01, 0.5} {
		s := Stroke{Width: w, MiterLimit: 10}
		m := strokeMask(16, 16, &s, line(2, 4.5, 10, 4.5))
		if got := m.at(5, 4); got != 255 {
			t.Fatalf("width %v: hairline pixel = %d, want 255", w, got)
		}
		if got := float64(m.sum()) / 255; math.Abs(got-8) > 0.01 {
			t.Fatalf("width %v: hairline area = %.2f, want 8", w, got)
		}
	}
}

func TestStrokeScaleHairline(t *testing.T) {
	s := Stroke{Width: 0.02, MiterLimit: 10}
	o := s.Outline(line(0, 0, 10, 0), 100)
	if got := o.Bounds(Identity); math.Abs(float64(got.Y1-got.Y0)-0.02) > 1e-4 {
		t.Fatalf("width at scale 100 = %v, want 0.02", got.Y1-got.Y0)
	}
}

func TestStrokeDegenerate(t *testing.T) {
	p := &Path{}
	p.MoveTo(8, 8)
	p.LineTo(8, 8)

	s := Stroke{Width: 4, MiterLimit: 10}
	s.SetCaps(CapButt)
	if m := strokeMask(16, 16, &s, p); m.sum() != 0 {
		t.Fatal("butt cap drew a dot")
	}
	s.SetCaps(CapRound)
	m := strokeMask(16, 16, &s, p)
	got := float64(m.sum()) / 255
	if got > math.Pi*4 || got < math.Pi*4*0.9 {
		t.Fatalf("round dot area = %.2f, want just under %.2f", got, math.Pi*4)
	}
	s.SetCaps(CapSquare)
	m = strokeMask(16, 16, &s, p)
	if got := m.sum() / 255; got != 16 {
		t.Fatalf("square dot area = %d, want 16", got)
	}

	q := &Path{}
	q.MoveTo(8, 8)
	q.Close()
	s.SetCaps(CapSquare)
	if m := strokeMask(16, 16, &s, q); m.sum() != 0 {
		t.Fatal("square cap drew a closed degenerate subpath")
	}
}

func TestStrokeDashLengths(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10, Dash: []float32{4, 4}}
	m := strokeMask(32, 16, &s, line(0, 4, 32, 4))
	for x := 0; x < 32; x++ {
		want := uint8(0)
		if x%8 < 4 {
			want = 255
		}
		if got := m.at(x, 4); got != want {
			t.Fatalf("dash at x=%d is %d, want %d", x, got, want)
		}
	}
}

func TestStrokeDashPhase(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10, Dash: []float32{4, 4}, DashPhase: 4}
	m := strokeMask(32, 16, &s, line(0, 4, 32, 4))
	if m.at(0, 4) != 0 || m.at(4, 4) != 255 {
		t.Fatal("dash phase ignored")
	}
	s.DashPhase = -4
	m = strokeMask(32, 16, &s, line(0, 4, 32, 4))
	if m.at(0, 4) != 0 || m.at(4, 4) != 255 {
		t.Fatal("negative dash phase not normalized")
	}
}

func TestStrokeDashOddArray(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10, Dash: []float32{4}}
	m := strokeMask(32, 16, &s, line(0, 4, 32, 4))
	for x := 0; x < 32; x++ {
		want := uint8(0)
		if x%8 < 4 {
			want = 255
		}
		if got := m.at(x, 4); got != want {
			t.Fatalf("odd dash at x=%d is %d, want %d", x, got, want)
		}
	}
}

func TestStrokeDashSolid(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10, Dash: []float32{4, 0}}
	m := strokeMask(32, 16, &s, line(0, 4, 32, 4))
	if m.sum() != 32*2*255 {
		t.Fatalf("dash with no gaps covered %d, want %d", m.sum()/255, 64)
	}
}

func TestStrokeDashClosedStaysClosed(t *testing.T) {
	p := &Path{}
	p.Rect(4, 4, 8, 8)
	s := Stroke{Width: 2, MiterLimit: 10, Dash: []float32{100, 4}}
	m := strokeMask(24, 24, &s, p)
	if m.at(3, 3) != 255 || m.at(12, 12) != 255 {
		t.Fatal("dashed closed rectangle lost its corners")
	}
}

func TestStrokeEmpty(t *testing.T) {
	s := Stroke{Width: 2, MiterLimit: 10}
	if o := s.Outline(&Path{}, 1); !o.IsEmpty() {
		t.Fatal("empty path stroked to something")
	}
}

// TestAGGStrokeFixtures replays testdata/aggstroke.tsv and testdata/aggdash.tsv,
// the coverage AGG's own stroker and dasher produce. CORPUS.md §4a.
func TestAGGStrokeFixtures(t *testing.T) {
	for _, name := range []string{"aggstroke.tsv", "aggdash.tsv"} {
		t.Run(name, func(t *testing.T) { replayStrokes(t, name) })
	}
}

func replayStrokes(t *testing.T, name string) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "testdata", name))
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
		if len(field) != 11 && len(field) != 13 {
			t.Fatalf("line %d has %d fields", line, len(field))
		}
		w, _ := strconv.Atoi(field[0])
		h, _ := strconv.Atoi(field[1])
		width, _ := strconv.ParseFloat(field[3], 32)
		miter, _ := strconv.ParseFloat(field[6], 32)

		p := &Path{}
		for i, pt := range strings.Fields(field[2]) {
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
				p.MoveTo(float32(x), float32(y))
			} else {
				p.LineTo(float32(x), float32(y))
			}
		}
		if field[7] == "1" {
			p.Close()
		}

		s := Stroke{Width: float32(width), MiterLimit: float32(miter)}
		switch field[4] {
		case "r":
			s.Join = JoinRound
		case "b":
			s.Join = JoinBevel
		}
		switch field[5] {
		case "r":
			s.SetCaps(CapRound)
		case "s":
			s.SetCaps(CapSquare)
		}
		if len(field) == 13 {
			for _, d := range strings.Split(field[8], ",") {
				v, err := strconv.ParseFloat(d, 32)
				if err != nil {
					t.Fatalf("line %d: bad dash %q", line, d)
				}
				s.Dash = append(s.Dash, float32(v))
			}
			phase, _ := strconv.ParseFloat(field[9], 32)
			s.DashPhase = float32(phase)
		}

		r := NewRasterizer(w, h)
		r.AddPath(s.Outline(p, 1), Identity)
		m := newMask(w, h)
		r.Sweep(NonZero, m)

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
		if want := strings.Join(field[len(field)-3:], "\t"); got != want {
			t.Errorf("line %d: coverage %s, AGG says %s\n  %s", line, got, want, text)
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

func BenchmarkStroke(b *testing.B) {
	p := benchPath(512, 512)
	s := Stroke{Width: 3, MiterLimit: 10, Join: JoinRound}
	s.SetCaps(CapRound)
	b.ResetTimer()
	for b.Loop() {
		s.Outline(p, 1)
	}
}

func BenchmarkStrokeDashed(b *testing.B) {
	p := benchPath(512, 512)
	s := Stroke{Width: 3, MiterLimit: 10, Dash: []float32{6, 4}, DashPhase: 1}
	b.ResetTimer()
	for b.Loop() {
		s.Outline(p, 1)
	}
}

func TestStrokeTriangleCap(t *testing.T) {
	s := Stroke{Width: 4, MiterLimit: 10}
	s.SetCaps(CapTriangle)
	m := strokeMask(24, 16, &s, line(4, 8, 20, 8))
	if m.at(2, 8) == 0 || m.at(1, 8) != 0 {
		t.Fatal("triangle cap does not reach half the width past the end")
	}
	if m.at(2, 6) != 0 {
		t.Fatalf("triangle cap is square at (2,6) = %d", m.at(2, 6))
	}
	if got, want := float64(m.sum())/255, 16.0*4+2*(4*2/2.0); math.Abs(got-want) > 0.05 {
		t.Fatalf("triangle capped area = %.2f, want %.2f", got, want)
	}

	p := &Path{}
	p.MoveTo(8, 8)
	p.LineTo(8, 8)
	if m := strokeMask(16, 16, &s, p); m.sum() != 0 {
		t.Fatal("triangle cap drew a dot")
	}
}
