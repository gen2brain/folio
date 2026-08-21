package pdf

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gen2brain/folio/raster"
)

// grabber keeps the color of the first fill on a page.
type grabber struct {
	BaseDevice
	cs    *ColorSpace
	color []float32
}

func (d *grabber) FillPath(p *raster.Path, evenOdd bool, ctm raster.Matrix, cs *ColorSpace, color []float32, alpha float32, cp ColorParams) {
	if d.cs == nil {
		d.cs, d.color = cs, append([]float32(nil), color...)
	}
}

// TestColors holds color conversion to what MuPDF and poppler produce for
// the same file. testdata/colors carries one document per case, filled with
// one color in one space, beside what the reference renderers made of it.
func TestColors(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("..", "testdata", "colors.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var checked, unverified int
	for _, line := range strings.Split(string(manifest), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		name, want, source := f[0], f[1], f[2]
		t.Run(name, func(t *testing.T) {
			doc, err := Open(filepath.Join("..", "testdata", "colors", name+".pdf"))
			if err != nil {
				t.Fatal(err)
			}
			defer doc.Close()
			p, err := doc.Page(0)
			if err != nil {
				t.Fatal(err)
			}
			dev := &grabber{}
			if err := p.Run(dev, p.Matrix(72)); err != nil {
				t.Fatal(err)
			}
			if dev.cs == nil {
				t.Fatal("nothing was filled")
			}
			r, g, b := dev.cs.RGB(dev.color)
			got := fmt.Sprintf("%.4f %.4f %.4f", r, g, b)
			if !colorNear(got, want) {
				t.Errorf("%s = %s, want %s (agreed with %s)", name, got, want, source)
			}
		})
		checked++
		if source == "unverified" {
			unverified++
		}
	}
	if checked == 0 {
		t.Fatal("no cases in the manifest")
	}
	t.Logf("%d colors checked, %d not confirmed by a reference renderer", checked, unverified)
}

func colorNear(a, b string) bool {
	var av, bv [3]float64
	if _, err := fmt.Sscanf(a, "%f %f %f", &av[0], &av[1], &av[2]); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(b, "%f %f %f", &bv[0], &bv[1], &bv[2]); err != nil {
		return false
	}
	for i := range av {
		if math.Abs(av[i]-bv[i]) > 1e-4 {
			return false
		}
	}
	return true
}

// TestFunctions checks each function type against values worked out by hand,
// which is the part the color comparison cannot isolate.
func TestFunctions(t *testing.T) {
	tests := []struct {
		name string
		dict string
		in   []float64
		want []float64
	}{
		{
			name: "exponential linear",
			dict: `<< /FunctionType 2 /Domain [0 1] /C0 [0 0.5] /C1 [1 1] /N 1 >>`,
			in:   []float64{0.25}, want: []float64{0.25, 0.625},
		},
		{
			name: "exponential squared",
			dict: `<< /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 2 >>`,
			in:   []float64{0.5}, want: []float64{0.25},
		},
		{
			name: "exponential clamps the domain",
			dict: `<< /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >>`,
			in:   []float64{2}, want: []float64{1},
		},
		{
			name: "range clamps the result",
			dict: `<< /FunctionType 2 /Domain [0 1] /Range [0 0.5] /C0 [0] /C1 [1] /N 1 >>`,
			in:   []float64{1}, want: []float64{0.5},
		},
		{
			name: "postscript arithmetic",
			dict: psFn(`{ 2 mul 1 exch sub }`, "[0 1]", "[0 1]"),
			in:   []float64{0.25}, want: []float64{0.5},
		},
		{
			name: "postscript conditional",
			dict: psFn(`{ dup 0.5 lt { pop 0 } { pop 1 } ifelse }`, "[0 1]", "[0 1]"),
			in:   []float64{0.75}, want: []float64{1},
		},
		{
			name: "postscript roll",
			dict: psFn(`{ 3 1 roll pop pop }`, "[0 1 0 1 0 1]", "[0 1]"),
			in:   []float64{0.1, 0.2, 0.3}, want: []float64{0.3},
		},
		{
			name: "postscript index",
			dict: psFn(`{ 1 index }`, "[0 1 0 1]", "[0 1 0 1]"),
			in:   []float64{0.25, 0.5}, want: []float64{0.5, 0.25},
		},
		{
			name: "postscript copy",
			dict: psFn(`{ 2 copy pop pop }`, "[0 1 0 1]", "[0 1 0 1]"),
			in:   []float64{0.25, 0.5}, want: []float64{0.25, 0.5},
		},
		{
			name: "postscript comparison",
			dict: psFn(`{ 0.5 gt { 0.75 } { 0.25 } ifelse }`, "[0 1]", "[0 1]"),
			in:   []float64{0.6}, want: []float64{0.75},
		},
		{
			name: "postscript integer maths",
			dict: psFn(`{ pop 7 2 idiv 7 2 mod add 4 bitshift cvr 1000 div }`, "[0 1]", "[0 1]"),
			in:   []float64{0}, want: []float64{0.064},
		},
		{
			name: "postscript trigonometry",
			dict: psFn(`{ pop 90 sin 0 1 atan 360 div add }`, "[0 1]", "[0 2]"),
			in:   []float64{0}, want: []float64{1},
		},
		{
			name: "sampled two points",
			dict: `<< /FunctionType 0 /Domain [0 1] /Range [0 1] /Size [2] /BitsPerSample 8 /Length 2 >>` +
				"\nstream\n\x00\xff\nendstream",
			in: []float64{0.5}, want: []float64{0.5},
		},
		{
			name: "sampled with decode",
			dict: `<< /FunctionType 0 /Domain [0 1] /Range [0 1] /Decode [1 0] /Size [2] /BitsPerSample 8 /Length 2 >>` +
				"\nstream\n\x00\xff\nendstream",
			in: []float64{0}, want: []float64{1},
		},
		{
			name: "stitching",
			dict: `<< /FunctionType 3 /Domain [0 1] /Functions [10 0 R 11 0 R] /Bounds [0.5] /Encode [0 1 0 1] >>`,
			in:   []float64{0.75}, want: []float64{0.5},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fn := loadFunction(t, tc.dict)
			if fn == nil {
				t.Fatal("function did not load")
			}
			got := fn.Eval(nil, tc.in...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if math.Abs(got[i]-tc.want[i]) > 1e-4 {
					t.Errorf("got %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

func psFn(prog, domain, rng string) string {
	return fmt.Sprintf("<< /FunctionType 4 /Domain %s /Range %s /Length %d >>\nstream\n%s\nendstream",
		domain, rng, len(prog)+1, prog)
}

// loadFunction builds a document around a function object and reads it back,
// so that the stream and the dictionary go through the real parser.
func loadFunction(t *testing.T, dict string) *Function {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 /MediaBox [0 0 10 10] >>",
		"<< /Type /Page /Parent 2 0 R >>",
		dict,
		// Two parts for the stitching case.
		"<< /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [0.25] /N 1 >>",
		"<< /FunctionType 2 /Domain [0 1] /C0 [0] /C1 [1] /N 1 >>",
	}
	var buf strings.Builder
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		n := i + 1
		if i >= 4 {
			n = i + 6
		}
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 12 /Root 1 0 R >>\nstartxref\n0\n%%%%EOF\n")

	doc, err := Load([]byte(buf.String()), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { doc.Close() })
	return doc.function(Ref{Num: 4})
}
