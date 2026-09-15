# M0142-0010 — recon: the `date_dim+store+store_sales` join-level gap is PG's own default eqjoinsel, not a goopg cardinality bug

Status: accepted (recon closed 2026-09-15, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0010 line, filed by M0142-0009: `date_dim+store+
store_sales` estimates ~40x under actual in both Q34 (`Gather`, est=2105 vs
actual=91450, qerr 43.4) and Q73 (`Hash Join`, est=630 vs actual=26312, qerr
41.8), newly visible in `make ea-ratchet` once M0142-0009's leaf-level
`date_dim` filter fix stopped drowning it out under a qerr in the thousands.
The task's own resume point: instrument `estimateJoin`/`estimateNLIndexJoin`
on this relset the way M0142-0009 instrumented `rangeOpSelectivityStats` —
establish the mechanism (measured-branch `pairNDistinct`/superkey selectivity
vs an unmeasured fallback) before proposing a fix, per the practice card's
standing warning against guessing a join-estimation mechanism from a plan-node
label alone.

## Method — instrument before theorising

Cloned `bench/tpcds/runtime_goopg/data-sf025` to a private port (5533,
`tmp/m0142-0010/`, per `goopg_shared_bench_cluster_collisions`) and added a
temporary `GOOPG_JOIN_TRACE=1`-gated `fmt.Fprintf(os.Stderr, …)` instrument
inside `estimateJoin`'s hash/merge branch (`internal/optimizer/cardinality.go`),
printing each equi-pair's chosen mechanism (`eqjoinselInnerMCV` hit /
`pairNDistinct` hit / unmeasurable default) and the resulting `sel`/`rows`.
Reverted after the recon — this task lands no production diff.

`EXPLAIN` on Q34 (own SQL, `bench/tpcds/runtime_goopg/tpcds-data/queries/
query34.sql`) against the trace-enabled clone reproduces the witness node
exactly: a `Gather` (`est=2111`, matching the filed `2105` within capture-run
noise) wrapping `Parallel Hash Join Hash Cond: (ss_store_sk = store.s_store_sk)`
over `Parallel Hash Join Hash Cond: (ss_sold_date_sk = date_dim.d_date_sk)`
between the FULL `store_sales` (`l=719876`) and the FILTERED `date_dim`
(`r=235`, itself accurate — matches PG's own `d_dom`/`d_year` filter estimate
of `rows=235` in the committed `bench/tpcds/plans-pg/Q34.txt` fixture, so
M0142-0009's fix is not implicated here). `EXPLAIN ANALYZE` confirms the
actual: `93640` rows survive the `store_sales`⋈`date_dim` join (summed across
4 parallel workers), `91450` after adding `store` — the `qerr ~43` the task
was filed against.

The trace pinpointed the exact arithmetic:

```
JOINTRACE *optimizer.Project x *optimizer.Project: l=719876 r=235 ... pairs=1
JOINTRACE   pair 0: nd nd=73049 nullSel=0.9543666653335094 -> sel=1.3064746476112054e-05
JOINTRACE   residualSel=1 rawRows=2210.1743970458456 final=2210
```

`pairNDistinct` returned `nd=73049` — `date_dim`'s own row count (it is a
primary key, so `ndistinct(d_date_sk) == reltuples`) — and the join estimate
computed is `l * r * (nullSel / nd)`, i.e. `store_sales_rows * (filtered_
date_dim_rows / date_dim_total_rows)`, the standard FK→PK "assume uniform
distribution over the referenced domain" formula.

## Is 73049 the right divisor? Checked against goopg's OWN measured stats, not assumed

`select count(distinct ss_sold_date_sk), count(*) from store_sales` on the
same clone: **1823** distinct sold-date keys in **719876** rows — `store_sales`
only ever references ~5 years of dates. `select min(d_date_sk), max(d_date_sk),
min(d_date), max(d_date) from date_dim`: the dimension table spans
**1900-01-02 to 2100-01-01** (73049 rows — 200 years, a scheduling-table span
far wider than any fact table's actual activity window). So `nd(date_dim.
d_date_sk) = 73049 ≫ nd(store_sales.ss_sold_date_sk) = 1823`: the FK column's
own value range covers only ~2.5% of the PK domain the join is being priced
against.

## Verdict: this is PG's own textbook formula, verified bit-for-bit against the real oracle, not a goopg defect

Read `eqjoinsel_inner` (`postgres/src/backend/utils/adt/selfuncs.c:2445`).
Two branches:

- **Both sides have usable MCV lists**: run the MCV lists against each other
  for an exact overlap-based estimate — far more accurate than the default.
- **Otherwise** (the comment at `:2601`, verbatim): `selec = MIN(1/nd1, 1/nd2)
  * (1-nullfrac1) * (1-nullfrac2)` — i.e. `1/MAX(nd1, nd2)`, "since the larger
  nd is determining the MIN" (own comment). This is a **conservative upper
  bound**, explicitly derived assuming uniform distribution over each column's
  full stats domain — the same assumption goopg's fallback makes.

Queried the real PG 18.3 oracle's `pg_stats` on the identically-generated
data (`:65438`, db `tpcds025`) to check which branch actually fires:

```
 attname         | n_distinct | mcv_n | null_frac
 ss_sold_date_sk |       1822 |   100 | 0.0433
 d_date_sk       |         -1 |  NULL | 0
```

`store_sales.ss_sold_date_sk` **does** carry a 100-entry MCV list (`n_distinct
1822`, matching goopg's own count almost exactly — 1823 vs 1822, sampling
noise). `date_dim.d_date_sk` carries **none** (`n_distinct = -1`, PG's
"fully-unique column" marker — ANALYZE never records an MCV for a primary
key, the identical decision M0142-0009 found for `d_moy`'s histogram, applied
here to MCVs instead). `have_mcvs1 && have_mcvs2` is therefore **false** —
the exact-overlap branch cannot fire because ONE side is a bare unique key —
so PG's own planner, if it costed this exact hash-join shape, would fall to
the identical no-MCV default: `MIN(1/1822, 1/73049) * (1-0.0433) * 1 =
1/73049 * 0.9567 = 1.31e-5` — **the same number goopg's trace printed**
(`1.3065e-5`, rounding-level agreement). Multiplying by `l * r` reproduces
goopg's `rows=2210` exactly.

**goopg's `estimateJoin` fallback branch is PG-formula-identical for this
relset, verified against the real oracle's own `pg_stats`, not merely
plausible-looking.** There is no cardinality-math defect here to fix — B2's
absorption principle is already satisfied; there is nothing left to absorb.

## So why is real PG's actual EXPLAIN accurate here?

Real PG never builds this join shape. `bench/tpcds/plans-pg/Q34.txt` (the
committed fixture) shows PG choosing `Nested Loop` + `Memoize` +
`Index Scan using date_dim_pkey` for `store_sales`⋈`date_dim`, keyed
per-outer-row on `ss_sold_date_sk`, filtered by the `d_dom`/`d_year` predicate
inside the index probe (`rows=1` per probe, `Memoize` caching repeats).
That shape doesn't route through `eqjoinsel` at all for its cardinality — it
prices each outer row's index probe directly against the actual matching
keys, which is precise for exactly this correlated-FK pattern (a probe either
hits the specific date or it doesn't; there is no "assume uniform over 73049
possible dates" step). PG picks that shape because, with only ~47 rows
estimated for the whole 3-way join once `household_demographics` and `store`
compose (`bench/tpcds/plans-pg/Q34.txt`'s own numbers), a nested loop is far
cheaper than materialising and hashing the 720K-row `store_sales` table.

goopg cannot choose that shape today: **M0142-0005** ("break the Memoize /
probe-multiplier interlock") is the filed, already-diagnosed reason —
goopg's executor has no Memoize on the NL probe path (`nl_index_join.go:127`),
so pricing an NL+IndexScan probe PG-faithfully sends other queries (Q72) into
320s timeouts, and `indexProbeCostMultiplier=2.0` was calibrated specifically
to keep the DP away from NL-index plans until that gap closes. Forced into a
hash join by that same standing limitation, goopg's cost model then correctly
applies PG's own (imprecise-for-this-shape) default formula — and inherits
PG's own known weak spot along with it.

**Conclusion: M0142-0010's finding is not a new mechanism. It is a second,
independently-verified symptom of the already-filed M0142-0005 gap** — not a
join-cardinality-formula defect to fix in `cardinality.go`. No code change
lands from this task.

## Why this is not "reject and move on" without a resume point

Per the deferral-ledger discipline, this recon materially strengthens
M0142-0005's own case (previously scoped purely as a cost/timeout tradeoff,
`Q72 4s -> 320s`): it is now also the load-bearing blocker for this specific
`ea-ratchet` witness pair (Q34/Q73), and likely for every other `date_dim`
join in the corpus keyed on a similarly wide-domain dimension table with a
narrow-domain fact-table FK (a very common TPC-DS shape — `date_dim` is the
join target of nearly every query). A ledger row records this so a future
M0142-0005 pass has the connection in hand rather than rediscovering it.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed (instrumentation added and reverted in the same
loop), so the mandatory plan-parity/ea-ratchet floors are unchanged from
M0142-0009's post-fix pin by construction: TPC-H `match=8/22`, TPC-DS
`match=2/99`, `make ea-ratchet` baseline stays the 112-entry set M0142-0009
pinned (this task neither fixes nor regresses any finding in it — Q34/Q73's
two `date_dim+store+store_sales` findings remain open, now with a diagnosed
and cross-referenced cause). `go build ./...` clean (no diff to build).
Throwaway server/clone (port 5533, `tmp/m0142-0010/`) stopped and removed
before commit.

## Follow-up

No new milestone task filed — the resume point is **M0142-0005** itself
(`nl_index_join.go:127`, `joinpathsnli.go:313,498`, `cost_funcs.go:1053`),
already on the fix_plan at its existing priority. This task's evidence is
folded into M0142-0010's fix_plan entry and this doc for the loop that picks
M0142-0005 up.
