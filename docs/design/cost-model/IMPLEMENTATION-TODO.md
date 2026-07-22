# Cost-Model Implementation — Progress Tracker

Live checklist for implementing [the cost-model design bundle](README.md) per the
[roadmap](11-roadmap.md). One row per sub-step. Status: `[ ]` todo, `[~]` in
progress, `[x]` done. Each done row records its gate result and commit hash.

Standing gates (every commit): `go build ./...`, `go test ./internal/planner/...`,
and the automatic pre-commit pgbench smoke. Additional per-phase gates noted below.

Branch: `introduce-costmodel`. Design of record: `docs/design/cost-model/`.

---

## C0 — Path substrate + create_plan (plan-preserving)

- [x] **C0.0** Doc reconciliation: chapter 09 §3 named `make plan-gate` as a
  goopg-vs-PG registry; it is actually goopg-vs-self-snapshot, with the vs-PG
  classification in `scripts/pg-oracle-diff.sh`. Corrected §3/§5. — _doc-only; gate: n/a_
- [ ] **C0.1** `internal/planner/path.go`: `Cost`, `Path`, `RelOptInfo`, `PathKind`,
  `RelSet`; `addPath`/`setCheapest` + `comparePathCostsFuzzily` (`STD_FUZZ_FACTOR`
  1.01, `disabled_nodes` first). Pure library, no integration. Unit tests:
  dominance, fuzz tie-break, determinism. Gate: units.
- [ ] **C0.2** `internal/planner/createplan.go`: `createPlan(path) Node`; wire at
  the `tryBushyDP` seam so the DP's chosen order round-trips through a (prebuilt)
  Path. Gate: **plan-gate zero diffs** vs `costmodel-c0-baseline`.

## C1 — Pathkeys (minimal)
- [ ] **C1.1** `pathkeys.go`: `PathKey`, `pathkeysContainedIn`; fold the pathkey
  dimension into `addPath`. Gate: plan-gate zero diffs; containment unit tests.

## C2 — Estimation inputs
- [ ] **C2.1** `RelOptInfo.Rows` via `set_baserel_size_estimates` analogue; tuple
  width estimator. Gate: rows-invariant (plan-gate zero diffs).
- [ ] **C2.2** `estimate_rel_size` row fallback off `smgr.NBlocks`. Gate: cold-start
  test (baseRows returns block estimate, not 0).

## C3 — Cost functions + path generation
- [ ] **C3.1** `cost_funcs.go`: per-node PG-unit cost functions reading config GUCs;
  unit checks vs oracle (`get_parallel_divisor(2)=2.4`, …).
- [ ] **C3.2** Generate costed scan + join paths into pathlists (selection still on
  integer DP). `addPath`/`setCheapest` dominance gate. Gate: plan-gate zero diffs.

## C4 — Switch join order to costed pathlists *(first behavior change)*
- [ ] **C4.1** Retire the integer argmin; DP composes via `addPath`/`setCheapest`;
  LIMIT on the startup axis. Gate: milestone bar — 5 regressions recover w/o losing
  Q5; rows byte-identical; plan-gate re-baselined + classified.

## C5 — Parallel paths + parallelize decision
- [ ] **C5.1** Partial paths + `generate_gather_paths`; parallelize = `setCheapest`;
  count = size ladder; partial-agg split as two-path case. Gate: identity gate;
  race-gate; sensible parallelize snapshot.

## C6 — Surface real cost + width in EXPLAIN
- [ ] **C6.1** Real `cost=`/`width=` in `operators_explain.go`. Gate: expected
  plan-gate diff (re-baseline); `rows=` unchanged.

## C7 — Statistics persistence *(deferred; STOP and consult user first)*
- [ ] **C7.1** Append-and-reload `reltuples`/`relpages` + real `stawidth`.

---

## Log

_(newest first; each entry: date — sub-step — gate result — commit)_

- 2026-07-22 — C0.0 — doc reconciliation (ch09 plan-gate) + TODO tracker created — _docs commit_
