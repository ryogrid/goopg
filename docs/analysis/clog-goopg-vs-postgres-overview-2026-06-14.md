# CLOG (Commit Log) — goopg vs. PostgreSQL 18.3: Overview

*Analysis date: 2026-06-14 · Branch: `align-data-structure-with-pg`*

This is the orientation document for a three-part gap analysis of goopg's
transaction-commit-log (CLOG) implementation versus upstream PostgreSQL 18.3
(source checked out under `./postgres/`).

- **This file** — what CLOG is, why it matters, and an architecture-at-a-glance
  comparison.
- [`clog-goopg-vs-postgres-reference-map-2026-06-14.md`](clog-goopg-vs-postgres-reference-map-2026-06-14.md)
  — the detailed, cited, side-by-side reference map (PG → goopg → difference).
- [`clog-goopg-gaps-and-remediation-2026-06-14.md`](clog-goopg-gaps-and-remediation-2026-06-14.md)
  — prioritized list of what is missing and what to do about it.

---

## What CLOG is

The commit log records, for every transaction ID (XID), whether that transaction
**committed**, **aborted**, or is still **in progress**. It is the source of
truth that MVCC visibility checks consult to decide whether a heap tuple created
or deleted by some XID should be visible to a reader. In PostgreSQL it lives
on disk under `pg_xact/` (historically `pg_clog/`) as a dense array of 2-bit
status codes managed through the SLRU (Simple LRU) buffer-pool subsystem.

## Why it matters for goopg

CLOG sits on three load-bearing paths:

1. **MVCC visibility** — resolving xmin/xmax commit status during scans.
2. **Crash recovery** — after a restart, every XID that did not durably commit
   must be treated as aborted, and `NextXID` must be advanced past every
   durably-recorded XID.
3. **Heterogeneous PG-standby compatibility** — goopg's stated goal is that a
   *real* PostgreSQL 18.x instance can attach to a goopg primary as a physical
   standby (basebackup + streaming). PG's startup/redo path reads `pg_xact/`
   directly via `SimpleLruReadPage_ReadOnly`, so the on-disk segment files must
   be **byte-compatible** with PG's 2-bits-per-XID layout. See
   `docs/design/0107-0001-m0106-pg-compat-invariants.md` (CLOG on-disk
   invariant).

## goopg's two-layer storage model (the key structural difference)

PostgreSQL has exactly one representation: the SLRU-managed `pg_xact/` segment
files, cached in a shared-memory buffer pool. goopg instead keeps **two**
representations:

1. **Authoritative runtime layer** — an in-memory array of per-bank byte slices
   (`clogBank`, 128K XIDs per bank, one *byte* per XID) backed by a single flat
   file at `<DataDir>/global/pg_xact` (note: a *file*, not the `pg_xact/`
   *directory*). One byte per XID, written with `os.WriteFile` and **no fsync**;
   the WAL commit record is the primary durability mechanism, so this file is a
   write-behind cache. Defined in `internal/mvcc/clog.go`.

2. **PG-canonical mirror layer** — an optional `pg_xact/` *directory* of SLRU
   segment files in PG's exact 2-bit-per-XID format, written and **fsynced on
   every commit/abort** so that an attached PG standby can read it. Enabled via
   `CLog.EnablePGSLRUMirror` (`internal/mvcc/clog.go:478`); the mirror is treated
   as authoritative on restart because it is the only fsynced copy
   (M0106-0013).

This dual-representation design is unique to goopg and is the root of several of
the gaps catalogued in the remediation doc (notably the
encode↔decode/flat-file↔SLRU "sibling path" consistency risk).

## Architecture at a glance

| Dimension | PostgreSQL 18.3 | goopg |
|---|---|---|
| On-disk format (canonical) | 2 bits/XID, 4 states, 32768 XIDs/page, 32 pages/segment, `pg_xact/<segno>` | SLRU **mirror** reproduces this exactly; primary store is a flat 1-byte/XID file `global/pg_xact` |
| Status states | IN_PROGRESS, COMMITTED, ABORTED, **SUB_COMMITTED** | Unknown, Committed, Aborted (**no SUB_COMMITTED**) |
| In-memory caching | SLRU shared buffer pool: slots, banks of 16, LRU eviction, page replacement | Whole status array resident in per-bank byte slices (no eviction, no page pool) |
| Durability | WAL + SLRU page writes at checkpoint; async-commit LSN tracking | WAL commit record (primary) + per-commit fsync of SLRU mirror; flat file not fsynced |
| Setting status | `TransactionIdSetTreeStatus`, multi-page 3-phase, **group commit** | `setStatus`: per-bank lock + flush + mirror; no group commit, no tree atomicity |
| Reading status | `TransactionIdGetStatus(xid,*lsn)` + 1-entry cache; consulted in visibility | `GetStatus` O(1) byte read — **but visibility path does NOT consult CLOG at runtime** |
| WAL records | `CLOG_ZEROPAGE`, `CLOG_TRUNCATE`; `clog_redo` | None CLOG-specific; CLOG derived from `XactCommit`/`XactAbort` WAL records |
| Truncation / wraparound | `TruncateCLOG`, `CLOGPagePrecedes`, frozenXID-driven vacuum | **No truncation**; grows monotonically; `ErrXIDWraparound` guard only |
| Subtransactions | Persistent `pg_subtrans` SLRU (4 bytes/XID) | In-memory parent maps only (lost across restart) |
| Concurrency | SLRU bank locks + buffer locks + lock-free group-update queue | Per-bank `RWMutex` (128K XIDs/bank) + `banksMu` for slice growth |

## Related design docs

- `docs/design/0030-0007-pg-xact-commit-log.md` — original CLOG design (flat
  file, bootstrap XIDs, upgrade path).
- `docs/design/0106-0011-crash-mid-tx-clog-implicit-abort.md` and
  `0106-0011-rollback-catalog-rows-clog-filter.md` — implicit-abort sweep.
- `docs/design/0106-0013-clog-recovery-and-xid-horizon.md` — SLRU-authoritative
  restart, `HighestKnownXID`, WAL-replay stamping.
- `docs/design/0107-0004-procarray-xidgen-clog-bank-locks.md` — per-bank lock
  geometry.
- `docs/design/0107-0001-m0106-pg-compat-invariants.md` — CLOG on-disk
  byte-compatibility invariant.
- `docs/design/0050-0001/0002/0003` — subtransaction stack, XID/visibility, and
  WAL/recovery.
