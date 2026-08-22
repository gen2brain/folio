package svg

import (
	"math"
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
)

// paint is what an element fills or strokes with. SVG lets both of them be
// left out, which is not the same as painting nothing: none means nothing is
// drawn, and an unset paint is inherited.
type paint struct {
	none bool
	// alpha is the fourth component of an rgba or an hsla, which multiplies
	// the opacity property that applies.
	color [3]float32
	alpha float32
}

var (
	black = paint{color: [3]float32{0, 0, 0}, alpha: 1}
	// noPaint is what none means: the shape is not drawn at all.
	noPaint = paint{none: true}
)

// parsePaint reads a paint value. current is what currentColor stands for,
// and ok is false for a word this does not understand, which leaves whatever
// was inherited in place.
func parsePaint(s string, current paint) (paint, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return paint{}, false
	case "none":
		return noPaint, true
	case "currentcolor":
		return current, true
	}
	// A paint may name a server and then a color to fall back on, and this
	// draws no server yet, so the fallback is what is left.
	if strings.HasPrefix(s, "url(") {
		if i := strings.IndexByte(s, ')'); i >= 0 {
			rest := strings.TrimSpace(s[i+1:])
			if rest == "" {
				return paint{}, false
			}
			return parsePaint(rest, current)
		}
		return paint{}, false
	}
	c, ok := parseColor(s, current)
	if !ok {
		return paint{}, false
	}
	return c, true
}

// parseColor reads the color forms of CSS Color 4 that SVG 1.1 allows: a
// keyword, three or six hexadecimal digits, and the two rgb functions.
func parseColor(s string, current paint) (paint, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return paint{}, false
	}
	if s[0] == '#' {
		return hexColor(s[1:])
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "rgb(") || strings.HasPrefix(low, "rgba(") ||
		strings.HasPrefix(low, "hsl(") || strings.HasPrefix(low, "hsla(") {
		i := strings.IndexByte(s, '(')
		j := strings.LastIndexByte(s, ')')
		if j < i {
			return paint{}, false
		}
		return funcColor(low[:i], s[i+1:j])
	}
	if low == "currentcolor" {
		return current, true
	}
	r, g, b, ok := gfx.NamedColor(low)
	if !ok {
		return paint{}, false
	}
	return paint{color: [3]float32{float32(r) / 255, float32(g) / 255, float32(b) / 255}, alpha: 1}, true
}

func hexColor(s string) (paint, bool) {
	var v [3]float32
	a := float32(1)
	switch len(s) {
	case 3, 4:
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(s[i:i+1], 16, 8)
			if err != nil {
				return paint{}, false
			}
			v[i] = float32(n*17) / 255
		}
		if len(s) == 4 {
			n, err := strconv.ParseUint(s[3:4], 16, 8)
			if err != nil {
				return paint{}, false
			}
			a = float32(n*17) / 255
		}
	case 6, 8:
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
			if err != nil {
				return paint{}, false
			}
			v[i] = float32(n) / 255
		}
		if len(s) == 8 {
			n, err := strconv.ParseUint(s[6:8], 16, 8)
			if err != nil {
				return paint{}, false
			}
			a = float32(n) / 255
		}
	default:
		return paint{}, false
	}
	return paint{color: v, alpha: a}, true
}

// funcColor reads the four functional forms, rgb and hsl with or without the
// fourth component.
func funcColor(name, args string) (paint, bool) {
	f := strings.FieldsFunc(args, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '/'
	})
	if len(f) < 3 {
		return paint{}, false
	}
	var n [3]float64
	for i := range 3 {
		s := strings.TrimSpace(f[i])
		pct := strings.HasSuffix(s, "%")
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return paint{}, false
		}
		if pct && strings.HasPrefix(name, "rgb") {
			v = v * 255 / 100
		}
		n[i] = v
	}
	a := float32(1)
	if len(f) > 3 {
		s := strings.TrimSpace(f[3])
		pct := strings.HasSuffix(s, "%")
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return paint{}, false
		}
		if pct {
			v /= 100
		}
		a = float32(min(max(v, 0), 1))
	}
	var v [3]float32
	if strings.HasPrefix(name, "hsl") {
		v = hslRGB(n[0], min(max(n[1], 0), 100)/100, min(max(n[2], 0), 100)/100)
	} else {
		for i := range 3 {
			v[i] = float32(min(max(n[i], 0), 255)) / 255
		}
	}
	return paint{color: v, alpha: a}, true
}

// hslRGB is CSS Color 4 7.1: a hue in degrees and two fractions.
func hslRGB(h, s, l float64) [3]float32 {
	h = math.Mod(math.Mod(h, 360)+360, 360) / 30
	f := func(n float64) float32 {
		k := math.Mod(n+h, 12)
		a := s * min(l, 1-l)
		return float32(l - a*max(-1, min(min(k-3, 9-k), 1)))
	}
	return [3]float32{f(0), f(8), f(4)}
}

// opacity reads a number or a percentage clamped to the unit interval, which
// is what every opacity property is.
func opacity(s string) (float32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
	if err != nil {
		return 0, false
	}
	if pct {
		v /= 100
	}
	return float32(min(max(v, 0), 1)), true
}
