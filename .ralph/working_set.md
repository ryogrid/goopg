Task: M0117-0006 Part C — remove the resident `banks`/legacy-flat-file CLOG
store now that the SLRU buffer pool (Part B, loop #11) is the sole live store.
COMPLETE and committed this loop (#48). Code changes themselves were already
authored by loop #47 (uncommitted, plan-only design-doc status at start of
this loop said "IN PROGRESS ... pending review"); this loop verified the
implementation against the plan, ran the full mandatory gate suite, fixed 3
stale doc comments + 1 now-dead parameter the implementation missed, and
closed out the bookkeeping (design doc, fix_plan, ledger).

Files: internal/mvcc/clog.go (940-line net removal: clogBank/banks/path/
dirtyMu/dirtyPages fields + getOrCreateBank/getBank/distributeToBanks/
markFlatDirty/flushDirtyPagesLocked/flush/flushLocked/
mirrorTerminalRangeBatchedUnlocked/loadFromSLRU deleted; banksMu→slruDirMu;
OpenCLog no longer reads path; IsEmpty rewritten to highestSLRUXID()==0
disk-truth check; TruncateCLOG/SetSubCommitted doc comments corrected THIS
loop); internal/mvcc/clog_groupcommit.go (mirrorGroupToSLRULocked/
applySegmentLanesLocked deleted; applyGroupBatchLocked's now-dead `batch`
param removed THIS loop); internal/mvcc/{clog,clog_dual_store_consistency,
clog_groupcommit,clog_slru_recovery,manager,snapshot_clog_fallback}_test.go +
internal/initdb/{xact_recovery,pg_catalog_physical_load}_test.go (test
migration to EnablePGSLRUMirror / test-local SLRU-segment decoders, per
loop #47's plan); internal/initdb/{open,initdb}.go (2 stale "legacy flat
file" comments corrected THIS loop, no logic change — EnablePGSLRUMirror call
sites were already correct); docs/design/0117-0006-clog-slru-buffer-pool.md
(status → Part A+B+C landed, gate results filled in); docs/design/README.md
(0117-0006 row updated); .ralph/fix_plan.md (M0117-0006 box checked, M0119-0002
narrowed to the 2 still-open CLOG siblings); .ralph/deferral_ledger.md (loop
#11 Part-C-deferred row flipped resolved, new Part-C-landed row appended).

Key symbols: CLog.pool (atomic.Pointer[clogBufferPool], now the ONLY store),
EnablePGSLRUMirror (creates the pool directly, no backfill round-trip),
IsEmpty/highestSLRUXID (disk-truth empty check), applyGroupBatchLocked
(pool.flushDirty(), no batch arg needed anymore).

Findings: implementation matched the design doc's plan exactly (verified
line-by-line against the diff) — the one gap was doc hygiene: 3 comments
(open.go, initdb.go, clog.go's TruncateCLOG/SetSubCommitted) still described
the deleted banks/flat-file mechanics, and applyGroupBatchLocked kept an
unused `batch` parameter (caught by go vet's unusedparams lint). No logic
bugs found. The design doc's "accepted compatibility cut" (pre-Part-B data
dirs' unmirrored flat-file bytes are now unrecoverable) is NOT a PG-fidelity
gap — PG has no such dual-store distinction, so this makes goopg MORE
PG-faithful, not less — recorded in the ledger only so it's not a surprise.

Next step: idle — nothing in flight for THIS task. Two CLOG siblings remain
open per M0119-0002: M0117-0007 Part B (live synchronous_commit=off) and
M0117-0008 Part B (on-disk datfrozenxid persistence) — both dedicated
full-gate-session Effort-L/M items. M0119-0009's 3 residual gaps (loop #46)
and M0119-0004's "still open" list (dump ordering, btree/hash amadjustmembers,
builtin-operator catalog) are independently resumable alternatives.

Gates run this loop: go build ./... clean; go vet ./... clean; go test -count=1
./internal/mvcc/... PASS; go test -count=1 ./internal/initdb/... PASS (169s);
go test -race ./internal/mvcc/... ./internal/wal/... PASS (1 unrelated
pre-existing internal/wal timing flake, reran green in isolation, package
untouched by this diff); go test -count=1 ./internal/server/... PASS;
TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart +
TestE2E_ChecksumStreamingGoopgToPG PASS; gofmt -l clean on all touched files;
scripts/tpch-spotcheck.sh RESULT=PASS (Q12=2 rows/28.32s, Q13=33 rows/90.45s);
make ralph-state-guard OK (self-repaired usual stale "completed" marker from
loop #47's clean exit). pgbench smoke runs at commit time via the pre-commit
hook.
