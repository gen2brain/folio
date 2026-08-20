package syntax

import (
	"compress/flate"
	"io"
)

func newRawInflater(r io.Reader) io.ReadCloser { return flate.NewReader(r) }

// lzwDecode implements the LZW variant PDF uses: MSB-first packing, 8-bit
// literals, and the early code width change that /EarlyChange turns off.
func lzwDecode(data []byte, early bool) ([]byte, error) {
	const (
		clearCode = 256
		eodCode   = 257
		firstCode = 258
		maxCode   = 1 << 12
	)
	var (
		prefix [maxCode]uint16
		suffix [maxCode]uint8
		stack  [maxCode + 1]byte
	)
	out := make([]byte, 0, len(data)*3)

	next := firstCode
	width := 9
	prev := -1
	earlyBit := 0
	if early {
		earlyBit = 1
	}

	var (
		bitBuf  uint32
		bitCnt  int
		pos     int
		lastErr error
	)
	for {
		for bitCnt < width {
			if pos >= len(data) {
				return out, lastErr
			}
			bitBuf = bitBuf<<8 | uint32(data[pos])
			pos++
			bitCnt += 8
		}
		code := int(bitBuf>>(uint(bitCnt-width))) & (1<<width - 1)
		bitCnt -= width

		switch {
		case code == eodCode:
			return out, lastErr
		case code == clearCode:
			next = firstCode
			width = 9
			prev = -1
			continue
		}

		top := len(stack)
		cur := code
		kwkwk := code >= next
		if kwkwk {
			if prev < 0 {
				return out, errInvalidf("lzw: code %d before any literal", code)
			}
			top--
			cur = prev
		}
		for cur >= firstCode {
			if top == 0 {
				return out, errInvalidf("lzw: dictionary loop at code %d", code)
			}
			top--
			stack[top] = suffix[cur]
			cur = int(prefix[cur])
		}
		if top == 0 {
			return out, errInvalidf("lzw: dictionary loop at code %d", code)
		}
		top--
		stack[top] = byte(cur)
		if kwkwk {
			stack[len(stack)-1] = byte(cur)
		}
		out = append(out, stack[top:]...)
		if len(out) > maxDecoded {
			return out, errInvalidf("lzw: output exceeds %d bytes", maxDecoded)
		}

		if prev >= 0 && next < maxCode {
			prefix[next] = uint16(prev)
			suffix[next] = stack[top]
			next++
		}
		prev = code
		if next+earlyBit >= 1<<width && width < 12 {
			width++
		}
	}
}

// BitReader reads MSB-first fields, which is how every packed value in PDF is
// stored: image samples, predictor components, function samples.
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
