# M0140-0006b-2 — wire parallel Gather consideration into the Phase-4 upper-rel pipeline

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0006b-2
**Parent:** M0140-0006b (`m0140-0006b-partial-append-cost-producer.md`)
**Depends on:** M0140-0006a (branch-rel threading), M0140-0006b (the partial producer)
**PG oracle:** `postgres/src/backend/optimizer/path/allpaths.c:3236` (`generate_useful_gather_paths`; upper-rel callers e.g. `create_grouping_paths`, `create_window_paths`, `create_setop_paths` each call `generate_gather_paths` on their own rel)

## Problem

M0140-0006b's acceptance check answered "does `generateUsefulGatherPaths`
read the new `PartialPathlist` for free?" with **no**: its three call sites
(`gatherpaths.go` `addBaseRelGatherPaths`, `joinsearchlevel.go`,
`geqo.go`) all walk a `*searchCtx`'s own joinrels, and every Phase-4
upper-rel producer (`createWindowPaths`, `createOrderedPaths`,
`electOrderedGrouping`, `createSetOpPaths`, …) runs from `planner.go`
entirely outside any `*searchCtx`. No upper rel ever had a Gather reader —
not just SETOP. This task wires one.

## What landed

`generateUpperRelGatherPaths(rel, cp)` (`internal/optimizer/gatherpaths.go`),
called once per rel after all other candidates are offered and before
`setCheapest`, at four sites:

- `createSetOpPaths` — the live site (after `addPartialSetOpPath`, which
  stamps the `ConsiderParallel` flag the reader gates on, so ordering
  matters);
- `createOrderedPaths`, `electOrderedGrouping` (after its per-candidate
  loop; the decline `restore` resets `Pathlist`, so a filed Gather cannot
  leak past a decline — and a hypothetical Gather winner hits the existing
  `default: restore("winner-shape-unexpected")`), `createWindowPaths`.

## Sizing decision (the task's own recon)

The task left open "trimmed struct? full `*searchCtx`? package knob?".
Answer: **neither — a two-argument helper delegating to the one body.**
Everything the method reads off the context is available without one:
`parallelModeOK(cp)` is a pure function of the currency
(`considerparallel.go:61`), `cp` is already in scope at every producer,
`trace` methods are nil-safe, and `nrels` only feeds the `top`-mode
final-rel check. The helper builds the minimal `&searchCtx{parallelModeOK,
cp}` and calls the real method — no twin body to keep in step (Hard-won
Rule #2). Currency is sound: the search's own `cp` comes from
`ctx.settings.costParams()` (`joinsearchseam.go:756`) and the upper sites
use the same statement settings' `costParams()`, the same precedent
`addPartialSetOpPath` already relies on.

The one policy stated rather than delegated is **`top` mode refuses upper
rels, fail-closed**: `top` admits only the search's FINAL rel and an upper
rel is never a search member (a zero-value `nrels` would otherwise admit
`relids-0` upper rels by arithmetic accident). `all` (default since
M0140-0003) admits; `off` refuses inside the delegated call.

## Why only four sites

`electOrderedDistinct` (DISTINCT) and `addGroupingPaths` (GROUPING) were
deliberately not wired: no producer files partials on any upper rel except
the SETOP rel, so a call there would be provably-dead surface today, and a
future producer on those rels adds the one-line helper call as part of its
own task (the 0006b lesson, applied forward). Known constraint recorded for
that future: `createOrderedPaths` returns `createPlanNode(best)` verbatim,
so a future ordered-rel partial producer whose Gather wins would need an
election-shape guard (a Gather is unordered — returning it as the ORDERED
winner would drop the ORDER BY). No code guards this today because no such
producer exists; this sentence is its resume point, not a ledger row (no PG
behavior is left unimplemented by this task).

## Acceptance (all gates PASS)

- `go build ./...`, `go vet` clean; `go test ./internal/optimizer/...`
  `./internal/executor/...` green (6 new tests in
  `gatherpaths_upperrel_test.go`: offer over a partial SetOp, refuse under
  `top`/`off`/parallel-disabled/`ConsiderParallel=false`, no-op without
  partials — the WINDOW/ORDERED/GROUP_AGG inertness pin).
- `TestCreateSetOpPathsPartialPathDoesNotMoveThePlan` updated: a Gather IS
  now generated over the fixture's partial SetOp but the serial seeds
  (100+100) dominate its price (~17 + `parallel_setup_cost`), so `add_path`
  prunes it — winner and emitted node unchanged.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34 canonical).
- `scripts/tpcds-sf025-regression.sh sweep`: PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99 changed=0.
- Parity floor, transitive (no fresh capture — `PARITY: N/A` reason below):
  TPC-DS goopg plans are byte-identical before/after (the sweep's 99/99),
  so parity-after == parity-before and the `match >= 2` floor (S2b-7,
  2026-09-17i) holds. TPC-H plans are identical by construction: 0/22
  queries contain a set operation (verified by grep over
  `tmp/c7-tpch-queries/`), so the live site never fires, and the other
  three sites are provably inert (unit-pinned) — the `match=8` floor (P0-E7)
  holds.
- `make ea-ratchet`: N/A — no estimate/selectivity path touched (costing
  wiring only, same disposition as fix2r/sweep-a).
- `tpch-acceptance-arm`: not run — required only for statistics/costing/
  executor changes; none touched. Values risk is covered by the sweep's
  MISMATCH=0 (TPC-DS) and the no-setop proof (TPC-H).

**Movement: none** — reachability plumbing; no corpus plan moved (sweep
99/99 identical, TPC-H identical by construction). The chain is now live:
with the reader wired and 0006c's whitelist landed, a UNION ALL over
bare-scan branches can elect a Parallel Append when it wins on cost
(0006c-2 widens the admitted branch kinds next).
