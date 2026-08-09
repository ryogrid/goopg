# Verify zero rmid-128 records emitted; document keep-the-classify-arms decision

**Status:** draft
**Date:** 2026-08-09
**Milestone:** M0130 (S6 — verification)

## Background

Deferral-ledger row 415 (2026-07-18) records: "B5 COMPLETE: rmid-128 retired
from the EMITTED stream … LANDED as DOCUMENTATION, not a literal deletion (a
deletion would be incorrect + unsafe)."

All 6 goopg-native catalog WAL record kinds (20/21/94/69/102/103) have been
converted to PG-native heap mutations. Zero records with rmgr ID 128 appear in
the emitted WAL stream. However, the `RmgrGoopgCatalog` constant is
deliberately **kept** — it classifies surviving non-catalog kinds:

| Kind | Name | Purpose |
|---|---|---|
| — | `RecordKindXactAssignment` | Transaction assignment tracking |
| — | `RecordKindXactSubAbort` | Sub-transaction abort tracking |

These are pinned by `record_kind_rmgr_mapping_test.go:53-66`. Deleting the
constant and dispatch arms would break the classify mapping for these
surviving kinds.

## Verification tasks (M0130 S6)

1. **pg_waldump gate:** run `pg_waldump` from the `./postgres/` oracle over a
   WAL stream produced by a full DDL workload (CREATE TABLE, ADD COLUMN,
   CREATE SCHEMA, CREATE INDEX, CREATE VIEW, ALTER TABLE SET DEFAULT, DROP
   TABLE). Confirm zero records with rmgr ID 128.
2. **Document the decision:** this design doc serves as the canonical record
   of why `RmgrGoopgCatalog` is kept. Cite ledger #415 and the test pin.
3. **Regression gate:** add a test that greps WAL output for rmid-128 and
   fails if any appear. This prevents accidental re-introduction.

## What is NOT in scope

- Deleting the `RmgrGoopgCatalog` constant — it is intentionally kept.
- Deleting the classify arms in `rmgr_map.go` — they serve surviving kinds.
- A `grep -rn "RmgrGoopgCatalog"` returning zero — the constant must exist.

## Gates

1. `pg_waldump` reports zero rmid-128 records over a full DDL workload.
2. Regression gate: automated rmid-128-in-WAL detection.
3. UNITS + SMOKE green.
4. Extended `TestE2E_FailoverGoopgToPG` passes.

## References

- `.ralph/deferral_ledger.md` row 415 — B5 COMPLETE / keep-classify-arms decision
- `internal/wal/xlog_record.go` — `RmgrGoopgCatalog` constant (kept)
- `internal/wal/rmgr_map.go` — classify mapping (kept for non-catalog kinds)
- `internal/wal/record_kind_rmgr_mapping_test.go:53-66` — test pin
- `internal/server/replication.go` — wal sender
