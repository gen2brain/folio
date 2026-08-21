// Package raster is a two dimensional graphics engine: paths, curve
// flattening, an anti-aliased cell rasterizer with both fill rules, a stroker
// and a dasher, clip masks, pixmaps of one to four color components with an
// optional alpha channel, and the span pipeline over them.
//
// It knows nothing about PDF. A color space reaches it as Model, which is
// three methods: how many components, and how to get to and from RGB.
package raster

import "math"

// Point is a position in whatever space the surrounding code is working in.
type Point struct{ X, Y float32 }

// Rect is an axis aligned rectangle. X0,Y0 is the lower left corner in PDF
// user space and the upper left in device space; nothing here cares which.
type Rect struct{ X0, Y0, X1, Y1 float32 }

// InfiniteRect is the rectangle that contains everything, used where a
// scissor is required but nothing is clipped.
var InfiniteRect = Rect{-1e20, -1e20, 1e20, 1e20}

// EmptyRect contains nothing. Union with it is the identity.
var EmptyRect = Rect{1e20, 1e20, -1e20, -1e20}

// IsEmpty reports whether r contains no area.
func (r Rect) IsEmpty() bool { return r.X0 >= r.X1 || r.Y0 >= r.Y1 }

// IsInfinite reports whether r is the everything rectangle.
func (r Rect) IsInfinite() bool { return r == InfiniteRect }

// Normalized returns r with its corners in the expected order.
func (r Rect) Normalized() Rect {
	if r.X0 > r.X1 {
		r.X0, r.X1 = r.X1, r.X0
	}
	if r.Y0 > r.Y1 {
		r.Y0, r.Y1 = r.Y1, r.Y0
	}
	return r
}

// Union returns the smallest rectangle containing both.
func (r Rect) Union(s Rect) Rect {
	if r.IsEmpty() {
		return s
	}
	if s.IsEmpty() {
		return r
	}
	return Rect{min32(r.X0, s.X0), min32(r.Y0, s.Y0), max32(r.X1, s.X1), max32(r.Y1, s.Y1)}
}

// Intersect returns the overlap of two rectangles.
func (r Rect) Intersect(s Rect) Rect {
	if r.IsEmpty() || s.IsEmpty() {
		return EmptyRect
	}
	out := Rect{max32(r.X0, s.X0), max32(r.Y0, s.Y0), min32(r.X1, s.X1), min32(r.Y1, s.Y1)}
	if out.IsEmpty() {
		return EmptyRect
	}
	return out
}

// AddPoint returns the smallest rectangle containing r and p. It is min and
// max with no emptiness test, so that accumulating from EmptyRect works and a
// rectangle that has collapsed to a line or a point still grows correctly.
func (r Rect) AddPoint(p Point) Rect {
	return Rect{min32(r.X0, p.X), min32(r.Y0, p.Y), max32(r.X1, p.X), max32(r.Y1, p.Y)}
}

// Outer returns the whole pixels r covers, and zeroes when it is empty.
func (r Rect) Outer() (x0, y0, x1, y1 int) {
	if r.IsEmpty() {
		return 0, 0, 0, 0
	}
	return int(math.Floor(float64(r.X0))), int(math.Floor(float64(r.Y0))),
		int(math.Ceil(float64(r.X1))), int(math.Ceil(float64(r.Y1)))
}

// Contains reports whether s lies entirely within r.
func (r Rect) Contains(s Rect) bool {
	if s.IsEmpty() {
		return true
	}
	return !r.IsEmpty() && s.X0 >= r.X0 && s.Y0 >= r.Y0 && s.X1 <= r.X1 && s.Y1 <= r.Y1
}

// Matrix is an affine transform in PDF order: the six numbers of the cm
// operator, applied to a row vector, so x' = a*x + c*y + e.
type Matrix struct{ A, B, C, D, E, F float32 }

// Identity leaves a point where it is.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Translate moves by x and y.
func Translate(x, y float32) Matrix { return Matrix{1, 0, 0, 1, x, y} }

// Scale scales about the origin.
func Scale(x, y float32) Matrix { return Matrix{x, 0, 0, y, 0, 0} }

// Rotate turns by an angle in degrees, counterclockwise.
func Rotate(deg float64) Matrix {
	switch d := math.Mod(deg, 360); {
	case d < 0:
		return Rotate(deg + 360)
	case d == 0:
		return Identity
	case d == 90:
		return Matrix{0, 1, -1, 0, 0, 0}
	case d == 180:
		return Matrix{-1, 0, 0, -1, 0, 0}
	case d == 270:
		return Matrix{0, -1, 1, 0, 0, 0}
	default:
		s, c := math.Sincos(deg * math.Pi / 180)
		return Matrix{float32(c), float32(s), float32(-s), float32(c), 0, 0}
	}
}

// Concat returns the transform that applies m and then n.
func Concat(m, n Matrix) Matrix {
	return Matrix{
		float32(m.A*n.A) + float32(m.B*n.C),
		float32(m.A*n.B) + float32(m.B*n.D),
		float32(m.C*n.A) + float32(m.D*n.C),
		float32(m.C*n.B) + float32(m.D*n.D),
		float32(float32(m.E*n.A)+float32(m.F*n.C)) + n.E,
		float32(float32(m.E*n.B)+float32(m.F*n.D)) + n.F,
	}
}

// Apply transforms a point.
func (m Matrix) Apply(p Point) Point {
	return Point{
		float32(float32(m.A*p.X)+float32(m.C*p.Y)) + m.E,
		float32(float32(m.B*p.X)+float32(m.D*p.Y)) + m.F,
	}
}

// ApplyRect returns the bounding box of the transformed rectangle.
func (m Matrix) ApplyRect(r Rect) Rect {
	if r.IsInfinite() {
		return r
	}
	out := EmptyRect
	for _, p := range [4]Point{{r.X0, r.Y0}, {r.X1, r.Y0}, {r.X0, r.Y1}, {r.X1, r.Y1}} {
		out = out.AddPoint(m.Apply(p))
	}
	return out
}

// Det is the determinant, zero when the transform collapses to a line.
func (m Matrix) Det() float32 { return float32(m.A*m.D) - float32(m.B*m.C) }

// Invert returns the inverse transform. A singular matrix inverts to the
// identity, which keeps a degenerate CTM from taking coordinates to infinity.
func (m Matrix) Invert() (Matrix, bool) {
	det := m.Det()
	if det > -1e-9 && det < 1e-9 {
		return Identity, false
	}
	r := 1 / det
	return Matrix{
		m.D * r, -m.B * r,
		-m.C * r, m.A * r,
		(float32(m.C*m.F) - float32(m.D*m.E)) * r,
		(float32(m.B*m.E) - float32(m.A*m.F)) * r,
	}, true
}

// Expansion is the square root of the absolute determinant: how much the
// transform scales lengths on average. Line width and flatness are measured
// with it.
func (m Matrix) Expansion() float32 {
	return float32(math.Sqrt(math.Abs(float64(m.Det()))))
}

// MaxExpansion is the largest factor any direction is scaled by.
func (m Matrix) MaxExpansion() float32 {
	max := absf(m.A)
	for _, v := range [3]float32{m.B, m.C, m.D} {
		if a := absf(v); a > max {
			max = a
		}
	}
	return max
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
