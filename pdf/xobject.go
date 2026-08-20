package pdf

import "github.com/gen2brain/pdf/raster"

// doXObject runs the Do operator.
func (ip *interp) doXObject(n Name) {
	f := ip.doc.f
	obj := f.Lookup(f.GetDict(ip.res["XObject"]), n)
	st := f.GetStream(obj)
	if st == nil {
		ip.errorf("XObject /%s is not in the resources", n)
		return
	}
	if !ip.doc.optionalContentVisible(st.Dict["OC"]) {
		return
	}
	switch f.GetName(st.Dict["Subtype"]) {
	case "Image":
		ip.drawImage(st)
	case "Form":
		if ip.depth >= maxNesting {
			ip.errorf("form XObject nested deeper than %d", maxNesting)
			return
		}
		ip.runForm(st, false)
	case "PS":
	default:
		ip.errorf("XObject /%s has no subtype", n)
	}
}

// runForm interprets a form XObject. asMask suppresses the transparency
// group, because the caller has already opened one.
func (ip *interp) runForm(st *Stream, asMask bool) {
	f := ip.doc.f
	data, err := st.Data()
	if err != nil {
		ip.errorf("form XObject: %v", err)
		return
	}

	saved := ip.gs
	ip.gs = ip.gs.clone()
	savedBase := ip.base
	savedStack := len(ip.gstack)
	ip.depth++

	if m := f.GetFloats(st.Dict["Matrix"]); len(m) == 6 {
		ip.gs.ctm = raster.Concat(raster.Matrix{
			A: float32(m[0]), B: float32(m[1]), C: float32(m[2]),
			D: float32(m[3]), E: float32(m[4]), F: float32(m[5]),
		}, ip.gs.ctm)
	}

	group, masked := false, false
	if true {
		if g := f.GetDict(st.Dict["Group"]); g != nil && f.GetName(g["S"]) == "Transparency" {
			isolated := f.GetBool(g["I"], false) || asMask
			var cs *ColorSpace
			if isolated && g["CS"] != nil {
				cs = ip.doc.colorSpace(g["CS"], ip.res, 0)
			}
			bbox := raster.InfiniteRect
			if b := f.GetFloats(st.Dict["BBox"]); len(b) == 4 {
				bbox = ip.gs.ctm.ApplyRect(raster.Rect{
					X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3]),
				}.Normalized())
			}
			masked = ip.beginSoftMask(bbox)
			ip.dev.BeginGroup(bbox, cs, isolated, f.GetBool(g["K"], false), ip.gs.blend, ip.gs.fillAlpha)
			group = true
			ip.gs.fillAlpha, ip.gs.strokeAlpha = 1, 1
			ip.gs.blend = BlendNormal
			ip.gs.softMask = nil
		}
	}

	clipped := false
	if b := f.GetFloats(st.Dict["BBox"]); len(b) == 4 {
		r := raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
		var p raster.Path
		p.Rect(r.X0, r.Y0, r.X1-r.X0, r.Y1-r.Y0)
		ip.dev.ClipPath(&p, false, ip.gs.ctm, ip.scissor)
		clipped = true
	}

	ip.base = ip.gs.ctm
	ip.pushResources(f.GetDict(st.Dict["Resources"]))
	ip.gs.clipDepth = 0
	ip.run(data)
	ip.flushText()

	for i := 0; i < ip.gs.clipDepth; i++ {
		ip.dev.PopClip()
	}
	for len(ip.gstack) > savedStack {
		g := ip.gstack[len(ip.gstack)-1]
		ip.gstack = ip.gstack[:len(ip.gstack)-1]
		for i := 0; i < g.clipDepth; i++ {
			ip.dev.PopClip()
		}
	}
	ip.popResources()

	if clipped {
		ip.dev.PopClip()
	}
	if group {
		ip.dev.EndGroup()
	}
	if masked {
		ip.dev.PopClip()
	}
	ip.depth--
	ip.gs = saved
	ip.base = savedBase
}

func (ip *interp) pushResources(res Dict) {
	ip.resStack = append(ip.resStack, ip.res)
	ip.defaultsStack = append(ip.defaultsStack, ip.defaults)
	if res != nil {
		ip.res = res
	}
	if ip.doc.hasDefaults(ip.res) {
		ip.defaults = ip.doc.readDefaults(ip.res, ip.defaults)
		ip.dev.SetDefaultColorSpaces(ip.defaults)
		ip.defaultsSet = append(ip.defaultsSet, true)
		return
	}
	ip.defaultsSet = append(ip.defaultsSet, false)
}

func (ip *interp) popResources() {
	if n := len(ip.resStack); n > 0 {
		ip.res = ip.resStack[n-1]
		ip.resStack = ip.resStack[:n-1]
	}
	set := false
	if n := len(ip.defaultsSet); n > 0 {
		set = ip.defaultsSet[n-1]
		ip.defaultsSet = ip.defaultsSet[:n-1]
	}
	if n := len(ip.defaultsStack); n > 0 {
		ip.defaults = ip.defaultsStack[n-1]
		ip.defaultsStack = ip.defaultsStack[:n-1]
	}
	if set {
		ip.dev.SetDefaultColorSpaces(ip.defaults)
	}
}

// hasDefaults reports whether a resource dictionary names any default color
// space.
func (d *Document) hasDefaults(res Dict) bool {
	csres := d.f.GetDict(res["ColorSpace"])
	return csres["DefaultGray"] != nil || csres["DefaultRGB"] != nil || csres["DefaultCMYK"] != nil
}

// readDefaults reads /DefaultGray, /DefaultRGB and /DefaultCMYK out of a
// resource dictionary, over whatever the enclosing one set.
func (d *Document) readDefaults(res Dict, base *DefaultColorSpaces) *DefaultColorSpaces {
	next := base.clone()
	csres := d.f.GetDict(res["ColorSpace"])
	for _, e := range []struct {
		key Name
		dst **ColorSpace
		n   int
	}{
		{"DefaultGray", &next.Gray, 1},
		{"DefaultRGB", &next.RGB, 3},
		{"DefaultCMYK", &next.CMYK, 4},
	} {
		if obj := csres[e.key]; obj != nil {
			if cs := d.colorSpace(obj, res, 0); cs != nil && cs.N == e.n {
				*e.dst = cs
			}
		}
	}
	return next
}

const maxTiles = 1 << 14

// unwind closes the graphics state stack a nested content stream left open.
func (ip *interp) unwind(depth int) {
	for i := 0; i < ip.gs.clipDepth; i++ {
		ip.dev.PopClip()
	}
	ip.gs.clipDepth = 0
	for len(ip.gstack) > depth {
		g := ip.gstack[len(ip.gstack)-1]
		ip.gstack = ip.gstack[:len(ip.gstack)-1]
		for i := 0; i < g.clipDepth; i++ {
			ip.dev.PopClip()
		}
	}
}
