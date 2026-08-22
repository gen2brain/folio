package svg

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// Options are what a caller may change about a render. A nil pointer is the
// defaults.
type Options struct {
	// PixelLimit bounds the pixel area a drawing may allocate. Zero is the
	// default of 2^28 and a negative number is no limit at all.
	PixelLimit int
	// Alpha keeps the background transparent rather than filling it white,
	// which is what a drawing meant to sit on something else wants.
	Alpha bool
	// Flatness is how far a curve may sit from the lines that stand in for
	// it, in device pixels.
	Flatness float32
}

const defaultPixelLimit = 1 << 28

func (o *Options) pixelLimit() int {
	if o == nil || o.PixelLimit == 0 {
		return defaultPixelLimit
	}
	return o.PixelLimit
}

func (o *Options) alpha() bool { return o != nil && o.Alpha }

func (o *Options) flatness() float32 {
	if o == nil || o.Flatness <= 0 {
		return 0
	}
	return o.Flatness
}

// Render draws the page into a pixmap, with ctm mapping the drawing onto it.
func (p *Page) Render(ctm raster.Matrix, o *Options) (*raster.Pixmap, error) {
	x0, y0, x1, y1 := ctm.ApplyRect(p.Bounds()).Outer()
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: the drawing is empty at this scale", ErrInvalid)
	}
	if lim := o.pixelLimit(); lim >= 0 && int64(w)*int64(h) > int64(lim) {
		return nil, fmt.Errorf("%w: %dx%d is over the pixel limit of %d", ErrUnsupported, w, h, lim)
	}
	px := raster.NewPixmap(gfx.DeviceRGB.Model(), w, h, o.alpha())
	if px == nil {
		return nil, fmt.Errorf("%w: %dx%d does not fit in memory", ErrUnsupported, w, h)
	}
	px.X, px.Y = x0, y0
	if !o.alpha() {
		px.ClearWhite()
	}
	dev := gfx.NewDrawDevice(px)
	if f := o.flatness(); f > 0 {
		dev.SetFlatness(f)
	}
	err := p.Run(dev, ctm)
	dev.Close()
	return px, err
}

// ImageDPI renders the drawing at a resolution, where 96 is the pixel its
// lengths are written in.
func (p *Page) ImageDPI(dpi float64) (*image.RGBA, error) {
	px, err := p.Render(p.Matrix(dpi), nil)
	if px == nil {
		return nil, err
	}
	return gfx.ToRGBA(px), err
}

// Decode reads a drawing and renders it at the size it asks to be, which is
// what a caller that wants a picture rather than a document wants.
//
// It is not registered with image.RegisterFormat. Registering changes what
// image.Decode does for the whole program, which is the importing program's
// decision and not this package's:
//
//	image.RegisterFormat("svg", "<svg", svg.Decode, svg.DecodeConfig)
func Decode(r io.Reader) (image.Image, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	d, err := Load(b)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	p, err := d.Page(0)
	if err != nil {
		return nil, err
	}
	return p.ImageDPI(96)
}

// DecodeConfig reads how big a drawing is without drawing it.
func DecodeConfig(r io.Reader) (image.Config, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return image.Config{}, err
	}
	d, err := Load(b)
	if err != nil {
		return image.Config{}, err
	}
	defer d.Close()
	return image.Config{
		ColorModel: color.RGBAModel,
		Width:      int(d.width + 0.5),
		Height:     int(d.height + 0.5),
	}, nil
}

// ImageSize renders the drawing into an image of exactly w by h pixels,
// scaling it to fit and keeping its aspect ratio, which is what an icon drawn
// at the size a caller has room for wants. A zero for either takes that side
// from the other.
func (p *Page) ImageSize(w, h int) (*image.RGBA, error) {
	b := p.Bounds()
	if b.X1 <= 0 || b.Y1 <= 0 || (w <= 0 && h <= 0) {
		return nil, fmt.Errorf("%w: no size to draw at", ErrInvalid)
	}
	sx, sy := float32(w)/b.X1, float32(h)/b.Y1
	switch {
	case w <= 0:
		sx = sy
	case h <= 0:
		sy = sx
	default:
		s := min(sx, sy)
		sx, sy = s, s
	}
	px, err := p.Render(raster.Scale(sx, sy), nil)
	if px == nil {
		return nil, err
	}
	return gfx.ToRGBA(px), err
}

// RenderTo draws the drawing into a destination the caller owns, with ctm
// mapping it onto that image's coordinates. This is how an icon is composited
// onto something already drawn rather than handed back on its own.
func (p *Page) RenderTo(dst draw.Image, ctm raster.Matrix, o *Options) error {
	px, err := p.Render(ctm, o)
	if px == nil {
		return err
	}
	src := gfx.ToRGBA(px)
	draw.Draw(dst, src.Bounds().Add(image.Pt(px.X, px.Y)), src, src.Bounds().Min, draw.Over)
	return err
}

// The structured text model comes from the gfx package.
type (
	// TextPage is a drawing's text: blocks of lines of characters.
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

// StructuredText returns the drawing's text with the box every character,
// line and block occupies, in the coordinates the drawing is written in.
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

// Text returns the drawing's text: a newline after every line and a blank
// line after every block.
func (p *Page) Text() (string, error) {
	st, err := p.StructuredText()
	if st == nil {
		return "", err
	}
	return st.Text(), err
}
