package pdf

import (
	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

// Image is an image XObject or an inline image. The pixels are not decoded
// until a device asks for them.
type Image struct {
	Width, Height int
	BPC           int
	CS            *ColorSpace
	// Mask is true for a stencil mask, which paints the fill color through
	// its one bit samples rather than carrying color of its own.
	Mask bool
	// Interpolate is the /Interpolate hint.
	Interpolate bool
	// Decode is the /Decode array, nil when the default applies.
	Decode []float64
	// SMask and StencilMask are the two ways an image carries transparency.
	SMask       *Image
	StencilMask *Image
	// ColorKey is the /Mask color key range, nil when there is none.
	ColorKey []int

	stream *syntax.Stream
	dict   Dict
	inline []byte
}

// image builds an Image from an image XObject or an inline image dictionary.
func (d *Document) image(st *syntax.Stream, dict Dict, res Dict, inline []byte, depth int) *Image {
	f := d.f
	img := &Image{
		Width:       int(f.GetInt(f.Lookup(dict, "Width", "W"), 0)),
		Height:      int(f.GetInt(f.Lookup(dict, "Height", "H"), 0)),
		BPC:         int(f.GetInt(f.Lookup(dict, "BitsPerComponent", "BPC"), 8)),
		Mask:        f.GetBool(f.Lookup(dict, "ImageMask", "IM"), false),
		Interpolate: f.GetBool(f.Lookup(dict, "Interpolate", "I"), false),
		Decode:      f.GetFloats(f.Lookup(dict, "Decode", "D")),
		stream:      st,
		dict:        dict,
		inline:      inline,
	}
	if img.Width <= 0 || img.Height <= 0 {
		d.errorf("image is %dx%d", img.Width, img.Height)
		return nil
	}
	if img.Mask {
		img.BPC = 1
	} else if cs := f.Lookup(dict, "ColorSpace", "CS"); cs != nil {
		img.CS = d.colorSpace(cs, res, 0)
	}

	if depth < maxNesting && inline == nil {
		if sm := f.GetStream(dict["SMask"]); sm != nil {
			img.SMask = d.image(sm, sm.Dict, res, nil, depth+1)
		}
		switch m := f.Resolve(dict["Mask"]).(type) {
		case *syntax.Stream:
			img.StencilMask = d.image(m, m.Dict, res, nil, depth+1)
		case Array:
			for _, v := range m {
				img.ColorKey = append(img.ColorKey, int(f.GetInt(v, 0)))
			}
		}
	}
	return img
}

// MaskImage returns the image that supplies this one's transparency, whether
// it came from /SMask or from a stencil /Mask. Both are drawn the same way:
// the mask clips, the image fills.
func (i *Image) MaskImage() *Image {
	if i.SMask != nil {
		return i.SMask
	}
	return i.StencilMask
}

// Dict returns the image dictionary.
func (i *Image) Dict() Dict { return i.dict }

// drawImage paints an image XObject.
func (ip *interp) drawImage(st *Stream) {
	img := ip.doc.image(st, st.Dict, ip.res, nil, 0)
	if img == nil {
		return
	}
	ip.paintImage(img)
}

func (ip *interp) paintImage(img *Image) {
	if ip.hidden > 0 {
		return
	}
	ctm := raster.Concat(raster.Matrix{A: 1, D: -1, F: 1}, ip.gs.ctm)
	bbox := ctm.ApplyRect(raster.Rect{X1: 1, Y1: 1})

	if mask := img.MaskImage(); mask != nil {
		group := false
		if ip.gs.blend != BlendNormal {
			ip.dev.BeginGroup(bbox, nil, false, false, ip.gs.blend, 1)
			group = true
		}
		ip.dev.ClipImageMask(mask, ctm, bbox)
		ip.drawImageBody(img, ctm)
		ip.dev.PopClip()
		if group {
			ip.dev.EndGroup()
		}
		return
	}

	d := ip.beginDraw(bbox)
	ip.drawImageBody(img, ctm)
	ip.endDraw(d)
}

func (ip *interp) drawImageBody(img *Image, ctm raster.Matrix) {
	if img.Mask {
		if ip.gs.fill.cs.IsPattern() {
			ip.dev.FillImageMask(img, ctm, DeviceGray, []float32{0}, ip.gs.fillAlpha, ip.gs.params)
		} else {
			ip.dev.FillImageMask(img, ctm, ip.gs.fill.cs, ip.gs.fill.value, ip.gs.fillAlpha, ip.gs.params)
		}
	} else {
		ip.dev.FillImage(img, ctm, ip.gs.fillAlpha, ip.gs.params)
	}
}

// inlineImage reads BI ... ID ... EI. The binary data lies in the content
// stream itself, so the parser has to be steered around it by hand.
func (ip *interp) inlineImage(p *syntax.Parser) {
	dict := Dict{}
	var key Name
	for {
		obj, ok := p.Object()
		if !ok {
			ip.errorf("end of content stream inside an inline image")
			return
		}
		if kw, isKw := obj.(syntax.Keyword); isKw {
			if kw == "ID" {
				break
			}
			ip.errorf("unexpected %q in an inline image dictionary", string(kw))
			continue
		}
		if key == "" {
			if n, isName := obj.(Name); isName {
				key = n
			}
			continue
		}
		dict[key] = obj
		key = ""
	}

	l := p.Lexer()
	buf := l.Bytes()
	start := min(l.Pos(), len(buf))
	if start < len(buf) && isPDFSpace(buf[start]) {
		start++
	}

	end := min(max(ip.inlineImageEnd(dict, buf, start), start), len(buf))
	data := buf[start:end]

	img := ip.doc.image(nil, dict, ip.res, data, 0)
	if img != nil && ip.doc.optionalContentVisible(dict["OC"]) {
		ip.paintImage(img)
	}

	l.SetPos(end)
	p.Refill()
	for {
		obj, ok := p.Object()
		if !ok {
			return
		}
		if kw, isKw := obj.(syntax.Keyword); isKw && kw == "EI" {
			return
		}
	}
}

// inlineImageEnd finds where the data of an inline image stops.
func (ip *interp) inlineImageEnd(dict Dict, buf []byte, start int) int {
	f := ip.doc.f
	if n := f.GetInt(f.Lookup(dict, "Length", "L"), 0); n > 0 && start+int(n) <= len(buf) {
		return start + int(n)
	}
	if len(f.GetNames(f.Lookup(dict, "Filter", "F"))) == 0 {
		w := int(f.GetInt(f.Lookup(dict, "Width", "W"), 0))
		h := int(f.GetInt(f.Lookup(dict, "Height", "H"), 0))
		bpc := int(f.GetInt(f.Lookup(dict, "BitsPerComponent", "BPC"), 8))
		n := 1
		if f.GetBool(f.Lookup(dict, "ImageMask", "IM"), false) {
			bpc, n = 1, 1
		} else if cs := f.Lookup(dict, "ColorSpace", "CS"); cs != nil {
			n = ip.doc.colorSpace(cs, ip.res, 0).N
		}
		if w > 0 && h > 0 && bpc > 0 && n > 0 {
			size := ((w*n*bpc + 7) / 8) * h
			if start+size <= len(buf) {
				return start + size
			}
		}
	}
	for i := start; i+1 < len(buf); i++ {
		if buf[i] != 'E' || buf[i+1] != 'I' {
			continue
		}
		if i > start && !isPDFSpace(buf[i-1]) {
			continue
		}
		if i+2 < len(buf) && !isPDFSpace(buf[i+2]) {
			continue
		}
		return i - 1
	}
	ip.errorf("inline image has no EI")
	return len(buf)
}

func isPDFSpace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}
