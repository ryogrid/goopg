# 02 — Canonical-only coverage audit

status: design · date: 2026-07-13 · base: `e453e3f2` · slice: S3 (hardening
tests) · gates: G-crash **in both modes**, G-unit, G-race

The correctness core of native-only: for every state change that today emits
**only** a canonical record (no native sibling), prove goopg's own crash
recovery does not regress when canonical emission stops. Exploration
classified every producer; the paired ones (user heap insert/HOT/delete/
prune, VACUUM prune, commit/abort — each with a native record goopg recovery
replays) need no work. Six items are canonical-only; each is dispositioned
below.

## 1. Catalog heap-row inserts — SELF-RESOLVING, and load-bearing (R1)

**Today**: DDL writes catalog rows (pg_class heap is virtual, but
pg_attribute/pg_proc/pg_type are heap — memory:
`goopg_pg_class_virtual_pg_attribute_heap`) through
`writeHeapRowCanonical` → `PgCanonicalHeapInsert` (operators_ddl.go:13453).
Critically, `writeHeapRowReturningPG` **nils the native `logHeap` hook only
when `LogCanonical != nil`** (operators_storage.go:8017-8019 at `e453e3f2`;
+13 lines ≈ :8030-8032 in a tree carrying the unrelated lockrows WIP) — the
two families are mutually exclusive on this path by design.

**Under the switch (off)**: `LogCanonical == nil` → the native hook is NOT
nilled → catalog heap inserts automatically emit native
`RecordKindHeapInsert` (the hook is installed unconditionally at
open.go:624, closure at :449 → `wal.EncodeHeapInsert`). This is exactly
legacy/unit-test mode behavior.

**Why this is load-bearing, not belt-and-suspenders**: `open.go:1228`
documents `loadUserTablesFromHeap` as **"the sole catalog recovery path: DDL
writes rows here via syncTableToCatalogHeap, and WAL replay restores them
after a crash."** Catalog crash recovery depends on replaying catalog heap
pages. (An earlier exploration pass claimed the pg_catalog.json snapshot +
pg_control carried catalog durability — that is **stale/incomplete**; the
snapshot accelerates startup but the heap replay is authoritative for
post-snapshot DDL.)

**S3 proof obligations**:
- crash test: `CREATE TABLE` (+ index, + a pg_proc-affecting DDL) → SIGKILL
  before checkpoint → restart under `GOOPG_WAL_CANONICAL=off` → catalog rows
  present, `loadUserTablesFromHeap` (and the M0114 catalog-cache fast path at
  open.go:1220 AND its heap-scan fallback) both serve the objects.
- **pd_lsn stamping parity**: the canonical path stamped the page LSN
  (batched-42 H1, `writeHeapRowCanonical`); verify the native path's
  `MarkDirtyLogicalChange`/`MarkDirtyChangeRecord` stamps an equivalent
  pd_lsn (it does for user tables — assert the catalog-heap path takes the
  same stamped route, or replay idempotency's `pd_lsn >= EndLSN` skip
  degrades to always-apply, which is still correct but should be stated).

### 1a. Catalog xmax-stamp / delete path — AUDIT ADDITION (adversarial F-2)

The insert story above is not the whole catalog write surface. ALTER re-syncs
("delete-old-rows + syncTableToCatalogHeap") and DDL rollback stamp/delete
catalog rows via `stampCatalogRows` / `deleteCatalogRowsForOID`
(operators_ddl.go:12654-12708), which call **`MarkDirtyForceFPI`**
(bufpool.go:1774) — an unconditional forced page image with **no logical
record and no `LogCanonical` involvement at all**. Consequences:

- The §7 grep method (LogCanonical/PgCanonical call sites) structurally
  cannot see this path — the completeness guard must be extended to
  image-only catalog writers (`MarkDirtyForceFPI` call sites).
- In production (fullPageWrites on) the forced image makes the stamp
  torn-page-safe and replay-safe by itself — and immune to the doc-03 window.
- In legacy/unit mode (`logFPI==nil`), `MarkDirtyForceFPI` emits nothing; the
  stamp's durability then rides the **DDL-record replay path**
  (`replayDatabaseDDLRecords`/`replayIndexDDLRecords`) — which means §1's
  "sole catalog recovery path" quote is PARTIAL: heap replay is authoritative
  for row content, the DDL-record path independently reconstructs
  drop/alter effects. State both; do not lean on either alone.
- **S3 test addition**: ALTER re-sync (e.g. DROP COLUMN) → crash → restart
  under canonical-off; and the doc-03 window test's catalog variant
  (pg_attribute page dirtied in the prior epoch + in-window insert).

## 2. System-catalog btree insert / split / multilevel — ACCEPT DRIFT

**Today**: sys-index writes (pg_class_oid_index 2662, relname_nsp 2663,
pg_attribute_relid_attnum 2659) use plain `ctx.Pool.MarkDirty(slot)` +
canonical `PgCanonicalBtreeInsert`/split records
(sys_catalog_index_insert.go:296-300, sys_catalog_btree_split.go:416-440,
sys_catalog_btree_multilevel.go:371/:449). No native btree record.

**Under native-only**:
- **Torn pages: covered.** Plain `MarkDirty` already calls `maybeEmitFPI`
  (bufpool.go:1708; pool built with `FullPageWrites: true`) → these pages get
  the native first-touch FPI per epoch like every other page.
- **Intra-epoch increments: unreplayed.** A 2nd+ insert into the same sys-
  btree leaf between checkpoints has no WAL record; after a crash the page
  reverts to its FPI state (or checkpoint-flushed state) → **missing entries
  on the on-disk sys-btree**.
- **Why acceptable**: goopg never reads these btrees for its own lookups —
  relation resolution is served from the in-memory catalog (zero read paths
  open index 2662 et al.); the on-disk sys-btrees exist purely for the
  (deferred) real-PG read surface. The in-memory catalog + catalog *heap*
  (item 1 — natively replayed) remain correct; `rebuildSysBtreeWithNewEntry`
  exists as the reconstruction precedent if/when the surface resumes.

**Decision (D-02-1): accept the drift + ledger row** (doc 04 §4 row 3).
Alternatives recorded for the resume path: route through the existing native
`LogBtreeInsert` hook, or rebuild sys-btrees from the catalog heap at startup.

**S3 test**: crash across sys-btree inserts under canonical-off → server
restarts clean, in-memory catalog correct, DDL/queries on the affected
objects work (the drift is invisible to goopg by construction — the test pins
that invisibility).

## 3. `pg_database.datfrozenxid` in-place write — COVERED

**Today**: VACUUM-end in-place overwrite emits `PgCanonicalHeapInplace`
(operators_vacuum_datfrozenxid.go:132) + plain `MarkDirty` (:140).

**Under native-only**: first-touch FPI covers the torn page; the datfrozenxid
*value* is re-derivable — recovery seeds from pg_control
(`CheckPointCopyNextXid`) + SLRU truncation state, and the next VACUUM
re-stamps. Worst case after a crash: datfrozenxid on the heap page reads
one-VACUUM stale, which only delays SLRU truncation until the next VACUUM —
PG itself tolerates exactly this staleness pattern. **S3 test**: crash after
VACUUM-end under canonical-off → restart → CLOG truncation still functions,
next VACUUM re-advances.

## 4. `XLOG_PARAMETER_CHANGE` — RETAINED (not gated)

goopg's own recovery consumes it (`replayXLogParameterChange`,
recovery.go:9244→9342: syncs a goopg standby's pg_control GUC echoes).
Startup-only, ~50 B. Doc 01 §3.3/D4: choke point 3 keeps only the
`PageHeadersEnabled()` gate. Nothing to audit further.

## 5. Commit/abort records — NATIVE PATH SUFFICIENT

`replayCLogFromWAL` (xact_recovery.go:63-91) handles native
`RecordKindXactCommit` / `RecordKindXactCommitInval` / `RecordKindXactAbort` /
`RecordKindClogTruncate` **first** with `continue`; the canonical `RmgrXact`
branch (:87-91) is a fallback that only fires when no native kind matched —
and the native records are always emitted (open.go:904-929). Removing
canonical xact records changes nothing. (The `recovery.go:68` "crash-recovery
is a no-op for XactCommit" comment describes the *first* replay pass; the
CLOG re-stamp is this separate second pass.)

## 6. Checkpoint record — OUT OF SCOPE BY CONSTRUCTION

Not canonical-enveloped: `EncodeCheckpointCompat` (88-byte PG `CheckPoint`
struct) is appended directly (checkpointer.go:550-556) and recovery matches
it by shape (`isCheckpointRecord`, recovery.go:10276: `len==1 || len==88`;
`replayStart` :10261 scans for the last one). Survives unchanged.

## 7. Completeness

The producer enumeration was exhaustive over `LogCanonical` / `PgCanonical*` /
`buildCanonicalPayload` call sites (plus `RelationMapUpdateMap`, a nil stub).
S3 adds a **guard test**: grep-based (or build-tag) assertion that no new
`LogCanonical` consumer appears without a native sibling or an entry in this
audit, **extended to image-only catalog writers** (`MarkDirtyForceFPI` call
sites — §1a showed the LogCanonical grep alone is blind to them) — insurance
against the sibling-paths trap (`pattern_sibling_paths_must_agree`).

## 8. Open questions (flagged)

- **O-02-1**: the pd_lsn stamping parity check (item 1) — if the catalog-heap
  native path turns out not to stamp pd_lsn, decide stamp-in-native-path vs
  accept always-reapply idempotency.
- ~~O-02-2~~ **resolved** (adversarial F-6): TOAST uses
  `ctx.Pool.LogHeapInsert()` **unconditionally** (toast.go:315-340) — always
  native-covered, no nil-hook dependency.
- **O-02-3**: does any *test* assert canonical records exist for the item-1/2
  paths specifically (beyond the 4 known breaking tests)? S3's both-modes
  G-unit run answers empirically.
