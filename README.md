## pdf

[PDF](https://en.wikipedia.org/wiki/PDF) renderer in pure Go. No CGo, no dependencies.

Work in progress: everything but JBIG2 and JPEG 2000 renders.

The object layer is held to MuPDF, poppler and qpdf over 4382 files, the interpreter to
`mutool trace` call for call, and the font engine to `mutool draw -F svg` glyph for glyph. The
2D engine is byte-exact against AGG 2.3, and rendered pages are scored against MuPDF, poppler,
cairo and Ghostscript.

### Rendering

```go
doc, err := pdf.Open("file.pdf")
defer doc.Close()

p, err := doc.Page(0)
img, err := p.Image(150)
```

A page composites in the document's own color space and converts once, at the end:

```go
img, err := p.ImageOptions(150, &pdf.Options{ColorSpace: pdf.DeviceCMYK})
img, err := p.ImageOptions(150, &pdf.Options{Alpha: true})
px, err := p.Render(p.Matrix(150), nil)
```

`Options` also carries the pixel limit, strictness and flatness. `Page.Run` drives any device:
the renderer, a trace, a bounding box, or another.

### Packages

`raster` is the 2D engine and `font` reads font programs; neither knows anything about PDF.
`syntax` is the file format with no graphics in it. `pdf` is the interpreter, the devices and
the public API.

### Supported

Paths, strokes and dashes, clipping by path, by text and by stencil, text in embedded TrueType,
CFF, Type1 and CID keyed fonts, the fourteen standard substitutes, every color space and
function type, images at every bit depth with soft masks and color keys, JPEG and CCITT Group 3
and 4, all seven shading types, tiling and shading patterns, transparency groups, the sixteen
blend modes and soft masks, annotation appearances, optional content, encryption, and files
damaged enough that the cross-reference table has to be rebuilt.

### Not implemented

JBIG2 and JPEG 2000 images.

Out of scope: writing or editing PDF, form field values, JavaScript, XFA, signature validation
and PDF/A validation.

### License

Apache-2.0. `NOTICE` lists what this derives from.
