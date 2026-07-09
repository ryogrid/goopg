# Per-database catalog namespace (M0122-0007 slice 4)

Status: accepted (plumbing sub-slice 4a landed; 4b-4e planned)
Date: 2026-07-09
Supersedes: none — extends the "Still open" note in
`0122-0017-database-ddl-drop-guards.md` and the deferral-ledger rows dated
2026-07-06/2026-07-09 (task-id `M0122-0007`, "physical-storage-isolation
slice 4").

## Problem

`catalog.InMemory` is one shared, process-wide namespace for every real
catalog object (`tables map[string]*Table`, `indexes map[string]*Index`,
`byTable map[uint32]map[string]*Index`, plus ~20 sibling maps for
collations/conversions/aggregates/etc.). There is no per-database key
anywhere in it — a table created while connected to `postgres` is visible
from, and blocks re-creation from, any other database on the same server.
Slices 1-3 of this epic (real `pg_database.oid` allocation, `base/<dbOid>`
directory create/remove) gave every database a distinct identity and a
distinct physical directory, but nothing yet routes a *lookup* through that
identity — every `LookupTable`/`CreateTable`/`DropTable`/... call still
resolves against the one shared map regardless of which database the
calling connection is attached to. This is the reason `CREATE DATABASE
... TEMPLATE` cannot copy anything meaningful (there is no way to
enumerate "template1's relations" as distinct from any other database's,
per the slice-2 deferral row) and the reason a real dump+restore round-trip
into a second database fails immediately on the first schema-level name
collision (`M0110-0001` DU-002/E-002 deferral row, 2026-07-06).

Every prior loop that touched this bucket flagged the same thing: this is
"a multi-loop epic", "needs its own design pass", "far beyond a single
bounded loop". No design pass had actually been written down before this
doc — loops kept re-deriving the same conclusion and re-deferring. This
doc is that design pass: it inventories the actual blast radius and lays
out an ordered, independently-landable sub-slice breakdown so a future
loop can pick up exactly one bounded piece without re-scoping from
scratch.

## Blast radius (measured, not estimated)

- `internal/catalog/catalog.go` directly indexes `c.tables`/`c.indexes`/
  `c.byTable` at 168 + 33 + 25 = **226 call sites** (as of this doc's
  writing; `grep -c 'c\.tables\b' internal/catalog/catalog.go` etc.).
- The public entry points other packages actually call — `LookupTable`,
  `CreateTable`, `DropTable`, `LookupIndex`, `CreateIndex`, `DropIndex`,
  `RenameTable`, `RenameIndex`, `AllTables`, `AllIndexes`,
  `TablesInSchema`, `RegisterRealTable`, `TryRegisterUserTable`, plus
  OID-keyed lookups (`LookupTableByOID`, `LookupIndexByOID`,
  `InheritanceChildren`, `PartitionChildren`, ...) — number in the
  dozens, none of which take a database parameter today. Callers in
  `internal/executor` and `internal/planner` number in the hundreds
  (e.g. `LookupTable` alone is called from most DML/DDL operator
  constructors and the analyzer's name-resolution pass).
- `storage.RelFileNode.DBOid` is already a real field (threaded since
  slice 1), but `catalog.InMemory.dbOid` — the value it gets stamped
  with — is a single mutable field (`SetDBOID`/`DBOID`), not a
  per-connection or per-database value. So even the storage layer isn't
  actually multi-tenant yet: every relation's `RelFileNode.DBOid` is
  still the one process-wide value regardless of which database a
  connection is attached to.
- goopg additionally special-cases "postgres" for physical storage: it
  is stamped internally with `dbOid = DefaultDBOid` (1, `template1`'s
  conventional oid) at `NewInMemory()` time and mirrored to both
  `base/1/` and `base/5/` on disk (`PostgresDBOid = 5` is the real
  on-disk oid `detectCatalogDBOID` reads back at startup) — see
  `catalog.go`'s `PostgresDBOid` doc comment. Any sub-slice that starts
  keying storage by `dbOid` must not break this dual-mirror without a
  deliberate, separately-reviewed migration (out of scope for this
  doc's sub-slices; flagged here so it isn't rediscovered blind).

Attempting all of this in one loop — as several prior loops correctly
declined to do — risks a multi-thousand-line, partially-mechanical diff
across a 17,000+ line file with no safe intermediate stopping point if a
loop gets cut off mid-rewrite (headless sessions can be interrupted at any
turn boundary, `.ralph/PROMPT.md` "Headless Execution Reality"). Every
sub-slice below is scoped so it independently builds, tests green, and
changes no observable behavior until the sub-slice that's explicitly
allowed to (4c/4d) lands.

## Sub-slice breakdown

### 4a — Context plumbing (LANDED this loop, 2026-07-09)

Give the executor `Context` a way to know the connecting database's REAL
physical oid, without yet consuming it anywhere. Previously `Context` only
carried `CurrentDatabase` (the *name*, wired for `pg_extension` per-DB
scoping, M0110-0003 gap #7c) — no oid form existed at all.

- `catalog.(*InMemory).ResolveDatabaseOid(name) (oid uint32, ok bool)`
  (`internal/catalog/catalog.go`, next to `DatabaseOid`): the single
  canonical resolver for "the real, physical oid of database `name`" —
  `c.DBOID()` for `"postgres"` (matches the real on-disk oid detected at
  startup), the fixed bootstrap oids for `template1`/`template0` (1/4),
  or the `CreateDatabase`-allocated oid via the existing `DatabaseOid`
  for anything else. Deliberately distinct from `pg_database`'s
  `VirtualRows` displayed-oid column, which shows `"postgres"` as the
  legacy `16384` placeholder for `CREATE SUBSCRIPTION`/datacl compat —
  see that closure's own comment. The two switches must be kept in sync
  by hand (documented with a cross-reference comment in both places);
  unifying them into one shared helper is left to a future cleanup pass
  since the `VirtualRows` closure returns strings for a wider row shape
  and refactoring it wasn't this loop's scope.
- `executor.Context.CurrentDatabaseOid uint32` (`internal/executor/context.go`):
  zero means "unresolved" (same sentinel convention as `DatabaseOid`).
- `Server.wireExtensionRows` (`internal/server/dispatch.go`) — already the
  ONE shared site both the simple (`dispatch.go`) and extended
  (`dispatch_extended.go`) query paths call to wire `CurrentDatabase`/
  `ExtensionRows` (M0110-0003 gap #7c) — now also resolves and stamps
  `CurrentDatabaseOid`. Using the existing shared site instead of adding a
  second wiring call avoids the "wire only one of the two sibling paths"
  trap ([[pattern_sibling_paths_must_agree]]) that this exact bucket has
  hit before.

Tests: `TestResolveDatabaseOid` (`internal/catalog/database_test.go`),
`TestWireExtensionRowsStampsCurrentDatabaseOid`
(`internal/server/database_oid_wiring_test.go`) — both confirmed
non-vacuous via `git stash` on the production file alone (compile failure
pre-fix). **No lookup site reads `CurrentDatabaseOid` yet** — this
sub-slice is pure, behavior-preserving plumbing so 4b can start from a
tested foundation instead of inventing this resolution logic under
time pressure inside a larger mechanical rewrite.

Gates: `go build ./...` clean; `go vet ./internal/catalog/...
./internal/executor/... ./internal/server/...` clean; `go test
./internal/catalog/... ./internal/executor/... ./internal/server/...`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
failed transactions, all 3 workloads).

### 4b — Namespace the table/index maps (planned, NOT started)

Wrap `tables`/`indexes`/`byTable` in a per-dbOid namespace, e.g.:

```go
type tableNamespace struct {
    tables  map[string]*Table
    indexes map[string]*Index
    byTable map[uint32]map[string]*Index
}
namespaces map[uint32]*tableNamespace // keyed by dbOid
```

with a `(c *InMemory) ns(dbOid uint32) *tableNamespace` accessor (lazily
creates, under `c.mu`) replacing all 226 direct `c.tables`/`c.indexes`/
`c.byTable` references. Every public entry point listed under "Blast
radius" gains an explicit `dbOid uint32` parameter. To keep this sub-slice
itself behavior-preserving and independently gate-able, **every existing
caller passes `catalog.DefaultDBOid`** (a mechanical, find-and-replace-style
change at each call site) — this sub-slice changes the data structure and
every signature but not yet any actual routing decision. Recommended
approach for whichever loop takes this on: do it as a single self-contained
pass (not spread across loops) since a half-migrated signature set (some
callers passing a real dbOid, most still hardcoding `DefaultDBOid`) would
be strictly worse than the current single-namespace state — silently
wrong instead of honestly incomplete. Budget for this to be a full loop
(or a dedicated worktree-isolated pass) on its own; do not attempt to also
start 4c in the same loop.

### 4c — Route READ paths through the connection's real dbOid (planned)

Once 4b lands, thread `ctx.CurrentDatabaseOid` (already available since 4a)
through the read-side call sites — name resolution in the analyzer/planner
(`LookupTable`, `LookupIndex`, schema-qualified name resolution) — instead
of `DefaultDBOid`. This is the first sub-slice with an actual observable
behavior change: a second database can have a distinct read-visible table
set from the default one. Requires auditing embedded/test callers that
have no live connection (empty `CurrentDatabase`/zero `CurrentDatabaseOid`)
to fall back to `DefaultDBOid` explicitly, matching today's behavior.

### 4d — Route WRITE paths (planned)

`CreateTable`/`CreateIndex`/`DropTable`/`DropIndex`/`RegisterRealTable`/
`AddColumn`/rename/ALTER paths, so objects actually land in — and get
removed from — the connection's own namespace instead of the shared
default. Also wire `RelFileNode.DBOid` to the connection's real dbOid at
creation time (currently hardcoded to `DefaultDBOid` regardless of
connection) so physical storage genuinely separates per database, closing
the loop with slices 2/3's `base/<dbOid>` directories. **Must account for
the `postgres`/`template1` dual-mirror** noted under "Blast radius" —
`postgres`'s storage identity is not simply `ResolveDatabaseOid("postgres")`
in every context; audit `NewInMemory`'s `dbOid: DefaultDBOid` seed and the
`base/1/` + `base/5/` mirror before changing what oid live relations are
created under.

### 4e — Cross-cutting fixups + the original motivation (planned)

Once 4b-4d land: dependent-object walks that assume a single global
namespace (FK target resolution, view dependency tracking, sequence
ownership), then finally the actual `CREATE DATABASE ... TEMPLATE` copy
mechanism (`CreateDatabaseUsingFileCopy`/`copydir`,
`postgres/src/backend/commands/dbcommands.c`) this whole epic exists to
unblock, and the AC-002/DU-002 dump+restore round-trip probe already
staged in `internal/testport/pgdump_connsetup_test.go` (a soft `t.Logf`
today, ready to become a hard assertion once this lands).

## Recommended order and stopping points

4a (landed) → 4b → 4c → 4d → 4e, strictly in order — each depends on the
previous sub-slice's data shape. Do not attempt to skip 4b by threading a
real dbOid through call sites while the underlying map is still a single
shared namespace (it would compile and silently do nothing, exactly the
kind of "worse than a no-op" outcome flagged in the slice-2 deferral row).
If a sub-slice is cut off mid-implementation, prefer reverting it whole
(the tree must build and tests must pass at every commit) over leaving a
partially-migrated signature set — see 4b's note above.

## Deferred / explicitly out of scope

- Unifying `ResolveDatabaseOid`'s switch with the `pg_database` `VirtualRows`
  closure's inline oid-selection switch (both must independently stay in
  sync with the postgres/template0/template1 special cases today).
- The `postgres`/`template1` dbOid dual-mirror migration strategy for 4d —
  flagged, not designed; needs its own investigation once 4d is reached.
- Any of the ~20 sibling per-name maps on `InMemory` beyond
  `tables`/`indexes`/`byTable` (collations, conversions, aggregates,
  operator classes, etc.) — genuinely out of scope for "per-database
  catalog namespace" as motivated by the template-copy/dump-restore use
  cases; each would need its own audit for whether PG actually scopes it
  per-database (most do) before being folded into this epic.
