package pdf

import (
	"unicode/utf16"
	"unicode/utf8"
)

// beginMarkedContent handles BMC and BDC. A layer is opened only for optional
// content that names a group.
func (ip *interp) beginMarkedContent(tag Name, prop Object) {
	ip.flushText()
	mc := markedContent{clip: ip.openClips()}

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
	case tag == "Layer":
		mc.layers = ip.beginLayer(prop)
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

// beginLayer opens a layer for the /Layer tag, which is not in the standard
// and which Illustrator writes with the layer's name in /Title.
func (ip *interp) beginLayer(obj Object) int {
	dict := ip.doc.f.GetDict(obj)
	if dict == nil {
		return 0
	}
	title, ok := ip.doc.f.Resolve(dict["Title"]).(String)
	if !ok {
		return 0
	}
	ip.dev.BeginLayer(decodeTextString(title))
	return 1
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
	if mc.layers == 0 {
		return
	}
	if ip.openClips() > mc.clip {
		ip.pending = append(ip.pending, pendingLayer{layers: mc.layers, clip: mc.clip})
		return
	}
	for i := 0; i < mc.layers; i++ {
		ip.dev.EndLayer()
	}
}

// decodeTextString reads a PDF text string: UTF-16 or UTF-8 when a byte order
// mark says so, UTF-8 when the bytes are valid UTF-8 anyway, and
// PDFDocEncoding otherwise.
func decodeTextString(s String) string {
	switch {
	case len(s) >= 2 && s[0] == 0xfe && s[1] == 0xff:
		return utf16String(s[2:], true)
	case len(s) >= 2 && s[0] == 0xff && s[1] == 0xfe:
		return utf16String(s[2:], false)
	case len(s) >= 3 && s[0] == 0xef && s[1] == 0xbb && s[2] == 0xbf:
		return string(s[3:])
	case utf8.Valid(s):
		return string(s)
	}
	out := make([]rune, len(s))
	for i, c := range s {
		out[i] = pdfDocRune(c)
	}
	return string(out)
}

func utf16String(s String, big bool) string {
	out := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		if big {
			out = append(out, uint16(s[i])<<8|uint16(s[i+1]))
		} else {
			out = append(out, uint16(s[i+1])<<8|uint16(s[i]))
		}
	}
	return string(utf16.Decode(out))
}

// pdfDocEncoding holds the code points PDFDocEncoding gives to 0x18 through
// 0x1f and 0x80 through 0xa0, which are the only ones it does not share with
// Latin-1. ISO 32000-1 annex D.2.
var pdfDocEncoding = [...]rune{
	0x02d8, 0x02c7, 0x02c6, 0x02d9, 0x02dd, 0x02db, 0x02da, 0x02dc,
	0x2022, 0x2020, 0x2021, 0x2026, 0x2014, 0x2013, 0x0192, 0x2044,
	0x2039, 0x203a, 0x2212, 0x2030, 0x201e, 0x201c, 0x201d, 0x2018,
	0x2019, 0x201a, 0x2122, 0xfb01, 0xfb02, 0x0141, 0x0152, 0x0160,
	0x0178, 0x017d, 0x0131, 0x0142, 0x0153, 0x0161, 0x017e, 0xfffd,
	0x20ac,
}

func pdfDocRune(c byte) rune {
	switch {
	case c >= 0x18 && c <= 0x1f:
		return pdfDocEncoding[c-0x18]
	case c >= 0x80 && c <= 0xa0:
		return pdfDocEncoding[c-0x80+8]
	case c == 0xad:
		return 0xfffd
	}
	return rune(c)
}
