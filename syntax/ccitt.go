package syntax

// CCITT Group 3 and Group 4 fax decoding, ITU-T T.4 and T.6, ported from
// pdf.js, which ported it from XPDF. The output is packed one bit rows in the
// convention /BlackIs1 describes: a zero bit is black unless it says
// otherwise, which is what an image or a stencil mask expects.

const (
	ccittEOF     = -1
	twoDimPass   = 0
	twoDimHoriz  = 1
	twoDimVert0  = 2
	twoDimVertR1 = 3
	twoDimVertL1 = 4
	twoDimVertR2 = 5
	twoDimVertL2 = 6
	twoDimVertR3 = 7
	twoDimVertL3 = 8
)

type ccitt struct {
	src  []byte
	pos  int
	read func() int

	encoding  int
	eoline    bool
	byteAlign bool
	columns   int
	rows      int
	eoblock   bool
	black     bool

	codingLine []uint32
	refLine    []uint32
	codingPos  int

	row        int
	nextLine2D bool
	inputBits  int
	inputBuf   uint32
	outputBits int
	rowsDone   bool
	eof        bool
	err        bool
}

// ccittFaxDecode expands a CCITT encoded stream into packed one bit rows.
func ccittFaxDecode(f *File, data []byte, parms Dict) ([]byte, error) {
	c := &ccitt{
		src:       data,
		columns:   1728,
		eoblock:   true,
		encoding:  int(f.GetInt(parms["K"], 0)),
		eoline:    f.GetBool(parms["EndOfLine"], false),
		byteAlign: f.GetBool(parms["EncodedByteAlign"], false),
		rows:      int(f.GetInt(parms["Rows"], 0)),
		black:     f.GetBool(parms["BlackIs1"], false),
	}
	if n := int(f.GetInt(parms["Columns"], 1728)); n > 0 {
		c.columns = n
	}
	if v := f.Lookup(parms, "EndOfBlock"); v != nil {
		c.eoblock = f.GetBool(v, true)
	}
	if c.columns > 1<<20 || c.rows < 0 {
		return nil, errInvalidf("CCITT is %d columns", c.columns)
	}
	c.start()

	rowBytes := (c.columns + 7) / 8
	limit := maxDecoded
	if c.rows > 0 {
		limit = min(limit, c.rows*rowBytes)
	}
	out := make([]byte, 0, min(limit, 1<<20))
	for len(out) < limit {
		v := c.readNextChar()
		if v < 0 {
			break
		}
		out = append(out, byte(v))
	}
	if c.rows > 0 {
		for len(out) < c.rows*rowBytes {
			out = append(out, 0xff)
		}
	}
	return out, nil
}

func (c *ccitt) start() {
	c.codingLine = make([]uint32, c.columns+1)
	c.refLine = make([]uint32, c.columns+2)
	c.codingLine[0] = uint32(c.columns)
	c.nextLine2D = c.encoding < 0

	var code1 int
	for {
		code1 = c.lookBits(12)
		if code1 != 0 {
			break
		}
		c.eatBits(1)
	}
	if code1 == 1 {
		c.eatBits(12)
	}
	if c.encoding > 0 {
		c.nextLine2D = c.lookBits(1) == 0
		c.eatBits(1)
	}
}

func (c *ccitt) next() int {
	if c.read != nil {
		return c.read()
	}
	if c.pos >= len(c.src) {
		return -1
	}
	v := int(c.src[c.pos])
	c.pos++
	return v
}

// readNextChar returns the next eight pixels, or -1 at the end.
func (c *ccitt) readNextChar() int {
	if c.eof {
		return -1
	}
	columns := uint32(c.columns)

	if c.outputBits == 0 {
		if c.rowsDone {
			c.eof = true
		}
		if c.eof {
			return -1
		}
		c.err = false

		if c.nextLine2D {
			c.decode2D(columns)
		} else {
			c.decode1D(columns)
		}

		gotEOL := false
		if c.byteAlign {
			c.inputBits &^= 7
		}

		if !c.eoblock && c.row == c.rows-1 {
			c.rowsDone = true
		} else {
			code1 := c.lookBits(12)
			if c.eoline {
				for code1 != ccittEOF && code1 != 1 {
					c.eatBits(1)
					code1 = c.lookBits(12)
				}
			} else {
				for code1 == 0 {
					c.eatBits(1)
					code1 = c.lookBits(12)
				}
			}
			switch code1 {
			case 1:
				c.eatBits(12)
				gotEOL = true
			case ccittEOF:
				c.eof = true
			}
		}

		if !c.eof && c.encoding > 0 && !c.rowsDone {
			c.nextLine2D = c.lookBits(1) == 0
			c.eatBits(1)
		}

		if c.eoblock && gotEOL && c.byteAlign {
			if c.lookBits(12) == 1 {
				c.eatBits(12)
				if c.encoding > 0 {
					c.lookBits(1)
					c.eatBits(1)
				}
				if c.encoding >= 0 {
					for i := 0; i < 4; i++ {
						c.lookBits(12)
						c.eatBits(12)
						if c.encoding > 0 {
							c.lookBits(1)
							c.eatBits(1)
						}
					}
				}
				c.eof = true
			}
		} else if c.err && c.eoline {
			for {
				code1 := c.lookBits(13)
				if code1 == ccittEOF {
					c.eof = true
					return -1
				}
				if code1>>1 == 1 {
					c.eatBits(12)
					if c.encoding > 0 {
						c.eatBits(1)
						c.nextLine2D = code1&1 == 0
					}
					break
				}
				c.eatBits(1)
			}
		}

		if c.codingLine[0] > 0 {
			c.codingPos = 0
			c.outputBits = int(c.codingLine[0])
		} else {
			c.codingPos = 1
			c.outputBits = int(c.codingLine[1])
		}
		c.row++
	}

	var v int
	if c.outputBits >= 8 {
		if c.codingPos&1 != 0 {
			v = 0
		} else {
			v = 0xff
		}
		c.outputBits -= 8
		if c.outputBits == 0 && c.codingLine[c.codingPos] < columns {
			c.codingPos++
			c.outputBits = int(c.codingLine[c.codingPos] - c.codingLine[c.codingPos-1])
		}
	} else {
		bits := 8
		for bits > 0 {
			if c.outputBits > bits {
				v <<= uint(bits)
				if c.codingPos&1 == 0 {
					v |= 0xff >> uint(8-bits)
				}
				c.outputBits -= bits
				bits = 0
			} else {
				v <<= uint(c.outputBits)
				if c.codingPos&1 == 0 {
					v |= 0xff >> uint(8-c.outputBits)
				}
				bits -= c.outputBits
				c.outputBits = 0
				if c.codingLine[c.codingPos] < columns {
					c.codingPos++
					c.outputBits = int(c.codingLine[c.codingPos] - c.codingLine[c.codingPos-1])
				} else if bits > 0 {
					v <<= uint(bits)
					bits = 0
				}
			}
		}
	}
	if c.black {
		v ^= 0xff
	}
	return v & 0xff
}

// decode1D reads a one dimensional line, a run of white and black lengths.
func (c *ccitt) decode1D(columns uint32) {
	c.codingLine[0] = 0
	c.codingPos = 0
	black := 0
	for c.codingLine[c.codingPos] < columns {
		run, code := 0, 0
		for {
			if black != 0 {
				code = c.blackCode()
			} else {
				code = c.whiteCode()
			}
			run += code
			if code < 64 {
				break
			}
		}
		c.addPixels(c.codingLine[c.codingPos]+uint32(run), black)
		black ^= 1
	}
}

// ref reads the reference line, which a stream that codes more changing
// elements than a row can hold will index past its two sentinels.
func (c *ccitt) ref(i int) uint32 {
	if i < 0 || i >= len(c.refLine) {
		return uint32(c.columns)
	}
	return c.refLine[i]
}

// decode2D reads a line against the one above it.
func (c *ccitt) decode2D(columns uint32) {
	refLine, codingLine := c.refLine, c.codingLine

	i := 0
	for ; codingLine[i] < columns; i++ {
		refLine[i] = codingLine[i]
	}
	refLine[i] = columns
	i++
	refLine[i] = columns

	codingLine[0] = 0
	c.codingPos = 0
	refPos := 0
	black := 0

	for codingLine[c.codingPos] < columns {
		switch code := c.twoDimCode(); code {
		case twoDimPass:
			c.addPixels(c.ref(refPos+1), black)
			if c.ref(refPos+1) < columns {
				refPos += 2
			}

		case twoDimHoriz:
			run1, run2, code3 := 0, 0, 0
			if black != 0 {
				for {
					code3 = c.blackCode()
					run1 += code3
					if code3 < 64 {
						break
					}
				}
				for {
					code3 = c.whiteCode()
					run2 += code3
					if code3 < 64 {
						break
					}
				}
			} else {
				for {
					code3 = c.whiteCode()
					run1 += code3
					if code3 < 64 {
						break
					}
				}
				for {
					code3 = c.blackCode()
					run2 += code3
					if code3 < 64 {
						break
					}
				}
			}
			c.addPixels(codingLine[c.codingPos]+uint32(run1), black)
			if codingLine[c.codingPos] < columns {
				c.addPixels(codingLine[c.codingPos]+uint32(run2), black^1)
			}
			for c.ref(refPos) <= codingLine[c.codingPos] && c.ref(refPos) < columns {
				refPos += 2
			}

		case twoDimVertR3, twoDimVertR2, twoDimVertR1, twoDimVert0:
			c.addPixels(c.ref(refPos)+uint32(vertOffset(code)), black)
			black ^= 1
			if codingLine[c.codingPos] < columns {
				refPos++
				for c.ref(refPos) <= codingLine[c.codingPos] && c.ref(refPos) < columns {
					refPos += 2
				}
			}

		case twoDimVertL3, twoDimVertL2, twoDimVertL1:
			c.addPixelsNeg(int64(c.ref(refPos))-int64(vertOffset(code)), black)
			black ^= 1
			if codingLine[c.codingPos] < columns {
				if refPos > 0 {
					refPos--
				} else {
					refPos++
				}
				for c.ref(refPos) <= codingLine[c.codingPos] && c.ref(refPos) < columns {
					refPos += 2
				}
			}

		case ccittEOF:
			c.addPixels(columns, 0)
			c.eof = true
			return

		default:
			c.addPixels(columns, 0)
			c.err = true
			return
		}
	}
}

// vertOffset is how far a vertical code moves from the reference line.
func vertOffset(code int) int {
	switch code {
	case twoDimVertR1, twoDimVertL1:
		return 1
	case twoDimVertR2, twoDimVertL2:
		return 2
	case twoDimVertR3, twoDimVertL3:
		return 3
	}
	return 0
}

func (c *ccitt) addPixels(a1 uint32, black int) {
	pos := c.codingPos
	if a1 > c.codingLine[pos] {
		if a1 > uint32(c.columns) {
			c.err = true
			a1 = uint32(c.columns)
		}
		if pos&1^black != 0 {
			pos++
		}
		c.codingLine[pos] = a1
	}
	c.codingPos = pos
}

func (c *ccitt) addPixelsNeg(a1 int64, black int) {
	pos := c.codingPos
	switch {
	case a1 > int64(c.codingLine[pos]):
		if a1 > int64(c.columns) {
			c.err = true
			a1 = int64(c.columns)
		}
		if pos&1^black != 0 {
			pos++
		}
		c.codingLine[pos] = uint32(a1)
	case a1 < int64(c.codingLine[pos]):
		if a1 < 0 {
			c.err = true
			a1 = 0
		}
		for pos > 0 && a1 < int64(c.codingLine[pos-1]) {
			pos--
		}
		c.codingLine[pos] = uint32(a1)
	}
	c.codingPos = pos
}

// tableCode searches a table for a code of between start and end bits.
func (c *ccitt) tableCode(start, end int, table [][2]int16, limit int) (int, bool, bool) {
	for i := start; i <= end; i++ {
		code := c.lookBits(i)
		if code == ccittEOF {
			return 1, true, false
		}
		if i < end {
			code <<= uint(end - i)
		}
		if limit == 0 || code >= limit {
			p := table[code-limit]
			if int(p[0]) == i {
				c.eatBits(i)
				return int(p[1]), true, true
			}
		}
	}
	return 0, false, false
}

func (c *ccitt) twoDimCode() int {
	if c.eoblock {
		code := c.lookBits(7)
		if code >= 0 {
			p := twoDimTable[code]
			if p[0] > 0 {
				c.eatBits(int(p[0]))
				return int(p[1])
			}
		}
	} else if v, found, valid := c.tableCode(1, 7, twoDimTable[:], 0); found && valid {
		return v
	}
	return ccittEOF
}

func (c *ccitt) whiteCode() int {
	if c.eoblock {
		code := c.lookBits(12)
		if code == ccittEOF {
			return 1
		}
		var p [2]int16
		if code>>5 == 0 {
			p = whiteTable1[code]
		} else {
			p = whiteTable2[code>>3]
		}
		if p[0] > 0 {
			c.eatBits(int(p[0]))
			return int(p[1])
		}
	} else {
		if v, found, _ := c.tableCode(1, 9, whiteTable2[:], 0); found {
			return v
		}
		if v, found, _ := c.tableCode(11, 12, whiteTable1[:], 0); found {
			return v
		}
	}
	c.eatBits(1)
	return 1
}

func (c *ccitt) blackCode() int {
	if c.eoblock {
		code := c.lookBits(13)
		if code == ccittEOF {
			return 1
		}
		var p [2]int16
		switch {
		case code>>7 == 0:
			p = blackTable1[code]
		case code>>9 == 0:
			p = blackTable2[(code>>1)-64]
		default:
			p = blackTable3[code>>7]
		}
		if p[0] > 0 {
			c.eatBits(int(p[0]))
			return int(p[1])
		}
	} else {
		if v, found, _ := c.tableCode(2, 6, blackTable3[:], 0); found {
			return v
		}
		if v, found, _ := c.tableCode(7, 12, blackTable2[:], 64); found {
			return v
		}
		if v, found, _ := c.tableCode(10, 13, blackTable1[:], 0); found {
			return v
		}
	}
	c.eatBits(1)
	return 1
}

// lookBits reads n bits without consuming them, or ccittEOF at the end.
func (c *ccitt) lookBits(n int) int {
	for c.inputBits < n {
		v := c.next()
		if v < 0 {
			if c.inputBits == 0 {
				return ccittEOF
			}
			return int(c.inputBuf<<uint(n-c.inputBits)) & (0xffff >> uint(16-n))
		}
		c.inputBuf = c.inputBuf<<8 | uint32(v)
		c.inputBits += 8
	}
	return int(c.inputBuf>>uint(c.inputBits-n)) & (0xffff >> uint(16-n))
}

func (c *ccitt) eatBits(n int) {
	if c.inputBits -= n; c.inputBits < 0 {
		c.inputBits = 0
	}
}

// group4 decodes MMR coded rows, which is the coding CCITTFaxDecode with a
// negative /K uses and which JBIG2 embeds.
type group4 struct {
	c ccitt
}

// newGroup4 returns a decoder of columns wide rows that pulls bytes from read,
// which returns -1 at the end of the data.
func newGroup4(read func() int, columns, rows int, endOfBlock bool) *group4 {
	g := &group4{c: ccitt{
		read:     read,
		encoding: -1,
		columns:  columns,
		rows:     rows,
		eoblock:  endOfBlock,
		black:    true,
	}}
	g.c.start()
	return g
}

// next returns the next eight pixels, most significant bit leftmost and a set
// bit black, or -1 at the end of the data.
func (g *group4) next() int { return g.c.readNextChar() }
