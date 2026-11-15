# M0149\-0001a1 — one OID per database on disk, in WAL and on display

Status: S1 and S2 landed 2026\-11\-15 \(postgres catalogs stored and
WAL\-logged at DBOid 5; postgres displayed as oid 5\). S3 designed and
probed \(see "S3 design"\); not implemented.
Parent: M0149\-0001a \(template1's PG catalog image\) → M0149\-0001 \(port
`recovery/t/034_create_database.pl`\).

## Why

PG 18.3 identifies a database by one OID everywhere:

- the `pg_database` row;
- the `base/<oid>` directory;
- the `RelFileLocator.dbOid` of every WAL record
  \(`template_pg_database.h`: template1 = 1; initdb's `make_template0` /
  `make_postgres` give template0 = 4 and postgres = 5\).

M0149's C2 contract requires goopg's files and WAL to use the same OIDs. A
PG 18.3 node started on a goopg directory, or replaying goopg WAL, must find
each database where PG would put it. Measured on 034:

- PG cold\-started on a crashed goopg copy cannot open template1:
  `base/1/pg_filenode.map` is missing.
- goopg's `base/1` holds the *postgres* database's catalog relations, not
  template1's.

## goopg's three database\-OID spaces \(design 0119\-0006bv §2, updated\)

| space | keys | template1 | template0 | postgres | `CREATE DATABASE`d |
|---|---|---|---|---|---|
| displayed \(`pg_database.oid`\) | what clients and PG see | 1 | 4 | **16384** \(`databaseDisplayOID`\) | own OID |
| catalog namespace | `catalog.tableNamespace[dbOid]` | **2** \(`Template1NamespaceDBOid`, Option A\) | 4 | **1** \(`DefaultDBOid`; `NamespaceDBOid` folds 5 → 1\) | own OID |
| storage / WAL \(`RelFileNode.DBOid`\) | `base/<oid>`, WAL block refs | **2** | 4 | user relations **5**; catalog relations **1, mirrored to 5** | own OID |

PG's target is the diagonal 1/1/1, 4/4/4, 5/5/5.

Key observation: PG compatibility constrains only the **displayed** and
**storage/WAL** rows.

- The catalog\-namespace key is internal \(an in\-memory map key\) and never
  reaches a file or the wire.
- So this task does not need to remove the namespace sentinel. It needs the
  storage and display mappings to land on PG's OIDs. That shrinks the epic
  substantially: most of the 603 `DefaultDBOid` references are namespace
  keys.

## Census of the storage/WAL and display sites

- **Catalog storage at OID 1.**
  - 23 literal `RelFileNode{DBOid: catalog.DefaultDBOid}` sites: the
    `sys_pg_*` catalog writers \(depend, cast, constraint, operator,
    aggregate, collation, conversion, enum, transform, foreign, am, ts\_\*,
    event\_trigger, publication, opclass/family\), plus operators\_ddl.go,
    initdb.go and catalog\_heap\_reload.go.
  - Each writes a catalog heap under `base/1`.
- **The mirror.** `mirrorCatalogRelToPostgresDB` has 68 call sites. It
  copies every block of a `(1, rel)` catalog file to `(5, rel)` after each
  write, so `base/5` is a complete, current copy of every postgres catalog.
- **Display.** `databaseDisplayOID` returns `"16384"` for postgres \(the
  legacy placeholder kept for CREATE SUBSCRIPTION / `datacl` keys,
  catalog.go:2919 and :6013\). The `pg_database` heap row that PG reads must
  carry the same value.
- **template1.** `Template1NamespaceDBOid = 2` doubles as its storage OID,
  so its relations live at `base/2`.

## Slices

1. **S1 — postgres catalog storage at base/5.**
   - Route every postgres\-namespace catalog relation's `RelFileNode` to
     `PostgresDBOid` through one helper \(`catalog.StorageDBOid(ns)`: 1 → 5,
     2 → 1 after S3, identity otherwise\).
   - Writes go straight to `base/5`, and WAL records name `1663/5/<rel>`.
     The mirror becomes a no\-op and is then deleted.
   - Backward compatible: the mirror kept `base/5` complete, so a cluster
     created before S1 \(including the shipped bench corpora, which the loop
     must not rewrite\) reads its catalogs from `base/5` unchanged.
   - Verification: units; TPC\-H spotcheck and acceptance; sf025 \(old
     corpora read through the new mapping\); pg\_waldump shows `1663/5/1259`
     for a postgres CREATE TABLE; restart and crash recovery tests.
2. **S2 — postgres displayed OID 5.**
   - `databaseDisplayOID` and the `pg_database` heap row use 5.
   - The CREATE SUBSCRIPTION / `datacl` keys that rely on 16384 are
     re\-keyed. Find them by searching for the `16384` / `FirstUserOID`
     coupling at catalog.go:2927.
   - Verification: `SELECT oid FROM pg_database` matches PG; a PG cold start
     connects to `postgres`; the pg\_dump / subscription tests.
3. **S3 — template1 at base/1.**
   - Once S1 frees `base/1`, template1's storage OID becomes 1 \(its
     namespace key may stay 2\). initdb writes template1's full bootstrap
     image there, including `pg_filenode.map`, which is M0149\-0001a itself.
   - Existing clusters need a startup migration: if `base/2` exists and
     `base/1` holds the pre\-S1 postgres catalog copy, replace `base/1` with
     `base/2`'s contents.
   - Verification: 034 mixed A1 = 1; the template1 isolation tests
     \(0119\-0006bv\); CREATE DATABASE … TEMPLATE template1.
4. **S4 \(optional, later\) — catalog namespace keys equal storage OIDs.**
   This retires `NamespaceDBOid`'s 5 → 1 fold. It is internal only and not
   needed for C2.

## S1 implementation findings \(2026\-11\-15, second loop\)

- Measured on a fresh cluster: `create table s1t (a int primary key, b
  text); insert …; checkpoint` in `postgres` emits about 40 WAL block
  references under DBOid **1** and 41 under DBOid **5**.
  - The DBOid\-1 references are the primary catalog writes: `pg_attribute`
    1249 \(12\), `pg_class` 1259 and its indexes 2659 / 2662 / 2663,
    `pg_index` 2610, `pg_constraint` 2606, and the `pg_depend` indexes
    2678 / 2679.
  - The DBOid\-5 references are the mirror's copies \(mostly `XLOG FPI`\).
  - PG 18.3 replaying this stream would write postgres's catalog rows into
    its own template1 \(`base/1`\). C2 therefore needs S1, not just S3.
- The writers are not only the 23 literal sites. `RelFileNode{…}` is built
  with a computed DBOid in 193 non\-test places: `cat.DBOID()` 29,
  `heapDBOid` 16, `dbOid` 14, `tableCatalogHeapDBOid(ctx)` 5, `key.DBOid`
  5, …
  - A source\-level rewrite is error\-prone.
- Chosen approach: one storage\-boundary normalization, `storage.StorageDBOid`
  \(1 → 5\), applied at every chokepoint **in a single commit**:
  1. the buffer tag at every `Pool` method taking a `RelFileNode` \(Pin,
     NBlocks, CachedNBlocks, Exists, CreateFile, RelPath, truncate / drop\),
     so `(1, rel)` and `(5, rel)` share one buffer;
  2. the smgr `Manager` path computation, for callers that bypass the Pool;
  3. the WAL block\-reference encoders \(`pg_assembled_emit.go` and the
     `recovery.go` record encoders that write `rel.DBOid`\), so new WAL names
     5;
  4. `mirrorCatalogRelToPostgresDB` returns early when the normalized source
     equals the destination, which is always the case after 1–2.
- Replay of a pre\-S1 segment decodes DBOid 1 and is normalized by \(1\) to
  base/5, which is what the mirror kept current.
- Applying only some of the chokepoints is unsafe: two buffers for one
  file means incoherence. Hence the single\-commit rule.
- Verification for the S1 commit: units; crash and restart tests;
  pg\_waldump on the same probe shows no `1663/1/…` block reference;
  spotcheck, sf025 and acceptance on old corpora; replay of a pre\-S1 WAL
  segment.

## S1 as landed \(2026\-11\-15, third loop\)

- `storage.StorageDBOid` \(1 → 5\) and `RelFileNode.Canonical()` are applied
  at every `Pool` / smgr entry point, in the VM and FSM map keys, in the heap
  extension lock key, and in the WAL block\-reference and main\-data
  encoders.
- The mirror returns early once the source and destination normalize to the
  same OID.
- **The mirror did not keep `base/5` complete.** This contradicts the
  assumption under "Risks". On the shipped SF0.25 corpus, 13 files differed.
  For example, `pg_statistic` \(2619\) was 688 KB in `base/1` but one empty
  page in `base/5`. With routing alone, sf025 values passed, but the fire\-set
  plan parity dropped from 99/99 to 2/99.
- Fix: a one\-time startup migration, `migratePostgresCatalogsToBase5`
  \(internal/initdb/layout\_postgres\_base5.go\).
  - It runs in `initdb.Open` before the storage Manager opens and before
    WAL redo.
  - For each relation file in `base/1`, it copies the file over `base/5`
    when `base/1`'s maximum page LSN is higher. It also copies when `base/5`
    lacks the file and the `base/1` file is non\-empty.
  - Each copy goes through a temp file, fsync and rename.
  - When done, it records `postgres_catalogs_at_base5` in
    `global/pg_goopg_features`.
  - Redo then replays any later record on top. A pre\-S1 record naming
    DBOid 1 is mapped to `base/5`.
- Measured:
  - A fresh probe running a postgres CREATE TABLE, INSERT and CHECKPOINT
    emits 70 block refs at `1663/5/…` and zero at `1663/1/…`.
  - A cluster and WAL written by the pre\-S1 binary replay correctly under
    S1.
- Gates:
  - units, postmaster, and tpch\-spotcheck PASS;
  - fire\-set fires=none on all three corpora, with parity 99/99, 99/99 and
    22/22;
  - sf025 99 PASS, with plans identical to the pre\-S1 baseline;
  - acceptance PASS;
  - the PG\-interop e2e tests PASS: FailoverGoopgToPG, PGStandbyFullCycle,
    PGCrashStartOnGoopgDataDir and PGStandbyFollowsGoopgTimelineSwitch;
  - the TAP ports Recovery001, 013, 019, 038, 039 and 047 PASS.
- A side effect of the sf025 sweep: the shipped SF0.25 corpus is now
  migrated. The migration is idempotent and only copies newer data.

## S2 as landed \(2026\-11\-15, fourth loop\)

- `databaseDisplayOID("postgres")` returns `PostgresDBOid` \(5\). The
  `pg_database` heap row already carried 5 \(`detectCatalogDBOID` reads it),
  so only the virtual display lied.
- The consumers that had copied the 16384 placeholder now use
  `catalog.PostgresDBOid`:
  - `pg_subscription.subdbid`, from both producers \(initdb
    replication\_views.go and postmaster dispatch.go\);
  - the `pg_db_role_setting.setdatabase` key, from ALTER DATABASE … SET,
    ALTER ROLE … IN DATABASE … SET and the virtual rows;
  - `pg_locks.database`, in both the static relation row and the advisory
    rows.
- Legacy clusters. `reloadDbRoleSettingsFromHeap` folds a heap row keyed
  16384 into 5.
  - Before S2, only the live postgres database could be written, so 16384
    always meant postgres.
  - `SyncDbRoleSettingRow` deletes the legacy \(16384, role\) row whenever
    it writes \(5, role\).
  - Until that first write, the on\-disk row still says 16384. A PG 18.3 node
    reading the heap would not apply it to postgres. That is acceptable
    because goopg itself never applies these settings at session start
    \(filed as M0122\-0008a\).
- Measured:
  - On a cluster created and configured by the pre\-S1 binary, the new
    binary shows `pg_database` 1/4/5 and `pg_db_role_setting` rows keyed 5.
  - A new ALTER DATABASE … SET rewrites the row under 5, and the result
    survives a restart.
- Gates PASS:
  - units; the catalog, postmaster, initdb and executor packages;
  - testport pg\_dump, subscription, pgoutput interop, PG cold start and
    Recovery: 56 tests;
  - spotcheck; sf025 99; acceptance.
- Not changed: `operators_ddl.go` LOCK TABLE keeps a 16384 fallback, which is
  only used when the catalog is not an `InMemory`.

## S3 design \(2026\-11\-15, fifth loop\) — template1 at base/1

### Why the S1 mapping cannot be extended

- The obvious S3 is to add `2 → 1` to `storage.StorageDBOid`, routing
  template1's namespace 2 to `base/1`. That breaks idempotency.
  - The normalization runs at the Pool, which sees both namespace OIDs
    \(from executor writers\) and storage OIDs \(from WAL redo\).
  - Once template1 is stored at 1, a redo record naming DBOid 1 means
    template1. The Pool cannot tell it apart from the postgres namespace 1,
    which must go to 5.
  - Replaying goopg's own post\-S3 WAL would write template1 blocks into
    postgres's `base/5`.
- The same conflict already exists today for PG\-sourced WAL. A PG 18.3
  primary's template1 writes \(autovacuum of template1, for example\) arrive
  at a goopg standby as `1663/1/<rel>`, and S1's `1 → 5` writes them into
  postgres's catalog files.
  - Before S1 they landed in `base/1`, which was then goopg's primary copy of
    the postgres catalogs.
  - Either way this is pre\-existing standby corruption in the mixed
    PG\-primary topology. S3 below removes it.

### Chosen design: Option C — namespace OIDs equal storage OIDs

- Set `catalog.DefaultDBOid = 5` and `catalog.Template1NamespaceDBOid = 1`,
  and make `storage.StorageDBOid` the identity. Namespace, storage, WAL and
  display then coincide: 1/1/1, 4/4/4, 5/5/5.
  - `NamespaceDBOid`'s `5 → 1` fold becomes the identity.
  - The `base/1 → base/5` mirror and `PgDatabaseRowOid`'s template1 special
    case become no\-ops and can be deleted.
  - This is slice S4, folded into S3, because it is the only design in which
    WAL decoding needs no namespace inverse.
- Probe \(worktree at d556cbbd7; diff kept as
  `tmp/m0149-s3-optionC-probe.diff`, 49 lines\):
  - Only one source fix was needed: a duplicate `case` in
    `CreatePerDatabaseScaffolding`, where `DefaultDBOid` and `PostgresDBOid`
    are now equal. Template1 now gets the image copy.
  - Unit packages: storage, access/\*, executor and postmaster PASS.
  - catalog fails 2 tests, `Test{Index,Table}RealPagesInUsesCatalogDatabase`.
    They are pinned to the old 1\-vs\-5 divergence and report "the case no
    longer reproduces"; retire them.
  - initdb fails 2 tests, close\_checkpoint and recovery. They build a
    literal `DBOid: 1` RelFileNode and expect `base/5`; re\-key them to 5.
  - Fresh cluster: `base/{1,4,5}` with `pg_filenode.map` in each, and
    `pg_database` shows 1/4/5. A table created in template1 is invisible
    from postgres and the reverse, and both survive a restart.
  - **PG 18.3 cold start on the goopg directory connects to template1 and
    to postgres and reads goopg\-created tables in both**. At HEAD, PG cannot
    open template1 at all \(no `base/1/pg_filenode.map`\). This is
    M0149\-0001a's verification.
  - PG\-side `CREATE DATABASE` then fails with "could not open relation with
    OID 1214". goopg's per\-database `pg_class` heap lacks 19 shared\-catalog
    rows: `pg_shdepend` and its 2 indexes, `pg_shdescription` and its
    index, and 7 shared TOAST tables with their indexes \(2846/2847,
    2966/2967, 4060/4061, 4177/4178, 4183/4184, 4185/4186, 6244/6245\).
    Filed as M0149\-0001c.

### Existing clusters: migration and legacy WAL

- Layout before S3: postgres at `base/5`; template1 at `base/2`; `base/1`
  holding a stale pre\-S1 postgres copy. The pre\-S1 corpora `:65433` and
  `:65436` still keep their primary postgres catalogs in `base/1`; S1's
  migration handles those first.
- Open\(\), in order:
  1. S1 `migratePostgresCatalogsToBase5` \(unchanged; it must run first\).
  2. When `template1_at_base1` is absent from `global/pg_goopg_features`
     and `base/2` exists, replace `base/1` with `base/2`'s files \(temp,
     fsync, rename\). Re\-running this after a crash is safe, because redo
     restarts from the same checkpoint on the same `base/2` image.
  3. Redo. If step 2 ran during this startup, every record replayed in
     this redo pass predates S3. Decoding maps the legacy DBOids, applied
     only to records from this pass: storage 1 → 5 \(pre\-S1 postgres
     catalogs\), 2 → 1 \(template1\), identity otherwise.
  4. After the end\-of\-recovery checkpoint, record `template1_at_base1`.
     A crash before that point repeats steps 2–3.
- The legacy map is applied at the WAL decode sites:
  - the block\-reference RelFileLocator \(`pg_xlog_decode.go`\);
  - the main\-data locators: smgr create/truncate in recovery.go; xact
    dropped rels in `pg_xact_parse.go`; `seq_log.go`; the 22 goopg\-private
    `payload[1:5]` decoders in recovery.go; the relmap\-update dbid
    \(`relmap.go`\).
  - Use one helper, `xlog.legacyRedoDBOid`, which is identity unless the
    legacy pass is active.
- Not covered: a goopg standby streaming WAL from an older goopg primary.
  Mixed\-version goopg replication is unsupported; ledger it.

### S3 verification

- units; initdb two\-restart tests; the template1 namespace tests
  \(0119\-0006bv\); CREATE DATABASE … TEMPLATE template1.
- A cluster built by the S2 binary, killed with an unclean WAL tail that
  touches both template1 and postgres, then opened by S3: the data is
  correct in both databases. Run it twice: once killed during the S3 redo,
  once not.
- A fresh cluster under PG 18.3 cold start: template1 and postgres
  readable.
- PG\-interop e2e and the Recovery ports.
- spotcheck, sf025 \(the shipped SF0.25 corpus migrates; idempotent\),
  acceptance, fire\-set.

## Risks

- The shipped corpora \(`:65433`, `:65436`, `:65437`\) were built with the
  old layout and are read\-only for the loop.
  - S1 must read them without migration. The mirror guarantee above is what
    makes that true, and must be verified on a clone.
  - S3's migration must never run against them. The gates use private
    clones.
- WAL replay of a pre\-S1 WAL stream names `1663/1/<rel>` for catalog
  writes.
  - Replay after the upgrade must keep both copies coherent, or must map
    `1/<catalog rel>` → `5/<catalog rel>` for the postgres namespace while a
    pre\-S1 segment is replayed.
  - Simplest rule: replay a `DBOid = 1` catalog block to both 1 and 5, as
    the mirror did, until S3 repurposes base/1.
