# M0144-0007 — Cost-margin census

**Instrument:** `scripts/goopg-margin-census.py`. For every divergent record
in `m0144-0002-census-*.txt` it runs, on a private goopg lane started with
`GOOPG_PGSHAPED_DP_TRACE=1` (DPPATH provenance on stderr → `--log` sliced
per query by byte offset):

1. **baseline** `EXPLAIN` (capture pins `work_mem=64MB`,
   `max_parallel_workers_per_gather=4`) — root total `cost0`, and the
   record is re-derived with the same `census_query` machinery
   (`baseline-record-not-reproduced` is flagged in `note`);
2. **forced** `EXPLAIN` under session arms that exclude the goopg winner —
   PG-style `enable_*=off` (`enable_sort/hashagg/nestloop/hashjoin/
   mergejoin/seqscan/indexscan/indexonlyscan/bitmapscan/memoize/material/
   incremental_sort`), `max_parallel_workers_per_gather=0`, or the
   parallel-encourage preset (`parallel_setup_cost/tuple_cost=0`,
   `min_parallel_*_scan_size=0`) when PG's child is parallel-ish. The
   forced plan is re-censused against the committed PG capture; `produced`
   means the recorded divergence actually moved. `rootd% =
   (cost1−cost0)/cost0`.
3. **candidate margin** — DPPATH lines whose producer matches the PG
   child's producer family (kind→producer map in the script). When the
   PG-shape candidate was OFFERED at a rel, `candm%` is its cost delta vs
   that rel's accepted winner — the exact "priced-and-lost" number.
   `offered=0` = the candidate was never generated.

Second pass per TPC-DS corpus: a lane restarted with
`GOOPG_INCREMENTAL_SORT=1 GOOPG_PARTIAL_SORT_PATHS=1` for the
`PG Incremental Sort` records (`*-isarm.txt` files).

**Margin classes** (03-forward-plan §2, extended by two signatures the
data surfaced):

| class | meaning | fix class |
|---|---|---|
| `election` | margin <1% | comparePaths/tie-break level |
| `input` | margin 1–20% | estimator / narrowing input |
| `priced-structural` | margin >20% | missing mechanism / priced-away |
| `forced-cheaper` | arm produced PG's shape CHEAPER than the winner | election-order/fuzz artifact — winner never had a real cost advantage |
| `dominated-noncost` | PG-shape candidate offered cheaper but dominated | lost on pathkeys/parameterisation, not price — input-shape structural gap |
| `unexpressible` | arm tried, no PG-shape candidate exists | generation gap |
| `unexpressible/no-arm` | no arm exists for the goopg winner's kind | generation/boundary gap (CTE, Subquery Scan, HashSetOp, same-kind qual) |

Raw per-query rows: `m0144-0007-margin-{tpch-parallel,tpcds-sf025,tpcds-sf1}.txt`
(+ `-isarm` variants) in this directory. Lanes: private sf025 clone
`tmp/c20a/data-sf025` :5591, canonical sf1 dir :5592 (EXPLAIN-only), private
tpch clone `tmp/goopg-spotcheck-tpch-data` :5590 — all HEAD `06d9d9ea8`,
all read-only EXPLAIN.

## Headline

| corpus | records | election+forced-cheaper | input | priced-structural | dominated-noncost | unexpressible(+no-arm) |
|---|---|---|---|---|---|---|
| TPC-H parallel | 21 | 6 | 0 | 4 | 3 | 8 |
| TPC-DS SF0.25 | 94 | 19 | 1 | 15 | 14 | 45 |
| TPC-DS SF1 | 95 | 33 | 0 | 8 | 1 | 53 |
| **all** | **210** | **58 (28%)** | **1** | **27 (13%)** | **18 (9%)** | **106 (50%)** |

**~72% of first-divergence decision points are structural, not elections.**
The "almost chose it" population (election + forced-cheaper + input) is
only ~28% — and `input`-class (the 1–20% band the spec reserves for
rows/width/cost-term divergence) is nearly empty: ONE record in 210. Where
the margin is measurable and non-trivial it is usually >20%
(`priced-structural`), and the largest single class is the candidate
simply never existing (`unexpressible`).

## 0002 tables with margin column appended

### TPC-H parallel (n=21)

| n | category | under | PG child | goopg child | depth | margin |
|---|---|---|---|---|---|---|
| 3 | sort-strategy | (root) | Finalize GroupAggregate | Sort | 0 | dominated-noncost×2 (cand −55..−75%), unexpressible×1 |
| 3 | sort-strategy | (root) | GroupAggregate | Sort | 0 | unexpressible×3 — sort-over-agg, `nonemptykeys=0` |
| 2 | aggregation-strategy | Sort | HashAggregate | GroupAggregate | 1 | dominated-noncost×1 (−16%), election×1 (0%) |
| 2 | parallelism | HashAggregate | Gather | (serial child) | 2 | forced-cheaper×1 (−21%), election×1 (0%) |
| 2 | parallelism | (join/agg) | Parallel Hash Join | Hash Join | 3–4 | priced-structural×1 (+148%), forced-cheaper×1 (−0.5%) |
| 1+1 | join-method | Partial Aggregate / Sort | Nested Loop | Hash Join / NL Semi→Anti | 3 | unexpressible/no-arm×2 |
| 1 | qual-placement | Hash Join | Nested Loop | Nested Loop | 2 | unexpressible/no-arm |
| 1 | parameterisation | Sort | Hash Join | Hash Join | 1 | unexpressible/no-arm |
| 5 | aggregation-strategy | (root/Sort/HashAggregate) | phased or plain agg | other agg impl | 0–2 | election×3 (0%), priced-structural×2 (+53%, +56%, +292%) |

### TPC-DS SF0.25 (n=97; 94 measured, 3 both-side `error`)

| n | category | under | PG child | goopg child | depth | margin |
|---|---|---|---|---|---|---|
| 14 | sort-strategy | Limit | GroupAggregate | Sort | 1 | election×5 (0%), dominated-noncost×7 (−48..0%), priced-structural×2 (+95..+121%) |
| 5 | sort-strategy | Limit | Incremental Sort | Sort | 1 | **unexpressible×5 — IS-arm lane: `nonemptykeys=0`, no presorted input is ever generated** |
| 4 | sort-strategy | Limit | Finalize GroupAggregate | Sort | 1 | dominated-noncost×2 (−51..−28%), unexpressible×2 |
| 3+2+2 | sort-strategy | Limit | Sort | CTE ss / ssr / ws | 1 | priced-structural×6 (+24..+264%), no-arm×2 — CTE-boundary structural class |
| 3 | error | (root) | — | — | 0 | n/a (SKIP_QUERYGEN both sides) |
| 2+2+2 | aggregation-strategy | Sort / Append / CTE | GroupAggregate / Finalize GroupAggregate | HashAggregate / GroupAggregate | 1–3 | election×4, unexpressible×3, dominated-noncost×2, input×1 |
| 2 | join-method | Nested Loop | Parallel Hash Join | Nested Loop | 8 | unexpressible×2 — NLI-probe context, no hash candidate at that rel |
| 2 | join-order | Nested Loop | Parallel Hash Join | Parallel Hash Join (leaf-set) | 6 | priced-structural×2 |
| 2+2 | qual-placement | Nested Loop / Sort | (same kind) | (same kind) | 2 | unexpressible/no-arm×3, election×2 (0%) |
| 2 | sort-strategy | WindowAgg | WindowAgg | Sort | 3 | priced-structural×2 (+27813%, +36363%) |
| … | | | | | | tail: unexpressible/no-arm dominates; a few election/forced-cheaper/priced-structural (see .txt) |

### TPC-DS SF1 (n=98; 95 measured, 3 both-side `error`)

| n | category | under | PG child | goopg child | depth | margin |
|---|---|---|---|---|---|---|
| 7 | aggregation-strategy | Sort | GroupAggregate | HashAggregate | 2 | election×9, unexpressible×1 — mostly 0% (Q34 cand outlier 277964% excluded from band) |
| 6 | sort-strategy | Limit | GroupAggregate | Sort | 1 | unexpressible×6 |
| 5 | sort-strategy | Limit | Incremental Sort | Sort | 1 | **unexpressible×5 — IS-arm lane confirms `nonemptykeys=0`** |
| 5 | sort-strategy | GroupAggregate | Nested Loop | Sort | 2 | election×5 (0%) |
| 3 | aggregation-strategy | CTE | GroupAggregate | HashAggregate | 2 | election×3 |
| 3+1 | sort-strategy | Limit | Sort | CTE ss | 1 | priced-structural×4 (+24..+104%) |
| 3 | error | (root) | — | — | 0 | n/a |
| 3 | sort-strategy | Limit | Finalize GroupAggregate | Sort | 1 | unexpressible×3 |
| … | | | | | | tail: unexpressible/no-arm dominates; parallelism rows are forced-cheaper/election |

## Findings

1. **The dominant sort-strategy family is a *generation* gap, not a cost
   loss.** For `Limit → PG {GroupAggregate|Incremental Sort} | goopg Sort`
   (~25 records SF0.25, ~14 SF1): every baseline `upper.ordered.seed`
   reports `keys=0` and `nonemptykeys=0` — no path carries ordering
   pathkeys into the sort decision, so neither the presorted-input
   candidate nor `Incremental Sort` can be generated. With
   `GOOPG_INCREMENTAL_SORT=1 GOOPG_PARTIAL_SORT_PATHS=1` the records are
   *still* unexpressible — the missing piece is upstream pathkey
   propagation (GroupAggregate/join output ordering), not the IS
   producer arm. Fix class: structural, pathkeys — matches S5's
   "missing mechanism" band and feeds M0144-0011's candidate layer.
2. **`PG Sort | goopg CTE ss/ssr/ws`** (SF0.25×7, SF1×4): all
   `priced-structural` on DPPATH margins (+24..+264%) — these are the
   single-reference-CTE planning-boundary records (goopg lacks PG's
   `inline_cte` — the M0142-0016c Finding-3 family). Structural, sized.
3. **`dominated-noncost` is a real population** (18 records): PG-family
   candidates ARE offered, priced *cheaper* than the winner (−0.0% to
   −92%), and still lose — dominated on pathkeys or parameterisation.
   Cost work would not move these; the input shape is missing.
4. **Parallelism divergences are mostly election/fuzz, not priced-away**:
   forcing zero parallel costs flips several to PG's parallel shape at
   −0.5% to −21% (forced-cheaper — the serial winner had no real cost
   advantage) or 0.000% (election). Only deep NLI-probe contexts
   (Q12/Q20 sf025) are genuinely unexpressible — parameterised probe
   where no hash/parallel candidate exists at that rel.
5. **aggregation-strategy splits two ways**: `PG HashAggregate | goopg
   GroupAggregate` under Sort is election/dominated-noncost; but where
   goopg picked plain `HashAggregate` vs PG `GroupAggregate` at SF1, the
   forced GroupAggregate is ~0% — election. The `Finalize` phased-agg
   variants that lose are priced-structural (+53%, +56%, +292% TPC-H).
6. **Outliers flagged, not hidden**: candm% values >10⁴ (Q47/Q57/Q75/Q34
   sf1) are cross-rel candidate comparisons — the margin column keeps
   them as `priced-structural` but the absolute numbers compare
   candidates at different subtree scales; treat as "structural, large"
   rather than a literal 480,000% price.

## Resume points

- `baseline-record-not-reproduced` flags ~9 TPC-H/sf025 records where
  HEAD's baseline already diverges from the committed census record —
  the margin is still valid, but the census table will drift; re-running
  0002's instrument on a fresh capture is the maintenance path.
- `unexpressible/no-arm` (56 records) needs GOOPG_* env arms or is
  boundary-structural (CTE/SubqueryScan/HashSetOp) — extending the arm
  map is the next refinement if 0011 wants those margins priced.
- The runner is corpus-agnostic: `--census/--pgplans/--qdir/--qpat` cover
  all three corpora; TPC-H queries were dumped from
  `internal/testutil/tpch.Queries()` (tmp/m0144-0007/tpch-queries).
