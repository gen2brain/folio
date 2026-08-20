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
