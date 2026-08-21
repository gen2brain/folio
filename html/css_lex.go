package html

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// cssKind is what a token is, as CSS Syntax Level 3 names them.
type cssKind uint8

const (
	cssEOF cssKind = iota
	cssIdent
	cssFunction
	cssAtKeyword
	cssHash
	cssString
	cssBadString
	cssURL
	cssBadURL
	cssDelim
	cssNumber
	cssPercentage
	cssDimension
	cssSpace
	cssCDO
	cssCDC
	cssColon
	cssSemicolon
	cssComma
	cssOpenSquare
	cssCloseSquare
	cssOpenParen
	cssCloseParen
	cssOpenCurly
	cssCloseCurly
)

// cssToken is one token. value holds an identifier, a string, a url or the
// name of a hash or at-keyword; unit the dimension of a number.
type cssToken struct {
	kind  cssKind
	value string
	unit  string
	num   float64
	// delim is the code point of a delimiter, which is never above ASCII.
	delim byte
	// id is set for a hash that spells an identifier, integer for a number
	// written without a fraction or an exponent, signed for one written with
	// an explicit sign.
	id      bool
	integer bool
	signed  bool
}

// cssLexer walks the code points of a stylesheet. Its input is preprocessed,
// so a zero byte means the end of it.
type cssLexer struct {
	s   string
	pos int
}

func newCSSLexer(s string) *cssLexer { return &cssLexer{s: cssPreprocess(s)} }

// cssPreprocess folds the line endings and replaces the nulls the tokenizer
// is defined over.
func cssPreprocess(s string) string {
	if strings.IndexAny(s, "\r\f\x00") < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			b.WriteByte('\n')
		case '\f':
			b.WriteByte('\n')
		case 0:
			b.WriteRune(utf8.RuneError)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (l *cssLexer) at(i int) byte {
	if l.pos+i < len(l.s) {
		return l.s[l.pos+i]
	}
	return 0
}

func (l *cssLexer) done() bool { return l.pos >= len(l.s) }

// next returns the token at the cursor and steps past it.
func (l *cssLexer) next() cssToken {
	l.comments()
	c := l.at(0)
	switch {
	case l.done():
		return cssToken{kind: cssEOF}
	case isCSSSpace(c):
		for isCSSSpace(l.at(0)) {
			l.pos++
		}
		return cssToken{kind: cssSpace}
	case c == '"' || c == '\'':
		return l.str(c)
	case c == '#':
		if isNameByte(l.at(1)) || l.validEscape(1) {
			t := cssToken{kind: cssHash, id: l.startsIdent(1)}
			l.pos++
			t.value = l.name()
			return t
		}
	case c == '+' || c == '.':
		if l.startsNumber(0) {
			return l.numeric()
		}
	case c == '-':
		switch {
		case l.startsNumber(0):
			return l.numeric()
		case l.at(1) == '-' && l.at(2) == '>':
			l.pos += 3
			return cssToken{kind: cssCDC}
		case l.startsIdent(0):
			return l.identLike()
		}
	case c == '<':
		if l.at(1) == '!' && l.at(2) == '-' && l.at(3) == '-' {
			l.pos += 4
			return cssToken{kind: cssCDO}
		}
	case c == '@':
		if l.startsIdent(1) {
			l.pos++
			return cssToken{kind: cssAtKeyword, value: l.name()}
		}
	case c == '\\':
		if l.validEscape(0) {
			return l.identLike()
		}
	case isDigitByte(c):
		return l.numeric()
	case isNameStartByte(c):
		return l.identLike()
	}
	l.pos++
	switch c {
	case '(':
		return cssToken{kind: cssOpenParen}
	case ')':
		return cssToken{kind: cssCloseParen}
	case '[':
		return cssToken{kind: cssOpenSquare}
	case ']':
		return cssToken{kind: cssCloseSquare}
	case '{':
		return cssToken{kind: cssOpenCurly}
	case '}':
		return cssToken{kind: cssCloseCurly}
	case ',':
		return cssToken{kind: cssComma}
	case ':':
		return cssToken{kind: cssColon}
	case ';':
		return cssToken{kind: cssSemicolon}
	}
	return cssToken{kind: cssDelim, delim: c}
}

func (l *cssLexer) comments() {
	for l.at(0) == '/' && l.at(1) == '*' {
		l.pos += 2
		for !l.done() && !(l.at(0) == '*' && l.at(1) == '/') {
			l.pos++
		}
		if !l.done() {
			l.pos += 2
		}
	}
}

// identLike returns an identifier, a function or a url.
func (l *cssLexer) identLike() cssToken {
	name := l.name()
	if l.at(0) != '(' {
		return cssToken{kind: cssIdent, value: name}
	}
	l.pos++
	if !strings.EqualFold(name, "url") {
		return cssToken{kind: cssFunction, value: name}
	}
	i := 0
	for isCSSSpace(l.at(i)) {
		i++
	}
	if c := l.at(i); c == '"' || c == '\'' {
		return cssToken{kind: cssFunction, value: name}
	}
	return l.url()
}

func (l *cssLexer) str(quote byte) cssToken {
	l.pos++
	start := l.pos
	var b []byte
	esc := false
	for {
		switch c := l.at(0); {
		case l.done():
			return cssToken{kind: cssString, value: l.text(start, b, esc)}
		case c == quote:
			t := cssToken{kind: cssString, value: l.text(start, b, esc)}
			l.pos++
			return t
		case c == '\n':
			return cssToken{kind: cssBadString}
		case c == '\\':
			if !esc {
				b, esc = append(b, l.s[start:l.pos]...), true
			}
			l.pos++
			switch {
			case l.done():
			case l.at(0) == '\n':
				l.pos++
			default:
				b = utf8.AppendRune(b, l.escape())
			}
		default:
			if esc {
				b = append(b, c)
			}
			l.pos++
		}
	}
}

func (l *cssLexer) text(start int, b []byte, esc bool) string {
	if esc {
		return string(b)
	}
	return l.s[start:l.pos]
}

func (l *cssLexer) url() cssToken {
	for isCSSSpace(l.at(0)) {
		l.pos++
	}
	start := l.pos
	var b []byte
	esc := false
	for {
		switch c := l.at(0); {
		case l.done():
			return cssToken{kind: cssURL, value: l.text(start, b, esc)}
		case c == ')':
			t := cssToken{kind: cssURL, value: l.text(start, b, esc)}
			l.pos++
			return t
		case isCSSSpace(c):
			t := cssToken{kind: cssURL, value: l.text(start, b, esc)}
			for isCSSSpace(l.at(0)) {
				l.pos++
			}
			if l.done() {
				return t
			}
			if l.at(0) == ')' {
				l.pos++
				return t
			}
			return l.badURL()
		case c == '"' || c == '\'' || c == '(' || isNonPrintable(c):
			return l.badURL()
		case c == '\\':
			if !l.validEscape(0) {
				return l.badURL()
			}
			if !esc {
				b, esc = append(b, l.s[start:l.pos]...), true
			}
			l.pos++
			b = utf8.AppendRune(b, l.escape())
		default:
			if esc {
				b = append(b, c)
			}
			l.pos++
		}
	}
}

func (l *cssLexer) badURL() cssToken {
	for !l.done() {
		if l.at(0) == ')' {
			l.pos++
			break
		}
		if l.validEscape(0) {
			l.pos++
			l.escape()
			continue
		}
		l.pos++
	}
	return cssToken{kind: cssBadURL}
}

func (l *cssLexer) numeric() cssToken {
	start := l.pos
	t := cssToken{integer: true}
	if c := l.at(0); c == '+' || c == '-' {
		t.signed = true
		l.pos++
	}
	for isDigitByte(l.at(0)) {
		l.pos++
	}
	if l.at(0) == '.' && isDigitByte(l.at(1)) {
		t.integer = false
		l.pos += 2
		for isDigitByte(l.at(0)) {
			l.pos++
		}
	}
	if c := l.at(0); c == 'e' || c == 'E' {
		i := 1
		if s := l.at(1); s == '+' || s == '-' {
			i = 2
		}
		if isDigitByte(l.at(i)) {
			t.integer = false
			l.pos += i
			for isDigitByte(l.at(0)) {
				l.pos++
			}
		}
	}
	t.num, _ = strconv.ParseFloat(l.s[start:l.pos], 64)
	switch {
	case l.startsIdent(0):
		t.kind, t.unit = cssDimension, strings.ToLower(l.name())
	case l.at(0) == '%':
		l.pos++
		t.kind = cssPercentage
	default:
		t.kind = cssNumber
	}
	return t
}

func (l *cssLexer) name() string {
	start := l.pos
	for isNameByte(l.at(0)) {
		l.pos++
	}
	if !l.validEscape(0) {
		return l.s[start:l.pos]
	}
	b := []byte(l.s[start:l.pos])
	for {
		switch {
		case isNameByte(l.at(0)):
			b = append(b, l.at(0))
			l.pos++
		case l.validEscape(0):
			l.pos++
			b = utf8.AppendRune(b, l.escape())
		default:
			return string(b)
		}
	}
}

// escape reads what follows a reverse solidus the cursor has passed.
func (l *cssLexer) escape() rune {
	if !isHexByte(l.at(0)) {
		if l.done() {
			return utf8.RuneError
		}
		r, n := utf8.DecodeRuneInString(l.s[l.pos:])
		l.pos += n
		return r
	}
	v := 0
	for i := 0; i < 6 && isHexByte(l.at(0)); i++ {
		v = v*16 + hexValue(l.at(0))
		l.pos++
	}
	if isCSSSpace(l.at(0)) {
		l.pos++
	}
	if v == 0 || v > unicode.MaxRune || (v >= 0xd800 && v <= 0xdfff) {
		return utf8.RuneError
	}
	return rune(v)
}

func (l *cssLexer) validEscape(i int) bool {
	return l.at(i) == '\\' && l.at(i+1) != '\n'
}

func (l *cssLexer) startsIdent(i int) bool {
	switch c := l.at(i); {
	case c == '-':
		return isNameStartByte(l.at(i+1)) || l.at(i+1) == '-' || l.validEscape(i+1)
	case isNameStartByte(c):
		return true
	}
	return l.validEscape(i)
}

func (l *cssLexer) startsNumber(i int) bool {
	switch c := l.at(i); {
	case c == '+' || c == '-':
		return isDigitByte(l.at(i+1)) || (l.at(i+1) == '.' && isDigitByte(l.at(i+2)))
	case c == '.':
		return isDigitByte(l.at(i + 1))
	}
	return isDigitByte(l.at(i))
}

func isCSSSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func isHexByte(c byte) bool {
	return isDigitByte(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexValue(c byte) int {
	switch {
	case c <= '9':
		return int(c - '0')
	case c <= 'F':
		return int(c-'A') + 10
	}
	return int(c-'a') + 10
}

// isNameStartByte covers every byte above ASCII: no non-ASCII code point is
// anything but a name.
func isNameStartByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func isNameByte(c byte) bool { return isNameStartByte(c) || isDigitByte(c) || c == '-' }

func isNonPrintable(c byte) bool {
	return c <= 8 || c == 0x0b || (c >= 0x0e && c <= 0x1f) || c == 0x7f
}
