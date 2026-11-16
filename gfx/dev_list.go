package gfx

import (
	"fmt"
	"image"
	"math"
	"reflect"
	"sync"

	"github.com/gen2brain/folio/raster"
)

// listKind names the device call a recorded command replays.
type listKind uint8

const (
	cmdFillPath listKind = iota
	cmdStrokePath
	cmdClipPath
	cmdClipStrokePath
	cmdFillText
	cmdStrokeText
	cmdClipText
	cmdClipStrokeText
	cmdIgnoreText
	cmdFillShade
	cmdFillImage
	cmdFillImageMask
	cmdClipImageMask
	cmdPopClip
	cmdDefaultColorSpaces
	cmdBeginMask
	cmdEndMask
	cmdBeginGroup
	cmdEndGroup
	cmdBeginTile
	cmdEndTile
	cmdBeginLayer
	cmdEndLayer
)

const (
	listEvenOdd = 1 << iota
	listLuminosity
	listIsolated
	listKnockout
)

// listCmd is one device call with its arguments. What the interpreter reuses
// between calls, which is the path it paints into, the graphics state's stroke
// and the color slice the operators write through, is copied here; everything
// else it hands over is already a value of its own.
type listCmd struct {
	kind  listKind
	flags uint8
	// bbox is what the command covers in device space, empty when it is
	// state rather than paint and must run wherever the list is replayed.
	bbox   raster.Rect
	blend  BlendMode
	alpha  float32
	ctm    raster.Matrix
	cp     ColorParams
	cs     *ColorSpace
	color  []float32
	obj    any
	stroke *raster.Stroke
	rect   raster.Rect
	view   raster.Rect
	steps  [2]float32
	name   string
}

// ListDevice records what a page draws so that it can be drawn again. One
// recording replays into as many destinations as a caller wants, which is what
// lets a page be rasterized in horizontal bands from several goroutines: every
// operation a raster renderer performs is local to the pixels it touches, so a
// band is the same page with a smaller destination.
type ListDevice struct {
	cmds []listCmd
	// bounds is the union of everything drawn, in device space.
	bounds raster.Rect
}

// NewListDevice returns an empty display list.
func NewListDevice() *ListDevice {
	return &ListDevice{bounds: raster.EmptyRect}
}

// Len returns how many device calls the list holds.
func (d *ListDevice) Len() int { return len(d.cmds) }

// Bounds returns the union of what the list draws, in device space.
func (d *ListDevice) Bounds() raster.Rect { return d.bounds }

func (d *ListDevice) add(c listCmd) { d.cmds = append(d.cmds, c) }

// paint records a command that covers a known part of the page, which a replay
// into a band can skip when the two do not meet.
func (d *ListDevice) paint(c listCmd, box raster.Rect) {
	if !box.IsEmpty() {
		d.bounds = d.bounds.Union(box)
		c.bbox = box
	}
	d.cmds = append(d.cmds, c)
}

func cloneColor(c []float32) []float32 { return append([]float32(nil), c...) }

func flag(b bool, bit uint8) uint8 {
	if b {
		return bit
	}
	return 0
}

// FillPath implements Device.
func (d *ListDevice) FillPath(path *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	p := path.Clone()
	d.paint(listCmd{kind: cmdFillPath, obj: p, flags: flag(evenOdd, listEvenOdd),
		ctm: ctm, cs: cs, color: cloneColor(color), alpha: alpha, cp: cp}, p.Bounds(ctm))
}

// StrokePath implements Device.
func (d *ListDevice) StrokePath(path *raster.Path, stroke *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	p := path.Clone()
	d.paint(listCmd{kind: cmdStrokePath, obj: p, stroke: stroke.Clone(),
		ctm: ctm, cs: cs, color: cloneColor(color), alpha: alpha, cp: cp},
		p.StrokeBounds(stroke, ctm))
}

// ClipPath implements Device.
func (d *ListDevice) ClipPath(path *raster.Path, evenOdd bool, ctm raster.Matrix, scissor raster.Rect) {
	d.add(listCmd{kind: cmdClipPath, obj: path.Clone(), flags: flag(evenOdd, listEvenOdd),
		ctm: ctm, rect: scissor})
}

// ClipStrokePath implements Device.
func (d *ListDevice) ClipStrokePath(path *raster.Path, stroke *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.add(listCmd{kind: cmdClipStrokePath, obj: path.Clone(), stroke: stroke.Clone(),
		ctm: ctm, rect: scissor})
}

// FillText implements Device.
func (d *ListDevice) FillText(text *Text, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.paint(listCmd{kind: cmdFillText, obj: text, ctm: ctm, cs: cs,
		color: cloneColor(color), alpha: alpha, cp: cp}, text.Bounds(ctm))
}

// StrokeText implements Device.
func (d *ListDevice) StrokeText(text *Text, stroke *raster.Stroke, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.paint(listCmd{kind: cmdStrokeText, obj: text, stroke: stroke.Clone(), ctm: ctm,
		cs: cs, color: cloneColor(color), alpha: alpha, cp: cp}, text.Bounds(ctm))
}

// ClipText implements Device.
func (d *ListDevice) ClipText(text *Text, ctm raster.Matrix, scissor raster.Rect) {
	d.add(listCmd{kind: cmdClipText, obj: text, ctm: ctm, rect: scissor})
}

// ClipStrokeText implements Device.
func (d *ListDevice) ClipStrokeText(text *Text, stroke *raster.Stroke, ctm raster.Matrix, scissor raster.Rect) {
	d.add(listCmd{kind: cmdClipStrokeText, obj: text, stroke: stroke.Clone(), ctm: ctm, rect: scissor})
}

// IgnoreText implements Device.
func (d *ListDevice) IgnoreText(text *Text, ctm raster.Matrix) {
	d.add(listCmd{kind: cmdIgnoreText, obj: text, ctm: ctm})
}

// FillShade implements Device.
func (d *ListDevice) FillShade(shade Shade, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.add(listCmd{kind: cmdFillShade, obj: shade, ctm: ctm, alpha: alpha, cp: cp})
}

// FillImage implements Device.
func (d *ListDevice) FillImage(img Image, ctm raster.Matrix, alpha float32, cp ColorParams) {
	d.paint(listCmd{kind: cmdFillImage, obj: img, ctm: ctm, alpha: alpha, cp: cp},
		ctm.ApplyRect(unitRect))
}

// FillImageMask implements Device.
func (d *ListDevice) FillImageMask(img Image, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	d.paint(listCmd{kind: cmdFillImageMask, obj: img, ctm: ctm, cs: cs,
		color: cloneColor(color), alpha: alpha, cp: cp}, ctm.ApplyRect(unitRect))
}

// ClipImageMask implements Device.
func (d *ListDevice) ClipImageMask(img Image, ctm raster.Matrix, scissor raster.Rect) {
	d.add(listCmd{kind: cmdClipImageMask, obj: img, ctm: ctm, rect: scissor})
}

// PopClip implements Device.
func (d *ListDevice) PopClip() { d.add(listCmd{kind: cmdPopClip}) }

// SetDefaultColorSpaces implements Device.
func (d *ListDevice) SetDefaultColorSpaces(defs *DefaultColorSpaces) {
	d.add(listCmd{kind: cmdDefaultColorSpaces, obj: defs})
}

// BeginMask implements Device.
func (d *ListDevice) BeginMask(area raster.Rect, luminosity bool, cs *ColorSpace, backdrop []float32, cp ColorParams) {
	d.add(listCmd{kind: cmdBeginMask, rect: area, flags: flag(luminosity, listLuminosity),
		cs: cs, color: cloneColor(backdrop), cp: cp})
}

// EndMask implements Device.
func (d *ListDevice) EndMask(transfer *[256]uint8) {
	d.add(listCmd{kind: cmdEndMask, obj: transfer})
}

// BeginGroup implements Device.
func (d *ListDevice) BeginGroup(area raster.Rect, cs *ColorSpace, isolated, knockout bool, blend BlendMode, alpha float32) {
	d.add(listCmd{kind: cmdBeginGroup, rect: area, cs: cs,
		flags: flag(isolated, listIsolated) | flag(knockout, listKnockout),
		blend: blend, alpha: alpha})
}

// EndGroup implements Device.
func (d *ListDevice) EndGroup() { d.add(listCmd{kind: cmdEndGroup}) }

// BeginTile implements Device. A list records what the interpreter drew, and
// the interpreter only repeats a tile itself when the device says it will not,
// so the recording always holds every repetition.
func (d *ListDevice) BeginTile(area, view raster.Rect, xstep, ystep float32, ctm raster.Matrix) int {
	d.add(listCmd{kind: cmdBeginTile, rect: area, view: view,
		steps: [2]float32{xstep, ystep}, ctm: ctm})
	return 0
}

// EndTile implements Device.
func (d *ListDevice) EndTile() { d.add(listCmd{kind: cmdEndTile}) }

// BeginLayer implements Device.
func (d *ListDevice) BeginLayer(name string) {
	d.add(listCmd{kind: cmdBeginLayer, name: name})
}

// EndLayer implements Device.
func (d *ListDevice) EndLayer() { d.add(listCmd{kind: cmdEndLayer}) }

// Close implements Device.
func (d *ListDevice) Close() error { return nil }

// Replay draws the list into another device. It may be called any number of
// times, and from several goroutines at once as long as each is given a device
// of its own: a list is read-only once the page has been run into it.
func (d *ListDevice) Replay(dev Device) { d.ReplayClip(dev, raster.InfiniteRect) }

// ReplayClip draws the list into another device, skipping what falls outside
// clip. What is skipped is only paint: a clip, a group or a layer runs
// wherever the list is replayed, because the calls after it depend on it.
func (d *ListDevice) ReplayClip(dev Device, clip raster.Rect) {
	for i := range d.cmds {
		c := &d.cmds[i]
		if !c.bbox.IsEmpty() && c.bbox.Intersect(clip).IsEmpty() {
			continue
		}
		switch c.kind {
		case cmdFillPath:
			dev.FillPath(c.obj.(*raster.Path), c.flags&listEvenOdd != 0, c.ctm, c.cs, c.color, c.alpha, c.cp)
		case cmdStrokePath:
			dev.StrokePath(c.obj.(*raster.Path), c.stroke, c.ctm, c.cs, c.color, c.alpha, c.cp)
		case cmdClipPath:
			dev.ClipPath(c.obj.(*raster.Path), c.flags&listEvenOdd != 0, c.ctm, c.rect)
		case cmdClipStrokePath:
			dev.ClipStrokePath(c.obj.(*raster.Path), c.stroke, c.ctm, c.rect)
		case cmdFillText:
			dev.FillText(c.obj.(*Text), c.ctm, c.cs, c.color, c.alpha, c.cp)
		case cmdStrokeText:
			dev.StrokeText(c.obj.(*Text), c.stroke, c.ctm, c.cs, c.color, c.alpha, c.cp)
		case cmdClipText:
			dev.ClipText(c.obj.(*Text), c.ctm, c.rect)
		case cmdClipStrokeText:
			dev.ClipStrokeText(c.obj.(*Text), c.stroke, c.ctm, c.rect)
		case cmdIgnoreText:
			dev.IgnoreText(c.obj.(*Text), c.ctm)
		case cmdFillShade:
			dev.FillShade(c.obj.(Shade), c.ctm, c.alpha, c.cp)
		case cmdFillImage:
			dev.FillImage(c.obj.(Image), c.ctm, c.alpha, c.cp)
		case cmdFillImageMask:
			dev.FillImageMask(c.obj.(Image), c.ctm, c.cs, c.color, c.alpha, c.cp)
		case cmdClipImageMask:
			dev.ClipImageMask(c.obj.(Image), c.ctm, c.rect)
		case cmdPopClip:
			dev.PopClip()
		case cmdDefaultColorSpaces:
			dev.SetDefaultColorSpaces(c.obj.(*DefaultColorSpaces))
		case cmdBeginMask:
			dev.BeginMask(c.rect, c.flags&listLuminosity != 0, c.cs, c.color, c.cp)
		case cmdEndMask:
			tr, _ := c.obj.(*[256]uint8)
			dev.EndMask(tr)
		case cmdBeginGroup:
			dev.BeginGroup(c.rect, c.cs, c.flags&listIsolated != 0, c.flags&listKnockout != 0, c.blend, c.alpha)
		case cmdEndGroup:
			dev.EndGroup()
		case cmdBeginTile:
			dev.BeginTile(c.rect, c.view, c.steps[0], c.steps[1], c.ctm)
		case cmdEndTile:
			dev.EndTile()
		case cmdBeginLayer:
			dev.BeginLayer(c.name)
		case cmdEndLayer:
			dev.EndLayer()
		}
	}
}

// DrawBands draws the list into the area of device space r, rows at a time,
// and hands each band to fn in order from the top. A band is a pixmap of
// model whose X and Y are its place in device space, valid only until fn
// returns. newDevice makes the device a band is drawn through, threads bands
// are drawn at once, and an error from fn stops the drawing and is returned.
func (d *ListDevice) DrawBands(r image.Rectangle, model raster.Model, alpha bool, rows, threads int,
	newDevice func(*raster.Pixmap) Device, fn func(*raster.Pixmap) error) error {
	w, h := r.Dx(), r.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	rows = min(max(rows, 1), h)
	n := min(max(threads, 1), (h+rows-1)/rows)
	bufs := make([]*raster.Pixmap, n)
	bands := make([]raster.Pixmap, n)
	errs := make([]error, n)
	for i := range bufs {
		if bufs[i] = raster.NewPixmap(model, w, rows, alpha); bufs[i] == nil {
			return fmt.Errorf("%w: %dx%d does not fit in memory", ErrUnsupported, w, rows)
		}
	}
	hold := d.imageHold(int64(bufs[0].Stride) * int64(h))
	draw := func(i int) {
		b := &bands[i]
		dev := newDevice(b)
		if hd, ok := dev.(interface{ setHold(*imageHold) }); ok {
			hd.setHold(hold)
		}
		d.ReplayClip(dev, raster.Rect{X0: float32(b.X), Y0: float32(b.Y), X1: float32(b.X + b.W), Y1: float32(b.Y + b.H)})
		errs[i] = dev.Close()
	}
	for y := r.Min.Y; y < r.Max.Y; y += n * rows {
		hold.release(y)
		k := 0
		for ; k < n && y+k*rows < r.Max.Y; k++ {
			y0 := y + k*rows
			b := bufs[k]
			bh := min(rows, r.Max.Y-y0)
			bands[k] = raster.Pixmap{
				W: w, H: bh, N: b.N, Alpha: b.Alpha, Stride: b.Stride,
				X: r.Min.X, Y: y0, Samples: b.Samples[:bh*b.Stride], Model: b.Model,
			}
			if alpha {
				bands[k].Clear()
			} else {
				bands[k].ClearWhite()
			}
		}
		if k == 1 {
			draw(0)
		} else {
			var wg sync.WaitGroup
			for i := range k {
				wg.Go(func() { draw(i) })
			}
			wg.Wait()
		}
		for i := range k {
			if errs[i] != nil {
				return errs[i]
			}
			if err := fn(&bands[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// imageHold keeps the images a banded drawing decodes from the first band
// that draws one to the last, within a bound of bytes or one image alone.
type imageHold struct {
	mu    sync.Mutex
	last  map[Image]int
	held  map[holdKey]*raster.Pixmap
	bytes int64
	limit int64
}

type holdKey struct {
	img    Image
	cs     *ColorSpace
	shrink int
}

// imageHold finds the lowest device row each image of the list reaches; an
// image inside a tile repeats and is kept to the end.
func (d *ListDevice) imageHold(limit int64) *imageHold {
	h := &imageHold{last: map[Image]int{}, held: map[holdKey]*raster.Pixmap{}, limit: limit}
	tiles := 0
	for i := range d.cmds {
		c := &d.cmds[i]
		switch c.kind {
		case cmdBeginTile:
			tiles++
		case cmdEndTile:
			tiles--
		case cmdFillImage, cmdFillImageMask, cmdClipImageMask:
			img, ok := c.obj.(Image)
			if !ok || !holdable(img) {
				continue
			}
			y := math.MaxInt
			if tiles == 0 {
				_, _, _, y = c.ctm.ApplyRect(unitRect).Outer()
			}
			if v, ok := h.last[img]; !ok || y > v {
				h.last[img] = y
			}
		}
	}
	return h
}

func holdable(img Image) bool { return img != nil && reflect.TypeOf(img).Comparable() }

func (h *imageHold) get(k holdKey) *raster.Pixmap {
	if h == nil || !holdable(k.img) {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held[k]
}

func (h *imageHold) put(k holdKey, px *raster.Pixmap) {
	if h == nil || px == nil || !holdable(k.img) {
		return
	}
	n := int64(len(px.Samples))
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.last[k.img]; !ok || h.held[k] != nil || h.bytes > 0 && h.bytes+n > h.limit {
		return
	}
	h.held[k] = px
	h.bytes += n
}

// release lets go of the images no row from y down draws.
func (h *imageHold) release(y int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, px := range h.held {
		if h.last[k.img] <= y {
			delete(h.held, k)
			h.bytes -= int64(len(px.Samples))
		}
	}
}
