# Milestone 0092 — Lazy row emission in indexScanOp + projectOp

**Status:** structural changes landed 2026-05-11 (commits
57312d5, 5211387, dc52f60, 8f32c07); pgbench-c10 TPS did NOT
improve — see close-out notes below. M0091's TPS bar (≥ 1,000)
remains unmet; the bottleneck has shifted to broadly-
distributed allocations + GC pressure across many small sites
rather than the cloneRow path that M0092 addressed.
**Depends on:** M0091 (the M0091 close-out identified this as
the residual bottleneck after the activity + btree.RangeScan
fixes landed; pgbench select-only at scale 100 -c 10 -T 180
recovered to 510 TPS but the ≥ 1 000 TPS bar requires this
refactor).
**Drives:** restore goopg's read-only TPS to the historical
M0026 baseline (~6 400 TPS at -c 4) by eliminating the
cloneRow → acquireRow → rowPool.New allocation chain that
dominates the post-M0091 alloc profile (34 % of allocs from
a single inlined helper).

## Context

After M0091-0001 + M0091-0002 landed on 2026-05-11, pgbench
select-only at scale 100, -c 10, -T 180 reaches **510.52 TPS**
(vs the pre-M0091 baseline of 350.89). The improvement is real
but well below the M0091 acceptance bar of 1 000 TPS.

Post-fix pprof
(`pprof-data/m0091/post-0002/select-only-c10.{cpu,heap,allocs}.prof`)
identifies the new dominant bottleneck:

- `runtime.gcDrain` 82 % of CPU — GC still dominant.
- `executor.init.0.func1` (the rowPool's `New`) — 34.75 % of
  total allocs, **2.6 GB over 60 s of pgbench**.
- Cum trace: `executor.acquireRow → cloneRow` 34.66 % of
  allocs.

cloneRow fires at two sites per query:

- `operators_index.go:285` — `o.rows = append(o.rows,
  cloneRow(row))` inside `indexScanOp`'s scanFn. Eagerly
  materialises every matched row into `o.rows` during
  `Open()`/`Rescan()`. For a unique pkey lookup that's 1 row;
  the cloneRow is logically unnecessary (only 1 match, no
  scanRow re-use within this query), but the eager-collect
  pattern requires it because `scanRow` is the decode buffer
  reused per match.
- `operators.go:94` — projectOp.Next:
  `return asSlot(o.schema, cloneRow(o.out)), nil` — allocates
  a fresh Row per emitted output row.

The `rowPool.Get → New` is the load-bearing piece: cloned
Rows are NEVER returned to the pool because the consumer
retains TupleSlot references past `Close()`. (A naïve
release-on-Close was attempted in M0091 and verified to
break `internal/executor/vm_test.go:169` which reads row
data after Close.)

## Approach (proposed)

The structural fix is a two-part refactor:

### M0092-0001 — lazy iteration in `indexScanOp`

Replace the eager `Open() → scanFn populates o.rows`
pattern with on-demand iteration. Possible designs:

- **(A) Iterator-style btree scan.** Introduce a stateful
  `RangeScanIterator` returning one `(key, ptr)` per
  `Next()`. The iterator holds the leaf-page pin between
  calls (cooperating with the M0091-0002 contract). The
  `indexScanOp`'s `Next()` reads one tuple from heap and
  emits — no `cloneRow`, no `o.rows` materialisation. Pin
  release happens when the iterator is `Close()`d or moves
  past the current leaf.
- **(B) Single-row preallocation for unique-key lookups.**
  If the planner has a unique-pkey hint (single match
  guaranteed), preallocate `o.rows` to length 1 with a
  pooled Row, write directly via `scanFn`, and skip
  cloneRow. Less general than (A) but simpler.

(A) is the cleaner long-term solution and matches PG's
`HeapTupleData` cursor model. (B) is the cheap shot.

### M0092-0002 — projectOp slot aliasing

`projectOp.Next` currently allocates a Row for every emitted
row via `cloneRow(o.out)`. Two options:

- **(A) Reuse `o.out` directly.** The consumer reads the
  slot before the next `Next()` call; we can return a slot
  that aliases `o.out`, then overwrite `o.out` on the next
  iteration. Saves the per-row alloc. Matches what
  `seqScanOp` does for its scanRow.
- **(B) Pool-aware emission.** Maintain a small ring of
  pre-allocated Rows and rotate; releaseRow happens after the
  consumer's next `Next()` call. More memory but fewer
  failures of the "consumer retains past current row"
  contract.

(A) is simpler and matches the existing rowPool design
intent. The tricky part is auditing every projectOp
consumer to confirm they don't retain past the next Next.

### M0092-0003 — pgbench re-measurement

After M0092-0001 + M0092-0002 land, re-run the same -S -c 10
-T 180 workload. Target: **TPS ≥ 1 000** (M0091's acceptance
bar). Stretch: ≥ 3 000 (historical -c 1 baseline).

## Required design docs

- `docs/design/0092-0001-indexscanop-lazy-iteration.md`
  (specifies iterator API, pin lifecycle, Rescan semantics).
- `docs/design/0092-0002-projectop-slot-aliasing.md`
  (audits projectOp consumers to confirm slot-aliasing is
  safe; documents the contract).
- `docs/design/0092-0003-rowpool-warmup-or-removal.md`
  (decides whether the rowPool is salvageable given the
  consumer-retains-past-Close pattern, or whether it should
  be removed entirely — pre-M0068 had no pool; M0068
  introduced it for TPC-H Q5 wide rows where pool reuse
  worked).

## Tasks

Tasks will be detailed when this milestone is picked up.

## Outcome (2026-05-11)

### Landed structural changes

- **Commit `57312d5`** — 3 design docs (0092-0001 / -0002 /
  -0003).
- **Commit `5211387` (M0092 prerequisite)** —
  `nestedLoopIndexJoinOp` now deep-copies outerRow into
  `o.currentOuter` (always-correct change; was an
  alias-only retention that worked only because upstream
  producers cloned).
- **Commit `dc52f60` (M0092-0002)** — dropped per-row
  `cloneRow(o.out)` in `projectOp.Next`. Changed
  `MaterializedSlot.Materialize` to ALWAYS deep-copy the
  row slice (was a no-op fast-path for non-arena rows).
  Updated two tests (`TestM0069MaterializedSlot`,
  `TestM0073MaterializeNoArenaIsNoOp →
  TestM0092MaterializeAlwaysDeepCopies`) for the new
  contract.
- **Commit `8f32c07` (M0092-0001)** — `indexScanOp`
  refactored from eager-materialise-all-rows to
  TID-list-eager + heap-fetch-lazy. Removed `o.rows []Row`
  and `o.arena *Arena` fields. Added `o.lastTID` +
  `o.hasLast` for HOT-resolved currentTID(). Per-Next
  pattern: Pin → RLock → followHOTChain → RUnlock → Unpin
  → DecodeRowInto → optional DetoastRow → return slot
  aliasing scanRow.

### Empirical pgbench result

`pgbench -S -c 10 -j 10 -T 180 -P 30` at scale 100:

| metric | pre-M0091 | post-M0091 | **post-M0092** |
|---|---:|---:|---:|
| TPS | 350.89 | **510.52** | **437.62** |
| latency avg | 28.50 ms | 19.59 ms | 22.85 ms |
| failed | 0 | 0 | 0 |

Post-M0092 TPS regressed vs post-M0091 (-14 %). The
structural changes are correct (all unit tests pass, data
integrity preserved) but did NOT deliver a measurable TPS
improvement at pgbench -c 10.

### Why the regression / no improvement

Post-M0092 alloc profile
(`pprof-data/m0092/select-only-c10.allocs.prof`) shows:

- `executor.init.0.func1` (rowPool.New) — 35.28 % of allocs
  — **essentially unchanged** vs the pre-M0092 34.75 %.
  The cloneRow path moved from `projectOp.Next` into
  `slot.Materialize` (which now always deep-copies), so
  every consumer that calls Materialize allocates a fresh
  Row. Net allocation rate per query is similar.
- `storage.PageGetHeapTuple` + `storage.ParseHeapTuple` +
  `executor.SlotFromRow` — small per-Next allocations that
  add up under heavy load.
- GC still ~80 % of CPU.

Conclusion: pgbench-c10's allocation pressure is broadly
distributed across many small sites, not concentrated in
the cloneRow path. Eliminating one site doesn't move the
needle.

### Where the structural changes still matter

The M0092 changes ARE real correctness / future-workload
improvements:

1. `indexScanOp` lazy means range scans with N matches no
   longer pre-materialise N Rows — only N ItemPointers
   (8 bytes each). For wide TPC-H index scans the memory
   footprint drop is dramatic.
2. The `MaterializedSlot.Materialize()` contract is now
   explicit and consistent (always deep-copies). The
   no-op fast-path hid a subtle producer-buffer-reuse
   contract that the eager `cloneRow` masked.
3. `nestedLoopIndexJoinOp.currentOuter` is independent of
   upstream buffer reuse — defensive against future
   producer changes.

### Deferred follow-up (M0093 candidate)

For pgbench-c10 to reach TPS ≥ 1,000, the next milestone
should address the broadly-distributed residual allocations:

- Protocol-layer per-DataRow: `cells := make([][]byte, ncols)`
  + `[]byte(d.Format())` per column per row.
- Plan caching for repeated SQL text (pgbench's simple-query
  protocol parses + plans every query).
- `SlotFromRow` allocates a `MaterializedSlot` struct per
  Next; could be pool'd or stack-allocated via in-place
  update.
- The 14 client-driven `LookupGoroutine` sites in
  `internal/initdb/open.go` (Pool/Manager/AIO hooks) still
  call `runtime.Stack`.
- The `ParseHeapTuple` per-Next copy could alias the page
  bytes (similar to the M0091-0002 PageGetItemRawNoCopy
  pattern) — bigger refactor.

## Definition of Done (sketch)

- pgbench select-only at scale 100 -c 10 -T 180: **TPS ≥
  1 000**.
- pprof: `runtime.gcDrain` CPU share < 40 %.
- `executor.cloneRow` allocations < 1 KB per query (down
  from ~88 KB).
- Existing executor / vm / TPC-H test suites continue to
  pass — the slot-aliasing contract must be respected by
  every existing consumer (audited via the M0092-0002
  design doc).
