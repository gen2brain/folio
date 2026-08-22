package html

import "encoding/binary"

const (
	huffHeader  = 24
	cdicHeader  = 16
	huffMinSize = 2584
	huffMaxRecs = 1024
	codeTable   = 33
	maxCodeLen  = 16
	maxDepth    = 20
)

// huffcdic is the dictionary a HUFF/CDIC book is coded with: the code tables
// of the HUFF record and the phrases of the CDIC records after it.
type huffcdic struct {
	table1  [256]uint32
	minCode [codeTable]uint32
	maxCode [codeTable]uint32
	offsets []uint16
	symbols [][]byte
	codeLen uint
	count   int
	read    int
}

// readHuffcdic reads the HUFF record at huff and the n-1 CDIC records after
// it.
func readHuffcdic(recs [][]byte, huff, n int) *huffcdic {
	if huff <= 0 || n < 2 || n > huffMaxRecs || huff+n > len(recs) {
		return nil
	}
	h := &huffcdic{}
	if !h.readHUFF(recs[huff]) {
		return nil
	}
	h.symbols = make([][]byte, 0, n-1)
	for i := 1; i < n; i++ {
		if !h.readCDIC(recs[huff+i]) {
			return nil
		}
	}
	if h.count != h.read {
		return nil
	}
	return h
}

// readHUFF reads the 256 entries that answer the top byte of a code and the
// 32 pairs bounding the codes of each length.
func (h *huffcdic) readHUFF(rec []byte) bool {
	if len(rec) < huffMinSize || string(rec[:4]) != "HUFF" {
		return false
	}
	if int(binary.BigEndian.Uint32(rec[4:])) < huffHeader {
		return false
	}
	d1 := int(binary.BigEndian.Uint32(rec[8:]))
	d2 := int(binary.BigEndian.Uint32(rec[12:]))
	if d1 < 0 || d1+256*4 > len(rec) || d2 < 0 || d2+64*4 > len(rec) {
		return false
	}
	for i := range h.table1 {
		h.table1[i] = binary.BigEndian.Uint32(rec[d1+4*i:])
	}
	h.minCode[0], h.maxCode[0] = 0, 0xffffffff
	for i := 1; i < codeTable; i++ {
		lo := binary.BigEndian.Uint32(rec[d2+8*(i-1):])
		hi := binary.BigEndian.Uint32(rec[d2+8*(i-1)+4:])
		h.minCode[i] = lo << (32 - uint(i))
		h.maxCode[i] = ((hi + 1) << (32 - uint(i))) - 1
	}
	return true
}

// readCDIC reads one record of phrases: where each sits, then the phrases.
// Every record declares the same total and the same index width.
func (h *huffcdic) readCDIC(rec []byte) bool {
	if len(rec) < cdicHeader || string(rec[:4]) != "CDIC" {
		return false
	}
	if int(binary.BigEndian.Uint32(rec[4:])) < cdicHeader {
		return false
	}
	count := int(binary.BigEndian.Uint32(rec[8:]))
	length := uint(binary.BigEndian.Uint32(rec[12:]))
	if length == 0 || length > maxCodeLen || count <= 0 {
		return false
	}
	if h.codeLen != 0 && (h.codeLen != length || h.count != count) {
		return false
	}
	h.codeLen, h.count = length, count
	if h.offsets == nil {
		if count > (1<<maxCodeLen)*huffMaxRecs {
			return false
		}
		h.offsets = make([]uint16, 0, count)
	}
	n := min(count-h.read, 1<<length)
	if n < 0 || cdicHeader+2*n > len(rec) {
		return false
	}
	for i := 0; i < n; i++ {
		off := int(binary.BigEndian.Uint16(rec[cdicHeader+2*i:]))
		at := cdicHeader + off
		if at+2 > len(rec) {
			return false
		}
		if at+2+int(binary.BigEndian.Uint16(rec[at:])&0x7fff) > len(rec) {
			return false
		}
		h.offsets = append(h.offsets, uint16(off))
	}
	h.read += n
	h.symbols = append(h.symbols, rec[cdicHeader:])
	return true
}

// unpack decompresses one record onto out. A code names a phrase, and a
// phrase not marked as text is more codes.
func (h *huffcdic) unpack(out, in []byte, depth int) []byte {
	if depth > maxDepth {
		return out
	}
	bits := len(in) * 8
	count, pos := 32, 0
	buf := fill64(in, pos)
	pos += 4
	for {
		if count <= 0 {
			count += 32
			buf = fill64(in, pos)
			pos += 4
		}
		code := uint32(buf >> uint(count))
		t := h.table1[code>>24]
		n := uint(t & 0x1f)
		max := ((t>>8)+1)<<(32-n) - 1
		if t&0x80 == 0 {
			for n < codeTable && code < h.minCode[n] {
				n++
			}
			if n >= codeTable {
				return out
			}
			max = h.maxCode[n]
		}
		// A code of no length would consume no bits and never end.
		if n == 0 || n > 32 {
			return out
		}
		count -= int(n)
		bits -= int(n)
		if bits < 0 {
			return out
		}
		i := int((max - code) >> (32 - n))
		if i < 0 || i >= h.read {
			return out
		}
		rec := i >> h.codeLen
		if rec >= len(h.symbols) {
			return out
		}
		sym := h.symbols[rec]
		at := int(h.offsets[i])
		if at+2 > len(sym) {
			return out
		}
		size := binary.BigEndian.Uint16(sym[at:])
		text := size&0x8000 != 0
		size &= 0x7fff
		if at+2+int(size) > len(sym) {
			return out
		}
		phrase := sym[at+2 : at+2+int(size)]
		if text {
			out = append(out, phrase...)
			continue
		}
		out = h.unpack(out, phrase, depth+1)
	}
}

// fill64 reads eight bytes big-endian, padding past the end with zeroes. The
// reader steps four at a time, so each read overlaps the one before.
func fill64(b []byte, at int) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v <<= 8
		if at+i < len(b) {
			v |= uint64(b[at+i])
		}
	}
	return v
}
