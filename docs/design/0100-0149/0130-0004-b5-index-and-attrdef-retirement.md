# B5 groups A+B — index/attrdef native WAL kinds verification (landed 2026-07-18)

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S4 — verification)

## Background

B5 groups A+B retired goopg-native WAL record kinds 20/21/94/69. The
implementation landed 2026-07-18 (commit `eb88b8a2`). This M0130 task verifies
the retirement is complete and adds regression gates.

## Landed state (HEAD)

### Group A — Index metadata (kinds 20, 21, 94)

- **Kind 20 (CREATE_INDEX):** retired. M0113 already made pg_index heap-backed;
  the heap insert produces PG-native WAL. No `EmitCreateIndex` call remains.
- **Kind 21 (DROP_INDEX):** retired. pg_index heap row deleted via heap
  mutation; PG-native WAL covers it.
- **Kind 94 (RENAME_INDEX):** retired. `ALTER INDEX RENAME` resyncs pg_index
  via heap update; PG-native WAL covers it.
- `index_ddl_recovery.go` deleted (kept only `index_ddl_recovery_test.go` for
  regression coverage).
- `operators_ddl.go` documents the retirement at the former emit sites.

### Group B — pg_attrdef (kind 69)

- **Kind 69:** retired. pg_attrdef (2604) is heap-backed.
- `SET DEFAULT` writes `Form_pg_attrdef` rows via `syncTableToCatalogHeap`.
- adbin stored as SQL text (canonical `pg_node_tree` deferred to M0123).
- `loadColumnDefaultsFromHeap` reloads at startup.
- `attrdef_ddl_recovery.go` deleted.

## Verification tasks (M0130 S4)

1. **grep audit:** confirm zero emit sites remain for kinds 20/21/94/69:
   `grep -rn "RecordKind(CreateIndex|DropIndex|RenameIndex|AttrDef)" --include='*.go'`
   must return zero results outside of test files and the classify mapping.
2. **Classify mapping:** verify `record_kind_rmgr_mapping_test.go` correctly
   classifies surviving non-catalog kinds.
3. **Regression gate:** extend the WAL test family to confirm index/attrdef
   DDL replays on a PG standby without errors.
4. **SurvivesRestart:** defaults survive restart and reload correctly.

## Gates

1. grep-audit clean. ✅ Verified 2026-08-09: zero emit sites; all references are
   in comments documenting the retirement.
2. UNITS + wal suite green. ✅ PASS (all 3 new regression tests + existing suite).
3. `WALPgWaldumpCompat` — records parsed. ✅ PASS (unchanged by this task).
4. SurvivesRestart: defaults survive restart. ✅ Existing coverage in
   `internal/initdb/index_ddl_recovery_test.go` + `internal/initdb/ddl_catalog_sync_test.go`.
5. Standby E2E: index DDL replays on PG standby. ✅
   `TestE2E_FailoverGoopgToPG` covers CREATE/ALTER INDEX post-failover.
6. Regression gates (added 2026-08-09):
   - `TestActiveRecordKindValuesNotRetiredB5IndexAttrdef` — enumerates every
     active RecordKind constant; fails if any uses retired byte values 20, 21,
     69, or 94.
   - `TestNativeApplyRecordKindKnownRejectsRetiredB5IndexAttrdef` — confirms
     `nativeApplyRecordKindKnown` returns false for retired kind bytes, so legacy
     WAL records route to the PG-xlog path (FATAL on real PG standby) rather than
     being silently dropped.

## References

- `.ralph/deferral_ledger.md` — B5 FEASIBILITY row (2026-07-18)
- Commit `eb88b8a2` — B5-A+B landing
- `internal/executor/operators_ddl.go` — retirement comments at former emit sites
- `internal/wal/record_kind_rmgr_mapping_test.go:53-66` — classify mapping
- `internal/initdb/open.go` — WAL recovery (no fallback for retired kinds)
- M0113 (`docs/milestones/0113-heap-based-index-recovery-via-pg-index.md`) — pg_index heap pattern
- `postgres/src/include/catalog/pg_attrdef.h`
