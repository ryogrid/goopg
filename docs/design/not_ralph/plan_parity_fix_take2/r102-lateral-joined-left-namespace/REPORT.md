# R102 result: grouped-JOIN LATERAL needs planner binding, not an analyzer-only fix

R102 tested the semantic boundary exposed by R101 without changing committed
production code. The temporary implementation was reverted after it proved
that the required correction crosses the scope's analyzer-only boundary.

## Native PostgreSQL 18.3 semantics

The stopped private R100 copy was started with
`postgres/local_install/bin/postgres` (reported `PostgreSQL 18.3`) on
`127.0.0.1:65441`, queried in `tpcds025`, and stopped normally. The complete
probe file was `/tmp/r102-pg-probes.sql`.

With one-row temporary tables `r102_a(id,x)=(1,10)` and
`r102_b(id,y)=(1,20)`, PostgreSQL returned 10 for each successful form:

* `(r102_a AS a JOIN r102_b AS b ...) JOIN LATERAL (SELECT a.x)`;
* the equivalent unparenthesized ordinary JOIN chain; and
* `(r102_a AS a JOIN r102_b AS b ...) AS j JOIN LATERAL (SELECT j.x)`.

For an explicitly aliased grouped join, `SELECT a.x` in the LATERAL body
failed with SQLSTATE `42P01`, while `j.x` succeeded. An unaliased
`USING (id)` group allowed `a.id`, `b.id`, and the merged unqualified `id`;
all returned 1. An otherwise identical non-LATERAL right subquery failed with
SQLSTATE `42P01` and PostgreSQL's LATERAL hint. These establish the R102
positive and negative namespace rules.

## Goopg boundary

Goopg's generated parser lowers an unaliased parenthesized JOIN through
`syntheticParenSelect` / `derivedRangeVar`, assigning `__sq_*`. A temporary
parser marker plus analyzer scope substitution made focused analyzer tests
pass for the two-source LATERAL form, ordinary-chain control, explicit alias,
`USING`, and non-LATERAL rejection.

However, the same isolated Goopg run (`/tmp/r97goopg/ds025`, port 5562,
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`) still rejected the
unchanged R101 hdem-first SQL before planning:

```
ERROR: column "ss_store_sk" does not exist
LINE 8: ON store_sales.ss_store_sk = store.s_store_sk
```

The failure is the next ordinary JOIN qualification after the grouped
synthetic relation. Therefore planner range bindings, not only analyzer name
resolution, must preserve the grouped relation's source identity. R102
explicitly forbids expanding into planner/executor work, so the temporary
change was reverted and no Goopg R101 value or forced-order plan was captured.
The disposable Goopg server and private PG server were stopped.

## Conclusion

No production change is authorized by R102. A future reviewed scope may cover
the parser/analyzer/planner representation and correlated binding end to end,
with explicit grouped-alias and `USING` visibility controls. It must not use
the forced Q96 forms as cost or natural-election evidence.
