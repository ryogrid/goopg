# TPC-H Round 5 — Bottleneck Fix Design Bundle

| field | value |
| --- | --- |
| status | draft — **DESIGN ONLY**, implementation not started |
| date | 2026-07-24 |
| branch | authored on `costmodel-enhance1` |
| upstream report | `analysis/tpch-round5-bottleneck-profiles-20260724.md` |
| scope | detailed implementation designs for the 5 bottleneck categories identified in the Round 5 serial TPC-H profiling (bottleneck #1 is addressed by two documents: 01 for the immediate fix, 02 for the systemic generalization) |
| note | Bottleneck #6 ("MHJ per-worker rebuild") from the report's §8 is a parallelism concern — in serial mode there is only one goroutine, and the 5.7% cum labeled "MHJ per-worker rebuild" is actually the MHJ **probe** path (`initStepHelper` at `multi_hash_join.go:502-564`), which is addressed by the decode and clone optimizations in docs 03–05 |

## The problem, in one measurement

`runtime.Stack()` is 69–86% of CPU for 3 of 5 profiled TPC-H queries (Q4, Q7, Q13),
called from `spillWriter.WriteRow` → `activity.LookupCurrentGoroutine` on every
row spilled to disk. After fixing this, the true bottlenecks are row decode
(24–38% cum CPU), row-pool allocation (25–47 GB cum), and row clone for hash
probe (12–26% cum where joins exist).

## Reading order

| Impl. order | Document | Depends on | Payoff | Importance |
| --- | --- | --- | --- | --- |
| **1** | [01-spill-writer-stack-elimination.md](01-spill-writer-stack-elimination.md) | None | **3–7× faster** for Q4/Q7/Q13 | CRITICAL |
| 2 | [02-systemic-backend-id-lookup.md](02-systemic-backend-id-lookup.md) | None (complements 01) | Eliminates remaining `runtime.Stack` callers | MEDIUM |
| 3 | [03-row-decode-fast-path.md](03-row-decode-fast-path.md) | None | 24–38% cum CPU reduction across all queries | HIGH |
| 4 | [04-double-clone-elimination.md](04-double-clone-elimination.md) | None | Eliminates redundant O(width) copy per build row | HIGH |
| 5 | [05-hash-probe-clone-elimination.md](05-hash-probe-clone-elimination.md) | None (optional after 01–04) | 12–26% cum CPU where joins exist | MEDIUM |
| 6 | [06-numeric-fast-path.md](06-numeric-fast-path.md) | Composes with 03 | 4–9% cum alloc reduction | LOW-MEDIUM |

**Recommended implementation order:** 01 → 02 → 06 → 03 → 04 → 05.

- Fixes 01 and 02 are independent and should be implemented first (highest payoff, lowest risk).
- Fix 06 (numeric fast-path) is simple and well-contained — do it before Fix 03 so the decode strategy can directly include the known-scale numeric decode function.
- Fixes 03 and 04 share the scan→decode pipeline and should be designed to compose.
- Fix 05 is the most invasive (hash table internals, ref-counting) and may be deferred — the VirtualSlot composition from M0071-0014 already eliminates per-match copies, so the remaining clone cost at the retention boundary may be acceptable after Fixes 01–04 reduce overall runtime by 3–7×.

## Prerequisites

- Go 1.24+ with `//go:linkname` support for `runtime/pprof.runtime_getProfLabel` (the `gls` package already depends on this)
- `internal/gls` package already created and verified (`gls.SetBackendID` called at connection startup in `server.go:973`)
- `internal/activity` package with `ActivityRegistry` and `slots []activitySlot` array
- `internal/mctx` arena system available (used by seqScanOp for arena-backed decode)
- `internal/executor/row_pool.go` with `sync.Pool` per width (M0068-0004)

## Key profiles (from Round 5 report)

### Serial execution times (baseline)

| Query | Time | Rows | Spills? | Dominant bottleneck |
| --- | ---: | ---: | --- | --- |
| Q1 | 22.55 s | 4 | No | Row decode 37.8% cum |
| Q9 | 30.64 s | 175 | No | Row decode 29.7% + hash probe/clone 26% cum |
| Q4 | 284.70 s | 5 | **Yes** | `runtime.Stack` 78.8% CPU |
| Q7 | 158.64 s | 4 | **Yes** | `runtime.Stack` 69.6% CPU |
| Q13 | 108.87 s | 33 | **Yes** | `runtime.Stack` 85.9% CPU |

### Allocation ranking (cumulative `alloc_space`)

| Query | Total alloc | Row pool | Decode | Numeric parse |
| --- | ---: | ---: | ---: | ---: |
| Q1 | 29.42 GB | 1.95 GB (6.6%) | 1.73 GB (5.9%) | 1.72 GB (5.8%) |
| Q9 | 42.03 GB | 9.94 GB (23.6%) | 1.95 GB (4.6%) | 2.31 GB (5.5%) |
| Q4 | 63.74 GB | 25.41 GB (39.9%) | 2.24 GB (3.5%) | 3.08 GB (4.8%) |
| Q7 | 83.61 GB | 36.72 GB (43.9%) | 2.50 GB (3.0%) | 3.84 GB (4.6%) |
| Q13 | 100.17 GB | 46.91 GB (46.8%) | 2.66 GB (2.7%) | 4.30 GB (4.3%) |

## File modification summary

| File | Changes | Fixes |
| --- | --- | --- |
| `internal/executor/spill.go` | Add `reg`/`procNum` cache to `spillWriter`/`spillReader`; use in `WriteRow`/`ReadRowInto` | 01 |
| `internal/activity/registry.go` | Add `SetGlobalRegistry`, `LookupByBackendID`, update `LookupCurrentGoroutine` to try gls first | 02 |
| `internal/server/server.go` | Add `SetGlobalRegistry` call at startup | 02 |
| `internal/executor/numeric.go` | Add `parseNumericFastScale`; rename existing to `parseNumericFastInt` | 06 |
| `internal/executor/codec.go` | Update numeric decode case to use known-scale fast path; add `*decodeStrategy` param to `DecodeRowIntoMctxPGTuple` | 03, 06 |
| `internal/executor/codec_strategy.go` | **New file**: `decodeStrategy`, `columnDecodeFn`, `buildDecodeStrategy`, per-type specialized decode functions | 03 |
| `internal/executor/operators_storage.go` | Add `strategy` field to `seqScanOp`; populate in `Open`; add transfer-mode hint | 03, 04 |
| `internal/executor/operators_join_agg.go` | Add `alreadyOwned` hint to `drainRowsCtx` to skip redundant copy | 04 |
| `internal/executor/multi_hash_join.go` | Hash-table entry storage for COW/ref-counted rows | 05 |
| `internal/executor/datum.go` | Potential `SharedRow` or COW Datum variant | 05 |

## Related design bundles

- [cost-model/](../cost-model/README.md) — PG-faithful Path/cost model (the planner that selects which plans these fixes benefit)
- [perf-optimize/](../perf-optimize/) — Round 1–4 performance optimizations, including the WAL `runtime.Stack` fix (M0068) that is the direct precedent for Fix 01
- [M0068-0004 row-slot pool](../0050-0099/0068-0004-row-slot-pool.md) — the `rowPool` design that Fix 03 builds upon
- [M0073-0002 decode arena binding](../0050-0099/0073-0002-decode-arena-binding.md) — arena-backed string/bytes decode that Fix 03 extends
