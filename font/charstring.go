package font

import (
	"strconv"

	"github.com/gen2brain/folio/raster"
)

const (
	maxCharstringDepth = 10
	maxCharstringSteps = 1 << 16
)

// t2 runs a Type 2 charstring, the format CFF uses.
type t2 struct {
	f     *cffFont
	p     raster.Path
	x, y  float32
	stack [48]float32
	sp    int

	subrs  [][]byte
	nStems int
	width  float32
	nomW   float32
	hasW   bool
	open   bool
	steps  int
	trans  [32]float32
}

// path builds the outline of a CFF glyph.
func (c *cffFont) path(gid int) *raster.Path {
	if gid < 0 || gid >= len(c.charStrings) {
		return nil
	}
	subrs, defW, nomW := c.privateFor(gid)
	t := &t2{f: c, subrs: subrs, width: defW, nomW: nomW}
	t.run(c.charStrings[gid], 0)
	t.closeContour()
	if m, ok := c.matrixFor(gid); ok {
		return t.p.Transform(m)
	}
	return &t.p
}

// advance returns the width a CFF charstring declares, which is only needed
// when the PDF font dictionary has none.
func (c *cffFont) advance(gid int) float32 {
	if gid < 0 || gid >= len(c.charStrings) {
		return 0
	}
	subrs, defW, nomW := c.privateFor(gid)
	t := &t2{f: c, subrs: subrs, width: defW, nomW: nomW}
	t.run(c.charStrings[gid], 0)
	return t.width
}

func (t *t2) push(v float32) {
	if t.sp < len(t.stack) {
		t.stack[t.sp] = v
		t.sp++
	}
}

func (t *t2) clear() { t.sp = 0 }

// takeWidth notes the optional leading width argument, which the first stack
// clearing operator carries when it sees one operand more than it uses.
func (t *t2) takeWidth(want int) {
	if t.hasW {
		return
	}
	t.hasW = true
	if t.sp > want {
		t.width = t.nomW + t.stack[0]
		copy(t.stack[:], t.stack[1:t.sp])
		t.sp--
	}
}

// takeWidthEven is the same for the stem operators, which take pairs.
func (t *t2) takeWidthEven() {
	if t.hasW {
		return
	}
	t.hasW = true
	if t.sp%2 == 1 {
		t.width = t.nomW + t.stack[0]
		copy(t.stack[:], t.stack[1:t.sp])
		t.sp--
	}
}

func (t *t2) moveTo(x, y float32) {
	t.closeContour()
	t.p.MoveTo(x, y)
	t.open = true
}

func (t *t2) closeContour() {
	if t.open {
		t.p.Close()
		t.open = false
	}
}

func bias(n int) int {
	switch {
	case n < 1240:
		return 107
	case n < 33900:
		return 1131
	default:
		return 32768
	}
}

func (t *t2) run(code []byte, depth int) {
	if depth > maxCharstringDepth {
		return
	}
	for i := 0; i < len(code); {
		if t.steps++; t.steps > maxCharstringSteps {
			return
		}
		b := code[i]
		switch {
		case b >= 32 || b == 28:
			var v float32
			switch {
			case b == 28:
				if i+3 > len(code) {
					return
				}
				v = float32(int16(be16(code, i+1)))
				i += 3
			case b <= 246:
				v = float32(int(b) - 139)
				i++
			case b <= 250:
				if i+2 > len(code) {
					return
				}
				v = float32((int(b)-247)*256 + int(code[i+1]) + 108)
				i += 2
			case b <= 254:
				if i+2 > len(code) {
					return
				}
				v = float32(-(int(b)-251)*256 - int(code[i+1]) - 108)
				i += 2
			default:
				if i+5 > len(code) {
					return
				}
				v = float32(int32(be32(code, i+1))) / 65536
				i += 5
			}
			t.push(v)
			continue
		}

		op := int(b)
		i++
		if op == 12 {
			if i >= len(code) {
				return
			}
			op = key12(int(code[i]))
			i++
		}

		switch op {
		case 1, 3, 18, 23: // hstem vstem hstemhm vstemhm
			t.takeWidthEven()
			t.nStems += t.sp / 2
			t.clear()

		case 19, 20: // hintmask cntrmask
			t.takeWidthEven()
			t.nStems += t.sp / 2
			t.clear()
			i += (t.nStems + 7) / 8

		case 21: // rmoveto
			t.takeWidth(2)
			if t.sp >= 2 {
				t.x += t.stack[t.sp-2]
				t.y += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()
		case 22: // hmoveto
			t.takeWidth(1)
			if t.sp >= 1 {
				t.x += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()
		case 4: // vmoveto
			t.takeWidth(1)
			if t.sp >= 1 {
				t.y += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()

		case 5: // rlineto
			for j := 0; j+1 < t.sp; j += 2 {
				t.x += t.stack[j]
				t.y += t.stack[j+1]
				t.p.LineTo(t.x, t.y)
			}
			t.clear()
		case 6, 7: // hlineto vlineto
			horiz := op == 6
			for j := 0; j < t.sp; j++ {
				if horiz {
					t.x += t.stack[j]
				} else {
					t.y += t.stack[j]
				}
				t.p.LineTo(t.x, t.y)
				horiz = !horiz
			}
			t.clear()

		case 8: // rrcurveto
			for j := 0; j+5 < t.sp; j += 6 {
				t.curve(t.stack[j], t.stack[j+1], t.stack[j+2], t.stack[j+3], t.stack[j+4], t.stack[j+5])
			}
			t.clear()
		case 24: // rcurveline
			j := 0
			for ; j+5 < t.sp-2; j += 6 {
				t.curve(t.stack[j], t.stack[j+1], t.stack[j+2], t.stack[j+3], t.stack[j+4], t.stack[j+5])
			}
			if j+1 < t.sp {
				t.x += t.stack[j]
				t.y += t.stack[j+1]
				t.p.LineTo(t.x, t.y)
			}
			t.clear()
		case 25: // rlinecurve
			j := 0
			for ; j+1 < t.sp-6; j += 2 {
				t.x += t.stack[j]
				t.y += t.stack[j+1]
				t.p.LineTo(t.x, t.y)
			}
			if j+5 < t.sp {
				t.curve(t.stack[j], t.stack[j+1], t.stack[j+2], t.stack[j+3], t.stack[j+4], t.stack[j+5])
			}
			t.clear()
		case 26, 27: // vvcurveto hhcurveto
			j := 0
			var d float32
			if t.sp%4 == 1 {
				d = t.stack[0]
				j = 1
			}
			for ; j+3 < t.sp; j += 4 {
				if op == 26 {
					t.curve(d, t.stack[j], t.stack[j+1], t.stack[j+2], 0, t.stack[j+3])
				} else {
					t.curve(t.stack[j], d, t.stack[j+1], t.stack[j+2], t.stack[j+3], 0)
				}
				d = 0
			}
			t.clear()
		case 30, 31: // vhcurveto hvcurveto
			horiz := op == 31
			j := 0
			for j+3 < t.sp {
				last := j+8 > t.sp
				var d float32
				if last && j+5 <= t.sp {
					d = t.stack[t.sp-1]
				}
				if horiz {
					t.curve(t.stack[j], 0, t.stack[j+1], t.stack[j+2], d, t.stack[j+3])
				} else {
					t.curve(0, t.stack[j], t.stack[j+1], t.stack[j+2], t.stack[j+3], d)
				}
				horiz = !horiz
				j += 4
			}
			t.clear()

		case key12(35): // flex
			if t.sp >= 13 {
				t.curve(t.stack[0], t.stack[1], t.stack[2], t.stack[3], t.stack[4], t.stack[5])
				t.curve(t.stack[6], t.stack[7], t.stack[8], t.stack[9], t.stack[10], t.stack[11])
			}
			t.clear()
		case key12(34): // hflex
			if t.sp >= 7 {
				y := t.y
				t.curve(t.stack[0], 0, t.stack[1], t.stack[2], t.stack[3], 0)
				t.curve(t.stack[4], 0, t.stack[5], y-t.y+0, t.stack[6], 0)
				t.y = y
			}
			t.clear()
		case key12(36): // hflex1
			if t.sp >= 9 {
				y := t.y
				t.curve(t.stack[0], t.stack[1], t.stack[2], t.stack[3], t.stack[4], 0)
				t.curve(t.stack[5], 0, t.stack[6], t.stack[7], t.stack[8], y-t.y)
			}
			t.clear()
		case key12(37): // flex1
			if t.sp >= 11 {
				sx, sy := t.x, t.y
				var dx, dy float32
				for j := 0; j < 10; j += 2 {
					dx += t.stack[j]
					dy += t.stack[j+1]
				}
				t.curve(t.stack[0], t.stack[1], t.stack[2], t.stack[3], t.stack[4], t.stack[5])
				t.curve(t.stack[6], t.stack[7], t.stack[8], t.stack[9], t.stack[10], sy+dy-t.y)
				t.x = sx + dx
			}
			t.clear()

		case 10, 29: // callsubr callgsubr
			subrs := t.subrs
			if op == 29 {
				subrs = t.f.globalSubrs
			}
			if t.sp > 0 {
				t.sp--
				n := int(t.stack[t.sp]) + bias(len(subrs))
				if n >= 0 && n < len(subrs) {
					t.run(subrs[n], depth+1)
				}
			}
		case 11: // return
			return

		case 14: // endchar
			if t.sp == 1 || t.sp == 5 {
				t.takeWidth(t.sp - 1)
			} else {
				t.hasW = true
			}
			if t.sp >= 4 {
				t.seac(int(t.stack[t.sp-2]), int(t.stack[t.sp-1]), t.stack[t.sp-4], t.stack[t.sp-3])
			}
			t.closeContour()
			t.clear()
			return

		case key12(20): // put
			if t.sp >= 2 {
				if j := int(t.stack[t.sp-1]); j >= 0 && j < len(t.trans) {
					t.trans[j] = t.stack[t.sp-2]
				}
				t.sp -= 2
			}
		case key12(21): // get
			if t.sp >= 1 {
				j := int(t.stack[t.sp-1])
				t.sp--
				if j >= 0 && j < len(t.trans) {
					t.push(t.trans[j])
				} else {
					t.push(0)
				}
			}
		default:
			t.clear()
		}
	}
}

// curve adds a relative cubic.
func (t *t2) curve(dx1, dy1, dx2, dy2, dx3, dy3 float32) {
	x1, y1 := t.x+dx1, t.y+dy1
	x2, y2 := x1+dx2, y1+dy2
	t.x, t.y = x2+dx3, y2+dy3
	if !t.open {
		t.moveTo(x1, y1)
	}
	t.p.CurveTo(x1, y1, x2, y2, t.x, t.y)
}

// seac draws an accented character out of two standard encoded glyphs, which
// endchar with four arguments asks for.
func (t *t2) seac(bchar, achar int, adx, ady float32) {
	base := t.f.gidForStandardCode(bchar)
	accent := t.f.gidForStandardCode(achar)
	if base < 0 || accent < 0 {
		return
	}
	t.closeContour()
	if p := t.f.path(base); p != nil {
		t.p.Append(p)
	}
	if p := t.f.path(accent); p != nil {
		t.p.Append(p.Transform(raster.Translate(adx, ady)))
	}
}

// gidForStandardCode maps a StandardEncoding code to a glyph by name.
func (c *cffFont) gidForStandardCode(code int) int {
	if code < 0 || code > 255 {
		return -1
	}
	name := standardEncoding[code]
	if name == "" {
		return -1
	}
	for gid := range c.charStrings {
		if c.glyphName(gid) == name {
			return gid
		}
	}
	return -1
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
