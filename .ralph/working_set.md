(idle — nothing in flight)

Loop #16 implemented + committed + pushed M0122-0003 writeback
simplification (4) (commit 1a0d6618, pushed to
origin/align-data-structure-with-pg).

Task: background writer / checkpointer `pg_stat_io` `writes`/`write_bytes`/
`write_time` cells were a hardcoded 0 (goopg only tracked writes in the
backend-scoped `sharedWrittenCount`, deliberately excluding these two
background paths per upstream's per-backend `pgBufferUsage` semantics).
This was the last named "writeback simplification" residual besides the
feature-sized `reuses` op counter (see 2026-07-05 ledger row).

Files (all committed, see 1a0d6618): `internal/storage/bufpool.go`
(+`sharedBgwriterWrittenCount`/`sharedBgwriterWriteTimeNanos`,
+`sharedCheckpointWrittenCount`/`sharedCheckpointWriteTimeNanos`,
+`OnBgwriterWriteWait/Done`/`OnCheckpointerWriteWait/Done` hook fields,
+accessors `BgwriterWrittenCount()`/`BgwriterWriteTimeNanos()`/
`CheckpointWrittenCount()`/`CheckpointWriteTimeNanos()`/
`AddBgwriterWriteTimeNanos`/`AddCheckpointWriteTimeNanos`; wired the
increment+hook-bracket into `WriteDirtyPages` (bgwriter, per-victim
`flushSlot`) and `flushBatch` (checkpointer, once per AIO batch)),
`internal/initdb/open.go` (wires the 4 new hooks, same
`act.LookupTrackedGoroutine()` → `WaitEventStart(...,WaitDataFileWrite)`/
`WaitEventEnd` shape as every other `On*Wait/Done` pair), `internal/
executor/pgstat_io.go` (fetchIOStatRows reads the new counters, renders
into the bgwriter/checkpointer rows' write cells; doc comment updated),
`internal/storage/writeback.go` (doc-comment update only — this file's own
writeback-hint accounting was untouched, the new write counters are a
separate mechanism), new tests
`TestPoolBgwriterAndCheckpointerWrittenCountsAreTracked` (storage/
writeback_test.go) + `TestPgStatIOBgwriterCheckpointerWritesRendered`
(executor/pgstat_io_test.go). Design doc
`docs/design/0122-0003-explain-format-xml-yaml.md` new "Background writer /
checkpointer `writes` counters" section + README index row extended.
`.ralph/deferral_ledger.md` new resolved row; `.ralph/fix_plan.md` banner
updated.

Next step: the M0122-0003 writeback/pg_stat_io bucket now has exactly one
open item left — the feature-sized `reuses` op counter, which needs a
`BufferAccessStrategy`-style ring-buffer storage-engine mechanism goopg has
never modeled (not a bounded single-loop slice; scope it as its own
design-doc-first task if picked up). Recommend instead continuing the
M0119-0004 pg_dump catalog-view parity battery / next unresolved DU-002
slice from `.ralph/deferral_ledger.md` (per-database catalog isolation
itself remains milestone-scale — do NOT attempt as a single loop, see the
2026-07-06 DU-002 round-trip-probe ledger row for the resume point; further
slices should target catalog-view parity gaps instead).

Gates run (this loop): `go build ./...` clean; `go vet` clean on
`internal/storage/... internal/executor/... internal/initdb/...`; `go test
-race ./internal/storage/...` PASS; `go test -count=1
./internal/executor/... ./internal/initdb/... ./internal/wal/...
./cmd/goopg/... ./internal/server/...` PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); `make ralph-state-guard` OK (auto-repaired the usual
stale completed-marker pattern); pgbench smoke PASS at commit time
(pre-commit hook).

Note: an untracked `postgres` directory shows build-artifact content
(GNUmakefile, config.log, etc.) — pre-existing, not touched or committed
(carried forward from prior loops).
