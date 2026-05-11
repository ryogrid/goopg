# Milestone 0092 — Lazy row emission in indexScanOp + projectOp

**Status:** planned
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
