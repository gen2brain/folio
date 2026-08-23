package font

// script is the writing system a character belongs to, for picking a face.
type script uint8

// The scripts a fallback is chosen per.
const (
	scriptLatin script = iota
	scriptHan
	scriptKana
	scriptHangul
	scriptCyrillic
	scriptGreek
	scriptArabic
	scriptHebrew
	scriptThai
	scriptDevanagari
	scriptEmoji
	scriptOther
)

// scriptOf is which writing system a character belongs to.
func scriptOf(r rune) script {
	switch {
	case r < 0x370:
		return scriptLatin
	case r >= 0x370 && r <= 0x3ff, r >= 0x1f00 && r <= 0x1fff:
		return scriptGreek
	case r >= 0x400 && r <= 0x52f:
		return scriptCyrillic
	case r >= 0x590 && r <= 0x5ff, r >= 0xfb1d && r <= 0xfb4f:
		return scriptHebrew
	case r >= 0x600 && r <= 0x6ff, r >= 0x750 && r <= 0x77f,
		r >= 0xfb50 && r <= 0xfdff, r >= 0xfe70 && r <= 0xfeff:
		return scriptArabic
	case r >= 0x900 && r <= 0x97f:
		return scriptDevanagari
	case r >= 0xe00 && r <= 0xe7f:
		return scriptThai
	case r >= 0x1100 && r <= 0x11ff, r >= 0xa960 && r <= 0xa97f,
		r >= 0xac00 && r <= 0xd7ff:
		return scriptHangul
	case r >= 0x3040 && r <= 0x30ff, r >= 0x31f0 && r <= 0x31ff:
		return scriptKana
	case r >= 0x2e80 && r <= 0x2fdf, r >= 0x3000 && r <= 0x303f,
		r >= 0x3190 && r <= 0x319f, r >= 0x3200 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff, r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xffef,
		r >= 0x20000 && r <= 0x3ffff:
		return scriptHan
	case r >= 0x1f000 && r <= 0x1faff, r >= 0x2600 && r <= 0x27bf:
		return scriptEmoji
	}
	return scriptOther
}

// fallbackFamilies are the faces a system is likely to have for a script,
// tried in order before anything else is opened.
var fallbackFamilies = map[script][]string{
	scriptHan: {
		"Noto Sans CJK", "Noto Serif CJK", "Source Han Sans", "Source Han Serif",
		"Noto Sans SC", "Noto Sans JP", "Noto Sans TC", "Noto Sans KR",
		"WenQuanYi Zen Hei", "WenQuanYi Micro Hei", "Droid Sans Fallback",
		"AR PL UMing", "AR PL UKai", "Hiragino Sans", "Hiragino Kaku Gothic ProN",
		"PingFang SC", "Heiti SC", "Songti SC", "STHeiti", "Yu Gothic", "Meiryo",
		"MS Gothic", "MS Mincho", "Microsoft YaHei", "Microsoft JhengHei",
		"SimSun", "SimHei", "Malgun Gothic", "Batang", "Gulim", "Arial Unicode MS",
	},
	scriptCyrillic: {"DejaVu Sans", "Liberation Sans", "Noto Sans", "Arial", "FreeSans"},
	scriptGreek:    {"DejaVu Sans", "Liberation Sans", "Noto Sans", "Arial", "FreeSans"},
	scriptArabic: {
		"Noto Naskh Arabic", "Noto Sans Arabic", "Amiri", "Scheherazade",
		"DejaVu Sans", "Arial Unicode MS", "Geeza Pro", "Tahoma",
	},
	scriptHebrew: {"Noto Sans Hebrew", "Noto Serif Hebrew", "DejaVu Sans", "Arial Hebrew", "David"},
	scriptThai:   {"Noto Sans Thai", "Noto Serif Thai", "Garuda", "Thonburi", "Tahoma"},
	scriptDevanagari: {
		"Noto Sans Devanagari", "Noto Serif Devanagari", "Lohit Devanagari",
		"Mangal", "Kohinoor Devanagari",
	},
	scriptEmoji: {"Noto Color Emoji", "Noto Emoji", "Apple Color Emoji", "Segoe UI Emoji"},
	scriptOther: {"Noto Sans", "DejaVu Sans", "FreeSerif", "Arial Unicode MS"},
}

func init() {
	// Kana and Hangul are drawn by the same faces the ideographs are.
	fallbackFamilies[scriptKana] = fallbackFamilies[scriptHan]
	fallbackFamilies[scriptHangul] = fallbackFamilies[scriptHan]
	fallbackFamilies[scriptLatin] = fallbackFamilies[scriptOther]
}
