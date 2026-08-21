package html

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"strconv"
	"strings"
	"sync"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// picture is a decoded image a box draws. It is what a device is handed, so
// it implements gfx.Image.
type picture struct {
	pix *raster.Pixmap
	w   int
	h   int
	mu  sync.Mutex
	// alt is the picture in another color model, kept for a device that
	// composites in one this was not decoded into.
	alt map[raster.Model]*raster.Pixmap
}

// Size implements gfx.Image.
func (p *picture) Size() (int, int) { return p.w, p.h }

// ColorSpace implements gfx.Image.
func (p *picture) ColorSpace() *gfx.ColorSpace { return gfx.DeviceRGB }

// Stencil implements gfx.Image.
func (p *picture) Stencil() bool { return false }

// Smooth implements gfx.Image.
func (p *picture) Smooth() bool { return true }

// Pixels implements gfx.Image.
func (p *picture) Pixels(cs *gfx.ColorSpace, shrink int) (*raster.Pixmap, error) {
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
func decodePicture(b []byte) (*picture, error) {
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
	return &picture{pix: px, w: r.Dx(), h: r.Dy()}, nil
}

// picture decodes the image an element names, once per part.
func (l *layout) picture(b *box) *picture {
	src := Attr(b.node, "src")
	if src == "" {
		return nil
	}
	return l.pictureAt(Resolve(l.path, src))
}

// pictureAt decodes one image of the book, once per part. The path is the one
// the address resolved to, which for a sheet is relative to the sheet.
func (l *layout) pictureAt(path string) *picture {
	if l.doc == nil || path == "" || strings.Contains(path, "://") {
		return nil
	}
	if p, ok := l.pics[path]; ok {
		return p
	}
	if l.pics == nil {
		l.pics = map[string]*picture{}
	}
	l.pics[path] = nil
	raw, err := l.doc.Read(path)
	if err != nil {
		l.fail(err)
		return nil
	}
	p, err := decodePicture(raw)
	if err != nil {
		l.fail(fmt.Errorf("%s: %w", path, err))
		return nil
	}
	l.pics[path] = p
	return p
}

func (l *layout) fail(err error) {
	if len(l.errs) < 32 {
		l.errs = append(l.errs, err)
	}
}

// pictureSize is how much room a picture takes: what the style or the element
// asks for, its own size otherwise, and never wider than the line.
func pictureSize(b *box, p *picture, avail, cbh float32) (float32, float32) {
	w, h := float32(p.w), float32(p.h)
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	ratio := h / w
	sw := b.style.Width
	sh, tall := definiteHeight(b.style.Height, cbh)
	switch {
	case sw.Unit == UnitPx:
		w, h = sw.Value, sw.Value*ratio
	case sw.Unit == UnitPercent && avail > 0:
		w = sw.Resolve(avail)
		h = w * ratio
	case tall:
		h, w = sh, sh/ratio
	default:
		if v := attrLength(b.node, "width"); v > 0 {
			w, h = v, v*ratio
		}
	}
	// A picture given both a width and a height is drawn at both of them,
	// which is how a cover fills the page it is the whole of.
	if tall && (sw.Unit == UnitPx || (sw.Unit == UnitPercent && avail > 0)) {
		h = sh
	}
	if m := b.style.MaxWidth; !m.Auto() {
		if lim := m.Resolve(avail); lim > 0 && w > lim {
			w, h = lim, lim*ratio
		}
	}
	if m, ok := definiteHeight(b.style.MaxHeight, cbh); ok && m > 0 && h > m {
		h, w = m, m/ratio
	}
	if avail > 0 && w > avail {
		w, h = avail, avail*ratio
	}
	return w, h
}

// attrLength reads the width or height an img element carries as a number.
func attrLength(n *Node, name string) float32 {
	v := strings.TrimSuffix(strings.TrimSpace(Attr(n, name)), "px")
	f, err := strconv.ParseFloat(v, 32)
	if err != nil || f <= 0 {
		return 0
	}
	return float32(f)
}
