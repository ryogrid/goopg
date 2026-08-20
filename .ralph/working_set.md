(idle — nothing in flight)

Last completed: M0134-0035 (interval.sql) PARKED and committed (3c5861c7 impl +
d76b3208 bookkeeping) — shipped `interval * numeric/int/float` and
`interval / numeric/int/float` (ported PG's `interval_mul`/`interval_div` from
timestamp.c: month/day/time carry-rounding, ±infinity/NaN factor handling,
int32/int64 overflow guards, division_by_zero on zero divisor), plus two
sibling gaps discovered mid-implementation (parser-analyzer 42804 rejection of
interval*numeric; `exprType`'s BinaryOp mistyping the result as numeric — wrong
wire TypeOID/psql alignment), plus a `targetMeta` `IntervalLit` arm so a bare
`SELECT interval '1 day'` names its column `interval` not `?column?`. Design
doc not needed (mechanical, two independent one-arm/one-function fixes,
judged below the "non-trivial subsystem" bar). interval.sql: 3016 -> 2711 diff
lines, 132 -> 104 `^+ERROR`, 214 -> 195 `^-ERROR`. Five deferral rows appended
in .ralph/deferral_ledger.md (2026-08-20, M0134-0035): `@`/`ago`/special-value
interval literal parsing (REFACTOR-tier), IntervalStyle output formats
(REFACTOR-tier, shared with horology/timestamp/date), interval overflow bounds
checking (not yet located), a systemic ExecError.Pos/LINE-N: false-positive
on runtime interval errors, and pg_input_is_valid/pg_input_error_info missing
interval/date/timestamp cases.

Next loop: per fix_plan.md banner, select M0134-0036 (create_table_like.sql,
status `failed`) — same sizing pattern as 0006..0035 (researcher sizes at HEAD
first, confirm not stale, bucket root causes CONTAINED vs REFACTOR-tier, ship
the smallest CONTAINED bucket or PARK with ledger rows).

Gates run this loop: RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS; go build/test PASS (internal/executor, internal/optimizer,
internal/parser/analyzer); pg-regress-runner interval PASS-run (net
improvement, verified numbers match worker report exactly); pgbench pre-commit
smoke PASS on both commits; make ralph-state-guard PASS after auto-repair
(stale running/completed status mismatch from a prior loop's clean exit, not
this loop's doing).

In-flight: none.
