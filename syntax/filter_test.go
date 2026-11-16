package syntax

import (
	"bytes"
	"compress/flate"
	"compress/lzw"
	"compress/zlib"
	"fmt"
	"io"
	"math/rand/v2"
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
	if got, want := string(runLengthDecode(in, maxDecoded)), "abxxxxx"; got != want {
		t.Errorf("runLength = %q, want %q", got, want)
	}
	if got := runLengthDecode([]byte{129, 'x', 129, 'y', 3, 'a', 'b', 'c', 'd'}, 200); len(got) != 200 {
		t.Errorf("runLength under a limit of 200 gave %d bytes", len(got))
	}
}

// TestDecodeLimit checks that an image decodes only as far as its samples.
func TestDecodeLimit(t *testing.T) {
	f := &File{}
	chain := Array{Name("RL"), Name("RL"), Name("RL"), Name("RL")}
	out, _, _, _ := f.decodeImageData(Dict{"Filter": chain}, []byte{0x81, 0x81}, Ref{}, 100)
	if len(out) > 2*100+64 {
		t.Errorf("a 100 byte image decoded %d bytes", len(out))
	}
	out, _, _, _ = f.decodeImageData(Dict{"Filter": chain}, []byte{0x81, 0x81}, Ref{}, maxDecoded)
	if len(out) != 1<<25 {
		t.Errorf("without a limit the chain decoded %d bytes", len(out))
	}
}

// TestFilterBounds checks the decoders whose output a few bytes can multiply.
func TestFilterBounds(t *testing.T) {
	f := &File{}
	chain := Array{Name("RL"), Name("RL"), Name("RL")}
	if out, _, _, _ := f.DecodeImageData(Dict{"Filter": chain}, []byte{0x81, 0x81}); len(out) != 1<<19 {
		t.Errorf("three RunLength stages gave %d bytes, want %d", len(out), 1<<19)
	}
	long := Array{}
	for range maxFilters + 1 {
		long = append(long, Name("AHx"))
	}
	if _, _, _, err := f.DecodeImageData(Dict{"Filter": long}, []byte("41>")); err == nil {
		t.Error("a chain of 17 filters was decoded")
	}

	for _, rows := range []int64{1 << 40, 1 << 62} {
		parms := Dict{"K": Integer(-1), "Columns": Integer(1 << 20), "Rows": Integer(rows)}
		out, _ := ccittFaxDecode(f, nil, parms)
		if len(out) > maxCCITTBytes {
			t.Errorf("CCITT of %d rows padded to %d bytes", rows, len(out))
		}
	}

	parms := Dict{"Predictor": Integer(12), "Colors": Integer(32), "BitsPerComponent": Integer(16), "Columns": Integer(1 << 24)}
	if got := applyPredictor(f, []byte{0, 1, 2}, parms); len(got) != 0 {
		t.Errorf("a predictor row longer than the data gave %d bytes", len(got))
	}
}

func TestLZWDecode(t *testing.T) {
	in := []byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}
	want := []byte{45, 45, 45, 45, 45, 65, 45, 45, 45, 66}
	got, err := lzwDecode(in, true, maxDecoded)
	if err != nil {
		t.Fatalf("lzw: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("lzw = %v, want %v", got, want)
	}
}

func TestLZWEarlyChange(t *testing.T) {
	if _, err := lzwDecode([]byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, false, maxDecoded); err != nil {
		t.Fatalf("lzw: %v", err)
	}
}

func TestFlateDecode(t *testing.T) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	want := bytes.Repeat([]byte("compress me "), 100)
	w.Write(want)
	w.Close()

	got, err := flateDecode(buf.Bytes(), maxDecoded)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("flate: %v, %d bytes", err, len(got))
	}

	if got, err := flateDecode(append([]byte("  \n"), buf.Bytes()...), maxDecoded); err != nil || !bytes.Equal(got, want) {
		t.Errorf("flate with leading space: %v", err)
	}
	if got, _ := flateDecode(buf.Bytes()[:20], maxDecoded); len(got) == 0 {
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

// TestCCITTGroup4 checks the runs of a known Group 4 bitmap and BlackIs1.
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

// TestImageReader checks the samples a reader decodes against the samples
// ImageDataLimit decodes whole, with the errors each records: Flate and LZW,
// every predictor, a raw and a damaged stream, leading space, truncation and a limit.
func TestImageReader(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	deflate := func(b []byte, header bool) []byte {
		var z bytes.Buffer
		if header {
			w := zlib.NewWriter(&z)
			w.Write(b)
			w.Close()
		} else {
			w, _ := flate.NewWriter(&z, 5)
			w.Write(b)
			w.Close()
		}
		return z.Bytes()
	}
	streams := 0
	for i := range 400 {
		colors, bpc, columns := 1+rng.IntN(4), []int{1, 2, 4, 8, 16}[rng.IntN(5)], 1+rng.IntN(70)
		rowLen := (colors*bpc*columns + 7) / 8
		rows := rng.IntN(40)
		parms := Dict{}
		var plain []byte
		switch pred := []int{1, 2, 10, 15}[rng.IntN(4)]; pred {
		case 1:
			plain = make([]byte, rows*rowLen)
			for k := range plain {
				plain[k] = byte(rng.IntN(256))
			}
		default:
			parms = Dict{"Predictor": Integer(pred), "Colors": Integer(colors),
				"BitsPerComponent": Integer(bpc), "Columns": Integer(columns)}
			if pred == 2 {
				plain = make([]byte, rows*rowLen+rng.IntN(rowLen+1))
				for k := range plain {
					plain[k] = byte(rng.IntN(256))
				}
				break
			}
			for range rows {
				plain = append(plain, byte(rng.IntN(6)))
				for range rowLen {
					plain = append(plain, byte(rng.IntN(256)))
				}
			}
			plain = append(plain, make([]byte, rng.IntN(rowLen+1))...)
		}
		z := deflate(plain, rng.IntN(4) != 0)
		name := Name("FlateDecode")
		if rng.IntN(3) == 0 {
			var b bytes.Buffer
			w := lzw.NewWriter(&b, lzw.MSB, 8)
			w.Write(plain)
			w.Close()
			z, name = b.Bytes(), "LZWDecode"
			parms["EarlyChange"] = Integer(rng.IntN(2))
		}
		switch rng.IntN(8) {
		case 0:
			z = append([]byte("\r\n "), z...)
		case 1:
			z = z[:rng.IntN(len(z)+1)]
		case 2:
			z = append(z, "garbage"...)
		case 3:
			z = append([]byte{'x'}, z...)
		case 4:
			if len(z) > 8 {
				z[len(z)/2] ^= 0x55
			}
		}
		dict := Dict{"Filter": name}
		if rng.IntN(5) == 0 {
			dict["Filter"] = Array{Name("ASCIIHexDecode"), name}
			dict["DecodeParms"] = Array{nil, parms}
			z = []byte(fmt.Sprintf("%x>", z))
		} else if len(parms) > 0 {
			dict["DecodeParms"] = parms
		}
		limit := maxDecoded
		if rng.IntN(3) == 0 {
			limit = rng.IntN(len(plain) + 1)
		}

		f1, f2 := &File{}, &File{}
		want, _, _, wantErr := (&Stream{Dict: dict, doc: f1, raw: z}).ImageDataLimit(limit)
		r, ok := (&Stream{Dict: dict, doc: f2, raw: z}).ImageReader(limit)
		if !ok {
			if wantErr == nil && len(want) > 0 {
				t.Fatalf("stream %d: the reader declined %d bytes the whole decode gave", i, len(want))
			}
			if len(f2.Err()) > 0 {
				t.Fatalf("stream %d: a declined reader recorded %v", i, f2.Err())
			}
			continue
		}
		streams++
		got, _ := io.ReadAll(r)
		r.Close()
		if !bytes.Equal(got, want) {
			t.Fatalf("stream %d: the reader gave %d bytes, the whole decode %d", i, len(got), len(want))
		}
		if e1, e2 := fmt.Sprint(f1.Err()), fmt.Sprint(f2.Err()); e1 != e2 {
			t.Fatalf("stream %d: errors %s, want %s", i, e2, e1)
		}
	}
	if streams < 300 {
		t.Errorf("only %d of 400 streams were read", streams)
	}
}
