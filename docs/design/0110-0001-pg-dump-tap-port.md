# 0110-0001 — pg_dump TAP test port (001_basic)

Status: accepted (partial — 001_basic landed; 002–010 deferred)

## Context

`M0110-0001` ports the upstream `pg_dump` TAP suite
(`postgres/src/bin/pg_dump/t/`) into Go tests under `internal/testport/`.
The suite has six files (001, 002, 003, 004, 005, 010). They split cleanly into
two tiers by what they exercise:

| upstream file | exercises | goopg dependency |
|---|---|---|
| `001_basic.pl` | CLI option handling only | **none** — binary argument parser only |
| `002_pg_dump.pl` | comprehensive schema/object dump | full catalog-view parity |
| `003_pg_dump_with_server.pl` | dump+restore round-trip vs live server | catalog parity + SQL restore |
| `004_pg_dump_parallel.pl` | parallel dump | multi-connection snapshot consistency |
| `005_pg_dump_filterfile.pl` | `--filter` file support | catalog parity |
| `010_dump_connstr.pl` | connection-string handling | live server + catalog parity |

## Decision

Port `001_basic.pl` this loop; defer 002–010.

`001_basic.pl` is a pure command-line-handling test. Its upstream comment is
explicit: the invalid-option / disallowed-combination checks "Doesn't require a
PG instance to be set up". Every assertion is decided by the binary's argument
parser *before* any server connection is attempted. goopg reuses the upstream
`pg_dump` / `pg_restore` / `pg_dumpall` binaries (shipped in
`postgres/local_install/bin`) unchanged, so the port simply drives those
binaries and validates the CLI surface the rest of the test suite depends on.

### Port shape (`internal/testport/pgdump_port_test.go`)

- Reuses the existing `clientToolBin` / `runTool` helpers
  (`client_tools_port_test.go`).
- Adds three small helpers mirroring `PostgreSQL::Test::Utils`:
  `programHelpOk`, `programVersionOk`, `programOptionsHandlingOk`.
- `commandFailsContaining` mirrors `command_fails_like`: non-zero exit + a
  literal substring of the expected error in combined stdout/stderr. Upstream
  uses `qr/\Q…\E/` literal-quoted regexes whose payload is a fixed substring, so
  `strings.Contains` is faithful and avoids regex-escaping drift.
- The `HAVE_LIBZ`-conditional block is reproduced by **probing the binary's
  behaviour** (`pg_dump -Z 15` → "between 1 and 9" means zlib is present) rather
  than reading compile config, so the test self-adapts to either build.

Test function: `TestPort_PgDump001Basic`. CSV row `DU-001` → `port`,
`pass_required=yes`. The umbrella row `E-002` retains the deferred remainder.

## Connection-setup compatibility (enabler for 002–010)

Before pg_dump runs *any* catalog query it executes a fixed handshake in
`setup_connection()` (`postgres/src/bin/pg_dump/pg_dump.c`): a battery of `SET`
commands plus `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY` for a
consistent snapshot. An empirical probe (real `pg_dump --no-sync postgres`
against a live goopg server) showed goopg aborting this handshake before
reaching the first catalog query, so the catalog-parity work below was
unreachable. Two classes of gap were closed:

1. **Unregistered GUCs.** `synchronize_seqscans`, `transaction_timeout`
   (PG 17+) and `row_security` were not in the GUC registry, so the
   corresponding `SET` failed with `unrecognized configuration parameter`.
   Added as accepted no-ops in `internal/config/defaults.go` (boot defaults
   mirror `guc_tables.c`: on/0/on) + `postgresql.conf.sample` entries. goopg
   enforces none of them (no synchronized scans, no per-txn timeout, no RLS),
   but `SET` must succeed.
2. **`SET TRANSACTION` mis-routing.** The server's simple-query string
   fast-path (`internal/server/query.go`) matched the generic `SET ` prefix and
   handed `TRANSACTION ISOLATION LEVEL …` to `handleSet`, which read
   `TRANSACTION` as a GUC name (`unrecognized configuration parameter
   "TRANSACTION"`). A new case routes `SET [LOCAL|SESSION] TRANSACTION …` and
   `SET SESSION CHARACTERISTICS …` through the parser-based executor, which
   already builds a `SetTransactionStmt` (M0096-0002) and applies the isolation
   level. The `"TRANSACTION "` trailing space distinguishes it from the
   `transaction_timeout` GUC. The parser's transaction-mode loop also now
   consumes the comma in `REPEATABLE READ, READ ONLY` (it previously stopped at
   the comma, leaving trailing tokens).

After this slice pg_dump completes `setup_connection()` and proceeds into its
catalog-dump phase, where it hits catalog-view parity gaps (DU-002+ below),
fixed one logical group per loop:

1. **`pg_roles.oid` (collectRoleNames) — LANDED.** pg_dump's `collectRoleNames`
   issues `SELECT oid, rolname FROM pg_catalog.pg_roles ORDER BY 1`
   (`pg_dump.c:10548`) to build its role-oid → name map. goopg's `pg_roles`
   virtual view (`internal/catalog/catalog.go`) gained an `oid` column at
   ordinal 0 carrying OID 10 (`BOOTSTRAP_SUPERUSERID`, the `postgres`
   superuser, per `pg_authid.dat`).
2. **`acldefault()` (getNamespaces) — LANDED.** pg_dump then runs
   `SELECT n.tableoid, n.oid, n.nspname, n.nspowner, n.nspacl,
   acldefault('n', n.nspowner) AS acldefault FROM pg_namespace n`, which failed
   with `function acldefault does not exist`. Added the `acldefault("char", oid)`
   builtin to the executor (`internal/executor/expr.go`, `evalAclDefault`),
   mirroring `acldefault()`/`acldefault_sql()` in
   `src/backend/utils/adt/acl.c`: it computes the hard-wired default privileges
   per object-type char and renders them as aclitem[] text, e.g.
   `acldefault('n', 10)` → `{postgres=UC/postgres}`. The function was already
   seeded in `pg_proc` (OID 3943); only the executor handler was missing. The
   `pg_namespace` columns (`oid`, `nspname`, `nspowner`, `nspacl`) already exist.
   Unit guard: `executor.TestEvalAclDefault` (+ `TestEvalAclDefaultEdgeCases`)
   pins all 13 object types and the privilege-letter order (`ACL_ALL_RIGHTS_STR`
   = `arwdDxtXUCTcsAm`).
3. **`tableoid` column label (getNamespaces) — DONE.** With `acldefault()` in
   place the getNamespaces query *executes* (verified live: all six columns
   return correct values), but pg_dump **segfaulted** during "reading
   schemas": its first projected column `n.tableoid` came back labelled
   `?column?` instead of `tableoid`, so `PQfnumber(res, "tableoid")` returned -1
   and `PQgetvalue(res, i, -1)` read out of bounds (`column number -1 is out of
   range 0..5`, SIGSEGV / exit 139). The *value* resolved correctly (2615); only
   the RowDescription field name was wrong, and it was wrong for **every** table
   (real and virtual): a planner output-column-naming gap, not a catalog-row gap.
   Root cause: `resolveColumnRefAt` lowers a bare `tableoid` on a
   non-partitioned base relation to a constant `*TableOidExpr`, but the planner's
   `targetMeta` (`internal/planner/planner.go`) had no case for that node — only
   the cast-wrapped form (`tableoid::regclass`) was handled — so it fell through
   to `?column?`. Fix: added a `*TableOidExpr` arm to `targetMeta` returning
   `("tableoid", oid)`, mirroring the existing `*CTIDExpr` → `"ctid"` case. The
   analyzer/executor twins (`deriveAnalyzerTargetName`, `deriveTargetName`)
   operate on the *parser* AST where `tableoid` is still a `*parser.ColumnRef`
   and already returned the right name, so no change was needed there. Unit
   guard: `server.TestTableoidColumnName` asserts the RowDescription field name
   for both bare and table-qualified `tableoid` projections.

4. **getTables catalog views (`pg_depend`, `pg_tablespace`,
   `pg_foreign_table`) — DONE.** With `tableoid` labelling fixed, pg_dump
   advances to its `getTables` query (`pg_dump.c:7080-7239`), which is one large
   `SELECT … FROM pg_class c LEFT JOIN pg_depend d … LEFT JOIN pg_tablespace tsp
   … LEFT JOIN pg_am am … LEFT JOIN pg_class tc (toast)` plus a relkind='f'
   subquery against `pg_foreign_table`. Three relations it touches were not
   exposed to the SQL query layer, so the query aborted with `relation "…" does
   not exist` (one per loop iteration of the live probe). All three were added as
   virtual catalog views in `internal/catalog/catalog.go`, alongside the existing
   `pg_am` view, with schemas matching upstream exactly so every column reference
   resolves:
   - **`pg_depend`** (OID 2608, columns classid/objid/objsubid/refclassid/
     refobjid/refobjsubid/deptype) — **empty**. goopg keeps no general dependency
     graph (sequence ownership, extension membership, etc. are untracked), so the
     LEFT JOIN yields NULL `owning_tab`/`owning_col` and `is_identity_sequence`
     = false — the correct result for a server with no recorded dependencies.
   - **`pg_tablespace`** (OID 1213, columns oid/spcname/spcowner/spcacl/
     spcoptions) — the on-disk shared heap (initdb seeds pg_default/pg_global) is
     not wired into the query layer, so the view returns the two bootstrap rows
     (OID 1663 `pg_default`, 1664 `pg_global`, owner 10) plus any in-place
     tablespaces in the M0095-0003 runtime registry, ordered by OID
     (`InMemory.tablespaceVirtualRows`, read-locked). Runtime tablespaces report
     `spcowner` = 10; goopg does not resolve the recorded owner-name to a role
     OID. For user relations `c.reltablespace` is 0, so the join correctly yields
     NULL `reltablespace` (the default tablespace).
   - **`pg_foreign_table`** (OID 3118, columns ftrelid/ftserver/ftoptions) —
     **empty**; goopg implements no foreign-data wrappers, so the relkind='f'
     `foreignserver` subquery returns no rows (and the branch never fires for
     goopg relations).
   Unit guards: `catalog.TestPgTablespaceVirtualView` (bootstrap rows + a runtime
   tablespace surfacing at the correct OID-ordered position),
   `catalog.TestPgDependAndForeignTableViews` (empty views, exact schema). After
   this slice `getTables` resolves all its relations; the next blocker is the
   scalar builtin `array_remove()` (used to strip `check_option=…` from
   `reloptions`), which is the following slice.

5. **`array_remove()` scalar builtin (getTables) — DONE.** With its relations
   resolved, the `getTables` query reached its `reloptions` projection
   `array_remove(array_remove(c.reloptions,'check_option=local'),
   'check_option=cascaded')` (pg_dump strips the two view `WITH CHECK OPTION`
   markers so they are re-emitted as a separate `ALTER VIEW`), which aborted with
   `function array_remove does not exist`. The function was already seeded in
   `pg_proc` (OID 3167, `HandlerName "array_remove"`); only the executor handler
   was missing, so dispatch fell through to `evalStoredRoutineFuncCall` → 42883.
   Added the `array_remove(anyarray, anyelement)` case to `evalFuncCall`
   (`internal/executor/expr.go`, beside `array_append`/`array_cat`): it removes
   every element equal to the second argument from goopg's text-array
   representation (`parseTextArray`/`formatTextArray`). Matching mirrors the
   sibling array builtins — formatted element-text equality, with a NULL element
   matching the `"NULL"` placeholder those siblings emit — and a NULL *array*
   returns NULL (PG's array_remove is `NotStrict` on the element but
   array-strict). Unit guards: `executor.TestEvalArrayRemove` (matching/no-match/
   reloptions-strip/empty-result/NULL-array) and `executor.TestEvalArrayRemoveNested`
   (the exact nested pg_dump form). After this slice `getTables` completes and
   pg_dump advances to its function dump (`getFuncs`), where the next blocker is
   the `pg_init_privs` catalog view (LEFT-JOINed to diff stored vs. initial
   privileges) — the following slice.

6. **`pg_init_privs` virtual catalog view — DONE.** `getFuncs` (like
   `getTables`/`getTypes`/…) LEFT-JOINs `pg_init_privs pip ON (p.oid=pip.objoid
   AND pip.classoid='pg_proc'::regclass AND pip.objsubid=0)` to diff the object's
   stored `*acl` against its initial (initdb/extension-installed) privileges; the
   missing relation aborted the query with `relation "pg_init_privs" does not
   exist`. Added the `pg_init_privs` virtual view (`internal/catalog/catalog.go`,
   beside the slice-4 `pg_depend`/`pg_tablespace`/`pg_foreign_table` block) with
   PG's exact schema (`objoid oid, classoid oid, objsubid int4, privtype "char",
   initprivs aclitem[]`, OID 3394, and — like the upstream catalog — NO `oid`
   system column). It is **empty by construction**: goopg installs no extensions
   and snapshots no initdb-time ACLs, so the LEFT JOIN yields NULL `pip.initprivs`
   and the `proacl IS DISTINCT FROM pip.initprivs` predicate degenerates to "dump
   the full ACL", which is correct for a server that records no initial
   privileges. After this slice `getFuncs` resolves `pg_init_privs`; the next
   blocker is the `pg_proc` view's missing `pronargs`/`proacl`/`proowner` columns
   (plus the `pg_cast`/`pg_transform` views the getFuncs filter references) — the
   following slice.

7. **`pg_proc` `pronargs`/`proacl`/`proowner` + `pg_cast`/`pg_transform` views —
   DONE.** `getFuncs` projects `p.pronargs, p.proargtypes, p.prorettype, p.proacl,
   acldefault('f', p.proowner) AS acldefault, p.pronamespace, p.proowner` and its
   `WHERE` admits a `pg_catalog` function only if it is referenced by a user cast
   (`EXISTS (SELECT 1 FROM pg_cast WHERE pg_cast.oid > <last_builtin> AND
   p.oid = pg_cast.castfunc)`) or transform (`pg_transform.trffromsql/trftosql`);
   the query aborted at `column p.pronargs does not exist`. Added three columns to
   the `pg_proc` virtual view (`internal/initdb/pg_proc_view.go`,
   `registerPgProcView`): `pronargs int2` = number of input args
   (`len(proargtypes)`), `proacl aclitem[]` = NULL (goopg tracks no per-routine
   grants — pg_dump treats every routine as default-privileged), `proowner oid` =
   10 (bootstrap superuser). Both row-builders — the `builtinProcs` loop and the
   user-routine loop (sibling paths) — were updated together. Added the empty
   `pg_cast` (OID 2605: `oid, castsource, casttarget, castfunc, castcontext,
   castmethod`) and `pg_transform` (OID 3576: `oid, trftype, trflang, trffromsql,
   trftosql`) virtual views (`internal/catalog/catalog.go`, beside `pg_init_privs`);
   goopg registers no user casts/transforms, so both are **empty by construction**
   and the two `EXISTS` subqueries are always false — only the genuine
   namespace/ACL predicates select rows (built-in casts/functions are never
   dumped, which is correct). `castfunc`/`trffromsql`/`trftosql` are typed `oid`
   (PG uses `regproc`, which is oid-compatible) so the `p.oid = …` comparisons
   resolve under goopg's oid equality operator. After this slice `getFuncs`
   completes; the next blocker is `getProcLangs`' `SELECT … FROM pg_language WHERE
   lanispl` — goopg has no `pg_language` view (`relation "pg_language" does not
   exist`) — the following slice.

Regression guard: `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`) drives real pg_dump and asserts
no `setup_connection()` error signature appears; it logs the remaining
catalog-parity blocker and auto-tightens to assert exit 0 once a clean dump
works. Unit guards: `config.TestPgDumpConnectionSetupGUCs`,
`parser.TestParseSetTransactionCommaSeparated`.

## Deferred (002–010) — catalog surface estimate

The remaining five tests all block on the same gap: a faithful schema dump
needs broad catalog-view parity. pg_dump issues a fixed battery of catalog
queries against `pg_class`, `pg_attribute`, `pg_type`, `pg_proc`, `pg_depend`,
`pg_namespace`, `pg_constraint`, `pg_index`, `pg_am`, `pg_collation`,
`pg_extension`, `pg_default_acl`, plus `format_type()`, `pg_get_*def()` helper
functions and `pg_catalog.set_config()`. 003 additionally needs SQL-level
restore (CREATE TABLE/INDEX/CONSTRAINT replay) to round-trip. These are tracked
under `M0110-0001` in `.ralph/fix_plan.md`; promote `E-002` rows to `port`
incrementally as the catalog surface lands (002 → schema dump, 003 → round-trip
first, per the fix_plan action).

## Verification

`go test -v -run TestPort_PgDump001Basic ./internal/testport/` → PASS.
`go test -v -run TestPort_PgDumpConnectionSetup ./internal/testport/` → PASS
(passes connection setup + collectRoleNames + getNamespaces' `acldefault()`;
logs the next gap: the `tableoid` `?column?` mislabel that segfaults pg_dump in
"reading schemas").
`go test -v -run TestEvalAclDefault ./internal/executor/` → PASS.
`go test ./internal/config/ ./internal/parser/ ./internal/server/` → PASS.
`go run ./cmd/gen-oracle-port-status` regenerates the status markdown.
