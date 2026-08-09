Task: M0122-0008 follow-up — LATIN2↔UTF8 encoding pair (second built-in pair)

Files:
- internal/mb/latin2.go: iso8859_2_to_utf8 + utf8_to_iso8859_2 conversion procs
  with [128]uint16 forward table (from PG's iso8859_2_to_utf8.map) +
  map[uint16]byte reverse lookup (init()-built)
- internal/mb/latin2_test.go: 8 tests (round-trip, expansion, untranslatable,
  NUL noError, dispatch integration, table integrity, reverse-map consistency)
- internal/mb/wchar.go: added PG_LATIN2 = 14
- internal/mb/conv.go: registered OIDs 4493/4492 in BuiltinConversions;
  refactored BuiltinLookup from if-else to generic builtinPair table
- docs/design/0122-0020-encoding-conversion-mb-layer.md: Follow-up section
- docs/design/README.md: updated entry

Key symbols:
- iso8859_2_to_utf8_table [128]uint16 — forward mapping (byte 0x80–0xFF → UTF8)
- iso8859_2_from_utf8_map map[uint16]byte — reverse mapping (init()-built)
- builtinPair map[[2]int32]uint32 — generic encoding-pair→proc-OID table

Hypothesis/Findings:
- The LATIN2 pattern proves the ISO 8859 family can be added mechanically:
  extract .map → create latin<N>.go → two-line BuiltinConversions registration
  → one-line builtinPair entry. Remaining 12 ISO 8859 variants are now trivial.
- Multi-byte encodings (EUC_JP, SJIS) remain separate slices — they need
  variable-width parsers and 4000+-entry mapping tables (radix trees in PG).
- The BuiltinLookup refactor (if-else → table) was clean and will scale.

Next step:
(1) M0122-0008 follow-up: remaining 12 ISO 8859 variants (LATIN3–10, ISO_8859_5–8)
    — mechanical addition following the same pattern
(2) M0122-0008 follow-up: query-text transcoding, pg_encoding_max_length
(3) M0122-0010 btree locking (Lehman-Yao crab-walk first slice)

Gates run:
- go build ./...: CLEAN
- go test ./internal/mb/...: ALL PASS (22 tests — 14 existing + 8 new)
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: 1 failed txn (pre-existing non-FIFO tuple-lock)
- scripts/tpch-spotcheck.sh: PASS (Q12=2, Q13=35)
- make ralph-state-guard: REPAIRED → CONSISTENT

In-flight: none
