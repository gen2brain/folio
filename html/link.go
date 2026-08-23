package html

import (
	"strings"

	"github.com/gen2brain/folio/raster"
	xhtml "golang.org/x/net/html"
)

// Link is a link on a page: the area it covers, and where following it goes.
type Link struct {
	// Rect is the area the link covers, in page space at 72 dots per inch.
	Rect raster.Rect
	// URI is where a link out of the document points, and "" for one inside.
	URI string
	// Path is the part a link inside the document leads to, Fragment the anchor.
	Path, Fragment string
}

// hrefOf is the address an anchor element carries, and "" for anything else.
func hrefOf(n *xhtml.Node) string {
	if n == nil || n.Type != xhtml.ElementNode || n.Data != "a" {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "href") && a.Val != "" {
			return a.Val
		}
	}
	return ""
}

// Links returns the links on the page, one for every run of text an anchor
// covers, in the order they are laid out.
func (p *Page) Links() []Link {
	if p.part == nil || p.part.root == nil {
		return nil
	}
	o := p.doc.options()
	m := p.pageMatrix(o)
	var out []Link
	var walk func(b *box)
	walk = func(b *box) {
		for i := range b.lines {
			ln := &b.lines[i]
			if ln.y >= p.bottom || ln.y+ln.h <= p.top {
				continue
			}
			for j := range ln.frags {
				f := &ln.frags[j]
				if f.link == "" || f.w <= 0 {
					continue
				}
				r := m.ApplyRect(raster.Rect{X0: f.x, Y0: ln.y, X1: f.x + f.w, Y1: ln.y + ln.h})
				out = appendLink(out, p, f.link, r)
			}
		}
		for _, k := range b.kids {
			walk(k)
		}
	}
	walk(p.part.root)
	return out
}

// appendLink adds one run to the link before it when the two are the same
// anchor side by side on a line.
func appendLink(out []Link, p *Page, href string, r raster.Rect) []Link {
	l := Link{Rect: r}
	if isExternal(href) {
		l.URI = href
	} else {
		l.Path, l.Fragment = splitFragment(Resolve(p.Path(), href))
		if l.Fragment == "" {
			_, l.Fragment = splitFragment(href)
		}
	}
	if n := len(out); n > 0 {
		if v := &out[n-1]; v.URI == l.URI && v.Path == l.Path && v.Fragment == l.Fragment &&
			near(v.Rect.Y0, r.Y0) && near(v.Rect.Y1, r.Y1) {
			v.Rect.X0 = min(v.Rect.X0, r.X0)
			v.Rect.X1 = max(v.Rect.X1, r.X1)
			return out
		}
	}
	return append(out, l)
}

func near(a, b float32) bool { return a-b < 0.01 && b-a < 0.01 }

// isExternal reports an address that leaves the document.
func isExternal(href string) bool {
	if i := strings.Index(href, "://"); i > 0 {
		return true
	}
	switch {
	case strings.HasPrefix(href, "mailto:"), strings.HasPrefix(href, "tel:"),
		strings.HasPrefix(href, "data:"):
		return true
	}
	return false
}
