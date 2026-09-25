# M0145-0020 — porting `examine_simple_variable`'s CTE arm cannot move the fires it was filed for

Status: **RECON COMPLETE 2026-09-22 — measured-no-gap on the named fires, and
the task's premise is refuted.** No production file touched. The mechanism that
actually causes the collapse is named below and filed as **M0145-0020a**.
Kind: impl → closed as recon (no code was needed to answer it)
Parent: M0145-0012
Movement: none

## What the task assumed

M0145-0020 was filed as M0145-0012's prerequisite: *"PG resolves a qual on a
CTE output column to the underlying base column's real statistics … goopg has
no such lookup, so quals on multi-ref CTE outputs (`ws` ×3, `inv` ×2,
`year_total` ×4) collapse to the default selectivity"*. Its success test is
that the three corpus fires estimate ≥ the `rows<=1` fallback's effect without
the fallback.

Two things are wrong with that, and both are checkable.

## 1. Upstream punts on all three fires before it reaches any statistics

`examine_simple_variable`'s CTE arm
(`postgres/src/backend/utils/adt/selfuncs.c:5737-5912`) has three exits that
fire *before* the recursion, and each of the three CTEs hits one:

| exit | upstream line | CTE |
|---|---|---|
| `if (subquery->setOperations \|\| subquery->groupingSets) return;` | `:5843` | **Q74 `year_total`** — it is a `UNION ALL` of two grouped selects |
| `if (subquery->groupClause) { … return; }` — `isunique` only when there is exactly ONE grouping column | `:5876-5883` | **Q31 `ws`** — `group by ca_county, d_qoy, d_year` (3 keys) |
| same, one level down after recursing into the subquery RTE | `:5876-5883` | **Q39 `inv`** — the CTE body is `select … from (… group by w_warehouse_name, w_warehouse_sk, i_item_sk, d_moy) foo`, so the recursion lands on the grouped subquery and punts there |

So a faithful port returns **no statistics** for exactly the columns the task
wants statistics for. The success test is unattainable by this route, not
merely hard. This is the same conclusion M0145-0009's census reached for the
ndistinct channel — *"a group key of a MULTI-key aggregate, where upstream's
`isunique` counting argument does not exist"* — arriving independently at the
selectivity channel.

goopg reaches the same answer by a different route: `resolveBaseColumn`
(`internal/optimizer/joinkeyproof.go`) already has a `*CTEScan` arm that
recurses into the body, and it stops at the same `Aggregate`/set-op boundary
upstream stops at. The two engines agree here; there is nothing to port.

## 2. The measured cause of Q39's collapse is somewhere else entirely

PG 18.3 does **not** collapse this CTE scan. Measured on the instrumented
SF0.25 cluster (`:5560`, `tpcds025`) against goopg's own SF0.25 capture:

| | goopg | PG 18.3 |
|---|---|---|
| HashAggregate INPUT | 11703 | 11606 |
| HashAggregate OUTPUT (after the `cov > 1` HAVING) | **20** | **3869** |
| `CTE Scan on inv` (after `d_moy = 1`) | 1 | **19** |

The inputs agree to within 1%. The divergence is entirely in the **grouped
output** estimate. PG's 3869 is `11606 × 0.3333` — every input row its own
group (`estimate_num_groups` capped at the input), then `DEFAULT_INEQ_SEL` for
the HAVING. goopg lands on 20, roughly 193× lower.

The CTE-output qual `d_moy = 1` then gets the same default selectivity in both
engines — 0.005 — and that is *not* the problem: 3869 × 0.005 = 19 survives,
20 × 0.005 = 0.1 clamps to 1. **The collapse is caused by the body estimate,
not by the qual's statistics.** Supplying CTE-output statistics would not have
prevented it, which is a second, independent refutation of the task's premise.

## What this means for M0145-0012

M0145-0012's `rows<=1` fallback is load-bearing because the grouped-output
estimate under-shoots, so the owner's sequencing — 0020 unblocks 0012 — does
not hold. The real prerequisite is the grouped-output cardinality, filed as
**M0145-0020a**, and it is worth noting that the fix direction is toward PG's
*more conservative* estimate (every row its own group) rather than a cleverer
one.

## Evidence

- PG: `tmp/pg18-optdebug` cluster, `EXPLAIN` of Q39's first statement.
- goopg: `tmp/m0145-0019a-parity/m0145-0019a-sf025.plans.txt`, `=== Q39`.
- Upstream exits: `selfuncs.c:5843`, `:5876-5883`.
