package gfx

import (
	"bytes"
	"encoding/binary"
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

// PictureDecoder decodes one raster picture format.
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

// RegisterPictureDecoder installs a decoder for data beginning with magic, where "?" matches any byte.
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

// DecodeRegistered decodes b with a registered decoder and reports whether one matched.
// A picture whose header gives more than limit pixels is refused.
func DecodeRegistered(b []byte, limit int) (image.Image, bool, error) {
	dec := pictureDecoder(b)
	if dec == nil {
		return nil, false, nil
	}
	if err := checkHeader(b, limit); err != nil {
		return nil, true, err
	}
	img, err := dec(b)
	return img, true, err
}

// checkHeader refuses a picture whose header gives more than limit pixels.
func checkHeader(b []byte, limit int) error {
	var w, h int64
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
		w, h = int64(cfg.Width), int64(cfg.Height)
	} else if len(b) >= 24 && string(b[:8]) == "\x89PNG\r\n\x1a\n" && string(b[12:16]) == "IHDR" {
		w, h = int64(binary.BigEndian.Uint32(b[16:])), int64(binary.BigEndian.Uint32(b[20:]))
	}
	if w > 1<<31 || h > 1<<31 || w*h > int64(limit) {
		return fmt.Errorf("%w: picture is %dx%d", ErrUnsupported, w, h)
	}
	return nil
}

// ErrUnsupported is a picture in a form this cannot decode.
var ErrUnsupported = errors.New("gfx: unsupported")

// DecodeImage decodes a raster picture into an Image.
func DecodeImage(b []byte) (Image, error) { return DecodePicture(b) }

// Picture is a decoded raster image.
type Picture struct {
	pix   *raster.Pixmap
	raw   []byte
	cache *PictureCache
	// W and H are the picture's size in pixels.
	W, H int
	mu   sync.Mutex
	// alt holds the picture converted to other color models.
	alt map[raster.Model]*raster.Pixmap
}

// NewPicture wraps a premultiplied pixmap as a Picture without copying it.
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
	if p.raw != nil {
		return p.cache.pixels(p, cs)
	}
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
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxPictureArea {
		return fmt.Errorf("%w: picture is %dx%d", ErrUnsupported, w, h)
	}
	return nil
}

// DecodePicture decodes a raster picture with a registered or a standard decoder.
func DecodePicture(b []byte) (*Picture, error) {
	px, err := decodePixmap(b)
	if err != nil {
		return nil, err
	}
	return &Picture{pix: px, W: px.W, H: px.H}, nil
}

func decodePixmap(b []byte) (*raster.Pixmap, error) {
	var src image.Image
	var err error
	if dec := pictureDecoder(b); dec != nil {
		if err = checkHeader(b, maxPictureArea); err != nil {
			return nil, err
		}
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
	return px, nil
}
