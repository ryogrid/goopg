# Per-database catalog namespace (M0122-0007 slice 4)

Status: accepted (sub-slices 4a, 4b-i, 4b-ii, 4c, and 4d-i landed; 4d-ii, 4e planned)
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

### 4d-ii — Thread dbOid through executor-operator-level lookups + `RelFileNode.DBOid` (planned)

Two remaining pieces, likely worth their own further split given the size:

1. Thread `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` through every
   executor-operator-level `LookupTable`/`LookupIndex` call that currently
   passes none (60+ in `operators_ddl.go` alone; more across the DML
   operators) — see the "critical scope finding" in 4d-i above for exactly
   why this is required before a second real database's tables are usable
   through anything but `CREATE TABLE` itself. Given the scale, consider
   whether wrapping `ectx.Catalog` itself in a `SearchPathCatalog` (mirroring
   `ectx.PlanCatalog`) is viable instead of touching every call site
   individually — but note this breaks every `im, ok :=
   o.ctx.Catalog.(*catalog.InMemory)` type assertion (dozens of them), which
   would need updating in lockstep, so it may not actually be less work than
   the mechanical per-call-site fix.
2. Wire `RelFileNode.DBOid` to the connection's real dbOid at creation time
   (currently hardcoded to `DefaultDBOid` regardless of connection) so
   physical storage genuinely separates per database, closing the loop with
   slices 2/3's `base/<dbOid>` directories. **Must account for the
   `postgres`/`template1` dual-mirror** noted under "Blast radius" —
   `postgres`'s storage identity is not simply
   `ResolveDatabaseOid("postgres")` in every context; audit `NewInMemory`'s
   `dbOid: DefaultDBOid` seed and the `base/1/` + `base/5/` mirror before
   changing what oid live relations are created under.

### 4e — Cross-cutting fixups + the original motivation (planned)

Once 4b-ii-4d land: dependent-object walks that assume a single global
namespace (FK target resolution, view dependency tracking, sequence
ownership), then finally the actual `CREATE DATABASE ... TEMPLATE` copy
mechanism (`CreateDatabaseUsingFileCopy`/`copydir`,
`postgres/src/backend/commands/dbcommands.c`) this whole epic exists to
unblock, and the AC-002/DU-002 dump+restore round-trip probe already
staged in `internal/testport/pgdump_connsetup_test.go` (a soft `t.Logf`
today, ready to become a hard assertion once this lands).

## Recommended order and stopping points

4a (landed) → 4b-i (landed) → 4b-ii (landed) → 4c (landed) → 4d-i (landed) →
4d-ii → 4e, strictly in order — each depends on the previous sub-slice's data
shape. 4d-ii is next: thread `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
through every executor-operator-level `LookupTable`/`LookupIndex` call site
that doesn't have it yet (see 4d-i's "critical scope finding" above for the
full list of affected files and the reasoning for why this, not
`RelFileNode.DBOid`, is the higher-priority half), then wire
`RelFileNode.DBOid` at creation time. Before starting 4d-ii, read 4d-i's
landed section above in full — it documents exactly which write entry points
already thread the connection's real dbOid (so 4d-ii's lookups must resolve
the SAME `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)` value to actually
find what 4d-i creates), the `postgres`/`DefaultDBOid` dual-mirror shim that
still applies unchanged, and the `ns()`/`getOrCreateNS` split (create-needing
paths use `getOrCreateNS`; don't revert them to the old always-creating
`ns()`). If a sub-slice is cut off mid-implementation, prefer reverting it
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
- Any of the ~20 sibling per-name maps on `InMemory` beyond
  `tables`/`indexes`/`byTable` (collations, conversions, aggregates,
  operator classes, etc.) — genuinely out of scope for "per-database
  catalog namespace" as motivated by the template-copy/dump-restore use
  cases; each would need its own audit for whether PG actually scopes it
  per-database (most do) before being folded into this epic.
