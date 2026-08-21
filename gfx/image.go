package gfx

import "github.com/gen2brain/pdf/raster"

// Image is a picture a device draws, decoded only when it asks.
type Image interface {
	// Size is the image's own size in pixels.
	Size() (w, h int)
	// ColorSpace is the space the samples are in, and nil for a stencil.
	ColorSpace() *ColorSpace
	// Stencil reports a one bit mask, which paints the fill color through it.
	Stencil() bool
	// Smooth reports that the image asks to be interpolated.
	Smooth() bool
	// Pixels decodes the image into cs, halved shrink times. A nil cs asks
	// for the coverage of a stencil, one byte a pixel.
	Pixels(cs *ColorSpace, shrink int) (*raster.Pixmap, error)
}
