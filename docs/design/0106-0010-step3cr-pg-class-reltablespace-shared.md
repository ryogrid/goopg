# M0106-0010 Step 3cr — pg_class.reltablespace for shared catalogs

## Blocker

After Step 3cq (`bootstrapPgTypeTuples`) let PG-standby user backends past
`InitPostgres`, the next FATAL surfaced from the autovacuum-launcher-
equivalent backend on `TestE2E_FailoverGoopgToPG/async`:

```
FATAL: could not open file "base/5/2672": No such file or directory
```

OID 2672 is `pg_database_oid_index`, a **shared** btree on `pg_database`
(OID 1262). The physical file lives at `global/2672`, not at
`base/<db>/2672`. So PG's file-path resolution is misrouting the open.

## Root cause

PG18 `RelationInitPhysicalAddr` (`relcache.c:1347-1354`):

```c
if (relation->rd_rel->reltablespace)
    relation->rd_locator.spcOid = relation->rd_rel->reltablespace;
else
    relation->rd_locator.spcOid = MyDatabaseTableSpace;
if (relation->rd_locator.spcOid == GLOBALTABLESPACE_OID)
    relation->rd_locator.dbOid = InvalidOid;
else
    relation->rd_locator.dbOid = MyDatabaseId;
```

The comment above this block (line 1335-1336) explicitly states:

> Note: at the physical level, relations in the pg_global tablespace must
> be treated as shared, even if relisshared isn't set.  Hence we do not
> look at relisshared here.

So the file path is derived **purely from `pg_class.reltablespace`** — it
must equal `GLOBALTABLESPACE_OID = 1664` for the path to resolve to
`global/<relfilenode>`. `formrdesc` sets this in memory at Phase 2
(`relcache.c:1948`), but Phase 3 then overrides `rd_rel` with the on-disk
`pg_class` row, so the **on-disk value must match**.

`bootstrapPgClassTuples` was writing `reltablespace = 0` for every nailed
relation — including the eight shared heaps (pg_database, pg_authid, …)
and every shared index attached to them. After Phase 3 ran during standby
boot, the in-memory `reltablespace = 1664` set by `formrdesc` got
clobbered to 0, and `RelationInitPhysicalAddr` computed
`spcOid = MyDatabaseTableSpace → DEFAULTTABLESPACE_OID`, dbOid =
MyDatabaseId = 5, path = `base/5/2672`.

A second, independent layer of the bug was in `flattenRels`: the helper
that fans out shared/local `idxSpec`s into `nailedRel` entries created
each index with `IsShared = false` (struct zero value) regardless of the
parent heap's flag. So even if `pgClassRow` consulted `IsShared`,
shared indexes would still be encoded with `relisshared = false` and the
wrong reltablespace.

## Fix

Two coordinated changes in `internal/initdb/`:

1. **`relcache_init.go::flattenRels`** — propagate `IsShared` from the
   heap list to each emitted `indexNailed` entry. All heaps in a single
   `flattenRels` call share the same `IsShared` value (the call sites
   are `nailedSharedRels = flattenRels(all-shared-heaps, …)` and
   `nailedLocalRels = flattenRels(all-local-heaps, …)`), so propagating
   from `heaps[0].IsShared` is unambiguous.

2. **`initdb.go::pgClassRow`** — replace the hardcoded
   `executor.NewIntDatum(0)` at the `reltablespace` slot with a helper
   `pgClassReltablespaceFor(rel.IsShared)` that returns:
     - `1664` (GLOBALTABLESPACE_OID) when the rel is shared,
     - `0` otherwise.

   The matching `buildPgClassBlob` (used by the init-file path) also
   now writes 1664 at struct offset 92 for shared rels, even though the
   init file is wiped by `RelationCacheInitFileRemove` at standby
   startup — keeping the two encoders in sync prevents the same bug from
   re-emerging if/when init-file reuse is restored.

## Regression pins

New file `internal/initdb/pg_class_reltablespace_test.go`:

- `TestPgClassRowSharedReltablespaceIsGlobalTablespaceOID` — asserts the
  Datum returned by `pgClassRow` at the reltablespace column has value
  1664 for shared rels and 0 for local rels.
- `TestPgClassRowSharedReltablespaceInEncodedPayload` — encodes a shared
  pg_database row through `executor.EncodeRowPG(pgClassColDefs, …)` and
  asserts bytes `[92:96]` of the on-disk Form_pg_class layout decode to
  1664 as little-endian uint32 (guards against alignment/encoder
  regressions).
- `TestFlattenRelsPropagatesIsSharedToIndexes` — walks `nailedSharedRels`
  and asserts every flattened index (`RelKind == 'i'`) has
  `IsShared = true`; walks `nailedLocalRels` and asserts every flattened
  index has `IsShared = false`.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestPgClassRowShared|TestFlattenRelsPropagatesIsSharedToIndexes' ./internal/initdb/` — PASS (3/3 new tests).
- `go test -count=1 ./internal/initdb/` — 15 failures, **identical to
  the pre-existing baseline** from Step 3cq (confirmed via `git stash` +
  diff; the new tests pass cleanly, no new regressions).
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next blocker

With reltablespace=1664 in `pg_class` for shared rels, PG's standby
`pg_database_oid_index` open will resolve to `global/2672`, where Step
3k's empty btree placeholder already lives. The next FATAL is expected
to fall further into autovacuum / catalog scan execution; tracked as
Step 3cs.
