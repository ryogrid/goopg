# Applying Go 1.26 `simd/archsimd` to goopg — design

Status: DESIGN, pre-implementation. Target: `docs/design/not_ralph/try-simd/`.
Date: 2026-09-08. Branch: `plan-narrowing-and-etc`.

## 0. Summary

`simd/archsimd` is Go 1.26's **experimental**, architecture-specific SIMD API.
This document establishes where it can profitably apply in goopg, and — more
importantly — where it **cannot**, which is most of the executor.

The verdict, in one line: **there is exactly one target worth *implementing*,
the PG data-page checksum.** Not because it is the only contiguous loop — PG
itself vectorises two other things — but because the other candidates are
already handled: goopg's WAL CRC-32C goes through `hash/crc32` with
`crc32.Castagnoli`, which Go's stdlib **already** implements in SSE4.2/PCLMULQDQ
assembly on amd64 (so PG's second-biggest SIMD win costs goopg no work at all),
and the snapshot-XID scan that PG vectorises with `pg_lfind32`
(`postgres/src/include/port/pg_lfind.h:108-142`, called from
`snapmgr.c:1903`) is already sorted-and-binary-searched in goopg above
`snapshotLinearScanThreshold` (`internal/access/transam/snapshot.go:120-151`)
with a typical `n` of 1-3.

Everything in the *executor proper* is blocked by the 48-byte tagged `Datum`.

A working prototype exists (`prototype/`), validated bit-identical against the
production scalar routine, and measured. Numbers are in §4.

## 1. Ground facts (verified, not assumed)

| fact | evidence |
|---|---|
| Toolchain is **go1.26.3** | `go version` |
| `simd/archsimd` exists, every file gated `//go:build goexperiment.simd` | `$(go env GOROOT)/src/simd/archsimd/*.go` |
| Requires **`GOEXPERIMENT=simd`** at build time | package doc: "only exists when building with the GOEXPERIMENT=simd environment variable set" |
| Host CPU is **AVX2, NOT AVX512** | `/proc/cpuinfo`; AMD Ryzen 7 5700X |
| `go.mod` declares `go 1.25.0`; repo gofmt baseline is go1.25 | `go.mod`, `CLAUDE.md` |
| Data checksums are **ENABLED** on all three bench clusters | `DataChecksumVersion=1` read from `pg_control` for `bench/tpch/runtime_goopg/data`, `bench/tpcds/runtime_goopg/data`, `bench/tpcds/runtime_goopg/data-sf05` |

The AVX512 absence is load-bearing: 256-bit is the widest vector available, so
`Int32x8`/`Uint32x8`/`Float64x4` are the working types and AVX512-only masked
forms are off the table on this machine.

## 2. Why most of the executor is NOT a SIMD target

SIMD needs **contiguous arrays of one fixed-width type**. goopg has none on the
query path:

- `Datum` (`internal/executor/datum.go:171`) is **48 bytes**, carrying a `Kind`
  tag, a `Scale`, an `ArenaID`, an `Int int64`, a `Buf []byte` and a `Hi`.
- A `Row` is `[]Datum`. Values of one *column* are therefore **48-byte strided**
  within a row, and consecutive rows are separate allocations.
- `PackedSlot.values` (`internal/executor/packedslot.go`) is also a `Row`. The
  packed-retention work changed *what is retained*, not the evaluator's row
  shape.

So for Q1/Q6-style filter-and-aggregate — the textbook SIMD wins — there is no
column vector to load. The options are all bad:

1. **Gather.** AVX2 `VPGATHERDD` is frequently no faster than scalar loads, and
   a 48-byte stride defeats cache-line reuse (one useful 8-byte value per
   64-byte line).
2. **Pack, then compute.** The pack step costs a full pass over the same data
   the scalar loop would already have finished.
3. **Build a columnar batch layer.** This is a genuine engine change, an order
   of magnitude larger than this item, and it would partly relitigate the
   `minimize_datum` workstream that just finished optimising the row path.

**Conclusion: no operator whose heavy work is on `Datum` values is in scope
without a columnar batch layer, which is out of scope here.**

That is narrower than an earlier draft of this document, which said "no executor
operator is in scope" — a claim the source review correctly falsified. It
silently equated *executor work* with *Datum-valued work*. Operators whose hot
inner work is on **untagged contiguous arrays** are a whole class this argument
does not touch, and at least three exist in goopg:

- **`internal/executor/tidbitmap.go`** — `tbmUnion` OR (`:136-138`),
  `tbmIntersect` AND (`:191-196`), and byte-at-a-time `popcount` (`:211-217`)
  over a fixed 256-byte `[]byte`. These run on the query path from
  `operators_bitmap.go:995` (BitmapAnd) and `:1074` (BitmapOr). In isolation
  this is a *cleaner* SIMD target than the checksum — lane-independent boolean
  ops, no serial multiply chain, and **neither §4.1 pitfall applies**. The
  counterweight is that the outer loop is a Go map range, so a 256-byte kernel
  is amortised against a map probe.
- **`internal/storage/fsm.go`** — `buildChunkMax` max-reduce (`:63-72`) and the
  `GetCandidates` threshold scan (`:100-116`, `:154-192`) over a flat
  `[]uint16`. On the INSERT path via `heap_insert_select.go:97`.
- **`internal/access/common/pglz/pglz.go:127-129`** — byte-at-a-time match
  extend, the textbook LZ vector kernel. Dismissed on merit rather than
  omitted: the *hot* half is decompression, which is serial tag-stream control
  flow; the vectorisable half is compression, which is colder.

So the checksum is the **best** target — hottest per byte, no map or
pointer-chasing wrapper — not the *only* one.

## 3. The target: PG data-page checksum

`internal/storage/checksum.go` `pageChecksumBlock` is a transcription of PG's
`pg_checksum_block` (`postgres/src/include/storage/checksum_impl.h:145-174`).
Its structure is *designed* for vectorisation — but for **128-bit SSE, not
AVX2**, and upstream is explicit about both halves of that:

- *"To hide the latency and to make use of SIMD parallelism multiple hash values
  are calculated in parallel"* (`checksum_impl.h:44-48`).
- *"The parallelism number 32 was chosen based on the fact that it is the
  largest state that fits into architecturally visible x86 **SSE** registers…
  **For future processors with 256bit vector registers this will leave some
  performance on the table.**"* (`checksum_impl.h:90-93`).

So the 4 × `Uint32x8` mapping below parks the whole accumulator state in 4 of 16
YMM registers, and upstream's own caveat about leaving width unused applies
directly to it. That does not invalidate the measured 8.6×; it means the ceiling
is set by the algorithm's SSE-era shape, not by AVX2.

Two further facts from upstream that matter here:

- **PG does not ship a hand-written kernel.** It relies on the compiler:
  *"Vectorization requires a compiler to do the vectorization for us"*
  (`checksum_impl.h:75-80`), with `checksum.o` built using
  `${CFLAGS_UNROLL_LOOPS} ${CFLAGS_VECTORIZE}`
  (`postgres/src/backend/storage/page/Makefile:22-23`). It does **not** pass
  `-msse4.1`/`-march`, so a stock x86-64 PG build lacks `pmulld` at baseline —
  an explicit AVX2 kernel therefore goes *beyond* what stock PG achieves.
- **Upstream names this as a real bottleneck**, and names the workload shape the
  measurement in §7 should target: *"Workloads where the database working set
  fits into OS file cache but not into shared buffers can read in pages at a
  very fast pace and **the checksum algorithm itself can become the largest
  bottleneck**"* (`checksum_impl.h:20-23`).

The algorithm details, all verified against upstream:

- **32 independent lanes** (`nChecksumSums = 32`, PG's `N_SUMS`).
- Inner step: `tmp = sum ^ v; sum = tmp*16777619 ^ (tmp >> 17)`.
- Input is a contiguous 8 KiB page of little-endian `uint32`.

On a little-endian host the page bytes **are** the LE `uint32` array, so no
per-word decode is required — the vector loads straight off the page.

Mapping to AVX2: the 32 lanes are 4 × `Uint32x8`. Row `i` of the page grid is 32
consecutive `uint32`s, i.e. exactly those 4 vectors. Required ops, all present:
`Xor`, `Mul` (`VPMULLD`, low 32 bits), `ShiftRight`, `LoadUint32x8Slice`,
`BroadcastUint32x8`, `Store`.

**It is on the measured path.** Checksums are enabled on all three bench
clusters, so this runs on every **non-new** page write and page-read
verification. The `PageIsNew` qualifier is not pedantry: PG skips the checksum
for such pages (`postgres/src/backend/storage/page/bufpage.c:109-119`, `:1514`,
`:1544`) and goopg matches at `internal/storage/smgr.go:768-771`,
`internal/storage/checksum.go:116-118` and `smgr.go:1107-1109`. An oracle review
checked specifically for over-checksumming relative to PG and found none.

## 4. Measured prototype results

Prototype in `prototype/` (all files carry `//go:build goexperiment.simd`, so
the default build skips them). `TestAgree*` assert bit-identical output against
the scalar routine on the same page. AMD Ryzen 7 5700X.

**Measurement caveat, stated first because it bounds everything below:** these
runs were taken on a machine concurrently running other agents' benchmarks.
Absolute ns/op drifts by ~20% between an idle and a loaded run. Ratios within a
single interleaved `-count=N` run are far more stable than absolutes, so the
table quotes **ranges from one interleaved run**, and the conclusions use
ratios, never point values.

Loaded run, `-benchtime=3s -count=6`, interleaved:

| variant | ns/op range |
|---|---|
| `scalar` — production form today | 2607 – 2854 |
| **`scalarSafeA`** — best safe scalar, **no `unsafe`** | **1232 – 1270** |
| `simdHoisted` — AVX2 kernel | **190 – 208** |

Idle-er earlier run for comparison: `scalar` ~2410, `scalarSafeA` ~1000,
`simdHoisted` ~174.

### The honest decomposition — and a correction to an earlier draft of this document

**Two independent wins, and they must not be bundled:**

1. **A safe scalar rewrite is worth ~2.1× – 2.4×**, with **no `unsafe`, no
   `GOEXPERIMENT`, and no SIMD**. The win comes from hoisting the row reslice,
   using three-index slices so the compiler can prove the 4-byte window, using
   `binary.LittleEndian.Uint32` on that window (it lowers to a single load), and
   hoisting the `pd_checksum` branch out of the inner loop
   (`off == 8` ⇔ `i == 0 && j == 2`).
2. **SIMD is worth a further ~5.7× – 6.7×** on top of that.

**An earlier draft of this section claimed "1.65× × 8.6×" and attributed the
scalar win to an `unsafe` word view. Both halves were wrong**, and a
source-falsification review caught it:

- The scalar win does **not** need `unsafe`. An unsafe variant and the safe
  variant land at the same speed, which proves the win is the branch hoist plus
  provable bounds, not the reinterpretation. I reproduced this after initially
  failing to — my first safe attempt used manual byte-shift assembly and stalled
  at ~1455 ns; switching to `binary.LittleEndian.Uint32` on the bounds-proven
  window reached ~1000 ns.
- Consequently the advertised SIMD multiplier was **inflated by roughly 1.6×**,
  because it was measured against a deliberately-unsafe intermediate rather than
  against the best reasonable scalar. **8.6× was a strawman comparison; ~6× is
  the real figure.**

This matters for the decision, not just the arithmetic: §7 says the end-to-end
share decides whether this lands at all, and **a ~6× kernel behind an
experimental compiler flag is a materially weaker case than an 8.6× one** —
especially when 2.4× of the total is available unconditionally without it.

### 4.1 Two archsimd pitfalls found the hard way

Both cost more than an order of magnitude and neither is obvious from the API:

1. **Never put a vector in an aggregate.** `var acc [4]archsimd.Uint32x8`
   indexed by a loop variable spills and reloads through the stack every
   iteration (`VMOVDQU (R8), Y1` … `VMOVDQU Y1, (R8)` in the disassembly). This
   made the SIMD version **6.4× slower than scalar**. archsimd's own doc warns
   of it — "not recommended to … put it in an aggregate type" — and the warning
   is worth an order of magnitude, not a style point.
2. **`ShiftAllRight(constant)` does not lower to an immediate shift.** It emits
   `MOVL $17, R9; MOVQ R9, X3; VPSRLD X3, …` — rematerialising the count every
   call. Hoisting `BroadcastUint32x8(17)` out of the loop and using the per-lane
   `ShiftRight(vec)` (`VPSRLVD`) took **3920 → 171 ns, a 23× difference from
   that single change.**

These are recorded because they are properties of the current experimental
compiler support, not of the algorithm, and the next person will hit them.

## 5. Build and dispatch strategy

Constraints: the **default build must be completely unaffected**, must keep
working on non-amd64 and on amd64 without AVX2, and `go.mod` says `go 1.25.0`.

Design:

- Three files in `internal/storage`:
  - `checksum.go` — unchanged public entry point; keeps the scalar
    implementation as the reference and the fallback.
  - `checksum_simd_amd64.go` — `//go:build goexperiment.simd && amd64`.
    Provides the vector kernel.
  - `checksum_nosimd.go` — `//go:build !(goexperiment.simd && amd64)`.
    Provides a stub that reports "unavailable".
- **Two gates, both required**: the build tag (compile time) *and*
  `archsimd.X86.AVX2()` (run time). A binary built with `GOEXPERIMENT=simd` on a
  machine without AVX2 must take the scalar path, not fault.
- Selection happens **once** at package init into a function variable, not per
  page, so the dispatch is not in the inner loop.
- `go.mod` stays at `1.25.0` if possible; the SIMD file is excluded from the
  default build by its tag, so the module still builds with the go1.25 language
  version. This must be **verified**, not assumed — if the toolchain refuses,
  the fallback is to document a `go 1.26` bump as a build-time-only requirement
  for the experimental variant.

Build invocation for the experimental variant:

```
GOEXPERIMENT=simd go build ./...
```

## 6. Correctness plan

The checksum is a **durability** surface: a wrong checksum either corrupts good
pages' verification (false `ChecksumError`) or silently accepts bad ones.

**None of the following has been done yet — this is the plan, not a result.**
The prototype today has exactly **one** deterministic page
(`prototype/bench_test.go`, `p[i] = byte(i*7+3)`), which is nowhere near the
corpus described here.

- **Equivalence test over random pages**: the production `pageChecksumBlock`
  vs the SIMD kernel must agree bit-for-bit on a large random corpus, including
  the `pd_checksum` masking case at word index 2, all-zero pages, all-0xFF
  pages, and — per the oracle review's §8.2 finding — a
  `pd_upper == 0`-but-nonzero page.
  **The oracle must be the production function, not a hand-copied scalar.** The
  prototype currently compares against its own transcription
  (`prototype/main.go`), which can silently drift from
  `internal/storage/checksum.go`.
- **Cross-check against real PG**: the existing checksum tests plus a page
  written by PG 18.3 must verify under both paths.
- **Mutation check**: the equivalence test must FAIL if the SIMD kernel is
  perturbed (e.g. shift 17→16), otherwise it proves nothing.
- The existing `internal/storage/checksum_io_test.go` suite must pass under
  both build configurations.
- **A gate must actually build the experimental variant.** The source review
  confirmed `GOEXPERIMENT` appears nowhere in `Makefile`, `scripts/`, `ci/` or
  `.github/`, and `go env GOEXPERIMENT` is empty — so the default build is
  safe, but the flip side is that **nothing would ever compile or exercise the
  SIMD kernel**, leaving a durability-critical code path permanently untested
  and free to bit-rot. An explicit `GOEXPERIMENT=simd go test
  ./internal/storage/` step must be added to the precommit scope or the nightly
  batch. "Must pass under both configurations" is not enough if only one is ever
  run.

## 7. What this design does NOT claim

**No end-to-end speedup is claimed yet.** An 8.6× kernel win is worth nothing if
the checksum is 0.3% of query time. The share of TPC-H and TPC-DS CPU spent in
`pageChecksumBlock` is being measured separately; **if that share is negligible,
the correct outcome is to record the kernel result and close the item as
not-worth-landing**, exactly as E-19 was closed. The measurement decides, not
this document.

That risk is the main one here, and it is the same shape as the trap E-19 hit:
a real, correctly-built mechanism whose caller turns out not to be where the
time goes.

**Two facts sharpen the share measurement, both from the source review:**

- **Only *physical* reads reach the checksum.** `verifyOnRead`
  (`internal/storage/smgr.go:766-780`) is not on the buffer-pool hit path, so
  the share is bounded by physical I/O, not by page accesses. With TPC-H SF=1
  at 1.9 GiB inside a 2 GB pool, a warm run may touch it almost not at all —
  which is precisely the shape that made E-19 measure zero.
- **There is a cheaper adjacent win on the write path.**
  `PageSetChecksumCopy` (`internal/storage/checksum.go:113-121`) does a
  `make([]byte, 8192)` **plus a full 8 KiB copy on every checksummed page
  write**, before the checksum is even computed. That allocation and memcpy are
  the same order of magnitude as the scalar checksum itself, and are pure GC
  pressure on the write path. If the motivation is write-path CPU, this needs no
  experiment flag and should be priced alongside the kernel.

## 7b. MEASURED VERDICT: the checksum is not hot. The item does not pay.

The share-of-CPU measurement §7 said would decide the item has been taken, and
it decides against. `pageChecksumBlock` cumulative share, four profiled
workloads on private clones, `DataChecksumVersion=1` throughout:

| workload | `pageChecksumBlock` cum | share |
|---|---:|---:|
| TPC-H SF=1, cold buffers, Q1/6/3/12/13 | 0.05 s / 117.56 s | **0.043%** |
| TPC-DS SF0.5, 28 queries | 0.23 s / 272.72 s | **0.084%** |
| pgbench `-i -s 10` COPY load tail | 0.03 s / 7.53 s | **0.4%** |
| pgbench OLTP `-n -c 8 -j 8`, 1268 tps | 0.17 s / 136.16 s | **0.125%** |

The entire write-side path including `PageSetChecksumCopy`'s 8 KiB
`make`+copy (§7's "cheaper adjacent win") is **0.13%** of the OLTP profile — so
that win is not worth taking either.

**A 13.2× kernel over 0.125% buys ~0.11% of one workload. The ceiling, at
infinite speed, is ~0.4%.** This is exactly the E-19 shape named in §7: a real,
correctly-built, verified-bit-identical mechanism whose caller is not where the
time goes.

### 7b.1 Corrections to §2's candidate list, from the profiles

- **`tidbitmap` is NOT a candidate** — this refutes the source review's
  strongest counterexample and, with it, part of §2's rewrite. There are no word
  operations: `bitmap` is a 256-byte `[]byte` touched **one bit at a time**
  through a `map[BlockNumber]*pageEntry` (`addOne`). It is pointer-chasing, not
  contiguous, and it is absent from every profile.
- `pglz` (correctly `internal/access/common/pglz`) is below the 0.5% pprof
  cutoff in every profile.
- WAL CRC is stdlib `crc32.Castagnoli`, already hardware-dispatched, and absent
  from all profiles — confirmed.
- Hash-join key hashing cannot vectorise either way: FNV-1a is a serial per-byte
  dependency chain within a key, and keys are short and variable-length across
  keys.

### 7b.2 Columnar is worse than doing nothing — measured, not argued

**`archsimd` exposes no gather or scatter at all** (zero `Gather`/`Scatter`
symbols in the package). So §2's gather option is not "often no faster"; it is
*unavailable*. The only route is an explicit scalar pack loop, measured on a
struct with `Datum`'s exact 48-byte layout, 4096 rows, `int64` column, with no
NULL/Kind/scale handling at all:

| | ns/op |
|---|---:|
| scalar sum off strided `Datum`s (what goopg does today) | 1981 |
| **pack into `[]int64` alone** | **2964** |
| pack + scalar sum | 4739 |
| pack + SIMD sum | 3681 |
| SIMD sum, already contiguous | 466 |

The kernel is genuinely 4.25× faster, and **the pack alone costs 1.5× the whole
computation it would replace**. Pack-then-compute is **1.86× slower than doing
nothing**.

And the Q1/Q6 premise fails on this corpus independently: HammerDB declares
`l_extendedprice`, `l_discount` and `l_quantity` as **`numeric`**
(`bench/tpch/cmd/hammerdb_load/dbgen.go:161`), not `float8`. Those aggregates
run through `NumericInt64FromStoredPayload` and `math/big.nat.scan` over
variable-width values. **There is no fixed-width float column in TPC-H here to
vectorise.**

### 7b.3 The incidental find that is worth ~50× this whole item

`strings.ToLower` is **7.3% of TPC-H CPU (8.58 s)** and 3.16% of TPC-DS, called
from `catalog.PhysicalTypeIsVarlena`, `InMemory.LookupEnum` and
`decodeRowRangeInfo` — i.e. **lowercasing type names once per value decoded**.
It is a hoisting/caching fix, not a SIMD one. The genuinely hot code is row
decode (`decodeRowRangeInfo` 63.5% cum in TPC-DS,
`decodePhysicalPGValueLowered` 45%, `mallocgc` 18.3%), which is serial and
data-dependent — varlena headers give the next offset — and structurally
un-vectorisable.

Filed as a successor item; it is not part of this design.

### 7b.4 What still gets built, and why

The owner asked for the experiment, so the kernel is implemented rather than
abandoned at the design stage — it is small, gated, and verifiable. But **no
end-to-end win is claimed or expected**, and the report will say so with these
numbers. Two further implementation facts from the survey:

- **`go.mod` does NOT need bumping** — verified by building `simd/archsimd`
  from a module declaring `go 1.25.0`.
- **`LoadUint32x8Slice(s[i:i+8])` emits a bounds check per load** and nothing is
  hoisted; that alone left an otherwise-correct kernel 1.8× *slower* than
  scalar. Only raw-pointer loads plus separate scalar accumulator variables
  reached 162 ns. This is a third archsimd pitfall to add to §4.1's two.

## 7c. Implementation landed; end-to-end measurement CUT SHORT (partial data)

Landed at `b8baeee13` as two deliberately separate changes: the unconditional
scalar rewrite (2131 -> 1017 ns/page, ~2.1x, no flag) and the AVX2 kernel
(1017 -> 163 ns, ~6.2x further, `goexperiment.simd && amd64` plus a runtime
`archsimd.X86.AVX2()` check). Equivalence is mutation-checked against the
production scalar oracle over 259 pages; units and the storage suite are green
in both build configurations.

**The three-arm TPC-H run was stopped after arm 1 when the owner redirected the
work.** It is recorded here rather than discarded, because the one arm that
completed already says something:

```
arm=base  cold  117.18 s
arm=base  warm1 117.24 s
```

**Cold and warm are indistinguishable (0.05%).** On this cluster — TPC-H SF=1,
1.9 GiB of data inside a 2048 MB buffer pool, on a host whose OS page cache
holds the rest — a "cold" arm performs essentially no physical reads. Since
`verifyOnRead` is only on the *physical* read path
(`internal/storage/smgr.go:766-780`), the checksum barely executes in either
arm. That is direct corroboration of §7b's profile-based verdict, from a
completely different instrument: **there is no end-to-end signal to find here,
and the remaining two arms would have measured the same noise.**

No end-to-end number is claimed for this item. The kernel is correct, fast in
isolation, and inert at the corpus level — which is the whole finding.

## 8. Review findings, and what they changed

Two adversarial reviews ran: a **PG 18.3 oracle** pass against `./postgres/`
(verdict: APPROVE-WITH-NITS) and a **source-falsification** pass over
`internal/` (verdict: **REQUEST-CHANGES**, three blocking findings — the
prototype broke `go build ./...`, §2's central claim was false as written, and
§4's speedup decomposition was inflated by comparing against a strawman
baseline). All three blockers are fixed above. Their corrections are folded in
rather than absorbed silently; the substantive ones were the SSE-vs-AVX2 framing (§3), the
`PageIsNew` qualifier (§3), and the "exactly one target" overreach (§0).

**It also settled the on-disk question empirically rather than by reading.** It
extracted `checksum_impl.h:105-215` unmodified into a C harness at `-O2`, built
200 test pages (198 random, one all-zero, one all-0xFF) with random block
numbers, and diffed real PG against goopg's `pageChecksumBlock`/`PageChecksum`:

```
vectors: 200  mismatches: 0
```

Negative controls prove the harness is sensitive: shift `17→16` ⇒ 200/200
mismatch; drop the `0xFFFF0000` mask ⇒ 199/200 (the all-zero page still matches,
because its `pd_checksum` is already 0). It also confirmed
`offsetof(PageHeaderData, pd_checksum) == 8` and that goopg's **masking is
bit-equivalent to PG's transient zeroing** — in fact strictly better, since PG
mutates a shared buffer and therefore needs `PageSetChecksumCopy`
(`bufpage.c:1509-1532`), while goopg does not.

### 8.1 The three open questions, now answered

1. **Alignment — safe.** Buffer-pool pages are over-allocated and trimmed to a
   **4096-byte** boundary (`internal/storage/arena.go:20-30`), exceeding PG's
   stated 4-byte requirement (`checksum_impl.h:142-143`). Two callers do *not*
   use arena pages — `PageSetChecksumCopy` (`checksum.go:114`) and `extendBatch`
   (`smgr.go:1102`) both `make([]byte, …)`, which Go guarantees only 8-byte
   aligned. This is a non-issue because the kernel uses **unaligned** loads
   (`VMOVDQU`), which is what the disassembly already shows. Recorded so nobody
   later "optimises" it into an aligned-load form that faults.
2. **Yes — land the scalar fix independently.** It is unconditional, needs no
   experiment flag, benefits every build, and **de-risks the whole item**: if
   §7's share measurement comes back negligible, the 1.65× still lands.
3. **CI/`GOEXPERIMENT` leakage — must be checked before landing** (assigned to
   the source review, not yet answered here).

### 8.2 A pre-existing correctness gap found on the way (NOT introduced here)

`storage.VerifyPage` (`checksum.go:128-136`) returns **`true`** for any page with
`pd_upper == 0` (`page.go:203-208`). PG does not: `PageIsVerified` skips the
*checksum* for such a page and then falls through to
`pg_memory_is_all_zeros(pagebytes, BLCKSZ)` (`bufpage.c:139-142`), returning
**`false`** if the page is not actually all zeroes (`bufpage.c:161`). goopg
therefore accepts a torn or garbage page whose bytes 14-15 happen to be zero,
where PG rejects it. goopg also lacks PG's header-sanity block
(`bufpage.c:128-133`). Relatedly, the doc comment at `page.go:201-202` says
"bytes-zero" while the code tests `pd_upper == 0` — the comment is wrong, and
`checksum.go:111-112`/`:125-127` repeat the mischaracterisation.

Out of scope for a kernel swap, but it gets a **deferral-ledger row**, and §6's
equivalence corpus must include a `pd_upper == 0`-but-nonzero page so the SIMD
path is held to at least the same contract.

### 8.3 Owed measurement

Compare against **real PG's** `pg_checksum`, not only goopg's own scalar
baseline. PG builds `checksum.o` with `-funroll-loops -ftree-vectorize`, so the
§4 table currently compares goopg to goopg. The PG-vs-goopg number is the more
interesting datapoint and is cheap to obtain.
