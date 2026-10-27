package svg

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/url"
	urlpath "path"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// image draws an image element fitted into its box by preserveAspectRatio.
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
	if !wok || w <= 0 {
		w = float32(iw)
	}
	if !hok || h <= 0 {
		h = float32(ih)
	}

	al, sl := aspect(n.attr["preserveAspectRatio"])
	fit := viewport([]float32{0, 0, float32(iw), float32(ih)}, al, sl, w, h).
		ApplyRect(raster.Rect{X1: float32(iw), Y1: float32(ih)})
	m := raster.Concat(raster.Matrix{
		A: fit.X1 - fit.X0, D: fit.Y1 - fit.Y0,
		E: x + fit.X0, F: y + fit.Y0,
	}, ctm)

	var box raster.Path
	box.Rect(x, y, w, h)
	clipped := al != "none" && sl
	cost := reach(r.dev, m.ApplyRect(raster.Rect{X1: 1, Y1: 1}))
	if clipped {
		cost += r.pathWork(&box, true, nil, ctm)
	}
	if !r.spend(cost) {
		return
	}
	if clipped {
		r.dev.ClipPath(&box, false, ctm, raster.InfiniteRect)
	}
	r.dev.FillImage(pic, m, st.fillOpacity, gfx.ColorParams{})
	if clipped {
		r.dev.PopClip()
	}
}

// drawn puts a nested drawing in the box an image element gives it.
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
	if !r.spend(r.pathWork(&box, true, nil, ctm)) {
		return
	}
	r.dev.ClipPath(&box, false, ctm, raster.InfiniteRect)
	if err := art.run(r.dev, m, r.depth+1, r.work); err != nil {
		r.fail(err)
	}
	r.dev.PopClip()
}

// drawing is the sub-drawing an address names, or nil for a raster picture.
func (r *runner) drawing(href string) *Page {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil
	}
	key := refKey(href)
	if p, ok := r.arts[key]; ok {
		return p
	}
	if r.arts == nil {
		r.arts = map[string]*Page{}
	}
	r.arts[key] = nil
	raw := r.imageBytes(href)
	b := unzip(raw)
	if !looksSVG(b) {
		if _, ok := r.pics[key]; !ok && len(raw) > 0 {
			r.keepPicture(key, raw)
		}
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
	r.arts[key] = p
	return p
}

// looksSVG reports whether b is markup and not a raster format.
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

// picture decodes what a reference names: a data URL or a file beside the drawing.
func (r *runner) picture(href string) gfx.Image {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil
	}
	key := refKey(href)
	if p, ok := r.pics[key]; ok {
		return p
	}
	return r.keepPicture(key, r.imageBytes(href))
}

// refKey keys a reference in the caches, a file by its cleaned path.
func refKey(href string) string {
	if strings.HasPrefix(href, "data:") {
		return href
	}
	return urlpath.Clean(href)
}

// keepPicture decodes a picture and caches it under key within maxHeldPixels.
func (r *runner) keepPicture(key string, b []byte) gfx.Image {
	var img gfx.Image
	if len(b) > 0 {
		if p, err := gfx.DecodePicture(b); err == nil {
			img = p
		} else {
			r.fail(err)
		}
	}
	if img != nil {
		w, h := img.Size()
		if r.held+w*h > maxHeldPixels {
			return img
		}
		r.held += w * h
	}
	if r.pics == nil {
		r.pics = map[string]gfx.Image{}
	}
	r.pics[key] = img
	return img
}

// maxHeldPixels bounds the pixels of the pictures one drawing keeps decoded.
const maxHeldPixels = 1 << 27

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
