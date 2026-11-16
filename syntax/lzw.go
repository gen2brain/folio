package syntax

import (
	"compress/flate"
	"io"
)

func newRawInflater(r io.Reader) io.ReadCloser { return flate.NewReader(r) }

// lzwDecode implements the LZW variant PDF uses: MSB-first packing, 8-bit
// literals, and the early code width change that /EarlyChange turns off.
func lzwDecode(data []byte, early bool, limit int) ([]byte, error) {
	d := newLZW(data, early)
	out := make([]byte, 0, len(data)*3)
	for {
		s, err := d.step()
		if err != nil {
			return out, err
		}
		if s == nil {
			return out, nil
		}
		out = append(out, s...)
		if len(out) >= limit && limit < maxDecoded {
			return out[:limit], nil
		}
		if len(out) > maxDecoded {
			return out, errInvalidf("lzw: output exceeds %d bytes", maxDecoded)
		}
	}
}

const (
	lzwClear = 256
	lzwEOD   = 257
	lzwFirst = 258
	lzwMax   = 1 << 12
)

// lzwDecoder decodes an LZW stream a code at a time.
type lzwDecoder struct {
	data     []byte
	prefix   [lzwMax]uint16
	suffix   [lzwMax]uint8
	stack    [lzwMax + 1]byte
	next     int
	width    int
	prev     int
	earlyBit int
	bitBuf   uint32
	bitCnt   int
	pos      int
}

func newLZW(data []byte, early bool) *lzwDecoder {
	d := &lzwDecoder{data: data, next: lzwFirst, width: 9, prev: -1}
	if early {
		d.earlyBit = 1
	}
	return d
}

// step returns the bytes the next code stands for, valid until the next
// step, and nil at the end of the stream.
func (d *lzwDecoder) step() ([]byte, error) {
	for {
		for d.bitCnt < d.width {
			if d.pos >= len(d.data) {
				return nil, nil
			}
			d.bitBuf = d.bitBuf<<8 | uint32(d.data[d.pos])
			d.pos++
			d.bitCnt += 8
		}
		code := int(d.bitBuf>>(uint(d.bitCnt-d.width))) & (1<<d.width - 1)
		d.bitCnt -= d.width

		switch {
		case code == lzwEOD:
			return nil, nil
		case code == lzwClear:
			d.next = lzwFirst
			d.width = 9
			d.prev = -1
			continue
		}

		stack := d.stack[:]
		top := len(stack)
		cur := code
		kwkwk := code >= d.next
		if kwkwk {
			if d.prev < 0 {
				return nil, errInvalidf("lzw: code %d before any literal", code)
			}
			top--
			cur = d.prev
		}
		for cur >= lzwFirst {
			if top == 0 {
				return nil, errInvalidf("lzw: dictionary loop at code %d", code)
			}
			top--
			stack[top] = d.suffix[cur]
			cur = int(d.prefix[cur])
		}
		if top == 0 {
			return nil, errInvalidf("lzw: dictionary loop at code %d", code)
		}
		top--
		stack[top] = byte(cur)
		if kwkwk {
			stack[len(stack)-1] = byte(cur)
		}
		if d.prev >= 0 && d.next < lzwMax {
			d.prefix[d.next] = uint16(d.prev)
			d.suffix[d.next] = stack[top]
			d.next++
		}
		d.prev = code
		if d.next+d.earlyBit >= 1<<d.width && d.width < 12 {
			d.width++
		}
		return stack[top:], nil
	}
}

// lzwReader decodes as lzwDecode does, a read at a time.
type lzwReader struct {
	d     *lzwDecoder
	f     *File
	limit int
	n     int
	pend  []byte
	done  bool
}

func (r *lzwReader) Read(p []byte) (int, error) {
	for len(r.pend) == 0 {
		if r.done {
			return 0, io.EOF
		}
		s, err := r.d.step()
		if err != nil {
			r.f.errorf("%v", err)
			r.done = true
			continue
		}
		if s == nil {
			r.done = true
			continue
		}
		if r.limit < maxDecoded && r.n+len(s) >= r.limit {
			s = s[:r.limit-r.n]
			r.done = true
		}
		r.n += len(s)
		if r.n > maxDecoded {
			r.f.errorf("%v", errInvalidf("lzw: output exceeds %d bytes", maxDecoded))
			r.done = true
		}
		r.pend = s
	}
	k := copy(p, r.pend)
	r.pend = r.pend[k:]
	return k, nil
}

func (r *lzwReader) Close() error { return nil }

// BitReader reads MSB-first fields.
type BitReader struct {
	buf []byte
	pos int
}

// NewBitReader reads from buf.
func NewBitReader(buf []byte) *BitReader { return &BitReader{buf: buf} }

// Read returns the next n bits, zero filled past the end of the buffer.
func (b *BitReader) Read(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		byteIdx := b.pos >> 3
		if byteIdx >= len(b.buf) {
			return v << uint(n-i)
		}
		bit := (b.buf[byteIdx] >> uint(7-b.pos&7)) & 1
		v = v<<1 | uint32(bit)
		b.pos++
	}
	return v
}

type bitWriter struct {
	buf  []byte
	cur  byte
	nbit int
}

func (b *bitWriter) write(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		b.cur = b.cur<<1 | byte(v>>uint(i)&1)
		b.nbit++
		if b.nbit == 8 {
			b.buf = append(b.buf, b.cur)
			b.cur, b.nbit = 0, 0
		}
	}
}

func (b *bitWriter) flush() {
	if b.nbit > 0 {
		b.buf = append(b.buf, b.cur<<uint(8-b.nbit))
		b.cur, b.nbit = 0, 0
	}
}
