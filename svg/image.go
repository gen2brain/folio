package svg

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/url"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// image draws an image element, which is a picture or a drawing of its own
// fitted into the box the element gives the way preserveAspectRatio asks.
func (r *runner) image(n *node, ctm raster.Matrix, st state) {
	if st.invisible {
		return
	}
	href := n.attr["href"]
	if art := r.drawing(href); art != nil {
		r.drawn(n, art, ctm, st)
		return
	}
	pic := r.picture(href)
	if pic == nil {
		return
	}
	iw, ih := pic.Size()
	if iw <= 0 || ih <= 0 {
		return
	}
	x, _ := r.length(n, "x", st.vw, st)
	y, _ := r.length(n, "y", st.vh, st)
	w, wok := r.length(n, "width", st.vw, st)
	h, hok := r.length(n, "height", st.vh, st)
	// A picture with no size of its own is drawn at the size it is.
	if !wok || w <= 0 {
		w = float32(iw)
	}
	if !hok || h <= 0 {
		h = float32(ih)
	}

	// preserveAspectRatio says where in the box the picture sits, which is
	// where its own rectangle lands under the same transform a viewBox uses.
	al, sl := aspect(n.attr["preserveAspectRatio"])
	fit := viewport([]float32{0, 0, float32(iw), float32(ih)}, al, sl, w, h).
		ApplyRect(raster.Rect{X1: float32(iw), Y1: float32(ih)})
	// A device draws a picture into the unit square the matrix maps.
	m := raster.Concat(raster.Matrix{
		A: fit.X1 - fit.X0, D: fit.Y1 - fit.Y0,
		E: x + fit.X0, F: y + fit.Y0,
	}, ctm)

	pushed := false
	if al != "none" && sl {
		// A picture that covers its box is clipped to it.
		var box raster.Path
		box.Rect(x, y, w, h)
		r.dev.ClipPath(&box, false, ctm, raster.InfiniteRect)
		pushed = true
	}
	r.dev.FillImage(pic, m, st.fillOpacity, gfx.ColorParams{})
	if pushed {
		r.dev.PopClip()
	}
}

// drawn puts a drawing of its own in the box an image element gives it, the
// way preserveAspectRatio asks.
func (r *runner) drawn(n *node, art *Page, ctm raster.Matrix, st state) {
	bb := art.Bounds()
	iw, ih := bb.X1, bb.Y1
	if iw <= 0 || ih <= 0 || r.depth >= maxNesting {
		return
	}
	x, _ := r.length(n, "x", st.vw, st)
	y, _ := r.length(n, "y", st.vh, st)
	w, wok := r.length(n, "width", st.vw, st)
	h, hok := r.length(n, "height", st.vh, st)
	if !wok || w <= 0 {
		w = iw
	}
	if !hok || h <= 0 {
		h = ih
	}
	al, sl := aspect(n.attr["preserveAspectRatio"])
	m := raster.Concat(viewport([]float32{0, 0, iw, ih}, al, sl, w, h),
		raster.Concat(raster.Translate(x, y), ctm))

	var box raster.Path
	box.Rect(x, y, w, h)
	r.dev.ClipPath(&box, false, ctm, raster.InfiniteRect)
	if err := art.run(r.dev, m, r.depth+1); err != nil {
		r.fail(err)
	}
	r.dev.PopClip()
}

// drawing is the sub-drawing an address names, and nil for one that is a
// picture rather than a drawing.
func (r *runner) drawing(href string) *Page {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil
	}
	if p, ok := r.arts[href]; ok {
		return p
	}
	if r.arts == nil {
		r.arts = map[string]*Page{}
	}
	r.arts[href] = nil
	b := unzip(r.imageBytes(href))
	if !looksSVG(b) {
		return nil
	}
	d, err := LoadWith(b, &LoadOptions{Open: r.doc.open, Width: r.doc.width, Height: r.doc.height})
	if err != nil {
		r.fail(err)
		return nil
	}
	p, err := d.Page(0)
	if err != nil {
		r.fail(err)
		return nil
	}
	r.arts[href] = p
	return p
}

// looksSVG reports markup rather than one of the raster formats.
func looksSVG(b []byte) bool {
	head := b
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("<svg"))
}

// unzip unpacks the gzip an svgz is wrapped in.
func unzip(b []byte) []byte {
	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, maxUnzipped))
	if err != nil && len(out) == 0 {
		return b
	}
	return out
}

// maxUnzipped bounds what one compressed drawing may expand to.
const maxUnzipped = 1 << 26

// picture decodes what a reference names: the bytes a data URL carries, or a
// file beside the drawing. Nothing is fetched over a network.
func (r *runner) picture(href string) gfx.Image {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil
	}
	if p, ok := r.pics[href]; ok {
		return p
	}
	b := r.imageBytes(href)
	var img gfx.Image
	if len(b) > 0 {
		if p, err := gfx.DecodePicture(b); err == nil {
			img = p
		} else {
			r.fail(err)
		}
	}
	if r.pics == nil {
		r.pics = map[string]gfx.Image{}
	}
	r.pics[href] = img
	return img
}

func (r *runner) imageBytes(href string) []byte {
	if strings.HasPrefix(href, "data:") {
		i := strings.IndexByte(href, ',')
		if i < 0 {
			return nil
		}
		head, body := href[5:i], href[i+1:]
		if strings.Contains(head, ";base64") {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
			if err != nil {
				return nil
			}
			return b
		}
		s, err := url.PathUnescape(body)
		if err != nil {
			return nil
		}
		return []byte(s)
	}
	// A drawing read out of memory with no loader has nowhere to look, and
	// an address that leaves the drawing is not followed.
	if r.doc.open == nil || strings.Contains(href, "://") {
		return nil
	}
	name := href
	if u, err := url.PathUnescape(href); err == nil {
		name = u
	}
	b, err := r.doc.open(name)
	if err != nil {
		return nil
	}
	return b
}
