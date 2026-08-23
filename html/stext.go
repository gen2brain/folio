package html

import (
	"bytes"
	"errors"
	"fmt"
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
	// Quad is the four corners a character covers.
	Quad = gfx.Quad
)

// StructuredText returns the page's text with the box every character covers.
func (p *Page) StructuredText() (*TextPage, error) {
	return p.StructuredTextOptions(nil)
}

// StructuredTextOptions is StructuredText with options.
func (p *Page) StructuredTextOptions(o *TextOptions) (*TextPage, error) {
	st := &TextPage{Bounds: p.Bounds()}
	dev := gfx.NewTextDevice(st, o)
	err := p.Run(dev, raster.Identity)
	dev.Close()
	return st, err
}

// WriteSVG writes the page as SVG, with text, borders and pictures as such.
func (p *Page) WriteSVG(w io.Writer) error {
	dev := gfx.NewSVGDevice(w, p.Bounds())
	err := p.Run(dev, raster.Identity)
	return errors.Join(err, dev.Close())
}

// SVG returns the page as SVG.
func (p *Page) SVG() (string, error) {
	var b bytes.Buffer
	err := p.WriteSVG(&b)
	return b.String(), err
}

// WriteHTML writes the page as HTML: the text where it was laid out, in the
// face and colour it was drawn with. Read gives the markup it was written in.
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
