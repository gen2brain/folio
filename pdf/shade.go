package pdf

import "github.com/gen2brain/pdf/raster"

// Shade is a shading dictionary: the sh operator paints one directly, and a
// shading pattern paints one through a path.
type Shade struct {
	Type int // 1 function, 2 axial, 3 radial, 4-7 mesh
	CS   *ColorSpace
	// Matrix is the pattern matrix when the shading came from a pattern, and
	// the identity when it came from sh.
	Matrix raster.Matrix
	// BBox is the shading's own bounding box in its own space, empty when it
	// has none.
	BBox       raster.Rect
	Background []float64
	Coords     []float64
	Domain     []float64
	Extend     [2]bool
	Function   []*Function

	// FuncMatrix, XDivs and YDivs describe a type 1 shading, whose function
	// is sampled over a rectangle of its own space.
	FuncMatrix   raster.Matrix
	XDivs, YDivs int

	dict Dict
}

// Coord6 returns the /Coords of an axial or radial shading, padded to the six
// numbers those two types use.
func (s *Shade) Coord6() [6]float32 {
	var out [6]float32
	if s.Type == 2 {
		for i := 0; i < 4 && i < len(s.Coords); i++ {
			j := i
			if i >= 2 {
				j = i + 1
			}
			out[j] = float32(s.Coords[i])
		}
		return out
	}
	for i := 0; i < 6 && i < len(s.Coords); i++ {
		out[i] = float32(s.Coords[i])
	}
	return out
}

// Domain4 returns the /Domain of a type 1 shading, which defaults to the unit
// square.
func (s *Shade) Domain4() [4]float32 {
	out := [4]float32{0, 1, 0, 1}
	for i := 0; i < 4 && i < len(s.Domain); i++ {
		out[i] = float32(s.Domain[i])
	}
	return out
}

// shade reads a shading dictionary.
func (d *Document) shade(obj Object, res Dict) *Shade {
	dict := d.f.GetDict(obj)
	if dict == nil {
		return nil
	}
	f := d.f
	sh := &Shade{
		Type:       int(f.GetInt(dict["ShadingType"], 0)),
		Matrix:     raster.Identity,
		Background: f.GetFloats(dict["Background"]),
		Coords:     f.GetFloats(dict["Coords"]),
		Domain:     f.GetFloats(dict["Domain"]),
		BBox:       raster.EmptyRect,
		dict:       dict,
	}
	if cs := dict["ColorSpace"]; cs != nil {
		sh.CS = d.colorSpace(cs, res, 0)
	} else {
		sh.CS = DeviceRGB
	}
	if b := f.GetFloats(dict["BBox"]); len(b) == 4 {
		sh.BBox = raster.Rect{X0: float32(b[0]), Y0: float32(b[1]), X1: float32(b[2]), Y1: float32(b[3])}.Normalized()
	}
	if e := f.GetArray(dict["Extend"]); len(e) == 2 {
		sh.Extend[0] = f.GetBool(e[0], false)
		sh.Extend[1] = f.GetBool(e[1], false)
	}
	if sh.Type == 1 {
		sh.FuncMatrix = raster.Identity
		if m := f.GetFloats(dict["Matrix"]); len(m) == 6 {
			sh.FuncMatrix = raster.Matrix{
				A: float32(m[0]), B: float32(m[1]), C: float32(m[2]),
				D: float32(m[3]), E: float32(m[4]), F: float32(m[5]),
			}
		}
		sh.XDivs, sh.YDivs = 32, 32
	}
	switch fn := f.Resolve(dict["Function"]).(type) {
	case Array:
		for _, v := range fn {
			sh.Function = append(sh.Function, d.function(v))
		}
	case nil:
	default:
		sh.Function = append(sh.Function, d.function(dict["Function"]))
	}
	if sh.Type < 1 || sh.Type > 7 {
		d.errorf("shading type %d", sh.Type)
		return nil
	}
	return sh
}

// Dict returns the shading dictionary.
func (s *Shade) Dict() Dict { return s.dict }
