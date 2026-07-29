(idle — nothing in flight)

Last loop: **M0125-0024's owed executor-side value gate CLOSED**. New file
`internal/executor/agg_state_sharing_value_test.go` (3 tests); design §5.1;
ledger row 587 flipped `resolved`; one new ledger row appended.

Files: the new test file, `docs/design/0125-0024-expression-identity-collisions.md`
(§5.1 + §6), `docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

## Facts the next loop should NOT re-derive

- **A user-defined sfunc IS reachable from the in-package executor harness.**
  The ledger row had budgeted a new fixture for this; it was not needed.
  `executeSFuncCall` falls back to `executeStoredRoutine`
  (`operators_join_agg.go:3633`) and `RAISE NOTICE` lands in `ctx.Notices`, so
  a plpgsql sfunc + `newDDLFixture` + `runQuery` (with_compat_test.go) is the
  whole recipe. Tests can COUNT sfunc calls, not just assert values.
- **The M0125-0024 verdict is now "wrong answer AND wrong error".** At
  `da6d2c0c`: `ua_sum(a+b), ua_sum(a-b)` → `(77, 77)` (PG: `(77, -63)`), and
  `DISTINCT ON (CASE …) … ORDER BY CASE …` was **rejected** `42P10` although PG
  18.3 accepts it (measured on the 65438 oracle). Do not re-argue the laxer
  direction from `equalfuncs.c` — it is measured.
- **PG oracle access:** 65438 is up; the role is **`ryo`, not `postgres`**
  (`-U postgres` → `FATAL: role "postgres" does not exist`), dbs `tpcds`/`tpcds05`.
  Read-only `VALUES`-derived queries need no DDL on the oracle cluster.
- **Pre/post-fix proof without a worktree:** `git checkout da6d2c0c --
  internal/planner/exprwalk.go internal/planner/planner.go`, run the *executor*
  package, then `git checkout HEAD -- <same two>`. Planner _test.go files are
  not compiled for another package, so this compiles cleanly. Cheap and exact.
- **Host was NOT quiet** (nightly `run-nightly.sh` PID 3541516 since 01:51, its
  TPC-DS stage on 65435 at ~11 GB RSS / ~590 % CPU, load ~12, budget-left ~78 min
  at 06:59; Q18 TIMEOUT at 06:53). No timing was attempted. A goroutine dump was
  taken from its pprof (6161) → `/tmp/nightly-goroutines-0713.txt`: it shows the
  CURRENT query in `multiHashJoinOp.Open`, **no** orphaned-backend evidence, so
  it is not the stack the parked shutdown-hang item wants.

## NEXT (banner order — M0124 closed, M0125 first, M-NIGHTLY filed-only)

1. **The owed SF0.5 gate, on a QUIET host** (`scripts/tpcds-sf05-regression.sh
   sweep`, ~1 h). Owed three times over and it **must precede M0125-0002
   commit 2**. Check `ci/batch/run-nightly.sh` is absent first.
2. `M0125-0002` **commit 2 — `cloneExprShiftIdx`** (`nl_index_join.go:777`),
   first commit expecting hunks; carries the full timed 22-query TPC-H run
   (`scripts/goopg-test-run.sh`, `GOGC=100` / `GOMEMLIMIT=12GiB`).
3. Then `M0125-0003` stage 2's TIMED arm, stage 3, `M0125-0005`.

Gates run: `go build ./...` clean; the 3 new tests proved to FAIL at `da6d2c0c`
and PASS at HEAD; `./internal/executor/` + `./internal/planner/` PASS; units
suite PASS (all cached); pgbench smoke via the commit hook; `make
ralph-state-guard`. NOT run (host): SF0.5 sweep, any timed TPC-H, plan-diff
(no planner/executor *product* code changed this loop — test-only).

In-flight: none.
