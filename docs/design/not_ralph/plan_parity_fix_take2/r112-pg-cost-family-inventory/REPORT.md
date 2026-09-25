# R112 — PG cost-family representation inventory: report

## Outcome

There is no defensible whole-planner "PG Datum size" substitution. The source
inventory confirms that each cost family has a different PG representation and
that Goopg has several distinct `[]Datum`-oriented substitutes. The first
evidence-backed follow-up is a **Sort-only** comparison scope; no production
cost or executor behaviour changed in R112.

## Evidence and reachability

The inventory used Goopg source at R108's implementation boundary and the
read-only PG18.3 `costsize.c` source. A fresh TPC-H plan-only capture used:

```text
GOOPG_BIN=/tmp/r112-goopg AUDIT_BIN=/tmp/r112-estimate-audit \
NO_BUILD=1 PLAN_ONLY=1 PGSHAPED=1 DP_TRACE=1 REFERENCE= \
scripts/tpch-estimate-audit-arm.sh r112-inventory --out /tmp/r112-audit
```

It started a fresh server, warmed the audit session's statistics, and captured
all 22 TPC-H query forms without executing them. The existing R108 SF0.25
99-query EXPLAIN capture was taken from the same implementation source and
served as the TPC-DS census. Selected-plan presence is reachability evidence,
not a claim that every possible alternative was costed.

| family | Goopg representation / source | PG18.3 representation / source | census and disposition |
|---|---|---|---|
| Seq / parallel Seq Scan | `costSeqscan` and `costParallelSeqscan` consume catalog `relPages` and tuple counts, not a Datum-sized row. | `cost_seqscan` also prices relation pages and tuples. | No R112 size substitution candidate. |
| Index / bitmap scan | `costIndexScanCore`; fallback `indexTupleWidth` models declared index columns plus `IndexTupleData` and line-pointer overhead. | `cost_index`, `cost_bitmap_heap_scan` use index/heap page estimates, separately from result width. | Separate candidate only if page/statistics provenance differs; never feed result Datum width into it. |
| Sort and Sort consumers (merge input, WindowAgg, SetOp) | `costSortRun` computes input/output bytes with `hashsize.EntryBytes(ncols, avgVarBytes)`. | `cost_tuplesort` calls `relation_byte_size(tuples, width)`, namely `MAXALIGN(width) + MAXALIGN(SizeofHeapTupleHeader)`. | **Candidate.** Selected Sort appears in 17/22 TPC-H plans and 89/99 TPC-DS plans. The TPC-H trace also records 22 `upper.ordered.sort` offers, proving offers beyond selected winners. |
| Hash Join | Default pricing still uses `hashsize.Choose`; R108's opt-in has separate packed `HashJoinTuple` batch and heap-header page formulas. | `ExecChooseHashTableSize`, `page_size`, `initial_cost_hashjoin`. | Already investigated by R108; its on/off census is unchanged. No R112 code. |
| HashAggregate / grouping | `costAgg` has an existing spill arm using `hashAggEntrySize`, `hashAggSetLimits`, and `inAvgVarBytes` I/O pricing. | `cost_agg`, `hash_agg_entry_size`, `hash_agg_set_limits`, `relation_byte_size`. | Candidate requiring an executor-correspondence audit first. Selected Hash/Group Aggregate appears in 17/22 TPC-H and 81/99 TPC-DS plans. |
| Memoize | `costMemoizeRescan` estimates cached rows and keys through `hashsize.EntryBytes(ncols, 0)` plus Goopg-specific overhead. | `cost_memoize_rescan` combines `relation_byte_size`, `ExecEstimateCacheEntryOverheadBytes`, and parameter expression widths. | Candidate, but lower-priority reachability: 1/22 TPC-H and 36/99 TPC-DS selected plans. |
| Materialize / rescan | Nested-loop execution materializes implicitly; there is no selected `Materialize` path node. `pathRescanCost` has a separate `relationByteSize` approximation. | `cost_material`, `cost_rescan` use relation-byte volume for explicit paths. | No direct selected path family; do not alter executor buffering from planner evidence. |
| Gather / Append | Their Goopg cost functions do not consume a row byte-volume for an election. | `cost_gather`, `cost_gather_merge`, `cost_append`, `cost_merge_append`. | Already-faithful / no R112 size substitution candidate. |

The selected-node census also found Hash Join in 12/22 TPC-H and 84/99 TPC-DS
plans, confirming that R108's no-movement result was not caused by the family
being absent. It was caused by one-batch PG geometry in the measured
candidates.

## Reverted diagnostic census

The existing traces do not expose every price input, so this inventory used a
temporary, default-off `R112COST` stderr diagnostic. It reported inputs after
they had reached `costSortRun`, `costAgg`, or `costMemoizeRescan`, but changed
no price, cardinality, path, allocation, or executor behaviour. The patch was
removed before this report was staged. The reproducible, plan-only captures
were:

```text
GOOPG_R112_COST_FAMILY_TRACE=1 GOOPG_BIN=/tmp/r112-trace-goopg \
AUDIT_BIN=/tmp/r112-trace-estimate-audit NO_BUILD=1 PLAN_ONLY=1 \
PGSHAPED=1 DP_TRACE=1 REFERENCE= \
scripts/tpch-estimate-audit-arm.sh r112-trace-tpch --out /tmp/r112-trace-tpch

GOOPG_R112_COST_FAMILY_TRACE=1 GOOPG_PGSHAPED_DP_TRACE=1 \
GOOPG_BIN=/tmp/r112-trace-goopg SF025_NO_BUILD=1 \
SF025_RESULTS_DIR=/tmp/r112-trace-ds SF025_PLANS_BASELINE=none \
scripts/tpcds-sf025-regression.sh plans
```

The temporary artifacts are `/tmp/r112-tpch-costtrace.txt`,
`/tmp/r112-ds-costtrace.txt`, `/tmp/r112-trace-tpch/`, and
`/tmp/r112-trace-ds/`. Counts are calls during candidate construction, rather
than selected nodes, so repeated upper-path exploration is expected.

| family | TPC-H calls and inputs | TPC-DS SF0.25 calls and inputs | decision / competing-path evidence |
|---|---|---|---|
| Sort | 2,530 calls; rows 2–3.60e10, `ncols` 2–57, `avgvar` 0–2,688, 128 MiB work memory; 1,234 memory and 1,296 disk branches. | 78,980 calls; rows 2–1.07e18, `ncols` 1–275, `avgvar` 0–1,728, 1 GiB work memory; 72,361 memory, 6,610 disk, and 9 bounded branches. | Reached in both memory regimes. `DPPATH` recorded 22/105 `upper.ordered.sort` and 17/142 `upper.groupagg.sort` offers, alongside 17/141 `upper.groupagg.hashed` offers: a genuine price comparison exists. |
| HashAggregate | 26 calls; rows 0–6.00e6, `ncols` 2–47, `avgvar` 32–3,122; batches 1–21 and depth 0–1 at 128 MiB. | 433 calls; rows 0.323–687,746, `ncols` 0–278, `avgvar` 0–4,704; batches 0–1 and depth 0 at 1 GiB. | Reached, but only TPC-H exercised its spill depth. The sort/hashed offers above establish an upper-group alternative; R112 does not establish executor correspondence, so no follow-up is authorized for it. |
| Memoize | 445 calls; `ncols` 3–16, keys 0–2, entry bytes 384–2.74e6, cache entries 49–349,525 at 128 MiB. | 7,280 calls; `ncols` 3–34, keys 0–2, entry bytes 384–2.97e6, cache entries 360–2.80e6 at 1 GiB. | Reached, but this trace has no memoize-specific offered-path provenance; selected presence alone does not prove a competing price decision. Keep it out of R113. |

The trace also records that all 26 TPC-H and all 433 TPC-DS Aggregate calls
reached strategy 0; the TPC-H depths split 22 at zero and four at one. A
diagnostic-free A/A repeat used the same `/tmp/r112-trace-goopg` binary with
the trace unset. After normalizing volatile cost/row fields, capture headers,
and generated SQL paths, its 22 TPC-H and 99 TPC-DS plan shapes were each
byte-identical to the traced arm. Thus the diagnostic itself is not a plan
signal; R112 asserts no cost or plan difference.

## Decision

Do not generalize `DatumBytes`, `hashsize.EntryBytes`, `Path.OutputWidth`, or
any PG tuple header as a universal value. The first follow-up may investigate
only `costSortRun`'s planner spill price, with emitted-width provenance and
executor-sort capacity treated as separate coordinates. HashAggregate and
Memoize remain separately scoped candidates; scan/index/materialization must
not be folded into a Sort change.
