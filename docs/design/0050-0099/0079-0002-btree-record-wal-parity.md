# 0079-0002 — Btree record-level WAL parity with PostgreSQL

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-11 |
| scope | wal, access/btree |
| related | 0079-0001 (catalog DDL WAL recovery), 0002-0002 (btree
concurrency), 0002-0003 (logical redo records) |

## 1. Problem statement

M0079-0001 closed the **catalog-side** index recovery gap — `CREATE
INDEX` and `DROP INDEX` now emit dedicated WAL records so the
in-memory catalog can be reconstructed after a non-graceful
restart.

This slice closes the orthogonal **record-side** gap: every page
mutation a btree performs during INSERT, SPLIT, and VACUUM should be
WAL-logged using a logical record where PostgreSQL has one. Today
goopg uses logical records for the two hottest paths (INSERT, SPLIT)
but falls back to FPI (full-page image, via `markDirtyWithPageRecord`
→ `LogPageImage`) for everything else. FPI is correct — replay just
overwrites the page byte-for-byte from the WAL — but it is verbose,
worsens checkpoint pressure, and diverges from the upstream layout
that operational tools (`pg_waldump`, the logical decoder, replicas)
expect.

The user's request is to align goopg's btree WAL surface with
PostgreSQL so all paths are logical-record-recoverable rather than
relying on FPI as a fallback.

## 2. Audit: what's already aligned, what falls back to FPI

| Btree path | goopg today | PostgreSQL | Status |
| ---------- | ----------- | ---------- | ------ |
| Leaf insert (non-split) | `RecordKindBtreeInsert` logical, idempotent via `pd_lsn` (`btree.go:1083-1090`, `1120-1127`) | `XLOG_BTREE_INSERT_LEAF` | ✅ aligned |
| Internal insert (downlink) | Same `RecordKindBtreeInsert` (`btree.go:1636-1644`) | `XLOG_BTREE_INSERT_UPPER` | ⚠️ fused: same record type for both leaf and internal; replay reads the page and applies the item — semantically correct, structurally fused |
| Page split (left + right + high key) | `RecordKindBtreeSplit` atomic (left post-image + right full image) (`btree.go:649-654`) | `XLOG_BTREE_SPLIT_L` / `XLOG_BTREE_SPLIT_R` | ✅ aligned (one record covers both PG variants) |
| New root after split | FPI via `markDirtyWithPageRecord` (`btree.go:697-716`) | `XLOG_BTREE_NEWROOT` | ❌ FPI fallback |
| Metapage update (root pointer + level) | FPI via `markDirtyWithPageRecord` (`btree.go:838`) | bundled into the parent record (`XLOG_BTREE_NEWROOT` carries metapage update) | ❌ FPI fallback |
| VACUUM kept-items rewrite | FPI per page (`btree_vacuum.go:113`) | `XLOG_BTREE_VACUUM` (slot list of items to remove) | ❌ FPI fallback — biggest win |
| VACUUM mark-half-dead | included in the FPI above (`btree_vacuum.go:104-111`) | `XLOG_BTREE_MARK_PAGE_HALFDEAD` | ❌ FPI fallback |
| VACUUM unlink leaf — prev sibling Next | FPI (`btree_vacuum.go:192`) | bundled into `XLOG_BTREE_UNLINK_PAGE` | ❌ FPI fallback |
| VACUUM unlink leaf — next sibling Prev | FPI (`btree_vacuum.go:208`) | bundled into `XLOG_BTREE_UNLINK_PAGE` | ❌ FPI fallback |
| VACUUM unlink leaf — leaf metadata | FPI (`btree_vacuum.go:265`) | `XLOG_BTREE_UNLINK_PAGE` | ❌ FPI fallback |
| VACUUM unlink leaf — parent downlink removal | FPI (`btree_vacuum.go:382`) | bundled into `XLOG_BTREE_UNLINK_PAGE` | ❌ FPI fallback |
| Reset to empty root after full VACUUM | FPI (`btree_vacuum.go:457`) | `XLOG_BTREE_NEWROOT` | ❌ FPI fallback |
| Page recycle (extend OR reuse FSM-recycled block) | implicit `RecordKindSmgrCreate` for extend; nothing for reuse | `XLOG_BTREE_REUSE_PAGE` | ❌ no record on reuse |
| Posting list dedup | plain `MarkDirty` (`btree.go:1188`) | upstream merges into the leaf-insert record | ⚠️ FPI on first dirty in epoch |

The only "must-fix-for-correctness" gaps are page recycle (recovery
must know an old page is now free) — which goopg sidesteps because
PinNew always extends; `RecordKindSmgrCreate` already covers
extension. Everything else marked ❌ is a WAL-volume optimisation
relative to PostgreSQL parity.

## 3. Scope of M0079-0002

This slice converts the **VACUUM kept-items rewrite** (`btree_vacuum.go:113`)
from FPI to a logical record. That's the most impactful one:

- It is the per-page btree mutation that runs the most often during
  AUTOVACUUM-heavy workloads (every dirty leaf gets re-imaged).
- Its shape is identical to the existing `RecordKindHeapVacuum`
  (kind | rel(9) | blk(4) | count(2) | slots[count](2 each)).
  Reusing the encoding pattern keeps the WAL family consistent.
- Replay is straightforward: read existing page, project to the
  kept-items set, write back. Idempotent via `pd_lsn`.

The other ❌ rows in §2 are deferred to M0079-0003+:

- **NewRoot** (split-bubbled and full-vacuum-empty paths) — small but
  semantically tricky because it touches the metapage atomically.
- **UnlinkPage** — bundles 4 page mutations (prev, next, leaf,
  parent). Highest WAL-volume win for VACUUM but also the most
  complex to make atomic on replay.
- **MarkPageHalfDead** — small standalone record; can land
  independently.
- **ReusePage** — depends on FSM integration (M0079-0004 candidate).
- **PostingListDedup** — Internal optimisation; unrelated to
  PostgreSQL parity.

Splitting into discrete slices keeps each commit revertible and
testable in isolation, mirroring the M0077 4-slice discipline.

## 4. Record format: `RecordKindBtreeVacuum`

Mirrors `RecordKindHeapVacuum` byte-for-byte at the header level so
shared helpers (`writeRelLocator`, `readRelLocator`, slot-list
serialisers) can be reused.

```
kind(1) = 22
rel(9)        // DBOid(4) + RelOid(4) + Fork(1)
blk(4)        // BlockNumber of the leaf page being vacuumed
count(2)      // number of slot entries removed
slots[count]  // 2 bytes each, 1-based slot numbers in ascending order
opaqueFlags(2) // BTPageOpaque.Flags AFTER vacuum (covers BTDeleted | BTHalfDead transitions)
```

Total size: `16 + 2*count + 2` bytes. For a leaf with 100 dead
items, that's 218 bytes vs. 8 KiB FPI — **~37x WAL-volume reduction
on the hot vacuum path**.

The trailing `opaqueFlags(2)` field is goopg-specific. It captures
the `BTDeleted | BTHalfDead` flag transition that
`btree_vacuum.go:104-105` performs in the same critical section
when the page becomes empty. Replay applies the slot removal AND
the flag update in one idempotent step.

## 5. Replay rules

```go
func replayBtreeVacuum(mgr *storage.Manager, r Record) error {
    rel, blk, slots, flagsAfter, err := DecodeBtreeVacuum(r.Payload)
    if err != nil { return err }

    page := readPage(rel, blk)
    if storage.PageLSN(page) >= r.EndLSN {
        // Already replayed; pd_lsn idempotency.
        return nil
    }

    // Project items to keep (drop slots in `slots`).
    items := pageItemsRaw(page)
    kept := items[:0]
    skip := slotSet(slots)
    for i, it := range items {
        if !skip[uint16(i+1)] {
            kept = append(kept, it)
        }
    }
    resetPageItems(page)
    for _, it := range kept {
        storage.PageAddItemRaw(page, it)
    }

    // Apply opaque flags (e.g., BTDeleted | BTHalfDead transition).
    op := readOpaque(page)
    op.Flags = flagsAfter
    writeOpaque(page, op)

    storage.SetPageLSN(page, r.EndLSN)
    writePage(rel, blk, page)
    return nil
}
```

The replay is byte-for-byte equivalent to the original
`btree_vacuum.go::VacuumIndexPages` per-page step (lines 86-117).
Both rebuild the page from the kept-items projection rather than
in-place removal, so the post-state is layout-stable.

## 6. Idempotency

- **`pd_lsn` check** at the top: replay is idempotent.
- **Slot list**: the on-disk record carries the SLOT NUMBERS removed
  (1-based). Replay re-reads the page (which may have been
  partially-replayed in the same crash and contain the post-vacuum
  layout), checks the LSN, and either skips or rewrites.
- **Flag transition**: idempotent — overwrite is safe regardless of
  the prior state.

## 7. Hook wiring

Mirrors the existing `LogHeapVacuum` shape:

```go
type LogBtreeVacuumFunc func(rel RelFileNode, blk BlockNumber, removedSlots []uint16, flagsAfter uint16) (LSN, error)

// in PoolConfig
LogBtreeVacuum LogBtreeVacuumFunc

// accessor
func (p *Pool) LogBtreeVacuum() LogBtreeVacuumFunc
```

The hook is wired in `internal/initdb/open.go::Open` to
`walWriter.Append(wal.EncodeBtreeVacuum(...))`. `internal/access/btree/btree_vacuum.go::VacuumIndexPages`
falls back to FPI via `markDirtyWithPageRecord` only when the hook
is nil.

## 8. Tests

- `internal/wal/recovery_test.go::TestEncodeBtreeVacuumRoundTrip` —
  encode/decode coverage with edge cases (empty slot list, max-page
  slot list, opaque flag bits set).
- `internal/wal/recovery_test.go::TestApplyRecordReplaysBtreeVacuum`
  — synthesise a leaf page with 10 items, emit the record removing
  slots {2, 5, 7}, apply via `ApplyRecord`, assert the page contains
  the 7 retained items in order with the recorded opaque flags.
- `internal/wal/recovery_test.go::TestApplyRecordBtreeVacuumIdempotent`
  — apply the same record twice; second call is a no-op via `pd_lsn`.
- `internal/access/btree/btree_vacuum_test.go::TestVacuumIndexPagesEmitsLogicalRecord`
  — wire a stub `LogBtreeVacuum` capturing the payload, run vacuum,
  assert the captured payload matches the page's removed slots.
- End-to-end: existing `internal/access/btree/btree_vacuum_test.go`
  and `internal/wal/recovery_test.go` regressions must continue to
  pass (FPI fallback path remains for clusters where the hook is
  unwired).

## 9. Acceptance

- All tests in §8 green.
- `internal/wal`, `internal/access/btree`, `internal/initdb`,
  `internal/storage` packages all green.
- Spot-check WAL volume on a synthetic VACUUM workload (e.g., 1000
  rows inserted then 500 deleted then VACUUM): WAL bytes from the
  vacuum step shrink from ~8 KiB × pages-touched to ~16-200 bytes
  × pages-touched.

## 10. Out of scope (carried to M0079-0003+)

- `RecordKindBtreeUnlinkPage` — the 4-page atomic record covering
  prev sibling Next, next sibling Prev, leaf metadata, parent
  downlink removal. The complexity here is making the 4 mutations
  one logical record so replay can reapply them atomically.
- `RecordKindBtreeNewRoot` — split-bubbled root creation and
  full-vacuum-empty reset.
- `RecordKindBtreeMarkPageHalfDead` — standalone half-dead flag
  transition (currently bundled into the FPI rewrite).
- `RecordKindBtreeReusePage` — page recycle from FSM. Depends on
  FSM integration that is not yet in goopg.
