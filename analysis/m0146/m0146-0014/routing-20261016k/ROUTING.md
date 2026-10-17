# M0146-0014 — S9 census pass at 3f5f73822 (2026-10-16)

The census `routing-20261016j` is still fresh. Its captures (the M0146-0208 arms) are code equal to 4a75cefbb, and
the commits since (99f13e8eb, 3f5f73822) touch only `.ralph/` and `analysis/`. The first-divergence files are copied
unchanged.

## Routes

- **SF1 Q18** is re-routed to a COSTTIE residual (M0146-0209): add\_path's fuzz boundary, with the corpus part going to
  M0146-0019.
- **Unchanged:** scan-type and SF1 Q68 belong to M0146-0019. The COSTTIE, GEQO-RNG, RELPAGES, ESTIMATE+RELPAGES and
  STALE-SF1 records are waived.

## Actionable records by category

| category | records | count |
|---|---|---|
| qual-placement | SF1 Q54 | 1 |

**Filed: M0146-0210** (qual-placement, SF1 Q54). SF0.25 Q54 is MATCH. The first divergence is at depth 6, under the
inner GroupAggregate (`Group Key: customer.c_customer_sk`).

| | PG | goopg |
|---|---|---|
| semi-join unique-ification | `Unique` (2 rows) over a `Sort` over a `Gather` (2 rows) | `Unique` (**1 row**) over a `Gather Merge` (2 rows) |
| join order above it | `Unique ⋈ customer_address` (2 rows), then `⋈ Materialize(store)` with the ca/store Join Filter, then `⋈ Gather(store_sales ⋈ date_dim)` with `c_customer_sk = ss_customer_sk` | `Unique ⋈ Gather(store_sales ⋈ date_dim)`, then `⋈ customer_address`, then `⋈ store` |
| top Nested Loop | 82996.61 | 82979.02 |

**Lead.** goopg's Unique estimates 1 row from a 2-row input. PG's unique-ification sizes its output with
`estimate_num_groups` over the semi-join RHS expressions, clamped to the input's rows (2 here, the gathered count). If
goopg sizes the Unique from the per-worker partial rows (1), every join above it shrinks to 1 row, and the order
changes. The Sort-over-Gather vs Gather Merge choice under the Unique may be another fuzz-boundary tie, as in
M0146-0209.
