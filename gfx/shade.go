package gfx

import "github.com/gen2brain/folio/raster"

// Shade is a smooth gradation of color over an area, a shading or a gradient.
type Shade interface {
	// Transform maps the shading's own space into the space ctm starts from.
	Transform() raster.Matrix
	// Bounds is the shading's bounding box in its own space, empty for none.
	Bounds() raster.Rect
	// ColorSpace is the space the shading's colors are in.
	ColorSpace() *ColorSpace
	// Shader returns what paints the shading into a destination of the given
	// model, under ctm and over box, and nil when nothing is painted.
	Shader(model raster.Model, ctm raster.Matrix, box raster.Rect) raster.Shader
}
