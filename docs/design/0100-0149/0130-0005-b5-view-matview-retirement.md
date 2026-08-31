# B5 group C — view/matview native WAL kinds verification (landed 2026-07-19)

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S5 — verification)

## Background

B5 group C retired goopg-native WAL record kinds 102/103 (CREATE VIEW /
CREATE MATERIALIZED VIEW). The implementation landed 2026-07-19 (commit
`2697504f`). This M0130 task verifies the retirement is complete and
confirms standby DDL replay.

## Landed state (HEAD)

- **Kind 102 (CREATE_VIEW):** retired. Runtime pg_rewrite (2618) writer
  produces heap rows with text `ev_action`. `loadViewsFromHeap` reload pass
  repopulates the rule cache at startup.
- **Kind 103 (CREATE_MATVIEW):** retired. Same heap-backed pattern.
- `view_ddl_recovery.go` and `matview_ddl_recovery.go` deleted.
- `relhasrules` is `false` in pg_class (goopg does not use the PG
  rewrite-rule system internally); this stays until M0123.
- Canonical `pg_node_tree` fidelity for `ev_action` is explicitly deferred
  to M0123. Text `ev_action` is sufficient for WAL replay — the content
  matters only when the view is *used*, not at replay time.

## Verification tasks (M0130 S5)

1. **grep audit:** confirmed zero emit sites remain for kinds 102/103.
   `grep -rn 'RecordKind\s*=\s*(102|103)'` returns no matches. All
   references to `RecordKindCreateView`/`RecordKindCreateMatView` are in
   comments documenting the retirement (recovery.go, xlog_record.go,
   catalog_heap_reload.go, testport view_pg_rewrite_durability_test.go,
   e2e_failover_goopg_to_pg_test.go).
2. **Standby DDL replay:** CREATE VIEW / CREATE MATERIALIZED VIEW DDL
   replays on a PG standby via native pg_rewrite heap inserts (XLOG_HEAP_INSERT
   on base/<dbOid>/2618). Verified by the existing
   `TestViewPgRewriteDurability` and `TestPort_E2EFailoverGoopgToPg`
   testport tests.
3. **M0123 deferral:** canonical node-tree fidelity for `ev_action` is
   ledger-recorded with a clear resume point. Text `ev_action` is sufficient
   for WAL replay — the content matters only when the view is *used*, not at
   replay time.

## Verification results (2026-08-09)

- **grep audit:** CLEAN — zero emit sites for kinds 102/103. Only comment
  references remain.
- **nativeApplyRecordKindKnown:** returns `false` for bytes 102 and 103
  (verified by `TestNativeApplyRecordKindKnownRejectsRetiredB5ViewMatview`).
  Retired-kind records correctly route to `replayDecodedXLogRecord` →
  FATAL "resource manager 128" on a real PG standby.
- **recordKindToRmgrInfo:** maps retired kinds 102/103 to
  `RmgrGoopgCatalog` via the default arm (verified by
  `TestActiveRecordKindValuesNotRetiredB5ViewMatview`).
- **Active RecordKind guard:** 28 active RecordKind constants enumerated;
  none reuses byte values 102 or 103 (verified by
  `TestActiveRecordKindValuesNotRetiredB5ViewMatview`).

## Gates

1. grep-audit clean. ✓
2. UNITS + wal suite green. ✓
3. `TestActiveRecordKindValuesNotRetiredB5ViewMatview` PASS.
4. `TestNativeApplyRecordKindKnownRejectsRetiredB5ViewMatview` PASS.
5. Standby E2E: view/matview DDL replays on PG standby (verified by
   existing testport tests).

## References

- `.ralph/deferral_ledger.md` — B5 FEASIBILITY row (2026-07-18)
- Commit `2697504f` — B5-C landing
- `internal/executor/operators_ddl.go` — retirement comments
- `internal/wal/record_kind_rmgr_mapping_test.go` — classify mapping
- M0123 (`docs/milestones/0123-canonical-pg-node-tree-serialization.md`) — canonical node-tree deferral
- `postgres/src/include/catalog/pg_rewrite.h`
