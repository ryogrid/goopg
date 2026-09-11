# R84 — Report: skip redundant outer Sort over Distinct (Q41)

*Ordering axis. Design: `SCOPE.md` (reviewed
APPROVE-WITH-NOTES, notes applied; scope commit `33832ff`).*

## Change

`internal/optimizer/planner.go` (1-line gate call),
`internal/optimizer/distinctpaths.go`
(`distinctOutputSatisfiesOrder` helper),
`internal/optimizer/distinct_outer_sort_skip_test.go`
(2 shape pins), `internal/executor/distinct_orderby_null_test.go`
(2 values pins).

The M0097-0046 outer Sort is skipped iff the node is
`*Distinct` (type-gate, fail-closed) AND every outer key is
ASC + nulls-last (effective flags) forming a positional
prefix of the distinct output (`ColumnRef Index == position`).
Sound because `distinctOp` hash-dedups then always re-sorts
ascending/nulls-last over all columns — input order is
destroyed, so the delivered order satisfies exactly its
prefixes. DESC / NULLS FIRST / non-prefix / non-`*Distinct`
all keep the Sort (root-0036 preserved).

## What it fixed

- **TPC-DS Q41: 3 → 1 categories.** `Limit → Sort →
  Unique → Sort → SeqScan` became `Limit → Unique → Sort →
  SeqScan` — PG's shape modulo display. Remaining gap is
  ONLY parameterisation (`$0` vs `i1.i_manufact`,
  named follow-up). Corpus totals: join-order 95→94,
  sort-strategy 80→79.
- Values identical by construction (order-preserving skip
  on already-ordered output) + pinned (NULL-ordering
  values tests both directions).

## Provenance note (recorded honestly)

The gate call line was present uncommitted in the working
tree with no helper (tree didn't build) and unattributable
provenance (no peer process active, last peer session 2h
prior). Verified the diff was exactly the scoped one-liner,
then completed it with the specified helper + pins. If a
peer claims it, the helper/pins/gates here are all mine.

## Gates (all 2026-09-12)

- Units + optimizer/executor suites fresh green
  (testcache cleaned; 4 new pins).
- TPC-H spotcheck Q12/Q13 PASS.
- SF0.25 sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0`;
  plan-shape channel 98 same / changed=Q41 (intended);
  Q41 values PASS (1 row, checksum-verified).
- DS A/B (same clone): plan-shape only Q41 moves
  (+ PID-noise error sections; timing moves in the
  non-blocking status channel are not plan moves);
  STOP conditions held (no other moves).
- TPC-H A/B: 22/22 identical.

## Evidence

- `/tmp/pp2/r84/ds-r84.txt`, `/tmp/pp2/r84/tpch-r84.txt`,
  `/tmp/pp2/r84/ds-diff-v.txt` (captures + verbose diff)
- Sweep: `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-20260912-051121.txt`
- Servers `:5557` (DS), `:5554` (TPC-H); peer `:5533`
  and PG refs untouched.

## Follow-ups (named, not owned)

- `$0` correlated display (PARAM_EXEC vs outer-var) —
  Q41's last gap; needs EXPLAIN renderer mapping work.
- `ParamRef` LIMIT allowlist (R83 follow-up, still open).
- M0097-0046 restructuring beyond the skip (none needed).
