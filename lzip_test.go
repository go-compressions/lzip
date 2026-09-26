// Copyright 2026 The go-compressions/lzip authors.
// SPDX-License-Identifier: BSD-3-Clause

package lzip

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"testing"
)

// The corpus is embedded, not read from disk: the emulated CI lanes ship a
// `go test -c` binary into a container that has no testdata/ directory, and a
// test that opens a file there would be skipped or fail on six of eight lanes.
//
//go:embed testdata
var corpus embed.FS

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := corpus.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("embedded fixture %s: %v", name, err)
	}
	return b
}

// decode runs the reader to completion and reports how many members it saw.
func decode(t *testing.T, in []byte) (out []byte, members int) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	out, err = io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return out, r.(*reader).members
}

// wantErr asserts that decoding in fails with target, whether at NewReader or
// during Read, and that a second Read keeps reporting the same error.
func wantErr(t *testing.T, in []byte, target error) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(in))
	if err != nil {
		if !errors.Is(err, target) {
			t.Fatalf("NewReader error = %v, want %v", err, target)
		}
		return
	}
	if _, err = io.ReadAll(r); !errors.Is(err, target) {
		t.Fatalf("ReadAll error = %v, want %v", err, target)
	}
	// The error is sticky: every later call reports the same one.
	if _, err2 := r.Read(make([]byte, 1)); !errors.Is(err2, target) {
		t.Fatalf("second Read error = %v, want the same %v", err2, target)
	}
}

// --- the coded dictionary size -------------------------------------------

// TestDictSizeFromCode pins the formula against values the real lzip emits
// (0x0c, 0x10, 0x52 are taken straight from the fixtures in testdata) and
// against the two ends of the permitted range. Two wrong readings of the byte
// are named in the table so it is clear what is being excluded: 0x52 is neither
// 82 (the byte as a size) nor 262144 (the exponent alone).
func TestDictSizeFromCode(t *testing.T) {
	for _, c := range []struct {
		code byte
		want int
		note string
	}{
		{0x0c, 4096, "exponent 12, no fraction: the 4 KiB minimum, in empty.lz"},
		{0x10, 65536, "exponent 16: the 64 KiB of lzip -0, in big-0.lz"},
		{0x12, 262144, "exponent 18, no fraction"},
		{0x52, 229376, "exponent 18 less 2/16: 224 KiB, in big-9.lz; not 82 and not 262144"},
		{0x53, 458752, "exponent 19 less 2/16: 448 KiB"},
		{0x95, 1572864, "exponent 21 less 4/16: the 1.5 MiB of lzip -2"},
		{0x1d, 1 << 29, "exponent 29: the 512 MiB maximum"},
		{0xfd, 1<<29 - (1<<29)/16*7, "exponent 29 less 7/16: 288 MiB, still legal"},
	} {
		got, err := dictSizeFromCode(c.code)
		if err != nil {
			t.Errorf("dictSizeFromCode(%#02x) = error %v, want %d (%s)", c.code, err, c.want, c.note)
			continue
		}
		if got != c.want {
			t.Errorf("dictSizeFromCode(%#02x) = %d, want %d (%s)", c.code, got, c.want, c.note)
		}
	}
}

func TestDictSizeFromCodeOutOfRange(t *testing.T) {
	for _, c := range []struct {
		code byte
		note string
	}{
		{0x00, "exponent 0: one byte, far below the 4 KiB minimum"},
		{0x0b, "exponent 11: 2 KiB, just below the minimum"},
		{0x1e, "exponent 30: 1 GiB, above the 512 MiB maximum"},
		{0x3f, "exponent 31 less 1/16: still above the maximum"},
		{0xec, "exponent 12 less 7/16: 2304 bytes, below the minimum"},
	} {
		if got, err := dictSizeFromCode(c.code); !errors.Is(err, ErrDictSize) {
			t.Errorf("dictSizeFromCode(%#02x) = %d, %v; want ErrDictSize (%s)", c.code, got, err, c.note)
		}
	}
}

func TestLZMAHeader(t *testing.T) {
	h := lzmaHeader(229376)
	want := []byte{0x5d, 0x00, 0x80, 0x03, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if !bytes.Equal(h, want) {
		t.Errorf("lzmaHeader(229376) = % x, want % x", h, want)
	}
	if lzmaProperties != 0x5d {
		t.Errorf("lzmaProperties = %#02x, want 0x5d for lc=3 lp=0 pb=2", lzmaProperties)
	}
}

// --- the corpus -----------------------------------------------------------

// TestFixtures decodes every file the real lzip and plzip wrote and compares
// bytes, not lengths, against the payload they were made from. It also asserts
// the member count, because a reader that stops after the first member of
// big-split.lz would return a quarter of the data and no error.
func TestFixtures(t *testing.T) {
	for _, c := range []struct {
		lz, plain  string
		wantMember int
		note       string
	}{
		{"single.lz", "hello.txt", 1, "one member, lzip -9"},
		{"empty.lz", "empty.txt", 1, "empty input still has a member"},
		{"onebyte.lz", "onebyte.txt", 1, "one byte"},
		{"big-0.lz", "big.txt", 1, "229_336 bytes through a 64 KiB dictionary"},
		{"big-3.lz", "big.txt", 1, "level 3"},
		{"big-6.lz", "big.txt", 1, "level 6"},
		{"big-9.lz", "big.txt", 1, "level 9, 224 KiB dictionary"},
		{"big-split.lz", "big.txt", 4, "plzip -B 64Ki: four members, one payload"},
	} {
		t.Run(c.lz, func(t *testing.T) {
			want := fixture(t, c.plain)
			got, members := decode(t, fixture(t, c.lz))
			if !bytes.Equal(got, want) {
				t.Errorf("%s (%s): decoded %d bytes, want %d; first difference at %d",
					c.lz, c.note, len(got), len(want), firstDiff(got, want))
			}
			if members != c.wantMember {
				t.Errorf("%s: read %d members, want %d", c.lz, members, c.wantMember)
			}
		})
	}
}

// TestConcatenatedMembers is the case a reader that returns after one member
// gets wrong while looking entirely healthy: three whole lzip members, each
// compressed at a different level, in one file.
func TestConcatenatedMembers(t *testing.T) {
	want := append(append(fixture(t, "part-a.txt"), fixture(t, "part-b.txt")...), fixture(t, "part-c.txt")...)
	got, members := decode(t, fixture(t, "concat3.lz"))
	if members != 3 {
		t.Errorf("read %d members, want 3", members)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

// TestEmptyMemberThenData puts a zero-byte member in front of a real one, so
// the first member contributes nothing and the reader must go on rather than
// report end of file.
func TestEmptyMemberThenData(t *testing.T) {
	in := append(fixture(t, "empty.lz"), fixture(t, "single.lz")...)
	want := fixture(t, "hello.txt")
	got, members := decode(t, in)
	if members != 2 {
		t.Errorf("read %d members, want 2", members)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

// TestOneByteAtATime crosses every member boundary with a one-byte buffer.
func TestOneByteAtATime(t *testing.T) {
	want := append(append(fixture(t, "part-a.txt"), fixture(t, "part-b.txt")...), fixture(t, "part-c.txt")...)
	r, err := NewReader(bytes.NewReader(fixture(t, "concat3.lz")))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	var got []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
	if n, err := r.Read(buf); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("Read after EOF = %d, %v; want 0, EOF", n, err)
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// --- malformed streams ----------------------------------------------------

// mutate copies a fixture and applies edits, so every negative case starts from
// bytes a real compressor wrote.
func mutate(t *testing.T, name string, edits map[int]byte) []byte {
	t.Helper()
	b := bytes.Clone(fixture(t, name))
	for at, v := range edits {
		b[at] = v
	}
	return b
}

func TestNotAnLzipStream(t *testing.T) {
	wantErr(t, []byte("this is not compressed at all"), ErrMagic)
}

func TestEmptyInput(t *testing.T) {
	wantErr(t, nil, ErrMagic)
}

func TestTruncatedHeader(t *testing.T) {
	wantErr(t, fixture(t, "single.lz")[:3], io.ErrUnexpectedEOF)
}

func TestVersionZeroRefusedByName(t *testing.T) {
	in := mutate(t, "single.lz", map[int]byte{4: 0})
	wantErr(t, in, ErrVersion0)
	_, err := NewReader(bytes.NewReader(in))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("version 0 is obsolete")) {
		t.Errorf("error = %v, want it to name version 0 as obsolete", err)
	}
}

func TestUnknownVersion(t *testing.T) {
	wantErr(t, mutate(t, "single.lz", map[int]byte{4: 2}), ErrVersion)
}

func TestBadDictionaryCode(t *testing.T) {
	wantErr(t, mutate(t, "single.lz", map[int]byte{5: 0x00}), ErrDictSize)
}

// TestTrailingData: a whole valid member followed by bytes that are not a
// member header. Silence here would be a reader that decodes part of a file and
// calls it the whole file.
func TestTrailingData(t *testing.T) {
	in := append(fixture(t, "single.lz"), []byte("trailing junk")...)
	wantErr(t, in, ErrMagic)
	r, _ := NewReader(bytes.NewReader(in))
	_, err := io.ReadAll(r)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("trailing data after member 1")) {
		t.Errorf("error = %v, want it to say where the trailing data is", err)
	}
}

func TestTruncatedTrailingHeader(t *testing.T) {
	in := append(fixture(t, "single.lz"), 'L', 'Z', 'I')
	wantErr(t, in, io.ErrUnexpectedEOF)
}

// TestTrailerMissing cuts the file at exactly the trailer boundary: the LZMA
// stream is whole, so only a reader that insists on its trailer notices.
func TestTrailerMissing(t *testing.T) {
	in := fixture(t, "single.lz")
	wantErr(t, in[:len(in)-trailerSize], io.ErrUnexpectedEOF)
}

func TestTrailerTruncated(t *testing.T) {
	in := fixture(t, "single.lz")
	wantErr(t, in[:len(in)-7], io.ErrUnexpectedEOF)
}

// TestTrailerCRCWrong flips a bit in the stored CRC32. The data decodes
// perfectly; only the trailer check can tell.
func TestTrailerCRCWrong(t *testing.T) {
	in := fixture(t, "single.lz")
	at := len(in) - trailerSize
	wantErr(t, mutate(t, "single.lz", map[int]byte{at: in[at] ^ 0x01}), ErrCRC)
}

func TestTrailerSizeWrong(t *testing.T) {
	in := fixture(t, "single.lz")
	at := len(in) - trailerSize + 4
	wantErr(t, mutate(t, "single.lz", map[int]byte{at: in[at] ^ 0x08}), ErrSize)
}

func TestTrailerMemberSizeWrong(t *testing.T) {
	in := fixture(t, "single.lz")
	at := len(in) - trailerSize + 12
	wantErr(t, mutate(t, "single.lz", map[int]byte{at: in[at] ^ 0x08}), ErrMemberSize)
}

// TestTrailerIsLittleEndian reads the three trailer fields the other way round
// and shows that a big-endian reader would reject a file real lzip wrote. It
// keeps the by-hand byte order honest independently of the decoder.
func TestTrailerIsLittleEndian(t *testing.T) {
	in := fixture(t, "single.lz")
	tr := in[len(in)-trailerSize:]
	plain := fixture(t, "hello.txt")
	le := uint64(tr[4]) | uint64(tr[5])<<8 | uint64(tr[6])<<16 | uint64(tr[7])<<24
	be := uint64(tr[11]) | uint64(tr[10])<<8 | uint64(tr[9])<<16 | uint64(tr[8])<<24
	if le != uint64(len(plain)) {
		t.Errorf("little-endian uncompressed size = %d, want %d", le, len(plain))
	}
	if be == uint64(len(plain)) {
		t.Errorf("big-endian read also gives %d; this fixture cannot tell the two apart", be)
	}
}

// TestCorruptCompressedData damages the LZMA body, which the LZMA layer reports
// rather than the framing.
func TestCorruptCompressedData(t *testing.T) {
	in := mutate(t, "big-9.lz", map[int]byte{200: 0x00, 201: 0xff, 202: 0x5a})
	r, err := NewReader(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("ReadAll succeeded on a damaged LZMA body, want an error")
	}
	if _, err := r.Read(make([]byte, 1)); err == nil {
		t.Fatal("second Read succeeded; the error should be sticky")
	}
}

// TestLZMAStartFailure exercises the one error return this package cannot reach
// with a real file, because it synthesises the .lzma header itself.
func TestLZMAStartFailure(t *testing.T) {
	saved := lzmaNewReader
	defer func() { lzmaNewReader = saved }()
	boom := errors.New("boom")
	lzmaNewReader = func(io.Reader) (io.Reader, error) { return nil, boom }
	if _, err := NewReader(bytes.NewReader(fixture(t, "single.lz"))); !errors.Is(err, boom) {
		t.Errorf("NewReader error = %v, want it to wrap %v", err, boom)
	}
}

// TestCountingReaderPropagatesError checks the counter passes a read error on
// unchanged; it is the reader that measures every member size.
func TestCountingReaderPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	c := &countingReader{r: errReader{boom}}
	if _, err := c.Read(make([]byte, 4)); !errors.Is(err, boom) {
		t.Errorf("Read error = %v, want %v", err, boom)
	}
	if c.n != 0 {
		t.Errorf("counted %d bytes on a failed read, want 0", c.n)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// TestSourceErrorSurfaces makes the underlying stream fail part-way through a
// member, which is not EOF and not a framing problem.
func TestSourceErrorSurfaces(t *testing.T) {
	boom := errors.New("disk went away")
	in := fixture(t, "big-9.lz")
	r, err := NewReader(io.MultiReader(bytes.NewReader(in[:1000]), errReader{boom}))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, boom) {
		t.Errorf("ReadAll error = %v, want %v", err, boom)
	}
}

func ExampleNewReader() {
	r, err := NewReader(bytes.NewReader([]byte{
		// `printf 'hi' | lzip -9`, byte for byte.
		0x4c, 0x5a, 0x49, 0x50, 0x01, 0x0c, 0x00, 0x34, 0x1a, 0x5c, 0xff, 0xff, 0xff,
		0xff, 0xf0, 0x00, 0x00, 0x00, 0xac, 0x2a, 0x93, 0xd8, 0x02, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x26, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}))
	if err != nil {
		fmt.Println(err)
		return
	}
	out, err := io.ReadAll(r)
	fmt.Printf("%q %v\n", out, err)
	// Output: "hi" <nil>
}

// TestExponentOnlyReadingIsInvisibleToDecoding records a blind spot, so nobody
// later concludes from a green corpus that the dictionary formula is pinned by
// it.
//
// Dropping the top three bits -- reading 0x52 as 262144 instead of 229376 --
// makes every fixture in testdata decode correctly anyway. An LZMA dictionary
// larger than the one the encoder used is harmless: the distances in the stream
// never reach past the real size, so the decoder never notices the slack. And
// the range check cannot catch it either: this test walks all 256 byte values
// and shows that whenever the correct size is legal, the exponent alone is also
// legal and never smaller. So no .lz file can witness that mistake, and the only
// thing that can is TestDictSizeFromCode reading the formula directly.
func TestExponentOnlyReadingIsInvisibleToDecoding(t *testing.T) {
	legal := 0
	for b := 0; b < 256; b++ {
		size, err := dictSizeFromCode(byte(b))
		if err != nil {
			continue
		}
		legal++
		exponentOnly := 1 << (byte(b) & 0x1f)
		if exponentOnly < size {
			t.Errorf("code %#02x: exponent-only reading %d is smaller than the true size %d, so a fixture could catch it",
				b, exponentOnly, size)
		}
		if exponentOnly < minDictSize || exponentOnly > maxDictSize {
			t.Errorf("code %#02x: exponent-only reading %d is out of range, so the range check would catch it",
				b, exponentOnly)
		}
	}
	if legal != 137 {
		t.Errorf("%d coded bytes are legal, want 137", legal)
	}
}
