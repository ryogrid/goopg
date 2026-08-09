Task: M0130-S7 — WAL fidelity audit: xl_prev 0-based + atomic heap-update (COMPLETE)

Files:
- internal/wal/pg_waldump_compat_test.go: added TestCrossSegmentXLPrevChain
  cross-segment regression gate (32-byte segments, ReadAll verifies all
  records recovered across boundary)
- docs/design/0130-0007-wal-record-fidelity-xlprev-atomic-update.md:
  status draft → accepted; S7.1 verification results + S7.2 audit findings
  (three-path trace: PG-canonical atomic, general two-record gap, native legacy)
- docs/design/README.md: 0130-0007 status draft → accepted
- .ralph/fix_plan.md: M0130-S7 marked [x] DONE

Key symbols:
- detectWritePos (writer.go:1385-1399): xl_prev −1 conversion
- EncodeHeapUpdatePG (pg_assembled_emit.go:254): PG-compatible atomic record, production
- EncodeHeapUpdate (recovery.go:1496): native legacy, zero production call sites
- updateHeapRowCanonicalPG (operators_storage.go:8526): PG-canonical path, atomic
- updateOp.Next() non-HOT fallback (operators_storage.go:4111): two-record gap
- LogHeapUpdateFunc (bufpool.go:723): WAL hook type, wired via initdb/open.go

Hypothesis/Findings:
- S7.1: xl_prev 0-based fix CONFIRMED at HEAD. Cross-segment chain verified
  via new TestCrossSegmentXLPrevChain (goopg ReadAll, 32-byte segments).
  pg_waldump not used for cross-segment because it requires standard segment
  sizes (1 MiB–1 GiB); single-segment pg_waldump coverage exists in
  TestPGWaldumpParsesEmittedWAL.
- S7.2: Three heap-update WAL paths traced and classified:
  1. updateHeapRowCanonicalPG (catalog ALTERs): atomic EncodeHeapUpdatePG ✅
  2. updateOp.Next() non-HOT fallback (user tables): two separate records
     (HeapDelete + HeapInsert) ⚠ — known ledger gap M0118-0129
  3. tryApplyHOTUpdate (same-page HOT): atomic RecordKindHeapHotUpdate ✅
  4. EncodeHeapUpdate native (RecordKindHeapUpdate=27): legacy/test-only,
     zero production call sites (confirmed in M0130-S6)
- Correction: PG's xl_heap_update does NOT carry the old tuple image — only
  14 bytes of metadata (old_xmax, old_offnum, flags, new_offnum). goopg's
  EncodeHeapUpdatePG matches this exactly.

Next step: M0130-S8 — Multi-timeline START_REPLICATION + timeline reconciliation
(est ~2 loops). TLI source-of-truth = pg_control; IDENTIFY_SYSTEM/START_REPLICATION
TIMELINE n; promotion TLI bump. Design:
docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md.

Gates run:
- go test ./internal/wal/...: PASS (5.3s, full suite)
- TestCrossSegmentXLPrevChain: PASS (new)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all cached)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, TPC-B 424.93 tps,
  simple-update 629.92 tps, select-only 13420.68 tps)
- make ralph-state-guard: REPAIRED + PASS

In-flight: none
