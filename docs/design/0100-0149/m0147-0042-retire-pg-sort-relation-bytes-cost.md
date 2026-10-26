# M0147-0042 — retire `GOOPG_PG_SORT_RELATION_BYTES_COST` (Sort byte-currency switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 9 → 8 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

PG's `cost_tuplesort` (`postgres/src/backend/optimizer/path/costsize.c`) decides whether a Sort spills by comparing
`relation_byte_size(tuples, width)` against `work_mem`. That figure is the MAXALIGNed pathtarget width plus a heap
tuple header, per row. goopg ports it as `pgRelationByteSize` (`internal/optimizer/sort_pgrelationbytes.go`).

R113 shipped it behind `GOOPG_PG_SORT_RELATION_BYTES_COST`, default off. M0146-0234 turned it on: goopg's own
entry-byte currency (`hashsize.EntryBytes`) priced TPC-DS SF1 Q64's store_sales Sort as a disk sort, where PG's fits
in `work_mem`. `=0` kept the goopg currency as a measurement switch.

## Removed / changed

- `sort_pgrelationbytes.go`: the env read, `pgSortRelationBytesCostFromEnv`, `pgSortRelationBytesCostEnabled` and
  `setPGSortRelationBytesCostForTest`.
- `cost_funcs.go`, `costSortRunWithWidth`: the PG currency is elected whenever `pgRelationByteSize` returns a valid
  figure.
  - The goopg entry-byte currency remains only as the fallback when the planner width is unknown (`width <= 0`, or an
    overflow). This is the same disposition M0147-0021 kept for the hash spill currency.
  - Both currencies are still computed for the `DPPGSORT` trace.
- `flaglabels.go`: the resolver row is deleted, and the variable moves to `flagProvenanceRetired`
  (`retired(M0147-0042)`). `scripts/planner-flags.env` is regenerated.
- The "default off" comments in `ordered_input_narrow.go` and `window_sort_narrow.go` are corrected.

`deadcode ./cmd/...` was diffed before and after. The old test-only setter left the unreachable list; nothing was
added to it.

## Tests

- Six `setPGSortRelationBytesCostForTest(true)` pins are deleted (4 in `sort_pgrelationbytes_test.go`, 1 in
  `partialsortpaths_test.go`, 1 in `windowsetoppaths_test.go`). ON was already the default they ran under.
- `TestCostSortRunPGRelationBytesSwitchOffIsLegacy` is deleted. It pinned only that `=0` reproduced the legacy
  currency.
- `TestPGSortRelationBytesTraceNamesBothCurrencies` keeps its PG-currency half. Its switch-off half now pins the
  fallback: a width-0 Sort elects `currency=goopg` with `pgbranch=unknown-width`.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` finds no setter; only the generated flag table names it. Unset, the variable resolved to ON, which is the
only arm left. The fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0042 entry in `.ralph/fix_plan.md`.
