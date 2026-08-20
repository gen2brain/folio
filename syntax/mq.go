package syntax

// The MQ arithmetic decoder, ITU-T T.88 Annex E and T.800 Annex C, shared by
// JBIG2 and JPEG 2000. Ported from pdf.js.

type mqState struct {
	qe         uint32
	nmps, nlps uint8
	switchMPS  uint8
}

var mqTable = [47]mqState{
	{0x5601, 1, 1, 1},
	{0x3401, 2, 6, 0},
	{0x1801, 3, 9, 0},
	{0x0ac1, 4, 12, 0},
	{0x0521, 5, 29, 0},
	{0x0221, 38, 33, 0},
	{0x5601, 7, 6, 1},
	{0x5401, 8, 14, 0},
	{0x4801, 9, 14, 0},
	{0x3801, 10, 14, 0},
	{0x3001, 11, 17, 0},
	{0x2401, 12, 18, 0},
	{0x1c01, 13, 20, 0},
	{0x1601, 29, 21, 0},
	{0x5601, 15, 14, 1},
	{0x5401, 16, 14, 0},
	{0x5101, 17, 15, 0},
	{0x4801, 18, 16, 0},
	{0x3801, 19, 17, 0},
	{0x3401, 20, 18, 0},
	{0x3001, 21, 19, 0},
	{0x2801, 22, 19, 0},
	{0x2401, 23, 20, 0},
	{0x2201, 24, 21, 0},
	{0x1c01, 25, 22, 0},
	{0x1801, 26, 23, 0},
	{0x1601, 27, 24, 0},
	{0x1401, 28, 25, 0},
	{0x1201, 29, 26, 0},
	{0x1101, 30, 27, 0},
	{0x0ac1, 31, 28, 0},
	{0x09c1, 32, 29, 0},
	{0x08a1, 33, 30, 0},
	{0x0521, 34, 31, 0},
	{0x0441, 35, 32, 0},
	{0x02a1, 36, 33, 0},
	{0x0221, 37, 34, 0},
	{0x0141, 38, 35, 0},
	{0x0111, 39, 36, 0},
	{0x0085, 40, 37, 0},
	{0x0049, 41, 38, 0},
	{0x0025, 42, 39, 0},
	{0x0015, 43, 40, 0},
	{0x0009, 44, 41, 0},
	{0x0005, 45, 42, 0},
	{0x0001, 45, 43, 0},
	{0x5601, 46, 46, 0},
}

// MQ decodes a decision at a time against a caller supplied context array, in
// which each entry packs the state index in its high seven bits and the more
// probable symbol in its low one. JBIG2 and JPEG 2000 both read it.
type MQ struct {
	data        []byte
	bp, end     int
	chigh, clow uint32
	ct          int32
	a           uint32
}

// NewMQ returns a decoder over data[start:end].
func NewMQ(data []byte, start, end int) *MQ {
	if start < 0 {
		start = 0
	}
	if end > len(data) {
		end = len(data)
	}
	d := &MQ{data: data, bp: start, end: end}
	d.chigh = d.byteAt(start)
	d.byteIn()
	d.chigh = ((d.chigh << 7) & 0xffff) | ((d.clow >> 9) & 0x7f)
	d.clow = (d.clow << 7) & 0xffff
	d.ct -= 7
	d.a = 0x8000
	return d
}

// byteAt reads past the end as a marker byte, which stops the decoder rather
// than letting it walk into whatever follows.
func (d *MQ) byteAt(i int) uint32 {
	if i >= 0 && i < d.end {
		return uint32(d.data[i])
	}
	return 0xff
}

func (d *MQ) byteIn() {
	if d.byteAt(d.bp) == 0xff {
		if d.byteAt(d.bp+1) > 0x8f {
			d.clow += 0xff00
			d.ct = 8
		} else {
			d.bp++
			d.clow += d.byteAt(d.bp) << 9
			d.ct = 7
		}
	} else {
		d.bp++
		d.clow += d.byteAt(d.bp) << 8
		d.ct = 8
	}
	if d.clow > 0xffff {
		d.chigh += d.clow >> 16
		d.clow &= 0xffff
	}
}

// ReadBit decodes one decision against contexts[pos].
func (d *MQ) ReadBit(contexts []uint8, pos uint32) int {
	if int(pos) >= len(contexts) {
		return 0
	}
	index := contexts[pos] >> 1
	mps := contexts[pos] & 1
	st := &mqTable[index]
	qe := st.qe
	a := d.a - qe

	var bit uint8
	if d.chigh < qe {
		if a < qe {
			a = qe
			bit = mps
			index = st.nmps
		} else {
			a = qe
			bit = 1 ^ mps
			if st.switchMPS == 1 {
				mps = bit
			}
			index = st.nlps
		}
	} else {
		d.chigh -= qe
		if a&0x8000 != 0 {
			d.a = a
			return int(mps)
		}
		if a < qe {
			bit = 1 ^ mps
			if st.switchMPS == 1 {
				mps = bit
			}
			index = st.nlps
		} else {
			bit = mps
			index = st.nmps
		}
	}
	for {
		if d.ct == 0 {
			d.byteIn()
		}
		a <<= 1
		d.chigh = ((d.chigh << 1) & 0xffff) | ((d.clow >> 15) & 1)
		d.clow = (d.clow << 1) & 0xffff
		d.ct--
		if a&0x8000 != 0 {
			break
		}
	}
	d.a = a
	contexts[pos] = index<<1 | mps
	return int(bit)
}
