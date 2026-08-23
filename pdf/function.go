package pdf

import (
	"fmt"
	"math"

	"github.com/gen2brain/folio/syntax"
)

// Function is a PDF function, ISO 32000-1 7.10: sampled, exponential,
// stitching, or a PostScript calculator.
type Function struct {
	Type   int
	Domain []float64
	Range  []float64

	m int // inputs
	n int // outputs

	samples []float64
	size    []int
	encode  []float64
	decode  []float64

	c0, c1 []float64
	exp    float64

	funcs  []*Function
	bounds []float64

	prog []psOp

	dict Dict
}

const maxFunctionDepth = 8

// maxComponents bounds the values a function may be asked for at once.
const maxComponents = 32

// function reads a function object.
func (d *Document) function(obj Object) *Function {
	return d.functionDepth(obj, 0)
}

func (d *Document) functionDepth(obj Object, depth int) *Function {
	if depth > maxFunctionDepth {
		d.errorf("function nested too deeply")
		return nil
	}
	dict := d.f.GetDict(obj)
	if dict == nil {
		return nil
	}
	fn := &Function{
		Type:   int(d.f.GetInt(dict["FunctionType"], -1)),
		Domain: d.f.GetFloats(dict["Domain"]),
		Range:  d.f.GetFloats(dict["Range"]),
		dict:   dict,
	}
	if len(fn.Domain) < 2 || len(fn.Domain)%2 != 0 {
		d.errorf("function has no domain")
		return nil
	}
	fn.m = len(fn.Domain) / 2
	fn.n = len(fn.Range) / 2

	var err error
	switch fn.Type {
	case 0:
		err = d.readSampled(fn, obj)
	case 2:
		err = d.readExponential(fn)
	case 3:
		err = d.readStitching(fn, depth)
	case 4:
		err = d.readPostScript(fn, obj)
	default:
		err = fmt.Errorf("function type %d", fn.Type)
	}
	if err != nil {
		d.errorf("%v", err)
		return nil
	}
	return fn
}

// NumOutputs returns how many values Eval returns, or zero when the function
// only learns that from its own definition.
func (f *Function) NumOutputs() int {
	if f == nil {
		return 0
	}
	return f.n
}

// Eval evaluates the function. The result is appended to out, which may be
// nil, so that a caller in a loop can reuse one buffer.
func (f *Function) Eval(out []float64, in ...float64) []float64 {
	if f == nil {
		return out
	}
	var buf [maxComponents]float64
	args := buf[:]
	if f.m > len(args) {
		args = make([]float64, f.m)
	}
	m := min(f.m, len(in))
	for i := 0; i < m; i++ {
		args[i] = clampRange(in[i], f.Domain, i)
	}
	for i := m; i < f.m; i++ {
		args[i] = f.Domain[2*i]
	}
	in = args[:f.m]

	switch f.Type {
	case 0:
		out = f.evalSampled(out, in)
	case 2:
		out = f.evalExponential(out, in)
	case 3:
		out = f.evalStitching(out, in)
	case 4:
		out = f.evalPostScript(out, in)
	}
	if f.n > 0 {
		for i := range out {
			out[i] = clampRange(out[i], f.Range, i)
		}
	}
	return out
}

// Eval1 evaluates a function of one input into a float32 slice, which is what
// color conversion works in.
func (f *Function) Eval1(out []float32, in float64) []float32 {
	var buf [64]float64
	v := f.Eval(buf[:0], in)
	for i, x := range v {
		if i < len(out) {
			out[i] = float32(x)
		}
	}
	return out
}

func clampRange(v float64, r []float64, i int) float64 {
	if 2*i+1 >= len(r) {
		return v
	}
	lo, hi := r[2*i], r[2*i+1]
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func interpolate(x, xmin, xmax, ymin, ymax float64) float64 {
	if xmax == xmin {
		return ymin
	}
	return ymin + (x-xmin)*(ymax-ymin)/(xmax-xmin)
}

// readSampled reads a type 0 function, whose samples are packed at a chosen
// bit depth in the stream.
func (d *Document) readSampled(fn *Function, obj Object) error {
	st := d.f.GetStream(obj)
	if st == nil {
		return fmt.Errorf("sampled function is not a stream")
	}
	if fn.n == 0 {
		return fmt.Errorf("sampled function has no range")
	}
	for _, s := range d.f.GetFloats(st.Dict["Size"]) {
		if s < 1 || s > 1<<24 {
			return fmt.Errorf("sampled function /Size %v", s)
		}
		fn.size = append(fn.size, int(s))
	}
	if len(fn.size) != fn.m {
		return fmt.Errorf("sampled function has %d sizes for %d inputs", len(fn.size), fn.m)
	}
	bps := int(d.f.GetInt(st.Dict["BitsPerSample"], 0))
	switch bps {
	case 1, 2, 4, 8, 12, 16, 24, 32:
	default:
		return fmt.Errorf("sampled function /BitsPerSample %d", bps)
	}

	fn.encode = d.f.GetFloats(st.Dict["Encode"])
	if len(fn.encode) != 2*fn.m {
		fn.encode = fn.encode[:0]
		for _, s := range fn.size {
			fn.encode = append(fn.encode, 0, float64(s-1))
		}
	}
	fn.decode = d.f.GetFloats(st.Dict["Decode"])
	if len(fn.decode) != 2*fn.n {
		fn.decode = fn.Range
	}

	total := fn.n
	for _, s := range fn.size {
		total *= s
		if total > 1<<26 {
			return fmt.Errorf("sampled function has too many samples")
		}
	}
	data, err := st.Data()
	if err != nil {
		return fmt.Errorf("sampled function: %w", err)
	}

	max := float64(uint64(1)<<uint(bps) - 1)
	fn.samples = make([]float64, total)
	br := syntax.NewBitReader(data)
	for i := range fn.samples {
		fn.samples[i] = float64(br.Read(bps)) / max
	}
	return nil
}

// evalSampled interpolates multilinearly between the 2^m samples around the
// input point, ISO 32000-1 7.10.2.
func (f *Function) evalSampled(out []float64, in []float64) []float64 {
	const maxInputs = 8
	if f.m > maxInputs {
		return append(out, make([]float64, f.n)...)
	}
	vertices := 1 << uint(f.m)
	var (
		weight [1 << maxInputs]float64
		index  [1 << maxInputs]int
	)
	for i := 0; i < vertices; i++ {
		weight[i] = 1
	}

	k, pos := f.n, 1
	for i := 0; i < f.m; i++ {
		e := interpolate(in[i], f.Domain[2*i], f.Domain[2*i+1], f.encode[2*i], f.encode[2*i+1])
		size := f.size[i]
		if e < 0 {
			e = 0
		} else if e > float64(size-1) {
			e = float64(size - 1)
		}
		e0 := math.Floor(e)
		if e0 >= float64(size-1) && size > 1 {
			e0 = float64(size - 2)
		}
		n1 := e - e0
		n0 := 1 - n1
		off0 := int(e0) * k
		off1 := off0
		if size > 1 {
			off1 += k
		}
		for j := 0; j < vertices; j++ {
			if j&pos != 0 {
				weight[j] *= n1
				index[j] += off1
			} else {
				weight[j] *= n0
				index[j] += off0
			}
		}
		k *= size
		pos <<= 1
	}

	for j := 0; j < f.n; j++ {
		var v float64
		for i := 0; i < vertices; i++ {
			if p := index[i] + j; p >= 0 && p < len(f.samples) {
				v += f.samples[p] * weight[i]
			}
		}
		out = append(out, interpolate(v, 0, 1, f.decode[2*j], f.decode[2*j+1]))
	}
	return out
}

// readExponential reads a type 2 function.
func (d *Document) readExponential(fn *Function) error {
	fn.c0 = d.f.GetFloats(fn.dict["C0"])
	fn.c1 = d.f.GetFloats(fn.dict["C1"])
	if len(fn.c0) == 0 {
		fn.c0 = []float64{0}
	}
	if len(fn.c1) == 0 {
		fn.c1 = []float64{1}
	}
	if len(fn.c0) != len(fn.c1) {
		return fmt.Errorf("exponential function has %d C0 and %d C1", len(fn.c0), len(fn.c1))
	}
	fn.exp = d.f.GetFloat(fn.dict["N"], 1)
	if fn.n == 0 {
		fn.n = len(fn.c0)
	}
	return nil
}

func (f *Function) evalExponential(out []float64, in []float64) []float64 {
	x := in[0]
	p := x
	switch f.exp {
	case 1:
	case 0:
		p = 1
	default:
		if x < 0 && f.exp != math.Trunc(f.exp) {
			p = 0
		} else {
			p = math.Pow(x, f.exp)
		}
	}
	for i := range f.c0 {
		out = append(out, f.c0[i]+p*(f.c1[i]-f.c0[i]))
	}
	return out
}

// readStitching reads a type 3 function, which splits its domain up.
func (d *Document) readStitching(fn *Function, depth int) error {
	for _, sub := range d.f.GetArray(fn.dict["Functions"]) {
		s := d.functionDepth(sub, depth+1)
		if s == nil {
			return fmt.Errorf("stitching function has an unreadable part")
		}
		fn.funcs = append(fn.funcs, s)
	}
	if len(fn.funcs) == 0 {
		return fmt.Errorf("stitching function has no parts")
	}
	fn.bounds = d.f.GetFloats(fn.dict["Bounds"])
	if len(fn.bounds) != len(fn.funcs)-1 {
		return fmt.Errorf("stitching function has %d bounds for %d parts", len(fn.bounds), len(fn.funcs))
	}
	fn.encode = d.f.GetFloats(fn.dict["Encode"])
	if len(fn.encode) != 2*len(fn.funcs) {
		return fmt.Errorf("stitching function has %d encode values", len(fn.encode))
	}
	if fn.n == 0 {
		fn.n = fn.funcs[0].NumOutputs()
	}
	return nil
}

func (f *Function) evalStitching(out []float64, in []float64) []float64 {
	x := in[0]
	k := 0
	for k < len(f.bounds) && x >= f.bounds[k] {
		k++
	}
	lo := f.Domain[0]
	if k > 0 {
		lo = f.bounds[k-1]
	}
	hi := f.Domain[1]
	if k < len(f.bounds) {
		hi = f.bounds[k]
	}
	x = interpolate(x, lo, hi, f.encode[2*k], f.encode[2*k+1])
	return f.funcs[k].Eval(out, x)
}

// readPostScript reads a type 4 function, a small PostScript calculator.
func (d *Document) readPostScript(fn *Function, obj Object) error {
	st := d.f.GetStream(obj)
	if st == nil {
		return fmt.Errorf("PostScript function is not a stream")
	}
	if fn.n == 0 {
		return fmt.Errorf("PostScript function has no range")
	}
	data, err := st.Data()
	if err != nil {
		return fmt.Errorf("PostScript function: %w", err)
	}
	prog, err := parsePostScript(data)
	if err != nil {
		return err
	}
	fn.prog = prog
	return nil
}

func (f *Function) evalPostScript(out []float64, in []float64) []float64 {
	var st psStack
	for _, v := range in {
		st.pushReal(v)
	}
	runPostScript(f.prog, &st, 0)

	n := f.n
	if n > st.len {
		n = st.len
	}
	for i := st.len - n; i < st.len; i++ {
		out = append(out, st.v[i].num())
	}
	for i := n; i < f.n; i++ {
		out = append(out, 0)
	}
	return out
}

// Dict returns the function dictionary.
func (f *Function) Dict() Dict { return f.dict }

// psOp is one instruction of a type 4 function.
type psOp struct {
	op        psOpcode
	val       float64
	i         int32
	isInt     bool
	then, els []psOp
}

type psOpcode uint8

const (
	psPush psOpcode = iota
	psIf
	psIfElse
	psAbs
	psAdd
	psAtan
	psCeiling
	psCos
	psCvi
	psCvr
	psDiv
	psExp
	psFloor
	psIdiv
	psLn
	psLog
	psMod
	psMul
	psNeg
	psRound
	psSin
	psSqrt
	psSub
	psTruncate
	psAnd
	psBitshift
	psEq
	psFalse
	psGe
	psGt
	psLe
	psLt
	psNe
	psNot
	psOr
	psTrue
	psXor
	psCopy
	psDup
	psExch
	psIndex
	psPop
	psRoll
)

var psOps = map[string]psOpcode{
	"abs": psAbs, "add": psAdd, "atan": psAtan, "ceiling": psCeiling,
	"cos": psCos, "cvi": psCvi, "cvr": psCvr, "div": psDiv, "exp": psExp,
	"floor": psFloor, "idiv": psIdiv, "ln": psLn, "log": psLog, "mod": psMod,
	"mul": psMul, "neg": psNeg, "round": psRound, "sin": psSin,
	"sqrt": psSqrt, "sub": psSub, "truncate": psTruncate,
	"and": psAnd, "bitshift": psBitshift, "eq": psEq, "false": psFalse,
	"ge": psGe, "gt": psGt, "le": psLe, "lt": psLt, "ne": psNe, "not": psNot,
	"or": psOr, "true": psTrue, "xor": psXor,
	"copy": psCopy, "dup": psDup, "exch": psExch, "index": psIndex,
	"pop": psPop, "roll": psRoll,
}

// parsePostScript reads the program, which is one brace delimited block.
func parsePostScript(data []byte) ([]psOp, error) {
	l := syntax.NewLexer(data)
	obj, ok := l.Next()
	if kw, isKw := obj.(syntax.Keyword); !ok || !isKw || kw != "{" {
		return nil, fmt.Errorf("PostScript function does not start with {")
	}
	prog, err := parsePSBlock(l, 0)
	if err != nil {
		return nil, err
	}
	return prog, nil
}

func parsePSBlock(l *syntax.Lexer, depth int) ([]psOp, error) {
	if depth > 32 {
		return nil, fmt.Errorf("PostScript function nested too deeply")
	}
	var prog []psOp
	var blocks [][]psOp
	for {
		obj, ok := l.Next()
		if !ok {
			return nil, fmt.Errorf("PostScript function ends inside a block")
		}
		switch v := obj.(type) {
		case syntax.Integer:
			prog = append(prog, psOp{op: psPush, val: float64(v), i: int32(v), isInt: true})
			continue
		case syntax.Real:
			prog = append(prog, psOp{op: psPush, val: float64(v)})
			continue
		case syntax.Bool:
			code := psFalse
			if v {
				code = psTrue
			}
			prog = append(prog, psOp{op: code})
			continue
		case syntax.Keyword:
			switch v {
			case "{":
				b, err := parsePSBlock(l, depth+1)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, b)
				continue
			case "}":
				return prog, nil
			case "if":
				if len(blocks) < 1 {
					return nil, fmt.Errorf("if without a block")
				}
				prog = append(prog, psOp{op: psIf, then: blocks[len(blocks)-1]})
				blocks = blocks[:len(blocks)-1]
				continue
			case "ifelse":
				if len(blocks) < 2 {
					return nil, fmt.Errorf("ifelse without two blocks")
				}
				prog = append(prog, psOp{
					op:   psIfElse,
					then: blocks[len(blocks)-2],
					els:  blocks[len(blocks)-1],
				})
				blocks = blocks[:len(blocks)-2]
				continue
			}
			if code, known := psOps[string(v)]; known {
				prog = append(prog, psOp{op: code})
				continue
			}
			return nil, fmt.Errorf("PostScript operator %q", string(v))
		}
		return nil, fmt.Errorf("PostScript function has a %T in it", obj)
	}
}

// psValue is a number or a boolean on the calculator stack.
type psValue struct {
	f    float64
	i    int32
	b    bool
	kind uint8
}

const (
	psReal uint8 = iota
	psInt
	psBool
)

func (v psValue) num() float64 {
	switch v.kind {
	case psInt:
		return float64(v.i)
	case psBool:
		if v.b {
			return 1
		}
		return 0
	}
	return v.f
}

func (v psValue) int() int32 {
	switch v.kind {
	case psInt:
		return v.i
	case psBool:
		if v.b {
			return 1
		}
		return 0
	}
	return int32(v.f)
}

func (v psValue) bool() bool {
	if v.kind == psBool {
		return v.b
	}
	return v.num() != 0
}

// psStack is the operand stack, bounded because a program is untrusted.
type psStack struct {
	v   [100]psValue
	len int
}

func (s *psStack) push(v psValue) {
	if s.len < len(s.v) {
		s.v[s.len] = v
		s.len++
	}
}

func (s *psStack) pushReal(f float64) { s.push(psValue{f: f, kind: psReal}) }
func (s *psStack) pushInt(i int32)    { s.push(psValue{i: i, kind: psInt}) }
func (s *psStack) pushBool(b bool)    { s.push(psValue{b: b, kind: psBool}) }

func (s *psStack) pop() psValue {
	if s.len == 0 {
		return psValue{}
	}
	s.len--
	return s.v[s.len]
}

// runPostScript executes a block.
func runPostScript(prog []psOp, s *psStack, depth int) {
	if depth > 32 {
		return
	}
	for _, op := range prog {
		switch op.op {
		case psPush:
			if op.isInt {
				s.pushInt(op.i)
			} else {
				s.pushReal(op.val)
			}
		case psIf:
			if s.pop().bool() {
				runPostScript(op.then, s, depth+1)
			}
		case psIfElse:
			if s.pop().bool() {
				runPostScript(op.then, s, depth+1)
			} else {
				runPostScript(op.els, s, depth+1)
			}
		default:
			psApply(op.op, s)
		}
	}
}

func psApply(op psOpcode, s *psStack) {
	switch op {
	case psAbs:
		v := s.pop()
		if v.kind == psInt {
			s.pushInt(absInt32(v.i))
		} else {
			s.pushReal(math.Abs(v.num()))
		}
	case psAdd, psSub, psMul:
		b, a := s.pop(), s.pop()
		if a.kind == psInt && b.kind == psInt {
			x, y := a.i, b.i
			switch op {
			case psAdd:
				s.pushInt(x + y)
			case psSub:
				s.pushInt(x - y)
			default:
				s.pushInt(x * y)
			}
			return
		}
		x, y := a.num(), b.num()
		switch op {
		case psAdd:
			s.pushReal(x + y)
		case psSub:
			s.pushReal(x - y)
		default:
			s.pushReal(x * y)
		}
	case psDiv:
		b, a := s.pop(), s.pop()
		if b.num() == 0 {
			s.pushReal(0)
			return
		}
		s.pushReal(a.num() / b.num())
	case psIdiv:
		b, a := s.pop(), s.pop()
		if b.int() == 0 {
			s.pushInt(0)
			return
		}
		s.pushInt(a.int() / b.int())
	case psMod:
		b, a := s.pop(), s.pop()
		if b.int() == 0 {
			s.pushInt(0)
			return
		}
		s.pushInt(a.int() % b.int())
	case psNeg:
		v := s.pop()
		if v.kind == psInt {
			s.pushInt(-v.i)
		} else {
			s.pushReal(-v.num())
		}
	case psAtan:
		den, num := s.pop(), s.pop()
		deg := math.Atan2(num.num(), den.num()) * 180 / math.Pi
		if deg < 0 {
			deg += 360
		}
		s.pushReal(deg)
	case psCeiling:
		s.pushReal(math.Ceil(s.pop().num()))
	case psFloor:
		s.pushReal(math.Floor(s.pop().num()))
	case psRound:
		s.pushReal(math.Round(s.pop().num()))
	case psTruncate:
		s.pushReal(math.Trunc(s.pop().num()))
	case psCos:
		s.pushReal(math.Cos(s.pop().num() * math.Pi / 180))
	case psSin:
		s.pushReal(math.Sin(s.pop().num() * math.Pi / 180))
	case psCvi:
		s.pushInt(s.pop().int())
	case psCvr:
		s.pushReal(s.pop().num())
	case psExp:
		b, a := s.pop(), s.pop()
		s.pushReal(math.Pow(a.num(), b.num()))
	case psLn:
		v := s.pop().num()
		if v <= 0 {
			s.pushReal(0)
			return
		}
		s.pushReal(math.Log(v))
	case psLog:
		v := s.pop().num()
		if v <= 0 {
			s.pushReal(0)
			return
		}
		s.pushReal(math.Log10(v))
	case psSqrt:
		v := s.pop().num()
		if v < 0 {
			v = 0
		}
		s.pushReal(math.Sqrt(v))

	case psAnd, psOr, psXor:
		b, a := s.pop(), s.pop()
		if a.kind == psBool || b.kind == psBool {
			x, y := a.bool(), b.bool()
			switch op {
			case psAnd:
				s.pushBool(x && y)
			case psOr:
				s.pushBool(x || y)
			default:
				s.pushBool(x != y)
			}
			return
		}
		x, y := a.int(), b.int()
		switch op {
		case psAnd:
			s.pushInt(x & y)
		case psOr:
			s.pushInt(x | y)
		default:
			s.pushInt(x ^ y)
		}
	case psNot:
		v := s.pop()
		if v.kind == psBool {
			s.pushBool(!v.b)
		} else {
			s.pushInt(^v.int())
		}
	case psBitshift:
		b, a := s.pop(), s.pop()
		n, v := b.int(), a.int()
		switch {
		case n >= 32 || n <= -32:
			s.pushInt(0)
		case n >= 0:
			s.pushInt(v << uint(n))
		default:
			s.pushInt(v >> uint(-n))
		}
	case psEq, psNe, psGt, psGe, psLt, psLe:
		b, a := s.pop(), s.pop()
		x, y := a.num(), b.num()
		switch op {
		case psEq:
			s.pushBool(x == y)
		case psNe:
			s.pushBool(x != y)
		case psGt:
			s.pushBool(x > y)
		case psGe:
			s.pushBool(x >= y)
		case psLt:
			s.pushBool(x < y)
		default:
			s.pushBool(x <= y)
		}
	case psTrue:
		s.pushBool(true)
	case psFalse:
		s.pushBool(false)

	case psPop:
		s.pop()
	case psDup:
		if s.len > 0 {
			s.push(s.v[s.len-1])
		}
	case psExch:
		if s.len >= 2 {
			s.v[s.len-1], s.v[s.len-2] = s.v[s.len-2], s.v[s.len-1]
		}
	case psCopy:
		n := int(s.pop().int())
		if n <= 0 || n > s.len || s.len+n > len(s.v) {
			return
		}
		for i := 0; i < n; i++ {
			s.push(s.v[s.len-n])
		}
	case psIndex:
		n := int(s.pop().int())
		if n < 0 || n >= s.len {
			s.pushInt(0)
			return
		}
		s.push(s.v[s.len-1-n])
	case psRoll:
		j := int(s.pop().int())
		n := int(s.pop().int())
		if n <= 0 || n > s.len {
			return
		}
		j %= n
		if j < 0 {
			j += n
		}
		if j == 0 {
			return
		}
		var tmp [100]psValue
		base := s.len - n
		copy(tmp[:n], s.v[base:s.len])
		for i := 0; i < n; i++ {
			s.v[base+(i+j)%n] = tmp[i]
		}
	}
}

func absInt32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
