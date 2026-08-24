package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"

	"github.com/gen2brain/folio/gfx"
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
	st, _ := p.StructuredTextOptions(&TextOptions{Images: true})
	dpi, box, crop := gfx.NaturalDPI(st, p.Bounds(), gfx.CropArea)
	img, err := p.ImageDPI(dpi)
	if img == nil || !crop {
		return img, err
	}
	return gfx.CropTo(img, box, dpi), err
}
