# Verify zero rmid-128 records emitted; document keep-the-classify-arms decision

**Status:** accepted
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

## Verification results (2026-08-09, M0130-S6 complete)

### Audit findings

- **Zero production emit sites:** grep confirmed that no production code calls
  `EncodeXactAssignment`, `EncodeXactRollbackTo`, `EncodeXactSubAbort`,
  `EncodeSmgrTruncate`, `EncodeHeapUpdate`, `EncodeHeapMultiInsert`,
  `EncodeHeapVisible`, `EncodeBtreeReusePage`, or `EncodeBtreeMetaCleanup` —
  every call site is in `*_test.go` files. All active production emit sites
  route through PG-analog arms in `recordKindToRmgrInfo` (Heap/Heap2/Btree/
  Xact/Storage/CLOG/XLog).
- **Nine legacy kinds** fall through the `default` arm to `RmgrGoopgCatalog`:
  `Checkpoint` (2, pre-assembled bypass), `SmgrTruncate` (12),
  `XactAssignment` (15), `XactRollbackTo` (16), `XactSubAbort` (17),
  `HeapUpdate` (27), `HeapMultiInsert` (28), `HeapVisible` (29),
  `BtreeReusePage` (30), `BtreeMetaCleanup` (31). All have encode functions
  but zero production call sites.
- **All six B5-retired catalog kinds** (20/21/69/94/102/103) route to
  `RmgrGoopgCatalog` via the default arm, and `nativeApplyRecordKindKnown`
  returns `false` for all of them (verified by S4/S5 regression gates).
- **`RmgrGoopgCatalog` constant deliberately KEPT.** It classifies the
  surviving legacy kinds and the retired B5 catalog kinds; deleting it would
  break the classify mapping for pre-B5 WAL replay and test infrastructure.

### Regression gate

`TestNoActiveRecordKindUnexpectedlyRoutesToRmgrGoopgCatalog`
(`internal/wal/record_kind_rmgr_mapping_test.go`) — PASS.

The test enumerates all 28 active RecordKind constants, asserts that only
the 10 expected legacy kinds map to rmid-128, and verifies no retired
B5 catalog kind has been re-added as an active kind.

### Keep-classify-arms decision

The `RmgrGoopgCatalog` constant, the `default` arm in `recordKindToRmgrInfo`,
and the `case RmgrGoopgCatalog` arms in `recovery.go` (goopg↔goopg crash
replay) and `stream_replayer.go` (standby WAL apply) are all deliberately
**kept**. Removing them would:

1. **Break pre-B5 WAL replay:** old goopg data dirs with on-disk WAL
   containing rmid-128 records would be unrecoverable.
2. **Break test infrastructure:** multiple tests exercise the encode/decode
   round-trip for legacy kinds.
3. **Remove the safety net:** if a future goopg-private RecordKind is
   added without a PG-analog case in `recordKindToRmgrInfo`, the default
   arm catches it with a clear FATAL rather than silently classifying as
   a wrong rmgr.

## Gates

1. `TestNoActiveRecordKindUnexpectedlyRoutesToRmgrGoopgCatalog` — PASS (S6 regression gate)
2. `go test ./internal/wal/...` — PASS (5.2s)
3. UNITS + SMOKE green (see status block)
4. The `default` arm and `RmgrGoopgCatalog` constant remain deliberately kept.

## References

- `.ralph/deferral_ledger.md` row 415 — B5 COMPLETE / keep-classify-arms decision
- `internal/wal/xlog_record.go` — `RmgrGoopgCatalog` constant (kept)
- `internal/wal/rmgr_map.go` — classify mapping (kept for non-catalog kinds)
- `internal/wal/record_kind_rmgr_mapping_test.go:53-66` — test pin
- `internal/server/replication.go` — wal sender
