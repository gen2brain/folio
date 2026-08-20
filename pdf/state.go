package pdf

import "github.com/gen2brain/pdf/raster"

// color is one of the two colors in the graphics state.
type color struct {
	cs      *ColorSpace
	value   []float32
	pattern Object // the pattern object named by scn, when cs is a Pattern
	// patternCTM is the transform that was in force where the pattern was
	// selected. A pattern is laid out in that space, not in the space of the
	// path it fills.
	patternCTM raster.Matrix
}

func (c *color) set(cs *ColorSpace) {
	c.cs = cs
	c.value = cs.Initial()
	c.pattern = nil
}

func (c color) clone() color {
	c.value = append([]float32(nil), c.value...)
	return c
}

// textState is everything the Tf, Tc, Tw, Tz, TL, Ts and Tr operators set.
// It survives BT and ET, which reset only the two text matrices.
type textState struct {
	font      *Font
	size      float32
	charSpace float32
	wordSpace float32
	hscale    float32 // Tz, already divided by 100
	leading   float32
	rise      float32
	render    int
	knockout  bool
}

// gstate is the graphics state that q and Q save and restore.
type gstate struct {
	ctm    raster.Matrix
	stroke raster.Stroke

	fill        color
	strokeColor color
	fillAlpha   float32
	strokeAlpha float32

	blend    BlendMode
	softMask *softMask

	params ColorParams
	text   textState

	// clipDepth counts the clips pushed since the enclosing q, so that Q can
	// pop exactly as many.
	clipDepth int
}

// softMask is an ExtGState /SMask, resolved where it was set.
type softMask struct {
	form       *Stream
	luminosity bool
	cs         *ColorSpace
	backdrop   []float32
	transfer   *Function
	// ctm and res are the transform and resources in force where the mask was
	// set: the mask is rendered in that space, not in the space of what it
	// masks.
	ctm raster.Matrix
	res Dict
}

func newGState(ctm raster.Matrix) gstate {
	g := gstate{
		ctm:         ctm,
		stroke:      raster.DefaultStroke(),
		fillAlpha:   1,
		strokeAlpha: 1,
		params:      DefaultColorParams,
		text:        textState{hscale: 1},
	}
	g.fill.set(DeviceGray)
	g.strokeColor.set(DeviceGray)
	return g
}

func (g gstate) clone() gstate {
	g.fill = g.fill.clone()
	g.strokeColor = g.strokeColor.clone()
	g.stroke.Dash = append([]float32(nil), g.stroke.Dash...)
	return g
}

// extGState applies the gs operator.
func (ip *interp) extGState(n Name) {
	f := ip.doc.f
	g := f.GetDict(f.Lookup(f.GetDict(ip.res["ExtGState"]), n))
	if g == nil {
		ip.errorf("ExtGState /%s is not in the resources", n)
		return
	}
	for _, k := range g.Keys() {
		v := f.Resolve(g[k])
		switch k {
		case "LW":
			ip.gs.stroke.Width = float32(f.GetFloat(v, 1))
		case "LC":
			ip.gs.stroke.SetCaps(raster.Cap(clampInt(int(f.GetInt(v, 0)), 0, 2)))
		case "LJ":
			ip.gs.stroke.Join = raster.Join(clampInt(int(f.GetInt(v, 0)), 0, 2))
		case "ML":
			ip.gs.stroke.MiterLimit = float32(f.GetFloat(v, 10))
		case "D":
			if a, ok := v.(Array); ok && len(a) == 2 {
				ip.gs.stroke.Dash = ip.gs.stroke.Dash[:0]
				for _, e := range f.GetArray(a[0]) {
					ip.gs.stroke.Dash = append(ip.gs.stroke.Dash, float32(f.GetFloat(e, 0)))
				}
				ip.gs.stroke.DashPhase = float32(f.GetFloat(a[1], 0))
			}
		case "CA":
			ip.gs.strokeAlpha = clamp01(float32(f.GetFloat(v, 1)))
		case "ca":
			ip.gs.fillAlpha = clamp01(float32(f.GetFloat(v, 1)))
		case "BM":
			mode := Name("")
			switch bm := v.(type) {
			case Name:
				mode = bm
			case Array:
				for _, e := range bm {
					if m, ok := blendMode(f.GetName(e)); ok {
						ip.gs.blend = m
						mode = ""
						break
					}
					mode = f.GetName(e)
				}
			}
			if mode != "" {
				if m, ok := blendMode(mode); ok {
					ip.gs.blend = m
				} else {
					ip.errorf("unknown blend mode /%s", mode)
					ip.gs.blend = BlendNormal
				}
			}
		case "SMask":
			ip.setSoftMask(v)
		case "Font":
			if a, ok := v.(Array); ok && len(a) == 2 {
				if ft := ip.doc.font(a[0]); ft != nil {
					ip.gs.text.font = ft
					ip.gs.text.size = float32(f.GetFloat(a[1], 0))
				}
			}
		case "TK":
			ip.gs.text.knockout = f.GetBool(v, true)
		case "OP":
			ip.gs.params.OverprintStroke = f.GetBool(v, false)
			if _, has := g["op"]; !has {
				ip.gs.params.Overprint = ip.gs.params.OverprintStroke
			}
		case "op":
			ip.gs.params.Overprint = f.GetBool(v, false)
		case "OPM":
			ip.gs.params.OverprintMode = int(f.GetInt(v, 0))
		case "RI":
			ip.gs.params.Intent = intentValue(f.GetName(v))
		}
	}
}

// setSoftMask resolves an ExtGState /SMask entry.
func (ip *interp) setSoftMask(v Object) {
	f := ip.doc.f
	dict, ok := v.(Dict)
	if !ok {
		ip.gs.softMask = nil
		return
	}
	form := f.GetStream(dict["G"])
	if form == nil {
		ip.errorf("soft mask has no group")
		ip.gs.softMask = nil
		return
	}
	sm := &softMask{
		form:       form,
		luminosity: f.GetName(dict["S"]) == "Luminosity",
		ctm:        ip.gs.ctm,
		res:        ip.res,
	}
	if grp := f.GetDict(form.Dict["Group"]); grp != nil && grp["CS"] != nil {
		sm.cs = ip.doc.colorSpace(grp["CS"], ip.res, 0)
	}
	if bc := f.GetFloats(dict["BC"]); len(bc) > 0 {
		sm.backdrop = make([]float32, len(bc))
		for i, x := range bc {
			sm.backdrop[i] = float32(x)
		}
	}
	if tr := dict["TR"]; tr != nil && f.GetName(tr) != "Identity" {
		sm.transfer = ip.doc.function(tr)
	}
	ip.gs.softMask = sm
}
