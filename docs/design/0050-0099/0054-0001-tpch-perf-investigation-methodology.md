# Design Doc 0054-0001 — TPC-H Performance Investigation Methodology

**Status:** accepted
**Milestone:** 0054 — TPC-H Performance & Optimisation Follow-Through
**Author:** Ralph (autonomous agent)
**Date:** 2026-05-05

## 1. Purpose

Define exactly how M0054 sub-tasks (M0054-0002 EXPLAIN audit, M0054-0004
pprof survey, M0054-0005 perf fixes, M0054-0007 run-012) collect
empirical evidence so reviewers do not have to re-derive the procedure
each loop. Also define the acceptance bar so a task cannot be closed
on hand-waving.

## 2. Two-track investigation harness

M0054 runs investigation in two complementary tracks:

### 2.1 Fast iteration track (10 % SF=1 synthetic data)

Used by M0054-0002 (index audit) and M0054-0003 (close gaps) for
CI-friendly per-PR feedback.

- Schema and sample data loaded via existing helpers in
  `internal/testutil/tpch/`:
  - `tpch.DDL()` — full TPC-H schema as goopg `CREATE TABLE` statements
    (mirrors HammerDB's table shapes).
  - `tpch.SampleInserts()` — small deterministic row sets per table.
- Scaling plan: load 10 % of SF=1 row counts (e.g. 600 K lineitems,
  150 K orders, etc.) using `tpch.SampleInserts()` repeatedly, or by
  generating deterministic synthetic rows in the test fixture. The
  intent is fast iteration — full SF=1 is reserved for M0054-0007
  run-012.
- After load, run `ANALYZE` on every table.
- For each TPC-H query Q1..Q22, capture
  `EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) <query>`. The query text
  comes from `bench/tpch/queries/` if present, otherwise transcribed
  from the TPC-H specification with parameter substitutions matching
  HammerDB's defaults.

### 2.2 End-to-end track (full SF=1 via HammerDB)

Used by M0054-0004 (pprof survey) and M0054-0007 (run-012).

- Full HammerDB-driven SF=1 build via `bench/tpch/setup_goopg.sh
  --reset` + `bench/tpch/build_schema_goopg.sh` + ANALYZE.
- Power test via `bench/tpch/run_power_test_goopg.sh` with the existing
  2-h timeout policy.
- pprof captured via the in-tree `pprof-all.sh` against the running
  goopg process at `127.0.0.1:6060`.

## 3. EXPLAIN capture procedure

### 3.1 Static plan capture (no runtime stats)

```sql
EXPLAIN (FORMAT JSON, VERBOSE)
<query>;
```

Used by M0054-0002 baseline assertions. The JSON output is stable
under M0018 (`internal/executor/operators_explain.go`, see
`TestExplainFormatJSONProducesValidJSON`). The test snapshots the
JSON and asserts on key plan-shape facts:

- Top-level node type (`Projection` / `Aggregate` / `Sort`).
- Presence/absence of `Index Scan` vs `Seq Scan` per table reference.
- Join algorithm (`Nested Loop` / `Hash Join` / `Merge Join`) and
  build side.
- For each `Index Scan`: the index name (which proves the planner
  picked the right index, e.g. `LINEITEM_PART_SUPP_FKIDX`).

### 3.2 Runtime stats capture (M0054 perf fix verification)

```sql
EXPLAIN (ANALYZE, FORMAT JSON, TIMING ON, SUMMARY ON)
<query>;
```

Per-node `Actual Rows`, `Actual Loops`, `Actual Total Time`, and
top-level `Planning Time` / `Execution Time` are emitted. The
M0054-0005 before/after evidence quotes the runtime stats for the
specific node that changed.

### 3.3 Tooling

- `psql` against the live goopg cluster. The bundled libpq lives at
  `./postgres/local_install/{bin,lib}` (LD_LIBRARY_PATH required).
- For test-fixture EXPLAIN, the existing
  `internal/executor/explain_*_test.go` patterns drive plans through
  the in-process executor.

## 4. pprof capture procedure

### 4.1 Endpoint

`cmd/goopg/main.go:141-157` binds `127.0.0.1:6060` via
`net/http/pprof` (side-effect import) on `goopg start`.

### 4.2 Profiles to capture

Per the existing `pprof-all.sh` (in repo root, currently untracked —
M0054-0004 promotes it to a tracked file):

| Profile | Endpoint | Duration | Format |
|---------|----------|----------|--------|
| CPU | `/debug/pprof/profile?seconds=30` | 30 s window | binary `.prof` |
| Heap | `/debug/pprof/heap` | snapshot | binary `.prof` |
| Mutex | `/debug/pprof/mutex` | snapshot | binary `.prof` |
| Block | `/debug/pprof/block` | snapshot | binary `.prof` |
| Goroutine | `/debug/pprof/goroutine?debug=2` | snapshot | text |

For mutex / block to be populated, set `runtime.SetMutexProfileFraction(1)`
and `runtime.SetBlockProfileRate(1)` at goopg start. M0054-0004 wires
these (with a GUC or env-var gate) since they are off by default for
production safety.

### 4.3 Capture timing for run-011 follow-up

Three windows during the M0054-0004 power test:

| Window | When | Duration | Goal |
|--------|------|----------|------|
| W1 | During Q9 (long join) steady state — start ~30 s after Q9 begins | 60 s | Identify Q9 hot path |
| W2 | During Q20 (correlated EXISTS) — start ~30 s after Q20 begins | 60 s | Identify Q20 hot path |
| W3 | At end of run (if reached) or right before timeout | snapshot | End-state heap / goroutine survey |

### 4.4 Analysis output

`go tool pprof -top -cum <profile>.prof` for each profile.
The M0054-0004 deliverable
(`analysis/tpch-pprof-bottleneck-survey.md`) lists, per profile:

- Top 10 functions by cumulative cost (with file:line).
- The 3 functions with the most actionable optimisation potential
  (i.e. clearly fixable, not "needs a redesign").

## 5. Acceptance bar per sub-task

A reviewer can close the task if and only if:

| Sub-task | Acceptance bar |
|----------|---------------|
| M0054-0001 | Regression test runs `kill -9 goopg`, restarts, asserts user database is present and queryable. Test runs in CI as part of `go test ./internal/initdb/...`. |
| M0054-0002 | `analysis/tpch-explain-baseline.md` lists ≥ 22 queries with full plan trees and the gap list; the test fixture asserts the snapshot. |
| M0054-0003a..N | Each gap closed produces an EXPLAIN diff committed to the M0054-0002 fixture. Manual inspection: the new EXPLAIN shows the expected `Index Scan` (or other algorithm change). |
| M0054-0004 | `analysis/tpch-pprof-bottleneck-survey.md` lists the top 10 / top 3 / top 3 hotspots with concrete next steps. |
| M0054-0005 | Three named code changes; each cites a before/after `pprof -top` slice or a before/after EXPLAIN ANALYZE timing slice. |
| M0054-0006 | `internal/executor/operators_nljoin_test.go` exists, tests result-parity vs HashJoin, EXPLAIN renders `Nested Loop`. Planner rule has unit tests for "picks NLI" / "picks HashJoin" cases. |
| M0054-0007 | run-012 hits 22/22 within 7200 s, OR every uncompleted query is named with a concrete follow-up sub-task. |

## 6. Gap-fix accounting model

For M0054-0003 and M0054-0005, every gap closure is recorded as a
small, reviewable entry:

```
### Gap N: <one-line summary>
- Before EXPLAIN: <quoted node line>
- After  EXPLAIN: <quoted node line>
- Code: <files changed, function names>
- Tests: <test names>
```

These entries live in the M0054-0002 baseline test's expected output
(so an EXPLAIN diff is mechanically detectable) and are summarised in
the relevant analysis report.

## 7. Anti-pattern register

The following are **explicitly forbidden** under the M0054 no-deferral
clause:

- Closing a task by claiming it is "out of scope" with no decomposition.
- Closing a task with "this needs more investigation" — investigation
  IS the work; do it now.
- Closing M0054-0007 with anything less than a 22/22 run, unless every
  uncompleted query is named and has a sized follow-up.
- Sliding deferred items to a milestone that is already accepted (the
  M0053 → M0033/M0040 dispatch that prompted M0054 in the first place).

## 8. References

- `cmd/goopg/main.go:141-157` — pprof endpoint
- `pprof-all.sh` — collection script (M0054-0004 promotes from untracked)
- `internal/executor/operators_explain.go` — EXPLAIN renderer (M0018)
- `internal/executor/instrument.go` — runtime stats (M0018)
- `internal/testutil/tpch/` — schema + sample-data helpers
- `bench/tpch/` — HammerDB driver scripts and Tcl
- `analysis/tpch-hammerdb-run-011.md` — run-011 report
- `docs/milestones/0054-tpch-performance-and-optimisation.md` — milestone
