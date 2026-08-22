package html

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

// openEPUB reads the container and the package document it points at.
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
	d := &Document{kind: KindEPUB}
	d.read = func(p string) ([]byte, error) {
		f, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxPartBytes))
		if err != nil {
			return nil, err
		}
		return d.deobfuscate(p, b), nil
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
	d.readObfuscation()
	d.readNav()
	return d, nil
}

// The two ways a publisher scrambles an embedded font, and how much of the
// file each touches.
const (
	obfIDPF  = "http://www.idpf.org/2008/embedding"
	obfAdobe = "http://ns.adobe.com/pdf/enc#RC"
	idpfHead = 1040
	adobHead = 1024
)

// readObfuscation reads META-INF/encryption.xml, which says which parts are
// scrambled and how. Nothing else in a book is encrypted this way: the
// mechanism exists for embedded fonts and the key is the book's own
// identifier.
func (d *Document) readObfuscation() {
	b, err := d.Read("META-INF/encryption.xml")
	if err != nil {
		return
	}
	// An attribute is read off the element that carries it, which no path
	// expression reaches into.
	var e struct {
		Data []struct {
			Method struct {
				Algorithm string `xml:"Algorithm,attr"`
			} `xml:"EncryptionMethod"`
			Cipher struct {
				Ref struct {
					URI string `xml:"URI,attr"`
				} `xml:"CipherReference"`
			} `xml:"CipherData"`
		} `xml:"EncryptedData"`
	}
	if unmarshal(b, &e) != nil {
		return
	}
	for _, v := range e.Data {
		uri := v.Cipher.Ref.URI
		if uri == "" {
			continue
		}
		var key []byte
		switch v.Method.Algorithm {
		case obfIDPF:
			key = idpfKey(d.meta.Identifier)
		case obfAdobe:
			key = adobeKey(d.meta.Identifier)
		default:
			continue
		}
		if len(key) == 0 {
			continue
		}
		if d.obfuscated == nil {
			d.obfuscated = map[string][]byte{}
		}
		d.obfuscated[unescapeURI(uri)] = key
	}
}

// deobfuscate undoes the scrambling of an embedded font: the first bytes of
// the file are exclusive ored with a key made from the book's identifier.
func (d *Document) deobfuscate(p string, b []byte) []byte {
	key, ok := d.obfuscated[p]
	if !ok || len(key) == 0 {
		return b
	}
	n := adobHead
	if len(key) == sha1.Size {
		n = idpfHead
	}
	for i := 0; i < n && i < len(b); i++ {
		b[i] ^= key[i%len(key)]
	}
	return b
}

// idpfKey is the SHA-1 of the identifier with every space taken out, which is
// what the IDPF font mangling algorithm asks for.
func idpfKey(id string) []byte {
	var b strings.Builder
	for _, r := range id {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	sum := sha1.Sum([]byte(b.String()))
	return sum[:]
}

// adobeKey is the sixteen bytes the hexadecimal digits of a UUID identifier
// spell, which Adobe's method uses.
func adobeKey(id string) []byte {
	if i := strings.LastIndex(id, "urn:uuid:"); i >= 0 {
		id = id[i+len("urn:uuid:"):]
	}
	var hex []byte
	for i := 0; i < len(id) && len(hex) < 32; i++ {
		if c := id[i]; isHexByte(c) {
			hex = append(hex, c)
		}
	}
	if len(hex) != 32 {
		return nil
	}
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(hexValue(hex[2*i])<<4 | hexValue(hex[2*i+1]))
	}
	return key
}

// unescapeURI undoes the percent escaping a cipher reference may carry.
func unescapeURI(s string) string {
	if !strings.ContainsRune(s, '%') {
		return s
	}
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}

// maxPartBytes bounds what one part of a book may decompress to.
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

// opf is the package document, ISO/IEC 23736 4.3.
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
		if item, ok := byID[ref.IDRef]; ok {
			item.Linear = ref.Linear != "no"
			d.spine = append(d.spine, item)
		}
	}
	if len(d.spine) == 0 {
		for _, item := range d.manifest {
			if isChapter(item.Type) {
				item.Linear = true
				d.spine = append(d.spine, item)
			}
		}
	}
	d.meta = p.metadata()
	d.tocID = p.Spine.TOC
	return nil
}

// isChapter reports a media type laid out as a chapter.
func isChapter(t string) bool {
	switch t {
	case "application/xhtml+xml", "text/html", "application/x-dtbook+xml", "text/plain":
		return true
	}
	return false
}

// rel puts a manifest href into the path the archive knows it by.
func (d *Document) rel(href string) string {
	href, _ = splitFragment(href)
	if d.base == "" {
		return path.Clean(href)
	}
	return path.Join(d.base, href)
}

// metadata reads the Dublin Core elements.
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

// parseDate reads the subset of ISO 8601 a book dates itself with.
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

func unmarshal(b []byte, v any) error { return lenient(b).Decode(v) }

// lenient decodes the XML a hand written book carries.
func lenient(b []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return dec
}
