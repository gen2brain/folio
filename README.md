## folio

[PDF](https://en.wikipedia.org/wiki/PDF), [EPUB](https://en.wikipedia.org/wiki/EPUB) and
[MOBI](https://en.wikipedia.org/wiki/Mobipocket) renderer in pure Go.

Work in progress: every page element a PDF can carry renders, and a book lays out and draws
through the same device seam.

```go
doc, err := pdf.Open("file.pdf")
defer doc.Close()

p, err := doc.Page(0)
img, err := p.ImageDPI(150)
txt, err := p.Text()
```

Over the pdf.js test corpus it renders a page in about the time MuPDF takes, and it renders
files MuPDF refuses. See the [package documentation](https://pkg.go.dev/github.com/gen2brain/folio/pdf)
for text extraction, SVG, rendering into another color space, driving a device other than the
renderer, concurrency, and optional content.

### Supported

Paths, strokes and dashes, clipping by path, by text and by stencil, text in embedded TrueType,
CFF, Type1 and CID keyed fonts, the fourteen standard substitutes, every color space and
function type, images at every bit depth with soft masks and color keys, JPEG, CCITT Group 3
and 4, JBIG2 and JPEG 2000, all seven shading types, tiling and shading patterns, transparency
groups, the sixteen blend modes and soft masks, annotation appearances, optional content layers
that a caller can turn on and off, encryption, and files damaged enough that the cross-reference
table has to be rebuilt.

A page also comes back as text with the box every character occupies, as the links it carries,
and as SVG; a document as its outline, its metadata and its page labels.

Out of scope: writing or editing PDF, form field values, JavaScript, XFA, signature validation
and PDF/A validation.

### Packages

| | |
| --- | --- |
| [pdf](https://pkg.go.dev/github.com/gen2brain/folio/pdf) | the interpreter and the public API |
| [gfx](https://pkg.go.dev/github.com/gen2brain/folio/gfx) | the device interface and the devices behind it |
| [raster](https://pkg.go.dev/github.com/gen2brain/folio/raster) | the 2D engine: paths, rasterizer, pixmaps, blend modes |
| [font](https://pkg.go.dev/github.com/gen2brain/folio/font) | font programs in, outlines and metrics out |
| [syntax](https://pkg.go.dev/github.com/gen2brain/folio/syntax) | the file format: objects, filters, encryption, repair |
| [html](https://pkg.go.dev/github.com/gen2brain/folio/html) | reflowable books: EPUB, MOBI and plain text, and the HTML, CSS and layout over them |

`raster` and `font` know nothing about PDF, `gfx` knows no file format, and `syntax` has no
graphics in it. Rendering a PDF pulls in nothing outside the standard library; `html` is the one
package with a dependency, `golang.org/x/net/html`.

### Correctness

The object layer is checked against MuPDF, poppler and qpdf; the interpreter against
`mutool trace`, call for call; the font engine against `mutool draw -F svg`, glyph for glyph;
text extraction against `mutool draw -F txt`, line for line. The 2D engine is byte-exact against
AGG, the JPEG 2000 decoder byte-exact against OpenJPEG, and rendered pages are scored against
MuPDF, poppler, cairo and Ghostscript.

### License

Apache-2.0. `NOTICE` lists what this derives from.
