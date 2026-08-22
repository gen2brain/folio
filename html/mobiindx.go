package html

import (
	"encoding/binary"
	"strings"
)

const (
	indxHeader   = 56
	indxMaxRecs  = 0xffff
	indxMaxEntry = 10000
	cncxMaxRecs  = 0xf
	labelMax     = 1000
	valuesMax    = 100
)

// The NCX tags a table of contents entry carries.
const (
	ncxFilepos = 1
	ncxLabel   = 3
	ncxLevel   = 4
	ncxParent  = 21
)

// indxTag is one row of the TAGX table: which tag it names, how many values
// one of them holds, the bit of the control byte that says it is present, and
// whether the row only ends a control byte.
type indxTag struct{ id, values, mask, control byte }

// indxEntry is one row of an index: its label and the tags it carries.
type indxEntry struct {
	label string
	tags  []indxValue
}

type indxValue struct {
	id  byte
	val []uint32
}

// value is the i'th value of a tag, and false when the entry has no such tag.
func (e *indxEntry) value(id byte, i int) (uint32, bool) {
	for _, t := range e.tags {
		if t.id == id {
			if i < len(t.val) {
				return t.val[i], true
			}
			return 0, false
		}
	}
	return 0, false
}

// readIndex reads the set of records an index spans: a master record naming
// the tags, the entry records after it, and the string records after those.
func readIndex(recs [][]byte, at int) ([]indxEntry, [][]byte) {
	if at <= 0 || at >= len(recs) {
		return nil, nil
	}
	head := recs[at]
	if len(head) < indxHeader || string(head[:4]) != "INDX" {
		return nil, nil
	}
	hlen := int(binary.BigEndian.Uint32(head[4:]))
	if hlen < indxHeader || hlen > len(head) {
		return nil, nil
	}
	n := int(binary.BigEndian.Uint32(head[24:]))
	total := int(binary.BigEndian.Uint32(head[36:]))
	ncncx := int(binary.BigEndian.Uint32(head[52:]))
	if n <= 0 || n > indxMaxRecs || total <= 0 || ncncx > cncxMaxRecs || at+n+ncncx >= len(recs) {
		return nil, nil
	}
	tags, ctrl := readTAGX(head[hlen:])
	if tags == nil {
		return nil, nil
	}
	entries := make([]indxEntry, 0, min(total, indxMaxEntry))
	for i := 1; i <= n; i++ {
		entries = readEntries(entries, recs[at+i], tags, ctrl)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return entries, recs[at+n+1 : at+n+1+ncncx]
}

// readTAGX reads the table saying which tags an entry of this index may hold.
func readTAGX(b []byte) ([]indxTag, int) {
	if len(b) < 12 || string(b[:4]) != "TAGX" {
		return nil, 0
	}
	size := int(binary.BigEndian.Uint32(b[4:]))
	if size < 12 || size > len(b) {
		return nil, 0
	}
	ctrl := int(binary.BigEndian.Uint32(b[8:]))
	out := make([]indxTag, 0, (size-12)/4)
	seen := 0
	for at := 12; at+4 <= size; at += 4 {
		t := indxTag{id: b[at], values: b[at+1], mask: b[at+2], control: b[at+3]}
		if t.control != 0 {
			seen++
		}
		out = append(out, t)
	}
	if ctrl != seen || ctrl <= 0 {
		return nil, 0
	}
	return out, ctrl
}

// readEntries reads one record of index entries, which are found through the
// IDXT table at its end.
func readEntries(out []indxEntry, rec []byte, tags []indxTag, ctrl int) []indxEntry {
	if len(rec) < indxHeader || string(rec[:4]) != "INDX" {
		return out
	}
	idxt := int(binary.BigEndian.Uint32(rec[20:]))
	n := int(binary.BigEndian.Uint32(rec[24:]))
	if n <= 0 || n > indxMaxEntry || idxt <= 0 || idxt+4+2*n > len(rec) {
		return out
	}
	if string(rec[idxt:idxt+4]) != "IDXT" {
		return out
	}
	for i := 0; i < n; i++ {
		start := int(binary.BigEndian.Uint16(rec[idxt+4+2*i:]))
		end := idxt
		if i+1 < n {
			end = int(binary.BigEndian.Uint16(rec[idxt+4+2*(i+1):]))
		}
		if start <= 0 || end > idxt || start >= end {
			continue
		}
		if e, ok := readEntry(rec[start:end], tags, ctrl); ok {
			out = append(out, e)
		}
	}
	return out
}

// readEntry reads one entry: a label, the control bytes saying which tags it
// carries, and the values of those tags.
func readEntry(b []byte, tags []indxTag, ctrl int) (indxEntry, bool) {
	var e indxEntry
	if len(b) < 1 {
		return e, false
	}
	n := int(b[0])
	if n > labelMax || 1+n+ctrl > len(b) {
		return e, false
	}
	e.label = strings.TrimRight(string(b[1:1+n]), "\x00")
	control := b[1+n : 1+n+ctrl]
	p := 1 + n + ctrl

	// A tag is present when its bits of the control byte are set. All of
	// them set and more than one bit wide means the count is written in the
	// entry rather than in the byte.
	type pending struct {
		tag   indxTag
		count uint32
		bytes uint32
		sized bool
	}
	var want []pending
	for _, t := range tags {
		if t.control == 1 {
			if len(control) > 0 {
				control = control[1:]
			}
			continue
		}
		if len(control) == 0 {
			break
		}
		v := control[0] & t.mask
		if v == 0 {
			continue
		}
		if v == t.mask && bits8(t.mask) > 1 {
			size, k := varlen(b, p)
			if k == 0 {
				return e, false
			}
			p += k
			want = append(want, pending{tag: t, bytes: size, sized: true})
			continue
		}
		for m := t.mask; m&1 == 0; m >>= 1 {
			v >>= 1
		}
		want = append(want, pending{tag: t, count: uint32(v)})
	}
	for _, w := range want {
		var vals []uint32
		if w.sized {
			for used := uint32(0); used < w.bytes && len(vals) < valuesMax; {
				v, k := varlen(b, p)
				if k == 0 {
					return e, false
				}
				p += k
				used += uint32(k)
				vals = append(vals, v)
			}
		} else {
			for c := int(w.count) * int(w.tag.values); c > 0 && len(vals) < valuesMax; c-- {
				v, k := varlen(b, p)
				if k == 0 {
					return e, false
				}
				p += k
				vals = append(vals, v)
			}
		}
		if len(vals) > 0 {
			e.tags = append(e.tags, indxValue{id: w.tag.id, val: vals})
		}
	}
	return e, true
}

func bits8(v byte) int {
	n := 0
	for ; v != 0; v >>= 1 {
		n += int(v & 1)
	}
	return n
}

// varlen reads the integer written seven bits at a time, most significant
// first, ending at the byte with its top bit set. It returns how many bytes
// it took, and zero when there is no end within four.
func varlen(b []byte, at int) (uint32, int) {
	var v uint32
	for i := 0; i < 4; i++ {
		if at+i >= len(b) {
			return 0, 0
		}
		c := b[at+i]
		v = v<<7 | uint32(c&0x7f)
		if c&0x80 != 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// cncxString reads one of the strings the index keeps in the records after
// its entries, which is where a label longer than a byte lives.
func cncxString(recs [][]byte, at uint32) string {
	i := int(at >> 15)
	off := int(at & 0x7fff)
	if i >= len(recs) {
		return ""
	}
	rec := recs[i]
	n, k := varlen(rec, off)
	if k == 0 || off+k+int(n) > len(rec) {
		return ""
	}
	return string(rec[off+k : off+k+int(n)])
}
