package gfx

import "github.com/gen2brain/pdf/raster"

// BBoxDevice accumulates the bounding box of everything drawn.
type BBoxDevice struct {
	BaseDevice
	rect raster.Rect
	// clip bounds what counts; a nested clip narrows it further.
	clips []raster.Rect
}

// NewBBoxDevice returns a device that measures a page.
func NewBBoxDevice() *BBoxDevice {
	return &BBoxDevice{rect: raster.EmptyRect}
}

// Bounds returns what has been drawn so far.
func (d *BBoxDevice) Bounds() raster.Rect { return d.rect }

func (d *BBoxDevice) add(r raster.Rect) {
	if len(d.clips) > 0 {
		r = r.Intersect(d.clips[len(d.clips)-1])
	}
	if !r.IsEmpty() && !r.IsInfinite() {
		d.rect = d.rect.Union(r)
	}
}

func (d *BBoxDevice) pushClip(r raster.Rect) {
	if len(d.clips) > 0 {
		r = r.Intersect(d.clips[len(d.clips)-1])
	}
	d.clips = append(d.clips, r)
}

// FillPath implements Device.
func (d *BBoxDevice) FillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.add(p.Bounds(ctm))
}

// StrokePath implements Device.
func (d *BBoxDevice) StrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.add(p.StrokeBounds(s, ctm))
}

// ClipPath implements Device.
func (d *BBoxDevice) ClipPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect) {
	d.pushClip(p.Bounds(ctm))
}

// ClipStrokePath implements Device.
func (d *BBoxDevice) ClipStrokePath(p *raster.Path, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.pushClip(p.StrokeBounds(s, ctm))
}

// FillText implements Device.
func (d *BBoxDevice) FillText(t *Text, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.add(t.Bounds(ctm))
}

// StrokeText implements Device.
func (d *BBoxDevice) StrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.add(t.Bounds(ctm))
}

// ClipText implements Device.
func (d *BBoxDevice) ClipText(t *Text, ctm raster.Matrix, scissor raster.Rect) {
	d.pushClip(t.Bounds(ctm))
}

// ClipStrokeText implements Device.
func (d *BBoxDevice) ClipStrokeText(t *Text, s *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.pushClip(t.Bounds(ctm))
}

// FillShade implements Device.
func (d *BBoxDevice) FillShade(sh Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	box := sh.Bounds()
	if box.IsEmpty() {
		if len(d.clips) > 0 {
			d.add(d.clips[len(d.clips)-1])
		}
		return
	}
	d.add(raster.Concat(sh.Transform(), ctm).ApplyRect(box))
}

// FillImage implements Device.
func (d *BBoxDevice) FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.add(ctm.ApplyRect(raster.Rect{X0: 0, Y0: 0, X1: 1, Y1: 1}))
}

// FillImageMask implements Device.
func (d *BBoxDevice) FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, c []float32, alpha float32, cp ColorParams) {
	d.add(ctm.ApplyRect(raster.Rect{X0: 0, Y0: 0, X1: 1, Y1: 1}))
}

// ClipImageMask implements Device.
func (d *BBoxDevice) ClipImageMask(img Image, ctm raster.Matrix, scissor raster.Rect) {
	d.pushClip(ctm.ApplyRect(raster.Rect{X0: 0, Y0: 0, X1: 1, Y1: 1}))
}

// PopClip implements Device.
func (d *BBoxDevice) PopClip() {
	if n := len(d.clips); n > 0 {
		d.clips = d.clips[:n-1]
	}
}

// BeginMask implements Device.
func (d *BBoxDevice) BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams) {
	d.pushClip(area)
}

// EndMask implements Device.
func (d *BBoxDevice) EndMask(*[256]uint8) {}

// BeginTile implements Device.
func (d *BBoxDevice) BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int {
	d.pushClip(area)
	return 0
}

// EndTile implements Device.
func (d *BBoxDevice) EndTile() { d.PopClip() }
