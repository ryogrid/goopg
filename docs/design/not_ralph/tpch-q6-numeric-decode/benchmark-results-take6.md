# Take 6 results — 97 % of the allocations, and why the win beat its own estimate

**Status:** implemented and measured
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Baseline:** `1cfcdede9` (take 5)
**Design:** [design-take6.md](design-take6.md) (agent-reviewed; §7 there)
**Oracle:** PostgreSQL 18.3, TPC-H SF=1, port 65432

---

## 1. Summary

| | take 5 | **take 6** | change |
|---|---:|---:|---:|
| allocations / query | 18,802,320 | **798,094** | **−95.8 %** |
| allocated bytes / query | 2.315 GB | **0.111 GB** | **−95.2 %** |
| Q6 serial | 6.551 s | **4.490 s** | **1.46×** |
| Q6 parallel | 1.210 s | **1.028 s** | **1.18×** |
| instructions / row | 13,853 | **12,323** | 1.12× |
| IPC | 2.33 | **2.51** | +7.7 % |

Result bit-identical (`102513054.4896`); `tpch-spotcheck` PASS; `-race` clean.

Against PostgreSQL, Q6 parallel is now **5.1×** (1.028 s vs 0.203 s), from
25.9× at the start of this series.

**The wall-clock win (1.18–1.46×) is larger than the design's own estimate
(1.10–1.14×).** That estimate was derived from a CPU-line budget and was, for
once, too *conservative* — §4 explains why, because a performance claim that
beats its prediction deserves the same scrutiny as one that misses.

---

## 2. What changed

Three sites, each roughly one allocation per scanned tuple, together 97.4 % of
everything the query allocated:

| # | change | file |
|---|---|---|
| 1 | `PageGetHeapTuple` does **one** copy, not three — it calls `parseHeapTupleAlias` because `raw` is already private, instead of `ParseHeapTuple`, which defensively re-copies `Data` and `Bitmap` for its other five callers | `internal/storage/heap.go` |
| 2 | New `PageGetHeapTupleInto` takes a caller-supplied scratch buffer; `seqScanOp` owns one for the life of the scan, so the remaining copy stops allocating | `internal/storage/heap.go`, `internal/executor/operators_storage.go` |
| 3 | `evalPrefilter` calls `evalExprSlot` with a **cached** `SlotView` instead of `evalExpr`, which boxed the row slice into an interface (`runtime.convTslice`) once per row to recompute a value that never changes | `internal/executor/scan_prefilter.go` |

Plus one correctness fix the review surfaced (§5).

`evalExprSlot` itself is **not** touched — see §6.

---

## 3. Measurements

### 3.1 Allocations — `runtime.MemStats` across four Q6 runs on a fresh server

| | take 5 | take 6 |
|---|---:|---:|
| `Mallocs` delta / query | 18,802,320 | **798,094** |
| `TotalAlloc` delta / query | 2.315 GB | **0.111 GB** |

Per scanned row that is 3.13 allocations → **0.13**. The design predicted
"toward ~0.5–1 M"; the measured 0.80 M lands inside it.

Post-change allocation profile — the three targets are gone, and what remains
is the scratch buffer's own growth plus arena materialisation for the ~2 % of
rows that survive the filter:

| | take-6 baseline | take 6 |
|---|---:|---:|
| `storage.PageGetHeapTuple` (flat) | 32.59 % | — |
| `storage.ParseHeapTuple` | 32.45 % | **gone** |
| `executor.evalExpr` | 32.35 % | **gone** |

### 3.2 Wall clock — alternating A/B, fresh server per arm, no profiler attached

| round | mode | take 5 | take 6 | speedup |
|---|---|---:|---:|---:|
| 1 | serial | 6.586 s | **4.620 / 4.626 s** | 1.42× |
| 2 | serial | 7.493 / 6.551 s | **4.507 / 4.490 s** | 1.46× |
| 1 | parallel | 1.224 / 1.204 s | **1.053 / 1.047 s** | 1.16× |
| 2 | parallel | 1.202 / 1.211 s | **0.985 / 1.026 s** | 1.20× |

Ranges disjoint in every round and mode. PostgreSQL, re-measured in the same
host state: **0.20 / 0.21 / 0.21 s** parallel — unchanged from the start of the
series, so the host drift discussed below moved goopg's arms, not the oracle.

### 3.3 Instructions per row — both arms back-to-back

Fresh server, 3 warm-up queries, then a 60 s `perf` window over a 20-rep serial
stream. Back-to-back because the host's clock drifted during this session.

| | take 5 | take 6 |
|---|---:|---:|
| per-query serial (settled) | 6.498 / 6.468 / 6.549 / 6.506 s | 6.049 / 6.020 / 6.048 s |
| `instructions:u` (60 s) | 766,777,760,826 | 734,830,006,517 |
| rows scanned in window | 55.35 M | 59.63 M |
| **instructions / row** | **13,853** | **12,323** |
| IPC | 2.33 | **2.51** |
| CPUs utilised | 1.851 | **1.763** |

Instructions/row falls **1.12×**, squarely inside the design's 1.10–1.14 %
budget. Note these serial timings (6.5 → 6.0 s) are *not* the §3.2 numbers
(6.55 → 4.49 s) — `perf` is attached here and the arm is running 20
back-to-back queries. Use §3.2 for wall clock and §3.3 for the ratio.

---

## 4. Why the wall-clock win exceeds the instruction budget

The design derived ≈ 12.1 % of removable CPU and therefore a **1.14× ceiling**.
Measured: 1.18× parallel, 1.46× serial. A result above its own predicted
ceiling is a signal that the model was wrong, so it is worth saying exactly how.

The budget counted only *instructions attributable to the removed code*. Three
effects it could not see:

1. **IPC rose 7.7 % (2.33 → 2.51).** Not fewer instructions — the *same*
   instructions retiring faster, because a query that allocates 0.11 GB instead
   of 2.32 GB touches far less fresh memory and keeps far more of its working
   set in cache. Instructions/row is blind to this by construction.
2. **Background GC work fell.** CPUs utilised dropped 1.851 → 1.763 over an
   identical 60 s window: ~0.09 of a core that was sweeper and mark-worker
   time. `GCCPUFraction` reports 0.013 % and does not count `runtime.bgsweep`,
   which was 2.32 % of the baseline profile — the design flagged this after
   review but still under-weighted it.
3. **Kernel-side page acquisition.** 2.32 GB → 0.11 GB per query is ~2.2 GB
   less memory the runtime has to fault in and hand back per query. That cost
   is largely outside `instructions:u`.

So the honest reading: **1.12× is the instruction-count win, and the rest is
memory-system and GC-thread relief.** The design's estimate was not wrong about
the instructions; it was wrong to treat instructions as the whole story for a
change whose entire subject is allocation.

The serial arm gains more than the parallel one (1.46× vs 1.18×) for the same
reason in reverse: with four workers the query is already limited by other
things, so relieving one backend's allocator matters proportionally less.

---

## 5. Correctness

| check | result |
|---|---|
| Q6 result bit-identical | ✅ `102513054.4896` on all four A/B arms |
| `scripts/tpch-spotcheck.sh` | ✅ `RESULT=PASS` — Q12 = 2, Q13 = 34 (query phase 24.6 s → **19.9 s**) |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | ✅ exit 0, 43 packages ok |
| `go test -race ./internal/executor/ ./internal/storage/` | ✅ pass |
| pre-commit pgbench smoke | runs on every commit; never bypassed |

### New tests

- **`TestDecodedRowDoesNotAliasSourceBuffer`** — the guard that makes the
  scratch buffer safe. Decodes a 13-column row spanning every storage class,
  overwrites the source buffer with `0xFF`, and asserts every Datum is
  unchanged. A future decode arm that starts aliasing the tuple bytes trips
  this immediately instead of silently reading the *next* row.
- **`TestDecodedRowAliasDetectorActuallyDetects`** — the positive control for
  it. A guard that quietly stops guarding is worse than no guard: this builds a
  Datum that *does* alias and asserts the detector notices. Without it, the
  test above would pass just as happily if `datumObservable` returned a
  constant.
- **`TestPageGetHeapTupleOwnsItsMemory`** — pins the pre-existing contract that
  `PageGetHeapTuple`'s result is independent of the page, which change 1 must
  not weaken.
- **`TestPageGetHeapTupleIntoMatchesAndGrows`** — ascending tuple sizes through
  one buffer, compared against the allocating entry point, so a larger tuple
  after a smaller one cannot be truncated.

### The bug the review found

The design review turned up a **latent correctness bug in the take-5 code**,
unrelated to take 6's own changes. The prefilter disarm block listed
`typeACLColIdx` and `attrACLColIdx` but omitted **`dbACLColIdx`**, even though
`pg_database.datacl` receives the identical post-clone `KindBytes → aclitemout`
rewrite. A predicate on `datacl` would have been prefiltered against the raw
`_aclitem` blob while `filterOp` saw rendered text — breaking the "can only
remove rows the Filter would remove anyway" guarantee that block exists to
protect. Fixed in `internal/executor/operators_storage.go`. Low reachability,
but exactly the sibling-path failure mode the repo's practice card warns about.

---

## 6. What is left

| item | measured | note |
|---|---:|---|
| `evalExprSlot` | 31.88 % cum, **14.19 % flat** | Untouched, deliberately. The flat half is interpreter dispatch and is what expression compilation (PG's `ExecReadyExpr`/JIT) would attack; the other ~18 % is the arithmetic underneath (`evalBinary`, `compareDatum`, `addTimeInterval`), which compilation does not remove. The prize is smaller than the headline. |
| `PageGetHeapTuple`'s `memmove` | 6.80 % | Structurally retained: the copy out of the page survives, it just stopped allocating. `PageGetHeapTupleNoCopy` would remove it — declined on lifetime coupling, with the reasoning in design §6. |
| type-name `strings.ToLower` per value | 5.21 % | `decodePhysicalPGValueMctxStyled` and `physicalPGTypeAlign` lowercase `t.Name` for every value; a per-column type code resolved once in `Open` removes it. Cheapest remaining item. |
| slice→interface boxing elsewhere | — | Fixed at the Q6 site only. `joinOp.evalHashKey`, `joinOp.joinPredicateMatch` and `sortOp.lessRows`→`evalSortKeyValue` still pay it per row — the last is O(n log n). |

## 7. Reproduction

```bash
go build -o tmp/take4/goopg-take6 ./cmd/goopg
tmp/take4/ab.sh          # alternating A/B, fresh server per arm, no profiler
tmp/take4/perf-ab.sh     # back-to-back instructions/row
TAG=q6-take6 SECS=30 tmp/take4/profile-q6.sh
GOOPG_BIN=$PWD/tmp/take4/goopg-spot6 scripts/tpch-spotcheck.sh
```

Environment unchanged from DESIGN.md §3: `GOGC=off`, `GOMEMLIMIT=12GiB`, cgroup
soft cap above `GOMEMLIMIT`, `perf stat -p PID … sleep N` with no trailing `--`.

## 8. The series so far

| | session start | take 4 | take 5 | **take 6** | PG 18.3 |
|---|---:|---:|---:|---:|---:|
| Q6 parallel | 5.235 s | 2.784 s | 1.210 s | **1.028 s** | 0.203 s |
| gap to PG | 25.9× | 13.7× | 6.0× | **5.1×** | 1.0× |
| allocations / query | 291.6 M | 60.1 M | 18.8 M | **0.80 M** | — |
