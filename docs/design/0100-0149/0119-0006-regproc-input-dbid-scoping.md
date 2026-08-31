# M0119-0006 (75th slice) — regproc/regprocedure INPUT is scoped to the connection's database

Closes deferral ledger row 1348 (filed by the 73rd slice).

## The gap

The regproc/regprocedure NAME→OID **input** half resolved every routine name
through `Routines.LookupByName` **with no dbOid argument**, so it always
resolved `DefaultDBOid`. The 4e-series routine registry (M0122-0007 slice 4e)
keys routines by `(dbOid, schema, name)` and a **live-created** routine is
registered under its real connection dbOid, so:

- from a connection to a genuinely distinct database, a routine created there
  was invisible to the name→OID lookup — and worse, when another database held
  a **same-named** routine the lookup resolved THAT routine's OID: a silent
  **cross-dbOid leak** (a `regproc`/`regprocedure` column would store the wrong
  database's function OID, and `'fn'::regproc` in a WHERE clause would match
  the wrong function);
- an initdb-reloaded routine (registered under `DefaultDBOid`) still resolved,
  which is why the bug was invisible on default-database connections.

Measured live on the 73rd slice's round-trip validation: `'g_offarg'::regprocedure`
failed `function "g_offarg" does not exist` on a stale server while the
reloaded `'f_offarg'` resolved. The 75th-slice repro tightened this: from a
distinct-dbOid connection, `'shared_fn'::regproc` returned **DefaultDBOid's**
same-named routine OID (`131072`) instead of the connection's own (`131073`) —
not a miss but a leak.

Upstream has no such bug: `regprocin`/`regprocedurein` look up `pg_proc` in the
**current** database (per-database function catalog), so a same-named function
in another database is simply not visible.

## The fix

Both sibling paths (Hard-won Rule #2) now thread the connection's dbOid into
`Routines.LookupByName`, mirroring the regclass arm's existing
`connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`:

- `internal/executor/reg_identifier.go` — `regIdentifierInput`'s
  regproc/regprocedure arm (feeds COPY FROM coercion + constraint checks):
  `LookupByName(name, connDBOid)`.
- `internal/executor/expr.go` — the `::regproc`/`::regprocedure` cast arm
  (feeds `'name'::regproc` in expressions): `LookupByName(name,
  catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))`.

`NamespaceDBOid` maps the connection dbOid to the catalog-key namespace OID
(`0`/`PostgresDBOid` → `DefaultDBOid`, else the real OID) — the same
normalization the 4e series applies everywhere, so default-database connections
behave exactly as before (the pre-existing `TestRegProcInputResolvesQuotedIdentifier`
and every reg* suite pin this).

**Isolation boundary preserved:** a routine that exists ONLY under another
database's dbOid must NOT resolve by name from an unrelated connection (the
regclass sibling's "must not leak DefaultDBOid's real user tables" guard).
Builtins still resolve through the global `LookupBuiltinProc` pg_proc index,
which is not dbOid-scoped — pg_catalog is implicitly visible in every database,
matching PG.

## Verification

- Unit: `TestRegProcInputScopedToConnectionDBOid` (same-named routine under
  two dbOids; each connection resolves its own through `::regproc`,
  `::regprocedure`, and `regIdentifierInput`), `TestRegProcInputSchemaQualifiedScopedToConnectionDBOid`
  (schema-qualified `schema.name` form, no cross-dbOid leak), and
  `TestRegProcInputDistinctDBOidMissIsNotDefaultLeak` (a DefaultDBOid-only
  routine raises 42883 from a distinct-dbOid connection). All three FAIL pre-fix
  (the first two leaked the wrong OID, the third resolved instead of raising)
  and PASS post-fix.
- Live E2E on a throwaway goopg server (port 5533, `CREATE DATABASE db1/db2`):
  db2's `'shared_fn'::regproc` → 42883 before its own routine exists; after
  creating the same-named routine there, each database resolves its own OID
  (131072 vs 131073).
- Identical scenario on a throwaway PG 18.3 oracle (port 5534) is
  byte-identical: 42883-then-resolve-own, `shared_fn(int4)`::regprocedure →
  `shared_fn(integer)`.

## Scope notes

- Only the regproc/regprocedure arm was unscoped. regclass (connDBOid),
  regcollation (`ctx.CurrentDatabaseOid`), regrole (cluster-wide roles) and
  regtype (global builtin + `userTypeOIDForName`) already behave correctly; the
  last's `userTypeOIDForName` schema-resolution gap is a SEPARATE deferral (row
  1343, the user-type store lacks a namespace field).
- The `::regproc`/`::regprocedure` cast's OID→name direction resolves by OID
  (`LookupByOID`), which needs no dbOid (OIDs are unique).
