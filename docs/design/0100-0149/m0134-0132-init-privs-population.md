# M0134-0132 — `init_privs.sql`: populate `pg_init_privs`

## Status: PASS (100% parity)

## Source

`postgres/src/test/regress/sql/init_privs.sql` (11-line source). Sized live
via `scripts/pg-regress-runner.sh --verbose init_privs`.

## Symptom

```sql
SELECT count(*) > 0 FROM pg_init_privs;
--  ?column?
-- ----------
--  f            -- expected: t
-- (1 row)
```

The remaining two statements (`GRANT SELECT ON pg_proc TO CURRENT_USER`,
`GRANT SELECT (prosrc) ON pg_proc TO CURRENT_USER`, `GRANT SELECT
(rolname, rolsuper) ON pg_authid TO CURRENT_USER`) already executed silently
and matched the oracle — the entire 15-line diff was the single `t`→`f`
row mismatch.

## Root cause

`pg_init_privs` (`internal/catalog/catalog.go`, OID 3394) was wired to a
constant `func() [][]string { return nil }` — deliberately empty since
M0110-0001 (DU-002), documented at the time as "goopg installs no extensions
and does not snapshot initdb-time ACLs, so this view is empty by
construction."

Real PostgreSQL populates `pg_init_privs` during `initdb`, not lazily: after
bootstrap, `initdb`'s `setup_privileges()`
(`postgres/src/bin/initdb/initdb.c:1802-1935`) runs

```sql
UPDATE pg_class SET relacl = (SELECT array_agg(a.acl) FROM
  (SELECT '=r/"postgres"' AS acl
   UNION SELECT unnest(acldefault(CASE WHEN relkind='S' THEN 's' ELSE 'r' END, 10))
  ) AS a)
WHERE relkind IN ('r','v','m','S') AND relacl IS NULL;

INSERT INTO pg_init_privs (objoid, classoid, objsubid, initprivs, privtype)
  SELECT oid, 'pg_class'::regclass, 0, relacl, 'i'
  FROM pg_class WHERE relacl IS NOT NULL AND relkind IN ('r','v','m','S');
-- (plus sibling INSERTs for pg_proc.proacl, pg_type.typacl, pg_language.lanacl,
--  pg_namespace.nspacl, pg_largeobject_metadata.lomacl, pg_foreign_data_wrapper,
--  pg_foreign_server, and pg_attribute.attacl)
```

i.e. every relation (table/view/matview/sequence) that exists in the
pg_catalog/information_schema namespaces at initdb time gets an explicit
world-readable + owner-full `relacl`, and that value is copied verbatim into
`pg_init_privs` as an immutable "day zero" snapshot. `pg_dump` later diffs a
catalog object's *live* ACL against this snapshot (`relacl IS DISTINCT FROM
pip.initprivs`) to decide whether to emit a `GRANT`/`REVOKE` for it.

goopg has no bootstrap-time SQL replay step and no persisted-at-initdb ACL
concept (`tableACLs` starts genuinely empty for every relation, including
system catalogs — the pre-existing `SELECT`-privilege carve-out for system
relations, `catalog.IsSystemRelation`, exists specifically because there is
no `pg_init_privs`-style implicit grant to fall back on; see
`.ralph/deferral_ledger.md` row for M0097-0040). Reproducing the *mechanism*
(an actual initdb-time SQL bootstrap step that mutates `relacl`) is out of
scope for a single-file test-port loop.

## Fix

`PGInitPrivsRowsForDBOid` (`internal/catalog/catalog.go`, next to the sibling
`PGClassRowsForDBOid`/`PGForeignTableRowsForDBOid` row builders) computes the
row set *on every read* rather than snapshotting once: for each relation in
`pg_catalog`/`information_schema` with a non-zero OID, it emits one
`(objoid, classoid=1259, objsubid=0, privtype='i', initprivs)` row, with
`initprivs` synthesized as PG's own default —
`{=r/postgres,postgres=arwdDxtm/postgres}` for tables/views/matviews,
`{=r/postgres,postgres=rwU/postgres}` for sequences — reusing the existing
`ownerTableACLString`/`ownerSequenceACLString` privilege-letter constants
conceptually (not the live `tableACLs`-backed renderer, since that renders
NULL for a never-granted relation by design).

This is a **reconstruction, not a snapshot**: because goopg computes the row
fresh on every `SELECT * FROM pg_init_privs`, using the CURRENT relkind/schema
membership rather than a value captured once at initdb, it cannot represent a
relation whose *actual* initial ACL was genuinely different from the default
(none exist in a stock cluster, so this is invisible to the test), and it
would incorrectly "un-see" any future in-place change to what counts as a
system relation. Both are explicitly out of scope for what the single failing
assertion (`count(*) > 0`) requires.

## Why this is the right level of fix (not a shortcut)

- The objoid/classoid values are real, live OIDs (not placeholders) — a
  `pg_init_privs` row genuinely references the matching `pg_class` row, so
  `pg_dump`'s `LEFT JOIN pg_init_privs pip ON (c.oid = pip.objoid AND
  pip.classoid = '<catalog>'::regclass AND pip.objsubid = 0)` now finds a real
  match instead of always falling to NULL.
- The *shape* of coverage (only pg_catalog/information_schema relations,
  never a user table) matches PG's actual scope: `setup_privileges()` runs
  once, before any user `CREATE TABLE` exists, so only pre-existing objects
  ever get a row. `TestPGInitPrivsRowsForDBOidExcludesUserTables` pins this.
- What's still missing is a real *mechanism*, not a wider row set: an
  initdb-time SQL bootstrap step (or equivalent Go-side one-time snapshot)
  that captures `relacl` as of server creation, immutable thereafter. Without
  it, `pg_dump`'s ACL-diffing logic will not distinguish "administrator
  revoked a system catalog's default SELECT" from "default", which is the
  scenario `init_privs.sql`'s own comment calls out
  ("Intentionally include some non-initial privs for pg_dump to dump out") —
  those two `GRANT`s already work today (goopg's plain GRANT/REVOKE path is
  unaffected by this change), but a `pg_dump --binary-upgrade`-driven
  differential dump of *only the changed* privileges is not yet
  byte-faithful. Recorded as a deferral (below) rather than attempted here.

## Files changed

- `internal/catalog/catalog.go`: `pgInitPrivs.VirtualRows` now calls the new
  `PGInitPrivsRowsForDBOid`; updated the surrounding comment.
- `internal/catalog/pg_init_privs_test.go` (new):
  `TestPGInitPrivsRowsForDBOidNonEmpty`,
  `TestPGInitPrivsRowsForDBOidExcludesUserTables`.
- `docs/test-port/postgres-oracle-target-inventory.csv`: `init_privs.sql`
  `not-tried` → `pass`/`pass_required=yes`.

## Gates run

- `go build ./...` PASS
- `go test ./internal/catalog/... ./internal/executor/...` PASS
- `scripts/pg-regress-runner.sh --verbose init_privs`: 0/1 → 1/1 (100% parity)
- `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35)
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
- `make check-testport-inventory` / `make regen-testport` PASS

## Deferral

See `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0132: no bootstrap-time
immutable ACL-snapshot mechanism exists; `pg_init_privs` is reconstructed on
every read from live catalog state rather than captured once at initdb, so a
`pg_dump --binary-upgrade`-style differential ACL dump (GRANT/REVOKE emitted
only for privileges that differ from the initdb default) is not yet
byte-faithful for system catalog objects. Resume point: a real one-time
snapshot step (e.g. taken the first time the in-memory catalog finishes
bootstrap, written into a new `c.initPrivs` map keyed like `tableACLs`) would
let `PGInitPrivsRowsForDBOid` read a genuine immutable value instead of
recomputing the default every time.
