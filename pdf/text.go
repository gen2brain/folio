package pdf

import "github.com/gen2brain/folio/raster"

// showObject shows a string, or the mix of strings and numbers a TJ array
// holds.
func (ip *interp) showObject(obj Object) {
	switch v := ip.doc.f.Resolve(obj).(type) {
	case String:
		ip.showString([]byte(v))
	case Array:
		for _, e := range v {
			switch item := ip.doc.f.Resolve(e).(type) {
			case String:
				ip.showString([]byte(item))
			case Integer:
				ip.adjustText(float32(item))
			case Real:
				ip.adjustText(float32(item))
			}
		}
	}
}

// adjustText applies a TJ number, which moves the pen by a thousandth of the
// font size against the writing direction.
func (ip *interp) adjustText(v float32) {
	ts := &ip.gs.text
	d := -v * 0.001 * ts.size
	if ts.font != nil && ts.font.WMode == 1 {
		ip.tm = raster.Concat(raster.Translate(0, d), ip.tm)
	} else {
		ip.tm = raster.Concat(raster.Translate(d*ts.hscale, 0), ip.tm)
	}
}

// showString places the glyphs of one string.
func (ip *interp) showString(s []byte) {
	ts := &ip.gs.text
	if ts.font == nil {
		ip.errorf("text shown with no font selected")
		ts.font = ip.doc.fallbackFont()
	}
	if !ip.inText {
		ip.errorf("text shown outside a text object")
	}
	font := ts.font

	// The characters are read here and nowhere else, but a Type3 glyph runs a
	// content stream of its own and can reach this again, so the buffer is on
	// the stack rather than on the interpreter.
	var buf [64]Char
	for _, c := range font.decode(buf[:0], s) {
		if ip.text != nil && ip.textMode != ts.render {
			ip.flushText()
		}
		if ip.text == nil {
			ip.text = &Text{}
			ip.textMode = ts.render
			ip.textCTM = ip.gs.ctm
		}

		tsm := raster.Matrix{A: ts.size * ts.hscale, D: ts.size, F: ts.rise}
		trm := raster.Concat(tsm, ip.tm)

		if proc := ip.type3Direct(font, c.Code); proc != nil {
			ip.flushText()
			ip.runType3Glyph(font, proc, trm)
			ip.text = &Text{}
			ip.textMode = 3
			ip.textCTM = ip.gs.ctm
		}

		ip.addGlyph(font, trm, c)

		if s := font.Text(c); len([]rune(s)) > 1 {
			for _, extra := range []rune(s)[1:] {
				ip.addFiller(font, trm, extra)
			}
		}

		if font.WMode == 1 {
			ty := c.Width*ts.size + ts.charSpace
			if c.Space {
				ty += ts.wordSpace
			}
			ip.tm = raster.Concat(raster.Translate(0, ty), ip.tm)
		} else {
			tx := c.Width*ts.size + ts.charSpace
			if c.Space {
				tx += ts.wordSpace
			}
			ip.tm = raster.Concat(raster.Translate(tx*ts.hscale, 0), ip.tm)
		}
	}
}

// addGlyph appends one glyph to the text being built, starting a new span
// when the font or the shape of the matrix changes.
func (ip *interp) addGlyph(font *Font, trm raster.Matrix, c Char) {
	t := ip.text
	n := len(t.Spans)
	if n == 0 {
		t.Spans = append(t.Spans, TextSpan{Font: font, WMode: font.WMode, Trm: trm})
		n = 1
	} else {
		sp := &t.Spans[n-1]
		if sp.Font != font || sp.WMode != font.WMode ||
			sp.Trm.A != trm.A || sp.Trm.B != trm.B || sp.Trm.C != trm.C || sp.Trm.D != trm.D {
			t.Spans = append(t.Spans, TextSpan{Font: font, WMode: font.WMode, Trm: trm})
			n++
		}
	}
	gid := font.Glyph(c)
	r := font.Rune(c)
	sp := &t.Spans[n-1]
	sp.Items = append(sp.Items, TextItem{
		X: trm.E, Y: trm.F,
		GID:  gid,
		Rune: r,
		Name: font.GlyphNameOf(c),
		Code: c.Code,
		CID:  c.CID,
		Adv:  c.Width,
	})
}

// type3Direct returns the glyph procedure to run in place, or nil when the
// glyph is a mask that a font engine would cache and stamp.
func (ip *interp) type3Direct(font *Font, code uint32) *Stream {
	if !font.Type3 || ip.gs.text.render == 3 || ip.gs.text.render == 7 || ip.depth >= maxNesting {
		return nil
	}
	name := font.GlyphName(code)
	if name == "" {
		return nil
	}
	proc := ip.doc.f.GetStream(ip.doc.f.Lookup(font.CharProcs, name))
	if proc == nil {
		return nil
	}
	if !font.uncacheable(name, proc) {
		return nil
	}
	return proc
}

// runType3Glyph interprets one glyph procedure.
func (ip *interp) runType3Glyph(font *Font, proc *Stream, trm raster.Matrix) {
	data, err := proc.Data()
	if err != nil {
		ip.errorf("Type3 glyph: %v", err)
		return
	}
	res := font.Resources
	if res == nil {
		res = ip.res
	}

	saved := ip.gs
	ip.gs = ip.gs.clone()
	savedBase := ip.base
	savedStack := len(ip.gstack)
	savedText := ip.inText
	savedTm, savedTlm := ip.tm, ip.tlm
	ip.depth++

	ip.gs.ctm = raster.Concat(font.FontMatrix, raster.Concat(trm, ip.gs.ctm))
	ip.base = ip.gs.ctm
	ip.gs.clipDepth = 0
	ip.gs.softMask = nil
	ip.inText = false
	ip.pushResources(res)
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
	ip.depth--
	ip.gs = saved
	ip.base = savedBase
	ip.inText = savedText
	ip.tm, ip.tlm = savedTm, savedTlm
}

// addFiller appends a glyphless item carrying one more character.
func (ip *interp) addFiller(font *Font, trm raster.Matrix, r rune) {
	t := ip.text
	if len(t.Spans) == 0 {
		return
	}
	sp := &t.Spans[len(t.Spans)-1]
	sp.Items = append(sp.Items, TextItem{
		X: trm.E, Y: trm.F,
		GID:  -1,
		Rune: r,
	})
}

// flushText hands the accumulated text to the device.
func (ip *interp) flushText() {
	t := ip.text
	if t == nil {
		return
	}
	ip.text = nil
	if len(t.Spans) == 0 {
		return
	}

	if ip.hidden > 0 {
		return
	}
	mode := ip.textMode
	ctm := ip.textCTM
	fill := mode == 0 || mode == 2 || mode == 4 || mode == 6
	stroke := mode == 1 || mode == 2 || mode == 5 || mode == 6
	clip := mode >= 4

	var d drawState
	if fill || stroke {
		d = ip.beginDraw(t.Bounds(ctm))
	}
	if fill {
		if ip.gs.fill.cs.IsPattern() {
			ip.textWithPattern(t, ctm, false)
		} else {
			ip.dev.FillText(t, ctm, ip.gs.fill.cs, ip.gs.fill.value, ip.gs.fillAlpha, ip.gs.params)
		}
	}
	if stroke {
		if ip.gs.strokeColor.cs.IsPattern() {
			ip.textWithPattern(t, ctm, true)
		} else {
			ip.dev.StrokeText(t, &ip.gs.stroke, ctm, ip.gs.strokeColor.cs, ip.gs.strokeColor.value, ip.gs.strokeAlpha, ip.gs.params)
		}
	}
	if !fill && !stroke && !clip {
		ip.dev.IgnoreText(t, ctm)
	}
	if fill || stroke {
		ip.endDraw(d)
	}

	if clip {
		ip.dev.ClipText(t, ctm, ip.scissor)
		ip.gs.clipDepth++
	}
}
