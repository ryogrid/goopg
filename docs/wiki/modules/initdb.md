# Module: `internal/initdb`

The cluster bootstrap + startup-recovery layer. `Init` creates a
**byte-compatible PostgreSQL 18.3 data directory** (config files, PG-canonical
system-catalog heap rows, btree index files, relcache init files, pg_control,
bootstrap WAL segment) that works for goopg's own runtime *and* for a real
PG 18.3 attached as a standby. `Open` turns an existing data dir into the
long-lived `Runtime` bundle by first replaying WAL for crash recovery, then
reconstructing the in-memory catalog from the on-disk catalog heaps.

## Key Files

- `initdb.go` (6,788) — `Init`: dir layout, config seeding, per-catalog heap
  writers, shared/CLOG/SLRU placeholders, template0 refresh, fsync.
- `open.go` (3,944) — `Open`: verify dir, WAL replay, construct `Runtime`, the
  ordered catalog-reload passes, runtime-view registration, save/close.
- `relcache_init.go` (3,580) — PG-binary `pg_internal.init` writer; `nailedRel`/
  `nailedAttr` descriptors and per-catalog column schemas.
- `catalog_heap_reload.go` (2,732) — generic heap-scan reload framework:
  `scanCatalogHeapRows`, `catalogRowLive`, `runCatalogReloads`, ~40
  `reload*FromHeap` passes.
- `pg_proc_seed_data.go` (3,405) — generated mirror of PG18 `pg_proc.dat` (3,397 rows).
- `btree_index_bootstrap.go` (3,000) — PG-conformant btree index file builders.
- `nailed_view_seed_data.go` / `information_schema_view_seed_data.go` — seed
  tables for the 80 pg_catalog + 65 information_schema views.
- `pg_rewrite_bootstrap.go` + `nailed_view_ev_action.go` + `pg_rewrite_toast_writer.go`
  — pg_rewrite `_RETURN` rule seed + captured `ev_action` blobs + TOAST.
- `pgcontrol.go`, `wal_bootstrap.go`, `timeline.go`, `standby.go`,
  `recovery_state.go`, `xact_recovery.go` — pg_control, bootstrap WAL, TLI,
  crash-recovery state, CLOG replay.
- `catalog_cache.go` — M0114 fast-start JSON snapshot.

## Public API

Cluster creation:

```go
func Init(opts Options) error
type Options struct{ /* DataDir, SuperuserName, WALDir, NoSync, SyncOnly,
    SyncMethod, AuthMethodHost/Local, PwFile, Encoding, Locale*, DataChecksums, … */ }
func SampleFiles() []FileSpec
func LoadOrCreateSystemID(dataDir string) (uint64, error)
func CreatePerDatabaseScaffolding(dataDir string, dbOID uint32) error
const CatalogVersion = misc.MajorVersion
```

Server open:

```go
func Open(opts OpenOptions) (*Runtime, error)
type Runtime struct{ StorageMgr, Pool, TxnMgr, Catalog, FSM, VM, WAL *xlog.Writer,
    Checkpointer, Slots, SyncRep, PubSub, AIO, Activity, /* Standby/Recovery flags */ }
type OpenOptions struct{ PoolSlots, WALBuffers, FsyncDisabled, CommitDelayUs/Siblings, /* … */ }
func (r *Runtime) SetImmediateShutdown()
func (r *Runtime) SaveCatalog() / SaveVM() / SaveFSM() / Close()
```

PG-compat artifacts:

```go
func WriteBootstrapWAL(dataDir string, sysID uint64, now time.Time) error
func BackupControlImage(dataDir string, redoLSN, ckptRecordLSN uint64, tli uint32) ([]byte, error)
func LoadOrCreateTimelineID(dataDir string) (uint32, error)
func IsStandby/IsRecovery/CreateStandbySignal/RemoveStandbySignal/RemoveRecoverySignal
```

## Internal structure

**Bootstrap (`Init`)** — validate options → create subdirs → write config →
`bootstrapSystemCatalogs` (seeds pg_type/pg_class/pg_attribute, XID=1) → a long
chain of per-catalog `bootstrap*Tuples` calls writing PG-canonical heap rows +
matching btree index files → pg_rewrite + TOAST → CLOG/SLRU placeholders →
`bootstrapRelcacheInitFiles` → `refreshTemplate0Image` → `WriteBootstrapWAL` →
`writePgControl` → optional checksums → `syncDataDir`.

**Open (`Open`)** — verify → `storage.NewManager` → `beginRecovery` (redo LSN) →
WAL replay (`xlog.ReplayFromDirWithMgrAt`) → system ID/TLI → WAL writer wiring →
CLOG replay + abort sweep → **catalog heap reloads** (system catalogs, schemas,
tablespaces, transforms, user tables per DB, databases, stats, indexes,
sequences, column defaults, inheritance, views, roles) → regen `pg_internal.init`
→ register runtime views → `stampInProduction`.

**Reload framework** — one shared scan loop `scanCatalogHeapRows` + liveness
filter `catalogRowLive` (xmin≠Invalid, xmax==Invalid, xmin not aborted) +
per-catalog `catalogReloadDesc` sorted by `Slot`.

## Dependencies

- **Uses** `internal/catalog`, `internal/executor` (EncodeRowPG, PG column
  schemas), `internal/storage` (+`aio`), `internal/access/transam` (+`xlog`,
  `control`), `internal/utils/misc`, `internal/parser`, `internal/nodes`,
  `internal/optimizer`, `internal/access/nbtree`, `internal/libpq/auth`.
- **Used by** `cmd/goopg`, `internal/postmaster`, `internal/executor`,
  `internal/optimizer`, `internal/backup/basebackup`,
  `internal/postmaster/autovacuum`. Import direction trap: `executor` cannot
  import `initdb` leaf constants (cycle) — version constants live in `utils/misc`.

## Notable patterns / gotchas

- **Dual-purpose seed data** — every catalog is written both as PG-canonical
  heap rows + btree index files (for PG standby/syscache) *and* reloadable into
  the in-memory catalog. A `pg_attribute`/`pg_internal.init` mismatch PANICs a
  hosted PG.
- **Generated corpus, captured oracle** — the seed-data files are generated by
  `cmd/gen-pg-proc-data` et al. from PG 18.3 dat files; system-view OIDs are
  pinned to upstream initdb's assignments (12000–16383 band) so captured
  `ev_action` blobs need no rewriting.
- **`ev_action` blobs are verbatim** — captured from a live PG; large blobs go
  out-of-line into the pg_rewrite TOAST pair (2838/2839).
- **pg_control is initdb-time + runtime-patched** — written at initdb and
  updated at runtime (crash-recovery state, nextOid, checkpoints).
- **Bootstrap XID = 1** — all seeded rows carry xid=1, always visible.
- **Reload liveness rules** — xmin==Invalid dead; any non-zero xmax dead
  (catalog mutations are delete+reinsert); aborted xmin dead.
- **M0114 catalog cache** — clean-shutdown JSON snapshot skips the
  pg_class/pg_attribute heap scan on next start.
- **Sibling-path discipline** — bootstrap-seed ↔ runtime-reload
  (`writeMultiPageHeap*` ↔ `load*FromHeap`), `EncodeRowPG` ↔ `DecodeRowIntoMctxPGTuple`.
