Task: none in flight — Loop (this session) verified + committed the peer
loop's handoff from Loop #49. Peer process (PID 3335771, SID 2087326) had
fully exited with no trace by this loop's start; the 22-file diff
(writeback.go/_linux.go/_other.go/_test.go real sync_file_range writeback
counters, pgstat_io fsyncs/writeback rendering, pg_collation_for real fold,
char-OID-18 cast-expr disambiguation) was independently re-verified
(`go build ./...` clean, `go test -count=1` fresh on
storage/executor/planner/parser/server/config all PASS, `scripts/
tpch-spotcheck.sh` PASS Q12=2/Q13=33) and committed at 34552c4e (pathspec-
scoped, NOT `git add -A`; pgbench pre-commit smoke gate also passed). Tree
is clean except the unrelated `postgres` oracle source dir (untracked,
not ours to touch).

`make ralph-state-guard` found+auto-repaired a stale
status="running"/progress="completed" mismatch (the completed marker was
the prior loop's clean-exit marker, not a real project-done signal) →
reconciled to in_progress. That leaves `.ralph/progress.json` with an
unstaged diff — harmless bookkeeping, pick it up in the next commit
alongside real work rather than committing it alone.

Next step: per the fix_plan.md "Next up" banner, pick ONE of:
  - `EXPLAIN (BUFFERS)` without `ANALYZE` (PG 17+ planning-time buffer
    counters — no planning-phase buffer-counting mechanism exists yet).
  - `reuses` pg_stat_io op counter — needs a new `BufferAccessStrategy`-
    style ring-buffer mechanism in `internal/storage/bufpool.go` (bigger,
    feature-sized; likely too large for one loop, may need its own
    decomposition).
  - `CTEDMLPrefix` nested-node EXPLAIN instrumentation residual.
  - One of the 4 named writeback simplifications-vs-upstream in the
    2026-07-05 deferral ledger row (range coalescing / per-session
    backend_flush_after / activity-registry timing for bgwriter+checkpointer
    / bgwriter+checkpointer write counters) — each independently scoped.
  - Or continue M0119-0004 pg_dump catalog-view parity battery / next
    unresolved DU-002 slice from `.ralph/deferral_ledger.md`.
Recommend starting with `CTEDMLPrefix` or one writeback simplification —
smallest, most bounded of the listed options.

Gates run this loop: `go build ./...` (clean), `go test -count=1` on
storage/executor/planner/parser/server/config (all PASS, fresh not cached),
`scripts/tpch-spotcheck.sh` (PASS Q12=2/Q13=33), pgbench pre-commit smoke
(PASS, auto-run by hook), `make ralph-state-guard` (repaired, now consistent).
