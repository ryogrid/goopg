Task: M0130-S6 — Verify zero rmid-128 emitted; document keep-classify-arms (COMPLETE)

Files:
- internal/wal/record_kind_rmgr_mapping_test.go: added regression gate test
  TestNoActiveRecordKindUnexpectedlyRoutesToRmgrGoopgCatalog
- docs/design/0130-0006-rmgr-goopg-catalog-retirement.md: status draft → accepted,
  gates section updated with verification results
- .ralph/fix_plan.md: M0130-S6 marked [x] DONE

Key symbols:
- recordKindToRmgrInfo: default arm → RmgrGoopgCatalog for 10 legacy kinds
- nativeApplyRecordKindKnown: returns true for legacy kinds (goopg↔goopg recovery)
- RmgrGoopgCatalog: deliberately KEPT for legacy kinds + pre-B5 WAL replay

Hypothesis/Findings:
- Zero production emit sites produce rmid-128 records. grep audit confirmed:
  EncodeXactAssignment, EncodeXactRollbackTo, EncodeXactSubAbort,
  EncodeSmgrTruncate, EncodeHeapUpdate, EncodeHeapMultiInsert,
  EncodeHeapVisible, EncodeBtreeReusePage, EncodeBtreeMetaCleanup — ALL
  called only from *_test.go files.
- 10 legacy RecordKind values fall through to RmgrGoopgCatalog: Checkpoint(2),
  SmgrTruncate(12), XactAssignment(15), XactRollbackTo(16), XactSubAbort(17),
  HeapUpdate(27), HeapMultiInsert(28), HeapVisible(29), BtreeReusePage(30),
  BtreeMetaCleanup(31).
- All 6 B5-retired catalog kinds (20/21/69/94/102/103) still route to
  RmgrGoopgCatalog via default arm, and nativeApplyRecordKindKnown rejects them.
- RmgrGoopgCatalog constant + default arm + recovery classify arms are
  deliberately KEPT (not deleted) for pre-B5 WAL replay + test infrastructure.

Next step: M0130-S7 — WAL fidelity audit: xl_prev 0-based + atomic heap-update
(est ~1 loop). xl_prev already fixed at HEAD; audit atomic heap-update
completeness (ledger #27).

Gates run:
- go test -run 'TestNoActiveRecordKindUnexpectedlyRoutesToRmgrGoopgCatalog' ./internal/wal/...: PASS
- go test ./internal/wal/...: PASS (5.2s)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all cached)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, TPC-B 427.97 tps, simple-update 632.28 tps, select-only 13519.28 tps)
- make ralph-state-guard: REPAIRED + PASS

In-flight: none
