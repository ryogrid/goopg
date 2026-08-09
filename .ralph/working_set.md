Task: M0122-0008 pg_client_encoding() + getdatabaseencoding() builtins (LANDED)

Files:
- internal/executor/expr.go: Added dispatch cases for pg_client_encoding and
  getdatabaseencoding in evalFuncCall switch; implemented evalPgClientEncoding
  (reads client_encoding GUC via GetSetting) and evalGetDatabaseEncoding
  (reads encoding ID from catalog.InMemory.DatabaseEncoding + maps via
  EncodingIDToName)
- internal/executor/encoding_builtins_test.go: TestEvalPgClientEncoding +
  TestEvalGetDatabaseEncoding (nil-ctx fallback, custom GUC, non-default encoding)
- .ralph/fix_plan.md: Updated M0122-0008 status
- unimplemented_feat.json: Updated encoding entry code_audit

Key symbols:
- evalPgClientEncoding(row, ctx) — returns current client_encoding as name Datum
- evalGetDatabaseEncoding(row, ctx) — returns database encoding as name Datum

Hypothesis/Findings:
- Both functions were registered in pg_proc_seed_data.go with HandlerName entries
  but had no dispatch arms in evalFuncCall → returned "function does not exist"
- Bootstrap encoding enforcement (initdb --encoding validation via resolveEncoding)
  was already fully implemented — the working_set's "Next step" from the previous
  loop was stale
- getdatabaseencoding() uses a type assertion to *catalog.InMemory because
  DatabaseEncoding is not on the Catalog interface (only on the concrete type)
- Verified against live psql: default UTF8, SET to LATIN1 reflects correctly

Next step:
- M0130 is COMPLETE. M0119-0005/0006/0007 are blocked/huge (need index AMs + types).
  M0122-0008 remaining: (1) channel binding blocked on TLS; (2) actual byte-level
  encoding conversion (port PG mb/conversion_procs). Most tractable next item:
  start on M0122-0010 btree locking (Lehman-Yao crab-walk first slice), or
  continue M0122-0008 with a small encoding follow-up like pg_encoding_to_char()
  and pg_char_to_encoding() SQL builtins (also registered but may lack dispatch).

Gates run:
- go build ./internal/executor/...: OK
- go test -run TestEvalPgClientEncoding/TestEvalGetDatabaseEncoding: PASS
- go test ./internal/executor/...: PASS (5.8s)
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- make ralph-state-guard: REPAIRED (progress.json reconciled)

In-flight: none
