# 0118-0107 — `timeouts.spec` PROMOTED to pass-required (M0118-0009)

**Status:** accepted
**Date:** 2026-06-25
**Milestone:** M0118-0009 (misc / system-level isolation specs)
**Spec:** `postgres/src/test/isolation/specs/timeouts.spec`
**Test:** `TestPort_IsolationTimeouts` (`internal/testport/isolation_port_test.go`)

## Summary

`timeouts.spec` matches PG 18.3 **byte-for-byte across all eight permutations**
with **no engine change**, and is promoted from observability (`runIsoSpec`,
which silently skips a non-match) to **pass-required** via `runIsoSpecStrict` in
the new `TestPort_IsolationTimeouts`. A probe of the remaining M0118-0009 /
M0118-0002/0004/0005 deferred specs found this one already passing — the
cheapest available promotion, locking in `statement_timeout`/`lock_timeout`
behaviour that is already correct so any future regression surfaces as a red
test instead of a silent skip.

## What the spec exercises

The spec drives the `statement_timeout` and `lock_timeout` GUCs against two
kinds of lock wait:

- **Table-level**: `s2` issues `LOCK TABLE accounts` while `s1` holds it (after
  `s1` ran `SELECT * FROM accounts` in an open READ COMMITTED transaction).
- **Row-level**: `s2` issues `DELETE FROM accounts WHERE accountid = 'checking'`
  while `s1` holds a row lock from `UPDATE accounts SET balance = balance + 100`.

Each of the two lock waits is run under four timeout configurations:

| step | configuration |
|------|---------------|
| `sto`  | `statement_timeout = '10ms'` only |
| `lto`  | `lock_timeout = '10ms'` only |
| `lsto` | `lock_timeout = '10ms'`, `statement_timeout = '10s'` (lock fires first) |
| `slto` | `lock_timeout = '10s'`, `statement_timeout = '10ms'` (statement fires first) |

→ 4 × 2 = **eight permutations**. The waiter is cancelled by whichever timeout
is set shorter, producing the matching error:

- `statement_timeout`: `ERROR: canceling statement due to statement timeout`
  (SQLSTATE **57014**)
- `lock_timeout`: `ERROR: canceling statement due to lock timeout`
  (SQLSTATE **55P03**)

The blocked steps (`locktbl`, `update`) are marked `(*)` upstream because the
10 ms timeouts are short enough that the isolation tester may cancel the step
before it ever observes it as `<waiting ...>`; the `(*)` marker tells the runner
to render those steps with consistent output regardless of whether the
"waiting" transition was caught. goopg's isolation runner already honours the
`(*)` convention (the 300 ms blocking-detection timeout from the runner is
independent of the in-server statement/lock timeout), so the captured output is
stable.

## Why no engine change is needed

`statement_timeout` and `lock_timeout` already drive query cancellation in
goopg with the correct SQLSTATEs, and the row-/table-level lock waits already
block as PG does (the same machinery the M0118-0003/0004 row-lock and
M0118-0008 DDL-lock groups rely on). The probe confirmed all eight permutations
were already byte-identical; the only change this loop is the test promotion
plus documentation.

## Stability

Timing-sensitive specs can be flaky, but the two timeouts in each permutation
are separated by three orders of magnitude (10 ms vs 10 s) and the blocked
steps carry the `(*)` marker, so the captured output is deterministic. Verified
stable across **8 consecutive runs** (`-count=3` then `-count=5`), 8
permutations each.

## Gates

- `TestPort_IsolationTimeouts` strict PASS — 8/8 permutations, stable across
  `-count=3` and `-count=5` (8 runs total).
- `go build ./...` + `go vet` clean.
- CSV `D-002` rationale appended; `postgres-oracle-port-status.md` regenerated
  via `go run ./cmd/gen-oracle-port-status`.
- pgbench smoke = pre-commit hook (test-only change; no executor/codec path
  touched).

## Oracle

`postgres/src/test/isolation/specs/timeouts.spec` and its expected output
`postgres/src/test/isolation/expected/timeouts.out`; GUC semantics per
`postgres/official_docs_in_md` (`statement_timeout`, `lock_timeout`).
