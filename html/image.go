package html

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/svg"
)

// picture is a decoded raster image, which gfx holds because the SVG side
// decodes them too.
type picture = gfx.Picture

// visual is what an element draws: a raster picture, or a drawing that runs
// onto the device rather than a grid of pixels. One of the two is set.
type visual struct {
	pic *picture
	art *svg.Page
	// w and h are how big it is of itself, in CSS pixels.
	w, h float32
}

// decodePicture reads one of the formats a book carries a picture in.
func decodePicture(b []byte) (*picture, error) { return gfx.DecodePicture(b) }

// picture decodes the image an element names, once per part.
func (l *layout) picture(b *box) *visual {
	src := Attr(b.node, "src")
	if src == "" {
		return nil
	}
	return l.pictureAt(Resolve(l.path, src))
}

// pictureAt decodes one image of the book, once per part. The path is the one
// the address resolved to, which for a sheet is relative to the sheet.
func (l *layout) pictureAt(p string) *visual {
	if l.doc == nil || p == "" || strings.Contains(p, "://") {
		return nil
	}
	if v, ok := l.pics[p]; ok {
		return v
	}
	if l.pics == nil {
		l.pics = map[string]*visual{}
	}
	l.pics[p] = nil
	raw, err := l.doc.Read(p)
	if err != nil {
		l.fail(err)
		return nil
	}
	v, err := l.doc.visualOf(p, raw, l.pw, l.ph)
	if err != nil {
		l.fail(err)
		return nil
	}
	l.pics[p] = v
	return v
}

// visualOf decodes what a book carries at a path, which is a drawing when it
// is SVG and a raster picture otherwise. A drawing reaches the pictures it
// names through the book it came out of.
func (d *Document) visualOf(p string, raw []byte, pw, ph float32) (*visual, error) {
	if isSVG(p, raw) {
		doc, err := svg.LoadWith(raw, &svg.LoadOptions{
			Open:  func(name string) ([]byte, error) { return d.Read(Resolve(p, name)) },
			Width: pw, Height: ph,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		pg, err := doc.Page(0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		bb := pg.Bounds()
		return &visual{art: pg, w: bb.X1 - bb.X0, h: bb.Y1 - bb.Y0}, nil
	}
	pic, err := decodePicture(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &visual{pic: pic, w: float32(pic.W), h: float32(pic.H)}, nil
}

// isSVG is what the address says, and what the bytes say when it says nothing.
func isSVG(p string, raw []byte) bool {
	if strings.EqualFold(path.Ext(p), ".svg") {
		return true
	}
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(string(head), "<svg")
}

func (l *layout) fail(err error) {
	if len(l.errs) < 32 {
		l.errs = append(l.errs, err)
	}
}

// pictureSize is how much room a picture takes: what the style or the element
// asks for, its own size otherwise, and never wider than the line.
func pictureSize(b *box, v *visual, avail, cbh float32) (float32, float32) {
	w, h := v.w, v.h
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

// drawnText is what a spine item that is a drawing says, and nothing for one
// that is a picture.
func (d *Document) drawnText(it Item) (string, error) {
	raw, err := d.Read(it.Path)
	if err != nil {
		return "", err
	}
	if !isSVG(it.Path, raw) {
		return "", nil
	}
	v, err := d.visualOf(it.Path, raw, 0, 0)
	if err != nil || v.art == nil {
		return "", err
	}
	return v.art.Text()
}
