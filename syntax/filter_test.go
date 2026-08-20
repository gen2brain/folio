package syntax

import (
	"bytes"
	"compress/zlib"
	"testing"
)

func TestASCIIHexDecode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"48656C6C6F>", "Hello"},
		{"48 65 6c\n6C 6F>", "Hello"},
		{"4>", "@"},
		{"48656C6C6F", "Hello"},
		{"", ""},
	} {
		if got := string(asciiHexDecode([]byte(tc.in))); got != tc.want {
			t.Errorf("asciiHex(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestASCII85Decode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"87cURD]i,\"Ebo80~>", "Hello World!"},
		{"z~>", "\x00\x00\x00\x00"},
		{"87cURD]i,\"Ebo80", "Hello World!"},
		{"<~87cURD]j~>", "Hello "},
	} {
		got, err := ascii85Decode([]byte(tc.in))
		if err != nil {
			t.Errorf("ascii85(%q): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("ascii85(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunLengthDecode(t *testing.T) {
	in := []byte{1, 'a', 'b', 252, 'x', 128, 'i', 'g', 'n'}
	if got, want := string(runLengthDecode(in)), "abxxxxx"; got != want {
		t.Errorf("runLength = %q, want %q", got, want)
	}
}

func TestLZWDecode(t *testing.T) {
	in := []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}
	want := []byte{45, 45, 45, 45, 45, 65, 45, 45, 45, 66}
	got, err := lzwDecode(in, true)
	if err != nil {
		t.Fatalf("lzw: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("lzw = %v, want %v", got, want)
	}
}

func TestLZWEarlyChange(t *testing.T) {
	if _, err := lzwDecode([]byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, false); err != nil {
		t.Fatalf("lzw: %v", err)
	}
}

func TestFlateDecode(t *testing.T) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	want := bytes.Repeat([]byte("compress me "), 100)
	w.Write(want)
	w.Close()

	got, err := flateDecode(buf.Bytes())
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("flate: %v, %d bytes", err, len(got))
	}

	if got, err := flateDecode(append([]byte("  \n"), buf.Bytes()...)); err != nil || !bytes.Equal(got, want) {
		t.Errorf("flate with leading space: %v", err)
	}
	if got, _ := flateDecode(buf.Bytes()[:20]); len(got) == 0 {
		t.Error("truncated flate returned nothing; partial data is better than none")
	}
}

func TestPNGPredictor(t *testing.T) {
	data := []byte{
		0, 10, 20,
		1, 1, 1,
		2, 5, 5,
	}
	parms := Dict{"Predictor": Integer(12), "Colors": Integer(1), "BitsPerComponent": Integer(8), "Columns": Integer(2)}
	got := applyPredictor(&File{}, data, parms)
	want := []byte{10, 20, 1, 2, 6, 7}
	if !bytes.Equal(got, want) {
		t.Errorf("png predictor = %v, want %v", got, want)
	}
}

func TestTIFFPredictor(t *testing.T) {
	data := []byte{10, 20, 30, 1, 1, 1}
	parms := Dict{"Predictor": Integer(2), "Colors": Integer(3), "BitsPerComponent": Integer(8), "Columns": Integer(2)}
	got := applyPredictor(&File{}, data, parms)
	want := []byte{10, 20, 30, 11, 21, 31}
	if !bytes.Equal(got, want) {
		t.Errorf("tiff predictor = %v, want %v", got, want)
	}
}

// TestCCITTGroup4 decodes what an independent encoder produced for a known
// bitmap: 32 by 8, twelve columns of one color and a band of twenty four on
// one row. Which color a run is depends on the convention the encoder wrote
// it in, so the test is on the runs, and on BlackIs1 flipping them.
func TestCCITTGroup4(t *testing.T) {
	data := []byte{0x24, 0x06, 0x8f, 0x2a, 0x05, 0x24, 0x06, 0x8f, 0xc0, 0x04, 0x00, 0x40}
	parms := Dict{"K": Integer(-1), "Columns": Integer(32), "Rows": Integer(8)}

	got, err := ccittFaxDecode(&File{}, data, parms)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8*4 {
		t.Fatalf("decoded %d bytes, want %d", len(got), 8*4)
	}
	for y := 0; y < 8; y++ {
		want := []int{12, 20}
		if y == 3 {
			want = []int{24, 8}
		}
		if runs := bitRuns(got[y*4:y*4+4], 32); !equalInts(runs, want) {
			t.Fatalf("row %d runs %v, want %v", y, runs, want)
		}
	}

	parms["BlackIs1"] = Bool(true)
	flipped, err := ccittFaxDecode(&File{}, data, parms)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if flipped[i] != got[i]^0xff {
			t.Fatalf("BlackIs1 byte %d = %02x, want %02x", i, flipped[i], got[i]^0xff)
		}
	}
}

// bitRuns is the lengths of the runs of equal bits in a row.
func bitRuns(row []byte, n int) []int {
	var runs []int
	last, count := row[0]>>7, 0
	for i := 0; i < n; i++ {
		b := row[i/8] >> (7 - i%8) & 1
		if b != last {
			runs = append(runs, count)
			last, count = b, 0
		}
		count++
	}
	return append(runs, count)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCCITTTruncated(t *testing.T) {
	full := []byte{0x24, 0x06, 0x8f, 0x2a, 0x05, 0x24, 0x06, 0x8f, 0xc0, 0x04, 0x00, 0x40}
	for n := 0; n <= len(full); n++ {
		parms := Dict{"K": Integer(-1), "Columns": Integer(32), "Rows": Integer(8)}
		got, err := ccittFaxDecode(&File{}, full[:n], parms)
		if err != nil {
			continue
		}
		if len(got) != 8*4 {
			t.Fatalf("%d bytes in decoded to %d bytes, want %d", n, len(got), 8*4)
		}
	}
}
