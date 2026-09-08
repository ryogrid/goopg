# Planner row-estimate collapse — diagnosis and fix design

Ledger row: `take3-tpcds-rowest-3-to-5-orders` (`.ralph/deferral_ledger.md:2100`).
Census that raised it: `analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/`
(commit `ca2809ab7`), which measured estimated-vs-actual input rows on all 100
`Sort` nodes across the 99 TPC-DS SF0.5 queries.

**Status: diagnosis + design only.** No `internal/` file is changed by this
document. Every mechanism below was confirmed by running a patched *probe*
binary built in a throwaway worktree; the probe diff is committed alongside
this doc as `analysis/planner-refactor-take3/rowest-collapse-20260906/probe-patch.diff`
and is **not** a proposed patch — it is the instrument.

```
date:      2026-09-06
goopg:     f7a345e32 (clean detached worktree; the working tree carried five agents' WIP)
probes:    tmp binaries `goopg` (base) and `goopg-fixab` (base + the two probe patches)
clusters:  throwaway :5533 (synthetic + a private copy of SF0.5 store_sales)
           bench/tpcds/runtime_goopg/data-sf05 on :65437, S-cold, under flock /tmp/goopg-65437.lock
oracle:    ./postgres (PG 18.3, read-only) and bench/tpcds/plans-pg/*.txt
```

---

## 0. Summary — four mechanisms, and one of them is not a bug

The census reported two defects. They resolve into **four** distinct
mechanisms, of which three are goopg defects and one is expected behaviour.

| # | mechanism | census witnesses | goopg | PG 18.3 | actual | verdict |
|---|---|---|---:|---:|---:|---|
| **A1** | range-pairing omits PG's null-double-exclusion correction | Q28 × 6 scans | 1 | 5 337 | 15 410 | **defect** (probe fix → 14 932) |
| **A2** | `rangeOpSelectivity` bails before reading the MCV list when a column has no histogram | none in TPC-DS; found synthetically | 1 000 | — | 10 000 | **defect** (latent) |
| **A3** | `LEFT JOIN … WHERE x IS NULL` is not reduced to an anti-join, and the `IS NULL` is then priced from `stanullfrac = 0` | Q78 | 1 | 858 | 245 587 | **defect** |
| **A4** | long chains of independent equi-join selectivities floor at `clamp_row_est` | Q47, Q57 (Q81, Q89 nearby) | 1 | **1** | 43 626 | **EXPECTED — PG produces the identical 1** |
| **B1** | `resolveBaseColumn` has no `*NestedLoopIndexJoin` arm → every grouping variable above an NLI prices at `DEFAULT_NUM_DISTINCT`, the independence product saturates, and the aggregate's output estimate becomes its *input* row count | Q99, Q62, Q22, Q12 | 720 657 | 72 | 90 | **defect** (probe fix → 90) |
| **B2** | same resolver, missing `*Append` arm (UNION ALL) | Q76 | 67 352 | 6 810 | 470 | **defect**, but PG is 14× over too |

The ledger's stated hypothesis for defect (b) — *"the error is inside the
per-set `estimateNumGroups`, the independent-ndistinct product across grouping
columns, where PG leans on extended statistics"* — is **refuted**.
`estimateNumGroups` (`internal/optimizer/cardinality.go:1183`) is a faithful
`estimate_num_groups` and computes the right answer whenever it is given real
ndistinct values. The failure is one level up, in variable *resolution*: the
estimator never finds the statistics, falls back to `DEFAULT_NUM_DISTINCT = 200`
per key, and the resulting product immediately saturates the closing
`numdistinct > rows` clamp. Extended statistics are not involved and would not
have helped.

After the two probe patches, goopg's estimates on the four aggregate witnesses
match or beat PG 18.3:

| query | goopg base | goopg + probe | PG 18.3 | actual |
|---|---:|---:|---:|---:|
| Q28 (per scan) | 1 | 14 932 | 5 337 | 15 410 |
| Q99 HashAggregate | 720 657 | **90** | 72 | 90 |
| Q62 HashAggregate | 359 432 | **150** | 120 | 150 |
| Q22 grouping-sets HashAggregate | 9 460 201 | 72 001 | 71 857 | 11 987 |
| Q12 WindowAgg | 107 310 | 4 572 | — | 932 |

---

## 1. Preliminary: statistics were NOT the cause

Both defects were suspected of being an artefact of goopg's per-connection
statistics. They are not, and this was checked before anything else.

The `data-sf05` cluster ANALYZEs at load time
(`scripts/tpcds-sf05-regression.sh:411-419`), and since M0125-0028/-0029 the
per-column stats and the relation size survive a restart. Verified directly:
on the private :5533 cluster, `ANALYZE store_sales` in one session, restart the
server, then `EXPLAIN` in a fresh session with no ANALYZE — every row estimate
came back byte-identical (`rows=1167`, `rows=7198`, `rows=479869`, …). The
S-cold regime the census ran under is therefore a *statistics-present* regime,
and every number below is an estimate computed from real MCV lists and
histograms.

Where statistics genuinely are absent the symptom is different and
distinguishable: `width=428` (the no-stats default width) and a seq-scan cost
built from a defaulted relation size. The census plans show `width=428` because
TPC-DS's `store_sales` row really is that wide, not because stats were missing —
the analyzed copy on :5533 reproduces the same `cost=…14396` scan.

---

## 2. Defect (a) — "collapse to exactly 1"

### 2.1 A1 — the range-pairing null double-exclusion (Q28)

#### Minimal reproducer

Four lines of SQL, no benchmark data
(`analysis/planner-refactor-take3/rowest-collapse-20260906/repro-a1-range-nullfrac.sql`):

```sql
CREATE TABLE nn (v integer);   -- no nulls
CREATE TABLE nz (v integer);   -- 4.4 % nulls, identical non-null distribution
INSERT INTO nn SELECT (i % 1000) + 1 FROM generate_series(1,500000) i;
INSERT INTO nz SELECT CASE WHEN i % 1000 < 44 THEN NULL ELSE (i % 1000) + 1 END
                 FROM generate_series(1,500000) i;
ANALYZE nn; ANALYZE nz;
EXPLAIN SELECT 1 FROM nn WHERE v between 100 and 149;   -- true 25 000
EXPLAIN SELECT 1 FROM nz WHERE v between 100 and 149;   -- true 25 000
EXPLAIN SELECT 1 FROM nz WHERE v between 100 and 109;   -- true  5 000
```

Observed (goopg `f7a345e32`, :5533):

| predicate | nullfrac | `>=` bound | `<=` bound | estimate | actual |
|---|---:|---:|---:|---:|---:|
| `nn.v BETWEEN 100 AND 149` | 0.000 | 451 011 | 73 815 | 24 827 | 25 000 |
| `nz.v BETWEEN 100 AND 149` | 0.044 | 451 874 | 51 925 | **3 800** | 25 000 |
| `nz.v BETWEEN 100 AND 109` | 0.044 | — | — | **2 500** | 5 000 |

The only difference between the two tables is the null fraction, and it costs
6.6× on the wide band and lands the narrow band on a hard-coded constant
(2 500 / 500 000 = 0.005 = `DEFAULT_RANGE_INEQ_SEL`).

On the real corpus, on `bench/tpcds/runtime_goopg/data-sf05` (:65437, S-cold, no
ANALYZE), the same mechanism produces the census number exactly:

```
EXPLAIN SELECT 1 FROM store_sales WHERE ss_quantity >= 0;
  ->  Parallel Seq Scan on store_sales  (cost=0.00..28158.25 rows=1376217 ...)
EXPLAIN SELECT 1 FROM store_sales WHERE ss_quantity <= 5;
  ->  Parallel Seq Scan on store_sales  (cost=0.00..14975.53 rows=57945 ...)
EXPLAIN SELECT 1 FROM store_sales WHERE ss_quantity between 0 and 5;
  ->  Parallel Seq Scan on store_sales  (cost=0.00..14396.09 rows=1 ...)   -- actual 68 801
```

`lobound = 1 376 217 / 1 439 608 = 0.955955`;
`hibound = 57 945 / 1 439 608 = 0.040251`;
`hibound + lobound − 1.0 = −0.003794`.

That lands in `(−0.01, 0]`, which is slammed to `1.0e-10`, and
`1 439 608 × 1e-10 = 0.000144` clamps to **1**. The `cost=…14396.09` matches
the census's Q28 scans to the cent, so this is the same node.

The sibling arm fires on the same query: `ss_coupon_amt BETWEEN 1319 AND 2319`
estimates 7 198 = `0.005 × 1 439 608` = `DEFAULT_RANGE_INEQ_SEL` exactly,
because for that column `hi + lo − 1 < −0.01`.

#### Mechanism

`conjunctionSelectivity`, `internal/optimizer/rangequery.go:179-193`:

```go
s2 := g.hiBound + g.loBound - 1.0
// PG additionally adds nulltestsel(IS_NULL) here to undo the
// double-exclusion of NULLs. goopg has no nulltestsel yet (P1-14), so
// that term is omitted — it only matters for a column with a
// significant null fraction, and omitting it UNDER-estimates slightly
// rather than reverting to the independent product.
switch {
case s2 < -0.01:
        s2 = defaultRangeIneqSel      // 0.005
case s2 <= 0.0:
        s2 = 1.0e-10
}
```

Upstream, `clauselist_selectivity_ext`,
`postgres/src/backend/optimizer/path/clausesel.c:290-313`:

```c
s2 = rqlist->hibound + rqlist->lobound - 1.0;

/* Adjust for double-exclusion of NULLs */
s2 += nulltestsel(root, IS_NULL, rqlist->var, varRelid, jointype, sjinfo);

if (s2 <= 0.0)
{
        if (s2 < -0.01)
                s2 = DEFAULT_RANGE_INEQ_SEL;
        else
                s2 = 1.0e-10;
}
```

Both bounds are computed by `scalarineqsel`, which returns the fraction of
**all** rows satisfying the operator — nulls satisfy neither, so each bound has
already excluded them. `hi + lo − 1` therefore subtracts the null mass twice,
and PG adds one copy back. goopg does not.

The comment is wrong on two counts, and both were load-bearing:

1. *"goopg has no nulltestsel yet (P1-14)"* — **stale**. P1-14 landed;
   `nullTestSelectivity` is `internal/optimizer/selectivity.go:1074` and the
   null fraction is one field lookup away (`columnStatsForChild(...).NullFrac`).
2. *"it only matters for a column with a significant null fraction, and
   omitting it UNDER-estimates slightly"* — **wrong about the magnitude**.
   The error is additive in the null fraction but the *result* is a selectivity
   of comparable size, so the relative error explodes as the range narrows.
   Once `range_sel − nullfrac` crosses zero the estimate does not degrade
   gracefully — it hits `1.0e-10` (five orders) or `0.005`, whichever guard it
   lands in. TPC-DS's fact tables carry ~4.4 % nulls in nearly every column, so
   **every predicate narrower than ~4.4 % of a TPC-DS fact table is destroyed**,
   which is most of the corpus's selective predicates.

#### Probe result

Adding `s2 += NullFrac` (probe patch, `rangequery.go`):

| predicate | base | probe | PG 18.3 | actual |
|---|---:|---:|---:|---:|
| `ss_quantity BETWEEN 0 AND 5` | 1 | 57 945 | — | 68 801 |
| `ss_coupon_amt BETWEEN 1319 AND 2319` | 7 198 | 31 527 | — | 30 142 |
| Q28 B1 full predicate | 1 | **14 932** | 5 337 | 15 410 |

3 % error on the composite predicate, against PG's own 2.9× underestimate.

### 2.2 A2 — the MCV list is unreachable when a column has no histogram

Latent: no TPC-DS witness, found while decomposing A1.

Reproducer (`repro-a2-no-histogram.sql`), 200 000 rows:

```
q100 (v = i % 100 + 1, so 100 distinct values → MCV list holds all, no histogram)
  v <= 5              est 66 666   (= 1/3)
  v >= 1              est 66 666   (= 1/3)
  v BETWEEN 1 AND 5   est  1 000   (= 0.005)   actual 10 000
q500 (500 distinct values → histogram present)
  v <= 25             est 10 371
  v >= 1              est 200 000
  v BETWEEN 1 AND 25  est 10 370               actual 10 000
```

`rangeOpSelectivity`, `internal/optimizer/selectivity.go:315-318`:

```go
stats := columnStatsForChild(col.Index, child)
if stats == nil || len(stats.Histogram) < 2 {
        return defaultIneqSelectivity          // 1/3
}
```

The function has a perfectly good MCV loop at lines 325-346 — it is simply
unreachable when the histogram is absent. Upstream does the opposite ordering
(`scalarineqsel`, `postgres/src/backend/utils/adt/selfuncs.c:588`, merge block
at `:696-712`):

```c
mcv_selec  = mcv_selectivity(vardata, &opproc, collation, constval, true, &sumcommon);
hist_selec = ineq_histogram_selectivity(...);      /* -1 when there is no histogram */

selec = 1.0 - stats->stanullfrac - sumcommon;
if (hist_selec >= 0.0)
        selec *= hist_selec;
else
        selec *= 0.5;          /* "arbitrarily assume half of them will match" */
selec += mcv_selec;
```

A missing histogram is worth `0.5` over the *residual* mass in PG; in goopg it
throws away the MCV list entirely and returns `1/3`. Paired by
`conjunctionSelectivity`, `1/3 + 1/3 − 1 = −1/3 < −0.01`, so a `BETWEEN` on any
such column is a flat `DEFAULT_RANGE_INEQ_SEL`.

ANALYZE produces no histogram exactly when every distinct value fits the MCV
list — i.e. for **low-cardinality columns**, which is where `BETWEEN` on a
small integer domain lives. This did not fire on TPC-DS SF0.5 (`ss_quantity`
has 100 distinct values in 1.44 M rows and does get a histogram there), but it
is one ANALYZE away on any narrower table.

`rangeOpSelectivityWithSource` carries the identical bail at
`internal/optimizer/selectivity.go:929`, so both members of the pair need the
same edit — this is the sibling-paths rule the file's own comments are about.

### 2.3 A3 — `LEFT JOIN … WHERE x IS NULL` is not reduced to an anti-join (Q78)

Reproducer (`repro-a3-antijoin.sql`), two tables, 500 000 + 5 rows, 10 % of the
fact rows have no matching dimension row:

```
SELECT 1 FROM fact2 f LEFT JOIN dim d ON f.d_id = d.id WHERE d.id IS NULL;
  ->  Hash Left Join  (cost=0.00..10000.06 rows=1 width=44)
        Hash Cond: (f.d_id = d.id)
        Filter: (d.id IS NULL)
                                                        actual 50 000

SELECT 1 FROM fact2 f WHERE NOT EXISTS (SELECT 1 FROM dim d WHERE d.id = f.d_id);
  ->  Hash Anti Join  (cost=0.00..6666.71 rows=83333 width=8)
                                                        actual 50 000
```

The same predicate, written two ways: the `NOT EXISTS` spelling becomes a
`Hash Anti Join` and estimates 83 333 (1.7× over — the right order); the
`LEFT JOIN … IS NULL` spelling stays a `Hash Left Join` with a residual
`Filter`, and the filter is priced by `nullTestSelectivity` from the base
column's `NullFrac`. `dim.id` has no nulls, so the filter's selectivity is
**0**, and `clamp_row_est` turns 0 into 1. A 50 000× underestimate produced by
a statistic that is correct and an inference that never happened.

This is the census's largest single miss: Q78's three CTE bodies each carry
this shape (`Filter: (wr_order_number IS NULL)` etc.), the Hash Join above each
one reads `rows=1` against a 1 439 608-row input, and the plan root's Sort
reads 1 against 245 587 actual.

PG converts the shape in `reduce_outer_joins`
(`postgres/src/backend/optimizer/prep/prepjointree.c:3379-3403`, "See if we can
reduce JOIN_LEFT to JOIN_ANTI"), which is why `bench/tpcds/plans-pg/Q78.txt`
shows `Nested Loop Anti Join (cost=1964.22..34743.01 rows=858 width=28)` where
goopg shows a left join and a filter.

goopg **has** this transform — `internal/optimizer/reduce_outer_joins.go:134-139`
sets `j.Type = parser.JoinAnti` when a forced-null column appears in a strict
position of the ON clause, with `find_forced_null_vars` mirrored at
`:493-510`. It did not fire here. The two-table reproducer above uses explicit
`LEFT JOIN` syntax with the IS NULL directly on the join key, which is the
textbook case, so the gap is in the transform's trigger conditions, not in
Q78's CTE nesting. Root-causing *why* it declines is a separate, bounded
investigation and is scoped as cut A3 below rather than guessed at here.

### 2.4 A4 — Q47 / Q57 are EXPECTED behaviour, not a collapse

Q47 and Q57 are the census's second- and fifth-largest misses (1 vs 43 626 and
1 vs 17 189) and they are **not defects**. Both are the TPC-DS "compare a
month's sales to the two adjacent months" shape: a CTE `v1`, self-joined three
ways in `v2` on `(i_category, i_brand, s_store_name, s_company_name)` plus
`rn = rn ± 1`. Seven equi-conditions, all mutually implied, multiplied as
independent events; the product floors at `clamp_row_est`.

PostgreSQL 18.3 does exactly the same thing on exactly the same node:

```
bench/tpcds/plans-pg/Q47.txt:   ->  Sort  (cost=745.30..745.30 rows=1 width=400)
bench/tpcds/plans-pg/Q57.txt:   ->  Sort  (cost=384.96..384.97 rows=1 width=690)
```

Both `rows=1`. The finding is a **reclassification**: an estimate of 1 on this
shape is the reference implementation's answer, and goopg reproducing it is
plan-parity, not a bug. Chasing it would mean building equivalence-class
de-duplication of redundant join clauses at estimate time — a real improvement,
but one PG does not have either, and therefore not a compatibility item.

The two neighbours are close enough to fall in the same bucket:
Q81 goopg 1 / PG 3 / actual 362, and Q89 goopg 1 / PG 60 / actual 1 730. Both
are ordinary join-cardinality underestimates within an order or two of PG's
own; neither is a mechanism of its own.

### 2.5 What the census's "22 estimated at 1" actually contains

Of the 52 census rows with `est = 1`, only eight have an actual above 2 000:

| query | actual | mechanism |
|---|---:|---|
| Q78 | 245 587 | A3 |
| Q47 | 43 626 | A4 — expected |
| Q28 × 6 | 15 406 – 18 372 | A1 |
| Q57 | 17 189 | A4 — expected |
| Q7, Q26 | 2 847, 1 822 | small; A4 class |

So the addressable population is **seven nodes**: six A1 and one A3.

---

## 3. Defect (b) — the aggregate over-estimate

### 3.1 Isolating it

Q99 groups by `substr(w_warehouse_name,1,20), sm_type, cc_name` over a five-way
join. Truth: `warehouse` has 4 distinct names, `ship_mode` 6 `sm_type`,
`call_center` 3 `cc_name`; 4 × 6 × 3 ≈ 90, and the query really does return 90
rows. goopg estimated 720 657.

Progressive decomposition on `data-sf05`
(`analysis/planner-refactor-take3/rowest-collapse-20260906/sf05-probe-q99-isolate.sql`):

| probe | HashAggregate `rows=` |
|---|---:|
| `GROUP BY w_warehouse_name` on `warehouse` alone | 5 ✓ |
| `GROUP BY substr(w_warehouse_name,1,20)` on `warehouse` alone | 5 ✓ |
| the three dim tables cross-joined, grouped by all three keys | 90 ✓ |
| the three dims **with `substr`** | 90 ✓ |
| Q99's whole five-way join, no `ORDER BY`/`LIMIT` | **90 ✓** |
| the same **plus `ORDER BY … LIMIT 100`** | **720 657 ✗** |

`substr` is exonerated; the join depth is exonerated. The trigger is the
`LIMIT`, and the reason is that the `LIMIT` changes the *plan shape*: with no
limit, `date_dim` is hash-joined (`Hash Join … rows=3048`); with the limit,
`tuple_fraction` makes the planner prefer a parameterised inner probe, and the
plan becomes

```
HashAggregate  (rows=720657)
  ->  Nested Loop  (cost=3.95..466752.07 rows=720657 width=1084)
        ->  Hash Join  (rows=720657)  ...  catalog_sales ⋈ warehouse ⋈ ship_mode ⋈ call_center
        ->  Memoize  (rows=1)
              ->  Index Scan using date_dim_pkey on date_dim
```

The top join is a `*NestedLoopIndexJoin`, and the aggregate's estimate is
exactly its input row count — the signature of the closing clamp in
`estimateNumGroups`.

Confirmed by `enable_memoize=off` and `enable_nestloop=off` (both still choose
the NLI shape and both still read 720 657), so it is the node type, not the
cache.

### 3.2 Mechanism

`resolveBaseColumn`, `internal/optimizer/joinkeyproof.go:130-192`, ends its
switch at `case *Join:` — **there is no `*NestedLoopIndexJoin` arm**:

```go
	case *Join:
		...
		if idx >= lw {
			return resolveBaseColumn(idx-lw, x.Right)
		}
		return resolveBaseColumn(idx, x.Left)
	}
	return baseColumnRef{}, false          // ← every NLI lands here
}
```

`*NestedLoopIndexJoin` is a separate node type (`internal/optimizer/plan.go:876`)
with `Outer` / `Inner` rather than `Left` / `Right`. Any column read above one
therefore resolves to nothing, and:

- `examineGroupVar` (`internal/optimizer/cardinality.go:1327-1356`) falls
  through both its arms and returns `groupVarInfo{ndistinct: defaultNumDistinct}`
  — `200.0`, `internal/optimizer/joinselectivity.go:62`;
- those variables have `rel == nil`, so they skip the per-relation clamp and
  multiply straight into the total (`cardinality.go:1225-1231`);
- `200³ = 8 000 000 > 720 657`, so the closing `if numdistinct > rows` clamp
  (`cardinality.go:1281-1283`) returns `rows`.

**The aggregate's output estimate becomes its input row count.** That is the
whole of defect (b): not an ndistinct product that is too large, but an
ndistinct product that is *meaningless* and a clamp doing its job.

The arithmetic checks out on the grouping-sets case too. Q22 is
`ROLLUP(i_product_name, i_brand, i_class, i_category)`, five sets, over an input
estimated at 4 710 000:

```
1 (the grand total) + 200 + 200² + 4 710 000 + 4 710 000 = 9 460 201
```

which is the census's Q22 number to the unit. `estimateAggregate`'s summing
(`cardinality.go:1087-1151`) is exactly right; every term it sums is garbage.

This is the sibling-paths defect class in a new place, and the file's own
comments say so. `relFilteredRowsWalk` — three hundred lines further down the
*same subsystem*, `internal/optimizer/cardinality.go:1474-1478` — **does** have
the arm:

```go
	case *NestedLoopIndexJoin:
		if x.Inner != nil {
			return joinSide(x.Outer, x.Inner)
		}
		return joinSide(x.Outer)
```

and `resolveBaseColumn`'s own doc comment (`joinkeyproof.go:112-127`) is a
warning about precisely this: *"same arm list, same coordinate rule, three
different answers about the same column … the `*Project` remap and the `*Join`
arm each went into ONE of them first and the divergence was a live defect both
times."* It happened a third time.

Because `columnStatsForChild` and `columnRawRowsForChild`
(`internal/optimizer/selectivity.go:761-782`) now both delegate to
`resolveBaseColumn`, the blast radius is **not limited to aggregates**: every
`clauseSelectivity` call on a column above an NLI join also falls to a default.
Any TPC-DS query whose LIMIT pushes it into an NLI shape is planning with
default selectivities from that node upward.

### 3.3 Probe result

Adding the `*NestedLoopIndexJoin` arm (17 lines, mirroring the `*Join` arm with
`Outer`/`Inner`; SEMI/ANTI is safe because such a join's `Output()` is
outer-only so `idx >= ow` cannot occur):

| query | base | probe | PG 18.3 | actual |
|---|---:|---:|---:|---:|
| Q99 HashAggregate | 720 657 | **90** | 72 | 90 |
| Q62 HashAggregate | 359 432 | **150** | 120 | 150 |
| Q22 grouping-sets HashAggregate | 9 460 201 | 72 001 | 71 857 | 11 987 |
| Q12 WindowAgg | 107 310 | 4 572 | — | 932 |
| Q76 HashAggregate | 67 352 | 67 352 | 6 810 | 470 |

Q99 and Q62 become exact. Q22 lands within 0.2 % of PostgreSQL's own estimate
(72 001 vs 71 857) — both are ~6× over the truth, which is upstream's error and
not goopg's to fix.

### 3.4 B2 — Q76 is a different, smaller gap

Q76 does not move, because its grouping keys come out of an `Append`
(`UNION ALL` of three branches), and `resolveBaseColumn` has no `*Append` arm
either — the same failure, one node type over. Two of its five keys
(`channel`, `col_name`) are per-branch string literals, which PG resolves as
`('catalog'::text)` constants.

PG estimates 6 810 against an actual of 470 — 14× over. goopg's 67 352 is 143×
over. So this is a real gap but a much smaller one, and there is no PG-parity
prize at the end of it.

---

## 4. Cut list

Every cut here changes estimates, and an estimate change is a cost change, so
**every cut moves plans**. That is stated once and then per-cut only where the
movement is expected to be large. None of these is a one-liner that can be
dropped in without a plan-gate re-pin.

### Cut A1 — the null-double-exclusion term (`rangequery.go`)

Add `s2 += nullfrac(var)` before the `s2 <= 0` guard, matching
`clausesel.c:292-294`.

- **Where**: `internal/optimizer/rangequery.go:179`. `rangeQueryClause` must
  carry the `*ColumnRef` so the null fraction is reachable; `rangeBoundOf`
  already computes it and discards it (`rangequery.go:63-88`), so the change is
  to thread it through, not to re-derive it. The value is
  `columnStatsForChild(cr.Index, child).NullFrac`. Prefer routing through
  `nullTestSelectivity` (`selectivity.go:1074`) so there is one null-fraction
  reader, not two.
- **Size**: ~15 lines.
- **Plan movement: large and pervasive.** Every paired `BETWEEN` on a nullable
  column changes, in the direction of *more* rows. TPC-DS fact tables are ~4.4 %
  null nearly everywhere, so most selective fact-table predicates move. Expect
  scan→index and join-order flips. This cut needs the SF0.5 sweep and the
  plan-gate re-pin, and its diff read line by line.
- **Also delete the stale comment** at `rangequery.go:180-184`; leaving a
  comment that says the term "only matters ... slightly" next to the fix would
  re-license the mistake.

### Cut A2 — MCV before histogram in `rangeOpSelectivity`

Restructure to PG's order: compute the MCV contribution first, then use the
histogram if there is one and `0.5` over the residual mass if there is not
(`selfuncs.c:696-712`).

- **Where**: `internal/optimizer/selectivity.go:315-318`, **and** its twin
  `rangeOpSelectivityWithSource` at `:929`. Landing one without the other is
  the exact drift the surrounding comments warn about. The `reliable` flag
  should become true when the MCV list answered, even with no histogram.
- **Size**: ~25 lines.
- **Plan movement: small on TPC-DS/TPC-H** (no witness in either corpus), but
  non-zero on the regress suite's small tables, where "all distinct values fit
  in the MCV list" is the normal case. Land it *after* A1 so the two are
  separable in the sweep.
- Landable independently of everything else.

### Cut A3 — make `LEFT JOIN … IS NULL` reduce to an anti-join

Root-cause why `reduce_outer_joins.go`'s LEFT→ANTI transform
(`internal/optimizer/reduce_outer_joins.go:134-139`) declines the two-table
reproducer in §2.3, then fix the trigger.

- **Where**: `internal/optimizer/reduce_outer_joins.go`. Compare
  `find_forced_null_vars` / the "reduce JOIN_LEFT to JOIN_ANTI" conditions at
  `postgres/src/backend/optimizer/prep/prepjointree.c:3379-3403`.
- **Start from the reproducer, not from Q78.** `repro-a3-antijoin.sql` is two
  tables and explicit `LEFT JOIN` syntax; whatever declines there is the
  smallest possible statement of the bug, and Q78's CTE nesting is very likely
  downstream of it.
- **Size**: unknown until the decline is located. Bound it: if the transform
  turns out to be structurally unable to see the qual (e.g. the qual is pushed
  to a different node before the transform runs), this becomes a
  larger-than-one-commit item and should be re-scoped rather than forced.
- **Plan movement: large but localised.** An anti-join is a different physical
  operator, so the shape changes, not only the number. On the other hand
  `Hash Anti Join` already exists and is already reached from `NOT EXISTS`, so
  the executor side is exercised.
- **Fallback if the transform cannot be fixed cheaply**: do *not* patch
  `nullTestSelectivity` to invent a non-zero selectivity for this case. That
  would fix the number by breaking the statistic, and the same `IS NULL` on a
  plain scan (which is correct today — `ss_customer_sk IS NULL` on `store_sales`
  estimates 64 638 against a real null fraction) would go wrong.

### Cut B1 — the `*NestedLoopIndexJoin` arm in `resolveBaseColumn`

- **Where**: `internal/optimizer/joinkeyproof.go`, after the `case *Join:` arm
  at `:180-191`. Mirror it with `Outer`/`Inner` and `ow := len(x.Outer.Output())`.
  The probe patch is 17 lines and is in
  `analysis/planner-refactor-take3/rowest-collapse-20260906/probe-patch.diff`.
- **Size**: 17 lines. This *is* effectively a one-liner-class fix, and it is
  left here deliberately rather than raced against the in-flight C-19f work.
- **Plan movement: large.** This is the highest-value cut in the document — it
  takes Q99 and Q62 from 8007×/2396× to exact, and Q22 to PG parity — and it
  changes selectivities as well as group counts, from every NLI node upward.
  Anything whose plan contains a `NestedLoopIndexJoin` re-costs.
- **Test it with the census, not with a unit test alone.** A unit test that
  builds an `*NestedLoopIndexJoin` and asserts the resolution is necessary but
  will not tell you whether the corpus improved.
- **Check the fourth family member** while here: `columnStatsForChild`
  (`selectivity.go:768`) is documented as still missing the `*IndexScan` arm and
  as delegating; re-read the family after this change and confirm the four
  entry points now share one arm list. The comment at `joinkeyproof.go:112-127`
  claims two of three can no longer drift; that claim is now three of four and
  should be re-stated accurately.

### Cut B2 — the `*Append` arm (optional, low value)

Same function, `case *Append:`. Cannot resolve to one base relation in general
(the branches are different relations), so the honest arm resolves only when
every branch agrees, and otherwise declines — which is what it already does.
The real fix is PG's per-branch constant handling. **Recommend deferring**:
PG is 14× over on Q76 itself, so the ceiling on this cut is low.

### Ordering

`B1` first (largest win, smallest diff, independently testable), then `A1`
(largest blast radius, needs its own sweep), then `A2` (inert on the corpora),
then `A3` (unknown size — investigate before committing to it). `B2` deferred.

Each cut lands with: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh`, the TPC-DS SF0.5 gate, `make plan-gate` re-pinned,
and — per §5 — the new estimate-accuracy capture re-run.

---

## 5. The gate

### 5.1 Nothing in this repository would have caught either defect

Confirmed by an exhaustive sweep of the Makefile, `scripts/ralph-precommit-test.sh`,
`.githooks/pre-commit`, and `ci/batch/`:

- `make plan-gate` runs `cmd/plan-snapshot` with `MODE ?= structural`
  (`Makefile:396`), and `planEqual` in structural mode *deletes*
  `(cost=… rows=… width=…)` outright (`cmd/plan-snapshot/main.go:408-434`).
  Two non-default modes (`costs`, `semantic-cost`) would see rows movement;
  nothing invokes them.
- `scripts/pg-plan-parity-diff.py` explicitly strips estimates before comparing
  shapes (`:35`).
- The nightly row anchors (`ci/batch/tpch-row-anchors.csv`,
  `ci/batch/tpcds-row-anchors.csv`) are *result* row counts, not estimates.
- The SF0.5 values gate compares result sets.

So the gates that run compare results and shapes, by construction. A five-order
estimate error is invisible to all of them.

### 5.2 Can `./estimate-audit` serve as the gate? Not as it stands

The instrument is right and the wiring is absent. Precisely:

**What it does right.** `cmd/estimate-audit/main.go` really does run
`EXPLAIN ANALYZE` (`main.go:340-345`) and compare the planner's estimate against
the executor's actual count, with an absolute 10³× tripwire (10² on Q9's final
joinrel) and — better — a *parity ratchet* (`internal/testutil/estimateaudit/parity.go`)
that compares goopg's misestimate ratio against PostgreSQL 18.3's ratio for the
same joinrel, with `DefaultParitySlack = 10.0` and `DefaultParityFloor = 100.0`.
That design is exactly right for this problem, and §2.4 above is the reason: an
absolute bar would have flagged Q47's `rows=1` as a five-order failure when PG
produces the identical 1. The parity ratchet would have passed it and flagged
Q99. `parity.go:5-12` says so in as many words.

**Four reasons it cannot be the gate today.**

1. **TPC-H only.** The query list comes from `internal/testutil/tpch`
   (`main.go:317-322`) and the stats warm-up from `tpch.Tables()`
   (`main.go:363-369`). There is no TPC-DS path — no query corpus, no 65436/65437,
   no connection config. `TODO_ALL.md:728` already asks for "EA ratchet on the
   12 TPC-DS grouping-sets queries", which the tool as written cannot run.
2. **TPC-H would not have caught these anyway.** Defect B1 needs an NLI plan
   under a `LIMIT`, and TPC-H's corpus has no `LIMIT` at all (the C-13a census
   records this as design F2). Defect A1 needs a `BETWEEN` on a nullable
   fact-table column; TPC-H's `l_shipdate` ranges are not null-bearing.
3. **Joinrel granularity.** `Violations` keys on `Rels`, the set of base
   relations beneath a *join* node (`internal/testutil/estimateaudit/audit.go:96-98`,
   `:337-364`). Q28's defect is a **base-relation Seq Scan estimated at 1** —
   not a joinrel, and therefore not a candidate for the tripwire even in
   principle. Any estimate gate for defect (a) must score scan nodes too.
4. **It is not wired to anything, and its baseline is missing.** The only
   invocation in the tree is the manual wrapper
   `scripts/tpch-estimate-audit-arm.sh:132-136`, which refuses to run while CI
   holds the host (`:90-96`); it appears in no Makefile target, no pre-commit
   hook, no precommit script, no nightly stage. Its default pinned PG reference,
   `analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt`
   (`scripts/tpch-estimate-audit-arm.sh:75`), **does not exist in the tree** —
   only the README beside it does — and `replayPlans` calls `fatal` on a read
   error (`main.go:257-267`), so a default-flag ratchet run exits 2 immediately.
   This is already ledgered at `.ralph/deferral_ledger.md:2058`.

**This is itself a finding.** The EA ratchet is cited as the gate for TODO_ALL
items C-05 (`:596`), C-10a (`:728`), C-20a (`:1039`) and C-21 (`:1131`). It is
at present an aspiration with no automated enforcement point and no live
baseline. Four items believe they are gated by something that has never run.

### 5.3 Proposed gate: an estimate-accuracy ratchet over TPC-DS

Build it as a **second corpus for `estimate-audit`**, not as a new tool. The
scoring, the parity ratchet and the report format are already right; what is
missing is a corpus abstraction and a scan-node channel.

1. **Corpus abstraction.** Factor the `tpch.Queries()` / `tpch.Tables()`
   dependency (`main.go:317-322`, `:363-369`) behind an interface with a
   `tpcds` implementation reading
   `bench/tpcds/runtime_goopg/tpcds-data/queries/query*.sql`, defaulting to
   port 65437 and the `data-sf05` cluster. Note the split-on-`;` requirement
   the C-13a census documents (several TPC-DS query files carry more than one
   statement).
2. **Score scan nodes, not only joinrels.** Add a base-relation channel keyed
   on `relname` + alias, with its own threshold. Without it defect (a) is
   invisible. Q28's six scans are the acceptance case.
3. **Ratchet against PG, not against an absolute bar.** Capture the PG side
   once from `bench/tpcds/runtime/pgdata` (:65438, db `tpcds05`) into a
   committed `.pg.plans.txt`, as the TPC-H side already intends to. §2.4 is the
   argument: the absolute bar produces a false positive on Q47/Q57, where PG
   itself answers 1.
4. **Pin a baseline and ratchet it.** A committed per-node
   `(query, node, est, actual, pg_est, pg_actual)` table; the gate fails on any
   node whose goopg/PG ratio-of-ratios regresses past `DefaultParitySlack`.
   The C-13a census's `census.py`
   (`analysis/planner-refactor-take3/c13a-limit-sort-census-20260906/census.py`)
   is a working tree-reconstruction parser and should be the starting point for
   the node walk — it already handles the CTE-body indentation trap.
5. **Where it runs.** Not pre-commit: an `EXPLAIN ANALYZE` sweep of 99 TPC-DS
   queries is a full power run (the C-13a capture was ~800 s of query time on a
   loaded host). It belongs in `ci/batch/stages/` next to `stage-tpcds.sh`,
   nightly, with the report diffed against the pinned baseline by
   `ci/batch/lib/summarize.py` the way the row anchors already are.

**Cost estimate**: the corpus abstraction and the scan channel are the real
work; the ratchet, the report and the thresholds already exist. The nightly
wiring is a copy of `stage-tpcds.sh`'s structure.

**Interim, zero-build option** (worth doing regardless, because it is one line):
the SF0.5 gate already captures plans. Switching `make plan-gate`'s `MODE` to
`semantic-cost` (`Makefile:396`, `cmd/plan-snapshot/main.go:435-464`) makes it
compare the `rows=` capture with a ±10 % tolerance, which would have caught
every cut in §4 as a diff. That is a change-detector, not an accuracy gate — it
tells you an estimate moved, never whether it moved toward the truth — so it is
a complement to the ratchet, not a substitute for it.

---

## 6. Reproducing

Artefacts: `analysis/planner-refactor-take3/rowest-collapse-20260906/`

| file | what |
|---|---|
| `repro-a1-range-nullfrac.sql` | A1, two synthetic tables, no benchmark data |
| `repro-a2-no-histogram.sql` | A2, two synthetic tables |
| `repro-a3-antijoin.sql` | A3, two synthetic tables |
| `repro-b1-groupvars.sql` | B1 control — group-by across hash joins and Append, all correct |
| `sf05-probe-q99-isolate.sql` | the six-step decomposition that isolates B1 to the LIMIT-induced NLI shape |
| `sf05-probe-ab.sql` | the A/B probe: Q28 decomposition + Q99/Q62/Q76/Q22 |
| `probe-patch.diff` | the instrument (NOT the proposed patch) |

Synthetic cuts, on a throwaway cluster — note the data directory must be short,
because a scratchpad-length path overflows the unix control socket's `sun_path`:

```bash
export PATH="$PWD/postgres/local_install/bin:$PATH"
git worktree add /tmp/wt-rowest --detach HEAD
( cd /tmp/wt-rowest && go build -o bin/goopg ./cmd/goopg )
/tmp/wt-rowest/bin/goopg init -D /tmp/rowest-5533 --no-sync
GOMEMLIMIT=8GiB GOOPG_CG_UNIT=goopg-rowest scripts/goopg-test-run.sh \
    /tmp/wt-rowest/bin/goopg start -D /tmp/rowest-5533 --listen 127.0.0.1:5533 &
psql -h 127.0.0.1 -p 5533 -U postgres -d postgres \
    -f analysis/planner-refactor-take3/rowest-collapse-20260906/repro-a1-range-nullfrac.sql
/tmp/wt-rowest/bin/goopg stop -D /tmp/rowest-5533
```

The corpus cuts, on the SF0.5 cluster, under the lock:

```bash
flock -w 900 /tmp/goopg-65437.lock bash -c '
  GOMEMLIMIT=12GiB GOGC=100 GOOPG_CG_UNIT=goopg-rowest-p4 scripts/goopg-test-run.sh \
      /tmp/wt-rowest/bin/goopg start -D bench/tpcds/runtime_goopg/data-sf05 \
      --listen 127.0.0.1:65437 --hba bench/tpcds/runtime_goopg/data-sf05/pg_hba.conf &
  until pg_isready -h 127.0.0.1 -p 65437 -U postgres; do sleep 1; done
  psql -h 127.0.0.1 -p 65437 -U postgres -d postgres \
      -f analysis/planner-refactor-take3/rowest-collapse-20260906/sf05-probe-ab.sql
  /tmp/wt-rowest/bin/goopg stop -D bench/tpcds/runtime_goopg/data-sf05
'
```

The private copy of `store_sales` used in §1 and §2.1 is
`\copy store_sales FROM 'bench/tpcds/runtime_goopg/tpcds-data-sf05/store_sales.tsv'
WITH (FORMAT text, NULL '\N')` into the schema from
`third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools/tpcds.sql:561`.
It loads 1 439 608 rows in 23 s and reproduces the census's Q28 actuals exactly
(15 410 rows for the B1 arm), which is how the reproducer was validated against
the census before any estimate was read.
