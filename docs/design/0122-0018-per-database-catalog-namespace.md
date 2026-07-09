# Per-database catalog namespace (M0122-0007 slice 4)

Status: accepted (sub-slices 4a, 4b-i, 4b-ii, 4c, 4d-i, 4d-ii-part-1, and 4d-ii-part-2a landed; 4d-ii-part-2b item 3 fully landed — own-signature half plus the `planCatalog()` half plus the `addGroupByPKDeps` follow-on bug it surfaced; items 1-2 + 4e planned)
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

### 4d-ii-part-2b — Remaining cross-file lookups + `RelFileNode.DBOid` (planned)

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
2. Wire `RelFileNode.DBOid` to the connection's real dbOid at creation time
   (currently hardcoded to `DefaultDBOid` regardless of connection) so
   physical storage genuinely separates per database, closing the loop with
   slices 2/3's `base/<dbOid>` directories. **Must account for the
   `postgres`/`template1` dual-mirror** noted under "Blast radius" —
   `postgres`'s storage identity is not simply
   `ResolveDatabaseOid("postgres")` in every context; audit `NewInMemory`'s
   `dbOid: DefaultDBOid` seed and the `base/1/` + `base/5/` mirror before
   changing what oid live relations are created under.
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
4d-ii-part-1 (landed) → 4d-ii-part-2a (landed) → 4d-ii-part-2b → 4e, strictly
in order — each depends on the previous sub-slice's data shape.
4d-ii-part-2b's item 3 (the `CreateView`/`AllUserViews`/`AllUserMatViews`/
`IndexesOnTable`/`planCatalog()` dbOid-awareness gap, see 4d-ii-part-2a's
"New finding" above) is now fully landed. What remains of 4d-ii-part-2b:
item 1, the cross-file sweep across
`operators_fk.go`/`operators_cluster.go`/`operators_reindex.go`/
`operators_sequence.go`/`operators_storage.go`/
`operators_pg_get_publication_tables.go`/the DML operators (see
4d-ii-part-1's "explicitly out of scope" list above for the full
breakdown, and this section's item 1 above — ~50 more un-threaded
`IndexesOnTable`/`AllUserViews`/`AllUserMatViews` call sites, measured via
`grep -rn '\.IndexesOnTable(\|\.AllUserViews(\|\.AllUserMatViews(' internal/`
during this loop), then item 2, wiring `RelFileNode.DBOid` at creation
time. Before resuming 4d-ii-part-2b, read 4d-i's, 4d-ii-part-1's,
and 4d-ii-part-2a's landed sections above in full — they document exactly
which write entry points and which `operators_ddl.go` lookups already
thread the connection's real dbOid (so 4d-ii-part-2b's remaining lookups
must resolve the SAME `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
value to actually find what 4d-i/4d-ii-part-1/4d-ii-part-2a create), the
`postgres`/`DefaultDBOid` dual-mirror shim that still applies unchanged, and
the `ns()`/`getOrCreateNS` split (create-needing paths use `getOrCreateNS`;
don't revert them to the old always-creating `ns()`). If a sub-slice is cut
off mid-implementation, prefer reverting it
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
