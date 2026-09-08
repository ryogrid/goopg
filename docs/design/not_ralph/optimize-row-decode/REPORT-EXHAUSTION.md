# Row-decode optimisation: exhaustion report

Branch `optimize-row-decode`. Final build `9112c762c`. Date: 2026-09-08.

**Conclusion: the class of defect that produced all four wins in this series is
exhausted. What remains is either real work, or requires an architectural
change rather than an optimisation.** Evidence below, from profiles of the
**final** build — not the ones that motivated the changes.

## 1. What was delivered

| part | change | TPC-H | TPC-DS |
|---|---|---:|---:|
| 1 | per-row `LookupEnum` → once per scan | **−11.6%** | not measured |
| 2 | thread the column memo; add `isVarlena` | **−9.6%** | **−9.2%** |
| 3 | remove the row pool (0.1% hit rate) | **−5.6%** | **−2.0%** |
| 4 | stop copying 312 B + 48 B per value | **−5.3%** | not measured |

**TPC-H cumulative: 115.37 s → 82.29 s, −28.7% (0.713×).**
Every part: values byte-identical (24/24 ordered digests; TPC-DS `PASS=95`
all-zero where measured), units gate green, ranges disjoint.

**Every one of the four was the same defect in a different disguise: a
per-column fact recomputed per row.** In three of the four, a sibling code path
had *already* solved it and the fix was to make the two agree — the enum
lookup (seq scan had it, index scan did not), the column memo (seq scan
threaded it, index and bitmap scans did not), and the varlena test (memoised
for two of three string derivations, missed for the third).

## 2. Why the class is exhausted

The four wins were found by one question: *is a per-column or per-scan fact
being computed per row?* Applied to the final build's profiles, the answer is
now no.

**TPC-H, final build** (`decodeRowRangeInfo`, `pprof -list`) — the loop that
carried three of the four fixes:

```
23.24s  62.73s (flat, cum) 24.04% of Total
  110ms   110ms  1422:  c := &cols[i]            <- was 5.62s as a 312-byte copy
  670ms   670ms  1450:  align, tname, isVarlena = info[i]...   <- the memo, cheap
   90ms   1.05s  1459:  off = catalog.AttAlignPointer(...)
16.44s  54.88s  1465:  v, consumed, err := decodePhysicalPGValueLowered(&c.Type, ...)
 3.87s   3.96s  1469:  dst[i] = v
```

Nothing in that loop recomputes anything per row any more. The memo lookup is
670 ms; the null-bitmap test 110 ms; alignment 90 ms flat. **The string work,
the pool, and the struct copies are all gone from the profile.**

`decodePhysicalPGValueLowered`'s own flat cost (11.91 s) is spread across
~40 lines with no line above 790 ms, and every one of them is byte parsing or
`Datum` construction — i.e. the actual job.

## 3. What remains, and why each is not an optimisation

| remaining cost | TPC-H | TPC-DS | why it stays |
|---|---:|---:|---|
| `Datum` movement (call/return/store) | ~20 s | ~large | **cut C — architectural**, see §3.1 |
| `cloneRowOwned` row retention | 10.0% cum | **30.3% cum** | **ownership contract — architectural**, §3.2 |
| `decodePhysicalPGValueLowered` parsing | 14.7% cum | 15.2% cum | real work: byte parsing |
| `NumericInt64FromStoredPayload` | 6.0% cum | — | real work: `math/big`, §3.3 |
| `mallocgc` | 13.8% cum | — | consequence of the above |
| `Syscall6` | 3.3% | — | I/O |

### 3.1 Cut C — rejected on a measured maintainability trade

The `dst[i] = v` store (3.87 s) and much of the 16.44 s call-site cost are the
same thing: a 48-byte `Datum` returned by value and stored. Eliminating it
means the decoder writing into a caller-owned destination.

**`decodePhysicalPGValueLowered` is 462 lines with 55 `return` statements.**
All 55 would have to be rewritten — in the function that decodes every value
read from disk, where a mistake is *wrong data*, not slow data. Part of the
remaining cost is also an unavoidable **write barrier**: `Datum` contains a
`Buf []byte`, so any store to `dst[i]` traps regardless of how the value
arrives.

For ~2% of one corpus, that is "severe maintainability regression out of
proportion to the measured gain" under the project's own SKIP policy. Rejected
with numbers, not deferred vaguely.

### 3.2 Row cloning — needs the ownership contract, which is a redesign

`cloneRowOwned` is **30.3% cum on TPC-DS** on the final build (`fetchExact` →
97.3% of its callers). Its cost is now `make` (16.01 s) + `MaterializeArena`
(18.50 s) + the copy loop (16.69 s) — no remaining overhead, just the work.

It exists because a scan reuses one `scanRow` buffer, so any row that escapes
downstream must be copied. **This is the same root cause that made the row
pool useless in part 3**: rows flow into joins, aggregates and sorts with no
statement of when the consumer is done with them.

Fixing it means an ownership/lifetime contract across the executor — a
redesign, not an optimisation, and the `minimize_datum` workstream already
attacks retained bytes from a different direction. `row_pool.go` retains
`releaseRow` as an explicit no-op precisely so that redesign has a hook.

### 3.3 Numeric decoding — real work, and partly a benchmark artefact

`NumericInt64FromStoredPayload` is 6.0% cum on TPC-H, reached through
`math/big`. Worth recording *why* it is so prominent: **HammerDB declares
TPC-H's `l_extendedprice`, `l_discount` and `l_quantity` as `numeric`**, not
`float8`. This is genuine variable-width decimal decoding, not overhead.

## 4. Honest accounting of this series' method

Recorded because it is the most transferable result here, and because it went
wrong repeatedly:

**A CPU share is not a forecast.** Three times a share was read as a prediction
and three times it was wrong:

| part | predicted | delivered | why |
|---|---|---|---|
| 1 | 17.94% | 11.6% | removing a cost re-weights what is beneath it |
| 2 | ≤7.9% | 9.6% | **bound derived from a profile of the wrong build** |
| 3 | TPC-DS > TPC-H | TPC-DS −2.0% vs TPC-H −5.6% | TPC-DS is more I/O/alloc-bound |

The correct statement: **a profile says where cycles are spent, not which
cycles are on the critical path.** Every design after part 2 states its
figures as upper bounds, and this report's own conclusions are drawn from
final-build profiles for exactly that reason.

**One conclusion was withdrawn.** `REPORT-3.md` initially attributed part 3's
TPC-DS gain to the lighter queries. The same-session pair inverted the split
completely while the totals agreed to 0.3 points — single S-cold runs make
per-query attribution noise. The claim was withdrawn in the report rather than
quietly dropped.

## 5. Termination

The loop terminates here, on these grounds:

1. **The productive defect class is verifiably gone.** The final-build profile
   of the decode loop shows no per-row recomputation of per-column facts, and
   the three lines that carried the previous fixes are now 110 ms, 670 ms and
   90 ms.
2. **The two largest remaining costs are architectural**, both rejected with
   measured reasons rather than deferred: cut C (55 return sites in the
   on-disk decoder, ~2%, partly an unavoidable write barrier) and row
   ownership (a lifetime contract across the executor).
3. **Everything else in the top of both profiles is real work** — byte
   parsing, `math/big` decimal conversion, allocation, and I/O.

Further optimisation of this path requires changing what goopg *does*
(columnar batches, row ownership, a narrower `Datum`), not how it does it.
Those are design programmes with their own risk profiles, not the next item in
this loop.
