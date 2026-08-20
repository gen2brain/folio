package gfx

import (
	"fmt"
	"math"

	"github.com/gen2brain/pdf/font"
	"github.com/gen2brain/pdf/raster"
)

// maxCachedGlyph is the area above which a glyph is drawn straight into the
// page rather than rendered to a mask and remembered.
const maxCachedGlyph = 100 * 100

// maxGlyphDepth bounds a font whose glyph procedures show text in the same
// font, which is a cycle a document is free to contain. A font that draws
// its own glyphs is expected to stop sooner; this is the backstop.
const maxGlyphDepth = 32

// DrawDevice renders into a raster.Pixmap. Everything it is given is in the
// pixmap's own coordinates, so a caller rendering part of a page translates
// the transform rather than the device.
type DrawDevice struct {
	BaseDevice

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
	depth int
	// cache remembers rendered glyph masks between pages, and err collects
	// what a damaged document did; both belong to whoever opened the file.
	cache *raster.GlyphCache
	err   func(error)
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

// NewDrawDevice returns a device that renders into dst.
func NewDrawDevice(dst *raster.Pixmap) *DrawDevice {
	// A pixmap covers the device rectangle its origin and size describe, so
	// a destination that is a band of a page, or a group's own buffer, is
	// drawn in the page's coordinates and mapped by the blitter.
	return &DrawDevice{
		dst: dst,
		ras: raster.NewRasterizer(dst.X+dst.W, dst.Y+dst.H),
		clip: clipState{rect: raster.Rect{
			X0: float32(dst.X), Y0: float32(dst.Y),
			X1: float32(dst.X + dst.W), Y1: float32(dst.Y + dst.H),
		}},
		src: make([]uint8, 0, 8),
	}
}

// Pixmap returns what has been drawn.
func (d *DrawDevice) Pixmap() *raster.Pixmap { return d.dst }

// Clip returns the rectangle drawing is confined to, which starts as the
// whole destination.
func (d *DrawDevice) Clip() raster.Rect { return d.clip.rect }

// SetGlyphCache gives the device somewhere to keep rendered glyph masks. The
// cache belongs to the document rather than to one rendering, so that a
// second page of the same file finds the glyphs of the first.
func (d *DrawDevice) SetGlyphCache(c *raster.GlyphCache) { d.cache = c }

// SetErrorFunc says where to report what went wrong on the way. A page that
// fails in the middle still draws the part that worked.
func (d *DrawDevice) SetErrorFunc(f func(error)) { d.err = f }

func (d *DrawDevice) fail(err error) {
	if d.err != nil {
		d.err(err)
	}
}

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
	cs.Convert(col, d.src)
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
	x0, y0, x1, y1 := box.Outer()
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
func (d *DrawDevice) FillShade(sh Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	if !d.drawing() || sh == nil {
		return
	}
	m := raster.Concat(sh.Transform(), ctm)
	box := d.clip.rect
	if b := sh.Bounds(); !b.IsEmpty() {
		box = box.Intersect(m.ApplyRect(b))
	}
	if box.IsEmpty() {
		return
	}
	s := sh.Shader(d.dst.Model, m, box)
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
		cs.Convert(backdrop, col)
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
func (d *DrawDevice) EndMask(transfer *[256]uint8) {
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
	m := g.px.Mask(g.luminosity, transfer)
	if m == nil {
		d.clip = clipState{rect: raster.EmptyRect}
		return
	}
	m.MulMask(f.clip.mask)
	d.clip = clipState{rect: g.box, mask: m}
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
	x0, y0, x1, y1 := box.Outer()
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
	d.eachGlyph(t, ctm, func(f Font, prog *font.Font, gid int, m raster.Matrix) {
		if prog == nil {
			d.runGlyph(f, gid, m, cs, col, alpha)
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
	d.eachGlyph(t, ctm, func(f Font, prog *font.Font, gid int, m raster.Matrix) {
		if prog == nil {
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
		d.eachGlyph(t, ctm, func(f Font, prog *font.Font, gid int, m raster.Matrix) {
			if prog == nil {
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

// runGlyph lets a font with no program draw one glyph for itself. The scratch
// color goes with it, because what the glyph draws runs through this device
// and would otherwise overwrite the color of the text it belongs to.
func (d *DrawDevice) runGlyph(f Font, gid int, m raster.Matrix, cs *ColorSpace, col []float32, alpha float32) {
	if d.depth >= maxGlyphDepth {
		return
	}
	saved := d.src
	d.src = nil
	d.depth++
	f.RunGlyph(d, gid, m, cs, col, alpha, d.depth)
	d.depth--
	d.src = saved
}

// eachGlyph walks the glyphs of a text object, handing each its transform in
// device space.
func (d *DrawDevice) eachGlyph(t *Text, ctm raster.Matrix, fn func(Font, *font.Font, int, raster.Matrix)) {
	for i := range t.Spans {
		sp := &t.Spans[i]
		if sp.Font == nil {
			continue
		}
		prog := sp.Font.Program()
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
	x0, y0, x1, y1 := box.Outer()
	if d.cache == nil || (x1-x0)*(y1-y0) > maxCachedGlyph || x1 <= x0 || y1 <= y0 {
		d.fillPath(p, false, full, paint)
		return
	}

	cache := d.cache
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

// unitRect is the square an image transform maps onto the page.
var unitRect = raster.Rect{X1: 1, Y1: 1}

// FillImage implements Device.
func (d *DrawDevice) FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	if !d.drawing() || img == nil {
		return
	}
	src := d.decodeImage(img, d.space(), shrinkFor(img, ctm))
	if src == nil {
		return
	}
	d.drawImage(src, ctm, raster.Paint{Alpha: alphaByte(alpha), Clip: d.clip.mask}, img.Smooth())
}

// FillImageMask implements Device.
func (d *DrawDevice) FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, col []float32, alpha float32, cp ColorParams) {
	if !d.drawing() || img == nil {
		return
	}
	src := d.maskPixmap(img, shrinkFor(img, ctm))
	if src == nil {
		return
	}
	d.drawImage(src, ctm, d.paint(cs, col, alpha), img.Smooth())
}

// ClipImageMask implements Device.
func (d *DrawDevice) ClipImageMask(img Image, ctm raster.Matrix, scissor raster.Rect) {
	src := d.maskPixmap(img, shrinkFor(img, ctm))
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
func (d *DrawDevice) maskPixmap(img Image, shrink int) *raster.Pixmap {
	if img == nil {
		return nil
	}
	if img.Stencil() {
		return d.decodeImage(img, nil, shrink)
	}
	px := d.decodeImage(img, DeviceGray, shrink)
	if px == nil {
		return nil
	}
	return px.Coverage()
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
	return subsampleFor(src.W, src.H, ctm)
}

// shrinkFor is subsampleBy asked before anything is decoded, from the size the
// image declares. A page that clips to the same scan forty times decodes
// it forty times if the answer only arrives after the pixmap does, and the
// full size pixmap is too large for the cache to keep.
func shrinkFor(img Image, ctm raster.Matrix) int {
	if img == nil {
		return 0
	}
	w, h := img.Size()
	return subsampleFor(w, h, ctm)
}

func subsampleFor(w, h int, ctm raster.Matrix) int {
	dw, dh := deviceExtent(ctm)
	if dw <= 0 || dh <= 0 {
		return 0
	}
	n := 0
	for sw, sh := float32(w), float32(h); sw >= dw*2 && sh >= dh*2 && n < 8; n++ {
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
	return abs32(ctm.A) + abs32(ctm.C), abs32(ctm.B) + abs32(ctm.D)
}

func abs32(v float32) float32 {
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
func (d *DrawDevice) decodeImage(img Image, dst *ColorSpace, shrink int) *raster.Pixmap {
	if img == nil {
		return nil
	}
	px, err := img.Pixels(dst, shrink)
	if err != nil {
		d.fail(fmt.Errorf("image: %w", err))
		return nil
	}
	return px
}
