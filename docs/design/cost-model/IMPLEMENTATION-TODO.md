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
- [x] **C0.1** `internal/planner/path.go`: `Cost`, `Path`, `RelOptInfo`, `PathKind`,
  `RelSet`; `addPath`/`addPartialPath`/`setCheapest` + `comparePathCostsFuzzily`
  (`STD_FUZZ_FACTOR` 1.01, `disabled_nodes` first) + `comparePaths` 4-axis
  dominance (cost/pathkeys/parallel_safe/required-outer). Minimal `pathkeys.go`
  (`PathKey`, `pathkeysContainedIn`, `comparePathkeysDim`) landed here since
  `addPath` needs the pathkey axis; C1 adds the produce/consume wiring. Pure
  library, no integration. Gate: units PASS (`path_test.go`, 13 tests), vet clean,
  full planner suite green.
- [x] **C0.2** `internal/planner/createplan.go`: `createPlan(path) Node` +
  `createPlanFromDPChoice`, wired at `bushy.go:207` (the `tryBushyDP` seam). In C0
  it is a proven **identity** (`createPlan(PathPrebuilt)` returns the same Node
  pointer), so plans are pointer-identical — a stronger guarantee than byte-
  identical EXPLAIN. Captured the warm (ANALYZE'd) reference
  `plan_snapshots/costmodel-c0-baseline.txt` (22 queries, stats-driven + parallel
  plans) for the C1–C3 gates. Gate: full planner suite green (incl. all
  bushy/joinorder/tpch plan-shape tests) + 3 createplan unit tests; live
  `make plan-gate` deferred to C4 where plans first change (C0.2 cannot change a
  plan by construction).

## C1 — Pathkeys (minimal)
- [x] **C1.1** `pathkeys.go`: core (`PathKey`, `comparePathkeysDim`,
  `pathkeysContainedIn`, pathkey axis in `addPath`) landed in C0.1; C1 completes the
  API with `pathkeysForSortKeys` (the `make_pathkeys_for_sortclauses` analogue,
  consumed from C3/C5) and 9 tests: prefix-satisfies, longer-requirement-fails,
  direction/NULLS mismatch, the deliberate syntactic false-negative, dominance of
  the longer ordering, divergence→incomparable, and the SortKey→PathKey produce
  helper. Plan-preserving (no live consumer yet). Gate: units PASS, suite green.

## C2 — Estimation inputs
- [x] **C2.1/C2.2** `internal/planner/relsize.go`: `baseRelRows`
  (`set_baserel_size_estimates` analogue), `tupleWidth`/`typeWidth` (type-derived
  width, needs no stats), and `estimateRelSizeRows` (the `estimate_rel_size`
  density fallback off a live block count). Pure helpers — no live caller yet
  (wired at C3/C4), so plan-preserving by construction. Gate: 4 unit tests
  including the **cold-start headline** (`baseRelRows(0, blocks, width) > 0` — a
  block estimate, not 0 — while a warm RowCount wins); suite green. Live wiring +
  the block-count source (`smgr.NBlocks` via `BlocksForTable`) plug in at C3.

## C3 — Cost functions + path generation
- [x] **C3.1** `cost_funcs.go`: per-node PG-unit cost functions —
  `getParallelDivisor`, `costSeqscan`, `costSortRun`, `hashJoinCost`,
  `nestloopCost`, `mergeJoinCost`, `gatherCost`, `aggCost` — plus `costParams` and
  `defaultCostParams`. 7 unit tests vs hand-computed oracle values
  (`getParallelDivisor(2)=2.4`, `costSeqscan(1000,100000,1)=2250`, build-is-startup,
  gather setup+transfer) + a **drift guard** asserting `defaultCostParams` equals
  the config-registered GUC BootVals. Pure, no live caller. Gate: units PASS.
- [x] **C3.2** `internal/planner/pathgen.go`: path-generation primitives —
  `generateScanPaths` (serial + divisor-divided partial) and
  `generateHashJoinPaths` (both build orientations; `add_path` keeps the cheaper,
  so the small dimension builds because it is cheaper, not by name-tag). 4 tests
  incl. the build-side-falls-out-of-cost dominance case. Pure functions; the
  DP-traversal wiring and merge/nestloop/MHJ generation land with C4 (where
  selection switches). Plan-preserving. Gate: units PASS, suite green.

## C4 — Full-fidelity cost-driven join order *(first behavior change)*
**Revised after the C4 binary-only attempt (see log + ch07 §4.5).** A binary-hash-
only order switch is UNSAFE: it recovered Q8 (200→21s) + Q5 (18→5s) but regressed
Q9 (27→>250s) because goopg packs MHJ and converts NL-index AFTER the DP, so
binary-hash cost ≠ real execution. The DP must cost the actual execution shape.
Direction chosen by user: **full-fidelity DP (MHJ/NLI-aware)**. The binary-only
attempt is stashed (`git stash list`: "C4 cost-driven join order (binary-hash…)").
- [~] **C4a** NL-index / MHJ costing.
  - [x] **C4a-i** *Primitives (pure, not wired — plan-preserving).* `cost_funcs.go`:
    `indexProbeCost` (one selective index probe) + `multiHashJoinCost` (ch06 §4.1
    comparability). `pathgen.go`: `generateNLIPath` (parameterized, RequiredOuter)
    + `generateMultiHashJoinPath`. `relsize.go`: `nodeTupleWidth`/`estScanPages`
    helpers. Tests incl. the **Q9 lesson** (NLI ruinous over a 6M-row outer, cheap
    over 100). Gate: units PASS, suite green.
  - [ ] **C4a-ii** *Wire NLI/MHJ generation into the DP* + measure. Blocked on three
    concrete sub-problems the integration analysis surfaced (each needs care +
    SF1 iteration; the DP switch is stashed until they land):
    1. **Composite-index NLI detection in the DP.** `pickIndexCoveringAllLeadingColumns`
       (`nl_index_join.go:950`) needs EVERY leading index column bound. The DP
       composes one edge at a time (`findEdgeBetweenIdx`), so a composite index —
       e.g. `partsupp_pk` on (ps_partkey, ps_suppkey), which Q9's good plan probes —
       is invisible at single-edge composition. The DP must collect ALL edges
       between the two masks and build the inner→outer column-NAME map, bridging its
       global-column-index coordinates to `tryBuildNLI`'s schema-name model.
    2. **MHJ structural constraints.** goopg's MHJ packs SINGLE-column hash keys
       only; a composite-key join (partsupp) cannot go in the MHJ and must be
       NLI/separate. The bad C4 plan wrongly put partsupp in the 4-way MHJ; the good
       plan keeps orders in the MHJ and partsupp as NLI. The DP must model this to
       pick the right MHJ membership.
    3. **In-memory cost-constant calibration.** goopg's indexes/heaps are in-memory
       at SF1; PG's disk-based `random_page_cost=4` / `seq_page_cost=1` mis-rank
       goopg's plans (an index probe is CPU-bound, ~0.02, not ~8 = 2·random_page).
       The PG-oracle constants likely need goopg calibration from SF1 profiling — a
       real, documented deviation from the "PG as oracle" premise (design impact:
       ch02 §3 / ch09).
    Gate: Q9 does not regress; the 5 regressions recover; Q5 held; SF1-measured.
- [ ] **C4b** MHJ-aware costing wired: for a subset whose binary shape packs into a
  `MultiHashJoin`, generate the MHJ path under the ch06 §4.1 comparability invariant
  (costed vs the equivalent left-deep hash cascade). Targets Q2/Q22. Lands with C4a-ii.
- [ ] **C4c** Switch selection to the costed pathlists + measure. Gate: the milestone
  bar — the 5 regressions recover WITHOUT regressing Q9 or losing Q5, on SF1;
  plan-gate re-baselined + `pg-oracle-diff` classified.

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

- 2026-07-22 — C4a-i — NLI/MHJ cost + path-gen primitives (indexProbeCost, multiHashJoinCost, generateNLIPath, generateMultiHashJoinPath) + size helpers; pure/plan-preserving; Q9-lesson test PASS
- 2026-07-22 — C4 attempt (binary-hash-only, STASHED not landed) — SF1 serial+stats: recovered Q8 200→21s, Q5 18→5s; but REGRESSED Q9 27→>250s (post-DP MHJ/NLI mismatch). Q2/Q4/Q12/Q22 unrecovered (their causes are outside the DP: semi-join method, MHJ, build-side). Full-fidelity DP (MHJ/NLI-aware) chosen; see ch07 §4.5. C0-baseline + costmodel-c4 snapshots + sf1-r4-w0-serial are the references.
- 2026-07-22 — C3.2 — pathgen.go: scan (serial+partial) + hash-join (both orientations) generation primitives + 4 tests; plan-preserving — `d92d10a9`
- 2026-07-22 — C3.1 — cost_funcs.go: 8 per-node PG-unit cost functions + 7 oracle tests + config drift guard; plan-preserving — `9297cf7a`
- 2026-07-22 — C2.1/C2.2 — relsize.go: baseRelRows + width estimator + estimate_rel_size fallback; cold-start test PASS; plan-preserving — `1e5b5a4f`
- 2026-07-22 — C1.1 — pathkeys API completed (pathkeysForSortKeys) + 9 tests; plan-preserving, suite green — `b4770527`
- 2026-07-22 — C0.2 — create_plan seam wired at bushy.go:207 (identity in C0); warm baseline captured; suite+createplan tests green — `b44998b5`
- 2026-07-22 — C0.1 — Path/RelOptInfo/add_path/set_cheapest + minimal pathkeys; 13 unit tests PASS, vet+suite green — `4fee8d87`
- 2026-07-22 — C0.0 — doc reconciliation (ch09 plan-gate) + TODO tracker created — `2dc44de8`
