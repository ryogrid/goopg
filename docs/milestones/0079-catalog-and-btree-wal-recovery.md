# Milestone 0079 — Catalog DDL WAL recovery + Btree WAL parity

**Status:** accepted (2026-05-11)
**Branch:** `try-codex` (commits `b48551f`, `0bb88f6`, `03803f0`, `2ba63a8`)
**Predecessor:** M0077 (Q5 planner fix)
**Drives:** PostgreSQL-aligned WAL parity for index-side records;
unblocks every workload that depends on indexes surviving a non-
graceful restart (pgbench's `pgbench_accounts_pkey` was the
surfacing case).

## Context

A pgbench measurement on the M0077-final binary regressed from
~60 TPS to 0.86 TPS after a non-graceful restart. Root cause:
goopg's `Runtime.SaveCatalog` was the ONLY persistence path for
index metadata, and it only ran in a graceful-shutdown `defer`.
SIGKILL / OOM / panic bypassed it, leaving `pgbench_accounts_pkey`
absent from the in-memory catalog after restart — every `WHERE
aid = :aid` fell back to a Seq Scan on a 10M-row heap.

Wider audit of goopg vs PostgreSQL's `nbtxlog.h` exposed five
additional btree page-mutation paths that emitted FPI (full-page
image) instead of logical records. M0079 closes the catalog gap +
those btree gaps.

## Sub-milestones

| # | Sub-milestone | Commit | Status |
| - | ------------- | ------ | ------ |
| 0001 | Catalog DDL WAL records (`CreateIndex` / `DropIndex`) | `b48551f` | accepted |
| 0002 | `RecordKindBtreeVacuum` (logical kept-items vs FPI) | `0bb88f6` | accepted |
| 0003 | `BtreeUnlinkPage` + `BtreeNewRoot` + `BtreeMarkPageHalfDead` records | `03803f0` | accepted |
| 0004 | `BtreeNewRoot` producer wiring (`createNewRoot` + `resetToEmptyRoot`) | `2ba63a8` | accepted |

## Design references

- `docs/design/0079-0001-index-ddl-wal-recovery.md`
- `docs/design/0079-0002-btree-record-wal-parity.md`
- `docs/design/0079-0003-btree-page-deletion-and-root-wal.md`

## Definition of Done

- ✅ `CREATE INDEX` survives a non-graceful restart (recovered
  from `RecordKindCreateIndex` via
  `internal/initdb/index_ddl_recovery.go`).
- ✅ `DROP INDEX` is replayed (no resurrection from earlier
  CREATE).
- ✅ Catalog DDL WAL records carry full Index metadata (OID,
  TableOID, Schema, Name, Method, Unique, Primary, Columns).
- ✅ Btree VACUUM emits one logical record per pruned leaf
  (~item bytes vs 8 KiB FPI).
- ✅ Btree page deletion emits ONE atomic `BtreeUnlinkPage`
  record covering all 4 page mutations (left sib Next, right
  sib Prev, leaf flags after, parent downlink removal).
- ✅ Btree new-root creation (post-split + reset-empty) emits
  one `BtreeNewRoot` record covering root content + metapage
  update.
- ✅ `go test ./...` PASS at every commit.

## Out of scope (deferred)

- Btree `MarkPageHalfDead` standalone producer (currently
  bundled into `BtreeVacuum`'s OpaqueFlags trailer; producer
  for the "already-empty" path is M0081 carry).

## References

- `internal/wal/recovery.go::RecordKindCreateIndex` (20),
  `DropIndex` (21), `BtreeVacuum` (22), `BtreeUnlinkPage` (23),
  `BtreeNewRoot` (24), `BtreeMarkPageHalfDead` (25).
- `internal/access/btree/replay.go` (exported replay helpers).
- `internal/initdb/index_ddl_recovery.go` (catalog-side replay
  driver).
