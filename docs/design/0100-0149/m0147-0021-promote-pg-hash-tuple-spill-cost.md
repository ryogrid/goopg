# M0147-0021 — promote `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108 PG hash-join spill price)

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 0 (promote-or-delete).
Lever count 22 → 21 (`scripts/lever-count.sh`).

## The lever

R108 added PG's hash-join batch decision and batch I/O price (`hashJoinBatchCost`, `internal/optimizer/cost_funcs.go`):
- packed `HashJoinTuple` geometry decides the batch count (`ExecChooseHashTableSize`,
  `postgres/src/backend/executor/nodeHash.c`, ported as `pgHashGeometry`);
- the heap-tuple `page_size` formula prices the batch I/O (`initial_cost_hashjoin`,
  `postgres/src/backend/optimizer/path/costsize.c`, ported as `pgHashSpillPages`).

It shipped behind `GOOPG_PG_HASH_TUPLE_SPILL_COST=1`, default off. The OFF arm priced batches with goopg's
executor-map geometry (`hashsize.Choose` + `spillPages`). The ON arm is PG logic, so R3 promotes it. M0139-0007a had
measured no plan movement ([doc](m0139-0007a-hash-tuple-spill-cost-measurement.md)) but left promotion behind a C3
memory precondition: the planner might charge less memory than goopg's larger executor footprint allocates.

## C3 measurement (memory first)

The arm only changes planner prices, so executor memory can move only through a changed plan. Measured on the HEAD
binary, with artefacts in `tmp/m0147-0021/` (not committed):

| corpus | ON vs OFF goopg plans | ON PLAN-PARITY | arm engaged |
|---|---|---|---|
| TPC-DS SF0.25 | byte-identical (0 diff lines, costs included) | 99 / 99 | `DPPGHASH currency=pg` on ~164k offered hash joins, batches up to 131072 |
| TPC-DS SF1 | byte-identical (0 diff lines) | 99 / 99 | (same capture shape) |

No winning plan or cost digit moved, so the executor allocates exactly what it did before. The task text's
remaining C3 gates also passed on the promoted binary: the acceptance arm (TPC-H SF1, executed, values) and the SF1
fire-set (see the fix_plan entry).

## Removed / changed

- `hashjoin_pgtuplesizing.go`: `pgHashTupleSpillCost`, its env read, `pgHashTupleSpillCostFromEnv`,
  `pgHashTupleSpillCostEnabled` and `setPGHashTupleSpillCostForTest`. `pgHashTupleSpillGeometryFor` no longer checks
  a switch. It still falls back to map pricing (`usePG=false`) when a PG width or geometry is invalid.
  `pgHashGeometry` / `pgHashSpillPages` are kept.
- `cost_funcs.go`: `hashJoinBatchCost`'s comment now says the price is promoted and cites the two PG functions.
- `flaglabels.go`: the variable moves to `flagProvenanceRetired` (`retired(M0147-0021)`), and `planner-flags.env`
  is regenerated.
- `tracePGHashTupleGeometry` keeps its format. Its `currency=` field now reads `map` only on the invalid-width
  fallback. No tool outside the package parses `DPPGHASH` (checked with `git grep`; the estimate-audit tool does not
  read it).

`deadcode ./cmd/...` was diffed before and after. Only the deleted test setter left its list.

## Tests

- `TestPGHashTupleSpillTraceMatchesCostFallbackAndDecision`: the switch-off arm is gone. The valid trace must carry
  `currency=pg`, the PG pages and the packed geometry. The invalid-width trace must carry the map fallback.
- `TestPGHashTupleSpillCostUsesSeparateGeometryAndPageWidths` (renamed from `…Switch…`):
  - the map reference price comes from the unknown-width fallback, because map pricing reads column counts, not
    widths;
  - the fail-closed check now asserts `usePG=false` and exactly `hashsize.Choose` + `spillPages` batch I/O.
- `TestPlannerSettingsReachDerivedTableJoin`, `…SetOpOperands`, `…ScalarSubqueryJoin`. Their `work_mem` arms
  (16 kB on a 100-row filtered build) only ever spilled goopg's wide map geometry. In PG's packed geometry a 100-row
  build fits every budget PG accepts, and below PG's 64 kB minimum the geometry declines.
  - The `work_mem` arms now run at **64 kB** (PG's minimum) on a ~10 000-row build (unfiltered, or `pa.v <> 3`),
    each compared with its own default-settings baseline.
  - The wide `IN` shape is pulled up into a semi-join with no inner plan left to price, so `work_mem` covers the
    other four sublink shapes. IN stays covered by the three cost arms.

## Inert-to-plans proof

The capture diff above is the proof: 0 changed lines on both TPC-DS corpora, and the fire-set A/B on the promoted
tree reports `fires=none`. No script, CI config or env file sets the variable.

## Gates

See the M0147-0021 entry in `.ralph/fix_plan.md`.
