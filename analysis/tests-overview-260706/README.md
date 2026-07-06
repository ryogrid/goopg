# goopg Test-Suite Overview (snapshot 2026-07-06)

This directory organizes the information needed to reason about **regression
detection across the whole implemented test suite** of goopg. It is a
*prerequisite information-gathering deliverable* — it does **not** design the
regression batch itself; it inventories what exists so a batch can be built on
top of it later.

All numbers here are transcribed from the cited files as of **2026-07-06** on
branch `align-data-structure-with-pg`. Test inventories and baselines drift as
milestones land — **re-read the cited authoritative file before trusting a
number in a live run.**

## The five documents

| File | Question it answers |
|------|---------------------|
| [`01-test-inventory.md`](01-test-inventory.md) | **What tests exist and which must pass now** — Go unit packages, `internal/testport` oracle tests, and the authoritative port-status / target-inventory CSVs. Includes the "green today / promotable later" model. |
| [`02-execution-scripts.md`](02-execution-scripts.md) | **What scripts / Make targets run the tests** — `scripts/*`, Makefile gate targets, the git pre-commit hook, and the coverage-CSV regenerators. |
| [`03-execution-constraints.md`](03-execution-constraints.md) | **Rules to honor while running** — cgroup memory cap, parallelism limits, the `mem_guard` watchdog, port/tmp isolation lanes, and CI parity. |
| [`04-performance-baselines.md`](04-performance-baselines.md) | **What perf/benchmark tests compare against** — TPC-H row-count + execution-time baselines, EXPLAIN plan snapshots, pgbench self-baseline, pprof snapshots. |
| [`05-duplicate-management.md`](05-duplicate-management.md) | **How overlapping/duplicated test tracking is managed** — and what a batch must do to avoid double-counting. |

## The core mental model (read this first)

goopg's test surface has three layers, each with a different "source of truth":

1. **Go unit/component tests** (`internal/<pkg>/*_test.go`) — run by
   `go test`. The must-pass set here is "whatever CI's unit step runs" (the
   whole module minus cluster-backed packages). There is no per-test allow/deny
   list; a package either compiles-and-passes or it is a known failure to fix.

2. **PostgreSQL oracle ports** (`internal/testport/TestPort_*`) — governed by
   `docs/test-port/postgres-oracle-port-status.csv`. This CSV is the
   **authoritative must-pass list**: rows with `status=port` +
   `pass_required=yes` must always pass; `status=defer` rows are *in scope but
   not yet required* (promotable when a named milestone lands); `status=excluded`
   rows are out of scope forever.

3. **Performance baselines** (`bench/`) — regression here is a *number moving in
   the wrong direction* (row count, query time, TPS, plan shape), compared
   against a pinned baseline file, not a pass/fail assertion.

**"FAIL now, promotable later"** is encoded structurally: a currently-failing or
deferred oracle test lives as `defer`/`failed` in the CSVs with a `deferred_to`
milestone; when it starts passing you flip its status to `port`/`pass` and
regenerate the rendered docs with the `cmd/gen-*` tools (see
`02-execution-scripts.md`). A regression batch should therefore **read the CSVs
at run time** rather than hard-code a test list.

## Snapshot facts (verify before use)

- Oracle port-status roll-up: **60 must-pass** (`port`/`yes`), 1 `port`/`no`,
  **8 promotable** (`defer`/`no`), 2 `excluded` → 71 rows total.
- Per-spec target inventory (900 rows): pass 144, port 43, defer 75, failed 106,
  not-tried 161, excluded 261, unknown 110.
- Isolation specs: 121 total, 120 pass, 1 failed (`deadlock-parallel.spec`).
- Regress cases: 232 discovered (per `regress-diff-baseline.csv`: 127 pass, 1 excluded pinned).
- TPC-H spot-check baseline: **Q12=2, Q13=33** (`bench/tpch/spotcheck_expected.env`).
