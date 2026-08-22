package gfx

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sync"

	"github.com/gen2brain/folio/raster"
)

// ErrUnsupported is a picture in a form this cannot decode.
var ErrUnsupported = errors.New("gfx: unsupported")

// DecodeImage reads one of the raster formats a document may carry a picture
// in and hands back an Image a device can draw.
func DecodeImage(b []byte) (Image, error) { return DecodePicture(b) }

// Picture is a decoded raster image.
type Picture struct {
	pix *raster.Pixmap
	// W and H are the picture's size in pixels.
	W, H int
	mu   sync.Mutex
	// alt is the picture in another color model, kept for a device that
	// composites in one this was not decoded into.
	alt map[raster.Model]*raster.Pixmap
}

// Size implements gfx.Image.
func (p *Picture) Size() (int, int) { return p.W, p.H }

// ColorSpace implements gfx.Image.
func (p *Picture) ColorSpace() *ColorSpace { return DeviceRGB }

// Stencil implements gfx.Image.
func (p *Picture) Stencil() bool { return false }

// Smooth implements gfx.Image.
func (p *Picture) Smooth() bool { return true }

// Pixels implements gfx.Image.
func (p *Picture) Pixels(cs *ColorSpace, shrink int) (*raster.Pixmap, error) {
	if cs == nil || cs.Model() == p.pix.Model {
		return p.pix, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m := cs.Model()
	if px, ok := p.alt[m]; ok {
		return px, nil
	}
	px := convertPixmap(p.pix, m)
	if p.alt == nil {
		p.alt = map[raster.Model]*raster.Pixmap{}
	}
	p.alt[m] = px
	return px, nil
}

// convertPixmap moves a pixmap into another color model, through RGB.
func convertPixmap(src *raster.Pixmap, m raster.Model) *raster.Pixmap {
	dst := raster.NewPixmap(m, src.W, src.H, src.Alpha)
	sn, dn := src.Comps(), dst.Comps()
	var rgb [3]uint8
	for y := range src.H {
		s := src.Samples[y*src.Stride:]
		d := dst.Samples[y*dst.Stride:]
		for x := range src.W {
			src.Model.ToRGB(rgb[:], s[x*sn:x*sn+src.N])
			m.FromRGB(d[x*dn:x*dn+dst.N], rgb[:])
			if src.Alpha {
				d[x*dn+dst.N] = s[x*sn+src.N]
			}
		}
	}
	return dst
}

// maxPictureArea bounds what one picture of a book may allocate.
const maxPictureArea = 1 << 26

// decodePicture reads one of the formats a book carries a picture in.
// DecodePicture reads one of the raster formats a document carries a picture
// in.
func DecodePicture(b []byte) (*Picture, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPictureArea {
		return nil, fmt.Errorf("%w: picture is %dx%d", ErrUnsupported, cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r := src.Bounds()
	rgba, ok := src.(*image.RGBA)
	if !ok || rgba.Rect != r {
		rgba = image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(rgba, rgba.Rect, src, r.Min, draw.Src)
	}
	px := raster.NewPixmap(raster.ModelRGB, r.Dx(), r.Dy(), true)
	for y := range r.Dy() {
		copy(px.Samples[y*px.Stride:], rgba.Pix[y*rgba.Stride:y*rgba.Stride+r.Dx()*4])
	}
	return &Picture{pix: px, W: r.Dx(), H: r.Dy()}, nil
}
