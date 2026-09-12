# R89 REPORT — Q96 partial-prefix cost attribution

R89 is a measurement-only follow-up to R88 (`dae2ebeb7`). It attributes the
same-relset partial Hash Join election before Gather and the later partial NLI;
it makes no production planner change.

## 1. Reproducible environment and controls

The measurement binary was built from scope commit `17911ceff` on 2026-09-12:

| item | value |
| --- | --- |
| Goopg measurement binary | `/tmp/pp2/r89/goopg-r89-trace`, SHA-256 `da467a4cfac185b65ded0d041ed54abcdd4913964ab2c16f6fab556fff580061` |
| Goopg post-removal binary | `/tmp/pp2/r89/goopg-r89`, SHA-256 `39e7876ee4e7b0d1d4b3fb2f6cc7846459f14f8abb698aae063a615e64ea7474` |
| private Goopg data directory / port | `/tmp/pp2/clone-ds025-r89`, `127.0.0.1:5560` |
| PostgreSQL oracle | PG 18.3, `127.0.0.1:65438/tpcds025` |
| session settings | `work_mem=64MB`, `max_parallel_workers_per_gather=4`, `parallel_leader_participation=on` |
| PG observed cost GUCs | `seq_page_cost=1`, `random_page_cost=4`, `cpu_tuple_cost=.01`, `cpu_index_tuple_cost=.005`, `cpu_operator_cost=.0025`, `parallel_setup_cost=1000`, `parallel_tuple_cost=.1`, `hash_mem_multiplier=2`, `effective_cache_size=4GB` |
| Goopg trace cost inputs | `seq_page_cost=1`, `cpu_tuple_cost=.01`, `cpu_operator_cost=.0025`, and a 134,217,728-byte hash budget from this query's `64MB` session work_mem and multiplier 2 |

The server ran foreground in a private cgroup scope. The temporary
`GOOPG_R89_PARTIAL_HASH_COST_TRACE=1` hook emitted only cost inputs and terms;
it was removed before building the post-removal binary. A source search for
`R89HCOST` and `R89_PARTIAL` is empty after removal.

The retained artifacts are under `/tmp/pp2/r89/`, notably
`q96-selected-partial-costs.txt`, `q96-pg-live.txt`,
`q96-pg-stats-indexes.txt`, `q96-pg-settings-plan.json.txt`,
`server-5560-trace.log`, and the listed plan/value captures.

Controls passed:

* Q96 natural trace A/A is byte-identical; `q96-natural-a.txt` and
  `q96-natural-b.txt` match.
* The natural and `GOOPG_GATHER_PATHS=top` Q96 plans are byte-identical,
  SHA-256 `af7f9e3fc2afa7afed9a1e62384610265e2047da505414a47a51355eab273368`.
  Thus diagnostic Gather admission did not change the natural tree.
* Trace-on/off plans match byte-for-byte for Q9, Q41, Q91, and Q96. Their
  SHA-256 values respectively are `4392109fd377…`, `660660223437…`,
  `106c24f5a7b…`, and `af7f9e3fc2a…`.
* Q96 values are identical: Goopg and PG each return `266`.
* `go test ./internal/optimizer ./internal/executor
  ./internal/testutil/estimateaudit`, the corresponding `go vet`, and
  `git diff --check` pass after the temporary hook was removed.

The full TPC-H digest, SF0.25 value sweep, and live-PG census are intentionally
not rerun: R89 leaves the production binary unchanged.

## 2. The election and Goopg's exact decomposition

`store_sales` is the partial outer (three workers, 232,218 rows per worker);
both `household_demographics` and `store` are complete inner builds. This is
the non-`parallel_hash` shape: partial probe plus undivided build. All four
selected candidates have one hash key, zero residual quals, one bucket tuple,
and no spill. The temporary trace records the full input tuple and the exact
Goopg terms below.

| candidate | startup equation | run equation | total |
| --- | --- | --- | ---: |
| L2 `ss -> hd` | `0 + 140 + 9 = 149` | `15256.1806 + 580.545 + 290.2725 + 221.86 = 16348.8581` | 16497.8581 |
| L2 `ss -> store` | `0 + 1.15 + .0125 = 1.1625` | `15256.1806 + 580.545 + 290.2725 + 184.91 = 16311.9081` | 16313.0706 |
| L3 `(ss -> hd) -> store` | `149 + 1.15 + .0125 = 150.1625` | `16348.8581 + 55.465 + 27.7325 + 17.67 = 16449.7256` | 16599.8881 |
| L3 `(ss -> store) -> hd` | `1.1625 + 140 + 9 = 150.1625` | `16311.9081 + 46.2275 + 23.11375 + 17.67 = 16398.91935` | 16549.08185 |

Each run equation is, in order: inherited outer run, probe-key hash CPU,
bucket-walk CPU, and output tuple CPU. There are no residual, target-list, or
spill terms in Goopg's path cost. The retained non-PG prefix therefore leads
by `184.7875` at L2 and `50.80625` at L3, exactly matching the rounded R88
DPPATH values.

For the first two candidates, the trace reports these relevant input
differences:

| input | `ss -> hd` | `ss -> store` |
| --- | ---: | ---: |
| complete inner rows / total cost | 720 / 140 | 1 / 1.15 |
| output rows | 22,186 | 18,491 |
| inner bucket fraction | 1/720 | 1 |
| Goopg build columns / variable bytes | 5 / 8 | 29 / 195 |
| Goopg hash geometry | 1,024 buckets, 1 batch | 1,024 buckets, 1 batch |

Thus the observed Goopg difference is not a hidden Gather charge or a build
being divided per worker. It is present entirely in the partial Hash Join costs
before either downstream decision.

## 3. PG18.3 source and live coordinate space

The live PG plan (`q96-pg-live.txt`) selects the opposite prefix:

```text
Parallel Seq Scan on store_sales  rows=232218
  Hash Join  rows=22198  Inner Unique: true  (inner hd rows=720)
    Hash Join rows=1769   Inner Unique: true  (inner store rows=1)
```

The nodes are `Hash Join`, not `Parallel Hash Join`; the JSON plan records
`Parallel Aware: false` for them. Consequently their partial outer rows are
per-worker while the `Hash` inputs are complete, undivided builds, as in the
`parallel_hash=false` source branch. The rejected PG `ss -> store` and its
L3 continuation are not exposed by `EXPLAIN`; their path-private state is
therefore explicitly unavailable and was neither inferred nor forced.

For the two selected PG nodes, the observable initial-cost portion of
`initial_cost_hashjoin` recomputes from the plan (minor differences below are
from EXPLAIN's rounded input costs):

| selected PG node | source startup equation | source initial total | displayed final total | final-only remainder |
| --- | --- | ---: | ---: | ---: |
| `ss -> hd` | `0 + 143 + (.0125 * 720) = 152` | `152 + 15258.18 + (.0025 * 232218) = 15990.725` | 16020.10 | 29.375 |
| `(ss -> hd) -> store` | `152 + 1.15 + (.0125 * 1) = 153.1625` | `153.1625 + (16020.10 - 152) + (.0025 * 22198) = 16076.7575` | 16099.21 | 22.4525 |

The catalog evidence is consistent with the visible branch: both inner keys
are non-null unique primary keys; `store_sales.ss_hdemo_sk` has null fraction
`.044066668`, and `ss_store_sk` has null fraction `.0438` with a six-value
MCV list. The full captured MCV lists, relation pages/tuples, index definitions,
and all cost GUCs are in `q96-pg-stats-indexes.txt` and
`q96-pg-settings-plan.json.txt`.

PG's `final_cost_hashjoin` nevertheless cannot be reproduced exactly from
EXPLAIN alone: its visible `Inner Unique: true` enters the branch at
`costsize.c:4434-4481`, which needs `extra->semifactors.outer_match_frac`,
`match_count`, rounded `outer_matched_rows`, `inner_scan_frac`, virtual
bucket count, the chosen bucket/MCV fractions, hash/qpqual costs, and
pathtarget costs. No rejected-path value is fabricated here.

## 4. Same-Goopg-coordinate PG counterfactual

The common `initial_cost_hashjoin` component is computable on all four exact
Goopg input rows.  Both its startup and initial total agree exactly with the
corresponding pre-final Goopg terms:

| Goopg candidate | PG-source initial startup on its exact inputs | PG-source initial total on its exact inputs | matching Goopg pre-final terms |
| --- | ---: | --- | --- |
| `ss -> hd` | `0 + 140 + (.0125 * 720) = 149` | `149 + 15256.1806 + 580.545 = 15985.7256` | startup 149; outer run + probe hash `15256.1806 + 580.545` |
| `ss -> store` | `0 + 1.15 + (.0125 * 1) = 1.1625` | `1.1625 + 15256.1806 + 580.545 = 15837.8881` | startup 1.1625; outer run + probe hash `15256.1806 + 580.545` |
| `(ss -> hd) -> store` | `149 + 1.15 + (.0125 * 1) = 150.1625` | `150.1625 + 16348.8581 + 55.465 = 16554.4856` | startup 150.1625; outer run + probe hash `16348.8581 + 55.465` |
| `(ss -> store) -> hd` | `1.1625 + 140 + (.0125 * 720) = 150.1625` | `150.1625 + 16311.9081 + 46.2275 = 16508.2981` | startup 150.1625; outer run + probe hash `16311.9081 + 46.2275` |

The required PG `final_cost_hashjoin` counterfactual cannot proceed beyond
that boundary on the same Goopg inputs. `hashJoinInputs` carries only path
costs/rows, output rows, hash-key count, one bucket fraction, Goopg column
geometry, and variable bytes. Goopg does separate hash keys from residual
qpquals and does compute executor bucket/batch geometry, so those are not the
missing representation. The absent inputs are `inner_unique`, either semijoin
factor, PG's virtual-bucket and MCV-frequency values, PG-style QualCost
startup/per-tuple values for the hash/qpqual split, and a join pathtarget
startup/per-tuple cost. The search itself states that it never marks a join
inner-unique (`joinpathsmemoize.go`); its cost function consequently always
executes the non-unique bucket-walk formula instead.

This is not an unavailable-observation result: the temporary trace recovers
every existing Goopg cost input, and the source data model proves the required
PG branch inputs do not exist at this boundary.

## 5. Outcome and next action

**Outcome: C2 — Goopg lacks source-required inputs/terms.** This is not C1:
the source-faithful common initial component agrees exactly. It is not C3:
the final source branch cannot be evaluated with the Goopg path model. It is
not C4 because the absence is representational, not a failed measurement.

The next task must be separately scoped before changing code. It must decide
how to carry a sound inner-unique proof and the semijoin factors into partial
and serial Hash Join costing, how to retain PG's matched/unmatched bucket
charges without guessing unavailable MCV-frequency/QualCost/pathtarget inputs,
and how to preserve the two estimator siblings. It must not claim that this
alone will flip Q96: R89 establishes the missing boundary, not the resulting
counterfactual total.
