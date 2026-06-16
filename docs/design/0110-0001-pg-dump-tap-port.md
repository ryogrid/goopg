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

Regression guard: `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`) drives real pg_dump and asserts
no `setup_connection()` error signature appears; as of slice 44 pg_dump exits
0, and as of slice 46 the test also asserts the table's archive entry **and the
full column list** (`id integer`, `name text`) are emitted; slice 48 enriched the
fixture so it also asserts the type modifiers `amount numeric(10,2)` and `code
character varying(8)` round-trip; slice 49 further asserts the PRIMARY KEY
`ADD CONSTRAINT` and the auto-named `CHECK ((qty >= 0))` constraint survive the
dump; slice 50 asserts the PK column's implicit `NOT NULL` (`id integer NOT
NULL`) survives the dump. Unit guards:
`planner.TestSRFJoinRightProjectionOffset`, `executor.TestUserPGAttributeTypmod`,
`config.TestPgDumpConnectionSetupGUCs`,
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
