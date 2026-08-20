package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"sync"

	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

// Page is one page of a document.
// Page is one page of a document, and is not safe for concurrent use: a
// goroutine that wants to render a page asks the Document for its own.
type Page struct {
	doc      *Document
	dict     Dict
	num      int
	defaults *DefaultColorSpaces
}

// defaultSpaces is what /DefaultGray, /DefaultRGB and /DefaultCMYK resolve to
// on this page, over the output intent.
func (p *Page) defaultSpaces() *DefaultColorSpaces {
	if p.defaults == nil {
		p.defaults = p.doc.readDefaults(p.Resources(),
			&DefaultColorSpaces{OutputIntent: p.doc.outputIntent()})
	}
	return p.defaults
}

// Dict returns the page dictionary, with inherited attributes filled in.
func (p *Page) Dict() Dict { return p.dict }

// Number returns the page index, counting from zero.
func (p *Page) Number() int { return p.num }

// Resources returns the page resource dictionary.
func (p *Page) Resources() Dict { return p.doc.f.GetDict(p.dict["Resources"]) }

// MediaBox returns the page boundary in points.
func (p *Page) MediaBox() raster.Rect {
	r := p.box("MediaBox")
	if r.IsEmpty() {
		return raster.Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	}
	return r
}

// Bounds returns the visible area of the page, the crop box clipped to the
// media box.
func (p *Page) Bounds() raster.Rect {
	media := p.MediaBox()
	crop := p.box("CropBox")
	if crop.IsEmpty() {
		return media
	}
	if r := crop.Intersect(media); !r.IsEmpty() {
		return r
	}
	return media
}

func (p *Page) box(key Name) raster.Rect {
	b := p.doc.f.GetFloats(p.dict[key])
	if len(b) != 4 {
		return raster.EmptyRect
	}
	r := raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
	if r.IsEmpty() {
		return raster.EmptyRect
	}
	return r
}

// Rotate returns the page rotation in degrees, a multiple of 90.
func (p *Page) Rotate() int {
	r := int(p.doc.f.GetInt(p.dict["Rotate"], 0)) % 360
	if r < 0 {
		r += 360
	}
	return r / 90 * 90
}

// UserUnit returns the /UserUnit scale, which is 1 for all but very large
// pages.
func (p *Page) UserUnit() float64 {
	u := p.doc.f.GetFloat(p.dict["UserUnit"], 1)
	if u <= 0 || u > 1000 {
		return 1
	}
	return u
}

// Matrix returns the transform from PDF user space to device space at a given
// resolution: the y axis flips, the page rotation is applied, and the visible
// box moves to the origin. The order is the one MuPDF uses, so that a trace
// taken here and a trace taken there carry the same numbers.
func (p *Page) Matrix(dpi float64) raster.Matrix {
	s := float32(dpi / 72 * p.UserUnit())
	m := raster.Scale(s, -s)
	if r := p.Rotate(); r != 0 {
		m = raster.Concat(raster.Rotate(float64(-r)), m)
	}
	box := m.ApplyRect(p.Bounds())
	return raster.Concat(m, raster.Translate(-box.X0, -box.Y0))
}

// DeviceBounds returns the page rectangle in device space at a resolution,
// which is where a raster of the page begins and ends.
func (p *Page) DeviceBounds(dpi float64) raster.Rect {
	return p.Matrix(dpi).ApplyRect(p.Bounds())
}

// Contents returns the page content stream, with the parts of a /Contents
// array joined.
func (p *Page) Contents() []byte {
	f := p.doc.f
	switch v := f.Resolve(p.dict["Contents"]).(type) {
	case *syntax.Stream:
		b, err := v.Data()
		if err != nil {
			p.doc.errorf("page %d contents: %v", p.num+1, err)
		}
		return b
	case Array:
		var buf bytes.Buffer
		for _, e := range v {
			st := f.GetStream(e)
			if st == nil {
				continue
			}
			b, err := st.Data()
			if err != nil {
				p.doc.errorf("page %d contents: %v", p.num+1, err)
				continue
			}
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return buf.Bytes()
	}
	return nil
}

// Run interprets the page into a device, with ctm mapping page space to
// device space. Both the contents and the annotation appearances are drawn.
func (p *Page) Run(dev Device, ctm raster.Matrix) error {
	p.RunContents(dev, ctm)
	p.RunAnnotations(dev, ctm)
	return dev.Close()
}

// RunContents interprets the page content stream only.
func (p *Page) RunContents(dev Device, ctm raster.Matrix) {
	f := p.doc.f
	if dd, ok := dev.(*DrawDevice); ok && dd.res == nil {
		dd.res = p.Resources()
	}
	bounds := p.Bounds()

	dev.SetDefaultColorSpaces(p.defaultSpaces())

	group := false
	if p.usesTransparency() {
		g := f.GetDict(p.dict["Group"])
		var cs *ColorSpace
		if g != nil && g["CS"] != nil {
			cs = p.doc.colorSpace(g["CS"], p.Resources(), 0)
			if cs != nil && cs.Kind != KindGray && cs.Kind != KindRGB && cs.Kind != KindCMYK {
				cs = nil
			}
		}
		dev.BeginGroup(ctm.ApplyRect(bounds), cs, true, false, BlendNormal, 1)
		group = true
	}

	media := p.MediaBox()
	clipped := false
	if bounds.X0 > media.X0 || bounds.X1 < media.X1 || bounds.Y0 > media.Y0 || bounds.Y1 < media.Y1 {
		var path raster.Path
		path.Rect(bounds.X0, bounds.Y0, bounds.X1-bounds.X0, bounds.Y1-bounds.Y0)
		dev.ClipPath(&path, true, ctm, raster.InfiniteRect)
		clipped = true
	}

	if data := p.Contents(); len(data) > 0 {
		ip := p.newInterp(dev, ctm)
		ip.run(data)
		ip.finish()
	}

	if clipped {
		dev.PopClip()
	}
	if group {
		dev.EndGroup()
	}
}

func (p *Page) newInterp(dev Device, ctm raster.Matrix) *interp {
	var ops int64
	res := p.Resources()
	if res == nil {
		res = Dict{}
	}
	return &interp{
		doc:  p.doc,
		dev:  dev,
		gs:   newGState(ctm),
		base: ctm,
		res:  res,
		defaults: p.doc.readDefaults(res,
			&DefaultColorSpaces{OutputIntent: p.doc.outputIntent()}),
		scissor: raster.InfiniteRect,
		ops:     &ops,
	}
}

// finish closes anything the content stream left open.
func (ip *interp) finish() {
	ip.flushText()
	for i := 0; i < ip.gs.clipDepth; i++ {
		ip.dev.PopClip()
	}
	for len(ip.gstack) > 0 {
		g := ip.gstack[len(ip.gstack)-1]
		ip.gstack = ip.gstack[:len(ip.gstack)-1]
		for i := 0; i < g.clipDepth; i++ {
			ip.dev.PopClip()
		}
	}
	ip.gs.clipDepth = 0
}

// usesTransparency reports whether the page needs a transparency group: it
// declares one, or something in its resources blends.
func (p *Page) usesTransparency() bool {
	f := p.doc.f
	if g := f.GetDict(p.dict["Group"]); g != nil && f.GetName(g["S"]) == "Transparency" {
		return true
	}
	seen := map[any]bool{}
	if p.doc.resourcesBlend(p.Resources(), seen, 0) {
		return true
	}
	for _, a := range f.GetArray(p.dict["Annots"]) {
		ap := p.appearance(f.GetDict(a))
		if ap == nil {
			continue
		}
		if p.doc.resourcesBlend(f.GetDict(ap.Dict["Resources"]), seen, 0) {
			return true
		}
	}
	return false
}

// resourcesBlend walks a resource dictionary looking for anything that forces
// a transparency group.
func (d *Document) resourcesBlend(res Dict, seen map[any]bool, depth int) bool {
	if res == nil || depth > maxNesting {
		return false
	}
	f := d.f
	for _, gs := range f.GetDict(res["ExtGState"]) {
		g := f.GetDict(gs)
		if bm := f.GetName(g["BM"]); bm != "" && bm != "Normal" && bm != "Compatible" {
			return true
		}
		if a, ok := f.Resolve(g["BM"]).(Array); ok {
			for _, e := range a {
				if bm := f.GetName(e); bm != "" && bm != "Normal" && bm != "Compatible" {
					return true
				}
			}
		}
	}
	for _, pat := range f.GetDict(res["Pattern"]) {
		if seen[key(pat)] {
			continue
		}
		seen[key(pat)] = true
		pd := f.GetDict(pat)
		if d.resourcesBlend(f.GetDict(pd["Resources"]), seen, depth+1) {
			return true
		}
	}
	for _, xo := range f.GetDict(res["XObject"]) {
		if seen[key(xo)] {
			continue
		}
		seen[key(xo)] = true
		xd := f.GetDict(xo)
		if xd == nil {
			continue
		}
		if g := f.GetDict(xd["Group"]); g != nil && f.GetName(g["S"]) == "Transparency" {
			return true
		}
		if f.GetName(xd["Subtype"]) == "Image" {
			if xd["SMask"] != nil {
				return true
			}
			continue
		}
		if d.resourcesBlend(f.GetDict(xd["Resources"]), seen, depth+1) {
			return true
		}
	}
	return false
}

// key identifies an object for cycle detection: a reference by its number, a
// direct object by nothing, since a direct object cannot be revisited.
func key(o Object) any {
	if r, ok := o.(Ref); ok {
		return r
	}
	return nil
}

// Render draws the page into a new pixmap, with ctm mapping page space to
// device space. The pixmap covers the page bounds under ctm, rounded out, and
// records where that is in its X and Y.
//
// A page that fails part way still returns what was drawn; the errors are on
// the Document unless Options.Strict asks for the first one back.
func (p *Page) Render(ctm raster.Matrix, o *Options) (*raster.Pixmap, error) {
	x0, y0, x1, y1 := outerBox(ctm.ApplyRect(p.Bounds()))
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: page %d is empty at this scale", ErrInvalid, p.num+1)
	}
	if lim := o.pixelLimit(); lim >= 0 && int64(w)*int64(h) > int64(lim) {
		return nil, fmt.Errorf("%w: %dx%d is over the pixel limit of %d", ErrUnsupported, w, h, lim)
	}

	cs := o.colorSpace()
	if cs == nil || cs.N < 1 || cs.N > 4 || cs.N == 2 {
		return nil, fmt.Errorf("%w: cannot render into %s", ErrUnsupported, csName(cs, "no color space"))
	}
	px := raster.NewPixmap(cs.Model(), w, h, o.alpha())
	if px == nil {
		return nil, fmt.Errorf("%w: %dx%d does not fit in memory", ErrUnsupported, w, h)
	}
	px.X, px.Y = x0, y0
	if !o.alpha() {
		px.ClearWhite()
	}

	first := p.doc.errCount()
	var err error
	if n := o.threads(); n > 1 && h >= n*minBandHeight {
		err = p.renderBands(px, ctm, o, n)
	} else {
		dev := NewDrawDevice(p.doc, px)
		dev.SetFlatness(o.flatness())
		dev.res = p.Resources()
		err = p.Run(dev, ctm)
	}
	if err == nil && o.strict() {
		err = p.doc.errAfter(first)
	}
	return px, err
}

// minBandHeight is how few rows a band may have before splitting the page
// costs more than it saves.
const minBandHeight = 64

// renderBands interprets the page once into a display list and then draws the
// list into n horizontal bands of one pixmap, a goroutine each. Every
// operation a raster renderer performs is local to the pixels it touches, so a
// band is the same page with a smaller destination and the bands never meet.
func (p *Page) renderBands(px *raster.Pixmap, ctm raster.Matrix, o *Options, n int) error {
	list := NewListDevice()
	if err := p.Run(list, ctm); err != nil {
		return err
	}

	res := p.Resources()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		y0, y1 := i*px.H/n, (i+1)*px.H/n
		if y0 >= y1 {
			continue
		}
		band := &raster.Pixmap{
			W: px.W, H: y1 - y0, N: px.N, Alpha: px.Alpha,
			Stride:  px.Stride,
			X:       px.X,
			Y:       px.Y + y0,
			Samples: px.Samples[y0*px.Stride : y1*px.Stride],
			Model:   px.Model,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			dev := NewDrawDevice(p.doc, band)
			dev.SetFlatness(o.flatness())
			dev.res = res
			list.ReplayClip(dev, dev.clip.rect)
			errs[i] = dev.Close()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Image renders the page at a resolution in dots per inch.
func (p *Page) Image(dpi float64) (*image.RGBA, error) {
	px, err := p.Render(p.Matrix(dpi), nil)
	if px == nil {
		return nil, err
	}
	return toRGBA(px), err
}

// ImageOptions renders the page at a resolution, returning the standard
// library image type that matches the options: *image.Gray, *image.RGBA,
// *image.CMYK, or *image.RGBA for anything with an alpha channel.
func (p *Page) ImageOptions(dpi float64, o *Options) (image.Image, error) {
	px, err := p.Render(p.Matrix(dpi), o)
	if px == nil {
		return nil, err
	}
	return toImage(px), err
}

// RenderTo draws the page into a destination the caller owns, with ctm
// mapping page space to the destination's coordinates.
func (p *Page) RenderTo(dst draw.Image, ctm raster.Matrix, o *Options) error {
	px, err := p.Render(ctm, o)
	if px == nil {
		return err
	}
	src := toImage(px)
	draw.Draw(dst, src.Bounds().Add(image.Pt(px.X, px.Y)), src, src.Bounds().Min, draw.Over)
	return err
}

// toImage converts a pixmap to the standard library type that holds it
// without losing anything.
func toImage(px *raster.Pixmap) image.Image {
	if px.Alpha {
		return toRGBA(px)
	}
	switch px.N {
	case 1:
		img := image.NewGray(image.Rect(0, 0, px.W, px.H))
		for y := 0; y < px.H; y++ {
			copy(img.Pix[y*img.Stride:], px.Row(y))
		}
		return img
	case 4:
		img := image.NewCMYK(image.Rect(0, 0, px.W, px.H))
		for y := 0; y < px.H; y++ {
			copy(img.Pix[y*img.Stride:], px.Row(y))
		}
		return img
	}
	return toRGBA(px)
}

// toRGBA converts a pixmap to premultiplied RGBA, which is where color
// leaves the document's own space.
func toRGBA(px *raster.Pixmap) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, px.W, px.H))
	model := px.Model
	if model == nil {
		model = raster.ModelRGB
	}
	n := px.Comps()
	rgb := make([]uint8, px.W*3)
	straight := make([]uint8, px.W*px.N)
	for y := 0; y < px.H; y++ {
		row := px.Row(y)
		out := img.Pix[y*img.Stride : y*img.Stride+px.W*4]
		if !px.Alpha {
			model.ToRGB(rgb, row)
			for x := 0; x < px.W; x++ {
				out[x*4], out[x*4+1], out[x*4+2] = rgb[x*3], rgb[x*3+1], rgb[x*3+2]
				out[x*4+3] = 255
			}
			continue
		}
		if px.N == 3 {
			for x := 0; x < px.W; x++ {
				copy(out[x*4:x*4+4], row[x*n:x*n+4])
			}
			continue
		}
		for x := 0; x < px.W; x++ {
			a := row[x*n+px.N]
			for c := 0; c < px.N; c++ {
				straight[x*px.N+c] = unpremultiply(row[x*n+c], a)
			}
		}
		model.ToRGB(rgb, straight)
		for x := 0; x < px.W; x++ {
			a := row[x*n+px.N]
			out[x*4] = premultiply(rgb[x*3], a)
			out[x*4+1] = premultiply(rgb[x*3+1], a)
			out[x*4+2] = premultiply(rgb[x*3+2], a)
			out[x*4+3] = a
		}
	}
	return img
}

func unpremultiply(v, a uint8) uint8 {
	if a == 0 || a == 255 {
		return v
	}
	if r := uint32(v) * 255 / uint32(a); r < 255 {
		return uint8(r)
	}
	return 255
}

func premultiply(v, a uint8) uint8 {
	t := uint32(v)*uint32(a) + 128
	return uint8((t + t>>8) >> 8)
}
