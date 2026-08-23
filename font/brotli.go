package font

import (
	"compress/flate"
	"fmt"
	"io"
	"strings"
	"sync"
)

// The bounds a brotli stream is decoded inside.
const (
	brotliRootBits    = 8
	brotliMaxCodeLen  = 15
	brotliNumCommands = 704
	brotliNumLiterals = 256
	brotliMaxBlockLen = 1 << 24
	brotliWindowGap   = 16
	brotliMaxDistance = 0x3FFFFFC
)

var brotliBlockLenCode = [26]struct {
	offset uint32
	bits   uint8
}{
	{1, 2}, {5, 2}, {9, 2}, {13, 2}, {17, 3}, {25, 3},
	{33, 3}, {41, 3}, {49, 4}, {65, 4}, {81, 4}, {97, 4},
	{113, 5}, {145, 5}, {177, 5}, {209, 5}, {241, 6}, {305, 6},
	{369, 7}, {497, 8}, {753, 9}, {1265, 10}, {2289, 11}, {4337, 12},
	{8433, 13}, {16625, 24},
}

var brotliCodeLenOrder = [18]uint8{1, 2, 3, 4, 0, 5, 17, 6, 16, 7, 8, 9, 10, 11, 12, 13, 14, 15}

var (
	brotliCodeLenPrefixLen = [16]uint8{2, 2, 2, 3, 2, 2, 2, 4, 2, 2, 2, 3, 2, 2, 2, 4}
	brotliCodeLenPrefixVal = [16]uint8{0, 4, 3, 2, 0, 4, 3, 1, 0, 4, 3, 2, 0, 4, 3, 5}
)

var brotliDictSizeBits = [32]uint8{
	0, 0, 0, 0, 10, 10, 11, 11, 10, 10, 10, 10, 10, 9, 9, 8,
	7, 7, 8, 7, 7, 6, 6, 5, 5, 0, 0, 0, 0, 0, 0, 0,
}

type brotliCommand struct {
	insertOffset uint16
	copyOffset   uint16
	insertBits   uint8
	copyBits     uint8
	distCode     int8
	context      uint8
}

var (
	brotliCmdOnce sync.Once
	brotliCmdLut  [brotliNumCommands]brotliCommand

	brotliDictOnce   sync.Once
	brotliDict       []byte
	brotliDictAt     [32]uint32
	brotliDictBroken bool
)

func brotliInitCommands() {
	insertBits := [24]uint8{0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 12, 14, 24}
	copyBits := [24]uint8{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 7, 8, 9, 10, 24}
	cellPos := [11]uint8{0, 1, 0, 1, 8, 9, 2, 16, 10, 17, 18}

	var insertOffset, copyOffset [24]uint16
	copyOffset[0] = 2
	for i := range 23 {
		insertOffset[i+1] = insertOffset[i] + uint16(1)<<insertBits[i]
		copyOffset[i+1] = copyOffset[i] + uint16(1)<<copyBits[i]
	}
	for sym := range brotliNumCommands {
		cell := cellPos[sym>>6]
		copyCode := int(cell<<3&0x18) + sym&7
		insertCode := int(cell&0x18) + sym>>3&7
		c := &brotliCmdLut[sym]
		c.copyBits = copyBits[copyCode]
		c.copyOffset = copyOffset[copyCode]
		c.insertBits = insertBits[insertCode]
		c.insertOffset = insertOffset[insertCode]
		c.context = 3
		if c.copyOffset <= 4 {
			c.context = uint8(c.copyOffset - 2)
		}
		if sym>>6 >= 2 {
			c.distCode = -1
		}
	}
}

func brotliInitDictionary() {
	d, err := io.ReadAll(flate.NewReader(strings.NewReader(brotliDictionaryData)))
	if err != nil || len(d) != 122784 {
		brotliDictBroken = true
		return
	}
	brotliDict = d
	for i := range 31 {
		n := uint32(0)
		if brotliDictSizeBits[i] != 0 {
			n = uint32(i) << brotliDictSizeBits[i]
		}
		brotliDictAt[i+1] = brotliDictAt[i] + n
	}
}

// brotliReader reads a brotli stream least significant bit first.
type brotliReader struct {
	src  []byte
	pos  int
	bits uint64
	n    uint
}

func (r *brotliReader) fill() {
	for r.n <= 56 {
		var b byte
		if r.pos < len(r.src) {
			b = r.src[r.pos]
		}
		r.pos++
		r.bits |= uint64(b) << r.n
		r.n += 8
	}
}

func (r *brotliReader) peek(n uint) uint32 {
	if r.n < n {
		r.fill()
	}
	return uint32(r.bits & (1<<n - 1))
}

func (r *brotliReader) drop(n uint) {
	r.bits >>= n
	r.n -= n
}

func (r *brotliReader) read(n uint) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek(n)
	r.drop(n)
	return v
}

// past reports whether more bits were taken than the stream holds.
func (r *brotliReader) past() bool { return r.pos-int(r.n)/8 > len(r.src) }

// align drops the bits up to the next byte boundary, which must be zero.
func (r *brotliReader) align() bool { return r.read(r.n&7) == 0 }

// brotliHuff decodes one prefix code.
type brotliHuff struct {
	root    []uint16
	counts  [brotliMaxCodeLen + 1]uint16
	symbols []uint16
	single  int32
}

func (h *brotliHuff) build(lens []uint8) bool {
	h.single = -1
	clear(h.counts[:])
	total := 0
	for _, l := range lens {
		h.counts[l]++
		if l != 0 {
			total++
		}
	}
	if total == 0 {
		return false
	}
	if total == 1 {
		for s, l := range lens {
			if l != 0 {
				h.single = int32(s)
				return true
			}
		}
	}

	var offset [brotliMaxCodeLen + 2]uint16
	for l := 1; l <= brotliMaxCodeLen; l++ {
		offset[l+1] = offset[l] + h.counts[l]
	}
	if int(offset[brotliMaxCodeLen+1]) != total {
		return false
	}
	h.symbols = make([]uint16, total)
	for s, l := range lens {
		if l != 0 {
			h.symbols[offset[l]] = uint16(s)
			offset[l]++
		}
	}

	if h.root == nil {
		h.root = make([]uint16, 1<<brotliRootBits)
	} else {
		clear(h.root)
	}
	code, index := 0, 0
	for l := 1; l <= brotliMaxCodeLen; l++ {
		for range h.counts[l] {
			if l <= brotliRootBits {
				rev := brotliReverse(uint32(code), uint(l))
				for j := rev; j < 1<<brotliRootBits; j += 1 << uint(l) {
					h.root[j] = h.symbols[index]<<4 | uint16(l)
				}
			}
			code++
			index++
		}
		code <<= 1
	}
	return true
}

func brotliReverse(v uint32, n uint) uint32 {
	var r uint32
	for range n {
		r = r<<1 | v&1
		v >>= 1
	}
	return r
}

func (h *brotliHuff) decode(r *brotliReader) uint32 {
	if h.single >= 0 {
		return uint32(h.single)
	}
	if v := h.root[r.peek(brotliRootBits)]; v != 0 {
		r.drop(uint(v & 15))
		return uint32(v >> 4)
	}
	code, first, index := 0, 0, 0
	for l := 1; l <= brotliMaxCodeLen; l++ {
		code |= int(r.read(1))
		count := int(h.counts[l])
		if code-first < count {
			return uint32(h.symbols[index+code-first])
		}
		index += count
		first = (first + count) << 1
		code <<= 1
	}
	return 0
}

// brotliState is one decode in progress.
type brotliState struct {
	r     brotliReader
	out   []byte
	limit int

	maxBackward int

	distRB  [4]int
	distIdx int

	numTypes  [3]int
	typeRB    [6]int
	blockLen  [3]int
	typeTree  [3]brotliHuff
	lenTree   [3]brotliHuff
	blockType [3]int

	modes       []uint8
	contextMap  []uint8
	distMap     []uint8
	trivial     []bool
	literals    []brotliHuff
	commands    []brotliHuff
	distances   []brotliHuff
	numLiterals int
	numDist     int

	npostfix   uint
	ndirect    int
	distBits   []uint8
	distOffset []uint32

	literal     *brotliHuff
	command     *brotliHuff
	lookup      string
	mapSlice    []uint8
	distSlice   []uint8
	distTree    int
	distContext int
	trivialCtx  bool
}

// brotliDecode unpacks a brotli stream of at most limit bytes.
func brotliDecode(src []byte, limit int) ([]byte, error) {
	brotliCmdOnce.Do(brotliInitCommands)
	s := &brotliState{limit: limit}
	s.r.src = src
	s.distRB = [4]int{16, 15, 11, 4}
	s.out = make([]byte, 0, min(limit, 1<<20))

	wbits := uint(16)
	if s.r.read(1) != 0 {
		if n := s.r.read(3); n != 0 {
			wbits = 17 + uint(n)
		} else if n := s.r.read(3); n == 1 {
			return nil, fmt.Errorf("%w: brotli large window", ErrUnsupported)
		} else if n != 0 {
			wbits = 8 + uint(n)
		} else {
			wbits = 17
		}
	}
	s.maxBackward = 1<<wbits - brotliWindowGap

	for {
		last, err := s.metablock()
		if err != nil {
			return nil, err
		}
		if last {
			break
		}
	}
	if s.r.past() {
		return nil, fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
	}
	return s.out, nil
}

func (s *brotliState) metablock() (bool, error) {
	r := &s.r
	last := r.read(1) != 0
	if last && r.read(1) != 0 {
		return true, nil
	}
	nibbles := int(r.read(2)) + 4
	metadata := nibbles == 7
	mlen := 0
	if metadata {
		if r.read(1) != 0 {
			return false, fmt.Errorf("%w: brotli reserved bit", ErrInvalid)
		}
		n := int(r.read(2))
		for i := range n {
			bits := int(r.read(8))
			if i+1 == n && n > 1 && bits == 0 {
				return false, fmt.Errorf("%w: brotli metadata length", ErrInvalid)
			}
			mlen |= bits << (i * 8)
		}
		if n != 0 {
			mlen++
		}
	} else {
		for i := range nibbles {
			bits := int(r.read(4))
			if i+1 == nibbles && nibbles > 4 && bits == 0 {
				return false, fmt.Errorf("%w: brotli block length", ErrInvalid)
			}
			mlen |= bits << (i * 4)
		}
		mlen++
	}

	uncompressed := false
	if !metadata && !last {
		uncompressed = r.read(1) != 0
	}
	if metadata || uncompressed {
		if !r.align() {
			return false, fmt.Errorf("%w: brotli padding", ErrInvalid)
		}
	}
	if r.past() {
		return false, fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
	}
	if metadata {
		for range mlen {
			r.read(8)
		}
		return last, nil
	}
	if mlen == 0 {
		return last, nil
	}
	if len(s.out)+mlen > s.limit {
		return false, fmt.Errorf("%w: brotli output over %d bytes", ErrInvalid, s.limit)
	}
	if uncompressed {
		for range mlen {
			s.out = append(s.out, byte(r.read(8)))
		}
		if r.past() {
			return false, fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
		}
		return last, nil
	}
	if err := s.header(); err != nil {
		return false, err
	}
	if err := s.commandLoop(mlen); err != nil {
		return false, err
	}
	return last, nil
}

// header reads everything a compressed meta-block declares before its data.
func (s *brotliState) header() error {
	r := &s.r
	s.blockLen = [3]int{brotliMaxBlockLen, brotliMaxBlockLen, brotliMaxBlockLen}
	s.numTypes = [3]int{1, 1, 1}
	s.typeRB = [6]int{1, 0, 1, 0, 1, 0}
	s.blockType = [3]int{}

	for i := range 3 {
		n := int(s.varLen()) + 1
		if n > 256 {
			return fmt.Errorf("%w: %d brotli block types", ErrInvalid, n)
		}
		s.numTypes[i] = n
		if n < 2 {
			continue
		}
		if err := s.huffman(&s.typeTree[i], n+2); err != nil {
			return err
		}
		if err := s.huffman(&s.lenTree[i], 26); err != nil {
			return err
		}
		s.blockLen[i] = s.blockLength(&s.lenTree[i])
	}

	bits := r.read(6)
	s.npostfix = uint(bits & 3)
	s.ndirect = int(bits>>2) << s.npostfix

	s.modes = make([]uint8, s.numTypes[0])
	for i := range s.modes {
		s.modes[i] = uint8(r.read(2))
	}

	var err error
	if s.contextMap, s.numLiterals, err = s.contextMapOf(s.numTypes[0] << 6); err != nil {
		return err
	}
	s.trivial = make([]bool, s.numTypes[0])
	for i := range s.trivial {
		slice := s.contextMap[i<<6 : i<<6+64]
		s.trivial[i] = true
		for _, v := range slice {
			if v != slice[0] {
				s.trivial[i] = false
				break
			}
		}
	}
	if s.distMap, s.numDist, err = s.contextMapOf(s.numTypes[2] << 2); err != nil {
		return err
	}

	distAlphabet := 16 + s.ndirect + (24 << (s.npostfix + 1))
	if s.literals, err = s.treeGroup(s.numLiterals, brotliNumLiterals); err != nil {
		return err
	}
	if s.commands, err = s.treeGroup(s.numTypes[1], brotliNumCommands); err != nil {
		return err
	}
	if s.distances, err = s.treeGroup(s.numDist, distAlphabet); err != nil {
		return err
	}

	s.distBits = make([]uint8, distAlphabet)
	s.distOffset = make([]uint32, distAlphabet)
	s.distanceCodes(distAlphabet)

	s.prepareLiteral()
	s.command = &s.commands[0]
	s.distSlice = s.distMap
	s.distTree = int(s.distSlice[0])
	if r.past() {
		return fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
	}
	return nil
}

// distanceCodes fills in the distance each code stands for.
func (s *brotliState) distanceCodes(n int) {
	i := 16
	for j := range s.ndirect {
		s.distBits[i] = 0
		s.distOffset[i] = uint32(j) + 1
		i++
	}
	bits, half := uint(1), 0
	for i < n {
		base := s.ndirect + (((((2 + half) << bits) - 4) << s.npostfix) + 1)
		for j := range 1 << s.npostfix {
			s.distBits[i] = uint8(bits)
			s.distOffset[i] = uint32(base + j)
			i++
		}
		bits += uint(half)
		half ^= 1
	}
}

func (s *brotliState) varLen() uint32 {
	r := &s.r
	if r.read(1) == 0 {
		return 0
	}
	n := r.read(3)
	if n == 0 {
		return 1
	}
	return 1<<n + r.read(uint(n))
}

func (s *brotliState) blockLength(h *brotliHuff) int {
	code := h.decode(&s.r)
	if code >= 26 {
		return brotliMaxBlockLen
	}
	e := brotliBlockLenCode[code]
	return int(e.offset) + int(s.r.read(uint(e.bits)))
}

func (s *brotliState) treeGroup(n, alphabet int) ([]brotliHuff, error) {
	g := make([]brotliHuff, n)
	for i := range g {
		if err := s.huffman(&g[i], alphabet); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// huffman reads one prefix code, simple or complex.
func (s *brotliState) huffman(h *brotliHuff, alphabet int) error {
	r := &s.r
	if r.past() {
		return fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
	}
	lens := make([]uint8, alphabet)
	switch skip := r.read(2); skip {
	case 1:
		maxBits := uint(0)
		for v := alphabet - 1; v != 0; v >>= 1 {
			maxBits++
		}
		n := int(r.read(2)) + 1
		var syms [4]int
		for i := range n {
			v := int(r.read(maxBits))
			if v >= alphabet {
				return fmt.Errorf("%w: brotli symbol %d", ErrInvalid, v)
			}
			for j := range i {
				if syms[j] == v {
					return fmt.Errorf("%w: brotli symbol %d twice", ErrInvalid, v)
				}
			}
			syms[i] = v
		}
		switch n {
		case 1:
			h.single = int32(syms[0])
			return nil
		case 2:
			lens[syms[0]], lens[syms[1]] = 1, 1
		case 3:
			lens[syms[0]], lens[syms[1]], lens[syms[2]] = 1, 2, 2
		case 4:
			if r.read(1) != 0 {
				lens[syms[0]], lens[syms[1]] = 1, 2
				lens[syms[2]], lens[syms[3]] = 3, 3
			} else {
				for _, v := range syms {
					lens[v] = 2
				}
			}
		}
	default:
		var codeLens [18]uint8
		space, codes := 32, 0
		for i := int(skip); i < 18; i++ {
			ix := r.peek(4)
			v := brotliCodeLenPrefixVal[ix]
			r.drop(uint(brotliCodeLenPrefixLen[ix]))
			codeLens[brotliCodeLenOrder[i]] = v
			if v != 0 {
				space -= 32 >> v
				codes++
				if space <= 0 {
					break
				}
			}
		}
		if codes != 1 && space != 0 {
			return fmt.Errorf("%w: brotli code length code", ErrInvalid)
		}
		var lenTree brotliHuff
		if !lenTree.build(codeLens[:]) {
			return fmt.Errorf("%w: brotli code length code", ErrInvalid)
		}
		if err := s.codeLengths(&lenTree, lens); err != nil {
			return err
		}
	}
	if !h.build(lens) {
		return fmt.Errorf("%w: brotli prefix code", ErrInvalid)
	}
	return nil
}

// codeLengths reads the code length of every symbol of an alphabet.
func (s *brotliState) codeLengths(tree *brotliHuff, lens []uint8) error {
	r := &s.r
	symbol, repeat, space := 0, 0, 32768
	prev, repeatLen := uint8(8), uint8(0)
	for symbol < len(lens) && space > 0 {
		if r.past() {
			return fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
		}
		code := uint8(tree.decode(r))
		if code < 16 {
			repeat = 0
			if code != 0 {
				lens[symbol] = code
				prev = code
				space -= 32768 >> code
			}
			symbol++
			continue
		}
		extra, newLen := uint(3), uint8(0)
		if code == 16 {
			extra, newLen = 2, prev
		}
		if repeatLen != newLen {
			repeat, repeatLen = 0, newLen
		}
		old := repeat
		if repeat > 0 {
			repeat = (repeat - 2) << extra
		}
		repeat += int(r.read(extra)) + 3
		delta := repeat - old
		if symbol+delta > len(lens) {
			return fmt.Errorf("%w: brotli code length repeat", ErrInvalid)
		}
		if repeatLen != 0 {
			for range delta {
				lens[symbol] = repeatLen
				symbol++
			}
			space -= delta << (15 - repeatLen)
		} else {
			symbol += delta
		}
	}
	if space != 0 {
		return fmt.Errorf("%w: brotli prefix code is not complete", ErrInvalid)
	}
	return nil
}

// contextMapOf reads a context map of the given size.
func (s *brotliState) contextMapOf(size int) ([]uint8, int, error) {
	r := &s.r
	trees := int(s.varLen()) + 1
	m := make([]uint8, size)
	if trees <= 1 {
		return m, trees, nil
	}
	maxRun := 0
	if bits := r.peek(5); bits&1 != 0 {
		maxRun = int(bits>>1) + 1
		r.drop(5)
	} else {
		r.drop(1)
	}
	var tree brotliHuff
	if err := s.huffman(&tree, trees+maxRun); err != nil {
		return nil, 0, err
	}
	for i := 0; i < size; {
		if r.past() {
			return nil, 0, fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
		}
		code := int(tree.decode(r))
		switch {
		case code == 0:
			m[i] = 0
			i++
		case code > maxRun:
			m[i] = uint8(code - maxRun)
			i++
		default:
			reps := int(r.read(uint(code))) + 1<<code
			if i+reps > size {
				return nil, 0, fmt.Errorf("%w: brotli context map run", ErrInvalid)
			}
			for range reps {
				m[i] = 0
				i++
			}
		}
	}
	if r.read(1) != 0 {
		brotliInverseMoveToFront(m)
	}
	for _, v := range m {
		if int(v) >= trees {
			return nil, 0, fmt.Errorf("%w: brotli context map entry %d", ErrInvalid, v)
		}
	}
	return m, trees, nil
}

func brotliInverseMoveToFront(v []uint8) {
	var mtf [256]uint8
	for i := range mtf {
		mtf[i] = uint8(i)
	}
	for i, x := range v {
		n := int(x)
		y := mtf[n]
		copy(mtf[1:n+1], mtf[:n])
		mtf[0] = y
		v[i] = y
	}
}

func (s *brotliState) prepareLiteral() {
	t := s.blockType[0]
	s.mapSlice = s.contextMap[t<<6:]
	s.trivialCtx = s.trivial[t]
	s.literal = &s.literals[s.mapSlice[0]]
	s.lookup = brotliContextLookup[int(s.modes[t]&3)<<9:]
}

func (s *brotliState) blockSwitch(t int) error {
	if s.numTypes[t] <= 1 {
		return fmt.Errorf("%w: brotli block switch", ErrInvalid)
	}
	code := int(s.typeTree[t].decode(&s.r))
	s.blockLen[t] = s.blockLength(&s.lenTree[t])
	rb := s.typeRB[t*2 : t*2+2]
	switch code {
	case 0:
		code = rb[0]
	case 1:
		code = rb[1] + 1
	default:
		code -= 2
	}
	if code >= s.numTypes[t] {
		code -= s.numTypes[t]
	}
	rb[0], rb[1] = rb[1], code
	s.blockType[t] = code
	switch t {
	case 0:
		s.prepareLiteral()
	case 1:
		s.command = &s.commands[code]
	case 2:
		s.distSlice = s.distMap[code<<2:]
		s.distTree = int(s.distSlice[s.distContext])
	}
	return nil
}

// commandLoop reads the commands of one meta-block and runs them.
func (s *brotliState) commandLoop(mlen int) error {
	r := &s.r
	for mlen > 0 {
		if r.past() {
			return fmt.Errorf("%w: brotli stream ends early", ErrInvalid)
		}
		if s.blockLen[1] == 0 {
			if err := s.blockSwitch(1); err != nil {
				return err
			}
		}
		s.blockLen[1]--
		code := s.command.decode(r)
		if code >= brotliNumCommands {
			return fmt.Errorf("%w: brotli command %d", ErrInvalid, code)
		}
		cmd := brotliCmdLut[code]
		insert := int(cmd.insertOffset) + int(r.read(uint(cmd.insertBits)))
		length := int(cmd.copyOffset) + int(r.read(uint(cmd.copyBits)))
		s.distContext = int(cmd.context)
		s.distTree = int(s.distSlice[s.distContext])

		if insert > 0 {
			if insert > mlen || len(s.out)+insert > s.limit {
				return fmt.Errorf("%w: brotli literal run", ErrInvalid)
			}
			if err := s.literalRun(insert); err != nil {
				return err
			}
			mlen -= insert
			if mlen <= 0 {
				break
			}
		}

		distance := int(cmd.distCode)
		if distance >= 0 {
			s.distContext = 1
			s.distIdx--
			distance = s.distRB[s.distIdx&3]
		} else {
			if s.blockLen[2] == 0 {
				if err := s.blockSwitch(2); err != nil {
					return err
				}
			}
			var err error
			if distance, err = s.distance(); err != nil {
				return err
			}
		}

		maxDistance := min(len(s.out), s.maxBackward)
		if distance > maxDistance {
			n, err := s.dictionaryWord(distance-maxDistance-1, length, distance)
			if err != nil {
				return err
			}
			mlen -= n
		} else {
			if distance <= 0 {
				return fmt.Errorf("%w: brotli distance %d", ErrInvalid, distance)
			}
			if length > mlen || len(s.out)+length > s.limit {
				return fmt.Errorf("%w: brotli copy of %d", ErrInvalid, length)
			}
			s.distRB[s.distIdx&3] = distance
			s.distIdx++
			at := len(s.out) - distance
			for i := range length {
				s.out = append(s.out, s.out[at+i])
			}
			mlen -= length
		}
	}
	if mlen < 0 {
		return fmt.Errorf("%w: brotli block overruns its length", ErrInvalid)
	}
	return nil
}

func (s *brotliState) literalRun(n int) error {
	r := &s.r
	for range n {
		if s.blockLen[0] == 0 {
			if err := s.blockSwitch(0); err != nil {
				return err
			}
		}
		s.blockLen[0]--
		h := s.literal
		if !s.trivialCtx {
			var p1, p2 byte
			if k := len(s.out); k > 0 {
				p1 = s.out[k-1]
				if k > 1 {
					p2 = s.out[k-2]
				}
			}
			h = &s.literals[s.mapSlice[s.lookup[p1]|s.lookup[256+int(p2)]]]
		}
		s.out = append(s.out, byte(h.decode(r)))
	}
	return nil
}

// distance reads a distance code and turns it into a distance.
func (s *brotliState) distance() (int, error) {
	r := &s.r
	code := int(s.distances[s.distTree].decode(r))
	s.blockLen[2]--
	s.distContext = 0
	if code < 16 {
		return s.ringDistance(code), nil
	}
	if code >= len(s.distBits) {
		return 0, fmt.Errorf("%w: brotli distance code %d", ErrInvalid, code)
	}
	bits := r.read(uint(s.distBits[code]))
	d := int(s.distOffset[code]) + int(bits)<<s.npostfix
	if d > brotliMaxDistance {
		return 0, fmt.Errorf("%w: brotli distance %d", ErrInvalid, d)
	}
	return d, nil
}

// ringDistance answers one of the sixteen short codes from the last four.
func (s *brotliState) ringDistance(code int) int {
	if code <= 3 {
		s.distContext = 1 >> code
		d := s.distRB[(s.distIdx-code+3)&3]
		s.distIdx -= s.distContext
		return d
	}
	index, base := 3, code-10
	if code < 10 {
		base = code - 4
	} else {
		index = 2
	}
	delta := int(0x605142>>(4*base)&0xF) - 3
	d := s.distRB[(s.distIdx+index)&3] + delta
	if d <= 0 {
		return 1 << 30
	}
	return d
}

// dictionaryWord appends the word a distance past the window stands for.
func (s *brotliState) dictionaryWord(address, length, distance int) (int, error) {
	if length < 4 || length > 24 {
		return 0, fmt.Errorf("%w: brotli dictionary word of %d", ErrInvalid, length)
	}
	brotliDictOnce.Do(brotliInitDictionary)
	if brotliDictBroken {
		return 0, fmt.Errorf("%w: brotli dictionary", ErrInvalid)
	}
	shift := brotliDictSizeBits[length]
	if shift == 0 {
		return 0, fmt.Errorf("%w: brotli dictionary word of %d", ErrInvalid, length)
	}
	index := address & (1<<shift - 1)
	transform := address >> shift
	if transform >= len(brotliTransforms) {
		return 0, fmt.Errorf("%w: brotli transform %d", ErrInvalid, transform)
	}
	s.distIdx += s.distContext
	at := int(brotliDictAt[length]) + index*length
	word := brotliDict[at : at+length]
	n := len(s.out)
	s.out = brotliTransform(s.out, word, transform)
	if len(s.out) == n && distance <= 120 {
		return 0, fmt.Errorf("%w: brotli transform leaves nothing", ErrInvalid)
	}
	if len(s.out) > s.limit {
		return 0, fmt.Errorf("%w: brotli output over %d bytes", ErrInvalid, s.limit)
	}
	return len(s.out) - n, nil
}

// brotliTransform appends a dictionary word through an RFC 7932 transform.
func brotliTransform(dst, word []byte, index int) []byte {
	t := brotliTransforms[index]
	prefix := brotliAffix(t[0])
	suffix := brotliAffix(t[2])
	kind := t[1]

	switch {
	case kind <= 9:
		if int(kind) >= len(word) {
			word = nil
		} else {
			word = word[:len(word)-int(kind)]
		}
	case kind >= 12 && kind <= 20:
		if skip := int(kind) - 11; skip >= len(word) {
			word = nil
		} else {
			word = word[skip:]
		}
	}

	dst = append(dst, prefix...)
	at := len(dst)
	dst = append(dst, word...)
	switch kind {
	case 10:
		brotliUpper(dst[at:], false)
	case 11:
		brotliUpper(dst[at:], true)
	}
	return append(dst, suffix...)
}

func brotliAffix(id uint8) string {
	at := int(brotliAffixAt[id])
	n := int(brotliAffixes[at])
	return brotliAffixes[at+1 : at+1+n]
}

// brotliUpper is the simplified upper casing the transforms are defined with.
func brotliUpper(b []byte, all bool) {
	for i := 0; i < len(b); {
		switch {
		case b[i] < 0xC0:
			if b[i] >= 'a' && b[i] <= 'z' {
				b[i] ^= 32
			}
			i++
		case b[i] < 0xE0:
			if i+1 >= len(b) {
				return
			}
			b[i+1] ^= 32
			i += 2
		default:
			if i+2 >= len(b) {
				return
			}
			b[i+2] ^= 5
			i += 3
		}
		if !all {
			return
		}
	}
}
