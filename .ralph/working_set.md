(idle — nothing in flight)

Last completed (commit 4a7ce9d8): M-NIGHTLY DateStyle follow-up —
plpgsql RAISE %-argument formatting now honors the session `datestyle`
GUC. `evalRaiseMsg` (`internal/executor/plpgsql_runtime.go`) called
`val.Format()` directly on each evaluated `%`-argument, hardcoding
ISO/Postgres-MDY for a DATE/TIMESTAMP/TIMESTAMPTZ argument. `ctx` was
already a parameter, so the fix is a one-line swap to
`formatDatumDateStyle(val, ctx)`. Audited the file's 2 other `.Format()`
sites (`datumToSQLLiteral`, `plpgsqlFormatDynArg`) — both build SQL
literal text for dynamic-SQL re-parsing (EXECUTE/trigger-ref
substitution), where ISO is the safer unambiguous choice, not a display
bug — left unchanged. New tests
`internal/executor/plpgsql_raise_datestyle_test.go`
(`TestRaiseMsgHonorsDateStyle`, `TestRaiseMsgDefaultsISOWithNoDateStyleGUC`);
both source the DATE value via `SELECT ... INTO` from a real table
column (NOT a `declare d date := '...'` default) to sidestep a sibling
bug found while writing the test (see below). Non-vacuousness confirmed
via `git stash` on just `plpgsql_runtime.go` (pre-fix message:
`bad date: 01-05-2026` — Format()'s hardcoded Postgres-MDY, not even
ISO). Design doc `docs/design/0097-0151-datestyle-partial-set-merge.md`
"Follow-up (2026-07-15): plpgsql RAISE %-argument DateStyle-awareness" +
README index updated. Deferral ledger row appended (open — 2 new
discovered gaps, see below). fix_plan.md M-NIGHTLY task appended.
Gates: go build/go vet (repo-wide) clean; go test -count=1
./internal/executor/... PASS (full package); tpch-spotcheck.sh PASS
(Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh PASS
(0 failed, all 3 workloads, ran automatically via pre-commit hook on
`git commit`). make ralph-state-guard: auto-repaired the same recurring
stale running/completed mismatch as prior loops, then OK.

Next DateStyle-adjacent slice (open per this loop's ledger row, not
started) — two NEW candidates surfaced this loop, either is a
reasonable next pick:
1. `coerceDatumToType`'s `isTimeTypeName` branch (plpgsql_runtime.go,
   ~line 2320s) mints a timestamp-shaped Datum (no flagDate) for ANY
   string-literal declare/assign of a time-family type (`date`,
   `timestamp`, `timestamptz`) via a single
   `tryParseStringAs(KindTime, ...)` call — so `declare d date :=
   '2026-01-05';` renders with a spurious `00:00:00` tail. Same bug
   class the `evalCast` "date" case had (fixed 2026-07-15, see ledger
   row 781) but never ported to this sibling. Fix: branch on
   `tn == "date"` specifically, use a date-only parse/NewDateDatum-
   equivalent construction mirroring the `evalCast` fix.
2. plpgsql composite/record/array variables bake a pre-rendered
   `Format()` string into their runtime representation with NO
   re-render hook (same architecture-gap family as the still-open
   ANALYZE/pg_stats binary-storage note): `rowToCompositeText`
   (trigger OLD/NEW), `bindRecordRowComposite` (record-typed SELECT
   INTO), `updateCompositeField` (`rec.field := value`), and the
   `ArrayAssignStmt` case's `elems[sub-1] = newVal.Format()`
   (`x[idx] := value`). Needs a design decision (typed-storage +
   re-render, or accept narrower same-session limitation) before
   touching any of the 4 sites — larger unit of work than #1.
EXPLAIN output remains fully unaudited (also a standing candidate,
lower priority than the two above per the ledger's "lower-traffic"
convention).

Also noted last loop (still informational, re-check this loop):
`ci/logs/action-items.md` still reflects only the older
`20260714-011651` run (mtime unchanged, Jul 14 03:34) — the nightly run
`20260715-010036` this loop re-checked at start was still running
(per prior loop's note) and had NOT regenerated action-items.md as of
this loop's start. Re-run `grep run: ci/logs/action-items.md` at the
start of the next loop to see if it has been regenerated; if so, triage
any new `## AI-` items into M-NIGHTLY tasks BEFORE picking either
DateStyle candidate above (preemption rule: applies at task-selection
time only, this loop's own task was already finished so the check is
clean for the next loop).

In-flight: none. All work committed (4a7ce9d8); tree clean of my
changes. Stray untracked/modified files present from other processes
(weekly_loc.*, analysis/perf-optimize3/runs/*, ci/logs/*.log,
analysis/tpch-explain-baseline.md, .ralph/progress.json, untracked
postgres/) were left untouched, same as prior loops — these belong to
the concurrently-running nightly batch (run 20260715-010036, PID tree
under ci/batch/run-nightly.sh).
