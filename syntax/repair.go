package syntax

import (
	"bytes"
	"fmt"
)

// repair rebuilds the cross-reference table by scanning the whole file for
// object headers.
func (f *File) repair() error {
	f.repaired = true
	f.xref.ents = nil
	clear(f.xref.cache)
	clear(f.xref.objstm)
	if f.trail == nil {
		f.trail = Dict{}
	}

	var objstms []Ref
	pos := 0
	for pos < len(f.buf) {
		i := bytes.Index(f.buf[pos:], []byte("obj"))
		if i < 0 {
			break
		}
		at := pos + i
		start, ok := objectHeaderStart(f.buf, at)
		if !ok {
			pos = at + 3
			continue
		}

		l := NewLexer(f.buf)
		l.pos = start
		p := NewParser(l, f)
		obj, ref, err := p.indirectHeader()
		if err != nil {
			f.errorf("repair: %v", err)
			pos = at + 3
			continue
		}
		f.xref.set2(ref.Num, xrefEntry{off: int64(start), gen: uint32(ref.Gen), kind: xrefNormal})
		f.xref.cache[ref.Num] = obj

		if st, isStream := obj.(*Stream); isStream && f.GetName(st.Dict["Type"]) == "ObjStm" {
			objstms = append(objstms, ref)
		}
		if pos = p.lex.pos; pos <= at {
			pos = at + 3
		}
	}

	f.scanTrailers()

	for _, r := range objstms {
		os := f.objStreamFor(r.Num)
		if os == nil {
			continue
		}
		for i, num := range os.nums {
			if _, ok := f.xref.entry(num); ok {
				continue
			}
			f.xref.set2(num, xrefEntry{off: int64(r.Num), gen: uint32(i), kind: xrefInStream})
		}
	}

	if root := f.GetDict(f.trail["Root"]); root == nil ||
		(root["Pages"] == nil && f.GetName(root["Type"]) != "Catalog") {
		f.trail["Root"] = nil
	}
	if f.trail["Root"] == nil {
		f.findCatalog()
	}
	if f.trail["Root"] == nil {
		return fmt.Errorf("%w: no catalog found by scanning", ErrInvalid)
	}
	return nil
}

// objectHeaderStart walks back from an obj keyword over "num gen " and reports
// where the header begins.
func objectHeaderStart(buf []byte, at int) (int, bool) {
	i := at
	skipBack := func(pred func(byte) bool) bool {
		j := i
		for i > 0 && pred(buf[i-1]) {
			i--
		}
		return i < j
	}
	if !skipBack(func(c byte) bool { return isSpace[c] }) {
		return 0, false
	}
	if !skipBack(func(c byte) bool { return c >= '0' && c <= '9' }) {
		return 0, false
	}
	if !skipBack(func(c byte) bool { return isSpace[c] }) {
		return 0, false
	}
	if !skipBack(func(c byte) bool { return c >= '0' && c <= '9' }) {
		return 0, false
	}
	if i > 0 && isRegular(buf[i-1]) {
		return 0, false
	}
	return i, true
}

// set2 records an entry, replacing what is there. The repair scan runs forward
// through the file, so the last definition wins.
func (x *xref) set2(num uint32, e xrefEntry) {
	if int(num) >= maxObjects {
		return
	}
	x.grow(int(num) + 1)
	x.ents[num] = e
}

// scanTrailers collects the trailer dictionaries, newest last, and takes the
// keys the document needs from them.
func (f *File) scanTrailers() {
	want := []Name{"Root", "Info", "Encrypt", "ID", "Size"}
	pos := 0
	for {
		i := bytes.Index(f.buf[pos:], []byte("trailer"))
		if i < 0 {
			break
		}
		pos += i + len("trailer")

		l := NewLexer(f.buf)
		l.pos = pos
		p := NewParser(l, f)
		obj, ok := p.Object()
		if !ok {
			continue
		}
		if dict, isDict := obj.(Dict); isDict {
			for _, k := range want {
				if v, ok := dict[k]; ok {
					f.trail[k] = v
				}
			}
		}
	}
	if f.trail["Root"] != nil {
		return
	}
	for num := range f.xref.ents {
		st, _ := f.xref.cache[uint32(num)].(*Stream)
		if st == nil || f.GetName(st.Dict["Type"]) != "XRef" {
			continue
		}
		for _, k := range want {
			if v, ok := st.Dict[k]; ok && f.trail[k] == nil {
				f.trail[k] = v
			}
		}
	}
}

// findCatalog looks for the document catalog among the objects the scan found.
func (f *File) findCatalog() {
	for num := range f.xref.ents {
		r := Ref{Num: uint32(num)}
		e, ok := f.xref.entry(r.Num)
		if !ok {
			continue
		}
		if e.kind == xrefNormal {
			r.Gen = uint16(e.gen)
		}
		d := f.GetDict(r)
		if d == nil {
			continue
		}
		if f.GetName(d["Type"]) == "Catalog" && d["Pages"] != nil {
			f.trail["Root"] = r
			f.errorf("catalog found by scanning: %v", r)
			return
		}
	}
	for num := range f.xref.ents {
		r := Ref{Num: uint32(num)}
		if _, ok := f.xref.entry(r.Num); !ok {
			continue
		}
		d := f.GetDict(r)
		if d == nil || f.GetName(d["Type"]) != "Pages" || d["Parent"] != nil {
			continue
		}
		f.root = Dict{"Type": Name("Catalog"), "Pages": r}
		f.trail["Root"] = f.root
		f.errorf("no catalog; using page tree root %v", r)
		return
	}
}
