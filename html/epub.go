package html

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// openEPUB reads the container, the package document it points at, and the
// navigation the package names.
func openEPUB(r io.ReaderAt, size int64) (*Document, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	files := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		if _, dup := files[f.Name]; !dup {
			files[f.Name] = f
		}
	}
	d := &Document{
		kind: KindEPUB,
		read: func(p string) ([]byte, error) {
			f, ok := files[p]
			if !ok {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxPartBytes))
		},
	}

	root, err := d.epubRoot()
	if err != nil {
		return nil, err
	}
	d.base = path.Dir(root)
	if d.base == "." {
		d.base = ""
	}
	if err := d.readPackage(root); err != nil {
		return nil, err
	}
	d.readNav()
	return d, nil
}

// maxPartBytes bounds what one part of a book may decompress to, which a
// crafted archive is free to say is a terabyte. No chapter, stylesheet, font
// or picture in a book comes near it.
const maxPartBytes = 1 << 26

// epubRoot reads META-INF/container.xml for the package document.
func (d *Document) epubRoot() (string, error) {
	b, err := d.Read("META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("%w: no META-INF/container.xml", ErrInvalid)
	}
	var c struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
			Type string `xml:"media-type,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("%w: container.xml: %v", ErrInvalid, err)
	}
	for _, rf := range c.Rootfiles {
		if rf.Path != "" {
			return path.Clean(rf.Path), nil
		}
	}
	return "", fmt.Errorf("%w: container.xml names no package document", ErrInvalid)
}

// opf is the package document, ISO/IEC 23736 and the EPUB specification
// before it: what the book is, what it holds, and what order to read it in.
type opf struct {
	UniqueID string `xml:"unique-identifier,attr"`
	Metadata struct {
		Title      []string `xml:"title"`
		Creator    []string `xml:"creator"`
		Language   []string `xml:"language"`
		Publisher  []string `xml:"publisher"`
		Descr      []string `xml:"description"`
		Subject    []string `xml:"subject"`
		Date       []string `xml:"date"`
		Identifier []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"identifier"`
		Meta []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Refines  string `xml:"refines,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			Type       string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC      string `xml:"toc,attr"`
		ItemRefs []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// readPackage reads the package document into the manifest, the spine and the
// metadata.
func (d *Document) readPackage(root string) error {
	b, err := d.Read(root)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, root, err)
	}
	var p opf
	if err := unmarshal(b, &p); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, root, err)
	}

	byID := map[string]Item{}
	for _, it := range p.Manifest.Items {
		if it.Href == "" {
			continue
		}
		item := Item{
			Path: d.rel(it.Href),
			Type: it.Type,
			ID:   it.ID,
		}
		if it.Properties != "" {
			item.Properties = strings.Fields(it.Properties)
		}
		d.manifest = append(d.manifest, item)
		if it.ID != "" {
			byID[it.ID] = item
		}
	}
	for _, ref := range p.Spine.ItemRefs {
		if ref.Linear == "no" {
			continue
		}
		if item, ok := byID[ref.IDRef]; ok {
			d.spine = append(d.spine, item)
		}
	}
	// A book with no spine is still worth reading: every chapter it holds is
	// in the manifest and the order they are listed in is the only one there
	// is.
	if len(d.spine) == 0 {
		for _, item := range d.manifest {
			if isChapter(item.Type) {
				d.spine = append(d.spine, item)
			}
		}
	}
	d.meta = p.metadata()
	d.tocID = p.Spine.TOC
	return nil
}

// isChapter reports a media type a reader lays out as a chapter.
func isChapter(t string) bool {
	switch t {
	case "application/xhtml+xml", "text/html", "application/x-dtbook+xml":
		return true
	}
	return false
}

// rel puts a manifest href into the path the archive knows it by, which is
// relative to the package document rather than to the archive.
func (d *Document) rel(href string) string {
	href, _ = splitFragment(href)
	if d.base == "" {
		return path.Clean(href)
	}
	return path.Join(d.base, href)
}

// metadata reads the Dublin Core elements, preferring the title and the
// identifier the package says are the main ones.
func (p *opf) metadata() Metadata {
	m := Metadata{
		Title:       first(p.Metadata.Title),
		Author:      strings.Join(nonEmpty(p.Metadata.Creator), ", "),
		Language:    first(p.Metadata.Language),
		Publisher:   first(p.Metadata.Publisher),
		Description: first(p.Metadata.Descr),
		Subjects:    nonEmpty(p.Metadata.Subject),
	}
	for _, id := range p.Metadata.Identifier {
		if m.Identifier == "" || (p.UniqueID != "" && id.ID == p.UniqueID) {
			m.Identifier = strings.TrimSpace(id.Value)
		}
	}
	m.Created = parseDate(first(p.Metadata.Date))
	for _, meta := range p.Metadata.Meta {
		switch {
		case meta.Property == "dcterms:modified" && meta.Refines == "":
			m.Modified = parseDate(meta.Value)
		case meta.Name == "calibre:series" && m.Publisher == "":
			m.Publisher = meta.Content
		}
	}
	return m
}

// parseDate reads the subset of ISO 8601 a book carries, which is a date and
// optionally a time, and nothing else.
func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func first(s []string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(s []string) []string {
	var out []string
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// unmarshal reads XML that names its elements in namespaces this does not
// want to spell out, which every book does, and that names an encoding the
// standard library will not decode. The decoder is told to accept what it
// finds rather than what a schema says: books are hand written and half of
// them have an undeclared entity in them somewhere.
func unmarshal(b []byte, v any) error { return lenient(b).Decode(v) }

// lenient is the decoder every one of them uses.
func lenient(b []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return dec
}
