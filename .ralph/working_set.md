Task: M0122-0007 verification + M0122-0008 encoding validation (COMPLETE this loop)

Files:
- internal/catalog/encoding.go: Added ValidServerEncodingName(name string) int32 +
  pgEncodingBELast constant (34 = PG_KOI8U)
- internal/catalog/catalog.go: Added databaseEncoding map[string]int32 +
  DatabaseEncoding/SetDatabaseEncoding methods + DropDatabase cleanup
- internal/catalog/conversion_test.go: Added TestValidServerEncodingName
- internal/server/database_ddl.go: Added createDatabaseEncodingRe regex,
  extractEncodingRawFromSQL, databaseEncodingRegistry interface; encoding
  validation in tryHandleDatabaseDDL (ValidateServerEncodingName rejection);
  dbEncodingID threaded through SetDatabaseEncoding + syncPgDatabaseHeapRow
- internal/executor/sys_pg_database.go: encodingOverride int32 param added
  to SyncPgDatabaseCatalogRow + clonePgDatabaseRowForCreate (overrides
  template encoding in pg_database heap row ordinal 3)
- .ralph/fix_plan.md: M0122-0007 marked [x] COMPLETE; M0122-0008 status
  updated with actual remaining items

Key symbols:
- ValidServerEncodingName(name) int32 — validates encoding is known + server-safe
- extractEncodingRawFromSQL(sql) (string, bool) — parses ENCODING option from raw SQL
- databaseEncodingRegistry (SetDatabaseEncoding) — new interface on catalog
- SyncPgDatabaseCatalogRow(..., encodingOverride int32) — new param
- clonePgDatabaseRowForCreate(..., encodingOverride int32) — new param

Hypothesis/Findings:
- M0122-0007 is actually COMPLETE: all three "remaining" items (physical storage
  isolation, WITH FORCE, REINDEX CONCURRENTLY) were already implemented in prior
  loops but the fix_plan checkbox was stale.
- M0122-0008: most items landed in prior loops. This loop added CREATE DATABASE
  encoding validation (name-to-ID resolution + server-encoding validity check +
  catalog storage + heap-row override). Genuinely remaining: channel binding
  (blocked on TLS) and deeper encoding constraints (client_encoding GUC
  validation, locale↔encoding mismatch checks — goopg doesn't do actual
  encoding conversion).

Next step:
- Per priority banner: M0119 then M0122. M0119-0005/0006/0007 are blocked/huge.
  M0122-0009 (MultiXact WAL) is multi-loop. M0122-0010 (Lehman/Yao) is multi-loop.
  Most actionable: M0122-0008 channel binding (if TLS infrastructure is built)
  or broader encoding enforcement (client_encoding GUC validation).

Gates run:
- go build ./internal/...: OK
- go test ./internal/catalog/...: PASS (TestValidServerEncodingName PASS)
- go test ./internal/server/...: PASS (all database DDL tests PASS)
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, 408263 txns, 13609 tps)
- ralph-state-guard: REPAIRED (in_progress)

In-flight: none
