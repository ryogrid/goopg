# R30 — ANALYZE must sort its sample into physical order

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Statistics axis. Found while sizing R29's follow-up (K33).*

## 0. How this round was chosen

R29 isolated one bitmap-vs-index costing divergence (K33). Before
designing around a single query, the scan-type category was censused
across all 99 TPC-DS queries by comparing per-table scan kinds against
the PG fixtures:

```
 119  goopg=Seq Scan          pg=Index Scan     <-- dominant
  17  goopg=Index Scan        pg=Seq Scan
  17  goopg=Seq Scan          pg=Index Only Scan
  12  goopg=Bitmap Heap Scan  pg=Index Scan     <-- K33, the query-sized one
```

K33 is 12 occurrences; "goopg seq-scans where PG index-scans" is 119.
The round follows the census, not the anecdote.

## 1. Root cause

`costindex.go` already implements PG's `cost_index` interpolation and
says what happens without the input: "A goopg index whose leading
column has no correlation statistic prices at `max_IO_cost`
(indexCorrelationFor returns 0 when stats are missing)". Every index
scan priced at the uncorrelated worst case is exactly the 119-row
pattern.

Measured on the TPC-DS SF0.5 clone, `customer.c_customer_sk` — the
primary key, physically ordered:

| | goopg | PG 18.3 |
|---|---|---|
| `c_customer_sk` correlation | **0.0737** | **0.999908** |

ANALYZE *does* compute correlation, and its formula is already PG's
identity from `compute_scalar_stats` (both x and y are {0..n-1}, so the
coefficient reduces to `(n*Σxy − Σx²)/(n*Σx² − Σx²)`). The statistic
also persists across sessions. What is wrong is the **input**:

`computeColumnStats` uses each sampled row's index in the reservoir as
its physical position. But the sampler is Algorithm R — past the cap it
replaces a **uniformly chosen slot** (`reservoir[keep] = row`), so the
reservoir's index order stops tracking physical order, and the
correlation of a perfectly-ordered column collapses toward 0.

Upstream has the identical problem and fixes it after sampling
(`analyze.c:1312-1322`):

```c
/* Otherwise we need to sort the collected tuples by position
 * (itempointer). */
if (numrows == targrows)
    qsort_interruptible(rows, numrows, sizeof(HeapTuple), compare_rows, NULL);
```

`compare_rows` (analyze.c:1361) orders by block then offset. goopg
never did this.

## 2. The change

Carry each reservoir entry's `(block, offset)` alongside the row, and
sort the reservoir by it before `computeColumnStats` runs — `compare_rows`,
transliterated. Upstream's guard is kept verbatim (`numrows == targrows`):
a relation that never filled the reservoir is already in physical order
and takes exactly its previous path.

## 3. Gates and prediction

Suites; TPC-H values digest byte-identical; SF0.5 sweep all-zero;
parity on BOTH corpora under a fresh-ANALYZE A/B with the seed pinned
(`GOOPG_ANALYZE_SEED`, which the lane launcher already sets).

Prediction: scan-type improves. Everything else is a genuine unknown —
correcting a cost-model INPUT moves whatever that input feeds, and a
downstream category may worsen. Per the goal's own terms ("plans must
become identical as a result of the same statistics, the same cost
computation, and the same planning logic") a correct statistic is a
prerequisite, so a downstream category regressing is a finding to
report, not a reason to restore a wrong statistic — that would be
preserving a compensating error.

## 4. Measurement protocol (learned in this round)

A statistics change cannot be A/B'd against stored stats: BOTH sides
must be re-ANALYZEd under the same pinned seed, or "stale vs fresh
sample" is confounded with the change. Measured here: re-ANALYZing
alone moved TPC-DS join-method 74 -> 77 with the binary held fixed.

An A/A under this protocol was run to establish the floor: category
counts identical, plans differing only in cost/row ESTIMATE digits on
5 queries (3 of them the known dsqgen artefacts Q36/Q70/Q86), which the
parity tool normalises away. Shapes are stable, so a category-count
delta of 1-2 is signal here, not noise.
