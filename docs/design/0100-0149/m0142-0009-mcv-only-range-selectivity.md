# M0142-0009 — a range predicate over an MCV-complete column must price from the MCV mass, not punt to the default

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md`'s M0142-0009 line, filed by M0142-0004c from the
post-C1-fix re-capture: several plain `Nested Loop`/`Gather` join nodes
estimate single-digit rows against four-to-five-digit actuals, at `loops=1`
(so not a second instance of 0004a's loops-capture bug). Named witnesses: Q25
`date_dim+store_returns` est=1 vs actual=6422; Q34
`date_dim+household_demographics+store` est=3 vs actual=9969; similarly
Q29/Q53/Q63/Q68/Q10/Q69/Q33. The task's own next step: instrument the
smallest witness (Q25) and establish whether this is one mechanism or several
coincidentally-similar ones, without assuming it is M0142-0006's
`semiJoinMatchFraction` gap — the practice card's standing warning against
guessing a join-estimation mechanism from a plan-node label alone
(`goopg_swallowed_error_in_rewrite_driver`, `goopg_plan_census_greps_a_label`).

## Method — instrument before theorising

Reproduced Q25 on a private clone of the TPC-DS SF0.25 data (`bench/tpcds`'s
shared `:65437` cluster copied to a throwaway port so the investigation could
restart the server with a debug binary without touching the shared lane —
`goopg_shared_bench_cluster_collisions`). `EXPLAIN` on the full query
isolates the witness node to a `Seq Scan on store_returns` (71676 rows) outer
joined via `Memoize` + `Index Scan using date_dim_pkey` inner, keyed on
`store_returns.sr_returned_date_sk`, with residual `Filter: (((d_moy >= 4)
AND (d_moy <= 10)) AND (d_year = 1999))`.

Narrowing further, the SINGLE-TABLE filter alone reproduces the collapse with
no join involved:

```
EXPLAIN SELECT * FROM date_dim WHERE d_moy BETWEEN 4 AND 10 AND d_year = 1999;
 Gather  (cost=0.00..730.52 rows=1 width=420)
   ->  Parallel Seq Scan on date_dim  (cost=0.00..730.50 rows=1 width=420)
         Filter: (((d_moy >= 4) AND (d_moy <= 10)) AND (d_year = 1999))
```

against real PG 18.3 on the same-generation data (`:65438`, db `tpcds025`):

```
 Seq Scan on date_dim  (cost=0.00..2702.36 rows=212 width=118)
   Filter: ((d_moy >= 4) AND (d_moy <= 10) AND (d_year = 1999))
(actual count: 214)
```

goopg is off by >200x on a single-table filter with no join involved — the
join-level qerr the task was filed against is a downstream symptom, not the
defect's location. Decomposing the filter:

- `d_moy >= 4` alone: `rows=24349` (73049/3, i.e. exactly
  `defaultIneqSelectivity` = 1/3 — a **punt**, not a measurement).
- `d_moy <= 10` alone: same punt, same reason.
- `d_moy BETWEEN 4 AND 10` (both bounds): `rows=365` = 73049 *
  `defaultRangeIneqSel` (0.005) — `rangequery.go`'s pairing correctly detects
  that both bounds independently equal the default constant and refuses to
  trust their combination (comment at `rangequery.go:186-193`), so it
  substitutes PG's own `DEFAULT_RANGE_INEQ_SEL` instead of computing
  `hi+lo-1`. This part is working as designed.
- `d_year = 1999` alone: `rows=362` (a real, stats-derived NDistinct-based
  estimate, not a punt).
- All three combined: `rows=1` — `365/73049 * 362/73049 * 73049 = 1.81`,
  floored. The BETWEEN pairing's own punt (0.005) is what starves the whole
  clause, not the AND-combination arithmetic.

The question became: why does `d_moy`'s range predicate punt to the default
at all, when `d_moy` is a fully-analysed 12-value column? Reading
`rangeOpSelectivityStats` (`internal/optimizer/selectivity.go:333`):

```go
func rangeOpSelectivityStats(op parser.OpCode, col *ColumnRef, val Expr, stats *catalog.ColumnStats) (float64, bool) {
	if stats == nil || len(stats.Histogram) < 2 {
		return 0, false
	}
	...
```

bails out unconditionally whenever the column's histogram has fewer than two
boundaries — discarding any MCV list the column DOES carry. And
`computeColumnStats` (`internal/executor/operators_analyze.go:1441,1489-1495`)
builds the histogram **only from the non-MCV remainder**:

```go
completeAndFits := nmultiple == len(buckets) && len(buckets) <= statsTarget && ...
...
nonMCV := buckets[mcvCount:]
if len(nonMCV) < 2 {
    return stats   // Histogram left nil
}
```

`d_moy` has 12 distinct values, comfortably under the default stats target,
so every bucket becomes an MCV entry (`completeAndFits`), `nonMCV` is empty,
and `stats.Histogram` is correctly left unset — this mirrors real PG's
`compute_scalar_stats` (`analyze.c`): when every distinct value fits the MCV
list, PG **also** builds no histogram. Both engines make the identical,
correct ANALYZE decision. The divergence is entirely on the *consumer* side:
PG's `scalarineqsel` (`selfuncs.c`) still sums the MCV-covered mass
(`mcv_selec`) and only defaults the **non-MCV remainder** (here, zero);
goopg's guard discarded the whole clause the moment the histogram was short,
throwing away a fully-measured MCV mass it already had in hand.

## Fix

`internal/optimizer/selectivity.go`, `rangeOpSelectivityStats`: relax the
early-return guard to proceed when the column carries an MCV list even
without a usable histogram —

```go
if stats == nil || (len(stats.Histogram) < 2 && len(stats.MCV) == 0) {
    return 0, false
}
```

No other line changes. The existing MCV-mass loop below already computes
`mcvHits` correctly; the existing `histogramOpSelectivity` call already
returns `defaultIneqSelectivity` gracefully when `bounds` is short (its own
`k < 1` guard, unchanged), and that constant is scaled by `nonMCVMass` — which
is ~0 for an MCV-complete column, so the defaulted term contributes
negligibly instead of replacing the whole answer. This is exactly PG's
`scalarineqsel` composition (`mcv_selec + hist_selec * (1 - nullfrac -
summcv)`), just reached through the two functions goopg already had.

Verified post-fix, same fixture:

```
EXPLAIN SELECT * FROM date_dim WHERE d_moy BETWEEN 4 AND 10 AND d_year = 1999;
 Gather  (cost=0.00..736.82 rows=211 width=420)   -- was rows=1; PG=212, actual=214
```

## Why this is absorption, not tuning

`AGENT.md`'s owner decision **B2** requires that a fix give PG's formula the
PG-equivalent input rather than bending an output toward a target number. This
change adds no constant and no query-specific special case: it restores a
code path (`histogramOpSelectivity`'s existing `k<1` fallback and the
MCV-mass loop already in the function) that was unconditionally skipped by an
over-broad guard. The behaviour it produces — MCV mass answers the query
directly when the histogram is empty because MCV already covers everything —
is PG's own `scalarineqsel` structure, confirmed against the live PG 18.3
oracle on identically-generated data (212 vs goopg's now-211, both close to
the actual 214; previously goopg read 1).

## Blast radius and floor measurements (mandatory for M0137–M0143 tasks)

`rangeOpSelectivityStats` is a shared primitive (single-table filters, OR
arms, join clause pricing via `clauseSelectivity`), so the fix reaches far
beyond the TPC-DS date-dimension witnesses. Measured before landing, per
`AGENT.md` §"What every M0137–M0143 task report must contain":

- **Unit suite**: `go test ./internal/optimizer/...` — PASS (pre-existing
  `TestOrRangeSelectivityMeasured` / `TestOrRangeSelectivityDeclinesWithoutHistogram`
  unaffected — their fixtures carry no MCV either, so the relaxed guard is a
  no-op for them). New test
  `TestRangeOpSelectivityUsesMCVWithoutHistogram` (`rangequery_test.go`) pins
  the mechanism directly: a 12-value MCV-complete column now measures `moy>=4`
  at 9/12 and the BETWEEN pairing at 7/12, not the 1/3 / 0.005 punts.
- **TPC-DS SF0.25 regression sweep** (`scripts/tpcds-sf025-regression.sh
  sweep`): `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3` — row
  counts and result checksums unchanged against the PG oracle; **83/99 plan
  shapes changed** underneath (more accurate stats reordering costed choices)
  with zero correctness regressions.
- **`make ea-ratchet`** (the corpus this task was filed from): baseline
  122 -> 112 findings. **12 FIXED**: Q10, Q25, Q29, Q34 (x3), Q68 (x2), Q69,
  Q73 (x2), Q79 — covering every named witness except Q33/Q53/Q56/Q60/Q63
  (those are CTE-`UNION ALL`-shaped, M0142-0004b's still-open C2 mechanism,
  or otherwise untouched by this fix — confirmed by name, not assumed). **2
  NEW, smaller findings** surfaced: `Q34:date_dim+store+store_sales` (Gather,
  qerr 43.4) and `Q73:date_dim+store+store_sales` (Hash Join, qerr 41.8) — a
  join-level estimate one level up the tree that was previously masked by
  the leaf's much larger (qerr in the thousands) error and is only now
  visible. Net corpus size improved (-10); baseline re-pinned to the new
  112-entry set (`EA_CAPTURE=tmp/c20a/ea-capture.txt EA_REPIN=1 bash
  scripts/estimate-parity-gate.sh`, same technique M0142-0004c used) after
  `make ea-ratchet` re-ran clean (`PASS`). The 2 new findings are filed as
  **M0142-0010** (below / fix_plan) rather than chased in this task, per the
  milestone's own recon-then-fix decomposition precedent.
- **TPC-H plan-parity floor** (`estimate-audit -plan-only` against a private
  clone of the SF=1 HammerDB bench data + PG `:65432`, `-serial` default):
  **match=8/22 both before and after** this fix (pre-fix build captured via
  `git stash` on the same data) — the floor (>=6) was already above its
  2026-09-14 filing figure from prior work (M0141-S2a-fix1) and this task
  neither raised nor lowered it. Category counts moved by at most ±1 per
  category (aggregation-strategy 9->8, sort-strategy 7->6, missingnode 2->1,
  shapediff 12->13) — noise-level, not a claimed win.
- **TPC-DS plan-parity floor** (`scripts/pg-plan-parity-diff.py` against the
  committed `bench/tpcds/plans-pg` fixture): **match=2/99 (Q9, Q41) both
  before and after** — floor held exactly, byte-identical
  `CATEGORIES`/`CATEGORIES-EXCL-MATCH` lines pre- and post-fix despite the
  83-query shape churn the sweep reported: the per-query divergence
  *category set* (which families of difference a query has vs PG) did not
  change even where the specific plan bytes did. This fix improves estimate
  *accuracy* (the `ea-ratchet` corpus) without yet flipping additional
  queries to full plan MATCH — consistent with the milestone's own "no
  single fix flips a query" framing.
- **`go build ./...`**: clean. **Pre-commit gates**: `scripts/tpch-spotcheck.sh`
  RESULT=PASS (Q12=2, Q13=34, both canonical); pgbench smoke via the commit
  hook.

## Follow-up

**M0142-0010** (filed in fix_plan.md): the 2 new post-fix findings,
`date_dim+store+store_sales` at qerr ~42 in both Q34 and Q73 — a join-level
estimate this fix's leaf correction unmasked. Resume point: instrument
`estimateJoin`/`estimateNLIndexJoin` on that specific relset the way this
task instrumented `rangeOpSelectivityStats` — establish the mechanism before
proposing a fix, per the same practice-card discipline this task followed.

Also still open and untouched by this task: M0142-0004b (C2, the 3-way CTE
`UNION ALL` `est=3` mechanism — Q33/Q56/Q60's remaining findings are that
shape, not this one).
