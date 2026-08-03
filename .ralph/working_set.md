(idle — nothing in flight)

M0127-P0.2 is CLOSED (loop #43, 2026-08-03) — the build side is now a single
pass. P0.1 + P0.2 done; P0 has one task left.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P0.3` (single-map build: planner threads key-type
info on `planner.Join`; executor picks int64 vs string map BEFORE the build;
extend the int64 path to Semi/Anti with the CTID exception preserved; delete
`lazyHashFinalize`'s dual-map dance). Bar: UNITS + DS05.**

Carry-over facts a next loop should not re-derive:

- **P0.2 shape:** the two build loops are now `joinOp.buildLoopLeft` /
  `buildLoopRight` in `internal/executor/operators_join_agg.go`, called by
  `buildLazyHashTable` with the child already Open and closed by the caller.
  `drainRowsBounded` is gone from this path (still used by
  `fused_hash_join.go:150`, which dies at P6.1). New helper `ownedBuildRow`
  carries the drain's M0073-0004 / M0097-0058 copy discipline.
- **`ctx.WorkMem` no longer reaches the hash build, on purpose** — it bounded
  only the intermediate `[]Row`, never the map it fed. Ledger row
  `2026-08-03 M0127-P0.2` cites `nodeHash.c: ExecHashIncreaseNumBatches`;
  resume point is P3.2. Do not "restore" the budget.
- **P0.3 entry points:** `lazyHashInsertDatum` (:1065 area) populates BOTH maps
  and `lazyHashFinalize` picks the survivor; the Semi/Anti lane in
  `buildLoopRight` bypasses it and appends to `o.lazyHash` directly;
  `buildHashRightWithCTID` is the CTID exception that must stay on the string
  map.
- **RACE gate is RED at clean HEAD** — `buildEnvInFlight` (`executor.go:35-41`,
  M0126-0006) is a package global written by every `buildWithEnv`, and Gather
  workers call `BuildWorker` concurrently. Reproduced in a HEAD worktree;
  already filed under M-NIGHTLY (fix_plan ~L1148). Every race frame is
  `buildWithEnv`; do not re-triage it as a new regression.
- **Do NOT `git stash`** in this tree — 9 unrelated stash entries exist and a
  bare `git stash pop` targets `stash@{0}` (a 2026-07-29 interactive pause).
  Compare against HEAD with `git worktree add /tmp/... HEAD` instead.
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Landed-task tracking = `docs/design/0127-pg-shaped-join-search.md` §6 +
  fix_plan checkbox + README index status.

Gates run this loop: UNITS precommit PASS; SPOT `scripts/tpch-spotcheck.sh`
PASS (Q12 rows=2, Q13 rows=35, 18.4 s query phase, peak 10,332 MB);
`go test ./internal/executor/` PASS; RACE red for the pre-existing cause above;
pgbench smoke via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
