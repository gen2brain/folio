package pdf

import "github.com/gen2brain/pdf/raster"

// doShading runs the sh operator, which paints a shading over the clip.
func (ip *interp) doShading(n Name) {
	f := ip.doc.f
	obj := f.Lookup(f.GetDict(ip.res["Shading"]), n)
	if obj == nil {
		ip.errorf("shading /%s is not in the resources", n)
		return
	}
	sh := ip.doc.shade(obj, ip.res)
	if sh == nil || ip.hidden > 0 {
		return
	}
	bbox := ip.scissor
	if !sh.BBox.IsEmpty() {
		bbox = ip.gs.ctm.ApplyRect(sh.BBox)
	}
	d := ip.beginDraw(bbox)
	ip.dev.FillShade(sh, ip.gs.ctm, ip.gs.fillAlpha, ip.gs.params)
	ip.endDraw(d)
}

// fillWithPattern paints a path with a tiling or shading pattern.
func (ip *interp) fillWithPattern(path *raster.Path, evenOdd, stroke bool) {
	c := &ip.gs.fill
	alpha := ip.gs.fillAlpha
	shape := path.Bounds(ip.gs.ctm)
	if stroke {
		c = &ip.gs.strokeColor
		alpha = ip.gs.strokeAlpha
		shape = path.StrokeBounds(&ip.gs.stroke, ip.gs.ctm)
	}
	ip.paintPattern(c, alpha, shape, func() {
		if stroke {
			ip.dev.ClipStrokePath(path, &ip.gs.stroke, ip.gs.ctm, ip.scissor)
		} else {
			ip.dev.ClipPath(path, evenOdd, ip.gs.ctm, ip.scissor)
		}
	})
}

// paintPattern fills whatever clip pushes with the pattern a color names.
// shape is the device space box of what is being painted, which is what
// bounds the repetitions of a tiling pattern.
func (ip *interp) paintPattern(c *color, alpha float32, shape raster.Rect, clip func()) {
	if c.pattern == nil || ip.depth >= maxNesting || alpha == 0 {
		return
	}
	f := ip.doc.f
	dict := f.GetDict(c.pattern)
	if dict == nil {
		return
	}

	pm := ip.doc.matrix(dict["Matrix"], raster.Identity)

	switch f.GetInt(dict["PatternType"], 0) {
	case 2:
		sh := ip.doc.shade(dict["Shading"], ip.res)
		if sh == nil {
			return
		}
		sh.Matrix = pm
		clip()
		ip.dev.FillShade(sh, c.patternCTM, alpha, ip.gs.params)
		ip.dev.PopClip()

	case 1:
		st := f.GetStream(c.pattern)
		if st == nil {
			return
		}
		if alpha != 1 {
			ip.dev.BeginGroup(shape, nil, false, false, BlendNormal, alpha)
			defer ip.dev.EndGroup()
			alpha = 1
		}
		clip()
		ip.runTile(st, raster.Concat(pm, c.patternCTM), shape, alpha)
		ip.dev.PopClip()
	}
}

// runTile draws one tiling pattern over the area a path covers. shape is the
// bounding box of that path in device space.
func (ip *interp) runTile(st *Stream, ptm raster.Matrix, shape raster.Rect, alpha float32) {
	f := ip.doc.f
	b := f.GetFloats(st.Dict["BBox"])
	if len(b) != 4 {
		return
	}
	view := raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
	xstep := float32(f.GetFloat(st.Dict["XStep"], float64(view.X1-view.X0)))
	ystep := float32(f.GetFloat(st.Dict["YStep"], float64(view.Y1-view.Y0)))
	if xstep == 0 {
		xstep = view.X1 - view.X0
	}
	if ystep == 0 {
		ystep = view.Y1 - view.Y0
	}
	if xstep == 0 || ystep == 0 {
		return
	}

	data, err := st.Data()
	if err != nil {
		ip.errorf("tiling pattern: %v", err)
		return
	}

	inv, ok := ptm.Invert()
	if !ok {
		return
	}
	area := inv.ApplyRect(shape)

	cached := ip.dev.BeginTile(area, view, xstep, ystep, ptm) != 0

	saved := ip.gs
	ip.gs = ip.gs.clone()
	savedBase := ip.base
	savedStack := len(ip.gstack)
	ip.depth++
	ip.gs.fillAlpha, ip.gs.strokeAlpha = alpha, alpha
	ip.gs.softMask = nil
	ip.gs.blend = BlendNormal
	if f.GetInt(st.Dict["PaintType"], 1) == 2 {
		base := saved.fill.cs.Base
		if base == nil {
			base = DeviceGray
		}
		ip.gs.fill.cs, ip.gs.strokeColor.cs = base, base
		ip.gs.fill.value = append([]float32(nil), saved.fill.value...)
		ip.gs.strokeColor.value = append([]float32(nil), saved.fill.value...)
	}
	ip.pushResources(f.GetDict(st.Dict["Resources"]))

	x0, y0, x1, y1 := 0, 0, 1, 1
	if !cached {
		x0, y0, x1, y1 = tileRange(area, view, xstep, ystep)
	}
	var cell raster.Path
	cell.Rect(view.X0, view.Y0, view.X1-view.X0, view.Y1-view.Y0)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			ip.gs.ctm = raster.Concat(raster.Translate(float32(x)*xstep, float32(y)*ystep), ptm)
			ip.base = ip.gs.ctm
			ip.gs.clipDepth = 0
			ip.dev.ClipPath(&cell, false, ip.gs.ctm, ip.scissor)
			ip.run(data)
			ip.flushText()
			ip.unwind(savedStack)
			ip.dev.PopClip()
		}
	}

	ip.popResources()
	ip.depth--
	ip.gs = saved
	ip.base = savedBase

	ip.dev.EndTile()
}

// tileRange returns the range of tile positions that cover area.
func tileRange(area, view raster.Rect, xstep, ystep float32) (x0, y0, x1, y1 int) {
	fx0 := (area.X0 - view.X0) / xstep
	fy0 := (area.Y0 - view.Y0) / ystep
	fx1 := (area.X1 - view.X0) / xstep
	fy1 := (area.Y1 - view.Y0) / ystep
	if fx0 > fx1 {
		fx0, fx1 = fx1, fx0
	}
	if fy0 > fy1 {
		fy0, fy1 = fy1, fy0
	}
	x0, y0 = int(floor32(fx0+0.001)), int(floor32(fy0+0.001))
	x1, y1 = int(ceil32(fx1-0.001)), int(ceil32(fy1-0.001))
	if fx1 > fx0 && x1 == x0 {
		x1 = x0 + 1
	}
	if fy1 > fy0 && y1 == y0 {
		y1 = y0 + 1
	}
	if n := (x1 - x0) * (y1 - y0); n > maxTiles || n < 0 {
		x1 = x0 + 1
		y1 = y0 + 1
	}
	return x0, y0, x1, y1
}

func floor32(v float32) float32 {
	i := float32(int(v))
	if v < 0 && i != v {
		i--
	}
	return i
}

func ceil32(v float32) float32 {
	i := float32(int(v))
	if v > 0 && i != v {
		i++
	}
	return i
}
