(idle — nothing in flight)

---

**Loop #32 (this session) — COMPLETE, committed + pushed.**

Task: M0122-0003 — implement 2 of the 5 still-`0` `pg_stat_io` op counters
named as open by the prior loop's ledger row: `evictions` and `extends`
(+ `extend_bytes`).

Landed: `storage.Pool` (`internal/storage/bufpool.go`) gains
`sharedEvictionCount`/`sharedExtendCount` atomic counters +
`EvictionCount()`/`ExtendCount()` accessors — same pattern as the
pre-existing `sharedDirtiedCount`/`sharedWrittenCount`/`sharedReadTimeNanos`
trio (own accessor pair, not a wider `BufferCounters()` tuple, to avoid
touching its 4 existing call sites). `sharedEvictionCount` increments once
in `evictVictim`, right after the "slot was free" early-return check (only
when a real, valid tag is displaced), regardless of dirty state — a strict
superset of the dirty-only `sharedWrittenCount`. `sharedExtendCount`
increments once in `PinNew` right after its `p.mgr.Extend` call succeeds —
confirmed the pool's *only* smgr-Extend call site (grepped, no sibling).
`internal/executor/pgstat_io.go`'s `fetchIOStatRows` wires both into the
existing per-op `switch` for the one row goopg instruments (client
backend/relation/normal); `extends` also renders `extend_bytes`
(`count*8192`). Tests: `internal/storage/bufpool_counters_test.go`
(`TestBufferCountersEvictionAndExtend` — 2-slot pool, fills to capacity
with 0 evictions then forces N further evictions via N more `PinNew`
calls), `internal/executor/pgstat_io_test.go`
(`TestPgStatIOEvictionsAndExtendsRendered` — end-to-end rendered-cell
assertion). Design: `docs/design/0122-0003-explain-format-xml-yaml.md`
new "`evictions`/`extends` counters" section; `docs/design/README.md` row
extended. Ledger row appended (`.ralph/deferral_ledger.md`, 2026-07-05,
M0122-0003, narrows the prior read-timing row). `.ralph/fix_plan.md`'s
M0122-0003 item + "Next up" banner updated to reflect current remaining
scope. Committed as `e1e64c2b` (8 files, pathspec-scoped commit), pushed.

Concurrency note: a live peer `ralph_loop.sh` tree (own process tree,
loop #38→#39 by the time this loop finished) was active throughout —
confirmed via `pgrep -af ralph_loop.sh`/`ps -ef` showing two genuinely
independent process trees (different parent PIDs), not the documented
subshell-argv-duplication artifact. The peer's own loop-#38 carry note
(overwritten by this entry) recorded that it saw this loop's in-flight
edits to `pgstat_io.go`/`bufpool.go`/their tests and deliberately left
them untouched, confirming disjointness from both sides before either
committed. The peer landed 2 commits on fully disjoint files during this
loop (`e1387bb1` DEFAULT-clause stale-entry closure + its `e5893325`
working_set carry, both M0122-0004 `unimplemented_feat.json`/bookkeeping
only) — confirmed via `git log`/`git diff --cached` before this loop's own
commit, no functional overlap. This loop's own commit used an explicit
pathspec covering only the files it exclusively owned (storage/executor
source+tests, ledger, design docs, fix_plan.md) — never touching
`.ralph/progress.json` (still shows a harmless timestamp-only diff from
`make ralph-state-guard`'s auto-repair; left uncommitted, likely the
driver's own bookkeeping to pick up) or `unimplemented_feat.json` (peer
already committed it before this loop's own commit ran).

Next step: M0122-0003 remaining sub-items (per the fix_plan banner):
`EXPLAIN (BUFFERS)` without ANALYZE (planning-time buffers — no
planning-phase buffer counters exist), local/temp-buffer terms,
`write_time`/`extend_time` + the 3 remaining `pg_stat_io` op counters
(reuses/writebacks/fsyncs — each needs a genuinely new counting mechanism:
strategy-ring reuse, bgwriter/checkpointer-scoped writeback attribution,
fsync call-site instrumentation respectively — not mechanical extensions
of the eviction/extend pattern), EXPLAIN's `I/O Timings` line, and a
`CTEDMLPrefix` nested-node instrumentation residual. Alternatively:
M0119-0004 pg_dump catalog-view parity, or the next unresolved M0122-0005
sub-item (per the peer's own carry notes: 1-byte `char` OID
disambiguation, `pg_collation_for()`). Re-check `git status` +
`pgrep -af ralph_loop.sh` fresh at loop start — multiple independent
loops may still be running concurrently on this tree.

Gates run: `go build ./...` clean; `go vet` clean on
`internal/storage/... internal/executor/...`; `go test
./internal/storage/... ./internal/executor/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pre-commit pgbench smoke
PASS (machine-enforced hook, ~235-14365 TPS across TPC-B/update/
select-only); `make ralph-state-guard` — found + auto-repaired a
stale-`completed`-marker inconsistency in `.ralph/progress.json` (the
peer's clean-exit marker from a prior loop, not a project-completion
marker), consistent after repair.
