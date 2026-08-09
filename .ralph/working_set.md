Task: M0130-S2 — pg_class heap persistence, reverse path COMPLETE

Files:
- internal/initdb/reverse_path_test.go: NEW — TestReversePathColdStartOpensWithoutCache
  validates that Open() succeeds when the goopg catalog cache is absent (simulating
  a PG-created data dir), and that core system catalogs are accessible
- docs/design/0130-0002-pg-class-heap-persistence.md: updated with reverse-path
  implementation section, WAL replay constraint, and remaining deferred items
- .ralph/fix_plan.md: M0130-S2 marked [x] DONE

Key symbols:
- loadUserTablesFromHeap: cold-start heap-scan path used when cache absent
- DecodePGClassPhysicalRow: PG fixed-offset decoder that reads PG-created rows
- isCheckpointRecord: recognises PG shutdown checkpoints (replay no-op)
- readCatalogCache / writeCatalogCache: goopg-specific fast-start markers

Hypothesis/Findings:
- The cold-start path (cache absent → heap scan) already handles PG-created data
  dirs correctly. The decoder fallback (DecodePGClassRow → DecodePGClassPhysicalRow)
  reads PG-physical-format rows.
- WAL replay is a no-op for cleanly shut down PG data dirs (shutdown checkpoint
  recognised by isCheckpointRecord). Unclean PG WAL would fail on unsupported
  rmid types — documented constraint.
- registerSystemTables() provides system catalogs; loadUserTablesFromHeap adds
  user tables. The combination is sufficient for practical reverse-path use.
- Deferred: system catalogs from heap, unclean PG WAL replay, E2E PG-attach test.

Next step: M0130-S3 — Catalog heap sync for remaining DDL (ADD COLUMN →
pg_attribute sync, CREATE SCHEMA → pg_namespace, pg_collation/FDW/server heap rows)

Gates run:
- go test ./internal/initdb/...: PASS (55s, all tests including new reverse path test)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all cached)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- make ralph-state-guard: REPAIRED + PASS

In-flight: none
