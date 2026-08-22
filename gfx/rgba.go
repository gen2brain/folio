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
			model.ToRGB(rgb, row)
			for x := 0; x < px.W; x++ {
				out[x*4], out[x*4+1], out[x*4+2] = rgb[x*3], rgb[x*3+1], rgb[x*3+2]
				out[x*4+3] = 255
			}
			continue
		}
		if px.N == 3 {
			for x := 0; x < px.W; x++ {
				copy(out[x*4:x*4+4], row[x*n:x*n+4])
			}
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
	return img
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
