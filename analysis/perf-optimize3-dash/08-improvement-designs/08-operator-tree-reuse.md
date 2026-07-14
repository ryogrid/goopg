# 08-08 — Operator-tree reuse (reset-and-rebind executor state)

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch,
G-perf → [README](README.md)

## 1. Problem and numbers

goopg rebuilds the executor operator tree per query. From 06-03: `opOpen`
+ `BuildFastIterator` is ~19.5 % gross of `-S` CPU (~10 % net of the eager btree
probe); the allocations feed the ~14–15 KB/query volume (doc 12). Confirmed
unchanged at scale 500 (07-02: `opOpen` incl. the eager `Rescan` probe = 16.8 %
of `-S` CPU). PostgreSQL reuses a prepared plan's executor state across
executions (portals, `ExecReScan`); goopg reconstructs it.

## 2. Current-code map (verified at `a640d2b0`)

- **`opTreeSlab`** — `internal/executor/opnode.go:215`: the backing `[]OpNode`
  slab; nodes hold `*opTreeSlab` + `int32` index (GC-light design, :211–223).
- **`BuildFastIterator(plan)`** — `opnode.go:395`: constructs the `OpIterator`
  from a `planner.Node` — runs per query.
- **`opOpen(tree, idx, ctx)`** — `opnode.go:472`: opens a node against a
  `Context` — the per-execution setup. The eager btree probe
  (`indexScanOp.Open→Rescan`) runs here (06-03), so part of `opOpen` is real work
  reuse cannot remove.

## 3. PostgreSQL reference

- `src/backend/executor/execMain.c` / `execAmi.c` — `ExecutorStart` builds the
  `PlanState` tree once per portal; `ExecReScan` (`execAmi.c`) resets it for
  re-execution without rebuilding. A cached (generic) plan
  (`plancache.c`) reuses the `PlannedStmt`; the executor state is rebuilt per
  execution but from an already-planned tree, and `ExecReScan` avoids even that
  for correlated re-scans.

## 4. Target design

Split operator construction into a **build-once** structural phase and a
**reset-and-rebind** per-execution phase:

- Cache the built `OpIterator`/`opTreeSlab` structure keyed by the plan (the
  prepared-plan cache already exists for extended protocol,
  `dispatch_extended.go:79`; extend reuse to the operator tree, not just the
  plan node).
- Per execution, `Reset` rebinds parameters + the snapshot + the `Context`
  instead of reallocating the node slab — the `ExecReScan` analog. The eager
  probe still runs (real work), but the slab allocation and node wiring do not.

### Decision log

- **D1 — reuse is gated on the prepared/extended path.** Simple protocol
  interpolates literals, so each SQL text is unique and the plan cache does not
  help (06-03) — the operator-tree reuse therefore pays off primarily under
  `-M prepared` / prepared statements, which doc 09 makes the default benchmark.
  This couples doc 08 to doc 09: reuse is measurable only once the extended path
  stops per-Execute teardown.
- **D2 — keep the slab design.** The `*opTreeSlab`+index model
  (`opnode.go:211`) is already GC-light; reuse extends it (reset the slab's
  per-execution fields, keep the backing array) rather than replacing it.
- **D3 — the eager probe stays.** `indexScanOp.Open→Rescan`'s btree descent is
  real execution (9.6 % of CPU, 06-03); reuse removes the *build*, not the
  *probe*.

## 5. Invariants and failure modes

- **I1 — a reset tree produces identical results.** Rebinding params/snapshot on
  a reused tree must be indistinguishable from a fresh build — row counts are the
  gate (G-tpch Q12/Q13 tripwires; a reuse bug that leaks stale state across
  executions is exactly the silent-regression class).
- **F1 — stale state across executions.** Any per-execution mutable field
  (arena slots, snapshot, param values, iterator position) must be reset;
  `pattern_sibling_paths_must_agree` — the build path and the reset path must set
  the same fields.
- **F2 — plan invalidation.** A cached operator tree must be invalidated when its
  plan is (DDL — `dispatch_extended.go:335` already invalidates the plan cache;
  the operator-tree cache hangs off the same lifetime).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | reset protocol | add `Reset(ctx, params, snapshot)` to `OpIterator`/operators; rebind without reallocating; verify identical results on repeated execution. | G-race, G-tpch |
| S2 | cache the tree | hang a built `OpIterator` off the prepared-plan cache entry; reuse across `Execute`s; invalidate with the plan. | G-tpch, D-002 |
| S3 | perf acceptance | `-S -M prepared` (needs doc 09): `opOpen`+`BuildFastIterator` build share drops; alloc volume falls (doc 12). | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| operator correctness on re-execution | `internal/executor/*_test.go` | S1 |
| prepared-statement reuse | `internal/server/extended_test.go` | S2 |
| TPC-H row counts (silent-regression tripwire) | `scripts/tpch-spotcheck.sh` | S1, S2 |

## 8. Performance verification

`-S -M prepared` at scale 100 (after doc 09): `opOpen`+`BuildFastIterator` build
share (~10 % net) drops; per-query alloc volume falls toward the doc-12 target;
`-S` TPS rises toward PG's prepared ceiling (281,941 in `prep100_a640d2b0`).

## 9. Open questions

- **O-OT-1** — How much of the 16.8 % `opOpen` is the removable build vs. the
  unremovable eager probe at scale? Split them in the profile before S3 to set a
  realistic target.
- **O-OT-2** — Thread-safety of a shared cached operator tree across concurrent
  executions of the same prepared statement (per-execution state must be
  instance-local, not slab-shared).
