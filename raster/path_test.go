package raster

import (
	"fmt"
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
