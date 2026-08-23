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

// PictureDecoder reads one of the raster formats a document carries a picture
// in. Returning an *image.RGBA whose bounds begin at the origin saves the
// conversion DecodePicture would otherwise do.
type PictureDecoder func(data []byte) (image.Image, error)

type registered struct {
	name  string
	magic string
	dec   PictureDecoder
}

var (
	decoderMu sync.RWMutex
	decoders  []registered
)

// RegisterPictureDecoder installs a decoder for a picture format, taking
// precedence over the standard library's for data beginning with magic, in
// which a question mark matches any byte. Registering the same name again
// replaces it, and a nil decoder removes it.
//
// It is what image.RegisterFormat would be if it did not decide for the whole
// program:
//
//	gfx.RegisterPictureDecoder("jpeg", "\xff\xd8", func(b []byte) (image.Image, error) {
//		return jpegn.Decode(bytes.NewReader(b), &jpegn.Options{ToRGBA: true})
//	})
func RegisterPictureDecoder(name, magic string, dec PictureDecoder) {
	decoderMu.Lock()
	defer decoderMu.Unlock()
	for i := range decoders {
		if decoders[i].name != name {
			continue
		}
		if dec == nil {
			decoders = append(decoders[:i], decoders[i+1:]...)
		} else {
			decoders[i] = registered{name, magic, dec}
		}
		return
	}
	if dec != nil {
		decoders = append(decoders, registered{name, magic, dec})
	}
}

// pictureDecoder is the decoder registered for what b begins with.
func pictureDecoder(b []byte) PictureDecoder {
	decoderMu.RLock()
	defer decoderMu.RUnlock()
	for _, r := range decoders {
		if matchMagic(r.magic, b) {
			return r.dec
		}
	}
	return nil
}

func matchMagic(magic string, b []byte) bool {
	if len(b) < len(magic) {
		return false
	}
	for i := 0; i < len(magic); i++ {
		if magic[i] != '?' && magic[i] != b[i] {
			return false
		}
	}
	return true
}

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

// NewPicture wraps a rendered pixmap as an image a device can draw. Its
// samples are premultiplied and are not copied.
func NewPicture(px *raster.Pixmap) *Picture {
	if px == nil {
		return nil
	}
	return &Picture{pix: px, W: px.W, H: px.H}
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

func boundPicture(w, h int) error {
	if w <= 0 || h <= 0 || w*h > maxPictureArea {
		return fmt.Errorf("%w: picture is %dx%d", ErrUnsupported, w, h)
	}
	return nil
}

// DecodePicture reads one of the raster formats a document carries a picture
// in, through a decoder RegisterPictureDecoder has installed for it or the
// standard library's.
func DecodePicture(b []byte) (*Picture, error) {
	var src image.Image
	var err error
	if dec := pictureDecoder(b); dec != nil {
		src, err = dec(b)
	} else {
		var cfg image.Config
		if cfg, _, err = image.DecodeConfig(bytes.NewReader(b)); err != nil {
			return nil, err
		}
		if err = boundPicture(cfg.Width, cfg.Height); err != nil {
			return nil, err
		}
		src, _, err = image.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return nil, err
	}
	r := src.Bounds()
	if err := boundPicture(r.Dx(), r.Dy()); err != nil {
		return nil, err
	}
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
