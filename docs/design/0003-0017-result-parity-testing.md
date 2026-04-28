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

## Current parity matrix (synthetic 75-row dataset)

After the 2026-04-29 NUMERIC-hash-key fix
(`canonicalNumericKey` in
`internal/executor/operators_join_agg.go`), 18/22 queries match
identically. The 4 remaining divergences cluster cleanly:

| Q   | Status      | Notes                                                                          |
| --- | ----------- | ------------------------------------------------------------------------------ |
| Q1  | DIVERGENT   | NUMERIC precision: `avg(...)` returns 11.666666 vs upstream 11.6666666666666667 |
| Q2..Q7  | IDENTICAL | (Q2 and Q3 0/1 row baselines, Q4-Q7 fact aggregations)                       |
| Q8  | DIVERGENT   | NUMERIC precision: 1.000000 vs 1.00000000000000000000                          |
| Q9..Q13 | IDENTICAL | (Q9 multi-row, Q11 HAVING-scalar-subquery, Q13 LEFT JOIN)                    |
| Q14 | DIVERGENT   | NUMERIC precision: 51.834417 vs 51.8344178319257926                            |
| Q15 | IDENTICAL   |                                                                                 |
| Q16 | DIVERGENT   | Row counts: goopg=0, upstream=3 — `NOT IN (subquery)` semantics               |
| Q17..Q22 | IDENTICAL |                                                                                |

**Summary**: identical=18, divergent=4, goopg-errored=0,
upstream-errored=0. Three of the four divergences are pure NUMERIC
precision deltas (gated on arbitrary-precision NUMERIC, deferred
per design 0003-0012). Q16 is the only remaining structural gap.

### What the NUMERIC-hash-key fix corrected

`datumKey` (used by hash-join, count-distinct, and group-by hashing)
previously fell through to `"k:6"` for every `KindNumeric` value
because the switch had no `KindNumeric` arm. Since every TPC-H join
key column (c_custkey, o_custkey, l_orderkey, ps_partkey, etc.) is
declared `NUMERIC`, every hash-join with a NUMERIC key degenerated
to a cross product. Q3, Q5, Q7, Q8, Q9, Q10, Q11, Q13 all closed
with this single fix.

The new helper `canonicalNumericKey(mantissa, scale)` strips
trailing-zero pairs (one digit + one scale step at a time) so two
numerics that compare equal hash equal: `1` (m=1,s=0), `1.0`
(m=10,s=1), and `1.00` (m=100,s=2) all canonicalise to `m:1:0`.
KindInt now also routes through this helper at scale 0 so
cross-kind hashes match (`aid = $1` works whether `$1` lands as
KindInt or scale-0 KindNumeric).

## Triage workstream

After the NUMERIC-hash-key fix, the remaining divergences cluster
into:

### NUMERIC precision (Q1, Q8, Q14)

Root cause: `numericDiv` in `internal/executor/numeric.go` uses
`scale = max(scales, 6)` while upstream's NUMERIC division
allows up to 1000 digits. Closing these requires either
arbitrary-precision NUMERIC (a major piece of work) or a wider
fixed scale that better matches HammerDB-shaped ratios. v0
deliberately deferred this in
`docs/design/0003-0012-numeric-arithmetic.md`.

### NOT IN (subquery) semantics (Q16)

Q16 uses `NOT IN (subquery)`. Goopg returns 0 rows where upstream
returns 3. Likely cause: three-valued logic on NULL in the inner
subquery's result set, or a `NOT IN` rewrite that filters too
aggressively. Small, contained — open as the next divergence to
close.

### Closed in 2026-04-29

The previous "row-count divergences from join / GROUP BY semantics"
cluster (Q3, Q5, Q7, Q9, Q10, Q11, Q13 — formerly 7 queries) was
all one bug: NUMERIC datums weren't hashed correctly so any
hash-join with NUMERIC keys degenerated to cross product. Single
~30-line fix in `datumKey` + `canonicalNumericKey`. The
HAVING-scalar-subquery shape in Q11 was a downstream symptom, not
a separate gap.

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
