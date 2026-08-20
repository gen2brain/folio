package syntax

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Object is a PDF object. The concrete types are Bool, Integer, Real, String,
// Name, Array, Dict, Ref and *Stream. A nil Object is the null object, which
// the spec makes equivalent to a missing one.
type Object interface {
	object()
}

// Bool is a PDF boolean.
type Bool bool

// Integer is a PDF integer.
type Integer int64

// Real is a PDF real number.
type Real float64

// String is a PDF string, already unescaped and decrypted. It is a byte
// string, not text: interpreting it needs the context it appeared in.
type String []byte

// Name is a PDF name, without the leading slash and with #xx decoded.
type Name string

// Array is a PDF array.
type Array []Object

// Dict is a PDF dictionary. A missing key and a key with a null value are the
// same thing, per ISO 32000-1 7.3.9.
type Dict map[Name]Object

// Ref is an indirect reference.
type Ref struct {
	Num uint32
	Gen uint16
}

// Stream is a PDF stream: a dictionary and the bytes that follow it.
type Stream struct {
	Dict Dict
	Ref  Ref

	doc       *File
	crypt     *cryptFilter
	raw       []byte // as stored, still encrypted and encoded
	dec       []byte // decoded, filled on first Data
	imgFilter Name   // the image filter left undecoded, if any
	imgParms  Dict
	err       error
}

func (Bool) object()    {}
func (Integer) object() {}
func (Real) object()    {}
func (String) object()  {}
func (Name) object()    {}
func (Array) object()   {}
func (Dict) object()    {}
func (Ref) object()     {}
func (*Stream) object() {}

// Keyword is a bare token: an operator, or one of the structural words such as
// obj, endobj, stream or R. It is not a PDF object type.
type Keyword string

func (Keyword) object() {}

// Get returns the value for key without following indirect references.
func (d Dict) Get(key Name) Object { return d[key] }

// Keys returns the dictionary keys in sorted order.
func (d Dict) Keys() []Name {
	ks := make([]Name, 0, len(d))
	for k := range d {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// Resolve follows indirect references until it reaches a direct object.
func (f *File) Resolve(o Object) Object {
	for i := 0; i < maxResolveDepth; i++ {
		r, ok := o.(Ref)
		if !ok {
			return o
		}
		o = f.object(r)
	}
	f.errorf("reference loop resolving %v", o)
	return nil
}

const maxResolveDepth = 32

// Lookup resolves dict[key], trying each key in turn. Several dictionaries use
// an abbreviated key inside inline images, so /Filter and /F name the same
// entry; the full name comes first.
func (f *File) Lookup(dict Dict, keys ...Name) Object {
	for _, k := range keys {
		if v, ok := dict[k]; ok {
			if v = f.Resolve(v); v != nil {
				return v
			}
		}
	}
	return nil
}

// GetDict resolves o and returns it as a dictionary, or nil. A stream's
// dictionary counts: /Resources and friends are sometimes given as one.
func (f *File) GetDict(o Object) Dict {
	switch v := f.Resolve(o).(type) {
	case Dict:
		return v
	case *Stream:
		return v.Dict
	}
	return nil
}

// GetArray resolves o and returns it as an array, or nil.
func (f *File) GetArray(o Object) Array {
	v, _ := f.Resolve(o).(Array)
	return v
}

// GetStream resolves o and returns it as a stream, or nil.
func (f *File) GetStream(o Object) *Stream {
	v, _ := f.Resolve(o).(*Stream)
	return v
}

// GetName resolves o and returns it as a name, or "".
func (f *File) GetName(o Object) Name {
	v, _ := f.Resolve(o).(Name)
	return v
}

// GetBytes resolves o and returns it as a string, or nil.
func (f *File) GetBytes(o Object) []byte {
	v, _ := f.Resolve(o).(String)
	return v
}

// GetBool resolves o and returns it as a boolean, or def.
func (f *File) GetBool(o Object, def bool) bool {
	if v, ok := f.Resolve(o).(Bool); ok {
		return bool(v)
	}
	return def
}

// GetInt resolves o and returns it as an integer, or def. A real is truncated:
// files write /Length 12.0 and mean 12.
func (f *File) GetInt(o Object, def int64) int64 {
	switch v := f.Resolve(o).(type) {
	case Integer:
		return int64(v)
	case Real:
		return int64(v)
	}
	return def
}

// GetFloat resolves o and returns it as a number, or def.
func (f *File) GetFloat(o Object, def float64) float64 {
	switch v := f.Resolve(o).(type) {
	case Integer:
		return float64(v)
	case Real:
		return float64(v)
	}
	return def
}

// GetFloats resolves o as an array of numbers.
func (f *File) GetFloats(o Object) []float64 {
	a := f.GetArray(o)
	if a == nil {
		return nil
	}
	out := make([]float64, len(a))
	for i, e := range a {
		out[i] = f.GetFloat(e, 0)
	}
	return out
}

// GetNames resolves o as either a single name or an array of names, which is
// how /Filter and /ProcSet are written.
func (f *File) GetNames(o Object) []Name {
	switch v := f.Resolve(o).(type) {
	case Name:
		return []Name{v}
	case Array:
		out := make([]Name, 0, len(v))
		for _, e := range v {
			if n, ok := f.Resolve(e).(Name); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

// String formats the reference as it appears in a file.
func (r Ref) String() string { return fmt.Sprintf("%d %d R", r.Num, r.Gen) }

// format renders an object in PDF syntax, for diagnostics and tests. Nothing
// in the renderer depends on it.
func format(o Object) string {
	var b strings.Builder
	writeObject(&b, o)
	return b.String()
}

func writeObject(b *strings.Builder, o Object) {
	switch v := o.(type) {
	case nil:
		b.WriteString("null")
	case Bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Integer:
		b.WriteString(strconv.FormatInt(int64(v), 10))
	case Real:
		b.WriteString(strconv.FormatFloat(float64(v), 'f', -1, 64))
	case String:
		b.WriteByte('(')
		for _, c := range v {
			switch c {
			case '(', ')', '\\':
				b.WriteByte('\\')
				b.WriteByte(c)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			default:
				if c < 0x20 || c > 0x7e {
					fmt.Fprintf(b, `\%03o`, c)
				} else {
					b.WriteByte(c)
				}
			}
		}
		b.WriteByte(')')
	case Name:
		b.WriteByte('/')
		for i := 0; i < len(v); i++ {
			c := v[i]
			if c < 0x21 || c > 0x7e || c == '#' || c == '/' || c == '(' || c == ')' ||
				c == '<' || c == '>' || c == '[' || c == ']' || c == '{' || c == '}' || c == '%' {
				fmt.Fprintf(b, "#%02X", c)
			} else {
				b.WriteByte(c)
			}
		}
	case Array:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(' ')
			}
			writeObject(b, e)
		}
		b.WriteByte(']')
	case Dict:
		b.WriteString("<<")
		for i, k := range v.Keys() {
			if i > 0 {
				b.WriteByte(' ')
			}
			writeObject(b, k)
			b.WriteByte(' ')
			writeObject(b, v[k])
		}
		b.WriteString(">>")
	case *Stream:
		writeObject(b, v.Dict)
		fmt.Fprintf(b, " stream[%d]", len(v.raw))
	case Ref:
		b.WriteString(v.String())
	case Keyword:
		b.WriteString(string(v))
	default:
		fmt.Fprintf(b, "?%T", o)
	}
}
