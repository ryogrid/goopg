# B5 group C — view/matview native WAL kinds verification (landed 2026-07-19)

**Status:** draft
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

1. **grep audit:** confirm zero emit sites remain for kinds 102/103:
   `grep -rn "RecordKind(CreateView|CreateMatView)" --include='*.go'`
   must return zero results outside of test files and the classify mapping.
2. **Standby DDL replay:** verify CREATE VIEW / CREATE MATERIALIZED VIEW
   DDL replays on a PG standby without errors.
3. **M0123 deferral:** confirm the canonical node-tree deferral is
   ledger-recorded with a clear resume point.

## Gates

1. grep-audit clean.
2. UNITS + wal suite green.
3. Standby E2E: view/matview DDL replays on PG standby.

## References

- `.ralph/deferral_ledger.md` — B5 FEASIBILITY row (2026-07-18)
- Commit `2697504f` — B5-C landing
- `internal/executor/operators_ddl.go` — retirement comments
- `internal/wal/record_kind_rmgr_mapping_test.go` — classify mapping
- M0123 (`docs/milestones/0123-canonical-pg-node-tree-serialization.md`) — canonical node-tree deferral
- `postgres/src/include/catalog/pg_rewrite.h`
