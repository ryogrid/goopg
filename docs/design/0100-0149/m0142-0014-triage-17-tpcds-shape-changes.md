# M0142-0014 — triage the 17 TPC-DS plan-shape changes M0142-0012-verify found

Status: accepted (landed 2026-09-15)

## Task

M0142-0012-verify's self-diff (goopg-before-M0142-0012 vs goopg-after,
cost-blind) found 17 TPC-DS queries whose plan shape moved (Q4, Q6, Q11, Q25,
Q29, Q31, Q34, Q45, Q54, Q56, Q60, Q64, Q72, Q73, Q78, Q79, Q88), with the
milestone-group headline metric unmoved (match still 2/99, Q9/Q41) and only
`join-order` moving in the aggregate category counts (90→91). That flat
aggregate could be masking a real regression on one query cancelled by a real
improvement on another (K50: "aggregate hides per-query movement" — the same
failure mode M0142-0001 caught on Q9/Q96 by reading them individually). This
task is a **recon: no production code changed.**

## Method

For each of the 17 queries, extracted its `pg-plan-parity-diff.py`
`SHAPE-DIFF`/`MISSING-NODE` category-tag list **against live PG 18.3** from
two already-committed diffs (no re-capture):

```bash
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0001-tpcds-goopg.txt      analysis/m0142/m0142-0001-tpcds-pg.txt      # pre-M0142-0012
python3 scripts/pg-plan-parity-diff.py analysis/m0142/m0142-0012verify-tpcds-goopg.txt analysis/m0142/m0142-0012verify-tpcds-pg.txt # post-M0142-0012
```

then diffed each query's pre/post category-tag *set* (not the cost numbers,
which move on nearly every query regardless of shape — the same reason
M0142-0012-verify needed a self-diff rather than a byte diff). A query is:

- **(a) neutral/improved** if its post-category set is a subset of (or equal
  to) its pre-category set — it never diverges from PG on a dimension it
  used to agree on.
- **(b) candidate regression** if its post-category set gained a category not
  present pre-fix — a dimension where goopg's plan used to structurally agree
  with PG's and, after M0142-0012, no longer does.

## Result: 17 = 13 neutral + 3 improved + 1 regression

| query | pre categories (vs PG) | post categories (vs PG) | delta | class |
|---|---|---|---|---|
| Q4  | join-order,join-method,scan-type,parameterisation,sort-strategy,parallelism,qual-placement (MISSING-NODE) | identical | none | neutral |
| Q6  | join-order,join-method,parameterisation,aggregation-strategy,sort-strategy,parallelism | identical | none | neutral |
| Q11 | join-order,join-method,scan-type,parameterisation,sort-strategy,parallelism,qual-placement (MISSING-NODE) | identical | none | neutral |
| Q25 | join-order,sort-strategy,parallelism | identical | none | neutral |
| Q29 | join-order,sort-strategy,parallelism | identical | none | neutral |
| **Q31** | join-order,join-method,scan-type,parameterisation,**aggregation-strategy,sort-strategy,parallelism**,rendering | join-order,join-method,scan-type,parameterisation,rendering | **-3** | **improved** |
| Q34 | join-order,join-method,scan-type,parameterisation,aggregation-strategy,sort-strategy,parallelism (MISSING-NODE) | identical | none | neutral |
| **Q45** | parameterisation,sort-strategy,parallelism | **join-order,scan-type**,parameterisation,sort-strategy,parallelism,**qual-placement** | **+3** | **REGRESSION** |
| **Q54** | join-order,**join-method**,scan-type,aggregation-strategy,sort-strategy,**parallelism** (MISSING-NODE) | join-order,scan-type,aggregation-strategy,sort-strategy | **-2** | **improved** |
| Q56 | join-order,join-method,scan-type,parameterisation,aggregation-strategy,sort-strategy,parallelism | identical | none | neutral |
| Q60 | join-order,join-method,scan-type,parameterisation,aggregation-strategy,sort-strategy,parallelism (MISSING-NODE) | identical | none | neutral |
| Q64 | join-order,join-method,scan-type,aggregation-strategy,sort-strategy,parallelism,rendering (MISSING-NODE) | identical | none | neutral |
| **Q72** | join-order,join-method,**scan-type**,parameterisation,aggregation-strategy,sort-strategy,parallelism,**qual-placement** | join-order,join-method,parameterisation,aggregation-strategy,sort-strategy,parallelism | **-2** | **improved** |
| Q73 | join-order,join-method,scan-type,parameterisation,aggregation-strategy,sort-strategy,parallelism,rendering | identical | none | neutral |
| Q78 | join-order,join-method,scan-type,parameterisation,aggregation-strategy,sort-strategy,parallelism (MISSING-NODE) | identical | none | neutral |
| Q79 | join-order,join-method,scan-type,aggregation-strategy,sort-strategy,parallelism,rendering | identical | none | neutral |
| Q88 | join-order,join-method,aggregation-strategy,parallelism | identical | none | neutral |

**This exactly and fully attributes the aggregate category deltas
M0142-0012-verify measured** (a consistency check the per-query read gives
for free): `join-order` 90→91 (**+1, solely Q45**), `join-method` 69→68
(**-1, solely Q54**), `scan-type` 61→61 (Q45 +1 cancels Q72 -1),
`aggregation-strategy` 70→69 (**-1, solely Q31**), `sort-strategy` 76→75
(**-1, solely Q31**), `parallelism` 85→83 (**-2, Q31 and Q54 one each**),
`qual-placement` 21→21 (Q45 +1 cancels Q72 -1), `parameterisation`/`rendering`
unchanged (no query's tag set touched them). No residual is unaccounted for —
the "moved sideways" aggregate is a **near-exact wash of one real regression
(Q45) against three real improvements (Q31, Q54, Q72)**, not an artefact of
the category taxonomy.

## Q45 confirmed as a genuine regression, not a diff-tool quirk

Q45 is the only query whose category set *grew*, and it is the sole source of
the `join-order` count regression the milestone-group banner already flagged
by number (90→91). Read the raw EXPLAIN text (`analysis/m0142/m0142-0001-tpcds-goopg.txt`
vs `m0142-0012verify-tpcds-goopg.txt`, both `=== Q45` blocks) to confirm this
is a real structural change, not a cosmetic rendering difference:

- **Pre-M0142-0012** (`m0142-0001`): `... ⋈ customer ⋈ customer_address ⋈
  item` — `customer_address` joins in before `item`, `item` is the outermost
  probe.
- **Post-M0142-0012** (`m0142-0012verify`): `... ⋈ customer ⋈ item ⋈
  customer_address` — `item` and `customer_address` swapped; `customer_address`
  is now the outermost probe.
- **Real PG 18.3** (`analysis/m0142/m0142-0001-tpcds-pg.txt`, `=== Q45`):
  `... ⋈ customer ⋈ customer_address ⋈ item` — matches the **pre**-M0142-0012
  goopg order exactly (same join sequence; PG additionally moves `Sort`
  outside the `Gather` where goopg keeps `Gather Merge`, which is why Q45 was
  already `SHAPE-DIFF` pre-fix on `parameterisation`/`sort-strategy`/
  `parallelism` — that part is unchanged and out of scope here).

So M0142-0012's cardinality fix flipped the DP search's chosen order for the
`item`/`customer_address` pair on Q45 specifically, moving it away from an
order that used to agree with PG's own choice. This is consistent with how
M0142-0012 works: it makes the decomposed-NLI `Join{Lateral:true}` shape's
cost estimate for an index-probe nested loop materially more accurate for
`item_pkey`/`customer_address_pkey` (both indexed lookups in this query), and
a more accurate cost for one candidate in a close DP race can legitimately
flip which candidate wins — the same class of effect M0142-0003b/0003c
documented for Q9's level-6 tie. It is a real, singular, attributable
regression, not a false positive of the classification method.

## Verdict and follow-up

- **13/17 neutral** (cost numbers moved, node/join shape did not — same verdict
  class M0142-0012-verify already gave TPC-H's 0/21 shape changes).
- **3/17 improved** (Q31, Q54, Q72 — each lost 2-3 divergent categories,
  moving closer to PG's shape; not investigated further, no code implicated).
- **1/17 regressed** (Q45 — gained 3 divergent categories including
  `join-order`, confirmed by raw-plan read to be the `item`/`customer_address`
  DP-search tie flipping away from PG's own choice).

Net: M0142-0012 is confirmed a **correct, verified fix** (per its own gates)
whose corpus-wide effect is dominated by improvement (3 queries) with one
genuine, now-attributed regression (Q45) — not a mystery any more. Filed as
**M0142-0015**: investigate why the `item`/`customer_address` DP-search race
on Q45 flips post-fix (is the *new* estimate more accurate than PG's own, in
which case Q45's real defect is elsewhere in the cost model rather than in
M0142-0012; or does M0142-0012 introduce a new inaccuracy for this specific
pair) — same class of question M0142-0003c already opened for Q9's level-6
tie, not yet run for Q45. One `.ralph/deferral_ledger.md` row records this.

## What was not done (scope boundary)

- Q45's regression was **triaged and attributed**, not fixed or reverted —
  M0142-0012 remains landed (it is a net corpus improvement per this triage,
  and Q45's pre-fix state was itself already `SHAPE-DIFF`, not a match, so
  nothing regressed from `match` to `shapediff`).
- Q31/Q54/Q72's improvements were not traced to their cost-model mechanism
  (out of scope for a classification task; no further action filed since they
  are wins, not defects).
- No new capture was run; all evidence comes from `analysis/m0142/m0142-0001-*`
  and `m0142-0012verify-*`, already committed by M0142-0001 and
  M0142-0012-verify.

## Verification

- No production Go code touched — pure recon; `go build ./...` unaffected.
- The per-query category-delta table's sum reproduces `pg-plan-parity-diff.py`'s
  own `CATEGORIES` line deltas exactly, cross-checking the manual per-query
  read against the tool's own aggregate (see table note above) — the classification
  method is self-consistent, not just plausible.
