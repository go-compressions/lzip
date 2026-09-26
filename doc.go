// Copyright 2026 The go-compressions/lzip authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package lzip decodes the lzip (.lz) compressed file format.
//
// lzip is LZMA with its own framing. A .lz file is a sequence of one or more
// members concatenated; each member is a six-byte header, a raw LZMA stream
// with no .lzma header of its own, and a twenty-byte little-endian trailer
// carrying the CRC32 of the uncompressed data, the uncompressed size and the
// size of the whole member. The format is specified in the GNU lzip manual,
// "File format".
//
// [NewReader] returns a reader over the concatenation of every member's
// uncompressed data. The CRC32 and both sizes in each member's trailer are
// verified as the member is finished, so a truncated or corrupted file is
// reported as an error rather than returned as short data.
//
// # There is no writer
//
// This package decodes and does not encode, on purpose. xz and zstd are read
// everywhere a .lz file might go and compress as well or better, so a new .lz
// file written today serves nobody: it would be a file fewer tools can open,
// for no gain in ratio. What a program does need is the ability to read the .lz
// files that already exist -- Linux distribution archives, GNU project
// tarballs, and anything a plzip pipeline produced. That is what this package
// is for. The LZMA coding itself comes from github.com/ulikunitz/xz/lzma.
//
// # Test corpus
//
// Every .lz fixture in this repository was produced by the real GNU lzip 1.26
// and plzip 1.13, not hand-assembled from the specification, and
// testdata/generate.sh asserts that premise: lzip -dc must read each file back
// before it is allowed to be a fixture. Fixtures for the error paths -- a bad
// magic, version 0, a wrong trailer, a truncated member -- are derived in the
// tests by mutating those real files, so even the negative cases start from
// bytes a real compressor wrote.
package lzip
