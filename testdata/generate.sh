#!/bin/sh
# Regenerate the test corpus with the real GNU lzip(1) and plzip(1).
#
# Every .lz file in this directory was produced by GNU lzip or plzip, not by
# us. Run this script from the directory that contains it.
#
# The script asserts its own premise: `lzip -dc` must read back every member it
# has just written before the file is allowed to become a fixture. A generator
# that writes streams its own decoder refuses is worse than no generator at
# all, and judging a new decoder against one is how hours disappear.
set -e
command -v lzip  >/dev/null || { echo "lzip(1) not found"  >&2; exit 1; }
command -v plzip >/dev/null || { echo "plzip(1) not found" >&2; exit 1; }
lzip --version  | head -n 1
plzip --version | head -n 1

# --- payloads -------------------------------------------------------------
printf 'the quick brown fox jumps over the lazy dog; the quick brown fox jumps over the lazy dog; and again, the quick brown fox.' > hello.txt
printf '' > empty.txt
printf 'A' > onebyte.txt
printf 'first member payload, repeated: first member payload, repeated.'   > part-a.txt
printf 'second member payload, repeated: second member payload, repeated.' > part-b.txt
printf 'third member payload, repeated: third member payload, repeated.'   > part-c.txt
python3 - <<'PY'
import random
random.seed(7)
w = ['alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta']
out = [w[random.randrange(8)] + str(i % 97) for i in range(30000)]
open('big.txt', 'w').write(' '.join(out))
PY

# --- members --------------------------------------------------------------
lzip -9 -c hello.txt   > single.lz
lzip -9 -c empty.txt   > empty.lz
lzip -9 -c onebyte.txt > onebyte.lz

# Three members, each compressed on its own and at a different level, then
# concatenated: what `cat a.lz b.lz c.lz` produces.
lzip -9 -c part-a.txt > .a.lz
lzip -6 -c part-b.txt > .b.lz
lzip -0 -c part-c.txt > .c.lz
cat .a.lz .b.lz .c.lz > concat3.lz
rm -f .a.lz .b.lz .c.lz

# Compression levels over one payload. -0 gives a 64 KiB dictionary, which is
# smaller than big.txt, so that file is also the "data larger than the
# dictionary" case. -3/-6/-9 give a 224 KiB dictionary, which is NOT a power of
# two: it is the case a decoder that reads the coded byte naively gets wrong.
lzip -0 -c big.txt > big-0.lz
lzip -3 -c big.txt > big-3.lz
lzip -6 -c big.txt > big-6.lz
lzip -9 -c big.txt > big-9.lz

# One payload split by plzip into several members. lzip 1.26's own -b is a
# limit, not a split, and leaves a single member for an input this size, so the
# multimember fixture comes from plzip -- which is the producer that matters
# anyway.
plzip -9 -B 64Ki -c big.txt > big-split.lz

# --- assert the premise ---------------------------------------------------
for f in single empty onebyte big-0 big-3 big-6 big-9 big-split; do
  case $f in
    single)  p=hello   ;;
    empty)   p=empty   ;;
    onebyte) p=onebyte ;;
    *)       p=big     ;;
  esac
  lzip -dc "$f.lz" | cmp - "$p.txt" || { echo "PREMISE FAILED: $f.lz" >&2; exit 1; }
done
cat part-a.txt part-b.txt part-c.txt > .cat.txt
lzip -dc concat3.lz | cmp - .cat.txt || { echo "PREMISE FAILED: concat3.lz" >&2; exit 1; }
rm -f .cat.txt
echo "premise OK: lzip -dc read back every fixture"
lzip -lv ./*.lz
