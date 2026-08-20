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

// RunAnnotations draws the appearance streams of the page annotations.
func (p *Page) RunAnnotations(dev Device, ctm raster.Matrix) {
	f := p.doc.f
	for _, a := range f.GetArray(p.dict["Annots"]) {
		dict := f.GetDict(a)
		if dict == nil {
			continue
		}
		if !p.annotDisplayed(dict) {
			continue
		}
		ap := p.appearance(dict)
		if ap == nil {
			continue
		}
		rect := p.doc.rect(dict["Rect"])
		if rect.IsEmpty() {
			continue
		}

		bbox := p.doc.rect(ap.Dict["BBox"])
		m := raster.Identity
		if fm := f.GetFloats(ap.Dict["Matrix"]); len(fm) == 6 {
			m = raster.Matrix{
				A: float32(fm[0]), B: float32(fm[1]), C: float32(fm[2]),
				D: float32(fm[3]), E: float32(fm[4]), F: float32(fm[5]),
			}
		}
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
		dev.SetDefaultColorSpaces(ip.defaults)
		ip.runForm(ap, false)
		ip.finish()
	}
}

// annotDisplayed reports whether an annotation is one a viewer shows at all.
// A widget with no field type or no name is a form field that was never
// finished, and MuPDF does not display one.
func (p *Page) annotDisplayed(dict Dict) bool {
	f := p.doc.f
	if f.GetName(dict["Subtype"]) != "Widget" {
		return true
	}
	return f.Lookup(dict, "FT") != nil && f.Lookup(dict, "T") != nil
}

// appearance returns the normal appearance stream of an annotation, or nil
// when it has none or must not be drawn.
func (p *Page) appearance(dict Dict) *Stream {
	f := p.doc.f
	if f.GetName(dict["Subtype"]) == "Popup" {
		return nil
	}
	flags := int(f.GetInt(dict["F"], 0))
	if flags&(annotHidden|annotNoView) != 0 {
		return nil
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

// rect reads a rectangle object.
func (d *Document) rect(obj Object) raster.Rect {
	b := d.f.GetFloats(obj)
	if len(b) != 4 {
		return raster.EmptyRect
	}
	return raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
}
