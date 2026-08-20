package syntax

import (
	"reflect"
	"testing"
)

func tokens(t *testing.T, src string) []Object {
	t.Helper()
	l := NewLexer([]byte(src))
	var out []Object
	for {
		o, ok := l.Next()
		if !ok {
			return out
		}
		out = append(out, o)
	}
}

func TestLexerTokens(t *testing.T) {
	tests := []struct {
		src  string
		want []Object
	}{
		{"1 2 3", []Object{Integer(1), Integer(2), Integer(3)}},
		{"0 -7 +12 34.5 -.002 4.", []Object{Integer(0), Integer(-7), Integer(12), Real(34.5), Real(-0.002), Real(4)}},
		{"true false null", []Object{Bool(true), Bool(false), nil}},
		{"/Name /A#20B /", []Object{Name("Name"), Name("A B"), Name("")}},
		{"(abc)", []Object{String("abc")}},
		{`(a\(b\)c)`, []Object{String("a(b)c")}},
		{`(a\053b)`, []Object{String("a+b")}},
		{`(a\nb\rc\td)`, []Object{String("a\nb\rc\td")}},
		{"(nested (parens) here)", []Object{String("nested (parens) here")}},
		{"<48656c6c6f>", []Object{String("Hello")}},
		{"<4>", []Object{String("@")}},
		{"<< /A 1 >>", []Object{Keyword("<<"), Name("A"), Integer(1), Keyword(">>")}},
		{"[1 2]", []Object{Keyword("["), Integer(1), Integer(2), Keyword("]")}},
		{"q Q BT ET", []Object{Keyword("q"), Keyword("Q"), Keyword("BT"), Keyword("ET")}},
		{"% comment\n42", []Object{Integer(42)}},

		{"--5", []Object{Integer(-5)}},
		{"6.-2", []Object{Real(6.2)}},
		{"1.2.3", []Object{Real(1.2), Real(0.3)}},
		{"/Name#", []Object{Name("Name#")}},
		{"(unterminated", []Object{String("unterminated")}},
		{"<48656", []Object{String("He`")}},
	}
	for _, tc := range tests {
		got := tokens(t, tc.src)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q\n got %v\nwant %v", tc.src, got, tc.want)
		}
	}
}

func TestLexerStringEOL(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"(a\r\nb)", "a\nb"},
		{"(a\rb)", "a\nb"},
		{"(a\nb)", "a\nb"},
		{"(a\\\r\nb)", "ab"},
		{"(a\\\nb)", "ab"},
	} {
		got := tokens(t, tc.src)
		if len(got) != 1 || string(got[0].(String)) != tc.want {
			t.Errorf("%q = %v, want %q", tc.src, got, tc.want)
		}
	}
}

func TestParserObjects(t *testing.T) {
	src := `<< /A [1 2 (x)] /B << /C 3 0 R >> /D /E >>`
	p := NewParser(NewLexer([]byte(src)), nil)
	obj, ok := p.Object()
	if !ok {
		t.Fatal("no object")
	}
	d, isDict := obj.(Dict)
	if !isDict {
		t.Fatalf("got %T, want Dict", obj)
	}
	if got := format(d["A"]); got != "[1 2 (x)]" {
		t.Errorf("/A = %s", got)
	}
	inner, _ := d["B"].(Dict)
	if got, want := inner["C"], (Ref{Num: 3}); got != want {
		t.Errorf("/B /C = %v, want %v", got, want)
	}
	if d["D"] != Name("E") {
		t.Errorf("/D = %v", d["D"])
	}
}

func TestParserRecovery(t *testing.T) {
	for _, src := range []string{
		"<< /A 1 /B", "[1 2", "<< 5 /A /B 1 >>", "<< /A << /B [ >> >>", ")))", "<<<<<<",
	} {
		p := NewParser(NewLexer([]byte(src)), nil)
		for i := 0; ; i++ {
			if _, ok := p.Object(); !ok {
				break
			}
			if i > 100 {
				t.Fatalf("%q does not terminate", src)
			}
		}
	}
}

func TestParserNesting(t *testing.T) {
	src := make([]byte, 0, 4*maxNestDepth)
	for i := 0; i < 2*maxNestDepth; i++ {
		src = append(src, '[')
	}
	p := NewParser(NewLexer(src), nil)
	if _, ok := p.Object(); !ok {
		t.Fatal("no object")
	}
}
