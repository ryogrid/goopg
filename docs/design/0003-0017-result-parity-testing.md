# TPC-H Result-Parity Testing vs Upstream PostgreSQL 18.3

**Status**: accepted
**Milestone**: 0003 (HammerDB TPC-H)
**Implementation**: `internal/testutil/tpch/parity_test.go`,
                    `internal/testutil/tpch/upstreampg_test.go`

## Problem

Milestone 0003's Definition of Done #3 requires that "for each
query, results are byte-identical or otherwise verified-equivalent
to the same query against an upstream PostgreSQL on the same
generated data set." The earlier executor-time test
(`TestRunTPCHQueriesAgainstSyntheticData`) only asserts that each
query *executes without error* — many semantic gaps are
silently wrong: a join that produces extra rows, an aggregate
that miscomputes precision, a HAVING clause that filters too
aggressively. None of these surface as errors.

The HammerDB SF1 path is gated on Docker `--network host`
reachability under WSL2 (see fix_plan.md notes), so we can't yet
verify parity at scale. But we *can* run the same DDL + INSERTs
against both engines on a small synthetic dataset and diff the
rows.

## Decision

Build an in-tree parity tester that:

1. spins up goopg via the existing `internal/testutil/cluster`
   harness;
2. spins up upstream PostgreSQL 18.3 from
   `postgres/local_install/bin` via a new minimal lifecycle
   wrapper (`upstreamPG`: `initdb` → `pg_ctl start` → libpq →
   `pg_ctl stop`);
3. applies the eight `tpch.DDL()` statements and the
   `tpch.SampleInserts()` rows to both engines;
4. runs each `tpch.Queries()` Q1..Q22 against both;
5. records per-query: status (OK / error), row count, and the
   first cell that differs;
6. logs a parity matrix and fails closed only when goopg
   errors on a query upstream handles cleanly (a real
   regression).

The test is *informational by default* on row-content
divergences: numeric-format conventions and trailing-space
behaviour diverge in known ways, and the synthetic dataset is
small enough that some result discrepancies are expected
(e.g., goopg's NUMERIC division is hardcoded to scale 6 while
upstream uses scale 17). As individual divergences are closed
the must-match list will grow.

## Current parity matrix (synthetic 75-row dataset, 2026-04-29)

| Q   | Status      | Notes                                                                          |
| --- | ----------- | ------------------------------------------------------------------------------ |
| Q1  | DIVERGENT   | NUMERIC precision: `avg(...)` returns 11.666666 vs upstream 11.6666666666666667 |
| Q2  | IDENTICAL   | 0 rows on both                                                                  |
| Q3  | DIVERGENT   | Row counts: goopg=5, upstream=1 — likely GROUP BY semantics gap               |
| Q4  | IDENTICAL   | 1 row                                                                           |
| Q5  | DIVERGENT   | Row counts: goopg=5, upstream=1 — likely same root cause as Q3                |
| Q6  | IDENTICAL   | 1 row                                                                           |
| Q7  | DIVERGENT   | Row counts: goopg=4, upstream=1                                                |
| Q8  | DIVERGENT   | Row counts: goopg=2, upstream=1                                                |
| Q9  | DIVERGENT   | Row counts: goopg=20, upstream=6                                              |
| Q10 | DIVERGENT   | Row counts: goopg=25, upstream=1                                              |
| Q11 | DIVERGENT   | Row counts: goopg=1, upstream=2 — HAVING/scalar subquery comparison           |
| Q12 | IDENTICAL   | 0 rows                                                                          |
| Q13 | DIVERGENT   | Row counts: goopg=1, upstream=2                                                |
| Q14 | DIVERGENT   | NUMERIC precision: 40.000000 vs 51.8344178319257926                            |
| Q15 | IDENTICAL   | 0 rows                                                                          |
| Q16 | DIVERGENT   | Row counts: goopg=0, upstream=3 — likely `NOT IN (subquery)` semantics        |
| Q17 | IDENTICAL   | 1 row                                                                           |
| Q18 | IDENTICAL   | 0 rows                                                                          |
| Q19 | IDENTICAL   | 1 row                                                                           |
| Q20 | IDENTICAL   | 0 rows                                                                          |
| Q21 | IDENTICAL   | 0 rows                                                                          |
| Q22 | IDENTICAL   | 0 rows                                                                          |

**Summary**: identical=11, divergent=11, goopg-errored=0,
upstream-errored=0.

## Triage workstream

Divergences group into three clusters that can be tackled
independently:

### NUMERIC precision (Q1, Q14)

Root cause: `numericDiv` in `internal/executor/numeric.go` uses
`scale = max(scales, 6)` while upstream's NUMERIC division
allows up to 1000 digits. Closing these requires either
arbitrary-precision NUMERIC (a major piece of work) or a wider
fixed scale that better matches HammerDB-shaped ratios. v0
deliberately deferred this in
`docs/design/0003-0012-numeric-arithmetic.md`.

### Row-count divergences from join / GROUP BY semantics (Q3, Q5, Q7, Q8, Q9, Q10, Q13)

These are the highest-impact divergences and the most likely to
reveal real bugs. The pattern (goopg returning more rows than
upstream) suggests one of:

- GROUP BY de-duplication failing for some grouping-key shapes
  (e.g., string-format keys vs typed-Datum keys);
- Hash-join producing duplicate matches (e.g., hash bucket
  match plus nested-loop match in the fallback path);
- Predicate-pushdown losing a conjunct between the rewrite and
  the resulting Filter node;
- Cross-join residual still emitting all-pairs when an
  expression couldn't be classified as disjoint-side.

The systematic next step is to add an EXPLAIN-emitting variant
of the parity test for the 7 row-count-divergent queries and
compare the plan tree shapes.

### NOT IN / HAVING subquery semantics (Q11, Q16)

Two subquery shapes that may have edge cases v0 doesn't cover:
- Q11's HAVING clause references a scalar subquery whose
  return value is then compared via `>`. If goopg's scalar
  subquery materialisation differs in scale or NULL handling,
  the HAVING-true count diverges.
- Q16 uses `NOT IN (subquery)`. If the subquery returns NULL,
  upstream PG specs `NOT IN (NULL)` evaluates to NULL, which
  filters out rows. Goopg's executor may not honour the
  three-valued logic exactly.

Each of these is a small, contained fix once the divergence is
isolated.

## Out of scope

- **SF1 parity**. That requires the HammerDB harness, blocked on
  Docker / WSL2 reachability — see fix_plan.md
  "2026-04-29 SF1 attempt: Docker `--network host` reachability
  blocker". Synthetic-data parity catches most semantic gaps at
  much lower cost.
- **Locale-sensitive comparison**. Upstream's COLLATE handling
  is rich; v0 sorts text byte-wise. The current test sets
  `LC_ALL=C` for the upstream cluster's `initdb` to match
  goopg's byte-wise behaviour.
- **Wire format byte-for-byte parity**. We compare values after
  libpq has scanned them as text via `database/sql`, which
  applies its own normalisation. Wire-format parity for the
  numeric BEGIN/END byte sequences is a separate concern and
  not covered here.

## Test gating and CI

The parity test is `t.Skip()`'d when `testing.Short()` is set
or when `postgres/local_install/bin` is missing, so it doesn't
fail in environments without the upstream binary. On dev
machines with the upstream install, it runs as part of the
`go test ./...` suite and currently completes in ~36 seconds.

## References

- `internal/testutil/tpch/parity_test.go` — the parity test
  itself.
- `internal/testutil/tpch/upstreampg_test.go` — the upstream PG
  lifecycle wrapper.
- `internal/testutil/tpch/tpch.go` — DDL, sample inserts, and
  query templates shared across all three TPC-H tests
  (plan/build/run/parity).
- `docs/milestones/0003-tpch-workload.md` — the DoD this test
  partially fulfils (#3 verified-equivalent to upstream PG).
- `docs/design/0003-0012-numeric-arithmetic.md` — context on
  the NUMERIC precision divergences.
