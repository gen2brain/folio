package html

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strconv"
)

// The SKEL and FRAG index tags.
const (
	skelCount = 1
	fragFile  = 3
	kf8Range  = 6
)

// kf8Book holds the files a KF8 book assembles, their starts and its fragments.
type kf8Book struct {
	files  [][]byte
	starts []int
	frags  []kf8Frag
	flows  []string
}

// kf8Frag is a fragment's insert position and its file.
type kf8Frag struct{ at, file int }

// kf8Sections reads the sections the FDST record lists, nil for none.
func kf8Sections(recs [][]byte, at int) [][2]int {
	if at <= 0 || at >= len(recs) {
		return nil
	}
	rec := recs[at]
	if len(rec) < 12 || string(rec[:4]) != "FDST" {
		return nil
	}
	off := int(binary.BigEndian.Uint32(rec[4:]))
	n := int(binary.BigEndian.Uint32(rec[8:]))
	if off < 12 || n <= 0 || n > (len(rec)-off)/8 {
		return nil
	}
	out := make([][2]int, 0, n)
	for i := 0; i < n; i++ {
		start := int(binary.BigEndian.Uint32(rec[off+8*i:]))
		end := int(binary.BigEndian.Uint32(rec[off+8*i+4:]))
		if start > end || end > maxBookBytes {
			break
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// kf8Flows cuts the text into its sections.
func kf8Flows(secs [][2]int, text []byte) [][]byte {
	var flows [][]byte
	for _, s := range secs {
		if s[1] > len(text) {
			break
		}
		flows = append(flows, text[s[0]:s[1]])
	}
	if len(flows) == 0 {
		return [][]byte{text}
	}
	return flows
}

// kf8Files assembles the files of the first flow from its skeletons and fragments.
func kf8Files(recs [][]byte, m mobi, flow []byte) *kf8Book {
	k := &kf8Book{}
	skels, _ := readIndex(recs, m.skel)
	frags, _ := readIndex(recs, m.frag)
	if len(skels) == 0 {
		k.files, k.starts = [][]byte{flow}, []int{0}
		return k
	}
	k.frags = make([]kf8Frag, len(frags))
	for i := range k.frags {
		k.frags[i].file = -1
	}
	j, total := 0, 0
	for i := range skels {
		s := &skels[i]
		count, _ := s.value(skelCount, 0)
		start, ok1 := s.value(kf8Range, 0)
		length, ok2 := s.value(kf8Range, 1)
		if !ok1 || !ok2 || int64(start)+int64(length) > int64(len(flow)) {
			break
		}
		at := int(start) + int(length)
		if total += int(length); total > len(flow) {
			break
		}
		file := slices.Clone(flow[start:at])
		for c := 0; c < int(count) && j < len(frags); c++ {
			f := &frags[j]
			j++
			n, ok := f.value(kf8Range, 1)
			ins, err := strconv.Atoi(f.label)
			if !ok || err != nil || int64(at)+int64(n) > int64(len(flow)) || total+int(n) > len(flow) {
				continue
			}
			total += int(n)
			p := min(max(ins-int(start), 0), len(file))
			file = slices.Insert(file, p, flow[at:at+int(n)]...)
			at += int(n)
			k.frags[j-1] = kf8Frag{at: ins, file: len(k.files)}
		}
		k.files = append(k.files, file)
		k.starts = append(k.starts, int(start))
	}
	if len(k.files) == 0 {
		k.files, k.starts = [][]byte{flow}, []int{0}
	}
	return k
}

// kf8Part names the n'th file of a KF8 book.
func kf8Part(n int) string { return fmt.Sprintf("part%05d.html", n) }

// kf8FlowName names the n'th flow and gives its type.
func kf8FlowName(n int, b []byte) (string, string) {
	if bytes.Contains(b[:min(len(b), 512)], []byte("<svg")) {
		return fmt.Sprintf("flow%05d.svg", n), "image/svg+xml"
	}
	return fmt.Sprintf("flow%05d.css", n), "text/css"
}

// locate resolves a kindle:pos link to a file and an offset in it.
func (k *kf8Book) locate(fid, off int) (string, int, bool) {
	if fid < 0 || fid >= len(k.frags) {
		return "", 0, false
	}
	f := k.frags[fid]
	if f.file < 0 {
		return "", 0, false
	}
	file := k.files[f.file]
	o := min(max(f.at-k.starts[f.file]+off, 0), len(file))
	if lt := bytes.LastIndexByte(file[:o], '<'); lt > bytes.LastIndexByte(file[:o], '>') {
		o = lt
	}
	return kf8Part(f.file), o, true
}

// kf8Ref is one kindle: reference in a file.
type kf8Ref struct {
	start, end int
	kind       string
	n, off     int
}

// kf8Refs finds the kindle: references in b.
func kf8Refs(b []byte) []kf8Ref {
	var out []kf8Ref
	for i := 0; ; {
		j := bytes.Index(b[i:], []byte("kindle:"))
		if j < 0 {
			return out
		}
		r := kf8Ref{start: i + j}
		end := r.start
		for end < len(b) && !bytes.ContainsRune([]byte("\"') \t\r\n>"), rune(b[end])) {
			end++
		}
		r.end = end
		tok := b[r.start+7 : end]
		if q := bytes.IndexByte(tok, '?'); q >= 0 {
			tok = tok[:q]
		}
		i = end
		var ok bool
		switch {
		case bytes.HasPrefix(tok, []byte("embed:")), bytes.HasPrefix(tok, []byte("flow:")):
			name, num, _ := bytes.Cut(tok, []byte(":"))
			r.kind = string(name)
			var v uint32
			v, ok = base32Number(num)
			r.n = int(v)
		case bytes.HasPrefix(tok, []byte("pos:fid:")):
			fid, off, found := bytes.Cut(tok[8:], []byte(":off:"))
			var f, o uint32
			if f, ok = base32Number(fid); ok && found {
				o, ok = base32Number(off)
			}
			r.kind, r.n, r.off = "pos", int(f), int(o)
		}
		if ok {
			out = append(out, r)
		}
	}
}

// rewrite resolves the kindle: references of one file and anchors every mark.
func (k *kf8Book) rewrite(b []byte, marks []int) []byte {
	refs := kf8Refs(b)
	if len(refs) == 0 && len(marks) == 0 {
		return b
	}
	slices.Sort(marks)
	marks = slices.Compact(marks)
	out := make([]byte, 0, len(b)+len(b)/16)
	i := 0
	emit := func(to int) {
		for len(marks) > 0 && marks[0] <= to {
			if marks[0] >= i {
				out = append(out, b[i:marks[0]]...)
				i = marks[0]
				out = append(out, `<a id="`+filepos(marks[0])+`"></a>`...)
			}
			marks = marks[1:]
		}
		out = append(out, b[i:to]...)
		i = to
	}
	for _, r := range refs {
		emit(r.start)
		out = append(out, k.target(r)...)
		i = r.end
	}
	emit(len(b))
	return out
}

// target resolves a reference, to an empty fragment when the book lacks it.
func (k *kf8Book) target(r kf8Ref) string {
	switch r.kind {
	case "embed":
		return mobiResource(r.n)
	case "flow":
		if r.n > 0 && r.n < len(k.flows) {
			return k.flows[r.n]
		}
	case "pos":
		if p, o, ok := k.locate(r.n, r.off); ok {
			return p + "#" + filepos(o)
		}
	}
	return "#"
}

// kf8Font unpacks the first limit bytes of the font a FONT record holds.
func kf8Font(rec []byte, limit int) []byte {
	if len(rec) < 24 || string(rec[:4]) != "FONT" {
		return nil
	}
	size := int(binary.BigEndian.Uint32(rec[4:]))
	flags := binary.BigEndian.Uint32(rec[8:])
	off := int(binary.BigEndian.Uint32(rec[12:]))
	klen := int(binary.BigEndian.Uint32(rec[16:]))
	koff := int(binary.BigEndian.Uint32(rec[20:]))
	if off < 24 || off > len(rec) {
		return nil
	}
	data := slices.Clone(rec[off:])
	if flags&2 != 0 {
		if klen <= 0 || koff < 0 || koff > len(rec)-klen {
			return nil
		}
		key := rec[koff : koff+klen]
		for i := 0; i < len(data) && i < 1040; i++ {
			data[i] ^= key[i%klen]
		}
	}
	if flags&1 == 0 {
		return data[:min(len(data), limit)]
	}
	z, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	out, _ := io.ReadAll(io.LimitReader(z, int64(min(size, limit, maxPartBytes))))
	return out
}

// fontType recognizes the font formats a FONT record may hold.
func fontType(b []byte) string {
	switch {
	case len(b) < 4:
		return ""
	case string(b[:4]) == "OTTO":
		return "font/otf"
	case string(b[:4]) == "\x00\x01\x00\x00", string(b[:4]) == "true":
		return "font/ttf"
	case string(b[:4]) == "wOFF":
		return "font/woff"
	case string(b[:4]) == "wOF2":
		return "font/woff2"
	}
	return ""
}

// kf8Document fills d with the files, the flows and the contents of a KF8 book.
func kf8Document(d *Document, parts map[string][]byte, recs [][]byte, m mobi, text []byte, secs [][2]int) {
	flows := kf8Flows(secs, text)
	k := kf8Files(recs, m, flows[0])
	k.flows = make([]string, len(flows))
	for i := 1; i < len(flows); i++ {
		name, t := kf8FlowName(i, flows[i])
		k.flows[i] = name
		d.manifest = append(d.manifest, Item{Path: name, Type: t})
	}
	marks := map[string][]int{}
	if m.ncx > 0 {
		d.outline, _ = mobiOutline(recs, m.ncx, func(e *indxEntry) (string, int, bool) {
			fid, ok1 := e.value(kf8Range, 0)
			off, ok2 := e.value(kf8Range, 1)
			if !ok1 || !ok2 {
				return "", 0, false
			}
			p, o, ok := k.locate(int(fid), int(off))
			if ok {
				marks[p] = append(marks[p], o)
			}
			return p, o, ok
		})
	}
	for _, f := range k.files {
		for _, r := range kf8Refs(f) {
			if r.kind == "pos" {
				if p, o, ok := k.locate(r.n, r.off); ok {
					marks[p] = append(marks[p], o)
				}
			}
		}
	}
	for i, f := range k.files {
		name := kf8Part(i)
		parts[name] = k.rewrite(f, marks[name])
		it := Item{Path: name, Type: "text/html", Linear: true}
		d.spine = append(d.spine, it)
		d.manifest = append(d.manifest, it)
	}
	for i := 1; i < len(flows); i++ {
		parts[k.flows[i]] = k.rewrite(flows[i], nil)
	}
}
