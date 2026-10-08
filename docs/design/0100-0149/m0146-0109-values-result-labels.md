# M0146-0109 — a FROM-less SELECT or one-row VALUES is a Result; several rows are a Values Scan

Status: done 2026-10-08 (658ec94ad). Parent: M0146-0042.

## Problem

goopg labelled every Values node `Values (N rows)`. PG 18.3 never prints
that label:

| query | goopg | PG |
|---|---|---|
| `select 1`, `select (select 1)`, `values (1)` | `Values (1 rows)` | `Result` |
| `select 1 where false` | `Values (1 rows)` / `Filter: (false)` | `Result` / `One-Time Filter: false` |
| `values (1), (2)` | `Values (2 rows)` | `Values Scan on "*VALUES*"` |
| two VALUES lists joined | `Values (2 rows)` twice | `"*VALUES*"`, `"*VALUES*_1"` |

The goopg-only label appeared in every `(select 1)` InitPlan, every
`select 3` UNION arm and every VALUES list across the regress suite.

## PG behaviour

- `create_valuesscan_plan` is reached only for a VALUES RTE with several
  rows. A one-row VALUES and a FROM-less SELECT are a plain targetlist
  over a Result (`create_projection_plan` / `create_result_plan`).
- A WHERE there is pseudo-constant, so it becomes the Result's
  `resconstantqual`, which EXPLAIN prints as `One-Time Filter:`.
- A VALUES RTE is named `*VALUES*`, and `set_rtable_names` numbers the
  repeats `*VALUES*_1`, `*VALUES*_2`, ….

## Change

- **The label** (`describePlanMode`). One row, or none, gives `Result`.
  Several rows give `Values Scan on <name>`, where the name is `"*VALUES*"`
  or the label the name table assigned.
- **The numbering** (`explainNames.collect`). Multi-row Values nodes claim
  `*VALUES*` through `claimName`, the set\_rtable\_names rule the scans
  use. The node carries no RTID, so walk order stands in for range-table
  order.
- **The qual** (`emitNodeDetailLines`). A WHERE attached to a one-row
  Values prints as `One-Time Filter:`, following the existing literal and
  parenthesis rule. A Values Scan keeps `Filter:`.

## Verification

- **Probe** (PG 18.3 vs goopg). The labels match PG for each of:
  - `select 1`, `(select 1)`, `select 1 where false`;
  - `values (1)` and `values (1), (2)`;
  - VALUES in FROM, two VALUES lists joined, a VALUES under UNION ALL,
    and a VALUES CTE.
- **Test.** `TestExplainValuesAndResultLabels`. Disabling the numbering or
  the One-Time Filter arm each fails it.
- **Regress A/B** (22 files), diff lines HEAD → new:

  | file | HEAD | new |
  |---|---|---|
  | union | 347 | 259 |
  | subselect | 1470 | 1394 |
  | join | 14997 | 14931 |
  | groupingsets | 867 | 839 |
  | partition\_prune | 4391 | 4381 |
  | updatable\_views | 1971 | 1961 |
  | rangefuncs | 1587 | 1581 |
  | window | 1943 | 1937 |
  | limit | 123 | 119 |
  | with | 1707 | 1703 |

- **TPC-DS / TPC-H.** No plan changes; neither corpus has a Values node.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.

## Deferred

- **Column names (ledgered).** A multi-row VALUES's columns are the RTE's
  `column1` … `columnN`, qualified `"*VALUES*".column1`. PG pulls the
  aliasing subquery up, so `v(x)` never names them. goopg prints the alias
  names (`Filter: (x > 1)`, `Hash Cond: (x = y)`).
- **Walk order (ledgered).** The `*VALUES*_N` numbering follows walk
  order, not range-table order.
- **`OFFSET 0` (filed as M0146-0110).** `limit_needed` (planner.c) drops a
  Limit whose OFFSET is a constant 0 and which has no LIMIT. goopg keeps
  it: `select 1 offset 0` prints `Limit -> Result`.
