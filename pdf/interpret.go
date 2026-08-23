package pdf

import (
	"github.com/gen2brain/folio/raster"
	"github.com/gen2brain/folio/syntax"
)

// Limits on what one page may ask for. A content stream is untrusted input
// and every one of these has a file behind it.
const (
	maxNesting    = 16      // form XObjects, patterns and Type3 glyphs
	maxOperands   = 64      // operands before an operator
	maxOperations = 1 << 24 // operators run for one page
	maxGStack     = 64
)

// interp runs a content stream against a device.
type interp struct {
	doc *Document
	dev Device

	gs     gstate
	gstack []gstate

	res      Dict
	resStack []Dict

	// defaults are the /Default color spaces the current resources name.
	defaults      *DefaultColorSpaces
	defaultsStack []*DefaultColorSpaces
	defaultsSet   []bool

	path raster.Path
	// held is the stack of paths being painted, one per painting operator
	// this is inside: taking the path out of the interpreter is what keeps a
	// soft mask or a pattern run from inside one from building into it.
	held    [maxNesting + 2]raster.Path
	holding int
	// clip is the pending clip of W or W*, applied by the next paint operator.
	clip    int
	scissor raster.Rect

	inText   bool
	tm, tlm  raster.Matrix
	text     *Text
	textMode int
	textCTM  raster.Matrix

	// base is the transform the page started with, which is the space a
	// pattern matrix is relative to.
	base raster.Matrix

	// mc is the marked content stack, and hidden counts how many enclosing
	// optional content groups are switched off. Hidden content is still
	// interpreted, so that the stacks stay balanced, but nothing is drawn.
	mc []markedContent
	// pending holds the layers whose EMC arrived inside a clip they opened.
	pending []pendingLayer
	hidden  int

	depth int
	// running is the form XObjects on the path from the page to here, so one
	// that reaches itself stops at the cycle rather than at the depth limit.
	// It unwinds on the way out, so a form drawn twice side by side still does.
	running []Ref
	ops     *int64
}

const (
	clipNone = iota
	clipNonZero
	clipEvenOdd
)

// markedContent is one entry of the BMC/BDC stack.
type markedContent struct {
	layers int  // layers opened on the device
	clip   int  // clips open on the device when it began
	hid    bool // this entry hid its content
}

// pendingLayer is a layer whose EMC arrived while a clip opened inside it was
// still on the device. Ending it there would leave the layer and the clip
// crossing rather than nesting, so it waits for the Q that pops the clip.
type pendingLayer struct {
	layers int
	clip   int
}

// openClips counts the clips the device is holding, which q resets per level
// and only the whole stack adds up.
func (ip *interp) openClips() int {
	n := ip.gs.clipDepth
	for _, g := range ip.gstack {
		n += g.clipDepth
	}
	return n
}

// closeLayers ends the layers that were waiting for a clip to be popped.
func (ip *interp) closeLayers() {
	for len(ip.pending) > 0 {
		p := ip.pending[len(ip.pending)-1]
		if ip.openClips() > p.clip {
			return
		}
		ip.pending = ip.pending[:len(ip.pending)-1]
		for i := 0; i < p.layers; i++ {
			ip.dev.EndLayer()
		}
	}
}

func (ip *interp) errorf(format string, a ...any) { ip.doc.errorf(format, a...) }

// run interprets one content stream.
func (ip *interp) run(data []byte) {
	l := syntax.NewLexer(data)
	p := syntax.NewParser(l, ip.doc.f)
	p.AllowStreams(false)

	var stack []syntax.Operand
	for {
		op, ok := p.Operand()
		if !ok {
			break
		}
		kw, isKw := op.Obj.(syntax.Keyword)
		if !isKw {
			if len(stack) < maxOperands {
				stack = append(stack, op)
			}
			continue
		}
		if *ip.ops++; *ip.ops > maxOperations {
			ip.errorf("page exceeds %d operations", maxOperations)
			return
		}
		if kw == "BI" {
			ip.inlineImage(p)
			stack = stack[:0]
			continue
		}
		ip.op(kw, stack)
		stack = stack[:0]
	}
	for _, e := range l.Errors() {
		ip.doc.errorf("content stream: %v", e)
	}
}

// num is operand i counting from the end of the list, so num(0) is the last.
func num(stack []syntax.Operand, i int) float32 {
	i = len(stack) - 1 - i
	if i < 0 || i >= len(stack) {
		return 0
	}
	if stack[i].IsNum {
		return float32(stack[i].Num)
	}
	return 0
}

func name(stack []syntax.Operand, i int) Name {
	i = len(stack) - 1 - i
	if i < 0 {
		return ""
	}
	n, _ := stack[i].Obj.(Name)
	return n
}

func (ip *interp) op(kw syntax.Keyword, stack []syntax.Operand) {
	switch kw {
	case "q":
		if len(ip.gstack) >= maxGStack {
			ip.errorf("graphics state stack overflow")
			return
		}
		ip.gstack = append(ip.gstack, ip.gs.clone())
		ip.gs.clipDepth = 0
	case "Q":
		ip.flushText()
		for i := 0; i < ip.gs.clipDepth; i++ {
			ip.dev.PopClip()
		}
		if n := len(ip.gstack); n > 0 {
			ip.gs = ip.gstack[n-1]
			ip.gstack = ip.gstack[:n-1]
		} else {
			ip.errorf("Q without q")
		}
		ip.closeLayers()
	case "cm":
		ip.flushText()
		if len(stack) >= 6 {
			m := raster.Matrix{
				A: num(stack, 5), B: num(stack, 4), C: num(stack, 3),
				D: num(stack, 2), E: num(stack, 1), F: num(stack, 0),
			}
			ip.gs.ctm = raster.Concat(m, ip.gs.ctm)
		}
	case "w":
		ip.flushText()
		ip.gs.stroke.Width = num(stack, 0)
	case "J":
		ip.flushText()
		ip.gs.stroke.SetCaps(raster.Cap(clampInt(int(num(stack, 0)), 0, 2)))
	case "j":
		ip.flushText()
		ip.gs.stroke.Join = raster.Join(clampInt(int(num(stack, 0)), 0, 2))
	case "M":
		ip.flushText()
		ip.gs.stroke.MiterLimit = num(stack, 0)
	case "d":
		ip.flushText()
		ip.setDash(stack)
	case "ri":
		ip.flushText()
		ip.gs.params.Intent = intentValue(name(stack, 0))
	case "i":
	case "gs":
		ip.flushText()
		ip.extGState(name(stack, 0))

	case "m":
		ip.path.MoveTo(num(stack, 1), num(stack, 0))
	case "l":
		ip.path.LineTo(num(stack, 1), num(stack, 0))
	case "c":
		ip.path.CurveTo(num(stack, 5), num(stack, 4), num(stack, 3), num(stack, 2), num(stack, 1), num(stack, 0))
	case "v":
		ip.path.CurveToV(num(stack, 3), num(stack, 2), num(stack, 1), num(stack, 0))
	case "y":
		ip.path.CurveToY(num(stack, 3), num(stack, 2), num(stack, 1), num(stack, 0))
	case "h":
		ip.path.Close()
	case "re":
		ip.path.Rect(num(stack, 3), num(stack, 2), num(stack, 1), num(stack, 0))

	case "S":
		ip.paint(false, true, false)
	case "s":
		ip.path.Close()
		ip.paint(false, true, false)
	case "f", "F":
		ip.paint(true, false, false)
	case "f*":
		ip.paint(true, false, true)
	case "B":
		ip.paint(true, true, false)
	case "B*":
		ip.paint(true, true, true)
	case "b":
		ip.path.Close()
		ip.paint(true, true, false)
	case "b*":
		ip.path.Close()
		ip.paint(true, true, true)
	case "n":
		ip.paint(false, false, false)
	case "W":
		ip.clip = clipNonZero
	case "W*":
		ip.clip = clipEvenOdd

	case "CS":
		ip.flushText()
		ip.gs.strokeColor.set(ip.doc.colorSpace(name(stack, 0), ip.res, 0))
	case "cs":
		ip.flushText()
		ip.gs.fill.set(ip.doc.colorSpace(name(stack, 0), ip.res, 0))
	case "SC", "SCN":
		ip.flushText()
		ip.setColor(&ip.gs.strokeColor, stack)
	case "sc", "scn":
		ip.flushText()
		ip.setColor(&ip.gs.fill, stack)
	case "G":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.strokeColor, DeviceGray, stack, 1)
	case "g":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.fill, DeviceGray, stack, 1)
	case "RG":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.strokeColor, DeviceRGB, stack, 3)
	case "rg":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.fill, DeviceRGB, stack, 3)
	case "K":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.strokeColor, DeviceCMYK, stack, 4)
	case "k":
		ip.flushText()
		ip.setDeviceColor(&ip.gs.fill, DeviceCMYK, stack, 4)

	case "BT":
		ip.flushText()
		ip.inText = true
		ip.tm, ip.tlm = raster.Identity, raster.Identity
	case "ET":
		ip.flushText()
		ip.inText = false
	case "Tc":
		ip.gs.text.charSpace = num(stack, 0)
	case "Tw":
		ip.gs.text.wordSpace = num(stack, 0)
	case "Tz":
		ip.gs.text.hscale = num(stack, 0) / 100
	case "TL":
		ip.gs.text.leading = num(stack, 0)
	case "Ts":
		ip.gs.text.rise = num(stack, 0)
	case "Tr":
		ip.gs.text.render = clampInt(int(num(stack, 0)), 0, 7)
	case "Tf":
		ip.gs.text.size = num(stack, 0)
		ip.setFont(name(stack, 1))
	case "Td":
		ip.translateText(num(stack, 1), num(stack, 0))
	case "TD":
		ip.gs.text.leading = -num(stack, 0)
		ip.translateText(num(stack, 1), num(stack, 0))
	case "Tm":
		if len(stack) >= 6 {
			ip.tlm = raster.Matrix{
				A: num(stack, 5), B: num(stack, 4), C: num(stack, 3),
				D: num(stack, 2), E: num(stack, 1), F: num(stack, 0),
			}
			ip.tm = ip.tlm
		}
	case "T*":
		ip.newline()
	case "Tj":
		ip.showObject(lastObject(stack))
	case "TJ":
		ip.showObject(lastObject(stack))
	case "'":
		ip.newline()
		ip.showObject(lastObject(stack))
	case "\"":
		ip.flushText()
		ip.gs.text.wordSpace = num(stack, 2)
		ip.gs.text.charSpace = num(stack, 1)
		ip.newline()
		ip.showObject(lastObject(stack))
	case "d0":
	case "d1":

	case "Do":
		ip.flushText()
		ip.doXObject(name(stack, 0))
	case "sh":
		ip.flushText()
		ip.doShading(name(stack, 0))
	case "BMC":
		ip.beginMarkedContent(name(stack, 0), nil)
	case "BDC":
		ip.beginMarkedContent(name(stack, 1), ip.markedProperty(stack))
	case "EMC":
		ip.endMarkedContent()
	case "MP", "DP", "BX", "EX":

	default:
		ip.errorf("unknown operator %q", string(kw))
	}
}

// markedProperty resolves the property list of a BDC, which is either an
// inline dictionary or a name to look up in the resources.
func (ip *interp) markedProperty(stack []syntax.Operand) Object {
	switch v := lastObject(stack).(type) {
	case Name:
		if props := ip.doc.f.GetDict(ip.res["Properties"]); props != nil {
			return props[v]
		}
		return nil
	default:
		return v
	}
}

func lastObject(stack []syntax.Operand) Object {
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1].Object()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (ip *interp) setDash(stack []syntax.Operand) {
	ip.gs.stroke.Dash = ip.gs.stroke.Dash[:0]
	ip.gs.stroke.DashPhase = num(stack, 0)
	if len(stack) < 2 {
		return
	}
	arr, _ := stack[len(stack)-2].Obj.(Array)
	allZero := true
	for _, e := range arr {
		v := ip.doc.f.GetFloat(e, 0)
		if v < 0 {
			ip.gs.stroke.Dash = ip.gs.stroke.Dash[:0]
			return
		}
		if v != 0 {
			allZero = false
		}
		ip.gs.stroke.Dash = append(ip.gs.stroke.Dash, float32(v))
	}
	if allZero {
		ip.gs.stroke.Dash = ip.gs.stroke.Dash[:0]
	}
}

func (ip *interp) setDeviceColor(c *color, cs *ColorSpace, stack []syntax.Operand, n int) {
	c.set(cs)
	for i := 0; i < n; i++ {
		c.value[i] = num(stack, n-1-i)
	}
	cs.Clamp(c.value)
}

// setColor handles sc, scn, SC and SCN, whose operands depend on the space
// that cs or CS selected.
func (ip *interp) setColor(c *color, stack []syntax.Operand) {
	if c.cs.IsPattern() {
		c.pattern = nil
		c.patternCTM = ip.base
		if n := name(stack, 0); n != "" {
			if pat := ip.doc.f.Lookup(ip.doc.f.GetDict(ip.res["Pattern"]), n); pat != nil {
				c.pattern = pat
			} else {
				ip.errorf("pattern /%s is not in the resources", n)
			}
			stack = stack[:max(len(stack)-1, 0)]
		}
		if base := c.cs.Base; base != nil && len(stack) > 0 {
			for i := 0; i < base.N && i < len(stack); i++ {
				c.value[i] = num(stack, base.N-1-i)
			}
			base.Clamp(c.value)
		}
		return
	}
	n := min(c.cs.N, len(stack))
	for i := 0; i < n; i++ {
		c.value[i] = num(stack, n-1-i)
	}
	c.cs.Clamp(c.value)
}

func (ip *interp) setFont(n Name) {
	res := ip.doc.f.GetDict(ip.res["Font"])
	obj := ip.doc.f.Lookup(res, n)
	if obj == nil {
		ip.errorf("font /%s is not in the resources", n)
		if ip.gs.text.font == nil {
			ip.gs.text.font = ip.doc.fallbackFont()
		}
		return
	}
	if ft := ip.doc.font(obj, ip.res); ft != nil {
		ip.gs.text.font = ft
	}
}

func (ip *interp) translateText(tx, ty float32) {
	ip.tlm = raster.Concat(raster.Translate(tx, ty), ip.tlm)
	ip.tm = ip.tlm
}

func (ip *interp) newline() {
	ip.translateText(0, -ip.gs.text.leading)
}

// paint applies the pending clip and hands the current path to the device.
func (ip *interp) paint(fill, stroke, evenOdd bool) {
	if ip.holding >= len(ip.held) {
		return
	}
	held, clip := &ip.held[ip.holding], ip.clip
	ip.holding++
	*held, ip.path, ip.clip = ip.path, *held, clipNone
	ip.path.Reset()
	defer func() {
		ip.holding--
		held.Reset()
		*held, ip.path, ip.clip = ip.path, *held, clipNone
	}()

	if ip.hidden > 0 {
		fill, stroke = false, false
	}
	if !held.IsEmpty() && (fill || stroke) {
		bbox := held.Bounds(ip.gs.ctm)
		if stroke {
			bbox = held.StrokeBounds(&ip.gs.stroke, ip.gs.ctm)
		}
		d := ip.beginDraw(bbox)
		if fill {
			if ip.gs.fill.cs.IsPattern() {
				ip.fillWithPattern(held, evenOdd, false)
			} else {
				ip.dev.FillPath(held, evenOdd, ip.gs.ctm, ip.gs.fill.cs, ip.gs.fill.value, ip.gs.fillAlpha, ip.gs.params)
			}
		}
		if stroke {
			if ip.gs.strokeColor.cs.IsPattern() {
				ip.fillWithPattern(held, evenOdd, true)
			} else {
				ip.dev.StrokePath(held, &ip.gs.stroke, ip.gs.ctm, ip.gs.strokeColor.cs, ip.gs.strokeColor.value, ip.gs.strokeAlpha, ip.gs.params)
			}
		}
		ip.endDraw(d)
	}

	if clip != clipNone {
		if held.IsEmpty() {
			held.MoveTo(0, 0)
		}
		ip.dev.ClipPath(held, clip == clipEvenOdd, ip.gs.ctm, ip.scissor)
		ip.gs.clipDepth++
	}
}

// drawState records what beginDraw opened around one drawing operation.
type drawState struct {
	mask  bool
	group bool
}

// beginDraw wraps a drawing operation in the soft mask and the blend group
// the graphics state calls for. bbox bounds what is about to be drawn.
func (ip *interp) beginDraw(bbox raster.Rect) drawState {
	var d drawState
	d.mask = ip.beginSoftMask(bbox)
	if ip.gs.blend != BlendNormal {
		ip.dev.BeginGroup(bbox, nil, false, false, ip.gs.blend, 1)
		d.group = true
	}
	return d
}

func (ip *interp) endDraw(d drawState) {
	if d.group {
		ip.dev.EndGroup()
	}
	if d.mask {
		ip.dev.PopClip()
	}
}

// beginSoftMask renders the soft mask in force, if any, so that the drawing
// operation about to happen is clipped by it.
func (ip *interp) beginSoftMask(bbox raster.Rect) bool {
	sm := ip.gs.softMask
	if sm == nil || ip.depth >= maxNesting {
		return false
	}

	area := raster.InfiniteRect
	if !sm.luminosity {
		area = ip.formBounds(sm.form, sm.ctm)
	}
	area = area.Intersect(bbox)

	cs := sm.cs
	if sm.luminosity && cs == nil {
		cs = DeviceGray
	}
	ip.dev.BeginMask(area, sm.luminosity, cs, sm.backdrop, ip.gs.params)

	saved := ip.gs
	ip.gs = ip.gs.clone()
	savedRes := ip.res
	ip.gs.softMask = nil
	ip.gs.ctm = sm.ctm
	ip.gs.fillAlpha, ip.gs.strokeAlpha = 1, 1
	ip.gs.blend = BlendNormal
	if sm.res != nil {
		ip.res = sm.res
	}
	ip.runForm(sm.form, true)
	ip.gs = saved
	ip.res = savedRes

	ip.dev.EndMask(transferTable(sm.transfer))
	return true
}

// formBounds returns the bounding box of a form XObject under ctm.
func (ip *interp) formBounds(st *Stream, ctm raster.Matrix) raster.Rect {
	f := ip.doc.f
	b := f.GetFloats(st.Dict["BBox"])
	if len(b) != 4 {
		return raster.InfiniteRect
	}
	r := raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
	return raster.Concat(ip.doc.matrix(st.Dict["Matrix"], raster.Identity), ctm).ApplyRect(r)
}
