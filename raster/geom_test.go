package raster

import "testing"

func TestMatrixConcat(t *testing.T) {
	m := Concat(Translate(10, 20), Scale(2, 3))
	if got, want := m.Apply(Point{1, 1}), (Point{22, 63}); got != want {
		t.Errorf("translate then scale = %v, want %v", got, want)
	}
	m = Concat(Scale(2, 3), Translate(10, 20))
	if got, want := m.Apply(Point{1, 1}), (Point{12, 23}); got != want {
		t.Errorf("scale then translate = %v, want %v", got, want)
	}
}

func TestMatrixInvert(t *testing.T) {
	m := Concat(Concat(Scale(2, 3), Rotate(30)), Translate(5, 7))
	inv, ok := m.Invert()
	if !ok {
		t.Fatal("not invertible")
	}
	p := Point{3, 4}
	q := inv.Apply(m.Apply(p))
	if absf(q.X-p.X) > 1e-4 || absf(q.Y-p.Y) > 1e-4 {
		t.Errorf("round trip = %v, want %v", q, p)
	}
	if _, ok := (Matrix{A: 1, B: 2, C: 2, D: 4}).Invert(); ok {
		t.Error("a singular matrix reported as invertible")
	}
}

func TestRotateExact(t *testing.T) {
	for _, tc := range []struct {
		deg  float64
		want Matrix
	}{
		{0, Identity},
		{90, Matrix{0, 1, -1, 0, 0, 0}},
		{180, Matrix{-1, 0, 0, -1, 0, 0}},
		{270, Matrix{0, -1, 1, 0, 0, 0}},
		{-90, Matrix{0, -1, 1, 0, 0, 0}},
		{450, Matrix{0, 1, -1, 0, 0, 0}},
	} {
		if got := Rotate(tc.deg); got != tc.want {
			t.Errorf("Rotate(%g) = %v, want %v", tc.deg, got, tc.want)
		}
	}
}

func TestRectAccumulation(t *testing.T) {
	r := EmptyRect.AddPoint(Point{5, 5})
	if r != (Rect{5, 5, 5, 5}) {
		t.Fatalf("first point = %v", r)
	}
	r = r.AddPoint(Point{1, 9})
	if r != (Rect{1, 5, 5, 9}) {
		t.Errorf("second point = %v", r)
	}

	m := Matrix{A: 1, D: -1}
	if got, want := m.ApplyRect(Rect{0, 0, 200, 100}), (Rect{0, -100, 200, 0}); got != want {
		t.Errorf("flipped rect = %v, want %v", got, want)
	}
}

func TestRectOps(t *testing.T) {
	a := Rect{0, 0, 10, 10}
	b := Rect{5, 5, 20, 20}
	if got, want := a.Intersect(b), (Rect{5, 5, 10, 10}); got != want {
		t.Errorf("intersect = %v", got)
	}
	if got, want := a.Union(b), (Rect{0, 0, 20, 20}); got != want {
		t.Errorf("union = %v", got)
	}
	if !a.Intersect(Rect{50, 50, 60, 60}).IsEmpty() {
		t.Error("disjoint rectangles intersect")
	}
	if got := a.Union(EmptyRect); got != a {
		t.Errorf("union with empty = %v", got)
	}
	if !InfiniteRect.Contains(a) {
		t.Error("the infinite rectangle contains nothing")
	}
}
