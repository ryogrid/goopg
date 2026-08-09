Task: M0119-0010 — char(N) typmods not restored per column on catalog reload — FIXED

Files:
- internal/catalog/codec.go: added pgAttributeOffTypMod=76 constant,
  AttTypMod int32 field on PGAttributeRow, decoding in
  DecodePGAttributePhysicalRow at offset 76.
- internal/initdb/open.go: loadUserTablesFromHeapForDB now calls
  pgTypeArgsFromTypmod(typOID, ar.AttTypMod) to reconstruct Type.Args.
- internal/initdb/heap_catalog_load_test.go: added
  TestBpCharVarcharTypModRestoredAfterRestart regression test.

Key symbols: PGAttributeRow.AttTypMod, DecodePGAttributePhysicalRow,
loadUserTablesFromHeapForDB, pgTypeArgsFromTypmod, coerceTextLikeDatum

Hypothesis/Findings:
- ROOT CAUSE: DecodePGAttributePhysicalRow never decoded atttypmod
  (offset 76), so loadUserTablesFromHeapForDB set Args: nil on every
  column. coerceTextLikeDatum defaulted to n=1, so every reloaded
  char(N) was length-checked as char(1).
- FIX: 3 lines of code + 1 test. pgTypeArgsFromTypmod already existed
  for the domain-reload path (catalog_heap_reload.go:1216); reused it.
- Verified non-vacuous: stash revert → test fails with Args=[] for all
  3 columns.

Next step: Per Current Priority banner, M-NIGHTLY is next (all items are
checked [x] — verify the restart-failure-cascade infrastructure item
or move on). Then M0119-0004 (pg_dump TAP), M0119-0005 (pg_waldump),
M0119-0006 (pg_amcheck), M0119-0007 (recvlogical — blocked).

Gates run:
- go test ./internal/initdb/...: PASS (including new test, ~57s)
- go test ./internal/catalog/...: PASS (cached)
- go test ./internal/executor/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)

In-flight: none
