Task: M0130-S4 — B5 verification: index/attrdef native WAL kinds (COMPLETE)

Files:
- internal/wal/record_kind_rmgr_mapping_test.go: added 2 regression gate tests
  (TestActiveRecordKindValuesNotRetiredB5IndexAttrdef +
  TestNativeApplyRecordKindKnownRejectsRetiredB5IndexAttrdef)
- docs/design/0130-0004-b5-index-and-attrdef-retirement.md: status → accepted,
  gates section updated with verification results + new regression gates

Key symbols:
- nativeApplyRecordKindKnown: gatekeeper function tested for retired kinds
- recordKindToRmgrInfo: verified retired kinds fall through to RmgrGoopgCatalog
- All active RecordKind constants enumerated in the new test

Hypothesis/Findings:
- grep audit confirmed zero emit sites: all references to kinds 20/21/94/69
  are in comments documenting retirement
- nativeApplyRecordKindKnown has no arms for retired kinds → legacy WAL
  records route to replayDecodedXLogRecord (FATAL on real PG standby)
- recordKindToRmgrInfo maps retired kinds to RmgrGoopgCatalog (default arm)
- 27 active RecordKind constants enumerated; none uses retired values

Next step: M0130-S5 — B5 verification: view/matview native WAL kinds (est ~1
loop). Kinds 102/103 already retired at HEAD (commit 2697504f); grep-verify
zero emit sites; confirm standby DDL replay.

Gates run:
- go test -run 'TestActiveRecordKindValuesNotRetiredB5IndexAttrdef|TestNativeApplyRecordKindKnownRejectsRetiredB5IndexAttrdef' ./internal/wal/...: PASS
- go test ./internal/wal/...: PASS (5.4s)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all cached)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, 13507 TPS)
- make ralph-state-guard: REPAIRED + PASS

In-flight: none
