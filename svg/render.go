package svg

import (
	"fmt"
	"image"
	"image/color"
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
