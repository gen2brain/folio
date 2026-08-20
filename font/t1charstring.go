package font

import "github.com/gen2brain/pdf/raster"

// t1 runs a Type 1 charstring.
type t1 struct {
	f     *type1Font
	p     raster.Path
	x, y  float32
	sbx   float32
	width float32

	stack [48]float32
	sp    int
	ps    [32]float32
	psp   int

	open   bool
	steps  int
	inFlex bool
	flex   []float32
}

func (t *t1) push(v float32) {
	if t.sp < len(t.stack) {
		t.stack[t.sp] = v
		t.sp++
	}
}

func (t *t1) clear() { t.sp = 0 }

func (t *t1) moveTo(x, y float32) {
	if t.inFlex {
		t.flex = append(t.flex, x, y)
		return
	}
	t.closeContour()
	t.p.MoveTo(x, y)
	t.open = true
}

func (t *t1) closeContour() {
	if t.open {
		t.p.Close()
		t.open = false
	}
}

func (t *t1) run(code []byte, depth int) bool {
	if depth > maxCharstringDepth {
		return true
	}
	for i := 0; i < len(code); {
		if t.steps++; t.steps > maxCharstringSteps {
			return true
		}
		b := code[i]
		if b >= 32 {
			var v float32
			switch {
			case b <= 246:
				v = float32(int(b) - 139)
				i++
			case b <= 250:
				if i+2 > len(code) {
					return true
				}
				v = float32((int(b)-247)*256 + int(code[i+1]) + 108)
				i += 2
			case b <= 254:
				if i+2 > len(code) {
					return true
				}
				v = float32(-(int(b)-251)*256 - int(code[i+1]) - 108)
				i += 2
			default:
				if i+5 > len(code) {
					return true
				}
				v = float32(int32(be32(code, i+1)))
				i += 5
			}
			t.push(v)
			continue
		}

		op := int(b)
		i++
		if op == 12 {
			if i >= len(code) {
				return true
			}
			op = key12(int(code[i]))
			i++
		}

		switch op {
		case 13: // hsbw
			if t.sp >= 2 {
				t.sbx = t.stack[0]
				t.width = t.stack[1]
				t.x = t.sbx
			}
			t.clear()
		case key12(7): // sbw
			if t.sp >= 4 {
				t.sbx = t.stack[0]
				t.x, t.y = t.stack[0], t.stack[1]
				t.width = t.stack[2]
			}
			t.clear()

		case 21: // rmoveto
			if t.sp >= 2 {
				t.x += t.stack[t.sp-2]
				t.y += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()
		case 22: // hmoveto
			if t.sp >= 1 {
				t.x += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()
		case 4: // vmoveto
			if t.sp >= 1 {
				t.y += t.stack[t.sp-1]
			}
			t.moveTo(t.x, t.y)
			t.clear()

		case 5: // rlineto
			if t.sp >= 2 {
				t.x += t.stack[0]
				t.y += t.stack[1]
				t.lineTo()
			}
			t.clear()
		case 6: // hlineto
			if t.sp >= 1 {
				t.x += t.stack[0]
				t.lineTo()
			}
			t.clear()
		case 7: // vlineto
			if t.sp >= 1 {
				t.y += t.stack[0]
				t.lineTo()
			}
			t.clear()

		case 8: // rrcurveto
			if t.sp >= 6 {
				t.curve(t.stack[0], t.stack[1], t.stack[2], t.stack[3], t.stack[4], t.stack[5])
			}
			t.clear()
		case 30: // vhcurveto
			if t.sp >= 4 {
				t.curve(0, t.stack[0], t.stack[1], t.stack[2], t.stack[3], 0)
			}
			t.clear()
		case 31: // hvcurveto
			if t.sp >= 4 {
				t.curve(t.stack[0], 0, t.stack[1], t.stack[2], 0, t.stack[3])
			}
			t.clear()

		case 9: // closepath
			t.closeContour()
			t.clear()
		case 1, 3, key12(0), key12(1), key12(2): // stems and dotsection
			t.clear()

		case 10: // callsubr
			if t.sp >= 1 {
				t.sp--
				n := int(t.stack[t.sp])
				if n >= 0 && n < len(t.f.subrs) {
					if t.run(t.f.subrs[n], depth+1) {
						return true
					}
				}
			}
		case 11: // return
			return false
		case 14: // endchar
			t.closeContour()
			return true

		case key12(6): // seac
			if t.sp >= 5 {
				t.seac(int(t.stack[3]), int(t.stack[4]), t.stack[1], t.stack[2])
			}
			return true
		case key12(12): // div
			if t.sp >= 2 {
				a, b := t.stack[t.sp-2], t.stack[t.sp-1]
				t.sp -= 2
				if b != 0 {
					t.push(a / b)
				} else {
					t.push(0)
				}
			}
		case key12(16): // callothersubr
			t.otherSubr()
		case key12(17): // pop
			if t.psp > 0 {
				t.psp--
				t.push(t.ps[t.psp])
			} else {
				t.push(0)
			}
		case key12(33): // setcurrentpoint
			if t.sp >= 2 {
				t.x, t.y = t.stack[0], t.stack[1]
			}
			t.clear()
		default:
			t.clear()
		}
	}
	return false
}

func (t *t1) lineTo() {
	if !t.open {
		t.moveTo(t.x, t.y)
		return
	}
	t.p.LineTo(t.x, t.y)
}

func (t *t1) curve(dx1, dy1, dx2, dy2, dx3, dy3 float32) {
	x1, y1 := t.x+dx1, t.y+dy1
	x2, y2 := x1+dx2, y1+dy2
	t.x, t.y = x2+dx3, y2+dy3
	if !t.open {
		t.moveTo(x1, y1)
	}
	t.p.CurveTo(x1, y1, x2, y2, t.x, t.y)
}

// otherSubr handles the callothersubr protocol: flex, hint replacement, and
// anything else, which is put back for the pops that follow.
func (t *t1) otherSubr() {
	if t.sp < 2 {
		t.clear()
		return
	}
	idx := int(t.stack[t.sp-1])
	n := int(t.stack[t.sp-2])
	t.sp -= 2
	if n < 0 || n > t.sp {
		n = t.sp
	}
	args := t.stack[t.sp-n : t.sp]
	t.sp -= n

	switch idx {
	case 1:
		t.inFlex = true
		t.flex = t.flex[:0]
	case 0:
		t.inFlex = false
		if len(t.flex) >= 14 {
			f := t.flex[2:]
			t.p.CurveTo(f[0], f[1], f[2], f[3], f[4], f[5])
			t.p.CurveTo(f[6], f[7], f[8], f[9], f[10], f[11])
			t.x, t.y = f[10], f[11]
		}
		t.psp = 0
		t.ps[0], t.ps[1] = t.y, t.x
		t.psp = 2
	case 3:
		t.psp = 0
		t.ps[0] = 3
		t.psp = 1
	default:
		t.psp = 0
		for i := len(args) - 1; i >= 0 && t.psp < len(t.ps); i-- {
			t.ps[t.psp] = args[i]
			t.psp++
		}
	}
}

// seac draws an accented character from two standard encoded glyphs.
func (t *t1) seac(bchar, achar int, adx, ady float32) {
	base := standardName(bchar)
	accent := standardName(achar)
	if base == "" || accent == "" {
		return
	}
	t.closeContour()
	if cs, ok := t.f.charstrings[base]; ok {
		r := &t1{f: t.f}
		r.run(cs, 0)
		r.closeContour()
		t.p.Append(&r.p)
	}
	if cs, ok := t.f.charstrings[accent]; ok {
		r := &t1{f: t.f}
		r.run(cs, 0)
		r.closeContour()
		t.p.Append(r.p.Transform(raster.Translate(t.sbx-r.sbx+adx, ady)))
	}
}

func standardName(code int) string {
	if code < 0 || code > 255 {
		return ""
	}
	return standardEncoding[code]
}
