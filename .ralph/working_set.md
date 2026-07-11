(idle — nothing in flight)

## Loop summary (2026-07-11, loop #37)

**Nightly triage:** action-items.md batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (2 co-load timing flakes + 1 resolved). No new
batch → triage complete. Proceeded to feature work.

**Task — `unimplemented_feat #5(d-iv)` leftward DAY carry before a time field —
DONE (committing this loop).**
Closed the LEFTWARD-CARRY half of the prior loop's deferred complex-body-under-
range. A bare magnitude immediately to the LEFT of a time field now takes DAY
via PG's right-to-left DecodeInterval carry (after DTK_TIME/`:`-DTK_TZ, type=
DTK_DAY): `interval '1 2:03:04'`=1 day 02:03:04, `'1.5 2:03:04'`=1 day 14:03:04,
`'-1 2:03:04'`, `'1 -2:03:04'` (signed time), `'1 12:00 h'` (absorb), and range
forms where DAY overrides the range default (`'10 2:03:04' minute to second`=10
days 02:03:04). Rejects preserved (2nd DAY / year-month-left / trailing-after-
time all collide, matching PG). Byte-for-byte vs live PG 18.3 (port 5601).
Mechanism: left-to-right peephole in `decodeIntervalFields`
(internal/parser/interval.go) — a bare magnitude whose non-unit successor field
contains `:` is stamped DAY; the time field decodes next iteration so a 2nd DAY
collides via the existing fmask. Both sibling paths via shared ParseIntervalBody.
Files: internal/parser/interval.go, internal/executor/interval_subday_test.go
(new TestIntervalLeftwardTimeCarry), docs/design/0003-0006-*.md (new Follow-up).

Next feature step (deferral ledger 2026-07-11): interval `±infinity`
(`interval 'infinity'`/`'-infinity'`, DTK_LATE/DTK_EARLY + INTERVAL_NOBEGIN/
NOEND encode/format — needs an infinite-interval Datum carrier; carve `infinity`
out of the sign-must-precede-digit guard in expandIntervalField). Then the
LEADING/cast typmod form `CAST(... AS interval hour to minute)` / `interval(p)
'...'` (typmod on the target type-name, distinct from the Form-1 trailing
qualifier already implemented).

Gates: build/vet clean; parser+analyzer+planner+executor suites PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.

In-flight: none
