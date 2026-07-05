(idle — nothing in flight)

Loop #9 landed M0122-0003's writeback simplification (2): `backend_flush_after`
is now genuinely per-session (upstream's GUC is PGC_USERSET), not a single
process-wide `storage.Pool` atomic.

Task: M0122-0003 (EXPLAIN/pg_stat instrumentation, `pg_stat_io` writeback
bucket). Deferral ledger row (2026-07-05, line 505) named 4 simplifications
vs. upstream in the writeback/writeback_time work; this loop closed #2.

Files: `internal/activity/registry.go` (+`coldActivity.BackendFlushAfterBlocks`,
`UpdateBackendFlushAfter`/`BackendFlushAfterOverride`), `internal/storage/
bufpool.go` (+`Pool.BackendFlushAfterOverride` hook field),
`internal/storage/writeback.go` (`accountWrite` signature changed from
`*atomic.Int32` to plain `int32`; `accountBackendWrite` resolves the
override first, falls back to the process-wide default), `internal/initdb/
open.go` (wires `pool.BackendFlushAfterOverride = act.BackendFlushAfterOverride`
right after `SetBackendFlushAfter`), `internal/server/server.go` (new
`backend_flush_after` OnChange hook + per-connection seed, mirrors
`track_io_timing`'s own wiring exactly) — plus new tests in
`internal/activity/registry_test.go`, `internal/server/server_test.go`,
`internal/storage/writeback_test.go`. Design doc `docs/design/0122-0003-
explain-format-xml-yaml.md` new "Per-session `backend_flush_after`" section
+ `docs/design/README.md` one-line addendum. `.ralph/deferral_ledger.md`
new 2026-07-06 row (resolved). `.ralph/fix_plan.md` M0122-0003 banner
updated.

Key symbols: `ActivityRegistry.BackendFlushAfterOverride()` (no fast-path
gate, unlike `TrackIOTimingOn`/`LookupTrackedGoroutine` — deliberate, since
`accountBackendWrite`'s only caller, `evictVictim`'s dirty-victim path, is
far rarer than the buffer-pin hot path that gate protects); `Pool.
accountBackendWrite` (writeback.go).

Findings: nothing new deferred by this loop — it closes exactly named
simplification (2). Still open in the same writeback bucket (unchanged):
`reuses` op counter (needs a `BufferAccessStrategy`-style ring buffer,
feature-sized), simplification (1) (single-relation-per-hint instead of
upstream's coalesced multi-range `WritebackContext`), simplification (3)
(bgwriter/checkpointer `writeback_time` gated via plain `time.Since`, not
the activity-registry wait-event clock — those two singleton background
goroutines have no registered `activity` background slot, unlike the WAL
writer's `walProcNum`), simplification (4) (bgwriter/checkpointer
`writes`/`write_bytes`/`write_time` stay an honest 0).

Next step: pick up one of the 3 remaining writeback simplifications (3 is
probably next in line — needs `RegisterBackground` slots for bgwriter/
checkpointer in `internal/activity/registry.go`, mirroring `walProcNum`,
then real wait-event brackets instead of the `time.Now()`/`time.Since`
pair), or the `reuses` op counter (bigger, needs a new `BufferAccessStrategy`
ring-buffer type), or continue the M0119-0004 pg_dump catalog-view parity
battery / next unresolved DU-002 slice (per-database catalog isolation
itself remains milestone-scale, do NOT attempt as a single loop — see the
2026-07-06 DU-002 round-trip-probe ledger row for the resume point).

Gates run: `go build ./...` clean; `go vet` clean on touched packages;
`go test -race ./internal/storage/... ./internal/activity/...` PASS;
`go test -count=1 ./internal/storage/... ./internal/activity/...
./internal/server/... ./internal/executor/... ./internal/initdb/...
./internal/config/...` all PASS (full packages, no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `make ralph-state-guard`
OK (auto-repaired the usual stale completed-marker pattern, same as every
recent loop); `scripts/ralph-precommit-test.sh` run at loop end (see commit
message / next loop for result if it finished after this file was
written).

Note: an untracked `postgres` directory/submodule shows build-artifact
content (GNUmakefile, config.log, etc.) — pre-existing, not touched or
committed this loop.
