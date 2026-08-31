# 0118-0095 — `eval-plan-qual-trigger.spec` PROMOTED to pass-required (M0118-0007)

Status: accepted
Date: 2026-06-25
Milestone/spec: M0118-0007 (Planner / output-format blockers; isolation D-002)

## Summary

`postgres/src/test/isolation/specs/eval-plan-qual-trigger.spec` already matches
PostgreSQL 18.3 **byte-for-byte across all 38 active permutations** with **no
engine change**. This loop promotes the dedicated test
`TestPort_IsolationEvalPlanQualTrigger` from the soft helper `runIsoSpec`
(`defer` → silent `t.Skip`) to `runIsoSpecStrict` (a non-`pass` result is now a
hard, red failure), so any future EvalPlanQual / trigger / upsert regression
surfaces instead of skipping.

This is the **harder half** of the EPQ output-parity pair. Its sibling
`eval-plan-qual.spec` still defers (EXPLAIN / column-format divergence around
expected L1171 — a cross-table EPQ recheck returns `(0 rows)` where PG
re-projects the updated row), so the M0118-0007 group stays open.

## Why this is a real (not trivial) promotion

The spec is deliberately stacked to stress the interaction of three executor
subsystems at once. Its byte-for-byte match evidences that goopg implements
each, and their composition, exactly as PG 18.3:

1. **EvalPlanQual recheck under READ COMMITTED.** When `s2` updates/deletes a
   row that `s1` modified and committed, `s2` re-reads the updated tuple via the
   CTID chain and **re-projects** it through its plan. The spec verifies EPQ is
   *performed* when `s1` commits and *skipped* when `s1` rolls back.
2. **BEFORE / AFTER row-level triggers (plpgsql).** `trig_report()` fires on
   INSERT/UPDATE/DELETE and emits a `NOTICE` describing `TG_NAME / TG_WHEN /
   TG_LEVEL / TG_OP / OLD / NEW`. The EPQ recheck must re-run the BEFORE-trigger
   queue against the *re-fetched* row and emit the trigger NOTICEs in the exact
   order and with the exact OLD/NEW values PG produces. (The spec also has a
   trigger-free comparison block — "no additional row locks" — to isolate the
   trigger contribution.)
3. **Key-update CTID-chain following.** `s1_upd_a_tob` changes the primary key
   (`key-a` → `key-b`); a concurrent `s2` update on the old key must block, then
   follow the chain (or find the row deleted / "leaped" away), per the
   `### Document that EPQ doesn't "leap"` block.
4. **`ON CONFLICT DO UPDATE` upsert arbiter** (`s2_upsert_a_data`) interleaved
   with the EPQ rechecks, with its own `WHERE` qual evaluated via
   NOTICE-emitting `noisy_oper()`.
5. **REPEATABLE READ 40001 serialization failures** — the final block runs the
   same conflicts under `BEGIN ISOLATION LEVEL REPEATABLE READ` and expects the
   serialization-failure path instead of EPQ.

All of this is observed through `RETURNING *` projection and the
`noisy_oper()` plpgsql `WHERE`-qual NOTICEs, so the comparison is sensitive to
NOTICE ordering, `pg_typeof` rendering, row order, and the EPQ row contents —
not just the final table state.

## Change

`internal/testport/isolation_port_test.go`:
`TestPort_IsolationEvalPlanQualTrigger` now calls `runIsoSpecStrict` instead of
`runIsoSpec`. Doc comment updated to record the promotion and what the spec
exercises.

`docs/test-port/postgres-oracle-port-status.csv` (D-002 row rationale): appended
the M0118-0007 `eval-plan-qual-trigger` promotion note; regenerated
`postgres-oracle-port-status.md` via `go run ./cmd/gen-oracle-port-status`. The
inventory row (`postgres-oracle-target-inventory.csv`) already carried `pass`
for this spec — only the dedicated test was still soft; this loop makes the test
enforce what the inventory already claimed.

## Verification

- `go test -run TestPort_IsolationEvalPlanQualTrigger ./internal/testport/`
  strict PASS (13.8 s), confirmed stable across repeated runs.
- Probe (throwaway `zz_probe_test.go`) over the M0118 candidate set confirmed
  `eval-plan-qual-trigger` = `pass` while `eval-plan-qual`, `ri-trigger`,
  `horizons`, `fk-partitioned-{1,2}` still `defer`.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Deferred / remaining (M0118-0007)

- `eval-plan-qual` — cross-table EvalPlanQual recheck returns `(0 rows)` where PG
  re-projects the updated row after a concurrent UPDATE; also EXPLAIN /
  column-format divergence (~expected L1171). EPQ-over-join executor work. Group
  stays open until it lands.
