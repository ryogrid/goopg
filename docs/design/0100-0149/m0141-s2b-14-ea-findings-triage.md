# M0141-S2b-14 — triage: the 13 NEW ea-ratchet findings from S2b-13's plan churn

`Parent: M0141-S2b-13`, `Kind: recon` — no production change.

Status: closed 2026-09-19 — recon complete; `Movement: none`.

## Verdict

**All 13 NEW findings are relset-key churn of PG-faithful estimates. No
estimator defect is isolated; no fix task is filed.** The ratchet keys
findings by `(query, node-type, relset)`; S2b-13's comparator restore
changed which plans survive, so the same pre-existing estimates surface
under different join decompositions. Every underlying misestimate was
reproduced by PostgreSQL 18.3's own estimator on the identical relset
and predicate set (read-only `EXPLAIN` on `:65438/tpcds025`).

## Method

For each flagged relset, extracted the query's full predicate set from
`bench/tpcds/runtime_goopg/tpcds-data/queries/query<N>.sql` and ran the
same two-table (or three-table) join through `EXPLAIN` on BOTH engines:
PG reference `:65438`/`tpcds025` (user `ryo`, read-only) and goopg
`:65437`/`postgres`. Plan context taken from the SF0.25 sweep captures
`bench/tpcds/runtime_goopg/tpcds-results-sf025/plans-20260918-220818.txt`
(new) vs `plans-20260918-195631.txt` (old) and `bench/tpcds/plans-pg/`.

## Per-finding classification

### Class A — PG-faithful `store_sales ⋈ date_dim` under-estimate, re-keyed (9)

| finding | goopg est | PG's own est for the same relset | actual | verdict |
|---|---|---|---|---|
| Q61 Gather `customer+date_dim+promotion+store_sales` | 140 | (PHJ `ss⋈dd` 94 vs PG **97**) | 11644 | churn |
| Q61 Gather `date_dim+store_sales` | 291 | same PHJ basis | 22849 | churn |
| Q68 Gather `date_dim+store_sales` | 660 | (PHJ `ss⋈dd` 213 vs PG **219**, full preds `d_dom BETWEEN 1 AND 2 AND d_year IN(3)`) | 25820 | churn |
| Q68 HJ `date_dim+store+store_sales` | 630 | store join sel 0.0796 = `630/(660×12)` — same sel as the minimal repro below | 25215 | churn |
| Q89 Gather `date_dim+store_sales` | 3416 | (PHJ `ss⋈dd` 1102 vs PG **1107**) | 136830 | churn |
| Q7 Gather `cd+date_dim+item+store_sales` | 46 | (PHJ `ss⋈dd` 1102 vs PG **1107**; PG's NL chain also ests 15) | 1944 | churn |
| Q13 Gather `ca+cd+date_dim+store_sales` | 118 | (PHJ `ss⋈dd` 1102 ≈ PG 1107) | 1373 | churn |
| Q61 HJ `date_dim+store+store_sales` | 23 | pg_est 8 — see Class C | 0 | artifact |
| Q85 ×4 (see Class B) | | | | churn |

The ~40–120x under-estimates are *shared* PG-formula errors (both
engines estimate ~100–3300 for relsets that actually produce
12K–137K rows). The ratchet flags them only because PG's chosen plan
decomposes the join order differently and never materializes the relset
(`pg_est=None` → absolute `qerr>10` bar). First-pass concern that Q68
showed a 15x goopg-side divergence (213 vs PG 3318) dissolved when the
full predicate set was used — `d_dom BETWEEN 1 AND 2` had been omitted;
with it, PG estimates **219** vs goopg's **213**.

### Class B — PG-faithful `web_sales ⋈ web_returns` under-estimate (Q85 ×4)

goopg's PHJ `ws⋈wr` on `(ws_item_sk, ws_order_number) = (wr_item_sk,
wr_order_number)` estimates **6**; PG's own estimator on the identical
relset estimates **5** (synthetic EXPLAIN). Actual ~17094 — a ~3400x
under-estimate PG's formula shares. The reason-early join order that
surfaces these relsets matches PG's own Q85 decomposition (`Hash Join`
on `wr_reason_sk = r_reason_sk` immediately after the web_returns
scan). The 4 flagged nodes are the same error under four relset keys:
`reason+web_returns+web_sales` (Gather, 12/17094),
`date_dim+reason+web_returns+web_sales` (1/3389),
`date_dim+reason+web_page+web_returns+web_sales` (1/3388),
`customer_demographics+date_dim+reason+web_page+web_returns+web_sales`
(1/40).

### Class C — reporting-convention artifact, not an estimate divergence (1)

Q61 `date_dim+store+store_sales` HJ: goopg est **23** vs `pg_est` **8**
(actual 0). 23 ≈ 8 × 3.1 — the parallel divisor. Verified via a minimal
`store_sales ⋈ store ON ss_store_sk = s_store_sk` repro on both
engines:

- goopg: `Parallel Seq Scan on store_sales rows=719876` (**total**),
  `Hash Join rows=688033`; actual count **687463** — essentially exact.
- PG: `Parallel Seq Scan on store_sales rows=232218` (**per-worker**,
  719876/3.1), `Hash Join rows=222047` per-worker ≈ 688K total — also
  essentially exact.

goopg's EXPLAIN stamps **total** rows on partial-path nodes; PG stamps
**per-worker** rows (cost_seqscan divides by `parallel_divisor`).
The scorer compares goopg-total vs PG-per-worker on matched relsets,
which reads a ~3.1x "worse" goopg estimate on any partial-path node —
the underlying selectivities are identical. Recorded here as a scorer
caveat; the production-side convention difference is the same class
M0141-S2b-15 covers (gather row stamp), and its scope note now names
the partial-path `rows=` display convention generally.

### Class D — node-type rename (1)

Q54 `GroupAggregate cte:my_customers+…` est 22/act 0 — the intended
S2b-13 HashAggregate→GroupAggregate flip at identical estimate and
actual; the rename moved the node toward PG's own plan (which is a
GroupAggregate there).

## Consequences

- The ea-ratchet re-pin to 76 (S2b-13) stands: every NEW finding is a
  re-keyed PG-faithful error, so the new baseline is the honest floor.
- No estimator defect isolated → no fix task filed. The M0142-0013
  precedent (recon the NEW findings, fix only what isolates a defect)
  is satisfied.
- The `date_dim ⋈ store_sales` ~40–120x under-estimate family remains a
  *shared-with-PG* estimator weakness (both engines wrong identically —
  `qerr > 10` but `pg_qerr` equally high when the relset exists in PG's
  plan). Matches the "PG also wrong" ratchet class; out of scope for
  parity work.
- Caveat recorded for the ea scorer: `pg_est` on partial-path nodes is
  per-worker while goopg's `est` is total — matched-relset comparisons
  there read ~3.1x high on the goopg side. Folded into S2b-15's scope
  note (production-side fix, not scorer-side).
