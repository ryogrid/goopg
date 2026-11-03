# M0148\-0001c3c — DROP / RENAME after a restart: resolve the catalog key the relation lives under

**Parent:** M0148\-0001c3 · **Status:** landed 2026\-11\-04; closed

## Divergence

After a server restart, objects created before it could not be dropped:

- `DROP VIEW vv` and `DROP INDEX tt_a` failed with `"view "vv" does not
  exist"` (or the index equivalent);
- `\dv` and `\di` still listed them, and `SELECT * FROM vv` still worked.

PostgreSQL drops them.

## Cause

`InMemory` keys a relation by `key(name)`. A live session stores an
unqualified relation under its bare name, while the restart reload
(`loadUserTablesFromHeap` / `loadUserIndexesFromHeap`) resolves the
namespace OID to the explicit `"public"` and stores it under
`"public.<name>"`.

- **Lookups** already accept either spelling: `LookupTable` falls back to
  the bare and `public.` keys, and `lookupIndexLocked` exists for exactly
  this ambiguity.
- **Mutators** did not: `DropView`, `DropTable`, `RenameTable` and
  `DropIndex` read and deleted `ns.tables[key(name)]` (or the index
  equivalent) by the exact key.
- So the executor's existence check passed, and the catalog delete then
  missed and reported "does not exist".

## Change

`lookupTableKeyLocked` (catalog.go) is `LookupTable`'s resolution over one
namespace, returning the key the relation actually lives under.

- `DropView`, `DropTable` and `RenameTable` delete or re\-key that key.
- `DropIndex` uses `lookupIndexLocked` the same way, for both `indexes` and
  `byTable`.
- The error text for a truly missing name is unchanged.

## Verification

- `TestDropResolvesRestartKeySpelling` (catalog) covers:
  - a `public.`\-keyed view dropped by its bare name, and a bare\-keyed one
    by its qualified name;
  - a `public.`\-keyed index dropped by its bare name;
  - a rename and a drop of a `public.`\-keyed table.
- **Live server.** Created a view, an index and a table, restarted, then
  ran DROP VIEW (bare and qualified), DROP INDEX and ALTER TABLE RENAME. All
  succeed, and the drops persist across a further restart.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass, as do the TPC gates.

## Not covered

- **Other exact\-key mutators.** Sequences, matviews and others were not
  audited. They are listed in the M0148\-0001c3b residue.
- **`\di` on a fresh cluster.** It lists `pg_class_oid_index` and other
  system catalog indexes under schema `public` with no table. This is
  pre\-existing (seen at HEAD) and unrelated.
