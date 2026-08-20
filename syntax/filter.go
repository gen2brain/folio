package syntax

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"sync"
)

// byteFilters are the filters that produce bytes. Anything else produces
// pixels and is left alone: the image decoder needs the encoded data, its
// dictionary and its color space together, and a caller may have registered a
// decoder for a filter this package has never heard of.
var byteFilters = map[Name]bool{
	"FlateDecode": true, "Fl": true,
	"LZWDecode": true, "LZW": true,
	"ASCII85Decode": true, "A85": true,
	"ASCIIHexDecode": true, "AHx": true,
	"RunLengthDecode": true, "RL": true,
	"CCITTFaxDecode": true, "CCF": true,
	"Crypt": true, "": true,
}

// Data returns the fully decoded stream contents.
func (s *Stream) Data() ([]byte, error) {
	b, filter, _, err := s.decode()
	if err != nil {
		return nil, err
	}
	if filter != "" {
		return nil, fmt.Errorf("%w: %s stream", ErrUnsupported, filter)
	}
	return b, nil
}

// Raw returns the stream contents as stored, decrypted but not decoded.
func (s *Stream) Raw() ([]byte, error) {
	if s.crypt == nil {
		return s.raw, nil
	}
	return s.crypt.decryptStream(s.raw), nil
}

// ImageData returns the stream contents with every byte filter applied, plus
// the image filter that remains and its parameters, if any.
func (s *Stream) ImageData() (data []byte, filter Name, parms Dict, err error) {
	return s.decode()
}

func (s *Stream) decode() ([]byte, Name, Dict, error) {
	if s.dec != nil || s.err != nil {
		return s.dec, s.imgFilter, s.imgParms, s.err
	}
	data, err := s.Raw()
	if err != nil {
		return nil, "", nil, err
	}
	data, s.imgFilter, s.imgParms, s.err = s.doc.DecodeImageData(s.Dict, data)
	if s.err != nil {
		return nil, "", nil, s.err
	}
	s.dec = data
	return s.dec, s.imgFilter, s.imgParms, nil
}

// DecodeImageData applies the byte filters dict names to data and returns what
// is left: the bytes, the image filter that produces pixels rather than bytes,
// and its parameters. It is how an inline image, whose data lies in the
// content stream rather than in a stream object, reaches the same decoders.
func (f *File) DecodeImageData(dict Dict, data []byte) ([]byte, Name, Dict, error) {
	filters := f.names(dict, "Filter", "F")
	parms := f.parmsList(dict, len(filters))

	for i, name := range filters {
		if !byteFilters[name] {
			if i != len(filters)-1 {
				return nil, "", nil, fmt.Errorf("%w: %s is not the last filter", ErrInvalid, name)
			}
			return data, name, parms[i], nil
		}
		out, err := applyFilter(f, name, data, parms[i])
		if err != nil {
			if out == nil {
				return nil, "", nil, err
			}
			f.errorf("%v", err)
		}
		data = out
	}
	return data, "", nil, nil
}

// names resolves a key that may hold either a name or an array of names.
func (f *File) names(dict Dict, keys ...Name) []Name {
	if f == nil {
		if v, ok := dict[keys[0]].(Name); ok {
			return []Name{v}
		}
		return nil
	}
	return f.GetNames(f.Lookup(dict, keys...))
}

// parmsList lines /DecodeParms up with /Filter, whichever of the several legal
// shapes it was written in.
func (f *File) parmsList(dict Dict, n int) []Dict {
	out := make([]Dict, n)
	if f == nil {
		return out
	}
	switch v := f.Lookup(dict, "DecodeParms", "DP").(type) {
	case Dict:
		if n > 0 {
			out[0] = v
		}
	case Array:
		for i := 0; i < n && i < len(v); i++ {
			out[i] = f.GetDict(v[i])
		}
	}
	return out
}

func applyFilter(f *File, name Name, data []byte, parms Dict) ([]byte, error) {
	switch name {
	case "FlateDecode", "Fl":
		out, err := flateDecode(data)
		if out != nil {
			out = applyPredictor(f, out, parms)
		}
		return out, err
	case "LZWDecode", "LZW":
		early := int64(1)
		if f != nil {
			early = f.GetInt(parms["EarlyChange"], 1)
		}
		out, err := lzwDecode(data, early != 0)
		if out != nil {
			out = applyPredictor(f, out, parms)
		}
		return out, err
	case "ASCII85Decode", "A85":
		return ascii85Decode(data)
	case "ASCIIHexDecode", "AHx":
		return asciiHexDecode(data), nil
	case "RunLengthDecode", "RL":
		return runLengthDecode(data), nil
	case "CCITTFaxDecode", "CCF":
		return ccittFaxDecode(f, data, parms)
	case "Crypt":
		return data, nil
	case "":
		return data, nil
	}
	return nil, fmt.Errorf("%w: filter /%s", ErrUnsupported, name)
}

// flateDecode inflates a stream, tolerating a corrupt header and trailing
// garbage, and keeping a partial result.
func flateDecode(data []byte) ([]byte, error) {
	for i := 0; i < len(data) && i < 32 && isSpace[data[i]]; i++ {
		data = data[1:]
		i--
	}
	if len(data) == 0 {
		return []byte{}, nil
	}

	out, err := inflate(data, true)
	if err == nil || len(out) > 0 {
		return out, err
	}
	if out, err2 := inflate(data, false); err2 == nil || len(out) > 0 {
		return out, err2
	}
	if len(data) > 1 {
		if out, err2 := inflate(data[1:], true); err2 == nil || len(out) > 0 {
			return out, err2
		}
	}
	return nil, err
}

// inflaters keeps the decompressors, which carry a 32 KB window each and are
// the largest thing a stream allocates.
var inflaters [2]sync.Pool

func inflate(data []byte, header bool) ([]byte, error) {
	pool := &inflaters[0]
	if header {
		pool = &inflaters[1]
	}
	src := bytes.NewReader(data)

	var r io.ReadCloser
	var err error
	if v := pool.Get(); v != nil {
		r = v.(io.ReadCloser)
		if rs, ok := r.(interface {
			Reset(io.Reader, []byte) error
		}); ok {
			err = rs.Reset(src, nil)
		} else {
			r.(flate.Resetter).Reset(src, nil)
		}
	} else if header {
		r, err = zlib.NewReader(src)
	} else {
		r = newRawInflater(src)
	}
	if err != nil {
		if r != nil {
			pool.Put(r)
		}
		return nil, err
	}
	defer func() {
		r.Close()
		pool.Put(r)
	}()

	var buf bytes.Buffer
	buf.Grow(min(len(data)*4, 1<<20))
	_, err = io.Copy(&buf, io.LimitReader(r, maxDecoded))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		if buf.Len() == 0 {
			return nil, err
		}
		return buf.Bytes(), fmt.Errorf("flate: %w after %d bytes", err, buf.Len())
	}
	return buf.Bytes(), nil
}

// maxDecoded bounds what one stream may inflate to. It fits in an int on 32
// bit targets, and a gigabyte is far past any legitimate stream.
const maxDecoded = 1 << 30

// applyPredictor undoes the TIFF or PNG predictor a filter's parameters name.
func applyPredictor(f *File, data []byte, parms Dict) []byte {
	if parms == nil || f == nil {
		return data
	}
	pred := int(f.GetInt(parms["Predictor"], 1))
	if pred <= 1 {
		return data
	}
	colors := int(f.GetInt(parms["Colors"], 1))
	bpc := int(f.GetInt(parms["BitsPerComponent"], 8))
	columns := int(f.GetInt(parms["Columns"], 1))
	if colors < 1 || colors > 32 || columns < 1 || columns > 1<<24 {
		f.errorf("bad predictor parameters: colors %d columns %d", colors, columns)
		return data
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		f.errorf("bad predictor bits per component: %d", bpc)
		return data
	}

	bpp := (colors*bpc + 7) / 8 // bytes per pixel, rounded up
	rowLen := (colors*bpc*columns + 7) / 8
	if pred == 2 {
		return tiffPredictor(data, colors, bpc, columns, rowLen)
	}

	rows := len(data) / (rowLen + 1)
	out := make([]byte, 0, rows*rowLen)
	prev := make([]byte, rowLen)
	for r := 0; r < rows; r++ {
		src := data[r*(rowLen+1):]
		ft := src[0]
		row := make([]byte, rowLen)
		copy(row, src[1:rowLen+1])
		switch ft {
		case 0:
		case 1:
			for i := bpp; i < rowLen; i++ {
				row[i] += row[i-bpp]
			}
		case 2:
			for i := 0; i < rowLen; i++ {
				row[i] += prev[i]
			}
		case 3:
			for i := 0; i < rowLen; i++ {
				var left byte
				if i >= bpp {
					left = row[i-bpp]
				}
				row[i] += byte((int(left) + int(prev[i])) / 2)
			}
		case 4:
			for i := 0; i < rowLen; i++ {
				var left, upLeft byte
				if i >= bpp {
					left, upLeft = row[i-bpp], prev[i-bpp]
				}
				row[i] += paeth(left, prev[i], upLeft)
			}
		default:
			f.errorf("unknown PNG predictor row type %d", ft)
		}
		out = append(out, row...)
		prev = row
	}
	return out
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func tiffPredictor(data []byte, colors, bpc, columns, rowLen int) []byte {
	rows := len(data) / rowLen
	for r := 0; r < rows; r++ {
		row := data[r*rowLen : (r+1)*rowLen]
		switch bpc {
		case 8:
			for i := colors; i < len(row); i++ {
				row[i] += row[i-colors]
			}
		case 16:
			for i := colors * 2; i+1 < len(row); i += 2 {
				v := uint16(row[i])<<8 | uint16(row[i+1])
				v += uint16(row[i-colors*2])<<8 | uint16(row[i-colors*2+1])
				row[i], row[i+1] = byte(v>>8), byte(v)
			}
		default:
			n := columns * colors
			vals := make([]uint16, n)
			br := BitReader{buf: row}
			for i := range vals {
				vals[i] = uint16(br.Read(bpc))
			}
			mask := uint16(1<<bpc - 1)
			for i := colors; i < n; i++ {
				vals[i] = (vals[i] + vals[i-colors]) & mask
			}
			bw := bitWriter{buf: row[:0]}
			for _, v := range vals {
				bw.write(uint32(v), bpc)
			}
			bw.flush()
		}
	}
	return data
}

func ascii85Decode(data []byte) ([]byte, error) {
	if hasPrefix(data, "<~") {
		data = data[2:]
	}
	out := make([]byte, 0, len(data)*4/5)
	var group [5]byte
	n := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case isSpace[c]:
			continue
		case c == '~':
			goto done
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
			continue
		case c < '!' || c > 'u':
			return out, fmt.Errorf("%w: ascii85 byte %q", ErrInvalid, c)
		}
		group[n] = c - '!'
		n++
		if n == 5 {
			v := uint32(0)
			for _, g := range group {
				v = v*85 + uint32(g)
			}
			out = append(out, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
			n = 0
		}
	}
done:
	if n > 0 {
		for i := n; i < 5; i++ {
			group[i] = 84
		}
		v := uint32(0)
		for _, g := range group {
			v = v*85 + uint32(g)
		}
		b := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, b[:n-1]...)
	}
	return out, nil
}

func asciiHexDecode(data []byte) []byte {
	out := make([]byte, 0, len(data)/2)
	first := -1
	for _, c := range data {
		if c == '>' {
			break
		}
		d := hexVal(c)
		if d < 0 {
			continue
		}
		if first < 0 {
			first = d
		} else {
			out = append(out, byte(first<<4|d))
			first = -1
		}
	}
	if first >= 0 {
		out = append(out, byte(first<<4))
	}
	return out
}

func runLengthDecode(data []byte) []byte {
	out := make([]byte, 0, len(data)*2)
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out
		case n < 128:
			end := i + n + 1
			if end > len(data) {
				end = len(data)
			}
			out = append(out, data[i:end]...)
			i = end
		default:
			if i >= len(data) {
				return out
			}
			for j := 0; j < 257-n; j++ {
				out = append(out, data[i])
			}
			i++
		}
	}
	return out
}
