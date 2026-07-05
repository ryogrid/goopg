(idle — nothing in flight)

Loop #7 landed and committed the Local/Temp/Planning-time I/O timing
terms for `EXPLAIN (BUFFERS)` (M0122-0003), closing the resume point
named by loop #6's deferral-ledger row, at commit `1ef465b8` (pushed to
origin/align-data-structure-with-pg).

Fix: `internal/executor/operators_explain.go`'s `planningBufferUsageJSON`
now takes a `trackIOTiming bool` parameter — both call sites in
`explainOp.Open` (ANALYZE-JSON branch reusing its existing
`trackIOTiming` local; the plain BUFFERS-without-ANALYZE branch
computing its own from `ctx.Activity`/`ctx.ProcNum`) pass it through.
When true it adds all six `Shared/Local/Temp I/O Read/Write Time` keys
as `float64(0)` constants to the "Planning" group (mirrors upstream's
non-text `show_buffer_usage`, which renders all six once track_io_timing
is on, even at zero). `planToJSONWithStats`'s existing `trackIOTiming`
block gained `Local I/O Read/Write Time`/`Temp I/O Read/Write Time`
(constant zero) next to its pre-existing real `Shared I/O Read/Write
Time` values. TEXT format needed no change (local/temp timing is always
zero, so upstream's own has_local_timing/has_temp_timing TEXT gates
would also suppress those clauses). New tests in
`internal/executor/explain_buffers_test.go`:
`TestPlanToJSONWithStatsIncludesLocalTempIOTimingWhenTrackIOTimingOn`,
`TestPlanToJSONWithStatsOmitsLocalTempIOTimingWhenTrackIOTimingOff`,
`TestPlanningBufferUsageJSONIncludesIOTimingWhenTrackIOTimingOn`,
`TestPlanningBufferUsageJSONOmitsIOTimingWhenTrackIOTimingOff`.

Gates run: `go build ./...` clean; `go test -count=1
./internal/executor/... ./internal/storage/... ./internal/planner/...
./internal/parser/... ./internal/server/... ./internal/config/...
./internal/activity/...` all PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench pre-commit smoke PASS (auto-run by hook); `make
ralph-state-guard` (repaired stale completed-marker→in_progress, same
recurring pattern as every recent loop, now consistent).

Docs updated same loop: `.ralph/fix_plan.md` (M0122-0003 banner),
`.ralph/deferral_ledger.md` (new resolved row closing the prior row's
resume point),
`docs/design/0122-0003-explain-format-xml-yaml.md` (new "Local/Temp/
Planning-time I/O timing terms" section) + `docs/design/README.md`
index row extended.

Next step: the M0122-0003 BUFFERS/`pg_stat_io` cluster's only remaining
open items are (1) the `reuses` `pg_stat_io` op counter (needs a
`BufferAccessStrategy`-style ring buffer goopg does not implement —
feature-sized, not a counter tweak, likely a full milestone-scale item
rather than a bounded slice), and (2) the 4 named writeback
simplifications-vs-upstream (2026-07-05 ledger row: single-relation-per-
hint instead of coalesced ranges, `backend_flush_after` applied
process-wide not per-session, bgwriter/checkpointer writeback timing
gated on boot-time track_io_timing via plain time.Since rather than the
activity-registry wait-event clock, and bgwriter/checkpointer
writes/write_bytes/write_time still zero). Both remaining items are
larger/fuzzier than the last several bounded slices. Recommend pivoting
away from M0122-0003 to the M0119-0004 pg_dump catalog-view parity
battery / next unresolved DU-002 slice from `.ralph/deferral_ledger.md`
(M0110-0001's self-promoting `TestPort_PgDumpConnectionSetup` guard),
per the Current Priority banner's stated work order, unless a future
loop decides `reuses` or the writeback simplifications are worth
tackling as their own multi-loop milestone.

Note: an untracked `postgres` directory/submodule shows build-artifact
content (GNUmakefile, config.log, etc.) — pre-existing, not touched or
committed this loop.
