# M0122-0015d — pg_proc is per-database, and the regressions a vacuous pg_dump test was hiding

Status: landed 2026-10-12.
Parent: M0122-0015. Found by the `005_pg_dump_filterfile.pl` port.

## 1. pg_proc rows per database

PostgreSQL's pg\_proc is per-database. goopg's virtual pg\_proc
(`initdb/pg_proc_view.go`) appended every routine in the Routines registry.
As a result, a `CREATE FUNCTION` in `sourcedb` showed up in postgres,
template1 and every database created later. `DROP FUNCTION` in those
databases then said the function did not exist, because the registry
*lookups* were already keyed by database (design 0122-0018 follow-up 44).

- **Generic hook.** `catalog.Table.VirtualRowsForDB func(dbOid) [][]string`
  is a generic per-database row hook. The values operator prefers it, called
  with `ctx.CurrentDatabaseOid`, over the static `VirtualRows`. The static
  rows remain the default database's answer for plan time and for callers
  with no connection.
- **pg_proc rows.** pg\_proc's rows builder takes a database:
  - built-in rows go to every database;
  - user routines go only where `NamespaceDBOid(r.DBOid)` matches;
  - each routine's `pronamespace` now resolves its schema in its own
    database.

## 2. Regressions the PREPARE fix unmasked

`TestPort_PgDumpConnectionSetup` checks dump content only behind
`res.ExitCode == 0`. Its failure branch was a `t.Logf` labelled "remaining
DU-002 catalog-parity gap". Since pg\_dump began issuing
`PREPARE getDomainConstraints(pg_catalog.oid)` (fixed by M0122-0015c),
pg\_dump of the fixture had failed and every content assertion had been
skipped. With M0122-0015c landed the assertions ran again, and two
regressions surfaced. Both had been byte-identical to PG when first written.

- **Negative literals.** PG prints a folded negative constant as
  `'-5'::integer` (ruleutils `get_const_expr`, with the type taken from the
  literal by `make_const`). DU-002 slice 364 matched a UnaryOp over a
  positive literal. The grammar later folded `-5` into `IntegerConst{-5}`
  (doNegate, playbook §12.5), so the pg\_dump deparse twins printed `-5`.
  - New `parser.FoldedNegativeLiteralSQL` renders the folded constant:
    int4 down to INT\_MIN, int8 below, numeric for a decimal.
  - Both twins call it first: `executor.defaultExprToSQL` and
    `catalog.formatExprForAttrdef`.
  - This affects CHECK, DEFAULT and index expressions.
- **Owner-side REVOKE.** M0134-0069 changed the owner check in the
  GRANT / REVOKE recorders (`postmaster/grant_ddl.go`) from the `postgres`
  sentinel to `tbl.Owner`.
  - A relation created by the bootstrap superuser has an empty `Owner`, so
    `REVOKE TRIGGER ON t FROM postgres` stopped materializing the owner
    default and recorded nothing.
  - `effectiveRelOwner` maps the empty owner to `postgres` in both sibling
    recorders.
  - The GRANT twin now also materializes the owner default before an
    owner-targeted grant (`merge_acl_with_grant` starts from `acldefault`).
    `GRANT SELECT ON t TO postgres` gives `{postgres=arwdDxtm/postgres}`,
    as in PG, instead of `{postgres=r/postgres}`.
- **Test hardening.** The connection-setup test's failure branch is now a
  `t.Errorf`. A failing pg\_dump can no longer pass the test vacuously.

## Verification

- Probes against PG 18.3, byte-identical:
  - pg\_proc rows from postgres, a CREATE DATABASE'd database and a later
    database;
  - the negative-literal CHECK / DEFAULT / index dump (including
    `-(3)` and INT\_MIN);
  - six owner GRANT / REVOKE combinations on tables and a sequence.
- Tests:
  - `TestPgProcViewIsPerDatabase` and `TestFoldedNegativeLiteralSQL`.
  - `TestPort_PgDumpConnectionSetup` passes non-vacuously.
  - The pg\_dump / pg\_dumpall testport family passes.
- Regress A/B over 13 cases is identical, apart from one table-size noise
  line in `psql`.
- Gates: units, tpch-spotcheck, acceptance arm, sf025 PASS=99.

## Residuals (ledgered / filed)

- **Functions in a CREATE DATABASE'd database are broken (S2, M0146-0156).**
  Such a function cannot be called there ("function does not exist"), and it
  is gone after a restart. This is pre-existing at HEAD. The call path
  resolves routines in the default database, and the WAL replay registers
  routines in the default database.
- **User aggregates are unscoped.** They have no database and still appear
  in every database's pg\_proc.
- **Other pg\_dump-based testport tests** may carry the same
  log-instead-of-fail pattern; they were not audited.
