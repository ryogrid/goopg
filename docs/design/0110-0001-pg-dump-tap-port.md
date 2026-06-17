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

8. **`pg_language` virtual view — DONE.** `getProcLangs` runs `SELECT tableoid,
   oid, lanname, lanpltrusted, lanplcallfoid, laninline, lanvalidator, lanacl,
   acldefault('l', lanowner) AS acldefault, lanowner FROM pg_language WHERE
   lanispl ORDER BY oid`; goopg had no `pg_language` view, so the query aborted at
   `relation "pg_language" does not exist`. Added the empty `pg_language` virtual
   view (`internal/catalog/catalog.go`, OID 2612, beside `pg_transform`) with the
   `pg_language.h` schema (`oid, lanname name, lanowner oid, lanispl bool,
   lanpltrusted bool, lanplcallfoid oid, laninline oid, lanvalidator oid, lanacl
   aclitem[]`). The view is **empty by construction**: the `WHERE lanispl`
   predicate admits only user-installed procedural languages — the built-in
   `internal`/`c`/`sql` languages have `lanispl = false` and are filtered out (PG
   never dumps them), and goopg installs no user PLs. `lanowner` is typed `oid` so
   `acldefault('l', lanowner)` resolves. After this slice `getProcLangs` completes;
   the next blocker is `getOperators`' `SELECT tableoid, oid, oprname,
   oprnamespace, oprowner, oprkind, oprleft, oprright, oprcode::oid AS oprcode FROM
   pg_operator` — goopg has no `pg_operator` view (`relation "pg_operator" does not
   exist`) — the following slice.

9. **`pg_operator` virtual view — DONE.** `getOperators` runs `SELECT tableoid,
   oid, oprname, oprnamespace, oprowner, oprkind, oprleft, oprright, oprcode::oid
   AS oprcode FROM pg_operator`; goopg had no `pg_operator` view, so the query
   aborted at `relation "pg_operator" does not exist`. Added the empty
   `pg_operator` virtual view (`internal/catalog/catalog.go`, OID 2617, beside
   `pg_language`) with the `pg_operator.h` schema (`oid, oprname name,
   oprnamespace oid, oprowner oid, oprkind char, oprcanmerge bool, oprcanhash
   bool, oprleft oid, oprright oid, oprresult oid, oprcom oid, oprnegate oid,
   oprcode oid, oprrest oid, oprjoin oid`). The view is **empty by
   construction**: `getOperators` reads all operators (built-ins included) and
   filters out system-defined ones at dump-out time by namespace dumpability —
   the built-ins live in `pg_catalog` (never dumped), and goopg defines no user
   operators. `oprcode` is `regproc` in PG but oid-compatible, so it is typed
   `oid` and the `oprcode::oid` cast resolves as a no-op. After this slice
   `getOperators` completes; the next blocker is `getOpclasses`' `SELECT
   tableoid, oid, opcmethod, opcname, opcnamespace, opcowner FROM pg_opclass` —
   goopg has no `pg_opclass` view (`relation "pg_opclass" does not exist`) — the
   following slice.
10. **`pg_opclass` virtual view — DONE.** `getOpclasses` runs `SELECT tableoid,
    oid, opcmethod, opcname, opcnamespace, opcowner FROM pg_opclass`; goopg had
    no `pg_opclass` view, so the query aborted at `relation "pg_opclass" does
    not exist`. Added the empty `pg_opclass` virtual view
    (`internal/catalog/catalog.go`, OID 2616, beside `pg_operator`) with the
    `pg_opclass.h` schema (`oid, opcmethod oid, opcname name, opcnamespace oid,
    opcowner oid, opcfamily oid, opcintype oid, opcdefault bool, opckeytype
    oid`). The view is **empty by construction**: `getOpclasses` reads all
    operator classes and filters out system-defined ones at dump-out time by
    namespace dumpability — the built-ins live in `pg_catalog` (never dumped),
    and goopg defines no user operator classes. After this slice `getOpclasses`
    completes; the next blocker is `getOpfamilies`' `SELECT tableoid, oid,
    opfmethod, opfname, opfnamespace, opfowner FROM pg_opfamily` — goopg has no
    `pg_opfamily` view (`relation "pg_opfamily" does not exist`) — the following
    slice.
11. **`pg_opfamily` virtual view — DONE.** `getOpfamilies` runs `SELECT
    tableoid, oid, opfmethod, opfname, opfnamespace, opfowner FROM pg_opfamily`;
    goopg had no `pg_opfamily` view, so the query aborted at `relation
    "pg_opfamily" does not exist`. Added the empty `pg_opfamily` virtual view
    (`internal/catalog/catalog.go`, OID 2753, beside `pg_opclass`) with the
    `pg_opfamily.h` schema (`oid, opfmethod oid, opfname name, opfnamespace oid,
    opfowner oid`). The view is **empty by construction**: `getOpfamilies` reads
    all operator families and filters out system-defined ones at dump-out time
    by namespace dumpability — the built-ins live in `pg_catalog` (never
    dumped), and goopg defines no user operator families. After this slice
    `getOpfamilies` completes; the next blocker is `getTSParsers`' `SELECT
    tableoid, oid, prsname, prsnamespace, prsstart::oid, prstoken::oid,
    prsend::oid, prsheadline::oid, prslextype::oid FROM pg_ts_parser` — goopg
    has no `pg_ts_parser` view (`relation "pg_ts_parser" does not exist`) — the
    following slice.
12. **`pg_ts_parser` virtual view — DONE.** `getTSParsers` runs `SELECT
    tableoid, oid, prsname, prsnamespace, prsstart::oid, prstoken::oid,
    prsend::oid, prsheadline::oid, prslextype::oid FROM pg_ts_parser`; goopg had
    no `pg_ts_parser` view, so the query aborted at `relation "pg_ts_parser"
    does not exist`. Added the empty `pg_ts_parser` virtual view
    (`internal/catalog/catalog.go`, OID 3601, beside `pg_opfamily`) with the
    `pg_ts_parser.h` schema (`oid, prsname name, prsnamespace oid, prsstart
    regproc, prstoken regproc, prsend regproc, prsheadline regproc, prslextype
    regproc`); the `::oid` casts in the query are no-ops since `regproc` is
    oid-compatible. The view is **empty by construction**: `getTSParsers` reads
    all TS parsers and filters out system-defined ones at dump-out time by
    namespace dumpability — the built-ins live in `pg_catalog` (never dumped),
    and goopg defines no user TS parsers. After this slice `getTSParsers`
    completes; the next blocker is `getTSTemplates`' `SELECT tableoid, oid,
    tmplname, tmplnamespace, tmplinit::oid, tmpllexize::oid FROM pg_ts_template`
    — goopg has no `pg_ts_template` view (`relation "pg_ts_template" does not
    exist`) — the following slice.
13. **`pg_ts_template` virtual view — DONE.** `getTSTemplates` runs `SELECT
    tableoid, oid, tmplname, tmplnamespace, tmplinit::oid, tmpllexize::oid FROM
    pg_ts_template`; goopg had no `pg_ts_template` view, so the query aborted at
    `relation "pg_ts_template" does not exist`. Added the empty `pg_ts_template`
    virtual view (`internal/catalog/catalog.go`, OID 3764, beside
    `pg_ts_parser`) with the `pg_ts_template.h` schema (`oid, tmplname name,
    tmplnamespace oid, tmplinit regproc, tmpllexize regproc`); the `::oid` casts
    are no-ops since `regproc` is oid-compatible. Empty by construction:
    built-in TS templates live in `pg_catalog` (filtered out by namespace
    dumpability), and goopg defines no user TS templates. After this slice
    `getTSTemplates` completes; the next blocker is `getTSDictionaries`' `SELECT
    tableoid, oid, dictname, dictnamespace, dictowner, dicttemplate,
    dictinitoption FROM pg_ts_dict` — goopg has no **queryable** `pg_ts_dict`
    relation (`relation "pg_ts_dict" does not exist`). Although initdb seeds a
    `pg_class` entry for pg_ts_dict at OID 3600, goopg's query layer resolves
    these system catalogs through the in-memory virtual-view registry, not the
    on-disk heap, so the seeded row is invisible to pg_dump's SELECT; an empty
    `pg_ts_dict` virtual view is the following slice. (NOTE: `getTSDictionaries`
    runs AFTER `getTSTemplates`, which is why this blocker only surfaced once
    slice 13 cleared `pg_ts_template`; the slice-12 note that "pg_ts_dict
    already passes" was a misread — the dump aborted at `getTSTemplates` before
    reaching it.)
14. **`pg_ts_dict` virtual view — DONE.** `getTSDictionaries` runs `SELECT
    tableoid, oid, dictname, dictnamespace, dictowner, dicttemplate,
    dictinitoption FROM pg_ts_dict`; goopg had no queryable `pg_ts_dict`
    relation, so the query aborted at `relation "pg_ts_dict" does not exist`.
    Added the empty `pg_ts_dict` virtual view (`internal/catalog/catalog.go`,
    OID 3600, beside `pg_ts_template`) with the `pg_ts_dict.h` schema (`oid,
    dictname name, dictnamespace oid, dictowner oid, dicttemplate oid,
    dictinitoption text`); `dicttemplate` is an `oid` FK to `pg_ts_template`
    (not a regproc). Empty by construction: built-in TS dictionaries live in
    `pg_catalog` (filtered out by namespace dumpability), and goopg defines no
    user TS dictionaries. After this slice `getTSDictionaries` completes; the
    next blocker is `getTSConfigurations`' `SELECT tableoid, oid, cfgname,
    cfgnamespace, cfgowner, cfgparser FROM pg_ts_config` — goopg has no
    queryable `pg_ts_config` relation (`relation "pg_ts_config" does not
    exist`); an empty `pg_ts_config` virtual view is the following slice.
15. **`pg_ts_config` virtual view — DONE.** `getTSConfigurations` runs `SELECT
    tableoid, oid, cfgname, cfgnamespace, cfgowner, cfgparser FROM pg_ts_config`;
    goopg had no queryable `pg_ts_config` relation, so the query aborted at
    `relation "pg_ts_config" does not exist`. Added the empty `pg_ts_config`
    virtual view (`internal/catalog/catalog.go`, OID 3602, beside `pg_ts_dict`)
    with the `pg_ts_config.h` schema (`oid, cfgname name, cfgnamespace oid,
    cfgowner oid, cfgparser oid`); `cfgparser` is an `oid` FK to `pg_ts_parser`.
    Empty by construction: built-in TS configurations live in `pg_catalog`
    (filtered out by namespace dumpability), and goopg defines no user TS
    configurations. After this slice `getTSConfigurations` completes; the next
    blocker (confirmed empirically) is `getForeignDataWrappers`' `SELECT
    tableoid, oid, fdwname, fdwowner, fdwhandler::pg_catalog.regproc,
    fdwvalidator::pg_catalog.regproc, fdwacl, …, array_to_string(…fdwoptions…)
    AS fdwoptions FROM pg_foreign_data_wrapper` — goopg has no queryable
    `pg_foreign_data_wrapper` relation (`relation "pg_foreign_data_wrapper" does
    not exist`); an empty `pg_foreign_data_wrapper` virtual view is the following
    slice.
16. **`pg_foreign_data_wrapper` virtual view — DONE.** `getForeignDataWrappers`
    runs `SELECT tableoid, oid, fdwname, fdwowner, fdwhandler::pg_catalog.regproc,
    fdwvalidator::pg_catalog.regproc, fdwacl, acldefault('F', fdwowner) AS
    acldefault, array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
    quote_literal(option_value) FROM pg_options_to_table(fdwoptions) ORDER BY
    option_name), E',\n    ') AS fdwoptions FROM pg_foreign_data_wrapper`; goopg
    had no queryable `pg_foreign_data_wrapper` relation, so the query aborted at
    `relation "pg_foreign_data_wrapper" does not exist`. Added the empty
    `pg_foreign_data_wrapper` virtual view (`internal/catalog/catalog.go`, OID
    2328, beside `pg_ts_config`) with the `pg_foreign_data_wrapper.h` schema
    (`oid, fdwname name, fdwowner oid, fdwhandler oid, fdwvalidator oid, fdwacl
    aclitem[], fdwoptions text[]`); `fdwhandler`/`fdwvalidator` are `oid` FKs to
    `pg_proc`. Empty by construction: goopg defines no foreign-data wrappers (no
    `CREATE FOREIGN DATA WRAPPER`), and only user-defined FDWs are dumped. After
    this slice the relation resolves, but the query advances to a **new** blocker
    (confirmed empirically): `column "option_name" does not exist`. The ARRAY
    subquery selects from `pg_options_to_table(fdwoptions)`, a set-returning
    function with output columns `(option_name, option_value)`. goopg seeds
    `pg_options_to_table` in `pg_proc` (OID 2289) but does not implement it as an
    executable FROM-clause SRF, so the subquery's column references are
    unresolvable at plan time — even though the outer view is empty (goopg
    resolves the subquery columns during planning regardless of outer
    emptiness). Implementing `pg_options_to_table` as a FROM-clause SRF (`text[]`
    of `name=value` options → rows of `(option_name, option_value)`) is the
    following slice; `getForeignServers` (`pg_foreign_server`) and
    `getUserMappings` (`pg_user_mappings`) follow.
17. **`pg_options_to_table` FROM-clause SRF — DONE.** `getForeignDataWrappers`'
    ARRAY subquery expands `fdwoptions` via `pg_options_to_table(fdwoptions)`,
    a set-returning function with output columns `(option_name text,
    option_value text)`; goopg seeded it in `pg_proc` (OID 2289) but never
    implemented it as an executable FROM-clause SRF, so the subquery's column
    references aborted planning with `column "option_name" does not exist`.
    Implemented across the standard FROM-SRF wiring, mirroring the
    `pg_partition_tree`/`unnest` precedents:
    - parser (`internal/parser/select.go`): `pg_options_to_table` added to the
      FROM-clause known-builtin SRF name switch;
    - plan node `PgOptionsToTable` (`internal/planner/plan.go`: `Arg`,
      `schema`) + `planPgOptionsToTable` (`internal/planner/planner.go`): two
      `text` output columns `option_name`/`option_value`, overridable by an `AS
      alias(col, col)` list, arg resolved through `lateralCtx`;
    - `FoldConstants` + `walkPlanExprs` cases for the new node
      (`foldconst.go`, `unnest.go`);
    - executor op `pgOptionsToTableOp`
      (`internal/executor/operators_pg_options_to_table.go`): evaluates the
      `text[]` arg against the current outer (lateral) row, expands it via
      `expandArrayDatum`, and splits each element at the FIRST `=` (later `=`
      stay in the value; bare names yield a NULL `option_value`) — faithful to
      `untransformRelOptions` / `pg_options_to_table` in
      `src/backend/foreign/foreign.c`;
    - **sibling path** (`internal/analyzer/analyzer.go` `tableFuncColumns`):
      the analyzer runs before the planner and derives FROM-SRF output columns
      independently; without a matching case the bare-column reference
      `option_name` failed analysis BEFORE FROM planning. Added the
      `pg_options_to_table` case there too. (This was the non-obvious bug: the
      executor produced correct rows for `SELECT *`, but named-column
      resolution went through the analyzer's separate derivation.)
    Unit guards: `TestPgOptionsToTable{SplitsNameValue,SplitsAtFirstEquals,
    EmptyArray,ColumnAlias}` (`internal/executor`). After this slice the
    subquery resolves and the query advances to a **new** blocker (confirmed
    empirically): `column "fdwoptions" does not exist`. `fdwoptions` is a
    CORRELATED reference to the outer `pg_foreign_data_wrapper` row, and goopg
    cannot resolve a FROM-clause SRF argument that reaches up into an OUTER
    query level from inside a scalar/ARRAY subquery. (Same-level explicit
    `FROM t, LATERAL pg_options_to_table(t.opts)` resolves fine.) Threading the
    outer scope into the planner's FROM-clause SRF argument resolution is the
    following slice; `getForeignServers` (`pg_foreign_server`) and
    `getUserMappings` (`pg_user_mappings`) follow.

18. **Correlated FROM-clause SRF argument resolution — DONE.**
    `getForeignDataWrappers`' ARRAY subquery
    `ARRAY(SELECT … FROM pg_options_to_table(fdwoptions))` references
    `fdwoptions` from the OUTER `pg_foreign_data_wrapper` row. The planner
    resolved the SRF arg against a context built only from same-level FROM
    siblings (`planFromClause`), and the lexical-scope `parent` (`planParent`)
    was only attached to the SELECT's resolveContext *after* FROM planning had
    already run — so a correlated arg with no left-siblings had no path up to
    the outer scope and failed with `42703 column "fdwoptions" does not exist`.
    Fix (`internal/planner/planner.go` `planPgOptionsToTable`): build the
    arg-resolution context by chaining up to `planParent`, exactly mirroring
    the existing `generate_series` precedent — when there are no lateral
    siblings use `&resolveContext{cat: cat, parent: planParent}`; when there
    are siblings but no parent, copy them and set `parent = planParent`. The
    correlated `fdwoptions` then resolves to an `OuterColumnRef`, which the
    executor (`pgOptionsToTableOp.Open`) evaluates against the outer row pushed
    onto `ctx.OuterRows` — once per outer row. The analyzer needed **no**
    change: `tableFuncColumns` builds the SRF's *output* columns but never
    resolves the arg expression, so analysis already passed (verified
    empirically — the 42703 came from the planner at the `opts` byte offset).
    Guards: `TestPlanPgOptionsToTableCorrelatedArg` (`internal/planner` — plans
    the ARRAY, scalar, and same-level LATERAL forms) and
    `TestPgOptionsToTableCorrelatedArg` (`internal/executor` — drives a
    correlated scalar subquery, asserting one result row per outer row with no
    OuterColumnRef out-of-range crash; exact expanded-option counts are not
    asserted because reading a `text[]` column back from the heap currently
    yields the binary array encoding rather than the text representation
    `expandArrayDatum` parses — a separate, pre-existing limitation orthogonal
    to the correlation fix, and irrelevant to the real pg_dump path since
    `pg_foreign_data_wrapper` is empty so the subquery never evaluates a
    non-empty `fdwoptions`). After this slice `getForeignDataWrappers` passes
    end-to-end; pg_dump advances to `getForeignServers` and the **new** blocker
    is `relation "pg_foreign_server" does not exist`. That query also expands
    `srvoptions` through the now-working correlated `pg_options_to_table`
    subquery, so the next slice is purely the empty `pg_foreign_server` virtual
    view; `getUserMappings` (`pg_user_mappings`) follows.

19. **`pg_foreign_server` virtual view — DONE.** `getForeignServers` runs
    `SELECT tableoid, oid, srvname, srvowner, srvfdw, srvtype, srvversion,
    srvacl, acldefault('S', srvowner) AS acldefault,
    array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
    quote_literal(option_value) FROM pg_options_to_table(srvoptions) ORDER BY
    option_name), E',\n    ') AS srvoptions FROM pg_foreign_server`; goopg had
    no queryable `pg_foreign_server` relation, so the query aborted at
    `relation "pg_foreign_server" does not exist`. Added the empty
    `pg_foreign_server` virtual view (`internal/catalog/catalog.go`, OID 1417,
    beside `pg_foreign_data_wrapper`) with the `pg_foreign_server.h` schema
    (`oid, srvname name, srvowner oid, srvfdw oid, srvtype text, srvversion
    text, srvacl aclitem[], srvoptions text[]`); goopg defines no foreign
    servers (no `CREATE SERVER`), so it is correctly empty (0 rows) and the
    correlated `pg_options_to_table(srvoptions)` ARRAY subquery (slice 18,
    already working) is never evaluated — no new SRF work. After this slice
    `getForeignServers` passes; pg_dump advances and — because goopg has no
    foreign servers, `getUserMappings` short-circuits without a catalog query —
    the **new** blocker is `getDefaultACLs`: `relation "pg_default_acl" does
    not exist`. An empty `pg_default_acl` virtual view (OID 826) is the next
    slice.
20. **`pg_default_acl` virtual view — DONE.** `getDefaultACLs` runs
    `SELECT oid, tableoid, defaclrole, defaclnamespace, defaclobjtype, defaclacl,
    CASE WHEN defaclnamespace = 0 THEN acldefault(CASE WHEN defaclobjtype = 'S'
    THEN 's'::"char" ELSE defaclobjtype END, defaclrole) ELSE '{}' END AS
    acldefault FROM pg_default_acl`; goopg had no queryable `pg_default_acl`
    relation, so the query aborted at `relation "pg_default_acl" does not
    exist`. Added the empty `pg_default_acl` virtual view
    (`internal/catalog/catalog.go`, OID 826, beside `pg_foreign_server`) with
    the `pg_default_acl.h` schema (`oid, defaclrole oid, defaclnamespace oid,
    defaclobjtype "char", defaclacl aclitem[]`); goopg defines no default-ACL
    entries (no `ALTER DEFAULT PRIVILEGES`), so it is correctly empty (0 rows)
    and the `CASE`/`acldefault` projection is never evaluated — no new
    expression work. After this slice `getDefaultACLs` passes; pg_dump advances
    to `getConversions`, and the **new** blocker is `relation "pg_conversion"
    does not exist` (`SELECT tableoid, oid, conname, connamespace, conowner FROM
    pg_conversion`). A `pg_conversion` virtual view (OID 2607) is the next
    slice — note PG ships ~130 built-in conversions there, but pg_dump filters
    them as built-ins, so an empty view may suffice (verify empirically).
21. **`pg_conversion` virtual view — DONE.** `getConversions` runs
    `SELECT tableoid, oid, conname, connamespace, conowner FROM pg_conversion`
    ("find all conversions, including builtin conversions; we filter out
    system-defined conversions at dump-out time"); goopg had no queryable
    `pg_conversion` relation, so the query aborted at `relation "pg_conversion"
    does not exist`. Added the empty `pg_conversion` virtual view
    (`internal/catalog/catalog.go`, OID 2607, beside `pg_default_acl`) with the
    `pg_conversion.h` schema (`oid, conname name, connamespace oid, conowner oid,
    conforencoding int4, contoencoding int4, conproc regproc(oid), condefault
    bool`). Although PG ships ~130 built-in conversions, every one lives in the
    `pg_catalog` namespace and is filtered out at dump-out time
    (`selectDumpableObject` marks `pg_catalog` objects `DUMP_COMPONENT_NONE`), so
    the **empty** view (0 rows) produces an identical dump — confirmed
    empirically: pg_dump now advances past `getConversions` to `getCasts`. The
    **new** blocker is `relation "pg_range" does not exist` (`SELECT tableoid,
    oid, castsource, casttarget, castfunc, castcontext, castmethod FROM pg_cast c
    WHERE NOT EXISTS ( SELECT 1 FROM pg_range r WHERE c.castsource = r.rngtypid
    AND c.casttarget = r.rngmultitypid ) ORDER BY 3,4`): `pg_cast` already exists,
    but its `NOT EXISTS` subquery references `pg_range`, which does not. An empty
    `pg_range` virtual view (OID 3541) is the next slice (goopg defines no range
    types, so empty should suffice; verify empirically).
22. **`pg_range` virtual view — DONE.** `getCasts` runs `SELECT tableoid, oid,
    castsource, casttarget, castfunc, castcontext, castmethod FROM pg_cast c WHERE
    NOT EXISTS ( SELECT 1 FROM pg_range r WHERE c.castsource = r.rngtypid AND
    c.casttarget = r.rngmultitypid ) ORDER BY 3,4` (range types' auto-generated
    casts are excluded so they aren't dumped separately); `pg_cast` existed but the
    `NOT EXISTS` subquery referenced `pg_range`, which did not, so the query aborted
    at `relation "pg_range" does not exist`. Added the empty `pg_range` virtual view
    (`internal/catalog/catalog.go`, OID 3541, beside `pg_conversion`) with the
    `pg_range.h` schema — note `pg_range` has **no** `oid` column; `rngtypid` is the
    key (`rngtypid oid, rngsubtype oid, rngmultitypid oid, rngcollation oid,
    rngsubopc oid, rngcanonical regproc(oid), rngsubdiff regproc(oid)`). goopg
    defines no range types, so the `NOT EXISTS` is always true and the **empty**
    view (0 rows) produces an identical dump — confirmed empirically: pg_dump now
    advances past `getCasts` to `getEventTriggers`. The **new** blocker is
    `relation "pg_event_trigger" does not exist` (`SELECT e.tableoid, e.oid,
    evtname, evtenabled, evtevent, evtowner, array_to_string(array(select
    quote_literal(x) from unnest(evttags) as t(x)), ', ') as evttags,
    e.evtfoid::regproc as evtfname FROM pg_event_trigger e ORDER BY e.oid`). An
    empty `pg_event_trigger` virtual view (OID 3466) is the next slice (goopg
    defines no event triggers, so empty should suffice; verify empirically) —
    plus the correlated `unnest(evttags)` arg resolution noted below.
23. **`pg_event_trigger` virtual view + correlated `unnest()` arg — DONE.**
    `getEventTriggers` runs `SELECT e.tableoid, e.oid, evtname, evtenabled,
    evtevent, evtowner, array_to_string(array(select quote_literal(x) from
    unnest(evttags) as t(x)), ', ') as evttags, e.evtfoid::regproc as evtfname
    FROM pg_event_trigger e ORDER BY e.oid`. Two gaps, fixed together:
    (a) goopg had no queryable `pg_event_trigger` relation. Added the empty
    `pg_event_trigger` virtual view (`internal/catalog/catalog.go`, OID 3466,
    beside `pg_range`) with the `pg_event_trigger.h` schema (`oid, evtname name,
    evtevent name, evtowner oid, evtfoid oid, evtenabled "char", evttags
    text[]`). goopg defines no event triggers (no `CREATE EVENT TRIGGER`), so the
    empty view (0 rows) produces an identical dump.
    (b) With the relation present, the query then failed at `column "evttags"
    does not exist` — the **same correlated FROM-clause SRF arg bug as slice 18**,
    but for `unnest` rather than `pg_options_to_table`. The `array(select … from
    unnest(evttags) …)` subquery references `evttags` from the OUTER
    `pg_event_trigger` row, but `planFromUnnest` (`internal/planner/planner.go`)
    built its arg-resolution context from same-level lateral siblings only
    (`ctx := &resolveContext{}; if lateralCtx != nil { ctx = lateralCtx }`),
    never chaining up to `planParent`, so a correlated arg with no left-siblings
    had no path to the outer scope. Fix mirrors `planPgOptionsToTable` /
    `planGenerateSeries`: build `ctx := &resolveContext{parent: planParent}` and,
    when lateral siblings exist with no parent, copy them and set `parent =
    planParent`. `evttags` then resolves to an `OuterColumnRef` the executor
    evaluates per outer row. With 0 rows the projection never evaluates, so the
    `text[]`-from-heap binary-decode limitation (orthogonal, pre-existing) does
    not bite the real pg_dump path. After this slice `getEventTriggers` passes
    end-to-end; pg_dump advances into the per-table attribute dump
    (`getTableAttrs`) and the **new** blocker is `column a.attstattarget does not
    exist` — that query reads many `pg_attribute`/`pg_constraint`/`pg_type`
    columns goopg's views do not yet expose (`attstattarget, attstorage,
    attfdwoptions, attcompression, attidentity, atthasmissing, attmissingval,
    attgenerated, conislocal, …`). Broadening those catalog columns is the next
    slice — deeper than the empty-view additions. Guard:
    `TestPlanUnnestCorrelatedArg` (`internal/planner` — plans the ARRAY, scalar,
    and same-level LATERAL forms of correlated `unnest`).
24. **`pg_attribute.attstattarget` column — DONE.** The `getTableAttrs`
    per-table attribute dump query (`pg_dump.c:9162`) reads `a.attstattarget`.
    goopg's pg_attribute already exposed every other column getTableAttrs reads
    (attstorage, attcompression, attidentity, atthasmissing, attmissingval,
    attgenerated, attfdwoptions, attcollation, attislocal, atthasdef, …); only
    `attstattarget` was missing. PG18 (`pg_attribute.h`) declares it a NULLABLE
    `int2` in the `CATALOG_VARLEN` section (`BKI_FORCE_NULL`), distinct from the
    pre-PG17 `int4 NOT NULL`. Added it as a single column to all four sibling
    layouts in lockstep: `catalog.PGAttributeColumns` (the queryable schema used
    for name resolution), `initdb.pgAttrColDefs` + `pgAttributeRow` (nailed
    catalog heap write), and `pg18_user_catalog_rows.pgAttributeColumnsPG18` +
    `buildUserPGAttributeRow` (user-table heap write). **It is appended LAST, not
    placed at the PG18-canonical position #4** (after atttypid): goopg's on-disk
    pg_attribute is already non-canonical (no `attcacheoff`, `attlen` before
    `attnum`), and `catalog.DecodePGAttributePhysicalRow` reads attrelid/attname/
    atttypid/attnum/attnotnull/attisdropped by **hardcoded byte offset**.
    Appending a nullable trailing column that is always NULL (exactly like the
    existing attacl/attoptions/attfdwoptions/attmissingval) keeps every byte
    offset valid; the null bitmap grows 3→4 bytes but `MAXALIGN(8)` keeps
    `t_hoff = 32`, so the data region and all positional readers are unchanged.
    SELECT resolves columns by name, so pg_dump reads `a.attstattarget` → NULL,
    which it treats as the default stats target (-1). Note: the relcache-init
    tupdesc `initdb.pgAttributeAttrs` (what an attaching PG standby reads) is a
    pre-existing, separately-divergent 24-col layout (it lists attstattarget#4 +
    attcacheoff#8 but omits attfdwoptions/attmissingval) and was intentionally
    left unchanged — making pg_attribute fully PG18-canonical on disk is a larger
    PG-standby task out of this slice's scope. After this slice `getTableAttrs`
    passes; the **new** blocker is `relation "pg_partitioned_table" does not
    exist` (partition-key detection: `SELECT partrelid FROM pg_partitioned_table
    WHERE (SELECT c.oid FROM pg_opclass …) = ANY(partclass)`) — back to the
    empty-view pattern for the next slice. Guards: count assertions updated in
    `catalog.TestPGAttributeColumnsCount` (24→25), `initdb.TestBootstrappedPG-
    AttributeRowsReadable` (24→25), `initdb.TestPgAttributeRowEmitsNullForOptional-
    ArrayColumns` (24→25 cols, attstattarget added to the NULL set).
25. **`pg_partitioned_table` empty virtual view — DONE.** After `getTableAttrs`,
    pg_dump probes partition keys with `SELECT partrelid FROM pg_partitioned_table
    WHERE (SELECT c.oid FROM pg_opclass c JOIN pg_am a ON c.opcmethod = a.oid
    WHERE opcname = 'enum_ops' AND opcnamespace = 'pg_catalog'::regnamespace AND
    amname = 'hash') = ANY(partclass)`. goopg surfaces partition membership via
    `pg_class.relkind='p'/'P'` + `pg_inherits`, not a separate per-partition-key
    heap, so an empty view (0 rows) is correct — no user partitioned tables in
    the dumped schema. Added an empty `pg_partitioned_table` virtual view (OID
    3350) in `internal/catalog/catalog.go` beside `pg_range`/`pg_event_trigger`.
    Schema matches `pg_partitioned_table.h`: partrelid oid, partstrat "char",
    partnatts int2, partdefid oid, partattrs int2vector, partclass oidvector,
    partcollation oidvector, partexprs pg_node_tree — with int2vector/oidvector
    represented as int2[]/oid[] per goopg's pg_index `indkey`/`indclass`
    convention. With 0 rows `= ANY(partclass)` is never evaluated. After this
    slice the partition probe passes; the **new** blocker is `relation
    "pg_trigger" does not exist` (per-table trigger collection `getTriggers`:
    `SELECT t.tgrelid, t.tgname, pg_get_triggerdef(...) … FROM unnest('{}'::oid[])
    AS src(tbloid) JOIN pg_trigger t ON …`) — another empty-view slice next.

26. **`pg_trigger` empty virtual view — DONE.** After `getTableAttrs`, pg_dump's
    `getTriggers` collects per-table triggers with `SELECT t.tgrelid, t.tgname,
    pg_catalog.pg_get_triggerdef(t.oid, false) AS tgdef, t.tgenabled, t.tableoid,
    t.oid, t.tgparentid <> 0 AS tgispartition FROM unnest('{}'::pg_catalog.oid[])
    AS src(tbloid) JOIN pg_catalog.pg_trigger t ON (src.tbloid = t.tgrelid) LEFT
    JOIN pg_catalog.pg_trigger u ON (u.oid = t.tgparentid) WHERE ((NOT
    t.tgisinternal AND t.tgparentid = 0) OR t.tgenabled != u.tgenabled) ORDER BY
    t.tgrelid, t.tgname`. goopg has no user-defined triggers, so an empty view (0
    rows) is correct; the `unnest('{}')` source is empty so the JOIN and
    `pg_get_triggerdef` are never evaluated. Added an empty `pg_trigger` virtual
    view (OID 2620) in `internal/catalog/catalog.go` beside
    `pg_partitioned_table`. Schema matches `pg_trigger.h`: oid, tgrelid oid,
    tgparentid oid, tgname name, tgfoid oid, tgtype int2, tgenabled "char",
    tgisinternal bool, tgconstrrelid oid, tgconstrindid oid, tgconstraint oid,
    tgdeferrable bool, tginitdeferred bool, tgnargs int2, tgattr int2vector,
    tgargs bytea, tgqual pg_node_tree, tgoldtable name, tgnewtable name — with
    int2vector represented as int2[] per goopg's pg_index `indkey` convention.
    After this slice the trigger probe passes; the **new** blocker is `relation
    "pg_rewrite" does not exist` (rule collection `getRules`: `SELECT tableoid,
    oid, rulename, ev_class AS ruletable, ev_type, is_instead, ev_enabled FROM
    pg_rewrite ORDER BY oid`) — another empty-view slice next.

27. **`pg_rewrite` empty virtual view — DONE.** After `getTriggers`, pg_dump's
    `getRules` collects rewrite rules with `SELECT tableoid, oid, rulename,
    ev_class AS ruletable, ev_type, is_instead, ev_enabled FROM pg_rewrite ORDER
    BY oid`. goopg has no user-defined rules, so an empty view (0 rows) is
    correct (an `ORDER BY oid` over an empty relation yields no rows). Added an
    empty `pg_rewrite` virtual view (OID 2618) in `internal/catalog/catalog.go`
    beside `pg_trigger`. Schema matches `pg_rewrite.h`: oid, rulename name,
    ev_class oid, ev_type "char", ev_enabled "char", is_instead bool, ev_qual
    pg_node_tree, ev_action pg_node_tree. After this slice the rule probe passes;
    the **new** blocker is `column p.pubgencols does not exist` (publication
    collection `getPublications`).

28. **`pg_publication.pubgencols` column — DONE.** pg_dump's `getPublications`
    issues `SELECT p.tableoid, p.oid, p.pubname, p.pubowner, p.puballtables,
    p.pubinsert, p.pubupdate, p.pubdelete, p.pubtruncate, p.pubviaroot,
    p.pubgencols FROM pg_publication p`. goopg's `pg_publication` (a virtual
    view backed by the `*catalog.PubSub` registry, in
    `internal/initdb/replication_views.go`) lacked PG18's `pubgencols` column.
    Appended `pubgencols` ("char": 'n'=none, 's'=stored generated columns —
    see `pg_publication.h`); goopg does not publish generated columns, so 'n' is
    emitted for every publication row. After this slice the publication probe
    passes; the **new** blocker is `relation "pg_largeobject_metadata" does not
    exist` (large-object collection `getBlobs`: `SELECT oid, lomowner, lomacl,
    acldefault('L', lomowner) AS acldefault FROM pg_largeobject_metadata ORDER BY
    lomowner, lomacl::pg_catalog.text, oid`) — another empty-view slice next.

29. **`pg_largeobject_metadata` empty virtual view — DONE.** pg_dump's
    `getBlobs` collects large objects with `SELECT oid, lomowner, lomacl,
    acldefault('L', lomowner) AS acldefault FROM pg_largeobject_metadata ORDER BY
    lomowner, lomacl::pg_catalog.text, oid`. goopg has no large-object support,
    so an empty view (0 rows) is correct; because the row set is empty the
    `acldefault('L', lomowner)` projection is never evaluated (so no
    `function acldefault does not exist` blocker surfaces). Added an empty
    `pg_largeobject_metadata` virtual view (OID 2995) in
    `internal/catalog/catalog.go` beside `pg_rewrite`. Schema matches
    `pg_largeobject_metadata.h`: oid, lomowner oid, lomacl aclitem[]. After this
    slice the large-object probe passes; the **new** blocker is `relation
    "pg_amproc" does not exist` (dependency collection `getDependencies`: a
    `pg_depend` UNION that joins `pg_amop` and `pg_amproc` to surface operator-
    family member dependencies) — an empty-view slice for `pg_amop` (OID 2602)
    and `pg_amproc` (OID 2603) is next.

30. **`pg_amop` + `pg_amproc` empty virtual views — DONE.** pg_dump's
    `getDependencies` issues a `pg_depend` UNION that joins both `pg_amop` and
    `pg_amproc` to resolve operator-family member dependencies (so they are not
    dumped as standalone objects). goopg has no user-defined operator
    classes/families feeding this dump path, so empty views (0 rows each) are
    correct. Added both beside `pg_largeobject_metadata` in
    `internal/catalog/catalog.go`. Schemas: `pg_amop` (OID 2602, `pg_amop.h`):
    oid, amopfamily oid, amoplefttype oid, amoprighttype oid, amopstrategy int2,
    amoppurpose "char", amopopr oid, amopmethod oid, amopsortfamily oid;
    `pg_amproc` (OID 2603, `pg_amproc.h`): oid, amprocfamily oid, amproclefttype
    oid, amprocrighttype oid, amprocnum int2, amproc regproc. After this slice
    the dependency-collection UNION passes; the **new** blocker is `relation
    "pg_seclabels" does not exist` (security-label collection `getSecLabels`).

31. **`pg_seclabels` empty virtual view — DONE.** pg_dump's `getSecLabels`
    issues `SELECT label, provider, classoid, objoid, objsubid FROM
    pg_catalog.pg_seclabels ORDER BY classoid, objoid, objsubid` to dump
    `SECURITY LABEL` statements. In stock PG `pg_seclabels` is a system view over
    `pg_seclabel` + `pg_shseclabel`; goopg supports no SECURITY LABEL, so an empty
    view (0 rows) is correct. `pg_seclabels` is a VIEW (no oid column), registered
    under an unused virtual OID (3597) beside `pg_amproc` in
    `internal/catalog/catalog.go`. Cols (full upstream view schema): objoid oid,
    classoid oid, objsubid int4, objtype text, objnamespace oid, objname text,
    provider text, label text. After this slice security-label collection passes;
    the **new** blocker is `relation "pg_sequence" does not exist` (sequence
    collection `getSequences`: `SELECT seqrelid, format_type(seqtypid, NULL),
    seqstart, seqincrement, seqmax, seqmin, seqcache, seqcycle, last_value,
    is_called FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid) ORDER BY
    seqrelid`). `pg_sequence` is a real catalog (one row per sequence relation)
    joined with the set-returning function `pg_get_sequence_data`. The next slice
    must decide: if goopg has no sequence support an empty `pg_sequence` view (0
    rows) suffices, BUT the `pg_get_sequence_data(seqrelid)` call must also resolve
    (a function, not a view) — verify CREATE SEQUENCE support before assuming 0
    rows. pg_sequence cols (`pg_sequence.h`): seqrelid oid, seqtypid oid, seqstart
    int8, seqincrement int8, seqmax int8, seqmin int8, seqcache int8, seqcycle bool.

32. **`pg_sequence` empty virtual view + `pg_get_sequence_data` SRF — DONE.**
    Two parts resolved `getSequences`'s `FROM pg_catalog.pg_sequence,
    pg_get_sequence_data(seqrelid)` comma join (an implicit LATERAL): (a) an empty
    `pg_sequence` virtual catalog (OID 2224, 0 rows) registered in
    `internal/catalog/catalog.go` beside `pg_seclabels`, cols matching
    `pg_sequence.h` (seqrelid oid, seqtypid oid, seqstart/seqincrement/seqmax/
    seqmin/seqcache int8, seqcycle bool); (b) `pg_get_sequence_data(regclass)`
    registered as a FROM-clause SRF returning (last_value int8, is_called bool) -
    `tableFuncColumns` in `internal/analyzer/analyzer.go` (so the analyzer's
    column resolution sees the shape; the analyzer runs *before* the planner and
    was the actual gate), `planPgGetSequenceData` + the `PgGetSequenceData` plan
    node in `internal/planner/`, and `pgGetSequenceDataOp` (0 rows) in
    `internal/executor/`. CREATE SEQUENCE *is* supported, but goopg's sequence
    virtual tables are skipped from the `pg_class` virtual view (Virtual with no
    `View`), so pg_dump's `getTables` never discovers a relkind='S' relation to
    dump - an empty `pg_sequence` is consistent with that, and the SRF is never
    invoked over the empty left side. **Full sequence-dump support (surfacing
    sequences as relkind='S' in pg_class, then populating seqrelid here from the
    sequence registry) is a larger follow-up slice - tracked, not done here.**
    After this slice sequence collection passes; the **new** blocker is
    `pg_dump: error: could not parse result of current_schemas()` - pg_dump's
    dumpable-object setup parses the `name[]` text-array literal returned by
    `current_schemas(true)`, and goopg does not render it in the `{a,b}`
    array-literal form `parsePGArray` expects (cf. the orthogonal text[]-from-heap
    array-encoding note). The next slice must make `current_schemas()` emit a
    parseable `name[]` array literal over the wire.
33. **`current_schemas(boolean)` → parseable `{a,b}` name[] literal — DONE.**
    pg_dump's `selectDumpableNamespace` setup runs `SELECT
    pg_catalog.current_schemas(false)` and feeds the result to `parsePGArray`,
    which requires the `{a,b}` text-array form; goopg previously aliased
    `current_schemas` to `current_schema` and returned a bare scalar, so pg_dump
    aborted "could not parse result of current_schemas()". Fix is executor-only
    (`internal/executor/expr.go`): a shared `searchPathSchemas(ctx)` resolver
    collects the existing search-path schemas in order (built-in
    pg_catalog/information_schema/public always exist; user schemas confirmed via
    the catalog); `current_schema` returns the scalar first entry, while the new
    `current_schemas(include_implicit)` renders them as a `{a,b}` literal,
    prepending the implicitly-searched `pg_catalog` when `include_implicit` is
    true (mirrors PG's `current_schemas` semantics). pg_proc already declared
    rettype 1003 (`name[]`), so no catalog change was needed. Unit guard:
    `executor.TestCurrentSchemasArrayLiteral`. After this slice pg_dump advances
    into `dumpFunc`; the **new** blocker is `column "proretset" does not exist`
    (`EXECUTE dumpFunc('1654')`) - the getFuncs/dumpFunc prepared query reads
    `pg_proc.proretset` (the returns-set flag), which goopg's pg_proc virtual view
    does not yet expose. The next slice must add `proretset` to the pg_proc view.

34. **`pg_proc.proretset` column — DONE.** dumpFunc projects `proretset` to
    decide whether to emit `RETURNS SETOF`; goopg's `pg_proc` virtual view
    (`internal/initdb/pg_proc_view.go`) did not expose it, so `EXECUTE
    dumpFunc('1654')` aborted with `column "proretset" does not exist`. Added the
    `proretset bool` column: built-in stubs (abs variants, RI_FKey_* triggers)
    are not SRFs so render `'f'`; user routines render `'t'`/`'f'` from the
    existing `catalog.Routine.ReturnsSet` (RETURNS SETOF, M0097-0020). Catalog-
    only change. After this slice pg_dump advances within `dumpFunc`; the **new**
    blocker is `column "probin" does not exist` (`EXECUTE dumpFunc('1654')`) -
    dumpFunc reads `pg_proc.probin` (the on-disk binary path for C-language
    functions, NULL for every internal/SQL routine goopg has). The next slice
    must add `probin` (always NULL) to the pg_proc view.

35. **`pg_proc.probin` column — DONE.** dumpFunc projects `probin` (the
    on-disk binary path for C-language functions) alongside `prosrc`; goopg's
    `pg_proc` virtual view did not expose it, so `EXECUTE dumpFunc('1654')`
    aborted with `column "probin" does not exist`. Added the `probin text`
    column: always NULL (`""`) for both built-in stubs and user routines —
    goopg has no C-language functions with an on-disk binary path. Catalog-only
    change (`internal/initdb/pg_proc_view.go`); guard
    `TestPgProcViewProbin`. After this slice pg_dump advances within `dumpFunc`;
    the **new** blocker is `column "proconfig" does not exist` (`EXECUTE
    dumpFunc('1654')`) - dumpFunc reads `pg_proc.proconfig` (the per-function
    GUC SET clauses, `text[]`; NULL for every goopg routine). The next slice
    must add `proconfig` (always NULL) to the pg_proc view.

36. **`pg_proc.proconfig` column — DONE.** dumpFunc projects `proconfig` (the
    per-function GUC SET clauses, e.g. `SET search_path = ...`); goopg's
    `pg_proc` virtual view did not expose it, so `EXECUTE dumpFunc('1654')`
    aborted with `column "proconfig" does not exist`. Added the
    `proconfig text[]` column: always NULL (`""`) for both built-in stubs and
    user routines — goopg tracks no per-function `SET` clauses, so dumpFunc
    emits no `SET ...` lines in a function's definition. Catalog-only change
    (`internal/initdb/pg_proc_view.go`); guard `TestPgProcViewProconfig`. After
    this slice pg_dump advances within `dumpFunc`; the **new** blocker is
    `column "procost" does not exist` (`EXECUTE dumpFunc('1654')`) - dumpFunc
    reads `pg_proc.procost` (the planner's estimated per-row execution cost,
    `float4`). The next slice must add `procost` to the pg_proc view.

37. **`pg_proc.procost` column — DONE.** dumpFunc projects `procost` (the
    planner's estimated per-row execution cost); goopg's `pg_proc` virtual view
    did not expose it, so `EXECUTE dumpFunc('1654')` aborted with
    `column "procost" does not exist`. Added the `procost float4` column,
    mirroring PG's `CREATE FUNCTION` default in `compute_function_attributes`:
    `1` for internal/C-language functions (`DEFAULT_FUNCTION_COST`), `100` for
    all other languages. Built-in stubs (internal language) emit `1`; user
    routines derive the cost from `catalog.Routine.Language`. Catalog-only
    change (`internal/initdb/pg_proc_view.go`); guard `TestPgProcViewProcost`.
    After this slice pg_dump advances within `dumpFunc`; the **new** blocker is
    `column "prorows" does not exist` (`EXECUTE dumpFunc('1654')`) - dumpFunc
    reads `pg_proc.prorows` (the estimated result-row count for set-returning
    functions, `float4`; PG default `0` for non-SRFs, `1000` for SRFs). The
    next slice must add `prorows` to the pg_proc view.
38. **`pg_proc.prorows` column — DONE.** dumpFunc projects `prorows` (the
    planner's estimated result-row count for set-returning functions); goopg's
    `pg_proc` virtual view did not expose it, so `EXECUTE dumpFunc('1654')`
    aborted with `column "prorows" does not exist`. Added the `prorows float4`
    column, mirroring PG's `CREATE FUNCTION` default: `1000` for set-returning
    functions, `0` for everything else. Built-in stubs (none are SRFs) emit
    `0`; user routines derive the value from `catalog.Routine.ReturnsSet`.
    Catalog-only change (`internal/initdb/pg_proc_view.go`); guard
    `TestPgProcViewProrows`. After this slice pg_dump advances within
    `dumpFunc`; the **new** blocker is `column "protrftypes" does not exist`
    (`EXECUTE dumpFunc('1654')`) - dumpFunc reads `pg_proc.protrftypes` (the
    OID array of argument types whose transforms the function uses,
    `oidvector`; NULL when the function uses no transforms — the case for
    every goopg routine). The next slice must add `protrftypes` to the pg_proc
    view.
39. **`pg_proc.protrftypes` column — DONE.** dumpFunc projects `protrftypes`
    (the OID array of argument types whose transforms the function uses); goopg's
    `pg_proc` virtual view did not expose it, so `EXECUTE dumpFunc('1654')`
    aborted with `column "protrftypes" does not exist`. Added the
    `protrftypes oidvector` column, always NULL — goopg supports no transforms,
    so `dumpFunc` emits no `TRANSFORM FOR TYPE ...` clause for any routine
    (built-in stubs and user routines alike). Catalog-only change
    (`internal/initdb/pg_proc_view.go`); guard `TestPgProcViewProtrftypes`.
    After this slice pg_dump advances within `dumpFunc`; the **new** blocker is
    `column "proparallel" does not exist` (`EXECUTE dumpFunc('1654')`) - dumpFunc
    reads `pg_proc.proparallel` (the parallel-safety marker, `char`: `'s'` safe /
    `'r'` restricted / `'u'` unsafe; PG's `CREATE FUNCTION` default is `'u'`
    unsafe — the case for every goopg routine). The next slice must add
    `proparallel` to the pg_proc view.
40. **`pg_proc.proparallel` column — DONE.** dumpFunc projects `proparallel`
    (the parallel-safety marker); goopg's `pg_proc` virtual view did not expose
    it, so `EXECUTE dumpFunc('1654')` aborted with `column "proparallel" does
    not exist`. Added the `proparallel char` column, always `'u'` (unsafe) —
    goopg tracks no parallel-safety, mirroring PG's `CREATE FUNCTION` default, so
    `dumpFunc` emits `PARALLEL UNSAFE` (the default) for every routine (built-in
    stubs and user routines alike). Catalog-only change
    (`internal/initdb/pg_proc_view.go`); guard `TestPgProcViewProparallel`.
    After this slice pg_dump advanced within `dumpFunc`; the next blocker was
    `column "prosupport" does not exist` (`EXECUTE dumpFunc('1654')`) - dumpFunc
    reads `pg_proc.prosupport` (the OID of the function's planner support
    function, `regproc`/`oid`; PG's `CREATE FUNCTION` default is `0` — no support
    function, the case for every goopg routine). The next slice must add
    `prosupport` to the pg_proc view.
41. **`pg_proc.prosupport` column — DONE.** dumpFunc projects `prosupport`
    (the OID of the function's planner support function, `regproc`/`oid`); goopg's
    `pg_proc` virtual view did not expose it, so `EXECUTE dumpFunc('1654')` aborted
    with `column "prosupport" does not exist`. Added the `prosupport oid` column,
    always `0` (no support function) — goopg has no planner support functions,
    mirroring PG's `CREATE FUNCTION` default, so `dumpFunc` emits no `SUPPORT …`
    clause for any routine (built-in stubs and user routines alike). Catalog-only
    change (`internal/initdb/pg_proc_view.go`); guard `TestPgProcViewProsupport`.
    After this slice all 22 columns `dumpFunc` projects from `pg_proc` resolve and
    the query plans/executes; the **new** blocker is a *different class* of error —
    `query returned 0 rows instead of one` (`EXECUTE dumpFunc('1654')`). dumpFunc's
    query is `… FROM pg_catalog.pg_proc p, pg_catalog.pg_language l WHERE p.oid = $1
    AND l.oid = p.prolang` (it joins to `pg_language` purely to fetch `lanname`).
    goopg's `pg_language` virtual view (`internal/catalog/catalog.go`) deliberately
    returns **0 rows** — that was correct for `getProcLangs`' `… WHERE lanispl`
    predicate (built-in langs have `lanispl=false` and are never dumped), but the
    `dumpFunc` join has no such filter, so the join over `prolang=12` (internal)
    yields nothing. The next slice must populate `pg_language`'s `VirtualRows` with
    the 3 built-in language rows (internal/12, c/13, sql/14 — matching
    `pgLanguageInitialEntries()`); this stays safe for `getProcLangs` because all 3
    have `lanispl=false` (its `WHERE lanispl` still excludes them).
42. **`pg_language` built-in rows + `pg_proc.prolang` oid type — DONE.** Two
    coupled fixes made the `dumpFunc` join resolve. (a) Populated `pg_language`'s
    `VirtualRows` (`internal/catalog/catalog.go`) with the 3 built-in language rows
    (internal/12, c/13, sql/14) matching `pgLanguageInitialEntries()`;
    `lanvalidator=0`, `lanacl=NULL`, and all `lanispl=f` (so `getProcLangs`' `WHERE
    lanispl` still returns 0 — no user PLs to dump). (b) Retyped the `pg_proc` view's
    `prolang` column from `text` to `oid` (`internal/initdb/pg_proc_view.go`),
    matching PG's `pg_proc.prolang` and the physical pg_proc catalog
    (`initdb.go`/`relcache_init.go`, already oid). The join `l.oid = p.prolang` was
    comparing `oid = text`, which silently matched nothing → `0 rows instead of
    one`; with both oid it resolves. Built-in stubs already emitted OID-string langs;
    user routines now map name→OID via the new `langNameToOIDStr` helper (plpgsql,
    not installed in goopg's `pg_language`, → `0`/InvalidOid — a plpgsql function's
    dump-join is a separate future gap). Guards: `catalog.TestPgLanguageBuiltinRows`,
    `initdb.TestPgProcViewProlangOID` (+ updated `TestPgProcViewRendersRoutine`).
    After this slice the join resolves and `dumpFunc` advances; the **new** blocker
    is `function pg_catalog.pg_get_function_identity_arguments does not exist`
    (`EXECUTE dumpFunc('1654')`) — dumpFunc's SELECT projects
    `pg_get_function_arguments`/`_identity_arguments`/`_result(p.oid)`, none of which
    goopg implements yet. The next slice must add these catalog functions (starting
    with `pg_get_function_identity_arguments`).
43. **`pg_get_function_identity_arguments(oid)` builtin — DONE.** The seed
    `pg_proc` already registered this function (OID 2232, `pg_proc_seed_data.go`),
    and its siblings `pg_get_function_arguments`/`pg_get_function_result` already
    had executor cases — only `pg_get_function_identity_arguments` lacked a
    dispatch case in `internal/executor/expr.go`, so the call raised 42883
    "function ... does not exist". Added the case mirroring `pg_get_function_arguments`:
    look up the routine by OID and return `buildFunctionArguments(r)`. Upstream
    (`ruleutils.c` `print_function_arguments`) differs from `pg_get_function_arguments`
    only by `print_defaults=false` (identity omits DEFAULT clauses); goopg's
    `buildFunctionArguments` never emits defaults, so the identity form is
    byte-identical to the full argument list. Guard:
    `executor.TestPgGetFunctionIdentityArguments` (+ `…OutMode` for mode prefixes).
    After this slice `dumpFunc` advances past the call; the **new** blocker is
    `function pg_get_function_sqlbody does not exist` (`EXECUTE dumpFunc('1654')`) —
    dumpFunc also projects `pg_get_function_sqlbody(p.oid)` (PG14+; the deparsed
    SQL-standard body of `LANGUAGE sql … BEGIN ATOMIC` functions). goopg has no
    such builtin yet; the next slice must add it (NULL for non-SQL/non-atomic
    routines mirrors PG).
44. **`pg_get_function_sqlbody(oid)` builtin — DONE; pg_dump now reaches exit 0.**
    The seed `pg_proc` already registered this function (OID 6197,
    `pg_proc_seed_data.go`), but the executor lacked a dispatch case in
    `internal/executor/expr.go`, so `dumpFunc`'s `EXECUTE dumpFunc('1654')`
    raised 42883. Added a case returning `NullDatum` unconditionally:
    `pg_get_function_sqlbody` deparses a standard body only for
    `LANGUAGE sql … BEGIN ATOMIC` functions (PG14+); goopg never parses a
    `prosqlbody`, so NULL for every routine is correct and matches what pg_dump
    expects for quoted-body SQL functions. Guards:
    `executor.TestPgGetFunctionSqlbody` (+ `…UnknownOID`). With this, **pg_dump
    runs to completion (exit 0)** — connection setup and the entire catalog-dump
    pipeline (roles, namespaces, tables, funcs, langs, operators, opclasses,
    TS objects, FDWs, …) now resolve end-to-end. `TestPort_PgDumpConnectionSetup`
    is promoted from a "no setup regression" guard to asserting the table's
    archive entry (`CREATE TABLE public.foo (`, `ALTER TABLE … OWNER TO`,
    `COPY public.foo`). The **new** blocker (slice 45) is *content* parity, not a
    hard error: the emitted `CREATE TABLE public.foo (\n)` has an **empty column
    list** (no `id integer, name text`) and a malformed `WITH (""='')` reloptions
    clause — `getTableAttrs`' per-table `pg_attribute` query returns no rows for
    user tables, and the reloptions `ARRAY` subquery yields one empty element.
    The next slice must populate `pg_attribute` columns in the dump path and
    suppress the empty reloption.
  - **Slice 45 — typed unnest elements join catalog columns
    (`internal/executor/operators_from_unnest.go`).** `getTableAttrs` reads
    columns via `FROM unnest('{16403}'::oid[]) AS src(tbloid) JOIN pg_attribute
    a ON src.tbloid = a.attrelid`. The join matched **nothing** because
    `expandArrayDatum` returns every array element as a text `KindString`
    (`s:16403`), whose `datumKey` differs from the `KindInt` key a catalog `oid`
    column (`pg_attribute.attrelid`, written via `NewIntDatum`) derives — so the
    hash join bucketed the two sides apart. `coerceUnnestElem` now casts each
    element to its declared output-schema type (oid/xid/int/float/numeric/bool
    family only; text stays a string; a cast failure falls back to the raw
    element) before the row is materialised, in **both** the single-arg and
    multi-arg unnest paths. With this the join key lines up and `pg_attribute`
    rows flow. Guards: `executor.TestCoerceUnnestElem_*` (join-key parity,
    text-unchanged, NULL pass-through, bad-value fallback).
  - **Slice 46 — unnest-join right-side projection offset
    (`internal/planner/bushy.go`).** With the join key fixed, pg_dump failed with
    `invalid column numbering in table "foo"`: the join *condition* resolved
    `a.attrelid` correctly (combined index 1) but the *projection* of right-side
    columns was **not shifted by the 1-column unnest (left) prefix** — `a.attname`
    resolved to combined index 1 (returned `attrelid`=16403) and `a.attnum` to
    combined index 4 (returned `attlen`=4 for `integer`). Root cause:
    `buildBindingsPosMap`'s `collect` walker advanced its running offset `off`
    for `*Values` (and scans/Project) but **not** for leaf FROM-clause
    set-returning / table-function nodes (`FromUnnest`, `GenerateSeries`,
    `GenerateSubscripts`, `UserSrfScan`, `ScalarFuncScan`, and the `Pg*` table
    functions). With `off` left too low, `remapTopProjection` shifted the
    right-side scan's projection columns DOWN by the SRF's output width. Fix:
    those node types now advance `off` by `len(x.Output())`, mirroring the
    `*Values` case; their own columns need no remap (the posMap returns the old
    index unchanged for bindings absent from `scanMap`). This is a sibling-path
    fix — join-condition resolution already offset correctly via the binding
    table; projection resolution went through `buildBindingsPosMap` and did not.
    pg_dump now reaches exit 0 **and** emits the real column list
    (`id integer, name text`). Guards:
    `planner.TestSRFJoinRightProjectionOffset` (VALUES-left reference vs
    unnest-left / generate_series-left projection-index parity) and the promoted
    `TestPort_PgDumpConnectionSetup` column-list assertion.
  - **Slice 47 — array-typed virtual-catalog cell NULL handling
    (`internal/planner/planner.go`).** With exit 0 and the column list correct,
    the dumped `CREATE TABLE` still carried a spurious `WITH (""='')` reloptions
    clause. Root cause: the virtual `pg_class` view
    (`internal/catalog/catalog.go`) stores `relacl`/`reloptions` as `""`
    (commented "NULL"), but `planner.TypedVirtualCell` — which converts a virtual
    row's text cells to typed constants — had **no case for array column types**.
    The empty cell fell through to the default `StringConst("")`, and goopg's
    array machinery parses a bare `""` in a `text[]` context as a single
    empty-string element (`{""}`), so `array_length(reloptions,1)` returned 1.
    pg_dump's getTables reads
    `array_remove(array_remove(c.reloptions,'check_option=local'),'check_option=cascaded')`
    and its `nonemptyReloptions` test (`strlen(reloptions) > 2`) saw `{""}` as
    non-empty, emitting `WITH (` + `fmtId("")` + `='')`. Fix: `TypedVirtualCell`
    now maps an **empty** array-typed cell (`text[]`/`_text`, `aclitem[]`,
    `oid[]`, `int2[]`/`int4[]`, `char[]`, `name[]`, `float4[]`, `anyarray`) to
    `NullConst` — PostgreSQL's convention for an absent reloptions / default ACL
    — while a non-empty cell passes through as the array text literal. The dumped
    `CREATE TABLE` is now byte-identical to upstream for a plain table (no `WITH`
    clause). Guards: `planner.TestTypedVirtualCell` (empty `text[]`/`aclitem[]`/
    `oid[]`/`_text` → NULL; non-empty `{a,b}` passes through) and the
    `TestPort_PgDumpConnectionSetup` no-`WITH (` assertion. This is the natural
    PG-correct fix at the single type-conversion choke point, so it also repairs
    every other array-typed virtual-catalog column (`proconfig`, `proacl`,
    `nspacl`, `datacl`, …) that previously decoded an absent value as `{""}`.

  - **Slice 48 — column type-modifier fidelity (`atttypmod`)
    (`internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    With the plain-table dump byte-identical to upstream, enriching the fixture
    with `amount numeric(10,2)` and `code character varying(8)` exposed the next
    gap: both columns dumped as their **bare base type** (`numeric`, `character
    varying`). Root cause: `buildUserPGAttributeRow` hardcoded `atttypmod = -1`,
    discarding the declared type arguments that the parser already captures in
    `catalog.Type.Args`. pg_dump's getTableAttrs renders each column via
    `format_type(a.atttypid, a.atttypmod)`, so with `atttypmod = -1` it always
    got the unmodified type. Fix: new `pgAttTypmod(typOID, args)` computes the
    **PG-canonical** `atttypmod`, mirroring the backend typmodin functions —
    numeric: `((precision<<16) | scale) + VARHDRSZ`; `character`/`character
    varying`: `length + VARHDRSZ`; everything else `-1` (no modifier). It is
    wired into the `atttypmod` cell of the user pg_attribute row. The matching
    decode side: `formatTypeOID` (goopg's `format_type`) already decoded the
    `character`/`character varying` length but returned a bare `numeric`, so it
    gained the numeric branch (`typmod >= VARHDRSZ` → `numeric(p,s)` via
    `((typmod-VARHDRSZ)>>16)&0xffff`, `(typmod-VARHDRSZ)&0xffff`), matching
    `numerictypmodout`. The CREATE TABLE now reproduces the declared precision
    and length faithfully. Guards: `executor.TestUserPGAttributeTypmod` (pins the
    atttypmod encoding **and** the format_type round-trip for numeric(10,2),
    numeric(8,0), varchar(64), char(10), and the no-modifier cases) and the
    enriched `TestPort_PgDumpConnectionSetup` fixture asserting `amount
    numeric(10,2)` + `code character varying(8)` survive the dump.
  - **Slice 49 — table CHECK constraints (`relchecks` + `pg_get_constraintdef`)
    (`internal/catalog/catalog.go`, `internal/executor/expr.go`).** Enriching the
    fixture with `id integer PRIMARY KEY` and `qty integer DEFAULT 0 CHECK
    (qty >= 0)` showed the PRIMARY KEY already round-trips (the `ALTER TABLE …
    ADD CONSTRAINT … PRIMARY KEY` path was complete) but the **column-level
    CHECK was silently dropped**. Two independent causes, both required:
    (1) pg_dump only queries per-table CHECK constraints when
    `pg_class.relchecks > 0` and then *asserts* the returned row count equals
    `relchecks` (`getTableAttrs`); goopg hardcoded the user-table `relchecks`
    cell to `0`, so the query never ran. The cell now counts the table's visible
    `NamedChecks` (those with a non-empty name **and** non-zero OID — exactly the
    set `pg_constraint`'s `VirtualRows` emits as `contype='c'`, so the count
    assertion holds). (2) `pg_get_constraintdef(c.oid)` — used to render each
    CHECK row — handled only index-backed UNIQUE/PRIMARY KEY/EXCLUDE constraints
    and returned NULL for a `contype='c'` OID. It gained a CHECK branch that
    scans the owning table's `NamedChecks` and renders `CHECK ((expr))`
    (appending ` NO INHERIT` when set), mirroring PG's deparser's extra paren
    layer. The fixture's auto-named column CHECK now dumps as
    `CONSTRAINT foo_qty_check CHECK ((qty >= 0))`. Guard: the enriched
    `TestPort_PgDumpConnectionSetup` fixture asserts both the PRIMARY KEY
    ADD CONSTRAINT and the CHECK constraint survive the dump.

  - **Slice 50 — implicit NOT NULL on a PRIMARY KEY column (named contype='n'
    constraint) (`internal/executor/operators_ddl.go`).** goopg dumped the PK
    column as bare `id integer`; upstream emits `id integer NOT NULL`. PG18's
    pg_dump no longer derives the inline NOT NULL clause from `attnotnull` —
    `getTableAttrs` LEFT-JOINs `pg_constraint co ON (a.attrelid = co.conrelid
    AND co.contype = 'n' AND co.conkey = array[a.attnum])` and prints NOT NULL
    only when `co.conname` is non-NULL (`determineNotNullFlags`/`dumpTableSchema`,
    `src/bin/pg_dump/pg_dump.c`). goopg already emits contype='n'
    `<table>_<col>_not_null` rows from `Table.NotNullConstraints`, and the PK
    path already set the column's `attnotnull=true`, **but** the CREATE TABLE
    NOT-NULL registration loop deliberately *excluded* PK columns (a `pkColSet`
    skip), so no contype='n' row existed for them and the join found nothing.
    Verified against real PG18: `id integer PRIMARY KEY` materialises a
    `foo_id_not_null` (contype='n', conkey={1}) constraint alongside `foo_pkey`.
    Fix: (1) drop the `pkColSet` exclusion so every not-null column — PK columns
    included — registers its named NOT NULL constraint on CREATE TABLE; (2) add
    the identical registration to the `ALTER TABLE … ADD PRIMARY KEY` sibling
    path (`execAlterTableAddPrimaryKey`), which also flips `attnotnull`, guarding
    against a duplicate when the column already carried a NOT NULL constraint.
    pg_dump now prints `id integer NOT NULL`; the auto-default constraint name is
    suppressed by pg_dump's `ChooseConstraintName` match. Guard: the fixture
    asserts `id integer NOT NULL` in the dumped column list.

  - **Slice 51 — FOREIGN KEY constraints (`contype='f'` + `pg_get_constraintdef`)
    (`internal/catalog/catalog.go`, `internal/executor/expr.go`,
    `internal/executor/operators_ddl.go`).** A column-level `REFERENCES` FK was
    silently dropped from the dump while a sibling UNIQUE constraint dumped fine
    (UNIQUE/PK/EXCLUDE go through the index-backed constraint path). pg_dump's
    `getConstraints` joins `pg_constraint c ON src.tbloid = c.conrelid WHERE
    contype = 'f' AND conparentid = 0` and renders each via
    `pg_get_constraintdef(c.oid)` (`src/bin/pg_dump/pg_dump.c:8172`). goopg's
    `catalog.ForeignKey` carried neither a name nor an OID, so `pg_constraint`
    emitted no contype='f' row and the join returned nothing; even had it, the
    deparser handled only index-backed constraints. Fix: (1) `catalog.ForeignKey`
    gained `Name`+`OID`, auto-assigned at DDL time using PG's
    `<table>_<col>_fkey` convention — both the CREATE TABLE inline-`REFERENCES`
    path and the `ALTER TABLE … ADD FOREIGN KEY` path (the latter honours an
    explicit `CONSTRAINT name`); (2) `pg_constraint.VirtualRows` emits the
    contype='f' row — `conkey`/`confkey` from the referencing/referenced column
    ordinals, `confrelid` = the referenced table OID, `confupdtype`/`confdeltype`
    from the FK action (`fkActionChar`), `confmatchtype='s'` (MATCH SIMPLE),
    `conparentid=0` so pg_dump's filter keeps it; (3) `pg_get_constraintdef`
    gained an FK branch (`buildForeignKeyDefString`) mirroring ruleutils.c
    `pg_get_constraintdef_worker` — `FOREIGN KEY (cols) REFERENCES
    public.reltbl(refcols)` (fully schema-qualified because pg_dump runs with
    `search_path=''`), appending `ON UPDATE`/`ON DELETE` and `DEFERRABLE` clauses
    only when non-default and omitting MATCH SIMPLE, exactly as PG does. The
    fixture's self-FK `parent_id integer REFERENCES public.foo(id)` now dumps as
    `ADD CONSTRAINT foo_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES
    public.foo(id)`. Guards: the integration fixture plus the unit test
    `executor.TestForeignKeySurfacesInPgConstraint`.
  - **Slice 52 — FK referential actions (`ON DELETE`/`ON UPDATE`) via
    `ALTER TABLE` (`internal/parser/ast.go`, `internal/parser/ddl.go`,
    `internal/executor/operators_ddl.go`).** Slice 51 wired actions through the
    inline column-FK path, but the `ALTER TABLE … ADD FOREIGN KEY` path dropped
    them at three layers: the parser never consumed the `ON DELETE`/`ON UPDATE`
    clause (so the syntax in fact errored before the comma/end-of-statement), the
    `AlterTableAction` AST had no `OnDelete`/`OnUpdate` field, and the executor's
    `AlterTableAddForeignKey` branch never set `catalog.ForeignKey.OnDelete/
    OnUpdate`. Fix: (1) the ALTER parser now parses the action clauses ahead of
    the `[NOT] DEFERRABLE` trailer, reusing `parseFKAction` exactly as the inline
    column path does; (2) `AlterTableAction` gained `OnDelete`/`OnUpdate` fields;
    (3) the executor copies them into the catalog FK. With the catalog carrying
    the action, `pg_constraint.confupdtype`/`confdeltype` (via `fkActionChar`) and
    `pg_get_constraintdef` (via `fkActionClause`, already added in slice 51) emit
    the clauses. The fixture's inline self-FK now carries `ON DELETE CASCADE`, and
    an ALTER-added `foo_mgr_fkey` carries `ON UPDATE CASCADE ON DELETE SET NULL`;
    both round-trip byte-identically (pg_get_constraintdef emits `ON UPDATE`
    before `ON DELETE`, mirroring ruleutils.c, and omits the default NO ACTION).
    Guards: the integration fixture plus the unit test
    `executor.TestAlterTableAddForeignKeyCapturesActions`.
  - **Slice 53 — table-level (composite) FOREIGN KEY in `CREATE TABLE`
    (`internal/parser/ast.go`, `internal/parser/ddl.go`,
    `internal/executor/operators_ddl.go`).** The inline column-FK path (slices
    51/52) handled only single-column `col … REFERENCES t (x)`. A table-level
    `FOREIGN KEY (a, b) REFERENCES t (x, y)` — the only way to express a composite
    FK — was a parser **no-op**: the `CONSTRAINT name FOREIGN KEY …` case simply
    skipped tokens to the next comma/paren, and there was no anonymous
    `FOREIGN KEY` branch at all, so a multi-column FK never reached the catalog,
    `pg_constraint`, or pg_dump. Fix: (1) a new `TableForeignKeyDef` AST node and
    `CreateTableStmt.TableForeignKeys` slice; (2) a shared parser helper
    `parseTableForeignKey(name)` that parses `FOREIGN KEY (cols) REFERENCES t
    [(refcols)] [ON DELETE/UPDATE action] [[NOT] DEFERRABLE [INITIALLY …]]`,
    wired into **both** the CONSTRAINT-named and a new anonymous table-level
    branch, with its action/deferrable grammar kept in lockstep with the inline
    column path; (3) the executor registers each as a `catalog.ForeignKey`,
    auto-naming an anonymous FK `<table>_<firstcol>_fkey` (PG's
    `ChooseConstraintName` convention). No deparse/pg_constraint change was
    needed — `buildForeignKeyDefString` already `strings.Join`s multiple columns
    and the `conkey`/`confkey` ordinal loops already iterate `fk.Columns`/
    `fk.RefColumns`. The fixture's new `bar (a, b)` composite PK and `baz`'s
    `FOREIGN KEY (x, y) REFERENCES bar (a, b) ON DELETE CASCADE` round-trip
    byte-identically. Guards: the integration fixture plus the unit test
    `executor.TestCreateTableTableLevelCompositeForeignKey` (covers the anonymous
    and named forms, multi-column `conkey`/`confkey`, and the composite deparse).

  - **Slice 54 — non-empty reloptions (`WITH (fillfactor=N)`) + a user schema
    guard (`internal/catalog/catalog.go`,
    `internal/executor/operators_ddl.go`).** Slice 47 made an *empty* reloptions
    read as SQL NULL (no `WITH` clause). The complementary case — an
    actually-set storage parameter — was silently dropped: goopg parsed and
    bounds-validated `WITH (fillfactor=N)` into `CreateTableStmt.With` but never
    persisted it on the catalog table, so the `pg_class` virtual view always
    emitted `""` (NULL) for `reloptions` and pg_dump produced a bare `CREATE
    TABLE`. Fix: (1) a new `catalog.Table.Fillfactor` field; (2) the executor's
    CREATE TABLE path extracts `s.With["fillfactor"]`, bounds-checks it (PG's
    `22023` for values outside 10–100, mirroring the existing CREATE INDEX
    check), and assigns it to the new field; (3) the `pg_class` virtual view's
    `reloptions` cell (index 32) emits the text[] literal `{fillfactor=N}` when
    set — `""` (→ NULL via `TypedVirtualCell`, slice 47) otherwise. pg_dump's
    `getTables` reads the array through `array_remove(…)`, `nonemptyReloptions`
    sees one element, and the dump renders `WITH (fillfactor='70')`. The fixture
    carries fillfactor on a dedicated `public.opt` table so the slice-47 "foo has
    no options" guard is unaffected (that guard is tightened to the exact
    empty-element bug signature `WITH ("` so a legitimate fillfactor `WITH`
    clause does not trip it). The same slice adds a cross-namespace regression
    guard: a user-defined `CREATE SCHEMA s` plus `s.widget` round-trip
    byte-identically (already worked; now guarded). Unit guard:
    `executor.TestFillfactorSurfacesInPgClassReloptions` (the persisted field +
    `{fillfactor=70}` cell vs `""` for a plain table) and
    `executor.TestFillfactorOutOfBoundsRejected` (the 22023 bounds check).

  - **Slice 55 — COMMENT ON COLUMN round-trip (`internal/parser/parser.go`).**
    goopg already parsed `COMMENT ON {TABLE,COLUMN,…}` and populated
    `pg_description` via `catalog.SetComment`, and pg_dump's `collectComments`
    query (`SELECT description, classoid, objoid, objsubid FROM
    pg_catalog.pg_description ORDER BY …`) re-emits a `COMMENT ON …` statement per
    row (table comment: `objsubid=0`; column comment: `objsubid=attnum`). The
    TABLE comment already round-tripped, but the COLUMN comment was silently
    dropped: pg_dump emits the canonical **3-part** form `COMMENT ON COLUMN
    schema.table.col`, while goopg's parser handled only the bare **2-part**
    `table.col`. `parseObjectName` consumes at most two dotted parts, and
    `parseCommentOnTail`'s column case mapped that to `{table=Schema, col=Name}`
    without reading a trailing `.col`; the 3-part input therefore raised
    `expected IS after object name`, and that parse error was silently swallowed
    by the server's `COMMENT` no-op fallback, so the column comment never reached
    `pg_description`. Fix: the column case now checks for a trailing `.col` after
    `parseObjectName` — when present, the parsed name is the (optionally
    schema-qualified) table and the trailing identifier is the column
    (`schema.table.col`); otherwise the 2-part `table.col` mapping stands. The
    executor (`execCommentOn`) already resolved a schema-qualified `ObjName` via
    `LookupTable`, so no executor change was needed. Unit guard:
    `parser.TestParseCommentOnColumn` (2-part and 3-part forms parse to the
    correct table/column).

  - **Slice 56 — secondary-index ASC/DESC + NULLS FIRST/LAST ordering
    (`internal/parser/ddl.go`, `internal/parser/ast.go`,
    `internal/catalog/catalog.go`, `internal/executor/operators_ddl.go`).** A
    plain (non-constraint) `CREATE INDEX` round-trips through pg_dump's
    `getIndexes` -> `pg_get_indexdef(indexrelid)` path (distinct from the
    index-backed constraint path that UNIQUE/PK use). The plain and partial-index
    forms already worked, but goopg's `parseIndexColumnList` **parsed and then
    discarded** each key column's `ASC`/`DESC` and `NULLS FIRST`/`LAST`
    modifiers, so a `CREATE INDEX … (col DESC)` round-tripped as an ascending
    index — a silent semantic change (a descending index reads back ascending).
    Fix threads the ordering end-to-end, mirroring PG's `pg_index.indoption`:
    (1) the parser captures per-column `IndexColOrder{Descending, NullsFirst}`
    into `CreateIndexStmt.ColOrders` (NullsFirst pre-resolved to the descending
    flag — NULLS FIRST is the btree default for DESC, NULLS LAST for ASC — unless
    an explicit NULLS clause overrides); (2) `catalog.Index` gains parallel
    `ColDescending`/`ColNullsFirst` slices, populated by `execCreateIndex` only
    when at least one column is non-default (a plain index keeps empty slices and
    dumps byte-identically); (3) `BuildIndexDef` renders the ordering with PG's
    default-suppression logic from `ruleutils.c pg_get_indexdef_worker` (DESC
    prints `NULLS LAST` only when overridden; ASC prints `NULLS FIRST` only when
    set). A pre-existing latent parser bug was also fixed: `NULLS` lexes as a bare
    `TokenIdent`, so the greedy opclass-name detection mis-read `(col NULLS
    FIRST)` as `col` with opclass `nulls`; the opclass branch now skips a
    case-insensitive `nulls`. Durability note: goopg does not persist the
    indoption bits to the on-disk `pg_index` heap, so an index's ordering would be
    lost across a server restart; `pg_get_indexdef` reads the in-memory
    `AllIndexes()` catalog, so the dump is faithful within a session (the test
    path). On-disk indoption persistence is a separate follow-up. Unit guards:
    `parser.TestParseCreateIndexColOrders` (all six ASC/DESC/NULLS captures incl.
    the NULLS-as-opclass guard), `catalog.TestBuildIndexDefColOrder` (all four
    render branches).

  - **Slice 57 — VIEW round-trip via `pg_get_viewdef` (`internal/parser/parser.go`,
    `internal/parser/ddl.go`, `internal/parser/ast.go`,
    `internal/catalog/catalog.go`, `internal/executor/operators_ddl.go`,
    `internal/executor/expr.go`).** pg_dump fetches every view's defining query
    via `pg_get_viewdef(oid)` in `createViewAsClause` and **aborts the entire
    dump** with `definition of view "v" appears to be empty (length zero)` when
    that returns NULL or "" — so a single user view made the whole dump fail (and
    the table DATA emitted after it never appeared). goopg stubbed
    `pg_get_viewdef` to NULL (it predated any deparser). Fix captures the raw view
    body verbatim rather than building a full SQL deparser: (1) the parser now
    keeps the original source string (`parser.src`) and a `captureSrcSpan` helper
    slices the text between the `AS` body's first token and the next unconsumed
    token, trimmed of whitespace and any trailing `;`; (2) `parseCreateViewTail`
    stores it on `CreateViewStmt.RawDef` (the trailing `WITH CHECK OPTION` clause
    is excluded — it is consumed after the span ends); (3) `execCreateView` copies
    `RawDef` onto `catalog.Table.ViewDef` (works for `CREATE OR REPLACE` too —
    `CreateView` returns the fresh table either way); (4) `pg_get_viewdef` looks
    the view up by OID (pg_dump) or name (psql) and returns `ViewDef + ";"`.
    pg_dump's `createViewAsClause` Asserts the last char is `;`, strips it, and
    wraps the rest in `CREATE VIEW … AS <body>`, so the terminating `;` is
    required. **Fidelity gap (documented):** PG's deparser fully schema-qualifies
    unqualified relation references; goopg echoes the literal text, so a view that
    referenced an unqualified table would fail to restore under pg_dump's
    `search_path=''`. Qualified views (like the fixture's `public.foo`) round-trip
    cleanly. RECURSIVE views and materialized views do not capture `RawDef` yet
    (their bodies take different parser paths) — a follow-up. Unit guard:
    `parser.TestParseCreateViewRawDef` (body capture incl. trailing-`;` trim and
    `WITH CHECK OPTION` exclusion).

  - **Slice 58 — VIEW with an explicit column list (`internal/executor/expr.go`).**
    `CREATE VIEW v (c1, c2, …) AS …` renames the view's output columns. PG's
    `pg_get_viewdef` bakes those names into the select list as `expr AS cN`, so
    the dumped/restored view exposes the declared names. goopg captures the body
    verbatim (slice 57) and the explicit names were already stored on
    `catalog.Table.ViewColumnAliases`, but `pg_get_viewdef` echoed the raw body
    unchanged — the restored view carried the *underlying* column names (e.g.
    `id, name`) instead of the declared `(col_a, col_b)`, a silent fidelity loss.
    Fix: `applyViewColumnAliases` splices the names into the raw select-list text
    when the view has a column list. It is a deliberately small, raw-text rewrite
    (no deparser): it finds the top-level `FROM` boundary (`findTopLevelFromKeyword`,
    respecting paren/bracket depth and string/identifier literals so `EXTRACT(…
    FROM …)` and subqueries are not mistaken for the clause), splits the select
    list on top-level commas (the existing `splitTopLevelCommas`), and appends
    ` AS <name>` (quoted via `quoteViewIdent` only when not a simple lowercase
    identifier) to each item. **Bails to the raw text** (renamed names lost) when
    it cannot rewrite unambiguously: top-level item count ≠ alias count, any item
    is `*`/`x.*`, or any item already carries a top-level `AS` alias — documented
    fidelity gaps for those uncommon shapes. Unit guard:
    `executor.TestApplyViewColumnAliases` (13 cases incl. internal-comma/internal-FROM
    protection, quoting, and every bail path).
  - **Slice 59 — GENERATED ALWAYS AS (expr) STORED column round-trip
    (`internal/executor/pg18_user_catalog_rows.go`, `internal/catalog/catalog.go`).**
    pg_dump emits the inline `GENERATED ALWAYS AS (%s) STORED` clause only when a
    column carries BOTH `pg_attribute.attgenerated='s'` AND a `pg_attrdef` row
    whose `pg_get_expr(adbin)` yields the generation expression — `dumpTableSchema`'s
    `print_default` gate requires `tbinfo->attrdefs[j] != NULL`, and
    `getTableAttrs` forces `separate=false` for any generated column so it is
    never deferred to an ALTER. goopg already set `attgenerated='s'`
    (`attGeneratedFor`) but `atthasdef` was `col.DefaultExpr != nil` (false for a
    generated column) and the `pg_attrdef` virtual view only iterated columns with
    a `DefaultExpr`, so no attrdef row existed — pg_dump silently dropped the
    GENERATED clause and the column dumped as a plain `area integer`, a
    stored-vs-computed semantic loss on restore. Fix: `atthasdef` now reports true
    when `GeneratedExpr != ""` as well, and `pg_attrdef.VirtualRows` emits a row
    for generated columns with `adbin = col.GeneratedExpr` (a column is never both
    DEFAULT and GENERATED, so `DefaultExpr` takes precedence). `pg_get_expr` passes
    the stored string through verbatim, so the dumped clause carries single parens
    (`(w * h)`); PG's deparser may add normalizing parens — both restore to an
    equivalent stored generated column. Guarded end-to-end by
    `TestPort_PgDumpConnectionSetup` (the `gen` fixture table).
  - **Slice 60 — MATERIALIZED VIEW round-trip (`internal/parser/ast.go`,
    `internal/parser/ddl.go`, `internal/executor/operators_ddl.go`).** pg_dump
    dumps a materialized view's `AS` clause through the SAME
    `createViewAsClause` → `pg_get_viewdef` path it uses for a plain view
    (`pg_dump.c` `dumpTableSchema`, `RELKIND_MATVIEW` branch emits `CREATE
    MATERIALIZED VIEW … AS\n<body>\n  WITH NO DATA;`), and `createViewAsClause`
    aborts the ENTIRE dump with `definition of view "v" appears to be empty
    (length zero)` when `pg_get_viewdef` returns NULL/"". goopg's matview is
    already surfaced in `pg_class` as `relkind='m'`, but `execCreateMatView`
    captured the body only as the SELECT AST (`tbl.View`, used for REFRESH) and
    never as raw text — `tbl.ViewDef` stayed `""`, so `pg_get_viewdef` returned
    NULL and any matview made the whole dump fail (mirroring the slice-57 plain-VIEW
    bug). Fix: `CreateMatViewStmt` gains a `RawDef` field, `parseCreateMatViewTail`
    captures the verbatim body via `captureSrcSpan` (from the SELECT/WITH keyword
    up to the optional `WITH [NO] DATA` clause, mirroring `parseCreateViewTail`),
    and `execCreateMatView` stores it on `catalog.Table.ViewDef` exactly as the
    plain-view path does (`vt.ViewDef = s.RawDef`). `pg_get_viewdef` already keys on
    `View != nil && ViewDef != ""`, so it now echoes the matview body unchanged.
    Guarded end-to-end by `TestPort_PgDumpConnectionSetup` (the `foo_mv` fixture)
    and by the parser unit test `parser.TestParseCreateMatViewRawDef`.
  - **Slice 61 — RECURSIVE VIEW round-trip (`internal/parser/ddl.go`).** A
    `CREATE RECURSIVE VIEW` is dumped through the SAME `pg_get_viewdef` path as a
    plain view, so a recursive view with an empty body aborts the WHOLE dump with
    `definition of view "v" appears to be empty (length zero)` — the slice-57
    blocker, repeated for the recursive parse path. `parseCreateRecursiveViewTail`
    already built the wrapped-CTE AST (`WITH RECURSIVE name(cols) AS (body) SELECT
    * FROM name`, used for execution) but set NO `RawDef`, so `pg_get_viewdef`
    returned NULL. PostgreSQL stores a recursive view as a regular view over a
    `WITH RECURSIVE` CTE and `pg_dump` re-emits it as a plain `CREATE VIEW`; goopg
    now mirrors that by SYNTHESIZING the wrapped form into `RawDef`: `WITH
    RECURSIVE name(cols) AS (<verbatim body>) SELECT cols FROM name`. The body is
    captured verbatim via `captureSrcSpan`, the CTE header and outer projection
    list the declared columns explicitly (PG expands the canonical column list;
    goopg has no deparser, so it spells them out — the same documented fidelity
    gap as the verbatim plain-view body), and the leading `WITH` keyword means
    `applyViewColumnAliases` bails (it only rewrites bodies starting with
    `SELECT`), so the column names come solely from the synthesized projection.
    The CTE self-reference inside the body must be UNQUALIFIED (it binds to the
    CTE name, not a schema-qualified relation), mirroring PG's CREATE RECURSIVE
    VIEW rewrite. Guarded end-to-end by `TestPort_PgDumpConnectionSetup` (the
    `foo_rec` fixture) and by `parser.TestParseCreateRecursiveViewRawDef`.
  - **Slice 62 — array-typed column round-trip (`internal/catalog/catalog.go`,
    `internal/catalog/codec.go`, `internal/executor/operators_ddl.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/initdb/open.go`).**
    `pg_dump` renders every column via `format_type(atttypid, atttypmod)`, which
    only yields the `[]` suffix when `pg_attribute.atttypid` holds the array
    (`_typename`) OID. The parser captured the SQL `[]` suffix
    (`ColumnType.IsArray`, M0097-0071) but the CREATE TABLE path (`addCol`)
    dropped it on the way into the catalog, so `buildUserPGAttributeRow` stored
    the SCALAR element OID and every array column dumped as its element type
    (`tags text`, not `tags text[]`) — a type-fidelity loss on restore.
    `catalog.Type` now carries an `IsArray` field; `addCol` propagates it from the
    parsed `ColumnType`; `buildUserPGAttributeRow` remaps the scalar OID to the
    array OID via `catalog.ArrayOIDForBase` (text→`_text` 1009, int2→`_int2` 1005,
    int4→`_int4` 1007, int8→`_int8` 1016 — the four element types `format_type`
    already renders as arrays) and sets `attndims=1`, while `atttypmod` still
    carries the ELEMENT typmod (computed from the base OID before the remap).
    `userTypeAttrsForOID` gains the four array OID cases (`typlen=-1`,
    `typstorage='x'`, `_int8` align `'d'`, `_text` default collation) so the heap
    row's attlen/byval/align/storage/collation match PG. The runtime evaluator is
    untouched — `Type.Name` still holds the element type, so only the catalog
    builders see the array OID. The heap loader (`loadUserTablesFromHeap`)
    reverse-maps the persisted array OID back to the element type and re-flags
    `IsArray` via `catalog.BaseOIDForArray`, so an array column round-trips across
    restart (heap-write ↔ heap-read sibling paths kept in sync). Guarded
    end-to-end by `TestPort_PgDumpConnectionSetup` (the `arr` fixture) and by
    `executor.TestUserPGAttributeArrayColumn`. (Slice 62 mapped int2/int4/int8/
    text; slice 63 below extended the set to bool/numeric.)
  - **Slice 63 — bool/numeric array columns (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    Slice 62 only mapped int2/int4/int8/text element types, so a `flags
    boolean[]` or `prices numeric(10,2)[]` column silently fell back to its
    scalar element OID and dumped as `boolean`/`numeric(10,2)` — the array
    dimension was lost. The DDL and heap-loader paths were already generic (they
    route through `catalog.ArrayOIDForBase`/`BaseOIDForArray`), so this slice only
    extended the three element-type-keyed tables: `ArrayOIDForBase`/
    `BaseOIDForArray` gain `bool↔_bool` (1000) and `numeric↔_numeric` (1231)
    cases; `userTypeAttrsForOID` gains the `_bool`/`_numeric` rows (both `typlen=-1`,
    `typalign='i'`, `typstorage='x'`); and `formatTypeOID` renders 1000 as
    `boolean[]` and 1231 by recursing on the element type (`formatTypeOID(1700,
    typmod) + "[]"`) so the carried element typmod yields `numeric(10,2)[]` rather
    than a bare `numeric[]`. Because `atttypmod` already carries the ELEMENT typmod
    (computed from the base OID before the array remap, slice 62), the
    precision/scale round-trip needed no new plumbing. Guarded by the enriched
    `arr` fixture (`flags boolean[]`, `prices numeric(10,2)[]`) in
    `TestPort_PgDumpConnectionSetup` and the new bool/numeric rows in
    `executor.TestUserPGAttributeArrayColumn`. Remaining gap: other element types
    (date[], timestamp[], uuid[], …) still fall back to their scalar OID until
    their array OID + `format_type` rendering land.
  - **Slice 64 — float8/date/timestamp array columns (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    Extends the array-column set to the date/time + double-precision families,
    closing part of slice 63's remaining gap. Identical proven 3-site pattern:
    `ArrayOIDForBase`/`BaseOIDForArray` gain `float8↔_float8` (1022),
    `date↔_date` (1182) and `timestamp↔_timestamp` (1115); `userTypeAttrsForOID`
    gains the `_float8`/`_date`/`_timestamp` rows (all `typlen=-1`,
    `typstorage='x'`; `_float8`/`_timestamp` use `typalign='d'` matching their
    8-byte elements, `_date` uses `typalign='i'`); and `formatTypeOID` renders
    1022 as `double precision[]`, 1182 as `date[]`, and 1115 as
    `timestamp without time zone[]`. (The element scalar OIDs — `OIDFloat8` 701,
    `OIDDate` 1082, `OIDTimestamp` 1114 — already existed; only the array
    mappings were missing.) Guarded by the enriched `arr` fixture (`ratios double
    precision[]`, `days date[]`, `moments timestamp[]`) in
    `TestPort_PgDumpConnectionSetup` and the new rows in
    `executor.TestUserPGAttributeArrayColumn`. Remaining gap: `uuid[]` and other
    element types still fall back to their scalar OID — `uuid` additionally lacks
    a scalar OID in `TypeNameToOID`, so it needs the scalar type wired first.

  - **Slice 65 — float4/time/timestamptz array columns (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    Extends the array-column set to the remaining scalar-OID-backed element types,
    completing the date/time + floating-point families. Identical proven 3-site
    pattern: `ArrayOIDForBase`/`BaseOIDForArray` gain `float4↔_float4` (1021),
    `time↔_time` (1183) and `timestamptz↔_timestamptz` (1185);
    `userTypeAttrsForOID` gains the `_float4`/`_time`/`_timestamptz` rows (all
    `typlen=-1`, `typstorage='x'`; `_float4` uses `typalign='i'` matching its
    4-byte element, `_time`/`_timestamptz` use `typalign='d'` matching their
    8-byte elements); and `formatTypeOID` renders 1021 as `real[]`, 1183 as
    `time without time zone[]`, and 1185 as `timestamp with time zone[]` (the
    `oidToBuiltinTypeName` table already had 1021/1185 but was missing 1183, now
    added). (The element scalar OIDs — `OIDFloat4` 700, `OIDTime` 1083,
    `OIDTimestampTZ` 1184 — already existed; only the array mappings were missing.)
    Guarded by the enriched `arr` fixture (`speeds real[]`, `times time[]`, `zoned
    timestamptz[]`) in `TestPort_PgDumpConnectionSetup` and the new rows in
    `executor.TestUserPGAttributeArrayColumn`. Remaining gap: `uuid[]` still needs
    the scalar `uuid` type (OID 2950) wired into `TypeNameToOID` first; ENUM/
    composite/domain user-type columns and IDENTITY/SEQUENCE columns are larger
    slices.
  - **Slice 66 — scalar `uuid` + `uuid[]` columns (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    `uuid` was the first scalar element type goopg had *not* wired into the
    type-name maps, so a `uuid` column fell back to `text` (OID 25) and dumped as
    `text`, and there was no `_uuid` array OID at all. This slice closes both: the
    scalar half adds `OIDUUID` (2950) to the const block, the `uuid` case to
    `TypeNameToOID`/`OIDToTypeName`; the array half adds `OIDArrayUUID` (2951),
    the `uuid↔_uuid` cases to `ArrayOIDForBase`/`BaseOIDForArray`, the `_uuid` row
    to `userTypeAttrsForOID`, and the `2951 → uuid[]` case to `formatTypeOID`
    (`2950 → uuid` was already present). Per `pg_type.dat`, scalar `uuid` is
    `typlen=16, typbyval=f, typalign='c', typstorage='p'`; the `_uuid` array row
    follows the established goopg convention (`typlen=-1`, `typstorage='x'`,
    element-`'c'` → array `typalign='i'`, matching `_bool`). Guarded by the `arr`
    fixture's `tok uuid` + `ids uuid[]` columns in `TestPort_PgDumpConnectionSetup`,
    the new `{"uuid", nil, 2951, "uuid[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and the `{"uuid", OIDUUID}` pair in
    `catalog.TestTypeNameToOIDRoundTrip`. Remaining gaps: ENUM/composite/domain
    user-type columns and IDENTITY/SEQUENCE columns are larger slices.

  - **Slice 67 — `bytea[]` array column (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    Scalar `bytea` (OID 17) was already wired through `TypeNameToOID`/
    `OIDToTypeName`/`userTypeAttrsForOID`/`formatTypeOID`, so it round-tripped;
    its array form `_bytea` (OID 1001) was the gap — a `bytea[]` column fell back
    to scalar `bytea`. Identical proven 3-site pattern: `OIDArrayBytea` (1001) in
    the const block, the `bytea↔_bytea` cases in `ArrayOIDForBase`/
    `BaseOIDForArray`, the `_bytea` row in `userTypeAttrsForOID` (`typlen=-1`,
    `typalign='i'`, `typstorage='x'`, matching `_bool`), and the `1001 → bytea[]`
    case in the canonical `formatTypeOID` (bytea has no typmod, so the bare
    element name with `[]`). Guarded by the `arr` fixture's `blob bytea` +
    `blobs bytea[]` columns in `TestPort_PgDumpConnectionSetup` and the new
    `{"bytea", nil, 1001, "bytea[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`. Remaining gaps unchanged:
    `varchar[]`/`bpchar[]` (typmod-bearing arrays), ENUM/composite/domain
    user-type columns, and IDENTITY/SEQUENCE columns.
  - **Slice 68 — remaining simple scalar-backed arrays `varchar[]`/`bpchar[]`/
    `oid[]` (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    The element scalars (`varchar` 1043, `bpchar` 1042, `oid` 26) already
    round-tripped; their array forms `_varchar` (1015), `_bpchar` (1014) and
    `_oid` (1028) were the gap. Same proven 3-site pattern: the three array-OID
    consts (`OIDArrayVarChar`/`OIDArrayBpChar`/`OIDArrayOID`), their cases in
    `ArrayOIDForBase`/`BaseOIDForArray`, the three rows in `userTypeAttrsForOID`
    (`typlen=-1`, `typalign='i'`, `typstorage='x'`; `_varchar`/`_bpchar` carry
    the element's default collation like `_text`), and the canonical
    `formatTypeOID` cases. `_varchar`/`_bpchar` are **typmod-bearing** like
    `_numeric` — they format the element with the carried typmod and re-append
    `[]` (`formatTypeOID(1043,typmod)+"[]"` → `character varying(20)[]`,
    `formatTypeOID(1042,typmod)+"[]"` → `character(4)[]`); `_oid` has no typmod so
    it is the bare `oid[]`. Guarded by the `arr` fixture's `label varchar(20)` +
    `labels varchar(20)[]` + `code char(4)` + `codes char(4)[]` + `oids oid[]`
    columns in `TestPort_PgDumpConnectionSetup` and the new
    `{"varchar",[]int64{20},1015,"character varying(20)[]"}`,
    `{"bpchar",[]int64{10},1014,"character(10)[]"}`, `{"oid",nil,1028,"oid[]"}`
    rows in `executor.TestUserPGAttributeArrayColumn`. This completes every
    simple scalar-OID-backed array type; remaining gaps are now
    ENUM/composite/domain user-type columns and IDENTITY/SEQUENCE columns.

  - **Slice 69 — JSON family `json`/`json[]` + `jsonb`/`jsonb[]`
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** `json` (OID 114) and `jsonb` (OID 3802) were
    absent from `TypeNameToOID`/`OIDToTypeName`, so a `json`/`jsonb` column fell
    back to `text` (OID 25) and dumped as `text`; the array path had no
    `_json`/`_jsonb` OID at all. Same proven 3-site pattern: the two scalar-OID
    consts (`OIDJSON`/`OIDJsonb`) + their `TypeNameToOID`/`OIDToTypeName` cases,
    the two array-OID consts (`OIDArrayJSON` 199 / `OIDArrayJsonb` 3807) + cases
    in `ArrayOIDForBase`/`BaseOIDForArray`, four rows in `userTypeAttrsForOID`
    (`typlen=-1`, `typbyval=f`, `typalign='i'`, `typstorage='x'`, no collation —
    matching `pg_type.dat`), and the array cases in the canonical `formatTypeOID`
    (the scalar `114`→`json` / `3802`→`jsonb` cases already existed). json/jsonb
    are varlena with **no typmod**, so the arrays render as the bare `json[]` /
    `jsonb[]`. Guarded by the `arr` fixture's `doc json` + `docs json[]` + `jdoc
    jsonb` + `jdocs jsonb[]` columns in `TestPort_PgDumpConnectionSetup` and the
    new `{"json",nil,199,"json[]"}`, `{"jsonb",nil,3807,"jsonb[]"}` rows in
    `executor.TestUserPGAttributeArrayColumn`.
  - **Slice 70 — `interval`/`interval[]`
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** `interval` (OID 1186) was rendered by both
    `formatTypeOID` and `oidToBuiltinTypeName` (the latter already mapped both
    `1186`→`interval` and `1187`→`interval[]`) but had **never been wired into
    `TypeNameToOID`/`OIDToTypeName`**, so an `interval` column fell back to `text`
    (OID 25) and dumped as `text`; the array path had no `_interval` OID at all.
    Same proven 3-site pattern: the scalar-OID const `OIDInterval` 1186 + its
    `TypeNameToOID`/`OIDToTypeName` cases, the array-OID const `OIDArrayInterval`
    1187 + cases in `ArrayOIDForBase`/`BaseOIDForArray`, two rows in
    `userTypeAttrsForOID` (scalar `typlen=16`, `typbyval=f`, `typalign='d'`,
    `typstorage='p'`; array `typlen=-1`, `typalign='d'`, `typstorage='x'` — matching
    `pg_type.dat` OIDs 1186/1187 and the `pg_type_seed_data.go` rows), and the
    array case in the canonical `formatTypeOID` (`1187`→`interval[]`; the scalar
    `1186`→`interval` case already existed). A bare interval column has typmod
    `-1`, so both render as the plain `interval` / `interval[]`. Guarded by the
    `arr` fixture's `span interval` + `spans interval[]` columns in
    `TestPort_PgDumpConnectionSetup` and the new `{"interval",nil,1187,
    "interval[]"}` row in `executor.TestUserPGAttributeArrayColumn`.
  - **Slice 71 — network-address family `inet`/`cidr`/`macaddr`/`macaddr8`
    (+ their array types)
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** All four scalar types (OIDs 869/650/829/774)
    and their array types (1041/651/1040/775) are already seeded in
    `pg_type_seed_data.go` so a PG standby can resolve the OIDs, but none had been
    wired into `TypeNameToOID`/`OIDToTypeName`, so each scalar column fell back to
    `text` (OID 25) and dumped as `text`; the array paths had no `_net` OIDs at
    all. Same proven 3-site pattern: scalar/array OID consts + their
    `TypeNameToOID`/`OIDToTypeName` and `ArrayOIDForBase`/`BaseOIDForArray` cases,
    rows in `userTypeAttrsForOID` (scalar `inet`/`cidr` `typlen=-1`,
    `typalign='i'`, `typstorage='m'`; `macaddr` `typlen=6` and `macaddr8`
    `typlen=8`, both `typbyval=f`, `typalign='i'`, `typstorage='p'`; arrays
    `typlen=-1`, `typalign='i'`, `typstorage='x'` — matching `pg_type.dat` and the
    `pg_type_seed_data.go` rows), and the scalar+array cases in `formatTypeOID`
    and `oidToBuiltinTypeName`. None carry a typmod, so every column renders as
    the plain `<type>` / `<type>[]`. Guarded by the `arr` fixture's `ip inet`,
    `ips inet[]`, `net cidr`, `nets cidr[]`, `mac macaddr`, `macs macaddr[]`,
    `mac8 macaddr8`, `mac8s macaddr8[]` columns in `TestPort_PgDumpConnectionSetup`
    and the four new rows in `executor.TestUserPGAttributeArrayColumn`.
  - **Slice 72 — geometric family `point`/`lseg`/`path`/`box`/`polygon`/`line`/
    `circle` (+ their array types)
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** All seven scalar types (OIDs
    600/601/602/603/604/628/718) and their array types
    (1017/1018/1019/1020/1027/629/719) are already seeded in
    `pg_type_seed_data.go`, but none had been wired into `TypeNameToOID`/
    `OIDToTypeName`, so each scalar column fell back to `text` (OID 25) and dumped
    as `text` (the lone `formatTypeOID(600)→point` case was dead — nothing routed a
    `point` column to OID 600); the array paths had no OIDs at all. Same proven
    3-site pattern: scalar/array OID consts + their `TypeNameToOID`/`OIDToTypeName`
    and `ArrayOIDForBase`/`BaseOIDForArray` cases, rows in `userTypeAttrsForOID`
    (fixed-width `point` `typlen=16`, `lseg`/`box` `typlen=32`, `line`/`circle`
    `typlen=24`, all `typalign='d'`, `typstorage='p'`; varlena `path`/`polygon`
    `typlen=-1`, `typalign='d'`, `typstorage='x'`; all scalars `typbyval=f`; arrays
    `typlen=-1`, `typalign='d'`, `typstorage='x'` — matching `pg_type.dat` and the
    `pg_type_seed_data.go` rows), and the scalar+array cases in `formatTypeOID` and
    `oidToBuiltinTypeName`. None carry a typmod, so every column renders as the
    plain `<type>` / `<type>[]`. Guarded by the `arr` fixture's `pt point`,
    `pts point[]`, `seg lseg`, `segs lseg[]`, `pth path`, `pths path[]`, `bx box`,
    `bxs box[]`, `poly polygon`, `polys polygon[]`, `ln line`, `lns line[]`,
    `circ circle`, `circs circle[]` columns in `TestPort_PgDumpConnectionSetup` and
    the seven new rows in `executor.TestUserPGAttributeArrayColumn`.
  - **Slice 73 — full-text-search family `tsvector`/`tsquery` (+ their array
    types) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** Both scalar types (`tsvector` OID 3614,
    `tsquery` 3615) and their array types (`_tsvector` 3643, `_tsquery` 3645) are
    already seeded in `pg_type_seed_data.go`, but neither had been wired into
    `TypeNameToOID`/`OIDToTypeName`, so each scalar column fell back to `text`
    (OID 25) and dumped as `text` (the scalar `formatTypeOID(3614)→tsvector` /
    `formatTypeOID(3615)→tsquery` cases existed but were dead — nothing routed a
    column to those OIDs); the array paths had no OIDs at all. Same proven 3-site
    pattern: scalar/array OID consts + their `TypeNameToOID`/`OIDToTypeName` and
    `ArrayOIDForBase`/`BaseOIDForArray` cases, rows in `userTypeAttrsForOID`
    (varlena `tsvector` `typlen=-1`, `typalign='i'`, `typstorage='x'`; `tsquery`
    `typlen=-1`, `typalign='i'`, `typstorage='p'`; both scalars `typbyval=f`;
    arrays `typlen=-1`, `typalign='i'`, `typstorage='x'` — matching `pg_type.dat`
    and the `pg_type_seed_data.go` rows), and the new array cases in
    `formatTypeOID` plus the scalar+array cases in `oidToBuiltinTypeName`. Neither
    carries a typmod, so every column renders as the plain `<type>` / `<type>[]`.
    Guarded by the `arr` fixture's `tsv tsvector`, `tsvs tsvector[]`, `tsq
    tsquery`, `tsqs tsquery[]` columns in `TestPort_PgDumpConnectionSetup` and the
    two new rows in `executor.TestUserPGAttributeArrayColumn`.
  - **Slice 74 — `xml`/`money` (+ their array types) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** Both scalar types (`xml` OID 142, `money` 790)
    and their array types (`_xml` 143, `_money` 791) are already seeded in
    `pg_type_seed_data.go`, but neither had been wired into `TypeNameToOID`/
    `OIDToTypeName`, so each scalar column fell back to `text` (OID 25) and dumped
    as `text`; the array paths had no OIDs. (`oidToBuiltinTypeName` already had the
    scalar `xml` case from an earlier path, but `money` and both arrays were
    missing, and nothing routed a column to those OIDs.) Same proven 3-site
    pattern: scalar/array OID consts + their `TypeNameToOID`/`OIDToTypeName` and
    `ArrayOIDForBase`/`BaseOIDForArray` cases, rows in `userTypeAttrsForOID`
    (varlena `xml` `typlen=-1`, `typbyval=f`, `typalign='i'`, `typstorage='x'`;
    fixed-width `money` `typlen=8`, `typbyval=t`, `typalign='d'`, `typstorage='p'`;
    arrays `typlen=-1`, `typbyval=f`, `_xml` `typalign='i'`/`_money`
    `typalign='d'`, `typstorage='x'` — matching `pg_type.dat` and the
    `pg_type_seed_data.go` rows), and the new scalar+array cases in `formatTypeOID`
    plus `oidToBuiltinTypeName`. Neither carries a typmod, so every column renders
    as the plain `<type>` / `<type>[]`. Guarded by the `arr` fixture's `xm xml`,
    `xms xml[]`, `mny money`, `mnys money[]` columns in
    `TestPort_PgDumpConnectionSetup` and the two new rows in
    `executor.TestUserPGAttributeArrayColumn`.

  - **Slice 75 — `bit`/`varbit` (+ their array types) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** Both scalar types (`bit` OID 1560, `varbit`
    1562) and their array types (`_bit` 1561, `_varbit` 1563) are already seeded in
    `pg_type_seed_data.go`, but neither had been wired into `TypeNameToOID`/
    `OIDToTypeName`, so each scalar column fell back to `text` (OID 25). Unlike the
    prior few slices, **both carry a typmod**: `bit(n)`/`bit varying(n)` store the
    raw bit length as `atttypmod` with **no VARHDRSZ adjustment** (mirroring
    `anybit_typmodin`/`anybit_typmodout`), so the 3-site pattern is extended with a
    fourth site — a `bit`/`varbit` case in `pgAttTypmod` (returns `args[0]`
    verbatim, not `+4`) — plus the typmod-aware `formatTypeOID` cases that render
    `bit(n)` / `bit varying(n)` (and the array cases that format the element with
    the carried typmod and re-append `[]`, like `_varchar`/`_numeric`). `format_type`
    special-cases these to the SQL spellings `bit` / `bit varying` (see
    `format_type_extended` `BITOID`/`VARBITOID`), so `oidToBuiltinTypeName` returns
    `"bit"`/`"bit varying"` and their `[]` forms. Rows in `userTypeAttrsForOID`:
    varlena `bit`/`varbit` `typlen=-1`, `typbyval=f`, `typalign='i'`,
    `typstorage='x'`; arrays likewise (matching `pg_type.dat`/`pg_type_seed_data.go`).
    The parser already maps `bit varying`→`varbit` and parses the `(n)` typmod +
    `[]` suffix. Guarded by the `arr` fixture's `bv bit(8)`, `bvs bit(8)[]`,
    `vb varbit(16)`, `vbs varbit(16)[]` columns in `TestPort_PgDumpConnectionSetup`
    (asserting the rendered `bit(8)` / `bit varying(16)` spellings) and new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`.
  - **Slice 76 — `pg_lsn` (+ its array type) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** The WAL log-sequence-number type (`pg_lsn`
    OID 3220) and its array (`_pg_lsn` 3221) are already seeded in
    `pg_type_seed_data.go` (and `pg_lsn` is supported by the analyzer/executor
    for arithmetic and comparison), but neither had been wired into the pg_dump
    catalog-codec path, so a `pg_lsn` column fell back to `text` (OID 25). Back to
    the plain 3-site pattern (no typmod): `pg_lsn` is an 8-byte by-value type
    (`typlen=8`, `typbyval=t`, `typalign='d'`, `typstorage='p'`; array
    `typalign='d'`, `typstorage='x'`). `oidToBuiltinTypeName` already returned
    `"pg_lsn"` for the scalar (used by older paths) but had no array case, and
    `formatTypeOID` had neither — both gained `pg_lsn` / `pg_lsn[]` cases. The
    parser accepts `pg_lsn` as a generic identifier (no multi-word / typmod
    handling needed). Guarded by the `arr` fixture's `lsn pg_lsn`, `lsns
    pg_lsn[]` columns in `TestPort_PgDumpConnectionSetup` and new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`.
  - **Slice 77 — `txid_snapshot` / `pg_snapshot` (+ their array types)
    (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** The transaction-snapshot types — the legacy
    txid-based `txid_snapshot` (OID 2970, array `_txid_snapshot` 2949) and its
    xid8-based successor `pg_snapshot` (OID 5038, array `_pg_snapshot` 5039) —
    are already seeded in `pg_type_seed_data.go` (and supported by the
    snapshot SRFs / I-O functions in `pg_proc_seed_data.go`), but neither had
    been wired into the pg_dump catalog-codec path, so a snapshot column fell
    back to `text` (OID 25). Both are varlena with no typmod, so this is the
    plain 3-site pattern: `typlen=-1`, `typbyval=f`, `typalign='d'`,
    `typstorage='x'` for both the scalars and the arrays. `TypeNameToOID` /
    `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray` gained the four
    cases; `oidToBuiltinTypeName` and `formatTypeOID` gained scalar + array
    cases (none existed before). The parser accepts both as generic identifiers
    (no multi-word / typmod handling needed). Guarded by the `arr` fixture's
    `txs txid_snapshot`, `txss txid_snapshot[]`, `pgs pg_snapshot`, `pgss
    pg_snapshot[]` columns in `TestPort_PgDumpConnectionSetup` and new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`.
  - **Slice 78 — `xid8` (+ its array type) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** `xid8` (OID 5069, array `_xid8` 271) is the
    64-bit full-transaction-id type and the element of `pg_snapshot`. It is
    already seeded in `pg_type_seed_data.go` and recognized as a known builtin by
    the analyzer (`isKnownBuiltinType`), with input/cast/cmp support in
    `expr.go` (M0097-0018), but had never been wired into the pg_dump
    catalog-codec path, so an `xid8` column fell back to `text` (OID 25). It is
    an 8-byte by-value type with no typmod, so this is the plain 3-site pattern:
    the scalar is `typlen=8`, `typbyval=t`, `typalign='d'`, `typstorage='p'`; the
    array is the varlena `typlen=-1`, `typbyval=f`, `typalign='d'`,
    `typstorage='x'`. `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` /
    `BaseOIDForArray` gained the two cases; `oidToBuiltinTypeName` and
    `formatTypeOID` gained scalar + array cases (none existed before). Guarded by
    the `arr` fixture's `x8 xid8`, `x8s xid8[]` columns in
    `TestPort_PgDumpConnectionSetup` and new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`.
  - **Slice 79 — `tid` / `xid` / `cid` (+ their array types)
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** `tid` (OID 27, array `_tid` 1010), `xid`
    (OID 28, array `_xid` 1011), and `cid` (OID 29, array `_cid` 1012) are the
    tuple-identifier / transaction-id / command-id system types. All three were
    seeded in `pg_type_seed_data.go` and recognized by the analyzer, and the
    scalar names already appeared in `formatTypeOID` (OIDs 27/28/29), but none
    were wired into the codec OID round-trip (`TypeNameToOID` / `OIDToTypeName`)
    nor into `oidToBuiltinTypeName`, and the three array OIDs had no entry in any
    path — so a `tid`/`xid`/`cid` column fell back to `text` (OID 25) and the
    arrays were unresolved. None carry a typmod, so this is the plain 3-site
    pattern: scalars are `tid` `{typlen=6, byval=f, align='s', storage='p'}`,
    `xid`/`cid` `{typlen=4, byval=t, align='i', storage='p'}`; each array is the
    varlena `{typlen=-1, byval=f, align='i', storage='x'}`. `TypeNameToOID` /
    `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray` gained the three
    scalar+array cases; `oidToBuiltinTypeName` gained scalar+array cases and
    `formatTypeOID` gained the three array cases (the scalars already existed).
    Guarded by the `arr` fixture's `td tid`, `tds tid[]`, `xd xid`, `xds xid[]`,
    `cd cid`, `cds cid[]` columns in `TestPort_PgDumpConnectionSetup` and new
    rows in `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`.
  - **Slice 80 — the OID-reference (`reg*`) family (+ their array types)
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** The eleven OID-alias types `regproc` (24),
    `regprocedure` (2202), `regoper` (2203), `regoperator` (2204), `regclass`
    (2205), `regtype` (2206), `regconfig` (3734), `regdictionary` (3769),
    `regnamespace` (4089), `regrole` (4096), and `regcollation` (4191), plus their
    `_reg*` array types (`1008`, `2207`, `2208`, `2209`, `2210`, `2211`, `3735`,
    `3770`, `4090`, `4097`, `4192`). All eleven scalars are 4-byte by-value oid
    aliases (`{typlen=4, byval=t, align='i', storage='p'}`) and each array is the
    varlena `{typlen=-1, byval=f, align='i', storage='x'}`; none carry a typmod, so
    this is the plain 3-site pattern. All were seeded in `pg_type_seed_data.go` (so
    a PG standby resolves the OIDs) but were never wired into the codec OID
    round-trip — so a declared `regclass` column fell back to `text` (OID 25) and
    the arrays were unresolved. (The `::regclass`/`::regproc`/`::regtype` value
    *casts* in `expr.go` and the catalog-column OID seeding in
    `initdb.pgCatalogTypeOID` / `typeOIDForCatalogColumn` already mapped `regproc`
    → 24 independently; those are separate paths and unchanged.) `TypeNameToOID` /
    `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray` gained the eleven
    scalar+array cases; `userTypeAttrsForOID` gained grouped scalar+array cases;
    `oidToBuiltinTypeName` and `formatTypeOID` gained the eleven scalar+array
    cases. Guarded by the `arr` fixture's `rp regproc` … `rcos regcollation[]`
    columns in `TestPort_PgDumpConnectionSetup`, new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`, and
    new pairs in `catalog.TestTypeNameToOIDRoundTrip` (which also guards against a
    name collision routing `regproc` to text).
  - **Slice 81 — the legacy vector types `int2vector` / `oidvector` (+ their array
    types) (`internal/catalog/codec.go`,
    `internal/executor/pg18_user_catalog_rows.go`, `internal/executor/expr.go`).**
    `int2vector` (22) is a space-separated list of `int2` (used internally for
    `pg_index.indkey`); `oidvector` (30) a space-separated list of `oid`
    (`pg_proc.proargtypes`). Both are declarable column types and were seeded in
    `pg_type_seed_data.go`, but **mis-wired**: `formatTypeOID` rendered OID 22/30 as
    `smallint[]` / `oid[]` — the renderings that belong to the *genuine* `_int2`
    (1005) and `_oid` (1028) array types — instead of their own bare names, and the
    codec had no `name→OID` entry, so a declared `oidvector` column fell back to
    `text` (OID 25). Real PG's `format_type(30,-1)` is `oidvector`, not `oid[]` (the
    vector types have `typcategory='A'` but are distinct from the array types). The
    scalars are varlena `{typlen=-1, byval=f, align='i', storage='p'}` and the arrays
    `_int2vector` (1006) / `_oidvector` (1013) are `{…, storage='x'}`; neither carries
    a typmod, so this is the plain 3-site pattern plus the two `formatTypeOID`
    corrections. `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` /
    `BaseOIDForArray` gained the scalar+array cases; `userTypeAttrsForOID` gained
    grouped scalar+array cases; `oidToBuiltinTypeName` gained the missing
    `int2vector` (22) case (`oidvector` was already correct), and `formatTypeOID`'s
    22/30 cases were **fixed** to the bare vector names with new 1006/1013 array
    cases. Guarded by the `arr` fixture's `iv int2vector` … `ovs oidvector[]`
    columns in `TestPort_PgDumpConnectionSetup`, new rows in
    `executor.TestUserPGAttributeArrayColumn` + `TestUserPGAttributeTypmod`, and new
    pairs in `catalog.TestTypeNameToOIDRoundTrip`.
  - **Slice 82 — the `name` catalog identifier type (+ its array `_name`)
    (`internal/catalog/codec.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** `name` (19) is the 64-byte fixed-length type
    used internally for catalog name columns (`relname`/`attname`/`typname`) and is
    a declarable column type; `format_type(19,-1)` renders the bare `name`. The
    scalar was *already* wired on every display/attr path — `formatTypeOID` (case
    19), `oidToBuiltinTypeName` (case 19), and `userTypeAttrsForOID` (case 19, with
    `typcollation=C_COLLATION_OID=950`) all predate this slice — but the **codec had
    no `name→OID` entry**, so a declared `name` column round-tripped as `text` (25),
    and the array `_name` (1003) had no `format_type`/array-OID wiring. Both `name`
    (19) and `_name` (1003) were already seeded in `pg_type_seed_data.go`. This slice
    adds only the missing array+codec wiring: `TypeNameToOID` / `OIDToTypeName` /
    `ArrayOIDForBase` / `BaseOIDForArray` gained the `name`↔`_name` cases,
    `userTypeAttrsForOID` gained the `_name` array case (varlena `{typlen=-1,
    byval=f, align='i', storage='x'}` carrying the `C` element collation), and
    `formatTypeOID` gained the 1003 case (→ `name[]`; `name` has no typmod). The
    `"char"` type (18) is deliberately **deferred**: the parser folds both quoted
    `"char"` and bare `char` to `ct.Name="char"`, so disambiguating the 1-byte
    internal char from `bpchar` needs a parser change, not just a codec entry.
    Guarded by the `arr` fixture's `nm name` / `nms name[]` columns in
    `TestPort_PgDumpConnectionSetup`, a new `{"name", nil, 1003, "name[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and a new `name`/`_name` round-trip
    block in `catalog.TestTypeNameToOIDRoundTrip`.
  - **Slice 83 — the `timetz` (`time with time zone`) type (+ its array `_timetz`)
    (`internal/catalog/codec.go`, `internal/executor/expr.go`,
    `internal/executor/pg18_user_catalog_rows.go`).** `timetz` (1266) is a
    12-byte (8-byte time + 4-byte zone offset) date/time type and a declarable
    column type; `format_type(1266,-1)` renders `time with time zone`. The display
    path was only **partially** wired — `oidToBuiltinTypeName` had case 1266, but
    `formatTypeOID` (the actual `format_type()` implementation that pg_dump reads
    for the column type) had **no 1266 case**, so it fell through to its default,
    and the **codec had no `timetz→OID` entry**, so a declared `timetz` column
    round-tripped as `text` (25). The array `_timetz` (1270) had no
    `format_type`/array-OID/attr wiring at all. Both `timetz` (1266) and `_timetz`
    (1270) were already seeded in `pg_type_seed_data.go`. This slice adds the
    missing wiring: `OIDTimeTZ`/`OIDArrayTimeTZ` consts plus the `timetz`↔`_timetz`
    cases in `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` /
    `BaseOIDForArray`; the **scalar** `formatTypeOID` case 1266 (→ `time with time
    zone`) that was missing, plus the array case 1270 (→ `time with time zone[]`)
    in **both** `formatTypeOID` and `oidToBuiltinTypeName` (sibling display paths);
    and `userTypeAttrsForOID` cases for the scalar (`{typlen=12, byval=f,
    align='d', storage='p'}`) and the array (varlena `{typlen=-1, byval=f,
    align='d', storage='x'}`). Guarded by the `arr` fixture's `tt timetz` /
    `tts timetz[]` columns in `TestPort_PgDumpConnectionSetup`, a new
    `{"timetz", nil, 1270, "time with time zone[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and a new `timetz`/`_timetz`
    round-trip block in `catalog.TestTypeNameToOIDRoundTrip`.
  - **Slice 84 — the `jsonpath` (SQL/JSON path) type (+ its array `_jsonpath`)
    (`internal/catalog/codec.go`, `internal/executor/expr.go`,
    `internal/executor/pg18_user_catalog_rows.go`).** `jsonpath` (4072) is a
    varlena type that stores a compiled SQL/JSON path expression and is a
    declarable column type; `format_type(4072,-1)` renders the bare `jsonpath`.
    The display path was wired **inconsistently** between the two sibling
    functions: `formatTypeOID` already carried a scalar `case 4072` (added
    speculatively alongside the json family in slice 69), but it was **dead code**
    because the **codec had no `jsonpath→OID` entry** — a declared `jsonpath`
    column resolved to `text` (25), so `formatTypeOID(4072)` was never reached.
    Meanwhile `oidToBuiltinTypeName` lacked **even the scalar** 4072 case, and the
    array `_jsonpath` (4073) had no `format_type`/array-OID/attr wiring in either
    function. Both `jsonpath` (4072) and `_jsonpath` (4073) were already seeded in
    `pg_type_seed_data.go`. This slice adds the missing wiring: `OIDJsonpath`/
    `OIDArrayJsonpath` consts plus the `jsonpath`↔`_jsonpath` cases in
    `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray`; the
    array `formatTypeOID` case 4073 (→ `jsonpath[]`) and **both** the scalar 4072
    and array 4073 cases in `oidToBuiltinTypeName` (sibling display paths brought
    back into sync); and `userTypeAttrsForOID` cases for the scalar and the array
    (both varlena `{typlen=-1, byval=f, align='i', storage='x'}`). Guarded by the
    `arr` fixture's `jp jsonpath` / `jps jsonpath[]` columns in
    `TestPort_PgDumpConnectionSetup`, a new
    `{"jsonpath", nil, 4073, "jsonpath[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and a new `jsonpath`/`_jsonpath`
    round-trip block in `catalog.TestTypeNameToOIDRoundTrip`.
  - **Slice 85 — the `refcursor` type (+ its array `_refcursor`)
    (`internal/catalog/codec.go`, `internal/executor/expr.go`,
    `internal/executor/pg18_user_catalog_rows.go`).** `refcursor` (1790) is a
    varlena cursor-name reference and a declarable column type; `format_type`
    renders the bare `refcursor`. Unlike slice 84's `jsonpath`, `refcursor` had
    **no wiring at all** in either display function or the codec, so a declared
    `refcursor` column resolved to `text` (25) and round-tripped as text; the
    array `_refcursor` (2201) was likewise unwired. Both 1790/2201 were already
    seeded in `pg_type_seed_data.go`. This slice adds `OIDRefcursor`/
    `OIDArrayRefcursor` consts plus the `refcursor`↔`_refcursor` cases in
    `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray`; the
    scalar 1790 and array 2201 cases in **both** `formatTypeOID`
    (→ `refcursor`/`refcursor[]`) and `oidToBuiltinTypeName` (sibling display
    paths kept in sync); and `userTypeAttrsForOID` cases for the scalar and the
    array (both varlena `{typlen=-1, byval=f, align='i', storage='x'}`). Guarded
    by the `arr` fixture's `rfc refcursor` / `rfcs refcursor[]` columns in
    `TestPort_PgDumpConnectionSetup`, a new
    `{"refcursor", nil, 2201, "refcursor[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and a new `refcursor`/`_refcursor`
    round-trip block in `catalog.TestTypeNameToOIDRoundTrip`.
  - **Slice 86 — the `aclitem` type (+ its array `_aclitem`)
    (`internal/catalog/codec.go`, `internal/executor/expr.go`,
    `internal/executor/pg18_user_catalog_rows.go`).** `aclitem` (1033) is the
    16-byte access-control-list item type used internally for catalog `*acl`
    columns (`pg_class.relacl`, `pg_database.datacl`, …) but is also a declarable
    column type; `format_type` renders the bare `aclitem`. Like slice 85's
    `refcursor`, the type had **no codec name→OID entry**, so a declared
    `aclitem` column resolved to `text` (25) and round-tripped as text; neither
    display function rendered 1033/1034 (the array name `aclitem[]`/`_aclitem`
    was only handled in the empty-array encode path and initdb's own resolver,
    not in the user-table DDL path). Both 1033/1034 were already seeded in
    `pg_type_seed_data.go` (typlen 16, typbyval f, typalign `'d'`, typstorage
    `'p'`; verified against upstream `pg_type.dat`). This slice adds
    `OIDAclitem`/`OIDArrayAclitem` consts plus the `aclitem`↔`_aclitem` cases in
    `TypeNameToOID` / `OIDToTypeName` / `ArrayOIDForBase` / `BaseOIDForArray`; the
    scalar 1033 and array 1034 cases in **both** `formatTypeOID`
    (→ `aclitem`/`aclitem[]`) and `oidToBuiltinTypeName` (sibling display paths
    kept in sync); and `userTypeAttrsForOID` cases for the scalar
    (`{16, byval=f, align='d', storage='p'}`) and the array
    (`{-1, byval=f, align='d', storage='x'}`). Guarded by the `arr` fixture's
    `acl aclitem` / `acls aclitem[]` columns in `TestPort_PgDumpConnectionSetup`,
    a new `{"aclitem", nil, 1034, "aclitem[]"}` row in
    `executor.TestUserPGAttributeArrayColumn`, and a new `aclitem`/`_aclitem`
    round-trip block in `catalog.TestTypeNameToOIDRoundTrip`.

  - **Slice 87 — the single-byte `"char"` type (+ its array `_char`)
    (`internal/catalog/codec.go`, `internal/executor/expr.go`,
    `internal/executor/pg18_user_catalog_rows.go`).** `"char"` (OID 18) is the
    1-byte internal character type — distinct from `bpchar`/`character` (1042) —
    used by catalog columns such as `pg_class.relkind` and declarable as a user
    column via the quoted spelling `"char"`. Unlike every prior slice, its
    name→OID lookup is **intentionally not invertible**: both `"char"` and
    `bpchar(1)` arrive at the catalog as type name `"char"`, so a name-only
    `TypeNameToOID` cannot disambiguate them. The parser already encodes the
    difference in the args — the unquoted `char` form is folded to `bpchar(1)`
    (length arg `[1]`), while a quoted `"char"` carries no arg
    (`internal/parser/ddl.go:2635`). The fix therefore lives at the catalog-row
    layer: `buildUserPGAttributeRow` remaps `bpchar`→`OIDChar` when the column's
    type name is `"char"` **and** it has no length arg, so `pg_attribute.atttypid`
    reports 18 (and `_char` 1002 for the array) and `format_type` renders the
    quoted `"char"` / `"char"[]`. `TypeNameToOID("char")` is left returning
    `bpchar` (the general, args-unaware callers — e.g. the value codec — keep
    treating a length-1 `"char"` as a 1-byte string, so no encode/decode path
    changes). This slice adds `OIDChar`/`OIDArrayChar` consts; the
    `OIDChar`↔`OIDArrayChar` cases in `ArrayOIDForBase` / `BaseOIDForArray`; the
    `OIDChar`→`"char"` case in `OIDToTypeName` (heap-loader reconstruction is
    self-consistent: a reloaded `"char"` column has no arg and re-resolves to 18);
    the array `case 1002` in `formatTypeOID` (the scalar 18 and array 1002 cases
    in `oidToBuiltinTypeName` were already present); and `userTypeAttrsForOID`'s
    `_char` case (`{-1, byval=f, align='i', storage='x'}`; the scalar 18 case was
    already present). Both 18/1002 were already seeded in `pg_type_seed_data.go`
    (char: typlen 1, byval t, align `'c'`, storage `'p'`, no collation). Guarded by
    the `arr` fixture's `ch "char"` / `chs "char"[]` columns in
    `TestPort_PgDumpConnectionSetup`, a `{"char", nil, 1002, "\"char\"[]"}` array
    row plus a scalar `"char"`-vs-`char(1)` disambiguation assertion in
    `executor.TestUserPGAttributeArrayColumn`, and a `char`/`_char` block (noting
    the deliberate `TypeNameToOID("char")==bpchar` asymmetry) in
    `catalog.TestTypeNameToOIDRoundTrip`.

  - **Slice 88 — user-defined `ENUM` types and enum-typed columns
    (`internal/catalog/catalog.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`).** The first **object** type in the fixture: the
    simple scalar/array *column* types are exhausted (every `pg_type` base type
    with a user-facing array peer is wired through slices 62–87). `CREATE TYPE …
    AS ENUM` is already supported (the enum heap row goes to `pg_type` via
    `syncEnumTypeToCatalogHeap`/`buildUserPGTypeRowForEnum` with `typtype='e'`,
    and `pg_enum` is a virtual view populated from the in-memory `enumTypes`
    registry), so pg_dump's `getTypes` + `dumpEnumType` already emit
    `CREATE TYPE public.mood AS ENUM ('sad', 'ok', 'happy');`. The gap was the
    enum **column**: an enum's `pg_type` OID is **dynamically allocated** at
    `CREATE TYPE` time (user OID range), and `TypeNameToOID` knows only built-ins,
    so an enum column folded to the `text` fallback (OID 25) — it dumped as
    `feeling text`, a silent type change on restore. Two resolution sites close
    it, both keyed on the dynamic OID so they cannot use a static `case`:
    (1) `buildUserPGAttributeRow` now takes the `catalog.Catalog` and, when the
    name resolves to the text fallback and is not an array, re-resolves it via
    `LookupEnum` to the enum OID and stamps the enum `pg_type` shape (`attlen=4`,
    `attbyval=f`, `attalign='i'`, `attstorage='p'`, no collation — mirroring
    `buildUserPGTypeRowForEnum`); (2) the `format_type` evaluator, when
    `formatTypeOID` returns the `"???"` unknown sentinel, resolves the OID back
    through the new `LookupEnumByOID` and renders `public.<name>` (pg_dump runs
    with `search_path=''`, under which `format_type` schema-qualifies a
    non-visible type; goopg enums live in `public`). `LookupEnum` /
    `LookupEnumByOID` are added to the `catalog.Catalog` interface (the
    `SearchPathCatalog` wrapper forwards them via embedding; `InMemory` is the
    sole concrete implementor). Scope is **scalar** enum columns: an enum array
    column has no `_enum` array-type OID (none is allocated for user enums), so
    the `!col.Type.IsArray` guard leaves it alone — out of scope, noted for a
    later slice. Cross-restart enum persistence (no `loadUserEnumsFromHeap`) is
    likewise out of scope: the dump test creates and dumps in one server session.
    Guarded by `executor.TestUserPGAttributeEnumColumn` (enum column → enum OID +
    enum `pg_type` shape, and the `LookupEnumByOID` inverse) and the
    `CREATE TYPE public.mood`/`feeling public.mood` (plus no-`feeling text`)
    assertions in `TestPort_PgDumpConnectionSetup` (real pg_dump round-trip).

  - **Slice 89 — enum **array** columns (`feelings mood[]`) + two enabling fixes
    (`internal/catalog/catalog.go`, `internal/executor/pg18_user_catalog_rows.go`,
    `internal/executor/expr.go`, `internal/executor/operators_ddl.go`,
    `internal/planner/planner.go`).** Slice 88 left enum **array** columns folding
    to `text[]`. PostgreSQL auto-generates a `_name` array type alongside every
    enum, so `RegisterEnum` now allocates **two** OIDs (the enum, then its array;
    `EnumType.ArrayOID = OID+1`) and `syncEnumTypeToCatalogHeap` writes **both**
    `pg_type` heap rows (`buildUserPGTypeRowForEnum` now stamps `typarray =
    ArrayOID`; the new `buildUserPGTypeRowForEnumArray` writes the `_mood` row with
    `typtype='b'`, `typcategory='A'`, `typelem = enumOID`, varlena/extended
    shape). `buildUserPGAttributeRow` resolves a `mood[]` column to `ArrayOID`
    (`attndims=1`, varlena array shape), and `format_type` renders it
    `public.mood[]` via the new `LookupEnumByArrayOID`. `DROP TYPE` stamps both
    rows. The array `pg_type` row is **required** because pg_dump's `getTableAttrs`
    passes the *joined* `t.oid` (`LEFT JOIN pg_type t ON a.atttypid = t.oid`) to
    `format_type` — a missing row joins to NULL and renders the column type blank.
    Two pre-existing goopg gaps surfaced and are fixed here so the array type is
    recognized as **auto-generated** (and not dumped as a bogus standalone
    `CREATE TYPE public._mood`), which hinges on pg_dump's `getTypes` `isarray`
    expression `typname[0] = '_' AND typelem != 0 AND (SELECT typarray FROM pg_type
    te WHERE oid = pg_type.typelem) = oid`:
    (a) **0-based `name` subscripting** — `array_subscript` treated every value as
    a 1-based text-array literal, so `typname[0]` over a `name` returned NULL.
    PostgreSQL indexes the fixed-length `name` pseudo-array 0-based by character;
    `array_subscript` now returns the Nth rune when the operand is not a `{…}`
    array literal.
    (b) **correlated reference to an unaliased outer table** —
    `bindingMatchesRelation` matched a binding by its *original table name* even
    when it carried an alias, so the subquery's `pg_type.typelem` (meant for the
    unaliased **outer** `pg_type`) wrongly bound to the inner `pg_type te`,
    yielding NULL. PostgreSQL hides the original name once a FROM entry is aliased;
    `bindingMatchesRelation` now matches an aliased binding **only** by its alias,
    letting `pg_type.typelem` fall through to the outer scope as an
    `OuterColumnRef`. Guarded by `executor.TestUserPGAttributeEnumArrayColumn`
    (array column → `ArrayOID`, `attndims=1`, varlena shape; `LookupEnumByArrayOID`
    inverse) and the `feelings public.mood[]` (plus no-`feelings text[]`,
    no-`CREATE TYPE public._mood`) assertions in `TestPort_PgDumpConnectionSetup`.

Regression guard: `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`) drives real pg_dump and asserts
no `setup_connection()` error signature appears; as of slice 44 pg_dump exits
0, and as of slice 46 the test also asserts the table's archive entry **and the
full column list** (`id integer`, `name text`) are emitted; slice 48 enriched the
fixture so it also asserts the type modifiers `amount numeric(10,2)` and `code
character varying(8)` round-trip; slice 49 further asserts the PRIMARY KEY
`ADD CONSTRAINT` and the auto-named `CHECK ((qty >= 0))` constraint survive the
dump; slice 50 asserts the PK column's implicit `NOT NULL` (`id integer NOT
NULL`) survives the dump; slice 51 asserts a `FOREIGN KEY` constraint and a
`UNIQUE` constraint both round-trip (`foo_parent_id_fkey FOREIGN KEY (parent_id)
REFERENCES public.foo(id)`, `foo_code_key UNIQUE (code)`); slice 52 asserts FK
referential actions survive — the inline self-FK's `ON DELETE CASCADE` and the
ALTER-added `foo_mgr_fkey`'s `ON UPDATE CASCADE ON DELETE SET NULL`; slice 53
asserts a table-level composite PK and FK survive — `bar_pkey PRIMARY KEY (a, b)`
and `baz_x_fkey FOREIGN KEY (x, y) REFERENCES public.bar(a, b) ON DELETE
CASCADE`; slice 54 asserts a non-empty reloptions survives — `CREATE TABLE
public.opt (…) WITH (fillfactor='70')` — plus a `CREATE SCHEMA s` + `s.widget`
cross-namespace round-trip; slice 55 asserts a TABLE comment and a COLUMN
comment both round-trip — `COMMENT ON TABLE public.foo IS 'a foo table'` and
`COMMENT ON COLUMN public.foo.name IS 'the name column'`; slice 56 asserts a
plain secondary index, a partial index, and two ordered indexes all round-trip —
`CREATE INDEX foo_name_idx … (name)`, `… foo_qty_partial_idx … (qty) WHERE qty >
0`, `… foo_name_desc_idx … (name DESC NULLS LAST)`, and `… foo_ord_idx … (name
DESC, qty NULLS FIRST)`; slice 57 asserts a VIEW round-trips — `CREATE VIEW
public.foo_view AS` followed by the verbatim body `SELECT id, name FROM
public.foo WHERE qty > 0;`; slice 58 asserts a VIEW created with an explicit
column list round-trips with the renamed columns — `CREATE VIEW
public.foo_rview AS` followed by `SELECT id AS col_a, name AS col_b FROM
public.foo;`; slice 59 asserts a STORED generated column round-trips with its
generation clause — `CREATE TABLE public.gen (` and `area integer GENERATED
ALWAYS AS (w * h) STORED`; slice 60 asserts a MATERIALIZED VIEW round-trips —
`CREATE MATERIALIZED VIEW public.foo_mv AS` followed by the verbatim body
`SELECT id, name FROM public.foo WHERE qty > 0` and the `WITH NO DATA;` clause;
slice 61 asserts a RECURSIVE VIEW round-trips — `CREATE VIEW public.foo_rec AS`
followed by the synthesized wrapped-CTE body `WITH RECURSIVE foo_rec(n) AS
(SELECT 1 UNION ALL SELECT n + 1 FROM foo_rec WHERE n < 5) SELECT n FROM
foo_rec;`; slice 62 asserts array-typed columns round-trip as their array type —
`CREATE TABLE public.arr (` with `tags text[]`, `scores integer[]`, and `big
bigint[]`; slice 63 extends the `arr` fixture so a `flags boolean[]` and a
`prices numeric(10,2)[]` column also round-trip (the numeric array carrying its
element precision/scale); slice 64 extends it further with `ratios double
precision[]`, `days date[]`, and `moments timestamp without time zone[]`;
slice 65 extends it again with `speeds real[]`, `times time without time
zone[]`, and `zoned timestamp with time zone[]`; slice 66 adds a scalar `tok
uuid` and a `ids uuid[]` column; slice 67 adds a scalar `blob bytea` and a
`blobs bytea[]` column; slice 68 adds `label varchar(20)`, `labels
varchar(20)[]`, `code char(4)`, `codes char(4)[]`, and `oids oid[]` — completing
every simple scalar-OID-backed array type; slice 69 adds the JSON family `doc
json`, `docs json[]`, `jdoc jsonb`, and `jdocs jsonb[]`; slice 70 adds a scalar
`span interval` and a `spans interval[]` column; slice 71 adds the
network-address family `ip inet`, `ips inet[]`, `net cidr`, `nets cidr[]`, `mac
macaddr`, `macs macaddr[]`, `mac8 macaddr8`, and `mac8s macaddr8[]`; slice 72
adds the geometric family `pt point`, `pts point[]`, `seg lseg`, `segs lseg[]`,
`pth path`, `pths path[]`, `bx box`, `bxs box[]`, `poly polygon`, `polys
polygon[]`, `ln line`, `lns line[]`, `circ circle`, and `circs circle[]`;
slice 73 adds the full-text-search family `tsv tsvector`, `tsvs tsvector[]`,
`tsq tsquery`, and `tsqs tsquery[]`; slice 74 adds `xm xml`, `xms xml[]`, `mny
money`, and `mnys money[]`; slice 75 adds the typmod-bearing `bv bit(8)`, `bvs
bit(8)[]`, `vb varbit(16)`, and `vbs varbit(16)[]`; slice 76 adds `lsn pg_lsn`
and `lsns pg_lsn[]`; slice 77 adds the snapshot types `txs txid_snapshot`, `txss
txid_snapshot[]`, `pgs pg_snapshot`, and `pgss pg_snapshot[]`; slice 78 adds `x8
xid8` and `x8s xid8[]`; slice 79 adds the identifier types `td tid`, `tds
tid[]`, `xd xid`, `xds xid[]`, `cd cid`, and `cds cid[]`; slice 80 adds the
OID-reference (`reg*`) family `rp regproc`, `rps regproc[]`, `rpd regprocedure`,
`rpds regprocedure[]`, `ropr regoper`, `roprs regoper[]`, `roo regoperator`,
`roos regoperator[]`, `rcl regclass`, `rcls regclass[]`, `rt regtype`, `rts
regtype[]`, `rcf regconfig`, `rcfs regconfig[]`, `rdi regdictionary`, `rdis
regdictionary[]`, `rn regnamespace`, `rns regnamespace[]`, `rr regrole`, `rrs
regrole[]`, `rco regcollation`, and `rcos regcollation[]`; slice 81 adds the
legacy vector types `iv int2vector`, `ivs int2vector[]`, `ov oidvector`, and `ovs
oidvector[]` (and fixes the mis-rendering of OID 22/30 as `smallint[]`/`oid[]`);
slice 82 adds the `name` catalog identifier type `nm name` and `nms name[]` (the
scalar display/attr paths predated this slice; only the codec `name→OID` and the
`_name` array wiring were missing); slice 83 adds the `timetz` (`time with time
zone`) type `tt timetz` and `tts timetz[]` (the codec had no `timetz→OID` entry
and `formatTypeOID` had no scalar 1266 case, so it round-tripped as `text`).

Slices 88–89 introduced the first OBJECT type: a user-defined `ENUM`
(`CREATE TYPE public.mood AS ENUM (...)`) and an enum-array column (`mood[]`),
allocating the enum's auto-generated `_mood` array type so the column resolves
to a distinct OID instead of folding to `text[]`. Slice 90 adds the second
object type — a `DOMAIN` (`CREATE DOMAIN public.zipcode AS text`) and a column
of the domain type. goopg had `CREATE DOMAIN` DDL but no `pg_type` row for the
domain, so pg_dump's `getTypes` never discovered it (no `CREATE DOMAIN` emitted)
and a domain column folded to its base (`zip text`). The slice adds
`syncDomainTypeToCatalogHeap` (writes the `typtype='d'` row with
`typbasetype`/`typcollation` inherited from the base so `dumpDomain` re-renders
`CREATE DOMAIN ... AS format_type(typbasetype, typtypmod)` with no spurious
`COLLATE`), a `buildUserPGAttributeRow` branch that re-resolves a domain column
(keyed on `Column.DeclaredTypeName`, since `CREATE TABLE` stores the resolved
base) to the domain's pg_type OID while reporting the base's physical layout,
and `LookupDomain`/`LookupDomainByOID` on the `Catalog` interface so
`format_type` renders `public.zipcode`. A NULL `typdefaultbin` additionally
exposed a `pg_get_expr` bug: `pg_get_expr(NULL, …)` returned `''` (non-NULL),
so `dumpDomain` emitted a spurious empty `DEFAULT `; it now returns NULL for a
NULL node tree (empty-but-non-null still returns `''`, so partition-bound
display is unaffected). Slice 91 extends the domain to carry a `NOT NULL`
constraint (`CREATE DOMAIN public.zipcode_nn AS text NOT NULL`). pg_dump's
`dumpDomain` reads `pg_type.typnotnull` and, against a PG17+ server with no
separate named not-null constraint row (`tyinfo->notnull == NULL` — goopg emits
no `contype='n'` `pg_constraint` row for domains), appends a bare ` NOT NULL` to
the `CREATE DOMAIN`. `catalog.Domain.NotNull` and `buildUserPGTypeRowForDomain`
already carried `typnotnull` from slice 90's `pg_type` emission, so the dump
round-trips `CREATE DOMAIN public.zipcode_nn AS text NOT NULL;` with no code
change — the slice is a regression guard for the not-null fidelity path (the
bare `zipcode` domain is the complementary `typnotnull='f'` guard). Unit guards:
`executor.TestUserPGAttributeDomainColumn`,
`planner.TestSRFJoinRightProjectionOffset`, `executor.TestUserPGAttributeTypmod`,
`executor.TestForeignKeySurfacesInPgConstraint`,
`executor.TestAlterTableAddForeignKeyCapturesActions`,
`executor.TestCreateTableTableLevelCompositeForeignKey`,
`executor.TestFillfactorSurfacesInPgClassReloptions`,
`executor.TestFillfactorOutOfBoundsRejected`,
`parser.TestParseCommentOnColumn`,
`parser.TestParseCreateIndexColOrders`,
`catalog.TestBuildIndexDefColOrder`,
`config.TestPgDumpConnectionSetupGUCs`,
`parser.TestParseSetTransactionCommaSeparated`,
`parser.TestParseCreateViewRawDef`,
`parser.TestParseCreateMatViewRawDef`,
`parser.TestParseCreateRecursiveViewRawDef`,
`executor.TestApplyViewColumnAliases`.

Slice 92 extends the domain to carry a `DEFAULT` expression
(`CREATE DOMAIN public.qty AS integer DEFAULT 0`). pg_dump's `dumpDomain` issues
`pg_get_expr(t.typdefaultbin, 'pg_catalog.pg_type'::regclass) AS typdefaultbin`
and, when that is non-NULL, appends ` DEFAULT <expr>` to the `CREATE DOMAIN`
verbatim (the `typdefaultbin` branch is *not* literal-quoted; only the fallback
`typdefault` text column is). Before this slice the parser **skipped** the
`DEFAULT` clause entirely (`parseCreateDomain` consumed tokens until the next
`NOT`/`NULL`/`CHECK`/`CONSTRAINT`/`COLLATE`/`;`), so the default was silently
dropped. Now `parseCreateDomain` parses the expression with `parseExpr` (which
stops at those same keyword boundaries — verified for `DEFAULT 42 NOT NULL`,
`NOT NULL DEFAULT 7`, and `DEFAULT 'x' CHECK (...)`), stores it on
`CreateDomainStmt.Default`, and `execCreateDomain` copies it to
`catalog.Domain.Default`. `Domain.DefaultBin()` renders it via the shared
`formatExprForAttrdef` (the same deparser used for `pg_attrdef.adbin`), and
`buildUserPGTypeRowForDomain` emits that string as `typdefaultbin` (NULL when
there is no default). goopg's `pg_get_expr` is a pass-through, so the rendered
string is exactly what the dump re-emits. The fixture uses an **integer** base:
an integer constant deparses identically in goopg (`0`) and real PG 18.3
(verified via a throwaway cluster: `DEFAULT 0`), whereas a text default gains a
`::text` cast in PG (`'foo'::text`) that `formatExprForAttrdef` does not
synthesize — matching that cast lands in slice 93.

Slice 93 closes that gap: a text DOMAIN with a **string-literal** default
(`CREATE DOMAIN public.label AS text DEFAULT 'n/a'`) round-trips as
`CREATE DOMAIN public.label AS text DEFAULT 'n/a'::text;`. PG's `get_const_expr`
appends a `::type` decoration to every `Const` whose type is not self-evident
from its literal form — `int4`, `numeric`, and `bool` print bare, but `text`,
`varchar`, and the like carry the cast. `Domain.DefaultBin()` now mirrors this:
after deparsing via `formatExprForAttrdef`, it appends `::<base-type-name>` when
the default is a `*parser.StringConst` (so `'n/a'` over `text` → `'n/a'::text`).
The cast is scoped to the domain default path only — `formatExprForAttrdef`
itself is unchanged, so `pg_attrdef.adbin` column-default rendering is untouched.
Integer defaults (slice 92) stay bare because an `*IntegerConst` is not a
`StringConst`. The slice-90 empty-DEFAULT negative guard was retargeted from the
now-legitimate substring `AS text DEFAULT` to the precise empty-clause forms
`DEFAULT;` / `DEFAULT \n` (a real default always carries an expression between
`DEFAULT` and the terminator), so it still protects the `pg_get_expr(NULL)`
regression for the no-default `zipcode`/`zipcode_nn` domains while admitting the
new text default.

Slice 94 extends the string-default decoration to a **multi-word** base type:
a DOMAIN over `varchar` (`CREATE DOMAIN public.vcdef AS varchar DEFAULT 'na'`)
round-trips as `CREATE DOMAIN public.vcdef AS character varying DEFAULT
'na'::character varying;`. Slice 93 appended the bare `d.Base.Name` (`text`), but
the user-typed `varchar`/`char` aliases differ from the canonical spelling
`get_const_expr` uses, which is `format_type(consttype, -1)` — `character
varying` for varchar, and `bpchar` (the internal name, *not* `character`) for
char/bpchar, verified against real pg_dump 18.3 (`DEFAULT 'ab'::bpchar`). A new
`domainConstCastTypeName` helper maps the base name to that spelling (falling
through unchanged for `text`, `uuid`, … whose alias already equals the
format_type name), and `Domain.DefaultBin()` routes the cast suffix through it.
The fixture uses a **bare** `varchar` (no length) deliberately: the CREATE DOMAIN
parser still discards the `(n)` type modifier (`stmt.BaseType` keeps only the
name), so a `varchar(20)` domain would lose its `(20)` in both the base-type
render and the cast — base-type typmod capture for domains is a separate,
larger gap left for a future slice.

Slice 95 closes that gap: a DOMAIN whose base type carries a **typmod**
(`varchar(20)`, `char(4)`, `numeric(10,2)`) now round-trips the declared
length/precision. `dumpDomain` renders the base via
`format_type(typbasetype, typtypmod)`, so a discarded modifier dumped
`varchar(20)` as bare `character varying`. `parseCreateDomain` previously
*skipped* the `(n)` argument list; it now captures it into the new
`CreateDomainStmt.BaseTypeArgs`, and `execCreateDomain` threads it onto
`catalog.Domain.Base.Args`, so `pgAttTypmod` computes the canonical
`typtypmod` and `formatTypeOID` renders `character varying(20)` /
`character(4)` / `numeric(10,2)`. The cast `get_const_expr` appends to a
string DEFAULT stays **typmod-less** — it is `format_type(consttype, -1)`, so
`varchar(20)` → `::character varying` and `char(4)` → `::bpchar` (the internal
name) — which `domainConstCastTypeName` already produced, so no cast change was
needed (verified against real pg_dump 18.3:
`CREATE DOMAIN public.ch4 AS character(4) DEFAULT 'ab'::bpchar;`). The numeric
default deparses bare (`DEFAULT 1.5`, a self-evident numeric Const). The bare
`bpchar`/`char` base *name* without a typmod remains a known divergence —
`formatTypeOID(1042, -1)` returns `character` where real PG returns `bpchar` —
but it is unreachable here because every typmod-bearing case carries an explicit
length; bare-name base render is part of the broader format_type fidelity work.

Slice 96 restores a **domain CHECK constraint**. `dumpDomain` appends every
domain check inline as `\n\tCONSTRAINT <name> <pg_get_constraintdef(oid)>`, and
those checks are collected by `getDomainConstraints`
(`SELECT … pg_get_constraintdef(oid) AS consrc, convalidated, contype FROM
pg_constraint WHERE contypid = $1 AND contype IN ('c','n') ORDER BY conname`).
goopg previously *discarded* a generic domain CHECK — `parseCreateDomain` only
captured the `CHECK (VALUE IN (...))` form into `CheckInValues` (never rendered),
and `pg_constraint` emitted no `contype='c'` row keyed on `contypid`, so a
`CHECK (VALUE > 0)` silently vanished from the dump. The fix: (1) the parser
captures the raw predicate into `CreateDomainStmt.CheckExpr`/`CheckName` via a new
`parseDomainCheckExpr` (the table-check twin `parseCheckExpr`, but it renders the
value placeholder as uppercase `VALUE` to match PG's ruleutils deparse — a TABLE
check must NOT do this, since `value` may be a real column there); (2)
`catalog.InMemory.SetDomainCheck` records the predicate and allocates a stable
constraint OID (`Domain.CheckOID`, auto-naming `<domain>_check` when the user gave
none); (3) the `pg_constraint` virtual view emits the `contype='c'` row with
`contypid` = the domain OID (and `conrelid`=0); (4) `pg_get_constraintdef` renders
`CHECK ((<expr>))`, double-wrapping the predicate exactly as PG does. Verified
byte-identical to real pg_dump 18.3:
`CREATE DOMAIN public.posqty AS integer\n\tCONSTRAINT posqty_check CHECK ((VALUE > 0));`
and the explicitly-named `CONSTRAINT must_be_pos CHECK ((VALUE > 0))`. The token
reconstruction normalizes spacing to PG's canonical form (`VALUE>0` → `VALUE > 0`).
The `CHECK (VALUE IN (...))` form — which PG deparses to a
`VALUE = ANY (ARRAY['a'::text, …])` ScalarArrayOpExpr, not the literal `IN` text —
lands in slice 97 below.

Slice 97 closes the `CHECK (VALUE IN (...))` gap for **text** domains. goopg has
long captured the membership list in `CreateDomainStmt.CheckInValues` (the executor
keeps it on `Domain.CheckInValues` for runtime IN validation), but it emitted no
`pg_constraint` row, so the check vanished from `pg_dump`. PG does not preserve the
`IN` syntax — `ruleutils` deparses a domain `CHECK (VALUE IN ('red','green'))` to a
`ScalarArrayOpExpr`: `VALUE = ANY (ARRAY['red'::text, 'green'::text])`. The executor
now synthesizes that exact text in `domainInValuesCheckExpr` (single-quoted,
embedded-quote-doubled literals, each cast `::text`) and feeds it through the
slice-96 plumbing (`SetDomainCheck` → `pg_constraint` `contype='c'` row →
`pg_get_constraintdef` double-wrap), yielding
`CONSTRAINT colr_check CHECK ((VALUE = ANY (ARRAY['red'::text, 'green'::text])))` —
byte-identical to real pg_dump 18.3, for both the auto-named and explicit-`CONSTRAINT`
cases (the parser now also threads the explicit name into `CheckName` for the
IN-values branch).

Slice 98 extends the IN-values deparse to the two other string base types,
`char(n)`/`bpchar` and `character varying`. `domainInValuesCheckExpr` is now
OID-driven (`catalog.TypeNameToOID(baseType)` resolves the alias the parser stored)
rather than string-matching `"text"`, so it covers all three string families with
the deparse PG actually emits (verified against real pg_dump 18.3, `/tmp/pgcheck_du98`):

| base type | deparse |
|---|---|
| `text` | `VALUE = ANY (ARRAY['red'::text, 'green'::text])` |
| `char(n)`/`bpchar` | `VALUE = ANY (ARRAY['a'::bpchar, 'b'::bpchar])` |
| `character varying` | `(VALUE)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[])` |

`text` and `bpchar` have native equality operators, so PG emits a bare per-element
cast with no coercion wrapper. `character varying` has no varchar-eq operator and
reuses `text`'s, so PG coerces both sides to `text` — the `(VALUE)::text` left side
and the `(ARRAY[…])::text[]` right side. The per-element cast always uses the base
type's bare name with **no typmod**, even for `varchar(20)`/`char(4)`.

Slice 99 extends the IN-values deparse to the numeric-family base types
`integer` and `numeric`. Two changes were needed beyond the deparse branch:
the CREATE DOMAIN parser (`tryParseCheckInValues`) previously accepted only
**string** literals in the IN-list, so a numeric list like `IN (1, 2, 3)`
silently failed the pattern match and fell through to `skipParenExpr` —
producing **no constraint at all** (neither runtime validation nor dump). The
parser now also accepts `TokenIntLit`/`TokenNumericLit` and stores the raw
token text; the runtime membership check in `expr.go` already compares via the
value's string form, so it works unchanged. The deparse (verified against real
pg_dump 18.3, `/tmp/pgcheck_du99`):

| base type | deparse |
|---|---|
| `integer` | `VALUE = ANY (ARRAY[1, 2, 3])` |
| `numeric(10,2)` | `VALUE = ANY (ARRAY[1.5, 2.5])` |

Integer/numeric literals already share the base type, so PG emits each element
verbatim — no quotes and no per-element cast.

Slice 100 extends the deparse to three more base types. The parser additionally
accepts the boolean keyword literals `true`/`false` in the IN-list (stored as
their canonical lowercase form); date/string literals were already accepted.
Verified against real pg_dump 18.3 (`/tmp/pgcheck_du100`):

| base type | deparse |
|---|---|
| `bigint` | `VALUE = ANY (ARRAY[(100)::bigint, (200)::bigint, (300)::bigint])` |
| `boolean` | `VALUE = ANY (ARRAY[true, false])` |
| `date` | `VALUE = ANY (ARRAY['2020-01-01'::date, '2021-06-15'::date])` |

`bigint` differs from `integer`: the IN-list literals parse as `int4` constants
and PG coerces each per element, so every element is wrapped `(N)::bigint`.
`boolean` is the verbatim shape (the keyword literals already have type `bool`).
`date` mirrors the string-with-cast shape — a quoted literal plus a bare `::date`
element cast, exactly like `text`/`bpchar` with a different cast type. Other
non-listed base types still return `""` and stay runtime-only.

Slice 101 extends the deparse to five more base types. No parser change was
needed — `real`/`float8` IN-lists are numeric literals and `timestamp`/`time`/
`uuid` IN-lists are string literals, both already accepted. Verified against real
pg_dump 18.3 (`/tmp/pgcheck_du101`):

| base type | deparse |
|---|---|
| `real` | `VALUE = ANY (ARRAY[(1.5)::real, (2.5)::real])` |
| `double precision` | `VALUE = ANY (ARRAY[(1.5)::double precision, (2.5)::double precision, (3.0)::double precision])` |
| `timestamp without time zone` | `VALUE = ANY (ARRAY['2020-01-01 00:00:00'::timestamp without time zone, …])` |
| `time without time zone` | `VALUE = ANY (ARRAY['12:00:00'::time without time zone, …])` |
| `uuid` | `VALUE = ANY (ARRAY['a0ee…'::uuid, 'b0ee…'::uuid])` |

`real`/`double precision` join `bigint` in the per-element coercion shape (the
shared `domainInValuesCoerced` helper wraps each numeric literal `(N)::<type>`,
because the IN-list literals parse as a narrower `int4`/`numeric` type than the
base). `timestamp`/`time`/`uuid` join `date`/`text`/`bpchar` in the
string-with-cast shape, each using its canonical (possibly multi-word) base-type
cast name. The fixtures declare the base via the single-word aliases
`real`/`float8`/`timestamp`/`time`/`uuid` so the CREATE DOMAIN object-name parser
accepts them, but `format_type` re-renders the canonical multi-word name from the
OID on dump. `timestamptz` is deliberately excluded: PG re-renders the stored
constant in the session timezone (`'…+00'` → `'…+09'`), so a verbatim deparse
from the raw token text would not be byte-identical.

Slice 102 extends the deparse to three more base types. No parser change was
needed — `smallint` IN-lists are integer literals and `bytea`/`inet` IN-lists are
string literals, both already accepted. Verified against real pg_dump 18.3
(`/tmp/pgcheck_du102`):

| base type | deparse |
|---|---|
| `smallint` | `VALUE = ANY (ARRAY[10, 20, 30])` |
| `bytea` | `VALUE = ANY (ARRAY['\xdeadbeef'::bytea, '\xcafe'::bytea])` |
| `inet` | `VALUE = ANY (ARRAY['192.168.0.1'::inet, '10.0.0.0/8'::inet])` |

`smallint` joins `integer`/`numeric`/`boolean` in the verbatim branch — small
integer constants const-fold to `int2` with no cast wrapper. `bytea`/`inet` join
the string-with-cast shape; their canonical input forms (`\x` hex for bytea,
dotted-quad / CIDR for inet) round-trip verbatim. `interval` is deliberately
excluded: PG normalizes the stored value (e.g. `'2 hours'` → `'02:00:00'`), so a
verbatim deparse from the raw token text would not be byte-identical. `json` is
deferred separately — it has no equality operator, so the CHECK must be written
`VALUE::text IN (...)`, a different parse shape than `VALUE IN (...)`.

Slice 103 extends the deparse to the MAC / network-address family that `inet`
began. No parser change was needed — all three are string literals. Verified
against real pg_dump 18.3 (`/tmp/pgcheck_du103`):

| base type | deparse |
|---|---|
| `macaddr` | `VALUE = ANY (ARRAY['08:00:2b:01:02:03'::macaddr, '00:11:22:33:44:55'::macaddr])` |
| `macaddr8` | `VALUE = ANY (ARRAY['08:00:2b:01:02:03:04:05'::macaddr8, '00:11:22:33:44:55:66:77'::macaddr8])` |
| `cidr` | `(VALUE)::inet = ANY ((ARRAY['192.168.0.0/24'::cidr, '10.0.0.0/8'::cidr])::inet[])` |

`macaddr`/`macaddr8` join the bare string-with-cast shape; their canonical
colon-separated forms round-trip verbatim. `cidr` is special: it has no
`cidr`-eq operator and reuses `inet`'s, so PG coerces **both sides to `inet`** —
the per-element cast stays `::cidr` but the comparison is wrapped
`(VALUE)::inet = ANY ((ARRAY[...])::inet[])`. This is the same coercion-envelope
mechanism `varchar`→`text` uses (slice 98); the implementation generalised the
old `coerceToText bool` flag into a `coerceTo string` target type so both
`varchar`→`text` and `cidr`→`inet` share one code path.

Slice 104 extends the deparse to two more base types with native equality
operators. No parser change was needed — both are string literals. Verified
against real pg_dump 18.3 (`/tmp/pgcheck_du104`):

| base type | deparse |
|---|---|
| `name` | `VALUE = ANY (ARRAY['alice'::name, 'bob'::name])` |
| `jsonb` | `VALUE = ANY (ARRAY['1'::jsonb, '"hello"'::jsonb])` |

`name`/`jsonb` join the bare string-with-cast shape (`castType` = `name` / `jsonb`,
no coercion envelope). `name` is a plain string and round-trips verbatim. `jsonb`
has its own equality operator, so a bare `VALUE IN (...)` works — but byte-identity
holds **only for already-canonical jsonb values**: scalars (`1`, `"hello"`) print
identically, whereas non-scalar jsonb (objects) is re-rendered with key reordering
and whitespace normalization. The fixtures therefore use canonical scalars.
Deliberately still excluded: `timestamptz` (session-tz re-render), `interval`
(normalized form), `money` (`lc_monetary`-dependent). `json` is handled in slice 105.

### DU-002 slice 105 — `json` `CHECK (VALUE::text IN (...))`

Slice 105 closes the long-deferred `json` case. `json` has **no** equality
operator, so a bare `VALUE IN (...)` is invalid; the domain must cast the
left-hand side, `CHECK (VALUE::text IN (...))`. This required the first
parser change since slice 99: `tryParseCheckInValues` now accepts an optional
`::<typename>` cast immediately after `VALUE` (consumed via
`parseTypeNameAfterCast`, the cast type itself discarded — the deparse shape is
decided from the domain's base type). Verified against real pg_dump 18.3
(`/tmp/pgcheck_du105`):

| base type | deparse |
|---|---|
| `json` | `(VALUE)::text = ANY (ARRAY['1'::text, '{"a": 1}'::text])` |

This is a new deparse mode — `lhsCast` in `domainInValuesCheckExpr`: the LHS is
cast (`(VALUE)::text`) but the array is **not** re-cast (unlike the `coerceTo`
envelope used by `varchar`/`cidr`), because each IN-list literal is an untyped
string constant already typed as the cast target `text`. Notably `json`
round-trips **byte-identically even for object/array values** (`'{"a": 1}'`) —
unlike `jsonb` (slice 104), `json` preserves the input text verbatim with no key
reordering or whitespace normalization, so the fixture uses an object value to
demonstrate this. Still excluded: `timestamptz`, `interval`, `money` (as above).

### DU-002 slice 106 — `xml` / `oid` / `bit` / `varbit`

Slice 106 extends the IN-values deparse to four more base types, exercising all
three render modes. Verified against real pg_dump 18.3 (`/tmp/pgcheck_du106`):

| base type | deparse |
|---|---|
| `xml` | `(VALUE)::text = ANY (ARRAY['<a/>'::text, '<b>1</b>'::text])` |
| `oid` | `VALUE = ANY (ARRAY[(1)::oid, (2)::oid, (3)::oid])` |
| `bit(4)` | `VALUE = ANY (ARRAY['1010'::"bit", '0101'::"bit"])` |
| `varbit` | `VALUE = ANY (ARRAY['101'::bit varying, '110'::bit varying])` |

`xml`, like `json`, has **no** equality operator, so its CHECK casts the LHS
(`CHECK (VALUE::text IN (...))`) and reuses the `lhsCast` mode added in slice 105
— demonstrating that mode generalizes beyond `json`. `xml` is stored and
re-emitted verbatim, so the text round-trips byte-identically. `oid` joins the
per-element coercion shape (`domainInValuesCoerced` → `(N)::oid`), like
`bigint`/`real`: the IN-list `int4` literals are coerced per element. `bit`/`varbit`
have native equality operators and use the bare string-with-cast shape; the only
nuance is that the deparser **quotes** `bit`'s cast type (`::"bit"`) because `bit`
is a non-standard type-name token, whereas `varbit` renders as `bit varying`. The
domain AS-clause typmod (`bit(4)` / `bit varying`) was already handled by the
slice-75 `format_type` work. Still excluded: `timestamptz`, `interval`, `money`
(as above).

### DU-002 slice 107 — `pg_lsn` / `tid` / `xid` / `cid`

Slice 107 extends the IN-values deparse to four system-ish base types that all
share the simplest render mode. Verified against real pg_dump 18.3
(`/tmp/pgcheck_du107`):

| base type | deparse |
|---|---|
| `pg_lsn` | `VALUE = ANY (ARRAY['16/B374D848'::pg_lsn, '0/0'::pg_lsn])` |
| `tid` | `VALUE = ANY (ARRAY['(0,1)'::tid, '(1,2)'::tid])` |
| `xid` | `VALUE = ANY (ARRAY['100'::xid, '200'::xid])` |
| `cid` | `VALUE = ANY (ARRAY['5'::cid, '10'::cid])` |

All four have native equality operators and canonical input forms that round-trip
verbatim, so each joins the bare string-with-cast shape with no coercion envelope
and no quoted cast (unlike `bit`). `pg_lsn`'s uppercase-hex LSN, `tid`'s
`(block,offset)` pair, and the decimal `xid`/`cid` forms are all stored and
re-emitted byte-for-byte.

Two related types were probed and **deliberately excluded**: `tsvector` and
`tsquery` re-render their lexemes with single quotes (`'a b'` → `'''a'' ''b'''`),
so they normalize like `timestamptz`/`interval`. The internal `"char"` type
(OID 18) is also excluded: `catalog.TypeNameToOID` maps the string `"char"` to
`bpchar` (OID 1042), so the quoted-vs-unquoted distinction required to emit
`::"char"` is not tracked — distinguishing it would need parser quote-state, out
of scope for this slice. Still excluded base types: `timestamptz`, `interval`,
`money`, `tsvector`, `tsquery`, `"char"`.

### DU-002 slice 108 — `interval` / `money`

Slice 108 promotes `interval` and `money` out of the excluded set. Both have
native equality operators and use the bare string-with-cast shape, so they were
not blocked by a coercion envelope — the only reason they were previously excluded
is output normalization. Verified against real pg_dump 18.3 (`/tmp/pgcheck_du108`):

| base type | deparse |
|---|---|
| `interval` | `VALUE = ANY (ARRAY['1 day'::interval, '02:00:00'::interval, '1 year 2 mons'::interval])` |
| `money` | `VALUE = ANY (ARRAY['$1.00'::money, '$2.50'::money])` |

The catch is identical to the jsonb-scalar contract: byte identity holds **only for
already-canonical inputs**. `interval`'s output function normalizes (`'2 hours'` →
`'02:00:00'`), and `money`'s output depends on `lc_monetary` (the default C/POSIX
locale yields `'$1.00'`). The fixtures therefore use values that are already in the
type's canonical output form, so the stored→deparse round-trip in real PG produces
the same literal goopg echoes. A non-canonical input (`'2 hours'`) or a non-C
`lc_monetary` would diverge, so the slice is scoped to canonical fixtures and the
contract is documented in `domainInValuesCheckExpr`.

Still excluded base types after slice 108: `timestamptz` (re-renders the stored
constant in the session timezone), `tsvector`/`tsquery` (re-render lexemes
single-quoted, `'cat'` → `'''cat'''`), and the internal `"char"` (OID 18,
quote-state not tracked).

### Slice 109 — domain over a user-defined ENUM base type

The IN-values deparse work so far covered only built-in base types. Slice 109
moves to a **user-defined enum base type**: `CREATE DOMAIN public.enum_in AS
public.mood CHECK (VALUE IN ('sad','happy'))`. Enums have a native equality
operator, so PG emits the familiar bare string-with-cast shape — but the cast is
**schema-qualified** (pg_dump empties `search_path`, so every user type is
qualified). Verified against real pg_dump 18.3 (`/tmp/pgcheck_du109`):

```
CREATE DOMAIN public.enum_in AS public.mood
	CONSTRAINT enum_in_check CHECK ((VALUE = ANY (ARRAY['sad'::public.mood, 'happy'::public.mood])));
```

Two distinct blockers had to be cleared (the first was the surprise):

1. **`typbasetype` resolved to `text`.** `buildUserPGTypeRowForDomain` derived the
   base OID via `catalog.TypeNameToOID(d.Base.Name)`, which returns `OIDText` as a
   *safe fallback* for any unrecognized name — including an enum's. So the domain's
   `pg_type` row carried `typbasetype = text` and `dumpDomain` rendered `AS text`.
   Fixed by recording the resolved base OID on `catalog.Domain` at CREATE time
   (new `BaseOID`/`BaseIsEnum` fields): `execCreateDomain` looks the enum up
   (`enumForDomainBaseType`, stripping any schema prefix) and stores `et.OID`.
   `buildUserPGTypeRowForDomain` now prefers `d.BaseOID` and, when the base is an
   enum, inherits the enum's physical layout (4-byte, int-aligned, plain storage,
   `'E'` category) instead of the text fallback — mirroring the enum-*column* path
   in `buildUserPGAttributeRow`. `format_type(typbasetype)` already resolves an
   enum OID to `public.<name>` via `LookupEnumByOID` (slice 88), so the
   `AS public.mood` render falls out for free once `typbasetype` is correct.

2. **The CHECK cast rendered as `::text`.** Same root cause inside
   `domainInValuesCheckExpr`: the `TypeNameToOID` fallback steered an enum name
   into the `OIDText` switch case (`'sad'::text`). Fixed by detecting the enum
   **before** the switch and emitting `'sad'::public.<enum>` directly. Enum labels
   round-trip verbatim (no output normalization), so the result is byte-identical.

The domain *column* reference (`eni public.enum_in`) already round-tripped
correctly before this slice — a column resolves to the domain by name regardless
of the base type — so only the domain definition and its constraint needed the
fix. Still excluded base types after slice 109 are unchanged from slice 108
(`timestamptz`, `tsvector`/`tsquery`, internal `"char"`).

### Slice 110 — `timestamp with time zone`

Slice 110 retires the first of the slice-108 excluded base types: a domain over
`timestamptz`. `timestamp with time zone` has a native equality operator, so PG
emits the bare string-with-cast shape — the same one `timestamp` (slice 95) uses,
except the cast names the verbose `timestamp with time zone` form. Verified
against real pg_dump 18.3 run under a **UTC session** (`PGTZ=UTC`, `SET timezone
= 'UTC'`):

```
CREATE DOMAIN public.tstz_in AS timestamp with time zone
	CONSTRAINT tstz_in_check CHECK ((VALUE = ANY (ARRAY['2020-01-01 00:00:00+00'::timestamp with time zone, '2021-06-15 12:30:00+00'::timestamp with time zone])));
```

The reason `timestamptz` was excluded is the **session-TimeZone re-render**: PG's
`timestamptztypoutput` renders the stored instant in the connection's `TimeZone`
GUC, so the *same* domain dumped under `Asia/Tokyo` emits `'2020-01-01
09:00:00+09'` instead. Byte-identity therefore holds only when the IN-list
literal is already in the session-TZ canonical form. The fixture pins the UTC
(`+00`) canonical form and the real-pg_dump oracle was run under a UTC session, so
the literals round-trip verbatim.

Crucially, goopg's deparse is **TZ-independent**: `domainInValuesCheckExpr` emits
the IN-list literals exactly as they were stored in `CheckInValues` (no output
function, no TZ conversion), so the only requirement is that the fixture supply
already-canonical literals. The single engine change is one switch arm —
`case catalog.OIDTimestampTZ: castType = "timestamp with time zone"`. Everything
else (`TypeNameToOID("timestamptz")` → `OIDTimestampTZ` (1184),
`userTypeAttrsForOID`/`pgTypeCategoryForOID` for 1184, `format_type(1184)` →
`"timestamp with time zone"`) was already in place from the timestamptz-column
work. Excluded base types remaining after slice 110: `tsvector`/`tsquery` (output
functions normalize and quote lexemes) and internal `"char"` (quoted-identifier
disambiguation from `bpchar` is lost in the parser).

### Slice 111 — `time with time zone`

Slice 111 adds a domain over `timetz`:

```
CREATE DOMAIN public.ttz_in AS time with time zone
	CONSTRAINT ttz_in_check CHECK ((VALUE = ANY (ARRAY['12:30:00+09'::time with time zone, '23:59:59-05'::time with time zone])));
```

`time with time zone` has a native equality operator, so PG emits the bare
string-with-cast shape (identical structure to slice 110's `timestamptz`). The
key difference — and why timetz is **lower-risk than timestamptz** — is that
`timetz_out` *preserves the stored zone offset verbatim*: unlike
`timestamptztypoutput`, it does NOT rotate the value into the connection's
`TimeZone` GUC. Verified against real pg_dump 18.3: the same domain dumped under
`Asia/Tokyo` still emits `'12:30:00+09'`/`'23:59:59-05'`, so byte-identity holds
*unconditionally* for already-canonical literals (no UTC-session requirement).
The canonical `timetz_out` form is `HH:MM:SS±HH[:MM[:SS]]`; the fixture literals
`'12:30:00+09'` and `'23:59:59-05'` are already canonical (confirmed by feeding
them through `'…'::timetz` and `pg_get_constraintdef` on real PG).

The single engine change is one switch arm — `case catalog.OIDTimeTZ: castType =
"time with time zone"`. All other plumbing was already present from the
timetz-column work (slice 83): `TypeNameToOID("timetz"/"time with time zone")` →
`OIDTimeTZ` (1266), `userTypeAttrsForOID(1266)`, `format_type(1266)` → `"time
with time zone"`. (`pgTypeCategoryForOID` falls to the `'U'` default for 1266 as
it does for 1184; pg_dump does not emit typcategory into `CREATE DOMAIN`, so this
does not affect byte-identity.) Excluded base types remaining after slice 111 are
unchanged: `tsvector`/`tsquery` and internal `"char"`.

### Slice 112 — `xid8`

Slice 112 adds a domain over `xid8` (the full 64-bit transaction id):

```
CREATE DOMAIN public.x8_in AS xid8
	CONSTRAINT x8_in_check CHECK ((VALUE = ANY (ARRAY['100'::xid8, '200'::xid8])));
```

`xid8` has a native equality operator, so PG emits the **simplest render mode** —
the bare string-with-cast shape, no coercion envelope, no quoted cast — exactly
like `xid`/`cid` (slice 107). Its decimal input form round-trips verbatim through
`::xid8` (the output function prints the stored 64-bit value as decimal with no
normalization), so byte-identity is unconditional. Verified against real pg_dump
18.3 (`/tmp/pgcheck_du112`): `pg_get_constraintdef` emits the literals above.

The single engine change is one switch arm — `case catalog.OIDXid8: castType =
"xid8"`. All other plumbing was already present from the xid8-column work
(M0097-0018): `TypeNameToOID("xid8")` → `OIDXid8` (5069),
`userTypeAttrsForOID(5069)` (typlen 8 / typbyval / `'d'` align / `'p'` storage),
`format_type(5069)` → `"xid8"`. Excluded base types remaining after slice 112 are
unchanged: `tsvector`/`tsquery` (output functions normalize+requote lexemes) and
internal `"char"` (quoted-ident disambiguation from `bpchar` lost in the parser).

### Slice 113 — domains over `int2vector` / `oidvector`

```sql
CREATE DOMAIN public.i2v_in AS int2vector
	CONSTRAINT i2v_in_check CHECK ((VALUE = ANY (ARRAY['1 2'::int2vector, '3 4'::int2vector])));
CREATE DOMAIN public.ovec_in AS oidvector
	CONSTRAINT ovec_in_check CHECK ((VALUE = ANY (ARRAY['1 2'::oidvector, '3 4'::oidvector])));
```

The two legacy vector types — `int2vector` (space-separated int2 list, e.g.
`pg_index.indkey`) and `oidvector` (space-separated oid list, e.g.
`pg_proc.proargtypes`) — each have a native equality operator (`int2vectoreq` /
`oidvectoreq`), so PG emits the **simplest render mode**: the bare
string-with-cast shape, no coercion envelope. Their output functions print the
canonical space-separated form, so an already-canonical input (`'1 2'`)
round-trips verbatim through `::int2vector` / `::oidvector`. Verified against real
pg_dump 18.3: `pg_get_constraintdef` emits the literals above.

The engine change is two switch arms — `case catalog.OIDInt2vector: castType =
"int2vector"` and `case catalog.OIDOidvector: castType = "oidvector"`. All other
plumbing was already present from the vector-column work (DU-002 slice 81):
`TypeNameToOID("int2vector")` → `OIDInt2vector` (22) / `"oidvector"` →
`OIDOidvector` (30), `userTypeAttrsForOID` and `format_type` both render the bare
names. Excluded base types remaining after slice 113 are unchanged:
`tsvector`/`tsquery` (output functions normalize+requote lexemes) and internal
`"char"` (quoted-ident disambiguation from `bpchar` lost in the parser).

### Slice 114 — domains over `tsvector` / `tsquery`

```sql
CREATE DOMAIN public.tsv_in AS tsvector
	CONSTRAINT tsv_in_check CHECK ((VALUE = ANY (ARRAY['''a'' ''b'''::tsvector, '''cat'' ''dog'''::tsvector])));
CREATE DOMAIN public.tsq_in AS tsquery
	CONSTRAINT tsq_in_check CHECK ((VALUE = ANY (ARRAY['''a'' & ''b'''::tsquery, '''cat'' | ''dog'''::tsquery])));
```

The two full-text-search types — `tsvector` (a lexeme set) and `tsquery` (a
lexeme boolean expression) — each have a native equality operator (`tsvector_eq`
/ `tsquery_eq`), so PG emits the **bare string-with-cast** shape, no coercion
envelope. They were excluded through slice 113 only because their output
functions normalize the value: `tsvector_out` single-quotes each lexeme, sorts,
deduplicates, and strips absent positions; `tsquery_out` normalizes operator
spacing and single-quotes lexemes. Byte-identity therefore holds **only for
already-canonical inputs** — the exact same canonical-only contract as jsonb
scalars (slice 104), interval / money (slice 108), and timestamptz (slice 110).
The fixtures pin canonical values: the SQL literal `'''a'' ''b'''` carries the
tsvector value `'a' 'b'` (a two-lexeme set, the doubled single quotes being SQL
escaping of the lexemes' own quotes), and `'''a'' & ''b'''` the tsquery `'a' &
'b'`. goopg stores the IN-list literals verbatim and re-quotes them for SQL
output (no output function, no normalization), so its deparse matches the
already-canonical oracle. Verified against real pg_dump 18.3:
`pg_get_constraintdef` emits the literals above (`/tmp/pgcheck_du114`).

The engine change is two switch arms — `case catalog.OIDTsvector: castType =
"tsvector"` and `case catalog.OIDTsquery: castType = "tsquery"`. All other
plumbing was already present from FTS-type column work: `TypeNameToOID` →
`OIDTsvector` (3614) / `OIDTsquery` (3615), `userTypeAttrsForOID` (typlen -1,
typbyval f, typalign 'i') and `format_type` both render the bare names. **The
EASY base-type track is now exhausted** — the only base type left is internal
`"char"` (needs parser quote-state to disambiguate `::"char"` from `bpchar`);
range / composite (`CREATE TYPE AS`) base-type domains need full catalog support
for those type families (no `OIDInt4Range`, no `int4range`/`CREATE TYPE AS` in
`TypeNameToOID`) — a structural multi-loop task.

### Slice 115 — sequence-dump downstream links (`pg_sequence` + `pg_get_sequence_data`)

Slice 115 **pivots off** the now-exhausted domain-`IN`-values sub-track (slices
98–114) to a new pg_dump object surface: **sequences**. pg_dump's `getSequences`
runs an implicit-LATERAL comma join:

```sql
SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement,
       seqmax, seqmin, seqcache, seqcycle, last_value, is_called
FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid)
ORDER BY seqrelid
```

Both sides were stubs (DU-002 slice 32 added the empty `pg_sequence` view and a
0-row `pg_get_sequence_data` SRF so the query merely parsed/planned). This slice
makes the **two downstream links real**:

1. **`pg_sequence` (singular, OID 2224)** — `VirtualRows` now emits one row per
   `IsSequence` catalog table. `seqrelid` is the sequence's own pg_class OID
   (read straight off the catalog `Table`, the same way the pg_class builder
   iterates `c.tables`); the parameters (`seqtypid` 21/23/20 for
   smallint/integer/bigint, `seqstart`, `seqincrement`, `seqmax`, `seqmin`,
   `seqcache`=1, `seqcycle`) come from the executor's sequence registry via a new
   `catalog.SeqParams` struct + `catalog.SequenceParamsFunc` hook. The hook
   mirrors the existing `VirtualSpecLockRowsFunc` seam: the catalog declares the
   var, the executor sets it in an `init()` (`sequenceParamsForCatalog` →
   `LookupSequence`). OID resolution stays in the catalog; parameters stay in the
   executor (their single source of truth) — no catalog→executor import.

2. **`pg_get_sequence_data(regclass)`** — promoted from a 0-row stub to a real
   SRF. It evaluates its regclass argument with `evalExprSlot` (the correlated
   lateral case binds the outer `pg_sequence` row via `BindLateralOuter`, exactly
   like `verify_heapam`; a constant argument resolves under a nil outer slot),
   resolves it through `verifyHeapamResolveTable`, and projects the sequence's
   existing `VirtualRows` tuple `[last_value, log_cnt, is_called]` down to
   `(last_value int8, is_called bool)` — reusing the same runtime state
   `SELECT * FROM <seq>` reads, so there is one source of truth for the value.

**Deliberately out of scope (→ slice 116):** surfacing the sequence in pg_class
with `relkind='S'`. pg_dump's `getTables` only discovers a sequence when pg_class
lists it; until then these two links are inert. Flipping pg_class *now* would let
pg_dump find a sequence whose `pg_sequence`/SRF data we had not yet wired and
ERROR — so the order is: links first (this slice, regression-free), discovery
second (slice 116, where the e2e fixture gains a sequence and asserts a
byte-identical `CREATE SEQUENCE` + `setval`). Because the e2e
`TestPort_PgDumpConnectionSetup` fixture creates no sequence, `pg_sequence` stays
empty there and the dump is unchanged (verified PASS). Direct coverage is
`executor.TestPgGetSequenceDataPopulated`: `CREATE SEQUENCE` → one `pg_sequence`
row with the declared params, the full `getSequences`-join shape yielding
`last_value`=start / `is_called`=false, a direct `'s'::regclass` call, and the
post-`nextval` flip to `is_called`=true.

### Slice 116 — surface sequences in `pg_class` (`relkind='S'`, `relam=0`)

Slice 116 is the **keystone** that makes pg_dump *discover* a sequence and dump it.
pg_dump's `getTables` selects `relkind IN ('r','S','v','c','m','f','p')`; until now
goopg's `pg_class` `VirtualRows` builder skipped every system virtual table
(`Virtual && View==nil && !IsMatView`), and user sequences — which are also virtual
tables (`IsSequence`, OID = the sequence's pg_class OID) — were swept up in that skip.
So `getTables` never saw a `relkind='S'` relation and no `CREATE SEQUENCE` was ever
emitted. The slice adds an `!IsSequence` exception to the skip and an `IsSequence →
relkind='S'` branch to the relkind selector. With slices 115's `pg_sequence` row and
`pg_get_sequence_data` SRF already in place, the chain is complete:
`getTables` (discovery) → `dumpSequence` (DDL from `pg_sequence`) → `dumpSequenceData`
(`setval()` from the SRF).

**`relam=0` is load-bearing, not cosmetic.** PostgreSQL stores `pg_class.relam=0`
for sequences: `RELKIND_HAS_TABLE_AM` (pg_class.h) deliberately *excludes*
`RELKIND_SEQUENCE` — a sequence uses the heap AM only at the relcache level
(`rd_tableam`), never via `pg_class.relam` (so `DefineRelation` leaves
`accessMethodId` invalid for a sequence). Emitting `relam=0` therefore matches real
PG **and** keeps the storage-less virtual sequence out of `pg_amcheck`: its
relation-selection CTE only heap-verifies relations with
`relam = HEAP_TABLE_AM_OID`, so a `relam=0` sequence is never fed to `verify_heapam`
(which would fail — the sequence has no heap blocks). Had we emitted `relam=2`
(heap), the existing `TestPort_PgAmcheck*` runs that create a sequence would have
started failing. (pg_dump never reads `relam` for a sequence, and `getTableAttrs`
skips sequences entirely via `if (relkind == RELKIND_SEQUENCE) continue;`, so
`pg_attribute` fidelity is irrelevant here.)

**Byte-exact output (verified vs pg_dump 18.3).** A plain `CREATE SEQUENCE
public.plain_seq` dumps with all-default clauses; an explicit one round-trips its
parameters:

```sql
CREATE SEQUENCE public.plain_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
...
SELECT pg_catalog.setval('public.plain_seq', 1, false);
```

A **standalone** sequence (no `OWNED BY`) has no `pg_depend` `'a'`/`'i'` row, so
pg_dump emits **no** `ALTER SEQUENCE ... OWNED BY` (only the `OWNER TO` role ALTER,
which every relation gets). The e2e `TestPort_PgDumpConnectionSetup` fixture now
creates both a plain and an explicit-parameter sequence and asserts the exact
default-suppressed clauses, the explicit `START WITH 100 / INCREMENT BY 10 /
MAXVALUE 1000`, and the **absence** of any `OWNED BY`. Unit coverage:
`executor.TestSequenceSurfacedInPgClass` (sequence appears in `pg_class` with
`relkind='S'`, `relam=0`).

### Slice 117 — typed (`AS smallint` / `AS integer`) and `CYCLE` sequences

Slice 117 extends the slice-116 discovery path to the two remaining
single-sequence DDL clauses pg_dump can emit: the `AS <type>` data-type clause and
the trailing `CYCLE`. **No production code changed** — the executor already tracked
both attributes end-to-end; this slice is a verification slice that pins the
byte-exact output with new fixtures.

The wiring was already complete before this slice:

- `CREATE SEQUENCE ... AS smallint|integer` is parsed and `SetSequenceDataType`
  records the declared type on the `seqState`; `seqTypeBounds` derives the
  type's default min/max (smallint `1..32767`, integer `1..2147483647` ascending),
  so the bounds match what real PG stores.
- `sequenceParamsForCatalog` maps the data type to `seqtypid` via `seqTypeOID`
  (`smallint→21`, `integer→23`, `bigint→20` default) and threads `seqcycle`
  through into the `pg_sequence` row.
- `formatTypeOID(21,NULL)="smallint"` and `formatTypeOID(23,NULL)="integer"`, so
  pg_dump's `getSequences` query `format_type(seqtypid, NULL)` renders the right
  type name.

**Byte-exact output (verified vs pg_dump 18.3).** A typed sequence emits an
`AS <type>` clause immediately after the header; because the type-derived
`seqmax` equals pg_dump's own `default_maxv` for that type, the `MAXVALUE` clause
is still suppressed to `NO MAXVALUE`:

```sql
CREATE SEQUENCE public.small_seq
    AS smallint
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
```

A bigint-default sequence (the slice-116 `plain_seq`/`num_seq`) emits **no**
`AS` clause — pg_dump suppresses the data-type clause for the default. A `CYCLE`
sequence sets `seqcycle=true`, which makes pg_dump append `CYCLE` as the final
clause before the semicolon:

```sql
CREATE SEQUENCE public.cyc_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
    CYCLE;
```

The `TestPort_PgDumpConnectionSetup` fixture now creates `small_seq AS smallint`,
`int_seq AS integer`, and `cyc_seq CYCLE`, and asserts the 4-space-indented blocks
that pin pg_dump's exact clause order (`AS <type>` before `START WITH`, `CYCLE`
last) plus the **absence** of a spurious `AS bigint` on any default sequence.

### Slice 118 — sequence `OWNED BY table.column` (`pg_depend` AUTO row)

Slice 118 closes the last single-sequence pg_dump surface: a sequence tied to a
column via `OWNED BY`. This is the **first slice to require a non-empty
`pg_depend`** — every prior catalog query treated it as the empty view.

**How pg_dump finds ownership.** `getTables` LEFT JOINs `pg_depend`, gated on the
relation being a sequence and the dependency being the column link:

```sql
LEFT JOIN pg_depend d ON
  (c.relkind = 'S' AND d.classid = 'pg_class'::regclass AND d.objid = c.oid AND
   d.objsubid = 0 AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a','i'))
```

It reads `d.refobjid AS owning_tab`, `d.refobjsubid AS owning_col`, and
`(d.deptype = 'i') IS TRUE AS is_identity_sequence`. For a plain `OWNED BY`
sequence the dependency is **AUTO (`'a'`)**, so `is_identity_sequence` is false and
`dumpSequence` emits a trailing statement (PG self-qualifies the table reference):

```sql
ALTER SEQUENCE public.owned_seq OWNED BY public.owner_tbl.id;
```

`getOwnedSeqs` ORs the owning table's dump components into the sequence, so the
`CREATE SEQUENCE` is still emitted; the AUTO row also feeds `getDependencies`,
correctly ordering the sequence after its table (exactly what upstream PG records).

**Production change.** goopg tracks the owner string (`seqState.ownedBy`,
`"table.column"`) end-to-end via `ALTER/CREATE SEQUENCE ... OWNED BY`, but the
catalog returned an empty `pg_depend`, so no `OWNED BY` ever dumped. Two changes
close the gap:

- `catalog.SeqParams` gains an `OwnedBy` field; `sequenceParamsForCatalog` fills it
  from `seqState.ownedBy`.
- `InMemory.dependVirtualRows` (wired to `pg_depend.VirtualRows`) iterates the
  `IsSequence` relations, and for each with a non-empty `OwnedBy` resolves the
  owning table OID + column attnum (1-based, `Ordinal+1`) from the catalog's own
  table registry, emitting one row: `classid=refclassid=1259 (pg_class)`,
  `objid=seq OID`, `objsubid=0`, `refobjid=table OID`, `refobjsubid=attnum`,
  `deptype='a'`. Standalone sequences contribute no row, so the empty-view
  behaviour (and the slice-116 "no spurious OWNED BY" guard) is preserved.

The owner reference is resolved against the **sequence's own schema** when the
`OWNED BY` clause is unqualified (PG requires the sequence and owning table to share
a schema), and against an explicit `schema.table.column` otherwise. The
`TestPort_PgDumpConnectionSetup` fixture creates `owner_tbl(id bigint, label text)`
+ `owned_seq OWNED BY owner_tbl.id` and asserts the `ALTER SEQUENCE ... OWNED BY`
statement plus the owning table's `CREATE TABLE`.

### Slice 119 — descending sequences (negative-direction default suppression)

Slice 119 is the **mirror** of the ascending default-bound work in slices 116/117:
it verifies that a *descending* sequence (`INCREMENT BY < 0`) round-trips through
pg_dump's negative-direction default suppression.

**How pg_dump's defaults flip.** `dumpSequence` computes `default_minv`/
`default_maxv` from the sign of the increment:

```c
default_minv = is_ascending ? 1            : PG_INT64_MIN;   /* bigint */
default_maxv = is_ascending ? PG_INT64_MAX : -1;
```

So for a descending bigint sequence the defaults are `minv=PG_INT64_MIN`,
`maxv=-1`, and the backend stores `seqstart=seqmax` (`-1`). pg_dump always emits
`START WITH`/`INCREMENT BY`, then suppresses MIN/MAX only when they equal those
flipped defaults. A plain `CREATE SEQUENCE … INCREMENT BY -1` therefore dumps:

```sql
CREATE SEQUENCE public.desc_seq
    START WITH -1
    INCREMENT BY -1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
SELECT pg_catalog.setval('public.desc_seq', -1, false);
```

An explicit-bound descending sequence (`INCREMENT BY -2 MINVALUE -100 MAXVALUE -5`)
differs from the defaults, so both clauses emit and `START WITH` defaults to the
maxv:

```sql
CREATE SEQUENCE public.desc_bound_seq
    START WITH -5
    INCREMENT BY -2
    MINVALUE -100
    MAXVALUE -5
    CACHE 1;
```

**No production change.** `execCreateSequence` already computes the descending
defaults identically to the PG backend — `seqTypeBounds` returns the type minimum,
the direction branch sets `minV=type_min, maxV=-1` for `increment < 0`, and
`start=maxV` — and `sequenceParamsForCatalog` threads `min`/`max`/`start` into
`pg_sequence` unchanged. `SequenceRowData` returns `start` (not the internal
`current = start - increment`) when the sequence is uncalled, so the `setval`
last_value is `-1`, matching real pg_dump. This slice adds two fixtures
(`desc_seq`, `desc_bound_seq`) to `TestPort_PgDumpConnectionSetup` with full
4-space-indented block assertions; both were confirmed byte-identical to real
pg_dump 18.3 (`/tmp/pgcheck_du119`).

### Slice 120 — identity columns (`GENERATED … AS IDENTITY`, `deptype='i'`)

Slice 120 is the first **multi-statement** pg_dump object beyond a standalone
sequence: an identity column's *backing* sequence dumps as an `ALTER TABLE …
ADD GENERATED … AS IDENTITY (SEQUENCE NAME …)` clause, **not** a standalone
`CREATE SEQUENCE`, and **not** the `ALTER SEQUENCE … OWNED BY` an explicit
OWNED-BY sequence (slice 118) gets.

**How pg_dump decides.** `getTables` LEFT JOINs `pg_depend` on the sequence and
reads `(d.deptype = 'i') IS TRUE AS is_identity_sequence`. The dependency type
distinguishes the two ownership kinds:

| deptype | meaning | pg_dump output |
|---------|---------|----------------|
| `'a'` (AUTO) | `OWNED BY` sequence (slice 118) | `CREATE SEQUENCE` + `ALTER SEQUENCE … OWNED BY` |
| `'i'` (INTERNAL) | identity-column sequence | `ALTER TABLE … ADD GENERATED … AS IDENTITY (SEQUENCE NAME …)` |

When `is_identity_sequence`, `dumpSequence` takes the identity branch
(`pg_dump.c` ~18910): it emits the `ALTER TABLE … ALTER COLUMN c ADD GENERATED`
prefix, then reads `pg_attribute.attidentity` for the keyword — `'a'` →
`ALWAYS`, `'d'` → `BY DEFAULT` — then ` AS IDENTITY (\n    SEQUENCE NAME …\n`
followed by the same `START WITH / INCREMENT BY / MIN/MAX / CACHE` body a
sequence gets. The identity branch **omits** the `AS <type>` clause (the column
already carries the type) and closes with `\n);`. `OWNED BY` is suppressed for
identity sequences. Real pg_dump 18.3 output for an `integer GENERATED ALWAYS`
column:

```sql
CREATE TABLE public.ident_tbl (
    id integer NOT NULL,
    label text
);
ALTER TABLE public.ident_tbl ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.ident_tbl_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
SELECT pg_catalog.setval('public.ident_tbl_id_seq', 1, false);
```

**Three coupled goopg changes** (all required for byte-identity):

1. **Identity KIND survives into the catalog.** The parser captured
   `ColumnDef.IdentityAlways` (ALWAYS vs BY DEFAULT) but the catalog dropped it;
   `catalog.Column` now carries `IdentityAlways`, plumbed through
   `operators_ddl.go`'s `CREATE TABLE` column build.
2. **`pg_attribute.attidentity` emits `'a'` / `'d'`.** `buildUserPGAttributeRow`
   replaced its hardcoded empty string with `attIdentityFor(col)` (`'a'` for
   ALWAYS, `'d'` for BY DEFAULT, empty otherwise) — the value pg_dump reads for
   the keyword.
3. **`pg_depend` synthesizes a `deptype='i'` row.** `InMemory.dependVirtualRows`
   resolves the sequence's owning column and flips the dependency type to `'i'`
   when that column `IsIdentity`, vs the `'a'` an OWNED-BY sequence keeps.

**Plus one discovery fix and one latent-type fix surfaced by the round-trip:**

- The implicit identity sequence was registered in the executor's sequence
  registry but **never given a catalog `IsSequence` relation**, so it appeared in
  neither `pg_class` (`relkind='S'`) nor `dependVirtualRows`' table scan — pg_dump
  could not discover it at all. The virtual-table creation in `execCreateSequence`
  was extracted to `createSeqCatalogTable` and is now also called for identity
  columns (SERIAL keeps its prior catalog-less behavior; its pg_dump round-trip is
  a separate future slice).
- The `seqDataType` switch for implicit sequences mapped a `bigint` identity
  column to `"integer"` (it only matched the `bigserial`/`serial8` spellings), so
  `pg_sequence.seqtypid` was `int4` while `seqmax` was `INT64_MAX`. pg_dump then
  computed `default_maxv = INT32_MAX ≠ seqmax` and emitted a spurious
  `MAXVALUE 9223372036854775807` instead of `NO MAXVALUE`. The switch now mirrors
  the `seqMin/seqMax` switch (`int2`/`smallint` → smallint, `int8`/`bigint` →
  bigint); these base aliases only reach the loop for identity columns, so SERIAL
  is unaffected.

Both an `integer GENERATED ALWAYS` and a `bigint GENERATED BY DEFAULT` fixture
(`ident_tbl`, `ident_def`) dump byte-identically to real pg_dump 18.3
(`/tmp/pgref_du120`), with negative guards asserting no standalone
`CREATE SEQUENCE` and no `ALTER SEQUENCE … OWNED BY` for either.

### Slice 121 — `SERIAL` / `BIGSERIAL` columns (`deptype='a'` + separate `SET DEFAULT`)

Slice 121 is the AUTO (`'a'`) counterpart to slice 120's INTERNAL (`'i'`)
identity column, and the first object whose default is forced into a **separate**
`ALTER TABLE … SET DEFAULT` statement by pg_dump's dependency-loop repair. PG
expands `serial` to a plain integer column `NOT NULL` with an *owned* sequence
and a `nextval()` default — pg_dump never emits the word "serial". A `serial`
column dumps as **four coupled statements**:

```sql
CREATE TABLE public.ser_tbl (
    id integer NOT NULL,
    label text
);
CREATE SEQUENCE public.ser_tbl_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.ser_tbl_id_seq OWNED BY public.ser_tbl.id;
ALTER TABLE ONLY public.ser_tbl ALTER COLUMN id SET DEFAULT nextval('public.ser_tbl_id_seq'::regclass);
SELECT pg_catalog.setval('public.ser_tbl_id_seq', 1, false);
```

A `bigserial` omits the `AS integer` clause (`int8`/`bigint` is the `CREATE
SEQUENCE` default type — slice 117 logic).

**Why the `SET DEFAULT` is separate (not inline in `CREATE TABLE`).** pg_dump's
`getTableAttrs` initially marks the default *not* separate (`pg_dump.c` ~9534) and
adds a `table → attrdef` dependency so the default is emitted before the table.
But the default references the sequence (`nextval`), and the sequence is `OWNED
BY` the table — so `getDependencies` reads two `pg_depend` rows that, with the
`table → attrdef` edge, close a loop:

```
table ──(pg_dump adds)──▶ attrdef ──(pg_depend 'n')──▶ sequence ──(pg_depend 'a' OWNED BY)──▶ table
```

`repairTableAttrDefMultiLoop` (`pg_dump_sort.c` ~1389) detects the 3-object loop,
removes the `table → attrdef` edge, and marks the attrdef `separate = true` —
producing the standalone `ALTER TABLE … SET DEFAULT`. A `DEFAULT nextval('seq')`
on an **un-owned** sequence has no such loop and stays inline; the OWNED-BY edge
is what forces the split. All of this happens inside the real pg_dump binary;
goopg only supplies the `pg_depend` rows that form the loop.

**Five coupled goopg changes** (all required for byte-identity):

1. **The serial sequence gets a catalog `IsSequence` relation.** The slice-120
   `createSeqCatalogTable` was gated on `c.IdentityColumn`; it now also runs for
   serial columns, so pg_dump discovers the sequence in `pg_class` (`relkind='S'`)
   and `dumpSequence` regenerates `CREATE SEQUENCE` from `pg_sequence`
   (`SetSequenceDataType` already records `integer`/`bigint`, so the `AS integer`
   clause flows from the slice-117 path).
2. **`pg_attribute.atttypid` remaps `serial`→`int4`, `bigserial`→`int8`,
   `smallserial`→`int2`.** `buildUserPGAttributeRow` keeps the catalog type name
   as the serial spelling (the `INSERT` auto-gen path keys on it) but reports the
   base-integer OID so `format_type(atttypid)` renders `integer`/`bigint`. The
   physical attrs (`attlen`/`attbyval`/…) follow the remapped OID.
3. **`atthasdef` reads true for serial columns** (`catalog.IsSerialTypeName`), so
   pg_dump fetches the column's default.
4. **`pg_attrdef` surfaces a `nextval()` row.** `InMemory.attrDefRowsLocked` — a
   new shared, deterministic (sorted-key) row builder feeding *both* the
   `pg_attrdef` virtual table and `dependVirtualRows` — synthesizes
   `adbin = nextval('<schema>.<table>_<col>_seq'::regclass)` for serial columns.
   pg_dump's `pg_get_expr` is a pass-through, so the schema-qualified form is
   stored verbatim.
5. **`pg_depend` synthesizes the NORMAL (`'n'`) attrdef→sequence link.**
   `dependVirtualRows` emits `(classid=2604 pg_attrdef, objid=attrdef oid,
   refclassid=1259 pg_class, refobjid=sequence oid, deptype='n')` for each serial
   column. The attrdef `oid` comes from the *same* `attrDefRowsLocked` numbering
   the `pg_attrdef` view uses, so pg_dump matches the scanned `pg_attrdef.oid`
   against the `pg_depend.objid` (the sibling-path constraint that closes the
   loop). The AUTO (`'a'`) OWNED-BY row is unchanged from slice 118 — serial
   columns are not identity columns, so `deptype` stays `'a'`.

`ser_tbl` (`serial`, `AS integer`) and `bigser_tbl` (`bigserial`, no `AS`)
dump byte-identically to real pg_dump 18.3, with negative guards asserting no
literal `serial`/`bigserial` and no *inline* `DEFAULT nextval` (the separate
`SET DEFAULT` is mandatory). The slice-90 empty-default guard was tightened to
newline-anchored forms (`DEFAULT;\n` / `DEFAULT \n`) so it no longer false-
matches pg_dump's new `-- … Type: DEFAULT; Schema: …` section comment.

### Slice 122 — multi-serial table (two owned sequences on one table)

Slice 122 is the multi-column counterpart to slice 121's single-serial table. A
table with **two** `serial` columns (`CREATE TABLE public.mser (a serial, b
serial, note text)`) expands into *two* owned sequences, and pg_dump emits — in
column order — two `CREATE SEQUENCE` / `OWNED BY` / `SET DEFAULT` / `setval`
groups:

```sql
CREATE TABLE public.mser (
    a integer NOT NULL,
    b integer NOT NULL,
    note text
);
CREATE SEQUENCE public.mser_a_seq AS integer …;
ALTER SEQUENCE public.mser_a_seq OWNED BY public.mser.a;
CREATE SEQUENCE public.mser_b_seq AS integer …;
ALTER SEQUENCE public.mser_b_seq OWNED BY public.mser.b;
ALTER TABLE ONLY public.mser ALTER COLUMN a SET DEFAULT nextval('public.mser_a_seq'::regclass);
ALTER TABLE ONLY public.mser ALTER COLUMN b SET DEFAULT nextval('public.mser_b_seq'::regclass);
SELECT pg_catalog.setval('public.mser_a_seq', 1, false);
SELECT pg_catalog.setval('public.mser_b_seq', 1, false);
```

**No production code change was required** — the slice-121 machinery generalizes
to N columns as-is. The verification value is the **sibling-path hazard**: each
column's `pg_attrdef` row must carry a *distinct* `oid`, and `dependVirtualRows`
must pair each attrdef oid with the matching sequence in the NORMAL (`'n'`)
`pg_depend` link. If two serial columns collided on one attrdef oid, or the
attrdef→sequence pairing crossed (`a → mser_b_seq`), pg_dump would silently
cross-wire the `nextval()` defaults to the wrong sequence. `attrDefRowsLocked`
already numbers rows deterministically per `(reloid, attnum)` sorted key, so the
oids are distinct and stably ordered. The slice asserts both sequences round-trip
byte-identically to real pg_dump 18.3 **and** adds negative guards that neither
`SET DEFAULT` is cross-wired (`a → mser_b_seq`, `b → mser_a_seq`). This locks the
multi-attrdef-per-table path against future regressions in the oid numbering or
the depend-row pairing.

### Slice 123 — mixed identity + serial table (two deptypes on one relation)

Slice 123 puts both an IDENTITY column (slice 120, INTERNAL `'i'`) and a SERIAL
column (slice 121, AUTO `'a'`) on **one** table (`CREATE TABLE public.mix (id
integer GENERATED ALWAYS AS IDENTITY, n serial, note text)`). Both columns own a
sequence, but pg_dump emits a **different form** for each on the same relation —
the deptype decides the shape:

```sql
CREATE TABLE public.mix (
    id integer NOT NULL,
    n integer NOT NULL,
    note text
);
-- identity sequence: embedded in the IDENTITY clause, NO standalone CREATE
-- SEQUENCE, NO OWNED BY, NO SET DEFAULT
ALTER TABLE public.mix ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.mix_id_seq START WITH 1 INCREMENT BY 1 …
);
-- serial sequence: standalone CREATE SEQUENCE + OWNED BY + separate SET DEFAULT
CREATE SEQUENCE public.mix_n_seq AS integer …;
ALTER SEQUENCE public.mix_n_seq OWNED BY public.mix.n;
ALTER TABLE ONLY public.mix ALTER COLUMN n SET DEFAULT nextval('public.mix_n_seq'::regclass);
SELECT pg_catalog.setval('public.mix_id_seq', 1, false);
SELECT pg_catalog.setval('public.mix_n_seq', 1, false);
```

**No production code change was required** — the slice-120 and slice-121
machinery compose on one relation as-is. The verification value is the **deptype
sibling-path hazard**: `dependVirtualRows` must tag the identity sequence's
`pg_depend` row INTERNAL (`'i'`) and the serial sequence's AUTO (`'a'`) *on the
same table*. If either were mis-classified, pg_dump would emit the wrong shape —
a standalone `CREATE SEQUENCE public.mix_id_seq` for the identity sequence, or an
`ADD GENERATED … AS IDENTITY` clause (and no SET DEFAULT) for the serial column.
The slice asserts both forms round-trip byte-identically to real pg_dump 18.3
**and** adds negative guards that the two paths never cross (no standalone
`CREATE SEQUENCE`/`OWNED BY` for `mix_id_seq`; no `ADD GENERATED` on column `n`;
no `SET DEFAULT nextval` on column `id`). One subtlety surfaced while writing the
guards: an unqualified `ALTER COLUMN id SET DEFAULT nextval` negative falsely
matched `ser_tbl`'s legitimate serial default (its column is also named `id`), so
the negatives are scoped with the `public.mix` table prefix. This locks the
identity/serial deptype split against future regressions in `dependVirtualRows`.

### Slice 124 — advanced sequence (`is_called=true` setval branch)

Every prior sequence slice (115–123) dumps its `setval` as `(name, start, false)`
— the **never-called** state. Slice 124 is the first over the **called** branch.
After `setval('public.bumped_seq', 42, true)`, the sequence's process-global
runtime state (`seqRegistry` in `operators_sequence.go`) is `current=42 /
called=true`, so `SequenceRowData` returns `last_value=42 / is_called=true`, the
`pg_get_sequence_data` SRF projects `(42, true)`, and pg_dump's `getSequences`
emits:

```sql
CREATE SEQUENCE public.bumped_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
-- data section:
SELECT pg_catalog.setval('public.bumped_seq', 42, true);
```

The bump lives only in the data-section `setval` (the `CREATE SEQUENCE` block is
the plain default). The `true` is load-bearing: a restore replays it so the next
`nextval` continues at 43 instead of restarting at 1 — a regression that
hard-wired `is_called=false` would silently corrupt sequence continuity yet pass
every never-called slice. The setval state is observed by the *separate* pg_dump
connection because the registry is process-global, not session-local. **No
production code change** — `SequenceRowData`'s `called=true` branch already
returns `current` as `last_value`; this slice is the regression guard. Asserts:
the exact `(42, true)` form is present; negatives reject the three wrong forms
(`(1, false)` never-called, `(42, false)` ignored-called-flag, `(1, true)`
ignored-value).

**Discovered (deferred → slice 125):** `SequenceRowData`'s `called=false`
branch returned `s.start` rather than the actual on-disk `last_value`. For a
sequence whose value was set with `setval(seq, N, false)` where `N != start`,
real pg_dump 18.3 emits `setval('..', N, false)` but goopg would emit
`setval('..', start, false)`. Fixed in slice 125 (below).

### Slice 125 — rewound sequence (`setval(seq, N, false)`, `N != start`)

The not-yet-called branch with a **non-default** last_value was the last
sequence gap. Slices 115–123 only dumped the never-called default (`last_value
= start`), and slice 124 covered the called branch — but a `setval(seq, N,
false)` rewinds a sequence to N *without* marking it called. Real PG stores
`last_value=N / is_called=false` (verified: `SELECT * FROM rewound_seq` →
`30/0/f`), so pg_dump keeps the original `START WITH 5` in the schema
`CREATE SEQUENCE` while the data section emits the rewound value:

```sql
CREATE SEQUENCE public.rewound_seq
    START WITH 5
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
-- data section:
SELECT pg_catalog.setval('public.rewound_seq', 30, false);
```

**Production fix** (`internal/executor/operators_sequence.go`,
`SequenceRowData`): the not-called branch now returns `current + increment`
instead of the bare `start`. The registry stores `current = nextTarget -
increment` (`RegisterSequence` seeds `start-increment`; `setval(N,false)` and
`RESTART WITH N` seed `N-increment`), so `current + increment` is exactly the
on-disk `last_value` pg_dump reads — `start` for a fresh sequence, `N` after a
rewind. The pre-fix code dropped any rewind, so a restore's next `nextval`
would yield `start` instead of `N`, silently corrupting continuity. This is the
single shared function behind both `SELECT * FROM <seq>` (the
`createSeqCatalogTable` VirtualRows closure) and the `pg_get_sequence_data` SRF
that pg_dump reads, so both sibling paths are fixed in one place; the
`pg_sequences` *view* is unaffected (it sources `AllSequenceInfos` and emits
NULL `last_value` while not called, matching PG). Verified byte-identical vs
real pg_dump 18.3 (reference `/tmp/du125_pgdata`). Asserts: the exact
`(30, false)` form + the unchanged `START WITH 5`; negatives reject the pre-fix
`(5, false)` and the wrong-flag/value forms.

### Slice 126 — multi-column UNIQUE constraint with non-table key order

The constraint-backed UNIQUE/PK path was previously covered only by a
single-column UNIQUE (`foo_code_key UNIQUE (code)`, slice 51) and a
declaration-order multi-column PRIMARY KEY (`bar_pkey PRIMARY KEY (a, b)`,
slice 53). Neither exercises a *multi-column UNIQUE* whose key order differs
from the table's column order — the case where the auto-generated name and the
rendered column list must both follow **index-key order**, not table order.
`CREATE TABLE public.uniqm (a integer, b integer, c text, UNIQUE (b, a))` dumps:

```sql
ALTER TABLE ONLY public.uniqm
    ADD CONSTRAINT uniqm_b_a_key UNIQUE (b, a);
```

No production change was needed — goopg already stores the index key columns in
declared order (`catalog.Index.Columns`), and both the deparse
(`buildConstraintDefString`, `internal/executor/expr.go`) and the
auto-name generator (`<table>_<col1>_<col2>_key`,
`internal/executor/operators_ddl.go`) consume that slice, so the `(b, a)`
order and the `uniqm_b_a_key` name fall out correctly. This slice is the
regression guard locking that behavior: positive assert for the exact
`uniqm_b_a_key UNIQUE (b, a)`, plus negative guards rejecting a key-order
regression (`uniqm_a_b_key` / `UNIQUE (a, b)`) that would silently reorder the
constraint columns on restore. Verified byte-identical vs real pg_dump 18.3
(reference `/tmp/du126_pgdata`).

### Slice 127 — anonymous table-level CHECK constraints (auto-named)

Slice 49 restored *column-level* CHECK constraints (`foo_qty_check`, auto-named
`<table>_<col>_check`) and slice 96 the *domain* CHECK path; explicitly-named
table CHECKs already round-tripped. The gap was the **anonymous table-level
CHECK** — `CREATE TABLE t (..., CHECK (expr))` with no `CONSTRAINT name`. goopg
stored these with an empty name and OID 0 (`AddCheck("", chk, 0)`), which
`pg_constraint`'s `VirtualRows` skips, so the constraint was **silently dropped**
from the dump (and `relchecks` undercounted).

PostgreSQL's `AddRelationNewConstraints` (`src/backend/catalog/heap.c`) assigns
every anonymous CHECK a name at DDL time via `ChooseConstraintName`: it counts
the distinct columns the predicate references (`pull_var_clause`, which does not
descend into sublinks) and names a single-column CHECK `<table>_<col>_check`,
any other CHECK (multiple columns, or none) `<table>_check`; collisions get an
incrementing numeric suffix on the `check` label (`<base>1`, `<base>2`, …).

goopg now mirrors this: the `s.TableChecks` loop in
`internal/executor/operators_ddl.go` calls a new `autoCheckName(tbl, expr)` that
re-parses the raw CHECK text (`parser.ParseExpr`), walks it with
`collectCheckExprColumns` to count distinct column refs, picks the
single-vs-multi-column form, and resolves collisions against the table's
existing CHECK names — then allocates an OID so the constraint surfaces in
`pg_constraint` (contype='c') and `relchecks`. The render path
(`pg_get_constraintdef` → `CHECK ((expr))`) already handled named checks
(slice 49), so the named constraint now flows through unchanged.

```sql
CREATE TABLE public.chk  (a integer, b integer, CHECK (a < b));  -- → CONSTRAINT chk_check    CHECK ((a < b))
CREATE TABLE public.chk1 (x integer,            CHECK (x > 0));  -- → CONSTRAINT chk1_x_check  CHECK ((x > 0))
```

`chk` exercises the multi-column branch, `chk1` the single-column branch.
Positive asserts pin both names + inline rendering; negative guards reject a
single-vs-multi flip (`chk_a_check` / `chk1_check`). Verified byte-identical to
real pg_dump 18.3 (reference `/tmp/du127_pgdata`).

### Slice 128 — anonymous table-level CHECK with NO INHERIT

Slice 127 made anonymous table-level CHECKs round-trip, but only the *aggregate*
`CreateTableStmt.TableHasNoInheritCheck` bool survived parsing — the **per-check**
NO INHERIT flag was discarded. So `CREATE TABLE t (..., CHECK (expr) NO INHERIT)`
stored `NamedCheckConstraint.NoInherit=false`, and the dump emitted a plain
*inheritable* CHECK: `pg_get_constraintdef` dropped the ` NO INHERIT` suffix and
`pg_constraint.connoinherit` reported `'f'`. On a re-loaded dump the constraint
would wrongly propagate to child tables — a silent semantic divergence.

The deparse path already appended ` NO INHERIT` when the flag was set (added in
slice 127's `pg_get_constraintdef` CHECK branch); the gap was purely the lost
flag. The fix threads it end-to-end:

- `parser`: `CreateTableStmt.TableCheckNoInherit []bool` is kept parallel to
  `TableChecks`; both anonymous-CHECK parse sites (bare and `CONSTRAINT`-with-
  empty-name) append one entry each, recording the per-check NO INHERIT.
- `catalog`: new `Table.AddCheckWithNoInherit(name, expr, oid, noInherit)`
  (`AddCheck` now delegates with `false`); the `pg_constraint` `VirtualRows`
  CHECK row sets `connoinherit` from `nc.NoInherit` (was hard-coded `'f'`).
- `executor`: the `s.TableChecks` loop passes `TableCheckNoInherit[i]` through.

```sql
CREATE TABLE public.chk2 (y integer, CHECK (y > 0) NO INHERIT);
-- → CONSTRAINT chk2_y_check CHECK ((y > 0)) NO INHERIT
```

Positive assert pins the suffix; negative guard rejects a plain
`CONSTRAINT chk2_y_check CHECK ((y > 0))\n` (flag dropped). Verified
byte-identical to real pg_dump 18.3 (reference `/tmp/du128_pgdata`).

### Slice 129 — named table-level CHECK with NO INHERIT

The named analog of slice 128. A `CONSTRAINT c CHECK (expr) NO INHERIT` is parsed
into a `PartitionCheckConstraint` (`CreateTableStmt.TableNamedChecks`), which had
**no** `NoInherit` field — the parser detected the suffix only into the aggregate
`TableHasNoInheritCheck` bool, and the executor stored named checks via
`AddCheck` (NoInherit=false). So the named NO-INHERIT check dumped as a plain
*inheritable* CHECK: `pg_get_constraintdef` dropped the suffix and
`pg_constraint.connoinherit` reported `'f'` — the identical silent
inheritance-semantics divergence as slice 128, but for the explicitly named form.

The deparse path needed no change: `pg_get_constraintdef`'s CHECK branch and the
`pg_constraint` `VirtualRows` row both already key off `NamedCheckConstraint.NoInherit`
in the catalog (shared by anonymous auto-named and named checks). The fix threads
the flag through the named path:

- `parser`: `PartitionCheckConstraint.NoInherit bool`; the named-CHECK parse site
  sets it on the just-appended entry once ` NO INHERIT` is consumed (the append
  precedes the suffix parse, so it back-fills the last element).
- `executor`: the `s.TableNamedChecks` loop now calls
  `AddCheckWithNoInherit(nc.Name, nc.Expr, oid, nc.NoInherit)` instead of `AddCheck`.

```sql
CREATE TABLE public.chk3 (z integer, CONSTRAINT chk3_pos CHECK (z > 0) NO INHERIT);
-- → CONSTRAINT chk3_pos CHECK ((z > 0)) NO INHERIT
```

Positive assert pins the named suffix; negative guard rejects a plain
`CONSTRAINT chk3_pos CHECK ((z > 0))\n` (flag dropped).

### Slice 130 — per-sequence CACHE size

`CREATE SEQUENCE … CACHE n` (and `ALTER SEQUENCE … CACHE n`) sets the sequence's
preallocation size. goopg parsed the clause but **discarded the value**: the
in-memory `seqState` carried no cache field, and `sequenceParamsForCatalog`
hard-wired `Cache: 1` into the `pg_sequence.seqcache` row pg_dump reads. So every
dumped `CREATE SEQUENCE` emitted `CACHE 1` regardless of the declared cache — a
silent loss of the parameter (a restored dump would change the sequence's caching
behaviour). The `ALTER SEQUENCE … CACHE n` path was the same: the parser parsed
the value into a throwaway local.

The fix tracks the cache on the registry-side sequence state and threads it into
the catalog row:

- `executor` (`operators_sequence.go`): `seqState.cache int64` (default 1 in
  `RegisterSequence`); new `SetSequenceCache(name, cache)` setter (clamps `< 1` to
  1, matching PG's minimum); `UpdateSequenceParams` gains a `cache *int64` param;
  `sequenceParamsForCatalog` returns the tracked value (default 1 for sequences
  registered before cache tracking).
- `executor` (`operators_ddl.go`): `execCreateSequence` calls `SetSequenceCache`
  when `CACHE n` was given; the ALTER path passes `s.Cache` into
  `UpdateSequenceParams`.
- `parser`: `AlterSequenceStmt.Cache *int64`; the ALTER `cache` case now stores
  the parsed value instead of discarding it (`CreateSequenceStmt.Cache` already
  existed and reached the catalog only after this slice).

```sql
CREATE SEQUENCE public.cache_seq CACHE 5;          -- → … CACHE 5;
CREATE SEQUENCE public.altcache_seq;
ALTER SEQUENCE public.altcache_seq CACHE 42;        -- → … CACHE 42;
```

Positive asserts pin the full 4-space CREATE blocks (CACHE is the last clause for
a non-cycling sequence); negative guard rejects `cache_seq` falling back to
`CACHE 1`. Verified byte-identical to real pg_dump 18.3.

### Slice 131 — UNIQUE constraint with an INCLUDE (covering) column

A table-level `UNIQUE (key) INCLUDE (cover)` constraint adds a covering column to
the backing index without making it part of the uniqueness key. Two facets must
round-trip through pg_dump and neither was previously exercised by the round-trip
test (the only INCLUDE coverage was an EXCLUDE-constraint *unit* test):

1. **Auto-generated name folds in the covering column.** PG builds
   `allIndexParams = list_concat_copy(indexParams, indexIncludingParams)`
   (`indexcmds.c`) and feeds the concatenation to
   `ChooseIndexColumnNames → ChooseIndexNameAddition`, so the covering column lands
   in the name too. `UNIQUE (a) INCLUDE (b)` is therefore named **`uniqi_a_b_key`**
   (not `uniqi_a_key`), confirmed empirically against real pg_dump 18.3
   (reference `/tmp/du131_pgdata`).
2. **`pg_get_constraintdef` appends ` INCLUDE (cols)`.** pg_dump emits the
   constraint from this deparse, which renders `UNIQUE (a) INCLUDE (b)`.

**No production change needed** — goopg already matched both facets:
`autoIndexNameWithIncludes(tbl, keyCols, inclCols, "key")`
(`internal/executor/operators_ddl.go`) joins `keyCols+inclCols` for the name, the
table-level UNIQUE path stores the covering list on `catalog.Index.IncludeColumns`,
and `buildConstraintDefString` (`internal/executor/expr.go`) appends the
` INCLUDE (...)` clause. This slice is the regression guard that locks the
name-join + clause render.

```sql
CREATE TABLE public.uniqi (a integer, b integer, c text, UNIQUE (a) INCLUDE (b));
-- → ALTER TABLE ONLY public.uniqi
--       ADD CONSTRAINT uniqi_a_b_key UNIQUE (a) INCLUDE (b);
```

Positive assert pins `ADD CONSTRAINT uniqi_a_b_key UNIQUE (a) INCLUDE (b)`; two
negative guards reject a dropped INCLUDE (`uniqi_a_key UNIQUE (a)`, covering column
lost) and a key/cover confusion (`uniqi_a_b_key UNIQUE (a, b)`, a different
uniqueness semantic). Verified byte-identical to real pg_dump 18.3.

### Slice 132 — view→table dependency ordering (topological emission)

A `pg_dump` archive is replayed top-to-bottom by `pg_restore`/`psql` with no
forward references: every object must appear *after* the objects it depends on.
A view that selects from `public.foo` therefore must have its `CREATE VIEW`
emitted **after** the `CREATE TABLE public.foo` that backs it; otherwise restore
fails with `relation "public.foo" does not exist`.

Slices 57/58/60 added view, renamed-column-view and materialized-view coverage,
but each only asserted the statement *text is present* — none pinned its
*position* relative to the base table. This slice locks the ORDER.

pg_dump derives the order by topologically sorting the dump's `TocEntry` DAG,
which it builds from the dependency edges goopg surfaces (`pg_depend` rows plus
the rewrite/relation edges `getDependencies` reads). A regression that dropped or
inverted goopg's view→table dependency edge would let a view sort ahead of its
table and silently produce an **unrestorable** dump that still passes every
presence check.

**No production change needed** — goopg already emits the table before all three
dependents. Empirically verified against goopg's own pg_dump output (byte
offsets): `CREATE TABLE public.foo (` at 21374 precedes `CREATE MATERIALIZED VIEW
public.foo_mv` (22004), `CREATE VIEW public.foo_rview` (22497) and `CREATE VIEW
public.foo_view` (22700). The new positional assert computes
`strings.Index(...)` for the base table and each dependent view and fails if any
view's offset is below the table's — guarding the topological order that restore
correctness depends on, complementing the presence checks of the earlier slices.

### Slice 133 — cross-table foreign-key dependency ordering (post-data split)

A foreign key from `public.baz` to `public.bar` introduces a
referencing→referenced edge. Unlike the view edge of slice 132, pg_dump does
**not** order the two `CREATE TABLE` statements by it. Instead it *splits* the FK
out of the table body into a separate `ALTER TABLE ... ADD CONSTRAINT ... FOREIGN
KEY` emitted in the **post-data** section, after every `CREATE TABLE`. That is
how pg_dump breaks dependency cycles (mutual FKs) while still guaranteeing the
referenced relation exists when the constraint is replayed.

So the invariant to pin is not "bar before baz" but "the FK `ADD CONSTRAINT`
after **both** tables". A regression that inlined the FK back into `CREATE TABLE
public.baz`, or emitted the `ALTER` ahead of `CREATE TABLE public.bar`, would
make `pg_restore` fail with `relation "public.bar" does not exist`. Slices 51/53
already assert the `ADD CONSTRAINT baz_x_fkey ... REFERENCES public.bar(a, b)`
*text* is present; none pinned its *position* relative to the two base tables.

**No production change needed** — goopg already splits the cross-table FK into a
post-data ALTER. Empirically verified against goopg's own pg_dump output (byte
offsets): `CREATE TABLE public.bar (` at 16740 and `CREATE TABLE public.baz (` at
16927 both precede `ADD CONSTRAINT baz_x_fkey` at 39048. The new positional
assert (1) fails if the FK offset is below either table offset and (2) confirms
the `REFERENCES public.bar` clause does not appear inside the `baz` table body
(i.e. the FK was emitted as a separate post-data statement, not inlined),
complementing the slice-132 view-ordering guard so both dependency-edge classes
pg_dump relies on are positionally locked.

### Slice 134 — `CREATE UNIQUE INDEX … NULLS NOT DISTINCT` round-trip

PostgreSQL 15 added the `NULLS NOT DISTINCT` index option: by default every NULL
in a unique key is considered distinct (so a column can hold many NULL rows), but
`NULLS NOT DISTINCT` treats NULLs as equal, allowing at most one NULL per key.
pg_dump reproduces it from `pg_index.indnullsnotdistinct` via
`pg_get_indexdef`, which appends ` NULLS NOT DISTINCT` after the column list
(ruleutils.c `pg_get_indexdef_worker`: `(cols) [INCLUDE (incl)] NULLS NOT
DISTINCT [WITH …] [WHERE …]`).

goopg's parser *accepted but discarded* the clause (`ddl.go`: "accept and
discard"), and `pg_index.indnullsnotdistinct` was hard-wired `false` in both the
virtual-catalog row builder (`catalog.go`) and the static one
(`pg18_user_catalog_rows.go`). So a `NULLS NOT DISTINCT` unique index dumped as a
plain `CREATE UNIQUE INDEX … (col)` — a silent loss of the NULL-deduplication
semantics on restore.

**Production change:** thread the flag end to end — `CreateIndexStmt.NullsNotDistinct`
(parser sets it only for `NULLS NOT DISTINCT`, leaving the default/`NULLS
DISTINCT` false) → `catalog.Index.NullsNotDistinct` (executor `execCreateIndex`)
→ `pg_index.indnullsnotdistinct` (both row builders) → `BuildIndexDef`
re-emitting the clause in ruleutils order. The test adds
`foo_nnd_idx … (name) NULLS NOT DISTINCT` and asserts the dumped DDL carries the
clause exactly once (no stray clause on the other indexes).

**Deferred (DU-002 follow-up):** *enforcement* of the NULLS-equal semantics at
INSERT/UPDATE time. `encodeIndexKeyFromCols` returns nil on any NULL key column
(so NULLs never collide); making NULLs collide for a `NullsNotDistinct` index
requires a NULL-sentinel encoding that must stay byte-consistent across the
insert-maintain, unique-check, **and** index-scan probe paths (a divergence there
would break equality SELECTs on such indexes). This slice pins the dump-fidelity
layer only; a `NULLS NOT DISTINCT` index in goopg v0 still permits multiple NULLs.
See the deferral ledger.

### Slice 135 — `UNIQUE NULLS NOT DISTINCT` table/column constraint round-trip

The CONSTRAINT sibling of slice 134. The same PostgreSQL 15+ `NULLS NOT DISTINCT`
option can be declared on a `UNIQUE` *constraint* (`CREATE TABLE t (…, UNIQUE
NULLS NOT DISTINCT (a))` / `ALTER TABLE … ADD CONSTRAINT … UNIQUE NULLS NOT
DISTINCT (a)`), not just a bare `CREATE UNIQUE INDEX`. pg_dump reproduces an
index-backed UNIQUE/PK constraint from `pg_get_constraintdef` (not
`pg_get_indexdef`), and the deparse order **differs**: ruleutils.c
`pg_get_constraintdef_worker` emits the clause **between** the keyword and the
column list — `UNIQUE NULLS NOT DISTINCT (cols)` — whereas `pg_get_indexdef`
trails it after the columns. The clause is emitted only for `CONSTRAINT_UNIQUE`,
never for a `PRIMARY KEY` (whose columns are already NOT NULL).

goopg's parser accepted-and-discarded the clause on a table-level UNIQUE (it never
reached `CreateTableStmt`), and the backing index's `NullsNotDistinct` stayed
`false`, so the constraint dumped as a plain `UNIQUE (a)` — the same silent loss
of NULL-deduplication semantics as slice 134, on the constraint surface.

**Production change:** capture the clause on the table-level UNIQUE constraint
parser path (`ddl.go`, the `UNIQUE [NULLS NOT DISTINCT] (cols)` table-constraint
case) into a new `CreateTableStmt.TableUniqueNullsNotDistinct []bool` riding
parallel to `TableUniques` (mirrors the existing `TableUniqueIncludes`); add
`TableConstraintDef.NullsNotDistinct` for the named-constraint AST; the executor's
table-UNIQUE loop (`operators_ddl.go`) threads the flag onto
`catalog.Index.NullsNotDistinct`; and `buildConstraintDefString`
(`internal/executor/expr.go`) emits ` NULLS NOT DISTINCT` between the keyword and
the column list for a non-primary UNIQUE. The round-trip test adds
`public.uniqnnd (…, UNIQUE NULLS NOT DISTINCT (a))` and asserts the dump carries
`ADD CONSTRAINT uniqnnd_a_key UNIQUE NULLS NOT DISTINCT (a)`, with the
clause-count guard tightened to exactly two (slice 134 index + slice 135
constraint) and a negative guard against the bare `UNIQUE (a)` regression.

**Deferred (DU-002 follow-up):** enforcement at INSERT/UPDATE time, identical to
slice 134 — the constraint shares the same backing-index key-encoding path
(`encodeIndexKeyFromCols`). This slice pins the dump-fidelity layer only.

### Slice 136 — inline-column `UNIQUE NULLS NOT DISTINCT` round-trip

The inline-on-column sibling of slice 135. The same PostgreSQL 15+ option can be
written directly after a column's `UNIQUE` keyword — `CREATE TABLE t (a integer
UNIQUE NULLS NOT DISTINCT, …)` — not only at the table-constraint level. pg_dump
reproduces an inline column UNIQUE as the *same* index-backed constraint a
table-level UNIQUE produces (`ALTER TABLE … ADD CONSTRAINT <table>_<col>_key
UNIQUE NULLS NOT DISTINCT (a)` via `pg_get_constraintdef`), so the dump surface
is identical to slice 135 — the new work is purely the column-form parser and
executor threading.

goopg's inline column-UNIQUE parser had **no slot** for the clause: the `KwUnique`
column-constraint case set `col.Unique = true` and stopped, so a trailing `NULLS
NOT DISTINCT` was left unconsumed (parse error) and, even once consumed, would be
dropped — the backing index's `NullsNotDistinct` stayed `false` and the
constraint dumped as a plain `UNIQUE (a)` (silent NULL-dedup loss).

**Production change:** add `ColumnDef.UniqueNullsNotDistinct bool` (`ast.go`);
parse the optional `NULLS [NOT] DISTINCT` after the inline `UNIQUE` keyword
(`ddl.go`, reusing the table-level capture pattern); the executor's inline
column-UNIQUE loop (`operators_ddl.go`) threads the flag onto
`catalog.Index.NullsNotDistinct`. The `buildConstraintDefString` deparse path is
unchanged from slice 135 (index-backed constraints share one render). The
round-trip test adds `public.uniqcnnd (a integer UNIQUE NULLS NOT DISTINCT, …)`
and asserts the dump carries `ADD CONSTRAINT uniqcnnd_a_key UNIQUE NULLS NOT
DISTINCT (a)`, with the clause-count guard tightened to exactly three (slice 134
index + slice 135 table constraint + slice 136 column constraint) and a negative
guard against the bare `UNIQUE (a)` regression.

**Deferred (DU-002 follow-up):** enforcement at INSERT/UPDATE time, as in slices
134/135 — same `encodeIndexKeyFromCols` backing-index path. Dump-fidelity only.

### Slice 137 — inline *named* column `UNIQUE` round-trip

The named sibling of slice 136. A column-level UNIQUE may carry an explicit
constraint name — `CREATE TABLE t (a integer CONSTRAINT myuniq UNIQUE NULLS NOT
DISTINCT, …)`. pg_dump emits the index-backed constraint under the **user-given**
name (`ALTER TABLE … ADD CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT (a)`), not
the auto-generated `<table>_<col>_key`.

goopg's `CONSTRAINT name UNIQUE` column-constraint case (in `parseColumnDef`'s
named-constraint switch) **absorbed the `UNIQUE` keyword without setting
`col.Unique`** — so no backing index was ever created and the constraint was
**silently dropped** from the dump entirely (a stricter failure than slice 136's
clause loss: the whole constraint vanished, not just the NULLS option). The named
PRIMARY KEY/CHECK column cases already worked; only named UNIQUE was a no-op.

**Production change:** add `ColumnDef.UniqueConstraintName string` (`ast.go`);
the named-constraint parser case (`ddl.go`) now keeps the parsed constraint name
(previously discarded), sets `col.Unique = true`, records the name on
`UniqueConstraintName`, and parses the optional `NULLS [NOT] DISTINCT` exactly as
the anonymous form. The executor's inline column-UNIQUE loop (`operators_ddl.go`)
uses `UniqueConstraintName` as the backing-index name when non-empty (which
becomes the `pg_constraint` name), falling back to the auto-generated
`<table>_<col>_key` otherwise. The `buildConstraintDefString` deparse path is
unchanged (index-backed constraints share one render). The round-trip test adds
`public.uniqcname (a integer CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT, …)` and
asserts the dump carries `ADD CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT (a)`,
with the clause-count guard tightened to exactly four and negative guards against
both the auto-generated name (`uniqcname_a_key`) and the dropped-clause regression.

**Deferred (DU-002 follow-up):** enforcement at INSERT/UPDATE time, as in slices
134/135/136 — same `encodeIndexKeyFromCols` backing-index path. Dump-fidelity only.

### Slice 138 — named *table-level* `UNIQUE NULLS NOT DISTINCT` round-trip

The table-level sibling of slice 137. A table-level UNIQUE constraint may carry an
explicit name and the PG15+ NULLS clause — `CREATE TABLE t (a integer, …,
CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a))`. pg_dump emits it under the
**user-given** name with the clause preserved: `ALTER TABLE … ADD CONSTRAINT tuniq
UNIQUE NULLS NOT DISTINCT (a)`.

goopg's **named** table-level UNIQUE parser case (`CONSTRAINT name UNIQUE (cols)`
in the table-constraint switch) **did not parse the optional `NULLS [NOT]
DISTINCT` clause** that precedes the column list — unlike the *anonymous*
table-level form, which slice 135 had already taught. So the `(` lookahead landed
on the `NULLS` token, `p.acceptSymbol("(")` returned false, and the **whole named
constraint was silently dropped** from the table (and thus the dump). Even had the
clause been parsed, the executor's `NamedConstraints` loop never threaded
`TableConstraintDef.NullsNotDistinct` to the backing index.

**Production change:** the named table-level UNIQUE parser case (`ddl.go`) now
parses the optional `NULLS [NOT] DISTINCT` before the column list, mirroring the
anonymous form, and records it on `TableConstraintDef.NullsNotDistinct` (field
already present from slice 135). The executor's `NamedConstraints` loop
(`operators_ddl.go`) sets `idx.NullsNotDistinct = nc.NullsNotDistinct` on the
backing index so `buildConstraintDefString` re-emits the clause (deparse path
unchanged — index-backed constraints share one render). The round-trip test adds
`public.uniqtname (a integer, b integer, CONSTRAINT tuniq UNIQUE NULLS NOT
DISTINCT (a))` and asserts the dump carries `ADD CONSTRAINT tuniq UNIQUE NULLS NOT
DISTINCT (a)`, with the clause-count guard tightened to exactly five and a negative
guard against the dropped-clause regression (`ADD CONSTRAINT tuniq UNIQUE (a)`).

**Deferred (DU-002 follow-up):** enforcement at INSERT/UPDATE time, as in slices
134/135/136/137 — same `encodeIndexKeyFromCols` backing-index path. Dump-fidelity only.

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
