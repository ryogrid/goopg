# Milestone 0054 — TPC-H Performance & Optimisation Follow-Through

**Status:** planned
**Depends on:** Milestone 0053 (HammerDB TPC-H complete run verification — PARTIAL),
Milestone 0018 (EXPLAIN / EXPLAIN ANALYZE — accepted), Milestone 0030
(catalog persistence — accepted but does not cover CREATE DATABASE WAL),
Milestone 0033 (subquery unnesting — accepted), Milestone 0040
(correlated subquery optimisation — accepted).
**Drives:** A complete, evidence-backed close-out of every "out of scope"
or "deferred" item documented during M0053 — culminating in a clean
22/22 HammerDB TPC-H SF=1 power-test run within the 2-hour budget.

## Context

M0053 completed schema build, data load, CREATE INDEX, and ANALYZE
end-to-end (`analysis/tpch-hammerdb-run-011.md`), but the power test
reached only Q14, Q2, Q9 before the 2-hour wall-clock budget exhausted
during Q20. Several items were dispatched out of M0053 with statements
like "tracked under M0033 / M0040" or "tracked under M0030", and one
("nested-loop index join") was deferred to "M0054" by
`docs/design/0053-0002-nested-loop-index-join-scope.md`.

The dispatch was incomplete: M0030, M0033, and M0040 were already
**accepted** before M0053 ran, yet the relevant gaps re-surfaced during
the run-011 verification. M0054 exists to close those gaps with real
empirical work, not another pointer hop.

The seven sub-tasks (M0054-0001 .. M0054-0007) cover:

1. **CREATE DATABASE WAL persistence** — survive a server crash with
   the user database intact (M0030 handled `pg_class / pg_attribute /
   pg_type` only, NOT database-level DDL).
2. **TPC-H index utilisation audit** — for each of Q1..Q22, capture
   `EXPLAIN (FORMAT JSON)` against a populated SF=1 schema and
   document which HammerDB-created indexes the planner actually uses,
   plus where it falls back to SeqScan when an index exists.
3. **Close the index-utilisation gaps** surfaced by item 2.
4. **pprof-driven bottleneck profiling** under the HammerDB power test
   (CPU, heap, mutex, block, goroutine), against Q9 / Q20 / end-of-run.
5. **Implement the top-3 pprof-flagged perf fixes** with before/after
   profile evidence.
6. **Nested-loop index join (NLI)** — implementation, not scope.
   `docs/design/0053-0002-*` already specified the architecture; M0054
   lands the code.
7. **Re-run HammerDB TPC-H SF=1 power test → run-012** with full 22/22
   completion as the pass criterion.

## Strict no-deferral policy

> **Strict no-deferral policy.** This milestone exists because earlier
> milestones (M0033, M0040) closed by claiming TPC-H slowness was
> "covered" without empirical follow-up; the residual work re-emerged
> in run-011 as Q20 timeout. M0054 tasks may NOT be closed by
> forwarding to another milestone unless: (a) that milestone is
> created and populated in the same loop, (b) the user is informed of
> the redirect in writing in the loop's status block, AND (c) a clear
> empirical reason — not architectural convenience — is recorded.
> "Out of scope" is not an acceptable closure rationale here. If a
> task is genuinely too large for a single loop, decompose it into
> sub-tasks and land each one; do not relabel the parent as a pointer
> to nowhere.

This rule applies to every M0054-NNNN task and every sub-task created
under it. Reviewers should reject any closure that violates the
clause.

## Required Design Docs

- `docs/design/0054-0001-tpch-perf-investigation-methodology.md` —
  EXPLAIN / pprof capture procedure, 10 % SF=1 synthetic-data harness
  reuse, acceptance bar per sub-task, gap-fix accounting model.

Additional design docs may be added as M0054-0003 / M0054-0005 /
M0054-0006 sub-tasks land. Each non-trivial subsystem follows the
project's existing one-design-doc-per-subsystem rule
(`.ralph/specs/GOAL_AND_REQUIREMENTS.md` §9).

## Definition of Done

The milestone closes when **all** of the following are true:

1. **M0054-0001 (CREATE DATABASE WAL):** the regression test in
   `internal/initdb/` proves a user database survives a `kill -9` of
   the goopg process and a clean restart.
2. **M0054-0002 (index audit):** `internal/testutil/tpch/index_utilisation_test.go`
   exists, asserts a baseline plan-shape for every Q1..Q22 against a
   loaded schema, and `analysis/tpch-explain-baseline.md` lists every
   query plan and every "should-IndexScan-but-SeqScan" gap.
3. **M0054-0003 (close gaps):** the top 3 gaps from M0054-0002 are
   closed; each shows an EXPLAIN diff in the baseline test snapshot.
4. **M0054-0004 (pprof survey):** `analysis/tpch-pprof-bottleneck-survey.md`
   names the top 10 hot functions per profile, the top 3 lock
   contention hotspots, and the top 3 allocation hotspots — each with
   actionable next steps.
5. **M0054-0005 (top-3 perf fixes):** three concrete code changes
   land, each documented with before/after profile evidence.
6. **M0054-0006 (NLI):** `NestedLoopIndexJoin` operator + planner rule
   are implemented and tested; EXPLAIN renders `Nested Loop` with the
   inner IndexScan; result-parity vs HashJoin is verified against
   representative TPC-H join shapes.
7. **M0054-0007 (run-012):** `analysis/tpch-hammerdb-run-012.md`
   reports a full 22/22 power-test completion within 7200 s wall
   clock. **A run that times out short of 22/22 does NOT close
   M0054-0007 — the slow query is named, root-caused (with EXPLAIN +
   pprof slice), and a concrete follow-up sub-task is opened.**

## Reference

- Run-011 report: `analysis/tpch-hammerdb-run-011.md`
- M0053 milestone: `docs/milestones/0053-hammerdb-tpch-complete-run-verification.md`
- NLI scope doc: `docs/design/0053-0002-nested-loop-index-join-scope.md`
- M0018 EXPLAIN docs: `docs/design/0018-000{1..4}-*.md`
- M0030 catalog persistence: `docs/milestones/0030-catalog-persistence-and-ddl-wal.md`
- M0033 subquery unnesting: `docs/milestones/0033-subquery-unnesting.md`
- M0040 correlated subquery optimisation: `docs/milestones/0040-correlated-subquery-optimization.md`
- pprof endpoint wiring: `cmd/goopg/main.go:141-157`
- pprof collection script: `pprof-all.sh` (root of repo)
- HammerDB TPC-H Tcl: `bench/tpch/tcl/build_schema.tcl`,
  `bench/tpch/tcl/run_power_test.tcl`
