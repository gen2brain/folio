package html

import (
	"errors"
	"fmt"
	"image"
	"math"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// pxPerPoint is what a CSS pixel is worth: 96 to the inch against 72.
const pxPerPoint = 72.0 / 96.0

// LayoutOptions is the page a book is laid out onto.
type LayoutOptions struct {
	// Width and Height are the page in CSS pixels, 96 to the inch. Zero is
	// the size the document asks for, and 800 by 1200 for one that asks for
	// none, which is a page of about eight inches by twelve.
	Width, Height float32
	// Margin is what is left blank around the text, in CSS pixels.
	Margin float32
	// FontSize is the size the reader is set to, which every relative length
	// resolves against. Zero is 16.
	FontSize float32
	// UserSheet is a stylesheet applied over the user agent's and under the
	// book's own.
	UserSheet *Stylesheet
}

func (o *LayoutOptions) or(def *LayoutOptions) LayoutOptions {
	v := *def
	if o != nil {
		v = *o
		if v.Width <= 0 {
			v.Width = def.Width
		}
		if v.Height <= 0 {
			v.Height = def.Height
		}
		if v.FontSize <= 0 {
			v.FontSize = def.FontSize
		}
	}
	if v.Width <= 0 {
		v.Width = 800
	}
	if v.Height <= 0 {
		v.Height = 1200
	}
	if v.FontSize <= 0 {
		v.FontSize = DefaultFontSize
	}
	if v.Margin < 0 {
		v.Margin = 0
	}
	return v
}

// laidPart is one part of the spine after it has been laid out.
type laidPart struct {
	path string
	root *box
	// tops are where each page of the part starts down the column.
	tops []float32
	// height is how far the column reaches.
	height float32
	// vertical is a part whose lines run down the page and whose pages run
	// right to left.
	vertical bool
}

// Page is one page of a laid out book.
type Page struct {
	doc  *Document
	part *laidPart
	num  int
	// top and bottom bound the part of the column the page shows, in CSS
	// pixels. bottom is where the next page starts, not top plus the page
	// height.
	top, bottom float32
}

// Layout lays the book out onto pages of a size and returns how many it
// makes. It must be called before NumPages or Page, and calling it again with
// another size lays the book out afresh. Pages may then be rendered from
// several goroutines at once, but not while another Layout is running.
func (d *Document) Layout(o *LayoutOptions) (int, error) {
	opt := o.or(&d.natural)
	cw := opt.Width - 2*opt.Margin
	ch := opt.Height - 2*opt.Margin
	if cw <= 0 || ch <= 0 {
		return 0, fmt.Errorf("%w: page %gx%g leaves no room", ErrInvalid, opt.Width, opt.Height)
	}
	media := Media{Width: cw, Height: ch, FontSize: opt.FontSize}

	var parts []*laidPart
	var pages []*Page
	var errs []error
	for _, it := range d.Spine() {
		if !it.IsChapter() {
			p, err := d.imagePart(it, cw, ch)
			if err != nil {
				errs = append(errs, err)
			}
			if p != nil {
				parts = append(parts, p)
				pages = append(pages, &Page{doc: d, part: p, num: len(pages), bottom: p.height})
			}
			continue
		}
		root, err := d.ParsePart(it.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sheets := d.Stylesheets(it.Path, root)
		if opt.UserSheet != nil {
			sheets = append(sheets[:1:1], append([]*Stylesheet{opt.UserSheet}, sheets[1:]...)...)
		}
		st := Cascade(root, media, sheets...)
		vertical := writingOf(root, st).Vertical()
		colw, pageLen := cw, ch
		if vertical {
			colw, pageLen = ch, cw
		}
		l := &layout{doc: d, path: it.Path, fonts: newFontSet(d, sheets), vertical: vertical,
			cbh: pageLen, pw: colw, ph: pageLen}
		l.run(buildBoxes(root, st), colw)
		errs = append(errs, l.errs...)
		if len(l.spans) == 0 {
			continue
		}

		p := &laidPart{path: it.Path, root: l.root, tops: paginate(l.spans, pageLen),
			height: l.y, vertical: vertical}
		parts = append(parts, p)
		for i, top := range p.tops {
			bottom := p.height
			if i+1 < len(p.tops) {
				bottom = p.tops[i+1]
			}
			pages = append(pages, &Page{doc: d, part: p, num: len(pages), top: top, bottom: max(bottom, top+1)})
		}
	}
	d.layoutMu.Lock()
	d.opt, d.parts, d.pages = opt, parts, pages
	d.layoutMu.Unlock()
	return len(pages), errors.Join(errs...)
}

// writingOf is the writing mode a part is laid out in, which is what its root
// element or its body asks for.
func writingOf(root *Node, st Styles) Writing {
	out := WritingHorizontal
	n := 0
	Walk(root, func(k *Node) bool {
		if k.Type != xhtml.ElementNode || n >= 2 {
			return n < 2
		}
		if w := st.Of(k).Writing; w != WritingHorizontal {
			out = w
		}
		if k.DataAtom == atom.Body {
			n = 2
		} else {
			n++
		}
		return true
	})
	return out
}

// imagePart makes a page of a spine item that is a picture rather than a
// document, which is how a comic and a picture book are written, and of one
// that is a drawing, which is how a pre-paginated book puts SVG in its spine.
func (d *Document) imagePart(it Item, cw, ch float32) (*laidPart, error) {
	if !strings.HasPrefix(it.Type, "image/") {
		return nil, nil
	}
	raw, err := d.Read(it.Path)
	if err != nil {
		return nil, err
	}
	vis, err := d.visualOf(it.Path, raw, cw, ch)
	if err != nil {
		return nil, err
	}
	w, h := vis.w, vis.h
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: %s is empty", ErrInvalid, it.Path)
	}
	// A drawing is scaled up to the page as well as down: it is the page,
	// not a picture placed on one, and it loses nothing by being enlarged.
	s := min(cw/w, ch/h)
	if s < 1 || vis.art != nil {
		w, h = w*s, h*s
	}
	root := &box{style: &Style{}, w: cw, h: ch}
	root.lines = []lineBox{{
		h: h, baseline: h,
		frags: []frag{{x: (cw - w) / 2, w: w, h: h, vis: vis, style: root.style}},
	}}
	return &laidPart{path: it.Path, root: root, tops: []float32{0}, height: h}, nil
}

// NumPages is how many pages the last Layout made, and zero before one.
func (d *Document) NumPages() int {
	d.layoutMu.Lock()
	defer d.layoutMu.Unlock()
	return len(d.pages)
}

// Page returns one page of the laid out book.
func (d *Document) Page(i int) (*Page, error) {
	d.layoutMu.Lock()
	defer d.layoutMu.Unlock()
	if len(d.pages) == 0 {
		return nil, fmt.Errorf("%w: the book is not laid out", ErrInvalid)
	}
	if i < 0 || i >= len(d.pages) {
		return nil, fmt.Errorf("%w: page %d of %d", ErrNotFound, i, len(d.pages))
	}
	return d.pages[i], nil
}

// Number is where the page sits in the book, counting from zero.
func (p *Page) Number() int { return p.num }

// Path is the part of the spine the page came out of.
func (p *Page) Path() string { return p.part.path }

// Bounds returns the page in points.
func (p *Page) Bounds() raster.Rect {
	o := p.doc.options()
	return raster.Rect{X1: o.Width * pxPerPoint, Y1: o.Height * pxPerPoint}
}

// Matrix returns the transform from page space to a device at a resolution.
func (p *Page) Matrix(dpi float64) raster.Matrix {
	s := float32(dpi / 72)
	return raster.Scale(s, s)
}

// DeviceBounds is the page at a resolution, in whole pixels.
func (p *Page) DeviceBounds(dpi float64) raster.Rect {
	r := p.Matrix(dpi).ApplyRect(p.Bounds())
	return raster.Rect{
		X0: float32(math.Floor(float64(r.X0))), Y0: float32(math.Floor(float64(r.Y0))),
		X1: float32(math.Ceil(float64(r.X1))), Y1: float32(math.Ceil(float64(r.Y1))),
	}
}

func (d *Document) options() LayoutOptions {
	d.layoutMu.Lock()
	defer d.layoutMu.Unlock()
	return d.opt
}

// Run draws the page through a device, under a transform from page space in
// points to the device.
func (p *Page) Run(dev gfx.Device, ctm raster.Matrix) error {
	o := p.doc.options()
	m := raster.Concat(p.pageMatrix(o), ctm)
	r := &painter{dev: dev, ctm: m, top: p.top, bottom: p.bottom, vertical: p.part.vertical}
	r.walk(p.part.root)
	return errors.Join(r.errs...)
}

// pageMatrix maps the column the part was laid out in onto the page, in
// points.
func (p *Page) pageMatrix(o LayoutOptions) raster.Matrix {
	if p.part != nil && p.part.vertical {
		// The lines run down the page and the pages run right to left: the
		// column turns a quarter and starts at the right edge.
		return raster.Matrix{
			B: pxPerPoint, C: -pxPerPoint,
			E: (o.Width - o.Margin + p.top) * pxPerPoint, F: o.Margin * pxPerPoint,
		}
	}
	return raster.Matrix{
		A: pxPerPoint, D: pxPerPoint,
		E: o.Margin * pxPerPoint, F: (o.Margin - p.top) * pxPerPoint,
	}
}

// ImageDPI renders the page at a resolution.
func (p *Page) ImageDPI(dpi float64) (*image.RGBA, error) {
	b := p.DeviceBounds(dpi)
	w, h := int(b.X1-b.X0), int(b.Y1-b.Y0)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: page is %dx%d", ErrInvalid, w, h)
	}
	px := raster.NewPixmap(raster.ModelRGB, w, h, false)
	px.ClearWhite()
	dev := gfx.NewDrawDevice(px)
	if err := p.Run(dev, p.Matrix(dpi)); err != nil {
		return nil, err
	}
	dev.Close()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		src := px.Samples[y*px.Stride:]
		dst := img.Pix[y*img.Stride:]
		for x := range w {
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = src[x*3], src[x*3+1], src[x*3+2], 255
		}
	}
	return img, nil
}

// Image renders the page at 96 dots per inch, one device pixel per CSS pixel.
func (p *Page) Image() (*image.RGBA, error) { return p.ImageDPI(96) }

// Text returns what the page says, a line of the text for each line box.
func (p *Page) Text() string {
	t := &textCollector{top: p.top, bottom: p.bottom}
	t.walk(p.part.root)
	return t.b.String()
}

// textCollector reads the text off the lines of one page.
type textCollector struct {
	b           strings.Builder
	top, bottom float32
}

func (t *textCollector) walk(b *box) {
	if b == nil || (b.reach > b.y && (b.y >= t.bottom || b.reach <= t.top)) {
		return
	}
	if len(b.lines) > 0 {
		for i := range b.lines {
			ln := &b.lines[i]
			if ln.y >= t.bottom || ln.y+ln.h <= t.top {
				continue
			}
			n := t.b.Len()
			for j := range ln.frags {
				f := &ln.frags[j]
				t.b.WriteString(f.text)
				// A drawing carries text of its own, which is all a page
				// whose spine item is one has to say.
				if f.vis != nil && f.vis.art != nil {
					if s, err := f.vis.art.Text(); err == nil {
						t.b.WriteString(s)
					}
				}
			}
			if t.b.Len() > n {
				t.b.WriteByte('\n')
			}
		}
		// The rest of the children are the inline boxes the lines were made
		// of, and say nothing the lines do not, except the ones laid out on
		// their own.
		for _, k := range b.kids {
			if k.ownFlow() {
				t.walk(k)
			}
		}
		return
	}
	for _, k := range b.kids {
		t.walk(k)
	}
}
