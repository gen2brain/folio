package pdf

import "github.com/gen2brain/pdf/raster"

// Link is a link annotation: the area of the page it covers, and where
// following it goes.
type Link struct {
	// Rect is the area the link covers, in page space at 72 dots per inch.
	Rect raster.Rect
	// URI is where a link out of the document points.
	URI string
	// Page is the page a link inside the document goes to, and -1 otherwise.
	Page int
	// Point is where on that page it goes, in page space.
	Point raster.Point
}

// Links returns the page's link annotations in the order the file lists them.
func (p *Page) Links() []Link {
	f := p.doc.f
	m := p.Matrix(72)
	var out []Link
	for _, a := range f.GetArray(p.dict["Annots"]) {
		dict := f.GetDict(a)
		if dict == nil || f.GetName(dict["Subtype"]) != "Link" {
			continue
		}
		l := Link{Rect: m.ApplyRect(p.annotRect(dict)), Page: -1}
		if act := f.GetDict(dict["A"]); act != nil {
			l.URI = p.doc.actionURI(act)
			if l.URI == "" {
				l.Page, l.Point = p.doc.destination(f.Lookup(act, "D"))
			}
		} else {
			l.Page, l.Point = p.doc.destination(dict["Dest"])
		}
		out = append(out, l)
	}
	return out
}

// annotRect is an annotation's /Rect, normalized.
func (p *Page) annotRect(dict Dict) raster.Rect {
	r := p.doc.f.GetFloats(dict["Rect"])
	if len(r) != 4 {
		return raster.EmptyRect
	}
	return raster.Rect{
		X0: float32(r[0]), Y0: float32(r[1]),
		X1: float32(r[2]), Y1: float32(r[3]),
	}.Normalized()
}

// actionURI reads where an action leads out of the document.
func (d *Document) actionURI(act Dict) string {
	f := d.f
	switch f.GetName(act["S"]) {
	case "URI":
		return string(f.GetBytes(act["URI"]))
	case "Launch":
		if fs := f.GetDict(act["F"]); fs != nil {
			return string(f.GetBytes(f.Lookup(fs, "UF", "F")))
		}
		return string(f.GetBytes(act["F"]))
	}
	return ""
}

// destination resolves a destination to a page index and a point, and -1 for
// one that does not resolve.
func (d *Document) destination(obj Object) (int, raster.Point) {
	f := d.f
	switch v := f.Resolve(obj).(type) {
	case Name:
		return d.destination(d.namedDest(string(v)))
	case String:
		return d.destination(d.namedDest(string(v)))
	case Dict:
		return d.destination(f.Lookup(v, "D"))
	case Array:
		return d.destArray(v)
	}
	return -1, raster.Point{}
}

// destArray reads the explicit form, ISO 32000-1 12.3.2.2.
func (d *Document) destArray(a Array) (int, raster.Point) {
	if len(a) == 0 {
		return -1, raster.Point{}
	}
	f := d.f
	page := -1
	switch v := a[0].(type) {
	case Ref:
		page = f.PageIndex(v)
	case Integer:
		if int(v) >= 0 && int(v) < f.NumPages() {
			page = int(v)
		}
	}
	if page < 0 {
		return -1, raster.Point{}
	}
	var pt raster.Point
	if len(a) >= 4 {
		switch f.GetName(a[1]) {
		case "XYZ", "FitR":
			pt.X = float32(f.GetFloat(a[2], 0))
			pt.Y = float32(f.GetFloat(a[3], 0))
		}
	}
	return page, pt
}

// namedDest looks a name up in /Dests, the name tree and the 1.1 dictionary.
func (d *Document) namedDest(name string) Object {
	f := d.f
	names := f.GetDict(f.Lookup(f.Catalog(), "Names"))
	if v := f.NameTreeLookup(f.Lookup(names, "Dests"), name); v != nil {
		return v
	}
	return f.Lookup(f.GetDict(f.Catalog()["Dests"]), Name(name))
}
