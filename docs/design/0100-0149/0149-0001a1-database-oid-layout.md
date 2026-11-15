# M0149\-0001a1 — one OID per database on disk, in WAL and on display

Status: census and slice plan, 2026\-11\-15. No code changed yet.
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
