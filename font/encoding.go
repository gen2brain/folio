package font

import (
	"strconv"
	"strings"
)

// Encoding returns one of the predefined simple font encodings, or nil.
func Encoding(name string) *[256]string {
	switch name {
	case "StandardEncoding":
		return &standardEncoding
	case "WinAnsiEncoding":
		return &winAnsiEncoding
	case "MacRomanEncoding":
		return &macRomanEncoding
	case "MacExpertEncoding":
		return &macExpertEncoding
	case "Symbol":
		return &symbolSetEncoding
	case "ZapfDingbats":
		return &zapfDingbatsEncoding
	}
	return nil
}

// StandardEncoding is the encoding a simple font falls back to.
func StandardEncoding() *[256]string { return &standardEncoding }

// RuneForName resolves a glyph name to a character through the Adobe Glyph
// List and the conventions that stand in for it: uniXXXX, uXXXX, a suffix
// after a dot, and a single character name.
func RuneForName(name string) rune {
	if name == "" {
		return 0
	}
	if s, ok := glyphList[name]; ok {
		if r := []rune(s); len(r) > 0 {
			return r[0]
		}
	}
	if i := strings.IndexByte(name, '.'); i > 0 {
		return RuneForName(name[:i])
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return rune(v)
		}
	}
	if s, ok := dingbatsList[name]; ok {
		if r := []rune(s); len(r) > 0 {
			return r[0]
		}
	}
	if len(name) == 1 {
		return rune(name[0])
	}
	return 0
}

// IndexForName reads the gXX, GXX, glyphXX and cidXX conventions, which name
// a glyph by its number rather than by what it looks like.
func IndexForName(name string) int {
	for _, prefix := range []string{"glyph", "cid", "g", "G"} {
		if strings.HasPrefix(name, prefix) {
			if v, err := strconv.Atoi(name[len(prefix):]); err == nil {
				return v
			}
		}
	}
	return -1
}

// MacRomanCode returns the MacRoman code of a character, or -1.
func MacRomanCode(r rune) int {
	if c, ok := macRomanCodes[r]; ok {
		return c
	}
	return -1
}

var macRomanCodes = func() map[rune]int {
	m := make(map[rune]int, 256)
	for code, name := range macRomanEncoding {
		if name == "" {
			continue
		}
		if r := RuneForName(name); r > 0 {
			if _, seen := m[r]; !seen {
				m[r] = code
			}
		}
	}
	return m
}()
