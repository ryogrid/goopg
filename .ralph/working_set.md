Task: M0122-0008 encoding conversion — LATIN1↔UTF8 first slice (LANDED)

Files:
- internal/mb/wchar.go: UTF8 validation (pgUTFMblen, pgUTF8IsLegal, error types)
- internal/mb/latin1.go: iso8859_1_to_utf8 + utf8_to_iso8859_1 conversion procs
- internal/mb/conv.go: ConvProc type, BuiltinConversions, DoEncodingConversion dispatch
- internal/mb/latin1_test.go: round-trip test (0x00–0xFF), expansion test,
  invalid/untranslatable rejection, DoEncodingConversion fast-path tests
- internal/catalog/catalog.go: FindDefaultConversionProc(forEnc, toEnc) method
- internal/server/dispatch.go: maybeConvertCellsForClientEncoding helper +
  wire-in at simple-query DataRow + cursor FETCH DataRow sites
- internal/server/extended.go: wire-in at extended-query Execute DataRow path
- docs/design/0122-0020-encoding-conversion-mb-layer.md: design doc
- docs/design/README.md: index entry
- .ralph/fix_plan.md: updated M0122-0008 status
- .ralph/deferral_ledger.md: new row for remaining encoding pairs

Key symbols:
- mb.ConvProc — func([]byte, bool) (int, []byte, error)
- mb.DoEncodingConversion — core dispatch with PG-faithful fast paths
- mb.BuiltinLookup — ConvProcLookup over BuiltinConversions map
- Server.maybeConvertCellsForClientEncoding — wire-in helper
- catalog.InMemory.FindDefaultConversionProc — lookup by (forEnc, toEnc)

Hypothesis/Findings:
- LATIN1↔UTF8 conversion works end-to-end: live psql with client_encoding=LATIN1
  correctly transcodes accented characters (é = 0xE9 ↔ 0xC3 0xA9)
- The wire-in architecture (single helper, called before PutDataRowScratch at all
  three DataRow sites + Bind parameter path) is clean and extensible
- Additional encoding pairs are straight ports of PG conversion procs (~100-200
  lines of C each)

Next step:
- M0130 is COMPLETE. M-NIGHTLY is clear. Remaining actionable items:
  (1) M0122-0008 follow-up: additional encoding pairs (EUC_JP, SJIS, etc.)
  (2) M0122-0010 btree locking (Lehman-Yao crab-walk first slice —
      needs buffer-pool fix first, risk documented in scouting report)
  (3) M0122-0008 follow-up: query-text transcoding, pg_encoding_max_length

Gates run:
- go test ./internal/mb/...: ALL PASS (8 tests)
- go test ./internal/catalog/...: PASS
- go test ./internal/server/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- scripts/tpch-spotcheck.sh: PASS (Q12=2, Q13=35)
- Live psql: client_encoding=LATIN1 correctly transcodes café/chr(233)
- make ralph-state-guard: REPAIRED → CONSISTENT

In-flight: none
