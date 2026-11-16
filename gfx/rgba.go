package gfx

import (
	"image"

	"github.com/gen2brain/folio/raster"
)

// ToRGBA copies a pixmap into the image type the standard library draws with,
// converting whatever color space it was composited in. This is the boundary
// the conversion happens at, and it happens once.
func ToRGBA(px *raster.Pixmap) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, px.W, px.H))
	toRGBA(img, px)
	return img
}

// RGBABands adapts a callback taking RGBA bands to DrawBands, converting each
// band into one reused image whose Rect is the band's place in device space
// less origin.
func RGBABands(origin image.Point, fn func(*image.RGBA) error) func(*raster.Pixmap) error {
	img := &image.RGBA{}
	var pix []uint8
	return func(px *raster.Pixmap) error {
		n := px.W * px.H * 4
		if cap(pix) < n {
			pix = make([]uint8, n)
		}
		*img = image.RGBA{
			Pix:    pix[:n],
			Stride: px.W * 4,
			Rect:   image.Rect(px.X, px.Y, px.X+px.W, px.Y+px.H).Sub(origin),
		}
		toRGBA(img, px)
		return fn(img)
	}
}

func toRGBA(img *image.RGBA, px *raster.Pixmap) {
	model := px.Model
	if model == nil {
		model = raster.ModelRGB
	}
	n := px.Comps()
	rgb := make([]uint8, px.W*3)
	straight := make([]uint8, px.W*px.N)
	for y := 0; y < px.H; y++ {
		row := px.Row(y)
		out := img.Pix[y*img.Stride : y*img.Stride+px.W*4]
		if !px.Alpha {
			src := row
			if model != raster.ModelRGB || px.N != 3 {
				model.ToRGB(rgb, row)
				src = rgb
			}
			for x := 0; x < px.W; x++ {
				p := out[x*4 : x*4+4 : x*4+4]
				q := src[x*3 : x*3+3 : x*3+3]
				p[0], p[1], p[2], p[3] = q[0], q[1], q[2], 255
			}
			continue
		}
		if px.N == 3 {
			copy(out, row[:px.W*4])
			continue
		}
		for x := 0; x < px.W; x++ {
			a := row[x*n+px.N]
			for c := 0; c < px.N; c++ {
				straight[x*px.N+c] = unpremultiply(row[x*n+c], a)
			}
		}
		model.ToRGB(rgb, straight)
		for x := 0; x < px.W; x++ {
			a := row[x*n+px.N]
			out[x*4] = premultiply(rgb[x*3], a)
			out[x*4+1] = premultiply(rgb[x*3+1], a)
			out[x*4+2] = premultiply(rgb[x*3+2], a)
			out[x*4+3] = a
		}
	}
}

func unpremultiply(v, a uint8) uint8 {
	if a == 0 || a == 255 {
		return v
	}
	if r := uint32(v) * 255 / uint32(a); r < 255 {
		return uint8(r)
	}
	return 255
}

func premultiply(v, a uint8) uint8 {
	t := uint32(v)*uint32(a) + 128
	return uint8((t + t>>8) >> 8)
}
