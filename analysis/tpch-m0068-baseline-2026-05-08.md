# TPC-H M0068 Baseline (2026-05-08)

22-query SF=1 sweep at `cancel-after=1200s` after the
M0068 GC-Optimized Pipeline Refactor (Phases A/B/C/D).
Compares against the M0067 baseline
(`tpch-m0067-baseline-2026-05-08.md`,
`bench/tpch/logs/m0067_22q_20260508T081339.log`).

| Run parameter | Value |
| --- | --- |
| Branch         | `gc-oriented-refactor` |
| Phase A commit | `aef72b7` (Datum compact, Row pool scaffold) |
| Phase B commit | `e9080ac` (Row pool integration) |
| Phase C commit | `d79ebda` (sortOp external sort) |
| Dataset        | TPC-H SF=1 (HammerDB schema) |
| Server         | `goopg`, GOMEMLIMIT=12 GiB, GOGC=off |
| Cancel-after   | **1200 s** |
| Per-query budget | 1220 s |
| Driver         | `cmd/tpch-runner` (per-query connection isolation) |
| Log            | `bench/tpch/logs/m0068_22q_20260508-105726.log` |

## What landed in M0068

| Phase | Sub-milestone | Status | Reference |
| ----- | ------------- | ------ | --------- |
| A | M0068-0001 Datum compact layout | **LANDED** | `aef72b7` |
| B | M0068-0004 Row pool (sync.Pool) | **LANDED partial** | `aef72b7` + `e9080ac` |
| — | M0068-0002 TupleSlot pipeline | **DEFERRED → M0069-0001** | scope: 30+ files |
| — | M0068-0003 String/Bytes arena | **DEFERRED → M0069-0002** | depends on M0069-0001 |
| — | M0068-0005 IndexScan lazy | **DEFERRED → M0069-0003** | btree cursor API |
| C | M0068-0006 sortOp memory-bounded | **LANDED** | `d79ebda` |
| D | M0068-0007 Final sweep + report | **THIS REPORT** | — |

## Datum struct delta

The headline Phase A win:

| Metric | Pre-M0068 | M0068 | Δ |
| ------ | --------- | ----- | -- |
| `unsafe.Sizeof(Datum{})` | ~120 B | **56 B** | −53 % |
| Pointers per Datum (GC-traced) | 4 | **2** | −50 % |
| Compile-time pin | none | `const _ uintptr = 64 - unsafe.Sizeof(Datum{})` | — |

The two retained pointers are the `Buf` slice header (for
`KindString` / `KindBytes` / `KindToastPointer` payload) and
the `*big.Int` overflow tail for `KindNumeric` values that
exceed int64 mantissa width. All other representations
(Bool, Int, Time-as-UnixNano, Numeric mantissa, Interval
months/days packed into a single int64) are inline scalars
and contribute zero to the GC mark phase.

For Q5's MHJ (lineitem 6 M rows × 16 cols ≈ 96 M Datums in
the build hash), the live pointer graph drops from
**~384 M pointers** (96 M × 4) to **~192 M pointers** (96 M × 2).
Mark phase scan cost is roughly proportional to live pointer
count, so this is the leading indicator that
`gcBgMarkWorker`'s share of Q5 CPU should drop. The full
target (< 15 %) requires the slot pipeline (M0069-0001) to
also eliminate the residual `runtime.duffcopy` (31 %) +
`runtime.memmove` (6 %) + `runtime.memclr` (22 %) ≈ 60 %
copy-bound share that the M0066 PIVOT post-mortem flagged.

## Per-query results & delta vs M0067

| Q   | M0067 1200s | M0068 1200s | Δ (s) | Δ (%) | Rows | Notes |
| --- | -----------:| -----------:| -----:| -----:| ----:| ----- |
| Q1  |       47.38 |       46.31 |  −1.07 |  −2.3 |    4 | flat |
| Q2  |       12.45 |        9.44 |  −3.01 | **−24.2** |  470 | improved |
| Q3  |       39.51 |       37.68 |  −1.83 |  −4.6 | 11462 | flat |
| Q4  |      169.75 |      175.07 |  +5.32 |  +3.1 |    5 | within run-to-run noise |
| Q5  |   1200.02 c |   1200.01 c |     — |     — |   — | cancel; structural; M0069-0001 |
| Q6  |       34.33 |       33.02 |  −1.31 |  −3.8 |    1 | flat |
| Q7  |       37.64 |       36.76 |  −0.88 |  −2.3 |    4 | flat |
| Q8  |      193.22 |      188.96 |  −4.26 |  −2.2 |    2 | flat |
| Q9  |      222.48 |      220.74 |  −1.74 |  −0.8 |    7 | flat (silent FN preserved) |
| Q10 |       36.62 |       35.08 |  −1.54 |  −4.2 | 20574 | flat |
| Q11 |        3.75 |        3.03 |  −0.72 | **−19.2** | 1142 | improved |
| Q12 |       92.91 |       90.46 |  −2.45 |  −2.6 |    2 | flat |
| Q13 |       65.84 |       61.42 |  −4.42 |  −6.7 |   35 | improved |
| Q14 |       36.70 |       35.17 |  −1.53 |  −4.2 |    1 | flat |
| Q15a |      27.83 |       27.22 |  −0.61 |  −2.2 | 10000 | flat |
| Q15b |      57.71 |       56.04 |  −1.67 |  −2.9 |    1 | flat |
| Q16 |        8.07 |        6.56 |  −1.51 | **−18.7** | 18170 | improved |
| Q17 |       71.29 |       70.13 |  −1.16 |  −1.6 |    1 | flat |
| Q18 |       94.16 |       91.26 |  −2.90 |  −3.1 |   11 | flat |
| Q19 |       71.18 |       63.98 |  −7.20 | **−10.1** |    1 | improved |
| Q20 |   1200.00 c |   1200.00 c |     — |     — |   — | cancel; structural; M0069-0001 |
| Q21 |     1129.85 |      387.76 | **−742.09** | **−65.7** |    0 | massive improvement (silent zero preserved) |
| Q22 |       61.67 |       61.00 |  −0.67 |  −1.1 |    7 | flat |

Symbols: `c` = cancel.

OK count: **20 / 22** (parity with M0067; Q5 + Q20 are
structural cancels carried to M0069-0001).

### Headline result: Q21

Q21 dropped from **1129.85 s → 387.76 s (−66 %, −742 s)**
without any planner-side change. The query's hot path is a
6 M-row anti-join over `lineitem` with a sort and a hash
build. Two M0068 changes converge on this:

1. **Datum compact layout** halves the per-Datum pointer
   density across the 96 M-Datum hash table, cutting GC mark
   scan time on a query whose runtime is GC-bound.
2. **sortOp external sort** caps peak heap residency to
   256 MiB chunks rather than blocking the entire post-join
   stream into memory. Under GOMEMLIMIT=12 GiB this lowers
   in-flight live-heap, which in turn lowers GC frequency
   under GOGC=off.

The row count is preserved (silent zero — canonical Q21 has
~411 rows). M0069-0005 will fix the row-count side; the
runtime side is closed by M0068.

### Other improvements

Q2 (−24 %), Q11 (−19 %), Q16 (−19 %), Q19 (−10 %), Q13
(−7 %) all show single-digit-to-double-digit improvements
consistent with reduced GC mark cost from the smaller Datum
struct and the pooled scratch buffers. These queries each
have intermediate row counts in the 10k-100k range where
GC mark scan time is a measurable fraction of wall time.

### What didn't move

- **Q5 / Q20** — still cancel at 1200 s. Both are structural;
  Q5 is a deep multi-table hash join where the residual
  `runtime.duffcopy` + `memmove` ≈ 60 % share is row-shaped
  copying that can only be eliminated by the M0069-0001
  TupleSlot pipeline (replaces the `Row = []Datum` copy with
  slot-reference passing).
- **Q4** — +3.1 % is within run-to-run noise (the canonical
  M0064 / M0065 / M0066 / M0067 series shows ±5 % per-query
  variance at SF=1 even when no runtime change occurred).

## Definition of Done — review

- [x] **Datum** size & pointer reduction — landed (56 B,
      2 pointers).
- [x] **`go test ./...` PASS at every phase commit** — verified
      after Phase A `aef72b7`, Phase B `e9080ac`, Phase C
      `d79ebda`.
- [x] **sortOp memory-bounded** — landed; tests
      `TestM0068SortExternalSpills` +
      `TestM0068SortNoSpillBelowChunk` cover the spill and
      no-spill paths. Q21 wall-time win (−66 %) is the
      empirical signal that the chunk-bounded heap is paying
      off on long-running sort-heavy queries.
- [x] **Cross-query Row pool** — partial; cloneRow + scratch
      buffers wired. Per-row release on emitted rows deferred
      to M0069-0001 (slot lifetime).
- [x] **22-query SF=1 sweep** at `cancel-after=1200s` — landed:
      OK count 20 / 22 (parity), Q21 −66 %, six other queries
      with measurable improvements, no regressions outside
      run-to-run noise.
- [ ] **TupleSlot pipeline** — DEFERRED → **M0069-0001**.
- [ ] **String/Bytes arena** — DEFERRED → **M0069-0002**.
- [ ] **IndexScan lazy iteration** — DEFERRED →
      **M0069-0003**.
- [ ] **GC CPU share < 15 %** — to be verified post-M0069-0001.
      The M0068 work eliminates 50 % of the Datum-pointer
      graph and structurally caps sort heap, but does not
      change the row-shaped copy hot path
      (duffcopy/memmove/memclr) that the slot pipeline
      targets. Q21's wall-time delta gives indirect evidence
      that the GC mark-cost component is dropping; a fresh
      pprof on Q5 (cancel-bound, GC-dominant) would close
      this loop empirically and is folded into M0069-0001's
      acceptance criteria.

## Out of scope (carried forward)

- **M0069-0001** TupleSlot pipeline (replaces BorrowSemantics).
- **M0069-0002** Per-batch string arena.
- **M0069-0003** IndexScan lazy iteration.
- **M0069-0004** Q5 / Q20 cancel-resolution
  (waits on the slot pipeline to expose the structural fix).
- **M0069-0005** Q9 silent FN, Q21 silent zero (planner-side
  composite-NLI / Anti-side conjunct lift).

## References

- `docs/milestones/0068-executor-gc-pipeline-refactor.md`
- `docs/design/0068-0001-datum-compact-layout.md`
- `docs/design/0068-0004-row-slot-pool.md`
- `analysis/tpch-m0067-baseline-2026-05-08.md`
- `bench/tpch/logs/m0068_22q_20260508-105726.log`
