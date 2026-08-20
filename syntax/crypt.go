package syntax

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
)

// cryptMethod is how one class of object is encrypted.
type cryptMethod int

const (
	cryptNone cryptMethod = iota
	cryptRC4
	cryptAESV2
	cryptAESV3
)

// encrypt is the standard security handler, ISO 32000-1 7.6.3 and 32000-2 7.6.4.
type encrypt struct {
	v, r   int
	key    []byte
	stmF   cryptMethod
	strF   cryptMethod
	owner  bool // the password given was the owner password
	permit int32
}

// cryptFilter decrypts the strings and stream of one indirect object.
type cryptFilter struct {
	enc *encrypt
	ref Ref
}

// pad is the 32-byte padding string of algorithm 2.
var pad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56,
	0xFF, 0xFA, 0x01, 0x08, 0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// setupEncrypt reads /Encrypt and derives the file key.
func (f *File) setupEncrypt(password string) error {
	e := f.trail["Encrypt"]
	if e == nil {
		f.enc = nil
		return nil
	}
	dict := f.GetDict(e)
	if dict == nil {
		f.errorf("/Encrypt is not a dictionary: %s", format(e))
		return nil
	}
	if filter := f.GetName(dict["Filter"]); filter != "Standard" {
		return fmt.Errorf("%w: security handler /%s", ErrUnsupported, filter)
	}

	enc := &encrypt{
		v:      int(f.GetInt(dict["V"], 0)),
		r:      int(f.GetInt(dict["R"], 0)),
		permit: int32(f.GetInt(dict["P"], 0)),
	}
	length := int(f.GetInt(dict["Length"], 40)) / 8
	if length < 5 || length > 16 {
		length = 5
	}

	switch enc.v {
	case 1:
		length = 5
		enc.stmF, enc.strF = cryptRC4, cryptRC4
	case 2:
		enc.stmF, enc.strF = cryptRC4, cryptRC4
	case 4, 5:
		cf := f.GetDict(dict["CF"])
		enc.stmF = f.cryptMethodFor(cf, f.GetName(dict["StmF"]), &length)
		enc.strF = f.cryptMethodFor(cf, f.GetName(dict["StrF"]), &length)
	default:
		return fmt.Errorf("%w: /Encrypt /V %d", ErrUnsupported, enc.v)
	}

	id := f.firstID()
	o := f.GetBytes(dict["O"])
	u := f.GetBytes(dict["U"])
	metadata := f.GetBool(dict["EncryptMetadata"], true)

	var err error
	if enc.r >= 5 {
		enc.key, enc.owner, err = f.key256(dict, []byte(password), o, u)
		length = 32
	} else {
		enc.key, enc.owner, err = f.key128(enc, []byte(password), o, u, id, metadata, length)
	}
	if err != nil {
		if err == ErrPassword && enc.stmF == cryptNone && enc.strF == cryptNone {
			f.errorf("password not accepted, but nothing in the body is encrypted")
			return nil
		}
		return err
	}
	f.enc = enc

	clear(f.xref.cache)
	clear(f.xref.objstm)
	return nil
}

// cryptMethodFor reads one entry of /CF. The key length of the filter wins
// over the one in the encryption dictionary.
func (f *File) cryptMethodFor(cf Dict, name Name, length *int) cryptMethod {
	switch name {
	case "", "Identity":
		return cryptNone
	}
	d := f.GetDict(cf[name])
	if d == nil {
		f.errorf("crypt filter /%s is not in /CF", name)
		return cryptNone
	}
	if n := int(f.GetInt(d["Length"], 0)); n > 0 {
		if n > 40 {
			n /= 8 // some writers give bits where the spec says bytes
		}
		if n >= 5 && n <= 32 {
			*length = n
		}
	}
	switch m := f.GetName(d["CFM"]); m {
	case "V2":
		return cryptRC4
	case "AESV2":
		return cryptAESV2
	case "AESV3":
		return cryptAESV3
	case "None", "Identity", "":
		return cryptNone
	default:
		f.errorf("unknown crypt filter method /%s", m)
		return cryptNone
	}
}

func (f *File) firstID() []byte {
	ids := f.GetArray(f.trail["ID"])
	if len(ids) == 0 {
		return nil
	}
	return f.GetBytes(ids[0])
}

// key128 is algorithm 2, for /R 2 to 4.
func (f *File) key128(enc *encrypt, password, o, u, id []byte, metadata bool, length int) ([]byte, bool, error) {
	tryKey := func(pw []byte) []byte {
		h := md5.New()
		h.Write(padPassword(pw))
		h.Write(o)
		p := int32(enc.permit)
		h.Write([]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)})
		h.Write(id)
		if enc.r >= 4 && !metadata {
			h.Write([]byte{0xff, 0xff, 0xff, 0xff})
		}
		key := h.Sum(nil)
		if enc.r >= 3 {
			for i := 0; i < 50; i++ {
				sum := md5.Sum(key[:length])
				key = sum[:]
			}
		}
		return key[:length]
	}

	key := tryKey(password)
	if validUser(enc, key, u, id) {
		return key, false, nil
	}
	if user := ownerToUser(enc, password, o, length); user != nil {
		key = tryKey(user)
		if validUser(enc, key, u, id) {
			return key, true, nil
		}
	}
	if len(password) == 0 {
		return nil, false, ErrPassword
	}
	return nil, false, ErrPassword
}

// validUser is algorithm 4 for /R 2 and algorithm 5 for /R 3 and 4.
func validUser(enc *encrypt, key, u, id []byte) bool {
	if len(u) < 16 {
		return false
	}
	if enc.r == 2 {
		c, err := rc4.NewCipher(key)
		if err != nil {
			return false
		}
		out := make([]byte, 32)
		c.XORKeyStream(out, pad)
		return bytes.Equal(out, u[:32])
	}
	h := md5.New()
	h.Write(pad)
	h.Write(id)
	out := h.Sum(nil)
	rc4Loop(key, out)
	return bytes.Equal(out[:16], u[:16])
}

// ownerToUser is algorithm 7: recover the user password from /O.
func ownerToUser(enc *encrypt, password, o []byte, length int) []byte {
	if len(o) < 32 {
		return nil
	}
	sum := md5.Sum(padPassword(password))
	key := sum[:]
	if enc.r >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(key)
			key = s[:]
		}
	}
	key = key[:length]

	out := make([]byte, 32)
	copy(out, o[:32])
	if enc.r == 2 {
		c, err := rc4.NewCipher(key)
		if err != nil {
			return nil
		}
		c.XORKeyStream(out, out)
		return out
	}
	tmp := make([]byte, len(key))
	for i := 19; i >= 0; i-- {
		for j := range key {
			tmp[j] = key[j] ^ byte(i)
		}
		c, err := rc4.NewCipher(tmp)
		if err != nil {
			return nil
		}
		c.XORKeyStream(out, out)
	}
	return out
}

// rc4Loop is the 20 round key mangling of algorithm 5.
func rc4Loop(key, data []byte) {
	tmp := make([]byte, len(key))
	for i := 0; i < 20; i++ {
		for j := range key {
			tmp[j] = key[j] ^ byte(i)
		}
		c, err := rc4.NewCipher(tmp)
		if err != nil {
			return
		}
		c.XORKeyStream(data, data)
	}
}

func padPassword(pw []byte) []byte {
	out := make([]byte, 32)
	n := copy(out, pw)
	copy(out[n:], pad)
	return out
}

// key256 is algorithm 2.A, for /R 5 and 6.
func (f *File) key256(dict Dict, password, o, u []byte) ([]byte, bool, error) {
	if len(u) < 48 || len(o) < 48 {
		return nil, false, fmt.Errorf("%w: /U or /O too short for /R 5", ErrInvalid)
	}
	r6 := f.GetInt(dict["R"], 0) >= 6
	pw := password
	if len(pw) > 127 {
		pw = pw[:127]
	}

	if h := hash256(r6, pw, o[32:40], u[:48]); bytes.Equal(h, o[:32]) {
		key := hash256(r6, pw, o[40:48], u[:48])
		out, err := aesNoIV(key, f.GetBytes(dict["OE"]))
		if err != nil {
			return nil, false, err
		}
		return out, true, nil
	}
	if h := hash256(r6, pw, u[32:40], nil); bytes.Equal(h, u[:32]) {
		key := hash256(r6, pw, u[40:48], nil)
		out, err := aesNoIV(key, f.GetBytes(dict["UE"]))
		if err != nil {
			return nil, false, err
		}
		return out, false, nil
	}
	return nil, false, ErrPassword
}

// hash256 is SHA-256 for /R 5, and algorithm 2.B for /R 6.
func hash256(r6 bool, password, salt, userBytes []byte) []byte {
	input := make([]byte, 0, len(password)+len(salt)+len(userBytes))
	input = append(input, password...)
	input = append(input, salt...)
	input = append(input, userBytes...)
	sum := sha256.Sum256(input)
	k := sum[:]
	if !r6 {
		return k
	}

	var e []byte
	for i := 0; i < 64 || int(e[len(e)-1]) > i-32; i++ {
		k1 := make([]byte, 0, 64*(len(password)+len(k)+len(userBytes)))
		for j := 0; j < 64; j++ {
			k1 = append(k1, password...)
			k1 = append(k1, k...)
			k1 = append(k1, userBytes...)
		}
		block, err := aes.NewCipher(k[:16])
		if err != nil {
			return k[:32]
		}
		e = make([]byte, len(k1)-len(k1)%aes.BlockSize)
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1[:len(e)])

		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		switch sum % 3 {
		case 0:
			h := sha256.Sum256(e)
			k = h[:]
		case 1:
			h := sha512.Sum384(e)
			k = h[:]
		case 2:
			h := sha512.Sum512(e)
			k = h[:]
		}
	}
	return k[:32]
}

// aesNoIV decrypts with AES-256 in CBC mode with a zero IV and no padding,
// which is how /OE and /UE are wrapped.
func aesNoIV(key, data []byte) ([]byte, error) {
	if len(key) != 32 || len(data) < 32 {
		return nil, fmt.Errorf("%w: bad /OE or /UE", ErrInvalid)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, data[:32])
	return out, nil
}

// cryptFor returns the decryptor for one object, or nil when the file is not
// encrypted.
func (f *File) cryptFor(ref Ref) *cryptFilter {
	if f.enc == nil {
		return nil
	}
	return &cryptFilter{enc: f.enc, ref: ref}
}

// objectKey derives the per-object key of algorithm 1. AES-256 uses the file
// key unchanged.
func (c *cryptFilter) objectKey(m cryptMethod) []byte {
	e := c.enc
	if m == cryptAESV3 {
		return e.key
	}
	h := md5.New()
	h.Write(e.key)
	h.Write([]byte{byte(c.ref.Num), byte(c.ref.Num >> 8), byte(c.ref.Num >> 16),
		byte(c.ref.Gen), byte(c.ref.Gen >> 8)})
	if m == cryptAESV2 {
		h.Write([]byte{0x73, 0x41, 0x6c, 0x54}) // "sAlT"
	}
	key := h.Sum(nil)
	n := len(e.key) + 5
	if n > 16 {
		n = 16
	}
	return key[:n]
}

func (c *cryptFilter) decryptString(b []byte) []byte { return c.apply(c.enc.strF, b) }
func (c *cryptFilter) decryptStream(b []byte) []byte { return c.apply(c.enc.stmF, b) }

func (c *cryptFilter) apply(m cryptMethod, b []byte) []byte {
	if c == nil || c.enc == nil || m == cryptNone || len(b) == 0 {
		return b
	}
	key := c.objectKey(m)
	switch m {
	case cryptRC4:
		ci, err := rc4.NewCipher(key)
		if err != nil {
			return b
		}
		out := make([]byte, len(b))
		ci.XORKeyStream(out, b)
		return out
	case cryptAESV2, cryptAESV3:
		return aesCBC(key, b)
	}
	return b
}

// aesCBC decrypts data that carries its initialization vector in the first
// block and is padded to a block boundary.
func aesCBC(key, data []byte) []byte {
	if len(data) <= aes.BlockSize {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	iv, body := data[:aes.BlockSize], data[aes.BlockSize:]
	body = body[:len(body)-len(body)%aes.BlockSize]
	if len(body) == 0 {
		return nil
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)

	if n := int(out[len(out)-1]); n >= 1 && n <= aes.BlockSize && n <= len(out) {
		ok := true
		for _, b := range out[len(out)-n:] {
			if int(b) != n {
				ok = false
				break
			}
		}
		if ok {
			out = out[:len(out)-n]
		}
	}
	return out
}
