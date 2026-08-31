# 0117-0008 — Persist `datfrozenxid` in the on-disk `pg_database` tuple

Status: accepted (Part A + Part B landed 2026-07-02)
Milestone: M0117-0008 (CLOG ↔ PostgreSQL alignment; gap G-followup; P2)
Author: Ralph
Date: 2026-06-15 (Part B: 2026-07-02)

## Problem

PostgreSQL stores each database's freeze horizon in the `pg_database.datfrozenxid`
catalog column and **advances it at the end of every VACUUM** via
`vac_update_datfrozenxid` (`postgres/src/backend/commands/vacuum.c`), which performs
an in-place `heap_inplace_update` of the live `pg_database` tuple. The cluster-wide
`vac_truncate_clog` then reads `MIN(pg_database.datfrozenxid)` to decide how far CLOG
(`pg_xact`) may be truncated, and anti-wraparound monitoring compares the assigned-XID
distance against `datfrozenxid`.

goopg bootstraps the on-disk `pg_database` heap once, at initdb time, with a
hard-coded `datfrozenxid = 3`
(`internal/initdb/initdb.go:1670`, `bootstrapPostgresDatabase`, heap file
`global/1262`). At runtime that on-disk value is **never updated** — VACUUM advances
only the in-memory `catalog.Table.RelFrozenXID`
(`internal/executor/operators_vacuum.go:67,70`), and `catalog.InMemory.DatFrozenXID()`
recomputes `min(relfrozenxid)` across user tables **on demand**
(`internal/catalog/catalog.go:4514`).

Consequence: the persisted `pg_database.datfrozenxid` is permanently stale (`3`). The
gap is a **continuous-PG-compatibility** defect (cf. `feedback_m0106_continuous_pg_compat`):
a PostgreSQL instance that attaches as a physical standby — or any external tool that
reads the catalog heap — sees a freeze horizon frozen at the bootstrap value, even
though the real horizon has advanced far past it. PG would mis-report wraparound
age and could make incorrect `datfrozenxid`-driven decisions.

### What is NOT affected

goopg's own CLOG truncation is **independent of the persisted tuple**. The
checkpointer `TruncateCLOGFn` reads the in-memory `cat.DatFrozenXID()` directly
(`internal/initdb/open.go:1112-1131`), not the on-disk `pg_database` row. So
truncation already uses an up-to-date horizon; the only loss is external (standby /
tooling) parity and restart-survivability of the *persisted* value. This bounds the
blast radius: persisting the tuple cannot change goopg's own visibility or truncation
behaviour — it only makes the on-disk catalog truthful for outside readers.

## Scope split

This task has two independently-deliverable halves. The fix_plan bundles them; this
doc separates them by verifiability.

### Part A — Dual-store consistency coverage of all status codes — **DONE (chain)**

The fix_plan's "extend the dual-store consistency tests for round-trip coverage of all
status codes" is **already satisfied** by the stacked chain. M0117-0004 extended
`internal/mvcc/clog_dual_store_consistency_test.go` so that all four PG CLOG lane
values round-trip flat-file ↔ SLRU:

| status | code | covered by |
|---|---|---|
| `IN_PROGRESS` / Unknown | `0x00` | `TestCLogTruncateKeepsStoresConsistent` (whole-bank-dropped → `Unknown`) |
| `COMMITTED` | `0x01` | `TestCLogDualStoreConsistency` (in-memory / SLRU-derived / flat-reopened views) |
| `ABORTED` | `0x02` | `TestCLogDualStoreConsistency` |
| `SUB_COMMITTED` | `0x03` | `TestCLogDualStoreConsistency` + `TestCLogSubCommittedResolvesViaParent` (`DidCommit` parent resolution, SLRU round-trip) |

Coverage spans adjacent lanes (bit-shift drift), the page boundary (XID 32767/32768),
two SLRU segments, and a `TruncateCLOG`. Verified green this loop:
`go test -race -run 'TestCLogDualStoreConsistency|TestCLogSubCommittedResolvesViaParent|TestCLogTruncateKeepsStoresConsistent' ./internal/mvcc/` → `ok`. No new test is
warranted — adding a bare "write `0x00`, read `0x00`" assertion would be low-signal
duplication of the truncation path's `Unknown` coverage.

### Part B — On-disk `pg_database.datfrozenxid` persistence — **DEFERRED**

Updating the on-disk tuple at VACUUM end is **not** the Effort-S change the fix_plan
label implies. A focused investigation of the runtime catalog-write surface
established:

1. **No runtime resolver for shared-catalog `RelFileNode`s.** The in-memory catalog
   maps only user tables/indexes to `RelFileNode`s
   (`catalog.RelFileNode(tbl)`). There is no
   `Catalog.SystemRelFileNode(oid)` that would resolve `pg_database` (OID 1262, a
   *shared* relation at `global/1262`, `RelFileNode{DBOid:0, RelOid:1262, Fork:Main}`)
   so the executor could open its page through the buffer pool. The VACUUM operator's
   `Context` (`o.ctx.Pool`, `o.ctx.Catalog`, `o.ctx.TxnMgr`) cannot currently reach it.

2. **A runtime catalog-heap *append* precedent exists, but no in-place update.**
   `syncTableToCatalogHeap` (`internal/executor/operators_ddl.go:~6279`,
   `writeHeapRowCanonical`) writes **new** `pg_class` / `pg_attribute` tuples during DDL
   — an append. PostgreSQL `datfrozenxid` advancement is deliberately an
   `heap_inplace_update` (overwrite a fixed-width field of the *existing* tuple, no new
   row version, no MVCC), which has no analogue in goopg. `pg_database` specifically is
   never written at runtime.

3. **`bootstrapRelcacheInitFiles` / `pg_internal.init` regeneration does not help.** The
   `PostCheckpointFn` (`internal/initdb/open.go:1092-1099`) regenerates only the relcache
   **TupleDesc/metadata** (`internal/initdb/relcache_init.go`), never the `pg_database`
   row **data**.

4. **Correctness needs WAL + locking + a standby gate.** A faithful port of
   `heap_inplace_update` must take the buffer content lock, log the change
   (`XLOG_HEAP_INPLACE` analogue) so crash recovery and the standby replay it, and run
   under the same wraparound-safe horizon already used elsewhere
   (`storage.XIDPrecedes`, M0117-0001). The *defining* verification — that an attached
   PG standby reads the advanced `datfrozenxid` — requires a live PG-attach E2E, which
   **SKIPs under the worktree isolation** this milestone uses to dodge the foreign
   M0100-0010 catalog WIP.

Per the `m0074_partial_scope_lessons` precedent (central/high-risk changes get
infrastructure-or-design-only scope in an autonomous loop; the risky live swap lands in
a dedicated session that can run the full gate) and the `feedback_m0106_continuous_pg_compat`
requirement (the on-disk/catalog state must hold for *ongoing* operation, verified
against a real PG attach), Part B is deferred to a dedicated full-gate session.

## Implementation plan for Part B (for the resuming session)

Mirror `vac_update_datfrozenxid` + `heap_inplace_update`:

1. **Resolve the shared catalog file.** Add a small resolver (e.g.
   `catalog.SharedCatalogRelFileNode(oid uint32) storage.RelFileNode`) returning
   `{DBOid:0, RelOid:oid, Fork:Main}` for the bootstrapped shared catalogs, so the
   executor can open `global/1262` through `o.ctx.Pool`.
2. **Locate the live tuple.** Open page 0 of `global/1262`, walk line pointers, match the
   row whose `datname` is the current database (single-db v0: the `postgres` / live row).
   The schema prefix up to `datfrozenxid` (ordinal 9) is entirely fixed-width
   (`oid, datname[NAMEDATALEN], datdba, encoding, datlocprovider, datistemplate,
   datallowconn, dathasloginevt, datconnlimit`), so `datfrozenxid` sits at a constant
   byte offset inside the tuple's fixed prefix — an overwrite needs no re-encode and
   cannot change tuple length or the null bitmap.
3. **In-place overwrite.** Compute `horizon = cat.DatFrozenXID()`; if it is `>=
   FirstNormalTransactionID` and `storage.XIDPrecedes(current_on_disk, horizon)`,
   overwrite the 4 bytes in place (and `datminmxid` once a multixact horizon exists).
   Take the buffer content lock; mark the page dirty.
4. **WAL-log it** (`XLOG_HEAP_INPLACE` analogue) so recovery and a streaming standby
   replay the overwrite; without this the standby would not observe the advance.
5. **Hook at VACUUM end.** Call it once after the per-table relfrozenxid advancement loop
   in `vacuumOp.Next` (`internal/executor/operators_vacuum.go`), guarded so a `pg_database`
   update failure never aborts the client VACUUM (matching the existing error-suppression
   contract).

### Gate for Part B (dedicated session, clean tree)

- `go test -race ./internal/mvcc/... ./internal/executor/... ./internal/catalog/...`
- Re-init data dir; full regress-port re-run (catalog-heap write → format-drift watch,
  cf. `m0106_codec_regressed_6_regress_tests`).
- **PG-standby-attach E2E**: after VACUUM advances the horizon on the goopg primary, the
  attached PG standby must read the advanced `pg_database.datfrozenxid` (the defining
  parity check; cf. `feedback_m0106_continuous_pg_compat`).
- TPC-H Q12/Q13 spot-check (executor path touched).

### Part B — On-disk `pg_database.datfrozenxid` persistence — **LANDED 2026-07-02**

Implemented the 5-step plan above almost verbatim, with one architectural correction
discovered mid-implementation: **step 1's premise was incomplete.** It's not only that
`catalog` lacked a shared-catalog `RelFileNode` resolver — `storage.Manager`'s path
builder (`relPath`/`RelPath`) had **no concept of a shared (`global/`) tablespace at
all**; every `RelFileNode` unconditionally resolved to `base/<DBOid>/<RelOid>`, so a
naive `RelFileNode{DBOid:0, RelOid:1262}` would have opened `base/0/1262` — silently
wrong, not merely unresolved. Fixed by making `DBOid == 0` goopg's sentinel for "shared
catalog" in `storage.Manager.relPath`/`RelPath` (a new `sharedOrPerDBRelDir` helper);
`DBOid == 0` is safe to repurpose because no live database is ever assigned OID 0
(`catalog.DefaultDBOid == 1`; the live default resolves to `"postgres"`'s OID 5 via
`detectCatalogDBOID` at startup) — grepping the tree confirmed zero existing
`RelFileNode{DBOid: 0, ...}` construction sites before this change.

**What landed, mapped to the 5-step plan:**

1. **Shared-catalog resolver** — `catalog.SharedCatalogRelFileNode(oid)` (new file
   `internal/catalog/pg_database_schema.go`) plus the storage-layer `global/` path fix
   above. The same file also hoists `bootstrapPostgresDatabase`'s previously-inline
   18-column `cols` literal into an exported `catalog.PgDatabaseColumnsPG18()` so
   initdb's bootstrap-time encode and the runtime persistence path's decode/encode
   share one schema definition and can never drift silently
   (`pattern_sibling_paths_must_agree`) — `internal/initdb/initdb.go`'s
   `bootstrapPostgresDatabase` now calls it instead of duplicating the column list.
2. **Locate the live tuple** — `internal/executor/operators_vacuum_datfrozenxid.go`'s
   `updatePgDatabaseTupleOnPage` scans `global/1262`'s page(s) for the row whose raw
   `oid` (offset 0, always fixed/non-nullable — same check as initdb's own
   `detectCatalogDBOID`/`decodePGDatabasePhysicalRow`) matches
   `catalog.InMemory.DBOID()`, gated on liveness (`Xmin` valid, `Xmax` invalid — the
   same test `decodePGDatabasePhysicalRow` uses). **Simplification vs. the original
   plan:** rather than hand-deriving `datfrozenxid`'s fixed byte offset from PG's
   struct-alignment rules, the implementation fully decodes the tuple via the existing
   `DecodeRowIntoMctxPGTuple` (the same header-driven PG-physical decoder ANALYZE and
   every heap-scan caller already use), mutates the one `Datum`, and re-encodes the
   whole row via `EncodeRowPG`. Since no other column's value or width changes, the
   re-encoded payload is byte-identical except for the mutated field — this sidesteps
   any hand-computed-offset bug class entirely rather than trading one micro-optimization
   for a durability-adjacent correctness risk.
3. **In-place overwrite** — `storage.PageReplaceItemRaw` (a pre-existing,
   already-tested primitive: same-length in-place line-pointer payload replacement)
   under the page's exclusive content lock (`Slot.Lock`/`Unlock`, held for the
   scan-locate-mutate-log-dirty window on each candidate page). The dirty-guard
   mirrors `vac_update_datfrozenxid`: skip the write when the on-disk value is already
   at-or-past the new horizon (`!storage.XIDPrecedes(current, horizon)`).
4. **WAL-log it** — a new `catalog.PgCanonicalHeapInplace`/
   `BuildCanonicalHeapInplacePayload` pair (`internal/catalog/canonical.go`), mirroring
   `PgCanonicalHeapInsert`'s full-page-image (FPI) strategy 1:1 rather than
   hand-encoding real PG's `xl_heap_inplace` shared-inval-message payload — a standby
   restores the whole post-overwrite page, so no tuple-level main-data parsing is
   needed on replay. Uses `XLOG_HEAP_INPLACE`'s real opcode (`0x70`); replay dispatch
   (`internal/wal/recovery.go`'s `replayDecodedXLogRecord`, `RmgrHeap` case) now
   recognizes `xlogHeapInplace` alongside `xlogHeapDelete/Update/HotUpdate` and applies
   the FPI identically (`replayDecodedXLogHeapFPIBlocks`) — this is new dispatch
   surface, not a reuse of the INSERT opcode, so a real PG standby's own
   `XLOG_HEAP_INPLACE` decoder (if one ever inspects goopg's WAL) sees the correct
   opcode rather than a misleading `XLOG_HEAP_INSERT`.
5. **Hook at VACUUM end** — `vacuumOp.Next` calls `persistDatFrozenXID` once,
   unconditionally (matching upstream: `vacuum.c`'s call site is gated only on
   `VACOPT_VACUUM`, which every `VacuumStmt`-backed `vacuumOp` invocation has — plain
   `ANALYZE` is a *different* statement/operator, `analyzeOp`, so this never fires for
   analyze-only), guarded with `_ = persistDatFrozenXID(...)` so a failure can never
   abort the client's VACUUM.

**Scope not carried over from real PG** (acceptable per the "purely external parity"
framing established when Part B was deferred): no `LockDatabaseFrozenIds`-equivalent
heavyweight lock is taken (the page's own content lock is sufficient for physical-write
atomicity on `pg_database`'s single v0 page; the pre-existing `intra-grant-inplace-db`
GRANT-conflict simulation, `waitForDatabaseACLChange`, is unaffected and unchanged), no
`datminmxid` advancement (goopg has no per-database multixact-freeze horizon to
advance — same gap noted by design `0118-0096`), and v0's single-live-database scope
(only the row matching `catalog.InMemory.DBOID()` is ever touched).

**Verification.** Beyond the two dedicated test files
(`internal/executor/operators_vacuum_datfrozenxid_test.go`:
`TestPersistDatFrozenXIDAdvances` proves the advance + idempotent no-write-when-not-ahead
dirty-guard; `TestPersistDatFrozenXIDEmitsCanonicalWAL` proves the WAL record shape) and
storage/WAL-level coverage (`internal/storage/shared_catalog_relpath_test.go`,
`internal/wal/canonical_heap_roundtrip_test.go`'s
`TestCanonicalHeapInplaceWALRoundTrip` — builds the payload, appends it to a real WAL
segment, decodes it back, and replays it onto a *fresh* `storage.Manager` to confirm the
mutated bytes land at `global/1262`, not `base/0/1262`), this was verified against a
**live, non-synthetic, `goopg init`-bootstrapped data directory**: `CREATE TABLE` +
`INSERT`, `SET vacuum_freeze_min_age = 1; VACUUM <table>;` over the wire via `psql`,
clean server shutdown (fsync), then decoding `global/1262` directly (bypassing SQL
entirely, since `SELECT datfrozenxid FROM pg_database` is served by the *virtual*
catalog / `InMemory.DatFrozenXID()` per design `0118-0096` — not this on-disk tuple, by
design) confirmed the `postgres` row's on-disk `datfrozenxid` advanced from the initdb
bootstrap value `3` to `4`, while `template0`/`template1` (never targeted) stayed at
their bootstrap values — proving the write is scoped to the correct row, not a
blanket rewrite.

## PostgreSQL references

- `postgres/src/backend/commands/vacuum.c` — `vac_update_datfrozenxid`,
  `vac_truncate_clog` (min-`datfrozenxid` across `pg_database`).
- `postgres/src/backend/access/heap/heapam.c` — `heap_inplace_update` (fixed-width,
  WAL-logged, no new row version).
- `postgres/src/include/catalog/pg_database.h` — column layout (mirrored at
  `internal/initdb/initdb.go`'s `bootstrapPostgresDatabase`, now sourced from
  `catalog.PgDatabaseColumnsPG18`).
- `postgres/src/include/access/heapam_xlog.h` — `XLOG_HEAP_INPLACE` (`0x70`),
  `xl_heap_inplace`.

## Decision

Part A (chain M0117-0004) and Part B (this loop, 2026-07-02) are both landed.
M0117-0008 is now fully closed in the fix_plan.
