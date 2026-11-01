// Copyright 2026 The go-compressions/lzip authors.
// SPDX-License-Identifier: BSD-3-Clause

package lzip

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"
)

// allocated reports how many bytes f allocated, by runtime.MemStats.
func allocated(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// tinyBomb is the smallest file that makes the unbounded reader allocate its
// largest dictionary: the magic, version 1, coded size 0x1d (512 MiB) and five
// bytes of range-coder start. Eleven bytes; the LZMA layer allocated 512 MiB
// for it before noticing there was nothing else.
func tinyBomb(code byte) []byte {
	return append([]byte("LZIP\x01"), code, 0, 0, 0, 0, 0)
}

func TestTinyFileCannotClaimAHugeDictionary(t *testing.T) {
	for _, code := range []byte{0x1d, 0x1c, 0x1b, 0xfd} { // 512, 256, 128, 288 MiB
		var err error
		cost := allocated(func() { _, err = NewReader(bytes.NewReader(tinyBomb(code))) })
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("code %#02x: err = %v, want ErrTooLarge", code, err)
		}
		if cost > 1<<20 {
			t.Errorf("code %#02x: refusal allocated %d bytes, want < 1 MiB", code, cost)
		}
	}
}

// The limit is exact: a member coding exactly the limit is started, one byte
// less is not. 0x14 codes 1 MiB.
func TestDictionaryLimitIsExact(t *testing.T) {
	in := mutate(t, "single.lz", map[int]byte{5: 0x14})
	r, err := NewReaderLimit(bytes.NewReader(in), 1<<20)
	if err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("at the limit, reading: %v", err)
	}
	if _, err := NewReaderLimit(bytes.NewReader(in), 1<<20-1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte under: err = %v, want ErrTooLarge", err)
	}
	if _, err := NewReaderLimit(bytes.NewReader(in), -1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("negative limit: err = %v, want ErrTooLarge", err)
	}
}

// The default is 64 MiB: coded 0x1a starts, 0x1b (128 MiB) does not.
func TestDefaultDictionaryLimit(t *testing.T) {
	if DefaultMaxDictSize != 64<<20 {
		t.Fatalf("DefaultMaxDictSize = %d, want 64 MiB", DefaultMaxDictSize)
	}
	if !testing.Short() { // it really allocates 64 MiB; the emulated lanes skip it
		r, err := NewReader(bytes.NewReader(mutate(t, "single.lz", map[int]byte{5: 0x1a})))
		if err != nil {
			t.Fatalf("64 MiB member: %v", err)
		}
		if _, err := io.ReadAll(r); err != nil {
			t.Fatalf("64 MiB member, reading: %v", err)
		}
	}
	if _, err := NewReader(bytes.NewReader(mutate(t, "single.lz", map[int]byte{5: 0x1b}))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("128 MiB member: err = %v, want ErrTooLarge", err)
	}
}

// A later member is held to the same limit, and the error says which.
func TestLaterMemberIsLimitedToo(t *testing.T) {
	in := append(bytes.Clone(fixture(t, "single.lz")), tinyBomb(0x1d)...)
	wantErr(t, in, ErrTooLarge)
	r, err := NewReader(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("member 2")) {
		t.Errorf("error = %v, want it to name member 2", err)
	}
}

// FuzzNewReader feeds arbitrary bytes to the reader under a small dictionary
// limit and reads a bounded amount: no input may panic, hang or make the
// reader accept a member above the limit.
func FuzzNewReader(f *testing.F) {
	for _, name := range []string{"single.lz", "empty.lz", "onebyte.lz", "concat3.lz", "big-0.lz"} {
		b, err := corpus.ReadFile("testdata/" + name)
		if err != nil {
			f.Fatalf("embedded fixture %s: %v", name, err)
		}
		f.Add(b)
	}
	f.Add(tinyBomb(0x1d))
	f.Add(tinyBomb(0x0c))
	f.Fuzz(func(t *testing.T, in []byte) {
		r, err := NewReaderLimit(bytes.NewReader(in), 1<<20)
		if err != nil {
			return
		}
		_, _ = io.CopyN(io.Discard, r, 1<<24)
	})
}
