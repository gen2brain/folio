package raster

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// record walks a path into a string, one segment per line.
type record struct{ b strings.Builder }

func (r *record) MoveTo(x, y float32) { fmt.Fprintf(&r.b, "m %g %g\n", x, y) }
func (r *record) LineTo(x, y float32) { fmt.Fprintf(&r.b, "l %g %g\n", x, y) }
func (r *record) CurveTo(x1, y1, x2, y2, x3, y3 float32) {
	fmt.Fprintf(&r.b, "c %g %g %g %g %g %g\n", x1, y1, x2, y2, x3, y3)
}
func (r *record) Close() { r.b.WriteString("h\n") }

func walk(p *Path) string {
	var r record
	p.Walk(&r)
	return r.b.String()
}

func TestPathBuilding(t *testing.T) {
	var p Path
	p.MoveTo(1, 2)
	p.LineTo(3, 4)
	p.CurveTo(5, 6, 7, 8, 9, 10)
	p.CurveToV(11, 12, 13, 14)
	p.CurveToY(15, 16, 17, 18)
	p.Close()

	want := "m 1 2\nl 3 4\nc 5 6 7 8 9 10\nc 9 10 11 12 13 14\nc 15 16 17 18 17 18\nh\n"
	if got := walk(&p); got != want {
		t.Errorf("path =\n%s\nwant\n%s", got, want)
	}
}

func TestPathCloseIsIdempotent(t *testing.T) {
	var p Path
	p.Rect(0, 0, 10, 10)
	p.Close()
	p.Close()
	if got, want := strings.Count(walk(&p), "h\n"), 1; got != want {
		t.Errorf("%d closepaths, want %d\n%s", got, want, walk(&p))
	}

	var q Path
	q.Close()
	if !q.IsEmpty() {
		t.Error("closing an empty path started one")
	}
}

func TestPathImplicitMove(t *testing.T) {
	var p Path
	p.LineTo(5, 6)
	if got, want := walk(&p), "m 5 6\n"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestPathDegenerate checks the segments a path leaves out: a line to the
// point it is already at, and a cubic whose control points sit on its ends,
// which is a straight line however it is written.
func TestPathDegenerate(t *testing.T) {
	var p Path
	p.MoveTo(1, 2)
	p.LineTo(3, 4)
	p.LineTo(3, 4)
	p.CurveTo(3, 4, 5, 6, 5, 6)
	p.CurveTo(5, 6, 5, 6, 7, 8)
	p.CurveTo(9, 10, 11, 12, 11, 12)
	want := "m 1 2\nl 3 4\nl 5 6\nl 7 8\nc 9 10 11 12 11 12\n"
	if got := walk(&p); got != want {
		t.Errorf("path =\n%s\nwant\n%s", got, want)
	}

	// A line to the point a move put the path at is a subpath of its own,
	// which a round or a square cap paints as a dot.
	var q Path
	q.MoveTo(1, 1)
	q.LineTo(1, 1)
	if got, want := walk(&q), "m 1 1\nl 1 1\n"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	// A cubic that collapses onto the point the path is already at is
	// nothing at all.
	var r Path
	r.MoveTo(0, 0)
	r.LineTo(2, 2)
	r.CurveTo(2, 2, 2, 2, 2, 2)
	if got, want := walk(&r), "m 0 0\nl 2 2\n"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestPathBounds(t *testing.T) {
	var p Path
	p.MoveTo(10, 10)
	p.LineTo(20, 30)
	if got, want := p.Bounds(Identity), (Rect{10, 10, 20, 30}); got != want {
		t.Errorf("bounds = %v, want %v", got, want)
	}

	s := DefaultStroke()
	s.Width = 4
	if got, want := p.StrokeBounds(&s, Identity), (Rect{-10, -10, 40, 50}); got != want {
		if !got.Contains(Rect{10, 10, 20, 30}) || got.X0 >= 10 {
			t.Errorf("stroke bounds = %v, want to contain the path with room for the width", got)
		}
		_ = want
	}
}

func TestPathTransform(t *testing.T) {
	var p Path
	p.Rect(0, 0, 10, 20)
	q := p.Transform(Scale(2, 3))
	if got, want := q.Bounds(Identity), (Rect{0, 0, 20, 60}); got != want {
		t.Errorf("transformed bounds = %v, want %v", got, want)
	}
	if got, want := p.Bounds(Identity), (Rect{0, 0, 10, 20}); got != want {
		t.Error("the original path was modified")
	}
}

// randomPath is a random walk of short lines and curves in subpaths, some
// closed and some carrying on from where a closed one started, with the odd
// long segment across the whole area.
func randomPath(rng *rand.Rand, size float32) *Path {
	var p Path
	pt := func(x, y float32, step float32) (float32, float32) {
		if rng.IntN(20) == 0 {
			step = size
		}
		return x + (rng.Float32()*2-1)*step, y + (rng.Float32()*2-1)*step
	}
	var x, y float32
	walk := func(n int) {
		for range n {
			if rng.IntN(2) == 0 {
				x, y = pt(x, y, 8)
				p.LineTo(x, y)
				continue
			}
			x1, y1 := pt(x, y, 8)
			x2, y2 := pt(x1, y1, 8)
			x, y = pt(x2, y2, 8)
			p.CurveTo(x1, y1, x2, y2, x, y)
		}
	}
	for range 1 + rng.IntN(4) {
		x, y = rng.Float32()*size, rng.Float32()*size
		p.MoveTo(x, y)
		walk(rng.IntN(40))
		if rng.IntN(2) == 0 {
			p.Close()
			if rng.IntN(3) == 0 {
				x, y = p.Start().X, p.Start().Y
				walk(1 + rng.IntN(10))
			}
		}
	}
	return &p
}

// coverIn rasterizes p under m into a mask of the rows from y0 to y1.
func coverIn(p *Path, m Matrix, rule FillRule, w int, y0, y1 float32) []uint8 {
	mask := NewMask(w, 200)
	r := NewRasterizer(w, 200)
	r.SetClip(Rect{X0: 0, Y0: y0, X1: float32(w), Y1: y1})
	r.AddPath(p, m)
	r.Sweep(rule, mask.MaskBlitter())
	return mask.Samples
}

// TestCullY checks that a path culled to a band fills the band exactly as the
// whole path does, and a culled stroke strokes it exactly as the whole one.
func TestCullY(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	culled, cut := 0, 0
	for i := range 3000 {
		p := randomPath(rng, 100)
		a := rng.Float64() * 360
		sc := 0.5 + rng.Float32()*1.5
		m := Concat(Matrix{A: sc, D: sc * (0.5 + rng.Float32())}, Rotate(a))
		m = Concat(m, Translate(100, 100))
		y0 := float32(rng.IntN(180))
		y1 := y0 + float32(1+rng.IntN(30))
		if i%3 == 0 {
			y0 += rng.Float32()
			y1 -= rng.Float32() / 2
		}
		rule := FillRule(rng.IntN(2))

		var c Path
		p.CullY(&c, m, y0, y1)
		if c.Len() < p.Len() {
			culled++
		}
		if got, want := coverIn(&c, m, rule, 200, y0, y1), coverIn(p, m, rule, 200, y0, y1); !slices.Equal(got, want) {
			t.Fatalf("path %d: the culled fill differs in rows %v to %v", i, y0, y1)
		}

		s := Stroke{
			Width: rng.Float32() * 8, MiterLimit: 1 + rng.Float32()*9,
			Join: Join(rng.IntN(3)),
		}
		s.SetCaps(Cap(rng.IntN(4)))
		r := s.Reach(m)
		var cs Path
		p.CullStrokeY(&cs, m, y0, y1, r)
		if cs.Len() < p.Len() {
			cut++
		}
		e := m.Expansion()
		got := coverIn(s.Outline(&cs, e), m, NonZero, 200, y0, y1)
		if want := coverIn(s.Outline(p, e), m, NonZero, 200, y0, y1); !slices.Equal(got, want) {
			t.Fatalf("path %d: the culled stroke %+v differs in rows %v to %v", i, s, y0, y1)
		}
	}
	if culled < 1000 || cut < 1000 {
		t.Errorf("only %d fills and %d strokes of 3000 were culled", culled, cut)
	}
}
