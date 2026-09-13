# R115 result: Q96 forced forms bypass the HashJoin path-cost seam

R115 tested its reviewed attribution protocol on the R111 common-input
clusters. It did not change planner behaviour, and it does not identify a
cost-term cause of the Q96 disagreement. It falsifies the premise that the
selected forced-form joins can be attributed at `hashJoinCost`.

## Reproduction, controls, and observations

The temporary, exact-value `GOOPG_Q96_COST_TRACE=1` diagnostic recorded each
`addHashJoinPath` cost attempt, its filing verdict, and any selected path. It
also recorded the existing map and PG packed hash geometries. It was removed
before this report; the final source diff contains no diagnostic or production
cost change.

On the private R111 Goopg data directory, a newly built binary ran with
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, and
`GOMEMLIMIT=12GiB`. For every invocation the client executed:

```
SET join_collapse_limit=1;
SET from_collapse_limit=1;
SET work_mem='64MB';
EXPLAIN (COSTS true, FORMAT TEXT) <the immutable R101 form>;
```

The `GOOPG_Q96_COST_TRACE=1` arm captured two plans for each form in
`/tmp/r115-hdem-first-{1,2}.plan` and
`/tmp/r115-store-first-{1,2}.plan`, with server stderr in
`/tmp/r115-goopg-server.log`. The same binary and launch controls with the
trace unset captured `/tmp/r115-hdem-first-off-{1,2}.plan` and
`/tmp/r115-store-first-off-{1,2}.plan`, with stderr in
`/tmp/r115-goopg-off-server.log`. SHA-256 was identical within and across
arms: hdem-first `7e1d192167508a45f892ea3986b5a6345d752d8b54181e3748caee66f56bd7a9`,
store-first `72d60d7e04e0a3f9a430a6fc8eaaf7e3e694d24094554bd848d4dc3d6f385959`.
The plans preserve the R111 totals: `hdem-first` 27661.94 and `store-first`
27654.26. Thus the observation switch did not alter either candidate's plan
or price.

A live positive control on that exact binary and server,
`SELECT count(*) FROM household_demographics h, store s WHERE
h.hd_demo_sk = s.s_store_sk`, emitted two `DPQ96COST` cost/filed records in
that same `/tmp/r115-goopg-server.log`; its corresponding `DPPATH` records
named `join.hash`. This proves both the environment switch and formatter
reach the live search path. Extracting `^DPQ96COST` from that log yields the
positive-control records and no Q96 record. The two Q96 parenthesized forced
forms emitted no `DPQ96COST` record at all. Their
`GOOPG_PGSHAPED_DP_TRACE=1` output contained only upper GroupAggregate and
Sort path records, not a join path record.

The distinction is structural, not an absent environment variable.
Source-level attribution identifies the likely boundary: `tryJoinSearch` in
`joinsearchseam.go` preserves the syntactic node whenever
`tryPGShapedJoinSearch` declines, and the latter has an explicit
`chainCarriesLateral` decline. Q96's final `JOIN LATERAL time_dim` satisfies
that source condition. This is not asserted as a captured runtime
`seam-decline reason=lateral` record; the retained log lacks that line. The
unchanged syntactic/prebuilt route is nevertheless established by the live
negative/positive control. A successor must capture the exact legacy route
before treating the LATERAL guard as the complete causal explanation.

## Ruling

**DONE as a negative attribution experiment.** R115 cannot decompose the
Q96 forced-order margin at the path-cost seam. In particular, its zero trace
does not support a conclusion about Goopg-versus-PostgreSQL Datum sizes,
HashJoin spill pricing, or any individual HashJoin cost term. A successor
must observe the legacy/prebuilt forced-join planning route and establish its
own selected-candidate mapping before proposing a cost change.
