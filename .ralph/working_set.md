Task: (loop #9 — design 0117-0009) CLEARED the meta-blocker that made loops
#7/#8 report BLOCKED. The mandated TPC-H spot-check gate infra-failed because
goopg startup hung; fixed. Committed this loop.

What landed:
- `EnablePGSLRUMirror` startup backfill no longer fsyncs once per XID (~1.5M
  fsyncs → >6 min on WSL2). Now routes through the existing batched
  `mirrorTerminalRangeBatchedUnlocked` (one fsync per segment, ≈2 total).
  Byte-equivalent; live per-commit `mirrorToSLRUUnlocked` path untouched.
- Files: internal/mvcc/clog.go (backfill block ~L839-862),
  internal/mvcc/clog_dual_store_consistency_test.go (new
  TestCLogEnableMirrorBackfillBatched), docs/design/0117-0009-*.md + README,
  .ralph/fix_plan.md (M0117 enabler note).
- Empirical proof: fresh `scripts/tpch-spotcheck.sh` start on the 2.2 GB bench
  dir reached *ready* in ~35 s (was >6 min). Gate now runs past readiness.

Gates run: go build ./... PASS; go vet ./internal/mvcc PASS; go test -race
./internal/mvcc/ ./internal/wal/ PASS; new regression PASS; make
ralph-state-guard OK (self-repaired); pgbench pre-commit smoke (on commit).

Next step (the gate is usable again):
1. The bench data dir `bench/tpch/runtime_goopg/data` lacks the `tpch` role, so
   the spotcheck SKIPs at the schema probe. Reload via
   `bench/tpch/setup_goopg.sh` + `build_schema_goopg.sh` to restore the real
   Q12/Q13 row-count run (re-pin Q13 after reload). THEN the populated-data gate
   is fully runnable.
2. With the gate runnable, a HUMAN/dedicated session can finally land the
   deferred M0117 live-path slices: 0117-0006 Part B (CLOG store swap, per the
   blueprint in design 0117-0006 §"Part B implementation blueprint") and
   0117-0007 Part B (async commit), gating with race mvcc/wal + xlog_replay +
   heterogeneous PG-standby E2E + fresh-server TPC-H Q12/Q13 + pgbench.
3. Alternatively unpause M0110 (pg_dump TAP, incremental/self-promoting).
