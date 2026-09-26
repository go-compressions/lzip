// Copyright 2026 The go-compressions/lzip authors.
// SPDX-License-Identifier: BSD-3-Clause

package lzip

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/ulikunitz/xz/lzma"
)

// headerSize is the size of a member header: magic (4) + version (1) + coded
// dictionary size (1).
const headerSize = 6

// trailerSize is the size of a member trailer: CRC32 of the uncompressed data
// (4) + uncompressed size (8) + member size (8), all little-endian.
const trailerSize = 20

// magic opens every member.
var magic = [4]byte{'L', 'Z', 'I', 'P'}

// version is the only format version this package decodes. Version 0 exists in
// the wild but is obsolete: it has no end-of-stream marker, so a version-0
// member cannot be found the way this reader finds the end of a version-1 one.
const version = 1

// The dictionary size lzip permits, from the format specification: 4 KiB to
// 512 MiB inclusive.
const (
	minDictSize = 1 << 12
	maxDictSize = 1 << 29
)

// lzmaProperties is the LZMA property byte for the parameters lzip always
// uses: lc=3 literal context bits, lp=0 literal position bits, pb=2 position
// bits. LZMA packs them as (pb*5+lp)*9+lc.
const lzmaProperties = byte((2*5+0)*9 + 3)

// unknownSize is the value the .lzma header uses for "the uncompressed size is
// not recorded". It makes the LZMA decoder stop at the end-of-stream marker,
// which is where an lzip member's LZMA stream ends.
const unknownSize = ^uint64(0)

// Errors reported for a malformed or corrupt stream. Each is wrapped with
// detail, so test with [errors.Is].
var (
	// ErrMagic reports that the four bytes where a member header was
	// expected are not "LZIP".
	ErrMagic = errors.New("lzip: not an lzip stream")
	// ErrVersion0 reports the obsolete version 0 of the format by name.
	ErrVersion0 = errors.New("lzip: format version 0 is obsolete and not supported")
	// ErrVersion reports a format version this package does not know.
	ErrVersion = errors.New("lzip: unsupported format version")
	// ErrDictSize reports a coded dictionary size outside the range the
	// format permits.
	ErrDictSize = errors.New("lzip: dictionary size out of range")
	// ErrCRC reports that a member's data does not match the CRC32 in its
	// trailer.
	ErrCRC = errors.New("lzip: CRC mismatch")
	// ErrSize reports that a member's uncompressed size does not match its
	// trailer.
	ErrSize = errors.New("lzip: uncompressed size mismatch")
	// ErrMemberSize reports that the bytes consumed by a member do not
	// match the member size in its trailer.
	ErrMemberSize = errors.New("lzip: member size mismatch")
)

// dictSizeFromCode decodes byte 5 of a member header.
//
// That byte is not a size. The low five bits are a base-2 exponent, and the top
// three bits subtract that many sixteenths of the resulting power of two, which
// is how lzip expresses the sizes between two powers of two (224 KiB, 1.5 MiB,
// 3 MiB and so on):
//
//	base = 1 << (b & 0x1f)
//	size = base - (base / 16) * ((b >> 5) & 7)
//
// For example 0x52 is exponent 18 with fraction 2: 262144 - 16384*2 = 229376,
// which is the 224 KiB dictionary lzip -9 chooses for a 229_336-byte input. A
// decoder that treats the byte as a size reads 0x52 as 82 bytes; one that keeps
// only the exponent reads it as 262144. Neither is the number in the file.
func dictSizeFromCode(b byte) (int, error) {
	base := uint64(1) << (b & 0x1f)
	size := base - (base/16)*uint64((b>>5)&7)
	if size < minDictSize || size > maxDictSize {
		return 0, fmt.Errorf("%w: coded byte %#02x gives %d bytes, want %d..%d",
			ErrDictSize, b, size, minDictSize, maxDictSize)
	}
	return int(size), nil
}

// lzmaHeader synthesises the thirteen-byte .lzma header that the LZMA decoder
// expects and an lzip member does not carry.
func lzmaHeader(dictSize int) []byte {
	h := make([]byte, lzma.HeaderLen)
	h[0] = lzmaProperties
	binary.LittleEndian.PutUint32(h[1:5], uint32(dictSize))
	binary.LittleEndian.PutUint64(h[5:13], unknownSize)
	return h
}

// countingReader counts the bytes handed out, which is how a member's own size
// is measured against the figure in its trailer. It deliberately sits above the
// buffering, so what it counts is what the member consumed and not what the
// buffer read ahead.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// lzmaNewReader is a seam: the tests replace it to exercise the path where the
// LZMA layer refuses the header, which cannot happen with a header this package
// synthesises itself.
var lzmaNewReader = func(r io.Reader) (io.Reader, error) { return lzma.NewReader(r) }

// reader decodes the concatenation of every member in a .lz stream.
type reader struct {
	src *countingReader
	lz  io.Reader

	crc   uint32 // running CRC32 of the current member's output
	size  uint64 // uncompressed bytes of the current member
	start int64  // src.n when the current member's header began

	members int   // members fully read and verified
	err     error // sticky
}

// NewReader returns a reader over the uncompressed data of every member of the
// lzip stream r, in order. The first member's header is read and checked
// immediately, so a stream that is not lzip at all is reported here.
//
// Each member's trailer is verified when that member ends: the CRC32 and the
// uncompressed size must match the data produced, and the member size must
// match the bytes consumed. Read reports the first disagreement as an error and
// every later call returns the same one.
func NewReader(r io.Reader) (io.Reader, error) {
	z := &reader{src: &countingReader{r: bufio.NewReader(r)}}
	more, err := z.nextMember()
	if err != nil {
		return nil, err
	}
	if !more {
		return nil, fmt.Errorf("%w: empty input", ErrMagic)
	}
	return z, nil
}

// nextMember reads and validates the header of the member at the current
// position and prepares an LZMA decoder for it. It reports false with no error
// when the stream ends cleanly at a member boundary.
func (z *reader) nextMember() (bool, error) {
	z.start = z.src.n
	var h [headerSize]byte
	switch _, err := io.ReadFull(z.src, h[:]); {
	case errors.Is(err, io.EOF):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("lzip: reading member header: %w", err)
	}
	if !bytes.Equal(h[:4], magic[:]) {
		if z.members > 0 {
			return false, fmt.Errorf("%w: trailing data after member %d is not a member header (%#02x)",
				ErrMagic, z.members, h[:4])
		}
		return false, fmt.Errorf("%w: magic is %#02x, want %q", ErrMagic, h[:4], magic[:])
	}
	switch h[4] {
	case 0:
		return false, ErrVersion0
	case version:
	default:
		return false, fmt.Errorf("%w: %d", ErrVersion, h[4])
	}
	dictSize, err := dictSizeFromCode(h[5])
	if err != nil {
		return false, err
	}
	lz, err := lzmaNewReader(io.MultiReader(bytes.NewReader(lzmaHeader(dictSize)), z.src))
	if err != nil {
		return false, fmt.Errorf("lzip: starting LZMA stream: %w", err)
	}
	z.lz, z.crc, z.size = lz, 0, 0
	return true, nil
}

// finishMember reads the trailer of the member just decoded and checks it
// against what was actually produced and consumed.
func (z *reader) finishMember() error {
	var t [trailerSize]byte
	if _, err := io.ReadFull(z.src, t[:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("lzip: reading member trailer: %w", err)
	}
	wantCRC := binary.LittleEndian.Uint32(t[0:4])
	wantSize := binary.LittleEndian.Uint64(t[4:12])
	wantMember := binary.LittleEndian.Uint64(t[12:20])
	if z.crc != wantCRC {
		return fmt.Errorf("%w in member %d: computed %#08x, trailer says %#08x",
			ErrCRC, z.members+1, z.crc, wantCRC)
	}
	if z.size != wantSize {
		return fmt.Errorf("%w in member %d: produced %d bytes, trailer says %d",
			ErrSize, z.members+1, z.size, wantSize)
	}
	if got := uint64(z.src.n - z.start); got != wantMember {
		return fmt.Errorf("%w in member %d: consumed %d bytes, trailer says %d",
			ErrMemberSize, z.members+1, got, wantMember)
	}
	z.members++
	return nil
}

// Read implements io.Reader over the concatenated members.
func (z *reader) Read(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	for {
		n, err := z.lz.Read(p)
		if n > 0 {
			z.crc = crc32.Update(z.crc, crc32.IEEETable, p[:n])
			z.size += uint64(n)
		}
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, io.EOF) {
			z.err = err
			return n, err
		}
		// This member's LZMA stream hit its end-of-stream marker. Check
		// the trailer, then look for another member: a .lz file may hold
		// any number of them, and stopping here would silently truncate
		// everything a plzip pipeline wrote.
		if err := z.finishMember(); err != nil {
			z.err = err
			return n, err
		}
		more, err := z.nextMember()
		if err != nil {
			z.err = err
			return n, err
		}
		if !more {
			z.err = io.EOF
			return n, io.EOF
		}
		if n > 0 {
			return n, nil
		}
	}
}
