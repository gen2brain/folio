package syntax

import "fmt"

// Parser turns tokens into objects. It keeps two tokens of lookahead, which is
// what "3 0 R" and "<< >> stream" need.
type Parser struct {
	lex *Lexer
	doc *File

	buf   [2]Operand
	ok    [2]bool
	start [2]int

	// allowStreams is false inside object streams and content streams, where
	// the stream keyword cannot appear.
	allowStreams bool
	// crypt decrypts strings and stream data for the object being read.
	crypt *cryptFilter
	// fetch is how many objects deep this parse already is, which bounds a
	// stream whose /Length is an indirect reference back to itself.
	fetch int
	depth int
	// last is where the token most recently returned begins.
	last int
}

// Lexer returns the scanner the Parser reads from.
func (p *Parser) Lexer() *Lexer { return p.lex }

// AllowStreams sets whether a stream keyword may follow a dictionary. It is
// false inside object streams and content streams, where a stream cannot
// appear.
func (p *Parser) AllowStreams(ok bool) { p.allowStreams = ok }

// NewParser reads objects from a lexer. The file is what an indirect
// reference is resolved against, and may be nil for a stream of objects that
// refers to nothing.
func NewParser(l *Lexer, f *File) *Parser {
	p := &Parser{lex: l, doc: f, allowStreams: true}
	p.refill()
	return p
}

// Refill reloads both lookahead slots after the scanner position was moved by
// hand, which is what stepping over the data of an inline image needs.
func (p *Parser) Refill() { p.refill() }

// refill reloads both lookahead slots after the scanner position was moved.
func (p *Parser) refill() {
	p.buf[0], p.ok[0] = p.lex.next()
	p.start[0] = p.lex.tokStart
	p.buf[1], p.ok[1] = p.lex.next()
	p.start[1] = p.lex.tokStart
}

// Pos returns where the token Object returned last begins. The content
// interpreter needs it because the data of an inline image starts one white
// space byte after the ID keyword, and the parser reads two tokens ahead.
func (p *Parser) Pos() int { return p.last }

func (p *Parser) shift() (Object, bool) {
	o, ok := p.shiftOperand()
	if !ok {
		return nil, false
	}
	return o.Object(), true
}

func (p *Parser) shiftOperand() (Operand, bool) {
	o, ok := p.buf[0], p.ok[0]
	p.last = p.start[0]
	p.buf[0], p.ok[0], p.start[0] = p.buf[1], p.ok[1], p.start[1]
	p.buf[1], p.ok[1] = p.lex.next()
	p.start[1] = p.lex.tokStart
	return o, ok
}

// Operand reads one item of a content stream's operand list. A number comes
// back unboxed; anything else is read as a whole object, so an array or a
// dictionary operand still arrives complete. A content stream has no indirect
// references, which is what lets a number be taken on sight.
func (p *Parser) Operand() (Operand, bool) {
	if p.ok[0] && p.buf[0].IsNum {
		return p.shiftOperand()
	}
	obj, ok := p.Object()
	return Operand{Obj: obj}, ok
}

func (p *Parser) isKeyword(i int, k Keyword) bool {
	if !p.ok[i] {
		return false
	}
	v, is := p.buf[i].Obj.(Keyword)
	return is && v == k
}

func (p *Parser) errorf(format string, a ...any) {
	if p.doc != nil {
		p.doc.errorf(format, a...)
		return
	}
	p.lex.errorf(format, a...)
}

// Object reads one object. ok is false at end of input.
func (p *Parser) Object() (obj Object, ok bool) {
	first, ok := p.shift()
	if !ok {
		return nil, false
	}
	kw, isKw := first.(Keyword)
	if !isKw {
		if s, isStr := first.(String); isStr && p.crypt != nil {
			return String(p.crypt.decryptString(s)), true
		}
		if n, isInt := first.(Integer); isInt {
			if p.ok[0] && p.buf[0].IsInt && p.isKeyword(1, "R") {
				g := Integer(p.buf[0].Whole)
				p.shift()
				p.shift()
				if n < 0 || n > 1<<31 || g < 0 || g > 0xffff {
					p.errorf("reference out of range: %d %d R", n, g)
					return nil, true
				}
				return Ref{Num: uint32(n), Gen: uint16(g)}, true
			}
		}
		return first, true
	}

	switch kw {
	case "[":
		return p.array(), true
	case "<<":
		return p.dictOrStream(), true
	}
	return first, true
}

func (p *Parser) array() Array {
	if p.depth++; p.depth > maxNestDepth {
		p.errorf("array nested deeper than %d", maxNestDepth)
		p.depth--
		return nil
	}
	defer func() { p.depth-- }()

	arr := Array{}
	for {
		if !p.ok[0] {
			p.errorf("end of file inside array")
			return arr
		}
		if p.isKeyword(0, "]") {
			p.shift()
			return arr
		}
		if p.isKeyword(0, ">>") || p.isKeyword(0, "endobj") {
			p.errorf("unterminated array")
			return arr
		}
		o, ok := p.Object()
		if !ok {
			return arr
		}
		arr = append(arr, o)
	}
}

func (p *Parser) dictOrStream() Object {
	if p.depth++; p.depth > maxNestDepth {
		p.errorf("dictionary nested deeper than %d", maxNestDepth)
		p.depth--
		return nil
	}
	defer func() { p.depth-- }()

	dict := Dict{}
	for {
		if !p.ok[0] {
			p.errorf("end of file inside dictionary")
			return dict
		}
		if p.isKeyword(0, ">>") {
			p.shift()
			break
		}
		key, isName := p.buf[0].Obj.(Name)
		if !isName {
			p.errorf("dictionary key is not a name: %s", format(p.buf[0].Object()))
			if p.isKeyword(0, "endobj") || p.isKeyword(0, "]") {
				return dict
			}
			p.shift()
			continue
		}
		p.shift()
		if !p.ok[0] {
			p.errorf("end of file after dictionary key /%s", key)
			return dict
		}
		v, ok := p.Object()
		if !ok {
			return dict
		}
		dict[key] = v
	}

	if p.allowStreams && p.isKeyword(0, "stream") {
		return p.stream(dict)
	}
	return dict
}

// stream reads the data of a stream whose dictionary has been parsed and whose
// next token is the stream keyword.
func (p *Parser) stream(dict Dict) Object {
	p.lex.pos = p.start[0] + len("stream")
	p.lex.skipLine()
	start := p.lex.pos

	length := -1
	if p.doc != nil {
		length = int(p.doc.getInt(dict["Length"], -1, p.fetch))
	} else if n, ok := dict["Length"].(Integer); ok {
		length = int(n)
	}

	end := -1
	if length >= 0 && start+length <= len(p.lex.buf) {
		end = start + length
		if !p.endstreamAt(end) {
			p.errorf("/Length %d does not reach endstream at %d", length, start)
			end = -1
		}
	} else if length >= 0 {
		p.errorf("/Length %d runs past end of file", length)
	}
	if end < 0 {
		if end = p.findEndstream(start); end < 0 {
			p.errorf("missing endstream after %d", start)
			end = len(p.lex.buf)
		}
	}

	p.lex.pos = end
	p.refill()
	if p.isKeyword(0, "endstream") {
		p.shift()
	} else {
		p.errorf("stream at %d does not end with endstream", start)
	}

	return &Stream{Dict: dict, doc: p.doc, crypt: p.crypt, raw: p.lex.buf[start:end]}
}

// endstreamAt reports whether the endstream keyword follows position i, with
// the end of line the spec requires before it and that writers often omit.
func (p *Parser) endstreamAt(i int) bool {
	b := p.lex.buf
	for i < len(b) && isSpace[b[i]] {
		i++
	}
	return i+9 <= len(b) && string(b[i:i+9]) == "endstream"
}

// findEndstream scans for the end of a stream whose length was wrong. Writers
// misspell the keyword, so accept the truncations pdf.js accepts.
func (p *Parser) findEndstream(start int) int {
	b := p.lex.buf
	for i := start; i+3 <= len(b); i++ {
		if b[i] != 'e' || b[i+1] != 'n' || b[i+2] != 'd' {
			continue
		}
		rest := b[i+3:]
		if !hasPrefix(rest, "stream") {
			if !hasPrefix(rest, "steam") && !hasPrefix(rest, "strea") {
				continue
			}
			if len(rest) <= 5 || !isSpace[rest[5]] {
				continue
			}
		}
		end := i
		if end > start && b[end-1] == '\n' {
			end--
		}
		if end > start && b[end-1] == '\r' {
			end--
		}
		return end
	}
	return -1
}

func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

const maxNestDepth = 64

// indirect reads "num gen obj ... endobj" at the current position, requiring
// it to be the object want names.
func (p *Parser) indirect(want Ref) (Object, error) {
	obj, got, err := p.indirectHeader()
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, fmt.Errorf("%w: found %v obj, want %v", ErrInvalid, got, want)
	}
	return obj, nil
}

// indirectAny reads an indirect object whose number is not known in advance,
// which is how an xref stream and the repair scan find them.
func (p *Parser) indirectAny() (Object, error) {
	obj, _, err := p.indirectHeader()
	return obj, err
}

func (p *Parser) indirectHeader() (Object, Ref, error) {
	num, ok := Integer(p.buf[0].Whole), p.buf[0].IsInt
	gen, ok2 := Integer(p.buf[1].Whole), p.buf[1].IsInt
	if !ok || !ok2 || num < 0 || num > maxObjects || gen < 0 || gen > 0xffff {
		return nil, Ref{}, fmt.Errorf("%w: object header at %d", ErrInvalid, p.start[0])
	}
	p.shift()
	p.shift()
	if !p.isKeyword(0, "obj") {
		return nil, Ref{}, fmt.Errorf("%w: no obj Keyword for %d %d at %d", ErrInvalid, num, gen, p.start[0])
	}
	p.shift()

	ref := Ref{Num: uint32(num), Gen: uint16(gen)}
	obj, ok := p.Object()
	if !ok {
		return nil, ref, fmt.Errorf("%w: empty object %v", ErrInvalid, ref)
	}
	if s, isStream := obj.(*Stream); isStream {
		s.Ref = ref
	}
	return obj, ref, nil
}
