// Package gfx is the graphics model a document is drawn through: the Device
// interface, the devices behind it, color spaces, and the text, image and
// shading a device is handed.
//
// It knows no file format. The PDF interpreter drives a Device and so does an
// HTML layout engine, and both reach the same renderer, the same text
// extraction and the same bounding box through it. What a device is given is
// already resolved - a path in user space, a run of glyphs with their
// transforms, an image that decodes on demand - so nothing downstream has to
// know where it came from.
//
// DrawDevice renders into a raster.Pixmap, BBoxDevice measures, and
// ListDevice records what was drawn so that it can be drawn again, which is
// how one page is rasterized in bands by several goroutines. Embed BaseDevice
// to write another.
package gfx
