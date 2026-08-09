# PG 18.3 standby E2E harness — basebackup + streaming + failover + reverse attach

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S10)

## Problem

M0130's ultimate acceptance vehicle: a PG 18.3 standby must start from a
goopg-created base backup, stream WAL, replay DDL+DML, survive failover,
and continue operating. No single test exercises this full path today
(components exist: `TestE2E_FailoverGoopgToPG` covers a narrow slice).

## Design

### E2E scenario (happy path)

1. **Start goopg primary** — fresh init, CREATE TABLE + INSERT workload.
2. **Create replication slot** (physical) on goopg primary.
3. **pg_basebackup** from goopg primary using the real `pg_basebackup` binary
   from `./postgres/local_install/bin/`.
4. **Configure PG standby:**
   - Write `postgresql.conf` with `primary_conninfo` pointing to goopg
     primary, `restore_command` empty, `primary_slot_name` = the slot name.
   - Create `standby.signal` file.
5. **Start PG standby** via `pg_ctl start`.
6. **Verify catch-up:** INSERT additional rows on goopg primary; verify
   they appear on PG standby via `psql` SELECT.
7. **DDL on primary:** CREATE TABLE, ADD COLUMN, CREATE INDEX, CREATE VIEW
   (requires S4–S6 completion).
8. **Verify DDL replays** on PG standby without FATAL.
9. **Failover:** promote PG standby via `pg_ctl promote`.
10. **Verify promotion:** PG standby is now writable; INSERT succeeds.
11. **Re-attach:** goopg can connect as standby to the promoted PG primary
    (reverse direction, requires S8 multi-timeline).

### Harness extension

- Extend `internal/testutil/replcluster/` to manage a real PG 18.3 instance
  alongside goopg instances.
- PG binary path: `./postgres/local_install/bin/` (the project's oracle PG).
- Test function: `TestE2E_PGStandbyFullCycle` in `internal/testport/`.

### Reverse attach (S10.4)

- After PG promotion, start a goopg standby against the PG primary's data dir:
  - The data dir was originally a goopg base backup → goopg should start
    against it (validates goal 1 forward direction).
  - Connect via WalReceiver to the promoted PG primary on the new timeline
    (validates goal 3 reverse direction + S8 multi-timeline).

## Guards

1. Full cycle: goopg primary → PG standby → promote → goopg standby.
2. DDL replays on PG standby without rmid-128 FATAL.
3. Rows written at each stage are visible at the next stage.
4. The existing replication family stays green (no regressions).
5. UNITS + SMOKE green.

## What was built

- **`TestE2E_PGStandbyFullCycle`** (`internal/testport/e2e_pg183_standby_full_cycle_test.go`):
  the M0130 acceptance vehicle. Four-phase E2E test:
  1. **Phase A (forward):** goopg primary → `pg_basebackup -X stream -C -S <slot> -R` →
     PG 18.3 standby via `pgcluster.OpenExisting` + direct `postgres` binary boot
     (pg_ctl can't read PMStatus from a goopg backup). Verify base-backup and
     post-backup rows are visible on the standby.
  2. **Phase B (DDL/DML replay):** CREATE TABLE, CREATE INDEX, ADD COLUMN, INSERT
     on goopg primary → verify each replays on the PG standby without FATAL.
     Exercises WAL fidelity (S4–S7).
  3. **Phase C (failover):** kill goopg primary → `pg_ctl promote` →
     verify promoted PG is writable.
  4. **Phase D (reverse attach):** `pg_basebackup` from promoted PG →
     start goopg standby against the new-timeline PG primary → verify
     streaming + INSERT visibility + all historical rows survived the
     full cycle. Exercises multi-timeline (S8) and bidirectional
     cluster-directory compatibility (goal 1).
- **`waitForPhysicalStreamingPGtoGoopg`** — new helper for the PG→goopg
  streaming direction (PG's pg_stat_replication + goopg's pg_stat_wal_receiver).
- Gated on `testing.Short()` and `GOOPG_SKIP_M0130_E2E` env var.

## References

- `internal/testport/` — `TestE2E_PhysicalReplication`, `TestE2E_FailoverGoopgToPG`
- `internal/testutil/replcluster/` — harness
- `internal/server/basebackup.go` — BASE_BACKUP
- `internal/server/walreceiver.go` — WalReceiver
- `postgres/local_install/bin/pg_basebackup` — oracle PG client
- `postgres/local_install/bin/pg_ctl` — oracle PG server control

## Addendum 2026-08-10 — SQL-callable replication-slot functions (M-NIGHTLY AI-20260810-011258-003)

The harness above never ran green: its very first statement,
`SELECT pg_create_physical_replication_slot('s10_forward')` on the goopg
primary, failed with `42883 function ... does not exist`.

**The discovery.** goopg could create replication slots only over the
*replication protocol* (`CREATE_REPLICATION_SLOT`, handled by
`internal/server/replication.go: replyCreateReplicationSlot`). Upstream also
exposes the registry as ordinary SQL functions —
`postgres/src/backend/replication/slotfuncs.c`,
`pg_create_physical_replication_slot` (OID 3779) and
`pg_drop_replication_slot` (OID 3780). Both OIDs were already seeded into
goopg's `pg_proc` (`internal/initdb/pg_proc_seed_data.go`), so name resolution
*succeeded* and the call then fell out of the executor's builtin switch — the
catalog advertised a function the executor could not run.

**What landed.**

- `internal/executor/expr_replslot.go` — `evalPgCreatePhysicalReplicationSlot`
  and `evalPgDropReplicationSlot`, dispatched from the builtin switch in
  `expr.go` next to `pg_promote`.
- `Context.ReplSlots *wal.Slots` (`internal/executor/context.go`), wired in
  `internal/server/dispatch.go` from `s.cfg.Slots` — the **same** registry the
  walsender commands mutate. The SQL and wire entry points therefore cannot
  drift (sibling-path rule); the guard proves it by creating over SQL and
  observing the duplicate across a restart.
- SQLSTATE mapping mirrors `replicationSlotErrCode`: duplicate_object 42710,
  undefined_object 42704, object_in_use 55006, invalid_parameter_value 22023,
  and 0A000 when the server has no slot registry.
- Reservation LSN is `WrittenLSN()+1` — the first byte of the *next* record,
  matching `replyCreateReplicationSlot` and the M0094-0005 off-by-one.

**Harness correction.** The test both created the slot via SQL *and* passed
`-C` to `pg_basebackup`, which upstream rejects against an existing slot. The
shared helper is now `runGoopgBasebackupToPGSlot(..., createSlot bool)`;
`TestE2E_PGStandbyFullCycle` passes `createSlot=false`,
`TestE2E_FailoverGoopgToPG` keeps `-C` via the unchanged wrapper.

**Where the harness now stops.** Phase A is green end to end (slot → basebackup
→ PG 18.3 standby boots from the goopg backup → streams → base-backup and
post-backup rows visible), and Phase B replays `CREATE TABLE` / `CREATE INDEX` /
`INSERT`. It fails at the `ALTER TABLE ... ADD COLUMN extra int DEFAULT 0`
check: any subsequent query on that relation *on the PG standby* raises
`could not open relation with OID 2656`. That is PG's `AttrDefaultFetch`
(`relcache.c`) opening `pg_attrdef` by its `adrelid/adnum` index
(`AttrDefaultIndexId` = 2656), which goopg does not materialize. This is a
pre-existing, already-ledgered catalog-completeness gap (see the deferral
ledger's 2026-07-19 `pg_attrdef` rows and the comment block at the end of
`internal/testport/e2e_failover_goopg_to_pg_test.go`), orthogonal to slot
management — the harness stays unchecked until it is closed.

**Deferred here.** `pg_create_physical_replication_slot` returns the
`(slot_name, lsn)` record as its *text* rendering rather than a composite —
goopg has no composite `Datum` kind, so `SELECT * FROM
pg_create_physical_replication_slot(...)` does not expand into two columns.
`temporary => true` raises 0A000 (`wal.Slots` has no session ownership), and
`immediately_reserve => false` still anchors `RestartLSN` (upstream defers
reservation until a walsender attaches; goopg's behaviour retains strictly
less WAL, never more). All three are ledger rows.

**Guard.** `TestPort_SQLPhysicalReplicationSlotFuncs`
(`internal/testport/sql_replication_slot_funcs_test.go`): create → duplicate →
`immediately_reserve` LSN rendering → drop → drop-missing → survives restart.

## Addendum 2026-08-10 (2) — pg_attrdef catalog completeness (M-NIGHTLY AI-20260810-011258-003)

The `could not open relation with OID 2656` blocker described in the previous
addendum is closed. It had **three** independent causes, all in the pg_attrdef
catalog surface goopg hands to a PG 18.3 standby:

1. **Missing indexes.** `pg_attrdef.h:53-54` declares
   `AttrDefaultIndexId` = 2656 `btree(adrelid oid_ops, adnum int2_ops)` and
   `AttrDefaultOidIndexId` = 2657 `btree(oid oid_ops)`. goopg materialized
   neither — no `pg_class`/`pg_index`/`pg_attribute` rows and no files — so
   PG's `AttrDefaultFetch` (`utils/cache/relcache.c`), which opens 2656 with
   **no seq-scan fallback**, FATAL'd on every relcache build of a table with
   `atthasdef`. Fixed by adding both to `nailedLocalRels` (`relcache_init.go`),
   to `pgIndexInitialEntries` (`initdb.go`, indkey `{2,3}` / `{1}`), and to the
   three critical-index placeholder lists that lay down `base/1`, `base/5` and
   `global` btree pages. Key-attribute descriptors auto-derive from the
   pg_attrdef heap attrs via `flattenRels`.
2. **Truncated tupledesc.** `pgAttrdefAttrs()` declared only
   `(oid, adrelid, adnum)` while `PGAttrdefColumnsPG18` had always written four
   columns. pg_attrdef is not `formrdesc`'d, so PG rebuilds its TupleDesc from
   the streamed `pg_attribute` rows for relid 2604 and therefore had no `adbin`
   column to read. Added `adbin` (`pg_node_tree`, OID 194, varlena,
   `BKI_FORCE_NOT_NULL`) and bumped relnatts 3 → 4.
3. **Not mirrored to the `postgres` database.** `writeAttrdefRow` writes to
   `tableCatalogHeapDBOid` (= `base/1` for a connection on postgres/template1),
   but the standby attaches with `dbname=postgres` and reads `base/5`.
   `mirrorTouchedCatalogsToPostgresDB` listed pg_class/pg_attribute/pg_index/…
   but not pg_attrdef, so the standby's scan found zero rows and PG downgraded
   to `WARNING: 1 pg_attrdef record(s) missing for relation "s10_t"` — a
   silently default-less column. 2604/2656/2657 are now in the mirror set.

Runtime index maintenance rides the existing canonical-sys-btree machinery:
`writeAttrdefRow` now calls `insertPgAttrdefAdrelidAdnumIndexEntry` /
`insertPgAttrdefOidIndexEntry` (`sys_catalog_index_insert.go`) with the heap
TID, reusing `buildIndexTupleOidInt2Key` / `buildIndexTupleOidKey` — the
pg_attrdef key shapes are byte-identical to
`pg_attribute_relid_attnum_index` and `pg_class_oid_index`.

**Result.** `TestE2E_PGStandbyFullCycle` Phases A, B and **C** (failover: kill
the goopg primary, `pg_ctl promote`, write and read on the promoted PG) now
pass — Phase C had never executed before. Phase D (reverse attach) fails on a
new, unrelated blocker: the promoted PG rejects
`SELECT pg_create_physical_replication_slot('s10_reverse')` with
`function pg_create_physical_replication_slot(unknown) does not exist`. PG 18's
signature is `(slot_name name, immediately_reserve bool DEFAULT false,
temporary bool DEFAULT false)`; goopg's seeded pg_proc row for OID 3779 does
not carry the `pronargdefaults` / `proargdefaults` that let PG resolve the
1-argument call form. Ledgered; the fix_plan item stays unchecked.

**Guards.** `TestPgAttrdefCatalogSurfaceIsPGComplete` and
`TestPgAttrdefIndexFilesBootstrapped` (`internal/initdb/pg_attrdef_indexes_test.go`)
pin the tupledesc, both index specs, the pg_index seed rows and the on-disk
placeholder files — cheap static checks that fire long before the PG-binary E2E.

## Addendum (2026-08-10) — blocker #4: pg_proc argument DEFAULTs were never seeded

The blocker recorded at the end of the previous addendum is closed.

**Symptom.** Phase D's very first statement,
`SELECT pg_create_physical_replication_slot('s10_reverse')`, executed against
the *promoted PG* (not against goopg), failed with
`function pg_create_physical_replication_slot(unknown) does not exist`.

**Root cause — not where it looks.** The function is present in goopg's seeded
`pg_proc`; what is missing is its *defaults*. Upstream
`postgres/src/include/catalog/pg_proc.dat` has **zero** `pronargdefaults`
entries — the bootstrap catalog format cannot express argument defaults at all.
Upstream instead runs `postgres/src/backend/catalog/system_functions.sql` at
the tail of `initdb`, which `CREATE OR REPLACE FUNCTION`s ~50 built-ins purely
to attach `DEFAULT` clauses. `pg_create_physical_replication_slot` is
system_functions.sql:469, where `immediately_reserve` and `temporary` each get
`DEFAULT false`.

goopg seeds `pg_proc` straight from the generated pg_proc.dat mirror
(`internal/initdb/pg_proc_seed_data.go`) and never replays
system_functions.sql, so every seeded row carried `pronargdefaults = 0` and an
empty `proargdefaults`. That is invisible to goopg itself — its builtin
dispatch resolves call shapes in Go — but on a real PG reading goopg's cluster
directory, `parse_func.c:func_get_detail` builds candidates via
`FuncnameGetCandidates`, which only admits a call with fewer arguments than
`pronargs` when `pronargdefaults` covers the shortfall. Zero defaults ⇒ the
1-argument call form does not exist.

**Fix.** `internal/initdb/pg_proc_seed_defaults.go` is a small table replaying
system_functions.sql's *effect* per OID: `(pronargdefaults, []pgnodes.Node)`.
`pgProcRow` now writes column 18 from it and column 24 from
`pgnodes.OutList(defaults)` — a new export that serializes a **bare List**
(`(elem elem)`, `<>` when NIL), which is the top-level shape `proargdefaults`
holds, as opposed to the single braced node `pg_attrdef.adbin` holds. The
rendered bytes are pinned against a stock PG 18.3 `initdb` capture in
`TestPgProcSeedArgDefaultsMatchesRealPG`; PG runs `stringToNode()` on this
column, so drift in field order, datum width or list punctuation is a standby
ERROR rather than a cosmetic diff.

Only OID 3779 is populated so far — the remaining ~50 system_functions.sql
DEFAULT clauses are ledgered, and adding one is a single table entry.

**Result.** Phase D's slot creation succeeds; the reverse attach now fails one
step later, in `pg_basebackup` against the promoted PG:

    pg_basebackup: error: connection to server ... FATAL: no pg_hba.conf entry
    for replication connection from host "127.0.0.1", user "ryo", no encryption

That is **blocker #5**, and it is a goopg gap for the same reason blocker #4
was: the promoted PG is running on a data directory goopg's `initdb` created,
so it is enforcing *goopg's* `pg_hba.conf`. `buildPgHBAConf`
(`internal/initdb/auth_bootstrap.go`) emits only `all`-database rules and omits
the three `replication` rules upstream `initdb` writes
(`local replication all <local>` + `host replication all 127.0.0.1/32 <host>` +
the `::1/128` twin). goopg ignores pg_hba.conf for its own auth decisions, so
the omission has never mattered until a real PG read the file. Ledgered; the
fix_plan item stays unchecked.

**Guards.** `TestPgProcSeedArgDefaultsMatchesRealPG`,
`TestPgProcSeedArgDefaultsUnlistedOIDsUnchanged` and
`TestPgProcRowCarriesSeedDefaults`
(`internal/initdb/pg_proc_seed_defaults_test.go`) plus `TestOutListBareList`
(`internal/pgnodes/pgnodes_test.go`).
