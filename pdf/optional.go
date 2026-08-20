package pdf

// beginMarkedContent handles BMC and BDC. A layer is opened only for optional
// content that names a group.
func (ip *interp) beginMarkedContent(tag Name, prop Object) {
	ip.flushText()
	mc := markedContent{}

	switch {
	case ip.hidden > 0:
		ip.hidden++
		mc.hid = true
	case tag == "OC":
		if !ip.doc.optionalContentVisible(prop) {
			ip.hidden++
			mc.hid = true
		} else {
			mc.layers = ip.beginOC(prop, 0)
		}
	}
	ip.mc = append(ip.mc, mc)
}

// beginOC opens a layer for each named optional content group, following an
// OCMD into the groups it refers to.
func (ip *interp) beginOC(obj Object, depth int) int {
	if depth > maxNesting {
		return 0
	}
	dict := ip.doc.f.GetDict(obj)
	if dict == nil {
		return 0
	}
	if n := dict["Name"]; n != nil {
		name := ""
		switch v := ip.doc.f.Resolve(n).(type) {
		case Name:
			name = string(v)
		case String:
			name = decodeTextString(v)
		}
		ip.dev.BeginLayer(name)
		return 1
	}
	count := 0
	for _, g := range ip.doc.f.GetArray(dict["OCGs"]) {
		count += ip.beginOC(g, depth+1)
	}
	return count
}

func (ip *interp) endMarkedContent() {
	ip.flushText()
	n := len(ip.mc)
	if n == 0 {
		ip.errorf("EMC without BMC or BDC")
		return
	}
	mc := ip.mc[n-1]
	ip.mc = ip.mc[:n-1]
	if mc.hid && ip.hidden > 0 {
		ip.hidden--
	}
	for i := 0; i < mc.layers; i++ {
		ip.dev.EndLayer()
	}
}

// decodeTextString reads a PDF text string, which is either UTF-16BE with a
// byte order mark or PDFDocEncoding.
func decodeTextString(s String) string {
	if len(s) >= 2 && s[0] == 0xfe && s[1] == 0xff {
		out := make([]rune, 0, len(s)/2)
		for i := 2; i+1 < len(s); i += 2 {
			out = append(out, rune(uint16(s[i])<<8|uint16(s[i+1])))
		}
		return string(out)
	}
	out := make([]rune, len(s))
	for i, c := range s {
		out[i] = rune(c)
	}
	return string(out)
}
