package svg

import (
	"strings"

	"github.com/gen2brain/folio/raster"
)

// Link is a link in a drawing: the area it covers, and where following it
// goes.
type Link struct {
	// Rect is the area the link covers, in the drawing's own coordinates.
	Rect raster.Rect
	// URI is where a link out of the drawing points, and "" for one inside.
	URI string
	// Fragment is the element a link inside the drawing names.
	Fragment string
}

// Links returns the links the drawing carries, one for every anchor element
// that covers something, in the order they are written.
func (p *Page) Links() []Link {
	d := p.doc
	if d == nil || d.root == nil {
		return nil
	}
	r := &runner{doc: d}
	m := raster.Identity
	st := initialState(d.width, d.height)
	if d.box != nil {
		m = viewport(d.box, d.align, d.slice, d.width, d.height)
		st.vw, st.vh = d.box[2], d.box[3]
	}
	var out []Link
	r.anchors(d.root, st, m, &out, 0)
	return out
}

// anchors walks the drawing for the anchors in it, carrying the transform and
// the viewport each one is written under.
func (r *runner) anchors(n *node, st state, ctm raster.Matrix, out *[]Link, depth int) {
	if depth > maxNesting || len(*out) > maxLinks {
		return
	}
	for _, k := range n.kids {
		switch k.name {
		case "defs", "symbol", "clipPath", "mask", "pattern", "marker",
			"linearGradient", "radialGradient", "title", "desc", "metadata", "style", "script":
			continue
		}
		at := raster.Concat(atOrigin(transform(r.prop(k, "transform")),
			r.prop(k, "transform-origin"), st.vw, st.vh, st.em), ctm)
		if k.name == "a" {
			if href := anchorHref(k); href != "" {
				if box := r.shapeBounds(k, st); !box.IsEmpty() {
					l := Link{Rect: at.ApplyRect(box)}
					if v, ok := strings.CutPrefix(href, "#"); ok {
						l.Fragment = v
					} else {
						l.URI = href
					}
					*out = append(*out, l)
				}
			}
		}
		r.anchors(k, st, at, out, depth+1)
	}
}

// maxLinks bounds what one drawing may report.
const maxLinks = 1 << 14

func anchorHref(n *node) string {
	if v := strings.TrimSpace(n.attr["href"]); v != "" {
		return v
	}
	return strings.TrimSpace(n.attr["xlink:href"])
}
