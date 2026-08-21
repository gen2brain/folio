package gfx

import "github.com/gen2brain/folio/raster"

// Shade is a smooth gradation of color filling an area: a PDF shading, or a
// CSS gradient.
type Shade interface {
	// Transform maps the shading's own space into the space ctm starts from.
	Transform() raster.Matrix
	// Bounds is the shading's own bounding box in its own space, and empty
	// when it has none.
	Bounds() raster.Rect
	// ColorSpace is the space the shading's colors are in.
	ColorSpace() *ColorSpace
	// Shader returns what paints the shading into a destination of the given
	// model, under ctm and over box, and nil when nothing is painted.
	Shader(model raster.Model, ctm raster.Matrix, box raster.Rect) raster.Shader
}
