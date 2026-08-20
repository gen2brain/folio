package pdf

import "github.com/gen2brain/pdf/raster"

// Annotation flags, ISO 32000-1 table 165.
const (
	annotInvisible = 1 << iota
	annotHidden
	annotPrint
	annotNoZoom
	annotNoRotate
	annotNoView
)

// RunAnnotations draws the appearance streams of the page annotations. The
// widgets come last, after everything else on the page, which is the order
// MuPDF draws them in and the one a form on top of its own background needs.
func (p *Page) RunAnnotations(dev Device, ctm raster.Matrix) {
	f := p.doc.f
	var widgets []Dict
	for _, a := range f.GetArray(p.dict["Annots"]) {
		dict := f.GetDict(a)
		if dict == nil {
			continue
		}
		switch f.GetName(dict["Subtype"]) {
		case "Popup", "Link":
			continue
		case "Widget":
			if f.Lookup(dict, "FT") == nil || f.Lookup(dict, "T") == nil {
				continue
			}
			widgets = append(widgets, dict)
			continue
		}
		p.runAnnotation(dev, ctm, dict)
	}
	for _, dict := range widgets {
		p.runAnnotation(dev, ctm, dict)
	}
}

// runAnnotation draws one annotation's appearance, fitted to its rectangle.
// The device is told the page's default color spaces first whether or not
// anything is then drawn, which is where MuPDF tells it.
func (p *Page) runAnnotation(dev Device, ctm raster.Matrix, dict Dict) {
	dev.SetDefaultColorSpaces(p.defaultSpaces())

	ap := p.appearance(dict)
	if ap == nil {
		return
	}
	rect := p.doc.rect(dict["Rect"])
	if rect.IsEmpty() {
		return
	}

	bbox := p.doc.rect(ap.Dict["BBox"])
	m := p.doc.matrix(ap.Dict["Matrix"], raster.Identity)
	fit := raster.Identity
	if !bbox.IsEmpty() {
		t := m.ApplyRect(bbox)
		if !t.IsEmpty() {
			sx, sy := float32(1), float32(1)
			if t.X1 != t.X0 {
				sx = (rect.X1 - rect.X0) / (t.X1 - t.X0)
			}
			if t.Y1 != t.Y0 {
				sy = (rect.Y1 - rect.Y0) / (t.Y1 - t.Y0)
			}
			fit = raster.Concat(raster.Translate(-t.X0, -t.Y0), raster.Scale(sx, sy))
			fit = raster.Concat(fit, raster.Translate(rect.X0, rect.Y0))
		}
	}

	ip := p.newInterp(dev, raster.Concat(fit, ctm))
	ip.defaults = p.defaultSpaces()
	ip.runForm(ap, false)
	ip.finish()
}

// appearance returns the normal appearance stream of an annotation, or nil
// when it has none or must not be drawn.
func (p *Page) appearance(dict Dict) *Stream {
	f := p.doc.f
	flags := int(f.GetInt(dict["F"], 0))
	if flags&(annotInvisible|annotHidden) != 0 {
		return nil
	}
	switch p.doc.usage {
	case UsagePrint:
		if flags&annotPrint == 0 {
			return nil
		}
	default:
		if flags&annotNoView != 0 {
			return nil
		}
	}
	if !p.doc.optionalContentVisible(dict["OC"]) {
		return nil
	}

	n := f.Resolve(f.GetDict(dict["AP"])["N"])
	switch v := n.(type) {
	case *Stream:
		return v
	case Dict:
		if as := f.GetName(dict["AS"]); as != "" {
			return f.GetStream(v[as])
		}
		if len(v) == 1 {
			for _, e := range v {
				return f.GetStream(e)
			}
		}
	}
	return nil
}

// matrix reads a six number array as a transform, or returns fallback when the
// object is not one.
func (d *Document) matrix(obj Object, fallback raster.Matrix) raster.Matrix {
	m := d.f.GetFloats(obj)
	if len(m) != 6 {
		return fallback
	}
	return raster.Matrix{
		A: float32(m[0]), B: float32(m[1]), C: float32(m[2]),
		D: float32(m[3]), E: float32(m[4]), F: float32(m[5]),
	}
}

// rect reads a rectangle object.
func (d *Document) rect(obj Object) raster.Rect {
	b := d.f.GetFloats(obj)
	if len(b) != 4 {
		return raster.EmptyRect
	}
	return raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
}
