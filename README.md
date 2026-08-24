## folio
[![Status](https://github.com/gen2brain/folio/actions/workflows/test.yml/badge.svg)](https://github.com/gen2brain/folio/actions)
[![Go Reference](https://pkg.go.dev/badge/github.com/gen2brain/folio.svg)](https://pkg.go.dev/github.com/gen2brain/folio)

Document rendering in pure Go.

**PDF**, **SVG**, **EPUB**, **MOBI**, **CHM**, **FB2**, **DOCX**, **PPTX**, **XLSX**, **HTML** and plain text.

### Rendering

```go
doc, err := pdf.Open("file.pdf")      // or svg.Open a drawing, html.Open a book or a document
defer doc.Close()

p, err := doc.Page(0)
img, err := p.ImageDPI(150)
txt, err := p.Text()
```

### Supported

 |                                      |                                                                                                                            |
 |--------------------------------------|----------------------------------------------------------------------------------------------------------------------------|
 | **PDF**                              | paths, text, images, shadings, patterns, transparency, annotations, layers; damaged files repaired, encrypted files opened |
 | **SVG**                              | shapes, paths, text on a path and down the page, paint servers, clipping, masking, markers, filters                        |
 | **EPUB, MOBI, CHM, FB2, HTML, text** | container, HTML, CSS cascade and layout: floats, tables, pagination, writing modes, embedded fonts                         |
 | **DOCX, PPTX, XLSX**                 | styles, lists, tables, pictures, footnotes; a slide is a page, a sheet a part, cells formatted as the file asks            |

A page also reads as structured text with the box every character occupies, as plain text, as its links, as SVG and as HTML, and a reflowable document as Markdown. A document carries its outline, metadata and page labels.

Not in scope: writing or editing documents, form field values, JavaScript, XFA, signature validation, PDF/A validation.

### Packages

 |                                                                |                                                           |
 |----------------------------------------------------------------|-----------------------------------------------------------|
 | [doc](https://pkg.go.dev/github.com/gen2brain/folio/doc)       | one Open over the three, for a file of unknown format     |
 | [pdf](https://pkg.go.dev/github.com/gen2brain/folio/pdf)       | the content interpreter and the public API                |
 | [svg](https://pkg.go.dev/github.com/gen2brain/folio/svg)       | a drawing as a document of its own                        |
 | [html](https://pkg.go.dev/github.com/gen2brain/folio/html)     | reflowable and office documents, and the layout over them |
 | [gfx](https://pkg.go.dev/github.com/gen2brain/folio/gfx)       | the device interface and the devices behind it            |
 | [raster](https://pkg.go.dev/github.com/gen2brain/folio/raster) | the 2D engine: paths, rasterizer, pixmaps, blend modes    |
 | [font](https://pkg.go.dev/github.com/gen2brain/folio/font)     | font programs in, outlines and metrics out                |
 | [syntax](https://pkg.go.dev/github.com/gen2brain/folio/syntax) | the file format: objects, filters, encryption, repair     |

Every format renders through one `gfx.Device`, so a caller can drive something other than the rasterizer.
`raster` and `font` know nothing about PDF, `gfx` knows no file format, and `syntax` has no graphics in it.

### Correctness

Objects, device calls, glyphs, text and rendered pixels are each compared against the established implementations: MuPDF, poppler, qpdf, AGG, OpenJPEG, cairo, Ghostscript, librsvg, resvg and a browser.

### License

Apache-2.0. `NOTICE` lists what this derives from.
