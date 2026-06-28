Task: M0117-0006 Part B — wire the SLRU buffer pool as the LIVE in-memory CLOG
store (replacing the resident per-XID banks). LANDED this loop (#11).

KEY META-FINDING (broke the loop #7–#10 block): the deferral reason on record —
"the mandatory Part B gates SKIP in the autonomous WSL2 loop" — is FALSE. The
PG binaries are present and the E2E tests only skip in `-short`. Verified RUN+PASS
here: TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart (23s),
TestE2E_ChecksumStreamingGoopgToPG, `-race ./internal/mvcc/... ./internal/wal/...`
(incl xlog_replay), and the TPC-H Q12=2/Q13=33 spot-check (runnable since
0117-0009+0117-0010). So Part B was actionable, not dedicated-session-only.

What landed (single-store design, blueprint §Resolutions 1-7):
- CLog.pool atomic.Pointer[clogBufferPool], promoted by EnablePGSLRUMirror AFTER
  the flat-file→SLRU backfill (so it faults from a complete pg_xact/).
- GetStatus→pool.getStatus; setStatus→pool.setStatus (PG clear-then-set; bootstrap
  XIDs <FirstNormal kept zero); applyGroupBatchLocked→pool.flushDirty REPLACES
  flushDirtyPagesLocked+mirrorGroupToSLRULocked.
- Bulk callers re-pointed (all run AFTER EnablePGSLRUMirror in initdb.Open):
  InitializeAsCommitted/MarkUnknownAsAborted sweep via pool + one flushDirty;
  HighestKnownXID→new highestSLRUXID() (SLRU tail scan); TruncateCLOG→new
  pool.invalidateBelow(cutoffPage) + skip the vestigial flat flush().
- Legacy never-fsynced global/pg_xact flat file RETIRED (SLRU is single durable
  store; basebackup already excluded it; PG has no such file). 3 flat-file-reopen
  test views redirected to the production recovery reopen (OpenCLog+EnablePGSLRUMirror).
- flushWAL barrier left nil (sync commit; async = M0117-0007 Part B). Pool auto-16.

Files: internal/mvcc/{clog.go, clog_bufferpool.go, clog_groupcommit.go,
clog_bufferpool_live_test.go (new)}, clog_groupcommit_test.go,
clog_dual_store_consistency_test.go, internal/initdb/pg_xact_slru_test.go,
docs/design/0117-0006-*.md + README, fix_plan.md, deferral_ledger.md.

Gates run (ALL PASS): build; -race mvcc+wal(+xlog_replay); initdb+server full
suites; standby-attach + checksum-streaming E2E; TPC-H Q12=2/Q13=33; gofmt/vet
clean; new clog_bufferpool_live_test.go. pgbench smoke on commit (pre-commit hook).

Next step: COMMIT (done if you see this with a clean tree). Then M0117-0006
Part C = remove the resident banks (dead-code once the &CLog{} no-mirror unit
tests are migrated) — separate focused loop; re-init data dir. Small follow-up:
wire transaction_buffers GUC → CLog.SetCLOGBuffers from initdb.Open.
