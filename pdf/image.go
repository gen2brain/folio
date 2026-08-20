package pdf

import (
	"bytes"
	"fmt"
	"image"
	stdcolor "image/color"
	"image/jpeg"
	"sync"

	"github.com/gen2brain/pdf/raster"
	"github.com/gen2brain/pdf/syntax"
)

// Image is an image XObject or an inline image. The pixels are not decoded
// until a device asks for them.
type Image struct {
	Width, Height int
	BPC           int
	CS            *ColorSpace
	// Mask is true for a stencil mask, which paints the fill color through
	// its one bit samples rather than carrying color of its own.
	Mask bool
	// Interpolate is the /Interpolate hint.
	Interpolate bool
	// Decode is the /Decode array, nil when the default applies.
	Decode []float64
	// SMask and StencilMask are the two ways an image carries transparency.
	SMask       *Image
	StencilMask *Image
	// ColorKey is the /Mask color key range, nil when there is none.
	ColorKey []int

	doc    *Document
	stream *syntax.Stream
	dict   Dict
	inline []byte
}

// image builds an Image from an image XObject or an inline image dictionary.
func (d *Document) image(st *syntax.Stream, dict Dict, res Dict, inline []byte, depth int) *Image {
	f := d.f
	img := &Image{
		doc:         d,
		Width:       int(f.GetInt(f.Lookup(dict, "Width", "W"), 0)),
		Height:      int(f.GetInt(f.Lookup(dict, "Height", "H"), 0)),
		BPC:         int(f.GetInt(f.Lookup(dict, "BitsPerComponent", "BPC"), 8)),
		Mask:        f.GetBool(f.Lookup(dict, "ImageMask", "IM"), false),
		Interpolate: f.GetBool(f.Lookup(dict, "Interpolate", "I"), false),
		Decode:      f.GetFloats(f.Lookup(dict, "Decode", "D")),
		stream:      st,
		dict:        dict,
		inline:      inline,
	}
	if img.Width <= 0 || img.Height <= 0 {
		d.errorf("image is %dx%d", img.Width, img.Height)
		return nil
	}
	if img.Mask {
		img.BPC = 1
	} else if cs := f.Lookup(dict, "ColorSpace", "CS"); cs != nil {
		img.CS = d.colorSpace(cs, res, 0)
	} else {
		img.CS = img.codestreamColorSpace()
	}

	if depth < maxNesting && inline == nil {
		if sm := f.GetStream(dict["SMask"]); sm != nil {
			img.SMask = d.image(sm, sm.Dict, res, nil, depth+1)
		}
		switch m := f.Resolve(dict["Mask"]).(type) {
		case *syntax.Stream:
			img.StencilMask = d.image(m, m.Dict, res, nil, depth+1)
		case Array:
			for _, v := range m {
				img.ColorKey = append(img.ColorKey, int(f.GetInt(v, 0)))
			}
		}
	}
	return img
}

// MaskImage returns the image that supplies this one's transparency, whether
// it came from /SMask or from a stencil /Mask. Both are drawn the same way:
// the mask clips, the image fills.
func (i *Image) MaskImage() *Image {
	if i.SMask != nil {
		return i.SMask
	}
	return i.StencilMask
}

// Dict returns the image dictionary.
func (i *Image) Dict() Dict { return i.dict }

// drawImage paints an image XObject.
func (ip *interp) drawImage(st *Stream) {
	img := ip.doc.image(st, st.Dict, ip.res, nil, 0)
	if img == nil {
		return
	}
	ip.paintImage(img)
}

func (ip *interp) paintImage(img *Image) {
	if ip.hidden > 0 {
		return
	}
	ctm := raster.Concat(raster.Matrix{A: 1, D: -1, F: 1}, ip.gs.ctm)
	bbox := ctm.ApplyRect(raster.Rect{X1: 1, Y1: 1})

	if mask := img.MaskImage(); mask != nil {
		d := ip.beginDraw(bbox)
		ip.dev.ClipImageMask(mask, ctm, bbox)
		ip.drawImageBody(img, ctm)
		ip.dev.PopClip()
		ip.endDraw(d)
		return
	}

	d := ip.beginDraw(bbox)
	ip.drawImageBody(img, ctm)
	ip.endDraw(d)
}

func (ip *interp) drawImageBody(img *Image, ctm raster.Matrix) {
	if !img.Mask {
		ip.dev.FillImage(img, ctm, ip.gs.fillAlpha, ip.gs.params)
		return
	}
	if ip.gs.fill.cs.IsPattern() {
		shape := ctm.ApplyRect(unitRect)
		ip.paintPattern(&ip.gs.fill, ip.gs.fillAlpha, shape, func() {
			ip.dev.ClipImageMask(img, ctm, shape)
		})
		return
	}
	ip.dev.FillImageMask(img, ctm, ip.gs.fill.cs, ip.gs.fill.value, ip.gs.fillAlpha, ip.gs.params)
}

// inlineImage reads BI ... ID ... EI. The binary data lies in the content
// stream itself, so the parser has to be steered around it by hand.
func (ip *interp) inlineImage(p *syntax.Parser) {
	dict := Dict{}
	var key Name
	for {
		obj, ok := p.Object()
		if !ok {
			ip.errorf("end of content stream inside an inline image")
			return
		}
		if kw, isKw := obj.(syntax.Keyword); isKw {
			if kw == "ID" {
				break
			}
			ip.errorf("unexpected %q in an inline image dictionary", string(kw))
			continue
		}
		if key == "" {
			if n, isName := obj.(Name); isName {
				key = n
			}
			continue
		}
		dict[key] = obj
		key = ""
	}

	l := p.Lexer()
	buf := l.Bytes()
	start := min(p.Pos()+len("ID"), len(buf))
	if start < len(buf) && isPDFSpace(buf[start]) {
		start++
	}

	end := min(max(ip.inlineImageEnd(dict, buf, start), start), len(buf))
	data := buf[start:end]

	img := ip.doc.image(nil, dict, ip.res, data, 0)
	if img != nil && ip.doc.optionalContentVisible(dict["OC"]) {
		ip.paintImage(img)
	}

	l.SetPos(end)
	p.Refill()
	for {
		obj, ok := p.Object()
		if !ok {
			return
		}
		if kw, isKw := obj.(syntax.Keyword); isKw && kw == "EI" {
			return
		}
	}
}

// inlineImageEnd finds where the data of an inline image stops.
func (ip *interp) inlineImageEnd(dict Dict, buf []byte, start int) int {
	f := ip.doc.f
	if n := f.GetInt(f.Lookup(dict, "Length", "L"), 0); n > 0 && start+int(n) <= len(buf) {
		return start + int(n)
	}
	if len(f.GetNames(f.Lookup(dict, "Filter", "F"))) == 0 {
		w := int(f.GetInt(f.Lookup(dict, "Width", "W"), 0))
		h := int(f.GetInt(f.Lookup(dict, "Height", "H"), 0))
		bpc := int(f.GetInt(f.Lookup(dict, "BitsPerComponent", "BPC"), 8))
		n := 1
		if f.GetBool(f.Lookup(dict, "ImageMask", "IM"), false) {
			bpc, n = 1, 1
		} else if cs := f.Lookup(dict, "ColorSpace", "CS"); cs != nil {
			n = ip.doc.colorSpace(cs, ip.res, 0).N
		}
		if w > 0 && h > 0 && bpc > 0 && n > 0 {
			size := ((w*n*bpc + 7) / 8) * h
			if start+size <= len(buf) {
				return start + size
			}
		}
	}
	for i := start; i+1 < len(buf); i++ {
		if buf[i] != 'E' || buf[i+1] != 'I' {
			continue
		}
		if i > start && !isPDFSpace(buf[i-1]) {
			continue
		}
		if i+2 < len(buf) && !isPDFSpace(buf[i+2]) {
			continue
		}
		return i - 1
	}
	ip.errorf("inline image has no EI")
	return len(buf)
}

func isPDFSpace(c byte) bool {
	switch c {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// maxImagePixels bounds what one image may decode to.
const maxImagePixels = 1 << 28

// Pixmap decodes the image into a pixmap in its own color space, with an
// alpha channel when it carries transparency. A stencil mask decodes to an
// alpha only pixmap, since it has no color of its own.
func (i *Image) Pixmap() (*raster.Pixmap, error) { return i.decode(i.CS, 0) }

// decode returns the image with its color converted into dst, which is the
// space the page composites in.
// decode returns the image reduced by shrink halvings, which is what the
// caller is going to draw it at. Where nothing has to touch the whole size
// pixmap the reduction happens as the samples are unpacked and it is never
// built; where transparency has to be hung on it first, it is built and
// reduced afterwards, because the alpha has to be averaged with the color it
// is premultiplied into rather than picked out of the middle of a block.
func (i *Image) decode(dst *ColorSpace, shrink int) (*raster.Pixmap, error) {
	if int64(i.Width)*int64(i.Height) > maxImagePixels {
		return nil, fmt.Errorf("%w: image is %dx%d", ErrUnsupported, i.Width, i.Height)
	}
	data, filter, parms, err := i.data()
	if err != nil {
		return nil, err
	}

	bpc, comps, cs := i.BPC, 1, i.CS
	if !i.Mask && cs != nil {
		comps = cs.N
	}
	decode := i.Decode
	var alpha []byte

	if filter != "" {
		f, ferr := decodeFiltered(filter, data, parms, i)
		if ferr != nil {
			return nil, ferr
		}
		data, bpc, alpha = f.pix, 8, f.alpha
		if f.cs != nil {
			cs = f.cs
		}
		if f.comps != comps && !i.Mask {
			i.doc.errorf("%s image has %d components, its color space %d", filter, f.comps, comps)
			comps = f.comps
		}
	}

	if i.Mask {
		return i.stencil(data, bpc, decode, shrink)
	}
	if cs == nil {
		return nil, fmt.Errorf("%w: image has no color space", ErrInvalid)
	}
	return i.samples(data, bpc, comps, decode, dst, cs, alpha, shrink)
}

// data returns the image bytes with the byte filters applied, and the image
// filter that is left.
func (i *Image) data() ([]byte, Name, Dict, error) {
	if i.stream != nil {
		return i.stream.ImageData()
	}
	var f *syntax.File
	if i.doc != nil {
		f = i.doc.f
	}
	return f.DecodeImageData(i.dict, i.inline)
}

// stencil decodes an /ImageMask into coverage: a sample of zero paints, and
// /Decode [1 0] turns that around.
func (i *Image) stencil(data []byte, bpc int, decode []float64, shrink int) (*raster.Pixmap, error) {
	sh := raster.NewMaskShrinker(i.Width, i.Height, shrink)
	if sh == nil {
		return nil, fmt.Errorf("%w: mask is %dx%d", ErrUnsupported, i.Width, i.Height)
	}
	on, off := uint8(255), uint8(0)
	if len(decode) >= 2 && decode[0] == 1 {
		on, off = off, on
	}
	rowBytes := (i.Width*bpc + 7) / 8
	for y := 0; y < i.Height; y++ {
		row := sh.Row()
		if row == nil {
			break
		}
		start := y * rowBytes
		if start >= len(data) {
			break
		}
		src := data[start:min(start+rowBytes, len(data))]
		for x := 0; x < i.Width; x++ {
			b := x >> 3
			if b >= len(src) {
				break
			}
			if src[b]&(0x80>>(x&7)) == 0 {
				row[x] = on
			} else {
				row[x] = off
			}
		}
		sh.Commit()
	}
	return sh.Pixmap(), nil
}

// samples unpacks the image and converts it, then hangs its transparency on
// the result.
func (i *Image) samples(data []byte, bpc, comps int, decode []float64, dst, cs *ColorSpace, opacity []byte, shrink int) (*raster.Pixmap, error) {
	if dst == nil {
		dst = DeviceRGB
	}
	alpha := i.SMask != nil || i.StencilMask != nil || i.ColorKey != nil || opacity != nil
	// An image with no transparency is finished the moment it is unpacked, so
	// it can be reduced on the way in. One with transparency is not.
	during := shrink
	if alpha {
		during = 0
	}
	sh := raster.NewShrinker(dst.Model(), i.Width, i.Height, alpha, during)
	if sh == nil {
		return nil, fmt.Errorf("%w: image is %dx%d", ErrUnsupported, i.Width, i.Height)
	}

	u := unpacker{
		img: i, data: data, bpc: bpc, comps: comps,
		decode: decode, dst: dst, cs: cs, px: sh.Pixmap(), sh: sh,
	}
	u.run()

	px := sh.Pixmap()
	if !alpha {
		return px, nil
	}
	i.transparency(px, opacity)
	premultiplyPixmap(px)
	return px.Subsample(shrink), nil
}

// unpacker turns packed samples into destination components.
type unpacker struct {
	img    *Image
	data   []byte
	bpc    int
	comps  int
	decode []float64
	dst    *ColorSpace
	// cs is the space the samples are in, which for a JPX image is what its
	// codestream says rather than what the dictionary does.
	cs *ColorSpace
	px *raster.Pixmap
	sh *raster.Shrinker
}

func (u *unpacker) run() {
	i, px := u.img, u.px
	n := px.Comps()
	rowBits := u.img.Width * u.comps * u.bpc
	rowBytes := (rowBits + 7) / 8
	maxVal := float32(uint32(1)<<uint(u.bpc)) - 1

	if lut := u.table(); lut != nil {
		for y := 0; y < i.Height; y++ {
			row := u.sh.Row()
			if row == nil {
				break
			}
			r := bitRow(u.data, y*rowBytes, rowBytes, u.bpc)
			for x := 0; x < i.Width; x++ {
				v := r.next()
				e := lut[int(v)*px.N:][:px.N:px.N]
				p := row[x*n:][:px.N:px.N]
				for c, s := range e {
					p[c] = s
				}
				if px.Alpha {
					row[x*n+px.N] = u.keyed(v)
				}
			}
			u.sh.Commit()
		}
		return
	}

	if u.direct() {
		for y := 0; y < i.Height; y++ {
			row := u.sh.Row()
			if row == nil {
				break
			}
			start := y * rowBytes
			if start >= len(u.data) {
				break
			}
			src := u.data[start:min(start+rowBytes, len(u.data))]
			w := min(i.Width, len(src)/u.comps)
			if !px.Alpha {
				// The samples are already the row, so it is one copy and
				// not one a pixel.
				copy(row[:w*n], src[:w*n])
				u.sh.Commit()
				continue
			}
			for x := 0; x < w; x++ {
				e := src[x*u.comps:][:u.comps:u.comps]
				p := row[x*n:][:px.N:px.N]
				for c, s := range e {
					p[c] = s
				}
				row[x*n+px.N] = u.keyedBytes(e)
			}
			u.sh.Commit()
		}
		return
	}

	c := make([]float32, u.comps)
	raw := make([]uint32, u.comps)
	for y := 0; y < i.Height; y++ {
		row := u.sh.Row()
		if row == nil {
			break
		}
		r := bitRow(u.data, y*rowBytes, rowBytes, u.bpc)
		for x := 0; x < i.Width; x++ {
			for j := range c {
				raw[j] = r.next()
				c[j] = u.value(j, raw[j], maxVal)
			}
			convertColor(u.cs, c, row[x*n:x*n+px.N])
			if px.Alpha {
				row[x*n+px.N] = u.keyedAll(raw)
			}
		}
		u.sh.Commit()
	}
}

// keyed reports the alpha of a sample that a one component color key may
// declare transparent.
func (u *unpacker) keyed(v uint32) uint8 {
	if len(u.img.ColorKey) < 2 {
		return 255
	}
	if int(v) >= u.img.ColorKey[0] && int(v) <= u.img.ColorKey[1] {
		return 0
	}
	return 255
}

func (u *unpacker) keyedBytes(s []uint8) uint8 {
	if len(u.img.ColorKey) < 2*len(s) {
		return 255
	}
	for j, v := range s {
		if int(v) < u.img.ColorKey[2*j] || int(v) > u.img.ColorKey[2*j+1] {
			return 255
		}
	}
	return 0
}

func (u *unpacker) keyedAll(raw []uint32) uint8 {
	if len(u.img.ColorKey) < 2*len(raw) {
		return 255
	}
	for j, v := range raw {
		if int(v) < u.img.ColorKey[2*j] || int(v) > u.img.ColorKey[2*j+1] {
			return 255
		}
	}
	return 0
}

// table builds the palette a one component image is converted through, one
// entry per possible sample.
func (u *unpacker) table() []uint8 {
	if u.comps != 1 || u.bpc > 8 {
		return nil
	}
	n := 1 << uint(u.bpc)
	maxVal := float32(n - 1)
	lut := make([]uint8, n*u.px.N)
	c := make([]float32, 1)
	for v := 0; v < n; v++ {
		c[0] = u.value(0, uint32(v), maxVal)
		convertColor(u.cs, c, lut[v*u.px.N:(v+1)*u.px.N])
	}
	return lut
}

// direct reports whether the samples are already the destination's own
// components, which is the ordinary case of an eight bit device image.
func (u *unpacker) direct() bool {
	return u.bpc == 8 && len(u.decode) == 0 && u.comps == u.px.N &&
		u.cs.Kind == u.dst.Kind &&
		(u.dst.Kind == KindGray || u.dst.Kind == KindRGB || u.dst.Kind == KindCMYK)
}

// value turns one raw sample into the component the color space expects,
// through the decode array when there is one.
func (u *unpacker) value(j int, s uint32, maxVal float32) float32 {
	lo, hi := float32(0), float32(1)
	if u.cs.Kind == KindIndexed {
		hi = maxVal
	} else if u.cs.Kind == KindLab {
		r := labRange(u.cs.Range)
		switch j {
		case 0:
			hi = 100
		case 1:
			lo, hi = r[0], r[1]
		case 2:
			lo, hi = r[2], r[3]
		}
	}
	if len(u.decode) >= 2*(j+1) {
		lo, hi = float32(u.decode[2*j]), float32(u.decode[2*j+1])
	}
	if maxVal <= 0 {
		return lo
	}
	return lo + float32(s)*(hi-lo)/maxVal
}

// bits reads samples of one row.
type bits struct {
	data []byte
	pos  int
	bit  uint
	bpc  int
}

func bitRow(data []byte, off, n, bpc int) *bits {
	if off > len(data) {
		off = len(data)
	}
	end := min(off+n, len(data))
	return &bits{data: data[off:end], bpc: bpc}
}

func (b *bits) next() uint32 {
	switch b.bpc {
	case 8:
		if b.pos >= len(b.data) {
			return 0
		}
		v := uint32(b.data[b.pos])
		b.pos++
		return v
	case 16:
		if b.pos+1 >= len(b.data) {
			b.pos = len(b.data)
			return 0
		}
		v := uint32(b.data[b.pos])<<8 | uint32(b.data[b.pos+1])
		b.pos += 2
		return v
	}
	var v uint32
	for i := 0; i < b.bpc; i++ {
		if b.pos >= len(b.data) {
			return v << uint(b.bpc-i)
		}
		v = v<<1 | uint32(b.data[b.pos]>>(7-b.bit)&1)
		b.bit++
		if b.bit == 8 {
			b.bit = 0
			b.pos++
		}
	}
	return v
}

// transparency hangs an /SMask or a stencil /Mask on a decoded image. Either
// may be a different size, so both are sampled rather than copied.
func (i *Image) transparency(px *raster.Pixmap, opacity []byte) {
	if !px.Alpha {
		return
	}
	if opacity != nil {
		n := px.Comps()
		for y := 0; y < px.H; y++ {
			row := px.Row(y)
			for x := 0; x < px.W; x++ {
				if p := y*px.W + x; p < len(opacity) {
					row[x*n+px.N] = opacity[p]
				}
			}
		}
		return
	}
	mask := i.SMask
	if mask == nil {
		mask = i.StencilMask
	}
	if mask == nil {
		return
	}
	m, err := mask.coverage()
	if err != nil || m == nil {
		i.doc.errorf("image mask: %v", err)
		return
	}
	n := px.Comps()
	for y := 0; y < px.H; y++ {
		row := px.Row(y)
		src := m.Row(y * m.H / px.H)
		for x := 0; x < px.W; x++ {
			row[x*n+px.N] = src[x*m.W/px.W]
		}
	}
}

// coverage decodes a mask image into one byte a pixel: a stencil mask as its
// one bit shape, a soft mask as its gray samples.
func (i *Image) coverage() (*raster.Pixmap, error) {
	px, err := i.decode(DeviceGray, 0)
	if err != nil {
		return nil, err
	}
	return flattenGray(px), nil
}

// flattenGray keeps the first component of every pixel, which is the coverage
// a mask carries when it has an alpha channel of its own.
func flattenGray(px *raster.Pixmap) *raster.Pixmap {
	n := px.Comps()
	if n == 1 {
		return px
	}
	out := raster.NewMask(px.W, px.H)
	if out == nil {
		return nil
	}
	for y := 0; y < px.H; y++ {
		row, dst := px.Row(y), out.Row(y)
		for x := 0; x < px.W; x++ {
			dst[x] = row[x*n]
		}
	}
	return out
}

// premultiplyPixmap folds the alpha channel into the color components, which
// is how a pixmap with alpha is stored.
func premultiplyPixmap(px *raster.Pixmap) {
	n := px.Comps()
	for y := 0; y < px.H; y++ {
		row := px.Row(y)
		for x := 0; x < px.W; x++ {
			p := row[x*n : x*n+n]
			a := p[px.N]
			if a == 255 {
				continue
			}
			for c := 0; c < px.N; c++ {
				p[c] = premultiply(p[c], a)
			}
		}
	}
}

// adobeTransform reads the transform an APP14 Adobe marker declares: 0 for
// CMYK, 2 for YCCK, and -1 when the marker is not there.
func adobeTransform(data []byte) int {
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xd8 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			i += 2
			continue
		}
		if marker == 0xda || marker == 0xd9 {
			return -1
		}
		n := int(data[i+2])<<8 | int(data[i+3])
		if marker == 0xee && n >= 12 && i+2+n <= len(data) &&
			string(data[i+4:i+9]) == "Adobe" {
			return int(data[i+2+n-1])
		}
		if n < 2 {
			return -1
		}
		i += 2 + n
	}
	return -1
}

// undoAdobe puts back the samples the JPEG stored, which image/jpeg has
// already turned into ink amounts.
func undoAdobe(pix []byte, transform int) {
	switch transform {
	case 0:
		for i := range pix {
			pix[i] = 255 - pix[i]
		}
	case 2:
		for i := 3; i < len(pix); i += 4 {
			pix[i] = 255 - pix[i]
		}
	}
}

// ImageDecoder turns the data of an image filter into interleaved eight bit
// samples, and reports the size and the number of components it produced.
type ImageDecoder func(data []byte, parms Dict) (pix []byte, w, h, comps int, err error)

var (
	decoderMu sync.RWMutex
	decoders  = map[Name]ImageDecoder{}
)

// RegisterImageDecoder installs a decoder for an image filter, taking
// precedence over the built in one. It is how a caller plugs in a JPEG
// decoder that reads what image/jpeg refuses, or a JPEG 2000 one.
func RegisterImageDecoder(filter Name, dec ImageDecoder) {
	decoderMu.Lock()
	defer decoderMu.Unlock()
	if dec == nil {
		delete(decoders, filter)
		return
	}
	decoders[filter] = dec
}

func imageDecoder(filter Name) ImageDecoder {
	decoderMu.RLock()
	defer decoderMu.RUnlock()
	return decoders[filter]
}

// filtered is what an image filter that produces pixels rather than bytes
// leaves behind. The color space and the opacity channel are the codestream's
// rather than the dictionary's, and neither is written back onto the Image,
// because two goroutines may be decoding the same one.
type filtered struct {
	pix   []byte
	comps int
	alpha []byte
	cs    *ColorSpace
}

// decodeFiltered runs the filter that produces pixels rather than bytes and
// returns eight bit samples.
func decodeFiltered(filter Name, data []byte, parms Dict, i *Image) (filtered, error) {
	var (
		out        filtered
		pix        []byte
		w, h, n    int
		err        error
		registered = imageDecoder(filter)
	)
	switch {
	case registered != nil:
		pix, w, h, n, err = registered(data, parms)
	case filter == "DCTDecode" || filter == "DCT":
		pix, w, h, n, err = jpegSamples(data)
	case filter == "JPXDecode":
		var img *jpxImage
		img, err = jpxDecode(data)
		if err == nil {
			pix, w, h, n = img.pix, img.width, img.height, img.comps
			pix, n, out.alpha, out.cs = i.adoptJPX(pix, w, h, n)
		}
	default:
		return out, fmt.Errorf("%w: %s image", ErrUnsupported, filter)
	}
	if err != nil {
		return out, fmt.Errorf("%s: %w", filter, err)
	}
	if w != i.Width || h != i.Height {
		i.doc.errorf("%s image is %dx%d, its dictionary says %dx%d", filter, w, h, i.Width, i.Height)
		pix = refit(pix, w, h, i.Width, i.Height, n)
	}
	out.pix, out.comps = pix, n
	return out, nil
}

// refit lays samples of one size into a buffer of another, which is what a
// file that lies about its image size gets: the overlap is kept and the rest
// is left at zero, as the other readers pad it.
func refit(pix []byte, w, h, dw, dh, n int) []byte {
	if dw <= 0 || dh <= 0 || int64(dw)*int64(dh) > maxImagePixels {
		return pix
	}
	out := make([]byte, dw*dh*n)
	for y := 0; y < min(h, dh); y++ {
		copy(out[y*dw*n:(y+1)*dw*n], pix[y*w*n:min((y+1)*w*n, len(pix))])
	}
	return out
}

// tightRows returns rows of n bytes a pixel with no padding between them,
// which is the decoder's own buffer whenever it has none to begin with.
func tightRows(pix []byte, stride, w, h, n int) []byte {
	if stride == w*n && len(pix) >= w*h*n {
		return pix[:w*h*n]
	}
	out := make([]byte, w*h*n)
	for y := 0; y < h; y++ {
		copy(out[y*w*n:], pix[y*stride:y*stride+w*n])
	}
	return out
}

// jpegSamples decodes a JPEG into interleaved samples of its own components.
// Gray stays gray, CMYK stays CMYK, and everything else becomes RGB, because
// those are the three shapes a PDF color space can name.
func jpegSamples(data []byte) ([]byte, int, int, int, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxImagePixels {
		return nil, 0, 0, 0, fmt.Errorf("%w: JPEG is %dx%d", ErrUnsupported, w, h)
	}

	switch m := img.(type) {
	case *image.Gray:
		return tightRows(m.Pix, m.Stride, w, h, 1), w, h, 1, nil

	case *image.CMYK:
		pix := tightRows(m.Pix, m.Stride, w, h, 4)
		undoAdobe(pix, adobeTransform(data))
		return pix, w, h, 4, nil

	case *image.YCbCr:
		pix := make([]byte, w*h*3)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				yi := m.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := m.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := stdcolor.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
				o := (y*w + x) * 3
				pix[o], pix[o+1], pix[o+2] = r, g, bl
			}
		}
		return pix, w, h, 3, nil
	}

	pix := make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := (y*w + x) * 3
			pix[o], pix[o+1], pix[o+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
		}
	}
	return pix, w, h, 3, nil
}

// imageKey identifies a decoded image: the stream it came from and the shape
// it was decoded into.
type imageKey struct {
	stream *syntax.Stream
	comps  int
	// shrink is how many times the decoded pixmap was halved. It belongs in
	// the key because the cache holds the shrunk pixmap, and a page that draws
	// one image at two sizes wants both.
	shrink int
}

type imageEntry struct {
	key        imageKey
	px         *raster.Pixmap
	size       int
	prev, next *imageEntry
}

// DefaultImageCacheBytes is how much of a document's decoded images are kept
// when nothing says otherwise.
const DefaultImageCacheBytes = 1 << 26

// decodedImage decodes an image into dst's components, or into a stencil when
// dst is nil, keeping the result while it fits in the cache. Inline images
// are not cached: their data lives in the content stream and is drawn once.
func (d *Document) decodedImage(img *Image, dst *ColorSpace, shrink int) (*raster.Pixmap, error) {
	if img.stream == nil {
		return img.decode(dst, shrink)
	}
	key := imageKey{stream: img.stream, shrink: shrink}
	if dst != nil {
		key.comps = dst.N
	}
	d.mu.Lock()
	e := d.images[key]
	if e != nil {
		d.imageUnlink(e)
		d.imageLink(e)
	}
	d.mu.Unlock()
	if e != nil {
		return e.px, nil
	}

	// Decoding happens outside the lock: it is the slowest thing a page does
	// and it depends on nothing the cache holds, so two pages that want the
	// same image at once both decode it and one of the two is kept.
	px, err := img.decode(dst, shrink)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	if e := d.images[key]; e != nil {
		px = e.px
	} else {
		d.cacheImage(key, px)
	}
	d.mu.Unlock()
	return px, nil
}

func (d *Document) cacheImage(key imageKey, px *raster.Pixmap) {
	max := d.ImageCacheBytes
	if max == 0 {
		max = DefaultImageCacheBytes
	}
	size := len(px.Samples) + 64
	if max < 0 || size > max {
		return
	}
	if d.images == nil {
		d.images = map[imageKey]*imageEntry{}
	}
	e := &imageEntry{key: key, px: px, size: size}
	d.images[key] = e
	d.imageLink(e)
	d.imageBytes += size
	for d.imageBytes > max && d.imageTail != nil {
		old := d.imageTail
		d.imageUnlink(old)
		d.imageBytes -= old.size
		delete(d.images, old.key)
	}
}

func (d *Document) imageLink(e *imageEntry) {
	e.prev, e.next = nil, d.imageHead
	if d.imageHead != nil {
		d.imageHead.prev = e
	}
	d.imageHead = e
	if d.imageTail == nil {
		d.imageTail = e
	}
}

func (d *Document) imageUnlink(e *imageEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		d.imageHead = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		d.imageTail = e.prev
	}
	e.prev, e.next = nil, nil
}

// adoptJPX gives a JPX image the color space and the transparency the
// codestream carries rather than the dictionary, which ISO 32000-1 7.4.9 says
// is what a reader must do when the dictionary is silent or disagrees.
func (i *Image) adoptJPX(pix []byte, w, h, n int) ([]byte, int, []byte, *ColorSpace) {
	smask := i.smaskInData()
	cs := i.CS
	color := n
	switch {
	case cs != nil && cs.N <= n:
		color = cs.N
	case cs == nil && (n == 2 || n == 4) && smask != 0:
		color = n - 1
	}
	var alpha []byte
	if color < n && w*h*n <= len(pix) {
		out := make([]byte, w*h*color)
		if smask != 0 {
			alpha = make([]byte, w*h)
		}
		for p := 0; p < w*h; p++ {
			copy(out[p*color:], pix[p*n:p*n+color])
			if alpha != nil {
				alpha[p] = pix[p*n+color]
			}
		}
		pix, n = out, color
	}
	if cs == nil || cs.N != n {
		switch n {
		case 1:
			cs = DeviceGray
		case 3:
			cs = DeviceRGB
		case 4:
			cs = DeviceCMYK
		}
	}
	return pix, n, alpha, cs
}

// codestreamColorSpace is what a JPX image with no /ColorSpace paints in,
// which is the component count of its codestream less any opacity channel.
func (i *Image) codestreamColorSpace() *ColorSpace {
	data, filter, _, err := i.data()
	if err != nil || filter != "JPXDecode" {
		return nil
	}
	n := jpxComponents(data)
	if (n == 2 || n == 4) && i.smaskInData() != 0 {
		n--
	}
	switch n {
	case 1:
		return DeviceGray
	case 3:
		return DeviceRGB
	case 4:
		return DeviceCMYK
	}
	return nil
}

func (i *Image) smaskInData() int64 {
	if i.doc == nil {
		return 0
	}
	return i.doc.f.GetInt(i.doc.f.Lookup(i.dict, "SMaskInData"), 0)
}
