# M0146-0009c — per-level decomposition of the 5 NEW ea-ratchet findings

Recon task filed by M0146-0009b (2026-09-28). The five NEW findings were
unmasked when the corrected ea-ratchet run (real clone `tmp/c20a/data-sf025`
on a private :5541 server instead of the stale empty clone + foreign :5534
PostgreSQL that caused the earlier vacuous PASS) scored 228 nodes on the
SF0.25 corpus. All five sit on plans **byte-identical** baseline-vs-candidate
— pre-existing drift, not M0146-0009b fallout.

Method: the M0146-0009a recipe — capture goopg `EXPLAIN ANALYZE` on the
private clone, capture PG 18.3 `EXPLAIN ANALYZE` on the reference oracle
(`:65438`/`tpcds025`), then standalone `EXPLAIN` probes of the flagged relsets
with the queries' complete predicate sets on BOTH engines.

Evidence files (all under `tmp/m0146-0009c/`):

| file | content |
|---|---|
| `q85-goopg-ea.txt` / `q85-pg-ea.txt` | full EXPLAIN ANALYZE, both engines |
| `q78-goopg-ea.txt` / `q78-pg-ea.txt` | full EXPLAIN ANALYZE, both engines |
| `q83-goopg-ea.txt` / `q83-pg-ea.txt` | full EXPLAIN ANALYZE, both engines |
| `probe85.sql` + `probe85-{goopg,pg}.txt` | standalone `ws ⋈ wr` with full Q85 predicates |
| `probe83.sql` + `probe83-{goopg,pg}.txt` | standalone `sr_items ⋈ cr_items` (Q83's two CTE legs) |
| `server-trace.log` | goopg server stderr under `GOOPG_PGSHAPED_DP_TRACE=1` (Q85 enum trace: 6602 DPTRACE / 17081 DPPATH lines) |

## Finding (a) — Q85, 3 relsets: PG-shared underestimate, no goopg defect

Findings: `reason+web_returns+web_sales` (Gather Merge est=1 vs ~596),
`reason+web_page+web_returns+web_sales` (NL est=1 vs ~596),
`customer_demographics+reason+web_page+web_returns+web_sales` (NL est=1 vs
~580). All `pg_est=null`.

Decomposition (goopg plan, `q85-goopg-ea.txt`): every finding cascades from
one node — `Hash Join {web_sales ⋈ web_returns} rows=1` (actual ~626 into the
NL). The joins above it (reason, web_page, cd2, cd1, ca, dd) each contribute
a pkey probe (rows=1 per probe) or a 1-row dimension join, so rows=1
propagates unchanged to the top. The first collapse is the `ws ⋈ wr`
join selectivity on `(ws_item_sk, ws_order_number) = (wr_item_sk,
wr_order_number)` — two at-implementation "independent" equality columns
that are in reality the same (order,lineitem) identity: sales↔returns is
a natural-key correlation, exactly the class the independence assumption
cannot see.

PG oracle, same relset standalone (`probe85-{goopg,pg}.txt`): **both engines
estimate rows=1** for `ws ⋈ wr` under the full Q85 predicate set. PG's own
in-corpus plan also lands at rows=1 end to end — it elects
`(ca ⋈ wr) ⋈ reason` (HJ rows=2780) then a `web_sales` pkey probe NL
(rows=1), i.e. PG estimates the same logical result at ~1 via a different
tree. Actual is ~596 either way.

The findings fire because goopg's elected tree materializes `{ws,wr}`-rooted
relsets PG's probe-order tree never builds (`pg_est=null` → scored alone).
DP trace (`server-trace.log`) confirms the PG-order candidates exist in
goopg's search space: `{reason} | {web_returns+web_sales}` created=1, and
`{customer_address+web_returns+web_sales}` exists with `npaths=6
cheapest=nli` — the parameterized-probe candidate is present and costed;
the elected tree won by epsilon (7621.6 nli subtree vs elected total 7643.1).
Per `joinsearchlevel.go` `s.trace.offer(...)` semantics: `created=0` means
the pair was *not* the first to create that joinrel (relset already existed),
NOT that no path was produced — paths still flow into the existing joinrel.

**Classification: relset-key churn of a PG-faithful estimate** — the exact
disposition M0141-S2b-14 reached for the same class (it measured `ws ⋈ wr`
6 vs PG 5 standalone; the residual since is the estimator's statless
clamps, not a divergence). The join-order difference is a cost-tie residual;
a separate order-parity task would be needed to make goopg's elected tree
PG's, but nothing here is an estimator defect. **No fix filed.**

## Finding (b) — Q78: `dd+sr+ss+wr+ws` Merge Left Join est 1,407 vs actual 123,049 — PG-shared

goopg (`q78-goopg-ea.txt:11`): `Merge Left Join rows=1407 actual=123049`
joining the two grouped legs — store leg `HashAggregate rows=281532` over
`{store_sales ⋈ date_dim ⋈ ANTI store_returns}` and web leg `HashAggregate
rows=94383` over `{web_sales ⋈ date_dim ⋈ ANTI web_returns}` — on
`(ss_customer, ss_item, d_year) = (ws_bill_customer, ws_item, d_year)`.

PG's semantically-equivalent node (`q78-pg-ea.txt:14`): `Merge Left Join
rows=1371 actual=123049` — **1.9% from goopg's 1,407 and identically ~90x
under actual**. Both legs' customers-vs-channels correlation is invisible to
the per-key ndistinct product; `calc_joinrel_size_estimate`'s left-join arm
applies `join_size_lo = join rows OR left rows` and both engines land at
~1.4k. (Leg-estimate divergence noted: goopg's store-leg group estimate is
281,532 vs PG's 1,371 vs actual 123,049 — goopg 2.3x over, PG 90x under; the
flagged node itself is faithful, legs are a different scoring surface.)

**Classification: PG-shared correlation underestimate at a semantically
identical node.** No goopg defect. **No fix filed.**

## Finding (c) — Q83: `cr+dd+item+sr` Merge Join est=1 vs actual 22 — REAL estimator gap, fix filed

Q83 joins two FROM-clause CTEs that each `GROUP BY i_item_id`:
`sr_items ⋈ cr_items` on `item_id = item_id` (the third `ss_items` leg is a
residual merge on the same key).

goopg (`q83-goopg-ea.txt:9`): `Merge Join rows=1 actual=22`, legs
`GroupAggregate rows=20` / `rows=10` — identical leaf estimates to PG's
(`rows=20`/`rows=10`, `q83-pg-ea.txt:16/:113`). 20 × 10 × 0.005 = 1:
the join selectivity is exactly `DEFAULT_EQ_SEL`.

Standalone probe (`probe83-{goopg,pg}.txt` — the two CTE legs + count):
goopg `Hash Join rows=1`; **PG `Merge Join rows=10`**. In-corpus PG estimates
the same node at rows=5. goopg is 22x off where PG is ~2-4x off.

Root cause (mechanism verified in code): the merge keys are CTE *output*
columns, not base-table columns. In goopg a CTE reference becomes a
`*CTEScan` leaf with `binding.table == nil`
(`joinsearchseam.go:2728`, `seamLeafBinding`), so `resolveJoinVarColumn`
fails and `examineJoinVar` takes its `!ok` arm
(`joinselectivity.go:163-178`). That arm already implements PG's
`examine_simple_variable` RTE_SUBQUERY `isunique` port — but reads
`relInfos[j].subqueryUniqueOutput`, which `joinsearchseam.go:1034` sets ONLY
for pulled ANY-subquery leaves (`pulledLeafUniqueOutput`, M0145-0008ab).
A FROM-clause `*CTEScan` leaf never gets it → `v.tuples` stays 0 →
`getVariableNumDistinct` hits the flat `nd=200` default → sel = 0.005 → est 1.

PG's rule (`selfuncs.c:5876-5883`, `examine_simple_variable` RTE_SUBQUERY):
`subquery->groupClause && list_length == 1 && targetIsInSortList(ste)` →
`isunique` → `get_variable_numdistinct` relative-unique arm →
`nd = leaf->tuples` (20 and 10) → `sel = (1-nf)/max(nd1,nd2) = 0.05` →
20 × 10 × 0.05 = **10** — PG's observed estimate, exactly.

Why this is attainable where M0145-0020 was not: 0020 closed the
`examine_simple_variable` CTE arm measured-no-gap because ITS three fires
(Q74 `UNION ALL`, Q31/Q39 multi-key `GROUP BY`) all hit PG's early exits —
`setOperations||groupingSets` (selfuncs.c:5843) or `groupClause` len≠1.
Q83's bodies group by a LONE column — the one arm where PG *does* produce
statistics (`isunique`). The fix is the narrow port of that arm to
FROM-clause derived leaves: per-leaf "unique output column" names (from the
`*CTEScan`'s body plan `GroupAggregate`/`Aggregate` lone group key, or the
retained WITH-list `*SelectStmt` via `*CTEScan.cte`), consumed in
`examineJoinVar`'s `!ok` arm alongside `tuples=leaf.baseRows` —
`vardata->rel` semantics.

Likely sibling coverage: Q95's `cte:ws_wh` Hash Semi Join est=1 vs 22
(finding #13 in the same list) is probably the same class — a lone-group-key
CTE output on one side.

**Filed: M0146-0009e** (impl, parent M0146-0009c).

## Disposition summary

| finding | relset | goopg est | PG same-relset est | actual | class |
|---|---|---|---|---|---|
| Q85 NL | `reason+wr+ws` | 1 | n/a (PG tree differs; standalone `ws⋈wr` = 1 both) | ~596 | PG-shared |
| Q85 GM | `reason+wr+ws` | 1 | same | ~596 | PG-shared |
| Q85 NL | `cd+reason+wp+wr+ws` | 1 | same | ~580 | PG-shared |
| Q78 MLJ | `dd+sr+ss+wr+ws` | 1,407 | 1,371 (same node) | 123,049 | PG-shared |
| Q83 MJ | `cr+dd+item+sr` | 1 | 5 in-corpus / 10 standalone | 22 | **goopg gap → M0146-0009e** |

Ledger rows for classes (a)/(b)/(c) flipped to `resolved` with these
dispositions; the single routed defect is M0146-0009e.
