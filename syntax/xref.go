package syntax

import (
	"bytes"
	"fmt"
)

// xrefKind is the type field of a cross-reference entry.
const (
	xrefFree = iota
	xrefNormal
	xrefInStream
)

type xrefEntry struct {
	// off is a file offset for xrefNormal, and the object number of the
	// containing object stream for xrefInStream.
	off int64
	// gen is the generation for xrefNormal, and the index within the object
	// stream for xrefInStream.
	gen  uint32
	kind uint8
}

type xref struct {
	ents  []xrefEntry
	cache map[uint32]Object

	// objstm caches the offsets parsed out of an object stream.
	objstm map[uint32]*objStream
}

func (x *xref) entry(num uint32) (xrefEntry, bool) {
	if int(num) >= len(x.ents) {
		return xrefEntry{}, false
	}
	e := x.ents[num]
	return e, e.kind != xrefFree
}

func (x *xref) grow(n int) {
	if n > maxObjects {
		n = maxObjects
	}
	for len(x.ents) < n {
		x.ents = append(x.ents, xrefEntry{})
	}
}

// set records an entry unless a newer section already provided one. Sections
// are read newest first, so the first writer wins.
func (x *xref) set(num uint32, e xrefEntry) {
	if int(num) >= maxObjects {
		return
	}
	x.grow(int(num) + 1)
	if x.ents[num].kind == xrefFree && x.ents[num].off == 0 {
		x.ents[num] = e
	}
}

const (
	maxObjects   = 1 << 24
	maxXrefChain = 64
)

// load parses a document from the whole file.
func load(buf []byte, password string) (*File, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, len(buf))
	}
	f := &File{buf: buf}
	f.xref.cache = map[uint32]Object{}
	f.xref.objstm = map[uint32]*objStream{}

	if !f.checkHeader() {
		f.errorf("no %%PDF- header")
	}

	if err := f.readXref(); err != nil {
		f.errorf("%v", err)
		if err := f.repair(); err != nil {
			return nil, err
		}
	}
	if err := f.setupEncrypt(password); err != nil {
		return nil, err
	}
	err := f.readCatalog()
	if err != nil && !f.repaired {
		f.errorf("%v", err)
		if err = f.repair(); err != nil {
			return nil, err
		}
		if err = f.setupEncrypt(password); err != nil {
			return nil, err
		}
		err = f.readCatalog()
	}
	if err != nil {
		if f.root == nil {
			return nil, err
		}
		f.errorf("%v", err)
	}
	return f, nil
}

// checkHeader looks for %PDF-, which may be anywhere near the start.
func (f *File) checkHeader() bool {
	n := min(len(f.buf), 1024)
	i := bytes.Index(f.buf[:n], []byte("%PDF-"))
	if i < 0 {
		i = bytes.Index(f.buf, []byte("%PDF-"))
	}
	if i < 0 {
		return false
	}
	f.hdrOff = int64(i)
	return true
}

// readXref follows the chain from startxref to the beginning of the file.
func (f *File) readXref() error {
	start, err := f.startXref()
	if err != nil {
		return err
	}

	seen := map[int64]bool{}
	first := true
	for n := 0; n < maxXrefChain; n++ {
		if start < 0 || start >= int64(len(f.buf)) {
			return fmt.Errorf("%w: xref offset %d out of range", ErrInvalid, start)
		}
		if seen[start] {
			return fmt.Errorf("%w: xref loop at %d", ErrInvalid, start)
		}
		seen[start] = true

		trailer, err := f.readXrefSection(start)
		if err != nil {
			return err
		}
		if first {
			f.trail = trailer
			first = false
		} else {
			for k, v := range trailer {
				if _, ok := f.trail[k]; !ok {
					f.trail[k] = v
				}
			}
		}

		if h, ok := trailer["XRefStm"].(Integer); ok && !seen[int64(h)] {
			seen[int64(h)] = true
			if _, err := f.readXrefSection(int64(h)); err != nil {
				f.errorf("hybrid xref stream at %d: %v", int64(h), err)
			}
		}

		prev, ok := trailer["Prev"].(Integer)
		if !ok {
			break
		}
		start = int64(prev)
	}
	if f.trail == nil {
		return fmt.Errorf("%w: no trailer", ErrInvalid)
	}
	return nil
}

// startXref reads the offset written after the last startxref keyword.
func (f *File) startXref() (int64, error) {
	tail := f.buf
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	i := bytes.LastIndex(tail, []byte("startxref"))
	if i < 0 {
		return 0, fmt.Errorf("%w: no startxref", ErrInvalid)
	}
	l := NewLexer(tail)
	l.pos = i + len("startxref")
	o, ok := l.Next()
	n, isInt := o.(Integer)
	if !ok || !isInt {
		return 0, fmt.Errorf("%w: startxref is not a number", ErrInvalid)
	}
	return int64(n), nil
}

// readXrefSection reads a classic table or an xref stream at off, and the
// trailer that comes with it.
func (f *File) readXrefSection(off int64) (Dict, error) {
	if off < 0 || off >= int64(len(f.buf)) {
		return nil, fmt.Errorf("%w: xref offset %d out of range", ErrInvalid, off)
	}
	l := NewLexer(f.buf)
	l.SetPos(int(off))
	p := NewParser(l, f)

	if p.isKeyword(0, "xref") {
		p.shift()
		return f.readXrefTable(p)
	}
	return f.readXrefStream(p, off)
}

// readXrefTable reads the classic "xref / n m / entries / trailer" form.
func (f *File) readXrefTable(p *Parser) (Dict, error) {
	for {
		if p.isKeyword(0, "trailer") {
			p.shift()
			t, _ := p.Object()
			dict, ok := t.(Dict)
			if !ok {
				return nil, fmt.Errorf("%w: trailer is not a dictionary", ErrInvalid)
			}
			return dict, nil
		}
		first, ok1 := Integer(p.buf[0].Whole), p.buf[0].IsInt
		n, ok2 := Integer(p.buf[1].Whole), p.buf[1].IsInt
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%w: bad xref subsection header", ErrInvalid)
		}
		p.shift()
		p.shift()
		start, count := int64(first), int64(n)
		if count < 0 || start < 0 || start+count > maxObjects {
			return nil, fmt.Errorf("%w: xref subsection %d+%d", ErrInvalid, start, count)
		}

		l := p.lex
		l.pos = p.start[0]
		for i := int64(0); i < count; i++ {
			off, gen, kind, err := readXrefEntry(l)
			if err != nil {
				return nil, err
			}
			num := uint32(start + i)
			if i == 0 && start == 1 && kind == xrefFree && gen == 65535 && off == 0 {
				f.errorf("xref subsection starts at 1; treating as 0")
				start = 0
				num = 0
			}
			if kind == xrefNormal {
				f.xref.set(num, xrefEntry{off: off, gen: uint32(gen), kind: xrefNormal})
			} else {
				f.xref.grow(int(num) + 1)
			}
		}
		p.refill()
	}
}

func readXrefEntry(l *Lexer) (off int64, gen int64, kind uint8, err error) {
	o, ok := l.Next()
	v1, is1 := o.(Integer)
	if !ok || !is1 {
		return 0, 0, 0, fmt.Errorf("%w: xref entry offset at %d", ErrInvalid, l.pos)
	}
	o, ok = l.Next()
	v2, is2 := o.(Integer)
	if !ok || !is2 {
		return 0, 0, 0, fmt.Errorf("%w: xref entry generation at %d", ErrInvalid, l.pos)
	}
	o, ok = l.Next()
	k, isKw := o.(Keyword)
	if !ok || !isKw {
		return 0, 0, 0, fmt.Errorf("%w: xref entry type at %d", ErrInvalid, l.pos)
	}
	kind = xrefNormal
	if k == "f" {
		kind = xrefFree
	} else if k != "n" {
		return 0, 0, 0, fmt.Errorf("%w: xref entry type %q", ErrInvalid, k)
	}
	return int64(v1), int64(v2), kind, nil
}

// readXrefStream reads a /Type /XRef stream, both the table and the trailer.
func (f *File) readXrefStream(p *Parser, off int64) (Dict, error) {
	obj, err := p.indirectAny()
	if err != nil {
		return nil, err
	}
	st, ok := obj.(*Stream)
	if !ok {
		return nil, fmt.Errorf("%w: no xref at %d", ErrInvalid, off)
	}
	st.crypt = nil
	data, err := st.Data()
	if err != nil {
		return nil, fmt.Errorf("xref stream at %d: %w", off, err)
	}

	w := f.GetFloats(st.Dict["W"])
	if len(w) < 3 {
		return nil, fmt.Errorf("%w: xref stream /W is %v", ErrInvalid, w)
	}
	var width [8]int
	total := 0
	for i, f := range w {
		if i >= len(width) || f < 0 || f > 8 {
			return nil, fmt.Errorf("%w: xref stream /W entry %v", ErrInvalid, f)
		}
		width[i] = int(f)
		total += int(f)
	}
	if total == 0 {
		return nil, fmt.Errorf("%w: xref stream /W is all zero", ErrInvalid)
	}

	index := f.GetFloats(st.Dict["Index"])
	if len(index) == 0 {
		index = []float64{0, float64(f.GetInt(st.Dict["Size"], 0))}
	}

	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start, count := int64(index[i]), int64(index[i+1])
		if start < 0 || count < 0 || start+count > maxObjects {
			return nil, fmt.Errorf("%w: xref stream /Index %d %d", ErrInvalid, start, count)
		}
		for j := int64(0); j < count; j++ {
			if pos+total > len(data) {
				f.errorf("xref stream at %d is short by %d bytes", off, pos+total-len(data))
				return st.Dict, nil
			}
			fld := [3]int64{1, 0, 0}
			for k := 0; k < len(w) && k < 3; k++ {
				if width[k] == 0 {
					continue
				}
				var v int64
				for b := 0; b < width[k]; b++ {
					v = v<<8 | int64(data[pos])
					pos++
				}
				fld[k] = v
			}
			for k := 3; k < len(w); k++ {
				pos += width[k]
			}
			num := uint32(start + j)
			switch fld[0] {
			case 1:
				f.xref.set(num, xrefEntry{off: fld[1], gen: uint32(fld[2]), kind: xrefNormal})
			case 2:
				f.xref.set(num, xrefEntry{off: fld[1], gen: uint32(fld[2]), kind: xrefInStream})
			default:
				f.xref.grow(int(num) + 1)
			}
		}
	}
	return st.Dict, nil
}

// maxFetchDepth bounds how far one object may be nested inside another before
// this gives up. A stream's /Length may be an indirect reference and an object
// may live in an object stream, so fetching one object can fetch another; the
// depth is what stops a file that refers to itself, and it is a parameter
// rather than a set on the File so that two goroutines fetching at once cannot
// see each other's recursion. Reading one object legitimately reaches a depth
// of three; the rest is headroom for a file that nests further than it should.
const maxFetchDepth = 16

// object fetches an indirect object, from the cache when possible.
func (f *File) object(r Ref, depth int) Object {
	f.mu.RLock()
	o, ok := f.xref.cache[r.Num]
	f.mu.RUnlock()
	if ok {
		return o
	}
	e, ok := f.xref.entry(r.Num)
	if !ok {
		return nil
	}
	if depth >= maxFetchDepth {
		f.errorf("object %d is nested too deeply", r.Num)
		return nil
	}

	// Parsing happens outside the lock: it can fetch other objects, and the
	// result depends only on the file, so two goroutines racing to read the
	// same object both produce the same answer.
	var obj Object
	switch e.kind {
	case xrefNormal:
		var err error
		obj, err = f.objectAt(e.off, Ref{Num: r.Num, Gen: uint16(e.gen)}, depth)
		if err != nil {
			f.errorf("object %d: %v", r.Num, err)
			obj = nil
		}
	case xrefInStream:
		obj = f.objectInStream(uint32(e.off), int(e.gen), r.Num, depth)
	}

	f.mu.Lock()
	if won, ok := f.xref.cache[r.Num]; ok {
		obj = won
	} else {
		f.xref.cache[r.Num] = obj
	}
	f.mu.Unlock()
	return obj
}

// objectAt reads the object stored at a file offset.
func (f *File) objectAt(off int64, want Ref, depth int) (Object, error) {
	if off < 0 || off >= int64(len(f.buf)) {
		return nil, fmt.Errorf("%w: offset %d out of range", ErrInvalid, off)
	}
	obj, err := f.parseAt(off, want, depth)
	if err == nil {
		return obj, nil
	}
	if f.hdrOff > 0 && off+f.hdrOff < int64(len(f.buf)) {
		if obj, err2 := f.parseAt(off+f.hdrOff, want, depth); err2 == nil {
			return obj, nil
		}
	}
	return nil, err
}

func (f *File) parseAt(off int64, want Ref, depth int) (Object, error) {
	l := NewLexer(f.buf)
	l.SetPos(int(off))
	p := NewParser(l, f)
	p.crypt = f.cryptFor(want)
	p.fetch = depth + 1
	return p.indirect(want)
}

// objStream is an object stream with its index parsed.
type objStream struct {
	data  []byte
	nums  []uint32
	offs  []int
	first int
}

// objectInStream reads object num, which the xref says is at index idx of the
// object stream stored in object stm.
func (f *File) objectInStream(stm uint32, idx int, num uint32, depth int) Object {
	os := f.objStreamFor(stm, depth+1)
	if os == nil {
		return nil
	}
	if idx < 0 || idx >= len(os.nums) || os.nums[idx] != num {
		idx = -1
		for i, n := range os.nums {
			if n == num {
				idx = i
				break
			}
		}
		if idx < 0 {
			f.errorf("object %d not found in object stream %d", num, stm)
			return nil
		}
	}
	start := os.first + os.offs[idx]
	if start < 0 || start > len(os.data) {
		f.errorf("object %d has offset %d in object stream %d", num, start, stm)
		return nil
	}
	l := NewLexer(os.data)
	l.pos = start
	p := NewParser(l, f)
	p.allowStreams = false
	obj, ok := p.Object()
	if !ok {
		return nil
	}
	return obj
}

func (f *File) objStreamFor(stm uint32, depth int) *objStream {
	f.mu.RLock()
	os, ok := f.xref.objstm[stm]
	f.mu.RUnlock()
	if ok {
		return os
	}
	if depth >= maxFetchDepth {
		f.errorf("object stream %d is nested too deeply", stm)
		return nil
	}

	st := f.stream(Ref{Num: stm}, depth)
	if st == nil {
		f.errorf("object stream %d is not a stream", stm)
		return f.rememberObjStream(stm, nil)
	}
	data, err := st.Data()
	if err != nil {
		f.errorf("object stream %d: %v", stm, err)
		return f.rememberObjStream(stm, nil)
	}
	n := int(f.GetInt(st.Dict["N"], 0))
	first := int(f.GetInt(st.Dict["First"], 0))
	if n < 0 || first < 0 || first > len(data) || n > maxObjects {
		f.errorf("object stream %d: /N %d /First %d", stm, n, first)
		return f.rememberObjStream(stm, nil)
	}

	os = &objStream{data: data, first: first}
	l := NewLexer(data[:first])
	for i := 0; i < n; i++ {
		a, ok1 := l.Next()
		b, ok2 := l.Next()
		num, isNum := a.(Integer)
		off, isOff := b.(Integer)
		if !ok1 || !ok2 || !isNum || !isOff || num < 0 || off < 0 {
			f.errorf("object stream %d: bad pair %d", stm, i)
			break
		}
		os.nums = append(os.nums, uint32(num))
		os.offs = append(os.offs, int(off))
	}
	return f.rememberObjStream(stm, os)
}

// rememberObjStream caches a parsed object stream, or the failure to parse
// one, and returns whichever answer another goroutine got there first with.
func (f *File) rememberObjStream(stm uint32, os *objStream) *objStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	if won, ok := f.xref.objstm[stm]; ok {
		return won
	}
	f.xref.objstm[stm] = os
	return os
}

// readCatalog finds the document catalog and the page tree.
func (f *File) readCatalog() error {
	root := f.GetDict(f.trail["Root"])
	if root == nil {
		return fmt.Errorf("%w: no document catalog", ErrInvalid)
	}
	if f.GetDict(root["Pages"]) == nil && f.GetName(root["Type"]) != "Catalog" {
		return fmt.Errorf("%w: /Root is not a catalog", ErrInvalid)
	}
	f.root = root

	f.pages = f.pageRefs()
	if len(f.pages) == 0 {
		return fmt.Errorf("%w: no pages", ErrInvalid)
	}
	return nil
}
