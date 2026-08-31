(idle — nothing in flight)

## Loop #3 (2026-09-01) result — M0134-0182 (type_sanity.sql) sized & PARKED,
## RESERV date/time literal fix shipped

**Nightly triage:** `ci/logs/action-items.md` still shows the same run
`20260831-013952` — already filed prior loop. A newer nightly
(`20260901-010436`) was mid-run (pgbench stage) when checked, and a
concurrent process was seen committing its logs — nothing new to file this
loop.

**Task:** M0134-0182 — `type_sanity.sql`. **PARKED** (CSV `not-tried` →
`failed`, 1151 diff lines / 9 `^+ERROR`, unchanged in COUNT before/after —
see why below). Design `docs/design/0100-0149/m0134-0182-type-sanity-sizing.md`.

**Shipped:** PG's RESERV date/time input keywords ('now'/'today'/
'tomorrow'/'yesterday'/'epoch' — DecodeDateTime/DecodeTimeOnly, datetime.c)
were entirely unimplemented for date/time/timetz/timestamp/timestamptz
literal input (only infinity/-infinity worked). New shared functions
`parseDateSpecialLiteral`/`parseTimestampSpecialLiteral`/
`parseTimeSpecialLiteral`/`parseTimeTZSpecialLiteral`
(internal/executor/copy_text.go) + `nowFromCtx(ctx)` helper (expr.go, next
to timeZoneFromCtx), threaded through all 10 literal/cast/
pg_input_is_valid/row-encode call sites in expr.go + codec.go. New test
`internal/executor/date_time_reserv_literal_test.go`
(TestDateTimeReservedLiteral, 20 self-consistent cases).

**Finding — fixing it unmasked a NEW bug:** `'1 2'::int2vector` works
standalone but `CREATE TABLE t AS SELECT '1 2'::int2vector` errors
`expected bytes for int2vector, got kind 3` — the CTAS row-encode path
wasn't reachable before (died earlier on 'today'::date). Net `^+ERROR`
count unchanged (9→9, one error swapped for another) — textbook
serially-masked-cause shape, same as M0134-0014/-0025/-0026.

**Biggest discovery (ledgered, NOT fixed — REFACTOR-tier):** goopg's live
`pg_proc`/`regproc` only expose ~32 hand-curated builtins
(`catalog.builtinProcsByName`) despite a full 3397-row pg_proc.dat mirror
(`internal/initdb.pgProcInitialEntries()`) correctly written to the on-disk
heap at initdb time (verified: `base/*/1255` is 778KB but
`SELECT count(*) FROM pg_proc` returns 32 on a fresh cluster; `int4pl`/
`array_in`/every PG builtin misses `'x'::regproc` despite being used
constantly by the evaluator). This is write-only heap data the live query
engine and `reg_identifier.go`'s regproc-cast fallback never read. Blocks 7
of this case's 9 `^+ERROR`s and very likely much wider "function does not
exist" false-negative noise across OTHER regress cases too. Filed as its
own ledger row, no milestone number assigned yet — worth prioritizing highly
next time M0134 selection reaches an unclaimed slot, or as a standalone
M-numbered milestone given its likely blast radius across the whole M0134
backlog.

**Gates run:** `go build ./...` clean; `go test ./internal/executor/...`
full package PASS (incl. new test + pre-existing infinity-literal tests,
confirming no regression on the folded-in infinity behavior);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full units
suite PASS; `make check-testport-inventory` PASS; `make regen-testport`
clean 6-file regen (CSV flip + derived docs, regress-sql 160→161 failed /
9→8 not-tried); `make ralph-state-guard` PASS.

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next
unclaimed M0134 case per ordering is **M0134-0183** (`typed_table.sql`,
`not-tried`, never sized) — pick that up unless the banner changes.
Separately worth flagging to the user/next-loop-selector: the pg_proc/
regproc exposure gap discovered this loop is a HIGH-VALUE, REFACTOR-tier
target (internal/executor/reg_identifier.go's regproc arm +
internal/initdb.pgProcInitialEntries() wiring into the live pg_proc
heap-scan/lookup path) that could unlock many other stuck M0134 cases
whose `^+ERROR`s are "function X does not exist" for common PG builtins —
consider filing it as a numbered milestone rather than leaving it as a bare
ledger row if it recurs in future sizing loops.

**In-flight:** none.
