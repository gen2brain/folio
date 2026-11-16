package gfx

import "github.com/gen2brain/folio/raster"

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

// RowImage is an Image that can also hand out its pixmap a row at a time.
type RowImage interface {
	Image
	// PixelRows starts decoding the rows Pixels would build into cs, halved
	// shrink times, top first; false when the image cannot be decoded so.
	PixelRows(cs *ColorSpace, shrink int) (*PixelRows, bool)
}

// PixelRows is an image's pixmap handed out a row at a time.
type PixelRows struct {
	W, H  int
	Model raster.Model
	Alpha bool
	// Next returns the next row, valid until the next call, and false at the end.
	Next func() ([]uint8, bool)
	// Stop ends the decode.
	Stop func()
}
