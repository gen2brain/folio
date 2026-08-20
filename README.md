## pdf

[PDF](https://en.wikipedia.org/wiki/PDF) renderer in pure Go.

Work in progress: every page element a PDF can carry renders.

```go
doc, err := pdf.Open("file.pdf")
defer doc.Close()

p, err := doc.Page(0)
img, err := p.Image(150)
```

Over the pdf.js test corpus it renders a page in about the time MuPDF takes, and it renders
files MuPDF refuses. See the [package documentation](https://pkg.go.dev/github.com/gen2brain/pdf/pdf)
for rendering into another color space, driving a device other than the renderer, concurrency,
and optional content.

### Supported

Paths, strokes and dashes, clipping by path, by text and by stencil, text in embedded TrueType,
CFF, Type1 and CID keyed fonts, the fourteen standard substitutes, every color space and
function type, images at every bit depth with soft masks and color keys, JPEG, CCITT Group 3
and 4, JBIG2 and JPEG 2000, all seven shading types, tiling and shading patterns, transparency
groups, the sixteen blend modes and soft masks, annotation appearances, optional content layers
that a caller can turn on and off, encryption, and files damaged enough that the cross-reference
table has to be rebuilt.

Out of scope: writing or editing PDF, form field values, JavaScript, XFA, signature validation
and PDF/A validation.

### Packages

| | |
| --- | --- |
| [pdf](https://pkg.go.dev/github.com/gen2brain/pdf/pdf) | the interpreter, the devices and the public API |
| [raster](https://pkg.go.dev/github.com/gen2brain/pdf/raster) | the 2D engine: paths, rasterizer, pixmaps, blend modes |
| [font](https://pkg.go.dev/github.com/gen2brain/pdf/font) | font programs in, outlines and metrics out |
| [syntax](https://pkg.go.dev/github.com/gen2brain/pdf/syntax) | the file format: objects, filters, encryption, repair |

`raster` and `font` know nothing about PDF, and `syntax` has no graphics in it.

### Correctness

The object layer is checked against MuPDF, poppler and qpdf; the interpreter against
`mutool trace`, call for call; the font engine against `mutool draw -F svg`, glyph for glyph.
The 2D engine is byte-exact against AGG, the JPEG 2000 decoder byte-exact against OpenJPEG, and
rendered pages are scored against MuPDF, poppler, cairo and Ghostscript.

### License

Apache-2.0. `NOTICE` lists what this derives from.
