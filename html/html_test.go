package html

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildEPUB writes the parts into the archive an EPUB is, mimetype first and
// stored, which is what the specification asks for and what a reader sniffs.
func buildEPUB(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	mt, err := z.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	mt.Write([]byte("application/epub+zip"))
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	for _, name := range names {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(parts[name]))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const container = `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
 <rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

const pkg = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:identifier id="other">wrong</dc:identifier>
  <dc:identifier id="pub">urn:uuid:1234</dc:identifier>
  <dc:title>A Book</dc:title>
  <dc:creator>One Author</dc:creator>
  <dc:creator>Another Author</dc:creator>
  <dc:language>en</dc:language>
  <dc:date>2019-04-01</dc:date>
  <dc:subject>Testing</dc:subject>
  <meta property="dcterms:modified">2020-02-03T04:05:06Z</meta>
 </metadata>
 <manifest>
  <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  <item id="one" href="text/one.xhtml" media-type="application/xhtml+xml"/>
  <item id="two" href="text/two.xhtml" media-type="application/xhtml+xml"/>
  <item id="cover" href="images/cover.png" media-type="image/png"/>
 </manifest>
 <spine>
  <itemref idref="one"/>
  <itemref idref="two"/>
  <itemref idref="cover" linear="no"/>
 </spine>
</package>`

// The nav is wrapped in a section and its titles are wrapped in spans, which
// is what real books do and what a shape matcher misses.
const nav = `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<body><section>
 <nav epub:type="landmarks"><ol><li><a href="text/one.xhtml">Start</a></li></ol></nav>
 <nav epub:type="toc"><h2>Contents</h2><ol>
  <li><a href="text/one.xhtml"><span>One</span></a>
   <ol><li><a href="text/one.xhtml#deep">Deeper</a></li></ol></li>
  <li><a href="text/two.xhtml">Two</a></li>
 </ol></nav>
</section></body></html>`

func openBook(t *testing.T, parts map[string]string) *Document {
	t.Helper()
	d, err := Load(buildEPUB(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func fullBook(t *testing.T) *Document {
	return openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html><body><p>One</p></body></html>",
		"EPUB/text/two.xhtml":    "<html><body><p>Two</p></body></html>",
		"EPUB/images/cover.png":  "\x89PNG",
	})
}

func TestEPUBMetadata(t *testing.T) {
	m := fullBook(t).Metadata()
	if m.Title != "A Book" {
		t.Fatalf("title = %q", m.Title)
	}
	if m.Author != "One Author, Another Author" {
		t.Fatalf("author = %q", m.Author)
	}
	if m.Identifier != "urn:uuid:1234" {
		t.Fatalf("identifier = %q, want the one the package says is unique", m.Identifier)
	}
	if m.Language != "en" || len(m.Subjects) != 1 {
		t.Fatalf("metadata = %+v", m)
	}
	if got := m.Created.Format("2006-01-02"); got != "2019-04-01" {
		t.Fatalf("created = %v", got)
	}
	if got := m.Modified.Format("2006-01-02"); got != "2020-02-03" {
		t.Fatalf("modified = %v", got)
	}
}

func TestEPUBSpine(t *testing.T) {
	d := fullBook(t)
	if d.Kind() != KindEPUB {
		t.Fatalf("kind = %v", d.Kind())
	}
	spine := d.Spine()
	// The cover is in the manifest and out of the spine, because the package
	// says it is not linear.
	if len(spine) != 2 {
		t.Fatalf("spine = %+v", spine)
	}
	if spine[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("path = %q, want it relative to the archive", spine[0].Path)
	}
	if len(d.Manifest()) != 4 {
		t.Fatalf("manifest has %d items, want 4", len(d.Manifest()))
	}
	b, err := d.Read(spine[1].Path)
	if err != nil || !strings.Contains(string(b), "Two") {
		t.Fatalf("read = %q, %v", b, err)
	}
	if _, err := d.Read("EPUB/nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a part that is not there = %v", err)
	}
}

func TestEPUBOutline(t *testing.T) {
	o := fullBook(t).Outline()
	if len(o) != 2 {
		t.Fatalf("outline = %+v", o)
	}
	if o[0].Title != "One" {
		t.Fatalf("a title wrapped in a span came out %q", o[0].Title)
	}
	if o[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("path = %q", o[0].Path)
	}
	if len(o[0].Children) != 1 || o[0].Children[0].Fragment != "deep" {
		t.Fatalf("children = %+v", o[0].Children)
	}
	if o[1].Title != "Two" {
		t.Fatalf("second = %+v", o[1])
	}
}

// TestEPUBNCX checks the EPUB 2 table of contents, which a book carries
// instead of a navigation document or as well as one.
func TestEPUBNCX(t *testing.T) {
	pkg2 := strings.Replace(pkg,
		`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`,
		`<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`, 1)
	pkg2 = strings.Replace(pkg2, `<spine>`, `<spine toc="ncx">`, 1)
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg2,
		"EPUB/text/one.xhtml":    "<html/>",
		"EPUB/text/two.xhtml":    "<html/>",
		"EPUB/toc.ncx": `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/">
			<navMap>
			 <navPoint><navLabel><text>Chapter One</text></navLabel><content src="text/one.xhtml"/>
			  <navPoint><navLabel><text>Inside</text></navLabel><content src="text/one.xhtml#in"/></navPoint>
			 </navPoint>
			</navMap></ncx>`,
	})
	o := d.Outline()
	if len(o) != 1 || o[0].Title != "Chapter One" || o[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("outline = %+v", o)
	}
	if len(o[0].Children) != 1 || o[0].Children[0].Fragment != "in" {
		t.Fatalf("children = %+v", o[0].Children)
	}
}

// TestEPUBNoSpine checks a package that lists its chapters and forgets to put
// them in the spine, which is still a book worth reading.
func TestEPUBNoSpine(t *testing.T) {
	pkg2 := strings.Replace(pkg, `<itemref idref="one"/>`, "", 1)
	pkg2 = strings.Replace(pkg2, `<itemref idref="two"/>`, "", 1)
	pkg2 = strings.Replace(pkg2, `<itemref idref="cover" linear="no"/>`, "", 1)
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg2,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html/>",
		"EPUB/text/two.xhtml":    "<html/>",
	})
	// The navigation document is a chapter by media type, so three.
	if got := len(d.Spine()); got != 3 {
		t.Fatalf("spine has %d parts, want the chapters of the manifest", got)
	}
}

func TestEPUBBroken(t *testing.T) {
	cases := []struct {
		name  string
		parts map[string]string
	}{
		{"no container", map[string]string{"EPUB/package.opf": pkg}},
		{"no package", map[string]string{"META-INF/container.xml": container}},
		{"container names nothing", map[string]string{
			"META-INF/container.xml": `<container><rootfiles/></container>`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(buildEPUB(t, tc.parts)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestText(t *testing.T) {
	d, err := Load([]byte("Call me Ishmael.\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Kind() != KindText {
		t.Fatalf("kind = %v", d.Kind())
	}
	if len(d.Spine()) != 1 {
		t.Fatalf("spine = %+v", d.Spine())
	}
	b, err := d.Read(d.Spine()[0].Path)
	if err != nil || string(b) != "Call me Ishmael.\n" {
		t.Fatalf("read = %q, %v", b, err)
	}
}

func TestNotABook(t *testing.T) {
	if _, err := Load([]byte{0x00, 0x01, 0x02, 0xff}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if _, err := Load(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty = %v, want ErrInvalid", err)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"EPUB/text/one.xhtml", "two.xhtml", "EPUB/text/two.xhtml"},
		{"EPUB/text/one.xhtml", "../images/a.png", "EPUB/images/a.png"},
		{"EPUB/text/one.xhtml", "#frag", "EPUB/text/one.xhtml"},
		{"EPUB/text/one.xhtml", "two.xhtml#frag", "EPUB/text/two.xhtml"},
		{"EPUB/text/one.xhtml", "/EPUB/a.css", "EPUB/a.css"},
		{"one.xhtml", "https://example.com/x", "https://example.com/x"},
	}
	for _, tc := range cases {
		if got := Resolve(tc.base, tc.ref); got != tc.want {
			t.Fatalf("Resolve(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
		}
	}
}

// FuzzBook opens arbitrary bytes as a book and then asks it everything: the
// metadata, the spine, the outline and every part. None of it may panic.
func FuzzBook(fu *testing.F) {
	fu.Add(buildEPUB(&testing.T{}, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html/>",
	}))
	fu.Add([]byte("plain text"))
	fu.Add(buildMOBI([]byte("<html><body>Hi</body></html>"), []byte{0x81}, []byte("\x89PNG\r\n\x1a\n0123456789")))
	fu.Fuzz(func(t *testing.T, b []byte) {
		d, err := Load(b)
		if err != nil {
			return
		}
		defer d.Close()
		d.Metadata()
		for _, it := range d.Spine() {
			d.Read(it.Path)
		}
		for _, it := range d.Manifest() {
			d.Read(it.Path)
		}
		var walk func([]Outline, int)
		walk = func(o []Outline, depth int) {
			if depth > 64 {
				t.Fatal("outline nested past the guard")
			}
			for _, e := range o {
				walk(e.Children, depth+1)
			}
		}
		walk(d.Outline(), 0)
	})
}

// buildMOBI writes the smallest PalmDB that is a book: a header, a record
// list, the record that describes the rest, one compressed text record and
// one picture.
func buildMOBI(text []byte, trailing []byte, image []byte) []byte {
	rec0 := make([]byte, 16+232)
	be16(rec0[0:], 2)                 // PalmDOC LZ77
	be32(rec0[4:], uint32(len(text))) // text length
	be16(rec0[8:], 1)                 // one text record
	be16(rec0[10:], 4096)             // record size
	copy(rec0[16:], "MOBI")
	be32(rec0[20:], 232)              // header length
	be32(rec0[28:], 65001)            // UTF-8
	be32(rec0[16+0x50-16:], 2)        // first non-book index
	be32(rec0[0x54:], uint32(16+232)) // the full name follows the header
	be32(rec0[0x58:], uint32(len("A Title")))
	be32(rec0[0x80:], 0x40) // EXTH is there
	if len(trailing) > 0 {
		be32(rec0[0xf0:], 2)
	}

	var exth []byte
	add := func(kind int, v string) {
		e := make([]byte, 8+len(v))
		be32(e, uint32(kind))
		be32(e[4:], uint32(len(e)))
		copy(e[8:], v)
		exth = append(exth, e...)
	}
	add(100, "An Author")
	add(101, "A Publisher")
	add(105, "A Subject")
	head := make([]byte, 12)
	copy(head, "EXTH")
	be32(head[4:], uint32(12+len(exth)))
	be32(head[8:], 3)
	rec0 = append(rec0, append(head, exth...)...)
	be32(rec0[0x54:], uint32(len(rec0)))
	rec0 = append(rec0, "A Title"...)

	var comp []byte
	for i := 0; i < len(text); i += 8 {
		n := min(8, len(text)-i)
		comp = append(comp, byte(n))
		comp = append(comp, text[i:i+n]...)
	}
	comp = append(comp, trailing...)

	recs := [][]byte{rec0, comp}
	if image != nil {
		recs = append(recs, image)
	}
	head2 := make([]byte, 78+8*len(recs))
	copy(head2, "A Book")
	copy(head2[60:], "BOOKMOBI")
	be16(head2[76:], uint16(len(recs)))
	off := len(head2)
	for i, r := range recs {
		be32(head2[78+8*i:], uint32(off))
		off += len(r)
	}
	out := head2
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

func be16(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

func be32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func TestMOBI(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	d, err := Load(buildMOBI([]byte("<html><body><p>Hello</p></body></html>"), nil, png))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Kind() != KindMOBI {
		t.Fatalf("kind = %v", d.Kind())
	}
	m := d.Metadata()
	if m.Title != "A Title" || m.Author != "An Author" || m.Publisher != "A Publisher" {
		t.Fatalf("metadata = %+v", m)
	}
	if len(m.Subjects) != 1 || m.Subjects[0] != "A Subject" {
		t.Fatalf("subjects = %v", m.Subjects)
	}
	b, err := d.Read("index.html")
	if err != nil || string(b) != "<html><body><p>Hello</p></body></html>" {
		t.Fatalf("text = %q, %v", b, err)
	}
	// The picture is a part named by the number the HTML refers to it by.
	if len(d.Manifest()) != 2 || d.Manifest()[1].Path != "00001" {
		t.Fatalf("manifest = %+v", d.Manifest())
	}
	if got, _ := d.Read("00001"); string(got) != string(png) {
		t.Fatalf("the picture came back as %d bytes", len(got))
	}
}

// TestMOBITrailing checks that the entries a record carries after its data
// are taken off, which is what tells the text from the index behind it.
func TestMOBITrailing(t *testing.T) {
	// One trailing entry of four bytes, its own size written backwards in the
	// last byte with the top bit marking where it ends.
	trailing := []byte{'x', 'y', 'z', 0x80 | 4}
	d, err := Load(buildMOBI([]byte("Hello"), trailing, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if b, _ := d.Read("index.html"); string(b) != "Hello" {
		t.Fatalf("text = %q, want the trailing entry gone", b)
	}
}

// TestMOBIRepeat checks the two byte form of the compression, which points
// back into what has already been written and may run past the end of it.
func TestMOBIRepeat(t *testing.T) {
	rec := []byte{2, 'a', 'b', 0x80, byte(2<<3 | (6 - 3))}
	if got := string(palmDoc(nil, rec)); got != "abababab" {
		t.Fatalf("palmDoc = %q, want abababab", got)
	}
}

func TestCP1252(t *testing.T) {
	if got := string(fromCP1252([]byte{'a', 0x92, 'b', 0xe9})); got != "a’bé" {
		t.Fatalf("fromCP1252 = %q", got)
	}
}
