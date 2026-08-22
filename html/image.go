package html

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
)

// picture is a decoded image a box draws. It is what a device is handed, so
// it implements gfx.Image.
// picture is a decoded raster image, which gfx holds because the SVG side
// decodes them too.
type picture = gfx.Picture

// decodePicture reads one of the formats a book carries a picture in.
func decodePicture(b []byte) (*picture, error) { return gfx.DecodePicture(b) }

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
	w, h := float32(p.W), float32(p.H)
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
