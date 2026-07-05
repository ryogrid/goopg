(idle — nothing in flight)

Loop #15 found a fully-implemented, uncommitted writeback simplification (3)
already in the working tree from a prior (interrupted) loop — verified it
end-to-end and committed it (0973f296), pushed to
origin/align-data-structure-with-pg.

Task: M0122-0003 writeback simplification (3) — bgwriter/checkpointer
`writeback_time` now backed by the real `ActivityRegistry` wait-event clock
(visible in `pg_stat_activity`, not just `pg_stat_io`) instead of a plain
`time.Now()`/`time.Since` pair. Also fixed a real bug found while closing
this: the checkpointer's old `act.Register(&Backend{PID:"cp-0",...})` call
silently collided with and clobbered the WAL writer's `activitySlot`
(`procNumForPID` maps every non-numeric PID to the same `bgBase` slot
`RegisterBackground(WalWriterIdx, ...)` already claims).

Files (already committed, see 0973f296): `internal/activity/registry.go`
(+`BgwriterIdx = 3`), `internal/initdb/open.go` (RegisterBackground for
checkpointer/bgwriter, OnLoopStart/OnLoopEnd wiring, writeback hooks now
match `OnBackendWritebackWait/Done`'s pattern exactly), `internal/storage/
bgwriter.go` (+OnLoopStart/OnLoopEnd fields), `internal/wal/checkpointer.go`
(+OnLoopStart/OnLoopEnd fields), `cmd/goopg/main.go` (dropped the
now-redundant/colliding `cp-0` Register call), new
`internal/initdb/background_activity_test.go` (4 regression tests). Design
doc `docs/design/0122-0003-explain-format-xml-yaml.md` new "Registered
bgwriter/checkpointer background slots" section + README index bump.
`.ralph/deferral_ledger.md` new resolved row; `.ralph/fix_plan.md` banner
updated.

Next step: pick up one of the 2 remaining M0122-0003 writeback bucket items
— the `reuses` `pg_stat_io` op counter (needs a `BufferAccessStrategy`-style
ring buffer, feature-sized) or simplification (4) (bgwriter/checkpointer
own `writes`/`write_bytes`/`write_time` cells staying an honest 0) — or
continue the M0119-0004 pg_dump catalog-view parity battery / next
unresolved DU-002 slice (per-database catalog isolation itself remains
milestone-scale, do NOT attempt as a single loop — see the 2026-07-06
DU-002 round-trip-probe ledger row for the resume point).

Gates run (this loop, before committing already-written code): `go build
./...` clean; `go vet` clean on touched packages; `go test -race
./internal/activity/... ./internal/storage/... ./internal/wal/...` PASS;
`go test -count=1 ./internal/initdb/... ./cmd/goopg/...
./internal/executor/... ./internal/server/...` PASS; `scripts/tpch-
spotcheck.sh` PASS (Q12=2/Q13=33); `make ralph-state-guard` OK (auto-
repaired the usual stale completed-marker pattern); pgbench smoke PASS at
commit time (pre-commit hook).

Note: an untracked `postgres` directory shows build-artifact content
(GNUmakefile, config.log, etc.) — pre-existing, not touched or committed.
