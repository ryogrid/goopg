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
