# Milestone 0117 — CLOG ↔ PostgreSQL subsystem alignment

**Status:** planned
**Depends on:** M0046 (heap & MVCC maturation), M0080 (heap WAL parity + VM/FSM persistence), M0093 (read-only commit skip), M0106 (PG relcache init compat), M0107 (procarray/xidgen/clog bank locks)
**Drives:** PG-faithful commit-log (`pg_xact`) and subtransaction (`pg_subtrans`) semantics — runtime visibility, subtransaction durability, buffer/flush model, async-commit safety, and XID-wraparound correctness — so a vanilla PostgreSQL 18.3 standby can attach and so goopg's transaction-status behavior matches upstream.

## Context

A bounded CLOG build recently landed on `align-data-structure-with-pg`:

- **G1** — `CLog.TruncateCLOG` / `CLOGPagePrecedes` / `OldestClogXid` / `AdvanceOldestClogXid`, durable-ordered truncation via the checkpointer hook (`internal/mvcc/clog.go`, `internal/wal/checkpointer.go`).
- **G9** — `RecordKindClogTruncate` WAL record (encode/decode/replay) so truncation is crash- and standby-safe (`internal/wal/recovery.go`, `internal/initdb/xact_recovery.go`).
- **G2 / G3** — dual-store consistency test and standby-attach round-trip E2E test.
- **G5 (partial)** — persistent `pg_subtrans` **write path** (`internal/mvcc/subxact_slru.go`).

The full gap analysis and the deferral record live in
`docs/analysis/clog-goopg-vs-postgres-overview-2026-06-14.md`,
`docs/analysis/clog-goopg-vs-postgres-reference-map-2026-06-14.md`,
`docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md`, and
`docs/analysis/clog-impl-task-division-2026-06-14.md` (Review-outcomes / deferral
section). Existing CLOG design docs:
`docs/design/0030-0007-pg-xact-commit-log.md`,
`docs/design/0106-0011-crash-mid-tx-clog-implicit-abort.md`,
`docs/design/0106-0013-clog-recovery-and-xid-horizon.md`,
`docs/design/0107-0004-procarray-xidgen-clog-bank-locks.md`, and the subxact series
`docs/design/0050-0001/0002/0003-*.md`.

What still diverges from PostgreSQL (verified against the merged code):

1. **Runtime visibility never consults the CLOG** — `Snapshot.SeesCommittedXID`
   decides solely from in-memory `Xmin`/`Xmax` + `InProgress`/`Aborted` arrays;
   `GetStatus` is read only at load/recovery, not during scans.
2. **`pg_subtrans` is write-only** — parent links are persisted but never read back
   at startup, so subxact parentage does not survive a restart.
3. **No `SUB_COMMITTED` (0x03) lane** — the CLOG mirror encodes only
   Unknown/Committed/Aborted; multi-page subxact-tree atomicity is not modeled.
4. **Whole-file flush, no group commit** — every commit rewrites the entire flat
   file and fsyncs one SLRU segment; there is no batching.
5. **No SLRU buffer pool** — the entire status array is resident in per-bank byte
   slices (≈1 byte/XID), with no page cache / eviction.
6. **No async-commit LSN tracking** (`group_lsn`) — only synchronous-commit
   semantics; hint-bit safety under `synchronous_commit=off` is not modeled.
7. **Horizon comparisons are not wraparound-safe** — `catalog.DatFrozenXID` and the
   checkpointer `TruncateCLOGFn` use plain `<` rather than a `TransactionIdPrecedes`
   equivalent.
8. **`datfrozenxid` is not persisted** into the `pg_database` catalog tuple — it is
   recomputed on demand from `min(relfrozenxid)`.

## PostgreSQL behaviour (reference)

Oracle: `postgres/src/backend/access/transam/clog.c` (status get/set, group commit,
`TruncateCLOG`, `CLOGPagePrecedes`, `group_lsn` / `CLOG_XACTS_PER_LSN_GROUP`),
`postgres/src/backend/access/transam/subtrans.c` (`SubTransSetParent` /
`SubTransGetParent` / `SubTransGetTopmostTransaction`),
`postgres/src/backend/access/transam/slru.c` (SLRU buffer pool, bank locks),
`postgres/src/include/access/clog.h` (`TRANSACTION_STATUS_SUB_COMMITTED = 0x03`),
and `postgres/src/backend/access/transam/transam.c` (`TransactionLogFetch`,
runtime status-as-visibility-oracle). Each task's design doc must cite the specific
upstream file/function it mirrors.

## Goal

Bring CLOG and `pg_subtrans` to PostgreSQL 18.3 parity across five axes:
visibility integration, subtransaction durability (`pg_subtrans` restore +
`SUB_COMMITTED`), buffer/flush model (group commit + bounded buffer pool),
async-commit LSN safety, and XID-wraparound-safe horizon selection — preserving the
M0105/M0106 on-disk PG-compat invariants throughout.

## Required design docs (author + index BEFORE the matching implementation)

Each task below MUST land its design doc under `docs/design/` and add a row to
`docs/design/README.md` **before** any implementation. Reserved filenames:

- `docs/design/0117-0001-xid-precedes-horizon-comparison.md`
- `docs/design/0117-0002-visibility-clog-fallback.md`
- `docs/design/0117-0003-pg-subtrans-restore-on-restart.md`
- `docs/design/0117-0004-clog-sub-committed-lane.md`
- `docs/design/0117-0005-clog-incremental-flush-group-commit.md`
- `docs/design/0117-0006-clog-slru-buffer-pool.md`
- `docs/design/0117-0007-clog-async-commit-lsn.md`
- `docs/design/0117-0008-datfrozenxid-persistence.md`

## Tasks

Ordered P0 → P2. **Every task: write and index its `docs/design/0117-NNNN-*.md`
design doc first, then implement, then run the stated gate.**

- [ ] **M0117-0001** — Wraparound-safe XID horizon comparison (gap M2, P0/correctness).
      Add an exported `storage.XIDPrecedes(a, b)` (mirroring `clog.go`'s `txnPrecedes`
      and PG's `TransactionIdPrecedes`) and use it for horizon selection in
      `catalog.DatFrozenXID` and the checkpointer `TruncateCLOGFn`
      (`internal/initdb/open.go`) instead of plain `<`. Design doc
      `0117-0001-xid-precedes-horizon-comparison.md` first.
      Gate: `go test ./internal/mvcc/... ./internal/catalog/...`. Effort: S.

- [ ] **M0117-0002** — Runtime CLOG-consulting visibility fallback (gap G4, P1).
      Add a CLOG fallback in `Snapshot.SeesCommittedXID` for in-window XIDs not
      classified by the snapshot arrays, keeping the arrays as the fast path; audit
      the `visibility.go` ↔ `subxact_visibility.go` sibling paths. Design doc
      `0117-0002-visibility-clog-fallback.md` first.
      Gate: **TPC-H spot-check (`scripts/tpch-spotcheck.sh`, canonical Q12=2/Q13=35)**
      + `go test -race ./internal/mvcc/...`. Effort: M.

- [ ] **M0117-0003** — `pg_subtrans` restore-on-restart (gap G5 read path, P1).
      Wire `SubxactMap.EnablePersistence` into the `internal/initdb/open.go` recovery
      sequence and load persisted parent links from the `pg_subtrans` SLRU back into
      the in-memory `SubxactMap` so subxact parentage survives a restart. Design doc
      `0117-0003-pg-subtrans-restore-on-restart.md` first.
      Gate: standby-attach E2E + `go test -race ./internal/mvcc/...`. Effort: M.

- [ ] **M0117-0004** — `SUB_COMMITTED` (0x03) CLOG lane (gap G5 SUB_COMMITTED, P1;
      builds on M0117-0003). Generate the 0x03 lane in the commit path
      (`mirrorToSLRUUnlocked`) for committed subxacts whose parent is still
      in-progress, and read it back in `loadFromSLRU`; document which code path writes
      each state. Design doc `0117-0004-clog-sub-committed-lane.md` first.
      Gate: extend the dual-store consistency test + `go test -race ./internal/mvcc/...`.
      Effort: S–M.

- [ ] **M0117-0005** — Incremental flush + group commit (gap G7, P2).
      Make `CLog.flush` write only changed pages/segments instead of the whole flat
      file, and add a group-commit batching layer (lock-free queue, mirroring PG's
      `TransactionGroupUpdateXidStatus`) over the SLRU fsync. New file
      `internal/mvcc/clog_groupcommit.go`. Design doc
      `0117-0005-clog-incremental-flush-group-commit.md` first.
      Gate: `go test -race ./internal/mvcc/...` + a commit-throughput sanity check.
      Effort: M.

- [ ] **M0117-0006** — SLRU buffer pool / 2-bit collapse (gap G6, P2; follows
      M0117-0005). Replace the fully-resident per-bank byte slices with a bounded
      page-cache over the 2-bit SLRU representation (LRU eviction; `transaction_buffers`
      GUC). Design doc `0117-0006-clog-slru-buffer-pool.md` first.
      Gate: `go test -race ./internal/mvcc/...`; full `mvcc`/`wal`/`initdb` suites;
      re-init the data dir (memory-model change). Effort: L.

- [ ] **M0117-0007** — Async-commit LSN tracking (gap G8, P2; feature-gated on a real
      `synchronous_commit=off` path). Add per-group commit-LSN tracking
      (`CLOG_XACTS_PER_LSN_GROUP`) and gate honoring a committed status / hint-bit
      setting on WAL flush position. Design doc `0117-0007-clog-async-commit-lsn.md`
      first. Gate: `go test -race ./internal/mvcc/...` + recovery E2E. Effort: L.

- [ ] **M0117-0008** — Persist `datfrozenxid` in `pg_database` + extend dual-store
      tests (P2). Persist the computed cluster horizon into the `pg_database` catalog
      tuple at VACUUM end (rather than only on-demand) and extend the dual-store
      consistency tests for round-trip coverage of all status codes. Design doc
      `0117-0008-datfrozenxid-persistence.md` first.
      Gate: `go test ./internal/catalog/...`; re-init the data dir + run the regress
      port (catalog tuple-format change). Effort: S.

## Risk

- **R1 — Silent visibility regression (M0117-0002).** Making the visibility path
  consult the CLOG is the highest-blast-radius change; a wrong fallback corrupts read
  results. Mitigation: keep snapshot arrays as the authoritative fast path, gate on the
  TPC-H spot-check (Q12=2/Q13=35), and audit the visibility sibling paths in the same loop.
- **R2 — Subtransaction durability correctness (M0117-0003/0004).** Loading stale or
  mis-parented subxact links would mis-resolve top-level XIDs. Mitigation: standby-attach
  E2E + race tests; mirror `subtrans.c` exactly.
- **R3 — Buffer-pool / flush rewrite (M0117-0005/0006).** Concurrency or page-replacement
  bugs only surface under load or after restart. Mitigation: race detector + recovery E2E;
  re-init data dirs after the memory-model change.
- **R4 — Wraparound edge cases (M0117-0001).** Low likelihood (goopg has a hard
  allocation guard) but a correctness gap; mitigate with targeted unit tests near 2^32.

## Definition of Done

- Each `M0117-NNNN` task: its `docs/design/0117-NNNN-*.md` is authored, indexed in
  `docs/design/README.md`, and marked `accepted`; the implementation has landed; and
  the task's stated gate passes.
- Runtime visibility consults the CLOG for in-window XIDs (M0117-0002) with no TPC-H
  row-count regression.
- Subxact parentage survives a restart and `SUB_COMMITTED` is generated/consumed
  (M0117-0003/0004).
- CLOG writes are incremental + group-batched and memory is bounded (M0117-0005/0006).
- All horizon comparisons are wraparound-safe (M0117-0001).
- `go test ./...` and the race/recovery gates pass; M0105/M0106 on-disk PG-compat
  invariants still hold (a PG 18.3 standby can still attach).

## Out of scope

- MultiXact (`pg_multixact`) and commit-timestamp (`pg_commit_ts`) SLRUs — separate
  subsystems; file under their own milestones if needed.
- Async-commit *feature* enablement beyond the CLOG LSN-tracking plumbing (M0117-0007
  is gated on a real `synchronous_commit=off` path being introduced elsewhere).
