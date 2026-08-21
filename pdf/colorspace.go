package pdf

import (
	"github.com/gen2brain/folio/gfx"
	"github.com/gen2brain/folio/syntax"
)

// colorSpace resolves a color space object, which is either a name, an array,
// or a name that has to be looked up in the resource dictionary.
func (d *Document) colorSpace(obj Object, res Dict, depth int) *ColorSpace {
	if depth > maxNesting {
		d.errorf("color space nested too deeply")
		return DeviceGray
	}
	switch v := d.f.Resolve(obj).(type) {
	case Name:
		switch v {
		case "DeviceGray", "G", "CalGray":
			return DeviceGray
		case "DeviceRGB", "RGB", "CalRGB":
			return DeviceRGB
		case "DeviceCMYK", "CMYK":
			return DeviceCMYK
		case "Pattern":
			return patternCS
		case "Indexed", "I":
			return DeviceGray
		}
		if sub := d.f.Lookup(d.f.GetDict(res["ColorSpace"]), v); sub != nil {
			return d.colorSpace(sub, nil, depth+1)
		}
		d.errorf("unknown color space /%s", v)
		return DeviceGray

	case Array:
		if len(v) == 0 {
			return DeviceGray
		}
		if len(v) == 1 {
			return d.colorSpace(v[0], res, depth+1)
		}
		return d.colorSpaceArray(v, res, depth)
	}
	return DeviceGray
}

func (d *Document) colorSpaceArray(a Array, res Dict, depth int) *ColorSpace {
	switch d.f.GetName(a[0]) {
	case "ICCBased":
		st := d.f.GetStream(a[1])
		if st == nil {
			return DeviceGray
		}
		n := int(d.f.GetInt(st.Dict["N"], 0))
		if p := d.iccProfile(st, n); p != nil {
			kind := KindRGB
			if n == 1 {
				kind = KindGray
			}
			return &ColorSpace{Name: "ICCBased", Kind: kind, N: n, ICC: p}
		}
		if alt := st.Dict["Alternate"]; alt != nil {
			if cs := d.colorSpace(alt, res, depth+1); cs.N == n {
				return cs
			}
		}
		switch n {
		case 1:
			return DeviceGray
		case 4:
			return DeviceCMYK
		default:
			return DeviceRGB
		}

	case "CalGray":
		cs := &ColorSpace{Name: "CalGray", Kind: KindCalGray, N: 1}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
			if g := d.f.GetFloat(p["Gamma"], 0); g > 0 {
				cs.Gamma = []float64{g}
			}
		}
		return cs
	case "CalRGB":
		cs := &ColorSpace{Name: "CalRGB", Kind: KindCalRGB, N: 3}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
			cs.Gamma = d.f.GetFloats(p["Gamma"])
			cs.Matrix = d.f.GetFloats(p["Matrix"])
		}
		return cs

	case "Lab":
		cs := &ColorSpace{Name: "Lab", Kind: KindLab, N: 3}
		if p := d.f.GetDict(a[1]); p != nil {
			cs.Range = d.f.GetFloats(p["Range"])
			cs.WhitePoint = d.f.GetFloats(p["WhitePoint"])
		}
		return cs

	case "Indexed", "I":
		if len(a) < 4 {
			return DeviceGray
		}
		base := d.colorSpace(a[1], res, depth+1)
		cs := &ColorSpace{
			Name:  "Indexed",
			Kind:  KindIndexed,
			N:     1,
			Base:  base,
			HiVal: int(d.f.GetInt(a[2], 0)),
		}
		switch v := d.f.Resolve(a[3]).(type) {
		case String:
			cs.Lookup = []byte(v)
		case *Stream:
			if b, err := v.Data(); err == nil {
				cs.Lookup = b
			} else {
				d.errorf("indexed palette: %v", err)
			}
		}
		return cs

	case "Separation":
		if len(a) < 3 {
			return DeviceGray
		}
		cs := &ColorSpace{
			Name:      "Separation",
			Kind:      KindSeparation,
			N:         1,
			Colorants: []string{string(d.f.GetName(a[1]))},
			Alternate: d.colorSpace(a[2], res, depth+1),
		}
		if len(a) > 3 {
			cs.Tint = d.function(a[3])
		}
		if cs.Colorants[0] == "None" {
			cs.Name = "Separation(None)"
		}
		return cs

	case "DeviceN":
		if len(a) < 3 {
			return DeviceGray
		}
		names := d.f.GetArray(a[1])
		cs := &ColorSpace{
			Name:      "DeviceN",
			Kind:      KindDeviceN,
			N:         max(len(names), 1),
			Alternate: d.colorSpace(a[2], res, depth+1),
		}
		for _, n := range names {
			cs.Colorants = append(cs.Colorants, string(d.f.GetName(n)))
		}
		if len(a) > 3 {
			cs.Tint = d.function(a[3])
		}
		return cs

	case "Pattern":
		cs := &ColorSpace{Name: "Pattern", Kind: KindPattern, N: 1}
		if len(a) > 1 {
			cs.Base = d.colorSpace(a[1], res, depth+1)
			cs.N = cs.Base.N
		}
		return cs

	case "DeviceGray", "G":
		return DeviceGray
	case "DeviceRGB", "RGB":
		return DeviceRGB
	case "DeviceCMYK", "CMYK":
		return DeviceCMYK
	}

	d.errorf("unknown color space %v", syntax.Name(d.f.GetName(a[0])))
	return DeviceGray
}

// iccProfile reads the profile an ICCBased space embeds, which says what its
// numbers mean where the alternate space only says how many there are. It is
// nil for a profile shaped in a way this cannot read, for one that says no
// more than sRGB already does, and for one that disagrees with /N.
func (d *Document) iccProfile(st *syntax.Stream, n int) *gfx.ICC {
	if n != 1 && n != 3 {
		return nil
	}
	data, err := st.Data()
	if err != nil {
		return nil
	}
	p := gfx.ParseICC(data)
	if p == nil || p.Components() != n {
		return nil
	}
	return p
}
