# M0146-0211 — upper producers seed from their input path's cost, not a Project wrapper's legacy estimate

Status: done 2026-10-16. Measured −1 at SF0.25 (Q84); the regression is filed as M0146-0212 (R3: kept, not reverted).

## Problem

PG's upper rels price their input at its own cost: `cheapest_input_path` for create_grouping_paths and
create_distinct_paths, and `input_path` for create_ordered_paths. The final target is applied to the scan/join paths
themselves (`apply_scanjoin_target_to_paths`, planner.c). For a partial path the target goes below the Gather, so
there is no node between the upper rel and its input.

- **Plain Vars.** A target of plain Vars is a dummy projection with zero cost delta.
- **Expressions.** Target-expression evaluation (`target->cost.per_tuple × rows`) lands on the projected path. The
  serial and parallel arms share that path below the Gather, so the charge appears in both.

goopg wraps the searched tree in a Project and seeded the grouping, partial-aggregate, ordered and (before
M0146-0210) DISTINCT producers from `legacyDisplayCostOf(child)`. For an unstamped Project that is the child plus
`cpu_tuple_cost × rows`, a charge PG does not make. It landed on the arms built from that seed and not on the
Gather Merge candidates built from the search's own paths.

## Change

- **The helper.** `upperInputPathCost(child)` (cardinality.go) reads the stamped search node through Project
  wrappers, keeping that node's initPlan charge (zero while upper paths are priced; `chargeInitPlans` runs at
  `Plan()`'s tail). The legacy display cost is the fallback for an input the search did not build. It is the
  `create_limit_path` analogue `limitInputPath` already used.
- **Used by:** the DISTINCT seed (M0146-0210's look-through, now through this helper), the grouping seed
  (`groupingpaths.go`), the partial-aggregate per-worker seed (`partialaggpaths.go`) and the ordered seed
  (`upperordered.go`).
- **Unchanged:** the row-only reads (`partialaggpaths.go:248`, `partialsortpaths.go:217`, `upperrel.go:197`).
  `EstimateRows(Project)` already passes through to the child, so their rows agree.
- **Test:** `TestUpperInputPathCost` covers unstamped, Project-over-stamped, and per-worker stamps. A guard asserts
  the fixture's legacy estimate really exceeds the stamped cost.

## Measured

Captures in `analysis/m0146/m0146-0211/`. The fire-set A/B ran HEAD `870869555` against the staged change.

- **TPC-DS SF0.25:** match 85 → 84. **Q84 regressed** (MATCH → SHAPE-DIFF \[sort-strategy, parallelism\]): it now
  plans Sort over Gather (8271.21) where it used to plan Gather Merge (8271.23), PG's shape. The old seed's 0.11
  Project charge had tipped that 0.02 tie. Filed as M0146-0212. Nine other plans changed estimates only.
- **TPC-DS SF1:** match 73 held; 12 plans changed, no verdict moved.
- **TPC-H:** match 20 held; Q2 and Q20 text changed, still MATCH.
- **Values:** SF0.25 sweep PASS=99; TPC-H acceptance arm 24 MATCH; fire-set executions introduced=none.

**Q84 is unexplained.** PG's own formulas give Sort-over-Gather 8143.66 against Gather Merge 8143.70, yet PG elects
the Gather Merge, so a PG mechanism here is not modelled. M0146-0212 traces it with the PG debug build
(`tmp/pgdbg`) rather than restoring a charge chosen by result (R6).
