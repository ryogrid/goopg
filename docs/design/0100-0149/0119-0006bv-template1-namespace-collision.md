# template1 shares postgres' namespace — the three-oid map

Status: ROOT-CAUSED AND MAPPED 2026-09-22; §7 added 2026-09-22 after a THIRD
face of the same collision was traced to it (the pg_extension re-attribution),
which also CORRECTS an earlier claim that that defect was independent. No
production file touched. The fix is an OWNER/DESIGN CHOICE between two options
with very different blast radii — see §5.
Kind: recon
Parent: M0119-0006
Movement: none

## 1. The defect

Connect to template1, `CREATE TABLE t1_marker(x int)`, then from `postgres`
`SELECT count(*) FROM t1_marker` — it succeeds. PostgreSQL treats template1 as
an ordinary database with its own catalog; only `datistemplate` and
`datallowconn` distinguish the templates.

Control, and it is what makes the diagnosis specific: the identical sequence
against a `CREATE DATABASE`d database is correctly invisible from everywhere
else. **Per-database isolation works.** template1 alone misses it.

## 2. goopg has THREE distinct database-oid spaces

This is the thing that was not written down anywhere, and not knowing it is
what made two earlier loops guess wrong.

| space | what it keys | postgres | template1 | `CREATE DATABASE`d |
|---|---|---|---|---|
| **displayed** | `pg_database.oid` a client sees | 16384 (legacy placeholder) | 1 | its own oid |
| **catalog namespace** | `catalog.tableNamespace[dbOid]` | `DefaultDBOid = 1` | **1** | its own oid |
| **storage** | `base/<RelFileNode.DBOid>` | `PostgresDBOid = 5` | **5** | its own oid |

Measured, not inferred: `base/` after initdb contains `1`, `4`, `5` (the PG
bootstrap oids for template1/template0/postgres). A table created on the
**postgres** connection and a table created on the **template1** connection
both landed in `base/5`. A table created in a `CREATE DATABASE`d `isolated`
landed in a fresh `base/16408`.

## 3. Where the merge happens

- `ResolveDatabaseOid("template1")` returns **1** explicitly
  (`internal/catalog/catalog.go`), and `const DefaultDBOid uint32 = 1`. So
  template1's catalog namespace IS the default namespace.
- `NamespaceDBOid` folds postgres (`PostgresDBOid = 5`) onto `DefaultDBOid`,
  so postgres shares that namespace too.
- Storage then derives from the namespace: namespace 1 stores at `base/5`,
  which is why template1's files follow postgres' rather than going to the
  `base/1` that initdb already laid out for it.

The collision is not a bug in any one function. It is that goopg picked `1` as
its internal "default namespace" sentinel, and `1` is also template1's real
PostgreSQL bootstrap OID.

## 4. Two dependencies MEASURED as absent

Both were candidate reasons a fix might be dangerous. Neither holds:

- **`CREATE DATABASE ... TEMPLATE` does not leak.** It physically copies the
  template's relations from `base/<dbOid>`, so a database created from
  template1 could have inherited postgres' user tables. With a `secret` table
  in postgres, both plain `CREATE DATABASE` and explicit
  `TEMPLATE template1` produce a database where `secret` does not exist.
- **The startup reload is not implicated.** `catalog_heap_reload.go` computes
  `NamespaceDBOid(cat.DBOID())` — it folds 5→1 for the *postgres* connection
  specifically. Moving template1 off oid 1 leaves that path correct.

## 5. The choice (why this is not just implemented)

**Option A — give template1 its own namespace oid.** Smallest diff: stop
`ResolveDatabaseOid` returning `DefaultDBOid` for template1. template1 then
gets its own namespace and, by the derivation above, its own storage
directory. Cost: its files land under the new oid rather than the `base/1`
initdb already created, leaving that directory unused; and the displayed oid
must stay 1, which is fine — goopg already separates displayed from real oids
for postgres (displays 16384, really 5).

**Option B — unwind the fold: give postgres namespace 5, matching its
storage.** This is the *architecturally* right answer — one oid per database
at every layer, no sentinel — and it removes the collision by construction
rather than routing around it. But `DefaultDBOid` is assumed to be "the"
namespace across the catalog (publications, ts_configs and more register under
it), and unwinding that is substantially M0122-0007's per-database-namespace
epic, whose slice 4b-4e are still open.

Option A is a loop of work. Option B is the epic. They are not
interchangeable: A leaves the sentinel in place, so the next database that
happens to collide with a bootstrap oid has the same problem.

**Recommendation**: Option A now, explicitly labelled as routing around the
sentinel rather than removing it, with a ledger row pointing at Option B as
the real fix. But that trade — a correct-but-narrow fix that leaves a known
structural hazard — is the owner's call, not the loop's, which is why this
document stops here.

## 6. Also worth knowing

`base/1` and `base/4` exist and are populated by initdb with bootstrap catalog
files, but no live connection routes to them. Any fix should decide whether
template1 adopts `base/1` or gets a fresh directory; adopting it is tidier but
means reconciling initdb's contents with the runtime's expectations.

## 7. Third face: a template1 extension re-attributes to "postgres" (2026-09-22)

A separate task had recorded this defect as **not** subsumed by the collision,
on the strength of one measurement: after `CREATE EXTENSION amcheck` in
template1, `base/1/3079` holds the row, so "the write side is not misrouted —
it went to template1's own heap". That inference is wrong, and the control that
refutes it is one line long.

**The control.** Do the same `CREATE EXTENSION` from a **postgres** connection
on a fresh cluster. It produces the identical on-disk signature:

| heap | template1 install | postgres install | user-db install |
|---|---|---|---|
| `base/1/3079` | 1 row | 1 row | 0 |
| `base/4/3079` (template0 control) | 0 | 0 | 0 |
| `base/5/3079` | 1 row | 1 row | 0 |
| `base/<userdb oid>/3079` | — | — | 1 row |

`base/1` is not template1's own heap — it is `DefaultDBOid`, the namespace §3
shows template1 and postgres SHARE. `base/5` is not a second live copy either:
`mirrorCatalogRelToPostgresDB` (`internal/executor/sys_catalog_postgres_db_mirror.go`)
copies `DefaultDBOid` → `PostgresDBOid` unconditionally, src hard-wired, so the
reload's `cat.DBOID()` pass can find it. That answers the question the task
stopped on ("why does `base/5/3079` carry a live image?") — a deliberate,
database-agnostic mirror, not anything template1-specific.

**Why no reload change can fix it.** A pg_extension row's database scope lives
ONLY in the in-memory registry; on disk the sole carrier of scope is *which*
`base/<dbOid>` heap holds the row. Measured directly: before a restart the
registry is right (`template1=1`, `postgres=0`), after it the row is attributed
to postgres. Since template1's row and a postgres row occupy the same heap and
are otherwise identical, no reader of that heap has the information needed to
attribute it to template1. Teaching the reload to scan more directories — the
shape the earlier task proposed — cannot recover information that was never
written.

The user-database column above is the paired control that makes this specific:
per-database routing WORKS (`NamespaceDBOid(oid) == oid` for an ordinary
database), and template1 fails only because its oid is the sentinel.

So the extension re-attribution is a **consequence** of §1's collision and
unblocks with it. It is not separately actionable, and it is now marked `[!]`
behind the same owner decision.

### A measurement trap this cost two false positives

Heap pages are not flushed at `CREATE EXTENSION`. Reading `base/<db>/3079`
while the server is still running showed **zero** rows for writes that had
plainly succeeded, which briefly looked like two new defects (a cross-database
write being skipped, and a user database not getting its own row). Both
dissolved when the same files were read after a clean shutdown. Any on-disk
catalog probe in this area must stop the server first, or it is reading the
past.
