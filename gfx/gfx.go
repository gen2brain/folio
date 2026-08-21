// Package gfx is the graphics model a document is drawn through: the Device
// interface, the devices behind it, color spaces, and the text, image and
// shading a device is handed.
//
// It knows no file format. A PDF interpreter drives a Device and so does an
// HTML layout engine, and both reach the same renderer through it. What a
// device is given is already resolved: a path in user space, a run of glyphs
// with their transforms, an image that decodes on demand.
//
// DrawDevice renders into a raster.Pixmap, TextDevice extracts, SVGDevice
// writes SVG, BBoxDevice measures, and ListDevice records what was drawn so
// that it can be drawn again. Embed BaseDevice to write another.
package gfx
