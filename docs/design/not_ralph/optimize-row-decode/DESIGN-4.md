# Row decode part 4: stop copying a 312-byte struct per value

Status: DESIGN, pre-implementation. Branch `optimize-row-decode`.
Follows parts 1–3. Profiles are **fresh, on the current build** (`7642bac63`).

## 0. Summary

`decodeRowRangeInfo` is the largest single flat cost on both corpora — 10.0%
of TPC-H CPU and 17.7% of TPC-DS. Line-level profiling shows that cost is not
in the decoding: **it is in copying structs by value inside the per-value
loop.**

| what | size | copied | TPC-DS flat |
|---|---:|---|---:|
| `c := cols[i]` — a whole `catalog.Column` | **312 B** | per column per row | **5.62 s (2.5%)** |
| `c.Type` passed by value to the decoder | 48 B | per column per row | part of 25.69 s |
| `dst[i] = v` — a `Datum` | 48 B | per column per row | 4.52 s (2.0%) |

`catalog.Column` is **312 bytes**. Nothing in the loop mutates it, and every
use is a field read. The copy buys nothing.

## 1. Evidence

Line-level profile of `decodeRowRangeInfo`, TPC-DS SF0.5, current build
(`go tool pprof -list`), flat / cum:

```
39.51s  82.63s (flat, cum) 37.10% of Total
 5.62s   5.62s  1418:  c := cols[i]
25.69s  67.85s  1461:  v, consumed, err := decodePhysicalPGValueLowered(c.Type, tname, data[off:], sctx, st)
 4.52s   4.54s  1465:  dst[i] = v
  630ms  630ms  1446:  align, tname, isVarlena = info[i].align, info[i].lower, info[i].isVarlena
  630ms  630ms  1433:  if len(bitmap) > 0 && (bitmap[i/8]>>(uint(i)%8))&1 == 0 {
  350ms  1.29s  1455:  off = catalog.AttAlignPointer(data, off, align, isVarlena)
```

Line 1461's **flat** 25.69 s is argument marshalling and return handling, not
the callee (the callee is the 67.85 s cum). It passes a 48-byte `catalog.Type`
by value and returns a 48-byte `Datum` by value, per value decoded.

Note what is *not* hot: the null-bitmap test (630 ms), the memo lookup
(630 ms), and alignment (350 ms flat) are all cheap. Parts 1–3 removed the
string work; what remains is data movement.

## 2. Design

### Cut A — `c := &cols[i]` (312 B → 8 B)

Every use of `c` in the loop is a read: `c.MissingValue` (only on the
`i >= storedNatts` path), `c.Type` / `c.Type.Name`, and `c.Name` (only in an
error message). A pointer serves all of them unchanged.

Trivially safe, entirely local to one function, and directly addresses the
5.62 s.

**Why the compiler does not already do this:** Go does not reliably elide a
struct copy for a value assignment from a slice element, and the profile shows
it did not here. This is not premature micro-optimisation of something the
optimiser handles; it is 2.5% of TPC-DS CPU measured on a line.

### Cut B — pass `*catalog.Type` to the decoder (48 B per value)

`decodePhysicalPGValueLowered(t catalog.Type, …)` takes its type by value. It
has **5 call sites** (one hot, one wrapper, three in one test), so changing the
signature to `*catalog.Type` is tractable.

The decoder must not retain the pointer beyond the call — it currently receives
a copy, so any retention would silently become aliasing. That has to be
verified by reading the function, not assumed, and is the one real hazard in
this cut.

### Cut C — NOT proposed: `dst[i] = v`

The 4.52 s on line 1465 is a 48-byte `Datum` store, and it is the loop's actual
product. Removing it would mean the decoder writing directly into `dst[i]`,
which changes the decoder's contract from "return a value" to "own a
destination". That is a larger refactor with an aliasing surface, and it is
**not** proposed here. Recorded so the remaining 2.0% is accounted for rather
than silently ignored.

## 3. Correctness plan

Neither cut changes any value; both change how bytes move.

- **Values byte-identical**: TPC-H 24/24 ordered digests, TPC-DS SF0.5
  `PASS=95` all-zero.
- **Aliasing check for cut B**: read `decodePhysicalPGValueLowered` and confirm
  it neither stores `t` nor returns anything pointing into it. If it does, cut B
  is withdrawn — a 48-byte saving is not worth an aliasing bug.
- **The `MissingValue` path must stay correct.** It is the rarely-taken
  `i >= storedNatts` arm (a column added by `ALTER TABLE` after the row was
  written). It is exercised by neither TPC corpus, so a dedicated test is
  required: add a column with a default to a table with existing rows, read
  them back, assert the default appears. Mutation-check it.
- Units scope, `-race` on `internal/executor`, `tpch-spotcheck.sh`.

## 4. What is NOT claimed

The 5.62 s + part-of-25.69 s is **the cost of the copies**, an upper bound on
what removing them recovers, not a forecast. Part 2's report records the
converse failure — a bound derived from a stale profile — so note that this
one is derived from a profile of the build being changed.

TPC-H's share is smaller (10.0% flat vs TPC-DS's 17.7%), so as with part 3,
**TPC-DS is expected to move more**. A TPC-H-only gain would be the surprising
outcome.
