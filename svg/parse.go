package svg

import (
	"math"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/raster"
)

// scanner reads the small grammars an SVG attribute is written in: a list of
// numbers, a transform, a path. They all separate their tokens with spaces
// and commas and none of them needs more than one token of lookahead.
type scanner struct {
	s string
	i int
}

func (p *scanner) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\r', '\n', '\f':
			p.i++
		default:
			return
		}
	}
}

// sep steps over the whitespace and the one comma a list may put between two
// numbers.
func (p *scanner) sep() {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
		p.space()
	}
}

func (p *scanner) done() bool {
	p.space()
	return p.i >= len(p.s)
}

// number reads one number in the form SVG writes it, which is C's without a
// hexadecimal or an infinity and with a leading dot allowed.
func (p *scanner) number() (float32, bool) {
	p.space()
	start := p.i
	if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
		p.i++
	}
	digits := p.digits()
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		digits = p.digits() || digits
	}
	if !digits {
		p.i = start
		return 0, false
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		mark := p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if !p.digits() {
			p.i = mark
		}
	}
	v, err := strconv.ParseFloat(p.s[start:p.i], 32)
	if err != nil {
		p.i = start
		return 0, false
	}
	return float32(v), true
}

func (p *scanner) digits() bool {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	return p.i > start
}

// flag reads the one digit an arc writes its two flags as, which may be run
// together with what follows it.
func (p *scanner) flag() (bool, bool) {
	p.sep()
	if p.i < len(p.s) && (p.s[p.i] == '0' || p.s[p.i] == '1') {
		v := p.s[p.i] == '1'
		p.i++
		return v, true
	}
	return false, false
}

// name reads an identifier, which introduces a transform.
func (p *scanner) name() string {
	p.space()
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			p.i++
			continue
		}
		break
	}
	return p.s[start:p.i]
}

// lengths reads a list of lengths, which is what the positions a text element
// gives each of its characters are written as.
func lengths(s string, ref, em float32) []float32 {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]float32, 0, len(f))
	for _, t := range f {
		v, ok := length(t, ref, em)
		if !ok {
			return out
		}
		out = append(out, v)
	}
	return out
}

// numberList is a filter's own list of plain numbers, which is nothing at all
// when one of them carries a unit.
func numberList(s string) []float32 {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]float32, 0, len(f))
	for _, t := range f {
		v, err := strconv.ParseFloat(t, 32)
		if err != nil {
			return nil
		}
		out = append(out, float32(v))
	}
	return out
}

// numbers reads a whole attribute as a list of numbers, which is what points
// and the dash pattern are.
func numbers(s string) []float32 {
	p := &scanner{s: s}
	var out []float32
	for {
		v, ok := p.number()
		if !ok {
			return out
		}
		out = append(out, v)
		p.sep()
	}
}

// length is a number with a unit after it. A percentage needs to know what it
// is a percentage of, which the caller passes as ref.
func length(s string, ref, em float32) (float32, bool) {
	s = strings.TrimSpace(s)
	p := &scanner{s: s}
	v, ok := p.number()
	if !ok {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(s[p.i:])) {
	case "", "px":
		return v, true
	case "%":
		return v * ref / 100, true
	case "pt":
		return v * 96 / 72, true
	case "pc":
		return v * 16, true
	case "in":
		return v * 96, true
	case "cm":
		return v * 96 / 2.54, true
	case "mm":
		return v * 96 / 25.4, true
	case "em":
		return v * em, true
	case "ex":
		return v * em / 2, true
	}
	return 0, false
}

// transform reads the transform attribute, which is a list of the six
// functions of SVG 1.1 7.6 applied left to right.
func transform(s string) raster.Matrix {
	m := raster.Identity
	p := &scanner{s: s}
	for !p.done() {
		name := p.name()
		if name == "" {
			return m
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '(' {
			return m
		}
		p.i++
		var v []float32
		for {
			n, ok := p.number()
			if !ok {
				break
			}
			v = append(v, n)
			p.sep()
		}
		p.space()
		if p.i < len(p.s) && p.s[p.i] == ')' {
			p.i++
		}
		p.sep()

		var t raster.Matrix
		switch name {
		case "matrix":
			if len(v) != 6 {
				continue
			}
			t = raster.Matrix{A: v[0], B: v[1], C: v[2], D: v[3], E: v[4], F: v[5]}
		case "translate":
			switch len(v) {
			case 1:
				t = raster.Translate(v[0], 0)
			case 2:
				t = raster.Translate(v[0], v[1])
			default:
				continue
			}
		case "scale":
			switch len(v) {
			case 1:
				t = raster.Scale(v[0], v[0])
			case 2:
				t = raster.Scale(v[0], v[1])
			default:
				continue
			}
		case "rotate":
			switch len(v) {
			case 1:
				t = raster.Rotate(float64(v[0]))
			case 3:
				t = raster.Concat(raster.Rotate(float64(v[0])), raster.Translate(v[1], v[2]))
				t = raster.Concat(raster.Translate(-v[1], -v[2]), t)
			default:
				continue
			}
		case "skewX":
			if len(v) != 1 {
				continue
			}
			t = raster.Matrix{A: 1, D: 1, C: tan32(v[0])}
		case "skewY":
			if len(v) != 1 {
				continue
			}
			t = raster.Matrix{A: 1, D: 1, B: tan32(v[0])}
		default:
			continue
		}
		m = raster.Concat(t, m)
	}
	return m
}

func tan32(deg float32) float32 {
	return float32(math.Tan(float64(deg) * math.Pi / 180))
}

// atOrigin turns a transform about the point transform-origin names instead of
// about the origin, CSS Transforms 1 section 8. The default reference box of
// an SVG element is its viewport, so a keyword and a percentage are a fraction
// of that.
func atOrigin(m raster.Matrix, s string, vw, vh, em float32) raster.Matrix {
	dx, dy, ok := origin(s, vw, vh, em)
	if !ok || (dx == 0 && dy == 0) {
		return m
	}
	return raster.Concat(raster.Concat(raster.Translate(-dx, -dy), m),
		raster.Translate(dx, dy))
}

// origin reads the one, two or three components transform-origin is written
// as. A pair may be given in either order when both are keywords, and the
// third component is the depth, which a 2D transform has no use for.
func origin(s string, vw, vh, em float32) (float32, float32, bool) {
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 3 {
		return 0, 0, false
	}
	x, y := "center", "center"
	if len(f) == 1 {
		if down(f[0]) {
			y = f[0]
		} else {
			x = f[0]
		}
	} else {
		x, y = f[0], f[1]
		if down(x) || across(y) {
			x, y = y, x
		}
	}
	dx, ok := originAxis(x, vw, em, false)
	if !ok {
		return 0, 0, false
	}
	dy, ok := originAxis(y, vh, em, true)
	if !ok {
		return 0, 0, false
	}
	return dx, dy, true
}

func across(s string) bool {
	s = strings.ToLower(s)
	return s == "left" || s == "right"
}

func down(s string) bool {
	s = strings.ToLower(s)
	return s == "top" || s == "bottom"
}

// originAxis is one component of transform-origin along one axis, which is a
// length, a percentage of the viewport, or a keyword naming one of its edges.
func originAxis(s string, ref, em float32, vertical bool) (float32, bool) {
	switch strings.ToLower(s) {
	case "center":
		return ref / 2, true
	case "left", "right":
		if vertical {
			return 0, false
		}
		if s[0] == 'l' || s[0] == 'L' {
			return 0, true
		}
		return ref, true
	case "top", "bottom":
		if !vertical {
			return 0, false
		}
		if s[0] == 't' || s[0] == 'T' {
			return 0, true
		}
		return ref, true
	}
	return length(s, ref, em)
}

// viewport is what viewBox and preserveAspectRatio put between the coordinates
// an element is drawn in and the box it is drawn into, SVG 1.1 7.7.
func viewport(box []float32, align string, slice bool, w, h float32) raster.Matrix {
	if len(box) != 4 || box[2] <= 0 || box[3] <= 0 || w <= 0 || h <= 0 {
		return raster.Identity
	}
	sx, sy := w/box[2], h/box[3]
	if align != "none" {
		s := min(sx, sy)
		if slice {
			s = max(sx, sy)
		}
		sx, sy = s, s
	}
	tx, ty := -box[0]*sx, -box[1]*sy
	switch {
	case strings.HasPrefix(align, "xMid"):
		tx += (w - box[2]*sx) / 2
	case strings.HasPrefix(align, "xMax"):
		tx += w - box[2]*sx
	}
	switch {
	case strings.HasSuffix(align, "YMid"):
		ty += (h - box[3]*sy) / 2
	case strings.HasSuffix(align, "YMax"):
		ty += h - box[3]*sy
	}
	return raster.Concat(raster.Scale(sx, sy), raster.Translate(tx, ty))
}

// aspect splits preserveAspectRatio into the alignment and whether the view
// covers the box rather than fitting inside it.
func aspect(s string) (align string, slice bool) {
	align = "xMidYMid"
	for _, f := range strings.Fields(s) {
		switch f {
		case "defer":
		case "none", "xMinYMin", "xMidYMin", "xMaxYMin",
			"xMinYMid", "xMidYMid", "xMaxYMid",
			"xMinYMax", "xMidYMax", "xMaxYMax":
			align = f
		case "slice":
			slice = true
		case "meet":
			slice = false
		}
	}
	return align, slice
}
