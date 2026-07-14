# Per-database catalog namespace (M0122-0007 slice 4)

Status: accepted (sub-slices 4a, 4b-i, 4b-ii, 4c, 4d-i, 4d-ii-part-1, and 4d-ii-part-2a landed; 4d-ii-part-2b items 1, 2, and 3 all fully landed; 4e's FK-target-resolution, sequence-ownership, and view-constraint-dependency items all landed; `CREATE DATABASE ... TEMPLATE` bounded validation landed 2026-07-10; the pg_class-under-fresh-database gap (name resolution generically, row enumeration for pg_class specifically) landed 2026-07-10, and the pg_indexes/pg_tables, pg_constraint, pg_index, pg_attrdef/pg_depend, pg_inherits, pg_policy, pg_trigger, pg_rewrite, pg_foreign_table, and pg_sequence row-content follow-ups landed the same day, plus the `oid::regclass`/`'name'::regclass` cast-direction dbOid-scoping gap (follow-up 33) — 2 sibling virtual-table builders (pg_statistic_ext, information_schema.routines/parameters/routine_*_usage) plus pg_sequences/information_schema.sequences (follow-up 34's narrowed remainder) plus the c.foreignServers registry-dbOid-key gap (follow-up 36, landed) and its same-shape c.userMappings sibling (follow-up 37, landed 2026-07-10); follow-up 37's own CURRENT_USER/SESSION_USER/CURRENT_ROLE/USER role-spec resolution discovery closed same-day (follow-up 38); restart durability for user tables created under a distinct-dbOid database — per-database pg_class/pg_attribute heap routing + startup reload into the owning namespace — landed same-day (follow-up 39); the real `CREATE DATABASE ... TEMPLATE` relation-copy mechanism itself landed 2026-07-10 for its bounded plain-table case (follow-up 40), extended the same day to also cover sequences (follow-up 41), plain views (follow-up 42), and materialized views (follow-up 43, including a real `execCreateMatView`/`execRefreshMatView` dbOid-scoping bugfix found along the way) — index/typed-table TEMPLATE copying remains deferred, see "Remaining 4e work" below)
Date: 2026-07-10
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

### 4b-i — Namespace the table/index maps, internal only (LANDED, 2026-07-09)

Wrapped `tables`/`indexes`/`byTable` in a per-dbOid namespace:

```go
type tableNamespace struct {
    tables  map[string]*Table
    indexes map[string]*Index
    byTable map[uint32]map[string]*Index
}
namespaces map[uint32]*tableNamespace // keyed by dbOid, on InMemory
```

with a `(c *InMemory) ns(dbOid uint32) *tableNamespace` accessor replacing
all 226 direct `c.tables`/`c.indexes`/`c.byTable` references inside
`internal/catalog/catalog.go`, plus the further 27 direct-field references
found in same-package white-box tests (`*_test.go` files in
`internal/catalog/`, e.g. `temp_namespace_test.go` seeding rows straight
into `c.tables`). All 253 sites now read `c.ns(DefaultDBOid).tables` (or
`.indexes`/`.byTable`) — a purely mechanical substitution since every one
of them already used receiver `c` (confirmed by grepping for any other
receiver — none existed).

**Locking contract (the one non-mechanical design decision in this
sub-slice):** `ns()` deliberately does **not** acquire `c.mu` itself —
`sync.RWMutex` is not reentrant, and every existing call site already
holds the right lock (`RLock` or `Lock`) before touching the table/index
maps, exactly as before 4b-i. This only stays sound because
`namespaces[DefaultDBOid]` is pre-seeded once inside `NewInMemory` (single
-threaded, before any goroutine fan-out), so `ns()`'s lazy-create branch
(`c.namespaces[dbOid] = newTableNamespace()`) is dead code for the
lifetime of 4b-i/4c — it exists only so a future missing seed fails soft
(empty namespace) instead of a nil-map panic, and it must stay dead until
whichever sub-slice starts creating namespaces for real dbOids does so
under `c.mu` held for writing (4d, when `CreateDatabase` needs its own
namespace).

**Deliberately deferred out of 4b-i** (this is the reason it's "4b-i" and
not simply "4b"): the design originally scoped 4b as ALSO giving every
public entry point (`LookupTable`, `CreateTable`, `DropTable`,
`LookupIndex`, ... — see "Blast radius") an explicit `dbOid uint32`
parameter, with every external caller (hundreds of sites across
`internal/executor`/`internal/planner`) updated to pass
`catalog.DefaultDBOid`. That signature-and-caller migration is
independently large (a second multi-hundred-site mechanical pass, this
time crossing package boundaries) and is NOT required for 4b-i's own
behavior-preservation guarantee — the internal data-structure swap is
already a complete, self-consistent, zero-observable-change unit on its
own with `ns(DefaultDBOid)` hardcoded at every one of catalog.go's call
sites. Splitting it into 4b-i (landed) / 4b-ii (below) avoids the
"half-migrated signature set is worse than not started" trap the
original doc warned about — 4b-ii starts from an honest 0%, not a
partially-mechanical mid-file state.

Gates run (all PASS): `go build ./...` clean; `go vet ./...` clean;
`go test -short $(go list ./... | grep -v /internal/testport)` (every
package, full repo, short mode); `go test ./internal/catalog/...
./internal/executor/... ./internal/server/...` (non-short, targeted);
`scripts/tpch-spotcheck.sh` (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` (0 failed, all 3 pgbench workloads).

### 4b-ii — Give catalog entry points an explicit dbOid parameter (LANDED, 2026-07-09)

Every public entry point listed under "Blast radius" — `LookupTable`,
`CreateTable`, `DropTable`, `LookupIndex`, `CreateIndex`, `DropIndex`,
`RenameTable`, `RenameIndex`, `AllTables`, `AllIndexes`,
`TablesInSchema`, `RegisterRealTable`, `TryRegisterUserTable`,
`LookupTableByOID`, `LookupIndexByOID`, `InheritanceChildren`,
`PartitionChildren` — gained an explicit `dbOid` parameter, threaded
through to `c.ns(dbOid)` from 4b-i.

**Deviation from the plan above (the actual shape that landed):** the
original text on this line called for a *required* `dbOid uint32`
parameter, with every one of the hundreds of external call sites in
`internal/executor`/`internal/planner` mechanically edited to pass
`catalog.DefaultDBOid` explicitly. That would have been a genuinely
large, cross-package, error-prone diff — a measured `grep` pass at
implementation time found 300+ non-test call sites for `LookupTable`
alone (and 800+ across all 17 entry points), any one of which, if
missed, would silently fail to compile (caught) or, worse, could be
mis-edited to reference the wrong variable. Instead, every one of the 17
entry points gained a **trailing variadic `dbOid ...uint32` parameter**
(a new package-level `resolveDBOid(dbOid []uint32) uint32` helper next to
`ns()` resolves it: `dbOid[0]` if supplied, else `DefaultDBOid`). This is
strictly forward-compatible with the plan's own stated goal — 4c/4d's
future call sites read exactly the same either way
(`c.LookupTable(name, ctx.CurrentDatabaseOid)`), since Go allows passing
0 or 1 arguments to a variadic parameter identically to a required one —
while making the "existing caller passes `DefaultDBOid`" step happen for
free, for all ~800 sites, with zero risk of a missed or mis-edited call
site. The `Catalog` interface (`internal/catalog/catalog.go`) gained the
same variadic parameter on its 9 overlapping methods
(`LookupTable`/`LookupIndex`/`CreateTable`/`CreateIndex`/`DropTable`/
`DropIndex`/`TablesInSchema`/`AllIndexes`/`RenameTable`); its only two
implementers are `*InMemory` (this sub-slice) and `*SearchPathCatalog`
(the `LookupTable` search-path override, updated to forward `dbOid...`
unchanged) — confirmed by grep, no mock/test-double implementers of the
interface exist. The internal `lookupIndexLocked` helper (shared by
`LookupIndex`/`RenameIndex`/`RenameIndexDuringRecovery`/
`RegisterIndexDuringRecovery`/`UnregisterIndexDuringRecovery`) and
`tableByOID` (shared by `LookupTableByOID` and 8 TOAST-relation helpers)
took a **required** `dbOid uint32` parameter instead, since they're
private and every call site is in the same file — the recovery-path and
TOAST-helper callers (out of scope for the 17-entry-point list) all pass
`DefaultDBOid` explicitly, unchanged behavior.

Zero observable behavior change: no caller anywhere in the tree passes a
non-default `dbOid` yet, so every one of the ~800 pre-existing call
sites resolves to exactly `DefaultDBOid` exactly as before — this
sub-slice still changes no actual routing decision (4c is the first
sub-slice that reads a real per-connection value). New regression test
`TestDBOidParameterRoutesToDistinctNamespace`
(`internal/catalog/dbid_namespace_test.go`) exercises all 17 entry
points with an explicit non-default `dbOid`, proving two namespaces are
genuinely isolated (a table/index created under one is invisible to
lookups against the other) rather than the parameter being accepted and
silently ignored — this is also the first test to exercise `ns()`'s
lazy-create branch with a real (non-pre-seeded) dbOid, safe here because
every write happens through a `CreateTable`/`CreateIndex` call that
already holds `c.mu` for writing (see 4b-i's locking contract).

Gates run (all PASS): `go build ./...` clean; `go vet ./...` clean; `go
test ./internal/catalog/... ./internal/executor/... ./internal/server/...`
PASS; `go test -short $(go list ./... | grep -v /internal/testport)`
(full repo, short mode) PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`
PASS (0 failed transactions, all 3 pgbench workloads).

### 4c — Route READ paths through the connection's real dbOid (LANDED this loop, 2026-07-09)

`ctx.CurrentDatabaseOid` (available since 4a) is now threaded into
`LookupTable`/`LookupIndex` — every analyzer/planner name-resolution call site
that goes through a `catalog.SearchPathCatalog` wrapper (`ctxPlanCatalog`/
`sessionPlanCatalog`/`o.planCatalog()`, the existing per-connection catalog
seam that already carries `TempOwnerToken`/`SnapshotPartitionDetachEpoch`/
`DisableSeqScan`) now resolves against the connection's own database instead
of always `DefaultDBOid`. Landed as three pieces:

1. **`catalog.SearchPathCatalog` gains a `DBOid uint32` field** plus an
   `effectiveDBOid(explicit []uint32) []uint32` helper: an explicit
   caller-supplied dbOid still wins unconditionally (unchanged pre-4c
   contract), else `c.DBOid` is translated via the new exported
   `catalog.NamespaceDBOid(uint32) uint32`. `LookupTable`'s existing override
   and a new `LookupIndex` override both call it.
2. **The `postgres`/`DefaultDBOid` dual-mirror shim** (`NamespaceDBOid`): a
   connection's real database oid is NOT used verbatim as the namespace key.
   `ResolveDatabaseOid("postgres")` answers `PostgresDBOid` (5, the real
   on-disk oid `detectCatalogDBOID` reads back at startup — see "Blast
   radius" above) but every catalog write path still unconditionally
   persists under `DefaultDBOid` (1) until 4d migrates them. Wiring the raw
   oid straight through would have made every existing table invisible to
   every "postgres" connection — i.e. essentially all real traffic
   (TPC-H/pgbench/regress all connect to "postgres"). `NamespaceDBOid` maps
   both `0` (no live connection — embedded/test contexts) and `PostgresDBOid`
   back to `DefaultDBOid`; only a genuinely distinct oid (a real
   `CREATE DATABASE`, or `template0`) gets routed to its own — today still
   empty, since 4d hasn't landed — namespace. This is the deliberate,
   narrowly-scoped instance of the "must account for the dual-mirror" warning
   4d's own section below already flagged; 4d must revisit it once write
   paths route by dbOid too.
3. **`ns()`'s read-path race hazard was fixed as a load-bearing prerequisite,
   not a nice-to-have.** `ns()` used to lazily create-and-register a missing
   dbOid's namespace on ANY call, including ones made under `c.mu.RLock()`
   (`LookupTable`/`LookupIndex`/5 other read entry points) — a concurrent
   map write racing every other RLock holder. Before 4c this was unreachable
   in production (every real call always resolved to the pre-seeded
   `DefaultDBOid`); 4c makes a never-seeded dbOid a normal read (any real
   second database, before 4d ever writes to it), so the hazard became live.
   Fix: `ns()` is now non-mutating (returns a shared, never-written
   `emptyTableNamespace` sentinel for a missing dbOid); a new `getOrCreateNS`
   — used only by the four entry points that can register a namespace's
   first object (`CreateTable`/`CreateIndex`/`RegisterRealTable`/
   `TryRegisterUserTable`), all called under `c.mu.Lock()` — keeps the
   create-on-demand behavior where it's actually safe. Rename/Drop paths
   look-up-then-bail on a missing entry and never needed create semantics.
   `TestNsReadOnlyUnderConcurrentUnseededDBOids`
   (`internal/catalog/dbid_namespace_test.go`) reproduces the exact
   `fatal error: concurrent map read and map write` crash under `-race`
   against the pre-fix `ns()` (confirmed via a scratch revert-and-rerun, not
   committed) and passes clean against the fix.

Also landed: the cross-session plan cache (`internal/server/plancache.go`,
M0098-0005) now segregates its key by `catalog.NamespaceDBOid` (new
`planCacheKey` helper) — a plan embeds resolved `*catalog.Table`/
`*catalog.Index` pointers from a specific namespace, so the single
server-wide cache map must never satisfy one connection's lookup with a plan
built for a different namespace. Both cache sites
(`dispatchSimpleQueryViaExecutor`'s single-statement cache and
`executeExtendedQueryViaExecutor`'s cross-session cache) were updated
together — this was caught by hand-tracing the plan-cache's actual key
computation, not by a failing test, since every connection resolves to the
same `DefaultDBOid` today (no test yet connects to two distinct real
databases through the live server) and would have passed silently wrong once
4d makes namespaces actually diverge.

New tests: `TestSearchPathCatalogDBOidRoutesReads` (5 subcases: zero,
`PostgresDBOid`, `DefaultDBOid` explicit, a genuinely distinct dbOid, and
explicit-argument-wins-over-wrapper-field) and
`TestNsReadOnlyUnderConcurrentUnseededDBOids` (both
`internal/catalog/dbid_namespace_test.go`).

Gates run (all PASS): `go build ./...` clean; `go vet ./...` clean; `go test
-race ./internal/catalog/... ./internal/executor/...` PASS;
`go test ./internal/catalog/... ./internal/executor/... ./internal/server/...`
PASS (the one `-race` failure in `internal/server` —
`TestConnectExceedsPositiveDatconnlimitRejected`, a pre-existing
`internal/activity/registry.go` `acquire`/`CountByDatName` race — reproduces
identically on unmodified HEAD via `git stash`, confirmed unrelated to this
loop and out of scope); `go test -short $(go list ./... | grep -v
/internal/testport)` (full repo, short mode) PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33 — the critical end-to-end proof that the `postgres`
dual-mirror shim works: every query against "postgres" still sees
`DefaultDBOid`'s tables); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3 pgbench
workloads — exercises both plan-cache paths under real concurrent load).

### 4d-i — Thread the connection's real dbOid through write entry points (LANDED this loop, 2026-07-09)

Every executor call site that mutates the catalog — `CreateTable`
(`execCreateTable`, `execCreateTableAs`'s no-storage and normal branches,
partition-child creation, `CreateSequenceCatalogRelation`), `CreateIndex`
(`execCreateIndex`'s gist/spgist/gin/brin branch, the btree bulk-build path,
`createExclusionIndexStub`), `DropTable` (`execDropTable`'s leaf-drop and
DROP-SCHEMA-CASCADE table loop, sequence virtual-relation removal, the TEMP
table shadow-drop in `execCreateTable`), `DropIndex` (`execDropIndex`,
`ApplyPendingIndexDrops`, the two build-failure/WAL-failure rollback paths in
`execCreateIndex`'s btree branch, the ALTER-TABLE-DROP-COLUMN dependent-index
cleanup), `RenameTable`/`RenameIndex` (the two ALTER TABLE/INDEX RENAME
branches), `AddColumn` (`addColumnRecursive`), and the savepoint-rollback
restore path (`RegisterTable`/`RestoreIndex` in `ProcessRollbackUndos`,
`execRollbackTo`, and the TEMP-table-drop shadow-restore in `execDropTable`)
— previously called the corresponding `catalog.InMemory` method with **no**
dbOid argument, which every one of them defaults to `DefaultDBOid`
(`resolveDBOid`'s zero-length fallback) regardless of which database the
connection was actually bound to. All ~24 call sites (in
`internal/executor/operators_ddl.go` and `operators_tx.go`) now pass
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` explicitly — the exact same
shim 4c uses for reads, so a `"postgres"` connection's writes still land
under `DefaultDBOid` (unchanged behavior; see the dual-mirror note below) while
a connection to a genuinely distinct, non-`"postgres"` real database (one
`CREATE DATABASE` allocated its own `pg_database.oid` for, per the
5c467579-and-friends commits) now creates/drops/renames objects in **that**
database's own namespace instead of the shared default. Two entry points
(`AddColumn`, `RegisterTable`, `RestoreIndex`) did not even have a `dbOid
...uint32` parameter yet (4b-ii missed them because nothing called them with
one) — added here, mirroring the existing variadic-parameter convention.
`CreateSequenceCatalogRelation` (shared between the live executor and
`internal/initdb/sequence_ddl_recovery.go`'s WAL replay) gained a *required*
`dbOid uint32` parameter instead of variadic, since its two callers'
correct values are so different (the live path resolves the connection's
real dbOid; startup replay has no per-connection concept yet and must keep
passing `catalog.DefaultDBOid` explicitly, preserving today's single-database
replay behavior). `ApplyPendingIndexDrops` needed no signature change — both
its callers already had a `*Context` in scope, so it resolves
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` internally.

**Dual-mirror preserved, not yet revisited:** `catalog.NamespaceDBOid` still
folds `PostgresDBOid` (5) back to `DefaultDBOid` (1) exactly as 4c left it —
this sub-slice deliberately did not touch that shim, so every `"postgres"`
connection's writes are bit-for-bit unchanged (confirmed via
`scripts/tpch-spotcheck.sh` staying Q12=2/Q13=33 and the pgbench smoke gate,
both of which connect as `"postgres"`). The "audit the dual-mirror before
changing what oid live relations are created under" caution from the
original 4d planning note therefore does not yet apply — no live relation's
storage identity changed for any existing connection kind.

**Critical scope finding, discovered while landing this — the real gap
4d-ii must close:** this sub-slice only fixes *half* of what a working
per-database namespace needs. `ectx.Catalog` (the raw `catalog.Catalog` every
executor operator holds, assigned once in
`internal/server/dispatch.go:295` as `s.cfg.Catalog`, a bare
`*catalog.InMemory`) is **never** wrapped in a `SearchPathCatalog` the way
`ectx.PlanCatalog` is (4c only wired the *planner's* catalog reference). That
means every executor-operator-level `LookupTable(name)` / `LookupIndex(name)`
call with no explicit dbOid argument — there are **60 in
`internal/executor/operators_ddl.go` alone**, plus more across
`operators_fk.go`, `operators_cluster.go`, `operators_reindex.go`,
`operators_sequence.go`, `operators_storage.go`,
`operators_pg_get_publication_tables.go`, and every DML operator (`expr.go`
and friends) — still resolves against `DefaultDBOid` only, completely
independent of `ctx.CurrentDatabaseOid`. Concretely: `execDropTable`,
`execCreateIndex`, `execAlterTable*`, and effectively every DML operator all
start by calling `o.ctx.Catalog.LookupTable(name)` (or `LookupIndex`) to find
the object they're about to operate on — for a table created under a
genuinely distinct dbOid by this loop's now-correct `CreateTable` routing,
that lookup will report "does not exist", because it never receives the
dbOid this loop threads on the *write* side. **This was proven empirically
while writing this loop's tests** (`internal/executor/ddl_write_dbid_routing_test.go`):
a `CREATE TABLE ... AS SELECT` against a table in a distinct namespace
required rewriting the test to avoid a `FROM` clause, since
`o.planCatalog()` (which falls back to the raw, unwrapped `ctx.Catalog` when
`ctx.PlanCatalog` is nil — true in every unit-test harness, and read-routed
only via `SearchPathCatalog` in the live server path) could not resolve the
source table either. In production, planner-level resolution (`PlanCatalog`)
*is* dbOid-aware since 4c, so a `SELECT` naming the table plans correctly —
but the *executor operator's own* subsequent lookup of the same table (e.g.
to fetch `*catalog.Table` for a DML op, or to locate it for DROP/ALTER) is
not, so plan-time and execute-time catalog resolution disagree for any
connection to a genuinely second real database. This is safe today only
because nothing in production or in any existing test connects to a second
real database end-to-end yet (confirmed by 4c's own writeup) — every real
connection today resolves through `catalog.NamespaceDBOid` to `DefaultDBOid`,
so this loop's changes are a pure no-op for all existing behavior (proved by
the full non-testport suite, `-race` catalog/executor run,
tpch-spotcheck, and pgbench smoke gates all staying green with zero row-count
or transaction-count drift). But **the write-routing landed here is
necessary, forward-compatible plumbing, not a complete story**: 4d-ii must
thread the same `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` argument
through every executor-operator-level `LookupTable`/`LookupIndex` call before
a second real database's objects are actually usable end-to-end.

New tests: `internal/executor/ddl_write_dbid_routing_test.go` —
`TestExecCreateTableRoutesToConnectionRealDBOid`,
`TestExecCreateTableAsRoutesToConnectionRealDBOid`, and
`TestExecCreateTablePostgresConnectionStaysOnDefaultDBOid` (the dual-mirror
pin, mirroring 4c's own read-side pin but for the write path). These test
`CreateTable` specifically because it is the one entry point that does not
need a prior `LookupTable` to already be dbOid-aware — every other entry
point this loop touched (`DropTable`, `CreateIndex`, `RenameTable`, etc.) is
correctly wired but not independently end-to-end-testable yet, precisely
because of the 4d-ii gap above (their callers obtain the `*catalog.Table` /
`*catalog.Index` they operate on via a `LookupTable`/`LookupIndex` call that
is not yet dbOid-routed).

Gates: `go build ./...` / `go vet ./...` clean; `go test -race
./internal/catalog/... ./internal/executor/...` PASS; `go test -short
$(go list ./... | grep -v /internal/testport)` (full repo, short mode) PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3 pgbench
workloads).

### 4d-ii-part-1 — Thread dbOid through operators_ddl.go's direct-ctx lookups (LANDED this loop, 2026-07-09)

Closed the mechanical, zero-signature-change subset of 4d-ii's first named
piece: all 60 call sites in `internal/executor/operators_ddl.go` that call
`o.ctx.Catalog.LookupTable(...)` / `o.ctx.Catalog.LookupIndex(...)` (or, for
the one bare-`ctx *Context`-parameter helper, `ctx.Catalog.LookupTable(...)`)
with no dbOid argument now append `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`
(respectively `ctx.CurrentDatabaseOid`) — the same shim 4c wired for reads
and 4d-i wired for writes. This closes exactly the gap 4d-i's own writeup
proved empirically: `execDropTable`, `execCreateIndex` (table + all its
internal re-lookups of the index it just built), `execCreateView`,
`execDropOneView`/`execDropOneMatView`, the ALTER TABLE family, partition
attach/detach/inherit, sequence `OWNED BY` resolution, and
`catalogHeapSyncAvailable` can now all find an object 4d-i's write-side
routing created under a genuinely distinct connection dbOid, instead of only
ever resolving `DefaultDBOid`.

Applied via a small throwaway Python script (not committed) that locates each
`.LookupTable(`/`.LookupIndex(` call preceded by `o.ctx.Catalog.` or
`ctx.Catalog.`, walks forward counting paren depth to find the matching close
paren (correctly handling the several multi-line call sites, e.g.
`catalogHeapSyncAvailable`'s and `validateSeqOwnedBy`'s), and inserts the
dbOid argument there — then the full diff was hand-reviewed before running
any gates.

New tests: `TestExecDropTableFindsOwnDistinctDBOidTable`,
`TestExecCreateIndexFindsOwnDistinctDBOidTable`
(`internal/executor/ddl_write_dbid_routing_test.go`) — CREATE TABLE followed
by DROP TABLE / CREATE INDEX on the *same* distinct-dbOid connection, which
failed with "does not exist" / "relation does not exist" before this loop
(the exact failure mode 4d-i's writeup described but could not yet fix).

Gates: `go build ./...` / `go vet ./...` clean; `go test -race
./internal/catalog/... ./internal/executor/...` PASS; `go test -short $(go
list ./... | grep -v /internal/testport)` (full repo, short mode) PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3 pgbench
workloads).

**Explicitly out of scope this loop (4d-ii-part-2, planned):**

1. 15 `im.LookupTable`/`im.LookupIndex`/`cat.LookupTable` call sites still
   inside `operators_ddl.go` itself, bound from a locally type-asserted `im,
   ok := o.ctx.Catalog.(*catalog.InMemory)` or a bare `cat Catalog` /
   `im *catalog.InMemory` function parameter (e.g.
   `collectAllViewTransitiveDeps(im *catalog.InMemory, startName
   parser.ObjectName)`, `walkSelectPKDeps(sel, cat, out, seen)`, the
   ACL-grant table-name loop around line 17026, the DROP-CASCADE helpers
   around lines 17179-17472). These are **not** a trailing-arg mechanical
   fix — the enclosing helper functions have no `ctx`/dbOid in scope, so
   fixing them requires threading a `dbOid uint32` parameter through each
   helper's own signature and updating every one of *its* callers too. A
   materially different, signature-cascading shape of change from this
   loop's direct-ctx sites.
2. Every other file the original 4d-i "critical scope finding" named:
   `operators_fk.go`, `operators_cluster.go`, `operators_reindex.go`,
   `operators_sequence.go`, `operators_storage.go`,
   `operators_pg_get_publication_tables.go`, and every DML operator
   (`expr.go` and friends) — entirely untouched. A bare
   `LookupTable`/`LookupIndex` call in any of those files still resolves
   only `DefaultDBOid`. Re-measure via `grep -n '\.LookupTable(\|\.LookupIndex('`
   across those files before starting part 2 — the design doc's original
   "60+ in operators_ddl.go alone, plus more" estimate is now precisely
   accounted for on the `operators_ddl.go` side (60 closed, 15 deferred to
   part 2 above) but the cross-file count was never grep-measured.
3. `RelFileNode.DBOid` at creation time (4d-ii's second named piece, below)
   — still hardcoded to `DefaultDBOid` regardless of connection.

### 4d-ii-part-2a — Signature-cascading `im`/`cat`-local lookups in `operators_ddl.go` (LANDED this loop, 2026-07-09)

Closed item 1 from 4d-ii-part-1's "explicitly out of scope" list: all 14
`im.LookupTable`/`im.LookupIndex`/`cat.LookupTable` call sites in
`operators_ddl.go` that were bound to a locally type-asserted `im, ok :=
o.ctx.Catalog.(*catalog.InMemory)` or a bare `cat`/`im` function parameter
with no dbOid argument. Re-auditing each site's enclosing function found the
split was **not** as originally estimated — most sites turned out to be
mechanical (the enclosing function is itself a `(o *ddlOp)` method, or
already receives `ctx *Context`, so `o.ctx`/`ctx` was in scope the whole
time and only a trailing `catalog.NamespaceDBOid(...)` argument was needed,
exactly like 4d-ii-part-1's 60 sites):

- `execCreateView`'s OR REPLACE branch (line ~4934, `im.LookupTable(s.Name)`).
- `execAttrACLChange`'s column-GRANT table loop (line ~17026).
- `execCommentOn`'s six `ObjKind` cases — table/index/column/constraint/
  trigger/policy/rule (lines ~17179-17398): `im.LookupTable(s.ObjName)`
  (×6) and `im.LookupIndex(s.ObjName)` (×1).
- `execCreateStatistics`'s FROM-table resolution (line ~17472).
- `lockRelationTransitively`'s view-body dependency walk (lines ~19887,
  19889) — this one already receives `ctx *Context` as a parameter (it is
  not a `ddlOp` method, but `ctx` was already threaded through), so it only
  needed `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` appended.

Only two clusters were genuine signature-cascades (the enclosing function
truly has no `ctx`/dbOid in scope, confirmed via `find_referencing_symbols`
before editing):

- `collectAllViewTransitiveDeps(im, startName)` → added a `dbOid uint32`
  parameter, threaded through its internal `im.LookupTable(curr)` call and
  all 4 call sites (`execDropView`, `execDropTable` ×3), all of which are
  `ddlOp` methods and pass `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`.
- The `collectViewPKDeps`/`walkSelectPKDeps`/`walkExprPKDeps`/
  `addGroupByPKDeps` cluster (mutually recursive AST walkers used by CREATE
  VIEW to register PK-constraint dependencies, M0097-0036) → all four gained
  a `dbOid uint32` parameter, threaded through `addGroupByPKDeps`'s internal
  `cat.LookupTable(...)` call; the single external call site
  (`execCreateView`) passes `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`.

New tests (`internal/executor/ddl_write_dbid_routing_test.go`):
`TestExecCommentOnFindsOwnDistinctDBOidTable` (table/column/index comment
targets), `TestExecCreateStatisticsFindsOwnDistinctDBOidTable`,
`TestExecAttrACLChangeFindsOwnDistinctDBOidTable` — each confirmed to FAIL
when the corresponding call site's dbOid argument is temporarily reverted
(non-vacuousness spot-check, not committed).

**The two signature-cascaded functions (`collectAllViewTransitiveDeps` and
the PK-deps cluster) are NOT covered by a regression test** — see the new
finding below; they are currently unreachable on a genuinely distinct dbOid
via any SQL path, so no test could observe a behavior difference yet. The
code change itself was verified by inspection (matches the identical
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` pattern proven correct at
70+ other sites across 4c/4d-i/4d-ii-part-1) and does not regress the
DefaultDBOid/`postgres` path (full repo test suite + tpch-spotcheck +
pgbench smoke all pass unchanged).

**New finding (blocks testing/using the two signature-cascaded functions on
a genuinely distinct dbOid today):** `catalog.InMemory.CreateView` (write
side) and `AllUserViews`/`AllUserMatViews`/`IndexesOnTable` (read side used
by `viewsDependingOnView`/`viewsDependingOnTable`/
`matViewsDependingOnRelation`/`addGroupByPKDeps`'s own `IndexesOnTable`
call) are **all** hardcoded to `c.ns(DefaultDBOid)` with no dbOid parameter
at all — a materially larger gap than the "60+ LookupTable/LookupIndex call
sites" 4d-i/4d-ii-part-1/4d-ii-part-2a closed. Concretely: (a) `CREATE VIEW`
issued on a distinct-dbOid connection always lands the view under
`DefaultDBOid` regardless of `ctx.CurrentDatabaseOid` (so two different
databases' views of the same name collide in one shared namespace today);
(b) even a table's own indexes are unreachable via `IndexesOnTable` once the
table lives under a distinct dbOid, because `IndexesOnTable` never consults
its `dbOid` — only `c.ns(DefaultDBOid).byTable`. Discovered while trying to
write an end-to-end/white-box regression test for this loop's
`collectAllViewTransitiveDeps` and PK-deps-cluster fixes: `CREATE VIEW ...
FROM base` on a distinct-dbOid connection fails validation at
`planner.Plan` (a separate, also-undocumented `o.planCatalog()` dbOid gap)
before even reaching `CreateView`, and a white-box attempt to bypass SQL
entirely by calling `addGroupByPKDeps` directly still failed because
`IndexesOnTable` couldn't find the PK index created under the distinct
dbOid. See the deferral-ledger row appended this loop for the resume point.

### 4d-ii-part-2b — Remaining cross-file lookups + `RelFileNode.DBOid`

Two remaining pieces (renumbered from the original "4d-ii-part-2"; part 2a's
`operators_ddl.go`-local scope above is now closed):

1. The full cross-file sweep item 2 from 4d-ii-part-1's "explicitly out of
   scope" list: `operators_fk.go`, `operators_cluster.go`,
   `operators_reindex.go`, `operators_sequence.go`, `operators_storage.go`,
   `operators_pg_get_publication_tables.go`, and every DML operator
   (`expr.go` and friends) — never grep-measured; re-measure via `grep -n
   '\.LookupTable(\|\.LookupIndex('` across those files before starting.
   Given the scale, consider whether wrapping `ectx.Catalog` itself in a
   `SearchPathCatalog` (mirroring `ectx.PlanCatalog`) is viable instead of
   touching every remaining call site individually — but note this breaks
   every `im, ok := o.ctx.Catalog.(*catalog.InMemory)` type assertion (262
   occurrences across `internal/executor`, measured via grep during
   4d-ii-part-1), which would need updating in lockstep, so it may not
   actually be less work than the mechanical per-call-site fix.
2. **LANDED (2026-07-10):** Wire `RelFileNode.DBOid` to the connection's real
   dbOid at creation time. `catalog.Table`/`catalog.Index` each gained a
   `DBOid uint32` field, populated by `CreateTable`/`CreateIndex` from
   `resolveDBOid(dbOid)` — the same `dbOid ...uint32` variadic parameter every
   executor DDL call site already threads as
   `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` (items 1/3). No call site
   of `CreateTable`/`CreateIndex` changed — the field rides the existing
   parameter. `InMemory.RelFileNode`/`IndexRelFileNode` now prefer
   `table.DBOid`/`index.DBOid` over the single process-wide `c.dbOid`, but
   **only when it names a genuinely distinct database** (nonzero and not
   `DefaultDBOid`) — this is the `postgres`/`template1` dual-mirror guard the
   original scope note above called out: every `NamespaceDBOid`-translated
   "postgres" table's own `DBOid` is `DefaultDBOid` (1) for `c.ns()` keying
   (same translation `LookupTable`/`CreateTable` already apply), but its
   correct *physical* dbOid is whatever `c.dbOid` currently resolves to —
   `PostgresDBOid` (5) after `SetDBOID` runs at startup
   (`detectCatalogDBOID`), preserving the `base/1/` + `base/5/` mirror.
   Using `table.DBOid` unconditionally was tried first and immediately broke
   `TestAlterTableSetTablespacePhysicalRelocationSurvivesRestart` (relocated
   files landed under `base/1/pg_tblspc/…` instead of the expected
   `base/5/pg_tblspc/…` after restart) — confirms the guard is load-bearing,
   not defensive boilerplate. A table/index created under a real
   `CREATE DATABASE`-allocated oid (the only case `table.DBOid` is neither 0
   nor `DefaultDBOid`) now gets its own physically-separate `base/<dbOid>/`
   relfilenode path instead of aliasing onto whatever `c.dbOid` happens to be
   process-wide. Verified via new test
   `TestRelFileNodeUsesTableOwnDBOidNotProcessWideDefault`
   (`internal/executor/ddl_write_dbid_routing_test.go`): two same-named
   tables created on distinct connection dbOids now resolve to distinct
   `storage.RelFileNode` values (previously they aliased onto the same
   on-disk path — physical storage was never actually multi-tenant despite
   `storage.RelFileNode.DBOid` being a real field since slice 1). **Not
   covered by this item:** `TryRegisterUserTable`/`RegisterRealTable`
   (`internal/catalog/catalog.go`, the pg_class-heap-scan and
   `pg_goopg_catalog_cache.json`-snapshot startup recovery paths) still don't
   set the new `Table.DBOid` field on the `*Table` they register — both
   callers (`internal/initdb/open.go`, `internal/initdb/catalog_cache.go`)
   only ever pass the implicit `DefaultDBOid`, since startup recovery is
   still single-database (multi-db startup replay is 4e/`CREATE DATABASE …
   TEMPLATE` territory, out of scope here), so this is currently
   unreachable dead weight rather than a live bug — flagged for whichever
   future loop makes startup recovery multi-db-aware.
3. **New scope surfaced by 4d-ii-part-2a's finding above — now fully LANDED:**
   `catalog.InMemory.CreateView`/`DropView`/`AllUserViews`/`AllUserMatViews`/
   `IndexesOnTable` each gained a `dbOid ...uint32` parameter (mirroring the
   4b-ii variadic pattern), and every call site in `operators_ddl.go` that
   references them now threads `catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`
   — this includes `execCreateView`, `execDropOneView`, `execDropOneMatView`,
   `execDropTable`'s RESTRICT/CASCADE dependency scans, and DROP SCHEMA
   CASCADE's view collection. Verified via
   `TestExecCreateViewRoutesToConnectionRealDBOid` and
   `TestIndexesOnTableFindsOwnDistinctDBOidTable` in
   `internal/executor/ddl_write_dbid_routing_test.go`.

   The `planCatalog()` half (the gap those two tests deliberately avoided by
   using a no-FROM view body) is now also fixed:
   `executor.ctxPlanCatalog` (`internal/executor/operators_ddl.go`) previously
   returned the bare, unwrapped `ctx.Catalog` whenever `ctx.PlanCatalog` was
   unset, so `planner.Plan`'s internal `LookupTable`/`LookupIndex` calls (no
   explicit dbOid argument) always resolved against `DefaultDBOid` — even
   though `server.sessionPlanCatalog`/`server.ctxPlanCatalog` already wire a
   dbOid-seeded `catalog.SearchPathCatalog` as `ctx.PlanCatalog` on every real
   connection (see `internal/server/dispatch.go`'s `sess != nil` block).
   The gap only reproduced through this package's own unit-test fixtures
   (`newVMFixture`/`runDDL`), which build a bare `*executor.Context` and
   never run that dispatch-layer wiring — but any other executor-internal
   caller that reaches `planner.Plan` via `ctxPlanCatalog` without a
   dispatch-supplied `PlanCatalog` had the same exposure. Fixed by having
   `ctxPlanCatalog`'s fallback branch wrap `ctx.Catalog` in a
   `catalog.WithSearchPath(...)` seeded with `ctx.CurrentDatabaseOid` (a
   `nil` schemas function; search-path fallback resolution is orthogonal to
   this fix) instead of returning it unwrapped, mirroring
   `sessionPlanCatalog`. Verified negatively — reverting only this one
   function change reproduces `42P01: relation "base" does not exist` on
   `CREATE VIEW v AS SELECT id, val FROM base` under a distinct dbOid — and
   positively via the new
   `TestExecCreateViewFromClauseRoutesToConnectionRealDBOid` regression test.

   Fixing `planCatalog()` surfaced a **second, closely related bug**:
   `addGroupByPKDeps` (`internal/executor/operators_ddl.go`, part of the
   `collectViewPKDeps` chain `execCreateView` calls to register
   view→PK-constraint GROUP-BY-functional-dependency deps) called
   `cat.IndexesOnTable(tbl)` with no `dbOid` argument, even though the
   function already receives one and threads it into the sibling
   `cat.LookupTable` call two lines above. On a distinct-dbOid connection
   this meant the base table's PK index could never be found, so
   `catalog.InMemory.RegisterViewConstraintDep` never fired — a view like
   `CREATE VIEW v AS SELECT id, count(*) FROM base GROUP BY id` silently
   lost its dependency tracking, meaning a subsequent `DROP CONSTRAINT ...
   RESTRICT` on `base`'s PK would incorrectly succeed instead of being
   blocked. This is the exact `collectViewPKDeps`/PK-deps-cluster fix that
   4d-ii-part-2a's white-box test was blocked on and had previously verified
   only by code inspection. Fixed by passing `dbOid` through to
   `IndexesOnTable`; verified via the new
   `TestExecCreateViewGroupByPKDepRegistersUnderDistinctDBOid` regression
   test, which asserts `catalog.InMemory.ViewsDependingOnConstraint` returns
   the view after `CREATE VIEW ... GROUP BY <pk-covering-cols>` under a
   distinct dbOid.

   All four tests live in `internal/executor/ddl_write_dbid_routing_test.go`.
   The former deferral-ledger row `AI-20260710-011513-001` /
   4d-ii-part-2b-item-1 is now resolved (see the ledger's `resolved` flip).

4. **Item 1, the cross-file sweep, is now fully landed, including the
   `applyworker.go` corner (2026-07-10).** Every remaining un-threaded
   `IndexesOnTable` call site got `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
   threaded through: `operators_fk.go` (1), `operators_cluster.go` (3),
   `operators_reindex.go` (2), `deferred_unique.go` (1), `context.go` (1),
   `operators_vacuum.go` (1), `operators_upsert.go` (4), `ssi.go` (5),
   `operators_storage.go` (5), and the ~24 remaining sites in
   `operators_ddl.go` left over from item 3's narrower scope.
   `operators_sequence.go` and `operators_pg_get_publication_tables.go` were
   re-measured and turned out to have zero `IndexesOnTable`/`AllUserViews`/
   `AllUserMatViews` call sites — no change needed there. `expr.go`'s
   `buildForeignKeyDefString` (pg_get_constraintdef's FK-constraint branch)
   gained a variadic `dbOid ...uint32` parameter, threaded from
   `ctx.CurrentDatabaseOid` at its one production call site.

   The `internal/planner` package's own `IndexesOnTable` call sites
   (`nl_index_join.go:655`, `planner.go:7379,8108,8225,11196,11400`) needed
   **no direct edits at all**. Instead, `catalog.SearchPathCatalog` gained a
   new `IndexesOnTable` override (mirroring its existing `LookupTable`/
   `LookupIndex` overrides) — before this addition, `SearchPathCatalog` had
   no override for `IndexesOnTable` at all, so any caller holding only a
   `catalog.Catalog` interface value (which every planner-package caller
   does — they all reach the catalog exclusively through `ctx.PlanCatalog`/
   `ctxPlanCatalog`, which item 3's fix above guarantees is always a
   dbOid-seeded `SearchPathCatalog`) silently promoted straight to the
   embedded `InMemory.IndexesOnTable` with no dbOid argument, always
   resolving DefaultDBOid regardless of `SearchPathCatalog.DBOid`. Adding
   the one override fixed all 6 planner-package call sites transparently —
   e.g. `resolveDefaultDoNothingArbiter`, which resolves the implicit
   arbiter index for a bare `ON CONFLICT DO NOTHING` (no explicit target).
   Verified as a real, previously-latent bug via new test
   `TestPlanUpsertDoNothingNoTargetFindsArbiterUnderDistinctDBOid`
   (`internal/executor/operators_upsert_test.go`), confirmed to fail
   (`ArbiterIndex` stays nil) against a revert of just the
   `SearchPathCatalog.IndexesOnTable` addition — without an arbiter index,
   `ON CONFLICT DO NOTHING`'s conflict probe never runs (`maintainArbiter`
   no-ops when `o.arbiterTree` is nil), so a genuine primary-key conflict
   would surface as an unhandled `23505` instead of being silently skipped.

   **`applyworker.go` corner LANDED (2026-07-10):** rather than threading a
   `dbOid` argument through each of `applyRelation`'s `w.cat.LookupTable`
   (line 217) and `primaryKeyOnlyRow`'s `cat.IndexesOnTable(tbl)`
   (~line 662) individually — which the prior loop noted would be a
   partial, inconsistent fix given how many `w.cat.*` call sites exist —
   `ApplyWorker.cat` itself is now constructed pre-wrapped in a
   dbOid-seeded `catalog.SearchPathCatalog`, so every un-dbOid-threaded
   call transparently resolves the subscription's own database via the
   `SearchPathCatalog.LookupTable`/`IndexesOnTable` overrides item 1's
   planner-package fix (above) already added. Three pieces:
   `catalog.Subscription` gained a `DBOid uint32` field, set by
   `PubSub.CreateSubscriptionAsOwner`'s new variadic `dbOid ...uint32`
   parameter (mirroring `CreateTable`/`CreateIndex`'s convention);
   `executor.execCreateSubscription` (`operators_ddl.go`) passes
   `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` at its one call site,
   and the `wal.EncodeCreateSubscription`/`DecodeCreateSubscription`
   record format gained a trailing 4-byte `dbOid` field (backward-compatible
   — a pre-existing on-disk WAL record with no trailer decodes as `dbOid=0`,
   i.e. `DefaultDBOid` via `NamespaceDBOid`, identical to the pre-fix
   default) so `internal/initdb/pubsub_ddl_recovery.go`'s replay path
   restores it too. `server.applyWorkerCatalog` (new helper,
   `internal/server/applylauncher.go`) wraps `cfg.Catalog` with
   `&catalog.SearchPathCatalog{Catalog: cat, DBOid: subDBOid}` and
   `DefaultLaunchApplyWorker` calls it before `executor.NewApplyWorker` —
   the one and only construction site (`sub catalog.Subscription` was
   already threaded all the way from `ApplyLauncher`'s
   `PubSub.Subscriptions()` scan into `LaunchApplyWorkerFunc`, so no
   further plumbing was needed there). Verified via new test
   `TestApplyWorkerAppliesInsertUnderDistinctSubscriptionDBOid`
   (`internal/executor/applyworker_test.go`), confirmed to fail — the
   applied row silently lands in the *wrong* (`DefaultDBOid`) same-named
   table instead of the subscription's own — against a revert of just the
   `SearchPathCatalog` wrap, exactly the cross-database aliasing hazard
   this corner was deferred over. `TestCreateSubscriptionRoutesToConnectionRealDBOid`
   / `TestCreateSubscriptionPostgresConnectionStaysOnDefaultDBOid`
   (`internal/executor/operators_ddl_pubsub_test.go`) and
   `TestApplyWorkerCatalogSeedsSubscriptionDBOid`
   (`internal/server/applylauncher_test.go`) cover the write-side routing
   and the helper's own contract respectively. The former deferral-ledger
   row for this corner is resolved (see the ledger's `resolved` flip).

### 4e — Cross-cutting fixups + the original motivation (in progress)

Once 4b-ii-4d land: dependent-object walks that assume a single global
namespace (FK target resolution, view dependency tracking, sequence
ownership), then finally the actual `CREATE DATABASE ... TEMPLATE` copy
mechanism (`CreateDatabaseUsingFileCopy`/`copydir`,
`postgres/src/backend/commands/dbcommands.c`) this whole epic exists to
unblock, and the AC-002/DU-002 dump+restore round-trip probe already
staged in `internal/testport/pgdump_connsetup_test.go` (a soft `t.Logf`
today, ready to become a hard assertion once this lands).

**FK target resolution — LANDED (2026-07-10).** Five call sites in
`internal/executor/operators_fk.go`/`operators_ddl.go` resolved the FK's
referenced/child table via `LookupTable`/`AllTables` with no `dbOid`
argument, always keying against `DefaultDBOid` regardless of the connection's
real database (all five already had the `dbOid ...uint32` parameter
available on the callee — this was purely a threading gap, not a signature
change, mirroring 4d-ii-part-2b item 1's pattern):

- `assertParentExists` (`operators_fk.go`) — the INSERT/UPDATE-time
  FK-parent-exists check. Before the fix, a missing lookup meant the
  "referenced table not found (CREATE TABLE out of order) — skip" branch
  fired for every insert on a distinct-dbOid connection, silently disabling
  FK enforcement entirely rather than rejecting rows with no matching
  parent. Highest blast radius — this is the hot path.
- `assertNoChildRows` (`operators_fk.go`) — only affected the parent
  primary-key column names in the FK-RESTRICT error's DETAIL line, not
  enforcement itself.
- `runAllDeferredFKChecks` (`operators_fk.go`) — resolved the child table for
  an `INITIALLY DEFERRED` check run at COMMIT; a miss took the `continue`
  branch, silently skipping the deferred violation check.
- `checkFKColumnTypeCompatibility` (`operators_fk.go`) — resolved the
  referenced table for enum-type FK compatibility checking at CREATE
  TABLE/ADD CONSTRAINT time; the very next line already threaded
  `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` into `IndexesOnTable`,
  which is what made this half-migrated site easy to spot.
- `execTruncate`'s CASCADE expansion (`operators_ddl.go`) — built its
  whole-catalog FK-referencer set via `im.AllTables()` with no dbOid,
  scanning `DefaultDBOid`'s (typically empty, for a distinct-dbOid
  connection) namespace instead of the connection's own — so `TRUNCATE ...
  CASCADE` silently failed to cascade to a real same-database referencing
  table, leaving its rows behind as dangling references to the
  just-truncated parent. Whole-database-scan blast radius.

New tests `TestAssertParentExistsFindsOwnDistinctDBOidParent` and
`TestExecTruncateCascadeFindsOwnDistinctDBOidReferencingTable`
(`internal/executor/fk_dbid_routing_test.go`) cover the two highest-severity
sites end-to-end (INSERT rejection and TRUNCATE CASCADE reach), each
confirmed to fail against a revert of just the FK-fix diff. Writing these
required a new test-only planning helper (`runDMLUnderDBOid`/
`runQueryUnderDBOid` in the same file): the package's existing `runDDL`/
`runQuery` helpers call `planner.Plan(stmts[0], ctx.Catalog)` with the raw,
un-wrapped catalog, which resolves table names for statements needing
planning-time name resolution (INSERT's target table, SELECT's FROM
table) against `DefaultDBOid` only — unlike the real server, which always
plans through `sessionPlanCatalog`/`ctxPlanCatalog`'s dbOid-seeded
`SearchPathCatalog` wrapper. This is a test-harness gap, not a production
bug (confirmed by reading `internal/server/dispatch.go`'s planning call
sites), but it means any *future* dbOid-routing test in this package that
needs a table resolved by name during planning (as opposed to CREATE
TABLE/CREATE VIEW's outer-statement plan, which needs no such resolution)
must use the new helper, not plain `runDDL`/`runQuery`.

**Sequence ownership — LANDED (2026-07-10).** `seqRegistry`'s key scheme
(`internal/executor/operators_sequence.go`) now embeds the owning dbOid:
`seqKey` composes `"<dbOid>:<normalised name>"` instead of the bare name, and
`seqState` gained an immutable `dbOid uint32` field so the two Range-based
scans (`FindSequenceOwnedBy`, `DropSequencesOwnedByTable`, `AllSequenceInfos`)
filter by dbOid instead of matching every entry in the process-global map.
Every one of the ~18 public entry points (`RegisterSequence`,
`SetSequenceDataType/Cache/Temporary/OwnedBy/ColumnMarker`, `LookupSequence`,
`DropSequence`, `RenameSequence`, `SequenceRowData/OwnedBy`,
`FindSequenceOwnedBy`, `DropSequencesOwnedByTable`, `ResetSequence`,
`GetSequenceCurrentValue`, `SetSequenceCurrentValue`, `UpdateSequenceParams`,
`AllSequenceInfos`) gained the same trailing `dbOid ...uint32` convention as
the catalog's own `resolveDBOid` (mirrored as a package-local
`resolveSeqDBOid`), so every pre-existing zero-arg call site keeps resolving
to exactly DefaultDBOid. All ~45 call sites across `operators_ddl.go`
(CREATE/ALTER/DROP/RENAME SEQUENCE, implicit SERIAL-column registration,
DROP TABLE's owned-sequence cascade, TRUNCATE ... RESTART IDENTITY, ALTER
TABLE/SCHEMA rename cascades), `operators_tx.go` (RESTART IDENTITY rollback —
`SeqRestoreEntry` gained a `DBOid` field so `ProcessRollbackUndos` restores
into the right database), `expr.go` (`pg_get_serial_sequence`, `currtid2`),
and `operators_sequence.go`'s own `evalNextval/evalCurrval/evalSetval/
evalLastval/autoGenerateSerialValues` were threaded with
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` (a new `ctxSeqDBOid` helper
for the `eval*` family). `evalGenExpr`/`evalGenFuncCall`/
`applyDefaultsForMissing` (`operators_generated.go`, the `DEFAULT
nextval(...)` expression-evaluator path, distinct from
`autoGenerateSerialValues`'s SERIAL/IDENTITY path) gained the same trailing
variadic and are threaded from their 3 real call sites with a live
`ctx`/dbOid (`applyworker.go`'s `applyInsert` via a new `ApplyWorker.dbOid()`
helper that reads `w.cat`'s `SearchPathCatalog.DBOid`, `operators_storage.go`,
`operators_upsert.go`); `computeGeneratedColumns`'s ~14 call sites were left
zero-arg deliberately — a `GENERATED ALWAYS AS (...) STORED` expression must
be `IMMUTABLE` in PostgreSQL, so it can never legally contain `nextval()`,
making dbOid-threading there unreachable-code churn with no behavioral
payoff. `wal.SequenceStatePayload` gained a trailing-appended `DBOid` field
(`EncodeSequenceState`/`DecodeSequenceState`) and `EncodeDropSequence`/
`DecodeDropSequence` gained a trailing 4-byte dbOid, both following the
`EncodeCreateSubscription` backward-compatible-trailer pattern (a pre-4e WAL
record with no trailer decodes as dbOid 0 → DefaultDBOid via
`NamespaceDBOid`, identical to the pre-4e default).
`internal/initdb/sequence_ddl_recovery.go`'s replay `live` map (used to dedupe
last-record-wins per sequence before the catalog-marker fixup pass) was
re-keyed from bare name to `"<dbOid>:<name>"` for the same reason — two
distinct databases' same-named sequences would otherwise dedupe onto one
replay entry. New tests
`TestSerialSequenceDoesNotAliasAcrossDistinctDBOid` and
`TestDropTableDoesNotCascadeSequenceAcrossDistinctDBOid`
(`internal/executor/sequence_dbid_routing_test.go`) prove two same-named
SERIAL-backed tables under distinct connection dbOids get independent
counters (a same-key collision under the old scheme silently reset the other
database's already-live counter, since `RegisterSequence` unconditionally
overwrites) and that a same-named table's owned sequence in one database
survives a `DROP TABLE` cascade in a different database; both confirmed to
fail against a revert of just `seqKey`'s dbOid component (a same-signature
neutered `seqKey` that folds the dbOid argument to a no-op, so the revert
isolates the key-scheme fix from the ~45 call-site threading, which is
mechanically inert without it).
**Deliberately still DefaultDBOid-only (separate, pre-existing gap, not
part of this item):** the `pg_sequence`/`pg_sequences` virtual-row builders
(`internal/catalog/catalog.go`'s `pgSequence.VirtualRows`,
`internal/initdb/pg_sequences_view.go`,
`internal/initdb/information_schema_sequences_view.go`) call
`SequenceParamsFunc`/`AllSequenceInfos` with no dbOid because their own
`VirtualRows func() [][]string` closures have no per-connection context to
draw one from — the same "per-connection virtual catalog scoping" mechanism
already solved for other virtual tables (thread DBName → Context, a
per-connection lister in `valuesOp.Open`) has not yet been applied to these
two. `sequenceParamsForCatalog` and the two `AllSequenceInfos()` call sites
still default to DefaultDBOid, matching their pre-4e behavior exactly (no
regression, but `SELECT * FROM pg_sequences` on a distinct-dbOid connection
still only sees DefaultDBOid's sequences).
**View constraint-dependency tracking — LANDED (2026-07-10).**
`c.constraintViewDeps map[string][]string` (`internal/catalog/catalog.go`,
field declared near line 2280) is a single flat field on `InMemory`, keyed
`"tableOID:constraintName"` (OID half was already safe — table OIDs are
globally unique, a single cluster-wide `nextOID` counter) → `[]viewName`;
the *value* was a bare, unqualified name with no dbOid. Its own
`UnregisterViewConstraintDeps`, called from `execDropOneView` on every
`DROP VIEW`, matched and removed entries **by bare name across every key in
the whole map** — so `DROP VIEW v` in database A also stripped a
same-named, unrelated view `v` in database B out of the map, silently
disabling that other database's `DROP CONSTRAINT RESTRICT` protection. A
concrete cross-database data-corruption path, not just a lookup miss.
`execCreateView`'s registration call site
(`im.RegisterViewConstraintDep(viewKey, ...)`, `viewKey := s.Name.String()`)
fed the same unqualified-name issue on the write side. Fixed by qualifying
the stored value itself: both `RegisterViewConstraintDep` and
`UnregisterViewConstraintDeps` gained the same trailing `dbOid ...uint32`
convention as every other 4e entry point (`resolveDBOid`), and the value
stored/matched is now `"<dbOid>:<viewName>"` rather than the bare name.
`ViewsDependingOnConstraint` (the RESTRICT-check read path) needed no
signature change — a dependent view can only live in its table's own
database, so `tableOID` alone already pins the result to one database — but
its output now strips the `dbOid:` prefix before returning, since callers
only use the bare name for the RESTRICT error message. The two call sites in
`internal/executor/operators_ddl.go` (`execCreateView`'s registration,
`execDropOneView`'s cleanup) now pass
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`. New test
`TestDropViewDoesNotEraseConstraintDepAcrossDistinctDBOid`
(`internal/executor/ddl_write_dbid_routing_test.go`) creates a same-named
view `v` in two distinct-dbOid databases, drops one, and asserts the other's
dependency entry survives; confirmed to fail against a revert of just the
value-qualification (a same-signature neutered qualifier that folds dbOid to
a constant, isolating the key-scheme fix from the two call-site threads,
which are mechanically inert without it). Everything else
view-dependency-related
(`CreateView`/`AllUserViews`/`AllUserMatViews`/`IndexesOnTable`/
`planCatalog()`/`viewsDependingOnView`/`viewsDependingOnTable`/
`matViewsDependingOnRelation`/`collectViewPKDeps`/`addGroupByPKDeps`) was
already dbOid-threaded — confirmed by inspection, not re-flagged.

**`CREATE DATABASE ... TEMPLATE` bounded validation landed (2026-07-10).**
`internal/server/database_ddl.go` gained `createDatabaseTemplateName` (parses
the `TEMPLATE [=] <name>` option, applied only to the substring after the new
database's own name via the new `extractFirstIdentifierSpan`, so a name that
happens to start with "template" — e.g. `CREATE DATABASE template_scratch` —
is never mistaken for the option) and `resolveCreateDatabaseTemplate`, called
from `tryHandleDatabaseDDL`'s `databaseDDLCreate` case before the target
database is allocated. It enforces the two checks goopg can honor without the
real copy mechanism: the template must exist (`ResolveDatabaseOid`,
mirroring dbcommands.c createdb()'s `ERRCODE_UNDEFINED_DATABASE`), and it
must have zero USER relations (`AllTables`, filtered to
`!catalog.IsSystemRelation`) — the only case a database that isn't actually
copied can still be semantically correct. Before this, a non-empty TEMPLATE
fell through classifyDatabaseDDL's "trailing options are ignored" looseness
and silently created an empty database instead of copying anything, a real
data-loss-shaped mismatch with PostgreSQL; that is now a typed
`FeatureNotSupported` (0A000) error instead.

The emptiness check is skipped entirely when the template resolves to
`catalog.DefaultDBOid` (1) — discovered live via
`TestSimpleQueryDropDatabaseActuallyDrops`'s failure during this loop's
verification pass: `"template1"` and `"postgres"` both alias that one shared
oid (the pre-existing dual-mirror this doc's 4c/4d sections document), which
every fixture and pre-4c code path still writes real user tables into, so
the check would misfire on any server that has ever created a table — the
overwhelmingly common case, including the default no-TEMPLATE-clause path.
Skipping there exactly preserves the pre-existing "silently produce an empty
database" behavior for `template1`/`postgres`/no-clause (unchanged from
before this loop); the new strict check only applies to a template that
resolves to its own distinct, `CreateDatabase`-allocated dbOid (any other
registered database) or `template0`'s fixed, never-aliased oid 4 — the only
oids where "is this template empty" is actually a reliable question. Tests:
`TestCreateDatabaseTemplateName`,
`TestTryHandleDatabaseDDLCreateTemplateDoesNotExistErrors`,
`TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds`,
`TestTryHandleDatabaseDDLCreateNonEmptyTemplateErrors`
(`internal/server/database_ddl_test.go`), each of the latter two negative
cases confirmed to fail against a revert of just the
`resolveCreateDatabaseTemplate` call site. Live-verified against the real
`cmd/goopg` binary via `psql`: `CREATE DATABASE foo` and
`CREATE DATABASE foo TEMPLATE template1` still succeed on a server that
already has tables in `postgres`; `CREATE DATABASE foo TEMPLATE <empty db>`
succeeds; `CREATE DATABASE foo TEMPLATE <db with a table>` fails with the new
0A000; `CREATE DATABASE foo TEMPLATE nosuchdb` fails with 3D000; a table
created in a freshly `CREATE DATABASE`'d database is correctly invisible from
`postgres`'s own `pg_class` query (real per-database isolation, not just a
name registered in `pg_database`).

**Residual gap noticed live during a prior loop's manual verification — FIXED
2026-07-10.** A connection to any newly `CREATE DATABASE`'d database
(unrelated to TEMPLATE — reproduced identically on a plain empty target)
could not query `pg_class`/run `psql`'s `\dt` at all (`ERROR: relation
"pg_class" does not exist`), even though DML/DDL against real tables in that
database worked and was correctly isolated from other databases. Root cause,
confirmed by a background Explore pass: `registerSystemTables` registers
every `pg_catalog`/`information_schema` virtual table exactly once, under
`DefaultDBOid`'s namespace only, and `CREATE DATABASE` never seeds a fresh
namespace with references to them — so `catalog.InMemory.LookupTable`
(via `SearchPathCatalog.effectiveDBOid`) simply could not find the name
`pg_class` in a genuinely distinct dbOid's (empty) namespace. Two-part fix:
(1) generic name-resolution fallback — `LookupTable` now falls back to
`DefaultDBOid`'s namespace, via new `lookupSystemCatalogTableLocked`, whenever
a name is schema-qualified `pg_catalog`/`information_schema` (or unqualified
and found there under one of those schemas) — this alone unblocks name
resolution for all ~70 system-catalog virtual tables plus the two heap-backed
ones (`pg_attribute`/`pg_type`), scoped tightly enough that a distinct dbOid's
connection still can never see `DefaultDBOid`'s real *user* tables; (2)
`pg_class`-specific row-content fix — its `VirtualRows` closure body was
extracted into an exported `catalog.InMemory.PGClassRowsForDBOid(dbOid
uint32) [][]string`, wired to a new per-connection
`executor.Context.PgClassRows` field (mirroring the existing `ExtensionRows`
pattern, set in `internal/server/dispatch.go`'s `wireExtensionRows`) so
`pg_class` now lists the CONNECTING database's own tables/indexes rather than
always `DefaultDBOid`'s. Live-verified end-to-end against a real `cmd/goopg` +
`psql`: `CREATE DATABASE freshdb` → connect → `pg_class` query succeeds → `\dt`
shows only that database's own table. See the 2026-07-10 deferral-ledger row
("pg_class-under-fresh-database") for the ~13 sibling virtual-table builders
(`pg_indexes`, `pg_tables`, `pg_constraint`, etc.) that are name-resolution-
fixed by part (1) above but still need part (2)'s row-content treatment
individually — each is its own bounded future loop.

**`pg_indexes` / `pg_tables` row-content — FIXED 2026-07-10 (same day, next
loop).** The two next-highest-value sibling builders from the list above (both
directly probed by HammerDB's checkschema step and psql's `\d`/`\dt` family)
got the same part-(2) closure-extraction treatment: `catalog.InMemory`
gained `PGIndexesRowsForDBOid(dbOid uint32)` / `PGTablesRowsForDBOid(dbOid
uint32)`, wired to new per-connection `executor.Context.PgIndexesRows`/
`PgTablesRows` fields (mirroring `PgClassRows`, set in
`internal/server/dispatch.go`'s `wireExtensionRows`), consumed by new
`tbl.Name == "pg_indexes"`/`"pg_tables"` branches in
`internal/executor/operators.go`'s `valuesOp.Open`. Live-verified end-to-end:
`CREATE DATABASE freshdb2` → connect → `CREATE TABLE only_in_freshdb2 (id int
PRIMARY KEY, ...)` → `pg_tables`/`pg_indexes` show only that table/its PK
index in `freshdb2`, and 0 rows for it when queried from `postgres`. See the
2026-07-10 deferral-ledger row ("pg_indexes/pg_tables per-dbOid content") for
the 11 remaining sibling builders (`pg_attrdef`, `pg_constraint`,
`pg_inherits`, `pg_index`, `pg_statistic_ext`, `pg_policy`, `pg_depend`,
`pg_trigger`, `pg_rewrite`, `information_schema.routines`/`parameters`/
`routine_*_usage`, `pg_foreign_table`) still needing this treatment — each is
its own bounded future loop; `pg_constraint`/`pg_depend`/`pg_index` are next
highest-value (pg_dump/psql `\d` catalog joins).

**`pg_constraint` row-content — FIXED 2026-07-10 (same day, next loop).** The
next highest-value sibling builder from the list above (directly probed by
pg_dump's `getConstraints`/`getDomainConstraints` and psql's `\d` constraint
listing) got the same part-(2) closure-extraction treatment: its `VirtualRows`
closure — the largest of these builders, emitting CHECK / UNIQUE·PK·EXCLUDE /
NOT NULL / FOREIGN KEY rows in four separate passes over ~300 lines — was
extracted into `catalog.InMemory.PGConstraintRowsForDBOid(dbOid uint32)
[][]string`, parameterizing its table/index namespace lookups on `dbOid` while
deliberately leaving `c.domains` (domain CHECK constraints) global, matching
the same not-yet-namespace-scoped precedent this doc already documents for
composite types in the `pg_class` section above. Wired to a new
per-connection `executor.Context.PgConstraintRows` field (mirroring
`PgIndexesRows`/`PgTablesRows`, set in `internal/server/dispatch.go`'s
`wireExtensionRows`), consumed by a new `tbl.Name == "pg_constraint"` branch in
`internal/executor/operators.go`'s `valuesOp.Open`. Live-verified end-to-end:
`CREATE DATABASE freshdb3` → connect → `CREATE TABLE only_in_freshdb3 (id int
PRIMARY KEY, val int CHECK (val > 0))` → `pg_constraint` in `freshdb3` shows
only that table's 3 constraints (pkey/check/not-null) at its own dbOid-local
OIDs, and only `postgres`'s own constraints when queried from `postgres`. See
the 2026-07-10 deferral-ledger row ("pg_constraint per-dbOid content") for the
10 remaining sibling builders (`pg_attrdef`, `pg_inherits`, `pg_index`,
`pg_statistic_ext`, `pg_policy`, `pg_depend`, `pg_trigger`, `pg_rewrite`,
`information_schema.routines`/`parameters`/`routine_*_usage`,
`pg_foreign_table`) still needing this treatment — each is its own bounded
future loop.

**`pg_index` row-content — FIXED 2026-07-10 (same day, next loop).** The next
highest-value sibling builder from the list above (pg_dump's index-metadata
queries and psql's `\d`/`indexrelid::regclass` catalog joins) got the same
part-(2) closure-extraction treatment: its `VirtualRows` closure was extracted
into `catalog.InMemory.PGIndexRowsForDBOid(dbOid uint32) [][]string`,
threading `dbOid` through `c.AllIndexes(dbOid)` (already dbOid-parameterized,
just unused by this closure until now) and through `c.toastBearingTables`,
whose signature gained a required `dbOid uint32` param (its only caller was
this closure, so no variadic-compat shim was needed). Wired to a new
per-connection `executor.Context.PgIndexRows` field (mirroring
`PgConstraintRows`, set in `internal/server/dispatch.go`'s
`wireExtensionRows`), consumed by a new `tbl.Name == "pg_index"` branch in
`internal/executor/operators.go`'s `valuesOp.Open`. Live-verified end-to-end:
`CREATE DATABASE freshidx1` → connect → `CREATE TABLE only_in_freshidx1 (id
int PRIMARY KEY, val int)` + `CREATE INDEX ... ON only_in_freshidx1(val)` →
raw `pg_index` in `freshidx1` shows exactly those 2 index rows at their own
dbOid-local OIDs, and only `postgres`'s own index row when queried from
`postgres`. Collateral discovery during this verification (NOT fixed, its own
deferral-ledger row): `oid::regclass` (the OID→name cast direction) still
resolves against `DefaultDBOid`'s `pg_class` only, so it silently prints the
bare numeric OID instead of a name for objects in any other database — a
separate cast/output-function mechanism from the `VirtualRows`-closure gap
this design doc's sub-slice tracks. See the 2026-07-10 deferral-ledger row
("pg_index per-dbOid content") for the 9 remaining sibling builders
(`pg_attrdef`, `pg_inherits`, `pg_statistic_ext`, `pg_policy`, `pg_depend`,
`pg_trigger`, `pg_rewrite`, `information_schema.routines`/`parameters`/
`routine_*_usage`, `pg_foreign_table`) still needing this treatment — each is
its own bounded future loop.

**`pg_attrdef` + `pg_depend` row-content — FIXED 2026-07-10 (same day, next
loop, follow-up 27).** These two builders are done together rather than
separately, unlike every prior sibling in this list: `dependVirtualRows`'s own
doc comment already stated "`attrDefRowsLocked` builds the deterministic row
set so this view and `dependVirtualRows` agree on the oids" — a SERIAL
column's implicit default registers a NORMAL ('n') `pg_depend` row
(`classid=2604`, `objid`=the `pg_attrdef` row's own oid, `refobjid`=the owned
sequence's OID) that must reference the exact same oid numbering
`pg_attrdef` itself emits, so fixing one without the other would desync them
under a non-default dbOid. `attrDefRowsLocked` (no dbOid parameter, hardcoded
to `c.ns(DefaultDBOid)`) became `attrDefRowsLockedForDBOid(dbOid uint32)`;
both call sites — the extracted `catalog.InMemory.PGAttrdefRowsForDBOid(dbOid
uint32) [][]string` and the extracted `catalog.InMemory.
PGDependRowsForDBOid(dbOid uint32) [][]string` — now call it with the SAME
dbOid. `PGDependRowsForDBOid` leaves two dbOid-agnostic pieces unfixed by
design, matching this list's `pg_constraint`-leaves-`c.domains`-global
precedent: the sequence-ownership ('a' deptype) lookup goes through the
package-level `catalog.SequenceParamsFunc(qualifiedName string) (SeqParams,
bool)` — no dbOid parameter exists on that function at all, so it cannot be
threaded through from here without its own signature change — and the AM
operator-class member rows (`c.amOpMembers`/`c.amProcMembers`, not yet
namespace-scoped anywhere). Wired to new per-connection `executor.Context.
PgAttrdefRows`/`PgDependRows` fields (mirroring `PgIndexRows`, set in
`internal/server/dispatch.go`'s `wireExtensionRows`), consumed by new
`tbl.Name == "pg_attrdef"`/`"pg_depend"` branches in
`internal/executor/operators.go`'s `valuesOp.Open`. Tests:
`TestPgAttrdefRowsScopedToConnectionDBOid`,
`TestPgDependRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`) — the latter asserts on the 'n'
attrdef→sequence row rather than the 'a' OWNED-BY row, since the 'a' row is
exactly the SequenceParamsFunc-gated class just described. Live-verified
end-to-end: `CREATE DATABASE freshdep1` → connect → `CREATE TABLE
only_in_freshdep1 (id serial PRIMARY KEY)` → raw `pg_attrdef`/`pg_depend
WHERE classid=2604` in `freshdep1` show exactly that table's own default/oid
(`adrelid`/`refobjid` = `freshdep1`'s own table/sequence OIDs), and only
`postgres`'s own rows when queried from `postgres` — no cross-database leak
either direction. Confirmed collaterally (not a regression — the `pg_depend`
view was DefaultDBOid-only before this loop too, so this class of row was
already never correct for a non-default database, just differently wrong):
querying `pg_depend WHERE classid=1259` (the 'a' OWNED-BY class) from
`freshdep1` now correctly returns **zero** rows (no cross-db leak) rather
than leaking `postgres`'s row, but it should have one row of its own — this
residual gap folds into the existing "sequence ownership follow-on" ledger
entry (same `SequenceParamsFunc`-lacks-a-dbOid-parameter root cause as
`pg_sequences`/`pg_sequence`), not a new row. See the 2026-07-10
deferral-ledger row ("pg_attrdef/pg_depend per-dbOid content") for the 8
remaining sibling builders (`pg_inherits`, `pg_statistic_ext`, `pg_policy`,
`pg_trigger`, `pg_rewrite`, `information_schema.routines`/`parameters`/
`routine_*_usage`, `pg_foreign_table`) still needing this treatment — each is
its own bounded future loop.

**`pg_inherits` row-content — FIXED 2026-07-10 (same day, next loop, follow-up
28).** Single-builder fix, no cross-builder oid-numbering coupling like the
`pg_attrdef`/`pg_depend` pair had. `pg_inherits.VirtualRows`'s inline closure
(two `c.ns(DefaultDBOid)` loops: partition/legacy-inheritance table
parent-child rows, then partitioned-index parent-child rows) was extracted
into new exported `catalog.InMemory.PGInheritsRowsForDBOid(dbOid uint32)
[][]string`, parameterizing both `c.ns(DefaultDBOid)` references to
`c.ns(dbOid)`. The registered closure now just calls
`PGInheritsRowsForDBOid(DefaultDBOid)`, byte-identical default behavior.
Wired new per-connection `executor.Context.PgInheritsRows func() [][]string`
field (mirroring `PgDependRows`), set in `internal/server/dispatch.go`'s
`wireExtensionRows` (new `pgInheritsRowLister` interface), consumed by a new
`tbl.Name == "pg_inherits"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. Test `TestPgInheritsRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), mirroring
`TestPgDependRowsScopedToConnectionDBOid`: a `PARTITION BY RANGE`/`PARTITION
OF` parent-child pair under each of two distinct dbOids, never cross-leak
their `pg_inherits` rows (by `inhrelid`). Live end-to-end verified against a
real `cmd/goopg` binary + real `psql`: `CREATE DATABASE freshinh1` → connect
→ `CREATE TABLE part_a (id int) PARTITION BY RANGE(id); CREATE TABLE
part_a_p1 PARTITION OF part_a FOR VALUES FROM (1) TO (100)` → `freshinh1`'s
`pg_inherits` shows exactly its own 1 row while `postgres`'s shows 0; then a
second, distinct partition pair created in `postgres` shows exactly its own 1
row while `freshinh1`'s stays unchanged — no cross-database leak either
direction. Collaterally confirmed the `oid::regclass` cast gap is still open
and unrelated to this fix (`inhrelid::regclass` printed the raw OID, not the
table name, on the live server). See the 2026-07-10 deferral-ledger row
("pg_inherits per-dbOid content") for the 7 remaining sibling builders
(`pg_statistic_ext`, `pg_policy`, `pg_trigger`, `pg_rewrite`,
`information_schema.routines`/`parameters`/`routine_*_usage`,
`pg_foreign_table`) still needing this treatment — each is its own bounded
future loop.

**`pg_policy` row-content — FIXED 2026-07-10 (same day, next loop, follow-up
29).** Single-builder fix, no cross-builder oid-numbering coupling like the
`pg_attrdef`/`pg_depend` pair had. `pg_policy.VirtualRows`'s inline closure
(one `c.ns(DefaultDBOid)` loop over every table's `Policies`) was extracted
into new exported `catalog.InMemory.PGPolicyRowsForDBOid(dbOid uint32)
[][]string`, parameterizing the `c.ns(DefaultDBOid)` reference to
`c.ns(dbOid)`. The registered closure now just calls
`PGPolicyRowsForDBOid(DefaultDBOid)`, byte-identical default behavior. Wired
new per-connection `executor.Context.PgPolicyRows func() [][]string` field
(mirroring `PgInheritsRows`), set in `internal/server/dispatch.go`'s
`wireExtensionRows` (new `pgPolicyRowLister` interface), consumed by a new
`tbl.Name == "pg_policy"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. Test `TestPgPolicyRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), mirroring
`TestPgInheritsRowsScopedToConnectionDBOid`: a `CREATE POLICY ... USING`
under each of two distinct dbOids, never cross-leak their `pg_policy` rows
(by `polname`). Live end-to-end verified against a real `cmd/goopg` binary +
real `psql`: `CREATE DATABASE polA` / `CREATE DATABASE polB` → `polA`:
`CREATE TABLE ta (a int); CREATE POLICY pol_a ON ta USING (a > 0)` → `polB`:
`CREATE TABLE tb (a int); CREATE POLICY pol_b ON tb USING (a > 0)` →
`SELECT polname FROM pg_policy` in `polA` returns exactly `pol_a`, the same
query in `polB` returns exactly `pol_b` — no cross-database leak either
direction. See the 2026-07-10 deferral-ledger row ("pg_policy per-dbOid
content") for the 6 remaining sibling builders (`pg_statistic_ext`,
`pg_trigger`, `pg_rewrite`, `information_schema.routines`/`parameters`/
`routine_*_usage`, `pg_foreign_table`) still needing this treatment — each is
its own bounded future loop.

**`pg_trigger` row-content — FIXED 2026-07-10 (same day, next loop, follow-up
30).** Single-builder fix, identical shape to `pg_policy`'s above (no
cross-builder oid-numbering coupling). `pg_trigger.VirtualRows`'s inline
closure (one `c.ns(DefaultDBOid)` loop over every table's `Triggers`) was
extracted into new exported `catalog.InMemory.PGTriggerRowsForDBOid(dbOid
uint32) [][]string`, parameterizing the `c.ns(DefaultDBOid)` reference to
`c.ns(dbOid)`. The registered closure now just calls
`PGTriggerRowsForDBOid(DefaultDBOid)`, byte-identical default behavior. Wired
new per-connection `executor.Context.PgTriggerRows func() [][]string` field
(mirroring `PgPolicyRows`), set in `internal/server/dispatch.go`'s
`wireExtensionRows` (new `pgTriggerRowLister` interface), consumed by a new
`tbl.Name == "pg_trigger"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. Test `TestPgTriggerRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), mirroring
`TestPgPolicyRowsScopedToConnectionDBOid`: `CREATE FUNCTION ... RETURNS
trigger` + `CREATE TRIGGER ... BEFORE INSERT` under each of two distinct
dbOids, never cross-leak their `pg_trigger` rows (by `tgname`). Live
end-to-end verified against a real `cmd/goopg` binary + real `psql`: `CREATE
DATABASE trgA` / `CREATE DATABASE trgB` → `trgA`: `CREATE TABLE ta (a int);
CREATE FUNCTION fn_a() RETURNS trigger ...; CREATE TRIGGER trig_a BEFORE
INSERT ON ta ... EXECUTE FUNCTION fn_a()` → `trgB`: same pattern with
`tb`/`fn_b`/`trig_b` → `SELECT tgname FROM pg_trigger` in `trgA` returns
exactly `trig_a`, the same query in `trgB` returns exactly `trig_b` — no
cross-database leak either direction. See the 2026-07-10 deferral-ledger row
("pg_trigger per-dbOid content") for the 5 remaining sibling builders
(`pg_statistic_ext`, `pg_rewrite`, `information_schema.routines`/`parameters`/
`routine_*_usage`, `pg_foreign_table`) still needing this treatment —
`pg_rewrite` is the cleanest next single-builder pick (identical shape); each
is its own bounded future loop.

**`pg_rewrite` row-content — FIXED 2026-07-10 (same day, next loop, follow-up
31).** Single-builder fix, identical shape to `pg_trigger`'s above (no
cross-builder oid-numbering coupling). `pg_rewrite.VirtualRows`'s inline
closure (one `c.ns(DefaultDBOid)` loop over every table's `Rules`) was
extracted into new exported `catalog.InMemory.PGRewriteRowsForDBOid(dbOid
uint32) [][]string`, parameterizing the `c.ns(DefaultDBOid)` reference to
`c.ns(dbOid)`. The registered closure now just calls
`PGRewriteRowsForDBOid(DefaultDBOid)`, byte-identical default behavior. Wired
new per-connection `executor.Context.PgRewriteRows func() [][]string` field
(mirroring `PgTriggerRows`), set in `internal/server/dispatch.go`'s
`wireExtensionRows` (new `pgRewriteRowLister` interface), consumed by a new
`tbl.Name == "pg_rewrite"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. Test `TestPgRewriteRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), mirroring
`TestPgTriggerRowsScopedToConnectionDBOid`: `CREATE RULE ... AS ON INSERT TO
... DO INSTEAD NOTHING` under each of two distinct dbOids, never cross-leak
their `pg_rewrite` rows (by `rulename`). Live end-to-end verified against a
real `cmd/goopg` binary + real `psql`: `CREATE DATABASE rwA` / `CREATE
DATABASE rwB` → `rwA`: `CREATE TABLE ta (a int); CREATE RULE rule_a AS ON
INSERT TO ta DO INSTEAD NOTHING` → `rwB`: same pattern with `tb`/`rule_b` →
`SELECT rulename FROM pg_rewrite` in `rwA` returns exactly `rule_a`, the same
query in `rwB` returns exactly `rule_b` — no cross-database leak either
direction. See the 2026-07-10 deferral-ledger row ("pg_rewrite per-dbOid
content") for the 3 remaining sibling builders (`pg_statistic_ext`,
`information_schema.routines`/`parameters`/`routine_*_usage`,
`pg_foreign_table`) still needing this treatment — each is its own bounded
future loop.

**`pg_foreign_table` row-content fixed (2026-07-10, follow-up 32):** the
simplest of the 3 remaining sibling builders — `pg_foreign_table.VirtualRows`
already had the exact same shape as `pg_rewrite`'s (a sorted scan over
`c.ns(DefaultDBOid).tables` filtering on `t.ForeignServerName != ""`), so it
got the identical closure-extraction treatment: new
`catalog.InMemory.PGForeignTableRowsForDBOid(dbOid uint32)`, new
per-connection `executor.Context.PgForeignTableRows func() [][]string` field,
wired in `internal/server/dispatch.go`'s `wireExtensionRows` (new
`pgForeignTableRowLister` interface), consumed by a new `tbl.Name ==
"pg_foreign_table"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. Test `TestPgForeignTableRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), mirroring
`TestPgRewriteRowsScopedToConnectionDBOid`: `CREATE SERVER` + `CREATE FOREIGN
TABLE ... SERVER ...` under each of two distinct dbOids, never cross-leak
their `pg_foreign_table` rows (matched by `ftrelid`, since the row itself
carries no table name column). One caveat carried forward unfixed: the
`ftserver` OID lookup (`c.foreignServers[t.ForeignServerName]`) still resolves
against a single process-global `map[string]*ForeignServer` with no dbOid key
— a `CREATE SERVER` of the same name in two different databases is a latent
name collision, and only the row-content per-dbOid scoping is fixed here (same
shape as `pg_statistic_ext`'s `c.statisticsObjs` gap noted above). See the
2026-07-10 deferral-ledger row ("pg_foreign_table per-dbOid content") for this
follow-on and the 2 remaining sibling builders (`pg_statistic_ext`,
`information_schema.routines`/`parameters`/`routine_*_usage`) still needing
the bigger registry-dbOid-key treatment.

**`oid::regclass`/`'name'::regclass` cast direction fixed (2026-07-10, follow-up
33):** the collateral discovery from follow-up 26 ("`oid::regclass` … returns
the bare numeric OID instead of resolving a name for objects in a non-
`DefaultDBOid` database") — a cast/output-function gap, not a `VirtualRows`
closure. Unlike the 10 VirtualRows-closure follow-ups, both directions of the
`regclass` cast (`internal/executor/expr.go`'s `CastExpr` arm, plus the
`regclass`/`regprocedure`/… function-call arm) resolved every lookup against
`DefaultDBOid` unconditionally: `<oid>::regclass` for a table in a distinct
`CREATE DATABASE`'d dbOid rendered the bare numeric OID instead of the
relation name (falls through `im.LookupTableByOID`/`im.ToastRelName`/
`ctx.Catalog.AllIndexes()`, all unscoped), and `'name'::regclass` couldn't
resolve that table's OID at all (`ctx.Catalog.LookupTable(objName)`, also
unscoped). Fixed by threading `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
through all five lookup call sites (mirroring every other per-dbOid site in
`operators_fk.go`/`operators_sequence.go`/`operators_tx.go`), plus giving
`catalog.InMemory.ToastRelName` a variadic `dbOid` parameter (it had none
before — hardcoded to `DefaultDBOid`; its sole call site is the `CastExpr` arm
just fixed). New test `TestRegclassCastScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`), confirmed to fail against a
revert of this loop's `catalog.go`/`expr.go` changes: two identically-named
tables (`shared_name`) in two distinct dbOids resolve `'shared_name'::regclass`
to their OWN database's OID (the realistic collision scenario — a
differently-named lookup miss can't demonstrate a leak, since it falls back to
an unresolved literal rather than another database's OID); the `<oid>::regclass`
direction is covered both ways with distinctly-named tables. `NamespaceDBOid(0)`
returns `DefaultDBOid` (existing helper, unchanged), so every pre-existing
`ctx.CurrentDatabaseOid == 0` test context is byte-identical to before this fix.

**`pg_sequence` row-content fixed (2026-07-10, follow-up 34):** unlike
`pg_statistic_ext`/`c.foreignServers`/`information_schema.routines` (all ruled
out above as needing a bigger registry redesign), `seqRegistry`
(`internal/executor/operators_sequence.go`) turned out to already be fully
dbOid-keyed by the earlier "sequence ownership" landing — the only remaining
gap was `catalog.SequenceParamsFunc`'s own signature (no `dbOid` parameter),
which made its `LookupSequence` call implicitly resolve against
`DefaultDBOid` regardless of which dbOid's sequence was actually being
enumerated. Gave `SequenceParamsFunc`/`sequenceParamsForCatalog` a `dbOid
uint32` parameter; extracted `pg_sequence.VirtualRows` into new
`catalog.InMemory.PGSequenceRowsForDBOid(dbOid uint32)` (identical
closure-extraction shape to `pg_foreign_table`/`pg_trigger`/`pg_rewrite`
above); fixed `PGDependRowsForDBOid`'s own `SequenceParamsFunc(...)` call,
which follow-up 27 had already dbOid-parameterized for its table enumeration
but not for this lookup — a latent bug from that earlier follow-up, closed
here. Wired new per-connection `executor.Context.PgSequenceRows func()
[][]string` field, set in `internal/server/dispatch.go`'s
`wireExtensionRows` (new `pgSequenceRowLister` interface), consumed by a new
`tbl.Name == "pg_sequence"` branch in `internal/executor/operators.go`'s
`valuesOp.Open`. New test `TestPgSequenceRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`). Live end-to-end verified
against a real `cmd/goopg` + `psql`: two `CREATE DATABASE`s, one `CREATE
SEQUENCE` each with distinct START/INCREMENT — each database's `pg_sequence`
shows exactly its own sequence with its own params, no cross-leak either
direction. `pg_sequences` (`internal/initdb/pg_sequences_view.go`) and
`information_schema.sequences`
(`internal/initdb/information_schema_sequences_view.go`) both still call
`executor.AllSequenceInfos()` with no dbOid and remain unfixed — see the
2026-07-10 deferral-ledger row (follow-up 34) for this narrowed remainder.

**`pg_sequences`/`information_schema.sequences` row-content fixed
(2026-07-10, follow-up 35):** closed the exact remainder follow-up 34 left
open above. Unlike `pg_sequence` (singular — a real `catalog.InMemory`-backed
table needing the `PGSequenceRowsForDBOid` cross-package interface plumbed
through `executor.Context`/`wireExtensionRows`), both `pg_sequences` (plural)
and `information_schema.sequences` read straight from this package's own
`seqRegistry` via the already dbOid-aware `AllSequenceInfos(dbOid ...uint32)`
— no `catalog.InMemory` indirection is needed at all, so no `Context` field or
`wireExtensionRows` wiring was added. Moved the row-shaping logic that used to
live inline in each view's `VirtualRows` closure
(`internal/initdb/pg_sequences_view.go`/`information_schema_sequences_view.go`)
into two new exported functions in `internal/executor/operators_sequence.go`:
`PGSequencesRows(dbOid uint32)` and `InformationSchemaSequencesRows(dbOid
uint32)` (plus a shared `sortedSequenceInfos` helper and the relocated
`seqDataTypePrecision`/a local `boolTextSeq`, since `executor` cannot import
`internal/initdb` — would be an import cycle). `internal/executor/
operators.go`'s `valuesOp.Open` gained two new branches, `tbl.Name ==
"pg_sequences"` and `tbl.Name == "sequences" && tbl.Schema ==
"information_schema"`, each calling the new function directly with
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` — mirroring the existing
`pg_stat_slru`/`pg_stat_io` direct-call branches (which also resolve
same-package state without a `Context`-field indirection) rather than the
`pg_class`/`pg_sequence`-style cross-package interface pattern. Each initdb
view's own `VirtualRows` closure is now a thin fallback calling the new
function with `catalog.DefaultDBOid`, used only when `ctx` is nil (e.g. a test
constructing the catalog directly without a connection). New test
`TestPGSequencesAndInfoSchemaSequencesRowsScopedToConnectionDBOid`
(`internal/executor/fk_dbid_routing_test.go`). Live end-to-end verified
against a real `cmd/goopg` + `psql`: two `CREATE DATABASE`s, one `CREATE
SEQUENCE` each with distinct START/INCREMENT — each database's
`pg_sequences` and `information_schema.sequences` show exactly their own
sequence with their own params, no cross-leak either direction. This closes
the last item follow-up 34 flagged as deferred for this sub-cluster.

**Remaining 4e work: the `CREATE DATABASE ... TEMPLATE` copy mechanism
itself** — deep-copying the source dbOid's `catalog.tableNamespace`
(tables/indexes/sequences/views, each under freshly allocated OIDs, with FK
target OIDs remapped) plus physically copying each relation's on-disk file(s)
(`internal/storage/smgr.go`'s `base/<dbOid>/<relOid>` layout, confirmed real
and per-database by this loop's investigation) into the new database's
directory — `CreateDatabaseUsingFileCopy`/`copydir`'s functional analog
(`postgres/src/backend/commands/dbcommands.c`), adapted to goopg's shared
in-memory-catalog-with-per-dbOid-namespaces architecture rather than PG's
literal per-database on-disk catalog. Unblocked since both upstream
prerequisites (sequence ownership, view constraint-dependency tracking) have
landed; the two checks landed this loop (template existence, template
emptiness) become largely redundant once the copy mechanism lands (a
non-empty template stops being an error and becomes the actual, common
case), so a future loop should replace `resolveCreateDatabaseTemplate`'s
`FeatureNotSupported` branch with the real copy rather than deleting the
function outright — the existence check and the `DefaultDBOid`-skip logic
both still apply.

**Foreign-server registry dbOid scoping — LANDED (2026-07-10, follow-up
36).** Strictly this belongs to the "~20 sibling per-name maps" this doc's
own "Deferred / explicitly out of scope" section named as out of scope for
the namespace epic proper — but the same shape had already been folded into
4e's "cross-cutting fixups" by follow-ups 30-35 (`seqRegistry`,
`constraintViewDeps`), so this row keeps that established numbering rather
than opening a separate doc. `catalog.ForeignServer`
(`internal/catalog/catalog.go`) gained a `DBOid uint32` field; the
`c.foreignServers` registry re-keyed from bare name to
`foreignServerKey(dbOid, name)` (`"<dbOid>:<name>"`, mirroring `seqKey`) so a
same-named `CREATE SERVER` in two distinct databases no longer collides
(the pre-fix bare-name key silently collapsed them onto one entry,
last-writer-wins). `RegisterForeignServer`/`DropForeignServer`/
`ListForeignServers`/`ForeignServerOID` (the last also updated in the
`catalog.Catalog` interface) and their `*DuringRecovery` counterparts gained
a trailing `dbOid ...uint32` parameter; `EncodeCreateForeignServer`/
`DecodeCreateForeignServer` and `EncodeDropForeignServer`/
`DecodeDropForeignServer` (`internal/wal/recovery.go`) each gained a
trailing-appended `dbOid` field, following `EncodeDropSequence`'s
backward-compatible-trailer pattern exactly (a pre-follow-up-36 WAL payload
decodes with `dbOid=0`, translated through `catalog.NamespaceDBOid` at the
replay call site). New `catalog.InMemory.PGForeignServerRowsForDBOid`
extracted from `pg_foreign_server.VirtualRows` (mirrors
`PGForeignTableRowsForDBOid`); a new `executor.Context.PgForeignServerRows`
field is wired the same way as every other 4e per-connection row-lister.
Auditing every `ForeignServerOID` call site this signature change touched
also caught 2 pre-existing un-dbOid'd sites that this loop's own regression
test suite (`TestPgForeignTableRowsScopedToConnectionDBOid`) surfaced as
newly broken: `CREATE FOREIGN TABLE ... SERVER`'s existence check and
`COMMENT ON SERVER`, both now threaded with
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`. Live end-to-end verified
against a real `cmd/goopg` + `psql`: two `CREATE DATABASE`s each
`CREATE SERVER shared ...` with a distinct `TYPE`; each database's
`pg_foreign_server` shows exactly its own row; `DROP SERVER` in one leaves
the other's same-named server intact; both facts survive a restart.
**Deliberately still un-dbOid'd, named in the deferral ledger's follow-up-36
row:** `pg_user_mappings`/`UserMapping` (same-shape sibling, its own future
loop), `internal/server/grant_ddl.go`'s `GRANT/REVOKE ... ON FOREIGN SERVER`
(still resolves against `DefaultDBOid` only — not a regression, matches
pre-follow-up-36 behavior), and the FDW registry itself.

**User-mapping registry dbOid scoping — LANDED (2026-07-10, follow-up 37).**
The same-shape sibling follow-up 36 named as its own future loop.
`catalog.UserMapping` (`internal/catalog/catalog.go`) gained a `DBOid uint32`
field; the `c.userMappings` registry re-keyed from
`strings.ToLower(user)+"\x00"+strings.ToLower(server)` to
`"<dbOid>:"+strings.ToLower(user)+"\x00"+strings.ToLower(server)`
(`userMappingKey`, mirroring `foreignServerKey`'s `"<dbOid>:<name>"` prefix
while keeping the pre-existing case-insensitive, NUL-separated (user,
server) part unchanged) so a same-named `CREATE USER MAPPING` (user, server)
pair in two distinct databases no longer collides (the pre-fix bare-key
silently collapsed them onto one entry, last-writer-wins).
`RegisterUserMapping`/`DropUserMapping`/`ListUserMappings` and their
`*DuringRecovery` counterparts gained a trailing `dbOid ...uint32`
parameter, exactly mirroring follow-up 36's `RegisterForeignServer` shape.
`EncodeCreateUserMapping`/`DecodeCreateUserMapping` and
`EncodeDropUserMapping`/`DecodeDropUserMapping` (`internal/wal/recovery.go`)
each gained a trailing-appended `dbOid` field, following
`EncodeCreateForeignServer`'s backward-compatible-trailer pattern exactly (a
pre-follow-up-37 WAL payload decodes with `dbOid=0`, translated through
`catalog.NamespaceDBOid` at the `internal/initdb/usermapping_ddl_recovery.go`
replay call site). New `catalog.InMemory.PGUserMappingsRowsForDBOid`
extracted from `pg_user_mappings.VirtualRows` (mirrors
`PGForeignServerRowsForDBOid`); inside it, the `srvid` column now resolves
via `c.ForeignServerOID(m.SrvName, dbOid)` instead of the bare-name call, so
a mapping in database B resolves its `srvid` against database B's own
`pg_foreign_server` registry, not database A's. A new
`executor.Context.PgUserMappingsRows` field is wired the same way as every
other 4e per-connection row-lister (`internal/server/dispatch.go`'s
`pgUserMappingsRowLister` interface + wiring next to
`pgForeignServerRowLister`), and a `pg_user_mappings` branch was added to
`internal/executor/operators.go` next to the `pg_foreign_server` one. CREATE
USER MAPPING's and DROP USER MAPPING's call sites in
`internal/executor/operators_ddl.go` now thread
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)` through to the registry
calls and the WAL-encode calls. Live end-to-end verified against a real
`cmd/goopg` + `psql`: two `CREATE DATABASE`s each `CREATE SERVER srv ...`
plus `CREATE USER MAPPING FOR postgres SERVER srv` with a distinct
`OPTIONS (label ...)`; each database's `pg_user_mappings` shows exactly its
own row (and no other database's); `DROP USER MAPPING` in one leaves the
other's same-named mapping intact; both facts (plus the surrounding
databases themselves) survive a restart. **Deliberately still un-dbOid'd
(deferred, see the 2026-07-10 follow-up-37 deferral ledger row):**
`internal/server/grant_ddl.go` has no `GRANT/REVOKE ... ON USER MAPPING`
support at all (PostgreSQL itself has no such grantable privilege on user
mappings, so this is not a gap); the parser's `scanUserMappingForServer`
(`internal/parser/ddl.go`) stores `CURRENT_USER`/`CURRENT_ROLE`/
`SESSION_USER`/`USER` verbatim as the literal string rather than resolving
to the connecting role's actual name (a pre-existing, explicitly-documented
simplification, confirmed still present by this loop's live E2E check:
`CREATE USER MAPPING FOR CURRENT_USER SERVER ...` renders `usename =
'current_user'` in `pg_user_mappings`, not the resolved role name) — unrelated
to dbOid scoping, orthogonal follow-up.

**`CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE`/`USER` role-spec resolution —
LANDED (2026-07-10, follow-up 38).** Closes follow-up 37's own orthogonal
discovery above. Real PostgreSQL's `CreateUserMapping`/`RemoveUserMapping`
(`foreigncmds.c`) both resolve the `FOR <user>` `RoleSpec` via
`get_rolespec_oid` (`acl.c`) — `CURRENT_USER`/`CURRENT_ROLE`/bare `USER`
resolve to `GetUserId()` (the session's *current* effective role, i.e. after
`SET ROLE`/`SET SESSION AUTHORIZATION`) and `SESSION_USER` resolves to
`GetSessionUserId()` (the originally authenticated role) — at CREATE/DROP
time, not stored symbolically. `internal/parser/ddl.go`'s
`scanUserMappingForServer` has no connection-state access, so (matching its
own doc comment) it still passes the raw keyword text through unchanged; the
resolution instead happens at the executor call sites in
`internal/executor/operators_ddl.go`, which already have `o.ctx`. New
`ddlOp.currentDDLOwnerName()` (name-string sibling of the existing
`currentDDLOwnerOID()`, same `NonSuperuserRole`-or-bootstrap-superuser
resolution) and `ddlOp.resolveUserMappingRoleName(user string) string` (case-
insensitively matches `current_user`/`session_user`/`current_role`/`user` and
substitutes `currentDDLOwnerName()`'s result; anything else, including
`public`/`""`, passes through unchanged). Both the CREATE USER MAPPING and
DROP USER MAPPING `"user mapping"` cases now route the parsed user token
through `resolveUserMappingRoleName` before touching the registry, so a
mapping created `FOR CURRENT_USER` can also be dropped `FOR CURRENT_USER`
(mirrors `RemoveUserMapping` resolving the same way `CreateUserMapping`
does). Like follow-up 37, goopg does not distinguish `SET ROLE` from `SET
SESSION AUTHORIZATION` anywhere else in this file (every other OWNER TO site
already collapses `CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE` into one
`"current_user"` sentinel resolved via `NonSuperuserRole`), so this follow-up
matches that established, intentional simplification rather than modeling
the two independently. Because the resolved name (not the literal keyword) is
now what gets WAL-logged (`wal.EncodeCreateUserMapping` receives
`um.UmUser`, already resolved), restart durability comes for free — no WAL
format change needed. Tests:
`TestCreateUserMappingCurrentUserResolvesToConnectingRoleName` (table-driven
over all 4 spellings), `TestCreateUserMappingCurrentUserFallsBackToBootstrapSuperuser`,
`TestDropUserMappingCurrentUserResolvesSameAsCreate`,
`TestCreateUserMappingPlainRoleNamePassesThrough` (guards the sentinel match
isn't over-broad: a role literally named e.g. `myuser`, and the `PUBLIC`
pseudo-role, must still pass through unchanged) — all in
`internal/executor/operators_ddl_user_mapping_current_user_test.go`. Live
end-to-end verified against a real `cmd/goopg` + `psql`: `CREATE USER MAPPING
FOR CURRENT_USER`/`FOR SESSION_USER` both resolve to `postgres` with no
active `SET ROLE`; after `SET ROLE alice`, `FOR CURRENT_USER` resolves to
`alice`, and the resulting mapping is droppable by its resolved name; a
mapping created `FOR CURRENT_USER` survives a restart still showing the
resolved name (not `current_user`) in `pg_user_mappings`. **Remaining 4e
scope (deferred, own future loops):** `pg_statistic_ext`/
`information_schema.routines` (bigger registry redesign); the real `CREATE
DATABASE ... TEMPLATE` relation-copy mechanism itself.

**Restart durability for user tables created under a distinct-dbOid database —
LANDED (2026-07-10, follow-up 39).** The first 4e item that touches the
on-disk catalog layout rather than an in-memory registry. Before this,
`syncTableToCatalogHeap` (`internal/executor/operators_ddl.go`) pinned every
user table's pg_class/pg_attribute rows to `DefaultDBOid`'s catalog heap
(`base/1/1259|1249`) and the startup loader (`loadUserTablesFromHeap`,
`internal/initdb/open.go`) read only `cat.DBOID()`'s heap, registering
everything into the `DefaultDBOid` namespace — so a table created under
`CREATE DATABASE db1` survived a restart only as a ghost in the `postgres`
namespace (its data unreachable, since the reloaded `catalog.Table` lost the
`DBOid` that routes reads to `base/<dbOid>/<relOid>`) and vanished from `db1`
entirely: a data-loss-shaped divergence from PostgreSQL's per-database catalog
layout (PG keeps a separate pg_class per database; relcache scans
`MyDatabaseId`'s own files). The fix adopts PG's layout for user-TABLE rows:

- **Write side (executor):** new `tableCatalogHeapDBOid(ctx)` (the
  connection's `catalog.NamespaceDBOid`) and its stamp-target sibling
  `tableCatalogDBOids(ctx)` (a distinct database's rows live ONLY in its own
  heap, so stamping the `DefaultDBOid`+mirror pair would miss them — and vice
  versa). `syncTableToCatalogHeap` routes its pg_class/pg_attribute heap
  writes by `tableCatalogHeapDBOid`; for a distinct dbOid it skips the
  sys-btree catalog index entries (the database has no bootstrapped catalog
  btree files, and planting TIDs into `DefaultDBOid`'s btrees would point at
  tuples in a different heap file — the startup loader scans heap blocks
  directly and never consults these indexes) and skips
  `mirrorTouchedCatalogsToPostgresDB` (nothing in the `DefaultDBOid` files
  changed). All table-row xmax-stamp sites (`execCreateView`/
  `execDropOneView`/`execDropOneMatView`/`dropTableByRefImmediate`/
  `execAlterTable`/`execAlterDropColumn`) switched `catalogDBOids` →
  `tableCatalogDBOids`; index-row stamps keep `catalogDBOids` (index writes
  are still `DefaultDBOid`-pinned, see deferred below). `rollbackDDLCreate`
  (`internal/executor/operators_tx.go`) now drops the relation file at the
  dbOid it was actually created under and stamps the matching heap.
- **Catalog:** `CreateView` now stamps `Table.DBOid` (mirrors `CreateTable`,
  so a view's pg_class row routes to the owning database); new
  `LookupTableByOIDAllDBs(oid) (*Table, uint32, bool)` searches every
  namespace (sound because table OIDs come from the single cluster-wide
  `nextOID` counter, so an OID matches at most one table).
- **Read side (initdb):** `loadUserTablesFromHeap` re-parameterized as
  `loadUserTablesFromHeapForDB(mgr, cat, clog, heapDBOid, nsDBOid)`; `Open`
  now loops over `cat.ListDatabases()` after `replayDatabaseDDLRecords` and
  loads each distinct-dbOid database's own catalog heap into that database's
  namespace with `Table.DBOid` stamped (data-file routing intact), before the
  view/matview/column-default replay passes — which now resolve their
  `TableOID`-only WAL records via `LookupTableByOIDAllDBs` so restored state
  lands on tables in any namespace.

E2E regression test `TestDistinctDatabaseTableSurvivesRestartInOwnNamespace`
(`internal/server/table_dbid_restart_test.go`, new file): real wire protocol +
real data-dir round trip; asserts the table reloads into its own database with
rows intact, a dropped sibling stays dropped (proves the xmax stamp routed to
the same per-database heap the insert went to), and nothing leaks into the
`postgres` namespace's pg_class. **Deferred (see the follow-up-39 ledger
row):** index/type/sequence catalog rows are still written to the
`DefaultDBOid`(+mirror) heaps only; distinct-dbOid databases get no sys-btree
catalog index entries (pg_dump's server-side index scans and an attaching PG
standby therefore only cover the `DefaultDBOid`/`postgres` catalogs — they
cannot yet dump/attach-to a distinct-dbOid database's tables); the
view/matview/column-default WAL records still carry no dbOid field
(`LookupTableByOIDAllDBs` compensates; a dbOid trailer would be the
PG-faithful shape).

**`CREATE DATABASE ... TEMPLATE` relation-copy mechanism, bounded plain-table
case — LANDED (2026-07-10, follow-up 40).** The last open 4e item. Before
this, `resolveCreateDatabaseTemplate` rejected ANY template with a non-empty
user relation set outright (`FeatureNotSupported`) — TEMPLATE never actually
copied anything, unlike real PostgreSQL's `createdb()`, which physically
copies each of the template's relation files
(`copy_relation_data`, `postgres/src/backend/commands/dbcommands.c`). This
lands that copy for the common bounded case: a template whose user relations
are ALL plain, unindexed heap tables (`!Virtual && View == nil &&
!IsMatView && !IsSequence && OfTypeOID == 0`, and the template's dbOid has
ZERO registered indexes at all — an index anywhere in the database rules out
the whole copy, since goopg still has no per-database sys-btree catalog
bootstrap, see follow-up 39's own deferral). Index/sequence/view/matview/
typed-table TEMPLATE copying remains deferred (own future loops — see the
deferral ledger's follow-up-40 row).

- **`internal/server/database_ddl.go`:** `resolveCreateDatabaseTemplate` now
  returns `(srcOid uint32, tables []*catalog.Table, err error)` instead of a
  bare `error`: `tables == nil, err == nil` means "nothing to copy" (the
  pre-existing `DefaultDBOid`-alias shortcut and the true-empty case both
  keep this shape unchanged — relied on by
  `TestTryHandleDatabaseDDLCreateEmptyTemplateSucceeds`); a non-empty
  `tables` with `err == nil` means the bounded plain-table case applies;
  `err != nil` (still `FeatureNotSupported`) means the template contains an
  index/sequence/view/matview/typed table anywhere. New
  `databaseTemplateRegistry.AllIndexes` method (already existed on
  `catalog.InMemory`) backs the index-anywhere-in-the-database rejection.
  `databaseDDLCreate`'s `CREATE DATABASE` branch, when `tables` is non-empty:
  (1) enforces a source-database busy guard mirroring PG's
  `CountOtherDBBackends(src_dboid)` (`s.cfg.Activity.CountByDatName`,
  `ERRCODE_OBJECT_IN_USE`) — only when there is real content to copy, so the
  empty-template path needs no such guard; (2) after
  `createDatabasePhysicalDirectory` succeeds, calls new
  `s.copyTemplateTables(srcOid, newOid, tables)`; (3) on any failure (copy or
  the subsequent WAL append), rolls back via new `s.rollbackTemplateCopy`
  (drops every table already registered under `newOid`) in addition to the
  existing `cat.DropDatabase` + `removeDatabasePhysicalDirectory` undo.
- **`copyTemplateTables`** clones each template table into the new dbOid:
  (a) a fresh `catalog.Table` via `catalog.InMemory.CreateTable(name, cols,
  newOid)` (a deep-copied `[]Column` slice, so the clone never shares
  backing-array storage with the template's own columns — `CreateTable`
  itself copies again internally); (b) a physical copy of the relation's
  `MainFork` data file via new `copyTemplateRelationFile`, which mirrors
  `internal/executor/operators_ddl.go`'s `relocateRelationPhysicalFile`
  copy+fsync discipline (flush the source, copy the bytes, fsync the
  destination) but — unlike that tablespace-move helper — never touches the
  source file afterward, since the template database keeps living; a source
  file that doesn't exist yet (relation created but never physically
  written) is a tolerated no-op, matching `relocateRelationPhysicalFile`'s
  own tolerance; (c) a `pg_class`/`pg_attribute` catalog-heap sync (plus the
  column-DEFAULT WAL snapshot, both automatic side effects) via the newly
  exported `executor.SyncTableToCatalogHeap`/`executor.CatalogHeapSyncAvailable`
  (thin wrappers around the pre-existing unexported
  `syncTableToCatalogHeap`/`catalogHeapSyncAvailable`), called from new
  `syncCopiedTableCatalogHeap`. Because `CREATE DATABASE` runs outside any
  user session's transaction, `syncCopiedTableCatalogHeap` builds its own
  minimal `*executor.Context` (`Pool`/`Catalog`/`TxnMgr`/`Tx`/`Snap`/`WAL`/
  `LogCanonical`/`CurrentDatabaseOid`) and opens+commits a short-lived
  internal `mvcc.Manager` transaction around the sync — the same "build a
  minimal Context, no session/planner state" shape
  `internal/executor/applyworker.go` already uses for its own out-of-band
  heap writes (logical replication apply, which also has no client
  transaction to ride).
- Column fidelity: no changes were needed to `catalog.Column` or
  `CreateTable` — the copied `[]Column` slice already carries
  `DefaultExpr`/`NotNull`/identity fields verbatim, and
  `syncTableToCatalogHeap`'s existing tail (unconditional on which caller
  invokes it) already snapshots every defaulted column's expression as SQL
  text via a `wal.EncodeColumnDefaults` record — a copied table's DEFAULT
  survives a restart exactly like an ordinary `CREATE TABLE ... DEFAULT`'s
  does, with no separate wiring.
- Tests: `TestTryHandleDatabaseDDLCreatePlainTableTemplateCopies`
  (`internal/server/database_ddl_test.go`, replaces the old
  `...NonEmptyTemplateErrors` test now that a plain-table template is
  copyable) asserts the in-memory clone under a catalog-only fixture (no
  Pool/TxnMgr, so the physical-copy/heap-sync steps are no-ops — only
  `CreateTable`'s clone is exercised); new
  `TestTryHandleDatabaseDDLCreateTemplateWithIndexErrors`/
  `...WithSequenceErrors` pin the still-rejected kinds. New E2E test
  `TestCreateDatabaseTemplatePlainTableCopiesDataAndSurvivesRestart`
  (`internal/server/database_template_copy_restart_test.go`, new file,
  mirrors follow-up 39's own `table_dbid_restart_test.go` shape): real wire
  protocol creates a source database + a plain table with real `INSERT`ed
  rows, `CREATE DATABASE ... TEMPLATE`s it, and asserts the copy's table AND
  its rows are visible both immediately and after a full server restart from
  the same data directory — proof the copy is durable (real physical file +
  real catalog-heap rows), not an in-memory-only illusion — while the
  template source stays completely unaffected throughout. Gates: `go build
  ./...`/`go vet ./...` clean; `go test ./internal/server/... ./internal/catalog/...
  ./internal/executor/... ./internal/initdb/...` PASS; `go test -race
  ./internal/server/...` PASS except the pre-existing, unrelated
  `TestConnectExceedsPositiveDatconnlimitRejected` race documented above (not
  introduced by this change — reproduces identically on unmodified code);
  `go test -short` full repo (excl. testport) PASS; `scripts/tpch-spotcheck.sh`
  PASS (Q12=2/Q13=33).
- **Deferred (see the deferral ledger's follow-up-40 row):** copying an
  index, sequence, view, materialized view, or typed table anywhere in the
  template still errors with `FeatureNotSupported` — each needs its own
  clone logic (index-file cloning + sys-btree catalog bootstrap for
  sequences' backing relation and indexes; AST/definition cloning for
  views/matviews; composite-type OID resolution for typed tables) that this
  bounded loop deliberately left for future work rather than risk landing
  all of it, untested, in one pass.

**`CREATE DATABASE ... TEMPLATE` sequence cloning — LANDED (2026-07-10,
follow-up 41).** Extends follow-up 40's plain-table-only copy to also cover
sequences — narrower in scope than "index/sequence/view/matview/typed-table"
sounds together: unlike the still-open index/view/matview/typed-table cases,
a sequence needs no relation file and no per-database sys-btree catalog
bootstrap (follow-up 39's own deferral), since goopg's sequence durable state
is a process-global registry entry (`internal/executor`'s `seqRegistry`), not
a heap page — the clone mechanism it needed already existed almost verbatim
as `RestoreSequenceFromWAL` (the same function WAL replay uses at startup).

- **`internal/executor/operators_sequence.go`:** new exported
  `SnapshotSequenceState(name string, dbOid uint32) (wal.SequenceStatePayload, bool)`
  captures a sequence's exact live state (start/increment/bounds/cache/cycle/
  current counter/called flag/ownership markers) via the same
  `payloadLocked` snapshot `WALLogSequenceState` already uses internally,
  without exposing the unexported `seqState` type across the package
  boundary. `ok=false` for a temporary sequence (mirrors `WALLogSequenceState`'s
  own temp skip) or a name with no registry entry.
- **`internal/server/database_ddl.go`:** `resolveCreateDatabaseTemplate`
  gained a third return value, `sequences []executor.SeqInfo` (alongside
  `tables`). **Important correctness finding:** sequences can NOT be
  detected via the same `tmpl.AllTables(oid)` walk `tables` uses — every
  sequence's virtual `pg_class` relation is marked `Virtual` by
  `executor.CreateSequenceCatalogRelation`, and `catalog.InMemory.AllTables`
  unconditionally skips every `Virtual` row (it's the general-purpose
  "real user relations" enumerator, e.g. also backing `DatFrozenXID`'s own
  identical skip). A first implementation attempt keyed sequence detection
  off `AllTables` + a hand-set `IsSequence` flag and passed its own unit
  tests (which built fixtures directly with `Virtual` left `false`) but
  failed the real end-to-end wire-protocol test with `pq: relation "s_copy"
  does not exist` — the clone's `nextval()` couldn't find a registry entry
  because the clone step never ran (`tmplSequences` came back empty from a
  real `CREATE SEQUENCE`). Fixed by enumerating sequences from
  `executor.AllSequenceInfos(oid)` instead (reads `seqRegistry` directly,
  independent of `Virtual`), then checking `executor.IsSequenceTemporary`
  per name — same "keep the whole template unsupported" rule follow-up 39/40
  established for indexes, now extended to a `TEMPORARY` sequence (session-
  scoped, no durable state worth cloning). The unit tests were corrected
  to build fixtures via the real registration pair
  (`executor.RegisterSequence` + `executor.CreateSequenceCatalogRelation`,
  mirroring `execCreateSequence` itself) instead of a hand-set `IsSequence`
  flag, so they now actually exercise the same `Virtual`-skipping behavior
  production code hits.
- **`copyTemplateSequences`** (new, parallels `copyTemplateTables`): for
  each `executor.SeqInfo`, snapshots the source via `SnapshotSequenceState`,
  overwrites the payload's `DBOid` to the new database's oid, re-registers it
  via the pre-existing `executor.RestoreSequenceFromWAL` (identical to a WAL
  replay call — no new registration primitive needed), re-creates the Virtual
  `pg_class` row via `executor.CreateSequenceCatalogRelation` (so `SELECT *
  FROM s_copy` / `pg_class`/`pg_sequences` see it under the new database),
  and — since `CREATE DATABASE` runs outside any client transaction, same
  reasoning as `syncCopiedTableCatalogHeap` — WAL-logs the clone's own
  durable snapshot via `executor.WALLogSequenceState` against a minimal
  `*executor.Context` (`CurrentDatabaseOid`/`WAL` only; no `Tx`/`Snap` needed,
  since sequence durability doesn't ride the heap/mvcc machinery table
  cloning does).
- **`rollbackTemplateCopy`** gained a second sweep: alongside its existing
  `im.AllTables(newOid)` walk (tables), it now also walks
  `executor.AllSequenceInfos(newOid)` and, for each, calls
  `executor.DropSequence` (the registry entry) followed by `im.DropTable`
  (the Virtual `pg_class` row) — mirroring real `DROP SEQUENCE`'s own
  pairing of the same two calls (`operators_ddl.go`). Without this second
  sweep, a mid-copy failure after the sequence step but before the table
  step (or vice versa) would leak a half-cloned sequence into the aborted
  database's oid, invisible to the table-only rollback walk.
- `tryHandleDatabaseDDL`'s `CREATE DATABASE` branch runs
  `copyTemplateSequences` as an independent step alongside
  `copyTemplateTables` (a template can contain either, both, or neither);
  both share the same source-database busy guard
  (`len(tmplTables) > 0 || len(tmplSequences) > 0`).
- Tests: `TestTryHandleDatabaseDDLCreateTemplateWithSequenceCopies` replaces
  the old `...WithSequenceErrors` test (sequences are no longer universally
  rejected); new `TestTryHandleDatabaseDDLCreateTemplateWithTemporarySequenceErrors`
  pins that a `TEMPORARY` sequence still rules out the whole template
  (`internal/server/database_ddl_test.go`). New E2E
  `TestCreateDatabaseTemplateSequenceCopiesStateAndSurvivesRestart`
  (`internal/server/database_template_copy_restart_test.go`): real wire
  protocol creates a sequence, advances it via real `nextval()`, TEMPLATE-
  copies it, and asserts the clone continues from the source's exact counter
  immediately, that advancing either copy never moves the other (no
  aliasing), and that both counters keep advancing independently — with
  values strictly above their pre-restart value, not necessarily +1, matching
  the pre-logging-gap semantics `TestPort_SerialSequenceSurvivesRestart`
  already documents — after a full server restart. Gates: `go build
  ./...`/`go vet ./...` clean; `go test ./internal/server/...
  ./internal/executor/... ./internal/catalog/...` PASS; `go test -race
  ./internal/server/... ./internal/executor/...` PASS except the
  pre-existing, unrelated `TestConnectExceedsPositiveDatconnlimitRejected`
  race documented above (re-confirmed this loop via `git stash` +
  `-count=3` against unmodified HEAD — reproduces identically, not
  introduced by this change); `go test -short` full repo (excl. testport)
  PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via
  `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 workloads).
- **Remaining 4e scope after follow-up 41 (deferred, own future loops):**
  index/matview/typed-table TEMPLATE copying (index-file cloning + per-
  database sys-btree catalog bootstrap; matview data clone; composite-type
  OID resolution); `pg_statistic_ext`/`information_schema.routines` registry
  redesign (pre-existing, unrelated to TEMPLATE).

**`CREATE DATABASE ... TEMPLATE` plain-view cloning — LANDED (2026-07-10,
follow-up 42).** Extends follow-up 40/41's copy mechanism to plain
(non-materialized) views — the last of the three "no relation file" cases
(sequences, views) sharing the same underlying gotcha: a view's pg_class row
is `Virtual` (`catalog.InMemory.CreateView` always sets it), so — exactly
like follow-up 41 discovered for sequences — `tmpl.AllTables(oid)` can never
see one; a template with only a plain view was previously silently dropping
the view with no error at all (not even the `FeatureNotSupported` the doc
comment claimed, since the `t.View != nil` check inside the `AllTables` loop
was itself dead code — `AllTables` filters out every `Virtual` row, including
views, before the loop body ever runs). Materialized views are unaffected by
this gap: `execCreateMatView` registers them via `CreateTable` (not
`CreateView`), so `IsMatView` rows are `Virtual: false` and were already
correctly caught by the `t.IsMatView` check.

- **`internal/catalog/catalog.go`:** new exported `InMemory.AllViews(dbOid
  ...uint32) []*Table`, a sibling of `AllTables` that walks the same raw
  `ns.tables` map but selects `View != nil && !IsMatView` instead of
  `!Virtual` — mirrors `PGClassRowsForDBOid`'s identical raw-map walk (the
  existing site that already needed this same distinction to render
  `relkind='v'` rows).
- **`internal/server/database_ddl.go`:** `resolveCreateDatabaseTemplate`
  gained a fourth return value, `views []*catalog.Table` (its signature is
  now `(srcOid uint32, tables []*catalog.Table, sequences []executor.SeqInfo,
  views []*catalog.Table, err error)`). Removed the dead `t.View != nil`
  branch from the `AllTables` loop (views never reach it) and its stale
  doc-comment claim that views were rejected — they never were, they were
  silently dropped. A view is unconditionally copyable in this slice (no
  `unsupported` check, matching sequences' treatment): it is only an AST
  (`catalog.Table.View`) plus raw SQL text (`ViewDef`), no relation file and
  no per-database sys-btree bootstrap needed.
- **`databaseTemplateRegistry`** (the narrow interface
  `resolveCreateDatabaseTemplate` type-asserts `s.cfg.Catalog` against)
  gained the matching `AllViews(dbOid ...uint32) []*catalog.Table` method.
- **`copyTemplateViews`** (new, parallels `copyTemplateSequences` — takes no
  `srcOid` parameter, unlike `copyTemplateTables`/`copyTemplateSequences`,
  since a view's full state already lives in the `views` slice with nothing
  left to re-fetch from the source registry): for each source view, deep-
  copies its columns/aliases and calls the real `catalog.InMemory.CreateView`
  to register the clone under the new dbOid (the AST pointer itself,
  `srcView.View`, is shared by reference across source and clone — safe,
  since a view's `Query` is only ever replaced wholesale by a later `CREATE
  OR REPLACE VIEW` on that exact name/dbOid, never mutated in place), then
  copies across the view-specific fields `CreateView` does not set itself
  (`ViewDef`, `CheckOption`, `SecurityBarrier(Set)`, `SecurityInvoker(Set)`
  — mirrors `execCreateView`'s own post-`CreateView` stamping order), then
  calls the pre-existing `syncCopiedTableCatalogHeap` to persist pg_class/
  pg_attribute rows plus the `RecordKindCreateView` WAL snapshot — no view-
  specific work was needed there: `syncTableToCatalogHeap`
  (`internal/executor/operators_ddl.go`) already branches generically on
  `tbl.View != nil && !tbl.IsMatView && tbl.ViewDef != ""`, and
  `internal/initdb/view_ddl_recovery.go`'s replay already resolves via
  `LookupTableByOIDAllDBs` (follow-up 39's cross-dbOid lookup) — both were
  written for the ordinary `CREATE VIEW` path but needed zero changes here.
- **`rollbackTemplateCopy`** gained a third sweep, alongside the existing
  table (`AllTables`) and sequence (`AllSequenceInfos`+`DropSequence`) walks:
  `im.AllViews(newOid)` + `im.DropView` (not `DropTable` — a view was
  registered via `CreateView`, unlike a sequence's Virtual row which goes
  through `CreateSequenceCatalogRelation`'s `CreateTable`-shaped path).
- `tryHandleDatabaseDDL`'s `CREATE DATABASE` branch runs `copyTemplateViews`
  as a fourth independent step (a template can contain any combination of
  tables/sequences/views, or none); the source-database busy guard condition
  extended to `len(tmplTables) > 0 || len(tmplSequences) > 0 || len(tmplViews) > 0`.
- **Pre-existing test-isolation hazard found and fixed in the same loop:**
  `executor`'s sequence registry (`seqRegistry`) is process-global, but every
  `database_ddl_test.go` test builds its own fresh `catalog.InMemory` that
  restarts OID numbering from `catalog.FirstUserOID` — so two independent
  tests' database oids routinely collide on the exact same number. Without
  cleanup, `TestTryHandleDatabaseDDLCreateTemplateWithTemporarySequenceErrors`
  registering `s`/`dbOid=X` as `TEMPORARY` silently leaked into
  `TestTryHandleDatabaseDDLCreateTemplateWithViewCopies`'s own `dbOid=X`
  (test file execution order, not test naming, decided the collision),
  making the new view test spuriously fail with the sequence test's own
  `FeatureNotSupported` error. Fixed by adding `t.Cleanup(func() {
  executor.DropSequence(...) })` to both existing sequence-copy tests
  (`...WithSequenceCopies`, `...WithTemporarySequenceErrors`) — a real,
  bounded correctness fix to pre-existing test hygiene debt, not a new
  hazard introduced by this loop's own view work.
- Tests: `TestTryHandleDatabaseDDLCreateTemplateWithViewCopies` (registers a
  view via the real `im.CreateView` path — same care as the sequence test's
  own doc comment: a hand-built `catalog.Table{View: ...}` with `Virtual`
  left `false` would not reproduce `AllTables`' real filtering), new
  `TestTryHandleDatabaseDDLCreateTemplateWithMatViewErrors` (pins that a
  materialized view still rules out the whole template, unlike a plain view;
  **superseded by follow-up 43 below, which repurposed this test into
  `...WithMatViewCopies` once matview copying itself landed**)
  (`internal/server/database_ddl_test.go`). New E2E
  `TestCreateDatabaseTemplateViewCopiesQueryAndSurvivesRestart`
  (`internal/server/database_template_copy_restart_test.go`): real wire
  protocol creates a table + a view over it, TEMPLATE-copies the database,
  asserts the copy's view query resolves against the copy's own table
  immediately, that dropping the copy's view leaves the source's view intact
  (independent objects, not aliased), and that the source's view still
  resolves correctly after a full server restart. Gates: `go build ./...`/
  `go vet ./...` clean; `go test ./internal/server/... ./internal/catalog/...
  ./internal/executor/... ./internal/initdb/...` PASS; `go test -race
  ./internal/server/... ./internal/catalog/... ./internal/executor/...`
  PASS except the pre-existing, unrelated
  `TestConnectExceedsPositiveDatconnlimitRejected` race and 2 unrelated
  flaky tests (`TestConnTxSessionNilWhenNotExplicit`,
  `TestSimpleQueryBatchAbortUndoesEarlierCreateType`) all re-confirmed via
  `git stash` against unmodified HEAD (reproduce identically, not introduced
  by this change); `go test -short` full repo (excl. testport) PASS;
  `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via
  `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 workloads).
- **Remaining 4e scope after follow-up 42 (deferred, own future loops):**
  index/typed-table TEMPLATE copying (index-file cloning + per-database
  sys-btree catalog bootstrap; composite-type OID resolution — matview data
  cloning itself landed the same day, follow-up 43 below);
  `pg_statistic_ext`/`information_schema.routines` registry redesign
  (pre-existing, unrelated to TEMPLATE).

**`CREATE DATABASE ... TEMPLATE` materialized-view cloning — LANDED
(2026-07-10, follow-up 43).** Extends follow-up 40/41/42's copy mechanism to
materialized views — the shape follow-up 42's own "Remaining 4e scope" bullet
flagged as needing both halves combined: a matview owns real heap storage
(`execCreateMatView`'s `CreateTable` call leaves `Virtual: false`, unlike a
plain view), so it already surfaces through the same `tmpl.AllTables(oid)`
loop `resolveCreateDatabaseTemplate` uses for plain tables — no new
`AllMatViews` registry method was needed, unlike `AllViews` for plain views.
It still needs the plain-table case's physical relation-file copy (its
materialized DATA) plus the plain-view case's AST/`ViewDef`/`IsPopulated`
field copy (its defining query) — `copyTemplateMatViews` combines both
disciplines directly rather than introducing a third one.

- **`internal/server/database_ddl.go`:** `resolveCreateDatabaseTemplate`
  gained a fifth return value, `matViews []*catalog.Table` (signature now
  `(srcOid uint32, tables []*catalog.Table, sequences []executor.SeqInfo,
  views []*catalog.Table, matViews []*catalog.Table, err error)`). The
  `AllTables(oid)` loop's `if t.IsMatView || t.OfTypeOID != 0 { unsupported =
  true }` branch was split: `OfTypeOID != 0` (typed tables) still marks the
  whole database unsupported, but `t.IsMatView` now appends to `matViews`
  instead — subject to the same whole-database `AllIndexes(oid) > 0` veto as
  everything else (an indexed matview, like an indexed plain table, still
  rules out the whole template; this bounded slice still implements no
  index-file cloning of any kind).
- **`copyTemplateMatViews`** (new, sibling of `copyTemplateTables` and
  `copyTemplateViews`, needs `srcOid` unlike `copyTemplateViews` since — like
  `copyTemplateTables` — it must locate the source's physical relation file):
  for each source matview, calls `catalog.InMemory.CreateTable` to register
  an ordinary heap-table clone (mirrors `copyTemplateTables`), then stamps
  the matview-specific fields `CreateTable` itself doesn't set
  (`IsMatView`, `IsPopulated`, `View`, `ViewDef` — mirrors
  `copyTemplateViews`' post-`CreateView` field-copy order, and
  `execCreateMatView`'s own stamping order; the `View` AST pointer is shared
  by reference across source and clone, safe for the same reason
  `copyTemplateViews` documents), then calls `copyTemplateRelationFile` to
  physically copy the MainFork data file (mirrors `copyTemplateTables`
  exactly), then calls the pre-existing `syncCopiedTableCatalogHeap` — no
  matview-specific plumbing needed there either:
  `syncTableToCatalogHeap`'s `ctx.Pool != nil && tbl.IsMatView &&
  tbl.ViewDef != ""` branch (the `RecordKindCreateMatView` WAL snapshot) and
  `internal/initdb/matview_ddl_recovery.go`'s replay (`LookupTableByOIDAllDBs`,
  dbOid-agnostic per follow-up 39) were both already written generically for
  the ordinary `CREATE MATERIALIZED VIEW` path.
- `rollbackTemplateCopy` needed **no changes** for matviews: unlike a plain
  view or sequence (both `Virtual`, invisible to `AllTables`), a matview's
  `Virtual: false` row already gets swept by the pre-existing
  `im.AllTables(newOid)` + `DropTable` loop that plain tables use — the same
  reason `resolveCreateDatabaseTemplate` needed no new `AllMatViews` registry
  method.
- `tryHandleDatabaseDDL`'s `CREATE DATABASE` branch runs `copyTemplateMatViews`
  as a fourth independent step (after the table-copy branch, since it shares
  the same physical-artifact-before-commit ordering); the source-database
  busy guard condition extended to also check `len(tmplMatViews) > 0`.
- **Real, previously-undiscovered correctness bug found and fixed in the same
  loop, independent of `CREATE DATABASE ... TEMPLATE` itself:**
  `execCreateMatView`'s own pre-validation called
  `analyzer.Analyze(s.Query, o.ctx.Catalog)` — the raw, dbOid-unaware
  catalog — while its own very next planning call,
  `planner.Plan(s.Query, o.planCatalog())` (or `PlanSchemaOnly` for `WITH NO
  DATA`), correctly used the search-path/dbOid-aware wrapper. This is the
  exact `ctxPlanCatalog` gap 4d-ii-part-2b item 3 fixed for `CREATE VIEW`'s
  FROM clause (see 4d-ii-part-2a's "New finding" above) — but that fix never
  touched `execCreateMatView`, which validates its query through a
  completely separate `analyzer.Analyze` call `execCreateView` doesn't even
  have. Net effect: `CREATE MATERIALIZED VIEW ... AS SELECT ... FROM <table>`
  failed with a bogus `42P01 relation "<table>" does not exist` on ANY
  non-default database whenever `<table>` itself lived under that same
  non-default dbOid — i.e. materialized views were silently broken on every
  distinct-dbOid database, caught only because this loop's own E2E test was
  the first to exercise `CREATE MATERIALIZED VIEW` under a non-default
  database at all. The identical gap existed a second time in
  `execRefreshMatView` (`analyzer.Analyze(tbl.View, o.ctx.Catalog)` before
  `planner.Plan(tbl.View, o.planCatalog())`), caught by this loop's own E2E
  test's `REFRESH MATERIALIZED VIEW` step failing the same way. Fixed both
  call sites to use `o.planCatalog()` instead of `o.ctx.Catalog`
  (`internal/executor/operators_ddl.go`).
- **Tests:** `TestTryHandleDatabaseDDLCreateTemplateWithMatViewCopies`
  (repurposed from the now-obsolete `...WithMatViewErrors`, same care as
  `...WithViewCopies` about reproducing `AllTables`' real filtering)
  (`internal/server/database_ddl_test.go`). New E2E
  `TestCreateDatabaseTemplateMatViewCopiesDataAndSurvivesRestart`
  (`internal/server/database_template_copy_restart_test.go`): real wire
  protocol creates a table + a populated materialized view over it,
  TEMPLATE-copies the database, asserts the copy's matview already carries
  the source's materialized DATA (not just its defining query) immediately,
  that inserting into the copy's own underlying table and refreshing the
  copy's matview leaves the source's matview rows unchanged (physical
  independence, not aliased relation files), and that the source's matview
  still resolves correctly after a full server restart. Gates: `go build
  ./...`/`go vet ./...` clean; `go test ./internal/server/...
  ./internal/catalog/... ./internal/executor/... ./internal/initdb/...`
  PASS; `go test -race ./internal/server/... ./internal/catalog/...
  ./internal/executor/...` PASS except the pre-existing, unrelated
  `TestConnectExceedsPositiveDatconnlimitRejected` race (re-confirmed via
  `git stash` against unmodified HEAD, reproduces identically, not
  introduced by this change); `go test -short` full repo (excl. testport)
  PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via
  `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
  failed, all 3 workloads).
- **Remaining 4e scope after follow-up 43 (deferred, own future loops):**
  index/typed-table TEMPLATE copying (index-file cloning + per-database
  sys-btree catalog bootstrap; composite-type OID resolution);
  `pg_statistic_ext`/`information_schema.routines` registry redesign
  (pre-existing, unrelated to TEMPLATE).

**`pg_collation`/`UserCollation` cross-database isolation — LANDED (2026-07-15,
follow-up beyond 4e, picked up from the "Deferred / explicitly out of scope"
list's collations item below).** Motivation: `TestPort_PgDumpConnectionSetup`'s
DU-002 dump+restore round-trip probe (`internal/testport/
pgdump_connsetup_test.go`) restores a captured dump into a brand-new, empty
database in the same cluster — the very first schema-level object in the
fixture is `CREATE COLLATION public.builtin_coll (provider = builtin, locale =
'C')`, which errored `collation "builtin_coll" already exists` because
`catalog.InMemory.userCollations` was one flat, dbOid-less
`[]*UserCollation`. Mirrors follow-up 36/37's `ForeignServer`/`UserMapping`
shape exactly (same-shape sibling maps, not the `tables`/`indexes` namespace
struct): `catalog.UserCollation` gained a `DBOid uint32` field;
`CreateCollation`/`DropCollation`/`RenameCollation`/`SetCollationOwner`/
`SetCollationSchema`/`CollationAttrsByName` each gained a trailing
`dbOid ...uint32` parameter (variadic, `resolveDBOid` defaults to
`DefaultDBOid` — every pre-existing call site, including all of
`create_collation_test.go`, keeps its old behavior unchanged). New
`catalog.InMemory.ListUserCollationsForDBOid`/`PGCollationRowsForDBOid`
(mirrors `PGForeignServerRowsForDBOid`); a new
`executor.Context.PgCollationRows` field is wired the same way as every other
per-connection row-lister (`internal/server/dispatch.go`'s
`pgCollationRowLister` interface + wiring next to `pgUserMappingsRowLister`),
and a `pg_collation` branch was added to `internal/executor/operators.go` next
to the `pg_user_mappings` one. All 8 `execCreateCollation`/`execAlterCollation`/
DROP COLLATION/COMMENT ON COLLATION call sites in
`internal/executor/operators_ddl.go` now thread
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)` through to the registry
calls. **Deliberately NOT done (scope kept bounded, unlike follow-up 36/37):**
no WAL-record format change — `wal.EncodeCreateCollation`/`DecodeCreateCollation`
and the sibling rename/owner/set-schema records still carry no dbOid, so
`CreateCollationDuringRecovery` (`internal/initdb/collation_ddl_recovery.go`)
now explicitly stamps every replayed collation `DBOid = DefaultDBOid` — a
restart still restores every database's collations into DefaultDBOid's
namespace, matching "every write path still persists under DefaultDBOid until
migrated" (4c's dual-mirror convention above). This is an accepted, recorded
residual (deferral ledger row), not silently dropped: a genuinely distinct
database's collations do not yet survive a restart under their own namespace.
Also left unscoped: `UserCollationOIDByName` (used to shadow a column's
`attcollation`) still searches all databases by bare name — a same-named
custom collation in two different databases could resolve the wrong OID for
this one reverse-lookup path; narrow, pre-existing-shaped edge case, not
exercised by the DU-002 fixture, recorded in the ledger rather than fixed
here. Confirmed via `TestPort_PgDumpConnectionSetup`: the round-trip restore's
failure point moved from `collation "builtin_coll" already exists` to a
different, later object (`type "b_in" already exists"`) — the next unscoped
sibling map in the same "Deferred / explicitly out of scope" list, a future
loop's own follow-up. New `TestCreateCollationCrossDatabaseIsolation`
(`internal/catalog/create_collation_test.go`): two distinct dbOids each
`CREATE COLLATION public.builtin_coll` without colliding, a genuine
same-database duplicate still errors, each database's `pg_collation` view
sees only its own row (+ the 7 shared builtins), and dropping one database's
copy leaves the other's intact. Gates: `go build ./...`/`go vet ./...` clean;
`go test ./internal/catalog/... ./internal/executor/... ./internal/server/...
./internal/initdb/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed,
all 3 workloads; first attempt hit 1 transient pgbench failure — 0.009%,
11,364 txns, unrelated to this loop's catalog/executor diff — retry passed
clean).

## Recommended order and stopping points

4a (landed) → 4b-i (landed) → 4b-ii (landed) → 4c (landed) → 4d-i (landed) →
4d-ii-part-1 (landed) → 4d-ii-part-2a (landed) → 4d-ii-part-2b (landed) → 4e,
strictly in order — each depends on the previous sub-slice's data shape.
4d-ii-part-2b's item 3 (the `CreateView`/`AllUserViews`/`AllUserMatViews`/
`IndexesOnTable`/`planCatalog()` dbOid-awareness gap, see 4d-ii-part-2a's
"New finding" above), item 1 (the cross-file `IndexesOnTable` sweep,
including the `catalog.SearchPathCatalog.IndexesOnTable` override that
transparently covers every `internal/planner` call site, and its
`applyworker.go` corner — see item 1's section above for
`server.applyWorkerCatalog`), and item 2 (wiring `RelFileNode.DBOid` at
creation time — see its section above for the `postgres`/`DefaultDBOid`
dual-mirror guard that turned out load-bearing) are now all fully landed
(2026-07-10). **4d-ii-part-2b is complete; the only remaining sub-slice is
4e** (dependent-object-walk fixups + the `CREATE DATABASE ... TEMPLATE`
copy mechanism). 4e's FK-target-resolution, sequence-ownership, and view
constraint-dependency items are now all landed (2026-07-10, see their
sections above); `CREATE DATABASE ... TEMPLATE`'s bounded
existence/emptiness validation also landed (2026-07-10, see its own section
above); the real relation-copy mechanism itself has now ALSO landed
(2026-07-10, follow-up 40, see its own section above) for its bounded
plain-table case, extended the same day to sequences (follow-up 41), plain
views (follow-up 42), and materialized views (follow-up 43, see their own
sections above) — **4e's only remaining scope is extending that copy
mechanism to index/typed-table templates** (see the deferral ledger's
follow-up-40/41/42/43 rows for the resume point). Before starting that
extension, read 4d-i's,
4d-ii-part-1's, and 4d-ii-part-2a's/4d-ii-part-2b's/4e's landed sections
above in full — they document exactly which write entry points and lookups
already thread the connection's real dbOid (so any extension must resolve
the SAME
`catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` value to actually find
what earlier sub-slices create), the `postgres`/`DefaultDBOid` dual-mirror
shim that still applies unchanged, and the `ns()`/`getOrCreateNS` split
(create-needing paths use `getOrCreateNS`; don't revert them to the old
always-creating `ns()`). This package's `runDDL`/`runQuery` test helpers
plan through the raw, un-wrapped catalog and so cannot resolve table names
under a distinct dbOid — use the new `runDMLUnderDBOid`/
`runQueryUnderDBOid` helpers (`internal/executor/fk_dbid_routing_test.go`)
for any test that needs planning-time name resolution under a distinct
dbOid. If a sub-slice is cut off mid-implementation, prefer reverting it
whole (the tree must build and tests must pass at every commit) over leaving
a partially-migrated state.

**Unrelated pre-existing hazard noticed while verifying 4c** (not part of
this epic, not fixed here): `go test -race ./internal/server/...` fails
`TestConnectExceedsPositiveDatconnlimitRejected` with a real data race in
`internal/activity/registry.go` (`ActivityRegistry.acquire` write racing
`CountByDatName`'s read of the same backend slot) — reproduces identically on
unmodified HEAD via `git stash` (confirmed 2026-07-09), so it predates this
epic entirely. Flagged here only because it surfaced during this loop's `-race`
verification pass; a future loop should file it against
`internal/activity/registry.go` directly (M0119-0006, which added
`CountByDatName`) rather than this design doc.

## Deferred / explicitly out of scope

- Unifying `ResolveDatabaseOid`'s switch with the `pg_database` `VirtualRows`
  closure's inline oid-selection switch (both must independently stay in
  sync with the postgres/template0/template1 special cases today).
- The `postgres`/`template1` dbOid dual-mirror migration strategy for 4d —
  flagged, not designed; needs its own investigation once 4d is reached.
- Any of the remaining ~19 sibling per-name maps on `InMemory` beyond
  `tables`/`indexes`/`byTable`/`foreignServers`/`userMappings`/`userCollations`
  (conversions, aggregates, operator classes, etc.) — genuinely out of scope
  for "per-database catalog namespace" as motivated by the
  template-copy/dump-restore use cases; each would need its own audit for
  whether PG actually scopes it per-database (most do) before being folded
  into this epic. `userCollations` itself was folded in 2026-07-15 (see its
  section above) once the DU-002 round-trip probe surfaced it as the specific
  next-in-line collision; the probe's failure point has since moved to a
  `CREATE TYPE` collision, the natural next candidate for this same audit.
