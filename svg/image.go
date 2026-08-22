package svg

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/raster"
)

// image draws an image element, which is a picture fitted into the box the
// element gives the way preserveAspectRatio asks.
func (r *runner) image(n *node, ctm raster.Matrix, st state) {
	pic := r.picture(n.attr["href"])
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
	// A drawing read out of memory has no directory to look in, and an
	// address that leaves it is not followed.
	if r.doc.dir == "" || strings.Contains(href, "://") {
		return nil
	}
	name := href
	if u, err := url.PathUnescape(href); err == nil {
		name = u
	}
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(r.doc.dir, filepath.FromSlash(name)))
	if err != nil {
		return nil
	}
	return b
}
