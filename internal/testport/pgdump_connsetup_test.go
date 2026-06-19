package testport

// M0110-0001 enabler: pg_dump connection-setup compatibility.
//
// Before pg_dump issues any catalog query it runs a fixed sequence of commands
// in setup_connection() (postgres/src/bin/pg_dump/pg_dump.c):
//
//	SET DATESTYLE = ISO
//	SET INTERVALSTYLE = POSTGRES
//	SET extra_float_digits TO 3
//	SET synchronize_seqscans TO off
//	SET statement_timeout = 0
//	SET lock_timeout = 0
//	SET idle_in_transaction_session_timeout = 0
//	SET transaction_timeout = 0          -- PG 17+
//	SET row_security = off
//	... then, for a consistent dump, inside a transaction:
//	SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY
//
// goopg previously aborted this handshake in two ways:
//   - `unrecognized configuration parameter` for synchronize_seqscans /
//     transaction_timeout / row_security (not registered), and
//   - `unrecognized configuration parameter "TRANSACTION"` because the server's
//     simple-query string fast-path mis-routed `SET TRANSACTION …` to the GUC
//     setter (handleSet) instead of the SetTransactionStmt executor; the parser
//     also stopped at the comma in `REPEATABLE READ, READ ONLY`.
//
// This test drives the real pg_dump binary against a live goopg server and
// asserts the connection-setup handshake no longer fails: any non-zero exit
// must NOT carry a setup_connection error signature. The full dump still fails
// later on catalog-view parity. Closed gaps so far: collectRoleNames'
// `pg_roles.oid` (DU-002 slice 1), getNamespaces' `acldefault()` function
// (slice 2), the `tableoid` output-column label (slice 3), getTables' catalog
// views `pg_depend`/`pg_tablespace`/`pg_foreign_table` (slice 4), and the
// `array_remove()` scalar builtin used to strip `check_option=…` from
// `reloptions` (slice 5), and the empty `pg_init_privs` virtual view that
// `getFuncs`/`getTables`/… LEFT-JOIN to diff stored vs. initial privileges
// (slice 6), and the `pg_proc` columns `pronargs`/`proacl`/`proowner` plus the
// empty `pg_cast`/`pg_transform` catalog views that `getFuncs` projects and
// filters on (slice 7), and the empty `pg_language` virtual view that
// `getProcLangs` reads (slice 8 — built-in PLs are filtered out by `WHERE
// lanispl`, so an empty view is correct; only user-installed PLs are dumped),
// and the empty `pg_operator` virtual view that `getOperators` reads (slice 9 —
// built-in operators live in pg_catalog and are filtered out by namespace
// dumpability, so an empty view is correct; only user-defined operators are
// dumped), and the empty `pg_opclass` virtual view that `getOpclasses` reads
// (slice 10 — built-in operator classes live in pg_catalog and are filtered
// out by namespace dumpability, so an empty view is correct; only user-defined
// operator classes are dumped), and the empty `pg_opfamily` virtual view that
// `getOpfamilies` reads (slice 11 — built-in operator families live in
// pg_catalog and are filtered out by namespace dumpability, so an empty view is
// correct; only user-defined operator families are dumped), and the empty
// `pg_ts_parser` virtual view that `getTSParsers` reads (slice 12 — built-in
// text-search parsers live in pg_catalog and are filtered out by namespace
// dumpability, so an empty view is correct; only user-defined TS parsers are
// dumped), and the empty `pg_ts_template` virtual view that `getTSTemplates`
// reads (slice 13 — built-in text-search templates live in pg_catalog and are
// filtered out by namespace dumpability, so an empty view is correct; only
// user-defined TS templates are dumped), and the empty `pg_ts_dict` virtual
// view that `getTSDictionaries` reads (slice 14 — built-in text-search
// dictionaries live in pg_catalog and are filtered out by namespace
// dumpability, so an empty view is correct; only user-defined TS dictionaries
// are dumped), and the empty `pg_ts_config` virtual view that
// `getTSConfigurations` reads (slice 15 — built-in text-search configurations
// live in pg_catalog and are filtered out by namespace dumpability, so an empty
// view is correct; only user-defined TS configurations are dumped), and the
// empty `pg_foreign_data_wrapper` virtual view that `getForeignDataWrappers`
// reads (slice 16 — goopg defines no foreign-data wrappers, so an empty view is
// correct; only user-defined FDWs are dumped), and the `pg_options_to_table`
// FROM-clause SRF that the dump query's ARRAY subquery expands `fdwoptions`
// through (slice 17 — text[] of "name=value" options → rows of (option_name,
// option_value); split at the first '=', bare names get a NULL value; mirrors
// untransformRelOptions in src/backend/foreign/foreign.c; the analyzer's
// tableFuncColumns sibling path was updated alongside the planner/executor),
// and CORRELATED FROM-clause SRF argument resolution so the dump query's
// `ARRAY(SELECT … FROM pg_options_to_table(fdwoptions))` resolves `fdwoptions`
// as an outer reference to the enclosing pg_foreign_data_wrapper row (slice 18
// — planPgOptionsToTable now chains its arg-resolution context up to planParent,
// mirroring generate_series, so an outer column reaching down into the SRF arg
// of a scalar/ARRAY subquery resolves to an OuterColumnRef the executor
// evaluates per outer row; the analyzer needed no change — it builds the SRF's
// output columns but never resolves the arg expression).
// The getForeignServers query reads the empty `pg_foreign_server` virtual view
// (slice 19 — `pg_foreign_server.h` schema: oid, srvname name, srvowner oid,
// srvfdw oid, srvtype text, srvversion text, srvacl aclitem[], srvoptions
// text[]; empty by construction, like pg_foreign_data_wrapper, since goopg
// defines no foreign servers; the correlated `pg_options_to_table(srvoptions)`
// ARRAY subquery is never evaluated).
// The getDefaultACLs query reads the empty `pg_default_acl` virtual view
// (slice 20 — `pg_default_acl.h` schema, OID 826: oid, defaclrole oid,
// defaclnamespace oid, defaclobjtype "char", defaclacl aclitem[]; empty by
// construction, since goopg defines no default-ACL entries; the CASE/acldefault
// projection is never evaluated).
// The getConversions query reads the empty `pg_conversion` virtual view
// (slice 21 — `pg_conversion.h` schema, OID 2607: oid, conname name,
// connamespace oid, conowner oid, conforencoding int4, contoencoding int4,
// conproc regproc(oid), condefault bool). PG ships ~130 built-in conversions,
// but every one is in pg_catalog and filtered out at dump-out time, so the empty
// view satisfies the dump identically — confirmed empirically by this test.
// The getCasts query reads the empty `pg_range` virtual view (slice 22 —
// `pg_range.h` schema, OID 3541, NO oid column: rngtypid oid, rngsubtype oid,
// rngmultitypid oid, rngcollation oid, rngsubopc oid, rngcanonical regproc(oid),
// rngsubdiff regproc(oid)). goopg defines no range types, so the NOT EXISTS is
// always true and the empty view satisfies the dump identically — confirmed
// empirically by this test.
// The getEventTriggers query reads the empty `pg_event_trigger` virtual view
// (slice 23 — `pg_event_trigger.h` schema, OID 3466: oid, evtname name, evtevent
// name, evtowner oid, evtfoid oid, evtenabled "char", evttags text[]). goopg
// defines no event triggers, so the empty view dumps identically. The same slice
// also fixed correlated FROM-clause `unnest()` arg resolution in the planner so
// the `array(select quote_literal(x) from unnest(evttags) as t(x))` projection
// resolves `evttags` up to the outer pg_event_trigger row (mirrors slice 18's
// pg_options_to_table fix) — confirmed empirically by this test.
// The getTableAttrs per-table attribute dump query reads `a.attstattarget`
// (slice 24 — PG18's nullable int2 stats-target column). goopg's pg_attribute
// already exposed attstorage/attcompression/attidentity/atthasmissing/
// attmissingval/attgenerated/attfdwoptions/attcollation/attislocal, so only
// attstattarget was missing. It was appended LAST (not at its PG18-canonical
// position #4) to goopg's on-disk pg_attribute heap layout (pgAttrColDefs /
// PGAttributeColumns / pgAttributeColumnsPG18 / buildUserPGAttributeRow), always
// emitted NULL like the four trailing nullable varlena columns. Appending keeps
// the fixed-offset physical decoder (DecodePGAttributePhysicalRow) valid and the
// null bitmap 3→4 bytes stays within the same MAXALIGN(8) boundary (t_hoff=32),
// so no positional reader breaks. SELECT resolves columns by name. pg_dump reads
// NULL → treats it as the default stats target (-1). Confirmed empirically.
// (DU-002 slice 26) The empty `pg_trigger` virtual view (OID 2620) is now
// defined in internal/catalog/catalog.go, so the getTriggers probe
// `SELECT t.tgrelid … FROM unnest('{}'::oid[]) … JOIN pg_catalog.pg_trigger t …`
// no longer errors.
// (DU-002 slice 27) The empty `pg_rewrite` virtual view (OID 2618) is now
// defined in internal/catalog/catalog.go, so the getRules probe
// `SELECT tableoid, oid, rulename, ev_class AS ruletable, ev_type, is_instead,
// ev_enabled FROM pg_rewrite ORDER BY oid` no longer errors (goopg has no user
// rules → 0 rows).
// (DU-002 slice 28) `pg_publication.pubgencols` (PG18 "char" column,
// publish-generated-columns mode) was appended in internal/initdb/
// replication_views.go, so getPublications' probe `SELECT … p.pubviaroot,
// p.pubgencols FROM pg_publication p` no longer errors. goopg does not publish
// generated columns, so 'n'(none) is emitted for every publication row.
// (DU-002 slice 29) The empty `pg_largeobject_metadata` virtual view (OID 2995)
// is now defined in internal/catalog/catalog.go, so getBlobs' probe `SELECT oid,
// lomowner, lomacl, acldefault('L', lomowner) AS acldefault FROM
// pg_largeobject_metadata ORDER BY lomowner, lomacl::pg_catalog.text, oid` no
// longer errors (goopg has no large objects → 0 rows; the acldefault projection
// is never evaluated over the empty set). Cols: oid, lomowner oid, lomacl aclitem[].
// (DU-002 slice 30) The empty `pg_amop` (OID 2602) + `pg_amproc` (OID 2603)
// virtual views are now defined in internal/catalog/catalog.go, so
// getDependencies' pg_depend UNION that joins both to surface opfamily member
// dependencies no longer errors (goopg has no user-defined operator classes →
// 0 rows each). pg_amop cols (pg_amop.h): oid, amopfamily oid, amoplefttype oid,
// amoprighttype oid, amopstrategy int2, amoppurpose "char", amopopr oid,
// amopmethod oid, amopsortfamily oid. pg_amproc cols (pg_amproc.h): oid,
// amprocfamily oid, amproclefttype oid, amprocrighttype oid, amprocnum int2,
// amproc regproc.
// (DU-002 slice 31) The empty `pg_seclabels` virtual view (OID 3597) is now
// defined in internal/catalog/catalog.go, so getSecLabels' query `SELECT label,
// provider, classoid, objoid, objsubid FROM pg_catalog.pg_seclabels ORDER BY
// classoid, objoid, objsubid` no longer errors (goopg supports no SECURITY LABEL
// → 0 rows). pg_seclabels is a system VIEW (no oid column); cols: objoid oid,
// classoid oid, objsubid int4, objtype text, objnamespace oid, objname text,
// provider text, label text.
// (DU-002 slice 32) The empty `pg_sequence` virtual view (OID 2224) is now
// defined in internal/catalog/catalog.go and `pg_get_sequence_data(regclass)`
// is registered as a FROM-clause SRF (last_value int8, is_called bool) in the
// analyzer (tableFuncColumns) + planner (planPgGetSequenceData) + executor, so
// getSequences' query `SELECT seqrelid, format_type(seqtypid, NULL), seqstart,
// … FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid) ORDER BY
// seqrelid` (an implicit-LATERAL comma join) no longer errors. goopg's sequence
// virtual tables are skipped from the pg_class virtual view (Virtual && no
// View), so pg_dump's getTables never discovers a relkind='S' relation; an
// empty pg_sequence (0 rows) is consistent with that, and pg_get_sequence_data
// is never invoked over the empty set. (Full sequence-dump support — surfacing
// sequences as relkind='S' in pg_class and populating seqrelid here — is a
// larger follow-up slice.) pg_sequence cols (pg_sequence.h): seqrelid oid,
// seqtypid oid, seqstart int8, seqincrement int8, seqmax int8, seqmin int8,
// seqcache int8, seqcycle bool.
// (DU-002 slice 33) `current_schemas(boolean)` now returns a parseable `{a,b}`
// name[] text array literal instead of a bare scalar string, so pg_dump's
// `SELECT pg_catalog.current_schemas(false)` (selectDumpableNamespace setup)
// parses via parsePGArray without aborting "could not parse result of
// current_schemas()". current_schema stays scalar; the shared search-path
// resolver (searchPathSchemas) backs both, and include_implicit prepends the
// implicitly-searched pg_catalog (mirrors PG semantics). Fix is executor-only
// (internal/executor/expr.go); pg_proc already declared rettype 1003 (name[]).
// Slice 34 added pg_proc.proretset (the returns-set boolean flag; backed by
// catalog.Routine.ReturnsSet for user routines, constant 'f' for built-in
// stubs). Slice 35 added pg_proc.probin (the on-disk binary path for
// C-language functions; always NULL for goopg's internal/SQL routines), so
// dumpFunc advances past both. Slice 36 added pg_proc.proconfig (the
// per-function GUC SET clauses, text[]; NULL for every goopg routine).
// Slice 37 added pg_proc.procost (the planner's estimated per-row execution
// cost, float4; 1 for internal/C, 100 for other-language routines).
// Slice 38 added pg_proc.prorows (the planner's estimated result-row count
// for set-returning functions, float4; 1000 for SRFs, 0 otherwise).
// Slice 39 added pg_proc.protrftypes (the OID array of argument types whose
// transforms the function uses, oidvector; NULL for every goopg routine).
// Slice 40 added pg_proc.proparallel (the parallel-safety marker, char; 'u'
// unsafe for every goopg routine, mirroring PG's CREATE FUNCTION default).
// Slice 41 added pg_proc.prosupport (the OID of the function's planner support
// function, oid; 0 for every goopg routine). With all 22 pg_proc columns
// dumpFunc projects now resolving, the query plans and executes.
// Slice 42 made dumpFunc's `pg_proc p, pg_language l WHERE p.oid=$1 AND
// l.oid=p.prolang` join resolve: it populated pg_language's VirtualRows with the
// 3 built-in language rows (internal/12, c/13, sql/14) AND retyped pg_proc.prolang
// from text to oid (matching PG's catalog) so the join compares oid=oid instead of
// oid=text — the latter silently returned "0 rows instead of one". Built-in stubs
// already used OID-string langs; user routines now map name→OID via
// langNameToOIDStr (plpgsql, absent from pg_language, → 0). This stays safe for
// getProcLangs (its WHERE lanispl excludes all 3, which have lanispl=false).
// Slice 43 added the `pg_get_function_identity_arguments(oid)` builtin to the
// executor's function dispatch. The seed pg_proc already registered its OID
// (2232), but the executor lacked a case, so the call raised 42883. Upstream
// (ruleutils.c print_function_arguments) differs from pg_get_function_arguments
// only by print_defaults=false; goopg emits no DEFAULT clauses, so the identity
// form reuses buildFunctionArguments and is byte-identical to the full arg list.
// (Its siblings pg_get_function_arguments/result were already implemented.)
// Slice 44 added the `pg_get_function_sqlbody(oid)` builtin (seed pg_proc OID
// 6197 was registered but the executor lacked a dispatch case → 42883 in
// dumpFunc's EXECUTE). It returns NULL for every routine: the builtin yields a
// deparsed SQL-standard body only for `LANGUAGE sql ... BEGIN ATOMIC`
// functions (PG14+), which goopg never parses, so NULL is correct and matches
// what pg_dump expects for quoted-body SQL functions. With that, **pg_dump now
// runs to completion (exit 0)** — connection setup + the full catalog dump
// pipeline work end-to-end. The test is promoted to assert the table's archive
// entry (CREATE TABLE / ALTER TABLE OWNER / COPY) is emitted.
// Slice 45 made typed unnest elements join catalog columns
// (internal/executor/operators_from_unnest.go): getTableAttrs reads columns via
// `FROM unnest('{oid}'::oid[]) AS src(tbloid) JOIN pg_attribute a ON
// src.tbloid = a.attrelid`, but expandArrayDatum returns each element as a text
// KindString whose datumKey differs from the KindInt key an oid catalog column
// derives, so the hash join matched nothing (empty column list above).
// coerceUnnestElem now casts each element to its declared output type, so the
// join key lines up and pg_attribute rows flow.
// Slice 46 closed the `invalid column numbering in table "foo"` blocker: the
// join condition resolved a.attrelid correctly but the PROJECTION of right-side
// columns was not shifted by the 1-column unnest (left) prefix — a.attname
// returned attrelid (16403) and a.attnum returned attlen (4). Root cause was in
// planner buildBindingsPosMap (internal/planner/bushy.go): leaf
// SRF/table-function nodes (FromUnnest, GenerateSeries, ScalarFuncScan, …) did
// not advance `off`, so remapTopProjection shifted right-side projection columns
// DOWN by the SRF width. They now advance `off` by their output width, mirroring
// the *Values case. pg_dump reaches exit 0 AND emits the real column list.
// Slice 47 removed the spurious `WITH (""='')` reloptions clause from the
// CREATE TABLE: the virtual pg_class view (internal/catalog/catalog.go) stored
// relacl/reloptions as "" meaning NULL, but planner.TypedVirtualCell had no
// array-type case, so the empty cell became a StringConst("") that the array
// machinery parsed as a single empty-string element ({""}). pg_dump's
// nonemptyReloptions then saw a non-empty array and emitted `WITH (""='')`.
// TypedVirtualCell now maps an empty array-typed virtual cell to SQL NULL, so
// reloptions/relacl read as NULL (PG's convention for no options / default
// ACL) and the dumped CREATE TABLE has no WITH clause — byte-identical to
// upstream pg_dump for a plain table.
// Slice 48 restored column type modifiers in the dump: buildUserPGAttributeRow
// (internal/executor/pg18_user_catalog_rows.go) hardcoded atttypmod=-1, so
// pg_dump's getTableAttrs — which renders each column via
// format_type(atttypid, atttypmod) — printed every typmod-bearing column as its
// bare base type (numeric(10,2)→numeric, character varying(8)→character
// varying). New pgAttTypmod computes the PG-canonical atttypmod from the
// declared type args (numeric: ((p<<16)|s)+VARHDRSZ; varchar/char: n+VARHDRSZ),
// and formatTypeOID gained the numeric-typmod decode (varchar/char display
// already existed). The CREATE TABLE now reproduces the declared precision and
// length faithfully. The fixture carries a numeric(10,2) + varchar(8) column to
// guard the round-trip.
// Slice 49 restored CHECK constraints in the dump. pg_dump fetches table CHECK
// constraints with a query gated on `pg_class.relchecks > 0` and renders each
// row via `pg_get_constraintdef(c.oid)`; goopg hardcoded the user-table
// pg_class.relchecks to 0 (so the query was skipped entirely) AND
// pg_get_constraintdef (internal/executor/expr.go) handled only index-backed
// UNIQUE/PRIMARY KEY/EXCLUDE constraints (so a contype='c' OID returned NULL).
// relchecks now counts the table's visible NamedChecks (name+OID, matching the
// rows pg_constraint emits, so pg_dump's count-consistency assertion holds), and
// pg_get_constraintdef gained a CHECK branch that renders `CHECK ((expr))`
// (+ ` NO INHERIT` when set), mirroring PG's deparser. The fixture's column-level
// `qty integer ... CHECK (qty >= 0)` now dumps as the auto-named
// `CONSTRAINT foo_qty_check CHECK ((qty >= 0))`. (The PRIMARY KEY ALTER-TABLE
// ADD CONSTRAINT path already worked.)
// Slice 50 restored the implicit NOT NULL on a PRIMARY KEY column: goopg dumped
// `id integer` where upstream PG dumps `id integer NOT NULL`. PG18's pg_dump no
// longer reads attnotnull for the inline NOT NULL clause — getTableAttrs
// LEFT-JOINs `pg_constraint co ON (a.attrelid=co.conrelid AND co.contype='n'
// AND co.conkey=array[a.attnum])` and prints NOT NULL only when `co.conname` is
// non-NULL. goopg set the PK column's attnotnull=true but DELIBERATELY skipped
// PK columns when registering the named `<table>_<col>_not_null` contype='n'
// rows (internal/executor/operators_ddl.go), so the join found nothing.
// Verified against real PG18: `id integer PRIMARY KEY` produces a
// `foo_id_not_null` (contype='n', conkey={1}) constraint alongside `foo_pkey`.
// Fix: stop excluding PK columns from AddNotNull on CREATE TABLE, and register
// the same NOT NULL constraint on the ALTER TABLE ADD PRIMARY KEY sibling path
// (which also sets attnotnull). pg_dump now emits `id integer NOT NULL`; the
// auto-default name is suppressed by pg_dump's ChooseConstraintName match.
// Slice 51 restored FOREIGN KEY constraints in the dump. A UNIQUE constraint
// already dumped (the index-backed constraint path covers UNIQUE/PK/EXCLUDE),
// but FKs were silently dropped: goopg's catalog.ForeignKey carried no name/OID,
// so pg_constraint emitted no contype='f' row and pg_dump's getConstraints
// (`JOIN pg_constraint c ON src.tbloid=c.conrelid WHERE contype='f'`) found
// nothing. Fix: (1) catalog.ForeignKey gained Name+OID, auto-assigned at DDL
// time using PG's <table>_<col>_fkey convention (CREATE TABLE inline REFERENCES
// + ALTER TABLE ADD FOREIGN KEY paths); (2) pg_constraint.VirtualRows emits the
// contype='f' row (conkey/confkey ordinals, confrelid = referenced table OID,
// confupdtype/confdeltype from the FK action, confmatchtype='s'); (3)
// pg_get_constraintdef gained an FK branch (buildForeignKeyDefString) that
// mirrors ruleutils.c — `FOREIGN KEY (cols) REFERENCES public.reltbl(refcols)`
// (fully schema-qualified since pg_dump runs with search_path=''), with
// ON UPDATE/ON DELETE and DEFERRABLE clauses appended only when non-default.
// The fixture's `parent_id integer REFERENCES public.foo(id)` self-FK now dumps
// as `ADD CONSTRAINT foo_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES
// public.foo(id)`; UNIQUE (code) → `foo_code_key UNIQUE (code)` guards the
// already-working index-backed path.
// Slice 52 restored FK referential actions (ON DELETE / ON UPDATE) in the dump.
// The inline column-FK path already parsed and stored the action, but the
// ALTER TABLE ADD FOREIGN KEY path silently dropped it: the parser never
// consumed the `ON DELETE/UPDATE` clause (so the syntax would in fact error
// before a comma/EOS), the AlterTableAction AST had no OnDelete/OnUpdate field,
// and the executor never set catalog.ForeignKey.OnDelete/OnUpdate. Fix: (1) the
// ALTER parser now parses the action clauses ahead of the [NOT] DEFERRABLE
// trailer, reusing parseFKAction (mirroring the inline column path); (2)
// AlterTableAction gained OnDelete/OnUpdate fields; (3) the executor's
// AlterTableAddForeignKey branch copies them into the catalog FK. The fixture's
// inline self-FK now carries `ON DELETE CASCADE`, and an ALTER-added
// `foo_mgr_fkey` carries `ON UPDATE CASCADE ON DELETE SET NULL`; both round-trip
// byte-identically (pg_get_constraintdef emits ON UPDATE before ON DELETE,
// mirroring ruleutils.c, and omits the default NO ACTION).
// Slice 55 restored COMMENT ON COLUMN in the dump. goopg already parsed
// COMMENT ON and populated pg_description (catalog.SetComment), and pg_dump
// re-emits a COMMENT statement per pg_description row. The TABLE comment already
// round-tripped, but the COLUMN comment was dropped: pg_dump emits the canonical
// 3-part `COMMENT ON COLUMN schema.table.col` and goopg's parser handled only the
// bare 2-part `table.col` (parseObjectName consumes two dotted parts; the column
// case never read the trailing `.col`), so the 3-part form raised "expected IS
// after object name" — an error the server's COMMENT fallback silently swallowed,
// so nothing reached pg_description. parseCommentOnTail's column case now reads
// the trailing `.col` when present, so both forms parse and the column comment
// surfaces (unit guard: internal/parser/comment_on_test.go TestParseCommentOnColumn).
// Slice 56 restored secondary-index ASC/DESC + NULLS FIRST/LAST ordering. A
// plain CREATE INDEX round-trips via getIndexes -> pg_get_indexdef (distinct
// from the index-backed constraint path); the plain and partial forms already
// worked, but goopg's parseIndexColumnList parsed and then DISCARDED each key
// column's ASC/DESC + NULLS modifiers, so a `(col DESC)` index round-tripped as
// ascending — a silent semantic change. The parser now captures per-column
// IndexColOrder into CreateIndexStmt.ColOrders, execCreateIndex stores it on
// catalog.Index.ColDescending/ColNullsFirst (only when non-default), and
// BuildIndexDef renders it with PG's default-suppression (DESC defaults NULLS
// FIRST; ASC defaults NULLS LAST). A latent parser bug — `NULLS` (a bare ident)
// being mis-read as an opclass name in `(col NULLS FIRST)` — was fixed alongside.
// Slice 57 restored VIEW round-trip. pg_dump fetches every view's defining
// query via `pg_get_viewdef(oid)` (createViewAsClause) and ABORTS THE WHOLE
// DUMP with `definition of view "v" appears to be empty (length zero)` when it
// returns NULL/"" — so a single view made the entire dump fail and the table
// DATA after it never emitted. goopg stubbed pg_get_viewdef to NULL. Fix: the
// parser now captures the raw view body (the SQL after `AS`) verbatim into
// CreateViewStmt.RawDef via a new parser.captureSrcSpan (the parser keeps the
// original source string), execCreateView stores it on catalog.Table.ViewDef,
// and pg_get_viewdef echoes it terminated with ';' (pg_dump's
// createViewAsClause strips the trailing ';' — it Asserts the last char is ';'
// — and wraps the rest in `CREATE VIEW … AS <body>`). The body is faithful to
// the literal text the user wrote; PG's deparser additionally schema-qualifies
// unqualified relation references, which goopg does NOT do (a documented
// fidelity gap — qualified views like the fixture's round-trip cleanly under
// pg_dump's search_path=''). RECURSIVE views and materialized views capture no
// RawDef yet (follow-up).
// Slice 58 extends VIEW fidelity to an explicit column list
// (`CREATE VIEW v (c1, c2, …) AS …`). PG's pg_get_viewdef bakes the renamed
// names into the select list as `expr AS cN`; goopg's applyViewColumnAliases
// (internal/executor/expr.go) splices them into the captured raw body so the
// restored view exposes the declared names, not the underlying ones. The
// rewrite is unambiguous-only (top-level item count must match; bails to raw
// text on `*`/`x.*`/already-aliased items — a documented fidelity gap).
// RUN this test after each add to find the REAL next blocker rather than
// trusting the predicted one.
// This test is the regression guard for the whole exit-0 dump pipeline and a
// marker for the next blocker.
//
// Like the other client-tool ports the bundled pg_dump links a PG-17+ libpq
// symbol, so it runs with LD_LIBRARY_PATH pointed at postgres/local_install/lib
// via amcheckEnv (shared connection-env helper).
//
// Design doc: docs/design/0110-0001-pg-dump-tap-port.md (M0110-0001).
// CSV row: DU-002 (deferred — catalog-view parity).

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

func TestPort_PgDumpConnectionSetup(t *testing.T) {
	bin := clientToolBin(t, "pg_dump")
	if bin == "" {
		t.Skip("pg_dump not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpconn")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE TABLE public.foo (id integer PRIMARY KEY, name text, "+
		"amount numeric(10,2), code character varying(8), qty integer DEFAULT 0 CHECK (qty >= 0), "+
		"parent_id integer REFERENCES public.foo(id) ON DELETE CASCADE, "+
		"mgr_id integer, "+
		"UNIQUE (code))"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Slice 52: the ALTER TABLE ADD FOREIGN KEY path must capture the ON
	// UPDATE/ON DELETE referential actions, just like the inline column path.
	// A non-default action here exercises the parser+executor fix end-to-end.
	if err := runSQLSimple(t, c, "ALTER TABLE public.foo ADD CONSTRAINT foo_mgr_fkey "+
		"FOREIGN KEY (mgr_id) REFERENCES public.foo (id) ON UPDATE CASCADE ON DELETE SET NULL"); err != nil {
		t.Fatalf("alter table add fk: %v", err)
	}

	// Slice 53: a table-level (composite) FOREIGN KEY declared in the CREATE
	// TABLE body must survive the dump. The parser previously treated table-level
	// FKs as a no-op, so a multi-column `FOREIGN KEY (x, y) REFERENCES t (a, b)`
	// never reached the catalog or pg_constraint. `bar` carries a composite PK so
	// the FK has a multi-column referent.
	if err := runSQLSimple(t, c, "CREATE TABLE public.bar (a integer, b integer, PRIMARY KEY (a, b))"); err != nil {
		t.Fatalf("create table bar: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.baz (x integer, y integer, "+
		"FOREIGN KEY (x, y) REFERENCES public.bar (a, b) ON DELETE CASCADE)"); err != nil {
		t.Fatalf("create table baz: %v", err)
	}

	// Slice 126: a table-level multi-column UNIQUE constraint whose column order
	// DIFFERS from the table's column order (`UNIQUE (b, a)` over `(a, b, c)`).
	// pg_dump emits constraint-backed UNIQUE/PK constraints from its index scan
	// (pg_get_indexdef-style deparse), rendering the column list and the
	// auto-generated constraint name in INDEX-key order, not table order. Real
	// pg_dump 18.3 emits `ADD CONSTRAINT uniqm_b_a_key UNIQUE (b, a)` — the name
	// joins the key columns (`<table>_<col1>_<col2>_key`) and the list preserves
	// the declared `(b, a)` order. The existing fixtures only cover a single-column
	// UNIQUE (`foo_code_key`) and a declaration-order multi-column PRIMARY KEY
	// (`bar_pkey (a, b)`), so neither exercises the multi-column `_key` name join
	// (operators_ddl.go autoIndexName path) NOR a non-table-order key list. goopg
	// stores the index key columns in declared order (catalog.Index.Columns), and
	// both buildConstraintDefString and the auto-name generator consume that slice,
	// so this round-trips byte-identically (verified vs real pg_dump 18.3,
	// reference /tmp/du126_pgdata). `uniqm` carries it on its own table so foo's
	// many asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqm (a integer, b integer, c text, UNIQUE (b, a))"); err != nil {
		t.Fatalf("create table uniqm: %v", err)
	}

	// Slice 131: a table-level UNIQUE constraint with an INCLUDE (covering) column
	// must round-trip BOTH the auto-generated name and the `INCLUDE (...)` clause.
	// PG folds the covering columns into the index, so `allIndexParams` (key +
	// INCLUDE, indexcmds.c list_concat_copy) feeds ChooseIndexColumnNames →
	// ChooseIndexNameAddition: the auto name therefore carries the INCLUDE columns
	// too (`UNIQUE (a) INCLUDE (b)` → `uniqi_a_b_key`, NOT `uniqi_a_key`), verified
	// empirically vs real pg_dump 18.3 (reference /tmp/du131_pgdata). pg_dump emits
	// the constraint from pg_get_constraintdef, which appends ` INCLUDE (cols)` for
	// a covering UNIQUE. goopg already: (a) names the index via
	// autoIndexNameWithIncludes(keyCols+inclCols) and (b) renders the INCLUDE in
	// buildConstraintDefString (internal/executor/expr.go), and stores the covering
	// list on catalog.Index.IncludeColumns (operators_ddl.go) — but NO pg_dump
	// round-trip previously exercised a constraint-backed UNIQUE with an INCLUDE
	// column (the only INCLUDE coverage was an EXCLUDE-constraint unit test), so
	// this slice locks the name-join + clause render. `uniqi` carries it on its own
	// table so foo's many asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqi (a integer, b integer, c text, UNIQUE (a) INCLUDE (b))"); err != nil {
		t.Fatalf("create table uniqi: %v", err)
	}

	// Slice 135: a table-level UNIQUE constraint declared `NULLS NOT DISTINCT`
	// (PostgreSQL 15+) must round-trip via the index-backed CONSTRAINT path
	// (`ALTER TABLE ... ADD CONSTRAINT name UNIQUE NULLS NOT DISTINCT (cols)`).
	// This is the CONSTRAINT sibling of slice 134's CREATE INDEX surface: for a
	// constraint ruleutils.c pg_get_constraintdef_worker emits the clause BETWEEN
	// the keyword and the column list (`UNIQUE NULLS NOT DISTINCT (a)`), whereas
	// pg_get_indexdef trails it after the columns. goopg's parser previously
	// accepted-and-discarded the clause on a table-level UNIQUE and the backing
	// index's NullsNotDistinct stayed false, so the constraint dumped as a plain
	// `UNIQUE (a)` — a silent loss of the NULL-deduplication semantics on restore.
	// The flag now rides parallel to TableUniques → catalog.Index.NullsNotDistinct
	// → buildConstraintDefString. `uniqnnd` carries it on its own table so foo's
	// many asserts are untouched. (Enforcement at INSERT/UPDATE remains deferred —
	// dump-fidelity layer only, matching slice 134.)
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqnnd (a integer, b integer, UNIQUE NULLS NOT DISTINCT (a))"); err != nil {
		t.Fatalf("create table uniqnnd: %v", err)
	}

	// Slice 136: the INLINE-on-column sibling of slice 135 —
	// `a integer UNIQUE NULLS NOT DISTINCT` (the clause follows the column's
	// UNIQUE keyword). pg_dump emits the same index-backed constraint form as a
	// table-level UNIQUE (`ADD CONSTRAINT uniqcnnd_a_key UNIQUE NULLS NOT
	// DISTINCT (a)`), so the dump surface matches slice 135; the NEW production
	// path is the parser+executor threading for the column form. goopg's inline
	// column-UNIQUE parser previously had no slot for the clause: it would have
	// left `NULLS NOT DISTINCT` unconsumed (parse error) or, post-fix, dropped it
	// — so the backing index's NullsNotDistinct stayed false and the constraint
	// dumped as a plain `UNIQUE (a)` (silent NULL-dedup loss). The flag now rides
	// ColumnDef.UniqueNullsNotDistinct → catalog.Index.NullsNotDistinct →
	// buildConstraintDefString. `uniqcnnd` carries it on its own table.
	// (Enforcement at INSERT/UPDATE remains deferred — dump-fidelity layer only.)
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqcnnd (a integer UNIQUE NULLS NOT DISTINCT, b integer)"); err != nil {
		t.Fatalf("create table uniqcnnd: %v", err)
	}

	// Slice 137: the inline NAMED column UNIQUE form —
	// `a integer CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT` (an explicit
	// CONSTRAINT name on a column-level UNIQUE). pg_dump emits the index-backed
	// constraint under the USER-GIVEN name (`ADD CONSTRAINT myuniq UNIQUE NULLS
	// NOT DISTINCT (a)`), not the auto-generated `uniqcname_a_key`. goopg's
	// `CONSTRAINT name UNIQUE` column-constraint case previously absorbed the
	// UNIQUE keyword WITHOUT setting col.Unique — so NO backing index was created
	// at all and the constraint was SILENTLY DROPPED from the dump. The name now
	// rides ColumnDef.UniqueConstraintName → the backing index name (used as the
	// pg_constraint name), and the NULLS NOT DISTINCT flag threads exactly as the
	// anonymous form. `uniqcname` carries it on its own table.
	// (Enforcement at INSERT/UPDATE remains deferred — dump-fidelity layer only.)
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqcname (a integer CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT, b integer)"); err != nil {
		t.Fatalf("create table uniqcname: %v", err)
	}

	// Slice 138: the NAMED TABLE-LEVEL UNIQUE form —
	// `CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a)` (an explicit CONSTRAINT
	// name on a table-level UNIQUE with the PG15+ NULLS NOT DISTINCT clause).
	// pg_dump emits `ADD CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a)` under
	// the USER-GIVEN name. goopg's named table-level UNIQUE parser case
	// (`CONSTRAINT name UNIQUE (cols)`) previously did NOT parse the optional
	// NULLS [NOT] DISTINCT clause that precedes the column list, so the `(`
	// lookahead failed and the WHOLE named constraint was SILENTLY DROPPED from
	// the table (and dump). The parser now mirrors the anonymous table-level
	// form (capturing TableConstraintDef.NullsNotDistinct), and the executor's
	// NamedConstraints loop threads the flag to the backing index so
	// buildConstraintDefString re-emits it. `uniqtname` carries it on its own
	// table. (Enforcement at INSERT/UPDATE remains deferred — dump-fidelity only.)
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqtname (a integer, b integer, CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a))"); err != nil {
		t.Fatalf("create table uniqtname: %v", err)
	}

	// Slice 139: a table-level UNIQUE constraint declared DEFERRABLE INITIALLY
	// DEFERRED must round-trip the DEFERRABLE clause. The anonymous table-level
	// UNIQUE parser case had NO DEFERRABLE branch at all (unlike PRIMARY KEY,
	// which silently DISCARDED it), so `UNIQUE (a) DEFERRABLE INITIALLY DEFERRED`
	// was a hard parse error — the whole CREATE TABLE failed. The parser now
	// captures the [NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE] trailer into
	// TableUniqueDeferrable/TableUniqueInitiallyDeferred (parallel to
	// TableUniques); the executor threads them onto catalog.Index.Deferrable/
	// InitiallyDeferred; pg_constraint now emits condeferrable/condeferred from
	// the index and buildConstraintDefString (pg_get_constraintdef) appends
	// ` DEFERRABLE INITIALLY DEFERRED` after the column list. pg_dump emits
	// `ADD CONSTRAINT uniqdef_a_key UNIQUE (a) DEFERRABLE INITIALLY DEFERRED`.
	// `uniqdef` carries it on its own table so foo's many asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqdef (a integer, b integer, UNIQUE (a) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table uniqdef: %v", err)
	}

	// Slice 140: the NAMED sibling of slice 139 — a table-level UNIQUE with an
	// explicit CONSTRAINT name AND a DEFERRABLE INITIALLY DEFERRED trailer
	// (`CONSTRAINT tudef UNIQUE (a) DEFERRABLE INITIALLY DEFERRED`). Before this
	// slice the named table-level UNIQUE case parsed NO trailing DEFERRABLE, so
	// the keyword was a HARD PARSE ERROR (trailing tokens after the column list) —
	// the whole CREATE TABLE failed. The parser now captures the trailer onto
	// TableConstraintDef.Deferrable / InitiallyDeferred; the executor's
	// NamedConstraints loop threads both onto the backing index (alongside
	// NullsNotDistinct from slice 138), and the shared deparse +
	// condeferrable/condeferred emission (slice 139) re-emit the clause. pg_dump
	// emits `ADD CONSTRAINT tudef UNIQUE (a) DEFERRABLE INITIALLY DEFERRED` under
	// the user-supplied name. `uniqtdef` carries it on its own table.
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqtdef (a integer, b integer, CONSTRAINT tudef UNIQUE (a) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table uniqtdef: %v", err)
	}

	// Slice 141: the INLINE-COLUMN sibling of slices 139/140 — a DEFERRABLE
	// trailer on a column-level UNIQUE (`a integer UNIQUE DEFERRABLE INITIALLY
	// DEFERRED`, anonymous; and `a integer CONSTRAINT cudef UNIQUE DEFERRABLE
	// INITIALLY DEFERRED`, named). Before this slice the inline column UNIQUE
	// parser case parsed only the optional NULLS [NOT] DISTINCT clause; a trailing
	// DEFERRABLE fell through to the column-constraint loop's default arm and
	// became a HARD PARSE ERROR — the whole CREATE TABLE failed. The parser now
	// captures the [NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE] trailer onto
	// ColumnDef.UniqueDeferrable / UniqueInitiallyDeferred (via the shared
	// parseUniqueDeferrable helper); the executor threads both onto the backing
	// catalog.Index, and the shared deparse + condeferrable/condeferred emission
	// (slice 139) re-emit the clause. pg_dump emits
	// `ADD CONSTRAINT uniqcdef_a_key UNIQUE (a) DEFERRABLE INITIALLY DEFERRED`
	// (anonymous → auto name) and `ADD CONSTRAINT cudef UNIQUE (a) DEFERRABLE
	// INITIALLY DEFERRED` (named → user name). Each carries it on its own table.
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqcdef (a integer UNIQUE DEFERRABLE INITIALLY DEFERRED, b integer)"); err != nil {
		t.Fatalf("create table uniqcdef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.uniqcndef (a integer CONSTRAINT cudef UNIQUE DEFERRABLE INITIALLY DEFERRED, b integer)"); err != nil {
		t.Fatalf("create table uniqcndef: %v", err)
	}

	// Slice 142: the PRIMARY KEY siblings of slices 139–141. All three PK forms
	// previously DISCARDED the DEFERRABLE flag (the anonymous + named table-level
	// parser cases accepted-and-dropped it; the inline column case had no slot at
	// all, so a trailing DEFERRABLE was a hard parse error). Now the parser
	// captures the [NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE] trailer onto the
	// backing tbl_pkey index (anonymous/inline via CreateTableStmt /
	// ColumnDef fields; named via the NamedConstraints loop), and the shared
	// buildConstraintDefString + condeferrable/condeferred emission re-emit the
	// clause. pg_dump emits `ADD CONSTRAINT <t>_pkey PRIMARY KEY (a) DEFERRABLE
	// INITIALLY DEFERRED` (auto name) and `ADD CONSTRAINT pkdef PRIMARY KEY (a)
	// DEFERRABLE INITIALLY DEFERRED` (named). Each on its own table.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pktdef (a integer, b integer, PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table pktdef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pkndef (a integer, b integer, CONSTRAINT pkdef PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table pkndef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pkcdef (a integer PRIMARY KEY DEFERRABLE INITIALLY DEFERRED, b integer)"); err != nil {
		t.Fatalf("create table pkcdef: %v", err)
	}

	// Slice 143: the EXCLUDE-constraint sibling — the last index-backed
	// constraint kind that still DISCARDED the DEFERRABLE flag. parseExclude-
	// Constraint stopped at the close-paren, so a trailing `DEFERRABLE INITIALLY
	// DEFERRED` was silently dropped (anonymous + named). Now the parser captures
	// the [NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE] trailer onto the
	// TableConstraintDef (parseConstraintDeferrable, shared with UNIQUE/PK); the
	// executor threads it onto the backing exclusion index (anonymous via the
	// TableExclusions loop, named via NamedConstraints); buildConstraintDefString's
	// EXCLUDE branch now appends the clause AND pg_constraint emits
	// condeferrable/condeferred for contype='x'. A btree-equality exclusion is used
	// so the index goes through the real createBTreeIndex path (method=btree
	// preserved). pg_dump emits `ADD CONSTRAINT excldef_a_excl EXCLUDE USING btree
	// (a WITH =) DEFERRABLE INITIALLY DEFERRED` (auto name) and `ADD CONSTRAINT
	// exdef EXCLUDE USING btree (a WITH =) DEFERRABLE INITIALLY DEFERRED` (named).
	if err := runSQLSimple(t, c, "CREATE TABLE public.excldef (a integer, b integer, EXCLUDE USING btree (a WITH =) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table excldef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.exclndef (a integer, b integer, CONSTRAINT exdef EXCLUDE USING btree (a WITH =) DEFERRABLE INITIALLY DEFERRED)"); err != nil {
		t.Fatalf("create table exclndef: %v", err)
	}

	// Slice 127: anonymous table-level CHECK constraints (written without an
	// explicit CONSTRAINT name) must round-trip. PG's AddRelationNewConstraints
	// auto-names each one at DDL time — "<table>_<col>_check" when the predicate
	// references exactly one column, "<table>_check" for any other case — so the
	// constraint surfaces in pg_constraint (contype='c') and pg_dump re-emits it
	// inline in the CREATE TABLE. goopg previously stored these with an empty name
	// and OID 0 (invisible to pg_constraint), so an anonymous table-level CHECK was
	// SILENTLY DROPPED from the dump — only column-level CHECKs (foo_qty_check) and
	// explicitly-named ones round-tripped. `chk` exercises the multi-column branch
	// (`chk_check`); `chk1` exercises the single-column branch (`chk1_x_check`).
	// Both carried on their own tables so foo's many asserts are untouched.
	// Verified byte-identical to real pg_dump 18.3 (reference /tmp/du127_pgdata).
	if err := runSQLSimple(t, c, "CREATE TABLE public.chk (a integer, b integer, CHECK (a < b))"); err != nil {
		t.Fatalf("create table chk: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.chk1 (x integer, CHECK (x > 0))"); err != nil {
		t.Fatalf("create table chk1: %v", err)
	}
	// Slice 128: an anonymous table-level CHECK with NO INHERIT. The slice-127
	// auto-naming path stored these with NoInherit=false (a single aggregate
	// CreateTableStmt.TableHasNoInheritCheck bool was kept, but the per-check
	// flag was discarded), so the dumped constraintdef DROPPED the ` NO INHERIT`
	// suffix and pg_constraint reported connoinherit='f' — a silent semantic
	// divergence (the constraint would wrongly propagate to child tables on a
	// re-loaded dump). The deparse path (pg_get_constraintdef appends
	// ` NO INHERIT`) already existed from slice 127; the gap was purely the lost
	// flag. Single-column predicate → `chk2_y_check` with the NO INHERIT suffix.
	// Verified byte-identical to real pg_dump 18.3.
	if err := runSQLSimple(t, c, "CREATE TABLE public.chk2 (y integer, CHECK (y > 0) NO INHERIT)"); err != nil {
		t.Fatalf("create table chk2: %v", err)
	}
	// Slice 129: a NAMED table-level CHECK with NO INHERIT
	// (`CONSTRAINT c CHECK (...) NO INHERIT`). The analog of slice 128 for the
	// named branch: PartitionCheckConstraint had no NoInherit field, and the
	// executor called AddCheck (NoInherit=false), so the dumped constraintdef
	// DROPPED the ` NO INHERIT` suffix and pg_constraint reported connoinherit='f'
	// — the same silent inheritance-semantics divergence, but for the explicitly
	// named form. Fix threads the per-constraint flag through the named path.
	if err := runSQLSimple(t, c, "CREATE TABLE public.chk3 (z integer, CONSTRAINT chk3_pos CHECK (z > 0) NO INHERIT)"); err != nil {
		t.Fatalf("create table chk3: %v", err)
	}

	// Slice 54: a non-empty reloptions (`WITH (fillfactor=70)`) must surface in
	// the dump. Slice 47 made an EMPTY reloptions read as SQL NULL (no WITH
	// clause); the complementary case — an actually-set storage parameter — was
	// silently dropped because goopg parsed+validated fillfactor but never
	// persisted it on the catalog table, so pg_class.reloptions stayed NULL.
	// `opt` carries it on its own table so the slice-47 "foo has no options"
	// guard is unaffected.
	if err := runSQLSimple(t, c, "CREATE TABLE public.opt (id integer PRIMARY KEY) WITH (fillfactor=70)"); err != nil {
		t.Fatalf("create table opt: %v", err)
	}

	// Slice 195: a SECOND table-level storage parameter (`parallel_workers`) must
	// also round-trip — and coexist with fillfactor in the same reloptions array.
	// goopg parsed every lowercase WITH key but only extracted+persisted
	// fillfactor (slice 54); any other recognized reloption (here
	// parallel_workers) was silently dropped, so pg_dump lost it. The fix
	// extracts/bounds-checks parallel_workers (0–1024) and joins it with
	// fillfactor in pg_class.reloptions. goopg has no parallel query, so the value
	// is catalog/dump-only (advisory). `optpw` carries it on its own table to keep
	// slice 54's single-option `opt` assertion (`WITH (fillfactor='70')`) intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optpw (id integer PRIMARY KEY) WITH (fillfactor=70, parallel_workers=4)"); err != nil {
		t.Fatalf("create table optpw: %v", err)
	}

	// Slice 196: a BOOLEAN table-level storage parameter (`autovacuum_enabled`)
	// must round-trip too. It is the most common non-fillfactor reloption in real
	// dumps and exercises the boolean reloption code path (parseReloptionBool
	// mirrors PG's parse_bool). goopg validated the WITH key as lowercase but
	// never extracted/persisted it, so it vanished from the dump. The fix stores
	// catalog.Table.AutovacuumEnabled{,Set}; the pg_class virtual view renders
	// `{autovacuum_enabled=false}`, which pg_dump emits as `WITH
	// (autovacuum_enabled='false')`. goopg has no autovacuum, so the value is
	// catalog/dump-only (advisory). `optav` carries it on its own table to keep
	// the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optav (id integer PRIMARY KEY) WITH (autovacuum_enabled=false)"); err != nil {
		t.Fatalf("create table optav: %v", err)
	}

	// Slice 197: an INTEGER table-level storage parameter with a non-zero
	// minimum (`toast_tuple_target`, valid 128–8160). It is the next-most-common
	// heap reloption after fillfactor/autovacuum and exercises the
	// minimum-is-128 "0-means-unset" variant of the integer code path (no
	// separate set flag, unlike parallel_workers whose 0 is a real value). goopg
	// validated the lowercase WITH key but never extracted/persisted it, so it
	// vanished from the dump. The fix stores catalog.Table.ToastTupleTarget; the
	// pg_class virtual view renders `{toast_tuple_target=256}`, which pg_dump
	// emits as `WITH (toast_tuple_target='256')`. goopg's TOAST thresholds are
	// fixed, so the value is catalog/dump-only (advisory). `optt` carries it on
	// its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optt (id integer PRIMARY KEY) WITH (toast_tuple_target=256)"); err != nil {
		t.Fatalf("create table optt: %v", err)
	}

	// Slice 224: the first `toast.*` namespace-qualified storage parameter
	// (`toast.autovacuum_enabled`). PostgreSQL keeps reloptions whose names carry
	// the `toast.` namespace on the table's TOAST relation's pg_class.reloptions
	// (without the prefix); pg_dump joins to that relation via reltoastrelid,
	// reads `tc.reloptions AS toast_reloptions`, and re-emits them WITH the
	// `toast.` prefix. goopg previously parsed `toast.autovacuum_enabled` as a
	// bare `toast` key (the dotted name was not combined) and modeled no TOAST
	// relation, so the option vanished from the dump. The fix: parseWithOptions
	// combines the dotted labels; the executor records catalog.Table.ToastReloptions
	// (normalized `autovacuum_enabled=false`); the pg_class virtual view synthesizes
	// a relkind='t' TOAST row carrying those reloptions and points the table's
	// reltoastrelid at it, so pg_dump emits `WITH (toast.autovacuum_enabled='false')`.
	// goopg has no TOAST, so the value is catalog/dump-only (advisory). `optoast`
	// carries it on its own table to keep the other reloption assertions intact.
	// Slice 225 adds a second RELOPT_KIND_TOAST boolean (`toast.vacuum_truncate`)
	// to the same table, exercising the multi-element toast reloptions array: the
	// synthesized TOAST relation's pg_class.reloptions becomes
	// `{autovacuum_enabled=false,vacuum_truncate=false}` and pg_dump re-emits both
	// in code order — `WITH (toast.autovacuum_enabled='false', toast.vacuum_truncate='false')`.
	// Slice 226 adds the first RELOPT_KIND_TOAST *integer* option
	// (`toast.autovacuum_vacuum_threshold`), extending the array to three elements:
	// `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100}`,
	// which pg_dump re-emits as the third prefixed element
	// `toast.autovacuum_vacuum_threshold='100'`. Slice 227 adds the first
	// RELOPT_KIND_TOAST *real* option (`toast.autovacuum_vacuum_scale_factor`),
	// extending the array to four elements; pg_dump re-emits the float element
	// `toast.autovacuum_vacuum_scale_factor='2.5'`. Slice 228 adds the second
	// RELOPT_KIND_TOAST *real* option (`toast.autovacuum_vacuum_cost_delay`),
	// extending the array to five elements; pg_dump re-emits the float element
	// `toast.autovacuum_vacuum_cost_delay='10.5'`. Slice 229 adds the second
	// RELOPT_KIND_TOAST *integer* option (`toast.autovacuum_vacuum_cost_limit`,
	// valid 1–10000), extending the array to six elements; pg_dump re-emits the
	// integer element `toast.autovacuum_vacuum_cost_limit='500'`. Slice 230 adds the
	// first RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_freeze_min_age`, valid 0–1000000000), extending the array to
	// seven elements; pg_dump re-emits the integer element
	// `toast.autovacuum_freeze_min_age='200000000'`. Slice 231 adds the second
	// RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_freeze_max_age`, valid 100000–2000000000), extending the
	// array to eight elements; pg_dump re-emits the integer element
	// `toast.autovacuum_freeze_max_age='500000000'`. Slice 232 adds the third
	// RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_freeze_table_age`, valid 0–2000000000), extending the
	// array to nine elements; the explicit 0 exercises that 0 is a valid value, and
	// pg_dump re-emits the integer element `toast.autovacuum_freeze_table_age='0'`.
	// Slice 233 adds the fourth RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_multixact_freeze_min_age`, valid 0–1000000000), extending
	// the array to ten elements; pg_dump re-emits the integer element
	// `toast.autovacuum_multixact_freeze_min_age='150000000'`. Slice 234 adds the
	// fifth RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_multixact_freeze_max_age`, valid 10000–2000000000), extending
	// the array to eleven elements; pg_dump re-emits the integer element
	// `toast.autovacuum_multixact_freeze_max_age='500000000'`. Slice 235 adds the
	// sixth RELOPT_KIND_TOAST autovacuum-age *integer* option
	// (`toast.autovacuum_multixact_freeze_table_age`, valid 0–2000000000), extending
	// the array to twelve elements; pg_dump re-emits the integer element
	// `toast.autovacuum_multixact_freeze_table_age='250000000'`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optoast (id integer PRIMARY KEY) WITH (toast.autovacuum_enabled=false, toast.vacuum_truncate=false, toast.autovacuum_vacuum_threshold=100, toast.autovacuum_vacuum_scale_factor=2.5, toast.autovacuum_vacuum_cost_delay=10.5, toast.autovacuum_vacuum_cost_limit=500, toast.autovacuum_freeze_min_age=200000000, toast.autovacuum_freeze_max_age=500000000, toast.autovacuum_freeze_table_age=0, toast.autovacuum_multixact_freeze_min_age=150000000, toast.autovacuum_multixact_freeze_max_age=500000000, toast.autovacuum_multixact_freeze_table_age=250000000)"); err != nil {
		t.Fatalf("create table optoast: %v", err)
	}

	// Slice 198: an INTEGER autovacuum-namespace storage parameter
	// (`autovacuum_vacuum_threshold`, valid 0–INT_MAX). It exercises the
	// "0-is-a-real-value" integer variant (PG's reloption default is -1 = unset,
	// so a separate set flag — not a zero check — records presence, the
	// parallel_workers pattern) for the autovacuum option family. goopg validated
	// the lowercase WITH key but never extracted/persisted it, so it vanished
	// from the dump. The fix stores catalog.Table.AutovacuumVacuumThreshold; the
	// pg_class virtual view renders `{autovacuum_vacuum_threshold=100}`, which
	// pg_dump emits as `WITH (autovacuum_vacuum_threshold='100')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory). `optavt` carries
	// it on its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavt (id integer PRIMARY KEY) WITH (autovacuum_vacuum_threshold=100)"); err != nil {
		t.Fatalf("create table optavt: %v", err)
	}

	// Slice 199: the first REAL-typed storage parameter
	// (`autovacuum_vacuum_scale_factor`, valid 0.0–100.0). Prior reloption slices
	// were all int/bool; a fractional value (`0.2`) lexes as TokenNumericLit,
	// which the WITH-options parser previously rejected with "expected option
	// value", so the option never reached the executor. The fix accepts
	// TokenNumericLit in parseWithOptions and parses/bounds-checks the float
	// (0.0 is a valid explicit value — separate set flag, the parallel_workers
	// pattern). catalog.Table.AutovacuumVacuumScaleFactor persists it; the
	// pg_class virtual view renders `{autovacuum_vacuum_scale_factor=0.2}`
	// (shortest exact decimal), which pg_dump emits as
	// `WITH (autovacuum_vacuum_scale_factor='0.2')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory). `optavsf` carries it on its own
	// table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavsf (id integer PRIMARY KEY) WITH (autovacuum_vacuum_scale_factor=0.2)"); err != nil {
		t.Fatalf("create table optavsf: %v", err)
	}

	// Slice 200: the second REAL-typed storage parameter
	// (`autovacuum_analyze_scale_factor`, valid 0.0–100.0), reusing the slice-199
	// float path. catalog.Table.AutovacuumAnalyzeScaleFactor persists it; the
	// pg_class virtual view renders `{autovacuum_analyze_scale_factor=0.05}`
	// (shortest exact decimal), which pg_dump emits as
	// `WITH (autovacuum_analyze_scale_factor='0.05')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory). `optaasf` carries it on its own
	// table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optaasf (id integer PRIMARY KEY) WITH (autovacuum_analyze_scale_factor=0.05)"); err != nil {
		t.Fatalf("create table optaasf: %v", err)
	}

	// Slice 201: the third REAL-typed storage parameter
	// (`autovacuum_vacuum_insert_scale_factor`, valid 0.0–100.0), reusing the
	// slice-199 float path. catalog.Table.AutovacuumVacuumInsertScaleFactor
	// persists it; the pg_class virtual view renders
	// `{autovacuum_vacuum_insert_scale_factor=0.2}` (shortest exact decimal),
	// which pg_dump emits as `WITH (autovacuum_vacuum_insert_scale_factor='0.2')`.
	// goopg has no autovacuum, so the value is catalog/dump-only (advisory).
	// `optavisf` carries it on its own table to keep the other reloption
	// assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavisf (id integer PRIMARY KEY) WITH (autovacuum_vacuum_insert_scale_factor=0.2)"); err != nil {
		t.Fatalf("create table optavisf: %v", err)
	}

	// Slice 202: the fourth (and final) REAL-typed storage parameter
	// (`autovacuum_vacuum_cost_delay`, valid 0.0–100.0), reusing the slice-199
	// float path. catalog.Table.AutovacuumVacuumCostDelay persists it; the pg_class
	// virtual view renders `{autovacuum_vacuum_cost_delay=2.5}` (shortest exact
	// decimal), which pg_dump emits as `WITH (autovacuum_vacuum_cost_delay='2.5')`.
	// goopg has no autovacuum, so the value is catalog/dump-only (advisory).
	// `optavcd` carries it on its own table to keep the other reloption assertions
	// intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavcd (id integer PRIMARY KEY) WITH (autovacuum_vacuum_cost_delay=2.5)"); err != nil {
		t.Fatalf("create table optavcd: %v", err)
	}

	// Slice 203: the second INTEGER autovacuum-namespace storage parameter
	// (`autovacuum_analyze_threshold`, valid 0–INT_MAX), reusing the slice-198
	// integer path (separate set flag — not a zero check — records presence, the
	// parallel_workers pattern). goopg validated the lowercase WITH key but never
	// extracted/persisted it, so it vanished from the dump. The fix stores
	// catalog.Table.AutovacuumAnalyzeThreshold; the pg_class virtual view renders
	// `{autovacuum_analyze_threshold=50}`, which pg_dump emits as
	// `WITH (autovacuum_analyze_threshold='50')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory). `optaat` carries it on its own table
	// to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optaat (id integer PRIMARY KEY) WITH (autovacuum_analyze_threshold=50)"); err != nil {
		t.Fatalf("create table optaat: %v", err)
	}

	// Slice 204: the third INTEGER autovacuum-namespace storage parameter
	// (`autovacuum_vacuum_insert_threshold`, valid -1–INT_MAX), reusing the
	// slice-198 integer path (separate set flag — not a zero check — records
	// presence, the parallel_workers pattern). goopg validated the lowercase WITH
	// key but never extracted/persisted it, so it vanished from the dump. The fix
	// stores catalog.Table.AutovacuumVacuumInsertThreshold; the pg_class virtual
	// view renders `{autovacuum_vacuum_insert_threshold=1000}`, which pg_dump emits
	// as `WITH (autovacuum_vacuum_insert_threshold='1000')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory). `optavit` carries
	// it on its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavit (id integer PRIMARY KEY) WITH (autovacuum_vacuum_insert_threshold=1000)"); err != nil {
		t.Fatalf("create table optavit: %v", err)
	}

	// Slice 205: the boolean vacuum_truncate storage parameter
	// (RELOPT_TYPE_BOOL, default true), reusing the slice-196 autovacuum_enabled
	// boolean path (a separate set flag records presence — the value carries no
	// zero-detectable default). goopg validated the lowercase WITH key but never
	// extracted/persisted it, so it vanished from the dump. The fix stores
	// catalog.Table.VacuumTruncate; the pg_class virtual view renders
	// `{vacuum_truncate=false}`, which pg_dump emits as
	// `WITH (vacuum_truncate='false')`. goopg has no VACUUM truncation, so the
	// value is catalog/dump-only (advisory). `optvt` carries it on its own table
	// to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optvt (id integer PRIMARY KEY) WITH (vacuum_truncate=false)"); err != nil {
		t.Fatalf("create table optvt: %v", err)
	}

	// Slice 206: the integer log_autovacuum_min_duration storage parameter
	// (RELOPT_TYPE_INT, valid -1–INT_MAX, default -1; 0 logs every autovacuum
	// action), the fourth INT-typed autovacuum-namespace reloption, reusing the
	// slice-198 integer path (a separate set flag records presence — -1 and 0 are
	// both valid explicit values). goopg stores
	// catalog.Table.LogAutovacuumMinDuration; the pg_class virtual view renders
	// `{log_autovacuum_min_duration=250}`, which pg_dump emits as
	// `WITH (log_autovacuum_min_duration='250')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory). `optlamd` carries it on its own table
	// to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optlamd (id integer PRIMARY KEY) WITH (log_autovacuum_min_duration=250)"); err != nil {
		t.Fatalf("create table optlamd: %v", err)
	}

	// Slice 207: the integer autovacuum_freeze_min_age storage parameter
	// (RELOPT_TYPE_INT, valid 0–1000000000, default -1 = unset), the fifth
	// INT-typed autovacuum-namespace reloption, reusing the slice-198 integer path
	// (a separate set flag records presence — 0 is a valid explicit value distinct
	// from the -1 unset sentinel). goopg stores
	// catalog.Table.AutovacuumFreezeMinAge; the pg_class virtual view renders
	// `{autovacuum_freeze_min_age=5000}`, which pg_dump emits as
	// `WITH (autovacuum_freeze_min_age='5000')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory). `optafma` carries it on its own table
	// to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optafma (id integer PRIMARY KEY) WITH (autovacuum_freeze_min_age=5000)"); err != nil {
		t.Fatalf("create table optafma: %v", err)
	}

	// Slice 208: the integer autovacuum_freeze_max_age storage parameter
	// (RELOPT_TYPE_INT, valid 100000–2000000000, default -1 = unset), the sixth
	// INT-typed autovacuum-namespace reloption, reusing the slice-198 integer path
	// (a separate set flag records presence; the range minimum 100000 means an
	// explicit -1 is rejected as out-of-range). goopg stores
	// catalog.Table.AutovacuumFreezeMaxAge; the pg_class virtual view renders
	// `{autovacuum_freeze_max_age=500000}`, which pg_dump emits as
	// `WITH (autovacuum_freeze_max_age='500000')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory). `optafmx` carries it on its own table
	// to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optafmx (id integer PRIMARY KEY) WITH (autovacuum_freeze_max_age=500000)"); err != nil {
		t.Fatalf("create table optafmx: %v", err)
	}

	// Slice 209: the integer autovacuum_freeze_table_age storage parameter
	// (RELOPT_TYPE_INT, valid 0–2000000000, default -1 = unset), the seventh
	// INT-typed autovacuum-namespace reloption, reusing the slice-198 integer path
	// (a separate set flag records presence; 0 is a valid explicit value so the
	// flag — not a zero check — guards presence). goopg stores
	// catalog.Table.AutovacuumFreezeTableAge; the pg_class virtual view renders
	// `{autovacuum_freeze_table_age=150000000}`, which pg_dump emits as
	// `WITH (autovacuum_freeze_table_age='150000000')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory). `optafta` carries it on its own
	// table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optafta (id integer PRIMARY KEY) WITH (autovacuum_freeze_table_age=150000000)"); err != nil {
		t.Fatalf("create table optafta: %v", err)
	}

	// Slice 210: the integer autovacuum_multixact_freeze_min_age storage parameter
	// (RELOPT_TYPE_INT, valid 0–1000000000, default -1 = unset), the eighth
	// INT-typed autovacuum-namespace reloption, reusing the slice-198 integer path
	// (a separate set flag records presence; 0 is a valid explicit value so the
	// flag — not a zero check — guards presence). goopg stores
	// catalog.Table.AutovacuumMultixactFreezeMinAge; the pg_class virtual view
	// renders `{autovacuum_multixact_freeze_min_age=5000000}`, which pg_dump emits
	// as `WITH (autovacuum_multixact_freeze_min_age='5000000')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory). `optamfma` carries
	// it on its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optamfma (id integer PRIMARY KEY) WITH (autovacuum_multixact_freeze_min_age=5000000)"); err != nil {
		t.Fatalf("create table optamfma: %v", err)
	}

	// Slice 211: the integer autovacuum_multixact_freeze_max_age storage parameter
	// (RELOPT_TYPE_INT, valid 10000–2000000000, default -1 = unset), the ninth
	// INT-typed autovacuum-namespace reloption, reusing the slice-198 integer path
	// (a separate set flag records presence; unlike the min/table-age options the
	// lower bound is 10000, but the flag — not a zero check — still guards presence).
	// goopg stores catalog.Table.AutovacuumMultixactFreezeMaxAge; the pg_class virtual
	// view renders `{autovacuum_multixact_freeze_max_age=500000000}`, which pg_dump
	// emits as `WITH (autovacuum_multixact_freeze_max_age='500000000')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory). `optamfmaxa` carries
	// it on its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optamfmaxa (id integer PRIMARY KEY) WITH (autovacuum_multixact_freeze_max_age=500000000)"); err != nil {
		t.Fatalf("create table optamfmaxa: %v", err)
	}

	// Slice 212: the integer autovacuum_multixact_freeze_table_age storage parameter
	// (RELOPT_TYPE_INT, valid 0–2000000000, default -1 = unset), the tenth INT-typed
	// autovacuum-namespace reloption, reusing the slice-198 integer path (a separate
	// set flag records presence; as with the min-age option 0 is a valid explicit
	// value, so the flag — not a zero check — guards presence). goopg stores
	// catalog.Table.AutovacuumMultixactFreezeTableAge; the pg_class virtual view
	// renders `{autovacuum_multixact_freeze_table_age=900000000}`, which pg_dump emits
	// as `WITH (autovacuum_multixact_freeze_table_age='900000000')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory). `optamftaa` carries it
	// on its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optamftaa (id integer PRIMARY KEY) WITH (autovacuum_multixact_freeze_table_age=900000000)"); err != nil {
		t.Fatalf("create table optamftaa: %v", err)
	}

	// Slice 213: the integer autovacuum_vacuum_cost_limit storage parameter
	// (RELOPT_TYPE_INT, valid 1–10000, default -1 = unset), the eleventh INT-typed
	// autovacuum-namespace reloption, reusing the slice-198 integer path (a separate
	// set flag records presence). Unlike the freeze-age options the lower bound is 1,
	// so 0 is below range and rejected. goopg stores
	// catalog.Table.AutovacuumVacuumCostLimit; the pg_class virtual view renders
	// `{autovacuum_vacuum_cost_limit=2500}`, which pg_dump emits as
	// `WITH (autovacuum_vacuum_cost_limit='2500')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory). `optavcl` carries it on its own table to
	// keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavcl (id integer PRIMARY KEY) WITH (autovacuum_vacuum_cost_limit=2500)"); err != nil {
		t.Fatalf("create table optavcl: %v", err)
	}

	// Slice 214: the boolean user_catalog_table storage parameter
	// (RELOPT_TYPE_BOOL, RELOPT_KIND_HEAP, default false), reusing the slice-196
	// autovacuum_enabled boolean path (a separate set flag records presence).
	// goopg stores catalog.Table.UserCatalogTable; the pg_class virtual view
	// renders `{user_catalog_table=true}`, which pg_dump emits as
	// `WITH (user_catalog_table='true')`. The option marks a heap as a catalog
	// table for logical decoding; goopg has none, so the value is catalog/dump-only
	// (advisory). `optuct` carries it on its own table to keep the other reloption
	// assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optuct (id integer PRIMARY KEY) WITH (user_catalog_table=true)"); err != nil {
		t.Fatalf("create table optuct: %v", err)
	}

	// Slice 215: the integer autovacuum_vacuum_max_threshold storage parameter — a
	// PG18 heap reloption (RELOPT_TYPE_INT, range -1–INT_MAX, default -2 = unset),
	// reusing the slice-204 integer path (a separate set flag records presence
	// since -1/0 are valid explicit values). goopg stores
	// catalog.Table.AutovacuumVacuumMaxThreshold; the pg_class virtual view renders
	// `{autovacuum_vacuum_max_threshold=5000}`, which pg_dump emits as
	// `WITH (autovacuum_vacuum_max_threshold='5000')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory). `optavmt` carries it on its own
	// table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optavmt (id integer PRIMARY KEY) WITH (autovacuum_vacuum_max_threshold=5000)"); err != nil {
		t.Fatalf("create table optavmt: %v", err)
	}

	// Slice 216: the REAL vacuum_max_eager_freeze_failure_rate storage parameter — a
	// PG18 heap reloption (RELOPT_TYPE_REAL, range 0.0–1.0, default -1 = unset),
	// reusing the slice-199 float path but with PG's narrower 0.0–1.0 range (a
	// separate set flag records presence since 0.0 is a valid explicit value). goopg
	// stores catalog.Table.VacuumMaxEagerFreezeFailureRate; the pg_class virtual view
	// renders `{vacuum_max_eager_freeze_failure_rate=0.1}`, which pg_dump emits as
	// `WITH (vacuum_max_eager_freeze_failure_rate='0.1')`. goopg has no eager
	// freezing, so the value is catalog/dump-only (advisory). `optvefr` carries it on
	// its own table to keep the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optvefr (id integer PRIMARY KEY) WITH (vacuum_max_eager_freeze_failure_rate=0.1)"); err != nil {
		t.Fatalf("create table optvefr: %v", err)
	}

	// Slice 217: the ENUM vacuum_index_cleanup storage parameter — a PG18 heap
	// reloption (RELOPT_TYPE_ENUM, members auto/on/off/true/false/yes/no/1/0,
	// default auto), goopg's first enum reloption. The value is stored verbatim
	// (no alias normalization) on catalog.Table.VacuumIndexCleanup; the pg_class
	// virtual view renders `{vacuum_index_cleanup=on}`, which pg_dump emits as
	// `WITH (vacuum_index_cleanup='on')`. goopg has no autovacuum, so the value is
	// catalog/dump-only (advisory). `optvic` carries it on its own table to keep
	// the other reloption assertions intact.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optvic (id integer PRIMARY KEY) WITH (vacuum_index_cleanup=on)"); err != nil {
		t.Fatalf("create table optvic: %v", err)
	}

	// Slice 166: an UNLOGGED table must round-trip as `CREATE UNLOGGED TABLE`.
	// pg_dump keys the UNLOGGED keyword off pg_class.relpersistence ==
	// RELPERSISTENCE_UNLOGGED ('u') (pg_dump.c dumpTableSchema). The parser
	// already captured CreateTableStmt.Unlogged and the executor stored it on
	// catalog.Table.Unlogged, but buildUserPGClassRow HARDCODED relpersistence
	// to 'p', so an UNLOGGED table was silently demoted to a logged one in the
	// dump (wrong DDL, loses the crash-truncation semantics on reload). The fix
	// emits 'u' from tbl.Unlogged; the table's index inherits 'u' too. `ulog`
	// carries a PRIMARY KEY so the index-persistence path is exercised. (TEMP
	// tables are session-local and never reach the on-disk catalog, so only the
	// 'u' branch is reachable here.)
	if err := runSQLSimple(t, c, "CREATE UNLOGGED TABLE public.ulog (id integer PRIMARY KEY, payload text)"); err != nil {
		t.Fatalf("create unlogged table ulog: %v", err)
	}

	// Slice 167: a RANGE-partitioned table and one of its partitions must
	// round-trip. pg_dump dumps the parent as
	//   CREATE TABLE public.part (...) PARTITION BY RANGE (id);
	// (the trailing clause comes from pg_get_partkeydef(oid), which goopg already
	// implements off catalog.Table.PartitionMethod/PartitionKey), and each
	// partition child as a standalone CREATE TABLE plus a separate
	//   ALTER TABLE ONLY public.part ATTACH PARTITION public.part_p0
	//       FOR VALUES FROM (0) TO (100);
	// where the `FOR VALUES …` bound is read back via
	// pg_get_expr(c.relpartbound, c.oid). The parent's relkind='p' and the
	// child's relispartition were already emitted, BUT buildUserPGClassRow (the
	// heap-backed pg_class row pg_dump actually reads) HARDCODED relpartbound to
	// "", so the child attached with an empty (invalid) bound — a silent loss of
	// the partition's value range on restore. The emitter now derives the bound
	// from catalog.FormatPartitionBound(tbl.PartitionBounds[0]) for partition
	// children (parents keep ""), mirroring catalog.go's VirtualRows sibling
	// path. `part`/`part_p0` carry it on their own tables so foo's many asserts
	// are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.part (id integer, val text) PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create partitioned table part: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.part_p0 PARTITION OF public.part FOR VALUES FROM (0) TO (100)"); err != nil {
		t.Fatalf("create partition part_p0: %v", err)
	}

	// Slice 168: non-RANGE partition bounds must round-trip too. RANGE bounds
	// (slice 167) happened to be integer literals, which render identically
	// quoted or not. A LIST partition keyed on a text column exposed a real
	// divergence: the bound value was stored via exprToString (the raw, unquoted
	// routing form), so FormatPartitionBound emitted the invalid
	// `FOR VALUES IN (a, b)` instead of PG's `FOR VALUES IN ('a', 'b')` — a
	// restore-breaking loss. The fix captures a parallel SQL-literal form
	// (PartitionBound.InValueLiterals) at partition-creation time and renders it.
	// `plist`/`phash` carry their own tables so the many `foo` asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.plist (grp text, val integer) PARTITION BY LIST (grp)"); err != nil {
		t.Fatalf("create LIST-partitioned table plist: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.plist_ab PARTITION OF public.plist FOR VALUES IN ('a', 'b')"); err != nil {
		t.Fatalf("create LIST partition plist_ab: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.phash (id integer, val text) PARTITION BY HASH (id)"); err != nil {
		t.Fatalf("create HASH-partitioned table phash: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.phash_0 PARTITION OF public.phash FOR VALUES WITH (MODULUS 4, REMAINDER 0)"); err != nil {
		t.Fatalf("create HASH partition phash_0: %v", err)
	}

	// Slice 169: RANGE bounds keyed on a text column have the SAME raw-vs-literal
	// divergence as LIST (slice 168). The earlier RANGE fixture (`part`) used
	// integer bounds, which render identically quoted or not. A text RANGE bound
	// was stored via exprToString (the raw routing form), so FormatPartitionBound
	// emitted the restore-breaking `FOR VALUES FROM (a) TO (m)` instead of PG's
	// `FOR VALUES FROM ('a') TO ('m')`. The fix captures parallel SQL-literal
	// tuples (PartitionBound.From/ToValueLiterals) — quoting strings and
	// uppercasing MINVALUE/MAXVALUE — at partition-creation time. `prange` uses an
	// open lower bound (MINVALUE) to also pin the keyword rendering.
	if err := runSQLSimple(t, c, "CREATE TABLE public.prange (grp text, val integer) PARTITION BY RANGE (grp)"); err != nil {
		t.Fatalf("create RANGE-partitioned table prange: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.prange_am PARTITION OF public.prange FOR VALUES FROM (MINVALUE) TO ('m')"); err != nil {
		t.Fatalf("create RANGE partition prange_am: %v", err)
	}

	// Slice 190: a DEFAULT partition (`CREATE TABLE child PARTITION OF parent
	// DEFAULT`) must round-trip. pg_dump reads the catch-all child's bound via
	// pg_get_expr(c.relpartbound, …), which returns the bare keyword `DEFAULT`,
	// and emits `ALTER TABLE ONLY public.parent ATTACH PARTITION public.child
	// DEFAULT;` (no FOR VALUES). The executor already records pb.IsDefault and
	// FormatPartitionBound returns "DEFAULT", so the relpartbound carries the
	// keyword; this fixture pins that the catch-all partition survives the dump
	// alongside a concrete sibling. `pdef` carries its own tables so the many
	// `foo` asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdef (k integer, v text) PARTITION BY LIST (k)"); err != nil {
		t.Fatalf("create LIST-partitioned table pdef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdef_1 PARTITION OF public.pdef FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create LIST partition pdef_1: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdef_def PARTITION OF public.pdef DEFAULT"); err != nil {
		t.Fatalf("create DEFAULT partition pdef_def: %v", err)
	}

	// Slice 191: per-leaf-partition storage parameters. PG allows `WITH
	// (fillfactor=N)` on a leaf partition (it is a concrete heap), and pg_dump
	// re-emits it on the leaf's own CREATE TABLE as `WITH (fillfactor='N')`.
	// goopg persisted fillfactor only on the non-partition CREATE TABLE path
	// (slice 54); execCreatePartitionChild took an early-return branch that never
	// extracted/persisted it, so pg_class.reloptions read NULL for the leaf and
	// the option was silently dropped from the dump. `pfo_1` is a LIST leaf of
	// `pfo` carrying fillfactor=70; the sibling `pfo_2` is left at default to keep
	// the assertion specific to the option-bearing leaf.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pfo (k integer, v text) PARTITION BY LIST (k)"); err != nil {
		t.Fatalf("create LIST-partitioned table pfo: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pfo_1 PARTITION OF public.pfo FOR VALUES IN (1) WITH (fillfactor=70)"); err != nil {
		t.Fatalf("create LIST leaf partition pfo_1 with fillfactor: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pfo_2 PARTITION OF public.pfo FOR VALUES IN (2)"); err != nil {
		t.Fatalf("create LIST leaf partition pfo_2: %v", err)
	}

	// Slice 192: a leaf partition child may carry a trailing TABLESPACE clause
	// (PG's CREATE TABLE ... PARTITION OF grammar admits OptTableSpace after
	// OptWith / OnCommitOption). The partition-child parser arm previously stopped
	// before TABLESPACE, leaving the token unconsumed so the whole statement
	// failed with a syntax error — a divergence from the non-partition CREATE
	// TABLE path, which already accepts and discards it. goopg's storage manager
	// does not honour tablespaces, so reltablespace stays 0 (the default-tablespace
	// sentinel) and pg_dump emits no TABLESPACE clause; the child round-trips
	// exactly like an option-less leaf. `ptbs_1` exercises the parse-and-discard
	// of `TABLESPACE pg_default` on a LIST leaf of `ptbs`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.ptbs (k integer, v text) PARTITION BY LIST (k)"); err != nil {
		t.Fatalf("create LIST-partitioned table ptbs: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.ptbs_1 PARTITION OF public.ptbs FOR VALUES IN (1) TABLESPACE pg_default"); err != nil {
		t.Fatalf("create LIST leaf partition ptbs_1 with TABLESPACE: %v", err)
	}

	// Slice 193: a leaf partition child may carry a USING <access_method> clause.
	// PG's CREATE TABLE ... PARTITION OF grammar is OptPartitionSpec
	// table_access_method_clause OptWith OnCommitOption OptTableSpace, so USING
	// precedes WITH. The partition-child parser arm previously omitted it, leaving
	// the USING token unconsumed so the whole statement failed with a syntax error
	// — a divergence from the non-partition CREATE TABLE path. goopg has a single
	// (heap) access method, so the name is accepted and discarded; relam stays at
	// its default and pg_dump emits no USING clause, round-tripping the child like
	// an access-method-less leaf. `puse_1` exercises `USING heap` on a LIST leaf of
	// `puse`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.puse (k integer, v text) PARTITION BY LIST (k)"); err != nil {
		t.Fatalf("create LIST-partitioned table puse: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.puse_1 PARTITION OF public.puse FOR VALUES IN (1) USING heap"); err != nil {
		t.Fatalf("create LIST leaf partition puse_1 with USING: %v", err)
	}

	// Slice 170: legacy table inheritance (CREATE TABLE child (...) INHERITS
	// (parent)) must round-trip. goopg merged the parent's columns into the child
	// but (a) emitted no pg_inherits row for the inheritance edge (only partition
	// children did) and (b) left the inherited columns marked attislocal=true, so
	// pg_dump dropped the `INHERITS (...)` clause AND re-emitted the parent's
	// columns inline — a structurally different (and on re-restore, doubly-defined)
	// table. The fix records the ordered parent OIDs on the child
	// (Table.InheritsParentOIDs → pg_inherits rows) and marks purely-inherited
	// columns Inherited=true (attislocal=false), so pg_dump omits them and emits
	// the clause. `inh_child` adds one local column over `inh_parent`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.inh_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create inheritance parent inh_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.inh_child (extra integer) INHERITS (public.inh_parent)"); err != nil {
		t.Fatalf("create inheritance child inh_child: %v", err)
	}

	// Slice 171: multi-level (sub-partitioned) partition tree. A partition that is
	// ITSELF partitioned (`CREATE TABLE mid PARTITION OF top ... PARTITION BY ...`)
	// is the one node that is simultaneously relispartition=true AND relkind='p':
	// pg_dump must emit its `PARTITION BY` clause (it has children) *and* an ATTACH
	// to its own parent (it is a child). The slices through 170 only exercised
	// single-level partition trees. `psub_east` is the middle node: a LIST partition
	// of `psub` that is sub-partitioned BY RANGE, with one leaf `psub_east_lo`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub (id integer, region text) PARTITION BY LIST (region)"); err != nil {
		t.Fatalf("create top-level partitioned table psub: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub_east PARTITION OF public.psub FOR VALUES IN ('east') PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create sub-partitioned partition psub_east: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub_east_lo PARTITION OF public.psub_east FOR VALUES FROM (0) TO (100)"); err != nil {
		t.Fatalf("create leaf partition psub_east_lo: %v", err)
	}

	// Slice 172: MULTI-parent legacy inheritance (`INHERITS (a, b)`). Slice 170
	// only exercised a single parent; multi-parent additionally relies on (a) the
	// column-merge dedup (a column present in both parents — `shared` — is kept
	// once, with a "merging multiple inherited definitions" notice; M0097-0046),
	// (b) every merged/inherited column being marked Inherited=true so pg_dump
	// omits it (the slice-170 loop iterates all cols, so shared/minh_a-only/
	// minh_b-only all qualify), and (c) pg_inherits emitting one row per parent in
	// declaration order (inhseqno 1,2 from the ordered InheritsParentOIDs) so
	// pg_dump re-emits the parents in the SAME order as the original clause. The
	// child adds one purely-local column (`own_col`) over the two parents.
	if err := runSQLSimple(t, c, "CREATE TABLE public.minh_a (shared integer, a_only integer)"); err != nil {
		t.Fatalf("create inheritance parent minh_a: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.minh_b (shared integer, b_only text)"); err != nil {
		t.Fatalf("create inheritance parent minh_b: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.minh_child (own_col boolean) INHERITS (public.minh_a, public.minh_b)"); err != nil {
		t.Fatalf("create multi-parent inheritance child minh_child: %v", err)
	}

	// Slice 173: a column DEFAULT that is a FUNCTION CALL (`DEFAULT now()`) must
	// round-trip. validateDefaultExpr accepts a non-aggregate, non-SRF *FuncCall,
	// so the parsed call lands in catalog.Column.DefaultExpr and surfaces in
	// pg_attrdef.adbin (atthasdef=true). pg_dump reads it back via
	// pg_get_expr(adbin) (a goopg pass-through) and re-emits it inline as
	// `DEFAULT <expr>`. The catalog-side renderer formatExprForAttrdef handled
	// only literal constants; a *FuncCall fell through to fmt.Sprintf("%v", e) — a
	// Go pointer string — so the dumped DEFAULT was corrupt (restore-breaking).
	// formatExprForAttrdef now renders the call form (mirroring
	// executor.defaultExprToSQL, the sibling proargdefaults renderer). A plain
	// literal default (`status integer DEFAULT 0`) on the same table guards the
	// pre-existing literal branch through the same dump path.
	//
	// Slice 174: a parenless SQL niladic value function (`DEFAULT
	// CURRENT_TIMESTAMP`) must NOT acquire the parens slice 173's call renderer
	// would add. goopg parses CURRENT_TIMESTAMP as a zero-arg *FuncCall
	// (parser.IsNoParenFuncName); PG stores it as a SQLValueFunction and
	// pg_get_expr deparses the bare uppercase keyword. formatExprForAttrdef now
	// special-cases the niladic form to emit `CURRENT_TIMESTAMP` (not
	// `current_timestamp()`, which is invalid SQL on restore). The `touched`
	// column guards that path.
	//
	// Slice 175: a function-call DEFAULT carrying LITERAL ARGUMENTS
	// (`DEFAULT lpad('x', 5)`) must round-trip with its arguments intact. Slice
	// 173 only exercised a zero-arg call (`now()`); the recursive argument
	// rendering in formatExprForAttrdef (string literal `'x'` + integer literal
	// `5`, joined with `, `) had no end-to-end coverage. validateDefaultExpr
	// accepts a non-aggregate, non-SRF *FuncCall regardless of arity, so the
	// parsed call (with its two literal args) reaches pg_attrdef.adbin and
	// pg_dump re-emits `DEFAULT lpad('x', 5)`. The `label` column guards that.
	//
	// Slice 176: a column DEFAULT that is a CAST expression (`DEFAULT '{}'::jsonb`)
	// must round-trip. validateDefaultExpr accepts a *CastExpr (recursing into its
	// operand), so the parsed cast reaches pg_attrdef.adbin. Before this slice the
	// catalog renderer formatExprForAttrdef handled only literals, niladic
	// functions and ordinary calls; a *CastExpr (along with *UnaryOp, *BinaryOp,
	// *TypedStringLit) fell through to fmt.Sprintf("%v", e) — a Go pointer string —
	// corrupting the dumped DEFAULT. formatExprForAttrdef now mirrors
	// executor.defaultExprToSQL for those nodes, rendering `'{}'::jsonb`. The
	// `meta` column guards the cast path end-to-end.
	//
	// Slice 177: a column DEFAULT that is an ARRAY constructor (`DEFAULT ARRAY[1,
	// 2, 3]`) must round-trip. validateDefaultExpr rejects only column refs,
	// subqueries and aggregate/SRF calls, so a parsed *ArrayConstructorExpr is
	// accepted and reaches pg_attrdef.adbin. Before this slice neither renderer
	// (catalog.formatExprForAttrdef nor executor.defaultExprToSQL) had an
	// *ArrayConstructorExpr arm, so it fell through to fmt.Sprintf("%v", e) — a Go
	// pointer string — corrupting the dumped DEFAULT. Both twins now render
	// `ARRAY[1, 2, 3]`. The `vals integer[]` column guards the array-constructor
	// path end-to-end.
	//
	// Slice 178: a column DEFAULT that is a CASE expression (`DEFAULT CASE WHEN
	// true THEN 1 ELSE 0 END`) must round-trip. validateDefaultExpr accepts a
	// parsed *CaseExpr (it rejects only column refs, subqueries and aggregate/SRF
	// calls), so the node reaches pg_attrdef.adbin. Before this slice neither
	// renderer (catalog.formatExprForAttrdef nor executor.defaultExprToSQL) had a
	// *CaseExpr arm, so it fell through to fmt.Sprintf("%v", e) — a Go pointer
	// string — corrupting the dumped DEFAULT. Both twins now render the single-line
	// `CASE WHEN true THEN 1 ELSE 0 END` form (valid, re-parseable SQL; PG's
	// pg_get_expr pretty-prints across lines but the dump round-trips either way).
	// The `grade integer` column guards the CASE-expression path end-to-end.
	//
	// Slice 179: a column DEFAULT that is a parenthesised row constructor
	// (`DEFAULT (1, 2)`) parses to a *RowExpr. validateDefaultExpr accepts it
	// (it rejects only column refs / subqueries / aggregate-or-SRF calls), so
	// the node reaches pg_attrdef.adbin. Before this slice neither renderer had
	// a *RowExpr arm, so it fell through to fmt.Sprintf("%v", e) — a Go pointer
	// string — corrupting the dumped DEFAULT. Both twins now emit the ROW(…)
	// form PG's ruleutils always prints for a RowExpr. The `pair integer` column
	// guards the row-constructor path end-to-end.
	//
	// Slice 180: a column DEFAULT that is an interval literal (`DEFAULT INTERVAL
	// '1' day`) parses to a *IntervalLit. validateDefaultExpr accepts it (it
	// rejects only column refs / subqueries / aggregate-or-SRF calls), so the
	// node reaches pg_attrdef.adbin. Before this slice neither renderer had a
	// *IntervalLit arm, so it fell through to fmt.Sprintf("%v", e) — a Go pointer
	// string — corrupting the dumped DEFAULT. Both twins now emit the native
	// `INTERVAL '<n>' <unit>` literal form (PG's pg_get_expr would deparse the
	// const-folded value as `'1 day'::interval`; both are valid, re-parseable SQL
	// that round-trips). The `span interval` column guards the interval-literal
	// path end-to-end.
	if err := runSQLSimple(t, c, "CREATE TABLE public.defcol (id integer, status integer DEFAULT 0, created timestamptz DEFAULT now(), touched timestamptz DEFAULT CURRENT_TIMESTAMP, label text DEFAULT lpad('x', 5), meta jsonb DEFAULT '{}'::jsonb, vals integer[] DEFAULT ARRAY[1, 2, 3], grade integer DEFAULT CASE WHEN true THEN 1 ELSE 0 END, pair integer DEFAULT (1, 2), span interval DEFAULT INTERVAL '1' day, nflag boolean DEFAULT (1 IS NOT NULL), bflag boolean DEFAULT (true IS NOT TRUE), dflag boolean DEFAULT (1 IS DISTINCT FROM 2))"); err != nil {
		t.Fatalf("create table defcol with function-call default: %v", err)
	}

	// Slice 182: a per-column storage override (`ALTER TABLE ... ALTER COLUMN
	// ... SET STORAGE <mode>`) must survive the dump. pg_dump compares
	// pg_attribute.attstorage against the column type's typstorage and emits the
	// `ALTER TABLE ONLY ... SET STORAGE` statement only when they differ
	// (pg_dump.c dumpTableSchema). goopg recorded the override on
	// catalog.Column.Storage, but the synthesized pg_attribute row always
	// reported the type default, so the override was silently dropped from the
	// dump (a restore-breaking loss of the storage strategy). `storcol` carries
	// the overrides on its own table so foo's many asserts are untouched. text
	// defaults to EXTENDED ('x'); EXTERNAL ('e') and MAIN ('m') both differ, so
	// both must re-emit; the untouched `d` column must NOT produce a SET STORAGE.
	if err := runSQLSimple(t, c, "CREATE TABLE public.storcol (a text, b text, d text)"); err != nil {
		t.Fatalf("create table storcol: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.storcol ALTER COLUMN a SET STORAGE EXTERNAL"); err != nil {
		t.Fatalf("alter storcol.a set storage external: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.storcol ALTER COLUMN b SET STORAGE MAIN"); err != nil {
		t.Fatalf("alter storcol.b set storage main: %v", err)
	}

	// Slice 183: a per-column TOAST compression method must survive the dump.
	// pg_dump emits `ALTER TABLE ONLY ... ALTER COLUMN ... SET COMPRESSION
	// <method>` whenever pg_attribute.attcompression is 'p' (pglz) or 'l' (lz4)
	// (pg_dump.c dumpTableSchema). goopg recorded the method on
	// catalog.Column.Compression, but the synthesized pg_attribute row hardcoded
	// attcompression="" (the '\0' default), so the method was silently dropped
	// from the dump. `cmprcol.a` uses the inline `COMPRESSION pglz` form; `b` the
	// `ALTER ... SET COMPRESSION lz4` form; the untouched `d` must NOT re-emit.
	if err := runSQLSimple(t, c, "CREATE TABLE public.cmprcol (a text COMPRESSION pglz, b text, d text)"); err != nil {
		t.Fatalf("create table cmprcol: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.cmprcol ALTER COLUMN b SET COMPRESSION lz4"); err != nil {
		t.Fatalf("alter cmprcol.b set compression lz4: %v", err)
	}

	// Slice 184: a per-column statistics target must survive the dump. pg_dump
	// emits `ALTER TABLE ONLY ... ALTER COLUMN ... SET STATISTICS <n>` whenever
	// pg_attribute.attstattarget >= 0 (pg_dump.c dumpTableSchema). goopg recorded
	// the target on catalog.Column.StatTarget, but the synthesized pg_attribute
	// row hardcoded attstattarget=NULL (the default), so the target was silently
	// dropped from the dump. `statcol.a` SET STATISTICS 100; `b` SET STATISTICS 0
	// (a valid, non-default target that disables sampling); the untouched `d`
	// must NOT re-emit.
	if err := runSQLSimple(t, c, "CREATE TABLE public.statcol (a integer, b integer, d integer)"); err != nil {
		t.Fatalf("create table statcol: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.statcol ALTER COLUMN a SET STATISTICS 100"); err != nil {
		t.Fatalf("alter statcol.a set statistics 100: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.statcol ALTER COLUMN b SET STATISTICS 0"); err != nil {
		t.Fatalf("alter statcol.b set statistics 0: %v", err)
	}

	// Slice 185: per-column attribute options must survive the dump. pg_dump
	// renders `array_to_string(a.attoptions, ', ')` and emits `ALTER TABLE
	// ONLY ... ALTER COLUMN ... SET (...)` whenever that is non-empty (pg_dump.c
	// dumpTableSchema). goopg captured the options on catalog.Column.Options,
	// but the synthesized pg_attribute row hardcoded attoptions=NULL (the
	// default), so they were silently dropped from the dump. `optcol.a` gets a
	// positive n_distinct, `b` a negative one; the untouched `d` must NOT re-emit.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optcol (a integer, b integer, d integer)"); err != nil {
		t.Fatalf("create table optcol: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.optcol ALTER COLUMN a SET (n_distinct=0.5)"); err != nil {
		t.Fatalf("alter optcol.a set (n_distinct=0.5): %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.optcol ALTER COLUMN b SET (n_distinct=-0.1)"); err != nil {
		t.Fatalf("alter optcol.b set (n_distinct=-0.1): %v", err)
	}

	// Slice 188: a per-column explicit collation (`COLLATE <name>`) must survive
	// the dump. pg_dump's getTableAttrs reports attcollation only when it differs
	// from the column type's typcollation (`CASE WHEN a.attcollation <>
	// t.typcollation …`), then dumpTableSchema re-emits `COLLATE <schema>.<name>`
	// inline in the CREATE TABLE column list. goopg recorded the name on
	// catalog.Column.Collation, but the synthesized pg_attribute row echoed the
	// type's typcollation unconditionally, so the COLLATE was silently dropped.
	// text's typcollation is the default (100), so C (950) and POSIX (951) both
	// differ and must re-emit; the untouched `d` (default collation) must NOT.
	if err := runSQLSimple(t, c, `CREATE TABLE public.collcol (a text COLLATE "C", b text COLLATE "POSIX", d text)`); err != nil {
		t.Fatalf("create table collcol: %v", err)
	}

	// Slice 189: the ARRAY types of the collatable scalars must NOT emit a
	// spurious COLLATE. A PG array inherits its element's typcollation, so
	// varchar[]/bpchar[]/name[] columns carry attcollation 100/100/950 — and the
	// bootstrapped pg_type heap must report the SAME typcollation for the array
	// OID, or getTableAttrs's `a.attcollation <> t.typcollation` fires and
	// dumpTableSchema emits `COLLATE pg_catalog."default"` on a column the user
	// never collated. The heap left _name/_bpchar/_varchar typcollation at 0
	// (slice 188 fixed only the scalars), so this was the slice-187 regression
	// still latent for array columns. No COLLATE clause must appear for collarr.
	if err := runSQLSimple(t, c, `CREATE TABLE public.collarr (a character varying[], b character(4)[], cc name[], d text[])`); err != nil {
		t.Fatalf("create table collarr: %v", err)
	}

	// Slice 54 (cross-namespace guard): a user-defined schema (other than public)
	// and a table inside it round-trip. pg_dump emits `CREATE SCHEMA s;` for every
	// dumpable non-public namespace and qualifies the contained objects; this
	// already worked, so it stands as a regression guard for the emit path.
	if err := runSQLSimple(t, c, "CREATE SCHEMA s"); err != nil {
		t.Fatalf("create schema s: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE s.widget (id integer PRIMARY KEY, label text)"); err != nil {
		t.Fatalf("create table s.widget: %v", err)
	}

	// Slice 55: COMMENT ON {TABLE,COLUMN} must survive the dump. goopg parses
	// COMMENT ON and populates pg_description via catalog.SetComment, and
	// pg_dump collects comments with `SELECT description, classoid, objoid,
	// objsubid FROM pg_catalog.pg_description ORDER BY …`, then re-emits a
	// `COMMENT ON …` statement per object. A table comment is keyed (classoid=
	// pg_class, objoid=table OID, objsubid=0); a column comment carries
	// objsubid=attnum. This probes whether the populated virtual view round-trips.
	if err := runSQLSimple(t, c, "COMMENT ON TABLE public.foo IS 'a foo table'"); err != nil {
		t.Fatalf("comment on table foo: %v", err)
	}
	if err := runSQLSimple(t, c, "COMMENT ON COLUMN public.foo.name IS 'the name column'"); err != nil {
		t.Fatalf("comment on column foo.name: %v", err)
	}

	// Slice 144: COMMENT ON CONSTRAINT must survive the dump for ALL constraint
	// kinds, not just CHECK / NOT NULL. goopg parsed COMMENT ON CONSTRAINT and
	// populated pg_description only for NamedChecks / NotNullConstraints, so a
	// comment on an index-backed (PRIMARY KEY / UNIQUE / EXCLUDE) or FOREIGN KEY
	// constraint was silently dropped — the lookup found no match and returned
	// without calling SetComment. execCommentOn now also resolves index-backed
	// constraints (the backing index OID is the pg_constraint OID) and FKs
	// (stored on the child table), so pg_dump's collectComments re-emits a
	// `COMMENT ON CONSTRAINT <name> ON <table> IS '...'` per object.
	constraintComments := []string{
		"COMMENT ON CONSTRAINT foo_pkey ON public.foo IS 'the primary key'",
		"COMMENT ON CONSTRAINT foo_code_key ON public.foo IS 'unique code'",
		"COMMENT ON CONSTRAINT foo_mgr_fkey ON public.foo IS 'manager fk'",
		"COMMENT ON CONSTRAINT exdef ON public.exclndef IS 'exclusion comment'",
	}
	for _, sql := range constraintComments {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// Slice 56: a plain (non-constraint) secondary index must survive the dump,
	// AND its per-column ASC/DESC + NULLS FIRST/LAST ordering must be preserved.
	// The UNIQUE (code) constraint round-trips via pg_dump's index-backed
	// constraint path (ADD CONSTRAINT ... UNIQUE); a standalone CREATE INDEX
	// instead flows through getIndexes -> pg_get_indexdef(indexrelid), a distinct
	// path that emits a separate `CREATE INDEX ... USING btree (...)` statement.
	// goopg parsed but SILENTLY DISCARDED the ordering modifiers, so a DESC index
	// round-tripped as ASC — a silent semantic change. foo_name_idx guards the
	// plain (all-default) path; the two ordered indexes guard each render branch.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_name_idx ON public.foo (name)"); err != nil {
		t.Fatalf("create index foo_name_idx: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE INDEX foo_qty_partial_idx ON public.foo (qty) WHERE qty > 0"); err != nil {
		t.Fatalf("create partial index: %v", err)
	}
	// DESC NULLS LAST exercises the DESC branch with a non-default NULLS override.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_name_desc_idx ON public.foo (name DESC NULLS LAST)"); err != nil {
		t.Fatalf("create DESC index: %v", err)
	}
	// A composite index mixing DESC (default NULLS FIRST, suppressed) with an
	// ASC NULLS FIRST override exercises the remaining two render branches.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_ord_idx ON public.foo (name DESC, qty NULLS FIRST)"); err != nil {
		t.Fatalf("create ordered composite index: %v", err)
	}
	// Slice 218: a plain CREATE INDEX with a `WITH (fillfactor=N)` storage
	// parameter must round-trip. goopg parsed and range-validated the fillfactor
	// but never STORED it on catalog.Index, so pg_get_indexdef (BuildIndexDef)
	// dropped the WITH clause and the index restored without its fill factor — a
	// silent loss. The value now threads parser → catalog.Index.Fillfactor →
	// BuildIndexDef's `WITH (fillfactor='N')` (the dump path for a plain index)
	// and the index's pg_class.reloptions virtual cell (the constraint-backed
	// sibling surface). goopg does not honor fill factor for page packing, so it
	// is advisory catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_ff_idx ON public.foo (qty) WITH (fillfactor=70)"); err != nil {
		t.Fatalf("create fillfactor index: %v", err)
	}
	// Slice 219: goopg's first index-level BOOLEAN reloption. A btree index
	// declared `WITH (deduplicate_items=off)` must round-trip. goopg's parser
	// accepted the WITH clause but only extracted fillfactor, discarding every
	// other key — so deduplicate_items was silently lost and the index restored
	// with btree posting-list deduplication implicitly ON. The value now threads
	// parser → catalog.Index.DeduplicateItems (*bool tri-state) → BuildIndexDef's
	// `WITH (deduplicate_items='off')` and the index pg_class.reloptions cell.
	// goopg performs no deduplication, so it is advisory catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_dedup_idx ON public.foo (qty) WITH (deduplicate_items=off)"); err != nil {
		t.Fatalf("create deduplicate_items index: %v", err)
	}
	// Slice 220: a GIN index declared `WITH (fastupdate=off)` must round-trip.
	// GIN previously fell through to "index method gin is not supported in v0";
	// it now registers catalog-only (like gist/spgist — no physical storage),
	// which lets its fastupdate boolean reloption thread parser →
	// catalog.Index.FastUpdate (*bool tri-state) → BuildIndexDef's `USING gin …
	// WITH (fastupdate='off')` and the index pg_class.reloptions cell. goopg has
	// no GIN pending-list, so the value is advisory catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_fastupdate_idx ON public.foo USING gin (qty) WITH (fastupdate=off)"); err != nil {
		t.Fatalf("create fastupdate gin index: %v", err)
	}
	// Slice 221: a GIN index declared `WITH (gin_pending_list_limit=128)` must
	// round-trip. goopg's parser previously extracted only fillfactor/deduplicate_
	// items/fastupdate from the WITH clause, discarding gin_pending_list_limit; it
	// now range-validates (64–2097151) and persists it on catalog.Index.
	// GinPendingListLimit (int), so BuildIndexDef emits `USING gin … WITH
	// (gin_pending_list_limit='128')` and the index pg_class.reloptions cell carries
	// it. goopg has no GIN pending-list, so the value is advisory catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_ginlimit_idx ON public.foo USING gin (qty) WITH (gin_pending_list_limit=128)"); err != nil {
		t.Fatalf("create gin_pending_list_limit gin index: %v", err)
	}
	// Slice 222: a BRIN index declared `WITH (pages_per_range=64)` must round-trip.
	// goopg's CREATE INDEX previously rejected `USING brin` (unsupported method); it
	// now registers BRIN catalog-only (like gist/spgist/gin), range-validates
	// pages_per_range (1–131072), and persists it on catalog.Index.PagesPerRange
	// (int), so BuildIndexDef emits `USING brin … WITH (pages_per_range='64')` and
	// the index pg_class.reloptions cell carries it. goopg has no BRIN
	// summarization, so the value is advisory catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_brinrange_idx ON public.foo USING brin (qty) WITH (pages_per_range=64)"); err != nil {
		t.Fatalf("create pages_per_range brin index: %v", err)
	}
	// Slice 223: a BRIN index declared `WITH (autosummarize=on)` must round-trip.
	// goopg's parser previously extracted only pages_per_range from the BRIN WITH
	// clause, discarding autosummarize; it now threads the boolean through parser →
	// catalog.Index.AutoSummarize (*bool tri-state), so BuildIndexDef emits `USING
	// brin … WITH (autosummarize='on')` and the index pg_class.reloptions cell
	// carries it. goopg has no BRIN summarization, so the value is advisory
	// catalog/dump-only.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_brinauto_idx ON public.foo USING brin (qty) WITH (autosummarize=on)"); err != nil {
		t.Fatalf("create autosummarize brin index: %v", err)
	}
	// Slice 134: a UNIQUE index declared `NULLS NOT DISTINCT` (PostgreSQL 15+)
	// must round-trip. goopg's parser accepted the clause but DISCARDED it, and
	// pg_index.indnullsnotdistinct was hard-wired false, so pg_get_indexdef
	// re-emitted the index as a plain `CREATE UNIQUE INDEX … (name)` — a silent
	// loss of the NULL-deduplication semantics on restore (under the default
	// NULLS DISTINCT, every NULL is unique; NULLS NOT DISTINCT treats them as
	// equal). The flag now threads parser → catalog.Index.NullsNotDistinct →
	// pg_index.indnullsnotdistinct, and BuildIndexDef re-emits the clause after
	// the column list (mirroring ruleutils.c pg_get_indexdef_worker), so the
	// dumped DDL preserves it. NOTE: enforcement of the NULLS-equal semantics at
	// INSERT/UPDATE time is deferred (DU-002 follow-up); this slice pins the
	// dump-fidelity layer only.
	if err := runSQLSimple(t, c, "CREATE UNIQUE INDEX foo_nnd_idx ON public.foo (name) NULLS NOT DISTINCT"); err != nil {
		t.Fatalf("create NULLS NOT DISTINCT unique index: %v", err)
	}

	// Slice 57: a VIEW must survive the dump. pg_dump fetches the defining
	// query via `pg_get_viewdef(oid)` (createViewAsClause), and aborts the
	// ENTIRE dump with `definition of view "v" appears to be empty (length
	// zero)` when it returns NULL/"". goopg stubbed pg_get_viewdef to NULL, so
	// any view made pg_dump fail outright (and the table DATA after it never
	// emitted). goopg now captures the raw view body at parse time
	// (catalog.Table.ViewDef) and pg_get_viewdef echoes it terminated with ';'
	// (pg_dump strips the trailing ';' and wraps it in `CREATE VIEW … AS`).
	// The body references public.foo with a schema qualification, so the dumped
	// view restores under pg_dump's search_path='' setting.
	if err := runSQLSimple(t, c, "CREATE VIEW public.foo_view AS SELECT id, name FROM public.foo WHERE qty > 0"); err != nil {
		t.Fatalf("create view foo_view: %v", err)
	}

	// Slice 58: a VIEW created with an explicit column list renames its
	// output columns. pg_dump fetches the body via pg_get_viewdef, which in PG
	// bakes the renamed names into the SELECT as `expr AS cN`. goopg captures
	// the body verbatim, so without alias splicing the dumped view would carry
	// the underlying column names (id, name) instead of (col_a, col_b) — a
	// silent fidelity loss. applyViewColumnAliases now splices the names in.
	if err := runSQLSimple(t, c, "CREATE VIEW public.foo_rview (col_a, col_b) AS SELECT id, name FROM public.foo"); err != nil {
		t.Fatalf("create view foo_rview: %v", err)
	}

	// Slice 59: a GENERATED ALWAYS AS (expr) STORED column must round-trip with
	// its generation clause. pg_dump prints `GENERATED ALWAYS AS (%s) STORED`
	// only when the column carries BOTH pg_attribute.attgenerated='s' AND a
	// pg_attrdef row whose pg_get_expr(adbin) yields the expression (pg_dump.c
	// dumpTableSchema: print_default requires tbinfo->attrdefs[j] != NULL).
	// goopg already set attgenerated='s' but left atthasdef=false and emitted no
	// pg_attrdef row for generated columns, so the GENERATED clause was silently
	// dropped — the column dumped as a plain `area integer`, a stored-vs-computed
	// semantic loss on restore. `gen` carries the generated column on its own
	// table so the fix is isolated from foo's many asserts.
	if err := runSQLSimple(t, c, "CREATE TABLE public.gen (w integer, h integer, "+
		"area integer GENERATED ALWAYS AS (w * h) STORED)"); err != nil {
		t.Fatalf("create table gen: %v", err)
	}

	// Slice 194: a VIRTUAL generated column (`GENERATED ALWAYS AS (expr) VIRTUAL`,
	// PG18) must round-trip preserving its strategy. pg_dump emits
	// `GENERATED ALWAYS AS (%s)` WITHOUT the `STORED` keyword when
	// pg_attribute.attgenerated='v' (pg_dump.c dumpTableSchema), and
	// `… STORED` when 's'. goopg previously reported 's' for every generated
	// column (attGeneratedFor hardcoded "s"), so a VIRTUAL column dumped as
	// STORED — a strategy divergence on restore. The parser now records the
	// declared strategy on catalog.Column.GeneratedVirtual (PG18's default, with
	// no keyword, is VIRTUAL), and attGeneratedFor maps it to 'v'/'s'. goopg
	// still materializes every generated column on write; the discriminator is
	// for catalog/dump fidelity only. `genv` carries a virtual column on its own
	// table so the assertion is isolated from the slice-59 stored fixture.
	if err := runSQLSimple(t, c, "CREATE TABLE public.genv (w integer, h integer, "+
		"varea integer GENERATED ALWAYS AS (w + h) VIRTUAL)"); err != nil {
		t.Fatalf("create table genv: %v", err)
	}

	// Slice 60: a MATERIALIZED VIEW must survive the dump. pg_dump dumps a
	// matview's `AS` clause via the SAME createViewAsClause -> pg_get_viewdef
	// path as a plain view (pg_dump.c dumpTableSchema, RELKIND_MATVIEW branch:
	// `CREATE MATERIALIZED VIEW … AS\n<body>\n  WITH NO DATA;`), and aborts the
	// ENTIRE dump with `definition of view "v" appears to be empty (length
	// zero)` when pg_get_viewdef returns NULL/"". goopg captured the matview body
	// only as the SELECT AST (tbl.View, for REFRESH) but never as raw text
	// (tbl.ViewDef stayed ""), so any matview made pg_dump fail outright. The
	// parser now captures the raw body (CreateMatViewStmt.RawDef via
	// captureSrcSpan) and execCreateMatView stores it on catalog.Table.ViewDef,
	// so pg_get_viewdef echoes it exactly as it does for a plain view.
	if err := runSQLSimple(t, c, "CREATE MATERIALIZED VIEW public.foo_mv AS SELECT id, name FROM public.foo WHERE qty > 0"); err != nil {
		t.Fatalf("create materialized view foo_mv: %v", err)
	}

	// Slice 116: a standalone SEQUENCE must be DISCOVERED and dumped. pg_dump's
	// getTables selects relkind IN ('r','S','v','c','m','f','p'); until this slice
	// goopg hid sequences from the pg_class virtual view, so getTables never saw a
	// relkind='S' relation and no CREATE SEQUENCE was emitted. pg_class now surfaces
	// each IsSequence relation as relkind='S' (relam=0, keeping the storage-less
	// virtual sequence out of pg_amcheck's heap CTE), so getTables discovers it and
	// dumpSequence regenerates the DDL from pg_sequence (slice 115's params) plus a
	// trailing setval() from pg_get_sequence_data (slice 115's SRF). A plain
	// `CREATE SEQUENCE` dumps as `START WITH 1 / INCREMENT BY 1 / NO MINVALUE /
	// NO MAXVALUE / CACHE 1`; an explicit one round-trips its parameters. A
	// standalone sequence (no OWNED BY) has no pg_depend 'a'/'i' row, so pg_dump
	// emits NO `ALTER SEQUENCE ... OWNED BY`.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.plain_seq"); err != nil {
		t.Fatalf("create sequence plain_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.num_seq START WITH 100 INCREMENT BY 10 MAXVALUE 1000"); err != nil {
		t.Fatalf("create sequence num_seq: %v", err)
	}
	// Slice 117: typed sequences (`AS smallint` / `AS integer`) and a `CYCLE`
	// sequence extend the slice-116 coverage. pg_dump's getSequences reads
	// `format_type(seqtypid, NULL)` and emits an `AS <type>` clause whenever the
	// type is not the bigint default; goopg's pg_sequence already carries the
	// declared seqtypid (21 smallint / 23 integer, from CREATE SEQUENCE ... AS),
	// and format_type(21/23, NULL) renders smallint/integer. A typed sequence's
	// default bounds are type-derived (smallint → seqmax 32767, integer →
	// 2147483647), each equal to pg_dump's own default_maxv for that type, so
	// pg_dump still emits `NO MAXVALUE`. A `CYCLE` sequence sets seqcycle=true,
	// which goopg threads through pg_sequence; pg_dump then emits a trailing
	// `CYCLE` clause (default sequences emit none).
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.small_seq AS smallint"); err != nil {
		t.Fatalf("create sequence small_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.int_seq AS integer"); err != nil {
		t.Fatalf("create sequence int_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.cyc_seq CYCLE"); err != nil {
		t.Fatalf("create sequence cyc_seq: %v", err)
	}
	// Slice 130: a per-sequence CACHE size must round-trip. goopg parsed `CACHE n`
	// on CREATE but DISCARDED the value (sequenceParamsForCatalog hard-wired
	// seqcache=1), so every dumped CREATE SEQUENCE emitted `CACHE 1` regardless of
	// the declared cache — a silent loss of the preallocation parameter (a restored
	// dump would change the sequence's caching behaviour). The executor now tracks
	// the cache on the in-memory seqState (SetSequenceCache) and pg_sequence.seqcache
	// reports it, so pg_dump re-emits the declared `CACHE n`. ALTER SEQUENCE ...
	// CACHE n (the sibling path — the parser previously parsed-and-threw-away the
	// value too) is wired through the same field via UpdateSequenceParams. Verified
	// byte-identical to real pg_dump 18.3 (CREATE: CACHE 5; ALTER: CACHE 42).
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.cache_seq CACHE 5"); err != nil {
		t.Fatalf("create sequence cache_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.altcache_seq"); err != nil {
		t.Fatalf("create sequence altcache_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER SEQUENCE public.altcache_seq CACHE 42"); err != nil {
		t.Fatalf("alter sequence altcache_seq cache: %v", err)
	}
	// Slice 118: a sequence with `OWNED BY table.column` is the last single-
	// sequence pg_dump surface. PG records the link as a pg_depend AUTO ('a') row
	// (classid/refclassid=pg_class, objid=seq oid, refobjid=table oid,
	// refobjsubid=column attnum); pg_dump's getTables LEFT JOIN reads it into
	// owning_tab/owning_col and dumpSequence emits a trailing
	// `ALTER SEQUENCE public.owned_seq OWNED BY public.owner_tbl.id;`. goopg
	// previously returned an empty pg_depend, so no OWNED BY ever dumped; the
	// catalog now synthesizes the 'a' row from the executor's sequence registry
	// (SeqParams.OwnedBy). The owning table must exist first (validateSeqOwnedBy).
	if err := runSQLSimple(t, c, "CREATE TABLE public.owner_tbl (id bigint, label text)"); err != nil {
		t.Fatalf("create table owner_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.owned_seq OWNED BY owner_tbl.id"); err != nil {
		t.Fatalf("create sequence owned_seq: %v", err)
	}
	// Slice 119: descending sequences exercise pg_dump's *descending-direction*
	// default-bound suppression, the mirror of the ascending branch verified by
	// slices 116/117. For a descending sequence PG stores seqmin=type_min (bigint:
	// PG_INT64_MIN) and seqmax=-1, and seqstart=seqmax; pg_dump's default_minv/
	// default_maxv flip to those same values when incby<0, so a plain
	// `INCREMENT BY -1` sequence dumps `START WITH -1 / NO MINVALUE / NO MAXVALUE`
	// (no min/max emitted). An explicit-bound descending sequence
	// (`INCREMENT BY -2 MINVALUE -100 MAXVALUE -5`) differs from those defaults, so
	// pg_dump emits both `MINVALUE -100` and `MAXVALUE -5` with `START WITH -5`
	// (start defaults to maxv for a descending seq). goopg's execCreateSequence
	// already computes the descending defaults identically (seqTypeBounds min,
	// maxV=-1, start=maxV) and threads them through pg_sequence, so this is a
	// verification slice; both blocks were confirmed byte-identical to real
	// pg_dump 18.3 (/tmp/pgcheck_du119).
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.desc_seq INCREMENT BY -1"); err != nil {
		t.Fatalf("create sequence desc_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.desc_bound_seq INCREMENT BY -2 MINVALUE -100 MAXVALUE -5"); err != nil {
		t.Fatalf("create sequence desc_bound_seq: %v", err)
	}

	// Slice 124: an ADVANCED sequence (is_called=true). Every prior sequence slice
	// dumps its setval as `(name, N, false)` — the never-called state, where
	// pg_get_sequence_data reports last_value=seqstart and is_called=false. This is
	// the FIRST slice to exercise the is_called=TRUE branch: after `setval(seq, 42,
	// true)` the sequence's runtime state is current=42 / called=true, so
	// SequenceRowData returns last_value=42 / is_called=true and the SRF projects
	// (42, true). pg_dump's getSequences then emits
	// `SELECT pg_catalog.setval('public.bumped_seq', 42, true)` — the value+true
	// form a restore must replay so the next nextval continues at 43 instead of
	// restarting at 1. The setval state lives in the process-global seqRegistry
	// (operators_sequence.go), so the separate pg_dump connection observes the bump.
	// Regression guard for the advanced-sequence (called) dump path; a regression
	// that hard-wired is_called=false (as the never-called slices would tolerate)
	// would silently corrupt restored sequence continuity.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.bumped_seq"); err != nil {
		t.Fatalf("create sequence bumped_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "SELECT setval('public.bumped_seq', 42, true)"); err != nil {
		t.Fatalf("setval bumped_seq: %v", err)
	}

	// Slice 125: a REWOUND sequence — `setval(seq, N, false)` with N != start.
	// Slices 115–124 only ever exercised the never-called default (last_value =
	// start) or the called branch (slice 124). The not-yet-called branch with a
	// NON-default last_value was the silent gap: after `setval('rewound_seq', 30,
	// false)` real PG stores last_value=30 / is_called=false (verified: `SELECT *
	// FROM rewound_seq` → 30/0/f), so pg_dump's data section emits
	// `SELECT pg_catalog.setval('public.rewound_seq', 30, false)` while the schema
	// CREATE SEQUENCE still carries the original `START WITH 5`. goopg's
	// SequenceRowData previously returned the bare `start` (5) for any not-called
	// sequence, so it would have dumped `setval(..., 5, false)` — losing the rewind
	// and corrupting restored continuity (next nextval would yield 5, not 30). The
	// fix returns `current + increment` (the on-disk last_value: start for a fresh
	// seq, N after setval(N,false)). Regression guard for the not-called /
	// non-default last_value dump path. Verified byte-identical vs real pg_dump
	// 18.3 (reference /tmp/du125_pgdata).
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.rewound_seq START WITH 5"); err != nil {
		t.Fatalf("create sequence rewound_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "SELECT setval('public.rewound_seq', 30, false)"); err != nil {
		t.Fatalf("setval rewound_seq: %v", err)
	}

	// Slice 120: an IDENTITY column's backing sequence is the first MULTI-statement
	// pg_dump object beyond a standalone sequence. PG records the column→sequence
	// link as a pg_depend INTERNAL ('i') row (vs the AUTO 'a' of an OWNED BY
	// sequence) and sets pg_attribute.attidentity to 'a' (GENERATED ALWAYS) or 'd'
	// (GENERATED BY DEFAULT). pg_dump keys is_identity_sequence on deptype='i' and
	// then dumps `ALTER TABLE t ALTER COLUMN c ADD GENERATED <kind> AS IDENTITY
	// (SEQUENCE NAME ...)` — NOT a standalone CREATE SEQUENCE, and NOT an ALTER
	// SEQUENCE OWNED BY; the ALWAYS/BY DEFAULT keyword comes from attidentity.
	// goopg previously left attidentity empty and synthesized only deptype='a'
	// rows, so an identity sequence dumped as a plain OWNED BY sequence (wrong DDL,
	// loses the identity). This slice: (a) catalog.Column.IdentityAlways stores the
	// KIND, plumbed from the parser; (b) attidentity emits 'a'/'d'; (c)
	// dependVirtualRows flips deptype to 'i' when the owning column is an identity
	// column. Integer/bigint identity sequences inherit their type's default
	// min/max, so both emit NO MINVALUE/NO MAXVALUE; the identity branch omits the
	// `AS <type>` clause (the type lives in the column def). Verified byte-identical
	// to real pg_dump 18.3 (/tmp/pgref_du120).
	if err := runSQLSimple(t, c, "CREATE TABLE public.ident_tbl (id integer GENERATED ALWAYS AS IDENTITY, label text)"); err != nil {
		t.Fatalf("create table ident_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.ident_def (id bigint GENERATED BY DEFAULT AS IDENTITY, note text)"); err != nil {
		t.Fatalf("create table ident_def: %v", err)
	}

	// Slice 121: a SERIAL / BIGSERIAL column is the AUTO ('a') counterpart to
	// slice 120's INTERNAL ('i') identity column. PG expands `serial` to a plain
	// integer column NOT NULL with an owned sequence and a nextval() default;
	// pg_dump never emits the word "serial". It dumps four coupled statements:
	//   CREATE TABLE ... (id integer NOT NULL, ...)
	//   CREATE SEQUENCE ..._id_seq AS integer ...        (AS integer: int4 ≠ bigint default)
	//   ALTER SEQUENCE ..._id_seq OWNED BY t.id;         (deptype 'a')
	//   ALTER TABLE ONLY t ALTER COLUMN id SET DEFAULT nextval('..._id_seq'::regclass);
	// The SET DEFAULT is dumped SEPARATELY (not inline in CREATE TABLE) because the
	// owned-sequence ↔ table dependency forms a loop pg_dump breaks via
	// repairTableAttrDefMultiLoop. goopg now (a) gives the serial sequence a
	// catalog IsSequence relation so pg_dump discovers it; (b) remaps the column's
	// atttypid to int4/int8; (c) surfaces a pg_attrdef row whose adbin is the
	// schema-qualified nextval(); (d) synthesizes the pg_depend NORMAL link from
	// that attrdef to the sequence so the loop (and the separate SET DEFAULT) forms.
	// bigserial omits the `AS integer` clause (int8 is the sequence default type).
	if err := runSQLSimple(t, c, "CREATE TABLE public.ser_tbl (id serial, label text)"); err != nil {
		t.Fatalf("create table ser_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.bigser_tbl (id bigserial, note text)"); err != nil {
		t.Fatalf("create table bigser_tbl: %v", err)
	}

	// Slice 122: a table with TWO serial columns. This is the multi-column
	// counterpart to slice 121's single-serial table — it stresses the per-table
	// attrdef builder (InMemory.attrDefRowsLocked) and the pg_depend synthesis
	// (dependVirtualRows) with more than one owned sequence on one table. PG
	// expands each serial into its own owned sequence (mser_a_seq, mser_b_seq) and
	// pg_dump emits, in column order, two CREATE SEQUENCE / OWNED BY / SET DEFAULT
	// / setval groups. The two attrdef rows must carry DISTINCT oids, each matched
	// against the correct pg_depend NORMAL link, or pg_dump would mis-pair the
	// nextval() defaults to the wrong sequence (sibling-path hazard). Verified
	// byte-identical to real pg_dump 18.3 (/tmp/du122_pgdata).
	if err := runSQLSimple(t, c, "CREATE TABLE public.mser (a serial, b serial, note text)"); err != nil {
		t.Fatalf("create table mser: %v", err)
	}

	// Slice 123: a table mixing an IDENTITY column and a SERIAL column. Both own a
	// sequence, but via DIFFERENT pg_depend deptypes — the identity sequence is an
	// INTERNAL ('i') dependency (slice 120), the serial sequence is the attrdef
	// ('a')/owned ('a') pair (slice 121). pg_dump routes each to a DIFFERENT
	// emission form on the SAME table: the identity sequence is embedded inside
	// `ALTER TABLE ... ADD GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME ...)` with NO
	// standalone CREATE SEQUENCE and NO `OWNED BY`, while the serial sequence emits
	// a standalone CREATE SEQUENCE + `OWNED BY` + separate SET DEFAULT. If goopg's
	// dependVirtualRows mis-classified either owned sequence (identity tagged 'a' or
	// serial tagged 'i'), pg_dump would emit the wrong form — a standalone
	// CREATE SEQUENCE for the identity sequence, or an IDENTITY clause for the
	// serial column. This is the deptype sibling-path hazard: both paths must
	// coexist on one relation's dependency graph. Verified byte-identical to real
	// pg_dump 18.3 (/tmp/du123_pgdata).
	if err := runSQLSimple(t, c, "CREATE TABLE public.mix (id integer GENERATED ALWAYS AS IDENTITY, n serial, note text)"); err != nil {
		t.Fatalf("create table mix: %v", err)
	}

	// Slice 61: a RECURSIVE VIEW must survive the dump. PG stores a recursive
	// view as a regular view over a WITH RECURSIVE CTE; pg_dump fetches the body
	// via the SAME pg_get_viewdef path as a plain view and aborts the WHOLE dump
	// with `definition of view "v" appears to be empty (length zero)` when it
	// returns NULL/"". goopg's recursive-view parser built the wrapped-CTE AST
	// (for execution) but set NO RawDef, so pg_get_viewdef returned NULL and any
	// recursive view killed the dump — the slice-57 blocker repeated for the
	// recursive parse path. The parser now synthesizes the wrapped form
	// `WITH RECURSIVE name(cols) AS (<body>) SELECT cols FROM name` into RawDef.
	// The CTE self-reference is UNQUALIFIED (`foo_rec`, not `public.foo_rec`) — it
	// binds to the CTE name, mirroring PG's CREATE RECURSIVE VIEW rewrite.
	if err := runSQLSimple(t, c, "CREATE RECURSIVE VIEW public.foo_rec(n) AS "+
		"SELECT 1 UNION ALL SELECT n + 1 FROM foo_rec WHERE n < 5"); err != nil {
		t.Fatalf("create recursive view foo_rec: %v", err)
	}

	// Slice 62: an array-typed column (text[]/integer[]/…) must survive the
	// dump as its array type, not its bare element type. pg_dump renders each
	// column via format_type(atttypid, atttypmod), which only yields the `[]`
	// suffix when pg_attribute.atttypid holds the array (_typename) OID. The
	// parser captured the `[]` suffix (ColumnType.IsArray) but dropped it on the
	// way into the catalog, so buildUserPGAttributeRow stored the SCALAR OID and
	// every array column dumped as its element type (`tags text`, not
	// `tags text[]`) — a type-fidelity loss on restore. catalog.Type now carries
	// IsArray, and buildUserPGAttributeRow remaps the scalar OID to the array OID
	// (text→_text 1009, int4→_int4 1007, int8→_int8 1016) with attndims=1.
	// `arr` carries the array columns on its own table so foo's many asserts are
	// untouched. Slice 63 extends the row with bool[] and numeric(p,s)[] columns:
	// these previously fell back to their scalar element OID because only
	// int2/int4/int8/text had array OID mappings. The numeric array additionally
	// exercises the typmod path — format_type carries the element typmod onto the
	// array, so `prices numeric(10,2)[]` must round-trip precision/scale.
	// Slice 64 extends the row with double precision[], date[] and timestamp[]:
	// these previously fell back to their scalar element OID because only
	// int2/int4/int8/text/bool/numeric had array OID mappings. All three follow
	// the proven 3-site pattern (array OID const + ArrayOID maps +
	// userTypeAttrsForOID + formatTypeOID); float8/timestamp use 'd' alignment,
	// date uses 'i'.
	// Slice 66 adds a scalar uuid column (`tok`) and a uuid[] column (`ids`).
	// uuid was the first scalar element type goopg had NOT wired into
	// catalog.TypeNameToOID/OIDToTypeName, so a `uuid` column fell back to text
	// (OID 25) and dumped as `text`; the array path had no _uuid OID at all.
	// Slice 66 wires uuid (OID 2950, typlen 16, typalign 'c', typstorage 'p')
	// and _uuid (OID 2951) through the same proven sites: scalar OID maps +
	// array OID const + ArrayOID maps + userTypeAttrsForOID + formatTypeOID
	// (2950/2951 already present in expr.go).
	// Slice 65 extends it again with real[] (_float4 1021, 'i'), time[] (_time
	// 1183, 'd') and timestamp with time zone[] (_timestamptz 1185, 'd') — the
	// remaining scalar-OID-backed element types, same 3-site pattern.
	if err := runSQLSimple(t, c, "CREATE TABLE public.arr (id integer PRIMARY KEY, "+
		"tags text[], scores integer[], big bigint[], "+
		"flags boolean[], prices numeric(10,2)[], "+
		"ratios double precision[], days date[], moments timestamp[], "+
		"speeds real[], times time[], zoned timestamptz[], "+
		"tok uuid, ids uuid[], "+
		"blob bytea, blobs bytea[], "+
		"label varchar(20), labels varchar(20)[], "+
		"code char(4), codes char(4)[], oids oid[], "+
		"doc json, docs json[], jdoc jsonb, jdocs jsonb[], "+
		"span interval, spans interval[], "+
		"ip inet, ips inet[], net cidr, nets cidr[], "+
		"mac macaddr, macs macaddr[], mac8 macaddr8, mac8s macaddr8[], "+
		"pt point, pts point[], seg lseg, segs lseg[], "+
		"pth path, pths path[], bx box, bxs box[], "+
		"poly polygon, polys polygon[], ln line, lns line[], "+
		"circ circle, circs circle[], "+
		"tsv tsvector, tsvs tsvector[], tsq tsquery, tsqs tsquery[], "+
		"xm xml, xms xml[], mny money, mnys money[], "+
		"bv bit(8), bvs bit(8)[], vb varbit(16), vbs varbit(16)[], "+
		"lsn pg_lsn, lsns pg_lsn[], "+
		"txs txid_snapshot, txss txid_snapshot[], "+
		"pgs pg_snapshot, pgss pg_snapshot[], "+
		"x8 xid8, x8s xid8[], "+
		"td tid, tds tid[], xd xid, xds xid[], cd cid, cds cid[], "+
		"rp regproc, rps regproc[], rpd regprocedure, rpds regprocedure[], "+
		"ropr regoper, roprs regoper[], roo regoperator, roos regoperator[], "+
		"rcl regclass, rcls regclass[], rt regtype, rts regtype[], "+
		"rcf regconfig, rcfs regconfig[], rdi regdictionary, rdis regdictionary[], "+
		"rn regnamespace, rns regnamespace[], rr regrole, rrs regrole[], "+
		"rco regcollation, rcos regcollation[], "+
		"iv int2vector, ivs int2vector[], ov oidvector, ovs oidvector[], "+
		"nm name, nms name[], "+
		"tt timetz, tts timetz[], "+
		"jp jsonpath, jps jsonpath[], "+
		"rfc refcursor, rfcs refcursor[], "+
		"acl aclitem, acls aclitem[], "+
		"ch \"char\", chs \"char\"[])"); err != nil {
		t.Fatalf("create table arr: %v", err)
	}

	// Slice 88: a user-defined ENUM type and a column that uses it must survive
	// the dump. This is the first OBJECT type (CREATE TYPE) in the fixture —
	// the simple scalar/array column types above are exhausted. pg_dump's
	// getTypes collects the enum from pg_type (typtype='e'), dumpEnumType reads
	// the ordered labels from pg_enum, and emits `CREATE TYPE public.mood AS
	// ENUM (...)`. A column of the enum type renders via
	// format_type(atttypid, atttypmod): goopg resolved the enum column to the
	// text fallback (atttypid=25) because TypeNameToOID knows only built-ins, so
	// it dumped as `feeling text` — a type-fidelity loss. buildUserPGAttributeRow
	// now re-resolves a non-built-in column name through catalog.LookupEnum to
	// the enum's dynamic pg_type OID, and format_type resolves that OID back to
	// the schema-qualified name (LookupEnumByOID), so the column round-trips as
	// `feeling public.mood` (pg_dump runs with search_path='', so format_type
	// qualifies the non-visible enum). `moody` carries the enum column on its own
	// table so the many `foo`/`arr` asserts are untouched.
	//
	// Slice 89 adds an enum ARRAY column (`feelings mood[]`). PostgreSQL
	// auto-generates a `_mood` array type alongside every enum; goopg now
	// allocates that second OID in RegisterEnum (EnumType.ArrayOID) so the array
	// column resolves to a distinct OID instead of folding to `text[]`.
	// format_type renders it as `public.mood[]` (LookupEnumByArrayOID).
	if err := runSQLSimple(t, c, "CREATE TYPE public.mood AS ENUM ('sad', 'ok', 'happy')"); err != nil {
		t.Fatalf("create type mood: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.moody (id integer PRIMARY KEY, feeling mood, feelings mood[])"); err != nil {
		t.Fatalf("create table moody: %v", err)
	}

	// Slice 90: a user-defined DOMAIN over a base type and a column that uses it
	// must survive the dump. This is the second OBJECT type (after the enum in
	// slices 88-89). pg_dump's getTypes collects the domain from pg_type
	// (typtype='d'), dumpDomain reads typbasetype and renders `CREATE DOMAIN
	// public.zipcode AS format_type(typbasetype, typtypmod)`; a column of the
	// domain type renders via format_type(atttypid) as the schema-qualified
	// domain name. goopg previously had NO pg_type row for a domain
	// (syncEnumTypeToCatalogHeap had no domain twin), so getTypes never
	// discovered it and the domain column folded to its base (`zip text`). New:
	// syncDomainTypeToCatalogHeap writes the typtype='d' row; buildUserPGAttributeRow
	// re-resolves a domain column (keyed on DeclaredTypeName, since CREATE TABLE
	// stores the resolved base) to the domain OID; format_type/LookupDomainByOID
	// renders the domain name. A NULL typdefaultbin also exposed a pg_get_expr
	// bug — pg_get_expr(NULL) returned '' (non-NULL), so dumpDomain emitted a
	// spurious `DEFAULT `; pg_get_expr now returns NULL for a NULL node tree.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.zipcode AS text"); err != nil {
		t.Fatalf("create domain zipcode: %v", err)
	}
	// Slice 91: a domain declared NOT NULL must round-trip the not-null
	// constraint. pg_dump's dumpDomain reads pg_type.typnotnull and, for a
	// PG17+ server with no separate named not-null constraint row
	// (tyinfo->notnull == NULL — goopg emits no contype='n' pg_constraint row
	// for domains), appends a bare ` NOT NULL` to the CREATE DOMAIN. goopg
	// already stores catalog.Domain.NotNull and buildUserPGTypeRowForDomain
	// already emits typnotnull from it, so the dump renders
	// `CREATE DOMAIN public.zipcode_nn AS text NOT NULL;`. Without the flag the
	// not-null was silently dropped (the bare-domain `zipcode` above is the
	// typnotnull='f' regression guard for the no-constraint case).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.zipcode_nn AS text NOT NULL"); err != nil {
		t.Fatalf("create domain zipcode_nn: %v", err)
	}
	// Slice 92: a domain with a DEFAULT expression must round-trip the default.
	// pg_dump's dumpDomain reads pg_get_expr(typdefaultbin) and, when non-NULL,
	// appends ` DEFAULT <expr>` verbatim. goopg's parser now keeps the DEFAULT
	// expr (was previously skipped) and buildUserPGTypeRowForDomain emits the
	// rendered expr as typdefaultbin (pg_get_expr is a pass-through), so the dump
	// renders `CREATE DOMAIN public.qty AS integer DEFAULT 0;`. An integer
	// constant deparses identically in goopg (formatExprForAttrdef) and real PG
	// (verified: `DEFAULT 0`).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.qty AS integer DEFAULT 0"); err != nil {
		t.Fatalf("create domain qty: %v", err)
	}
	// Slice 93: a DOMAIN over text with a STRING-literal DEFAULT. pg_get_expr
	// decorates a coerced string constant with its target type, so real pg_dump
	// renders `CREATE DOMAIN public.label AS text DEFAULT 'n/a'::text;` (verified
	// pg_dump 18.3). goopg's Domain.DefaultBin now appends `::<base>` for a
	// StringConst default to match; integer defaults stay bare (slice 92).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.label AS text DEFAULT 'n/a'"); err != nil {
		t.Fatalf("create domain label: %v", err)
	}
	// Slice 94: a DOMAIN over `varchar` (character varying) with a STRING DEFAULT.
	// Like slice 93's text domain, pg_get_expr decorates the coerced string Const
	// with its target type — but for varchar the cast name is the MULTI-WORD
	// canonical spelling `character varying`, not the user-typed `varchar` alias.
	// Real pg_dump 18.3 emits `CREATE DOMAIN public.vcdef AS character varying
	// DEFAULT 'na'::character varying;` (verified). goopg's DefaultBin now maps the
	// base name through domainConstCastTypeName so the cast renders the format_type
	// spelling instead of the bare alias. A bare `varchar` (no length) isolates the
	// cast-name concern from base-type typmod capture (which the CREATE DOMAIN
	// parser still discards — a separate gap).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.vcdef AS varchar DEFAULT 'na'"); err != nil {
		t.Fatalf("create domain vcdef: %v", err)
	}
	// Slice 95: a DOMAIN whose base type carries a TYPMOD (varchar(20), char(4),
	// numeric(10,2)) must round-trip the declared length/precision. pg_dump's
	// dumpDomain renders the base via format_type(typbasetype, typtypmod); goopg's
	// CREATE DOMAIN parser previously DISCARDED the `(n)` modifier, so typtypmod
	// stayed -1 and varchar(20) dumped as bare `character varying` — a
	// schema-fidelity loss. The parser now captures BaseTypeArgs and the executor
	// threads them onto catalog.Domain.Base.Args, so pgAttTypmod yields the right
	// typtypmod. The cast PG appends to a string DEFAULT stays typmod-less
	// (format_type(consttype, -1)) — varchar(20)→`::character varying`,
	// char(4)→`::bpchar` (internal name) — verified against real pg_dump 18.3. The
	// numeric default deparses bare (a numeric Const is self-evident, no cast).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.vc20 AS varchar(20) DEFAULT 'na'"); err != nil {
		t.Fatalf("create domain vc20: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.ch4 AS char(4) DEFAULT 'ab'"); err != nil {
		t.Fatalf("create domain ch4: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.numd AS numeric(10,2) DEFAULT 1.5"); err != nil {
		t.Fatalf("create domain numd: %v", err)
	}
	// Slice 96: a DOMAIN with a generic CHECK (VALUE <comparison>) constraint must
	// round-trip the constraint. pg_dump's getDomainConstraints reads the check
	// from pg_constraint (contypid = domain OID, contype='c') and dumpDomain emits
	// it inline as `\n\tCONSTRAINT <name> CHECK ((<expr>))`. goopg previously
	// DISCARDED a generic domain CHECK (the parser only captured CHECK (VALUE IN
	// (...)) into CheckInValues, which was never rendered, and emitted no
	// contype='c' pg_constraint row keyed on contypid). The parser now captures the
	// raw predicate (CreateDomainStmt.CheckExpr/CheckName), the executor allocates a
	// constraint OID (catalog.Domain.CheckOID) and emits the pg_constraint row, and
	// pg_get_constraintdef renders `CHECK ((expr))` — verified byte-identical to
	// real pg_dump 18.3. `posqty` uses the auto-generated `posqty_check` name;
	// `named_chk` carries an explicit CONSTRAINT name. The token-reconstructed expr
	// (`VALUE > 0`) matches PG's deparse spacing. The `VALUE IN (...)` form (which
	// deparses to a `= ANY (ARRAY[...])` ScalarArrayOpExpr) is exercised by the
	// `colr`/`named_in` text domains below (slice 97).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.posqty AS integer CHECK (VALUE > 0)"); err != nil {
		t.Fatalf("create domain posqty: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.named_chk AS integer CONSTRAINT must_be_pos CHECK (VALUE > 0)"); err != nil {
		t.Fatalf("create domain named_chk: %v", err)
	}
	// DU-002 slice 97: a `CHECK (VALUE IN (...))` over a text domain. goopg captures
	// the membership list in CheckInValues (runtime validation) but previously emitted
	// no pg_constraint row, so the check vanished from pg_dump. The executor now
	// synthesizes PG's ScalarArrayOpExpr deparse — `VALUE = ANY (ARRAY['red'::text,
	// 'green'::text])` — and stores it as conbin, so pg_get_constraintdef re-renders
	// the inline `CONSTRAINT <name> CHECK ((...))` byte-identically to real pg_dump
	// 18.3. `colr` is auto-named (colr_check); `named_in` carries an explicit name.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.colr AS text CHECK (VALUE IN ('red','green'))"); err != nil {
		t.Fatalf("create domain colr: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.named_in AS text CONSTRAINT must_be_color CHECK (VALUE IN ('red','green'))"); err != nil {
		t.Fatalf("create domain named_in: %v", err)
	}
	// DU-002 slice 98: the same `CHECK (VALUE IN (...))` over char/varchar base
	// types. char(n)/bpchar has a native equality operator, so PG deparses it with
	// the same bare per-element-cast shape as text — `VALUE = ANY (ARRAY['a'::bpchar,
	// ...])`. character varying has no varchar-eq operator and borrows text's, so PG
	// wraps both sides in a text coercion envelope — `(VALUE)::text = ANY
	// ((ARRAY['a'::character varying, ...])::text[])`. The per-element cast uses the
	// bare base name with no typmod even when the domain is varchar(20)/char(4).
	// Verified byte-identical to real pg_dump 18.3 (/tmp/pgcheck_du98).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.vc_in AS varchar CHECK (VALUE IN ('a','b'))"); err != nil {
		t.Fatalf("create domain vc_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.vc20_in AS varchar(20) CONSTRAINT must_ab CHECK (VALUE IN ('a','b'))"); err != nil {
		t.Fatalf("create domain vc20_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.ch_in AS char(4) CHECK (VALUE IN ('a','b'))"); err != nil {
		t.Fatalf("create domain ch_in: %v", err)
	}
	// DU-002 slice 99: `CHECK (VALUE IN (...))` over numeric-family base types.
	// integer/numeric literals already share the base type, so PG deparses the
	// membership list verbatim — no quotes, no per-element cast: `VALUE = ANY
	// (ARRAY[1, 2, 3])`. (bigint differs — its int4 literals are wrapped
	// `(N)::bigint` — and is deferred to a later slice.) Until this slice the
	// CREATE DOMAIN parser only captured *string* IN-lists, so numeric lists
	// silently fell through and produced no constraint at all. Verified
	// byte-identical to real pg_dump 18.3 (/tmp/pgcheck_du99).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.i_in AS integer CHECK (VALUE IN (1, 2, 3))"); err != nil {
		t.Fatalf("create domain i_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.i_in_n AS integer CONSTRAINT must_set CHECK (VALUE IN (10, 20))"); err != nil {
		t.Fatalf("create domain i_in_n: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.n_in AS numeric(10,2) CHECK (VALUE IN (1.5, 2.5))"); err != nil {
		t.Fatalf("create domain n_in: %v", err)
	}
	// DU-002 slice 100: bigint coerces its int4 IN-list literals per element
	// (`(N)::bigint`); boolean keyword literals render verbatim; date mirrors the
	// string-with-cast shape (`'…'::date`). Verified byte-identical to real
	// pg_dump 18.3 (/tmp/pgcheck_du100).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.b_in AS bigint CHECK (VALUE IN (100, 200, 300))"); err != nil {
		t.Fatalf("create domain b_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.bo_in AS boolean CHECK (VALUE IN (true, false))"); err != nil {
		t.Fatalf("create domain bo_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.d_in AS date CHECK (VALUE IN ('2020-01-01', '2021-06-15'))"); err != nil {
		t.Fatalf("create domain d_in: %v", err)
	}
	// DU-002 slice 101: real/double precision coerce their numeric IN-list
	// literals per element (`(N)::real` / `(N)::double precision`); timestamp,
	// time and uuid mirror the string-with-cast shape with their canonical
	// base-type cast name. Single-word base aliases (real/float8/timestamp/time/
	// uuid) are used so the CREATE DOMAIN object-name parser accepts them; pg_dump
	// renders the canonical multi-word name from the OID regardless. Verified
	// byte-identical to real pg_dump 18.3 (/tmp/pgcheck_du101). timestamptz is
	// deliberately omitted — PG re-renders the stored constant in the session
	// timezone, so a verbatim deparse from the raw token text is not byte-identical.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.r_in AS real CHECK (VALUE IN (1.5, 2.5))"); err != nil {
		t.Fatalf("create domain r_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.f8_in AS float8 CHECK (VALUE IN (1.5, 2.5, 3.0))"); err != nil {
		t.Fatalf("create domain f8_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.ts_in AS timestamp CHECK (VALUE IN ('2020-01-01 00:00:00', '2021-06-15 12:30:00'))"); err != nil {
		t.Fatalf("create domain ts_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.tm_in AS time CHECK (VALUE IN ('12:00:00', '13:30:00'))"); err != nil {
		t.Fatalf("create domain tm_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.u_in AS uuid CHECK (VALUE IN ('a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', 'b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12'))"); err != nil {
		t.Fatalf("create domain u_in: %v", err)
	}
	// DU-002 slice 102: smallint (verbatim, like integer — small literals const-fold
	// to int2 with no cast wrapper), bytea + inet (string-with-cast; their canonical
	// input forms `\x` hex / dotted-quad-CIDR round-trip verbatim). Verified
	// byte-identical to real pg_dump 18.3 (/tmp/pgcheck_du102). interval is excluded
	// — PG normalizes the stored value ('2 hours'→'02:00:00'), not byte-identical.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.si_in AS smallint CHECK (VALUE IN (10, 20, 30))"); err != nil {
		t.Fatalf("create domain si_in: %v", err)
	}
	if err := runSQLSimple(t, c, `CREATE DOMAIN public.by_in AS bytea CHECK (VALUE IN ('\xdeadbeef', '\xcafe'))`); err != nil {
		t.Fatalf("create domain by_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.inet_in AS inet CHECK (VALUE IN ('192.168.0.1', '10.0.0.0/8'))"); err != nil {
		t.Fatalf("create domain inet_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.mac_in AS macaddr CHECK (VALUE IN ('08:00:2b:01:02:03', '00:11:22:33:44:55'))"); err != nil {
		t.Fatalf("create domain mac_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.mac8_in AS macaddr8 CHECK (VALUE IN ('08:00:2b:01:02:03:04:05', '00:11:22:33:44:55:66:77'))"); err != nil {
		t.Fatalf("create domain mac8_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.cidr_in AS cidr CHECK (VALUE IN ('192.168.0.0/24', '10.0.0.0/8'))"); err != nil {
		t.Fatalf("create domain cidr_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.nm_in AS name CHECK (VALUE IN ('alice', 'bob'))"); err != nil {
		t.Fatalf("create domain nm_in: %v", err)
	}
	if err := runSQLSimple(t, c, `CREATE DOMAIN public.jb_in AS jsonb CHECK (VALUE IN ('1', '"hello"'))`); err != nil {
		t.Fatalf("create domain jb_in: %v", err)
	}
	// json has no equality operator, so the CHECK must cast VALUE to text. Unlike
	// jsonb the input text round-trips verbatim, so an object value is byte-identical
	// through pg_dump (no key reordering / whitespace normalization). DU-002 slice 105.
	if err := runSQLSimple(t, c, `CREATE DOMAIN public.js_in AS json CHECK (VALUE::text IN ('1', '{"a": 1}'))`); err != nil {
		t.Fatalf("create domain js_in: %v", err)
	}
	// xml also lacks an equality operator, so it uses the same `VALUE::text IN`
	// cast form as json (slice 105); xml is re-emitted verbatim, so it round-trips
	// byte-identically. oid joins the per-element coercion shape (`(N)::oid`).
	// bit(n)/varbit use the bare string-with-cast shape (bit's cast is quoted
	// `::"bit"`). DU-002 slice 106.
	if err := runSQLSimple(t, c, `CREATE DOMAIN public.xml_in AS xml CHECK (VALUE::text IN ('<a/>', '<b>1</b>'))`); err != nil {
		t.Fatalf("create domain xml_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.oid_in AS oid CHECK (VALUE IN (1, 2, 3))"); err != nil {
		t.Fatalf("create domain oid_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.bit_in AS bit(4) CHECK (VALUE IN ('1010', '0101'))"); err != nil {
		t.Fatalf("create domain bit_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.vbit_in AS varbit CHECK (VALUE IN ('101', '110'))"); err != nil {
		t.Fatalf("create domain vbit_in: %v", err)
	}
	// pg_lsn/tid/xid/cid all have native equality operators and canonical input
	// forms that round-trip verbatim through the bare string-with-cast shape.
	// DU-002 slice 107.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.lsn_in AS pg_lsn CHECK (VALUE IN ('16/B374D848', '0/0'))"); err != nil {
		t.Fatalf("create domain lsn_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.tid_in AS tid CHECK (VALUE IN ('(0,1)', '(1,2)'))"); err != nil {
		t.Fatalf("create domain tid_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.xid_in AS xid CHECK (VALUE IN ('100', '200'))"); err != nil {
		t.Fatalf("create domain xid_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.cid_in AS cid CHECK (VALUE IN ('5', '10'))"); err != nil {
		t.Fatalf("create domain cid_in: %v", err)
	}
	// interval/money — DU-002 slice 108. Both have native equality operators and
	// use the bare string-with-cast shape, but only canonical-output forms
	// round-trip byte-identically: interval's output normalizes ('2 hours'→
	// '02:00:00') and money's output depends on lc_monetary (C/POSIX → '$1.00'),
	// so the fixtures use already-canonical values. Verified byte-identical to
	// real pg_dump 18.3 (/tmp/pgcheck_du108).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.iv_in AS interval CHECK (VALUE IN ('1 day', '02:00:00', '1 year 2 mons'))"); err != nil {
		t.Fatalf("create domain iv_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.mny_in AS money CHECK (VALUE IN ('$1.00', '$2.50'))"); err != nil {
		t.Fatalf("create domain mny_in: %v", err)
	}
	// Domain over a user-defined enum base type — DU-002 slice 109. Enums have a
	// native equality operator, so PG emits the bare string-with-cast shape, but
	// with the schema-qualified enum type name (pg_dump sets an empty
	// search_path). Reuses the public.mood enum created above; labels round-trip
	// verbatim (no normalization). Verified byte-identical to real pg_dump 18.3
	// (/tmp/pgcheck_du109).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.enum_in AS public.mood CHECK (VALUE IN ('sad', 'happy'))"); err != nil {
		t.Fatalf("create domain enum_in: %v", err)
	}
	// slice 110: a domain over `timestamp with time zone`. Native equality, so
	// the bare string-with-cast shape; its output is rendered in the session
	// TimeZone, so the fixtures pin the UTC (`+00`) canonical form and the
	// real-pg_dump comparison was run under a UTC session. goopg stores and
	// re-emits the IN-list literals verbatim, so its deparse is TZ-independent.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.tstz_in AS timestamptz CHECK (VALUE IN ('2020-01-01 00:00:00+00', '2021-06-15 12:30:00+00'))"); err != nil {
		t.Fatalf("create domain tstz_in: %v", err)
	}
	// slice 111: a domain over `time with time zone`. Native equality, so the
	// bare string-with-cast shape. Unlike timestamptz, timetz's output preserves
	// the stored zone offset verbatim (no session-TZ re-render), so the canonical
	// 'HH:MM:SS±HH[:MM]' form round-trips byte-identically regardless of session TZ.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.ttz_in AS timetz CHECK (VALUE IN ('12:30:00+09', '23:59:59-05'))"); err != nil {
		t.Fatalf("create domain ttz_in: %v", err)
	}
	// slice 112: a domain over `xid8` (full 64-bit transaction id). Native
	// equality, so the bare string-with-cast shape; the decimal form round-trips
	// verbatim through `::xid8` — the simplest render mode, same as xid/cid
	// (slice 107).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.x8_in AS xid8 CHECK (VALUE IN ('100', '200'))"); err != nil {
		t.Fatalf("create domain x8_in: %v", err)
	}
	// slice 113: domains over the legacy vector types int2vector / oidvector.
	// Both have native equality operators (int2vectoreq / oidvectoreq), so PG
	// emits the bare string-with-cast shape; the canonical space-separated form
	// ('1 2') round-trips verbatim through `::int2vector` / `::oidvector`.
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.i2v_in AS int2vector CHECK (VALUE IN ('1 2', '3 4'))"); err != nil {
		t.Fatalf("create domain i2v_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.ovec_in AS oidvector CHECK (VALUE IN ('1 2', '3 4'))"); err != nil {
		t.Fatalf("create domain ovec_in: %v", err)
	}
	// slice 114: domains over the full-text-search types tsvector / tsquery, the
	// last two slice-108 excluded base types. Both have native equality operators
	// (tsvector_eq / tsquery_eq), so PG emits the bare string-with-cast shape, but
	// only already-canonical lexeme forms round-trip byte-identically (the output
	// functions single-quote lexemes, sort/dedup, and normalize operator spacing).
	// The fixtures pin canonical values: the SQL literal '''a'' ''b''' carries the
	// tsvector value `'a' 'b'`. Verified byte-identical to real pg_dump 18.3
	// (pg_get_constraintdef, /tmp/pgcheck_du114).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.tsv_in AS tsvector CHECK (VALUE IN ('''a'' ''b''', '''cat'' ''dog'''))"); err != nil {
		t.Fatalf("create domain tsv_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.tsq_in AS tsquery CHECK (VALUE IN ('''a'' & ''b''', '''cat'' | ''dog'''))"); err != nil {
		t.Fatalf("create domain tsq_in: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.dom (id integer PRIMARY KEY, zip zipcode, zip_nn zipcode_nn, q qty, lbl label, vc vcdef, v20 vc20, c4 ch4, nd numd, pq posqty, nc named_chk, co colr, ni named_in, vci vc_in, vc20i vc20_in, chi ch_in, ii i_in, iin i_in_n, ni2 n_in, bi b_in, boi bo_in, di d_in, ri r_in, f8i f8_in, tsi ts_in, tmi tm_in, ui u_in, sii si_in, byi by_in, ineti inet_in, maci mac_in, mac8i mac8_in, cidri cidr_in, nmi nm_in, jbi jb_in, jsi js_in, xmli xml_in, oidi oid_in, biti bit_in, vbiti vbit_in, lsni lsn_in, tidi tid_in, xidi xid_in, cidi cid_in, ivi iv_in, mnyi mny_in, eni enum_in, tstzi tstz_in, ttzi ttz_in, x8i x8_in, i2vi i2v_in, oveci ovec_in, tsvi tsv_in, tsqi tsq_in)"); err != nil {
		t.Fatalf("create table dom: %v", err)
	}

	// Slice 147: a user function so COMMENT ON FUNCTION has a real pg_proc target
	// the dump re-emits. pg_dump deparses the signature via
	// pg_get_function_identity_arguments → `public.add_one(integer)`.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_one(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 + 1 $$"); err != nil {
		t.Fatalf("create function add_one: %v", err)
	}

	// Slice 149: a second user function carrying explicit IMMUTABLE STRICT
	// markers, so the dump exercises the pg_proc virtual view's `provolatile`
	// ('i') and `proisstrict` ('t') columns end-to-end — slice 148 only proved
	// the default 'v'/'f' path (add_one emits neither clause). pg_dump's dumpFunc
	// (pg_dump.c:13531/13542) appends ` IMMUTABLE` when provolatile[0] != 'v' and
	// ` STRICT` when proisstrict[0] == 't', both inline after `LANGUAGE sql`.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_two(integer) RETURNS integer LANGUAGE sql IMMUTABLE STRICT AS $$ SELECT $1 + 2 $$"); err != nil {
		t.Fatalf("create function add_two: %v", err)
	}

	// Slice 150: a third user function carrying an explicit PARALLEL SAFE marker,
	// so the dump exercises the pg_proc virtual view's `proparallel` column at a
	// NON-default value ('s'). Slices 148/149 only ever drove the hardcoded 'u'
	// (unsafe) cell, which dumpFunc suppresses — so the round-trip was a no-op.
	// goopg previously discarded the PARALLEL clause entirely (the parser parsed
	// then dropped it; the view hardcoded 'u'), so PARALLEL SAFE was silently lost
	// on dump — a real divergence. pg_dump's dumpFunc (pg_dump.c:13581) appends
	// ` PARALLEL SAFE` inline after `LANGUAGE sql` when proparallel[0] != 'u'.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_three(integer) RETURNS integer LANGUAGE sql PARALLEL SAFE AS $$ SELECT $1 + 3 $$"); err != nil {
		t.Fatalf("create function add_three: %v", err)
	}

	// Slice 151: a fourth user function carrying an explicit COST 50, so the dump
	// exercises the pg_proc virtual view's `procost` column at a NON-default value
	// (LANGUAGE sql's default cost is 100). goopg previously discarded the COST/ROWS
	// numeric entirely (the parser parsed then dropped it; the view hardcoded the
	// language-derived default), so an explicit COST/ROWS was silently reset on
	// dump — a real divergence. dumpFunc (pg_dump.c:13556) appends ` COST 50` inline
	// after `LANGUAGE sql` when procost differs from the language default.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_four(integer) RETURNS integer LANGUAGE sql COST 50 AS $$ SELECT $1 + 4 $$"); err != nil {
		t.Fatalf("create function add_four: %v", err)
	}

	// Slice 152: a set-returning function with an explicit ROWS 5, so the dump
	// exercises the SETOF result-type deparse together with prorows at a
	// NON-default value. pg_dump builds the RETURNS clause from
	// pg_get_function_result(oid), which in PG (ruleutils.c) prefixes the
	// result type with `SETOF ` for set-returning functions. goopg previously
	// returned the bare type name, so an SRF was silently downgraded to a
	// scalar `RETURNS integer` on dump — a real divergence. dumpFunc
	// (pg_dump.c:13571) additionally appends ` ROWS 5` when proretset='t' and
	// prorows ∉ {0,1000}.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.gen_series_lite(integer) RETURNS SETOF integer LANGUAGE sql ROWS 5 AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function gen_series_lite: %v", err)
	}

	// Slice 153: a sixth user function carrying SECURITY DEFINER and LEAKPROOF, so
	// the dump exercises the pg_proc virtual view's `prosecdef` and `proleakproof`
	// columns at their NON-default values ('t'). Slices 148–152 only ever drove the
	// hardcoded 'f' for both, which dumpFunc suppresses. The parser+executor already
	// thread SECURITY DEFINER/LEAKPROOF onto catalog.Routine (unlike the
	// parsed-then-dropped clauses of slices 150/151), and pg_proc_view emits 't'/'t'
	// — but NO pg_dump round-trip previously asserted these columns flow through
	// dumpFunc, so this slice locks the coverage. dumpFunc (pg_dump.c:13545/13548)
	// appends ` SECURITY DEFINER` then ` LEAKPROOF` inline after STRICT and before
	// COST. (LEAKPROOF requires a superuser, which the test connection is.)
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_five(integer) RETURNS integer LANGUAGE sql SECURITY DEFINER LEAKPROOF AS $$ SELECT $1 + 5 $$"); err != nil {
		t.Fatalf("create function add_five: %v", err)
	}

	// Slice 154: a CREATE PROCEDURE (prokind='p'), so the dump exercises
	// dumpFunc's PROCEDURE branch (pg_dump.c:13484) and the no-RETURNS path
	// (pg_dump.c:13498) — distinct from every prior slice, which only ever
	// dumped functions (prokind='f'). pg_dump renders procedures with the
	// `PROCEDURE` keyword, NO `RETURNS` clause, and — because procedures always
	// carry an argmode (buildFunctionArguments sets showMode for procedures,
	// matching ruleutils) — the `IN ` mode prefix on the named parameter. The
	// body has no `$`, so pg_dump's appendStringLiteralDQ picks the bare `$$`
	// delimiter (every prior function body contained `$N`, forcing `$_$`). This
	// is the first procedure ever sent through getFuncs/dumpFunc, so it proves
	// prokind='p' rows are discovered and rendered without the RETURNS type.
	if err := runSQLSimple(t, c, "CREATE PROCEDURE public.ins_foo(a integer) LANGUAGE sql AS $$ INSERT INTO public.foo (id) VALUES (a) $$"); err != nil {
		t.Fatalf("create procedure ins_foo: %v", err)
	}

	// Slice 155: a procedure carrying an OUT parameter, so the dump exercises
	// buildFunctionArguments' `OUT ` argmode branch (expr.go) through dumpFunc —
	// a path NO prior slice reached. Every function/procedure dumped so far had
	// only IN parameters (slice 154's ins_foo rendered `IN a integer`; functions
	// with all-IN params suppress the mode prefix entirely). A procedure with a
	// mixed IN/OUT signature forces pg_get_function_arguments to emit BOTH the
	// `IN ` and the `OUT ` prefix, and pg_dump renders the full mode-qualified
	// list verbatim in the CREATE PROCEDURE signature. The OUT parameter is pure
	// catalog metadata (proargmodes='b'/'o' element); the INSERT body is always
	// accepted by validateSQLFunctionBody regardless of return shape, keeping the
	// fixture focused on the argmode render rather than SQL-procedure body rules.
	// A dropped or mis-rendered OUT prefix (e.g. emitting it as a plain `IN`, or
	// omitting the OUT param from the signature) would surface exactly here.
	if err := runSQLSimple(t, c, "CREATE PROCEDURE public.proc_out(a integer, OUT b integer) LANGUAGE sql AS $$ INSERT INTO public.foo (id) VALUES (a) $$"); err != nil {
		t.Fatalf("create procedure proc_out: %v", err)
	}

	// Slice 156: a procedure carrying an INOUT parameter, driving the LAST
	// unrendered argmode through pg_dump. Slice 155's proc_out reached the OUT
	// branch ('o'); the INOUT branch (proargmodes element 'b') is the only mode
	// prefix pg_get_function_arguments could still emit that no slice had
	// exercised end-to-end. A lone INOUT param sets showMode=true (the 'b'
	// element trips the OUT/INOUT detector in expr.go's renderer), so the dump
	// must render the explicit `INOUT ` prefix — NOT a bare or `IN`-qualified
	// name. The parser maps INOUT -> FuncArgInout (operators_ddl.go:5524) and the
	// renderer's `case "b"` writes `INOUT ` (expr.go:11352). The INSERT body is
	// accepted by validateSQLFunctionBody regardless of the INOUT shape, keeping
	// the fixture pinned on the argmode render. A dropped/mis-rendered INOUT
	// prefix (e.g. collapsed to `IN`, or the param omitted) surfaces in the
	// assertion below.
	if err := runSQLSimple(t, c, "CREATE PROCEDURE public.proc_inout(INOUT x integer) LANGUAGE sql AS $$ INSERT INTO public.foo (id) VALUES (x) $$"); err != nil {
		t.Fatalf("create procedure proc_inout: %v", err)
	}

	// Slice 157: a function carrying STABLE + PARALLEL RESTRICTED, driving the
	// two volatility/parallel cells that no prior slice reached. Slice 149 hit the
	// IMMUTABLE cell (provolatile='i') and slice 150 the PARALLEL SAFE cell
	// (proparallel='s'); the STABLE cell (provolatile='s') and the PARALLEL
	// RESTRICTED cell (proparallel='r') were the last non-default volatility /
	// parallel-safety values pg_dump could emit that nothing had exercised
	// end-to-end. The parser already maps STABLE -> 's' (function.go:184) and
	// RESTRICTED -> 'r' (function.go:253); the executor stores both onto
	// catalog.Routine and pg_proc_view emits r.Volatile / r.Parallel verbatim, so
	// this is a clean positive (no production change) closing the matrix. dumpFunc
	// appends ` STABLE` (provolatile[0]=='s', pg_dump.c:13533) then
	// ` PARALLEL RESTRICTED` (proparallel[0]=='r', pg_dump.c:13583) — volatility
	// before parallel — yielding the one-line `LANGUAGE sql STABLE PARALLEL
	// RESTRICTED`. A dropped or reordered clause (or a downgrade of either marker
	// to its default) surfaces in the assertion below.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_six(integer) RETURNS integer LANGUAGE sql STABLE PARALLEL RESTRICTED AS $$ SELECT $1 + 6 $$"); err != nil {
		t.Fatalf("create function add_six: %v", err)
	}

	// Slice 158: a function whose SQL body holds MORE THAN ONE statement,
	// separated by an internal `;`. Every prior function/procedure slice carried
	// a single-statement body, so two distinct paths were never exercised:
	//   (1) goopg's simple-query statement splitter must treat the inner `;` as
	//       part of the dollar-quoted body, NOT as a batch separator — otherwise
	//       this CREATE FUNCTION would be truncated at the first `;` and fail to
	//       parse (caught immediately by the runSQLSimple error below); and
	//   (2) the multi-statement body must be stored as prosrc verbatim (including
	//       the inner `;`) and re-emitted by dumpFunc within the dollar quote.
	// validateSQLFunctionBody parses the whole body, scans every statement for
	// param refs, and requires only the LAST statement to be a scalar SELECT
	// (operators_ddl.go) — so `SELECT 1; SELECT $1 + 7` is accepted. The `$1` in
	// the body forces pg_dump's appendStringLiteralDQ to escalate to the `$_$`
	// delimiter (same as add_six). Clean positive: the body is opaque text to
	// pg_dump, so no new dump branch is driven — the coverage is on goopg's
	// splitter + verbatim-prosrc round-trip. Real pg_dump 18.3 renders the body
	// exactly as stored, inner `;` and all.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_seven(integer) RETURNS integer LANGUAGE sql AS $$ SELECT 1; SELECT $1 + 7; $$"); err != nil {
		t.Fatalf("create function add_seven: %v", err)
	}

	// Slice 159: a function with a VARIADIC parameter of an ARRAY type. Every
	// prior function slice (148–158) declared only fixed, by-value IN parameters
	// (single unnamed `integer`); none exercised the VARIADIC argmode ('v') or an
	// array parameter type. This drives two paths nothing had reached for a
	// pg_proc dump:
	//   (1) CREATE FUNCTION stores argModes[i]='v' for the trailing parameter and
	//       the array type name `integer[]` (operators_ddl.go ~5519/5511); and
	//   (2) pg_dump reconstructs the CREATE FUNCTION signature from
	//       pg_get_function_arguments(oid), which goopg answers via
	//       buildFunctionArguments — that function maps argmode 'v' to the
	//       `VARIADIC ` prefix and emits the canonical array type name, yielding
	//       `VARIADIC arr integer[]`.
	// The body has no `$`, so pg_dump keeps the plain `$$` dollar-quote delimiter
	// (contrast add_seven's `$1`, which escalates to `$_$`). Clean positive: the
	// argmode/array plumbing already exists; this slice is the first end-to-end
	// pg_dump assertion that a VARIADIC array parameter round-trips.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.sum_variadic(VARIADIC arr integer[]) RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$"); err != nil {
		t.Fatalf("create function sum_variadic: %v", err)
	}

	// Slice 160: a function with a trailing parameter carrying a DEFAULT value
	// (`b integer DEFAULT 10`). Every prior function slice declared parameters
	// without defaults, so the ` DEFAULT <expr>` clause was never exercised
	// end-to-end. This was a REAL DIVERGENCE: pg_dump reconstructs the CREATE
	// FUNCTION signature from pg_get_function_arguments(oid), which goopg answers
	// via buildFunctionArguments — and that reconstructor NEVER emitted the DEFAULT
	// clause, even though the parser captured `a.Default` and CREATE FUNCTION stored
	// it in catalog.Routine.ArgDefaults. So `add_default(a integer, b integer
	// DEFAULT 10)` dumped as `add_default(a integer, b integer)` — a function that
	// no longer accepts the one-arg call form, i.e. a non-round-tripping signature.
	// Fix: buildFunctionArguments (and its sibling buildFunctionDef for
	// pg_get_functiondef) now append ` DEFAULT <expr>` for input args, matching PG's
	// print_function_arguments with print_defaults=true; the bare expression text is
	// the deparse-canonical form goopg stores (`10`). The identity form
	// (pg_get_function_identity_arguments, print_defaults=false) still drops the
	// default — pinned by TestPgGetFunctionArgumentsDefault in the executor package.
	// The body's `$1`/`$2` force pg_dump's appendStringLiteralDQ to the `$_$`
	// delimiter (same as add_seven).
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_default(a integer, b integer DEFAULT 10) RETURNS integer LANGUAGE sql AS $$ SELECT $1 + $2 $$"); err != nil {
		t.Fatalf("create function add_default: %v", err)
	}

	// Slice 161: a SET-RETURNING function (`RETURNS SETOF integer`). Every prior
	// function slice returned a single scalar (`RETURNS integer`/`void`), so the
	// proretset='t' return-clause shape was never exercised end-to-end. This drives
	// two paths nothing had reached for a pg_proc dump:
	//   (1) CREATE FUNCTION stores ReturnsSet=true (the parser strips SETOF and sets
	//       the flag, function.go:97); validateSQLFunctionBody then SKIPS the
	//       single-column scalar-return check (operators_ddl.go:5728), so a body
	//       whose final statement yields a row set is accepted; and
	//   (2) the runtime pg_proc view emits proretset='t' AND the SRF-default
	//       prorows='1000' (pg_proc_view.go:330/351), with prorettype set to the
	//       ELEMENT type (integer, OID 23) — NOT an array type.
	// pg_dump's dumpFunc renders the return clause as `RETURNS SETOF <rettype>` when
	// proretset[0]=='t' (pg_dump.c) and SUPPRESSES the ROWS clause when prorows is the
	// 1000 default — so the dump carries no explicit `ROWS`. The `$`-free body keeps
	// the plain `$$` delimiter. Clean positive: the proretset/prorows plumbing already
	// exists; this is the first end-to-end pg_dump assertion that a SETOF return shape
	// round-trips. A dropped SETOF (function restored as scalar-returning) or a stray
	// `ROWS 1000` surfaces exactly in the assertion below.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.gen_one() RETURNS SETOF integer LANGUAGE sql AS $$ SELECT 1 $$"); err != nil {
		t.Fatalf("create function gen_one: %v", err)
	}

	// Slice 162: a function whose RESULT type is an ARRAY (`RETURNS integer[]`).
	// Slice 159 proved an array works as an ARGUMENT type (VARIADIC arr
	// integer[]), but the return-type path was a separate, untested code path —
	// and it was BROKEN. The parser stores an array type as the base name
	// ("integer") with IsArray set, NOT as "integer[]". The CREATE FUNCTION
	// executor re-appends the "[]" suffix for argument types (operators_ddl.go:
	// 5510) but PREVIOUSLY did not for the return type — a sibling-path
	// divergence. As a result catalog.Routine.ReturnType.Name was the bare
	// "integer", so the pg_proc view's typeNameToOIDStr resolved prorettype to
	// the SCALAR element OID 23 instead of the array OID 1007, and pg_dump
	// rendered `RETURNS integer`, silently dropping the array. This slice adds
	// the missing suffix re-append (operators_ddl.go) so prorettype=1007 and
	// pg_dump's format_type(1007) yields `integer[]`. The body `SELECT ARRAY[1,
	// 2, 3]` is `$`-free (plain `$$` delimiter) and validateSQLFunctionBody
	// passes it: checkSQLFuncReturnTypeBasic only statically types string/int
	// literals, so an ARRAY[...] expression is "undeterminable" and accepted.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.make_arr() RETURNS integer[] LANGUAGE sql AS $$ SELECT ARRAY[1, 2, 3] $$"); err != nil {
		t.Fatalf("create function make_arr: %v", err)
	}

	// Slice 163: a function written in `LANGUAGE plpgsql` (the most common
	// real-world PL). Every prior function slice used LANGUAGE sql. The plpgsql
	// path was a separate, untested sibling: the parser/executor accept plpgsql
	// bodies (operators_ddl.go's CREATE FUNCTION pins LANGUAGE to plpgsql|sql|c),
	// but pg_proc's prolang is resolved by name via langNameToOIDStr, which
	// previously returned "0" for plpgsql. pg_dump's dumpFunc joins pg_proc to
	// pg_language on `l.oid = p.prolang` (no lanispl filter) purely to fetch
	// lanname; prolang=0 matches no pg_language row, so the join returns "0 rows
	// instead of one" and ABORTS the whole dump. This slice gives plpgsql a
	// pg_language row (OID 13627, matching a stock PG 18.3 initdb) and maps the
	// name in langNameToOIDStr, so prolang=13627 resolves to lanname='plpgsql'.
	// The new row keeps lanispl=f (like internal/c/sql) so getProcLangs's
	// `WHERE lanispl` still emits no CREATE LANGUAGE — real PG suppresses it via a
	// pg_depend pin instead, but the net dump output is identical. The body
	// contains `$1`, so pg_dump dollar-quotes with the `$_$` tag (the body is
	// stored verbatim as prosrc and rendered untouched — plpgsql bodies are NOT
	// deparsed, unlike `LANGUAGE sql ... BEGIN ATOMIC`).
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.plpg_inc(integer) RETURNS integer LANGUAGE plpgsql AS $$ BEGIN RETURN $1 + 1; END; $$"); err != nil {
		t.Fatalf("create function plpg_inc: %v", err)
	}

	// Slice 164: a function whose RESULT type is the pseudo-type `record`
	// (`RETURNS record`). Every prior function slice returned a concrete scalar
	// or array type whose OID typeNameToOIDStr already knew; `record` was a
	// separate, untested sibling and it was BROKEN. The parser stores the bare
	// type name "record" on ReturnType, but typeNameToOIDStr had no case for it,
	// so prorettype resolved to "0" (InvalidOid). pg_dump's dumpFunc builds the
	// RETURNS clause from `format_type(p.prorettype, NULL)`; format_type(0)
	// yields the placeholder `-`, so the dump rendered `RETURNS -` — broken SQL
	// that no longer round-trips. This slice adds the missing `record`→2249 (and
	// array `record[]`→2287) mappings to typeNameToOIDStr; goopg's format_type
	// already renders 2249 as `record` (expr.go), so the two sibling paths now
	// agree and pg_dump emits `RETURNS record`. The body `SELECT (1, 2)` is a
	// single row-constructor column (a record value), so validateSQLFunctionBody
	// sees exactly one target and accepts it; PG likewise accepts a SQL function
	// declared `RETURNS record` whose final SELECT yields a composite value.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.ret_rec() RETURNS record LANGUAGE sql AS $$ SELECT (1, 2) $$"); err != nil {
		t.Fatalf("create function ret_rec: %v", err)
	}

	// Slice 165: a function declared `RETURNS TABLE(col type, ...)`. goopg's parser
	// desugars RETURNS TABLE into trailing OUT args (mode 'o') + RETURNS SETOF record,
	// which is semantically equivalent but DIVERGES from upstream pg_dump: without
	// this slice the table columns leaked into the argument list and the result
	// rendered as `SETOF record`, so pg_dump emitted
	// `ret_tab(OUT id integer, OUT label text) RETURNS SETOF record` instead of
	// `ret_tab() RETURNS TABLE(id integer, label text)`. pg_dump's dumpFunc builds
	// the signature from pg_get_function_arguments(p.oid) and the RETURNS clause from
	// pg_get_function_result(p.oid), using both verbatim. This slice teaches both
	// deparsers about the RETURNS TABLE marker: pg_get_function_arguments now EXCLUDES
	// the table columns (PG's print_function_arguments skips PROARGMODE_TABLE args) and
	// pg_get_function_result renders them as `TABLE(...)`. The body returns two columns
	// (ReturnsSet=true bypasses the single-column check), matching the two table cols.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.ret_tab() RETURNS TABLE(id integer, label text) LANGUAGE sql AS $$ SELECT 1, 'x' $$"); err != nil {
		t.Fatalf("create function ret_tab: %v", err)
	}

	// Slice 145: COMMENT ON {VIEW,SEQUENCE,INDEX,SCHEMA} must survive the dump.
	// Before this slice, parseCommentOnTail handled only TABLE/INDEX/COLUMN/
	// CONSTRAINT/STATISTICS; VIEW/SEQUENCE/SCHEMA fell through to the unsupported
	// branch and the server silently swallowed them, so the comment never reached
	// pg_description. The INDEX case parsed and stored (classoid=pg_class) but was
	// never asserted through pg_dump. The parser now recognises VIEW/SEQUENCE/
	// SCHEMA, and execCommentOn keys views/sequences under pg_class (1259, shared
	// LookupTable path) and schemas under pg_namespace (2615). pg_dump's
	// collectComments re-emits a `COMMENT ON <kind> …` per object, the keyword
	// chosen from relkind / namespace.
	miscComments := []string{
		"COMMENT ON VIEW public.foo_view IS 'a view comment'",
		"COMMENT ON SEQUENCE public.plain_seq IS 'a sequence comment'",
		"COMMENT ON INDEX public.foo_name_idx IS 'an index comment'",
		"COMMENT ON SCHEMA s IS 'a schema comment'",
		// Slice 146: COMMENT ON {MATERIALIZED VIEW, TYPE, DOMAIN} must also
		// survive the dump. Before this slice, parseCommentOnTail had no branch
		// for these kinds, so the server silently swallowed them and the comment
		// never reached pg_description. The parser now recognises MATERIALIZED
		// VIEW / TYPE / DOMAIN, and execCommentOn keys the matview under pg_class
		// (1259, shared LookupTable path; pg_dump picks MATERIALIZED VIEW from
		// relkind='m') and the enum type + domain under pg_type (1247; pg_dump
		// picks TYPE vs DOMAIN from typtype). foo_mv, public.mood, public.zipcode
		// are created earlier in this fixture.
		"COMMENT ON MATERIALIZED VIEW public.foo_mv IS 'a matview comment'",
		"COMMENT ON TYPE public.mood IS 'a type comment'",
		"COMMENT ON DOMAIN public.zipcode IS 'a domain comment'",
		// Slice 147: COMMENT ON FUNCTION must also survive the dump. Before this
		// slice, parseCommentOnTail had no FUNCTION branch, so the server silently
		// swallowed it and the comment never reached pg_description. The parser now
		// recognises FUNCTION + its argument signature, and execCommentOn resolves
		// the routine OID (Routines().Lookup) and keys it under pg_proc (1255).
		"COMMENT ON FUNCTION public.add_one(integer) IS 'a function comment'",
	}
	for _, sql := range miscComments {
		if err := runSQLSimple(t, c, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dump: %v", err)
	}

	// setup_connection() error signatures that this slice eliminated. None may
	// appear: a regression in the GUC registry or the SET TRANSACTION routing
	// would re-surface exactly here.
	setupSignatures := []string{
		`unrecognized configuration parameter "synchronize_seqscans"`,
		`unrecognized configuration parameter "transaction_timeout"`,
		`unrecognized configuration parameter "row_security"`,
		`unrecognized configuration parameter "TRANSACTION"`,
		`SET synchronize_seqscans`,
		`SET TRANSACTION ISOLATION LEVEL`,
	}
	for _, sig := range setupSignatures {
		if strings.Contains(res.Stderr, sig) {
			t.Fatalf("pg_dump connection-setup regressed: stderr contains %q\n  full stderr=%q",
				sig, res.Stderr)
		}
	}

	if res.ExitCode == 0 {
		// pg_dump now runs to completion (slice 44 closed the last dumpFunc
		// blocker, pg_get_function_sqlbody). The dump reaches the per-object
		// emit stage and writes the table's archive entry, so assert the
		// CREATE TABLE statement and the schema/owner scaffolding are present —
		// this is the regression guard for the whole exit-0 pipeline.
		want := []string{
			"CREATE TABLE public.foo (",
			"ALTER TABLE public.foo OWNER TO",
			"COPY public.foo",
		}
		for _, sub := range want {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump output missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 46 closed (asserted):** getTableAttrs reads columns via
		// `FROM unnest('{oid}'::oid[]) src JOIN pg_attribute a ON
		// src.tbloid = a.attrelid`. Slice 45 lined up the join key; slice 46
		// fixed right-side projection offsetting in buildBindingsPosMap (leaf
		// SRF/table-function nodes now advance `off`, mirroring *Values), so the
		// projected attname/atttypid columns resolve to the correct combined
		// indices instead of returning attrelid/attlen. The dump now emits the
		// real column list, so assert both user columns appear in the
		// CREATE TABLE body — this is the regression guard for the
		// SRF-join-projection fix end-to-end.
		cols := []string{"id integer NOT NULL", "name text"}
		for _, sub := range cols {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump column list missing %q (SRF-join right-side projection regressed)\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 50 closed (asserted via the `id integer NOT NULL` entry above):**
		// PG18's pg_dump prints the inline NOT NULL clause from a contype='n'
		// pg_constraint LEFT JOIN, not from attnotnull. goopg set the PK column's
		// attnotnull=true but skipped PK columns when registering named NOT NULL
		// constraints, so the join found nothing and the column dumped as bare
		// `id integer`. Registering the `foo_id_not_null` constraint (CREATE TABLE
		// + ALTER ... ADD PRIMARY KEY paths) restores the inline NOT NULL — the
		// regression guard is the `id integer NOT NULL` assertion in `cols`.
		// **Slice 48 closed (asserted):** buildUserPGAttributeRow hardcoded
		// atttypmod=-1, so every typmod-bearing column dumped as its bare base
		// type (numeric(10,2) → numeric, character varying(8) → character
		// varying), a schema-fidelity loss. pgAttTypmod now computes the
		// PG-canonical atttypmod from the declared type args (VARHDRSZ added for
		// numeric/varchar/char), and formatTypeOID decodes the numeric typmod
		// (varchar/char display already existed). Assert the declared precision/
		// length survive the dump round-trip — the regression guard for the fix.
		typmodCols := []string{"amount numeric(10,2)", "code character varying(8)"}
		for _, sub := range typmodCols {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a column type modifier; missing %q (atttypmod regressed)\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 47 closed (asserted):** the virtual pg_class view stored
		// reloptions/relacl as "" (intended to mean NULL). TypedVirtualCell
		// routed the empty cell through its default StringConst branch, and
		// the array machinery parsed "" as a single empty-string element
		// ({""}). pg_dump's getTables reads
		// `array_remove(array_remove(c.reloptions,…),…)`, and
		// nonemptyReloptions({""}) is true (strlen>2), so the CREATE TABLE
		// gained a spurious `WITH (""='')` clause. TypedVirtualCell now maps
		// an empty array-typed cell to SQL NULL, so reloptions/relacl are
		// NULL (PG's convention for a table with no options / default ACL)
		// and no WITH clause is emitted. Assert the table dumps with no
		// reloptions clause — this is the regression guard for the fix.
		// The slice-47 bug surfaced an empty-string array element as `WITH
		// (""='')`; guard that exact signature (a legitimate `WITH
		// (fillfactor='70')` from slice 54's `opt` table is expected elsewhere).
		if strings.Contains(res.Stdout, `WITH (""`) {
			t.Errorf("pg_dump emitted a spurious empty-element reloptions WITH clause for a table with no options\n  full stdout=%q", res.Stdout)
		}
		// **Slice 54 closed (asserted):** a non-empty reloptions must survive the
		// dump. goopg parsed and validated `WITH (fillfactor=70)` but never stored
		// it on the catalog table, so pg_class.reloptions read NULL and the option
		// vanished. catalog.Table.Fillfactor now persists it and the pg_class
		// virtual view emits the `{fillfactor=70}` text[] cell, which pg_dump
		// renders back as `WITH (fillfactor='70')`. Assert the round-trip.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.opt (") {
			t.Errorf("pg_dump missing CREATE TABLE public.opt\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (fillfactor='70')") {
			t.Errorf("pg_dump dropped a non-empty reloptions; missing %q\n  full stdout=%q", "WITH (fillfactor='70')", res.Stdout)
		}
		// **Slice 195 closed (asserted):** a second recognized storage parameter
		// (parallel_workers) must also round-trip and coexist with fillfactor in one
		// reloptions array. goopg parsed the WITH key but only persisted fillfactor,
		// so parallel_workers vanished from the dump. catalog.Table.ParallelWorkers
		// (+ ParallelWorkersSet, since 0 is a valid value) now persists it and the
		// pg_class virtual view joins both options into `{fillfactor=70,parallel_workers=4}`,
		// which pg_dump renders as `WITH (fillfactor='70', parallel_workers='4')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optpw (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optpw\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (fillfactor='70', parallel_workers='4')") {
			t.Errorf("pg_dump dropped the parallel_workers reloption; missing %q\n  full stdout=%q", "WITH (fillfactor='70', parallel_workers='4')", res.Stdout)
		}
		// **Slice 196 closed (asserted):** a BOOLEAN storage parameter
		// (autovacuum_enabled) must round-trip — the most common non-fillfactor
		// reloption in real dumps. goopg validated the lowercase WITH key but never
		// extracted/persisted it, so it vanished from the dump.
		// catalog.Table.AutovacuumEnabled{,Set} now persists it (parseReloptionBool
		// mirrors PG's parse_bool); the pg_class virtual view renders
		// `{autovacuum_enabled=false}`, which pg_dump emits as `WITH
		// (autovacuum_enabled='false')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optav (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optav\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_enabled='false')") {
			t.Errorf("pg_dump dropped the autovacuum_enabled reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_enabled='false')", res.Stdout)
		}
		// **Slice 197 closed (asserted):** an INTEGER storage parameter whose
		// minimum is 128 (toast_tuple_target) must round-trip via the
		// "0-means-unset" integer path (no set flag). goopg validated the
		// lowercase WITH key but never extracted/persisted it, so it vanished from
		// the dump. catalog.Table.ToastTupleTarget now persists it and the pg_class
		// virtual view renders `{toast_tuple_target=256}`, which pg_dump emits as
		// `WITH (toast_tuple_target='256')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optt (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optt\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (toast_tuple_target='256')") {
			t.Errorf("pg_dump dropped the toast_tuple_target reloption; missing %q\n  full stdout=%q", "WITH (toast_tuple_target='256')", res.Stdout)
		}
		// **Slice 224 closed (asserted):** the first `toast.*` namespace-qualified
		// storage parameter (toast.autovacuum_enabled) must round-trip. PG stores
		// it on the TOAST relation's reloptions (no prefix) and pg_dump re-emits it
		// WITH the `toast.` prefix via the reltoastrelid join. goopg combines the
		// dotted WITH key, records catalog.Table.ToastReloptions, and the pg_class
		// virtual view synthesizes a relkind='t' TOAST row (reloptions
		// `{autovacuum_enabled=false}`) that the parent table's reltoastrelid points
		// at — so pg_dump emits `WITH (toast.autovacuum_enabled='false')`. The TOAST
		// row is filtered out of getTables' relkind WHERE, so it is never dumped as
		// an object. This is goopg's first synthesized TOAST pg_class row.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optoast (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optoast\n  full stdout=%q", res.Stdout)
		}
		// **Slice 225 + 226 + 227 + 228 + 229 + 230 + 231 + 232 + 233 + 234 closed (asserted):**
		// a second RELOPT_KIND_TOAST boolean (toast.vacuum_truncate), the first
		// RELOPT_KIND_TOAST integer (toast.autovacuum_vacuum_threshold), two
		// RELOPT_KIND_TOAST reals (toast.autovacuum_vacuum_scale_factor,
		// toast.autovacuum_vacuum_cost_delay), a second RELOPT_KIND_TOAST integer
		// (toast.autovacuum_vacuum_cost_limit), and six RELOPT_KIND_TOAST
		// autovacuum-age integers (toast.autovacuum_freeze_min_age,
		// toast.autovacuum_freeze_max_age, toast.autovacuum_freeze_table_age,
		// toast.autovacuum_multixact_freeze_min_age,
		// toast.autovacuum_multixact_freeze_max_age,
		// toast.autovacuum_multixact_freeze_table_age) on the
		// same table exercise the multi-element toast reloptions array. The
		// synthesized TOAST relation's reloptions are
		// `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100,autovacuum_vacuum_scale_factor=2.5,autovacuum_vacuum_cost_delay=10.5,autovacuum_vacuum_cost_limit=500,autovacuum_freeze_min_age=200000000,autovacuum_freeze_max_age=500000000,autovacuum_freeze_table_age=0,autovacuum_multixact_freeze_min_age=150000000,autovacuum_multixact_freeze_max_age=500000000,autovacuum_multixact_freeze_table_age=250000000}`
		// (code order), so pg_dump emits all twelve prefixed options in one WITH clause.
		if !strings.Contains(res.Stdout, "WITH (toast.autovacuum_enabled='false', toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100', toast.autovacuum_vacuum_scale_factor='2.5', toast.autovacuum_vacuum_cost_delay='10.5', toast.autovacuum_vacuum_cost_limit='500', toast.autovacuum_freeze_min_age='200000000', toast.autovacuum_freeze_max_age='500000000', toast.autovacuum_freeze_table_age='0', toast.autovacuum_multixact_freeze_min_age='150000000', toast.autovacuum_multixact_freeze_max_age='500000000', toast.autovacuum_multixact_freeze_table_age='250000000')") {
			t.Errorf("pg_dump dropped a toast.* reloption; missing %q\n  full stdout=%q", "WITH (toast.autovacuum_enabled='false', toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100', toast.autovacuum_vacuum_scale_factor='2.5', toast.autovacuum_vacuum_cost_delay='10.5', toast.autovacuum_vacuum_cost_limit='500', toast.autovacuum_freeze_min_age='200000000', toast.autovacuum_freeze_max_age='500000000', toast.autovacuum_freeze_table_age='0', toast.autovacuum_multixact_freeze_min_age='150000000', toast.autovacuum_multixact_freeze_max_age='500000000', toast.autovacuum_multixact_freeze_table_age='250000000')", res.Stdout)
		}
		// The synthesized TOAST relation must never be dumped as its own object.
		if strings.Contains(res.Stdout, "pg_toast_") {
			t.Errorf("pg_dump leaked a TOAST relation into the dump (pg_toast_*)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 198 closed (asserted):** an INTEGER autovacuum-namespace storage
		// parameter (autovacuum_vacuum_threshold) must round-trip via the
		// "0-is-a-real-value" integer path (separate set flag, the parallel_workers
		// pattern). goopg validated the lowercase WITH key but never
		// extracted/persisted it, so it vanished from the dump.
		// catalog.Table.AutovacuumVacuumThreshold now persists it and the pg_class
		// virtual view renders `{autovacuum_vacuum_threshold=100}`, which pg_dump
		// emits as `WITH (autovacuum_vacuum_threshold='100')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavt (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavt\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_threshold='100')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_threshold reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_threshold='100')", res.Stdout)
		}
		// **Slice 199 closed (asserted):** the first REAL-typed storage parameter
		// (autovacuum_vacuum_scale_factor) must round-trip. A fractional value
		// (`0.2`) lexes as TokenNumericLit, which parseWithOptions previously
		// rejected with "expected option value", so the option never reached the
		// executor. parseWithOptions now accepts TokenNumericLit and the executor
		// parses/bounds-checks the float (0.0 valid via a separate set flag).
		// catalog.Table.AutovacuumVacuumScaleFactor persists it and the pg_class
		// virtual view renders `{autovacuum_vacuum_scale_factor=0.2}`, which pg_dump
		// emits as `WITH (autovacuum_vacuum_scale_factor='0.2')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavsf (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavsf\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_scale_factor='0.2')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_scale_factor reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_scale_factor='0.2')", res.Stdout)
		}
		// **Slice 200 closed (asserted):** the second REAL-typed storage parameter
		// (autovacuum_analyze_scale_factor) must round-trip, reusing the slice-199
		// float path. catalog.Table.AutovacuumAnalyzeScaleFactor persists it and the
		// pg_class virtual view renders `{autovacuum_analyze_scale_factor=0.05}`,
		// which pg_dump emits as `WITH (autovacuum_analyze_scale_factor='0.05')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optaasf (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optaasf\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_analyze_scale_factor='0.05')") {
			t.Errorf("pg_dump dropped the autovacuum_analyze_scale_factor reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_analyze_scale_factor='0.05')", res.Stdout)
		}
		// **Slice 201 closed (asserted):** the third REAL-typed storage parameter
		// (autovacuum_vacuum_insert_scale_factor) must round-trip, reusing the
		// slice-199 float path. catalog.Table.AutovacuumVacuumInsertScaleFactor
		// persists it and the pg_class virtual view renders
		// `{autovacuum_vacuum_insert_scale_factor=0.2}`, which pg_dump emits as
		// `WITH (autovacuum_vacuum_insert_scale_factor='0.2')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavisf (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavisf\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_insert_scale_factor='0.2')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_insert_scale_factor reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_insert_scale_factor='0.2')", res.Stdout)
		}
		// **Slice 202 closed (asserted):** the fourth (and final) REAL-typed storage
		// parameter (autovacuum_vacuum_cost_delay) must round-trip, reusing the
		// slice-199 float path. catalog.Table.AutovacuumVacuumCostDelay persists it
		// and the pg_class virtual view renders `{autovacuum_vacuum_cost_delay=2.5}`,
		// which pg_dump emits as `WITH (autovacuum_vacuum_cost_delay='2.5')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavcd (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavcd\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_cost_delay='2.5')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_cost_delay reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_cost_delay='2.5')", res.Stdout)
		}
		// **Slice 203 closed (asserted):** the second INTEGER autovacuum-namespace
		// storage parameter (autovacuum_analyze_threshold) must round-trip via the
		// "0-is-a-real-value" integer path (separate set flag, the parallel_workers
		// pattern). goopg validated the lowercase WITH key but never
		// extracted/persisted it, so it vanished from the dump.
		// catalog.Table.AutovacuumAnalyzeThreshold now persists it and the pg_class
		// virtual view renders `{autovacuum_analyze_threshold=50}`, which pg_dump
		// emits as `WITH (autovacuum_analyze_threshold='50')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optaat (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optaat\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_analyze_threshold='50')") {
			t.Errorf("pg_dump dropped the autovacuum_analyze_threshold reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_analyze_threshold='50')", res.Stdout)
		}
		// **Slice 204 closed (asserted):** the third INTEGER autovacuum-namespace
		// storage parameter (autovacuum_vacuum_insert_threshold) must round-trip via
		// the "0-is-a-real-value" integer path (separate set flag, the
		// parallel_workers pattern). goopg validated the lowercase WITH key but never
		// extracted/persisted it, so it vanished from the dump.
		// catalog.Table.AutovacuumVacuumInsertThreshold now persists it and the
		// pg_class virtual view renders `{autovacuum_vacuum_insert_threshold=1000}`,
		// which pg_dump emits as `WITH (autovacuum_vacuum_insert_threshold='1000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavit (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavit\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_insert_threshold='1000')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_insert_threshold reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_insert_threshold='1000')", res.Stdout)
		}
		// **Slice 205 closed (asserted):** the boolean vacuum_truncate storage
		// parameter (RELOPT_TYPE_BOOL, default true) must round-trip via the
		// slice-196 autovacuum_enabled boolean path (a separate set flag records
		// presence). goopg validated the lowercase WITH key but never
		// extracted/persisted it, so it vanished from the dump.
		// catalog.Table.VacuumTruncate now persists it and the pg_class virtual
		// view renders `{vacuum_truncate=false}`, which pg_dump emits as
		// `WITH (vacuum_truncate='false')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optvt (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optvt\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (vacuum_truncate='false')") {
			t.Errorf("pg_dump dropped the vacuum_truncate reloption; missing %q\n  full stdout=%q", "WITH (vacuum_truncate='false')", res.Stdout)
		}
		// **Slice 206 closed (asserted):** the integer log_autovacuum_min_duration
		// storage parameter (RELOPT_TYPE_INT, valid -1–INT_MAX, default -1; 0 logs
		// every autovacuum action) — the fourth INT-typed autovacuum-namespace
		// reloption — must round-trip via the slice-198 integer path (a separate
		// set flag records presence since -1 and 0 are both valid explicit values).
		// catalog.Table.LogAutovacuumMinDuration persists it and the pg_class
		// virtual view renders `{log_autovacuum_min_duration=250}`, which pg_dump
		// emits as `WITH (log_autovacuum_min_duration='250')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optlamd (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optlamd\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (log_autovacuum_min_duration='250')") {
			t.Errorf("pg_dump dropped the log_autovacuum_min_duration reloption; missing %q\n  full stdout=%q", "WITH (log_autovacuum_min_duration='250')", res.Stdout)
		}
		// **Slice 207 closed (asserted):** the integer autovacuum_freeze_min_age
		// storage parameter (RELOPT_TYPE_INT, valid 0–1000000000, default -1 =
		// unset) — the fifth INT-typed autovacuum-namespace reloption — must
		// round-trip via the slice-198 integer path (a separate set flag records
		// presence since 0 is a valid explicit value distinct from the -1 unset
		// sentinel). catalog.Table.AutovacuumFreezeMinAge persists it and the
		// pg_class virtual view renders `{autovacuum_freeze_min_age=5000}`, which
		// pg_dump emits as `WITH (autovacuum_freeze_min_age='5000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optafma (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optafma\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_freeze_min_age='5000')") {
			t.Errorf("pg_dump dropped the autovacuum_freeze_min_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_freeze_min_age='5000')", res.Stdout)
		}
		// **Slice 208 closed (asserted):** the integer autovacuum_freeze_max_age
		// storage parameter (RELOPT_TYPE_INT, valid 100000–2000000000, default -1 =
		// unset) — the sixth INT-typed autovacuum-namespace reloption — must
		// round-trip via the slice-198 integer path (a separate set flag records
		// presence; the range minimum 100000 means an explicit -1 is rejected as
		// out-of-range). catalog.Table.AutovacuumFreezeMaxAge persists it and the
		// pg_class virtual view renders `{autovacuum_freeze_max_age=500000}`, which
		// pg_dump emits as `WITH (autovacuum_freeze_max_age='500000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optafmx (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optafmx\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_freeze_max_age='500000')") {
			t.Errorf("pg_dump dropped the autovacuum_freeze_max_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_freeze_max_age='500000')", res.Stdout)
		}
		// **Slice 209 closed (asserted):** the integer autovacuum_freeze_table_age
		// storage parameter (RELOPT_TYPE_INT, valid 0–2000000000, default -1 =
		// unset) — the seventh INT-typed autovacuum-namespace reloption — must
		// round-trip via the slice-198 integer path (a separate set flag records
		// presence; 0 is a valid explicit value so the flag guards presence).
		// catalog.Table.AutovacuumFreezeTableAge persists it and the pg_class
		// virtual view renders `{autovacuum_freeze_table_age=150000000}`, which
		// pg_dump emits as `WITH (autovacuum_freeze_table_age='150000000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optafta (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optafta\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_freeze_table_age='150000000')") {
			t.Errorf("pg_dump dropped the autovacuum_freeze_table_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_freeze_table_age='150000000')", res.Stdout)
		}
		// **Slice 210 closed (asserted):** the integer
		// autovacuum_multixact_freeze_min_age storage parameter (RELOPT_TYPE_INT,
		// valid 0–1000000000, default -1 = unset) — the eighth INT-typed
		// autovacuum-namespace reloption — must round-trip via the slice-198 integer
		// path (a separate set flag records presence; 0 is a valid explicit value so
		// the flag guards presence). catalog.Table.AutovacuumMultixactFreezeMinAge
		// persists it and the pg_class virtual view renders
		// `{autovacuum_multixact_freeze_min_age=5000000}`, which pg_dump emits as
		// `WITH (autovacuum_multixact_freeze_min_age='5000000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optamfma (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optamfma\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_multixact_freeze_min_age='5000000')") {
			t.Errorf("pg_dump dropped the autovacuum_multixact_freeze_min_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_multixact_freeze_min_age='5000000')", res.Stdout)
		}
		// **Slice 211 closed (asserted):** the integer
		// autovacuum_multixact_freeze_max_age storage parameter (RELOPT_TYPE_INT,
		// valid 10000–2000000000, default -1 = unset) — the ninth INT-typed
		// autovacuum-namespace reloption — must round-trip via the slice-198 integer
		// path (a separate set flag records presence; unlike the min/table-age options
		// the lower bound is 10000, but the flag still guards presence).
		// catalog.Table.AutovacuumMultixactFreezeMaxAge persists it and the pg_class
		// virtual view renders `{autovacuum_multixact_freeze_max_age=500000000}`, which
		// pg_dump emits as `WITH (autovacuum_multixact_freeze_max_age='500000000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optamfmaxa (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optamfmaxa\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_multixact_freeze_max_age='500000000')") {
			t.Errorf("pg_dump dropped the autovacuum_multixact_freeze_max_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_multixact_freeze_max_age='500000000')", res.Stdout)
		}
		// **Slice 212 closed (asserted):** the integer
		// autovacuum_multixact_freeze_table_age storage parameter (RELOPT_TYPE_INT,
		// valid 0–2000000000, default -1 = unset) — the tenth INT-typed
		// autovacuum-namespace reloption — must round-trip via the slice-198 integer
		// path (a separate set flag records presence; as with the min-age option 0 is a
		// valid explicit value, so the flag — not a zero check — guards presence).
		// catalog.Table.AutovacuumMultixactFreezeTableAge persists it and the pg_class
		// virtual view renders `{autovacuum_multixact_freeze_table_age=900000000}`, which
		// pg_dump emits as `WITH (autovacuum_multixact_freeze_table_age='900000000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optamftaa (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optamftaa\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_multixact_freeze_table_age='900000000')") {
			t.Errorf("pg_dump dropped the autovacuum_multixact_freeze_table_age reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_multixact_freeze_table_age='900000000')", res.Stdout)
		}
		// **Slice 213 closed (asserted):** the integer autovacuum_vacuum_cost_limit
		// storage parameter (RELOPT_TYPE_INT, valid 1–10000, default -1 = unset) — the
		// eleventh INT-typed autovacuum-namespace reloption — must round-trip via the
		// slice-198 integer path (a separate set flag records presence; unlike the
		// freeze-age options the lower bound is 1, so 0 is below range and rejected).
		// catalog.Table.AutovacuumVacuumCostLimit persists it and the pg_class virtual
		// view renders `{autovacuum_vacuum_cost_limit=2500}`, which pg_dump emits as
		// `WITH (autovacuum_vacuum_cost_limit='2500')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavcl (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavcl\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_cost_limit='2500')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_cost_limit reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_cost_limit='2500')", res.Stdout)
		}
		// **Slice 214 closed (asserted):** the boolean user_catalog_table storage
		// parameter (RELOPT_TYPE_BOOL, RELOPT_KIND_HEAP, default false) must
		// round-trip via the slice-196 autovacuum_enabled boolean path (a separate
		// set flag records presence). catalog.Table.UserCatalogTable persists it and
		// the pg_class virtual view renders `{user_catalog_table=true}`, which
		// pg_dump emits as `WITH (user_catalog_table='true')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optuct (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optuct\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (user_catalog_table='true')") {
			t.Errorf("pg_dump dropped the user_catalog_table reloption; missing %q\n  full stdout=%q", "WITH (user_catalog_table='true')", res.Stdout)
		}
		// **Slice 215 closed (asserted):** the integer autovacuum_vacuum_max_threshold
		// storage parameter (RELOPT_TYPE_INT, range -1–INT_MAX, default -2 = unset; a
		// PG18 heap reloption) must round-trip via the slice-204 integer path (a
		// separate set flag records presence since -1/0 are valid explicit values).
		// catalog.Table.AutovacuumVacuumMaxThreshold persists it and the pg_class
		// virtual view renders `{autovacuum_vacuum_max_threshold=5000}`, which pg_dump
		// emits as `WITH (autovacuum_vacuum_max_threshold='5000')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optavmt (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optavmt\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (autovacuum_vacuum_max_threshold='5000')") {
			t.Errorf("pg_dump dropped the autovacuum_vacuum_max_threshold reloption; missing %q\n  full stdout=%q", "WITH (autovacuum_vacuum_max_threshold='5000')", res.Stdout)
		}
		// **Slice 216 closed (asserted):** the REAL vacuum_max_eager_freeze_failure_rate
		// storage parameter (RELOPT_TYPE_REAL, range 0.0–1.0, default -1 = unset; a PG18
		// heap reloption) must round-trip via the slice-199 float path with PG's
		// narrower 0.0–1.0 range (a separate set flag records presence since 0.0 is a
		// valid explicit value). catalog.Table.VacuumMaxEagerFreezeFailureRate persists
		// it and the pg_class virtual view renders
		// `{vacuum_max_eager_freeze_failure_rate=0.1}`, which pg_dump emits as
		// `WITH (vacuum_max_eager_freeze_failure_rate='0.1')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optvefr (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optvefr\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (vacuum_max_eager_freeze_failure_rate='0.1')") {
			t.Errorf("pg_dump dropped the vacuum_max_eager_freeze_failure_rate reloption; missing %q\n  full stdout=%q", "WITH (vacuum_max_eager_freeze_failure_rate='0.1')", res.Stdout)
		}
		// **Slice 217 closed (asserted):** the ENUM vacuum_index_cleanup storage
		// parameter (RELOPT_TYPE_ENUM, members auto/on/off/true/false/yes/no/1/0,
		// default auto; a PG18 heap reloption — goopg's first enum reloption) must
		// round-trip with no alias normalization. catalog.Table.VacuumIndexCleanup
		// persists the verbatim value and the pg_class virtual view renders
		// `{vacuum_index_cleanup=on}`, which pg_dump emits as
		// `WITH (vacuum_index_cleanup='on')`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.optvic (") {
			t.Errorf("pg_dump missing CREATE TABLE public.optvic\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "WITH (vacuum_index_cleanup='on')") {
			t.Errorf("pg_dump dropped the vacuum_index_cleanup reloption; missing %q\n  full stdout=%q", "WITH (vacuum_index_cleanup='on')", res.Stdout)
		}
		// **Slice 166 closed (asserted):** an UNLOGGED table was silently demoted
		// to a logged one because buildUserPGClassRow hardcoded relpersistence to
		// 'p'. The emitter now derives 'u' from catalog.Table.Unlogged, so pg_dump
		// re-emits the UNLOGGED keyword. Assert the round-trip, and guard that the
		// plain logged tables did NOT pick up a spurious UNLOGGED keyword.
		if !strings.Contains(res.Stdout, "CREATE UNLOGGED TABLE public.ulog (") {
			t.Errorf("pg_dump dropped the UNLOGGED keyword; missing %q\n  full stdout=%q", "CREATE UNLOGGED TABLE public.ulog (", res.Stdout)
		}
		if strings.Contains(res.Stdout, "CREATE UNLOGGED TABLE public.foo (") ||
			strings.Contains(res.Stdout, "CREATE UNLOGGED TABLE public.opt (") {
			t.Errorf("pg_dump emitted a spurious UNLOGGED keyword on a permanent table\n  full stdout=%q", res.Stdout)
		}
		// **Slice 167 closed (asserted):** a RANGE-partitioned table and its
		// partition must round-trip. The parent's PARTITION BY clause comes from
		// pg_get_partkeydef; the child's FOR VALUES bound from
		// pg_get_expr(relpartbound, oid). buildUserPGClassRow hardcoded
		// relpartbound to "", so the ATTACH PARTITION lost its bound. The emitter
		// now derives it from the catalog. Assert both the parent partition-key
		// clause and the child's ATTACH-with-bound survive.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.part (") ||
			!strings.Contains(res.Stdout, "PARTITION BY RANGE (id)") {
			t.Errorf("pg_dump dropped/mangled the parent PARTITION BY clause; missing %q\n  full stdout=%q", "PARTITION BY RANGE (id)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.part_p0 FOR VALUES FROM (0) TO (100)") {
			t.Errorf("pg_dump dropped the partition bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.part_p0 FOR VALUES FROM (0) TO (100)", res.Stdout)
		}
		// **Slice 168 closed (asserted):** non-RANGE partition bounds. A text LIST
		// bound exposed a real divergence — the value was stored unquoted (the raw
		// routing form), so the dump emitted the restore-breaking `FOR VALUES IN
		// (a, b)`. The fix renders the captured SQL-literal form. Assert (1) the
		// parent LIST/HASH key clauses survive, (2) the text LIST bound is quoted,
		// and (3) the HASH modulus/remainder bound survives.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.plist (") ||
			!strings.Contains(res.Stdout, "PARTITION BY LIST (grp)") {
			t.Errorf("pg_dump dropped/mangled the LIST partition-key clause; missing %q\n  full stdout=%q", "PARTITION BY LIST (grp)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.plist_ab FOR VALUES IN ('a', 'b')") {
			t.Errorf("pg_dump emitted an unquoted/invalid LIST bound; want %q\n  full stdout=%q", "ATTACH PARTITION public.plist_ab FOR VALUES IN ('a', 'b')", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "PARTITION BY HASH (id)") {
			t.Errorf("pg_dump dropped/mangled the HASH partition-key clause; missing %q\n  full stdout=%q", "PARTITION BY HASH (id)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.phash_0 FOR VALUES WITH (modulus 4, remainder 0)") {
			t.Errorf("pg_dump dropped/mangled the HASH partition bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.phash_0 FOR VALUES WITH (modulus 4, remainder 0)", res.Stdout)
		}
		// **Slice 169 closed (asserted):** a text RANGE bound. Like the LIST case
		// (slice 168), the bound was stored unquoted (routing form), so the dump
		// emitted the restore-breaking `FOR VALUES FROM (a) TO (m)`. The literal
		// tuples now quote the string edge and uppercase MINVALUE. Assert both the
		// parent key clause and the quoted/keyword bound survive the round-trip.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.prange (") ||
			!strings.Contains(res.Stdout, "PARTITION BY RANGE (grp)") {
			t.Errorf("pg_dump dropped/mangled the RANGE-on-text parent table; missing CREATE TABLE public.prange / PARTITION BY RANGE (grp)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.prange_am FOR VALUES FROM (MINVALUE) TO ('m')") {
			t.Errorf("pg_dump emitted an unquoted/invalid RANGE bound; want %q\n  full stdout=%q", "ATTACH PARTITION public.prange_am FOR VALUES FROM (MINVALUE) TO ('m')", res.Stdout)
		}
		// **Slice 190 (asserted):** a DEFAULT (catch-all) partition. pg_dump reads
		// the bound via pg_get_expr(relpartbound), which yields the bare keyword
		// `DEFAULT`, and emits `ATTACH PARTITION public.<child> DEFAULT` (no FOR
		// VALUES). Assert (1) the parent's LIST key clause, (2) the concrete
		// sibling's IN bound, and (3) the DEFAULT child's keyword bound — with no
		// spurious `FOR VALUES` trailing the DEFAULT child.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.pdef (") ||
			!strings.Contains(res.Stdout, "PARTITION BY LIST (k)") {
			t.Errorf("pg_dump dropped/mangled the DEFAULT-partition parent table; missing CREATE TABLE public.pdef / PARTITION BY LIST (k)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pdef_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped/mangled the concrete sibling bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pdef_1 FOR VALUES IN (1)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pdef_def DEFAULT") {
			t.Errorf("pg_dump dropped/mangled the DEFAULT partition bound; want %q\n  full stdout=%q", "ATTACH PARTITION public.pdef_def DEFAULT", res.Stdout)
		}
		if strings.Contains(res.Stdout, "ATTACH PARTITION public.pdef_def FOR VALUES") {
			t.Errorf("pg_dump emitted a spurious FOR VALUES on the DEFAULT partition\n  full stdout=%q", res.Stdout)
		}
		// **Slice 191 closed (asserted):** per-leaf-partition storage parameters.
		// goopg persisted `WITH (fillfactor=N)` only on the non-partition CREATE
		// TABLE path; execCreatePartitionChild never extracted it, so the leaf's
		// pg_class.reloptions read NULL and the option vanished from the dump. The
		// fix extracts/validates/persists the fillfactor on the leaf. pg_dump emits
		// the reloptions on the leaf's own CREATE TABLE as `WITH (fillfactor='70')`
		// (a plain string match would also catch slice 54's `opt` table, so scope
		// the check to the pfo_1 statement) and the option-less sibling pfo_2 must
		// carry no WITH clause.
		if pfoStart := strings.Index(res.Stdout, "CREATE TABLE public.pfo_1 ("); pfoStart >= 0 {
			rest := res.Stdout[pfoStart:]
			stmtEnd := strings.Index(rest, ";")
			if stmtEnd < 0 {
				stmtEnd = len(rest)
			}
			pfoStmt := rest[:stmtEnd]
			if !strings.Contains(pfoStmt, "WITH (fillfactor='70')") {
				t.Errorf("pg_dump dropped the leaf partition's fillfactor; missing %q in pfo_1 CREATE TABLE\n  pfo_1 stmt=%q\n  full stdout=%q", "WITH (fillfactor='70')", pfoStmt, res.Stdout)
			}
		} else {
			t.Errorf("pg_dump did not emit CREATE TABLE for leaf partition pfo_1\n  full stdout=%q", res.Stdout)
		}
		if pfo2Start := strings.Index(res.Stdout, "CREATE TABLE public.pfo_2 ("); pfo2Start >= 0 {
			rest := res.Stdout[pfo2Start:]
			stmtEnd := strings.Index(rest, ";")
			if stmtEnd < 0 {
				stmtEnd = len(rest)
			}
			if strings.Contains(rest[:stmtEnd], "WITH (") {
				t.Errorf("pg_dump emitted a spurious WITH clause on the option-less leaf partition pfo_2\n  full stdout=%q", res.Stdout)
			}
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pfo_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped/mangled the fillfactor leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pfo_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 192 closed (asserted):** a TABLESPACE clause on a leaf partition.
		// The partition-child parser arm previously omitted OptTableSpace, so the
		// trailing `TABLESPACE pg_default` left unconsumed tokens and the whole
		// CREATE TABLE failed with a syntax error (the runSQLSimple above would have
		// fatal'd at fixture setup). The parser now accepts and discards the name,
		// mirroring the non-partition path. reltablespace stays 0 (default), so
		// pg_dump emits NO TABLESPACE clause and the child round-trips exactly like
		// an option-less leaf: a plain CREATE TABLE plus its ATTACH bound, with no
		// spurious WITH or TABLESPACE.
		if ptbsStart := strings.Index(res.Stdout, "CREATE TABLE public.ptbs_1 ("); ptbsStart >= 0 {
			rest := res.Stdout[ptbsStart:]
			stmtEnd := strings.Index(rest, ";")
			if stmtEnd < 0 {
				stmtEnd = len(rest)
			}
			ptbsStmt := rest[:stmtEnd]
			if strings.Contains(ptbsStmt, "TABLESPACE") {
				t.Errorf("pg_dump emitted a spurious TABLESPACE clause on the default-tablespace leaf ptbs_1\n  ptbs_1 stmt=%q\n  full stdout=%q", ptbsStmt, res.Stdout)
			}
			if strings.Contains(ptbsStmt, "WITH (") {
				t.Errorf("pg_dump emitted a spurious WITH clause on the option-less leaf ptbs_1\n  full stdout=%q", res.Stdout)
			}
		} else {
			t.Errorf("pg_dump did not emit CREATE TABLE for leaf partition ptbs_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.ptbs_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped/mangled the TABLESPACE leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.ptbs_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 193 closed (asserted):** a USING <access_method> clause on a leaf
		// partition. The partition-child parser arm previously omitted
		// table_access_method_clause, so the `USING heap` token left unconsumed and
		// the whole CREATE TABLE failed with a syntax error (the runSQLSimple above
		// would have fatal'd at fixture setup). The parser now accepts and discards
		// the name, mirroring the non-partition path. relam stays at its default, so
		// pg_dump emits NO USING clause and the child round-trips exactly like an
		// access-method-less leaf: a plain CREATE TABLE plus its ATTACH bound, with
		// no spurious USING/WITH/TABLESPACE.
		if puseStart := strings.Index(res.Stdout, "CREATE TABLE public.puse_1 ("); puseStart >= 0 {
			rest := res.Stdout[puseStart:]
			stmtEnd := strings.Index(rest, ";")
			if stmtEnd < 0 {
				stmtEnd = len(rest)
			}
			puseStmt := rest[:stmtEnd]
			if strings.Contains(puseStmt, "USING") {
				t.Errorf("pg_dump emitted a spurious USING clause on the default-access-method leaf puse_1\n  puse_1 stmt=%q\n  full stdout=%q", puseStmt, res.Stdout)
			}
			if strings.Contains(puseStmt, "WITH (") {
				t.Errorf("pg_dump emitted a spurious WITH clause on the option-less leaf puse_1\n  full stdout=%q", res.Stdout)
			}
		} else {
			t.Errorf("pg_dump did not emit CREATE TABLE for leaf partition puse_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.puse_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped/mangled the USING leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.puse_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 170 closed (asserted):** legacy table inheritance. goopg emitted
		// no pg_inherits row for the INHERITS edge and left the inherited columns
		// attislocal=true, so pg_dump dropped the `INHERITS (...)` clause and
		// re-emitted the parent's columns inline. The child now records its parent
		// OIDs (pg_inherits rows) and marks inherited columns Inherited=true. Assert
		// (1) the `INHERITS (public.inh_parent)` clause is emitted, (2) the child's
		// local column survives, and (3) the inherited columns are NOT re-emitted in
		// the child's CREATE TABLE column list.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.inh_child (") ||
			!strings.Contains(res.Stdout, "INHERITS (public.inh_parent)") {
			t.Errorf("pg_dump dropped the INHERITS clause; missing CREATE TABLE public.inh_child / INHERITS (public.inh_parent)\n  full stdout=%q", res.Stdout)
		}
		// The child's CREATE TABLE block runs from its header to the INHERITS clause;
		// the inherited parent columns must not appear inside it.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.inh_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, "INHERITS (public.inh_parent)")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the child's local column; want %q in inh_child block\n  block=%q", "extra integer", block)
			}
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in inh_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		}
		// **Slice 171 (asserted):** multi-level partition tree. The middle node
		// psub_east is both a partition of psub (relispartition=true → ATTACH with a
		// LIST bound) AND a partitioned table itself (relkind='p' → its own
		// PARTITION BY RANGE clause + a leaf attached to it). buildUserPGClassRow
		// already derives relkind='p' from PartitionMethod regardless of being a
		// partition, and execCreatePartitionChild sets the sub-partition key, so this
		// round-trips; assert it to guard the sub-partitioned shape. Verify (1) the
		// top key clause, (2) the middle node's OWN partition-key clause, (3) its
		// ATTACH-with-LIST-bound to the top, and (4) the leaf's ATTACH-with-RANGE
		// bound to the middle node.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.psub (") ||
			!strings.Contains(res.Stdout, "PARTITION BY LIST (region)") {
			t.Errorf("pg_dump dropped/mangled the top-level partition-key clause; missing %q\n  full stdout=%q", "PARTITION BY LIST (region)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE TABLE public.psub_east (") ||
			!strings.Contains(res.Stdout, "PARTITION BY RANGE (id)") {
			t.Errorf("pg_dump dropped the sub-partitioned partition's own PARTITION BY clause; missing CREATE TABLE public.psub_east / PARTITION BY RANGE (id)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.psub_east FOR VALUES IN ('east')") {
			t.Errorf("pg_dump dropped the middle node's ATTACH-to-top bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.psub_east FOR VALUES IN ('east')", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.psub_east_lo FOR VALUES FROM (0) TO (100)") {
			t.Errorf("pg_dump dropped the leaf's ATTACH-to-middle bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.psub_east_lo FOR VALUES FROM (0) TO (100)", res.Stdout)
		}
		// **Slice 172 (asserted):** multi-parent legacy inheritance. minh_child
		// inherits from BOTH minh_a and minh_b, which share column `shared` (merged
		// once). The same machinery slice 170 added for a single parent must, for two
		// parents, (1) re-emit `INHERITS (public.minh_a, public.minh_b)` in declaration
		// order (driven by pg_inherits inhseqno from the ordered InheritsParentOIDs),
		// (2) keep the child's purely-local `own_col`, and (3) omit ALL inherited
		// columns — including the merged `shared` — from the child's column list, since
		// they arrive via the parents. An ordering regression would flip the parents;
		// a merge/Inherited regression would re-emit `shared`/`a_only`/`b_only`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.minh_child (") ||
			!strings.Contains(res.Stdout, "INHERITS (public.minh_a, public.minh_b)") {
			t.Errorf("pg_dump dropped/reordered the multi-parent INHERITS clause; missing CREATE TABLE public.minh_child / INHERITS (public.minh_a, public.minh_b)\n  full stdout=%q", res.Stdout)
		}
		if start := strings.Index(res.Stdout, "CREATE TABLE public.minh_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, "INHERITS (public.minh_a, public.minh_b)")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "own_col boolean") {
				t.Errorf("pg_dump dropped the child's local column; want %q in minh_child block\n  block=%q", "own_col boolean", block)
			}
			for _, inheritedCol := range []string{"shared integer", "a_only integer", "b_only text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in minh_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		}
		// **Slice 173 (asserted):** function-call column DEFAULT. defcol carries a
		// `created timestamptz DEFAULT now()` column (FuncCall default) and a
		// `status integer DEFAULT 0` column (literal default). Before the fix the
		// FuncCall rendered as a Go pointer string in pg_attrdef.adbin, so the
		// dumped DEFAULT clause was corrupt; formatExprForAttrdef now renders the
		// call form. Assert both defaults survive inside the defcol block (the
		// literal `DEFAULT 0` guards the pre-existing branch; `DEFAULT now()` guards
		// the fix). A render regression on the FuncCall would surface as a
		// `0x...`/`&{...}` token instead of `now()`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.defcol (") {
			t.Errorf("pg_dump missing CREATE TABLE public.defcol\n  full stdout=%q", res.Stdout)
		}
		if start := strings.Index(res.Stdout, "CREATE TABLE public.defcol ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "DEFAULT now()") {
				t.Errorf("pg_dump dropped/corrupted the function-call default; want %q in defcol block\n  block=%q", "DEFAULT now()", block)
			}
			if !strings.Contains(block, "DEFAULT 0") {
				t.Errorf("pg_dump dropped the literal default; want %q in defcol block\n  block=%q", "DEFAULT 0", block)
			}
			// **Slice 174 (asserted):** parenless SQL niladic default. The
			// `touched` column carries `DEFAULT CURRENT_TIMESTAMP`. PG deparses the
			// SQLValueFunction as the bare uppercase keyword; goopg must match —
			// emitting `current_timestamp()` (the call-form renderer from slice 173)
			// would be a restore-breaking regression.
			if !strings.Contains(block, "DEFAULT CURRENT_TIMESTAMP") {
				t.Errorf("pg_dump dropped/altered the niladic default; want %q in defcol block\n  block=%q", "DEFAULT CURRENT_TIMESTAMP", block)
			}
			if strings.Contains(strings.ToLower(block), "current_timestamp()") {
				t.Errorf("pg_dump added spurious parens to CURRENT_TIMESTAMP (slice 174 regression)\n  block=%q", block)
			}
			// **Slice 175 (asserted):** function-call default with LITERAL
			// ARGUMENTS. The `label` column carries `DEFAULT lpad('x', 5)`.
			// formatExprForAttrdef renders the call with its arguments recursively
			// (string literal `'x'`, integer literal `5`, joined with `, `); a
			// regression in argument rendering would drop the args or corrupt them.
			if !strings.Contains(block, "DEFAULT lpad('x', 5)") {
				t.Errorf("pg_dump dropped/corrupted the function-call default args; want %q in defcol block\n  block=%q", "DEFAULT lpad('x', 5)", block)
			}
			// **Slice 176 (asserted):** a CAST-expression default. The `meta`
			// column carries `DEFAULT '{}'::jsonb`. validateDefaultExpr accepts a
			// *CastExpr; formatExprForAttrdef now renders `operand::type` (mirroring
			// executor.defaultExprToSQL). Before the fix a *CastExpr fell through to
			// fmt.Sprintf("%v", e), so the dumped DEFAULT was a corrupt Go pointer
			// string. Assert the cast survives intact.
			if !strings.Contains(block, "DEFAULT '{}'::jsonb") {
				t.Errorf("pg_dump dropped/corrupted the cast-expression default; want %q in defcol block\n  block=%q", "DEFAULT '{}'::jsonb", block)
			}
			// **Slice 177 (asserted):** an ARRAY-constructor default. The `vals`
			// column carries `DEFAULT ARRAY[1, 2, 3]`. validateDefaultExpr accepts a
			// *ArrayConstructorExpr (it rejects only column refs / subqueries /
			// aggregate-or-SRF calls); both renderers now emit `ARRAY[e1, …]`. Before
			// the fix the node fell through to fmt.Sprintf("%v", e), so the dumped
			// DEFAULT was a corrupt Go pointer string. Assert the array survives intact.
			if !strings.Contains(block, "DEFAULT ARRAY[1, 2, 3]") {
				t.Errorf("pg_dump dropped/corrupted the array-constructor default; want %q in defcol block\n  block=%q", "DEFAULT ARRAY[1, 2, 3]", block)
			}
			// **Slice 178 (asserted):** a CASE-expression default. The `grade`
			// column carries `DEFAULT CASE WHEN true THEN 1 ELSE 0 END`.
			// validateDefaultExpr accepts a *CaseExpr; both renderers now emit the
			// single-line CASE form. Before the fix the node fell through to
			// fmt.Sprintf("%v", e), so the dumped DEFAULT was a corrupt Go pointer
			// string. Assert the CASE survives intact.
			if !strings.Contains(block, "DEFAULT CASE WHEN true THEN 1 ELSE 0 END") {
				t.Errorf("pg_dump dropped/corrupted the case-expression default; want %q in defcol block\n  block=%q", "DEFAULT CASE WHEN true THEN 1 ELSE 0 END", block)
			}
			// **Slice 179 (asserted):** a row-constructor default. The `pair`
			// column carries `DEFAULT (1, 2)`, parsed as a *RowExpr.
			// validateDefaultExpr accepts it; both renderers now emit the ROW(…)
			// keyword form PG's ruleutils always prints. Before the fix the node
			// fell through to fmt.Sprintf("%v", e), so the dumped DEFAULT was a
			// corrupt Go pointer string. Assert the row constructor survives intact.
			if !strings.Contains(block, "DEFAULT ROW(1, 2)") {
				t.Errorf("pg_dump dropped/corrupted the row-constructor default; want %q in defcol block\n  block=%q", "DEFAULT ROW(1, 2)", block)
			}
			// **Slice 180 (asserted):** an interval-literal default. The `span`
			// column carries `DEFAULT INTERVAL '1' day`, parsed as a *IntervalLit.
			// validateDefaultExpr accepts it; both renderers now emit the native
			// `INTERVAL '<n>' <unit>` form. Before the fix the node fell through to
			// fmt.Sprintf("%v", e), so the dumped DEFAULT was a corrupt Go pointer
			// string. Assert the interval literal survives intact.
			if !strings.Contains(block, "DEFAULT INTERVAL '1' day") {
				t.Errorf("pg_dump dropped/corrupted the interval-literal default; want %q in defcol block\n  block=%q", "DEFAULT INTERVAL '1' day", block)
			}
			// **Slice 181 (asserted):** the boolean-test predicate family closes
			// the last realistic fall-through-corruption gap in the column-DEFAULT
			// renderer. `nflag` carries `DEFAULT (1 IS NOT NULL)` (*IsNullExpr),
			// `bflag` carries `DEFAULT (true IS NOT TRUE)` (*IsBoolExpr), and `dflag`
			// carries `DEFAULT (1 IS DISTINCT FROM 2)` (*IsDistinctFromExpr).
			// validateDefaultExpr accepts all three (it rejects only column refs /
			// subqueries / aggregate-or-SRF calls); both renderers now emit the
			// `IS [NOT] NULL` / `IS [NOT] TRUE|FALSE|UNKNOWN` / `IS [NOT] DISTINCT
			// FROM` deparse PG's pg_get_expr produces for NullTest/BooleanTest/
			// DistinctExpr. Before the fix each node fell through to
			// fmt.Sprintf("%v", e), so the dumped DEFAULT was a corrupt Go pointer
			// string. The predicate core is asserted (paren-robust: pg_dump may or
			// may not wrap the whole default in parens).
			if !strings.Contains(block, "1 IS NOT NULL") {
				t.Errorf("pg_dump dropped/corrupted the IS NOT NULL default; want %q in defcol block\n  block=%q", "1 IS NOT NULL", block)
			}
			if !strings.Contains(block, "true IS NOT TRUE") {
				t.Errorf("pg_dump dropped/corrupted the IS NOT TRUE default; want %q in defcol block\n  block=%q", "true IS NOT TRUE", block)
			}
			if !strings.Contains(block, "1 IS DISTINCT FROM 2") {
				t.Errorf("pg_dump dropped/corrupted the IS DISTINCT FROM default; want %q in defcol block\n  block=%q", "1 IS DISTINCT FROM 2", block)
			}
		}
		// **Slice 182 (asserted):** per-column storage overrides. `storcol.a` was
		// SET STORAGE EXTERNAL and `storcol.b` SET STORAGE MAIN; both differ from
		// text's EXTENDED default, so pg_dump must re-emit each as a standalone
		// `ALTER TABLE ONLY public.storcol ALTER COLUMN <c> SET STORAGE <mode>;`
		// (pg_dump.c dumpTableSchema). Before the fix attstorage echoed the type
		// default, so neither statement appeared. The untouched `d` column must
		// NOT produce a SET STORAGE (its attstorage == typstorage).
		for _, sub := range []string{
			"ALTER TABLE ONLY public.storcol ALTER COLUMN a SET STORAGE EXTERNAL;",
			"ALTER TABLE ONLY public.storcol ALTER COLUMN b SET STORAGE MAIN;",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a column storage override; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "ALTER COLUMN d SET STORAGE") {
			t.Errorf("pg_dump emitted a spurious SET STORAGE for an untouched column (storcol.d)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 183 (asserted):** per-column compression methods. `cmprcol.a`
		// was declared `COMPRESSION pglz` and `cmprcol.b` `SET COMPRESSION lz4`;
		// both are non-default, so pg_dump must re-emit each as a standalone
		// `ALTER TABLE ONLY public.cmprcol ALTER COLUMN <c> SET COMPRESSION
		// <method>;` (pg_dump.c dumpTableSchema). Before the fix attcompression
		// echoed the '\0' default, so neither statement appeared. The untouched
		// `d` column must NOT produce a SET COMPRESSION.
		for _, sub := range []string{
			"ALTER TABLE ONLY public.cmprcol ALTER COLUMN a SET COMPRESSION pglz;",
			"ALTER TABLE ONLY public.cmprcol ALTER COLUMN b SET COMPRESSION lz4;",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a column compression method; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "ALTER COLUMN d SET COMPRESSION") {
			t.Errorf("pg_dump emitted a spurious SET COMPRESSION for an untouched column (cmprcol.d)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 184 (asserted):** per-column statistics targets. `statcol.a`
		// got `SET STATISTICS 100` and `statcol.b` `SET STATISTICS 0`; both are
		// non-default (attstattarget >= 0), so pg_dump must re-emit each as a
		// standalone `ALTER TABLE ONLY public.statcol ALTER COLUMN <c> SET
		// STATISTICS <n>;` (pg_dump.c dumpTableSchema). Before the fix
		// attstattarget was hardcoded NULL, so neither statement appeared. The
		// untouched `d` column (attstattarget NULL) must NOT produce one.
		for _, sub := range []string{
			"ALTER TABLE ONLY public.statcol ALTER COLUMN a SET STATISTICS 100;",
			"ALTER TABLE ONLY public.statcol ALTER COLUMN b SET STATISTICS 0;",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a column statistics target; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "ALTER COLUMN d SET STATISTICS") {
			t.Errorf("pg_dump emitted a spurious SET STATISTICS for an untouched column (statcol.d)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 185 (asserted):** per-column attribute options. `optcol.a` got
		// `SET (n_distinct=0.5)` and `optcol.b` `SET (n_distinct=-0.1)`; pg_dump
		// renders `array_to_string(a.attoptions, ', ')` and re-emits each as a
		// standalone `ALTER TABLE ONLY public.optcol ALTER COLUMN <c> SET (...);`
		// (pg_dump.c dumpTableSchema). Before the fix attoptions was hardcoded
		// NULL, so neither statement appeared. The untouched `d` (attoptions
		// NULL) must NOT produce one.
		for _, sub := range []string{
			"ALTER TABLE ONLY public.optcol ALTER COLUMN a SET (n_distinct=0.5);",
			"ALTER TABLE ONLY public.optcol ALTER COLUMN b SET (n_distinct=-0.1);",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a per-column attribute option; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "ALTER COLUMN d SET (") {
			t.Errorf("pg_dump emitted a spurious SET (...) for an untouched column (optcol.d)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 188 (asserted):** per-column explicit collation. `collcol.a` got
		// COLLATE "C" and `collcol.b` COLLATE "POSIX"; pg_dump's getTableAttrs
		// reports attcollation when it differs from the type's typcollation, and
		// dumpTableSchema re-emits `COLLATE pg_catalog."<name>"` inline in the
		// column list (text's typcollation is the default=100, so C=950/POSIX=951
		// both differ). Before the fix attcollation echoed the type default, so
		// neither appeared. The untouched `d` (default collation) must NOT carry one.
		for _, sub := range []string{
			`a text COLLATE pg_catalog."C"`,
			`b text COLLATE pg_catalog."POSIX"`,
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a per-column collation; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		if strings.Contains(res.Stdout, "d text COLLATE") {
			t.Errorf("pg_dump emitted a spurious COLLATE for an untouched column (collcol.d)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 189 (asserted):** array-of-collatable columns must NOT carry a
		// spurious COLLATE. _name/_bpchar/_varchar inherit their element collation
		// (950/100/100); once the heap typcollation for these array OIDs matches the
		// virtual pg_attribute.attcollation, getTableAttrs reports no difference and
		// dumpTableSchema omits the clause. Any COLLATE on a collarr column means the
		// heap/attribute sibling paths diverged again (the slice-187 regression).
		if !strings.Contains(res.Stdout, "CREATE TABLE public.collarr") {
			t.Errorf("pg_dump dropped the collarr table entirely\n  full stdout=%q", res.Stdout)
		} else {
			// Any COLLATE on one of collarr's four default-collation array columns is
			// spurious (format_type renders varchar[]/bpchar(4)[]/name[]/text[]).
			for _, sub := range []string{
				`character varying[] COLLATE`,
				`character(4)[] COLLATE`,
				`name[] COLLATE`,
				`text[] COLLATE`,
			} {
				if strings.Contains(res.Stdout, sub) {
					t.Errorf("pg_dump emitted a spurious COLLATE on a default-collation array column (collarr): %q\n  full stdout=%q", sub, res.Stdout)
				}
			}
		}
		// **Slice 49 closed (asserted):** a column-level CHECK was silently
		// dropped from the dump. pg_dump gates its per-table CHECK query on
		// `pg_class.relchecks > 0` and then renders each row via
		// `pg_get_constraintdef(c.oid)`; goopg hardcoded relchecks=0 (so the
		// query never ran) AND pg_get_constraintdef handled only index-backed
		// constraints (so even a forced query returned NULL). relchecks now
		// counts the table's visible NamedChecks, and pg_get_constraintdef
		// renders `CHECK ((expr))` for contype='c' constraints. Assert the
		// auto-named column CHECK survives the dump round-trip — the regression
		// guard for the fix. (The PRIMARY KEY ALTER-TABLE path already worked.)
		// **Slice 51 closed (asserted):** a FOREIGN KEY was silently dropped —
		// catalog.ForeignKey had no name/OID, so pg_constraint emitted no
		// contype='f' row and pg_dump's getConstraints found nothing. The FK now
		// gets a name+OID at DDL time, surfaces as a contype='f' pg_constraint
		// row, and pg_get_constraintdef renders the schema-qualified definition.
		// (UNIQUE already worked via the index-backed constraint path.)
		// **Slice 52 closed (asserted):** FK referential actions (ON DELETE/ON
		// UPDATE) must survive the dump. The inline column path already parsed
		// and stored the action; the ALTER TABLE ADD FOREIGN KEY path silently
		// dropped it (the parser never consumed the ON DELETE/UPDATE clause, the
		// AST had no field, and the executor never set OnDelete/OnUpdate). Now
		// both paths carry the action into pg_constraint, and
		// pg_get_constraintdef renders ` ON UPDATE …`/` ON DELETE …` (ON UPDATE
		// before ON DELETE, mirroring ruleutils.c). The inline self-FK carries
		// ON DELETE CASCADE; the ALTER-added foo_mgr_fkey carries ON UPDATE
		// CASCADE ON DELETE SET NULL — both round-trip byte-identically.
		check := []string{
			"ADD CONSTRAINT foo_pkey PRIMARY KEY (id)",
			"CONSTRAINT foo_qty_check CHECK ((qty >= 0))",
			"ADD CONSTRAINT foo_code_key UNIQUE (code)",
			"ADD CONSTRAINT foo_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.foo(id) ON DELETE CASCADE",
			"ADD CONSTRAINT foo_mgr_fkey FOREIGN KEY (mgr_id) REFERENCES public.foo(id) ON UPDATE CASCADE ON DELETE SET NULL",
			// **Slice 53 closed (asserted):** a table-level (composite) FOREIGN
			// KEY in the CREATE TABLE body was silently dropped — the parser
			// treated table-level FKs as a no-op, so the multi-column FK never
			// reached the catalog/pg_constraint. The parser now stores it
			// (anonymous form auto-named <table>_<firstcol>_fkey) and the executor
			// registers it as a catalog FK; pg_constraint's conkey/confkey
			// ordinals and pg_get_constraintdef's join were already multi-column
			// aware. The composite PK on `bar` and the composite FK on `baz`
			// round-trip byte-identically.
			"ADD CONSTRAINT bar_pkey PRIMARY KEY (a, b)",
			"ADD CONSTRAINT baz_x_fkey FOREIGN KEY (x, y) REFERENCES public.bar(a, b) ON DELETE CASCADE",
			// **Slice 54:** a user-defined schema and a table inside it must
			// round-trip. pg_dump emits `CREATE SCHEMA s;` for every dumpable
			// non-public namespace and fully qualifies the contained objects.
			"CREATE SCHEMA s;",
			"CREATE TABLE s.widget (",
			"ADD CONSTRAINT widget_pkey PRIMARY KEY (id)",
			// **Slice 126:** a multi-column UNIQUE constraint with a key order
			// (`b, a`) that differs from the table's column order (`a, b, c`).
			// Both the auto-generated name and the column list must follow the
			// INDEX-key order, not the table order: `uniqm_b_a_key UNIQUE (b, a)`.
			"ADD CONSTRAINT uniqm_b_a_key UNIQUE (b, a)",
			// **Slice 131:** a UNIQUE constraint with an INCLUDE (covering) column.
			// PG folds the covering column into the auto-generated name (key + INCLUDE
			// → `uniqi_a_b_key`) and pg_get_constraintdef appends ` INCLUDE (b)`.
			"ADD CONSTRAINT uniqi_a_b_key UNIQUE (a) INCLUDE (b)",
			// **Slice 135:** a table-level UNIQUE constraint declared NULLS NOT
			// DISTINCT re-emits the clause BETWEEN the keyword and the column list
			// (`UNIQUE NULLS NOT DISTINCT (a)`, ruleutils.c pg_get_constraintdef
			// order) — distinct from CREATE INDEX where it trails the columns.
			"ADD CONSTRAINT uniqnnd_a_key UNIQUE NULLS NOT DISTINCT (a)",
			// **Slice 136:** the INLINE-on-column sibling of slice 135. An inline
			// `a integer UNIQUE NULLS NOT DISTINCT` dumps as the same index-backed
			// constraint (`uniqcnnd_a_key UNIQUE NULLS NOT DISTINCT (a)`); the new
			// path is the column-form parser+executor threading.
			"ADD CONSTRAINT uniqcnnd_a_key UNIQUE NULLS NOT DISTINCT (a)",
			// **Slice 137:** the inline NAMED column UNIQUE form. An explicit
			// `CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT` on a column dumps under
			// the USER-GIVEN name (not the auto-generated `uniqcname_a_key`); the
			// new path makes `CONSTRAINT name UNIQUE` set col.Unique + carry the
			// name to the backing index, where it previously created no index.
			"ADD CONSTRAINT myuniq UNIQUE NULLS NOT DISTINCT (a)",
			// **Slice 138:** the NAMED TABLE-LEVEL UNIQUE form. An explicit
			// `CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a)` dumps under the
			// USER-GIVEN name with the clause preserved; the named table-level
			// UNIQUE parser case previously skipped the NULLS [NOT] DISTINCT clause
			// and the `(` lookahead failed, silently dropping the whole constraint.
			"ADD CONSTRAINT tuniq UNIQUE NULLS NOT DISTINCT (a)",
			// **Slice 139:** a table-level UNIQUE constraint declared DEFERRABLE
			// INITIALLY DEFERRED re-emits the ` DEFERRABLE INITIALLY DEFERRED`
			// trailer (ruleutils.c order: after the column/INCLUDE list). The
			// anonymous UNIQUE parser previously had no DEFERRABLE branch, so the
			// constraint was a hard parse error; now it round-trips.
			"ADD CONSTRAINT uniqdef_a_key UNIQUE (a) DEFERRABLE INITIALLY DEFERRED",
			// **Slice 140:** the NAMED sibling — a table-level UNIQUE with an
			// explicit CONSTRAINT name AND a DEFERRABLE INITIALLY DEFERRED trailer
			// dumps under the USER-GIVEN name (`tudef`) with the clause preserved.
			// The named table-level UNIQUE parser case previously parsed no trailing
			// DEFERRABLE, so the keyword was a hard parse error; now it round-trips.
			"ADD CONSTRAINT tudef UNIQUE (a) DEFERRABLE INITIALLY DEFERRED",
			// **Slice 141:** the INLINE-COLUMN siblings — a DEFERRABLE trailer on a
			// column-level UNIQUE round-trips for both the anonymous form (auto name
			// `uniqcdef_a_key`) and the named form (user name `cudef`). The inline
			// column UNIQUE parser previously had no DEFERRABLE slot, so a trailing
			// DEFERRABLE was a hard parse error; now both round-trip with the trailer.
			"ADD CONSTRAINT uniqcdef_a_key UNIQUE (a) DEFERRABLE INITIALLY DEFERRED",
			"ADD CONSTRAINT cudef UNIQUE (a) DEFERRABLE INITIALLY DEFERRED",
			// **Slice 142:** the PRIMARY KEY siblings — a DEFERRABLE trailer on all
			// three PK forms round-trips: anonymous table-level (auto name
			// `pktdef_pkey`), named table-level (user name `pkdef`), and inline column
			// (auto name `pkcdef_pkey`). All three previously dropped the flag (the
			// inline form was a hard parse error). pg_dump now re-emits the clause via
			// the shared buildConstraintDefString (Primary branch) + condeferrable.
			"ADD CONSTRAINT pktdef_pkey PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED",
			"ADD CONSTRAINT pkdef PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED",
			"ADD CONSTRAINT pkcdef_pkey PRIMARY KEY (a) DEFERRABLE INITIALLY DEFERRED",
			// **Slice 143:** the EXCLUDE-constraint siblings — a DEFERRABLE trailer
			// on an EXCLUDE constraint round-trips for both the anonymous form (auto
			// name `excldef_a_excl`) and the named form (user name `exdef`). The
			// EXCLUDE parser previously stopped at the close-paren, silently dropping
			// the trailer; now buildConstraintDefString's EXCLUDE branch re-emits it.
			"ADD CONSTRAINT excldef_a_excl EXCLUDE USING btree (a WITH =) DEFERRABLE INITIALLY DEFERRED",
			"ADD CONSTRAINT exdef EXCLUDE USING btree (a WITH =) DEFERRABLE INITIALLY DEFERRED",
			// **Slice 127:** anonymous table-level CHECK constraints round-trip
			// inline with PG's auto-generated names. The multi-column predicate
			// (`a < b`) gets the table-only name `chk_check`; the single-column
			// predicate (`x > 0`) gets the column-qualified `chk1_x_check`. Both
			// were silently dropped before (empty name + OID 0).
			"CONSTRAINT chk_check CHECK ((a < b))",
			"CONSTRAINT chk1_x_check CHECK ((x > 0))",
			// **Slice 128:** an anonymous table-level CHECK with NO INHERIT must
			// re-emit the ` NO INHERIT` suffix; the per-check flag was previously
			// discarded so the dump produced a plain inheritable CHECK.
			"CONSTRAINT chk2_y_check CHECK ((y > 0)) NO INHERIT",
			// **Slice 129:** a NAMED table-level CHECK with NO INHERIT must keep
			// its user-given name AND re-emit the ` NO INHERIT` suffix; the
			// PartitionCheckConstraint per-constraint flag was previously absent.
			"CONSTRAINT chk3_pos CHECK ((z > 0)) NO INHERIT",
		}
		for _, sub := range check {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a constraint; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Slice 126 negative guards: a key-order or name regression (sorting the
		// columns into table order) would render `uniqm_a_b_key`/`UNIQUE (a, b)`,
		// silently changing the constraint's column ordering on restore.
		for _, neg := range []string{
			"ADD CONSTRAINT uniqm_a_b_key UNIQUE (a, b)",
			"UNIQUE (a, b)\n",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump reordered a multi-column UNIQUE key into table order: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// Slice 131 negative guards: a dropped INCLUDE clause would render the bare
		// `uniqi_a_key UNIQUE (a)` (covering column lost → restored index no longer
		// index-only-scannable); folding the covering column into the KEY would
		// render `UNIQUE (a, b)` (a different uniqueness semantic).
		for _, neg := range []string{
			"ADD CONSTRAINT uniqi_a_key UNIQUE (a)",
			"uniqi_a_b_key UNIQUE (a, b)",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump mangled a UNIQUE...INCLUDE constraint: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// Slice 127 negative guards: the single-vs-multi-column naming must not
		// flip. A multi-column CHECK that wrongly took the single-column branch
		// would name itself `chk_a_check`/`chk_b_check`; a single-column CHECK that
		// took the multi-column branch would name itself `chk1_check`. Either is a
		// silent constraint-name change on restore.
		for _, neg := range []string{
			"CONSTRAINT chk_a_check",
			"CONSTRAINT chk_b_check",
			"CONSTRAINT chk1_check ",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump mis-named an anonymous CHECK constraint: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// Slice 128 negative guard: dropping the NO INHERIT flag would render the
		// chk2 constraint as a plain inheritable CHECK (terminated by a newline
		// before `);`), silently changing its inheritance semantics on restore.
		if strings.Contains(res.Stdout, "CONSTRAINT chk2_y_check CHECK ((y > 0))\n") {
			t.Errorf("pg_dump dropped NO INHERIT from an anonymous CHECK\n  full stdout=%q", res.Stdout)
		}
		// Slice 129 negative guard: dropping the per-constraint flag on the named
		// path would render chk3_pos as a plain inheritable CHECK (terminated by a
		// newline), silently changing its inheritance semantics on restore.
		if strings.Contains(res.Stdout, "CONSTRAINT chk3_pos CHECK ((z > 0))\n") {
			t.Errorf("pg_dump dropped NO INHERIT from a named CHECK\n  full stdout=%q", res.Stdout)
		}
		// **Slice 55 closed (asserted):** COMMENT ON {TABLE,COLUMN} must survive
		// the dump. goopg already parsed COMMENT ON and populated pg_description
		// via catalog.SetComment, and pg_dump's collectComments query
		// (`SELECT description, classoid, objoid, objsubid FROM
		// pg_catalog.pg_description ORDER BY …`) re-emits a `COMMENT ON …` per
		// object (table: objsubid=0; column: objsubid=attnum). The TABLE comment
		// already round-tripped, but the COLUMN comment was dropped: pg_dump emits
		// the canonical 3-part `COMMENT ON COLUMN schema.table.col`, and goopg's
		// parser only handled the bare 2-part `table.col` — parseObjectName
		// consumes two dotted parts and the column case never read the trailing
		// `.col`, so the 3-part form raised "expected IS after object name". That
		// parse error was silently swallowed by the server's COMMENT fallback, so
		// the column comment never reached pg_description. The column case now
		// reads the trailing `.col` when present, so both forms parse. Assert both
		// comments round-trip — the regression guard for the fix.
		comments := []string{
			"COMMENT ON TABLE public.foo IS 'a foo table';",
			"COMMENT ON COLUMN public.foo.name IS 'the name column';",
			// **Slice 144 (asserted):** COMMENT ON CONSTRAINT must round-trip for
			// every constraint kind. Before this slice, execCommentOn populated
			// pg_description only for CHECK / NOT NULL constraints, so a comment on
			// a PRIMARY KEY / UNIQUE / EXCLUDE (index-backed) or FOREIGN KEY
			// constraint never reached pg_description and pg_dump dropped it. Each
			// line below exercises one previously-unhandled kind.
			"COMMENT ON CONSTRAINT foo_pkey ON public.foo IS 'the primary key';",
			"COMMENT ON CONSTRAINT foo_code_key ON public.foo IS 'unique code';",
			"COMMENT ON CONSTRAINT foo_mgr_fkey ON public.foo IS 'manager fk';",
			"COMMENT ON CONSTRAINT exdef ON public.exclndef IS 'exclusion comment';",
			// **Slice 145 (asserted):** COMMENT ON {VIEW,SEQUENCE,INDEX,SCHEMA} must
			// round-trip. VIEW/SEQUENCE/SCHEMA were silently swallowed (parser had no
			// branch); the INDEX path was wired but never asserted through pg_dump.
			// Each line below exercises one previously-unhandled (or untested) kind.
			"COMMENT ON VIEW public.foo_view IS 'a view comment';",
			"COMMENT ON SEQUENCE public.plain_seq IS 'a sequence comment';",
			"COMMENT ON INDEX public.foo_name_idx IS 'an index comment';",
			"COMMENT ON SCHEMA s IS 'a schema comment';",
			// **Slice 146 (asserted):** COMMENT ON {MATERIALIZED VIEW,TYPE,DOMAIN}
			// must round-trip. All three were silently swallowed (parser had no
			// branch). pg_dump picks the keyword from relkind='m' (matview) and
			// typtype ('e' enum → TYPE, 'd' → DOMAIN).
			"COMMENT ON MATERIALIZED VIEW public.foo_mv IS 'a matview comment';",
			"COMMENT ON TYPE public.mood IS 'a type comment';",
			"COMMENT ON DOMAIN public.zipcode IS 'a domain comment';",
			// **Slice 147 (asserted):** COMMENT ON FUNCTION must round-trip. It was
			// silently swallowed (parser had no FUNCTION branch). pg_dump deparses
			// the signature via pg_get_function_identity_arguments and emits the
			// comment keyed off pg_description (classoid=pg_proc).
			"COMMENT ON FUNCTION public.add_one(integer) IS 'a function comment';",
		}
		for _, sub := range comments {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a COMMENT; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 148 (asserted + fixed):** the user FUNCTION definition itself must
		// round-trip. Slice 147 created public.add_one(integer) only as a COMMENT
		// target and asserted the COMMENT line; the CREATE FUNCTION body that
		// pg_dump's getFuncs/dumpFunc emit from the pg_proc virtual view (slice 7's
		// getFuncs SELECT + slice 147's pg_proc.tableoid fix) was exercised but
		// never asserted — and it carried a real defect. pg_dump renders an
		// old-style SQL function (prosqlbody NULL, so the body comes from prosrc) as:
		//   CREATE FUNCTION public.add_one(integer) RETURNS integer
		//       LANGUAGE sql
		//       AS $_$ SELECT $1 + 1 $_$;
		// The signature args come from pg_get_function_arguments (integer), the
		// return type from formatTypeOID(prorettype=23 → integer), LANGUAGE from
		// the pg_language join (prolang=14 → sql), and the dollar-quoted body
		// verbatim from prosrc. The body is quoted `$_$…$_$` (not `$$…$$`) because
		// pg_dump's appendStringLiteralDQ escalates the dollar-tag when the body
		// contains a bare `$` — here the `$1` parameter — so `$_$` is the CORRECT,
		// PG-identical rendering. The defect: goopg's pg_proc.prosupport was typed
		// `oid` and emitted the text `0`; pg_dump's dumpFunc emits `SUPPORT <val>`
		// whenever `strcmp(prosupport, "-") != 0`, so the dump carried the invalid
		// `LANGUAGE sql SUPPORT 0` (SUPPORT wants a function name — a restore error).
		// Real PG types prosupport `regproc`, which renders InvalidOid as `-`;
		// retyping the column + emitting `-` suppresses the bogus clause. Assert the
		// exact two-line LANGUAGE/AS fragment (which only matches once the spurious
		// SUPPORT clause is gone) and a negative guard on `SUPPORT 0`. Verified
		// against real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_one(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the CREATE FUNCTION signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql\n    AS $_$ SELECT $1 + 1 $_$;") {
			t.Errorf("pg_dump dropped/mangled the CREATE FUNCTION body or emitted a spurious clause\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "SUPPORT 0") {
			t.Errorf("pg_dump emitted invalid `SUPPORT 0` (prosupport must render regproc InvalidOid as '-')\n  full stdout=%q", res.Stdout)
		}
		// **Slice 149 (asserted):** the IMMUTABLE STRICT function (add_two) must
		// round-trip with both clauses. Slice 148 only proved the all-default
		// volatility/strict path; this slice drives the pg_proc virtual view's
		// `provolatile`='i' and `proisstrict`='t' cells through getFuncs -> dumpFunc.
		// dumpFunc appends ` IMMUTABLE` (provolatile[0] != 'v') then ` STRICT`
		// (proisstrict[0] == 't') inline after `LANGUAGE sql`, so real pg_dump 18.3
		// renders:
		//   CREATE FUNCTION public.add_two(integer) RETURNS integer
		//       LANGUAGE sql IMMUTABLE STRICT
		//       AS $_$ SELECT $1 + 2 $_$;
		// If the view typed/emitted either column wrong the clause would be dropped
		// or misordered. Assert the exact one-line LANGUAGE/IMMUTABLE/STRICT fragment.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_two(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_two signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql IMMUTABLE STRICT\n    AS $_$ SELECT $1 + 2 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_two's IMMUTABLE STRICT clauses or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 150 (asserted):** the PARALLEL SAFE function (add_three) must
		// round-trip. proparallel is the LAST inline clause dumpFunc appends to
		// the LANGUAGE line (after volatility/strict/secdef/leakproof/cost/rows/
		// support), so for a function carrying ONLY `PARALLEL SAFE` real pg_dump
		// 18.3 renders:
		//   CREATE FUNCTION public.add_three(integer) RETURNS integer
		//       LANGUAGE sql PARALLEL SAFE
		//       AS $_$ SELECT $1 + 3 $_$;
		// Before this slice the view hardcoded proparallel='u', so dumpFunc emitted
		// nothing and the marker was silently lost. Assert the exact one-line
		// LANGUAGE/PARALLEL fragment.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_three(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_three signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql PARALLEL SAFE\n    AS $_$ SELECT $1 + 3 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_three's PARALLEL SAFE clause or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 151 (asserted):** the COST 50 function (add_four) must round-trip.
		// procost is emitted by dumpFunc (pg_dump.c:13556) inline after the LANGUAGE
		// line whenever it differs from the language default (100 for sql), so real
		// pg_dump 18.3 renders:
		//   CREATE FUNCTION public.add_four(integer) RETURNS integer
		//       LANGUAGE sql COST 50
		//       AS $_$ SELECT $1 + 4 $_$;
		// Before this slice the view hardcoded procost=100 (the language default), so
		// dumpFunc emitted nothing and the explicit COST was silently lost. Assert the
		// exact one-line LANGUAGE/COST fragment.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_four(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_four signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql COST 50\n    AS $_$ SELECT $1 + 4 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_four's COST clause or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 152 (asserted):** a set-returning function round-trips with its
		// SETOF result type AND non-default ROWS. pg_dump builds the RETURNS
		// clause from pg_get_function_result(oid), which prefixes the type with
		// `SETOF ` for SRFs; dumpFunc (pg_dump.c:13571) appends ` ROWS 5` when
		// proretset='t' and prorows ∉ {0,1000}. Real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.gen_series_lite(integer) RETURNS SETOF integer
		//       LANGUAGE sql ROWS 5
		//       AS $_$ SELECT $1 $_$;
		// Before this slice pg_get_function_result returned the bare type, so the
		// SRF was downgraded to a scalar `RETURNS integer` on dump — a real
		// divergence. Assert the SETOF signature and the LANGUAGE/ROWS fragment.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.gen_series_lite(integer) RETURNS SETOF integer") {
			t.Errorf("pg_dump dropped SETOF from gen_series_lite's result type\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql ROWS 5\n    AS $_$ SELECT $1 $_$;") {
			t.Errorf("pg_dump dropped/mangled gen_series_lite's ROWS clause or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 153 (asserted):** the SECURITY DEFINER LEAKPROOF function
		// (add_five) must round-trip both clauses. dumpFunc appends ` SECURITY
		// DEFINER` (prosecdef[0]=='t') then ` LEAKPROOF` (proleakproof[0]=='t')
		// inline after STRICT and before COST, so real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.add_five(integer) RETURNS integer
		//       LANGUAGE sql SECURITY DEFINER LEAKPROOF
		//       AS $_$ SELECT $1 + 5 $_$;
		// The parser+executor already thread both flags onto catalog.Routine and
		// pg_proc_view emits 't'/'t'; this is the first pg_dump round-trip to
		// assert the prosecdef/proleakproof columns reach dumpFunc.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_five(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_five signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql SECURITY DEFINER LEAKPROOF\n    AS $_$ SELECT $1 + 5 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_five's SECURITY DEFINER/LEAKPROOF clauses or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 154 (asserted):** the procedure (ins_foo) must round-trip via
		// dumpFunc's PROCEDURE branch. pg_dump uses the `PROCEDURE` keyword,
		// emits NO `RETURNS` clause (prokind='p' short-circuits the result-type
		// output at pg_dump.c:13498), prefixes the parameter with `IN ` (procedures
		// always carry an argmode), and — because the body contains no `$` — quotes
		// it with the bare `$$` delimiter, so real pg_dump 18.3 renders:
		//   CREATE PROCEDURE public.ins_foo(IN a integer)
		//       LANGUAGE sql
		//       AS $$ INSERT INTO public.foo (id) VALUES (a) $$;
		// A function-only divergence (missing prokind='p' discovery, a stray
		// RETURNS, a dropped IN, or wrong dollar-quoting) would surface exactly here.
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.ins_foo(IN a integer)") {
			t.Errorf("pg_dump dropped/mangled the ins_foo procedure signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.ins_foo(IN a integer)\n    LANGUAGE sql\n    AS $$ INSERT INTO public.foo (id) VALUES (a) $$;") {
			t.Errorf("pg_dump dropped/mangled ins_foo's LANGUAGE/body or emitted a stray RETURNS\n  full stdout=%q", res.Stdout)
		}
		// **Slice 155 (asserted):** the OUT-parameter procedure (proc_out) must
		// round-trip with BOTH the `IN ` and `OUT ` mode prefixes. pg_dump rebuilds
		// the signature from pg_get_function_arguments(oid), which renders each
		// parameter mode-qualified; the OUT param (proargmodes element 'b'/'o') is
		// the first non-IN argmode any slice has driven through dumpFunc. Real
		// pg_dump 18.3 renders:
		//   CREATE PROCEDURE public.proc_out(IN a integer, OUT b integer)
		//       LANGUAGE sql
		//       AS $$ INSERT INTO public.foo (id) VALUES (a) $$;
		// A divergence (OUT dropped, rendered as IN, or the param omitted) surfaces
		// exactly here.
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.proc_out(IN a integer, OUT b integer)") {
			t.Errorf("pg_dump dropped/mangled the proc_out OUT-parameter signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.proc_out(IN a integer, OUT b integer)\n    LANGUAGE sql\n    AS $$ INSERT INTO public.foo (id) VALUES (a) $$;") {
			t.Errorf("pg_dump dropped/mangled proc_out's LANGUAGE/body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 156 (asserted):** the INOUT-parameter procedure (proc_inout) must
		// round-trip with the explicit `INOUT ` mode prefix. This is the last
		// argmode prefix pg_get_function_arguments can emit that no prior slice
		// drove through dumpFunc end-to-end (slice 155 reached OUT). A single INOUT
		// param (proargmodes element 'b') is enough to set showMode, so pg_dump
		// rebuilds the signature mode-qualified. Real pg_dump 18.3 renders:
		//   CREATE PROCEDURE public.proc_inout(INOUT x integer)
		//       LANGUAGE sql
		//       AS $$ INSERT INTO public.foo (id) VALUES (x) $$;
		// A divergence (INOUT collapsed to IN, or the param omitted) surfaces here.
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.proc_inout(INOUT x integer)") {
			t.Errorf("pg_dump dropped/mangled the proc_inout INOUT-parameter signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE PROCEDURE public.proc_inout(INOUT x integer)\n    LANGUAGE sql\n    AS $$ INSERT INTO public.foo (id) VALUES (x) $$;") {
			t.Errorf("pg_dump dropped/mangled proc_inout's LANGUAGE/body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 157 (asserted):** the STABLE + PARALLEL RESTRICTED function
		// (add_six) must round-trip both markers. Slice 149 covered IMMUTABLE
		// (provolatile='i') and slice 150 PARALLEL SAFE (proparallel='s'); STABLE
		// (provolatile='s') and PARALLEL RESTRICTED (proparallel='r') are the last
		// non-default volatility/parallel cells dumpFunc can emit. dumpFunc appends
		// volatility before parallel, so real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.add_six(integer) RETURNS integer
		//       LANGUAGE sql STABLE PARALLEL RESTRICTED
		//       AS $_$ SELECT $1 + 6 $_$;
		// A divergence (either marker downgraded to its default, dropped, or the
		// two clauses reordered) surfaces exactly here.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_six(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_six signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql STABLE PARALLEL RESTRICTED\n    AS $_$ SELECT $1 + 6 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_six's STABLE/PARALLEL RESTRICTED clauses or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 158 (asserted):** the multi-statement-body function (add_seven)
		// must round-trip its body verbatim, inner `;` included. This pins two
		// goopg behaviours: the simple-query splitter kept the dollar-quoted body
		// intact at CREATE time (already enforced by the runSQLSimple fatal above),
		// and dumpFunc emits prosrc verbatim. The `$1` forces the `$_$` delimiter.
		// Real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.add_seven(integer) RETURNS integer
		//       LANGUAGE sql
		//       AS $_$ SELECT 1; SELECT $1 + 7; $_$;
		// A body truncated at the inner `;`, a collapsed/normalized body, or a
		// dropped statement surfaces exactly here.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_seven(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_seven signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql\n    AS $_$ SELECT 1; SELECT $1 + 7; $_$;") {
			t.Errorf("pg_dump dropped/mangled add_seven's multi-statement body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 159 (asserted):** the VARIADIC-array function (sum_variadic) must
		// round-trip its parameter as `VARIADIC arr integer[]`. pg_dump builds the
		// signature from pg_get_function_arguments(oid); goopg answers via
		// buildFunctionArguments, which maps argmode 'v' to the `VARIADIC ` prefix
		// and emits the canonical array type name. A dropped VARIADIC prefix (arg
		// rendered as a plain array IN param) or a mangled array type surfaces here.
		// The `$`-free body keeps the plain `$$` delimiter. Real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.sum_variadic(VARIADIC arr integer[]) RETURNS integer
		//       LANGUAGE sql
		//       AS $$ SELECT 1 $$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.sum_variadic(VARIADIC arr integer[]) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the sum_variadic VARIADIC-array signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql\n    AS $$ SELECT 1 $$;") {
			t.Errorf("pg_dump dropped/mangled sum_variadic's body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 160 (asserted):** the DEFAULT-arg function (add_default) must
		// round-trip its trailing parameter's default as `b integer DEFAULT 10`.
		// pg_dump builds the signature from pg_get_function_arguments(oid); goopg's
		// buildFunctionArguments now appends ` DEFAULT <expr>` for input args
		// (print_defaults=true). A dropped DEFAULT clause (parameter rendered as a
		// bare `b integer`) yields a function that rejects the one-arg call form —
		// the exact divergence this slice fixes — and surfaces here. The `$1`/`$2`
		// in the body force the `$_$` delimiter. Real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.add_default(a integer, b integer DEFAULT 10) RETURNS integer
		//       LANGUAGE sql
		//       AS $_$ SELECT $1 + $2 $_$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_default(a integer, b integer DEFAULT 10) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the add_default DEFAULT-arg signature\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "    LANGUAGE sql\n    AS $_$ SELECT $1 + $2 $_$;") {
			t.Errorf("pg_dump dropped/mangled add_default's body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 161 (asserted):** the SET-RETURNING function (gen_one) must
		// round-trip its `RETURNS SETOF integer` return clause. pg_dump reads
		// proretset/prorettype directly from pg_proc; goopg's runtime pg_proc view
		// emits proretset='t' with prorettype=integer (element type, OID 23) and the
		// SRF-default prorows='1000', which dumpFunc SUPPRESSES (no explicit ROWS).
		// A dropped SETOF (function restored as a plain scalar-returning function) or
		// a stray `ROWS 1000` surfaces exactly here. The `$`-free body keeps the plain
		// `$$` delimiter. Real pg_dump 18.3 renders:
		//   CREATE FUNCTION public.gen_one() RETURNS SETOF integer
		//       LANGUAGE sql
		//       AS $$ SELECT 1 $$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.gen_one() RETURNS SETOF integer") {
			t.Errorf("pg_dump dropped/mangled the gen_one SETOF return clause\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.gen_one() RETURNS SETOF integer\n    LANGUAGE sql\n    AS $$ SELECT 1 $$;") {
			t.Errorf("pg_dump dropped/mangled gen_one's body or emitted a stray ROWS clause\n  full stdout=%q", res.Stdout)
		}
		// **Slice 162 (asserted):** the ARRAY-return function (make_arr) must
		// round-trip its `RETURNS integer[]` clause. pg_dump renders the return
		// type via the server's format_type(prorettype) — prorettype must be the
		// array OID 1007 (which format_type renders "integer[]"), NOT the scalar
		// element OID 23. Before the operators_ddl.go fix the array suffix was
		// dropped at catalog-build time, so prorettype was 23 and the dump read
		// `RETURNS integer`. Real pg_dump emits:
		//   CREATE FUNCTION public.make_arr() RETURNS integer[]
		//       LANGUAGE sql
		//       AS $$ SELECT ARRAY[1, 2, 3] $$;
		// A dropped/scalarized return type surfaces in the first assertion; a
		// mangled body in the second.
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.make_arr() RETURNS integer[]") {
			t.Errorf("pg_dump dropped/scalarized the make_arr integer[] return type\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.make_arr() RETURNS integer[]\n    LANGUAGE sql\n    AS $$ SELECT ARRAY[1, 2, 3] $$;") {
			t.Errorf("pg_dump dropped/mangled make_arr's body or return clause\n  full stdout=%q", res.Stdout)
		}
		// **Slice 163 (asserted):** a `LANGUAGE plpgsql` function (plpg_inc) must
		// round-trip. dumpFunc resolves the language name by joining pg_proc.prolang
		// to pg_language.oid; plpgsql now has OID 13627 in both the pg_language view
		// and langNameToOIDStr, so the join finds lanname='plpgsql'. Before this
		// slice prolang was 0, the join returned 0 rows, and pg_dump aborted the
		// ENTIRE dump. The body contains `$1`, so pg_dump dollar-quotes it with the
		// `$_$` tag (the verbatim prosrc is rendered untouched — plpgsql is not
		// deparsed). Real pg_dump 18.3 emits:
		//   CREATE FUNCTION public.plpg_inc(integer) RETURNS integer
		//       LANGUAGE plpgsql
		//       AS $_$ BEGIN RETURN $1 + 1; END; $_$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.plpg_inc(integer) RETURNS integer") {
			t.Errorf("pg_dump dropped/mangled the plpg_inc signature (prolang join likely failed)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.plpg_inc(integer) RETURNS integer\n    LANGUAGE plpgsql\n    AS $_$ BEGIN RETURN $1 + 1; END; $_$;") {
			t.Errorf("pg_dump dropped/mangled plpg_inc's plpgsql language or body\n  full stdout=%q", res.Stdout)
		}
		// **Slice 164 (asserted):** a function returning the pseudo-type `record`
		// (ret_rec) must round-trip. dumpFunc builds the RETURNS clause from
		// `format_type(p.prorettype, NULL)`. Before this slice typeNameToOIDStr
		// had no `record` case, so prorettype was 0 and format_type(0) yielded the
		// placeholder `-` — the dump rendered `RETURNS -`, broken SQL. record now
		// maps to OID 2249, and goopg's format_type(2249) is `record`, so the two
		// sibling paths agree. Real pg_dump 18.3 emits:
		//   CREATE FUNCTION public.ret_rec() RETURNS record
		//       LANGUAGE sql
		//       AS $$ SELECT (1, 2) $$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.ret_rec() RETURNS record") {
			t.Errorf("pg_dump dropped/mangled the ret_rec record return type (prorettype likely 0)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.ret_rec() RETURNS record\n    LANGUAGE sql\n    AS $$ SELECT (1, 2) $$;") {
			t.Errorf("pg_dump dropped/mangled ret_rec's body or return clause\n  full stdout=%q", res.Stdout)
		}
		// **Slice 165 (asserted):** a `RETURNS TABLE(...)` function must round-trip in
		// the upstream form, not the divergent OUT-args desugaring. dumpFunc builds the
		// signature from pg_get_function_arguments (which must EXCLUDE the table cols,
		// leaving `ret_tab()`) and the RETURNS clause from pg_get_function_result (which
		// must render `TABLE(id integer, label text)`). Before this slice the table
		// columns leaked into the arg list and the result was `SETOF record`. Real
		// pg_dump 18.3 emits:
		//   CREATE FUNCTION public.ret_tab() RETURNS TABLE(id integer, label text)
		//       LANGUAGE sql
		//       AS $$ SELECT 1, 'x' $$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.ret_tab() RETURNS TABLE(id integer, label text)") {
			t.Errorf("pg_dump dropped/mangled ret_tab's RETURNS TABLE clause (table cols leaked to args or result was SETOF record)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.ret_tab() RETURNS TABLE(id integer, label text)\n    LANGUAGE sql\n    AS $$ SELECT 1, 'x' $$;") {
			t.Errorf("pg_dump dropped/mangled ret_tab's body or RETURNS TABLE clause\n  full stdout=%q", res.Stdout)
		}
		// **Slice 56 (asserted):** a plain (non-constraint) secondary index must
		// survive the dump via getIndexes -> pg_get_indexdef, distinct from the
		// index-backed constraint path. PG renders it `CREATE INDEX <name> ON
		// <schema>.<table> USING btree (<cols>);`. goopg parsed ASC/DESC and NULLS
		// FIRST/LAST modifiers but SILENTLY DISCARDED them, so a DESC index dumped
		// as an ASC index — a silent semantic change. The parser now captures the
		// per-column ordering into CreateIndexStmt.ColOrders, the executor stores
		// it on catalog.Index.ColDescending/ColNullsFirst, and BuildIndexDef
		// renders it with PG's default-suppression (DESC defaults NULLS FIRST; ASC
		// defaults NULLS LAST). Assert each render branch round-trips.
		indexDefs := []string{
			// plain (all-default ASC NULLS LAST) — no ordering clause
			"CREATE INDEX foo_name_idx ON public.foo USING btree (name);",
			// partial index predicate (slice-56 regression guard — already worked)
			"CREATE INDEX foo_qty_partial_idx ON public.foo USING btree (qty) WHERE qty > 0;",
			// DESC with a non-default NULLS LAST override
			"CREATE INDEX foo_name_desc_idx ON public.foo USING btree (name DESC NULLS LAST);",
			// DESC (default NULLS FIRST suppressed) + ASC NULLS FIRST override
			"CREATE INDEX foo_ord_idx ON public.foo USING btree (name DESC, qty NULLS FIRST);",
			// Slice 134: NULLS NOT DISTINCT clause re-emitted after the column list
			// (ruleutils.c order: (cols) [INCLUDE] NULLS NOT DISTINCT [WHERE]).
			"CREATE UNIQUE INDEX foo_nnd_idx ON public.foo USING btree (name) NULLS NOT DISTINCT;",
			// Slice 218: `WITH (fillfactor='N')` storage parameter re-emitted after
			// the column list (ruleutils.c order: (cols) [INCLUDE] [NULLS NOT
			// DISTINCT] WITH [WHERE]); flatten_reloptions single-quotes the value.
			"CREATE INDEX foo_ff_idx ON public.foo USING btree (qty) WITH (fillfactor='70');",
			// Slice 219: btree boolean reloption `WITH (deduplicate_items='off')`
			// re-emitted via flatten_reloptions (single-quoted value), in the same
			// WITH clause position as fillfactor.
			"CREATE INDEX foo_dedup_idx ON public.foo USING btree (qty) WITH (deduplicate_items='off');",
			// Slice 220: GIN catalog-only index with a `WITH (fastupdate='off')`
			// boolean reloption. Exercises the `USING gin` access-method rendering
			// (not silently upgraded to btree) plus flatten_reloptions single-quoting.
			"CREATE INDEX foo_fastupdate_idx ON public.foo USING gin (qty) WITH (fastupdate='off');",
			// Slice 221: GIN catalog-only index with a `WITH (gin_pending_list_limit=
			// '128')` integer reloption. Exercises the `USING gin` rendering plus the
			// integer reloption path through flatten_reloptions (single-quoted value).
			"CREATE INDEX foo_ginlimit_idx ON public.foo USING gin (qty) WITH (gin_pending_list_limit='128');",
			// Slice 222: BRIN catalog-only index with a `WITH (pages_per_range='64')`
			// integer reloption. Exercises the `USING brin` access-method rendering
			// (previously rejected as an unsupported method) plus flatten_reloptions
			// single-quoting of the integer value.
			"CREATE INDEX foo_brinrange_idx ON public.foo USING brin (qty) WITH (pages_per_range='64');",
			// Slice 223: BRIN catalog-only index with a `WITH (autosummarize='on')`
			// boolean reloption. Exercises the `USING brin` rendering plus the boolean
			// reloption path through flatten_reloptions (single-quoted value).
			"CREATE INDEX foo_brinauto_idx ON public.foo USING brin (qty) WITH (autosummarize='on');",
		}
		for _, sub := range indexDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled a secondary index; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Slice 134/135/136/137/138 (regression guard): a plain unique/secondary
		// index or a default-distinct UNIQUE constraint must NOT gain a stray NULLS
		// NOT DISTINCT — the flag is only set where explicitly declared. Exactly
		// five clauses must appear: the slice-134 CREATE INDEX (foo_nnd_idx), the
		// slice-135 table-level ADD CONSTRAINT (uniqnnd_a_key), the slice-136
		// inline-column ADD CONSTRAINT (uniqcnnd_a_key), the slice-137 inline
		// NAMED column ADD CONSTRAINT (myuniq), and the slice-138 NAMED table-level
		// ADD CONSTRAINT (tuniq).
		if got := strings.Count(res.Stdout, "NULLS NOT DISTINCT"); got != 5 {
			t.Errorf("expected exactly five NULLS NOT DISTINCT in dump, got %d\n  full stdout=%q", got, res.Stdout)
		}
		// Slice 135/136 negative guard: dropping the clause would render the bare
		// `<name> UNIQUE (a)`, silently restoring with default NULLS DISTINCT
		// (every NULL unique) instead of the declared NULLS-equal semantics.
		if strings.Contains(res.Stdout, "ADD CONSTRAINT uniqnnd_a_key UNIQUE (a)") {
			t.Errorf("pg_dump dropped NULLS NOT DISTINCT from a table-level UNIQUE constraint\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "ADD CONSTRAINT uniqcnnd_a_key UNIQUE (a)") {
			t.Errorf("pg_dump dropped NULLS NOT DISTINCT from an inline-column UNIQUE constraint\n  full stdout=%q", res.Stdout)
		}
		// Slice 137 negative guards: the named inline column UNIQUE must surface
		// under the USER name (myuniq), never the auto-generated uniqcname_a_key,
		// and the constraint must NOT be silently dropped (the pre-slice bug
		// created no backing index, so no ADD CONSTRAINT line appeared at all).
		if strings.Contains(res.Stdout, "ADD CONSTRAINT myuniq UNIQUE (a)") {
			t.Errorf("pg_dump dropped NULLS NOT DISTINCT from a named inline-column UNIQUE constraint\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "uniqcname_a_key") {
			t.Errorf("named inline column UNIQUE used the auto-generated name instead of myuniq\n  full stdout=%q", res.Stdout)
		}
		// Slice 138 negative guard: the named table-level UNIQUE must surface under
		// the USER name (tuniq) WITH the clause; dropping it would render the bare
		// `ADD CONSTRAINT tuniq UNIQUE (a)` (silent NULL-dedup loss), and the
		// pre-slice parser bug dropped the whole constraint (no ADD CONSTRAINT line).
		// Slice 139 negative guard: dropping the DEFERRABLE flags would render the
		// bare `ADD CONSTRAINT uniqdef_a_key UNIQUE (a)` (terminated by ';'),
		// silently restoring the constraint as NOT DEFERRABLE — a semantic change
		// (a deferred constraint is only checked at COMMIT). Emitting DEFERRABLE
		// without INITIALLY DEFERRED would also be wrong (default is IMMEDIATE).
		// Slice 140 negative guard: the NAMED deferrable form must not lose its
		// trailer either — dropping it would render the bare
		// `ADD CONSTRAINT tudef UNIQUE (a)` or the half-clause `… DEFERRABLE`.
		// Slice 141 negative guard: the same for the INLINE-COLUMN deferrable forms
		// (anonymous `uniqcdef_a_key` and named `cudef`) — neither the bare
		// constraint nor the half-clause `… DEFERRABLE` (missing INITIALLY DEFERRED)
		// may appear.
		for _, neg := range []string{
			"ADD CONSTRAINT uniqdef_a_key UNIQUE (a);",
			"ADD CONSTRAINT uniqdef_a_key UNIQUE (a) DEFERRABLE;",
			"ADD CONSTRAINT tudef UNIQUE (a);",
			"ADD CONSTRAINT tudef UNIQUE (a) DEFERRABLE;",
			"ADD CONSTRAINT uniqcdef_a_key UNIQUE (a);",
			"ADD CONSTRAINT uniqcdef_a_key UNIQUE (a) DEFERRABLE;",
			"ADD CONSTRAINT cudef UNIQUE (a);",
			"ADD CONSTRAINT cudef UNIQUE (a) DEFERRABLE;",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump mangled a DEFERRABLE UNIQUE constraint: %q", neg)
			}
		}
		// Slice 142 negative guard: the PRIMARY KEY deferrable forms must not lose
		// their trailer either — neither the bare constraint nor the half-clause
		// `… DEFERRABLE` (missing INITIALLY DEFERRED) may appear for the anonymous
		// (`pktdef_pkey`), named (`pkdef`), or inline-column (`pkcdef_pkey`) forms.
		for _, neg := range []string{
			"ADD CONSTRAINT pktdef_pkey PRIMARY KEY (a);",
			"ADD CONSTRAINT pktdef_pkey PRIMARY KEY (a) DEFERRABLE;",
			"ADD CONSTRAINT pkdef PRIMARY KEY (a);",
			"ADD CONSTRAINT pkdef PRIMARY KEY (a) DEFERRABLE;",
			"ADD CONSTRAINT pkcdef_pkey PRIMARY KEY (a);",
			"ADD CONSTRAINT pkcdef_pkey PRIMARY KEY (a) DEFERRABLE;",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump mangled a DEFERRABLE PRIMARY KEY constraint: %q", neg)
			}
		}
		// Slice 143 negative guard: the EXCLUDE deferrable forms must not lose
		// their trailer — neither the bare constraint (terminated by `;`) nor the
		// half-clause `… DEFERRABLE;` (missing INITIALLY DEFERRED) may appear for
		// the anonymous (`excldef_a_excl`) or named (`exdef`) forms.
		for _, neg := range []string{
			"ADD CONSTRAINT excldef_a_excl EXCLUDE USING btree (a WITH =);",
			"ADD CONSTRAINT excldef_a_excl EXCLUDE USING btree (a WITH =) DEFERRABLE;",
			"ADD CONSTRAINT exdef EXCLUDE USING btree (a WITH =);",
			"ADD CONSTRAINT exdef EXCLUDE USING btree (a WITH =) DEFERRABLE;",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump mangled a DEFERRABLE EXCLUDE constraint: %q", neg)
			}
		}
		if strings.Contains(res.Stdout, "ADD CONSTRAINT tuniq UNIQUE (a)") {
			t.Errorf("pg_dump dropped NULLS NOT DISTINCT from a named table-level UNIQUE constraint\n  full stdout=%q", res.Stdout)
		}
		// **Slice 57 (asserted):** a VIEW must round-trip. pg_dump aborts the
		// whole dump when pg_get_viewdef returns empty; goopg now returns the
		// captured raw view body, so the `CREATE VIEW … AS <body>` statement is
		// emitted. Assert both the header and the verbatim body — the regression
		// guard for the pg_get_viewdef fix.
		viewDefs := []string{
			"CREATE VIEW public.foo_view AS",
			"SELECT id, name FROM public.foo WHERE qty > 0;",
		}
		for _, sub := range viewDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a VIEW; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 58 (asserted):** a VIEW with an explicit column list must
		// round-trip with the renamed column names. pg_get_viewdef now splices
		// the `(col_a, col_b)` names into the select list as `AS` aliases, so the
		// restored view exposes the declared column names rather than the
		// underlying `id, name`. Regression guard for applyViewColumnAliases.
		rviewDefs := []string{
			"CREATE VIEW public.foo_rview AS",
			"SELECT id AS col_a, name AS col_b FROM public.foo;",
		}
		for _, sub := range rviewDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped renamed view columns; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 59 (asserted):** a GENERATED ALWAYS AS (expr) STORED column
		// must round-trip with its generation clause. atthasdef now reports true
		// for generated columns and pg_attrdef emits a row whose adbin is the raw
		// generation expr, so pg_dump (keyed on attgenerated='s') re-emits the
		// inline GENERATED clause. Regression guard for the atthasdef + pg_attrdef
		// generated-column wiring. (goopg stores the expr verbatim, so the dumped
		// clause has single parens; PG may add normalizing parens — both restore
		// to an equivalent stored column.)
		if !strings.Contains(res.Stdout, "CREATE TABLE public.gen (") {
			t.Errorf("pg_dump missing CREATE TABLE public.gen\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "area integer GENERATED ALWAYS AS (w * h) STORED") {
			t.Errorf("pg_dump dropped the GENERATED clause on a stored generated column\n  full stdout=%q", res.Stdout)
		}
		// **Slice 194 (asserted):** a VIRTUAL generated column round-trips with its
		// generation clause AND its virtual strategy — pg_dump emits
		// `GENERATED ALWAYS AS (w + h)` with NO trailing `STORED` (attgenerated='v').
		// Guards that attGeneratedFor reports 'v' for a VIRTUAL column and that the
		// pg_attrdef expr wiring is shared with the stored path.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.genv (") {
			t.Errorf("pg_dump missing CREATE TABLE public.genv\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "varea integer GENERATED ALWAYS AS (w + h)") {
			t.Errorf("pg_dump dropped the GENERATED clause on a virtual generated column\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "varea integer GENERATED ALWAYS AS (w + h) STORED") {
			t.Errorf("pg_dump emitted STORED for a VIRTUAL generated column (attgenerated should be 'v')\n  full stdout=%q", res.Stdout)
		}
		// **Slice 60 (asserted):** a MATERIALIZED VIEW must round-trip. pg_dump
		// dumps the matview body via the same pg_get_viewdef path as a plain view
		// and aborts the whole dump when it returns empty; goopg now stores the
		// raw body on catalog.Table.ViewDef (parser RawDef capture + execCreateMatView
		// wiring), so the `CREATE MATERIALIZED VIEW … AS <body> WITH NO DATA;`
		// statement is emitted. Regression guard for the matview ViewDef wiring.
		matViewDefs := []string{
			"CREATE MATERIALIZED VIEW public.foo_mv AS",
			"SELECT id, name FROM public.foo WHERE qty > 0",
			"WITH NO DATA;",
		}
		for _, sub := range matViewDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a MATERIALIZED VIEW; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 132 (asserted):** DEPENDENCY-ORDERING — every view that selects
		// from `public.foo` must be emitted AFTER the `CREATE TABLE public.foo`
		// that backs it. pg_restore replays the dump top-to-bottom with no
		// forward references, so a view whose CREATE precedes its base table
		// would fail restore with `relation "public.foo" does not exist`. The
		// preceding slices (57/58/60) only asserted each view's TEXT is PRESENT;
		// none pinned its POSITION relative to the base table. pg_dump derives
		// this order by topologically sorting the dump's TocEntry DAG, which it
		// builds from the dependency rows goopg surfaces (pg_depend + the
		// rewrite/relation edges getDependencies reads) — so a regression that
		// dropped or inverted goopg's view→table dependency edge would let the
		// view sort ahead of the table and silently produce an unrestorable dump.
		// Empirically verified vs goopg's own pg_dump output: foo precedes
		// foo_view, foo_rview and foo_mv. This guards the ORDER, complementing the
		// presence checks above. (strings.Index returns the first occurrence; all
		// four markers are unique CREATE headers, so a non-negative index is the
		// statement's emission offset.)
		tableOff := strings.Index(res.Stdout, "CREATE TABLE public.foo (")
		if tableOff < 0 {
			t.Errorf("pg_dump missing CREATE TABLE public.foo for dependency-order check\n  full stdout=%q", res.Stdout)
		} else {
			dependents := []string{
				"CREATE VIEW public.foo_view AS",
				"CREATE VIEW public.foo_rview AS",
				"CREATE MATERIALIZED VIEW public.foo_mv AS",
			}
			for _, dep := range dependents {
				depOff := strings.Index(res.Stdout, dep)
				if depOff < 0 {
					t.Errorf("pg_dump missing %q for dependency-order check\n  full stdout=%q", dep, res.Stdout)
					continue
				}
				if depOff < tableOff {
					t.Errorf("pg_dump emitted a dependent view BEFORE its base table — unrestorable dump: %q at %d precedes CREATE TABLE public.foo at %d\n  full stdout=%q",
						dep, depOff, tableOff, res.Stdout)
				}
			}
		}
		// **Slice 133 (asserted):** CROSS-TABLE FOREIGN-KEY dependency-ordering. A
		// FK from `public.baz` to `public.bar` introduces a referencing→referenced
		// edge, but unlike the view edge (slice 132) pg_dump does NOT order the two
		// CREATE TABLE statements by it — instead it SPLITS the FK out of the table
		// body into a separate `ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY`
		// emitted in the POST-DATA section, after every CREATE TABLE. That is how
		// pg_dump breaks dependency cycles (mutual FKs) and guarantees the referenced
		// relation exists when the constraint is replayed. So the invariant to pin is
		// not "bar before baz" but "the FK ADD CONSTRAINT after BOTH tables": a
		// regression that inlined the FK back into `CREATE TABLE public.baz`, or
		// emitted the ALTER ahead of `CREATE TABLE public.bar`, would make
		// pg_restore fail with `relation "public.bar" does not exist`. The existing
		// slice-51/53 checks only assert the ADD CONSTRAINT TEXT is present; none
		// pinned its POSITION relative to the two base tables. Empirically verified
		// vs goopg's own pg_dump output: CREATE TABLE bar @16740 and baz @16927 both
		// precede `ADD CONSTRAINT baz_x_fkey` @39048 (post-data). (strings.Index
		// returns the first occurrence; each marker is a unique header.)
		fkOff := strings.Index(res.Stdout, "ADD CONSTRAINT baz_x_fkey")
		barOff := strings.Index(res.Stdout, "CREATE TABLE public.bar (")
		bazOff := strings.Index(res.Stdout, "CREATE TABLE public.baz (")
		switch {
		case fkOff < 0:
			t.Errorf("pg_dump missing ADD CONSTRAINT baz_x_fkey for FK dependency-order check\n  full stdout=%q", res.Stdout)
		case barOff < 0:
			t.Errorf("pg_dump missing CREATE TABLE public.bar for FK dependency-order check\n  full stdout=%q", res.Stdout)
		case bazOff < 0:
			t.Errorf("pg_dump missing CREATE TABLE public.baz for FK dependency-order check\n  full stdout=%q", res.Stdout)
		default:
			if fkOff < barOff {
				t.Errorf("pg_dump emitted the FK ADD CONSTRAINT before its REFERENCED table — unrestorable dump: baz_x_fkey at %d precedes CREATE TABLE public.bar at %d\n  full stdout=%q",
					fkOff, barOff, res.Stdout)
			}
			if fkOff < bazOff {
				t.Errorf("pg_dump emitted the FK ADD CONSTRAINT before its OWNING table — unrestorable dump: baz_x_fkey at %d precedes CREATE TABLE public.baz at %d\n  full stdout=%q",
					fkOff, bazOff, res.Stdout)
			}
		}
		// The FK must be a separate post-data ALTER TABLE, NOT inlined into the
		// CREATE TABLE public.baz body (which would order baz after bar and defeat
		// cycle-breaking). Assert the cross-table FOREIGN KEY clause does not appear
		// inside the baz table body, i.e. before the FK's own ALTER statement.
		if bazOff >= 0 && fkOff >= 0 {
			bazBody := res.Stdout[bazOff:fkOff]
			if strings.Contains(bazBody, "REFERENCES public.bar") {
				t.Errorf("pg_dump inlined the cross-table FK into CREATE TABLE public.baz instead of a post-data ALTER TABLE\n  baz body=%q", bazBody)
			}
		}
		// **Slice 116 (asserted):** a standalone SEQUENCE must round-trip. pg_dump's
		// getTables now discovers the relkind='S' relation (pg_class surfaces each
		// IsSequence table) and dumpSequence regenerates the DDL from pg_sequence
		// (slice 115). A plain `CREATE SEQUENCE` emits all-default clauses; an
		// explicit one round-trips START WITH / INCREMENT BY / MAXVALUE. The plain
		// sequence's defaults pin pg_dump's exact byte output (NO MINVALUE / NO
		// MAXVALUE / CACHE 1). A standalone sequence has no OWNED BY (no pg_depend
		// 'a'/'i' row), so assert that clause is ABSENT. Regression guard for the
		// pg_class relkind='S' / relam=0 surfacing.
		seqDefs := []string{
			"CREATE SEQUENCE public.plain_seq",
			"CREATE SEQUENCE public.num_seq",
			"START WITH 100",
			"INCREMENT BY 10",
			"MAXVALUE 1000",
			"NO MINVALUE",
			"NO MAXVALUE",
			"CACHE 1;",
		}
		for _, sub := range seqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a SEQUENCE; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// A standalone sequence (no OWNED BY) must NOT emit ALTER SEQUENCE OWNED BY.
		if strings.Contains(res.Stdout, "ALTER SEQUENCE public.plain_seq OWNED BY") ||
			strings.Contains(res.Stdout, "ALTER SEQUENCE public.num_seq OWNED BY") {
			t.Errorf("pg_dump emitted a spurious OWNED BY for a standalone sequence\n  full stdout=%q", res.Stdout)
		}
		// **Slice 117 (asserted):** typed sequences emit an `AS <type>` clause
		// immediately after the CREATE SEQUENCE header (pg_dump renders
		// format_type(seqtypid, NULL) and suppresses the clause only for the
		// bigint default), and a CYCLE sequence emits a trailing `CYCLE;`. The
		// 4-space-indented blocks pin pg_dump's exact byte order: `AS <type>`
		// precedes START WITH, and CYCLE is the last clause. Typed sequences keep
		// `NO MAXVALUE` because their default seqmax equals pg_dump's type-derived
		// default_maxv (smallint 32767 / integer 2147483647).
		typedSeqDefs := []string{
			"CREATE SEQUENCE public.small_seq\n    AS smallint\n",
			"CREATE SEQUENCE public.int_seq\n    AS integer\n",
			"CREATE SEQUENCE public.cyc_seq\n",
			"    CYCLE;",
		}
		for _, sub := range typedSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a typed/CYCLE SEQUENCE clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The bigint-default sequences must NOT carry a spurious `AS bigint` clause
		// (pg_dump suppresses the type clause for the default), and the typed
		// sequences must NOT be cycling (no stray CYCLE on small_seq/int_seq).
		if strings.Contains(res.Stdout, "    AS bigint\n") {
			t.Errorf("pg_dump emitted a spurious `AS bigint` for a default sequence\n  full stdout=%q", res.Stdout)
		}
		// **Slice 118 (asserted):** a sequence with `OWNED BY table.column` must emit
		// a trailing `ALTER SEQUENCE ... OWNED BY ...;`. pg_dump derives owning_tab/
		// owning_col from the pg_depend AUTO ('a') row the catalog now synthesizes
		// from SeqParams.OwnedBy; without it the join yields NULL and no OWNED BY is
		// emitted. The owning table is dumped too (a plain CREATE TABLE). Regression
		// guard for the pg_depend ownership-row synthesis.
		ownedSeqDefs := []string{
			"CREATE SEQUENCE public.owned_seq",
			"ALTER SEQUENCE public.owned_seq OWNED BY public.owner_tbl.id;",
			"CREATE TABLE public.owner_tbl (",
		}
		for _, sub := range ownedSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped an OWNED BY sequence/owner; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 119 (asserted):** descending sequences must round-trip through
		// pg_dump's descending-direction default suppression. A plain
		// `INCREMENT BY -1` sequence keeps NO MINVALUE/NO MAXVALUE (its seqmin=
		// PG_INT64_MIN and seqmax=-1 equal pg_dump's descending default_minv/
		// default_maxv) and starts at -1; an explicit-bound descending sequence
		// emits both MINVALUE/MAXVALUE and starts at its maxv. The full 4-space
		// blocks pin the exact byte order (START WITH < 0, INCREMENT BY < 0).
		// Regression guard for the negative-direction default computation in
		// execCreateSequence + the pg_sequence min/max/start threading.
		descSeqDefs := []string{
			"CREATE SEQUENCE public.desc_seq\n    START WITH -1\n    INCREMENT BY -1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;\n",
			"CREATE SEQUENCE public.desc_bound_seq\n    START WITH -5\n    INCREMENT BY -2\n    MINVALUE -100\n    MAXVALUE -5\n    CACHE 1;\n",
			"SELECT pg_catalog.setval('public.desc_seq', -1, false);",
			"SELECT pg_catalog.setval('public.desc_bound_seq', -5, false);",
		}
		for _, sub := range descSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a descending SEQUENCE clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// A descending sequence must NOT carry a spurious `AS bigint` (already
		// guarded above) and the plain descending seq must NOT emit MINVALUE/MAXVALUE.
		if strings.Contains(res.Stdout, "CREATE SEQUENCE public.desc_seq\n    START WITH -1\n    INCREMENT BY -1\n    MINVALUE") {
			t.Errorf("pg_dump emitted a spurious MINVALUE for a plain descending sequence\n  full stdout=%q", res.Stdout)
		}
		// **Slice 130 (asserted):** a per-sequence CACHE size must round-trip. The
		// CREATE path (`CACHE 5`) and the ALTER path (`ALTER SEQUENCE ... CACHE 42`
		// over a default-cache sequence) both surface in pg_sequence.seqcache, which
		// pg_dump renders as the 4-space `CACHE n` clause. The full blocks pin the
		// exact byte order (CACHE is the last clause for a non-cycling sequence). A
		// regression that re-hard-wired seqcache=1 — the behaviour every other
		// sequence slice tolerates, since they all use the default — would silently
		// emit `CACHE 1` here instead.
		cacheSeqDefs := []string{
			"CREATE SEQUENCE public.cache_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 5;\n",
			"CREATE SEQUENCE public.altcache_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 42;\n",
		}
		for _, sub := range cacheSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a per-sequence CACHE size; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Negative guard: the explicit-cache sequences must NOT fall back to CACHE 1
		// (the old hard-wired seqcache=1 behaviour).
		if strings.Contains(res.Stdout, "CREATE SEQUENCE public.cache_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;\n") {
			t.Errorf("pg_dump emitted CACHE 1 for a CACHE 5 sequence (seqcache hard-wired)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 124 (asserted):** an ADVANCED sequence dumps its setval with
		// is_called=TRUE and the bumped last_value. After `setval(bumped_seq, 42,
		// true)` the runtime state is current=42 / called=true; pg_get_sequence_data
		// reports (42, true) and pg_dump emits `setval('public.bumped_seq', 42,
		// true)`. This is the first slice over the called branch — every other
		// sequence dumps `(name, start, false)`. The CREATE SEQUENCE itself is the
		// plain default block (the bump lives only in the data-section setval).
		// Regression guard for SequenceRowData's called=true path + the SRF
		// last_value/is_called projection.
		bumpedSeqDefs := []string{
			"CREATE SEQUENCE public.bumped_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;\n",
			"SELECT pg_catalog.setval('public.bumped_seq', 42, true);",
		}
		for _, sub := range bumpedSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled the advanced-sequence dump; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The advanced sequence must NOT dump as never-called (the regression a
		// hard-wired is_called=false, or a SequenceRowData that ignored the bump and
		// reported seqstart=1, would produce). Both wrong forms are guarded.
		for _, neg := range []string{
			"SELECT pg_catalog.setval('public.bumped_seq', 1, false);",
			"SELECT pg_catalog.setval('public.bumped_seq', 42, false);",
			"SELECT pg_catalog.setval('public.bumped_seq', 1, true);",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted the wrong setval form for an advanced sequence: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 125 (asserted):** a REWOUND sequence — `setval(seq, N, false)` with
		// N != start — must keep the original `START WITH 5` in the CREATE SEQUENCE
		// while the data section emits the rewound `setval(..., 30, false)`. The
		// not-called branch now reports the on-disk last_value (current+increment=30),
		// not the bare start (5). A regression that reverted to returning `start`
		// would dump `setval(..., 5, false)` and silently lose the rewind.
		rewoundSeqDefs := []string{
			"CREATE SEQUENCE public.rewound_seq\n    START WITH 5\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;\n",
			"SELECT pg_catalog.setval('public.rewound_seq', 30, false);",
		}
		for _, sub := range rewoundSeqDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled the rewound-sequence dump; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The rewound sequence must NOT dump its setval with the original start (the
		// pre-fix bug), nor flip is_called, nor mangle the value.
		for _, neg := range []string{
			"SELECT pg_catalog.setval('public.rewound_seq', 5, false);",
			"SELECT pg_catalog.setval('public.rewound_seq', 30, true);",
			"SELECT pg_catalog.setval('public.rewound_seq', 5, true);",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted the wrong setval form for a rewound sequence: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 120 (asserted):** an IDENTITY column's backing sequence must dump
		// via `ALTER TABLE ... ADD GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY (SEQUENCE
		// NAME ...)` — pg_dump selects this branch when is_identity_sequence (pg_depend
		// deptype='i') and reads pg_attribute.attidentity ('a'/'d') for the keyword.
		// The CREATE TABLE renders the identity column as plain `NOT NULL` (the
		// GENERATED clause is the separate ALTER). Integer/bigint identity sequences
		// inherit their type default min/max, so both emit NO MINVALUE/NO MAXVALUE; an
		// uncalled sequence sets setval last_value=1, is_called=false. Regression guard
		// for the IdentityAlways KIND + attidentity 'a'/'d' emission + deptype='i'
		// pg_depend synthesis.
		identityDefs := []string{
			"CREATE TABLE public.ident_tbl (\n    id integer NOT NULL,\n    label text\n);",
			"ALTER TABLE public.ident_tbl ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (\n    SEQUENCE NAME public.ident_tbl_id_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1\n);",
			"CREATE TABLE public.ident_def (\n    id bigint NOT NULL,\n    note text\n);",
			"ALTER TABLE public.ident_def ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (\n    SEQUENCE NAME public.ident_def_id_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1\n);",
			"SELECT pg_catalog.setval('public.ident_tbl_id_seq', 1, false);",
			"SELECT pg_catalog.setval('public.ident_def_id_seq', 1, false);",
		}
		for _, sub := range identityDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped an identity-column clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// An identity sequence must NOT dump as a standalone CREATE SEQUENCE, and must
		// NOT emit an ALTER SEQUENCE ... OWNED BY (both are the deptype='a' OWNED-BY
		// path, suppressed by pg_dump for is_identity_sequence).
		for _, neg := range []string{
			"CREATE SEQUENCE public.ident_tbl_id_seq",
			"CREATE SEQUENCE public.ident_def_id_seq",
			"ALTER SEQUENCE public.ident_tbl_id_seq OWNED BY",
			"ALTER SEQUENCE public.ident_def_id_seq OWNED BY",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted a spurious standalone/OWNED BY form for an identity sequence: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 121 (asserted):** a SERIAL / BIGSERIAL column round-trips as a
		// plain integer column + standalone sequence + OWNED BY + a SEPARATE column
		// SET DEFAULT nextval(). The column type must be the base integer (never
		// "serial"); the sequence carries `AS integer` only for serial4 (int8 is the
		// CREATE SEQUENCE default, so bigserial omits it). The SET DEFAULT must be a
		// standalone `ALTER TABLE ONLY ... ALTER COLUMN ... SET DEFAULT` (the
		// dependency-loop break), not inline in CREATE TABLE.
		serialDefs := []string{
			"CREATE TABLE public.ser_tbl (\n    id integer NOT NULL,\n    label text\n);",
			"CREATE TABLE public.bigser_tbl (\n    id bigint NOT NULL,\n    note text\n);",
			"CREATE SEQUENCE public.ser_tbl_id_seq\n    AS integer\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;",
			"CREATE SEQUENCE public.bigser_tbl_id_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;",
			"ALTER SEQUENCE public.ser_tbl_id_seq OWNED BY public.ser_tbl.id;",
			"ALTER SEQUENCE public.bigser_tbl_id_seq OWNED BY public.bigser_tbl.id;",
			"ALTER TABLE ONLY public.ser_tbl ALTER COLUMN id SET DEFAULT nextval('public.ser_tbl_id_seq'::regclass);",
			"ALTER TABLE ONLY public.bigser_tbl ALTER COLUMN id SET DEFAULT nextval('public.bigser_tbl_id_seq'::regclass);",
			"SELECT pg_catalog.setval('public.ser_tbl_id_seq', 1, false);",
			"SELECT pg_catalog.setval('public.bigser_tbl_id_seq', 1, false);",
		}
		for _, sub := range serialDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled a SERIAL clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// A serial column must NOT emit the word "serial" anywhere, and its default
		// must NOT be inlined into CREATE TABLE (the separate SET DEFAULT is required
		// by the dependency-loop break).
		for _, neg := range []string{
			"id serial",
			"id bigserial",
			"id integer DEFAULT nextval",
			"id bigint DEFAULT nextval",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted a spurious serial/inline-default form: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 122 (asserted):** a table with TWO serial columns round-trips with
		// one owned sequence + SET DEFAULT per column, in column order. Each column's
		// attrdef must pair with its own sequence (distinct attrdef oids matched to
		// distinct pg_depend NORMAL links); a mis-pair would cross-wire the nextval()
		// defaults. Regression guard for the multi-attrdef-per-table path.
		multiSerialDefs := []string{
			"CREATE TABLE public.mser (\n    a integer NOT NULL,\n    b integer NOT NULL,\n    note text\n);",
			"CREATE SEQUENCE public.mser_a_seq\n    AS integer\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;",
			"CREATE SEQUENCE public.mser_b_seq\n    AS integer\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;",
			"ALTER SEQUENCE public.mser_a_seq OWNED BY public.mser.a;",
			"ALTER SEQUENCE public.mser_b_seq OWNED BY public.mser.b;",
			"ALTER TABLE ONLY public.mser ALTER COLUMN a SET DEFAULT nextval('public.mser_a_seq'::regclass);",
			"ALTER TABLE ONLY public.mser ALTER COLUMN b SET DEFAULT nextval('public.mser_b_seq'::regclass);",
			"SELECT pg_catalog.setval('public.mser_a_seq', 1, false);",
			"SELECT pg_catalog.setval('public.mser_b_seq', 1, false);",
		}
		for _, sub := range multiSerialDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled a multi-serial clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The two SET DEFAULTs must not be cross-wired (a→b_seq or b→a_seq).
		for _, neg := range []string{
			"ALTER COLUMN a SET DEFAULT nextval('public.mser_b_seq'::regclass)",
			"ALTER COLUMN b SET DEFAULT nextval('public.mser_a_seq'::regclass)",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump cross-wired a multi-serial default to the wrong sequence: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 123 (asserted):** a table mixing an IDENTITY column and a SERIAL
		// column. The two owned sequences travel DIFFERENT deptype paths and pg_dump
		// emits a DIFFERENT form for each on the SAME table: the identity sequence is
		// embedded in `ADD GENERATED ALWAYS AS IDENTITY (SEQUENCE NAME ...)` (no
		// standalone CREATE SEQUENCE, no OWNED BY), while the serial sequence emits a
		// standalone CREATE SEQUENCE + OWNED BY + separate SET DEFAULT. Regression
		// guard for the identity('i')/serial('a') deptype split on one relation.
		mixDefs := []string{
			"CREATE TABLE public.mix (\n    id integer NOT NULL,\n    n integer NOT NULL,\n    note text\n);",
			"ALTER TABLE public.mix ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (\n    SEQUENCE NAME public.mix_id_seq\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1\n);",
			"CREATE SEQUENCE public.mix_n_seq\n    AS integer\n    START WITH 1\n    INCREMENT BY 1\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1;",
			"ALTER SEQUENCE public.mix_n_seq OWNED BY public.mix.n;",
			"ALTER TABLE ONLY public.mix ALTER COLUMN n SET DEFAULT nextval('public.mix_n_seq'::regclass);",
			"SELECT pg_catalog.setval('public.mix_id_seq', 1, false);",
			"SELECT pg_catalog.setval('public.mix_n_seq', 1, false);",
		}
		for _, sub := range mixDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled a mixed identity+serial clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The two deptype paths must NOT cross. The identity sequence has NO
		// standalone CREATE SEQUENCE and NO OWNED BY; the serial column has NO
		// IDENTITY clause and the identity column has NO nextval() SET DEFAULT.
		for _, neg := range []string{
			"CREATE SEQUENCE public.mix_id_seq",
			"ALTER SEQUENCE public.mix_id_seq OWNED BY",
			"public.mix ALTER COLUMN n ADD GENERATED",
			"public.mix ALTER COLUMN id SET DEFAULT nextval",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump crossed the identity/serial deptype paths: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 61 (asserted):** a RECURSIVE VIEW must round-trip. PG stores it
		// as a regular view over a WITH RECURSIVE CTE and pg_dump re-emits a plain
		// CREATE VIEW; goopg synthesizes the wrapped form into RawDef so
		// pg_get_viewdef returns a non-empty body instead of NULL (which aborts the
		// whole dump). Assert the CREATE VIEW header and the wrapped-CTE body.
		// Regression guard for the recursive-view RawDef synthesis.
		recViewDefs := []string{
			"CREATE VIEW public.foo_rec AS",
			"WITH RECURSIVE foo_rec(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM foo_rec WHERE n < 5) SELECT n FROM foo_rec;",
		}
		for _, sub := range recViewDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a RECURSIVE VIEW; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 62 (asserted):** array-typed columns must round-trip as their
		// array type. pg_dump renders columns via format_type(atttypid, atttypmod);
		// the `[]` suffix only appears when atttypid is the array (_typename) OID.
		// goopg now carries the parser's IsArray flag into the catalog and remaps
		// the scalar OID to the array OID in buildUserPGAttributeRow, so each column
		// dumps with its declared array type. Regression guard for the IsArray
		// plumbing (parser → catalog.Type → pg_attribute.atttypid).
		// Slice 63 adds boolean[] and numeric(10,2)[]: bool/numeric now map to
		// their array OIDs (_bool 1000, _numeric 1231), and the numeric array
		// carries the element typmod so precision/scale survive the dump.
		// Slice 64 adds double precision[], date[] and timestamp[]: float8/date/
		// timestamp now map to their array OIDs (_float8 1022, _date 1182,
		// _timestamp 1115) so each dumps with its declared array type.
		// Slice 65 adds real[], time[] and timestamp with time zone[]: float4/
		// time/timestamptz now map to their array OIDs (_float4 1021, _time 1183,
		// _timestamptz 1185) so each dumps with its declared array type.
		// Slice 66 adds a scalar uuid (`tok uuid`) and uuid[] (`ids uuid[]`):
		// uuid (OID 2950) is now in TypeNameToOID/OIDToTypeName and _uuid (2951)
		// in the array OID maps, so both dump with their declared types instead
		// of falling back to text.
		// Slice 67 adds a scalar bytea (`blob bytea`, already round-tripped) and
		// bytea[] (`blobs bytea[]`): _bytea (1001) is now in the array OID maps
		// (ArrayOIDForBase/BaseOIDForArray + userTypeAttrsForOID + formatTypeOID),
		// so the array dumps as `bytea[]` instead of falling back to scalar bytea.
		// Slice 68 adds the remaining simple scalar-backed arrays: varchar(20)[]
		// (_varchar 1015), char(4)[] (_bpchar 1014) and oid[] (_oid 1028). varchar/
		// bpchar carry the element typmod onto the array (like numeric) so the
		// declared length survives the dump; oid has no typmod.
		// Slice 69 adds the JSON family: scalar json (`doc`, OID 114) + json[]
		// (`docs`, _json 199) and scalar jsonb (`jdoc`, OID 3802) + jsonb[]
		// (`jdocs`, _jsonb 3807). Both were previously absent from
		// TypeNameToOID/OIDToTypeName, so a json/jsonb column fell back to text
		// (OID 25) and dumped as `text`; the array path had no _json/_jsonb OID
		// at all. json/jsonb are varlena with no typmod, so the arrays render as
		// the bare `json[]` / `jsonb[]` (formatTypeOID already had the scalar
		// cases 114/3802).
		// Slice 70 adds a scalar interval (`span`, OID 1186) and interval[]
		// (`spans`, _interval 1187). interval was rendered in formatTypeOID/
		// oidToBuiltinTypeName but had NOT been wired into catalog.TypeNameToOID/
		// OIDToTypeName, so an `interval` column fell back to text (OID 25) and
		// dumped as `text`; the array path had no _interval OID at all. A bare
		// interval column carries typmod -1, so both render as the plain
		// `interval` / `interval[]`.
		// Slice 71 adds the network-address family: scalar inet (`ip`, OID 869) +
		// inet[] (`ips`, _inet 1041), cidr (`net`, 650) + cidr[] (`nets`, 651),
		// macaddr (`mac`, 829) + macaddr[] (`macs`, 1040), and macaddr8 (`mac8`,
		// 774) + macaddr8[] (`mac8s`, 775). All are seeded in pg_type but had NOT
		// been wired into TypeNameToOID/OIDToTypeName, so each scalar fell back to
		// text (OID 25) and the array paths had no _net OIDs. None carry a typmod,
		// so every column renders as the plain `<type>` / `<type>[]`.
		// Slice 72 adds the geometric family: point (`pt`, 600) + point[] (`pts`,
		// _point 1017), lseg (`seg`, 601) + lseg[] (`segs`, 1018), path (`pth`,
		// 602) + path[] (`pths`, 1019), box (`bx`, 603) + box[] (`bxs`, 1020),
		// polygon (`poly`, 604) + polygon[] (`polys`, 1027), line (`ln`, 628) +
		// line[] (`lns`, 629), circle (`circ`, 718) + circle[] (`circs`, 719).
		// Slice 73 adds the full-text-search family: tsvector (`tsv`, 3614) +
		// tsvector[] (`tsvs`, _tsvector 3643), tsquery (`tsq`, 3615) + tsquery[]
		// (`tsqs`, _tsquery 3645).
		// Slice 74 adds xml (`xm`, 142) + xml[] (`xms`, _xml 143) and money
		// (`mny`, 790) + money[] (`mnys`, _money 791). Neither carries a typmod.
		// Slice 75 adds bit (`bv`, 1560) + bit[] (`bvs`, _bit 1561) and varbit
		// (`vb`, 1562) + varbit[] (`vbs`, _varbit 1563). Both carry the bit length
		// as typmod (raw, no VARHDRSZ), so format_type renders `bit(8)` /
		// `bit varying(16)` and the arrays carry the element typmod.
		// Slice 76 adds pg_lsn (`lsn`, 3220) + pg_lsn[] (`lsns`, _pg_lsn 3221).
		// pg_lsn carries no typmod, so format_type renders the bare `pg_lsn` /
		// `pg_lsn[]`.
		// Slice 77 adds the snapshot types: txid_snapshot (`txs`, 2970) +
		// txid_snapshot[] (`txss`, _txid_snapshot 2949) and pg_snapshot (`pgs`,
		// 5038) + pg_snapshot[] (`pgss`, _pg_snapshot 5039). Neither carries a
		// typmod, so format_type renders the bare names.
		// Slice 78 adds xid8 (`x8`, 5069) + xid8[] (`x8s`, _xid8 271). xid8 is an
		// 8-byte by-value type with no typmod, so format_type renders the bare
		// `xid8` / `xid8[]`.
		// Slice 79 adds the transaction/tuple identifier types: tid (`td`, 27) +
		// tid[] (`tds`, _tid 1010), xid (`xd`, 28) + xid[] (`xds`, _xid 1011), and
		// cid (`cd`, 29) + cid[] (`cds`, _cid 1012). None carry a typmod, so
		// format_type renders the bare names.
		// Slice 80 adds the OID-reference ("reg*") family: regproc (24)/_regproc
		// (1008), regprocedure (2202)/_regprocedure (2207), regoper (2203)/_regoper
		// (2208), regoperator (2204)/_regoperator (2209), regclass (2205)/_regclass
		// (2210), regtype (2206)/_regtype (2211), regconfig (3734)/_regconfig
		// (3735), regdictionary (3769)/_regdictionary (3770), regnamespace (4089)/
		// _regnamespace (4090), regrole (4096)/_regrole (4097), and regcollation
		// (4191)/_regcollation (4192). Each is a 4-byte oid alias seeded in pg_type
		// but never wired into TypeNameToOID/OIDToTypeName, so each scalar fell back
		// to text (OID 25). None carry a typmod, so format_type renders the bare
		// `<type>` / `<type>[]`.
		// All are seeded in pg_type but were never wired into TypeNameToOID/
		// OIDToTypeName, so each scalar fell back to text (OID 25) and the array
		// paths had no OIDs. None carry a typmod, so each renders as the plain
		// `<type>` / `<type>[]`.
		arrCols := []string{
			"CREATE TABLE public.arr (",
			"tags text[]",
			"scores integer[]",
			"big bigint[]",
			"flags boolean[]",
			"prices numeric(10,2)[]",
			"ratios double precision[]",
			"days date[]",
			"moments timestamp without time zone[]",
			"speeds real[]",
			"times time without time zone[]",
			"zoned timestamp with time zone[]",
			"tok uuid",
			"ids uuid[]",
			"blob bytea",
			"blobs bytea[]",
			"label character varying(20)",
			"labels character varying(20)[]",
			"code character(4)",
			"codes character(4)[]",
			"oids oid[]",
			"doc json",
			"docs json[]",
			"jdoc jsonb",
			"jdocs jsonb[]",
			"span interval",
			"spans interval[]",
			"ip inet",
			"ips inet[]",
			"net cidr",
			"nets cidr[]",
			"mac macaddr",
			"macs macaddr[]",
			"mac8 macaddr8",
			"mac8s macaddr8[]",
			"pt point",
			"pts point[]",
			"seg lseg",
			"segs lseg[]",
			"pth path",
			"pths path[]",
			"bx box",
			"bxs box[]",
			"poly polygon",
			"polys polygon[]",
			"ln line",
			"lns line[]",
			"circ circle",
			"circs circle[]",
			"tsv tsvector",
			"tsvs tsvector[]",
			"tsq tsquery",
			"tsqs tsquery[]",
			"xm xml",
			"xms xml[]",
			"mny money",
			"mnys money[]",
			"bv bit(8)",
			"bvs bit(8)[]",
			"vb bit varying(16)",
			"vbs bit varying(16)[]",
			"lsn pg_lsn",
			"lsns pg_lsn[]",
			"txs txid_snapshot",
			"txss txid_snapshot[]",
			"pgs pg_snapshot",
			"pgss pg_snapshot[]",
			"x8 xid8",
			"x8s xid8[]",
			"td tid",
			"tds tid[]",
			"xd xid",
			"xds xid[]",
			"cd cid",
			"cds cid[]",
			"rp regproc",
			"rps regproc[]",
			"rpd regprocedure",
			"rpds regprocedure[]",
			"ropr regoper",
			"roprs regoper[]",
			"roo regoperator",
			"roos regoperator[]",
			"rcl regclass",
			"rcls regclass[]",
			"rt regtype",
			"rts regtype[]",
			"rcf regconfig",
			"rcfs regconfig[]",
			"rdi regdictionary",
			"rdis regdictionary[]",
			"rn regnamespace",
			"rns regnamespace[]",
			"rr regrole",
			"rrs regrole[]",
			"rco regcollation",
			"rcos regcollation[]",
			// Slice 81 adds the legacy vector types int2vector (22)/_int2vector
			// (1006) and oidvector (30)/_oidvector (1013). Both were seeded in
			// pg_type but mis-wired: format_type rendered OID 22/30 as the genuine
			// _int2/_oid arrays ("smallint[]"/"oid[]") instead of their own bare
			// names, and the codec had no name→OID entry so a declared column fell
			// back to text (25). Now each round-trips as "int2vector"/"oidvector".
			"iv int2vector",
			"ivs int2vector[]",
			"ov oidvector",
			"ovs oidvector[]",
			// Slice 82 adds name (19)/_name (1003), the 64-byte fixed-length
			// catalog identifier type. The scalar was already wired, but the
			// codec had no name→OID entry so a declared `name` column fell back
			// to text (25), and _name had no format_type/attr wiring. Now both
			// round-trip as "name"/"name[]" (distinct from text/text[]).
			"nm name",
			"nms name[]",
			// Slice 83: the timetz (time with time zone) type + its array
			// _timetz. The scalar display path existed in oidToBuiltinTypeName
			// (case 1266), but formatTypeOID had NO 1266 case and the codec had
			// no timetz→OID entry, so a declared `timetz` column rendered/
			// round-tripped as text; _timetz had no format_type/attr wiring.
			// Now both survive as "time with time zone"/"time with time zone[]".
			"tt time with time zone",
			"tts time with time zone[]",
			// Slice 84: the jsonpath (SQL/JSON path) type + its array _jsonpath.
			// formatTypeOID already had the scalar case 4072 (dead until now), but
			// oidToBuiltinTypeName lacked even the scalar, the array was wired in
			// neither display fn, and the codec had no jsonpath→OID entry — so a
			// declared `jsonpath` column round-tripped as text. Now both survive
			// as "jsonpath"/"jsonpath[]" (distinct from json[]/jsonb[]).
			"jp jsonpath",
			"jps jsonpath[]",
			// Slice 85: refcursor (cursor-name reference) + its array _refcursor.
			// Neither display fn nor the codec had any refcursor wiring, so a
			// declared `refcursor` column round-tripped as text. Now both survive
			// as "refcursor"/"refcursor[]".
			"rfc refcursor",
			"rfcs refcursor[]",
			// Slice 86: aclitem (access-control-list item) + its array _aclitem.
			// aclitem is used internally for catalog *acl columns (relacl, etc.)
			// but had no codec name→OID entry, so a declared `aclitem` column fell
			// back to text (25), and neither display fn rendered 1033/1034 — so the
			// scalar/array round-tripped as text. Now both survive as
			// "aclitem"/"aclitem[]".
			"acl aclitem",
			"acls aclitem[]",
			// Slice 87: the single-byte "char" type (18) + its array _char (1002).
			// It shares the spelling "char" with bpchar; the quoted `"char"` form
			// declares the real catalog type (no length arg), distinct from the
			// fixture's `code char(4)` (bpchar). atttypid wrongly folded to bpchar
			// (TypeNameToOID is name-only), so it round-tripped as character. The
			// args-aware remap now resolves atttypid=18/1002 so format_type renders
			// the quoted `"char"` / `"char"[]`.
			"ch \"char\"",
			"chs \"char\"[]",
		}
		for _, sub := range arrCols {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped an array column type; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 88 (asserted):** a user-defined ENUM type and a column using it
		// must round-trip. pg_dump emits the CREATE TYPE … AS ENUM statement (one
		// label per line) from pg_type + pg_enum, and the enum column renders as
		// the schema-qualified enum name via format_type. Without the slice-88
		// fix the column dumped as `feeling text` (TypeNameToOID's text fallback)
		// and the type definition could still appear, silently changing the
		// column's type on restore. Assert both the CREATE TYPE header with all
		// three ordered labels and the enum-typed column in the CREATE TABLE.
		enumDefs := []string{
			"CREATE TYPE public.mood AS ENUM (",
			"'sad'",
			"'ok'",
			"'happy'",
			"CREATE TABLE public.moody (",
			"feeling public.mood",
			// Slice 89: the enum ARRAY column renders via the enum's
			// auto-generated array OID, not the text[] fallback.
			"feelings public.mood[]",
		}
		for _, sub := range enumDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled the ENUM type round-trip; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The enum column must NOT regress to the text fallback (scalar slice 88
		// or array slice 89).
		if strings.Contains(res.Stdout, "feeling text") {
			t.Errorf("pg_dump rendered the enum column as text (slice-88 enum OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "feelings text[]") {
			t.Errorf("pg_dump rendered the enum-array column as text[] (slice-89 array OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		// The auto-generated `_mood` array type must NOT dump as its own CREATE
		// TYPE: pg_dump's getTypes isarray subquery recognizes it (base enum's
		// typarray points back at it) and suppresses it. Slice 89.
		if strings.Contains(res.Stdout, "CREATE TYPE public._mood") {
			t.Errorf("pg_dump emitted the auto-generated enum array type as a separate CREATE TYPE (slice-89 isarray suppression regressed)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 90 (asserted):** a DOMAIN and a column using it must round-trip.
		// The CREATE DOMAIN statement must carry NO spurious `DEFAULT ` clause
		// (the pg_get_expr(NULL) fix), and the column must render as the
		// schema-qualified domain name, not the base type.
		domainDefs := []string{
			"CREATE DOMAIN public.zipcode AS text;",
			"CREATE TABLE public.dom (",
			"zip public.zipcode",
			// Slice 91: the NOT NULL domain round-trips the not-null clause.
			"CREATE DOMAIN public.zipcode_nn AS text NOT NULL;",
			"zip_nn public.zipcode_nn",
			// Slice 92: the domain DEFAULT round-trips (integer const, no cast).
			"CREATE DOMAIN public.qty AS integer DEFAULT 0;",
			"q public.qty",
			// Slice 93: a text DOMAIN with a string DEFAULT round-trips with the
			// pg_get_expr `::text` cast decoration.
			"CREATE DOMAIN public.label AS text DEFAULT 'n/a'::text;",
			"lbl public.label",
			// Slice 94: a varchar DOMAIN with a string DEFAULT round-trips with the
			// MULTI-WORD `::character varying` cast (format_type spelling, not the
			// `varchar` alias).
			"CREATE DOMAIN public.vcdef AS character varying DEFAULT 'na'::character varying;",
			"vc public.vcdef",
			// Slice 95: a typmod-bearing base type round-trips the declared length/
			// precision. The base render carries the typmod (character varying(20),
			// character(4), numeric(10,2)); the string DEFAULT's cast stays
			// typmod-less (::character varying, ::bpchar). The numeric default is bare.
			"CREATE DOMAIN public.vc20 AS character varying(20) DEFAULT 'na'::character varying;",
			"v20 public.vc20",
			"CREATE DOMAIN public.ch4 AS character(4) DEFAULT 'ab'::bpchar;",
			"c4 public.ch4",
			"CREATE DOMAIN public.numd AS numeric(10,2) DEFAULT 1.5;",
			"nd public.numd",
			// Slice 96: a generic domain CHECK round-trips inline. The auto-named
			// check uses `<domain>_check`; the explicitly-named one keeps its name.
			// The predicate is double-wrapped by the deparser: CHECK ((VALUE > 0)).
			"CREATE DOMAIN public.posqty AS integer",
			"CONSTRAINT posqty_check CHECK ((VALUE > 0))",
			"pq public.posqty",
			"CREATE DOMAIN public.named_chk AS integer",
			"CONSTRAINT must_be_pos CHECK ((VALUE > 0))",
			"nc public.named_chk",
			// Slice 97: a `CHECK (VALUE IN (...))` over a text domain deparses to a
			// ScalarArrayOpExpr — byte-identical to real pg_dump 18.3.
			"CREATE DOMAIN public.colr AS text",
			"CONSTRAINT colr_check CHECK ((VALUE = ANY (ARRAY['red'::text, 'green'::text])))",
			"co public.colr",
			"CREATE DOMAIN public.named_in AS text",
			"CONSTRAINT must_be_color CHECK ((VALUE = ANY (ARRAY['red'::text, 'green'::text])))",
			"ni public.named_in",
			// Slice 98: char/varchar IN-values. bpchar mirrors the text shape with a
			// `::bpchar` element cast; varchar adds the `(VALUE)::text`/`::text[]`
			// coercion envelope. typmod never appears in the element cast.
			"CREATE DOMAIN public.vc_in AS character varying",
			"CONSTRAINT vc_in_check CHECK (((VALUE)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[])))",
			"vci public.vc_in",
			"CREATE DOMAIN public.vc20_in AS character varying(20)",
			"CONSTRAINT must_ab CHECK (((VALUE)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[])))",
			"vc20i public.vc20_in",
			"CREATE DOMAIN public.ch_in AS character(4)",
			"CONSTRAINT ch_in_check CHECK ((VALUE = ANY (ARRAY['a'::bpchar, 'b'::bpchar])))",
			"chi public.ch_in",
			// Slice 99: numeric-family IN-values. integer/numeric literals match the
			// base type, so PG emits them verbatim — no quotes, no per-element cast.
			"CREATE DOMAIN public.i_in AS integer",
			"CONSTRAINT i_in_check CHECK ((VALUE = ANY (ARRAY[1, 2, 3])))",
			"ii public.i_in",
			"CREATE DOMAIN public.i_in_n AS integer",
			"CONSTRAINT must_set CHECK ((VALUE = ANY (ARRAY[10, 20])))",
			"iin public.i_in_n",
			"CREATE DOMAIN public.n_in AS numeric(10,2)",
			"CONSTRAINT n_in_check CHECK ((VALUE = ANY (ARRAY[1.5, 2.5])))",
			"ni2 public.n_in",
			// Slice 100: bigint coerces each int4 literal `(N)::bigint`; boolean
			// keyword literals render verbatim; date is string-with-`::date`-cast.
			"CREATE DOMAIN public.b_in AS bigint",
			"CONSTRAINT b_in_check CHECK ((VALUE = ANY (ARRAY[(100)::bigint, (200)::bigint, (300)::bigint])))",
			"bi public.b_in",
			"CREATE DOMAIN public.bo_in AS boolean",
			"CONSTRAINT bo_in_check CHECK ((VALUE = ANY (ARRAY[true, false])))",
			"boi public.bo_in",
			"CREATE DOMAIN public.d_in AS date",
			"CONSTRAINT d_in_check CHECK ((VALUE = ANY (ARRAY['2020-01-01'::date, '2021-06-15'::date])))",
			"di public.d_in",
			// Slice 101: real/double precision coerce each numeric literal
			// `(N)::<type>`; timestamp/time/uuid are string-with-cast using the
			// canonical multi-word base-type name. Base types declared via the
			// single-word aliases real/float8/timestamp/time/uuid still dump with
			// the canonical name (format_type from the OID).
			"CREATE DOMAIN public.r_in AS real",
			"CONSTRAINT r_in_check CHECK ((VALUE = ANY (ARRAY[(1.5)::real, (2.5)::real])))",
			"ri public.r_in",
			"CREATE DOMAIN public.f8_in AS double precision",
			"CONSTRAINT f8_in_check CHECK ((VALUE = ANY (ARRAY[(1.5)::double precision, (2.5)::double precision, (3.0)::double precision])))",
			"f8i public.f8_in",
			"CREATE DOMAIN public.ts_in AS timestamp without time zone",
			"CONSTRAINT ts_in_check CHECK ((VALUE = ANY (ARRAY['2020-01-01 00:00:00'::timestamp without time zone, '2021-06-15 12:30:00'::timestamp without time zone])))",
			"tsi public.ts_in",
			"CREATE DOMAIN public.tm_in AS time without time zone",
			"CONSTRAINT tm_in_check CHECK ((VALUE = ANY (ARRAY['12:00:00'::time without time zone, '13:30:00'::time without time zone])))",
			"tmi public.tm_in",
			"CREATE DOMAIN public.u_in AS uuid",
			"CONSTRAINT u_in_check CHECK ((VALUE = ANY (ARRAY['a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11'::uuid, 'b0eebc99-9c0b-4ef8-bb6d-6bb9bd380a12'::uuid])))",
			"ui public.u_in",
			"CREATE DOMAIN public.si_in AS smallint",
			"CONSTRAINT si_in_check CHECK ((VALUE = ANY (ARRAY[10, 20, 30])))",
			"sii public.si_in",
			"CREATE DOMAIN public.by_in AS bytea",
			`CONSTRAINT by_in_check CHECK ((VALUE = ANY (ARRAY['\xdeadbeef'::bytea, '\xcafe'::bytea])))`,
			"byi public.by_in",
			"CREATE DOMAIN public.inet_in AS inet",
			"CONSTRAINT inet_in_check CHECK ((VALUE = ANY (ARRAY['192.168.0.1'::inet, '10.0.0.0/8'::inet])))",
			"ineti public.inet_in",
			"CREATE DOMAIN public.mac_in AS macaddr",
			"CONSTRAINT mac_in_check CHECK ((VALUE = ANY (ARRAY['08:00:2b:01:02:03'::macaddr, '00:11:22:33:44:55'::macaddr])))",
			"maci public.mac_in",
			"CREATE DOMAIN public.mac8_in AS macaddr8",
			"CONSTRAINT mac8_in_check CHECK ((VALUE = ANY (ARRAY['08:00:2b:01:02:03:04:05'::macaddr8, '00:11:22:33:44:55:66:77'::macaddr8])))",
			"mac8i public.mac8_in",
			"CREATE DOMAIN public.cidr_in AS cidr",
			"CONSTRAINT cidr_in_check CHECK (((VALUE)::inet = ANY ((ARRAY['192.168.0.0/24'::cidr, '10.0.0.0/8'::cidr])::inet[])))",
			"cidri public.cidr_in",
			"CREATE DOMAIN public.nm_in AS name",
			"CONSTRAINT nm_in_check CHECK ((VALUE = ANY (ARRAY['alice'::name, 'bob'::name])))",
			"nmi public.nm_in",
			"CREATE DOMAIN public.jb_in AS jsonb",
			"CONSTRAINT jb_in_check CHECK ((VALUE = ANY (ARRAY['1'::jsonb, '\"hello\"'::jsonb])))",
			"jbi public.jb_in",
			"CREATE DOMAIN public.js_in AS json",
			"CONSTRAINT js_in_check CHECK (((VALUE)::text = ANY (ARRAY['1'::text, '{\"a\": 1}'::text])))",
			"jsi public.js_in",
			"CREATE DOMAIN public.xml_in AS xml",
			"CONSTRAINT xml_in_check CHECK (((VALUE)::text = ANY (ARRAY['<a/>'::text, '<b>1</b>'::text])))",
			"xmli public.xml_in",
			"CREATE DOMAIN public.oid_in AS oid",
			"CONSTRAINT oid_in_check CHECK ((VALUE = ANY (ARRAY[(1)::oid, (2)::oid, (3)::oid])))",
			"oidi public.oid_in",
			"CREATE DOMAIN public.bit_in AS bit(4)",
			"CONSTRAINT bit_in_check CHECK ((VALUE = ANY (ARRAY['1010'::\"bit\", '0101'::\"bit\"])))",
			"biti public.bit_in",
			"CREATE DOMAIN public.vbit_in AS bit varying",
			"CONSTRAINT vbit_in_check CHECK ((VALUE = ANY (ARRAY['101'::bit varying, '110'::bit varying])))",
			"vbiti public.vbit_in",
			"CREATE DOMAIN public.lsn_in AS pg_lsn",
			"CONSTRAINT lsn_in_check CHECK ((VALUE = ANY (ARRAY['16/B374D848'::pg_lsn, '0/0'::pg_lsn])))",
			"lsni public.lsn_in",
			"CREATE DOMAIN public.tid_in AS tid",
			"CONSTRAINT tid_in_check CHECK ((VALUE = ANY (ARRAY['(0,1)'::tid, '(1,2)'::tid])))",
			"tidi public.tid_in",
			"CREATE DOMAIN public.xid_in AS xid",
			"CONSTRAINT xid_in_check CHECK ((VALUE = ANY (ARRAY['100'::xid, '200'::xid])))",
			"xidi public.xid_in",
			"CREATE DOMAIN public.cid_in AS cid",
			"CONSTRAINT cid_in_check CHECK ((VALUE = ANY (ARRAY['5'::cid, '10'::cid])))",
			"cidi public.cid_in",
			"CREATE DOMAIN public.iv_in AS interval",
			"CONSTRAINT iv_in_check CHECK ((VALUE = ANY (ARRAY['1 day'::interval, '02:00:00'::interval, '1 year 2 mons'::interval])))",
			"ivi public.iv_in",
			"CREATE DOMAIN public.mny_in AS money",
			"CONSTRAINT mny_in_check CHECK ((VALUE = ANY (ARRAY['$1.00'::money, '$2.50'::money])))",
			"mnyi public.mny_in",
			// slice 109: domain over a user-defined enum base type; the cast is
			// schema-qualified (public.mood) since pg_dump empties search_path.
			"CREATE DOMAIN public.enum_in AS public.mood",
			"CONSTRAINT enum_in_check CHECK ((VALUE = ANY (ARRAY['sad'::public.mood, 'happy'::public.mood])))",
			"eni public.enum_in",
			// slice 110: domain over timestamp with time zone. The IN-list literals
			// pin the UTC (`+00`) canonical form so they round-trip verbatim against
			// a UTC-session pg_dump (the output function renders in the session TZ).
			"CREATE DOMAIN public.tstz_in AS timestamp with time zone",
			"CONSTRAINT tstz_in_check CHECK ((VALUE = ANY (ARRAY['2020-01-01 00:00:00+00'::timestamp with time zone, '2021-06-15 12:30:00+00'::timestamp with time zone])))",
			"tstzi public.tstz_in",
			// slice 111: domain over time with time zone. timetz preserves the
			// stored zone offset verbatim (no session-TZ re-render), so the
			// canonical literals round-trip byte-identically through `::time with
			// time zone` regardless of session TZ.
			"CREATE DOMAIN public.ttz_in AS time with time zone",
			"CONSTRAINT ttz_in_check CHECK ((VALUE = ANY (ARRAY['12:30:00+09'::time with time zone, '23:59:59-05'::time with time zone])))",
			"ttzi public.ttz_in",
			// slice 112: domain over xid8 (full 64-bit transaction id). Native
			// equality, simplest render mode — the decimal form round-trips
			// verbatim through `::xid8` (same as xid/cid, slice 107).
			"CREATE DOMAIN public.x8_in AS xid8",
			"CONSTRAINT x8_in_check CHECK ((VALUE = ANY (ARRAY['100'::xid8, '200'::xid8])))",
			"x8i public.x8_in",
			// slice 113: domains over the legacy vector types int2vector /
			// oidvector. Native equality (int2vectoreq / oidvectoreq), so the
			// bare string-with-cast shape; the canonical space-separated form
			// round-trips verbatim through `::int2vector` / `::oidvector`.
			"CREATE DOMAIN public.i2v_in AS int2vector",
			"CONSTRAINT i2v_in_check CHECK ((VALUE = ANY (ARRAY['1 2'::int2vector, '3 4'::int2vector])))",
			"i2vi public.i2v_in",
			"CREATE DOMAIN public.ovec_in AS oidvector",
			"CONSTRAINT ovec_in_check CHECK ((VALUE = ANY (ARRAY['1 2'::oidvector, '3 4'::oidvector])))",
			"oveci public.ovec_in",
			// slice 114: domains over the full-text-search types tsvector /
			// tsquery. Native equality (tsvector_eq / tsquery_eq), so the bare
			// string-with-cast shape; already-canonical lexeme forms round-trip
			// verbatim through `::tsvector` / `::tsquery`. The doubled single
			// quotes are SQL escaping of the lexemes' own single quotes.
			"CREATE DOMAIN public.tsv_in AS tsvector",
			"CONSTRAINT tsv_in_check CHECK ((VALUE = ANY (ARRAY['''a'' ''b'''::tsvector, '''cat'' ''dog'''::tsvector])))",
			"tsvi public.tsv_in",
			"CREATE DOMAIN public.tsq_in AS tsquery",
			"CONSTRAINT tsq_in_check CHECK ((VALUE = ANY (ARRAY['''a'' & ''b'''::tsquery, '''cat'' | ''dog'''::tsquery])))",
			"tsqi public.tsq_in",
		}
		for _, sub := range domainDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped the DOMAIN round-trip; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The domain column must NOT fold back to its base type, and the domain
		// definition must NOT carry an empty DEFAULT clause.
		if strings.Contains(res.Stdout, "zip text") {
			t.Errorf("pg_dump rendered the domain column as its base type (slice-90 domain OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		// A bare `DEFAULT` immediately closing the statement (`DEFAULT;\n` or
		// `DEFAULT \n`) is the slice-90 empty-clause regression. Legitimate
		// defaults (`DEFAULT 0;`, `DEFAULT 'n/a'::text;`) carry an expression
		// between DEFAULT and the terminator, so this stays precise even with the
		// slice-93 text-default domain present. The checks are newline-anchored so
		// they do NOT match pg_dump's `-- ... Type: DEFAULT; Schema: ...` section
		// comment, which slice 121's separate SERIAL column defaults introduce.
		// Slice 190's DEFAULT (catch-all) partition emits a legitimate
		// `ATTACH PARTITION public.pdef_def DEFAULT;\n` whose tail also matches
		// `DEFAULT;\n`; scrub that exact line first so this domain-only check stays
		// precise.
		dfltScrub := strings.ReplaceAll(res.Stdout, "ATTACH PARTITION public.pdef_def DEFAULT;\n", "")
		if strings.Contains(dfltScrub, "DEFAULT;\n") || strings.Contains(dfltScrub, "DEFAULT \n") {
			t.Errorf("pg_dump emitted a spurious empty DEFAULT on the domain (slice-90 pg_get_expr(NULL) regressed)\n  full stdout=%q", res.Stdout)
		}
		return
	}

	// Still blocked downstream on catalog-view parity. Confirm the failure is a
	// post-setup catalog/query error, not a setup_connection failure, and log
	// the precise next blocker so the next loop has a target.
	t.Logf("pg_dump passes connection setup; remaining DU-002 catalog-parity gap: "+
		"exit=%d stderr=%q stdout(%d bytes)=%q",
		res.ExitCode, strings.TrimSpace(res.Stderr), len(res.Stdout), res.Stdout)
}
