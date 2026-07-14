(idle — nothing in flight)

Last completed (commit 9bba3892): M-NIGHTLY DateStyle follow-up —
ANALYZE's MCV/histogram-bound rendering now honors the session `datestyle`
GUC; `to_char`'s generic fallback audited and confirmed a non-issue.
`operators_analyze.go`'s `computeColumnStats` called `Datum.Format()`
directly at its 2 DATE/TIMESTAMP-affecting sites (MCV entry `Value`,
histogram-boundary strings), hardcoded ISO/Postgres-MDY. Threaded a new
`dsCtx *Context` param through `analyzeRelationWith`/`computeColumnStats`
(from `analyzeRelationCtx`'s live `ctx` on the real ANALYZE path; `nil`
from the test-only `analyzeRelation` wrapper) and swapped both sites for
`formatDatumDateStyle(d, dsCtx)`. Audited `to_char` first (the other named
candidate): `evalToChar`'s `KindTime` branch always renders via an explicit
user-supplied format string (`pgToCharToGoFormat` → `time.Format`), never
`Datum.Format()`, so `SET datestyle` never applies there — confirmed no
code change needed. New tests `internal/executor/analyze_datestyle_test.go`
(`TestAnalyzeMCVHistogramHonorDateStyle` — full ANALYZE via
newVMFixture/runDDL under German DMY, 5 dup + 5 distinct DATE rows so
`mcvFreqMargin=1.25` yields exactly 1 MCV entry + a 5-value histogram;
`TestAnalyzeMCVHistogramNilCtxDefaultsISO` — direct analyzeRelation call,
ISO default unchanged); non-vacuousness confirmed via `git stash` on just
`operators_analyze.go` (test file's new 8-arg call no longer compiles).
Live psql verification (port 5541, cleaned up): `SET datestyle='German,
DMY'; ANALYZE; SELECT most_common_vals, histogram_bounds FROM pg_stats`
returned `{05.01.2026}` / `{01.02.2026,...,05.02.2026}`. Design doc
docs/design/0097-0151-datestyle-partial-set-merge.md "Follow-up
(2026-07-15): ANALYZE's MCV/histogram-bound rendering" + README index
updated. Deferral ledger row appended (open — goopg bakes the rendering
in at ANALYZE time rather than storing binary values and re-rendering at
`pg_stats`-SELECT time like real PG, so a later session with a different
DateStyle still sees the ANALYZE-time text). fix_plan.md M-NIGHTLY task
appended. Gates: go build/go vet (repo-wide) clean; go test -count=1
./internal/executor/... PASS (full package); tpch-spotcheck.sh PASS
(Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh PASS
(0 failed, all 3 workloads, ran automatically via pre-commit hook on
`git commit`). make ralph-state-guard: auto-repaired the same recurring
stale running/completed mismatch as prior loops, then OK.

Next DateStyle-adjacent slice (open per the ledger tail, not started):
plpgsql RAISE/string-building (plpgsql_runtime.go) and EXPLAIN output are
the next candidates — audit the same way this loop did for
operators_analyze.go/to_char, then swap `.Format()`→`formatDatumDateStyle`
wherever a live `ctx` is reachable. After that: TIMESTAMPTZ's missing
session-timezone-aware conversion/offset, then pgoutput.go's DateStyle
gap. Separately, the ANALYZE binary-storage/query-time-rendering gap
recorded in today's ledger row (pg_stats should re-render at SELECT time
using the querying session's DateStyle, not bake in the ANALYZE-time
value) is its own bounded follow-up: touch catalog.ColumnStats (store
typed values, not pre-rendered strings) + operators_analyze.go +
pgstat_tables.go's pg_stats projection.

Also noted this loop (not yet actioned, informational only): the nightly
run `20260715-010036` (sha 751b8217) is STILL running under a separate
`ci/batch` process — as of this loop's read it had progressed through
`units` FAIL, `race` FAIL, and is now in the S2 TPC-H EXPLAIN-capture
stage (q21 done at 02:57, more queries pending). `ci/logs/action-items.md`
still reflects only the older `20260714-011651` run (already fully
triaged into M-NIGHTLY tasks) — its mtime is unchanged (Jul 14 03:34).
The next loop should check whether `ci/logs/action-items.md` has been
regenerated for `20260715-010036` once that run finishes (`grep run:
ci/logs/action-items.md` to see which run_id it reflects) and triage any
new `## AI-` items per the standing nightly-triage rule BEFORE picking
the plpgsql-RAISE/EXPLAIN slice above, per the preemption rule (new-run
triage only preempts at task-selection time, not mid-task — this loop's
own task was already finished, so the check is clean for the next loop).

In-flight: none. All work committed (9bba3892); tree clean of my changes.
Stray untracked/modified files present from other processes (weekly_loc.*,
analysis/perf-optimize3/runs/*, ci/logs/*.log, analysis/tpch-explain-
baseline.md, .ralph/progress.json, untracked postgres/) were left
untouched, same as prior loops — these belong to the concurrently-running
nightly batch (run 20260715-010036, PID tree under ci/batch/run-nightly.sh).
