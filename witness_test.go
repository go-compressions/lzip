// Copyright 2026 The go-compressions/lzip authors.
// SPDX-License-Identifier: BSD-3-Clause

package lzip

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os/exec"
	"strconv"
	"testing"
)

// The tests in this file use the real lzip(1) and plzip(1) as an outside judge.
// They share no code with this package: lzip decodes what lzip encoded, and the
// only question asked is whether we agree with it.
//
// This matters because testdata/ is our corpus. It was written by the real
// compressor, which is far better than hand-assembling members from the
// specification, but it is still nine files chosen by us. A witness on the
// machine can compress inputs nobody committed -- incompressible data, lengths
// that straddle a dictionary, member sizes we did not pick -- and disagree.
//
// CI has no lzip, so these tests skip on all eight lanes. That is the honest
// arrangement: the corpus is what CI checks, and the witness is what a
// developer with lzip installed can add on top. What CI cannot tell you is
// whether a fixture was mis-generated in the first place; testdata/generate.sh
// asserting `lzip -dc` on its own output is the guard for that, and these tests
// are the second one.

// judge returns the path of an outside witness, or skips.
func judge(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s(1) not on PATH: skipping the outside judge. "+
			"Without it, agreement is only with our own committed corpus, "+
			"which cannot fail the way a file from a real compressor can. "+
			"Install it (brew install %s) to run this test.", name, name)
	}
	return p
}

// run feeds stdin to a command and returns its stdout, failing on any error.
func run(t *testing.T, prog string, stdin []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(prog, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v (stderr: %s)", prog, args, err, errb.String())
	}
	return out.Bytes()
}

// TestOutsideJudgeAgreesOnTheCorpus decodes every committed fixture twice --
// once with the real lzip, once with this package -- and compares the two
// outputs. Unlike TestFixtures it does not trust the committed plaintext
// either: both sides are recomputed here.
func TestOutsideJudgeAgreesOnTheCorpus(t *testing.T) {
	lzip := judge(t, "lzip")
	for _, name := range []string{
		"single.lz", "empty.lz", "onebyte.lz", "concat3.lz",
		"big-0.lz", "big-3.lz", "big-6.lz", "big-9.lz", "big-split.lz",
	} {
		t.Run(name, func(t *testing.T) {
			in := fixture(t, name)
			want := run(t, lzip, in, "-dc")
			got, _ := decode(t, in)
			if !bytes.Equal(got, want) {
				t.Errorf("we decoded %d bytes, lzip -dc decoded %d; first difference at %d",
					len(got), len(want), firstDiff(got, want))
			}
		})
	}
}

// payloads returns inputs no committed fixture covers, deterministically so a
// failure can be reproduced.
func payloads() map[string][]byte {
	rng := rand.New(rand.NewSource(20260926))
	incompressible := make([]byte, 300000)
	rng.Read(incompressible)
	zeros := make([]byte, 400000)
	oneWord := bytes.Repeat([]byte("abcdefgh"), 50000)
	// A length that lands just past the 64 KiB dictionary of lzip -0.
	straddle := make([]byte, 65537)
	for i := range straddle {
		straddle[i] = byte(i%251) ^ byte(i>>8)
	}
	return map[string][]byte{
		"empty":          {},
		"one-byte":       {0x00},
		"incompressible": incompressible,
		"all-zero":       zeros,
		"one-word":       oneWord,
		"straddles-64ki": straddle,
	}
}

// TestOutsideJudgeCompressesFreshInput has the real lzip compress inputs that
// are not in the repository, at every compression level, and checks that this
// package gives the payload back byte for byte.
func TestOutsideJudgeCompressesFreshInput(t *testing.T) {
	lzip := judge(t, "lzip")
	for name, want := range payloads() {
		for level := 0; level <= 9; level++ {
			t.Run(name+"/-"+strconv.Itoa(level), func(t *testing.T) {
				lz := run(t, lzip, want, "-"+strconv.Itoa(level), "-c")
				// Assert the premise before judging us: lzip must read
				// back what lzip just wrote.
				if back := run(t, lzip, lz, "-dc"); !bytes.Equal(back, want) {
					t.Fatalf("PREMISE FAILED: lzip -dc did not return its own input for %s at -%d", name, level)
				}
				got, members := decode(t, lz)
				if !bytes.Equal(got, want) {
					t.Errorf("decoded %d bytes, want %d; first difference at %d",
						len(got), len(want), firstDiff(got, want))
				}
				if members != 1 {
					t.Errorf("read %d members, want 1", members)
				}
			})
		}
	}
}

// TestOutsideJudgeMultiMember has plzip split payloads into member counts we did
// not choose, and asserts both the bytes and the count.
func TestOutsideJudgeMultiMember(t *testing.T) {
	plzip := judge(t, "plzip")
	lzip := judge(t, "lzip")
	want := fixture(t, "big.txt")
	for _, size := range []string{"64Ki", "100Ki", "128Ki", "1Mi"} {
		t.Run(size, func(t *testing.T) {
			lz := run(t, plzip, want, "-9", "-B", size, "-c")
			if back := run(t, lzip, lz, "-dc"); !bytes.Equal(back, want) {
				t.Fatalf("PREMISE FAILED: lzip -dc did not return plzip -B %s's input", size)
			}
			got, members := decode(t, lz)
			if !bytes.Equal(got, want) {
				t.Errorf("decoded %d bytes, want %d; first difference at %d",
					len(got), len(want), firstDiff(got, want))
			}
			// Count the LZIP magics independently of our own reader, so
			// the count is not asserted against the thing under test.
			// A magic could in principle occur inside a compressed
			// body; it does not for these inputs, and if it ever did
			// this check would have to be reached differently rather
			// than relaxed.
			if n := bytes.Count(lz, magic[:]); members != n {
				t.Errorf("we read %d members; there are %d LZIP magics in the stream", members, n)
			}
			t.Logf("plzip -B %s gave %d members", size, members)
		})
	}
}

// TestOutsideJudgeRejectsWhatWeReject checks the two decoders agree that a
// damaged file is damaged. A decompressor that accepts a corrupt member is the
// failure this repository's trailer checks exist to prevent, and lzip is the
// reference for what "corrupt" means.
func TestOutsideJudgeRejectsWhatWeReject(t *testing.T) {
	lzipPath := judge(t, "lzip")
	base := fixture(t, "big-9.lz")
	for _, c := range []struct {
		name string
		at   int
		xor  byte
	}{
		{"compressed body", 500, 0xff},
		{"trailer CRC", len(base) - trailerSize, 0x01},
		{"trailer uncompressed size", len(base) - trailerSize + 4, 0x08},
		{"trailer member size", len(base) - trailerSize + 12, 0x08},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := bytes.Clone(base)
			in[c.at] ^= c.xor
			cmd := exec.Command(lzipPath, "-t")
			cmd.Stdin = bytes.NewReader(in)
			if err := cmd.Run(); err == nil {
				t.Skipf("lzip -t accepts this mutation, so it is not a corruption to judge by")
			}
			r, err := NewReader(bytes.NewReader(in))
			if err != nil {
				return // refused at the header, which is a refusal
			}
			if _, err := io.ReadAll(r); err == nil {
				t.Errorf("we accepted a stream lzip -t rejects")
			} else if errors.Is(err, io.EOF) {
				t.Errorf("we reported EOF rather than an error for a stream lzip -t rejects")
			}
		})
	}
}
