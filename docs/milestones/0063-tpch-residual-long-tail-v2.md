# Milestone 0063 — TPC-H Residual Long-Tail v2

**Status:** planned
**Depends on:** Milestone 0062 (commit `977ff22`, verified in
`analysis/tpch-m0062-final-baseline-2026-05-07.md`)
**Drives:** clean 22/22 SF=1 pass with canonical row counts and
sub-600 s wall-clock for every query, OR a named follow-up for
each remaining gap. NO-DEFERRAL POLICY identical to M0061 / M0062.

## Context

After M0062 wrapped, six queries still block a 22/22 SF=1 pass.
The post-M0062 sweep (commit `977ff22`,
`bench/tpch/logs/m0062_post_22q_20260507T161549.log`) shows:

| Q | Symptom | Bucket |
| --- | --- | --- |
| Q5 | ERROR after 600 s (cancel held) | B — 6-table MHJ probe throughput |
| Q8 | OK 188.23 s, 0 rows | A — derived-table NLI outer column-resolution bug |
| Q13 | ERROR after 600 s (cancel held) | E — LEFT JOIN with NL + NOT LIKE residual |
| Q15b | OK 25.08 s, 0 rows | A — same bucket as Q8 |
| Q20 | ERROR after 600 s | C — multi-level subquery + correlated scalar |
| Q21 | ERROR after 600 s (cancel held) | D — anti-join with 6 M-row build |

Cancel-prop is verified responsive on all four 600 s queries
(M0062-0001 / M0062-0005 / commit `6f618d2` build-phase ctx);
the residual gaps are throughput / correctness, not propagation.

The five buckets (A–E) are technically distinct enough to warrant
one design doc each. Q8 and Q15b share root cause (bucket A) and
ride a single sub-task with two acceptance gates.

## Sub-tasks

- **M0063-0001** Bucket A — derived-table NLI outer
  column-resolution. Q8 + Q15b. Symmetric to M0062-0006's
  MHJ-side fix; the NLI rewrite for `Project(Values(...))` /
  aggregate-result outers mis-resolves the IndexScan key.
  See `docs/design/0063-0001-nli-derived-table-key-resolution.md`.

- **M0063-0002** Bucket B — Q5 six-table MHJ probe
  throughput. Build-order tuning, possibly index-driven
  inner sides for the small dimensions (region, nation,
  supplier).
  See `docs/design/0063-0002-q5-six-table-mhj-tuning.md`.

- **M0063-0003** Bucket C — Q20 correlated scalar subquery
  decorrelation. M0062-0004 unblocked the outer IN gate;
  the inner correlated scalar
  `SELECT 0.5 * SUM(...) WHERE l_partkey=ps_partkey AND
  l_suppkey=ps_suppkey` still re-evaluates per partsupp row.
  Decorrelate to a hash-keyed aggregate joined to the outer.
  See `docs/design/0063-0003-q20-correlated-scalar-decorrelation.md`.

- **M0063-0004** Bucket D — Q21 anti-join with index-driven
  inner. M0062-0005 produces a correct hash-anti, but its
  build over 6 M lineitem rows is the bottleneck. Rewrite
  to NestedLoopIndexAnti probing `idx_lineitem_orderkey`
  per outer row (reuses M0054-0006 NLI machinery + an
  anti-emit variant).
  See `docs/design/0063-0004-q21-anti-join-index-driven.md`.

- **M0063-0005** Bucket E — Q13 LEFT JOIN with NOT-LIKE
  residual. The current planner emits a Nested Loop for the
  LEFT JOIN with NOT-LIKE in the join Predicate; that's
  150 K × 1.5 M ≈ 225 G pair evaluations. Rewrite to LEFT
  Hash Join on `c_custkey = o_custkey` plus a post-join
  Filter for NOT-LIKE (scoped to actually-matched rows).
  See `docs/design/0063-0005-q13-left-join-not-like-rewrite.md`.

## Required Design Docs

- `docs/design/0063-0001-nli-derived-table-key-resolution.md`
- `docs/design/0063-0002-q5-six-table-mhj-tuning.md`
- `docs/design/0063-0003-q20-correlated-scalar-decorrelation.md`
- `docs/design/0063-0004-q21-anti-join-index-driven.md`
- `docs/design/0063-0005-q13-left-join-not-like-rewrite.md`

## Definition of Done

- [ ] M0063-0001 merged; Q8 ≥ 1 row AND Q15b ≥ 1 row on SF=1.
      Reproducer probe passes:
      `SELECT count(*) FROM supplier, (SELECT 1 AS x) v
       WHERE s_suppkey = v.x` returns `1` regardless of the
      `enable_nestloop_index` GUC.
- [ ] M0063-0002 merged; Q5 OK in < 600 s on SF=1.
- [ ] M0063-0003 merged; Q20 OK in < 600 s on SF=1.
- [ ] M0063-0004 merged; Q21 OK in < 600 s on SF=1.
- [ ] M0063-0005 merged; Q13 OK in < 600 s on SF=1.
- [ ] Final 22-query SF=1 sweep with row-count parity for
      every previously-OK query AND < 600 s for every query.
- [ ] `go test ./...` PASS at each step.

## ⚠ NO-DEFERRAL POLICY

Identical to milestones 0058 / 0061 / 0062: blocked sub-tasks
must carry `BLOCKED: <reason>` in `.ralph/fix_plan.md` and name
a concrete follow-up. Silent close is not allowed.
