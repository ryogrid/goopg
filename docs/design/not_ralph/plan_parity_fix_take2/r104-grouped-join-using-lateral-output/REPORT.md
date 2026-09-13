# R104 result: grouped JOIN USING bindings survive LATERAL

R104 preserves source-column bindings for an unaliased parenthesized JOIN
through its synthetic `SELECT *` output. A `rangeBinding.columnOffsets` map
now represents the non-contiguous `JOIN USING` layout: both qualified source
references for a USING column resolve to the one merged physical output slot.

## Reproducible PG18.3 witness

Native binary: `postgres/local_install/bin/postgres`, version `PostgreSQL
18.3`; private R100 cluster `/tmp/r100-pg18-q96-oracle`, database `tpcds025`,
port 65442. The complete setup and every probe statement are retained in
`/tmp/r104-pg-probes.sql`. With `r104_a(id,bid)=(1,10)` and
`r104_b(id,bid)=(1,20)`, PG returned scalar 1 for a later-ON-plus-LATERAL
`a.id`, LATERAL `b.id`, and LATERAL unqualified merged `id`; unqualified
`bid` failed 42702. An explicitly aliased group accepted `j.id` and rejected
`a.id` with 42P01; the corresponding non-LATERAL derived table also failed
42P01.

## Goopg evidence

The matching Goopg probe used `/tmp/r97goopg/ds025`, port 5562, and the same
SQL. It returned the three scalar values and matched 42702/42P01 controls.
`TestPlanLateralGroupedUsingBindings` pins the later ON, qualified/merged
LATERAL, ambiguity, alias, and non-LATERAL cases. Focused parser/analyzer,
optimizer, and executor package tests pass.

The unchanged R101 forms both returned **266**. EXPLAIN retains:

* hdem-first: `store_sales × household_demographics`, then `store`, then
  `Nested Loop` / `time_dim_pkey`;
* store-first: `store_sales × store`, then `household_demographics`, then the
  same final probe.

This is semantic enablement only. It supplies no Goopg cost evidence and does
not turn forced SQL order into a natural-election conclusion.
