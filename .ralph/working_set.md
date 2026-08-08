Task: M-NIGHTLY AI-019 PlpgsqlToast — FIXED (command counter per-statement advance).

Files:
- internal/executor/plpgsql_runtime.go: `executePLpgSQLStmtList` — added
  `routineCommandCounterIncrement(ctx, r)` after each statement so subsequent
  statements see prior writes (mirrors PG's SPI per-statement increment).
- internal/executor/operators_ddl.go: `execDoBlock` — added initial
  `routineCommandCounterIncrement(o.ctx, r)` before executing the statement
  list (mirrors the same call in `executePLpgSQLRoutine`).

Key symbols: executePLpgSQLStmtList, execDoBlock, routineCommandCounterIncrement,
  CommandCounter, executePLpgSQLRoutine

Hypothesis/Findings:
- Root cause: `routineCommandCounterIncrement` was called only once at routine
  entry, not after each embedded SQL statement. INSERTs within the same DO block
  stamped tuples with cmin == curcid, making them invisible to subsequent FOR
  loop SELECT (cmin >= curcid → hidden). Only pre-existing rows from prior
  transactions were visible. The FOR loop iterated 1 time instead of 3.
- PG advances the command counter through SPI after EVERY statement of a volatile
  routine (postquel_getnext in functions.c).
- Fix verified: PlpgsqlToast PASS, executor suite PASS, TPC-H spotcheck PASS
  (Q12=2, Q13=35), pre-commit pgbench smoke PASS (0 failed, all 3 workloads).

Next step: Investigate EvalPlanQual (AI-007) — the only remaining M-NIGHTLY
  engine failure. Pre-existing `markJoinPreserveCTID` walk lacks arms for
  non-joinOp plan shapes under DP=0 (deferral ledger row 2026-08-07 M0127-P6.2).

Gates run:
- `go build ./...`: PASS
- `go test ./internal/executor/...`: PASS (6.080s, all cached)
- PlpgsqlToast repro: PASS (4.25s)
- `scripts/tpch-spotcheck.sh`: PASS (Q12=2, Q13=35)
- `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`: PASS
  (0 failed, all 3 workloads: TPC-B 395 tps, simple-update 418 tps, select-only 12303 tps)
- `make ralph-state-guard`: PENDING (run below)

In-flight: none
