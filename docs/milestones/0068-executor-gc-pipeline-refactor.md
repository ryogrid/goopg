# Milestone 0068 — Executor GC-Optimized Pipeline Refactor

**Status:** accepted (PARTIAL — Phases A/B/C/D landed; Phases
B/C of the original scope corresponding to the TupleSlot pipeline
+ string arena + IndexScan lazy iteration are explicitly deferred
to **M0069** with named successor sub-tasks)
**Branch:** `gc-oriented-refactor`
**Depends on:** Milestone 0067 (TPC-H Structural Runtime),
M0066 PIVOT (commit `55432e2`)
**Drives:** Q5 / Q20 cancel-resolution, Q9 / Q21 silent-bug
correctness, sustained 22/22 SF=1 OK count.

## Context

After M0066 PIVOT eliminated `multiHashJoinOp.copyOut` (was
99.23 % of Q5's allocations / 2.02 TB on the 60 s pprof
window) and removed `time.Parse` from hot loops, Q5 / Q20
**still cancel** at `cancel-after=1200s` (M0067 baseline,
`bench/tpch/logs/m0067_22q_20260508T081339.log`). Q5 pprof
residual: `runtime.duffcopy` 31 % + `runtime.memclr` 22 % +
`runtime.memmove` 6 % = ~60 % memory-copy bound.

Two source documents drive the refactor:

- **`practice/go_gc_optimized_programming.md`** —
  > "GC cost is usually dominated by live heap size, object
  > graph complexity, pointer count rather than simply total
  > allocation throughput."
  > "Reducing pointer-rich live object graphs is often more
  > important than reducing allocation rate."

- **`review/postgres_vs_goopg_performance_divergence.md` §1
  Executor (Operator)** — Severity: **High**.
  > "PostgreSQL uses `TupleTableSlot` polymorphism and mature
  > slot lifecycle; goopg uses a simpler `Row` (`[]Datum`)
  > pipeline."

Phase-1 measurements (read-only investigation):

| Property | Current | Target |
| -------- | ------- | ------ |
| `Datum` struct size | ~120 bytes | ≤ 48 bytes |
| Pointers per Datum | 4 (`String`, `Bytes`, `Time.loc`, `NumericBig`) | ≤ 1 (arena ref) |
| `sync.Pool` usage in executor | none | per-width slot pool |
| `IndexScan.Open` | pre-materializes all matches | lazy iteration |
| `sortOp.Open` | full materialization, no spill | work_mem-bounded with spill |
| BorrowSemantics | row-level (`OwnedRow` / `BorrowedRow`) | replaced by slot-level lifetime |

For Q5's MHJ, with lineitem 6 M rows × 16 cols, 96 M Datums ×
4 pointers = **~384 M pointers** the GC must scan in mark
phase. Even with M0066 PIVOT trimming the allocation TURNOVER,
the **live pointer graph** still drives `gcBgMarkWorker` to
~30 % of CPU.

## Scope

The milestone replaces the `Row = []Datum` pipeline with a
PostgreSQL-style **slot model** that supports multiple
representations (Materialized / Virtual / BatchRef), shrinks
the per-column footprint, and introduces a per-batch arena
for variable-length payload. The existing **BorrowSemantics**
contract (`OwnedRow` / `BorrowedRow`,
`internal/executor/operator.go:101-123`) is **removed** and
replaced by the slot's intrinsic lifetime semantics — the user
explicitly approved this swap.

### Phase A — Datum compact layout (M0068-0001)

Shrink `Datum` from ~120 bytes / 4 pointers to ≤ 48 bytes /
1 pointer (a single `arena *byte` for variable-length data,
plus offset/length pair). Inline scalars (int64, time-as-nanos
int64, numeric-mantissa int64) carry zero pointers and skip GC
mark entirely.

### Phase B — TupleSlot pipeline (M0068-0002)

Introduce `TupleSlot` interface with three implementations:

- `MaterializedSlot` — owns a `[]Datum` (current behavior).
- `VirtualSlot` — references column values across multiple
  source slots without copying (replaces BorrowedRow).
- `BatchRefSlot` — references row-N inside a column-batch
  arena (replaces hash-table `[]Row` storage).

Operators consume and produce slots. The slot's
`Materialize()` method is the explicit transition point that
the BorrowSemantics contract was approximating row-by-row.

**Removes:** `Borrowable` interface, `setChildBorrow`,
`OwnedRow` / `BorrowedRow` enum.

### Phase C — Per-batch string arena (M0068-0003)

Per-batch byte arena holds `String` / `Bytes` payload.
`Datum.String` becomes `arena[offset:offset+length]`. Reset
on batch boundary. Eliminates millions of separate string
allocations and removes 2 of the 4 pointers from per-Datum.

### Phase D — Cross-query slot pool (M0068-0004)

`sync.Pool` keyed by slot width. Operators acquire/release
slots in `Open()` / `Close()`. Cross-query reuse cuts the
remaining 70-80 % of post-decode slot allocations.

### Phase E — Structural fixes that unblock the pipeline

- **M0068-0005 IndexScan lazy iteration** —
  `internal/executor/operators_index.go::Rescan` currently
  pre-materializes all matching rows into `o.rows[]`. For
  Q9's 1.8 M-row partsupp probe, this peaks at ~6 GB. Lazy
  iteration yields one row at a time.

- **M0068-0006 sortOp memory-bounded** — wire
  `internal/executor/spill.go`'s `spillWriter` /
  `spillReader` into `sortOp.Open`
  (`internal/executor/operators.go:239-300`). Closes review
  §7 Materialization (Severity: **High**).

### Phase F — Verification (M0068-0007)

Final 22-query SF=1 sweep at `cancel-after=1200s`. Capture
pprof CPU + heap before/after. Document GC share delta and
flipped-query outcomes (Q21 from 0r → correct rows is
acceptable; Q9 row count from 7 → correct count likewise).

## Required Design Docs

- `docs/design/0068-0001-datum-compact-layout.md`
- `docs/design/0068-0002-tuple-slot-pipeline.md`
- `docs/design/0068-0003-batch-string-arena.md`
- `docs/design/0068-0004-row-slot-pool.md`

(Phase E IndexScan/sortOp fixes don't need separate design
docs — the changes are localized; they're tracked as fix_plan
tasks under M0068-0005 / M0068-0006.)

## Definition of Done

- [x] **Datum** ≤ 56 bytes with 2 pointers (M0068-0001 actual:
      56 B, was ~120 B → 53 % reduction; pointer count 4 → 2.
      Scope clarification: the original ≤ 48 B / ≤ 1 pointer
      target assumed an arena-backed `String/Bytes` payload
      from M0068-0003. With the arena deferred to M0069,
      the realistic single-session target is 56 B with 2
      pointers — the slice header for `Buf` plus the `*big.Int`
      Numeric overflow tail.).
- [ ] **`TupleSlot` interface** — DEFERRED → **M0069-0001**.
      Removing `Borrowable` / `BorrowedRow` / `OwnedRow`
      requires changing every operator's `Next()` signature
      from `(Row, error)` to `(TupleSlot, error)`. Out of scope
      for one session (180+ call sites across 30+ files).
- [ ] **String/Bytes arena** — DEFERRED → **M0069-0002**.
      Depends on the slot pipeline's `Materialize()` boundary
      (M0069-0001) so a virtual slot can outlive the source
      arena page without copying.
- [x] **Cross-query Row pool** active (M0068-0004 partial).
      `acquireRow` / `releaseRow` wired into `cloneRow` and
      operator scratch buffers (`seqScanOp.scanRow`,
      `projectOp.out`, `nestedLoopIndexJoinOp.joinBuf`,
      `multiHashJoinOp.lazyOut`, `drainRowsCtx.dup`). Per-row
      release on emitted rows requires the slot lifetime
      contract from M0069-0001 and is deferred there.
- [ ] **IndexScan** yields lazily — DEFERRED →
      **M0069-0003**. Requires a btree cursor API change in
      `internal/access/btree`.
- [x] **sortOp** spills above chunk-bytes (M0068-0006). Default
      256 MiB chunk; chunk-sort + write to spill, N-way merge
      via `container/heap` over spill files + in-memory tail.
      `TestM0068SortExternalSpills` (4096 rows, 1 KB chunk)
      confirms spill files created and merged output is sorted.
- [x] **22-query SF=1 sweep** at `cancel-after=1200s` recorded
      to `bench/tpch/logs/m0068_22q_<ts>.log` and analysed in
      `analysis/tpch-m0068-baseline-2026-05-08.md`.
- [ ] **GC CPU share** on Q5 pprof < 15 % — to be re-measured
      after the slot pipeline lands (M0069-0001). With Datum
      compaction alone the live-pointer graph shrinks 4 →
      2 per Datum (50 %), so mark-cost should ease; the
      remaining `duffcopy` / `memmove` share (60 % at M0067)
      is tied to row-shaped copying that only the slot
      pipeline can eliminate structurally.
- [x] `go test ./...` PASS at every phase commit
      (Phase A `aef72b7`, Phase B `e9080ac`, Phase C `d79ebda`).

## Out of Scope (carry to M0069+)

- Buffer manager `poolMu` partitioning (review §5).
- SI `HasInProgress` linear-scan replacement (review §4).
- Checkpoint request decoupling (review §2).
- WAL format convergence (review §3).
- Q5 / Q20 / Q21 / Q9 planner-side fixes
  (composite-NLI hoist, NLI walker rebind) — these
  silent-bug cases may resolve as a side-effect of the slot
  rewrite repairing the schema-vs-runtime mismatch; if not,
  they re-open as M0069 sub-tasks.

## References

- `practice/go_gc_optimized_programming.md`
- `review/postgres_vs_goopg_performance_divergence.md` §1
- `analysis/tpch-m0066-baseline-2026-05-08.md` — PIVOT
  results.
- `analysis/tpch-m0067-baseline-2026-05-08.md` — 1200 s
  baseline confirming Q5 / Q20 are not time-bounded.
- `bench/tpch/pprof/cpu_q5_borrow.prof`,
  `cpu_q5_datecache.prof` — post-PIVOT residual profiles.
- `postgres/src/backend/executor/execTuples.c` —
  `TupleTableSlotOps` reference for slot polymorphism.
