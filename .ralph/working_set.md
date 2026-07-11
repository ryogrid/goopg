(idle — nothing in flight)

## Loop summary (2026-07-12, loop #72)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts / TuplelockUpgradeNoDeadlock / PgWaldumpVacuumPruneRoundtrip)
already `[x]` in M-NIGHTLY (co-load timing flakes). No new nightly work.

**Task — per-slot catalog-xmin retention hook in vacuum/pruning
(unimplemented_feat, logical-decoding pipeline). COMPLETE + committed.**
Closed the named CONSUMER gap: `OldestXmin` never consulted slot `catalog_xmin`.

Landed:
- `internal/wal/slots.go`: `MinCatalogXmin()` (min non-zero across
  non-invalidated slots; physical/fresh-logical=0 skipped) + monotonic durable
  producer `AdvanceCatalogXmin(name,xid)` (mirrors LogicalIncreaseXminForSlot).
- `internal/mvcc/manager.go`: lock-free installed hook
  `catalogXminSource atomic.Pointer[func() uint64]` + `SetCatalogXminSource`;
  `OldestXmin()` floors the global horizon to the pinned catalog_xmin (never
  advances forward). This horizon feeds both heap-prune paths, index-only prune,
  and CLOG/SLRU truncation.
- `internal/initdb/open.go`: wires `SetCatalogXminSource(slotsReg.MinCatalogXmin)`
  before the CLOG-truncation horizon read.
- Tests: `TestSlotsMinCatalogXmin`, `TestSlotsAdvanceCatalogXminMonotonicAndDurable`,
  `TestOldestXminFoldsCatalogXminSource`.
- Design: `docs/design/0008-0001-logical-decoding-pipeline.md` "Catalog xmin
  retention — hook landed" + README row. unimplemented_feat.json → resolved.

Deferred (ledger 2026-07-12): decoder→`AdvanceCatalogXmin` wiring (reserve at
slot create, advance on confirm); upstream data-vs-catalog horizon split (v0
floors ONE global horizon → over-retains user tables, safe); logical
`CREATE_REPLICATION_SLOT` still uses generic `Slots.Create`.

Gates: build/vet clean; wal/mvcc/initdb suites PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard consistent.

In-flight: none
