# Code Review Summary — 2026-08-31

## Scope

All Go source files in the goopg codebase (`cmd/` + `internal/`), **excluding** `*_test.go`
files and test-support packages (`internal/testutil/`, `internal/testport/`):

- **641 source files** across ~40 packages
- **15 subsystem groups** reviewed independently in parallel
- **~2468 lines** of review output (16 markdown files)

## Review Criteria

Per the user's request:

1. **Obviously wasteful processing** — redundant computation, repeated allocations, unnecessary
   copies, O(n²) patterns, repeated map/slice lookups in loops, string concatenation in loops,
   redundant syscalls, dead code, duplicate work.
2. **More efficient processing without major logic changes** — hoisting loop invariants, reusing
   buffers instead of reallocating, using `strconv` over `fmt.Sprintf`, pre-computing loop-invariant
   results, using cheaper alternatives where available.

## Methodology

Each subsystem was reviewed by a dedicated subagent, which read every Go source file in its
group and wrote findings incrementally to `review/260831/<subsystem>.md`. The orchestrator
spot-checked a sample of findings (both high- and medium-severity) using the Serena MCP server
to verify they are grounded in the actual code.

## Key Findings by Package

| Package | Files | Findings | Top issues |
|---------|-------|----------|------------|
| executor-core | 36 | 23 | Per-row `strings.ToLower` in `evalCast`; dead `fullMantissa` in `copy_binary`; per-row map rebuild in `applyworker`; per-row `fmt.Sprintf` for `uint32`; COPY FROM bypassing row pool |
| executor-operators-1 | 24 | 14 | `sortOp.lessRows` key re-evaluation on every compare; `analyzeRelationWith` decode-before-select reservoir; bitmap index per-entry allocation; `generateSeriesOp` per-row alloc; FK column linear scan |
| executor-operators-2 | 36 | 23 | (see file for per-operator findings) |
| executor-sys | 82 | 22 | PL/pgSQL re-parse/re-plan per call; regexp recompilation per invocation; per-DDL full-catalog mirror pin-walk; multi-column TOAST detoast re-scan |
| optimizer-1 | 20 | 14 | `estimateNumGroups`/`relFilteredRows` repeated full-tree walks + subtree re-estimation; `semiPairMatchFraction` O(n·m) MCV matching; `EstimateRows` no memoization (O(N²) on left-deep chains) |
| optimizer-2 | 70 | 32 | (see file for per-function findings) |
| xlog | 62 | 31 | `readOneAt` double header read; `encodeRecordXLog` two-alloc/three-copy chain; O(N²) segment append growth on recovery; `pgoutput` per-row `time.Date` rebuild |
| initdb | 57 | 14 | Multi-page heap writer loop invariant; `pg_aggregate` view per-query re-allocation; bulk of code is one-shot bootstrap — most findings are minor |
| storage | 35 | 23 | `CollectDeadHeapSlots` uses copy-allocating `ParseHeapTuple` when only header is needed; FSM grow-one-at-a-time + O(n) scan per insert; three tuple-decode paths with unnecessary data copy |
| nbtree-amcheck | 35 | 14 | `pglz.Compress` quadratic brute-force matcher; `Search` whole-leaf decode per point lookup; `resetPageItems`/`pinNewOrRecycled` byte-at-a-time zeroing |
| transam | 21 | 14 | Per-snapshot `abortedXIDs` copies; per-statement deep `Clone()` of pinned RR/SSI snapshots; goroutine-per-wait in `WaitForXID` |
| parser-nodes | 42 | 18 | Per-token `mapToken` string-hash map lookup; double `strings.ToLower` per token in `next()`; `strings.Split`/`fmt.Sprintf` in date-time parsing |
| catalog-postmaster | 61 | (see file) | Generated seed/data files are intentional; main findings in catalog routines and postmaster dispatch |
| utils | 28 | 15 | `encoding_guc` re-cleans constant encoding table per lookup; `pg_datetime_format` Sprintf per-cell; `conv` dead `destBuf` up to 4× input; `mctx.Lookup` global mutex on hot datum-read path |
| cmd | 25 | 7 | One-shot generators — findings are minor (double JSON decode, dead sort, per-row alloc in EXPLAIN path) |

## Distribution

- **High-severity findings**: 0 (no blocking correctness or performance issues found)
- **Medium-severity findings**: ~30 (mostly in hot paths: executor, xlog, optimizer, nbtree, storage)
- **Low-severity findings**: ~180 (micro-optimizations, loop-invariant hoisting, allocation patterns)

## Most Impactful Findings

1. **`pglz.Compress`** (nbtree-amcheck): quadratic brute-force matching in the LZ compressor
2. **`executor.go`/`expr.go`** — per-row `strings.ToLower` in `evalCast` (medium)
3. **`applyworker.go`** — per-row column-name map rebuild (medium)
4. **`copy_binary.go`** — dead `fullMantissa` computation (medium)
5. **`copy.go`** — COPY FROM bypasses row pool (medium)
6. **`xlog/iterator.go`** — `readOneAt` double header read (medium)
7. **`xlog/format.go`** — `encodeRecordXLog` two-alloc copy chain (medium)
8. **`xlog/reorder.go`** — `foldChanges` full copy even when nothing folds (medium)
9. **`storage/heap.go`** — `CollectDeadHeapSlots` uses copy-allocating decode (medium)
10. **`transam/snapshot.go`** — per-snapshot `abortedXIDs` copies (medium)

## Conclusion

The codebase is generally well-written, with the bulk of findings being low-severity
micro-optimizations. No correctness issues or performance bugs were found. The most
valuable improvements would be in the hot paths of the executor (expression evaluation,
COPY, data decoding), WAL append/decode, and the btree search path.

The `pglz` compression, `encoding_guc` encoding table, and a few other areas have
genuine medium-severity inefficiencies that would benefit from targeted optimization.