# 0118-0024 — `drop-index-concurrently-1` isolation spec promoted to pass-required (M0118-0007)

Status: accepted
Date: 2026-06-22
Milestone: M0118 (Upstream Isolation Spec Suite Pass-Through), sub-task M0118-0007

## Context

M0118-0007 covers the two "planner / output-format blocker" isolation specs:

- `eval-plan-qual` — needs planner `RETURNING` support inside the EvalPlanQual
  recheck path.
- `drop-index-concurrently-1` — `EXPLAIN`-driven plan-format parity (a SELECT's
  plan must switch from index-scan to seq-scan once a concurrently-dropped index
  becomes invalid, and the READ COMMITTED snapshot must observe the right row
  versions while the drop is in flight).

Following the probe-first discipline (throwaway `RunAndCompare` over the group,
ranked by first divergence — see [[m0118_isolation_specs_often_frontend_gaps]]),
`drop-index-concurrently-1` was found to already match PG 18.3 **byte-for-byte**.
`eval-plan-qual` still diverges, but only late: a single cross-table EvalPlanQual
permutation returns `(0 rows)` where PG re-finds the row after a concurrent
update — a genuine EPQ-join recheck gap, deferred.

## Decision

Promote `drop-index-concurrently-1` to pass-required with **no engine change**.
The behaviour it exercises was already correct from prior milestones:

- `DROP INDEX CONCURRENTLY`'s two-phase index invalidation (mark-invalid →
  drain → drop) so concurrent sessions stop using the index at the right point.
- The cost-model / plan selection that falls back to a sequential scan once the
  index is no longer usable, surfaced verbatim through `EXPLAIN`.
- READ COMMITTED per-statement snapshot visibility across the drop.

Promotion mechanism is the established `runIsoSpecStrict` helper (introduced in
design 0118-0022): it hard-asserts a `pass` status, so a future regression
surfaces as a red test instead of the soft `t.Skip` that `runIsoSpec` emits for
still-deferred specs. `TestPort_IsolationDropIndexConcurrently1` was switched
from `runIsoSpec` to `runIsoSpecStrict`.

## Scope

- `internal/testport/isolation_port_test.go` — one helper switch + doc comment.
- `docs/test-port/postgres-oracle-port-status.{csv,md}` — D-002 narrative records
  the promotion; `.md` regenerated via `cmd/gen-oracle-port-status`.

No production code changed; blast radius is the test gate only.

## Remaining (deferred — M0118-0007 stays open)

`eval-plan-qual` is deferred (ledger 2026-06-22): the EvalPlanQual recheck over a
**join** returns no row after a concurrent `UPDATE` where PG re-projects the
updated row (`x|y|id|value|id|data` permutation, ~L1171 of the expected output).
This needs the EPQ recheck to re-evaluate the full join plan against the updated
tuple, not just the directly-locked relation — real executor work, tracked as the
M0118-0007 resume point.

## Gates

- `go test -run TestPort_IsolationDropIndexConcurrently1 ./internal/testport/` —
  PASS under the strict gate (~2.6s).
- pgbench CI-parity smoke via the pre-commit hook at commit.

## See also

- [[goopg_no_hot_update_index_reeval]] — index re-eval semantics on UPDATE.
- design 0118-0022 (MERGE/ON CONFLICT promotion — `runIsoSpecStrict` origin),
  0118-0023 (FK/RI partial promotion — sibling M0118-0005 group).
