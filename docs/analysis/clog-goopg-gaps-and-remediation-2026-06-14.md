# CLOG Gaps & Remediation — goopg vs. PostgreSQL 18.3

*Analysis date: 2026-06-14 · Branch: `align-data-structure-with-pg`*

Prioritized catalogue of where goopg's CLOG diverges from PostgreSQL 18.3, with
impact and suggested remediation. Read alongside the
[overview](clog-goopg-vs-postgres-overview-2026-06-14.md) and the
[reference map](clog-goopg-vs-postgres-reference-map-2026-06-14.md) (which carry
the supporting citations).

Effort estimates: **S** ≈ days, **M** ≈ 1–2 weeks, **L** ≈ multi-week with
design + recovery/standby test coverage.

---

## P0 — correctness / longevity blockers

### G1. No CLOG truncation, freezing, or wraparound recovery
- **PG:** VACUUM advances `datfrozenxid`, freezes old tuples, and
  `TruncateCLOG` reclaims old `pg_xact/` segments; the cluster runs
  indefinitely.
- **goopg:** CLOG (flat file + in-memory banks) grows monotonically with
  `NextXID`; no `TruncateCLOG`/`CLOGPagePrecedes`/`AdvanceOldestClogXid`
  analogue and no VACUUM freezing. Wraparound is only *guarded*
  (`ErrXIDWraparound` in `internal/mvcc/manager.go`), not recovered from.
- **Impact:** Unbounded `global/pg_xact` and resident memory growth; a
  long-lived cluster eventually refuses new transactions at wraparound. Also a
  PG-compat gap — a real PG standby expects `pg_xact/` to be truncatable and
  `datfrozenxid` to advance.
- **Remediation:** Implement tuple freezing in VACUUM (write
  `FrozenTransactionID` into eligible tuple headers — the visibility fast path
  at `internal/mvcc/visibility.go:39-42` already honors it), track an
  `oldestXid` horizon, then add a truncation pass that drops flat-file prefix +
  SLRU segments using PG's wraparound-safe page comparison. Mirror
  `CLOG_TRUNCATE` semantics for standby compatibility.
- **Effort:** **L**.

### G2. Dual-store consistency risk (flat file ↔ SLRU mirror)
- **PG:** Single representation; no possibility of two stores disagreeing.
- **goopg:** Two stores written on every commit — the non-fsynced flat file
  (`flush`, `internal/mvcc/clog.go:398`) and the per-commit-fsynced SLRU mirror
  (`mirrorToSLRUUnlocked`, `clog.go:686`). They use different encodings (1
  byte/XID vs 2 bits/XID) and different durability (no fsync vs fsync). Recovery
  resolves disagreement by treating the SLRU as authoritative
  (`loadFromSLRU`, `clog.go:550`).
- **Impact:** This is exactly the *sibling-path* failure mode that has bitten
  this project before (`pattern_sibling_paths_must_agree`,
  `m0106_codec_regressed_6_regress_tests`): an encode/decode or
  flat-file/mirror mismatch passes a unit test on one path while the other is
  silently wrong. The `0x03` lane handling
  (`loadFromSLRU` reads it as committed, the `default` case at
  `clog.go:608-616`) is a concrete encoding-asymmetry seam.
- **Remediation:** Add a cross-store consistency check (a debug/CI assertion
  that flat file and SLRU agree for all terminal XIDs after recovery), and a
  round-trip test that writes via the commit path and re-reads via
  `loadFromSLRU`. Longer term, consider collapsing to the single 2-bit SLRU
  representation (see G6) to remove the seam entirely.
- **Effort:** **S** (consistency test) / **L** (collapse to one store).

### G3. Standby-attach `InitializeAsCommitted` ordering is untested
- **PG:** N/A (homogeneous).
- **goopg:** A basebackup-attached cluster **must** call
  `InitializeAsCommitted(upstream_nextXid)` *before* the implicit-abort sweep
  `MarkUnknownAsAborted`, or upstream XIDs absent from the local CLOG get
  wrongly stamped `Aborted` (documented at `internal/mvcc/clog.go:237-241`).
- **Impact:** A subtle, data-corrupting ordering invariant that is enforced only
  by a comment. If a future standby/attach code path forgets it, committed
  upstream rows silently disappear — and per
  `pg_on_goopg_catalog_lacks_pg_stat_views`, heterogeneous-standby E2E is
  already a fragile area.
- **Remediation:** Add an E2E/integration test that attaches via basebackup,
  restarts, and asserts upstream rows remain visible. Consider making the
  ordering structural (e.g. have the attach path own both calls) rather than
  relying on callers.
- **Effort:** **M**.

---

## P1 — semantic / compatibility gaps

### G4. Visibility path never consults CLOG at runtime
- **PG:** `HeapTupleSatisfies*` calls `TransactionIdDidCommit` →
  `TransactionIdGetStatus`; CLOG is the runtime commit oracle.
- **goopg:** `Snapshot.SeesCommittedXID` (`internal/mvcc/snapshot.go`) decides
  from `Xmin`/`Xmax` plus in-memory `InProgress`/`Aborted` arrays
  (`snapshot.go:62-73`); `GetStatus` is used only at load/recovery time.
- **Impact:** Correctness depends on every aborted-but-in-window XID being
  present in the snapshot's `Aborted` array at capture time. For very long-lived
  readers or large in-flight sets this array grows, and any path that fails to
  populate it (e.g. an abort learned about after snapshot capture) risks showing
  rows of an aborted transaction. It also diverges from PG's model, complicating
  future features (e.g. `SELECT FOR UPDATE` recheck, standby visibility).
- **Remediation:** Add a CLOG fallback in `SeesCommittedXID` for in-window XIDs
  not otherwise classified (consult `GetStatus`), keeping the snapshot arrays as
  a fast path. **Caution:** this is a planner/executor-adjacent visibility
  change — gate it behind the TPC-H spot-check (`scripts/tpch-spotcheck.sh`,
  canonical Q12=2/Q13=35) and audit sibling visibility paths
  (`visibility.go` ↔ `subxact_visibility.go`) per the practice card.
- **Effort:** **M**.

### G5. No persistent `pg_subtrans` / no `SUB_COMMITTED` state
- **PG:** Persistent `pg_subtrans` SLRU (4 bytes/XID) +
  `TRANSACTION_STATUS_SUB_COMMITTED` give durable, cross-restart subxact
  parentage and multi-page commit-tree atomicity.
- **goopg:** In-memory `SubxactMap` only
  (`internal/mvcc/subxact_visibility.go:14`); subxact lifecycle is WAL-logged
  and replayed into memory, but nothing is persisted to a `pg_subtrans`
  directory, and there is no `SUB_COMMITTED` lane.
- **Impact:** Fine today (subxacts can't span a restart), but blocks: durable
  subxact resolution for an attached PG standby, two-phase commit, and any
  reader that must resolve a subxact's top-level XID after the owning backend is
  gone.
- **Remediation:** When standby/2PC work begins, add a PG-byte-compatible
  `pg_subtrans` mirror analogous to the existing CLOG SLRU mirror, and a
  `SUB_COMMITTED` encoding in the CLOG mirror. Defer until a concrete consumer
  exists.
- **Effort:** **L**.

### G6. No SLRU buffer pool — whole status array resident
- **PG:** SLRU shared buffer pool pages CLOG in/out; memory bounded by
  `transaction_buffers`.
- **goopg:** Entire status array resident as per-bank byte slices
  (`internal/mvcc/clog.go:64`), 1 byte/XID; SLRU mirror files are accessed
  per-update with no page cache.
- **Impact:** Memory scales linearly with `NextXID` (≈1 byte/XID for the runtime
  store; 16× PG's 2-bit density). Combined with G1 (no truncation) this is the
  practical memory ceiling. Negligible at current goopg scale.
- **Remediation:** Largely subsumed by collapsing to the 2-bit SLRU
  representation with a bounded page cache (the G2 long-term option). Only worth
  doing after G1.
- **Effort:** **L** (couple with G2/G1).

---

## P2 — performance / fidelity

### G7. No group commit; whole-file rewrite per status change
- **PG:** `TransactionGroupUpdateXidStatus` batches concurrent updates behind one
  bank-lock acquisition; CLOG pages are written at checkpoint, not per commit.
- **goopg:** Each `SetCommitted`/`SetAborted` rewrites the **entire** flat file
  (`flush`, `internal/mvcc/clog.go:398`, `os.WriteFile` of the whole array) and
  fsyncs one SLRU segment (`mirrorToSLRUUnlocked`, `clog.go:742-746`). No
  batching across backends.
- **Impact:** Commit cost is O(flat-file size) plus a per-commit fsync; under
  high commit concurrency this serializes on the file write and the bank lock.
  A latent throughput cliff as the flat file grows (interacts with G1).
- **Remediation:** Make `flush` incremental (write only the changed
  page/segment, not the whole file) and add a group-commit batching layer over
  the SLRU fsync. Pairs naturally with the G6/G2 single-store refactor.
- **Effort:** **M**.

### G8. No async-commit LSN tracking (`group_lsn`)
- **PG:** Per-32-XID async-commit LSN (`CLOG_XACTS_PER_LSN_GROUP`,
  `postgres/.../clog.c:91-96`) gates honoring a committed status until WAL is
  flushed to that LSN — what makes hint bits safe under `synchronous_commit=off`.
- **goopg:** No `group_lsn` concept. On the commit path the xact-marker hook
  flushes the WAL up to the commit LSN *before* stamping CLOG
  (`internal/initdb/open.go`), i.e. effectively synchronous commit only (the
  abort path does not pre-flush, but an un-flushed abort is harmless on replay).
- **Impact:** goopg cannot offer PG's async-commit performance mode while keeping
  hint-bit safety; and if async commit were added naively, hint bits could be
  set before the commit record is durable. Confirm goopg does not currently
  expose `synchronous_commit=off` as a real async path before relying on this.
- **Remediation:** Only if/when async commit is targeted: add per-group commit
  LSN tracking to the CLOG and gate `SeesCommittedXID`/hint-bit setting on WAL
  flush position.
- **Effort:** **L**.

### G9. No CLOG-specific WAL records (`CLOG_ZEROPAGE` / `CLOG_TRUNCATE`)
- **PG:** Page zeroing and truncation are WAL-logged (`clog.h:55-56`) and
  replayed by `clog_redo`.
- **goopg:** CLOG is re-derived from `XactCommit`/`XactAbort` records
  (`internal/wal/recovery.go:73-78`, `internal/initdb/xact_recovery.go`); no
  zeropage/truncate records.
- **Impact:** Acceptable while there is no truncation (G1) and the flat file is a
  derived cache. Becomes required the moment truncation lands — a PG standby
  replaying from goopg WAL would expect `CLOG_TRUNCATE` to advance its own
  `oldestClogXid`.
- **Remediation:** Add `CLOG_TRUNCATE` (and, if the SLRU becomes the primary
  store, `CLOG_ZEROPAGE`) WAL emission as part of G1.
- **Effort:** **M** (bundled with G1).

---

## Prioritization summary

Keyed to goopg's goal of **heterogeneous PostgreSQL-standby compatibility**:

| ID | Gap | Priority | Effort |
|----|-----|----------|--------|
| G1 | No truncation / freeze / wraparound recovery | **P0** | L |
| G2 | Dual-store consistency risk | **P0** | S→L |
| G3 | Untested standby-attach `InitializeAsCommitted` ordering | **P0** | M |
| G4 | Visibility never consults CLOG at runtime | P1 | M |
| G5 | No persistent `pg_subtrans` / `SUB_COMMITTED` | P1 | L |
| G6 | No SLRU buffer pool (whole array resident) | P1 | L |
| G7 | No group commit; whole-file rewrite per commit | P2 | M |
| G8 | No async-commit LSN tracking | P2 | L |
| G9 | No `CLOG_ZEROPAGE`/`CLOG_TRUNCATE` WAL records | P2 | M (with G1) |

**Suggested sequencing:** land the cheap safety nets first — G2's consistency
test and G3's standby E2E — since they protect existing behavior. Then take G1
(freeze + truncation) as the keystone, bundling G9's `CLOG_TRUNCATE` WAL record;
G6/G7 (single-store + incremental flush + group commit) follow naturally on top
of G1. G4, G5, and G8 are feature-driven — pursue them when runtime CLOG
visibility, durable subxacts/2PC, or async commit become concrete requirements.

> **Note:** This is an analysis document, not a committed roadmap. Every gap was
> verified against branch HEAD on 2026-06-14, but any work derived from it should
> re-confirm the cited code (the codebase moves fast) and run the relevant
> practice-card gates (TPC-H spot-check for visibility changes; race detector +
> recovery/replication E2E for WAL/MVCC changes).
