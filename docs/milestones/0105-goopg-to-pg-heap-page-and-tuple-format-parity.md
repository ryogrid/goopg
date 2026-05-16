# Milestone 0105 — goopg→PG Data-File Format Parity

**Status:** planned
**Filed:** 2026-05-16
**Depends on:** M0102 (heterogeneous replication E2E), M0014 (PG-compatible WAL on-disk format), M0101 (pg_waldump compatibility)
**Reference plan:** `.ralph/fix_plan.md` (M0105 section)

## Operational policy (2026-05-16)

- **Within this milestone, marking any sub-task as DEFERRED is not permitted.**
  Without data-file format parity, Scenario B of M0102 (goopg primary → PG standby)
  cannot pass — the PG standby crashes on startup because it cannot read goopg's
  page, tuple, or catalog structures.
- Blocker resolution is itself in scope when the blocker is internal to goopg.

## Goal

Make goopg's on-disk data file layout — heap page header, line-pointer encoding,
heap tuple header, and catalog bootstrap pages — byte-compatible with PostgreSQL 18.
Once aligned, a PG standby bootstrapped from a goopg `pg_basebackup` clone can start
successfully, replay streamed WAL, and serve read queries on replicated data.

This is the next-order blocker for **Scenario B** (goopg primary + PG standby)
from M0102. Scenario A (PG primary + goopg standby) is already passing.

## Background

M0102-0007 (2026-05-15/16) landed the wire-level and checkpoint-encoding
interoperability pieces:

- `BASE_BACKUP` wire command on goopg primary
- pg_control checkpoint REDO update during BASE_BACKUP
- 1-based→0-based LSN conversion
- PG18-compatible CheckPoint struct (88 bytes) in WAL
- PG-compatible WAL segment filenames in backup tar
- `IDENTIFY_SYSTEM` returning the real system identifier
- PG-required initdb directories

PG standby now reaches `entering standby mode` but then crashes:

```
FATAL: could not access status of transaction 0
DETAIL: Could not open file "pg_subtrans/0303": No such file or directory.
```

After creating `pg_subtrans/`, PG encounters a segmentation fault while
trying to read goopg's catalog or heap pages — the page layout, tuple
header encoding, or both diverge from PG18 expectations at a byte level
that causes the PG process to read garbage and crash.

## In Scope

1. **Page header (`PageHeaderData`)**: verify `pd_lsn`, `pd_checksum`, `pd_flags`,
   `pd_lower`, `pd_upper`, `pd_special`, `pd_pagesize_version`,
   `pd_prune_xid` offsets and encoding match PG18 `bufpage.h`.

2. **Line pointer (`ItemIdData`)**: verify the 32-bit bit-field encoding
   (`lp_off:15, lp_flags:2, lp_len:15`) matches PG18 `itemid.h`. Ensure
   `LP_UNUSED`/`LP_NORMAL`/`LP_REDIRECT`/`LP_DEAD` values are identical.

3. **Heap tuple header (`HeapTupleHeaderData`)**: verify byte-level layout
   of `t_xmin`, `t_xmax`, `t_field3` (xvac), `t_ctid`, `t_infomask2`,
   `t_infomask`, `t_hoff` matches PG18 `htup_details.h`. Ensure null
   bitmap encoding is PG-compatible.

4. **Catalog bootstrap pages**: ensure the pages written during `goopg init`
   (`bootstrapSystemCatalogs`, `bootstrapCLog`) are PG-compatible at the
   page level — a PG standby must be able to read the system catalog
   pages enough to complete startup.

5. **Re-verify M0102-0007 Scenario B**: after format alignment, run
   `TestE2E_FailoverGoopgToPG` and confirm it passes for the async subtest;
   then add and pass the sync variant.

## Out of Scope

- Full catalog schema parity (PG expects extended catalog tables with many
  columns goopg doesn't write; making minimal pages readable is sufficient
  for startup)
- Index page format parity (B-tree pages)
- TOAST format parity
- VACUUM FULL / freeze compatibility
- Cross-major PG version compatibility (target is PG18)

## Definition of Done

1. PG standby starts successfully from a goopg `pg_basebackup -X stream` clone.
2. WAL streaming from goopg primary to PG standby reaches `streaming` state.
3. `INSERT` on goopg primary is replicated and visible on PG standby.
4. `TestE2E_FailoverGoopgToPG/async` passes.
5. `TestE2E_FailoverGoopgToPG/sync_remote_apply` passes (zero-loss invariant).
6. **Regression bar:** `TestE2E_FailoverPGtoGoopg` (Scenario A, both subtests)
   continues to pass. All existing unit tests pass.
7. Style + state gates: `gofmt -l .` empty; `go vet ./...` clean;
   `make ralph-state-guard` passes.

## Required Design Docs

Under `docs/design/`:

- `0105-0001-heap-page-and-tuple-format-parity.md` — Byte-level audit of
  goopg's page/tuple format against PG18 and the changes required to align.

Each design doc must cite the upstream PostgreSQL implementation under
`postgres/src/` with concrete file:line references.
