package syntax

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func open(t *testing.T, name, password string) *File {
	t.Helper()
	f, err := OpenPassword(filepath.Join("..", "testdata", name), password)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// checkMinimal asserts everything testdata/minimal.pdf contains, whatever
// shape the file it came from was written in.
func checkMinimal(t *testing.T, f *File) {
	t.Helper()
	if got := f.NumPages(); got != 1 {
		t.Fatalf("pages = %d, want 1", got)
	}
	page := f.Page(0)
	if page == nil {
		t.Fatal("no page 0")
	}
	if got := f.GetFloats(page["MediaBox"]); len(got) != 4 || got[2] != 200 || got[3] != 100 {
		t.Errorf("/MediaBox = %v", got)
	}
	st := f.GetStream(page["Contents"])
	if st == nil {
		t.Fatal("no content stream")
	}
	data, err := st.Data()
	if err != nil {
		t.Fatalf("content stream: %v", err)
	}
	if !strings.Contains(string(data), "(Hi) Tj") {
		t.Errorf("content stream = %q", data)
	}
	res := f.GetDict(page["Resources"])
	font := f.GetDict(f.GetDict(res["Font"])["F1"])
	if got := f.GetName(font["BaseFont"]); got != "Helvetica" {
		t.Errorf("/BaseFont = %v", got)
	}
}

func TestMinimal(t *testing.T) {
	f := open(t, "minimal.pdf", "")
	checkMinimal(t, f)
	if f.Repaired() {
		t.Error("a valid file should not be repaired")
	}
	if errs := f.Err(); len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
}

// TestXrefStream reads the same document written with an xref stream and
// object streams, so every object arrives through a different path.
func TestXrefStream(t *testing.T) {
	f := open(t, "xrefstream.pdf", "")
	checkMinimal(t, f)
	if errs := f.Err(); len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
}

// TestRepair reads a file whose startxref points at nothing.
func TestRepair(t *testing.T) {
	f := open(t, "brokenxref.pdf", "")
	if !f.Repaired() {
		t.Error("Repaired() = false")
	}
	checkMinimal(t, f)
}

func TestEncrypted(t *testing.T) {
	for _, tc := range []struct{ file, password string }{
		{"rc4-40.pdf", "user"},
		{"rc4-40.pdf", "owner"},
		{"rc4-128.pdf", "user"},
		{"rc4-128.pdf", "owner"},
		{"aes-128.pdf", "user"},
		{"aes-128.pdf", "owner"},
		{"aes-256.pdf", "user"},
		{"aes-256.pdf", "owner"},
		{"aes-256-nouser.pdf", ""},
		{"aes-256-nouser.pdf", "owner"},
	} {
		t.Run(tc.file+"/"+tc.password, func(t *testing.T) {
			checkMinimal(t, open(t, tc.file, tc.password))
		})
	}
}

func TestWrongPassword(t *testing.T) {
	for _, name := range []string{"rc4-40.pdf", "rc4-128.pdf", "aes-128.pdf", "aes-256.pdf"} {
		_, err := OpenPassword(filepath.Join("..", "testdata", name), "nope")
		if err != ErrPassword {
			t.Errorf("%s: err = %v, want %v", name, err, ErrPassword)
		}
	}
}

// TestMalformedFiles truncates and corrupts every bundled file through the
// whole entry path. Nothing may panic, hang or allocate without bound.
func TestMalformedFiles(t *testing.T) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.pdf"))
	if len(names) == 0 {
		t.Skip("no testdata")
	}
	for _, name := range names {
		orig, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, frac := range []int{1, 2, 3, 4, 8} {
			b := make([]byte, len(orig)*(frac-1)/frac)
			copy(b, orig)
			mustNotPanic(t, name, b)
		}
		for i := 0; i < len(orig); i += 17 {
			b := make([]byte, len(orig))
			copy(b, orig)
			b[i] ^= 0xff
			mustNotPanic(t, name, b)
		}
	}
}

func mustNotPanic(t *testing.T, name string, b []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: panic: %v", name, r)
		}
	}()
	f, err := Load(b, "user")
	if err != nil {
		return
	}
	for i := 0; i < f.NumPages() && i < 8; i++ {
		page := f.Page(i)
		if st := f.GetStream(page["Contents"]); st != nil {
			st.Data()
		}
	}
	f.Close()
}

func FuzzLoad(fu *testing.F) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.pdf"))
	for _, name := range names {
		if b, err := os.ReadFile(name); err == nil {
			fu.Add(b)
		}
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		f, err := Load(b, "")
		if err != nil {
			return
		}
		for i := 0; i < f.NumPages() && i < 4; i++ {
			f.Page(i)
		}
	})
}

// BenchmarkJBIG2 decodes the fixtures, which between them cover the generic
// region on its own, a region carried by a globals stream, an intermediate
// region buffer, and a refinement region with typical prediction on. Run it on
// a board: this laptop carries load.
func BenchmarkJBIG2(b *testing.B) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.jb2"))
	var data [][]byte
	for _, name := range names {
		if v, err := os.ReadFile(name); err == nil {
			data = append(data, v)
		}
	}
	if len(data) == 0 {
		b.Skip("no JBIG2 fixtures")
	}
	b.ResetTimer()
	for b.Loop() {
		for _, v := range data {
			jbig2Decode(nil, v, nil, Ref{})
		}
	}
}

func FuzzJBIG2(fu *testing.F) {
	names, _ := filepath.Glob(filepath.Join("..", "testdata", "*.jb2"))
	for _, name := range names {
		if b, err := os.ReadFile(name); err == nil {
			fu.Add(b)
		}
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		jbig2Decode(nil, b, nil, Ref{})
	})
}

// conformanceDirs is the colon separated list of corpus roots, which is the
// knob every decoder in this family reads.
func conformanceDirs(t *testing.T) []string {
	t.Helper()

	env := os.Getenv("CONFORMANCE_DIR")
	if env == "" {
		t.Skip("set CONFORMANCE_DIR")
	}

	return strings.Split(env, ":")
}

// corpusFile finds a file the manifest names under whichever root holds it.
func corpusFile(dirs []string, name string) (string, bool) {
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// TestCorpus reads every file in the corpora named by $CONFORMANCE_DIR and
// holds the result to testdata/corpus.tsv, which records what MuPDF and
// poppler make of each one. It skips when the variable is unset.
func TestCorpus(t *testing.T) {
	dirs := conformanceDirs(t)
	manifest, err := os.ReadFile(filepath.Join("..", "testdata", "corpus.tsv"))
	if err != nil {
		t.Fatal(err)
	}

	var checked, missing, opened, mismatched int
	for _, line := range strings.Split(string(manifest), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		opens, pages := f[1] == "1", atoi(f[2])
		name, found := corpusFile(dirs, f[0])
		if !found {
			missing++
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			missing++
			continue
		}
		checked++

		got, ok := corpusPages(t, name, b)
		switch {
		case !opens:
			if ok {
				opened++
			}
		case !ok:
			t.Errorf("%s: will not open, but MuPDF and poppler do", f[0])
			mismatched++
		case pages >= 0 && got != pages:
			t.Errorf("%s: %d pages, MuPDF and poppler agree on %d", f[0], got, pages)
			mismatched++
		}
	}
	if checked == 0 {
		t.Skipf("no corpus files under %s", strings.Join(dirs, ":"))
	}
	t.Logf("%d files checked, %d not present, %d mismatched, %d opened that no other reader does",
		checked, missing, mismatched, opened)
}

func corpusPages(t *testing.T, name string, b []byte) (pages int, ok bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panic: %v", name, r)
			pages, ok = 0, false
		}
	}()
	f, err := Load(b, "")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return f.NumPages(), true
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}
