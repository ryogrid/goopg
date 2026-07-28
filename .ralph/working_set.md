(idle — nothing in flight)

Last loop (#5): M-NIGHTLY item (c) — the root-0032 §5 redo failure. Fixed and
committed as **root-0033**
(`docs/design/root-0033-redo-prune-redirect-only-compaction.md`).

A crash under sustained write load left the cluster unstartable at
`wal: xlog heap-update add new tuple: storage: not enough free space in page`.
Root cause was a sibling divergence, not a WAL-format bug: the runtime pruner
`pagePruneCore` (`internal/storage/prune.go`) compacts on BOTH arms — with the
dead set when slots became unused, and with a **nil** dead set when the prune
produced only redirects, because `VacuumHeapPageBySlots` repacks only
`ItemIDNormal` survivors and a just-redirected chain root is `ItemIDRedirect`,
so its body is reclaimed. The PG-format redo arm `replayDecodedXLogHeapPrune`
(`internal/wal/recovery.go`, introduced by the A7 `EncodeHeapPruneOptPG` switch)
guarded that repack on `len(unused) > 0`, so a redirect-only prune — the common
pgbench HOT shape — skipped it and permanently shrank the replayed page's free
space. The legacy native arm `replayHeapPruneOpt` in the same file always
compacted, so only the PG-format sibling had drifted.

Remaining M-NIGHTLY steps from the same investigation (all still open, they
preempt M0124): (a) re-run the alphabetical regress PREFIX through
`select_distinct` and finally MEASURE `portals_p2` / `select` /
`select_distinct` — with root-0032 + root-0033 the restart should now succeed,
so the 53 phantom `deferred: cluster restart failed` cases should disappear;
(b) `index_including`'s real divergence (diffs land in `GOOPG_REGRESS_DIFF_DIR`);
(d) the harness's phantom `deferred:` per case after a failed restart (ledger
2026-07-28) — collapse the tail into one `regress/cluster-dead` item the way
root-0029 collapsed the wedge. ALWAYS grep a suite log for
`restarting the cluster` / `restart failed` before believing a case result.

Gates run: `analysis/wal-crash-restart-repro.sh LOADSEC=200 KILLAT=170` — HEAD
FAILS, fixed build reports `RESTART_OK` (the defect itself, end to end);
`go test ./internal/wal/ ./internal/storage/` PASS; `go test -race
./internal/wal/` PASS; negative control on the new test (fails with the fix
reverted: pd_upper 8112 vs 8160, so non-vacuous);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (exit 0);
pgbench smoke via the commit hook. tpch-spotcheck deliberately not run — WAL
redo path only, no planner/executor/codec change.
In-flight: none.
