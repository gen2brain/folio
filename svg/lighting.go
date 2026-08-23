package svg

import (
	"math"

	"github.com/gen2brain/folio/raster"
)

// light is what one of the three light elements resolves to, in samples.
type light struct {
	kind               string
	azimuth, elevation float32
	x, y, z            float32
	atX, atY, atZ      float32
	exponent, cone     float32
	limited            bool
}

// vec3 is the surface normal and the direction of the light at a sample.
type vec3 struct{ x, y, z float32 }

func (v vec3) dot(o vec3) float32 { return v.x*o.x + v.y*o.y + v.z*o.z }

func (v vec3) length() float32 {
	return float32(math.Sqrt(float64(v.x*v.x) + float64(v.y*v.y) + float64(v.z*v.z)))
}

func (v vec3) unit() vec3 {
	l := v.length()
	if l == 0 {
		return v
	}
	return vec3{v.x / l, v.y / l, v.z / l}
}

// lightOf reads the one light element a lighting primitive carries.
func (p *pipe) lightOf(k *node) *light {
	for _, c := range k.kids {
		l := &light{kind: c.name}
		switch c.name {
		case "feDistantLight":
			l.azimuth = number(c.attr["azimuth"], 0)
			l.elevation = number(c.attr["elevation"], 0)
		case "fePointLight", "feSpotLight":
			l.x, l.y = number(c.attr["x"], 0), number(c.attr["y"], 0)
			l.z = number(c.attr["z"], 0)
			l.atX, l.atY = number(c.attr["pointsAtX"], 0), number(c.attr["pointsAtY"], 0)
			l.atZ = number(c.attr["pointsAtZ"], 0)
			if l.exponent = number(c.attr["specularExponent"], 1); l.exponent <= 0 {
				l.exponent = 1
			}
			if v, ok := c.attr["limitingConeAngle"]; ok {
				l.cone, l.limited = number(v, 0), true
			}
			p.place(l)
		default:
			continue
		}
		return l
	}
	return nil
}

// place moves a light from user space into the samples of the region.
func (p *pipe) place(l *light) {
	at := p.ctm.Apply(raster.Point{X: l.x, Y: l.y})
	l.x, l.y = at.X-float32(p.src.X), at.Y-float32(p.src.Y)
	to := p.ctm.Apply(raster.Point{X: l.atX, Y: l.atY})
	l.atX, l.atY = to.X-float32(p.src.X), to.Y-float32(p.src.Y)
	// The third axis takes the length the other two stretch by.
	sz := float32(math.Sqrt(float64(p.sx*p.sx)+float64(p.sy*p.sy)) / math.Sqrt2)
	l.z *= sz
	l.atZ *= sz
}

func (p *pipe) lighting(k *node, lin bool, specular bool) *fimage {
	l := p.lightOf(k)
	out := p.blank(lin)
	if l == nil || out == nil {
		return out
	}
	in := p.input(k, "in")
	scale := number(k.attr["surfaceScale"], 1)
	col := [3]float32{1, 1, 1}
	if v, ok := parseColor(p.inherit(k, "lighting-color"), p.current(k)); ok {
		col = v.color
	}
	kd := number(k.attr["diffuseConstant"], 1)
	ks := number(k.attr["specularConstant"], 1)
	exp := number(k.attr["specularExponent"], 1)
	if specular && (exp < 1 || exp > 128) {
		return out
	}
	lightPixmap(out.px, in.px, l, scale, col, kd, ks, exp, specular)
	return out
}

// lightPixmap is SVG 1.1 15.20 and 15.22: the alpha channel of the input is a
// height field, and each sample is lit by the normal of that surface.
func lightPixmap(dst, src *raster.Pixmap, l *light, scale float32, col [3]float32,
	kd, ks, exp float32, specular bool) {

	w, h := src.W, src.H
	if w < 3 || h < 3 {
		return
	}
	alpha := func(x, y int) int32 { return int32(src.Samples[y*src.Stride+x*4+3]) }

	dir := vec3{1, 1, 1}
	if l.kind == "feDistantLight" {
		az := float64(l.azimuth) * math.Pi / 180
		el := float64(l.elevation) * math.Pi / 180
		dir = vec3{
			float32(math.Cos(az) * math.Cos(el)),
			float32(math.Sin(az) * math.Cos(el)),
			float32(math.Sin(el)),
		}
	}
	for y := range h {
		for x := range w {
			fx, fy, nx, ny := surfaceNormal(alpha, w, h, x, y)
			if l.kind != "feDistantLight" {
				nz := float32(alpha(x, y)) / 255 * scale
				dir = vec3{l.x - float32(x), l.y - float32(y), l.z - nz}.unit()
			}
			c := lightColor(l, col, dir)
			f := lightFactor(fx, fy, nx, ny, dir, scale, kd, ks, exp, specular)
			o := dst.Samples[y*dst.Stride+x*4:]
			for i := range 3 {
				o[i] = round8(c[i] * f)
			}
			if specular {
				o[3] = max(max(o[0], o[1]), o[2])
			} else {
				o[3] = 255
			}
		}
	}
}

func lightFactor(fx, fy float32, nx, ny int32, dir vec3, scale, kd, ks, exp float32, specular bool) float32 {
	var k float32
	flat := nx == 0 && ny == 0
	if !specular {
		if flat {
			k = dir.z
		} else {
			n := vec3{float32(-nx) * scale / 255 * fx, float32(-ny) * scale / 255 * fy, 1}
			k = n.dot(dir) / n.length()
		}
		return kd * k
	}
	half := vec3{dir.x, dir.y, dir.z + 1}
	hl := half.length()
	if hl == 0 {
		return 0
	}
	if flat {
		k = half.z / hl
	} else {
		n := vec3{float32(-nx) * scale / 255 * fx, float32(-ny) * scale / 255 * fy, 1}
		k = n.dot(half) / n.length() / hl
	}
	if exp != 1 {
		k = float32(math.Pow(float64(k), float64(exp)))
	}
	return ks * k
}

// lightColor is the colour reaching one sample; only a spot light narrows.
func lightColor(l *light, col [3]float32, dir vec3) [3]float32 {
	if l.kind != "feSpotLight" {
		return col
	}
	to := vec3{l.atX - l.x, l.atY - l.y, l.atZ - l.z}.unit()
	d := -dir.dot(to)
	if d <= 0 {
		return [3]float32{}
	}
	if l.limited && float64(d) < math.Cos(float64(l.cone)*math.Pi/180) {
		return [3]float32{}
	}
	f := float32(math.Pow(float64(d), float64(l.exponent)))
	return [3]float32{col[0] * f, col[1] * f, col[2] * f}
}

// surfaceNormal is the Sobel pair of SVG 1.1 15.20, which has nine forms: one
// for the middle and one for each edge and corner.
func surfaceNormal(a func(int, int) int32, w, h, x, y int) (float32, float32, int32, int32) {
	const (
		f12 = 1.0 / 2
		f13 = 1.0 / 3
		f14 = 1.0 / 4
		f23 = 2.0 / 3
	)
	left, right := x > 0, x < w-1
	top, bottom := y > 0, y < h-1
	switch {
	case !left && !top:
		return f23, f23,
			-2*a(0, 0) + 2*a(1, 0) - a(0, 1) + a(1, 1),
			-2*a(0, 0) - a(1, 0) + 2*a(0, 1) + a(1, 1)
	case !right && !top:
		return f23, f23,
			-2*a(w-2, 0) + 2*a(w-1, 0) - a(w-2, 1) + a(w-1, 1),
			-a(w-2, 0) - 2*a(w-1, 0) + a(w-2, 1) + 2*a(w-1, 1)
	case !left && !bottom:
		return f23, f23,
			-a(0, h-2) + a(1, h-2) - 2*a(0, h-1) + 2*a(1, h-1),
			-2*a(0, h-2) - a(1, h-2) + 2*a(0, h-1) + a(1, h-1)
	case !right && !bottom:
		return f23, f23,
			-a(w-2, h-2) + a(w-1, h-2) - 2*a(w-2, h-1) + 2*a(w-1, h-1),
			-a(w-2, h-2) - 2*a(w-1, h-2) + a(w-2, h-1) + 2*a(w-1, h-1)
	case !top:
		return f13, f12,
			-2*a(x-1, 0) + 2*a(x+1, 0) - a(x-1, 1) + a(x+1, 1),
			-a(x-1, 0) - 2*a(x, 0) - a(x+1, 0) + a(x-1, 1) + 2*a(x, 1) + a(x+1, 1)
	case !bottom:
		return f13, f12,
			-a(x-1, h-2) + a(x+1, h-2) - 2*a(x-1, h-1) + 2*a(x+1, h-1),
			-a(x-1, h-2) - 2*a(x, h-2) - a(x+1, h-2) + a(x-1, h-1) + 2*a(x, h-1) + a(x+1, h-1)
	case !left:
		return f12, f13,
			-a(0, y-1) + a(1, y-1) - 2*a(0, y) + 2*a(1, y) - a(0, y+1) + a(1, y+1),
			-2*a(0, y-1) - a(1, y-1) + 2*a(0, y+1) + a(1, y+1)
	case !right:
		return f12, f13,
			-a(w-2, y-1) + a(w-1, y-1) - 2*a(w-2, y) + 2*a(w-1, y) - a(w-2, y+1) + a(w-1, y+1),
			-a(w-2, y-1) - 2*a(w-1, y-1) + a(w-2, y+1) + 2*a(w-1, y+1)
	}
	return f14, f14,
		-a(x-1, y-1) + a(x+1, y-1) - 2*a(x-1, y) + 2*a(x+1, y) - a(x-1, y+1) + a(x+1, y+1),
		-a(x-1, y-1) - 2*a(x, y-1) - a(x+1, y-1) + a(x-1, y+1) + 2*a(x, y+1) + a(x+1, y+1)
}
