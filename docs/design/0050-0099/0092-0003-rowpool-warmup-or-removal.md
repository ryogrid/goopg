# Design 0092-0003 — rowPool warmup after M0092 fixes

**Status:** authoritative for M0092 close-out.
**Milestone:** [M0092](../../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Background

`internal/executor/row_pool.go` defines a per-width
`sync.Pool` for `Row` slices, introduced in M0068-0004 to
reduce allocation churn for TPC-H wide rows. The pool only
helps when callers DO return rows via `releaseRow`. The
M0091 post-fix pprof showed the pool's `New` callback at
34 % of allocs — i.e., callers were not returning rows
because consumers retained TupleSlot references past Close.

After M0092-0001 (indexScanOp lazy-iterate) and M0092-0002
(projectOp slot-aliasing) land:

- `indexScanOp.scanRow` is the SAME instance across all
  Next() calls. Released back to the pool in Close.
- `projectOp.o.out` is the SAME instance across all Next()
  calls. Released back to the pool in Close.
- `cloneRow` is no longer called from these hot paths.

## Decision

**Keep the rowPool unchanged.**

Rationale:

- After M0092, the rowPool sees correct usage from
  `indexScanOp.Open` / `Close` (acquireRow + releaseRow
  paired) and from `projectOp` lifecycle.
- The remaining `cloneRow` callers (`spill.go:438`,
  `operators.go:510` for the materialize wrapper,
  `operators_storage.go:259` for seqScanOp.Next when filter
  matches) are either rare paths or follow the same
  acquire/release pattern.
- Removing the pool would force every `acquireRow` call to
  call `make`, increasing allocation rate.

The pool's `New` callback fired heavily PRE-fix because
`cloneRow` was on the hot path producing rows that were
never released. POST-fix, cloneRow is rare and the pool
should stay warm.

## Verification

The end-to-end pgbench re-measurement (M0092-0003) is the
empirical check. If `rowPool.New` is still > 5 % of CPU /
allocs post-fix, file as a M0093 candidate to either pool
more aggressively or remove entirely.
