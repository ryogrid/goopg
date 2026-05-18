# 08 — Relcache Init Files and PG_VERSION

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); supersedes
[`docs/design/0106-0001-relcache-init-file-format.md`](../0106-0001-relcache-init-file-format.md).

---

## Scope

The two startup sidecar artefacts PG reads before catcache is usable:

- `global/pg_internal.init` — cached `RelationData` + `Form_pg_class`
  + `Form_pg_attribute` for the five nailed shared rels and six
  critical shared indexes.
- `base/<dboid>/pg_internal.init` — same, for the four nailed-local
  rels and seven critical local indexes, per database.
- `PG_VERSION` flat files at `$PGDATA/` and every `base/<dboid>/`,
  payload `"18\n"`.

In scope: `RELCACHE_INIT_FILEMAGIC` and the per-record framing;
the per-file inventory enforced by `load_relcache_init_file`'s
trailing-count check; PG's runtime `unlink()`-on-invalidate
contract; the `PG_VERSION` literal and `ValidatePgVersion` read.

Out of scope: heap-row bytes in pg_class/pg_attribute/pg_proc/pg_type
(see [`04-`](04-shared-catalog-bootstrap.md),
[`05-`](05-local-catalog-bootstrap.md)); `Form_pg_class`/
`Form_pg_attribute` column tables (also 04/05). This file documents
the *cached* representation; a change to either column inventory in
04/05 mandates a matching change here.

---

## Upstream references

`src/...` paths are relative to `postgres/`; lines vs PG 18.3.

| Symbol | File:line |
|---|---|
| `RELCACHE_INIT_FILEMAGIC = 0x573266` | `src/backend/utils/cache/relcache.c:93` |
| `RELCACHE_INIT_FILENAME = "pg_internal.init"` | `src/include/utils/relcache.h:25` |
| `NUM_CRITICAL_SHARED_RELS = 5` | `relcache.c:4086` |
| `NUM_CRITICAL_LOCAL_RELS = 4` | `relcache.c:4143` |
| `NUM_CRITICAL_LOCAL_INDEXES = 7` | `relcache.c:4194` |
| `NUM_CRITICAL_SHARED_INDEXES = 6` | `relcache.c:4226` |
| `formrdesc` shared block | `relcache.c:4075-4086` |
| `formrdesc` local block + `load_critical_index` local | `relcache.c:4134-4194` |
| `load_critical_index` shared | `relcache.c:4213-4226` |
| `load_relcache_init_file` reader | `relcache.c:6147-6578` (magic `:6201-6205`; size checks `:6225, :6266, :6284`; count check `:6510-6547`) |
| `write_relcache_init_file` writer | `relcache.c:6584-6793` (heap loop `:6680-6696`; index sub-record `:6702-6744`; temp-rename `:6783`) |
| `RelationCacheInitFilePreInvalidate` (unlink) | `relcache.c:6859-6882` |
| `RelationCacheInitFilePostInvalidate` | `relcache.c:6884-6888` |
| `RelationCacheInitFileRemove` (postmaster) | `relcache.c:6899-6929` |
| `RelationIdIsInInitFile` | `relcache.c:6819-6835` |
| `RegisterRelcacheInvalidation` sets the flag | `src/backend/utils/cache/inval.c:650-651` |
| `AtEOXact_Inval` Pre/Post sandwich | `inval.c:1219, 1228` |
| `ProcessCommittedInvalidationMessages` (redo) | `inval.c:1145-1171` |
| `RelationMapUpdateMap` (mapped relfilenode bump) | `src/backend/utils/cache/relmapper.c:325` |
| `XLOG_RELMAP_UPDATE = 0x00` | `src/include/utils/relmapper.h:25` |
| `write_version_file` | `src/bin/initdb/initdb.c:1023-1040`; called `:3087, :3102` |
| `PG_MAJORVERSION "18"` | `src/include/pg_config.h:605` |
| `ValidatePgVersion` | `src/backend/utils/init/miscinit.c:1769-1825`; per-DB callsite `src/backend/utils/init/postinit.c:1164` |

---

## Initdb-time output

### File paths

| File | Producer at initdb | Removed by postmaster at startup? |
|---|---|---|
| `$PGDATA/global/pg_internal.init` | first backend after Phase 2 `formrdesc`s | yes — `RelationCacheInitFileRemove` (`relcache.c:6899`) |
| `$PGDATA/base/<dboid>/pg_internal.init` | first backend per DB after Phase 3 `formrdesc`s | yes — `RelationCacheInitFileRemoveInDir("base")` (`:6912`) |
| `$PGDATA/PG_VERSION` | `initdb.c:3087 → write_version_file(NULL)` | no |
| `$PGDATA/base/<dboid>/PG_VERSION` | `initdb.c:3102 → write_version_file("base/1")` for template1; clone-via-`pg_database` for others | no |

DB OIDs in goopg today: `1` (template1, `catalog.DefaultDBOid` —
`internal/initdb/initdb.go:170`); `5` (postgres — `:916`). `4`
(template0) is not created. Both `pg_internal.init` files are
removed at every postmaster start because PITR or crash recovery may
replay catalog changes that occurred after the prior file was
written (`relcache.c:6890-6898` header comment).

### Binary layout

After the 4-byte magic header the file is a sequence of per-relation
records (writer `relcache.c:6680-6744`, reader `:6207-6508`):

```
magic        uint32             = RELCACHE_INIT_FILEMAGIC (0x573266)

for each relation:
  relDescLen uint32             = sizeof(RelationData)        (reader asserts == :6225)
  relDesc    bytes[relDescLen]  = RelationData
  relFormLen uint32             = CLASS_TUPLE_SIZE
  relForm    bytes[relFormLen]  = FormData_pg_class
  for i in 0..relnatts-1:
    attrLen  uint32             = ATTRIBUTE_FIXED_PART_SIZE   (reader asserts == :6266)
    attr     bytes[attrLen]     = FormData_pg_attribute       (first 20 cols)
  optLen     uint32             = VARSIZE(rd_options) or 0
  options    bytes[optLen]      = reloptions varlena
  if relkind == RELKIND_INDEX:                                 # :6702-6744
    indexTupleLen   uint32      = HEAPTUPLESIZE + rd_indextuple->t_len
    indexTuple      bytes       = pg_index HeapTuple (header + body)
    opfamilyLen     uint32      = relnatts*sizeof(Oid);   opfamily      bytes
    opcintypeLen    uint32      = relnatts*sizeof(Oid);   opcintype     bytes
    supportLen      uint32      = relnatts*amsupport*sizeof(RegProcedure); support bytes
    indcollationLen uint32      = relnatts*sizeof(Oid);   indcollation  bytes
    indoptionLen    uint32      = relnatts*sizeof(int16); indoption     bytes
    for i in 0..relnatts-1:
      opcoptLen     uint32      = VARSIZE(rd_opcoptions[i]) or 0
      opcoptions    bytes
```

The reader rejects via `goto read_failed` on any short read, on any
length mismatch above, on `len != VARSIZE(rd_options)` (`:6284`),
and on a trailing-count mismatch (`:6510-6547`). The shared-file
count mismatch additionally fires `Assert(false)` (`:6531`) so an
assertion build crashes loudly.

### `global/pg_internal.init` — 5 rels + 6 indexes

| Kind | OID | Name | Citation |
|---|---|---|---|
| heap | 1262 | pg_database | `relcache.c:4075` |
| heap | 1260 | pg_authid | `relcache.c:4077` |
| heap | 1261 | pg_auth_members | `relcache.c:4079` |
| heap | 3592 | pg_shseclabel | `relcache.c:4081` |
| heap | 6100 | pg_subscription | `relcache.c:4083` |
| index | 2671 | pg_database_datname_index | `relcache.c:4213` |
| index | 2672 | pg_database_oid_index | `relcache.c:4215` |
| index | 2676 | pg_authid_rolname_index | `relcache.c:4217` |
| index | 2677 | pg_authid_oid_index | `relcache.c:4219` |
| index | 2695 | pg_auth_members_member_role_index | `relcache.c:4221` |
| index | 3593 | pg_shseclabel_object_index | `relcache.c:4223` |

Index 2694 (`pg_auth_members_role_member_index`) is *not* on the
critical list; it may legitimately appear in the file (it passes
`RelationIdIsInInitFile`'s syscache check) but does not count
against the six.

### `base/<dboid>/pg_internal.init` — 4 rels + 7 indexes

| Kind | OID | Name | Citation |
|---|---|---|---|
| heap | 1259 | pg_class | `relcache.c:4134` |
| heap | 1249 | pg_attribute | `relcache.c:4136` |
| heap | 1255 | pg_proc | `relcache.c:4138` |
| heap | 1247 | pg_type | `relcache.c:4140` |
| index | 2662 | pg_class_oid_index | `relcache.c:4179` |
| index | 2659 | pg_attribute_relid_attnum_index | `relcache.c:4181` |
| index | 2679 | pg_index_indexrelid_index | `relcache.c:4183` |
| index | 2687 | pg_opclass_oid_index | `relcache.c:4185` |
| index | 2655 | pg_amproc_fam_proc_index | `relcache.c:4187` |
| index | 2693 | pg_rewrite_rel_rulename_index | `relcache.c:4189` |
| index | 2701 | pg_trigger_tgrelid_tgname_index | `relcache.c:4191` |

`pg_trigger` (2620) is intentionally *not* among the four nailed
heaps even though its critical index 2701 *is* — see
[`05-`](05-local-catalog-bootstrap.md) for the rationale. The
loader only validates the index count, not pg_trigger's heap
presence.

### `PG_VERSION`

ASCII decimal major version + LF. PG18 → three bytes
`0x31 0x38 0x0A` (`"18\n"`). `ValidatePgVersion` parses with
`strtol` (`miscinit.c:1781, :1802`) and FATALs on `my_major !=
file_major` (`:1815-1819`). A missing per-DB file FATALs *before*
any relcache work — `"is not a valid data directory"`
(`miscinit.c:1789`), surfaced at `postinit.c:1164` after
`MyDatabaseId` is resolved.

Vanilla initdb writes `$PGDATA/PG_VERSION` (`initdb.c:3087`) and
`base/1/PG_VERSION` (`:3102`); `base/4/` and `base/5/` inherit their
copies via the `pg_database`-clone step at `initdb.c:2811-2820`.

---

## Continuous maintenance

`pg_internal.init` is cache, not source of truth. PG's contract is
*unlink before any backend can read stale*; the next backend
regenerates via `formrdesc` + `load_critical_index` +
`write_relcache_init_file`. No in-place rewrite exists. goopg must
mirror this contract for every DDL that mutates a nailed rel.

### PG invalidation pipeline

1. DDL emits a relcache inval. `RegisterRelcacheInvalidation`
   (`inval.c:650-651`):

   ```c
   if (relId == InvalidOid || RelationIdIsInInitFile(relId))
       info->RelcacheInitFileInval = true;
   ```

2. At commit, `AtEOXact_Inval(isCommit=true)` runs
   (`inval.c:1219-1229`):
   `RelationCacheInitFilePreInvalidate` → SI messages →
   `RelationCacheInitFilePostInvalidate`.

3. `…PreInvalidate` (`relcache.c:6859-6882`) acquires
   `RelCacheInitLock` exclusively and `unlink()`s **both**
   `<DatabasePath>/pg_internal.init` and `global/pg_internal.init`
   (`:6879-6881`). The shared file is always unlinked alongside a
   per-DB invalidation.

4. On standby, `ProcessCommittedInvalidationMessages`
   (`inval.c:1145-1171`) performs the same Pre/Post sandwich
   during WAL replay when the commit record carries
   `RelcacheInitFileInval=true`.

5. Next backend reads `pg_internal.init`, finds it absent, falls
   back to `formrdesc`/`load_critical_index`, rewrites via
   temp-file + rename (`relcache.c:6609-6622, 6783`). A racing
   inval after the data scan deletes the just-written temp file
   (`:6772-6789`).

`write_relcache_init_file` aborts if any inval already arrived
during this backend's life (`relcache.c:6599`).

### Per-DDL invalidation table

"Both" = `global/pg_internal.init` *and*
`base/<dboid>/pg_internal.init` — `…PreInvalidate` always unlinks
both (`relcache.c:6879-6881`).

| Operation | Nailed rel touched | Init file unlinked |
|---|---|---|
| `CREATE TABLE` / `CREATE INDEX` / `DROP TABLE` / `DROP INDEX` on a user rel | none — pg_class is read-modified-write but the *invalidation* targets the new/dropped relation OID, not 1259 | none |
| `CREATE FUNCTION` / `CREATE TYPE` / `CREATE TRIGGER` (user objects) | none — inval is by new-tuple OID, not by catalog | none |
| `ALTER TABLE pg_class …` (system-catalog DDL, normally blocked via `allowSystemTableMods`) | pg_class OID 1259 → in init file | both |
| `VACUUM FULL pg_class` / `CLUSTER pg_attribute USING …` / `TRUNCATE pg_xxx` | relfilenode bump on mapped catalog → `RelationMapUpdateMap` + SI relcache inval on the mapped rel | both |
| `REINDEX INDEX pg_class_oid_index` (mapped critical index) | relfilenode bump on 2662 → inval on parent 1259 | both |
| `REINDEX INDEX pg_database_oid_index` (mapped critical shared index) | inval on 1262 | both |
| `VACUUM` / `ANALYZE` on a nailed rel | inplace pg_class update via `PreInplace_Inval` / `AtInplace_Inval` (`inval.c:1250-1276`) — flag set on init-file rels | both |
| postmaster startup | n/a (forced; PITR/crash safety) | both — `RelationCacheInitFileRemove` walks every `base/N/` |

User-level DDL therefore does **not** unlink. The trigger is
mutation of a *nailed-rel descriptor* itself: a heap row in one of
9 catalogs (1259/1249/1255/1247/1262/1260/1261/3592/6100), or a
relfilenode bump in one of the 13 critical indexes.

### goopg call sites that must issue the unlink

Every PG-canonical mutation of a nailed-rel tuple inside
`internal/executor/` must, in the same transaction:

1. Call `internal/catalog.RelcacheInitFileUnlink(dataDir, dboid)`
   removing both files (ENOENT-safe), mirroring
   `relcache.c:6879-6881`.
2. Emit the commit-record invalidation array with
   `RelcacheInitFileInval=true` so the standby's
   `ProcessCommittedInvalidationMessages` performs the same unlink
   during redo.
3. Serialise unlink + SI-send via a `RelCacheInitLock`-equivalent.

Today goopg does none of this. The init files are written at
initdb time and `chmod 0o400`
(`internal/initdb/relcache_init.go:1465, :49`) to prevent PG-side
rewrites. That is a temporary measure for the M0106 single-attach
happy path only; the moment a goopg-emitted `XLOG_HEAP_UPDATE`
against pg_class (e.g. planner stats) replays on a standby with
init-file cache intact, the standby's catcache serves stale rows
derived from the cached `Form_pg_class`.

### Relmap dependency

Every nailed local catalog and every critical index is *mapped*
(`pg_class.relfilenode = 0`; real RelFileNumber in
`global/pg_filenode.map` or `base/<dboid>/pg_filenode.map` —
`internal/initdb/initdb.go:131, :937-998`). `REINDEX` on a critical
index goes through `RelationMapUpdateMap` (`relmapper.c:325`),
which emits `XLOG_RELMAP_UPDATE` and queues a relcache inval on the
mapped-rel parent. That inval then sets `RelcacheInitFileInval` via
the path above.

---

## What goopg must produce

### File-creation status

| Artefact | goopg site | Status | Gap |
|---|---|---|---|
| `global/pg_internal.init` | `bootstrapRelcacheInitFiles → writeRelcacheInitFile(shared=true)` (`internal/initdb/relcache_init.go:31, :1406`) | **partial** | (1) Writes ~38 records (5 heaps + ~33 indexes from `nailedSharedRels` after `flattenRels`), not exactly 5+6. The trailing-count check (`relcache.c:6524-6534`) will fail → `goto read_failed` + `Assert(false)` on assertion builds. (2) `writeRelcacheInitFile` emits `optLen=0` after each record and stops — it never writes the index sub-record (pg_index tuple, opfamily, opcintype, support, indcollation, indoption, opcoptions). The reader interprets the next record's `relDescLen` as the supposed pg_index tuple length and `goto read_failed`. Backends therefore *always* fall back to `formrdesc`/`load_critical_index` today; the file is effectively decorative. |
| `base/1/pg_internal.init` | `writeRelcacheInitFile(shared=false)` (`relcache_init.go:35`) | **partial** | Same two structural gaps. `nailedLocalRels` after `flattenRels` is ~60+ heaps and ~150+ indexes — far past 4+7. |
| `base/5/pg_internal.init` | byte-copy of `base/1` then `chmod 0o400` (`relcache_init.go:39-51`) | **partial** | Inherits both gaps. The 0o400 mode blocks PG's rewrite via `relcache.c:6624` (`AllocateFile` EACCES on the temp file), masking the regeneration path. |
| `base/4/pg_internal.init` | none | **missing** | `base/4/` not created. Latent — PG18 rejects connections to template0 via `datallowconn=false` (`postinit.c::CheckMyDatabase`). |
| `$PGDATA/PG_VERSION` | `SampleFiles()[0]` `"18\n"` (`initdb.go:127`) | **done** | Mode 0o600, payload matches `PG_MAJORVERSION + "\n"`. |
| `base/1/PG_VERSION` | none — only `$PGDATA/` and `base/5/` get one | **missing** | A `psql -d template1` FATALs in `ValidatePgVersion("$PGDATA/base/1")` (`postinit.c:1164` → `miscinit.c:1789`). Masked today by always connecting to OID 5. |
| `base/5/PG_VERSION` | `os.WriteFile(dbDir/PG_VERSION, "18\n", 0o600)` (`initdb.go:920`) | **done** | Matches PG. |
| `base/4/PG_VERSION` | none | **missing** | Same masking as `base/4/pg_internal.init`. |

### Runtime invalidation status

| Behaviour | goopg implementation | Status | Gap |
|---|---|---|---|
| Unlink both init files on commit of a transaction that invalidated an init-file rel | none | **missing** | No `os.Remove` call exists; the 0o400 chmod means a PG-side rewrite-after-fallback hits EACCES at `relcache.c:6624` and silently continues without a new file. |
| Emit `RelcacheInitFileInval=true` in commit-record inval array | none | **missing** | `internal/wal/` does not yet support invalidation message arrays. |
| `RelCacheInitLock`-equivalent | none | **missing** | No counterpart serialisation. |
| Mapped-rel relfilenode bump → init-file unlink | none | **missing** | `RelationMapUpdateMap` path not yet wired (see 05's *VACUUM FULL* / *REINDEX* gaps). |

### Recommended Go entry points

Two additions under `internal/catalog/`:

1. `func RelcacheInitFileUnlink(dataDir string, dboid catalog.OID)
   error` — `os.Remove("global/pg_internal.init")` and
   `os.Remove("base/<dboid>/pg_internal.init")`, both ENOENT-safe.
2. `func WithRelCacheInitLock(fn func() error) error` — per-process
   serialiser for every unlink-or-rewrite.

Every nailed-rel DDL handler (`internal/executor/operators_ddl.go`,
the future `internal/catalog/relmapper.go`, the inplace pg_class
update path in `internal/executor/operators_vacuum.go`) must funnel
through `WithRelCacheInitLock` → `RelcacheInitFileUnlink` → SI-send
→ commit. Standby-side WAL redo in `internal/wal/recovery.go` must
unlink before SI delivery on any commit record carrying the inval
flag.

### Supersession of `0106-0001-relcache-init-file-format.md`

That earlier doc predates the M0106-0010 step-3a..3dm reactive
seeding chain and the DWARF struct-size verification work captured
in `relcache_init.go:18-23`. It identified the magic constant and
the broad layout but listed 9 critical local rels (conflating
nailed-rel and load-critical-index sets) and omitted the index
sub-record entirely. This file supersedes it; update that doc's
front-matter to point here once landed.

---

## Verification

1. **Magic-header byte check.** Both files must start with
   `66 32 57 00` (little-endian `0x573266`):

   ```bash
   xxd "$GOOPGDATA/global/pg_internal.init" | head -1
   xxd "$GOOPGDATA/base/1/pg_internal.init" | head -1
   ```

   `cmp` against a vanilla initdb is *not* meaningful past the
   first record — `system_identifier`, mapped-rel
   `rd_node.relNumber`, and pg_index tuple `t_xmin`/`t_xmax` differ
   across runs.

2. **Trailing-count smoke test.** Launch a vanilla PG18 backend
   against `$GOOPGDATA` with assertions enabled and grep the
   postmaster log for

   ```
   found N nailed shared rels and M nailed shared indexes in init file,
       but expected 5 and 6 respectively
   ```

   (`relcache.c:6527-6529`). Today goopg trips this on every
   start. Success = absence of this WARNING and no `Assert(false)`
   PANIC.

3. **Backend acceptance.** Real PG18 against goopg-output PGDATA:

   ```bash
   postgres -D "$GOOPGDATA" -p 5535 &
   psql -p 5535 -d postgres -c '\d pg_class' </dev/null
   ```

   `\d` exercises every nailed-rel/critical-index syscache
   (RELOID, RELNAMENSP, ATTNUM, ATTNAME, TYPEOID, INDEXRELID).
   M0106 panic chain `could not open critical system index` must
   not appear.

4. **Invalidation round-trip.** A simulated nailed-rel DDL via
   `internal/catalog/inval_test.go` must leave both init files
   absent before the next backend opens `$GOOPGDATA`:

   ```bash
   test -f "$GOOPGDATA/global/pg_internal.init" && echo FAIL || echo OK
   test -f "$GOOPGDATA/base/5/pg_internal.init" && echo FAIL || echo OK
   ```

5. **`PG_VERSION` parity.**

   ```bash
   diff <(printf '18\n') "$GOOPGDATA/PG_VERSION"
   diff <(printf '18\n') "$GOOPGDATA/base/5/PG_VERSION"
   test -f "$GOOPGDATA/base/1/PG_VERSION" || echo "missing base/1/PG_VERSION"
   ```

   The third command prints `missing` today — that is the next
   FATAL chain a `psql -d template1` against goopg would hit.

6. **E2E gate.** `TestE2E_FailoverGoopgToPG/async` is the
   integration sentinel: the standby's
   `RelationCacheInitializePhase2/3` must return `true` from
   `load_relcache_init_file` on first attach, and observe an
   unlinked init file after any goopg-side DDL touching one of
   the 9 nailed OIDs or 13 critical indexes. A new
   `TestE2E_NailedDDLInvalidatesInitFile` covers the second half.
