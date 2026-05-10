# Milestone 0081 — WAL record producer wiring (heap + btree)

**Status:** planned
**Depends on:** M0079, M0080 (record infrastructure landed)
**Drives:** Make the M0079 / M0080 record kinds whose producer
sites are still on FPI emit logical records, reducing WAL volume
and aligning the on-wire WAL stream with PostgreSQL for tooling
(pg_waldump, logical decoding).

## Context

M0079 / M0080 added record infrastructure (encode / decode /
replay) for six WAL kinds whose producer sites were not yet
wired. Each producer wiring is a focused refactor of one
executor / storage path, but each carries its own design
consideration (lock ordering, batch boundaries, accuracy
trade-offs) that warrants a per-record design note.

## Sub-milestone scope (sketch)

| # | Record | Producer site |
| - | ------ | ------------- |
| 0001 | `RecordKindHeapUpdate` (atomic non-HOT UPDATE) | `internal/executor/operators_storage.go::updateOp.Next` non-HOT branch |
| 0002 | `RecordKindHeapMultiInsert` | COPY / bulk INSERT (`internal/executor/operators_copy.go`) |
| 0003 | `RecordKindHeapVisible` (VM bit set/clear emission) | VACUUM SetAllVisible + heap INSERT/DELETE/UPDATE clear paths |
| 0004 | `RecordKindBtreeReusePage` (page recycle notification) | `internal/access/btree/btree_vacuum.go::recycleBlock` |
| 0005 | `RecordKindBtreeMetaCleanup` (metapage cleanup-XID) | btree vacuum cleanup; depends on goopg gaining cleanup-XID concept |
| 0006 | `RecordKindBtreeMarkPageHalfDead` (standalone half-dead) | `VacuumIndexPages` "already empty before vacuum" path |

## Required design docs

To be picked up when this milestone is started:

- `docs/design/0081-0001-heap-update-atomic-producer.md`
  (lock-ordering rule for the two-page UPDATE; deadlock
  prevention vs concurrent UPDATEs to swapped page pairs).
- `docs/design/0081-0002-heap-multi-insert-producer.md`
  (batch boundary policy; page-overflow handling mid-record).
- `docs/design/0081-0003-heap-visible-producer.md`
  (VM clear-bit emission policy: per-mutation vs deferred).
- `docs/design/0081-0004-btree-reuse-page-producer.md`
  (recycle-XID tracking; M0083 multi-xact dependency).
- `docs/design/0081-0005-btree-meta-cleanup-and-halfdead.md`
  (cleanup-XID concept introduction).

## Tasks

Tasks will be detailed per sub-milestone when each is picked up.
See the fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- Each sub-milestone replaces its producer's `MarkDirty(slot)`
  FPI path with the corresponding logical-record emission.
- Existing regression tests in `internal/access/btree`,
  `internal/vacuum`, `internal/executor` continue to pass.
- Spot-check WAL volume reduction (synthetic workload that
  exercises the producer) shows the expected shrink.
- Per-record replay tests (or existing ones with FPI fallback)
  cover the logical path.
