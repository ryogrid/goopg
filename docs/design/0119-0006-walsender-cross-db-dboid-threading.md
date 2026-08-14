# 0119-0006bh — logical walsender cross-DB dbOid threading (M0119-0006)

Status: accepted (2026-08-14)

## Problem

The logical walsender resolved every catalog lookup against `DefaultDBOid`
(database 1), regardless of the connection's database. A logical replication
slot is **per-database** in PostgreSQL — upstream acquires the slot only when
its database matches `MyDatabaseId` (`postgres/src/backend/replication/slot.c:1760`;
`walsender.c:1447-1518`) and resolves publications/relations in that database
(`pgoutput.c:1800-1821`) — so a slot created against a non-default database
decoded **nothing** and, on the rare value that did render, produced a
wrong/numeric `reg*` name.

This is deferral-ledger row 1354 **claim 2** (cross-DB regclass resolution),
the sibling of claim 1 (off-path schema qualification, closed by the 84th slice
— `0119-0006bg`). It is the tail of `0119-0006bg`'s own "Deferred" note.

## The three DefaultDBOid consumers (not one)

All three live inside `runLogicalWalsender` (`internal/server/logicalwalsender.go`),
which is why a renderer-only fix is a half-fix:

1. **The reg* renderer** — `RegOutRendererVisible(im, visible)` binds no
   `dbOid`, so `regOutShared`'s regclass arm resolves via
   `LookupTableByOID(oid, DefaultDBOid)` (DB-1 namespace only): a DB-2 relation
   renders its name only if its OID collides with a DB-1 table, else falls to
   the numeric fallback (`regproc.c:943-987` dangling-OID shape).
2. **The catalog snapshot** — `BuildCatalogSnapshot` → `AllTables()` with no
   `dbOid` freezes **DB-1's** schema only. `PgOutput.Change` skips every change
   whose relation is not in the snapshot (`snap.Lookup` miss), so a non-default-DB
   slot decodes nothing at all — the dominant defect and the reason a renderer-only
   fix would be dead code.
3. **The publication filter** — `buildPublicationFilter` hardcodes
   `LookupPublication(name, catalog.DefaultDBOid)`. The PubSub registry is keyed
   per-database (`pubMapKey(dbOid, name)`, `catalog/pubsub.go`), so a non-default-DB
   publication is not found, and the still-non-nil-but-empty filter makes
   `Allows` return false for every change — all changes silently dropped.

## Change

Thread the connection's `dbName` (available one frame up at `runPostStartupLoop`,
`server.go:1501`) down the replication chain and resolve it to a `dbOid` at
`runLogicalWalsender` entry, then feed the OID to all three consumers:

```
runPostStartupLoop (dbName in scope)
  → handleReplicationCommand(..., dbName)            server.go:1636
    → replyStartReplication(..., dbName)             replication.go:61
      → runLogicalWalsender(..., dbName)             replication.go:469
        dbOid := resolveConnDBOid(im, dbName)        logicalwalsender.go
        → RegOutRendererVisible(im, visible, dbOidVar...)   (renderer)
        → BuildCatalogSnapshot(im, regOut, dbOidVar...)     (snapshot)
        → buildPublicationFilter(ps, names, dbOidVar...)    (filter)
```

`handleReplicationCommand` / `replyStartReplication` / `runLogicalWalsender` each
gain a trailing `dbName string`; the chain is strictly linear (single caller
each, verified), so no other call sites change.

### The ≠0 guard

`resolveConnDBOid` returns 0 on an empty/unknown name. Passing `[]uint32{0}` to
a variadic selects **namespace 0** (empty), not the DefaultDBOid fallback. So the
slice is built only when the OID is non-zero; an empty slice reaches each API's
own `resolveDBOid` default (DefaultDBOid), preserving today's DB-1 behavior
byte-identically:

```go
dbOid := resolveConnDBOid(im, dbName)
var dbOidVar []uint32
if dbOid != 0 { dbOidVar = []uint32{dbOid} }
```

### BuildCatalogSnapshot signature

`BuildCatalogSnapshot(c, regOut ...func(string,uint32) string)` became
`BuildCatalogSnapshot(c, regOut func(string,uint32) string, dbOid ...uint32)`:
Go forbids two variadics, so `regOut` is now a nil-able single parameter (nil =
numeric fallback, as the empty variadic previously meant) and `dbOid` is the
variadic. `AllTables(dbOid...)` scopes the freeze; empty keeps DB-1 scoping.
Existing callers that passed no renderer now pass `nil` (test files only — no
production caller omitted the renderer).

## Tests

- `TestBuildCatalogSnapshotScopesToDBOid` (`internal/wal/snapshot_test.go`) —
  a DB-2 snapshot contains DB-2's relation and not DB-1's; empty dbOid keeps DB-1.
- `TestPgOutputEmitsChangeForNonDefaultDB` (`internal/wal/pgoutput_test.go`) —
  the "was silently nothing" proof: a DB-2-scoped snapshot makes `PgOutput.Change`
  emit R+I for a DB-2 relation; the default snapshot still drops it.
- `TestBuildPublicationFilterResolvesCrossDB` (`internal/server/logicalwalsender_test.go`)
  — the DB-2 publication resolves with dbOid, and stays absent under the default.
- `TestRegOutRendererCrossDB` (`internal/server/pgoutput_reg_names_test.go`) —
  `RegOutRendererVisible(im, visible, db2Oid)` renders the DB-2 regclass name; the
  default dbOid falls to the numeric form.

## PG oracle

- `regclassout` / `RelationIsVisible` — `postgres/src/backend/utils/adt/regproc.c:943-987`.
- Text-mode reg* via typoutput — `postgres/src/backend/replication/logical/proto.c:848`.
- Per-database publication resolution — `postgres/src/backend/replication/pgoutput/pgoutput.c:1800-1821`.
- Slot-in-`MyDatabaseId` — `postgres/src/backend/replication/slot.c:1760`,
  `postgres/src/backend/replication/walsender.c:1447-1518`.

## Out of scope

Row 1355 (the regtype arm's fixed `regtypeQualify` and the user-type store's
missing namespace field) is a distinct catalog-representation change and stays
open. The slot record is not a dbOid source here: server-side
`CREATE_REPLICATION_SLOT LOGICAL` leaves `Slot.Database` empty, so the connection
`dbName` is the only reliable source.
