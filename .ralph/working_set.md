(idle — nothing in flight)

Loop #6 landed and committed local/temp-buffer `* Blocks` terms for
`EXPLAIN (BUFFERS)` (M0122-0003), closing the resume point named by
deferral-ledger row 508, at commit `4c8dda01` (pushed to
origin/align-data-structure-with-pg).

Fix: both non-TEXT buffer-rendering sites in
`internal/executor/operators_explain.go` now emit six always-zero keys
— `Local Hit/Read/Dirtied/Written Blocks` and `Temp Read/Written
Blocks` — alongside the pre-existing `Shared *` keys:
`planningBufferUsageJSON()` (the bare-BUFFERS "Planning" group) and
`planToJSONWithStats`'s per-node ANALYZE block. Matches upstream's
non-TEXT `show_buffer_usage` (`peek_buffer_usage`: "print even if all
zeroes" for any non-TEXT format once BUFFERS is requested). goopg has
no local-buffer-manager or temp-buffer concept — every relation,
including temp tables, goes through the one shared `storage.Pool` — so
zero is architecturally correct, not a narrower stub. TEXT format
needed no change (`formatBuffersLine` only emits the `shared` clause;
upstream's own has_local/has_temp TEXT gates would also suppress an
all-zero clause). New tests in
`internal/executor/explain_buffers_test.go`:
`TestExplainBuffersJSONAlwaysIncludesLocalTempBlocks` (per-node),
`TestExplainBuffersPlanningGroupIncludesLocalTempBlocks` (Planning
group).

Gates run: `go build ./...` clean; `go test -count=1
./internal/executor/... ./internal/storage/... ./internal/planner/...
./internal/parser/... ./internal/server/... ./internal/config/...` all
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench
pre-commit smoke PASS (auto-run by hook); `make ralph-state-guard`
(repaired stale completed-marker→in_progress, same recurring pattern as
every recent loop, now consistent).

Docs updated same loop: `.ralph/fix_plan.md` (M0122-0003 banner),
`.ralph/deferral_ledger.md` (new resolved row closing the row-508
resume point + naming the new follow-up below),
`docs/design/0122-0003-explain-format-xml-yaml.md` (new "Local/Temp `*
Blocks` terms" section) + `docs/design/README.md` index row extended.

Next step: the M0122-0003 BUFFERS/`pg_stat_io` cluster's only remaining
open items are (1) the `reuses` `pg_stat_io` op counter (needs a
`BufferAccessStrategy`-style ring buffer goopg does not implement —
feature-sized, not a counter tweak), (2) Local/Temp/Planning-time I/O
timing terms (`Local/Temp I/O Read/Write Time`, plus
`planningBufferUsageJSON()` never threading a `trackIOTiming` flag at
all — a small, bounded follow-up: thread
`ctx.Activity.TrackIOTiming(ctx.ProcNum)` into `explainOp.Open`'s
non-ANALYZE JSON/XML/YAML call site the same way the ANALYZE path
already does, then emit the same "unconditional once BUFFERS+
track_io_timing" all-zero pattern this loop just established for
Blocks), and (3) the 4 named writeback simplifications-vs-upstream
(2026-07-05 ledger row). Recommend (2) as the next bounded M0122-0003
slice (mirrors this loop's exact pattern), or pivot to the M0119-0004
pg_dump catalog-view parity battery / next unresolved DU-002 slice from
`.ralph/deferral_ledger.md` if BUFFERS/pg_stat_io work is deprioritized.

Note: an untracked `postgres` directory/submodule shows build-artifact
content (GNUmakefile, config.log, etc.) — pre-existing, not touched or
committed this loop.
