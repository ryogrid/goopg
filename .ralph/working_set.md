(idle — nothing in flight)

Loop #5 landed and committed `EXPLAIN (BUFFERS)` without `ANALYZE`
(M0122-0003), closing deferral-ledger gap (2) from rows 471/481/497/498,
at commit `f840d4e1` (pushed to origin/align-data-structure-with-pg).
Fix: new `planningBufferUsageJSON()` in
`internal/executor/operators_explain.go` — both non-TEXT `explainOp.Open`
render sites now set `root["Planning"]` to an always-present all-zero
`{Shared Hit/Read/Dirtied/Written Blocks}` map whenever `opts.Buffers` is
true, independent of `ANALYZE` (matches upstream's `peek_buffer_usage`:
non-TEXT formats show this group unconditionally once BUFFERS was
requested). TEXT format untouched — its existing positive-only gate
already renders correctly (no block) since goopg's planner never touches
`storage.Pool` during cost estimation, so planning buffers are genuinely
always zero. New tests in
`internal/executor/explain_buffers_test.go` (4: JSON with/without
ANALYZE, JSON without BUFFERS opt-out, XML sibling).

Gates run: `go build ./...` clean; `go test -count=1
./internal/executor/... ./internal/storage/... ./internal/planner/...
./internal/parser/... ./internal/server/... ./internal/config/...` all
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench
pre-commit smoke PASS (auto-run by hook); `make ralph-state-guard`
(repaired stale completed-marker→in_progress, same recurring pattern as
prior loops, now consistent).

Docs updated same loop: `.ralph/fix_plan.md` (M0122-0003 item + banner),
`.ralph/deferral_ledger.md` (new resolved row closing gap (2)),
`docs/design/0122-0003-explain-format-xml-yaml.md` (new "`EXPLAIN
(BUFFERS)` without `ANALYZE`" section) + `docs/design/README.md` index
row extended.

Next step: per fix_plan.md banner, M0122-0003's only remaining sub-items
are local/temp-buffer terms (goopg has no local-buffer-manager concept —
materially larger architectural addition, not a counter tweak) and the
`reuses` `pg_stat_io` op counter (needs a `BufferAccessStrategy`-style
ring-buffer mechanism in `internal/storage/bufpool.go` — feature-sized).
Both are lower priority / bigger-than-a-loop. Recommend switching to the
M0119-0004 pg_dump catalog-view parity battery / next unresolved DU-002
slice from `.ralph/deferral_ledger.md` as the next task, or picking
`local/temp-buffer terms` if a bounded first slice can be scoped (e.g.
just render the always-zero `Local * Blocks`/`Temp * Blocks` JSON/XML/
YAML keys the same way this loop's `Planning` group did, without a real
local-buffer-manager — mirrors the `planningBufferUsageJSON` precedent).

Note: an untracked `postgres` directory/submodule shows build-artifact
content (GNUmakefile, config.log, etc.) — pre-existing, not touched or
committed this loop.
