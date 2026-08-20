package pdf

import (
	"math"

	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/raster"
)

// maxCachedGlyph is the area above which a glyph is drawn straight into the
// page rather than rendered to a mask and remembered.
const maxCachedGlyph = 100 * 100

// DrawDevice renders into a raster.Pixmap. Everything it is given is in the
// pixmap's own coordinates, so a caller rendering part of a page translates
// the transform rather than the device.
type DrawDevice struct {
	BaseDevice

	doc *Document
	dst *raster.Pixmap
	ras *raster.Rasterizer
	mas *raster.Rasterizer

	clip  clipState
	stack []drawFrame
	// off counts the reasons not to draw, which is a group or a mask whose
	// area came out empty. Clips are still tracked, so the stacks stay
	// balanced.
	off int
	// knockout is set while the destination is a knockout group, whose
	// elements replace one another rather than layering.
	knockout bool

	flat    float32
	stroked raster.Path
	// src is the destination color of the operation being drawn, valid only
	// until the next one; a nested interpreter takes its own.
	src   []uint8
	res   Dict
	depth int
}

// clipState is the clip in force: a rectangle, and an optional mask over it.
type clipState struct {
	rect raster.Rect
	mask *raster.Pixmap
}

// drawFrame is one entry of the clip and group stack: what to put back, and
// what to do with anything drawn while it was open.
type drawFrame struct {
	clip clipState
	off  bool
	// dst is the destination to go back to, and group what was opened, when
	// the frame opened a transparency group or a soft mask.
	dst   *raster.Pixmap
	group *groupFrame
}

// groupFrame is a transparency group or a soft mask being drawn into.
type groupFrame struct {
	px    *raster.Pixmap
	box   raster.Rect
	blend raster.BlendMode
	alpha uint8
	// mask and luminosity describe a soft mask, which ends as a clip rather
	// than as something composited onto the page.
	mask       bool
	luminosity bool
	// knockout is what the destination this group closes into was, which is
	// also how this group has to composite into it.
	knockout bool
}

// NewDrawDevice returns a device that renders a page of doc into dst. The
// document is needed for the glyph cache and for Type3 glyph procedures,
// which are content streams and have to be interpreted where they are drawn.
func NewDrawDevice(doc *Document, dst *raster.Pixmap) *DrawDevice {
	return &DrawDevice{
		doc: doc,
		dst: dst,
		ras: raster.NewRasterizer(dst.W, dst.H),
		clip: clipState{rect: raster.Rect{
			X0: 0, Y0: 0, X1: float32(dst.W), Y1: float32(dst.H),
		}},
		src: make([]uint8, 0, 8),
	}
}

// Pixmap returns what has been drawn.
func (d *DrawDevice) Pixmap() *raster.Pixmap { return d.dst }

// SetFlatness sets how far a flattened curve may stray from the true one, in
// device pixels.
func (d *DrawDevice) SetFlatness(tol float32) {
	d.flat = tol
	d.ras.SetFlatness(tol)
}

func (d *DrawDevice) paint(cs *ColorSpace, col []float32, alpha float32) raster.Paint {
	if n := d.dst.N; cap(d.src) < n {
		d.src = make([]uint8, n)
	} else {
		d.src = d.src[:n]
	}
	convertColor(cs, col, d.src)
	return raster.Paint{Color: d.src, Alpha: alphaByte(alpha), Clip: d.clip.mask}
}

func alphaByte(a float32) uint8 {
	switch {
	case a <= 0:
		return 0
	case a >= 1:
		return 255
	}
	return uint8(a*255 + 0.5)
}

// convertColor turns a color in cs into the components of the destination.
func convertColor(cs *ColorSpace, col []float32, out []uint8) {
	if cs == nil {
		cs = DeviceGray
		col = nil
	}
	switch len(out) {
	case 1:
		out[0] = clamp8(cs.Gray(col))
	case 4:
		c, m, y, k := cs.CMYK(col)
		out[0], out[1], out[2], out[3] = clamp8(c), clamp8(m), clamp8(y), clamp8(k)
	default:
		r, g, b := cs.RGB(col)
		if len(out) >= 3 {
			out[0], out[1], out[2] = clamp8(r), clamp8(g), clamp8(b)
		}
	}
}

func (d *DrawDevice) drawing() bool { return d.off == 0 && !d.clip.rect.IsEmpty() }

// fillPath rasterizes a path in device space and blits the paint through it.
func (d *DrawDevice) fillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, paint raster.Paint) {
	rule := raster.NonZero
	if evenOdd {
		rule = raster.EvenOdd
	}
	d.ras.Reset()
	d.ras.SetClip(d.clip.rect)
	d.ras.AddPath(p, ctm)
	d.ras.Fill(d.dst, rule, paint)
}

// strokeOutline turns a stroke into the path that fills it, in the space the
// path is already in. The result lives until the next stroke.
func (d *DrawDevice) strokeOutline(p *raster.Path, s *raster.Stroke, ctm raster.Matrix) *raster.Path {
	return s.OutlineInto(&d.stroked, p, ctm.Expansion())
}

// deviceStroke strokes a path that is not in user space, a glyph outline, by
// moving it into device space and scaling the stroke to match.
func deviceStroke(p *raster.Path, s *raster.Stroke, m raster.Matrix, scale float32) *raster.Path {
	sd := *s
	sd.Width *= scale
	sd.DashPhase *= scale
	if len(s.Dash) > 0 {
		sd.Dash = make([]float32, len(s.Dash))
		for i, v := range s.Dash {
			sd.Dash[i] = v * scale
		}
	}
	return sd.Outline(p.Transform(m), 1)
}

// FillPath implements Device.
func (d *DrawDevice) FillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	d.fillPath(p, evenOdd, ctm, d.paint(cs, col, alpha))
}

// StrokePath implements Device.
func (d *DrawDevice) StrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	d.fillPath(d.strokeOutline(p, s, ctm), false, ctm, d.paint(cs, col, alpha))
}

// ClipPath implements Device.
func (d *DrawDevice) ClipPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect) {
	d.pushPathClip(p, evenOdd, ctm)
}

// ClipStrokePath implements Device.
func (d *DrawDevice) ClipStrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.pushPathClip(d.strokeOutline(p, s, ctm), false, ctm)
}

func (d *DrawDevice) pushPathClip(p *raster.Path, evenOdd bool, ctm raster.Matrix) {
	if r, ok := p.AsRect(ctm); ok {
		d.push(clipState{rect: d.clip.rect.Intersect(r), mask: d.clip.mask})
		return
	}
	d.push(d.maskClip(p.Bounds(ctm), func(r *raster.Rasterizer, ox, oy int) {
		m := ctm
		m.E -= float32(ox)
		m.F -= float32(oy)
		r.AddPath(p, m)
	}, evenOdd))
}

// maskClip renders geometry into a clip mask covering its own bounding box,
// narrowed by whatever clip is already in force.
func (d *DrawDevice) maskClip(bounds raster.Rect, add func(*raster.Rasterizer, int, int), evenOdd bool) clipState {
	box := bounds.Intersect(d.clip.rect)
	x0, y0, x1, y1 := outerBox(box)
	if x1 <= x0 || y1 <= y0 {
		return clipState{rect: raster.EmptyRect}
	}
	mask := raster.NewMask(x1-x0, y1-y0)
	if mask == nil {
		return clipState{rect: raster.EmptyRect}
	}
	mask.X, mask.Y = x0, y0

	if d.mas == nil {
		d.mas = raster.NewRasterizer(mask.W, mask.H)
	} else {
		d.mas.SetSize(mask.W, mask.H)
	}
	d.mas.SetFlatness(d.flat)
	d.mas.SetClip(raster.Rect{
		X0: box.X0 - float32(x0), Y0: box.Y0 - float32(y0),
		X1: box.X1 - float32(x0), Y1: box.Y1 - float32(y0),
	})
	rule := raster.NonZero
	if evenOdd {
		rule = raster.EvenOdd
	}
	add(d.mas, x0, y0)
	d.mas.Sweep(rule, mask.MaskBlitter())

	mask.MulMask(d.clip.mask)
	return clipState{rect: box, mask: mask}
}

func outerBox(r raster.Rect) (x0, y0, x1, y1 int) {
	if r.IsEmpty() {
		return 0, 0, 0, 0
	}
	return int(math.Floor(float64(r.X0))), int(math.Floor(float64(r.Y0))),
		int(math.Ceil(float64(r.X1))), int(math.Ceil(float64(r.Y1)))
}

func (d *DrawDevice) push(c clipState) {
	d.stack = append(d.stack, drawFrame{clip: d.clip})
	d.clip = c
}

// PopClip implements Device. It also closes a group, because a content stream
// that leaves its stack unbalanced has to leave the device balanced anyway.
func (d *DrawDevice) PopClip() {
	n := len(d.stack)
	if n == 0 {
		return
	}
	f := d.stack[n-1]
	d.stack = d.stack[:n-1]
	d.clip = f.clip
	if f.off {
		d.off--
	}
	if f.dst != nil {
		d.dst = f.dst
	}
	if g := f.group; g != nil {
		d.knockout = g.knockout
		if !g.mask {
			if g.knockout {
				d.dst.KnockoutOver(g.px, g.alpha)
			} else {
				d.dst.BlendOver(g.px, g.alpha, g.blend)
			}
		}
	}
}

// FillShade implements Device.
func (d *DrawDevice) FillShade(sh *Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	if !d.drawing() || sh == nil {
		return
	}
	m := raster.Concat(sh.Matrix, ctm)
	box := d.clip.rect
	if !sh.BBox.IsEmpty() {
		box = box.Intersect(m.ApplyRect(sh.BBox))
	}
	if box.IsEmpty() {
		return
	}
	var s raster.Shader
	switch sh.Type {
	case 1:
		if p := d.functionShader(sh, m, box); p != nil {
			s = p
		}
	case 2, 3:
		if g := newGradient(sh, m, d.dst.N); g != nil {
			s = g
		}
	case 4, 5, 6, 7:
		if p := d.meshShader(sh, m, box); p != nil {
			s = p
		}
	}
	if s == nil {
		return
	}
	var p raster.Path
	p.Rect(box.X0, box.Y0, box.X1-box.X0, box.Y1-box.Y0)
	d.ras.Reset()
	d.ras.SetClip(d.clip.rect)
	d.ras.AddPath(&p, raster.Identity)
	d.ras.FillShader(d.dst, raster.NonZero, s, raster.Paint{
		Alpha: alphaByte(alpha), Clip: d.clip.mask,
	})
}

// functionShader evaluates a type 1 shading over a grid of its domain, sized
// to how much of the page the domain covers.
func (d *DrawDevice) functionShader(sh *Shade, m raster.Matrix, box raster.Rect) *pixmapShader {
	if len(sh.Function) == 0 {
		return nil
	}
	full := raster.Concat(sh.FuncMatrix, m)
	inv, ok := full.Invert()
	if !ok {
		return nil
	}
	dom := sh.Domain4()
	dx, dy := dom[1]-dom[0], dom[3]-dom[2]
	if dx <= 0 || dy <= 0 {
		return nil
	}
	on := full.ApplyRect(raster.Rect{X0: dom[0], Y0: dom[2], X1: dom[1], Y1: dom[3]}).Intersect(box)
	if on.IsEmpty() {
		return nil
	}
	w := clampInt(int(on.X1-on.X0)+1, 1, maxShadeGrid)
	h := clampInt(int(on.Y1-on.Y0)+1, 1, maxShadeGrid)
	px := raster.NewPixmap(d.dst.Model, w, h, true)
	if px == nil {
		return nil
	}

	n := px.Comps()
	col := make([]float32, shadeComps(sh))
	for j := 0; j < h; j++ {
		y := float64(dom[2]) + (float64(j)+0.5)/float64(h)*float64(dy)
		row := px.Row(j)
		for i := 0; i < w; i++ {
			x := float64(dom[0]) + (float64(i)+0.5)/float64(w)*float64(dx)
			shadeColor(sh, col, x, y)
			convertColor(sh.CS, col, row[i*n:i*n+px.N])
			row[i*n+px.N] = 255
		}
	}
	grid := raster.Matrix{
		A: float32(w) / dx, D: float32(h) / dy,
		E: -dom[0] * float32(w) / dx, F: -dom[2] * float32(h) / dy,
	}
	return &pixmapShader{px: px, inv: raster.Concat(inv, grid), n: d.dst.N}
}

// meshShader paints a type 4 to 7 shading into a pixmap of the part of the
// page it covers. The triangles meet without anti-aliasing, so that the mesh
// is smooth inside and its own edge is the only one the rasterizer sees.
func (d *DrawDevice) meshShader(sh *Shade, m raster.Matrix, box raster.Rect) *pixmapShader {
	x0, y0, x1, y1 := outerBox(box)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	px := raster.NewPixmap(d.dst.Model, x1-x0, y1-y0, true)
	if px == nil {
		return nil
	}
	if !d.paintMesh(sh, raster.Concat(m, raster.Translate(float32(-x0), float32(-y0))), px) {
		return nil
	}
	return &pixmapShader{
		px: px, inv: raster.Translate(float32(-x0), float32(-y0)), n: d.dst.N,
	}
}

// BeginMask implements Device. The mask group draws into a pixmap of its own,
// which EndMask turns into the clip that narrows what comes next.
func (d *DrawDevice) BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams) {
	if d.off > 0 {
		d.pushOff()
		return
	}
	box := area.Intersect(d.clip.rect)
	g := &groupFrame{box: box, mask: true, luminosity: luminosity, alpha: 255, knockout: d.knockout}
	g.px = d.groupPixmap(box, !luminosity)
	if g.px != nil && luminosity {
		col := make([]uint8, d.dst.N)
		convertColor(cs, backdrop, col)
		g.px.FillRect(g.px.X, g.px.Y, g.px.X+g.px.W, g.px.Y+g.px.H,
			raster.Paint{Color: col, Alpha: 255})
	}
	if g.px == nil {
		d.stack = append(d.stack, drawFrame{clip: d.clip, dst: d.dst, group: g, off: true})
		d.off++
		return
	}
	d.stack = append(d.stack, drawFrame{clip: d.clip, dst: d.dst, group: g})
	d.dst = g.px
	d.knockout = false
	d.clip = clipState{rect: box, mask: d.clip.mask}
}

// EndMask implements Device. The frame stays on the stack, because the mask is
// a clip and the interpreter pops it.
func (d *DrawDevice) EndMask(transfer *Function) {
	n := len(d.stack)
	if n == 0 {
		return
	}
	f := &d.stack[n-1]
	if f.off {
		f.off = false
		d.off--
	}
	g := f.group
	if g == nil || !g.mask {
		return
	}
	f.group = nil
	d.dst = f.dst
	d.knockout = g.knockout
	if g.px == nil {
		d.clip = clipState{rect: raster.EmptyRect}
		return
	}
	m := g.px.Mask(g.luminosity, transferTable(transfer))
	if m == nil {
		d.clip = clipState{rect: raster.EmptyRect}
		return
	}
	m.MulMask(f.clip.mask)
	d.clip = clipState{rect: g.box, mask: m}
}

// transferTable evaluates a soft mask's /TR into the 256 values a mask can
// take, nil when the function is the identity or unusable.
func transferTable(fn *Function) *[256]uint8 {
	if fn == nil {
		return nil
	}
	var t [256]uint8
	var out [1]float32
	for i := range t {
		fn.Eval1(out[:], float64(i)/255)
		t[i] = clamp8(out[0])
	}
	return &t
}

// BeginGroup implements Device. A group that cannot see what is under it, or
// that has something to do when it closes, draws into a pixmap of its own;
// a non-isolated group with no alpha, no blend mode and no knockout on either
// side draws straight through, which is exactly the same result.
func (d *DrawDevice) BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32) {
	a := alphaByte(alpha)
	through := !isolated && !knockout && !d.knockout && blend == BlendNormal && a == 255
	if d.off > 0 || through {
		d.push(d.clip)
		return
	}
	box := area.Intersect(d.clip.rect)
	px := d.groupPixmap(box, true)
	if px == nil {
		d.pushOff()
		return
	}
	d.stack = append(d.stack, drawFrame{
		clip: d.clip, dst: d.dst,
		group: &groupFrame{px: px, box: box, blend: blend, alpha: a, knockout: d.knockout},
	})
	d.dst = px
	d.knockout = knockout
	d.clip = clipState{rect: box, mask: d.clip.mask}
}

// EndGroup implements Device.
func (d *DrawDevice) EndGroup() { d.PopClip() }

// groupPixmap allocates the destination a group draws into, covering the whole
// pixels of its area and positioned there, so that everything drawing into it
// keeps working in the page's own coordinates.
func (d *DrawDevice) groupPixmap(box raster.Rect, alpha bool) *raster.Pixmap {
	x0, y0, x1, y1 := outerBox(box)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	px := raster.NewPixmap(d.dst.Model, x1-x0, y1-y0, alpha)
	if px == nil {
		return nil
	}
	px.X, px.Y = x0, y0
	return px
}

func (d *DrawDevice) pushOff() {
	d.stack = append(d.stack, drawFrame{clip: d.clip, off: true})
	d.off++
}

// BeginTile implements Device. Returning zero asks the interpreter to run the
// pattern's content once for every repetition, which is what draws it.
func (d *DrawDevice) BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int {
	d.push(d.clip)
	return 0
}

// EndTile implements Device.
func (d *DrawDevice) EndTile() { d.PopClip() }

// FillText implements Device.
func (d *DrawDevice) FillText(t *Text, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	paint := d.paint(cs, col, alpha)
	d.eachGlyph(t, ctm, func(f *Font, prog *font.Font, gid int, m raster.Matrix) {
		if f.Type3 {
			d.type3Glyph(f, gid, m, cs, col, alpha)
			return
		}
		d.drawGlyph(prog, gid, m, paint)
	})
}

// StrokeText implements Device.
func (d *DrawDevice) StrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	paint := d.paint(cs, col, alpha)
	d.eachGlyph(t, ctm, func(f *Font, prog *font.Font, gid int, m raster.Matrix) {
		if f.Type3 {
			return
		}
		p := prog.GlyphPath(gid)
		if p == nil {
			return
		}
		full := raster.Concat(prog.Matrix, m)
		d.fillPath(deviceStroke(p, s, full, ctm.Expansion()), false, raster.Identity, paint)
	})
}

// ClipText implements Device.
func (d *DrawDevice) ClipText(t *Text, ctm raster.Matrix, scissor raster.Rect) {
	d.pushTextClip(t, ctm, nil)
}

// ClipStrokeText implements Device.
func (d *DrawDevice) ClipStrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.pushTextClip(t, ctm, s)
}

func (d *DrawDevice) pushTextClip(t *Text, ctm raster.Matrix, s *raster.Stroke) {
	d.push(d.maskClip(t.Bounds(ctm), func(r *raster.Rasterizer, ox, oy int) {
		d.eachGlyph(t, ctm, func(f *Font, prog *font.Font, gid int, m raster.Matrix) {
			if f.Type3 {
				return
			}
			p := prog.GlyphPath(gid)
			if p == nil {
				return
			}
			full := raster.Concat(prog.Matrix, m)
			full.E -= float32(ox)
			full.F -= float32(oy)
			if s != nil {
				r.AddPath(deviceStroke(p, s, full, ctm.Expansion()), raster.Identity)
				return
			}
			r.AddPath(p, full)
		})
	}, false))
}

// eachGlyph walks the glyphs of a text object, handing each its transform in
// device space.
func (d *DrawDevice) eachGlyph(t *Text, ctm raster.Matrix, fn func(*Font, *font.Font, int, raster.Matrix)) {
	for i := range t.Spans {
		sp := &t.Spans[i]
		if sp.Font == nil {
			continue
		}
		prog := sp.Font.Program()
		if prog == nil && !sp.Font.Type3 {
			continue
		}
		for _, it := range sp.Items {
			if it.GID < 0 {
				continue
			}
			m := raster.Concat(raster.Matrix{
				A: sp.Trm.A, B: sp.Trm.B, C: sp.Trm.C, D: sp.Trm.D,
				E: it.X, F: it.Y,
			}, ctm)
			fn(sp.Font, prog, it.GID, m)
		}
	}
}

// drawGlyph stamps one glyph, from the cache when it is small enough to be
// worth remembering.
func (d *DrawDevice) drawGlyph(prog *font.Font, gid int, m raster.Matrix, paint raster.Paint) {
	p := prog.GlyphPath(gid)
	if p == nil {
		return
	}
	full := raster.Concat(prog.Matrix, m)

	ix, sx := subPixel(full.E)
	iy, sy := subPixel(full.F)
	phase := full
	phase.E, phase.F = float32(sx)/raster.SubPixels, float32(sy)/raster.SubPixels

	box := p.Bounds(phase)
	x0, y0, x1, y1 := outerBox(box)
	if d.doc == nil || (x1-x0)*(y1-y0) > maxCachedGlyph || x1 <= x0 || y1 <= y0 {
		d.fillPath(p, false, full, paint)
		return
	}

	cache := d.doc.glyphCache()
	key := raster.GlyphKey{
		Font: prog, GID: int32(gid),
		A: full.A, B: full.B, C: full.C, D: full.D,
		SubX: sx, SubY: sy,
	}
	mask := cache.Get(key)
	if mask == nil {
		mask = d.renderGlyph(p, phase, x0, y0, x1, y1)
		cache.Put(key, mask)
	}
	if mask == nil {
		return
	}
	stamp := *mask
	stamp.X += ix
	stamp.Y += iy
	d.dst.DrawMask(&stamp, paint)
}

func (d *DrawDevice) renderGlyph(p *raster.Path, m raster.Matrix, x0, y0, x1, y1 int) *raster.Pixmap {
	mask := raster.NewMask(x1-x0, y1-y0)
	if mask == nil {
		return nil
	}
	mask.X, mask.Y = x0, y0
	if d.mas == nil {
		d.mas = raster.NewRasterizer(mask.W, mask.H)
	} else {
		d.mas.SetSize(mask.W, mask.H)
	}
	d.mas.SetFlatness(d.flat)
	m.E -= float32(x0)
	m.F -= float32(y0)
	d.mas.AddPath(p, m)
	d.mas.Sweep(raster.NonZero, mask.MaskBlitter())
	return mask
}

// subPixel splits a device coordinate into the pixel it lands in and the
// phase inside it, in quarter pixels.
func subPixel(v float32) (int, uint8) {
	f := math.Floor(float64(v))
	i := int(f)
	s := uint8((float64(v) - f) * raster.SubPixels)
	if s >= raster.SubPixels {
		s = raster.SubPixels - 1
	}
	return i, s
}

// type3Glyph interprets a glyph procedure where the glyph goes. A Type3 glyph
// is a content stream, so the only thing that can draw it is the interpreter.
func (d *DrawDevice) type3Glyph(f *Font, code int, m raster.Matrix, cs *ColorSpace, col []float32, alpha float32) {
	if d.doc == nil || d.depth >= maxNesting || code < 0 || code > 255 {
		return
	}
	name := f.GlyphName(uint32(code))
	if name == "" {
		return
	}
	proc := d.doc.f.GetStream(d.doc.f.Lookup(f.CharProcs, name))
	if proc == nil {
		return
	}
	data, err := proc.Data()
	if err != nil {
		d.doc.errorf("Type3 glyph %s: %v", name, err)
		return
	}
	res := f.Resources
	if res == nil {
		res = d.res
	}
	if res == nil {
		res = Dict{}
	}

	ctm := raster.Concat(f.FontMatrix, m)
	var ops int64
	ip := &interp{
		doc:      d.doc,
		dev:      d,
		gs:       newGState(ctm),
		base:     ctm,
		res:      res,
		defaults: &DefaultColorSpaces{},
		scissor:  raster.InfiniteRect,
		ops:      &ops,
		depth:    d.depth + 1,
	}
	ip.gs.fill.cs = cs
	ip.gs.fill.value = append([]float32(nil), col...)
	ip.gs.strokeColor.cs = cs
	ip.gs.strokeColor.value = append([]float32(nil), col...)
	ip.gs.fillAlpha, ip.gs.strokeAlpha = alpha, alpha

	saved := d.src
	d.src = nil
	d.depth++
	ip.run(data)
	ip.finish()
	d.depth--
	d.src = saved
}

// unitRect is the square an image transform maps onto the page.
var unitRect = raster.Rect{X1: 1, Y1: 1}

// FillImage implements Device.
func (d *DrawDevice) FillImage(img *Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	src := d.decodeImage(img, d.space())
	if src == nil {
		return
	}
	d.drawImage(src, ctm, raster.Paint{Alpha: alphaByte(alpha), Clip: d.clip.mask}, img.Interpolate)
}

// FillImageMask implements Device.
func (d *DrawDevice) FillImageMask(img *Image, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() {
		return
	}
	src := d.maskPixmap(img)
	if src == nil {
		return
	}
	d.drawImage(src, ctm, d.paint(cs, col, alpha), img.Interpolate)
}

// ClipImageMask implements Device.
func (d *DrawDevice) ClipImageMask(img *Image, ctm raster.Matrix, scissor raster.Rect) {
	src := d.maskPixmap(img)
	if src == nil {
		d.push(clipState{rect: raster.EmptyRect})
		return
	}
	c := d.maskClip(ctm.ApplyRect(unitRect), func(r *raster.Rasterizer, ox, oy int) {
		m := ctm
		m.E -= float32(ox)
		m.F -= float32(oy)
		var p raster.Path
		p.Rect(0, 0, 1, 1)
		r.AddPath(&p, m)
	}, false)
	if c.mask != nil {
		if inv, ok := sourceMatrix(ctm, src); ok {
			c.mask.MulImage(src, inv)
		}
	}
	d.push(c)
}

// maskPixmap decodes an image into coverage, one byte a pixel, whether it is
// the one bit stencil of an image mask or the gray samples of a soft mask.
func (d *DrawDevice) maskPixmap(img *Image) *raster.Pixmap {
	if img == nil {
		return nil
	}
	if img.Mask {
		return d.decodeImage(img, nil)
	}
	px := d.decodeImage(img, DeviceGray)
	if px == nil {
		return nil
	}
	return flattenGray(px)
}

// drawImage rasterizes the square the image occupies and samples the image
// through it.
func (d *DrawDevice) drawImage(src *raster.Pixmap, ctm raster.Matrix, paint raster.Paint, interpolate bool) {
	if n := subsampleBy(src, ctm); n > 0 {
		src = src.Subsample(n)
	}
	inv, ok := sourceMatrix(ctm, src)
	if !ok {
		return
	}
	var p raster.Path
	p.Rect(0, 0, 1, 1)

	d.ras.Reset()
	d.ras.SetClip(d.clip.rect)
	d.ras.AddPath(&p, ctm)
	d.ras.FillImage(d.dst, src, inv, paint, smoothImage(src, ctm, interpolate))
}

// sourceMatrix maps a device pixel to a pixel of the image.
func sourceMatrix(ctm raster.Matrix, src *raster.Pixmap) (raster.Matrix, bool) {
	inv, ok := ctm.Invert()
	if !ok {
		return raster.Identity, false
	}
	return raster.Concat(inv, raster.Scale(float32(src.W), float32(src.H))), true
}

// subsampleBy is how many times an image should be halved before it is
// sampled, which is what keeps a scan from turning into noise.
func subsampleBy(src *raster.Pixmap, ctm raster.Matrix) int {
	w, h := deviceExtent(ctm)
	if w <= 0 || h <= 0 {
		return 0
	}
	n := 0
	for sw, sh := float32(src.W), float32(src.H); sw >= w*2 && sh >= h*2 && n < 8; n++ {
		sw /= 2
		sh /= 2
	}
	return n
}

// smoothImage decides between bilinear and nearest: magnifying by more than
// four keeps the samples crisp unless the file asks for interpolation.
func smoothImage(src *raster.Pixmap, ctm raster.Matrix, interpolate bool) bool {
	if interpolate {
		return true
	}
	w, h := deviceExtent(ctm)
	return w < float32(src.W)*4 || h < float32(src.H)*4
}

// deviceExtent is how wide and tall the unit square is after ctm.
func deviceExtent(ctm raster.Matrix) (w, h float32) {
	return absf32(ctm.A) + absf32(ctm.C), absf32(ctm.B) + absf32(ctm.D)
}

func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// space is the color space the destination composites in.
func (d *DrawDevice) space() *ColorSpace {
	switch d.dst.N {
	case 1:
		return DeviceGray
	case 4:
		return DeviceCMYK
	}
	return DeviceRGB
}

// decodeImage decodes an image into the destination's components, from the
// document's cache when it is there. A nil space asks for the stencil of an
// image mask.
func (d *DrawDevice) decodeImage(img *Image, dst *ColorSpace) *raster.Pixmap {
	if img == nil || d.doc == nil {
		return nil
	}
	px, err := d.doc.decodedImage(img, dst)
	if err != nil {
		d.doc.errorf("image: %v", err)
		return nil
	}
	return px
}
