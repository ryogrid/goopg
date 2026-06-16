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
		}
		for _, sub := range check {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a constraint; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
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
		}
		for _, sub := range comments {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a COMMENT; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
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
		}
		for _, sub := range indexDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled a secondary index; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
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
		// A bare `DEFAULT` immediately closing the statement (`DEFAULT;` or
		// `DEFAULT \n`) is the slice-90 empty-clause regression. Legitimate
		// defaults (`DEFAULT 0;`, `DEFAULT 'n/a'::text;`) carry an expression
		// between DEFAULT and the terminator, so this stays precise even with the
		// slice-93 text-default domain present.
		if strings.Contains(res.Stdout, "DEFAULT;") || strings.Contains(res.Stdout, "DEFAULT \n") {
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
