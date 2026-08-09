(idle — nothing in flight)

Last loop: M-NIGHTLY triage of nightly run `20260809-020705` (49 items, sha
`f2e3f167`). All 49 filed under M-NIGHTLY in `.ralph/fix_plan.md`; 48 closed.

- 26 regress items (AI-…-024..049) had ONE root cause: M0129-S10 (`d6bcc190`)
  deleted the `LINE N:`/caret stripping from `NormalizeRegressOutput`, assuming
  goopg now emits `FieldPosition` everywhere. It does not — the `P` field only
  rides an `ExecError` with non-zero `Pos`, so coercion-time datatype errors
  (`invalid input syntax for type smallint`) and row-constraint errors carry no
  position; and `limit`'s CREATE VIEW error emits one where PG emits none.
  Every one of the 26 diffs was position lines only, zero message-text
  divergence. Fix: strip from BOTH sides again (`internal/testport/framework/
  regress.go`). All 26 PASS at HEAD after the fix.
- Why it shipped green: a diverging regress case reports `t.Skip`, so
  `TestPort_RegressSuite` still says PASS — the suite structurally cannot catch
  this class. New unit gate `TestNormalizeRegressOutputStripsErrorPositionLines`
  (both directions + 2 non-vacuity assertions) is the guard.
- 22 testport items (AI-…-001..017, 019..023) re-ran STALE — all PASS at HEAD
  `d4bee0df`. Consistent with the `root-0029` suite-wedge casualty mode.
- 1 testport item is REAL: `TestPort_IsolationMergeUpdate` (AI-…-018) still
  FAILs at HEAD (4.09s). Left unchecked — one task per loop.

Design doc: `docs/design/0060-0002-postgres-oracle-port-framework.md` §4.3.1
(new) states the both-sides policy + the two conditions for ever removing it
again. 1 ledger row (partial `ExecError.Pos` coverage, `parser_errposition()`
resume point).

Gates run: units precommit PASS; the 26 regress cases PASS; framework unit
tests PASS; `make ralph-state-guard` OK (auto-repaired stale completed marker);
pgbench hook PASS at commit. tpch-spotcheck NOT run — the change is
test-harness only, no executor/planner/codec path touched.

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
All M0130 slices are `[x]`, so M-NIGHTLY selection is live. Next M-NIGHTLY task:
  `TestPort_IsolationMergeUpdate` (AI-20260809-020705-018); repro
  `go test -v -run '^TestPort_IsolationMergeUpdate$' ./internal/testport/`.
Carried M0119-0006 remainder (enum expr keys, checkunique posting lists,
box/int4range/int4[]/interval encodings, unscoped whole-DB pg_amcheck) plus the
un-run TPC-DS SF0.5 sweep still sit below the banner.

In-flight: none.
