package syntax

import (
	"fmt"
	"strconv"
)

// Lexer tokenizes PDF syntax. The whole file is in memory, so a token is a
// slice of it and scanning is a pointer walk.
type Lexer struct {
	buf []byte
	pos int

	// tokStart is where the token most recently returned by Next begins.
	tokStart int

	errs []error
}

func NewLexer(buf []byte) *Lexer { return &Lexer{buf: buf} }

// PDF white space and delimiters, ISO 32000-1 table 1 and 2.
var (
	isSpace = [256]bool{0: true, '\t': true, '\n': true, '\f': true, '\r': true, ' ': true}
	isDelim = [256]bool{'(': true, ')': true, '<': true, '>': true, '[': true, ']': true,
		'{': true, '}': true, '/': true, '%': true}
)

func isRegular(c byte) bool { return !isSpace[c] && !isDelim[c] }

func (l *Lexer) errorf(format string, a ...any) {
	if len(l.errs) < maxErrors {
		l.errs = append(l.errs, fmt.Errorf(format, a...))
	}
}

func (l *Lexer) at(i int) int {
	if i < 0 || i >= len(l.buf) {
		return -1
	}
	return int(l.buf[i])
}

func (l *Lexer) peek() int { return l.at(l.pos) }

// skipSpace advances past white space and comments.
func (l *Lexer) skipSpace() {
	for l.pos < len(l.buf) {
		c := l.buf[l.pos]
		switch {
		case isSpace[c]:
			l.pos++
		case c == '%':
			for l.pos < len(l.buf) && l.buf[l.pos] != '\n' && l.buf[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

// skipLine advances past the next end of line, which is where stream data
// begins.
func (l *Lexer) skipLine() {
	for l.pos < len(l.buf) {
		c := l.buf[l.pos]
		l.pos++
		if c == '\n' {
			return
		}
		if c == '\r' {
			if l.pos < len(l.buf) && l.buf[l.pos] == '\n' {
				l.pos++
			}
			return
		}
	}
}

// Pos returns the scanner position.
func (l *Lexer) Pos() int { return l.pos }

// SetPos moves the scanner, clamped to the buffer. It is how the content
// interpreter steps over the binary data of an inline image.
func (l *Lexer) SetPos(p int) {
	if p < 0 {
		p = 0
	} else if p > len(l.buf) {
		p = len(l.buf)
	}
	l.pos = p
}

// Bytes returns the buffer being scanned.
func (l *Lexer) Bytes() []byte { return l.buf }

// Errors returns what the scanner could not make sense of.
func (l *Lexer) Errors() []error { return l.errs }

// Next returns the next token. ok is false at end of input.
func (l *Lexer) Next() (obj Object, ok bool) {
	l.skipSpace()
	l.tokStart = l.pos
	if l.pos >= len(l.buf) {
		return nil, false
	}
	c := l.buf[l.pos]
	switch {
	case c >= '0' && c <= '9', c == '+', c == '-', c == '.':
		return l.number(), true
	case c == '(':
		l.pos++
		return l.literalString(), true
	case c == '/':
		l.pos++
		return l.name(), true
	case c == '[':
		l.pos++
		return Keyword("["), true
	case c == ']':
		l.pos++
		return Keyword("]"), true
	case c == '<':
		if l.at(l.pos+1) == '<' {
			l.pos += 2
			return Keyword("<<"), true
		}
		l.pos++
		return l.hexString(), true
	case c == '>':
		if l.at(l.pos+1) == '>' {
			l.pos += 2
			return Keyword(">>"), true
		}
		l.pos++
		l.errorf("stray %q at %d", c, l.pos-1)
		return l.Next()
	case c == '{', c == '}':
		l.pos++
		return Keyword(l.buf[l.pos-1 : l.pos]), true
	case c == ')':
		l.pos++
		l.errorf("stray %q at %d", c, l.pos-1)
		return l.Next()
	}
	return l.command(), true
}

// number reads an integer or real, with the malformed syntax Acrobat accepts:
// a double negative, a line break before the digits, a minus in the middle,
// and more than one decimal point.
func (l *Lexer) number() Object {
	start := l.pos
	neg := false
	if c := l.peek(); c == '-' || c == '+' {
		neg = c == '-'
		l.pos++
		for l.peek() == '-' {
			l.pos++
		}
	}
	for l.peek() == '\n' || l.peek() == '\r' {
		l.pos++
	}

	var (
		intPart  int64
		frac     float64
		fracDiv  float64 = 1
		isReal   bool
		digits   int
		overflow bool
	)
	if l.peek() == '.' {
		isReal = true
		l.pos++
	}
	for {
		c := l.peek()
		switch {
		case c >= '0' && c <= '9':
			digits++
			if isReal {
				fracDiv *= 10
				frac += float64(c-'0') / fracDiv
			} else if intPart > (1<<62)/10 {
				overflow = true
			} else {
				intPart = intPart*10 + int64(c-'0')
			}
			l.pos++
		case c == '.':
			if isReal {
				return l.numberValue(neg, intPart, frac, isReal, digits, overflow, start)
			}
			isReal = true
			l.pos++
		case c == '-':
			l.errorf("minus inside number at %d", l.pos)
			l.pos++
		default:
			return l.numberValue(neg, intPart, frac, isReal, digits, overflow, start)
		}
	}
}

func (l *Lexer) numberValue(neg bool, intPart int64, frac float64, isReal bool, digits int, overflow bool, start int) Object {
	if digits == 0 {
		l.errorf("invalid number at %d", start)
		return Integer(0)
	}
	if overflow {
		v, err := strconv.ParseFloat(string(l.buf[start:l.pos]), 64)
		if err == nil {
			return Real(v)
		}
	}
	if isReal {
		v := float64(intPart) + frac
		if neg {
			v = -v
		}
		return Real(v)
	}
	if neg {
		intPart = -intPart
	}
	return Integer(intPart)
}

// literalString reads a (...) string. The opening parenthesis is consumed.
func (l *Lexer) literalString() String {
	var out []byte
	depth := 1
	for l.pos < len(l.buf) {
		c := l.buf[l.pos]
		l.pos++
		switch c {
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return String(out)
			}
			out = append(out, c)
		case '\\':
			if l.pos >= len(l.buf) {
				break
			}
			e := l.buf[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\n':
			case '\r':
				if l.peek() == '\n' {
					l.pos++
				}
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for i := 0; i < 2; i++ {
					d := l.peek()
					if d < '0' || d > '7' {
						break
					}
					v = v<<3 | int(d-'0')
					l.pos++
				}
				out = append(out, byte(v))
			default:
				out = append(out, e)
			}
		case '\r':
			if l.peek() == '\n' {
				l.pos++
			}
			out = append(out, '\n')
		default:
			out = append(out, c)
		}
	}
	l.errorf("unterminated string")
	return String(out)
}

// hexString reads a <...> string. The opening angle bracket is consumed.
func (l *Lexer) hexString() String {
	var out []byte
	first := -1
	for l.pos < len(l.buf) {
		c := l.buf[l.pos]
		l.pos++
		if c == '>' {
			break
		}
		d := hexVal(c)
		if d < 0 {
			if !isSpace[c] {
				l.errorf("invalid hex digit %q at %d", c, l.pos-1)
			}
			continue
		}
		if first < 0 {
			first = d
		} else {
			out = append(out, byte(first<<4|d))
			first = -1
		}
	}
	if first >= 0 {
		out = append(out, byte(first<<4))
	}
	return String(out)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// name reads a /Name. The slash is consumed.
func (l *Lexer) name() Name {
	start := l.pos
	hash := false
	for l.pos < len(l.buf) && isRegular(l.buf[l.pos]) {
		if l.buf[l.pos] == '#' {
			hash = true
		}
		l.pos++
	}
	raw := l.buf[start:l.pos]
	if !hash {
		return Name(raw)
	}
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '#' || i+2 >= len(raw) {
			out = append(out, raw[i])
			continue
		}
		hi, lo := hexVal(raw[i+1]), hexVal(raw[i+2])
		if hi < 0 || lo < 0 {
			out = append(out, raw[i])
			continue
		}
		out = append(out, byte(hi<<4|lo))
		i += 2
	}
	return Name(out)
}

// command reads a bare token: an operator, or true, false, null, or one of the
// structural words.
func (l *Lexer) command() Object {
	start := l.pos
	for l.pos < len(l.buf) && isRegular(l.buf[l.pos]) {
		l.pos++
	}
	if l.pos == start {
		l.pos++
		l.errorf("stray %q at %d", l.buf[start], start)
		return Keyword(l.buf[start : start+1])
	}
	switch s := l.buf[start:l.pos]; string(s) {
	case "true":
		return Bool(true)
	case "false":
		return Bool(false)
	case "null":
		return nil
	default:
		if k := keywords[string(s)]; k != nil {
			return k
		}
		return Keyword(s)
	}
}

// keywords are the operators and file keywords, interned so that scanning a
// content stream does not allocate a string per token.
var keywords = func() map[string]Object {
	m := map[string]Object{}
	for _, k := range []string{
		"b", "B", "b*", "B*", "BDC", "BI", "BMC", "BT", "BX", "c", "cm", "CS",
		"cs", "d", "d0", "d1", "Do", "DP", "EI", "EMC", "ET", "EX", "f", "F",
		"f*", "G", "g", "gs", "h", "i", "ID", "j", "J", "K", "k", "l", "m",
		"M", "MP", "n", "q", "Q", "re", "RG", "rg", "ri", "s", "S", "SC",
		"sc", "SCN", "scn", "sh", "T*", "Tc", "Td", "TD", "Tf", "Tj", "TJ",
		"TL", "Tm", "Tr", "Ts", "Tw", "Tz", "v", "w", "W", "W*", "y", "'",
		"\"",
		"obj", "endobj", "stream", "endstream", "R", "xref", "trailer",
		"startxref", "n", "f",
	} {
		m[k] = Keyword(k)
	}
	return m
}()
