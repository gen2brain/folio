package doc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gen2brain/folio/html"
	"github.com/gen2brain/folio/pdf"
	"github.com/gen2brain/folio/svg"
)

const onePagePDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 100]>>endobj
trailer<</Root 1 0 R>>
%%EOF
`

const oneDrawing = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10">` +
	`<title>A drawing</title><text x="0" y="8">Drawn</text></svg>`

const oneBook = `<!DOCTYPE html><html><head><title>A book</title></head>` +
	`<body><p>Read me.</p></body></html>`

func TestDetect(t *testing.T) {
	for _, c := range []struct {
		name string
		data string
		want Kind
	}{
		{"pdf", onePagePDF, KindPDF},
		{"svg", oneDrawing, KindSVG},
		{"svg after a declaration", "<?xml version=\"1.0\"?>\n<!-- a note -->\n" + oneDrawing, KindSVG},
		{"html", oneBook, KindBook},
		{"text", "Just some words.\n", KindBook},
		{"zip", "PK\x03\x04rest of it", KindBook},
		{"chm", "ITSF and the rest", KindBook},
		{"fictionbook", `<?xml version="1.0"?><FictionBook><body/></FictionBook>`, KindBook},
		{"binary", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR", 0},
		{"empty", "", 0},
	} {
		if got := Detect([]byte(c.data)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{KindPDF: "PDF", KindSVG: "SVG", KindBook: "book", 0: "unknown"} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d) = %q, want %q", k, got, want)
		}
	}
}

func TestLoad(t *testing.T) {
	for _, c := range []struct {
		name  string
		data  string
		kind  Kind
		title string
		says  string
	}{
		{"pdf", onePagePDF, KindPDF, "", ""},
		{"svg", oneDrawing, KindSVG, "A drawing", "Drawn"},
		{"book", oneBook, KindBook, "A book", "Read me."},
	} {
		d, err := Load([]byte(c.data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if d.Kind() != c.kind {
			t.Errorf("%s: opened as %v", c.name, d.Kind())
		}
		if got := d.Metadata().Title; got != c.title {
			t.Errorf("%s: title %q, want %q", c.name, got, c.title)
		}
		if n := d.NumPages(); n != 1 {
			t.Fatalf("%s: %d pages, want 1", c.name, n)
		}
		p, err := d.Page(0)
		if err != nil {
			t.Fatalf("%s: page: %v", c.name, err)
		}
		if b := p.Bounds(); b.X1-b.X0 <= 0 || b.Y1-b.Y0 <= 0 {
			t.Errorf("%s: bounds %v", c.name, b)
		}
		txt, err := p.Text()
		if err != nil {
			t.Errorf("%s: text: %v", c.name, err)
		}
		if c.says != "" && !strings.Contains(txt, c.says) {
			t.Errorf("%s: page says %q, want %q in it", c.name, txt, c.says)
		}
		if _, err := p.Image(); err != nil {
			t.Errorf("%s: image: %v", c.name, err)
		}
		if _, err := p.ImageDPI(72); err != nil {
			t.Errorf("%s: image at 72 dpi: %v", c.name, err)
		}
		if _, err := p.StructuredText(); err != nil {
			t.Errorf("%s: structured text: %v", c.name, err)
		}
		if _, err := p.SVG(); err != nil {
			t.Errorf("%s: svg: %v", c.name, err)
		}
		if _, err := p.HTML(); err != nil {
			t.Errorf("%s: html: %v", c.name, err)
		}
		p.Links()
		if err := d.Close(); err != nil {
			t.Errorf("%s: close: %v", c.name, err)
		}
	}
}

func TestUnderlying(t *testing.T) {
	for _, c := range []struct {
		name string
		data string
		is   func(any) bool
	}{
		{"pdf", onePagePDF, func(v any) bool { _, ok := v.(*pdf.Document); return ok }},
		{"svg", oneDrawing, func(v any) bool { _, ok := v.(*svg.Document); return ok }},
		{"book", oneBook, func(v any) bool { _, ok := v.(*html.Document); return ok }},
	} {
		d, err := Load([]byte(c.data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !c.is(d.Underlying()) {
			t.Errorf("%s: Underlying is %T", c.name, d.Underlying())
		}
		d.Close()
	}
}

func TestOpenAndStream(t *testing.T) {
	name := filepath.Join(t.TempDir(), "drawing.svg")
	if err := os.WriteFile(name, []byte(oneDrawing), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind() != KindSVG {
		t.Errorf("opened as %v", d.Kind())
	}
	if err := d.Close(); err != nil {
		t.Errorf("close: %v", err)
	}

	d, err = NewStream(strings.NewReader(oneBook))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind() != KindBook {
		t.Errorf("streamed as %v", d.Kind())
	}
	d.Close()
}

func TestUnsupported(t *testing.T) {
	if _, err := Load([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a PNG gave %v, want ErrUnsupported", err)
	}
	if _, err := Load(nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("nothing gave %v, want ErrUnsupported", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("opening a file that is not there succeeded")
	}
}
