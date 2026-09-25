# R106 result: forced-margin term attribution is unobservable

R106 made no source change. It verifies that enabling Goopg's diagnostic does
not alter the two forced plans, but the available trace and PG JSON cannot
separate the Hash Join cost terms needed to explain R101/R105's root-margin
difference. The scope's stop rule applies.

## Trace-on reproduction

Source was `662fa602b`; `/tmp/r106-goopg`, built with
`go build -o /tmp/r106-goopg ./cmd/goopg`, has SHA-256
`8871063e25b546e0f226e60d9316afcc508ecb94dced4c1b2229637a4b575653`.
Four separately named transient user services used the private clone
`/tmp/r97goopg/ds025` on port 5563, then were stopped. All used
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`,
`GOOPG_PGSHAPED_DP_TRACE=1`, `GOGC=off`, and `GOMEMLIMIT=12GiB`; no ANALYZE
or data/SQL change occurred.

| form/run | trace-on EXPLAIN SHA-256 | R105 trace-off SHA-256 | equal | complete journal SHA-256 | DPPATH/DPTRACE-content SHA-256 |
| --- | --- | --- | --- | --- | --- |
| hdem-first/1 | `30e10cb916a12267373b5f3976b2a7ee16483b374ab8f1e354aa83a96ed20c13` | `30e10cb916a12267373b5f3976b2a7ee16483b374ab8f1e354aa83a96ed20c13` | yes | `98e781abfab8324e87817d0131b2e61c65b081582e9348ef31f685cf56180bff` | `a8abf9fcc60731bca4e5ab9125044f3640c6af5e7d0cc5463d4ee016529f5dea` |
| hdem-first/2 | `30e10cb916a12267373b5f3976b2a7ee16483b374ab8f1e354aa83a96ed20c13` | `30e10cb916a12267373b5f3976b2a7ee16483b374ab8f1e354aa83a96ed20c13` | yes | `dce8dbaf6a0fc9a0074f204e62d703b2bf5054f5a9012f7aca84fd521fc2a202` | `a8abf9fcc60731bca4e5ab9125044f3640c6af5e7d0cc5463d4ee016529f5dea` |
| store-first/1 | `2b35b93c26f960395d68128feba28e8b66ec92ec0f385551c20663f99cb05de9` | `2b35b93c26f960395d68128feba28e8b66ec92ec0f385551c20663f99cb05de9` | yes | `7cc65b5d0ea58816150b75c1914521d81527fbd9425c54b53628707926d31ec5` | `22e66066649a80f3f0c0f9c650f06464160203948cbb9cc46ded43bb8c5ec31c` |
| store-first/2 | `2b35b93c26f960395d68128feba28e8b66ec92ec0f385551c20663f99cb05de9` | `2b35b93c26f960395d68128feba28e8b66ec92ec0f385551c20663f99cb05de9` | yes | `7cf0346e07a328bdc1f36ef1cd9c7c283822b2f33c3106ae737c580abb3d3423` | `22e66066649a80f3f0c0f9c650f06464160203948cbb9cc46ded43bb8c5ec31c` |

The service unit is the statement/run delimiter for each journal artifact.
The differing complete-journal hashes include lifecycle timestamps; the
trace-content hash is equal within each form. Each trace consists only of an
accepted group aggregate, a refused aggregate upper gate, and an accepted
ordered sort. It contains no DPTRACE/DPPATH record for either forced Hash
Join, so it exposes none of their hash-build, probe, bucket, or selectivity
terms. A separate trace-on `goopg-r106-values.service` run executed each
immutable form once: hdem-first = 266 and store-first = 266; it was also
stopped before reporting.

## Four-operator ledger

`observed` values come from R101 PG JSON or R105 Goopg text EXPLAIN;
`unavailable` means neither format/trace exposes the requested component.

| form / level | engine | child totals and rows | join output total / rows | incremental total | width | hash build/probe/selectivity term |
| --- | --- | --- | --- | ---: | ---: | --- |
| hdem / first | PG | Seq Scan 15258.18 / 232218; Hash 143.00 / 720 | 16020.10 / 22198 | 618.92 | 8 | unavailable |
| hdem / second | PG | prior 16020.10 / 22198; Hash 1.15 / 1 | 16099.21 / 1769 | 77.96 | 4 | unavailable |
| store / first | PG | Seq Scan 15258.18 / 232218; Hash 1.15 / 1 | 16074.77 / 18504 | 815.44 | 8 | unavailable |
| store / second | PG | prior 16074.77 / 18504; Hash 143.00 / 720 | 16275.37 / 1769 | 57.60 | 4 | unavailable |
| hdem / first | Goopg | Seq Scan 7198.76 / 719876; Seq Scan 72.00 / 7200 | 14148.45 / 687769 | 6877.69 | 476 | unavailable |
| hdem / second | Goopg | prior 14148.45 / 687769; Seq Scan 0.12 / 12 | 27598.12 / 657186 | 13449.55 | 1152 | unavailable |
| store / first | Goopg | Seq Scan 7198.76 / 719876; Seq Scan 0.12 / 12 | 14077.53 / 687865 | 6878.65 | 1104 | unavailable |
| store / second | Goopg | prior 14077.53 / 687865; Seq Scan 72.00 / 7200 | 27600.04 / 657186 | 13522.39 | 1152 | unavailable |

The PG hdem-second incremental value is 77.96 because the printed rounded
operands yield 16099.21 - 16020.10 - 1.15 = 77.96; this is a plan total delta,
not an exposed component. It is deliberately not used as an exact formula
input.

Most importantly, the two engines' observed base estimates already differ:
PG has 232218 `store_sales` rows and 720/1 dimension rows, while Goopg has
719876 and 7200/12. The same scalar result does not make these cost inputs
comparable. Therefore the 176.16 versus 1.92 forced root margin cannot be
attributed to a Goopg hash cost term from these artifacts.

## Reached-code audit and ruling

The relevant Goopg path is `addHashJoinPath` in `pathgen.go`, which passes
child path totals/rows, output rows, key count, bucket statistic, width and
column sizing into `hashJoinCost` in `cost_funcs.go`. That function adds inner
build read/hash, outer scan/probe and output CPU, optional bucket walk,
inner-unique virtual-bucket adjustments, and spill I/O; residual qualification
cost is added after it. `hashJoinFinalCostInputFor` supplies the inner-unique
match fraction when a complete unique inner index is provable. These are
candidate terms for every ledger row, but none is emitted by the forced
parenthesized-join trace, and PG JSON does not expose matching internals.

**Ruling: UNOBSERVABLE.** Do not change any cost/selectivity term or infer a
natural-order result. A successor needs a valid common-data, common-stats
oracle and a reviewed diagnostic that records the actually selected forced
Hash Join inputs/components without changing plan output.
