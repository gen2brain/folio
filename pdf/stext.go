package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// The structured text model comes from the gfx package.
type (
	// TextPage is a page's text: blocks of lines of characters.
	TextPage = gfx.TextPage
	// TextBlock is a paragraph of text, or one image.
	TextBlock = gfx.TextBlock
	// TextLine is a run of characters along one baseline.
	TextLine = gfx.TextLine
	// TextChar is one character where it was drawn.
	TextChar = gfx.TextChar
	// TextOptions configure what StructuredTextOptions collects.
	TextOptions = gfx.TextOptions
	// Quad is the four corners of what a character occupies.
	Quad = gfx.Quad
)

// StructuredText returns the page's text with the box every character, line
// and block occupies, in page space at 72 dots per inch.
func (p *Page) StructuredText() (*TextPage, error) {
	return p.StructuredTextOptions(nil)
}

// StructuredTextOptions is StructuredText with options.
func (p *Page) StructuredTextOptions(o *TextOptions) (*TextPage, error) {
	st := &TextPage{Bounds: p.Bounds()}
	err := p.Run(gfx.NewTextDevice(st, o), p.Matrix(72))
	return st, err
}

// Text is the page's text: a newline after a line, a blank line after a block.
func (p *Page) Text() (string, error) {
	st, err := p.StructuredText()
	if st == nil {
		return "", err
	}
	return st.Text(), err
}

// WriteSVG writes the page as SVG. Paths, text and images come out as
// themselves; a shading and a soft mask are rasterized.
func (p *Page) WriteSVG(w io.Writer) error {
	m := p.Matrix(72)
	dev := gfx.NewSVGDevice(w, m.ApplyRect(p.Bounds()))
	return p.Run(dev, m)
}

// SVG returns the page as SVG.
func (p *Page) SVG() (string, error) {
	var b bytes.Buffer
	err := p.WriteSVG(&b)
	return b.String(), err
}

// WriteHTML writes the page as HTML: the text where it was drawn, in the face
// and the colour it was drawn with.
func (p *Page) WriteHTML(w io.Writer) error {
	st, err := p.StructuredText()
	if st == nil {
		return err
	}
	_, werr := io.WriteString(w, gfx.HTMLDocument(p.doc.Metadata().Title,
		st.HTML(fmt.Sprintf("page%d", p.num))))
	return errors.Join(err, werr)
}

// HTML returns the page as HTML.
func (p *Page) HTML() (string, error) {
	var b bytes.Buffer
	err := p.WriteHTML(&b)
	return b.String(), err
}

// Image renders the page at the resolution its own content is in. A page with
// no text whose largest image covers half of it is cropped to that image.
func (p *Page) Image() (*image.RGBA, error) {
	dpi, box, crop := p.naturalDPI()
	img, err := p.ImageDPI(dpi)
	if img == nil || !crop {
		return img, err
	}
	return cropTo(img, box, dpi), err
}

// defaultDPI is what a page with no image in it is rendered at.
const defaultDPI = 300

// naturalDPI is the highest resolution any image on the page is drawn at, and
// the box of the largest when the page is nothing but that image.
func (p *Page) naturalDPI() (dpi float64, box raster.Rect, crop bool) {
	st, _ := p.StructuredTextOptions(&TextOptions{Images: true})
	if st == nil {
		return defaultDPI, box, false
	}
	var area float32
	text := false
	for i := range st.Blocks {
		b := &st.Blocks[i]
		if b.Image == nil {
			text = true
			continue
		}
		w, h := b.Image.Size()
		bw, bh := b.Bounds.X1-b.Bounds.X0, b.Bounds.Y1-b.Bounds.Y0
		if bw > 0 {
			dpi = max(dpi, float64(w)*72/float64(bw))
		}
		if bh > 0 {
			dpi = max(dpi, float64(h)*72/float64(bh))
		}
		if bw*bh > area {
			area, box = bw*bh, b.Bounds
		}
	}
	if dpi <= 0 {
		dpi = defaultDPI
	}
	page := p.Bounds()
	pa := (page.X1 - page.X0) * (page.Y1 - page.Y0)
	return dpi, box, !text && pa > 0 && area >= 0.5*pa
}

// cropTo cuts out the part of a rendered page box covers.
func cropTo(img *image.RGBA, box raster.Rect, dpi float64) *image.RGBA {
	s := dpi / 72
	r := image.Rect(
		int(float64(box.X0)*s), int(float64(box.Y0)*s),
		int(float64(box.X1)*s+0.5), int(float64(box.Y1)*s+0.5),
	).Intersect(img.Bounds())
	if r.Empty() {
		return img
	}
	return img.SubImage(r).(*image.RGBA)
}
