package doc

import (
	"errors"
	"io"

	"github.com/gen2brain/folio/html"
	"github.com/gen2brain/folio/pdf"
	"github.com/gen2brain/folio/svg"
)

func closeBoth(err error, c io.Closer) error {
	if c == nil {
		return err
	}
	return errors.Join(err, c.Close())
}

type pdfDoc struct {
	d *pdf.Document
	c io.Closer
}

func (d pdfDoc) Kind() Kind      { return KindPDF }
func (d pdfDoc) Underlying() any { return d.d }
func (d pdfDoc) NumPages() int   { return d.d.NumPages() }
func (d pdfDoc) Close() error    { return closeBoth(d.d.Close(), d.c) }
func (d pdfDoc) Metadata() Metadata {
	m := d.d.Metadata()
	return Metadata{Title: m.Title, Author: m.Author, Created: m.Created, Modified: m.Modified}
}

func (d pdfDoc) Page(i int) (Page, error) {
	p, err := d.d.Page(i)
	if err != nil {
		return nil, err
	}
	return pdfPage{p}, nil
}

type pdfPage struct{ *pdf.Page }

func (p pdfPage) Links() []Link {
	src := p.Page.Links()
	out := make([]Link, len(src))
	for i, l := range src {
		out[i] = Link{Rect: l.Rect, URI: l.URI}
	}
	return out
}

type bookDoc struct {
	d *html.Document
	c io.Closer
}

func (d bookDoc) Kind() Kind      { return KindBook }
func (d bookDoc) Underlying() any { return d.d }
func (d bookDoc) NumPages() int   { return d.d.NumPages() }
func (d bookDoc) Close() error    { return closeBoth(d.d.Close(), d.c) }
func (d bookDoc) Metadata() Metadata {
	m := d.d.Metadata()
	return Metadata{Title: m.Title, Author: m.Author, Created: m.Created, Modified: m.Modified}
}

func (d bookDoc) Page(i int) (Page, error) {
	p, err := d.d.Page(i)
	if err != nil {
		return nil, err
	}
	return bookPage{p}, nil
}

type bookPage struct{ *html.Page }

func (p bookPage) Links() []Link {
	src := p.Page.Links()
	out := make([]Link, len(src))
	for i, l := range src {
		out[i] = Link{Rect: l.Rect, URI: l.URI}
	}
	return out
}

type svgDoc struct {
	d *svg.Document
	c io.Closer
}

func (d svgDoc) Kind() Kind         { return KindSVG }
func (d svgDoc) Underlying() any    { return d.d }
func (d svgDoc) NumPages() int      { return d.d.NumPages() }
func (d svgDoc) Close() error       { return closeBoth(d.d.Close(), d.c) }
func (d svgDoc) Metadata() Metadata { return Metadata{Title: d.d.Metadata().Title} }

func (d svgDoc) Page(i int) (Page, error) {
	p, err := d.d.Page(i)
	if err != nil {
		return nil, err
	}
	return svgPage{p}, nil
}

type svgPage struct{ *svg.Page }

func (p svgPage) Links() []Link {
	src := p.Page.Links()
	out := make([]Link, len(src))
	for i, l := range src {
		out[i] = Link{Rect: l.Rect, URI: l.URI}
	}
	return out
}
