# R101 result: PG loser price observed; Goopg cannot bind the equivalent form

R101 used two disposable explicit-INNER Q96 forms. Their complete SQL text is
`/tmp/r101-{hdem-first,store-first}.sql`; their SHA-256 values are,
respectively,
`d83c3d58d7d4b543d68908da78de79bad900eb230e0dbe830faf27eec6293784`
and `cda057e138b1fd9f9731b7249ea893a257f5c1c9561cffea2b5b8182fc27d5d6`.
They retain the SELECT/WHERE/ORDER/LIMIT semantics and use a final LATERAL
`time_dim` probe. Sessions pinned the R100 planning settings plus
`join_collapse_limit=1` and `from_collapse_limit=1`.

## PG18.3 measurement

Both JSON captures per form were byte-identical on the R100 native oracle and
both values were **266**. EXPLAIN confirms the intended first two leaves and
the final `time_dim_pkey` probe.

| forced first order | total | first Hash Join total/rows | second Hash Join total/rows |
|---|---:|---:|---:|
| `store_sales → hdem → store` | 17673.05 | 16020.10 / 22198 | 16099.21 / 1769 |
| `store_sales → store → hdem` | 17849.21 | 16074.77 / 18504 | 16275.37 / 1769 |

PG therefore prices hdem-first **176.16 cheaper**. This is a direct loser
price observation, not an inference from EXPLAIN's natural winner. It confirms
the historical natural order and rules out a PG exact-cost tie.

## Goopg boundary

Goopg rejects both logically equivalent forms during binding with
`missing FROM-clause entry for table "store_sales"` at the LATERAL
time predicate. Its repeated outputs are identical errors, not plans, so no
Goopg forced-order costs or values exist. R101's explicit stop rule applies:
do not disable methods, rewrite away the correlated probe, or use a forced
PG margin as a Goopg cost-change target.

A later scope may separately establish parenthesized-join/LATERAL namespace
semantics and value identity. Until then, R101 does not alter R98's
inner-unique ruling or authorize any cost change.
