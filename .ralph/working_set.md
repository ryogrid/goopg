Task: M0122-0008 pg_char_to_encoding() builtin dispatch (LANDED)

Files:
- internal/executor/expr.go: Added case "pg_char_to_encoding" dispatch arm in
  evalFuncCall switch (line 11555); delegates to catalog.EncodingNameToID
- internal/executor/encoding_builtins_test.go: Added TestEvalPgCharToEncoding
  (7 subtests: canonical UTF8, alias unicode, LATIN1, NULL, unknown → -1,
  case insensitive, punctuation variant UTF-8)

Key symbols:
- evalFuncCall case "pg_char_to_encoding" — inline dispatch, no separate eval func
- catalog.EncodingNameToID — already existed, used by CREATE CONVERSION validation

Hypothesis/Findings:
- pg_encoding_to_char() was ALREADY implemented (dispatch arm at L11542, DU-002 slice 399)
- pg_char_to_encoding() was registered in pg_proc_seed_data.go (OID 1264, HandlerName
  "PG_char_to_encoding") but had NO dispatch arm in evalFuncCall
- The catalog-side function (EncodingNameToID + cleanConvEncodingName +
  pgConvEncAliases) already existed since DU-002 slice 400, so the executor work
  was a one-line delegation

Next step:
- M0130 is COMPLETE. M-NIGHTLY is clear (all 49 items resolved by command-counter fix).
  M0119-0005/0006/0007 are blocked on index AMs / types / logical decoding.
  Most tractable next items:
  (1) M0122-0008 remaining: channel binding (blocked on TLS) and actual byte-level
      encoding conversion (port PG mb/conversion_procs)
  (2) M0122-0010 btree locking (Lehman-Yao crab-walk first slice)
  (3) M0122-0008 small follow-ups: pg_encoding_to_char and pg_char_to_encoding
      are both done — only the hard blockers remain in this bucket

Gates run:
- go build ./internal/executor/...: OK
- go test -run TestEvalPgCharToEncoding: PASS (7/7)
- go test ./internal/executor/...: PASS (5.7s)
- go test ./internal/catalog/... (encoding): PASS
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- Live psql: pg_char_to_encoding('UTF8')=6, ('unicode')=6, (NULL)=NULL, ('nonexistent')=-1 ✓
- make ralph-state-guard: REPAIRED

In-flight: none
