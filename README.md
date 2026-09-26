<p align="center"><img src="https://raw.githubusercontent.com/go-compressions/brand/main/social/go-compressions-lzip.png" alt="go-compressions/lzip" width="720"></p>

# lzip

[![ci](https://github.com/go-compressions/lzip/actions/workflows/ci.yml/badge.svg)](https://github.com/go-compressions/lzip/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-compressions/lzip.svg)](https://pkg.go.dev/github.com/go-compressions/lzip)

A pure-Go reader for the lzip (`.lz`) compressed file format.

```go
func NewReader(r io.Reader) (io.Reader, error)
```

```go
f, err := os.Open("archive.tar.lz")
// ...
r, err := lzip.NewReader(f)
// ...
_, err = io.Copy(dst, r)
```

`CGO_ENABLED=0`. The LZMA coding itself comes from
[`github.com/ulikunitz/xz/lzma`](https://pkg.go.dev/github.com/ulikunitz/xz/lzma);
this package is the framing around it, which is why it is small.

## There is no writer, on purpose

xz and zstd are read everywhere a `.lz` file might go and compress as well or
better. A new `.lz` written today would be a file fewer tools can open for no
gain in ratio, so it serves nobody. What a Go program does need is to read the
`.lz` files that already exist — GNU project tarballs, distribution archives,
anything a `plzip` pipeline produced. That is what this package does.

## What the format is

A `.lz` file is **one or more members concatenated**. Each member is:

| | |
|---|---|
| magic | `LZIP`, 4 bytes |
| version | `1`. Version 0 is obsolete and refused by name: it has no end-of-stream marker, so a version-0 member's LZMA stream cannot be found the way this reader finds a version-1 one. |
| coded dictionary size | 1 byte, **not a size** — see below |
| LZMA stream | raw, with **no** 13-byte `.lzma` header of its own |
| trailer | little-endian: CRC32 of the *uncompressed* data (u32), uncompressed size (u64), size of the whole member (u64) |

Because the member carries no `.lzma` header, this package synthesises one for
the LZMA decoder: the standard properties `lc=3 lp=0 pb=2` (property byte
`0x5d`), the decoded dictionary size, and an **unknown** uncompressed size, so
the decoder stops at the end-of-stream marker rather than at a byte count.

All three trailer fields are checked when the member ends. A decompressor that
does not check its own trailer is how a corrupt file becomes a silently short
one.

### The coded dictionary size

The byte at offset 5 is not a size. Its low five bits are a base-2 exponent, and
its top three bits subtract that many sixteenths of the resulting power of two —
which is how lzip expresses the sizes that fall between two powers of two (224
KiB, 1.5 MiB, 3 MiB …):

```
base = 1 << (b & 0x1f)
size = base - (base / 16) * ((b >> 5) & 7)
```

`lzip -9` on the 229 336-byte `testdata/big.txt` writes `0x52`: exponent 18 with
fraction 2, so `262144 - 16384*2 = 229376`, the 224 KiB dictionary. Read as a
size that byte is 82; read as an exponent alone it is 262144. Neither is the
number in the file.

## Test corpus

Every `.lz` file in `testdata/` was written by the **real GNU lzip 1.26 and
plzip 1.13**, not hand-assembled from the specification.
[`testdata/generate.sh`](testdata/generate.sh) regenerates them and **asserts its
own premise**: `lzip -dc` must read each file back before it is allowed to become
a fixture. Judging a new decoder against a generator nobody checked is how hours
disappear.

The fixtures for the malformed cases — bad magic, version 0, a wrong trailer
field, a truncated member, a damaged LZMA body — are derived in the tests by
mutating those real files, so even the negative cases start from bytes a real
compressor wrote. Tests assert **bytes**, not lengths.

| fixture | producer | members | what it covers |
|---|---|---|---|
| `single.lz` | `lzip -9` | 1 | one member |
| `empty.lz` | `lzip -9` | 1 | empty input still has a member |
| `onebyte.lz` | `lzip -9` | 1 | one byte |
| `concat3.lz` | `cat` of three `lzip` runs at `-9`, `-6`, `-0` | **3** | separately-compressed members concatenated |
| `big-split.lz` | `plzip -9 -B 64Ki` | **4** | one payload split into members by the producer |
| `big-0.lz` | `lzip -0` | 1 | 229 336 bytes through a 64 KiB dictionary — data larger than the dictionary |
| `big-3.lz`, `big-6.lz`, `big-9.lz` | `lzip -3/-6/-9` | 1 | compression levels; a 224 KiB (non-power-of-two) dictionary |

The corpus is embedded with `//go:embed`: the emulated CI lanes ship a
`go test -c` binary into a container with no `testdata/` directory, and a test
that opened a file there would be vacuous on six of the eight lanes.

### An outside judge

`witness_test.go` uses the real `lzip` and `plzip` as a judge that shares no code
with this package. When they are on `PATH` it has them compress inputs that are
**not** in the repository -- incompressible data, 400 KB of zeros, a length that
lands one byte past the 64 KiB dictionary of `lzip -0` -- at all ten levels, and
splits a payload at four member sizes with `plzip -B`, asserting the premise each
time (`lzip -dc` must return its own input) before judging us. It also checks
that we refuse the mutations `lzip -t` refuses.

Those tests **skip on all eight CI lanes**, which have no lzip. So what CI checks
is the committed corpus, and the judge is what a developer's machine adds on top.
Both matter: `testdata/generate.sh` guards against a mis-generated fixture at
generation time, and these tests guard against one that slipped through.

## Memory

The LZMA dictionary is allocated up front from the size coded in the member
header, before any data is decoded, because that size is the only statement of it
a member makes. lzip permits up to 512 MiB, so a **thirty-six byte file can ask
for a 512 MiB allocation** and nothing in the framing contradicts it until the
trailer, which is at the far end. A program decoding `.lz` files it did not
produce should bound that itself, by the size of the input or by refusing files
larger than it is willing to serve, rather than assume a small `.lz` means a
small decode.

## Ablations

Each row is the whole test suite run against a deliberately broken copy of the
decoder. A row that passes is a claim the tests do not actually support.

| # | ablation | whole suite | corpus tests alone |
|---|---|---|---|
| 1a | coded dictionary byte read as a literal size | **FAILS** | **FAILS** |
| 1b | coded dictionary byte read as an exponent only, top three bits dropped | **FAILS** | ⚠ **PASSES** |
| 1c | dictionary fraction added instead of subtracted | **FAILS** | ⚠ **PASSES** |
| 2 | stop after the first member | **FAILS** | **FAILS** |
| 3 | trailer CRC32 ignored | **FAILS** | **FAILS** |
| 4 | trailer uncompressed size ignored | **FAILS** | **FAILS** |
| 4b | trailer member size ignored | **FAILS** | **FAILS** |
| 5 | trailer fields read big-endian | **FAILS** | **FAILS** |
| 6 | version 0 accepted | **FAILS** | **FAILS** |

**Ablations 1b and 1c pass against the corpus**, and that is worth stating
plainly rather than hiding behind the green column. An LZMA dictionary *larger*
than the one the encoder used is harmless: the distances in the stream never
reach past the real size, so the decoder never notices the slack. And the range
check cannot catch an oversized reading either —
`TestExponentOnlyReadingIsInvisibleToDecoding` walks all 256 byte values and
shows that for each of the 137 legal ones, the exponent-only reading is also
within 4 KiB…512 MiB and never smaller than the true size. **No `.lz` file can
witness that mistake.** The only thing that can is `TestDictSizeFromCode`, which
reads the formula directly against values taken from the fixtures. That is why
the formula has a unit test of its own and not merely a round trip.

It still matters: the size in the file is what a caller is told about the memory
the decode will need, and 1b is a 14% over-allocation on `big-9.lz` alone.

## CI

Eight lanes, all gated on **100% statement coverage**: `linux/amd64`,
`linux/arm64`, `darwin/arm64`, `windows/amd64` native, and `riscv64`, `loong64`,
`ppc64le`, `s390x` under QEMU. `s390x` is big-endian, which is the lane that
would notice if the little-endian trailer reads were ever written with the host's
byte order.

## Licence

BSD-3-Clause. See [LICENSE](LICENSE).
