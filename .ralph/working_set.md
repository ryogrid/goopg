(idle — nothing in flight)

## Loop summary (2026-07-11, loop #41)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items already
`[x]` in fix_plan.md (same batch as loops #37–#40, no new batch). Triage complete.

**Task — `unimplemented_feat #5(d-iv)` unary `- interval` negation — DONE
(committing this loop).** Closed deferred item (2) the prior EXTRACT row named.
The operand was rejected at TWO layers:
- Analyzer (`internal/analyzer/analyzer.go`): split the combined `OpUnaryPos,
  OpUnaryNeg` arm — `OpUnaryNeg` now also accepts a type named `interval`
  (PG's `interval_um`); `OpUnaryPos` stays numeric-only (PG has NO unary
  `+ interval`, verified live → 42883).
- Evaluator (`negateInterval`, `internal/executor/expr.go`): line-ports
  `interval_um_internal` (timestamp.c:3444). `±infinity` sentinels SWAP
  (NOBEGIN↔NOEND); finite fields negate with signed-min overflow guard +
  `INTERVAL_NOT_FINITE(result)` guard. Wired into `evalUnary`'s `OpUnaryNeg`
  arm (single funnel — fast-path `evalFastExpr` + interpreted both delegate).
Files: internal/analyzer/analyzer.go, internal/executor/expr.go,
internal/executor/interval_subday_test.go (new `TestNegateInterval`, 8 cases),
docs/design/0003-0006-date-interval-arithmetic.md (new Follow-up),
.ralph/deferral_ledger.md (row), .ralph/fix_plan.md (checked).
All `want` from live PG 18.3 (`local_install`, socket /tmp:5599).

**Next feature step (deferral ledger 2026-07-11):** remaining #5(d-iv) items —
(1) `timestamp/timestamptz ± interval 'infinity'` (needs a NEW infinite-timestamp
carrier + `timestamp_pl_interval` short-circuit in addTimeInterval); (3) cast-form
typmod `CAST(... AS interval hour to minute)` / `interval(p) '...'` (type-name
typmod path + AdjustIntervalForTypmod); (4) EXTRACT numeric trailing-zero scale
gap (`6.5` vs PG `6.500000`) — shared with timestamp EXTRACT path, scope on its
own with a full EXTRACT re-verify.

Gates: build/vet clean; analyzer + executor suites PASS; values cross-checked vs
PG 18.3; tpch-spotcheck + pgbench smoke via pre-commit hook.

In-flight: none
