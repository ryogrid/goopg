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
	// Slice 362: a COMPOUND or function-call table-level CHECK predicate must
	// round-trip with PostgreSQL's per-node parenthesization, not goopg's legacy
	// token-text wrap. goopg stores a CHECK body as token-reconstructed raw text
	// (parser.parseCheckExpr); the dump path wrapped it as `CHECK ((<raw>))`, which
	// is byte-correct ONLY for a single bare comparison. For a boolean combination
	// PG re-deparses each operand with its own parens (`a > 0 AND b > 0` →
	// `CHECK (((a > 0) AND (b > 0)))`), and a function call must lose the spaces the
	// token join inserts around parentheses (`length(name) > 0` →
	// `CHECK ((length(name) > 0))`). renderCheckPredicate (operators_ddl.go) now
	// re-parses the raw text and deparses it through defaultExprToSQL — the same
	// fully-parenthesizing renderer the index-predicate / expression-index /
	// partition-key paths use — reproducing PG's bytes; the single outer paren layer
	// comes from the `CHECK (%s)` wrapper (defaultExprToSQL already parenthesizes the
	// top-level OpExpr/BoolExpr). The single-comparison slices 127–129 are unchanged
	// (`a < b` still deparses to `(a < b)` → `CHECK ((a < b))`). Auto-naming already
	// matches PG (autoCheckName counts distinct columns via ParseExpr): the
	// two-column `chkand`/`chkor` get the table-only name, the one-column `chkfn` the
	// column-qualified name. Each on its own table so existing CHECK asserts are
	// untouched. Verified byte-identical to real pg_dump 18.3.
	if err := runSQLSimple(t, c, "CREATE TABLE public.chkand (a integer, b integer, CHECK (a > 0 AND b > 0))"); err != nil {
		t.Fatalf("create table chkand: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.chkor (a integer, b integer, CHECK (a < 0 OR b > 10))"); err != nil {
		t.Fatalf("create table chkor: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.chkfn (name text, CHECK (length(name) > 0))"); err != nil {
		t.Fatalf("create table chkfn: %v", err)
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
	// `toast.autovacuum_multixact_freeze_table_age='250000000'`. Slice 236 adds the
	// integer `toast.log_autovacuum_min_duration` option (valid -1–INT_MAX; unlike the
	// autovacuum-age options, -1 is a valid explicit value so the floor is -1 not 0),
	// extending the array to thirteen elements; pg_dump re-emits the integer element
	// `toast.log_autovacuum_min_duration='-1'`. Slice 237 adds the integer
	// `toast.autovacuum_vacuum_insert_threshold` option (RELOPT_KIND_HEAP|TOAST,
	// valid -1–INT_MAX; like log_autovacuum_min_duration both -1 and 0 are valid),
	// extending the array to fourteen elements; pg_dump re-emits the integer element
	// `toast.autovacuum_vacuum_insert_threshold='1000'`. Slice 238 adds the integer
	// `toast.autovacuum_vacuum_max_threshold` option (RELOPT_KIND_HEAP|TOAST, valid
	// -1–INT_MAX, default -2; both -1 and 0 valid so floor is -1), extending the array
	// to fifteen elements; pg_dump re-emits `toast.autovacuum_vacuum_max_threshold='2000'`.
	// Slice 239 adds the *real* `toast.autovacuum_vacuum_insert_scale_factor` option
	// (RELOPT_KIND_HEAP|TOAST, valid 0.0–100.0, default -1), extending the array to
	// sixteen elements; pg_dump re-emits the float element
	// `toast.autovacuum_vacuum_insert_scale_factor='1.5'`. Slice 240 adds the *real*
	// `toast.vacuum_max_eager_freeze_failure_rate` option (RELOPT_KIND_HEAP|TOAST, valid
	// 0.0–1.0 — a page fraction, not the 0.0–100.0 used by the autovacuum reals — default
	// -1), extending the array to seventeen elements; pg_dump re-emits the float element
	// `toast.vacuum_max_eager_freeze_failure_rate='0.5'`. Slice 241 adds the only
	// RELOPT_KIND_TOAST *enum* option (`toast.vacuum_index_cleanup`, valid
	// auto/on/off/true/false/yes/no/1/0, stored verbatim), extending the array to
	// eighteen elements; pg_dump re-emits the enum element
	// `toast.vacuum_index_cleanup='on'`. This is the 18th and final RELOPT_KIND_TOAST
	// option, so the toast.* reloption surface is now complete.
	if err := runSQLSimple(t, c, "CREATE TABLE public.optoast (id integer PRIMARY KEY) WITH (toast.autovacuum_enabled=false, toast.vacuum_truncate=false, toast.autovacuum_vacuum_threshold=100, toast.autovacuum_vacuum_scale_factor=2.5, toast.autovacuum_vacuum_cost_delay=10.5, toast.autovacuum_vacuum_cost_limit=500, toast.autovacuum_freeze_min_age=200000000, toast.autovacuum_freeze_max_age=500000000, toast.autovacuum_freeze_table_age=0, toast.autovacuum_multixact_freeze_min_age=150000000, toast.autovacuum_multixact_freeze_max_age=500000000, toast.autovacuum_multixact_freeze_table_age=250000000, toast.log_autovacuum_min_duration=-1, toast.autovacuum_vacuum_insert_threshold=1000, toast.autovacuum_vacuum_max_threshold=2000, toast.autovacuum_vacuum_insert_scale_factor=1.5, toast.vacuum_max_eager_freeze_failure_rate=0.5, toast.vacuum_index_cleanup=on)"); err != nil {
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

	// Slice 300: a nested-arithmetic EXPRESSION partition key — the third deparse
	// context fed by executor.defaultExprToSQL (after slice 298's index PREDICATE
	// and slice 299's index COLUMN), reached via pg_get_partkeydef(oid). Every
	// prior partition fixture uses a bare COLUMN key, so this is the first to
	// exercise the expression branch. It exposed (and now pins the fix for) a real
	// divergence: pg_get_partkeydef_worker (ruleutils.c) wraps each non-function
	// expression key in `(%s)` (the `looks_like_function` branch), but goopg's
	// pg_get_partkeydef emitted defaultExprToSQL(keyExpr) with NO wrap — so
	// `PARTITION BY RANGE (((a + b) * c))` dumped as `RANGE (((a + b) * c))`, one
	// paren short of real pg_dump 18.3's `RANGE ((((a + b) * c)))` (verified
	// byte-identical against a live PG 18.3 instance). The fix adds the `(%s)`
	// wrap unless the key is a *parser.FuncCall (goopg's single representation for
	// every callable form — `abs(a)` correctly stays unwrapped as `RANGE (abs(a))`).
	// `pexpr` carries its own table so the many `foo` asserts are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pexpr (a integer, b integer, c integer) PARTITION BY RANGE (((a + b) * c))"); err != nil {
		t.Fatalf("create expression-key partitioned table pexpr: %v", err)
	}

	// Slice 262: a MULTI-COLUMN RANGE partition with a MINVALUE/MAXVALUE open edge
	// on a NON-leading column. Every prior partition fixture (part/prange_am) is
	// single-column, so the per-element bound machinery added in slices 169/261 —
	// FormatPartitionBound's `, `-joined multi-element tuple, the parallel
	// From/ToValueLiterals capture, and the parallel From/ToUnbounded[Max] flag
	// tuples that route a concrete prefix column against an unbounded suffix edge —
	// had never been exercised end to end through pg_dump. `pmc` is partitioned BY
	// RANGE (a, b); `pmc_lo` keeps a fully-open lower bound `(MINVALUE, MINVALUE)`
	// and a mixed upper bound `(10, MAXVALUE)` (concrete leading + open trailing),
	// the exact shape PG requires (once an element is MINVALUE/MAXVALUE, every
	// trailing element must match). pg_dump must re-emit the two-element tuples
	// verbatim with bare keywords (not quoted literals) so the relpartbound restores
	// as the same unbounded edges.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pmc (a integer, b integer, val text) PARTITION BY RANGE (a, b)"); err != nil {
		t.Fatalf("create multi-column RANGE-partitioned table pmc: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pmc_lo PARTITION OF public.pmc FOR VALUES FROM (MINVALUE, MINVALUE) TO (10, MAXVALUE)"); err != nil {
		t.Fatalf("create multi-column RANGE partition pmc_lo: %v", err)
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

	// Slice 263: a WIDE multi-level partition tree — beyond slice 171's single-leaf
	// `psub_east` chain. Two distinct fan-out shapes that slice 171 never exercised:
	//   (a) one middle node with MULTIPLE leaves: `psub_east` gains a second leaf
	//       `psub_east_hi` alongside `psub_east_lo`, so pg_inherits must emit TWO
	//       child rows that both point at the same `psub_east` parent (the per-parent
	//       inhseqno counter in catalog.go must increment independently per leaf), and
	//       pg_dump must emit a separate ATTACH for each.
	//   (b) a SIBLING sub-partitioned middle node: `psub_west` is a second LIST
	//       partition of `psub` that is itself partitioned BY RANGE, with its own leaf
	//       `psub_west_lo`. This proves a leaf resolves its IMMEDIATE parent (the leaf
	//       under `psub_west` must ATTACH to `psub_west`, NOT to the sibling
	//       `psub_east` nor the grandparent `psub`) when several middle nodes coexist.
	// No production change — pg_inherits already keys parent rows by each child's own
	// PartitionParentOID (catalog.go ~4110); this slice proves + guards the wide tree.
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub_east_hi PARTITION OF public.psub_east FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("create second leaf partition psub_east_hi: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub_west PARTITION OF public.psub FOR VALUES IN ('west') PARTITION BY RANGE (id)"); err != nil {
		t.Fatalf("create sibling sub-partitioned partition psub_west: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.psub_west_lo PARTITION OF public.psub_west FOR VALUES FROM (0) TO (100)"); err != nil {
		t.Fatalf("create leaf partition psub_west_lo: %v", err)
	}

	// Slice 264: a CHILD-ONLY CHECK constraint on a LIST partition leaf must
	// round-trip through pg_dump. A partition child inherits every column from
	// the partitioned parent, so pg_dump prints NONE of them (shouldPrintColumn
	// is false for an inherited attribute) — the leaf's CREATE TABLE body is
	// otherwise empty. But a named CHECK declared in the PARTITION OF
	// column-override list (`(CONSTRAINT pchk_1_pos CHECK (a > 0))`) is LOCAL to
	// the leaf: execCreatePartitionChild routes it through tbl.AddCheck, which
	// records IsLocal=true / InhCount=0 (operators_ddl.go ~3178). goopg's
	// pg_constraint VirtualRows then emits that row with conislocal='t',
	// conrelid=leaf OID, and pg_get_constraintdef renders `CHECK ((a > 0))`
	// (expr.go ~6727), so the real pg_dump 18.3 emits `CONSTRAINT pchk_1_pos
	// CHECK ((a > 0))` INSIDE the leaf's column-less CREATE TABLE body, then the
	// ATTACH. Every prior partition fixture (psub*/part/prange*/pmc/pdef/pfo/
	// ptbs/puse) left the partition-leaf local-constraint dump path untested; this
	// slice proves and guards it. No production change.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pchk (a integer) PARTITION BY LIST (a)"); err != nil {
		t.Fatalf("create LIST-partitioned table pchk: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pchk_1 PARTITION OF public.pchk (CONSTRAINT pchk_1_pos CHECK (a > 0)) FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pchk_1 with child-only CHECK: %v", err)
	}

	// Slice 265: a CHILD-ONLY column DEFAULT on a LIST partition leaf must
	// round-trip through pg_dump. This is the column-ATTRIBUTE sibling of slice
	// 264's table-level CHECK: where the CHECK appears as a separate constraint
	// item, a per-column DEFAULT must be re-attached to its column INSIDE the
	// leaf's column list. Critically, pg_dump prints the leaf's FULL inherited
	// column list either way — shouldPrintColumn (pg_dump.c:9970) returns true
	// for every column of a partition (`tbinfo->ispartition`), so the body is
	// NOT column-less (slice 264's comment that a leaf "prints no columns" is
	// inaccurate; real pg_dump 18.3 emits `a integer` for pchk_1 too). The
	// DEFAULT declared in the PARTITION OF column-override list (`(b DEFAULT
	// 42)`) is LOCAL to the leaf: execCreatePartitionChild records it on the
	// leaf's catalog.Column.DefaultExpr, so goopg's pg_attrdef emits the leaf's
	// adbin (atthasdef=true) and pg_get_expr renders `42`. Real pg_dump 18.3
	// then emits the leaf body `a integer, b integer DEFAULT 42` followed by the
	// LIST-bound ATTACH (verified byte-identical vs PG 18.3 and a fresh goopg
	// server). Every prior partition fixture exercised bounds, storage/AM
	// clauses, or a table-level CHECK, never a per-column override DEFAULT on a
	// leaf; this slice proves and guards that pg_attrdef path. No production
	// change — execCreatePartitionChild's column-override DEFAULT handling and
	// the pg_attrdef/pg_get_expr leaf path already exist.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdfl (a integer, b integer) PARTITION BY LIST (a)"); err != nil {
		t.Fatalf("create LIST-partitioned table pdfl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdfl_1 PARTITION OF public.pdfl (b DEFAULT 42) FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pdfl_1 with child-only DEFAULT: %v", err)
	}

	// Slice 266: child-only NOT NULL override on a partition leaf — the LAST
	// per-column override form (after CHECK in slice 264 and DEFAULT in slice
	// 265). The leaf `pnnl_1` of `pnnl (a integer, b integer)` carries `(b NOT
	// NULL)` in its PARTITION OF column-override list. As with the DEFAULT case,
	// shouldPrintColumn (pg_dump.c:9970) returns true for every partition column
	// (`tbinfo->ispartition`), so the leaf body is NOT column-less: real pg_dump
	// 18.3 emits the full body `a integer, b integer NOT NULL` followed by the
	// LIST-bound ATTACH (verified byte-identical vs PG 18.3 this loop). The NOT
	// NULL is LOCAL to the leaf: execCreatePartitionChild records it on the
	// leaf's catalog.Column.NotNull, and pg_dump's per-column attribute renderer
	// appends ` NOT NULL` inline. In PG18 a NOT NULL is also a named pg_constraint
	// (contype='n'), but for a partition leaf pg_dump still emits the inline
	// `NOT NULL` decoration on the column (not a separate CONSTRAINT clause), so
	// this guards the inline-NOT-NULL leaf path. No production change —
	// execCreatePartitionChild's column-override NOT NULL handling and the inline
	// pg_dump column decoration already exist.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pnnl (a integer, b integer) PARTITION BY LIST (a)"); err != nil {
		t.Fatalf("create LIST-partitioned table pnnl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pnnl_1 PARTITION OF public.pnnl (b NOT NULL) FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pnnl_1 with child-only NOT NULL: %v", err)
	}

	// Slice 281 (partition-leaf counterpart of the inherited NOT NULL body forms,
	// 271/277/279/280): a NOT NULL added to a partition leaf's INHERITED column via
	// `ALTER TABLE ADD CONSTRAINT` is routed to the INLINE column form, NOT the
	// standalone body form the legacy-inheritance `mninh`/`idfnd` siblings produce.
	// shouldPrintColumn (pg_dump.c:9970) returns `attislocal[j] || ispartition`, so
	// for a partition leaf EVERY column prints inline; the standalone-body branch
	// (`!shouldPrintColumn && notnull_islocal`, pg_dump.c:17213) is therefore NEVER
	// reached. Instead print_notnull (pg_dump.c:17116, true because `ispartition`)
	// renders the constraint as the inline decoration at pg_dump.c:17178-17183 —
	// `CONSTRAINT <name> NOT NULL` when the name is non-default, bare ` NOT NULL`
	// when it collapses. `pnna_1` adds TWO conislocal NOT NULLs on distinct inherited
	// columns: `qb` keeps a NON-default name (`pnna_named` != auto-name
	// `pnna_1_qb_not_null`) so its inline decoration is `qb integer CONSTRAINT
	// pnna_named NOT NULL`, while `qc`'s name EQUALS its auto-name
	// (`pnna_1_qc_not_null`) so it collapses to the bare `qc text NOT NULL`. This is
	// the partition twin of slice 280 (which proved the same per-column collapse on
	// the legacy-inheritance STANDALONE body path): here the SAME ALTER shape routes
	// INLINE because ispartition flips shouldPrintColumn. The partition key column
	// `qa` stays a plain `qa integer` (no NOT NULL). No production change — goopg
	// already exposes the conislocal NOT NULL pg_constraint rows + attnotnull for the
	// ALTER path (proven by mninh/idfnd) and reports the leaf as a partition (proven
	// by pnnl), so real pg_dump 18.3 renders the inline form. Verified byte-identical
	// vs PG 18.3 this loop. A regression that emitted the standalone body form for a
	// partition leaf (ignoring ispartition in shouldPrintColumn) would print
	// `CONSTRAINT pnna_named NOT NULL qb` after the column list; one that collapsed
	// globally would drop the `CONSTRAINT pnna_named` prefix on qb; one that lost
	// either AlterTableAddNotNull would drop an inline decoration.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pnna (qa integer, qb integer, qc text) PARTITION BY LIST (qa)"); err != nil {
		t.Fatalf("create LIST-partitioned table pnna: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pnna_1 PARTITION OF public.pnna FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pnna_1: %v", err)
	}
	// qb FIRST (non-default name → keeps inline CONSTRAINT prefix)...
	if err := runSQLSimple(t, c, "ALTER TABLE public.pnna_1 ADD CONSTRAINT pnna_named NOT NULL qb"); err != nil {
		t.Fatalf("add named NOT NULL on partition leaf inherited column pnna_1.qb: %v", err)
	}
	// ...then qc (default name → collapses to bare inline NOT NULL).
	if err := runSQLSimple(t, c, "ALTER TABLE public.pnna_1 ADD CONSTRAINT pnna_1_qc_not_null NOT NULL qc"); err != nil {
		t.Fatalf("add default-named NOT NULL on partition leaf inherited column pnna_1.qc: %v", err)
	}

	// Slice 282: a DEFAULT applied to a partition leaf's INHERITED column via
	// `ALTER TABLE <leaf> ALTER COLUMN <inherited-col> SET DEFAULT <expr>`. This is
	// the DEFAULT analog of slice 281 (which proved the NOT NULL ALTER path rides
	// INLINE on a partition leaf) and the partition-INLINE twin of slice 269 (which
	// proved the SAME ALTER shape on a LEGACY-inheritance child emits a STANDALONE
	// `ALTER TABLE ONLY ... ALTER COLUMN ... SET DEFAULT`). The discriminator is the
	// `attrdefs[].separate` flag (pg_dump.c:9507-9535): pg_dump marks a default
	// `separate` (→ standalone ALTER) only on the `!shouldPrintColumn` branch; for a
	// partition leaf shouldPrintColumn returns `attislocal[j] || ispartition`
	// (pg_dump.c:9964) → true for EVERY column, so `separate` stays false and the
	// DEFAULT rides INLINE on the already-printed column (`kb integer DEFAULT 7`).
	// The standalone `ALTER TABLE ONLY public.pdfa_1 ALTER COLUMN kb SET DEFAULT 7;`
	// form (slice 269's legacy shape) must therefore NEVER appear. The partition key
	// column `ka` stays a plain `ka integer`. NO production change — goopg already
	// records the ALTER-path DEFAULT on the child column (AlterTableSetDefault, added
	// for slice 269: Column.DefaultExpr → pg_attrdef + atthasdef) and reports the leaf
	// as a partition (proven by pnnl/pnna), so real pg_dump 18.3 renders the inline
	// form. Verified byte-identical vs PG 18.3 this loop. A regression that ignored
	// ispartition in shouldPrintColumn would suppress `kb` and emit the standalone
	// ALTER; one that lost AlterTableSetDefault would drop the `DEFAULT 7` decoration.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdfa (ka integer, kb integer) PARTITION BY LIST (ka)"); err != nil {
		t.Fatalf("create LIST-partitioned table pdfa: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pdfa_1 PARTITION OF public.pdfa FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pdfa_1: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.pdfa_1 ALTER COLUMN kb SET DEFAULT 7"); err != nil {
		t.Fatalf("set DEFAULT on partition leaf inherited column pdfa_1.kb: %v", err)
	}

	// Slice 283: a STORED GENERATED column inherited onto a partition leaf. The
	// parent pgna declares `gb` as `GENERATED ALWAYS AS (ga * 2) STORED`; the
	// partition leaf pgna_1 INHERITS the generated column (attislocal=false). This
	// exercises a DIFFERENT discriminator branch than slices 281/282: there the
	// default/NOT-NULL rode inline because shouldPrintColumn forced every partition
	// column to print; here the dominant force is attgenerated. pg_dump.c:9507
	// sets `attrdefs[].separate = false` UNCONDITIONALLY whenever
	// `tbinfo->attgenerated[adnum-1]` is non-empty — a generation expression can
	// NEVER be split into a standalone `ALTER TABLE ... ALTER COLUMN ... SET
	// DEFAULT` (that syntax cannot express a generated column). So even before
	// ispartition enters the picture, `separate` is false and the generation clause
	// must ride INLINE on the column. Layered on the partition leaf's ispartition=true
	// (shouldPrintColumn true for every column, slices 281/282), the leaf body prints
	// `gb integer GENERATED ALWAYS AS (ga * 2) STORED` inline. NO production change —
	// goopg already round-trips STORED generated columns on standalone tables (slice
	// 59: attgenerated='s' + a pg_attrdef row carrying the deparse) and inherits
	// parent columns onto partition leaves (slices 281/282). Verified byte-identical
	// vs PG 18.3. A regression that dropped the generation expression on the inherited
	// column would print a bare `gb integer`; one that mis-set `separate` would try to
	// emit an (illegal) standalone SET DEFAULT for a generated column.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgna (ga integer, gb integer GENERATED ALWAYS AS (ga * 2) STORED) PARTITION BY LIST (ga)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgna with generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgna_1 PARTITION OF public.pgna FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pgna_1: %v", err)
	}

	// Slice 284: the VIRTUAL counterpart of slice 283. The parent pvna declares
	// `vb` as `GENERATED ALWAYS AS (va * 2) VIRTUAL` (attgenerated='v', slice 194);
	// the partition leaf pvna_1 INHERITS the virtual generated column
	// (attislocal=false). This rides the SAME separate=false-via-attgenerated branch
	// as slice 283 — pg_dump.c:9507 sets `attrdefs[].separate = false` UNCONDITIONALLY
	// whenever `tbinfo->attgenerated[adnum-1]` is non-empty, and ATTRIBUTE_GENERATED_VIRTUAL
	// ('v') is non-empty exactly like ATTRIBUTE_GENERATED_STORED ('s') — but the
	// RENDER differs: pg_dump.c:17171 emits `GENERATED ALWAYS AS (%s)` with NO trailing
	// keyword for a virtual column (the STORED branch at 17168 is skipped). Layered on
	// the leaf's ispartition=true (shouldPrintColumn true for every column, slices
	// 281/282), the leaf body prints `vb integer GENERATED ALWAYS AS (va * 2)` inline,
	// bare. NO production change — goopg already round-trips VIRTUAL generated columns on
	// standalone tables (slice 194: attGeneratedFor reports 'v') and inherits parent
	// columns onto partition leaves (slices 281/282/283); the two facts compose. A
	// regression that mis-mapped attgenerated 'v'→'s' would print a spurious trailing
	// STORED; one that dropped the generation expression would print a bare `vb integer`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pvna (va integer, vb integer GENERATED ALWAYS AS (va * 2) VIRTUAL) PARTITION BY LIST (va)"); err != nil {
		t.Fatalf("create LIST-partitioned table pvna with virtual generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pvna_1 PARTITION OF public.pvna FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pvna_1: %v", err)
	}

	// Slice 285: a MULTI-ATTRIBUTE generation expression inherited onto a partition
	// leaf. Slices 283/284 proved a generated column whose expression references a
	// SINGLE other column (`ga * 2`); this slice proves the generation deparse
	// resolves TWO distinct inherited column references through the leaf. The parent
	// pgmc declares `gb` as `GENERATED ALWAYS AS (ga + gc) STORED` over two plain
	// columns `ga`, `gc`; the partition leaf pgmc_1 INHERITS all three (attislocal=false).
	// The render path is identical to slice 283 — attgenerated forces
	// attrdefs[].separate=false unconditionally (pg_dump.c:9507), ispartition forces
	// shouldPrintColumn true for every column (slices 281/282), so the leaf body prints
	// `gb integer GENERATED ALWAYS AS (ga + gc) STORED` inline — but the NEW fact under
	// test is the deparse of a binary expression over two Vars: each Var must resolve to
	// the correct inherited column NAME on the leaf (not an attnum-shifted or dropped
	// reference). A regression in the multi-Var generation deparse (e.g. one that resolved
	// only the first Var, or that swapped ga↔gc by attnum) would surface as a corrupted
	// generation clause here. NO production change — goopg already deparses multi-column
	// expressions for generated columns (slice 59 attgenerated='s' + pg_attrdef deparse)
	// and inherits parent columns onto partition leaves (slices 281–284); the two compose.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgmc (ga integer, gc integer, gb integer GENERATED ALWAYS AS (ga + gc) STORED) PARTITION BY LIST (ga)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgmc with multi-attr generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgmc_1 PARTITION OF public.pgmc FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pgmc_1: %v", err)
	}

	// Slice 286: a FORWARD-REFERENCE generation expression inherited onto a partition
	// leaf. Slice 285 proved a multi-attr generation expression where every referenced
	// column was declared BEFORE the generated column (ga=attnum1, gc=attnum2 →
	// gb=attnum3). This slice flips the declaration order: the generated column `gz` is
	// attnum 1 and references `ya` (attnum 2) and `yc` (attnum 3), both declared AFTER
	// it. The new fact under test is that the generation deparse resolves each Var by
	// column NAME (not by a positional/forward-only scan that would only see columns up
	// to the generated column's own attnum). PG places all table columns in scope for a
	// generation expression regardless of declaration order, so `(ya + yc)` is legal even
	// though both operands come later in the body. The render path is identical to slices
	// 283/285 — attgenerated forces attrdefs[].separate=false unconditionally
	// (pg_dump.c:9507), ispartition forces shouldPrintColumn true for every column (slices
	// 281/282) — so the leaf body prints columns in attnum order: the inline
	// `gz integer GENERATED ALWAYS AS (ya + yc) STORED` FIRST, then `ya integer`,
	// `yc integer`. A regression that resolved Vars positionally relative to the generated
	// column's attnum (a forward-only scan) would drop or corrupt the `(ya + yc)` clause
	// here, where neither operand precedes `gz`. NO production change — goopg resolves
	// generation-expression columns by name (evalGeneratedExpr over catalog.Column) and
	// inherits parent columns onto partition leaves (slices 281–285); the two compose.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgfr (gz integer GENERATED ALWAYS AS (ya + yc) STORED, ya integer, yc integer) PARTITION BY LIST (ya)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgfr with forward-reference generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgfr_1 PARTITION OF public.pgfr FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pgfr_1: %v", err)
	}

	// Slice 287: a generated column placed in the MIDDLE (attnum 2) whose single
	// expression resolves a BACKWARD Var (`ma`, attnum 1), a literal Const (`1`),
	// and a FORWARD Var (`mc`, attnum 3) — all in one deparse. Slice 285 referenced
	// only columns before the generated column; slice 286 referenced only columns
	// after it. This slice exercises BOTH directions plus a literal simultaneously:
	// `mg integer GENERATED ALWAYS AS (ma + 1 + mc) STORED` sits between `ma` and
	// `mc`. The partition leaf pgmx_1 INHERITS all three (attislocal=false), and the
	// same render path holds — attgenerated forces attrdefs[].separate=false
	// (pg_dump.c:9507), ispartition forces shouldPrintColumn true for every column
	// (slices 281/282) — so the leaf body prints in attnum order: `ma integer`,
	// then the inline generated `mg`, then `mc integer`. NO production change —
	// goopg stores the generation expression as verbatim source text and renders it
	// back through pg_get_expr (slices 283–286); pg_dump wraps it `(%s)`, so the
	// three-operand `ma + 1 + mc` prints flat with no nested parens. A regression
	// that resolved Vars by a forward-only positional scan would lose `ma` (declared
	// before `mg`); a backward-only scan would lose `mc` (declared after `mg`). Only
	// NAME-based resolution renders both operands of this mid-position column.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgmx (ma integer, mg integer GENERATED ALWAYS AS (ma + 1 + mc) STORED, mc integer) PARTITION BY LIST (ma)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgmx with mid-position mixed-direction generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgmx_1 PARTITION OF public.pgmx FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pgmx_1: %v", err)
	}

	// Slice 288: a generated column of TEXT type whose expression uses the string
	// concatenation operator `||` instead of integer arithmetic. Every prior
	// generation slice (283–287) used an integer column over `+`/`*` operators;
	// this one proves the inherited-leaf generation render path is BOTH type-
	// agnostic (the column is `text`, not `integer`) AND operator-agnostic (`||`,
	// not `+`). The render path keys only off attgenerated ('s') and the verbatim
	// pg_get_expr text — attGeneratedFor (pg18_user_catalog_rows.go:834) inspects
	// no type, and goopg's pg_get_expr is a pass-through of the stored expression
	// source, so `cc text GENERATED ALWAYS AS (ca || cb) STORED` round-trips by the
	// SAME mechanism as the integer slices. The `||` token joins flat (no parens,
	// no function call), so the deparse stays faithful: pg_dump wraps it `(%s)` →
	// `(ca || cb)`. The partition leaf pgcc_1 INHERITS all three columns
	// (attislocal=false); attgenerated forces attrdefs[].separate=false
	// (pg_dump.c:9507) and ispartition forces shouldPrintColumn true for every
	// column (slices 281/282), so the leaf body prints in attnum order: `ca text`,
	// `cb text`, then the inline generated `cc`. A regression that special-cased
	// integer generated columns, or one that lost a non-arithmetic operator in the
	// deparse, would drop or corrupt `cc` here.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgcc (ca text, cb text, cc text GENERATED ALWAYS AS (ca || cb) STORED) PARTITION BY LIST (ca)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgcc with text concatenation generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgcc_1 PARTITION OF public.pgcc FOR VALUES IN ('x')"); err != nil {
		t.Fatalf("create partition leaf pgcc_1: %v", err)
	}

	// Slice 289: a generation expression with PRECEDENCE-GROUPING PARENS
	// (`(fa + fb) * 2`) inherited onto a partition leaf. Every prior generation
	// slice (283–288) used a FLAT operator chain (`ga * 2`, `ga + gc`, `ca || cb`)
	// whose captured tokens space-join faithfully. A parenthesised expression
	// exposed a deparse defect: goopg captured the GENERATED expression as raw
	// tokens and joined them with single spaces, so `(fa + fb) * 2` became
	// `( fa + fb ) * 2` — and pg_dump wrapped that to `(( fa + fb ) * 2)`, which
	// diverges from real pg_dump's `((fa + fb) * 2)` (pg_get_expr renders the
	// precedence paren tightly). This slice carries the PRODUCTION fix:
	// joinGeneratedExprTokens (parser/ddl.go) reconstructs pg_get_expr's spacing —
	// tight grouping parens and tight function calls, spaced binary operators — so
	// the stored generation source now matches pg_get_expr verbatim. The render
	// path is otherwise identical to slices 283–288: attgenerated ('s') forces
	// attrdefs[].separate=false (pg_dump.c:9507) and ispartition forces
	// shouldPrintColumn for every column (slices 281/282), so the leaf pgpp_1
	// inherits all three columns and prints `fa integer`, `fb integer`, then the
	// inline `fc integer GENERATED ALWAYS AS ((fa + fb) * 2) STORED`. Before the
	// fix this slice failed against the real-pg_dump oracle with the spurious
	// inner spaces.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgpp (fa integer, fb integer, fc integer GENERATED ALWAYS AS ((fa + fb) * 2) STORED) PARTITION BY LIST (fa)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgpp with parenthesised generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgpp_1 PARTITION OF public.pgpp FOR VALUES IN (1)"); err != nil {
		t.Fatalf("create partition leaf pgpp_1: %v", err)
	}

	// Slice 290: a generation expression that is a FUNCTION CALL (`upper(fn)`)
	// inherited onto a partition leaf. Every prior generation slice (283–289) used
	// only operators — flat arithmetic (`ga * 2`, `ga + gc`), string concat
	// (`ca || cb`), or precedence-grouped arithmetic (`(fa + fb) * 2`). This is the
	// FIRST slice whose generation body is a function invocation, exercising the
	// joinGeneratedExprTokens call-paren branch end-to-end: the helper renders the
	// call parens TIGHT (`upper(fn)`, not `upper ( fn )`) so the stored source
	// matches pg_get_expr. The render path is otherwise identical to slices 283–289
	// — attgenerated ('s') forces attrdefs[].separate=false (pg_dump.c:9507) and
	// ispartition forces shouldPrintColumn for every column (slices 281/282), so the
	// leaf pgfx_1 inherits both columns and prints `fn text`, then the inline
	// generated `fu text GENERATED ALWAYS AS (upper(fn)) STORED`. No rows are
	// inserted, so this rides the dump-time deparse path only (materialization of
	// upper() is not exercised here).
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgfx (fn text, fu text GENERATED ALWAYS AS (upper(fn)) STORED) PARTITION BY LIST (fn)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgfx with function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgfx_1 PARTITION OF public.pgfx FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pgfx_1: %v", err)
	}

	// Slice 291: a generation expression that is a TWO-ARGUMENT function call
	// (`coalesce(cn, dn)`) inherited onto a partition leaf. Slice 290 proved the
	// single-argument call-paren branch of joinGeneratedExprTokens end-to-end
	// (`upper(fn)`); this slice pins the `, `-separated ARGUMENT-LIST branch on the
	// oracle: the helper must render the comma TIGHT to its left operand and SPACED
	// to its right (`coalesce(cn, dn)`, not `coalesce(cn ,dn)` or `coalesce(cn,dn)`)
	// so the stored source byte-matches what pg_get_expr returns. goopg's source is
	// what real pg_dump reads back (this test dumps a live goopg server), so the
	// lowercase `coalesce` is preserved verbatim — there is no real-PG pg_get_expr
	// case normalization in this path. The render path is otherwise identical to
	// slices 281–290: attgenerated ('s') forces attrdefs[].separate=false
	// (pg_dump.c:9507) and ispartition forces shouldPrintColumn for every column, so
	// the leaf pgcl_1 inherits both columns and prints `cn text`, `dn text`, then the
	// inline generated `en text GENERATED ALWAYS AS (coalesce(cn, dn)) STORED`. No
	// rows are inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgcl (cn text, dn text, en text GENERATED ALWAYS AS (coalesce(cn, dn)) STORED) PARTITION BY LIST (cn)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgcl with two-arg function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgcl_1 PARTITION OF public.pgcl FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pgcl_1: %v", err)
	}

	// Slice 292: a NESTED function-call generation expression
	// (`upper(coalesce(gn, hn))`) inherited onto a partition leaf. Slices 290/291
	// pinned the single- and two-argument call-paren branches of
	// joinGeneratedExprTokens at one nesting level; this slice pins their
	// COMPOSITION — a call whose argument is itself a call — proving the helper
	// keeps BOTH call parens tight while spacing the inner argument comma:
	// `upper(coalesce(gn, hn))`, not `upper ( coalesce ( gn ,hn ) )` or any
	// intermediate. The token walk relies on the `(`-after-ident rule firing twice
	// (once for `upper(`, once for the inner `coalesce(`) and the `)`-is-always-tight
	// rule firing twice at the tail, so a regression that special-cased only a
	// single paren depth would corrupt the inner or outer call. goopg's stored
	// source is what real pg_dump reads back (this test dumps a live goopg server),
	// so both lowercase function names are preserved verbatim — no real-PG
	// pg_get_expr case normalization in this path. The render path is otherwise
	// identical to slices 281–291: attgenerated ('s') forces attrdefs[].separate=false
	// (pg_dump.c:9507) and ispartition forces shouldPrintColumn for every column, so
	// the leaf pgnc_1 inherits both columns and prints `gn text`, `hn text`, then the
	// inline generated `jn text GENERATED ALWAYS AS (upper(coalesce(gn, hn))) STORED`.
	// No rows are inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgnc (gn text, hn text, jn text GENERATED ALWAYS AS (upper(coalesce(gn, hn))) STORED) PARTITION BY LIST (gn)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgnc with nested function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgnc_1 PARTITION OF public.pgnc FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pgnc_1: %v", err)
	}

	// Slice 293: a THREE-argument function-call generation expression
	// (`concat(ka, la, ma)`) inherited onto a partition leaf. Slice 291 pinned
	// the two-argument call-paren branch of joinGeneratedExprTokens (one comma in
	// the argument list); this slice extends that to a REPEATED-comma argument
	// list — `concat(ka, la, ma)` exercises the comma-spacing rule firing twice
	// inside a single call, proving the helper emits `, ` between every adjacent
	// argument pair (`concat(ka, la, ma)`, not `concat(ka, la,ma)` or
	// `concat(ka ,la ,ma)`) while keeping the single call paren tight. The
	// argument count is the only thing that varies from slice 291, so a
	// regression that hard-coded the two-token argument list would corrupt the
	// third argument's separator. goopg's stored source is what real pg_dump
	// reads back (this test dumps a live goopg server), so the lowercase function
	// name is preserved verbatim — no real-PG pg_get_expr case normalization in
	// this path. The render path is otherwise identical to slices 281–292:
	// attgenerated ('s') forces attrdefs[].separate=false (pg_dump.c:9507) and
	// ispartition forces shouldPrintColumn for every column, so the leaf pg3c_1
	// inherits all three plain columns and prints `ka text`, `la text`, `ma text`,
	// then the inline generated `na text GENERATED ALWAYS AS (concat(ka, la, ma)) STORED`.
	// No rows are inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pg3c (ka text, la text, ma text, na text GENERATED ALWAYS AS (concat(ka, la, ma)) STORED) PARTITION BY LIST (ka)"); err != nil {
		t.Fatalf("create LIST-partitioned table pg3c with three-arg function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pg3c_1 PARTITION OF public.pg3c FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pg3c_1: %v", err)
	}

	// Slice 294 (PRODUCTION fix): a function-call generation expression with a
	// STRING-LITERAL argument (`concat(ka, '-', la)`) inherited onto a partition
	// leaf. Slices 291–293 pinned the call-paren / comma-spacing branches of
	// joinGeneratedExprTokens for IDENTIFIER arguments only. A string-literal
	// argument exposed a latent bug: the lexer stores a literal's UNQUOTED body
	// (`'-'` → Token.Value "-"), and the helper space-joined token values raw, so
	// `concat(ka, '-', la)` would have round-tripped as the MALFORMED
	// `concat(ka, -, la)` (the quotes dropped, the literal indistinguishable from
	// a minus operator). The fix re-quotes TokenStringLit tokens (doubling any
	// embedded single quote) and gates the punctuation spacing rules on
	// TokenSymbol so a literal body of ")"/","/"("/"." can't be mistaken for a
	// punctuator. Because this test dumps a LIVE goopg server, real pg_dump reads
	// goopg's stored generation source verbatim — so the assertion pins goopg's
	// own canonical rendering `concat(ka, '-', la)` (goopg does not add the
	// `::text` cast that real PG's pg_get_expr would inject; that divergence is
	// out of scope, like the lowercase-function-name divergence of slices 290–293).
	// Render path is otherwise identical to slices 281–293: attgenerated ('s')
	// forces attrdefs[].separate=false (pg_dump.c:9507) and ispartition forces
	// shouldPrintColumn for every column, so the leaf pglc_1 inherits both plain
	// columns and prints `ka text`, `la text`, then the inline generated
	// `na text GENERATED ALWAYS AS (concat(ka, '-', la)) STORED`. No rows are
	// inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pglc (ka text, la text, na text GENERATED ALWAYS AS (concat(ka, '-', la)) STORED) PARTITION BY LIST (ka)"); err != nil {
		t.Fatalf("create LIST-partitioned table pglc with string-literal-argument function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pglc_1 PARTITION OF public.pglc FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pglc_1: %v", err)
	}

	// Slice 295: a function-call generation expression whose string-literal
	// argument's BODY IS A COMMA (`concat(ka, ',', la)`) inherited onto a
	// partition leaf — the adversarial complement to slice 294. Here the literal
	// `','` directly COLLIDES with the argument-separator comma. Slice 294's fix
	// gates the punctuation spacing rules on TokenSymbol; this fixture exercises
	// that gating on the ORACLE path. The pre-slice-294 Value-based switch would
	// have matched the literal token's `,` value against the separator `,` case
	// (noSpace) AND dropped its quotes, collapsing the three commas into the
	// malformed `concat(ka,,,la)`. With the fix, the TokenStringLit literal is
	// skipped by the symbol-only switch and re-quoted, so it renders distinctly
	// as `concat(ka, ',', la)`. Because this test dumps a LIVE goopg server, real
	// pg_dump reads goopg's stored source verbatim, pinning goopg's own canonical
	// rendering (no `::text` cast — that pg_get_expr divergence is out of scope,
	// like slices 290–294). Render path is otherwise identical to slice 294:
	// attgenerated ('s') forces attrdefs[].separate=false (pg_dump.c:9507) and
	// ispartition forces shouldPrintColumn for every column, so the leaf pgkc_1
	// inherits both plain columns and prints `ka text`, `la text`, then the
	// inline generated `na text GENERATED ALWAYS AS (concat(ka, ',', la)) STORED`.
	// No rows are inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgkc (ka text, la text, na text GENERATED ALWAYS AS (concat(ka, ',', la)) STORED) PARTITION BY LIST (ka)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgkc with comma-literal-argument function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgkc_1 PARTITION OF public.pgkc FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pgkc_1: %v", err)
	}

	// Slice 296: a function-call generation expression whose string-literal
	// argument's BODY IS A SINGLE QUOTE (`concat(ka, '''', la)`) inherited onto a
	// partition leaf — the adversarial complement to slices 294 (body `-`) and 295
	// (body `,`). This is the only fixture that exercises slice 294's quote-DOUBLING
	// (`strings.ReplaceAll(t.Value, "'", "''")` in joinGeneratedExprTokens.renderTok)
	// on the ORACLE path. The lexer stores a literal's UNQUOTED, un-escaped body, so
	// the SQL literal `''''` (four quotes = a literal containing one `'`) is stored
	// as the single byte `'`. The pre-slice-294 helper space-joined that raw byte
	// into the malformed `concat(ka, ', la)` (the lone `'` opening a phantom string
	// that swallows the rest of the expression); a fix that re-quoted but FORGOT to
	// double the embedded quote would emit `concat(ka, ''', la)` (three quotes —
	// unbalanced). The fix re-quotes AND doubles, so the literal renders as the
	// balanced four-quote `''''`. Because this test dumps a LIVE goopg server, real
	// pg_dump reads goopg's stored source verbatim, pinning goopg's own canonical
	// rendering (no `::text` cast — that pg_get_expr divergence is out of scope, like
	// slices 290–295). Render path is otherwise identical to slice 295: attgenerated
	// ('s') forces attrdefs[].separate=false (pg_dump.c:9507) and ispartition forces
	// shouldPrintColumn for every column, so the leaf pgqc_1 inherits both plain
	// columns and prints `ka text`, `la text`, then the inline generated
	// `na text GENERATED ALWAYS AS (concat(ka, '''', la)) STORED`. No rows are
	// inserted, so this rides the dump-time deparse path only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgqc (ka text, la text, na text GENERATED ALWAYS AS (concat(ka, '''', la)) STORED) PARTITION BY LIST (ka)"); err != nil {
		t.Fatalf("create LIST-partitioned table pgqc with embedded-quote-literal-argument function-call generated column: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.pgqc_1 PARTITION OF public.pgqc FOR VALUES IN ('a')"); err != nil {
		t.Fatalf("create partition leaf pgqc_1: %v", err)
	}

	// Slice 267: a LOCAL CHECK constraint on a LEGACY (non-partition) INHERITS
	// child must round-trip. Slices 264–266 covered the per-child override forms
	// on a PARTITION leaf, where `tbinfo->ispartition` forces shouldPrintColumn
	// (pg_dump.c:9970) to print EVERY column. A legacy INHERITS child is the
	// opposite regime: ispartition is false, so shouldPrintColumn gates on
	// attislocal ALONE — the inherited columns (`pid`, `pname`, attislocal=false)
	// are OMITTED while the child's own local column (`extra`, attislocal=true)
	// prints. Layered on top, a CHECK declared in the child's CREATE TABLE
	// (`CONSTRAINT ichk_child_pos CHECK (extra > 0)`) is conislocal='t': pg_dump
	// emits it INSIDE the child's body alongside the local column, then the
	// `INHERITS (public.ichk_parent)` clause (NOT an ATTACH — legacy inheritance,
	// not a partition). Slice 170 proved column-omission + the INHERITS clause for
	// a plain child; slice 264 proved a conislocal CHECK on a partition leaf; this
	// slice proves their INTERSECTION — a conislocal CHECK on a column-omitting
	// legacy child — which neither prior fixture exercised. Real pg_dump 18.3 emits
	// the body `extra integer, CONSTRAINT ichk_child_pos CHECK ((extra > 0))`
	// followed by `INHERITS (public.ichk_parent)` (verified byte-identical this
	// loop). No production change — the conislocal CHECK path (operators_ddl.go
	// AddCheck → pg_constraint VirtualRows) and the legacy-inheritance column
	// omission (Table.InheritsParentOIDs + Column.Inherited) already exist.
	if err := runSQLSimple(t, c, "CREATE TABLE public.ichk_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent ichk_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.ichk_child (extra integer, CONSTRAINT ichk_child_pos CHECK (extra > 0)) INHERITS (public.ichk_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child ichk_child with local CHECK: %v", err)
	}

	// Slice 268: a LOCAL column DEFAULT on a LEGACY (non-partition) INHERITS child
	// must round-trip. This is the pg_attrdef sibling of slice 267's table-level
	// CHECK: instead of a conislocal CHECK, the child's own local column carries an
	// attrdef (`extra integer DEFAULT 42`). The same column-omission regime applies
	// — `idfl_child`'s inherited `pid`/`pname` (attislocal=false) are dropped while
	// its local `extra` (attislocal=true) prints — but the new wrinkle is that the
	// DEFAULT must ride INLINE on that local column. Slice 265 proved a child-only
	// DEFAULT on a PARTITION leaf (ispartition forces every column to print, so the
	// DEFAULT rode an already-printed column); this slice proves the DEFAULT still
	// rides correctly when the column is printed BECAUSE OF attislocal, not despite
	// it — the legacy-inheritance code path that slices 170/267 exercise. Real
	// pg_dump 18.3 emits the body `extra integer DEFAULT 42` followed by
	// `INHERITS (public.idfl_parent)` (verified byte-identical this loop). No
	// production change — the local-column attrdef path (operators_ddl.go column
	// DEFAULT → pg_attrdef) and the legacy-inheritance column omission
	// (Table.InheritsParentOIDs + Column.Inherited) already exist.
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfl_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent idfl_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfl_child (extra integer DEFAULT 42) INHERITS (public.idfl_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child idfl_child with local DEFAULT: %v", err)
	}

	// Slice 269: a child-level DEFAULT applied to an INHERITED column via
	// `ALTER TABLE child ALTER COLUMN <inherited-col> SET DEFAULT <expr>`.
	// Slices 265/268 rode the DEFAULT INLINE on a column that pg_dump prints;
	// this slice exercises the OPPOSITE — a DEFAULT on an inherited column that
	// pg_dump SUPPRESSES from the child's column list (attislocal=false). Because
	// the column is not printed, pg_dump cannot ride the DEFAULT inline; instead
	// pg_dump.c marks `attrdefs[].separate` (the `!shouldPrintColumn` branch,
	// pg_dump.c:9527) and emits it as a STANDALONE
	// `ALTER TABLE ONLY public.idfa_child ALTER COLUMN pid SET DEFAULT 7;`
	// (dumpAttrDef, pg_dump.c:18028). This required NEW production support:
	// `ALTER TABLE ... ALTER COLUMN ... SET DEFAULT` was previously swallowed as
	// a no-op by the parser. The new AlterTableSetDefault action records the
	// parsed expr on the child's catalog column (Column.DefaultExpr), which feeds
	// both pg_attrdef (catalog attrDefRowsLocked) and the pg_attribute heap
	// atthasdef flag (flushed via the same delete-old-rows + syncTableToCatalogHeap
	// path SET STORAGE/COMPRESSION use). idfa_child keeps a purely-local column
	// (`extra`) so its CREATE TABLE body is non-empty and the inherited
	// `pid`/`pname` are still omitted (arriving via INHERITS).
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfa_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent idfa_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfa_child (extra integer) INHERITS (public.idfa_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child idfa_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.idfa_child ALTER COLUMN pid SET DEFAULT 7"); err != nil {
		t.Fatalf("set DEFAULT on inherited column idfa_child.pid: %v", err)
	}

	// Slice 270: child-level `SET NOT NULL` on an INHERITED column of a legacy
	// INHERITS child. This is the NOT NULL twin of slice 269's DEFAULT case, but
	// pg_dump takes a DIFFERENT catalog path: PG18 records NOT NULL as a
	// pg_constraint row (contype='n'), and pg_dump's getTableAttrs LEFT-JOINs
	// pg_constraint to populate notnull_constrs/notnull_islocal. Because the
	// inherited column is suppressed from the child's column list
	// (!shouldPrintColumn) yet carries a LOCAL NOT NULL constraint
	// (conislocal='t'), pg_dump emits it as a STANDALONE `NOT NULL <col>`
	// constraint item INSIDE the CREATE TABLE body (pg_dump.c:17213-17232) —
	// NOT as a separate ALTER (the way DEFAULT is dumped). The auto-name matches
	// PG's `<table>_<col>_not_null`, so notnull_constrs is the unnamed "" form.
	// This required NEW production support: `ALTER TABLE ... ALTER COLUMN ...
	// SET NOT NULL` was previously swallowed as a no-op by the parser. The new
	// AlterTableSetNotNull action sets Column.NotNull AND records a contype='n'
	// constraint (catalog.Table.AddNotNull, conislocal=true, coninhcount=0),
	// flushing pg_attribute.attnotnull via the same delete-old-rows +
	// syncTableToCatalogHeap path. idfn_child keeps a purely-local column
	// (`extra`) so its body is non-empty; inherited `pid`/`pname` arrive via
	// INHERITS, with `NOT NULL pid` emitted in the body in attnum order.
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfn_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent idfn_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfn_child (extra integer) INHERITS (public.idfn_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child idfn_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.idfn_child ALTER COLUMN pid SET NOT NULL"); err != nil {
		t.Fatalf("set NOT NULL on inherited column idfn_child.pid: %v", err)
	}

	// Slice 271: a *named* NOT NULL on an inherited column via PG18's
	// `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>`. This is the named
	// counterpart of slice 270's unnamed `""` path. pg_dump reads the
	// constraint's conname (notnull_name) and compares it against the computed
	// default `<table>_<col>_not_null`; because `idfnn_nn` differs, notnull_constrs
	// carries the real name and pg_dump prints `CONSTRAINT idfnn_nn NOT NULL pid`
	// — the named body form (pg_dump.c:17228) — rather than the unnamed
	// `NOT NULL pid`. New production support: the ADD CONSTRAINT NOT NULL parser
	// branch (previously the column-level `NOT NULL` was only parsed inline) plus
	// the AlterTableAddNotNull executor action, which records a contype='n'
	// constraint with the EXPLICIT name (conislocal=true, coninhcount=0) and
	// flushes pg_attribute.attnotnull via the same delete-old-rows +
	// syncTableToCatalogHeap path as SET NOT NULL. idfnn_child keeps a local
	// `extra` column so its body is non-empty; the inherited `pid`/`pname` arrive
	// via INHERITS, with `CONSTRAINT idfnn_nn NOT NULL pid` emitted in the body.
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfnn_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent idfnn_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfnn_child (extra integer) INHERITS (public.idfnn_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child idfnn_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.idfnn_child ADD CONSTRAINT idfnn_nn NOT NULL pid"); err != nil {
		t.Fatalf("add named NOT NULL on inherited column idfnn_child.pid: %v", err)
	}

	// Slice 272: a `NO INHERIT` NOT NULL on a STANDALONE (non-inherited) table,
	// dumped INLINE as `<col> <type> NOT NULL NO INHERIT`. Slices 270/271 covered
	// NOT NULL on *inherited* columns (standalone body items); this exercises the
	// `connoinherit='t'` rendering on a plain local column. PG18 records the NOT
	// NULL as a contype='n' pg_constraint row with connoinherit='t'; pg_dump reads
	// it as notnull_noinh[j] and appends ` NO INHERIT` after the inline `NOT NULL`
	// (pg_dump.c:17188). Because the column is local (notnull_islocal='t') and the
	// constraint name equals the computed default `nninh_c_not_null`, pg_dump emits
	// the UNNAMED inline form `c integer NOT NULL NO INHERIT` (no CONSTRAINT prefix).
	// The whole production path already existed — the inline parser consumes the
	// `NO INHERIT` trailer into ColumnDef.NotNullNoInherit (ddl.go), the CREATE
	// TABLE executor threads it through `AddNotNull(..., noInherit, isLocal=true)`
	// (operators_ddl.go), and the pg_constraint virtual builder renders
	// connoinherit from NamedNotNullConstraint.NoInherit (catalog.go) — but no dump
	// path had asserted it. Verified byte-for-byte against real pg_dump 18.3:
	// `c integer NOT NULL NO INHERIT,\n    d integer`. A regression that dropped the
	// NoInherit thread would emit a plain `c integer NOT NULL` (connoinherit='f').
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh (c integer NOT NULL NO INHERIT, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh with NOT NULL NO INHERIT column: %v", err)
	}

	// Slice 273: a NAMED inline NOT NULL whose name differs from PG's auto-name,
	// dumped INLINE as `<col> <type> CONSTRAINT <name> NOT NULL [NO INHERIT]`.
	// Slice 272 covered the UNNAMED inline form (name == default → bare `NOT
	// NULL`); here the explicit `CONSTRAINT c_nn` name (≠ `nninh2_c_not_null`)
	// forces pg_dump to re-emit the `CONSTRAINT <name>` prefix (pg_dump.c:17184),
	// followed by ` NO INHERIT` (connoinherit='t'). Before this slice goopg's
	// inline-CONSTRAINT parser arm had no NOT NULL case, so `CONSTRAINT c_nn NOT
	// NULL` was silently dropped (column dumped as a plain `c integer`). The fix
	// captures the name into ColumnDef.NotNullConstraintName (ddl.go) and the
	// executor threads it onto AddNotNull (operators_ddl.go) so the pg_constraint
	// virtual row carries the user-given conname; pg_dump's getTableAttrs query
	// then reports a non-default notnull name. Second column `e` carries a named
	// NOT NULL WITHOUT NO INHERIT (`CONSTRAINT e_nn`) to assert the suffix is not
	// spuriously added. Verified against real pg_dump 18.3:
	// `c integer CONSTRAINT c_nn NOT NULL NO INHERIT,\n    e integer CONSTRAINT e_nn NOT NULL`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh2 (c integer CONSTRAINT c_nn NOT NULL NO INHERIT, e integer CONSTRAINT e_nn NOT NULL)"); err != nil {
		t.Fatalf("create standalone table nninh2 with named NOT NULL columns: %v", err)
	}

	// Slice 274: a NAMED inline NOT NULL whose name EQUALS PG's computed default
	// `<table>_<col>_not_null` must COLLAPSE back to the bare `NOT NULL` form —
	// pg_dump only emits the `CONSTRAINT <name>` prefix when the conname differs
	// from the default (pg_dump.c:17184 ChooseConstraintName match). Slice 273
	// covered the DIFFERING-name case (prefix re-emitted); this is the boundary
	// twin: the user spells out the exact auto-name `nninh3_c_not_null`, so even
	// though goopg records it as an EXPLICIT name on AddNotNull, pg_dump's
	// default-name comparison finds them equal and drops the prefix. A regression
	// that unconditionally emitted the `CONSTRAINT` prefix whenever an explicit
	// name was given (instead of letting pg_dump's default match decide) would
	// leak `CONSTRAINT nninh3_c_not_null` into the dump. Verified against real
	// pg_dump 18.3: `c integer NOT NULL` (bare, no CONSTRAINT prefix).
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh3 (c integer CONSTRAINT nninh3_c_not_null NOT NULL, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh3 with default-named NOT NULL column: %v", err)
	}

	// Slice 275: a NAMED `NO INHERIT` NOT NULL added to a LOCAL column via
	// `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO INHERIT`, dumped
	// INLINE as `<col> <type> CONSTRAINT <name> NOT NULL NO INHERIT`. This is the
	// ALTER-path counterpart of slice 273/274's CREATE-TABLE-inline cases: nninh2
	// proved the inline form when the constraint is spelled at table-creation
	// time; here the SAME inline rendering must result when the named NO INHERIT
	// constraint arrives AFTER the fact through the AlterTableAddNotNull executor.
	// It combines slice 271's ADD CONSTRAINT NOT NULL parser/executor branch with
	// slice 272's NO INHERIT thread on a STANDALONE (non-inherited) table: the
	// parser captures the `NO INHERIT` trailer into AlterTableAction.NoInherit
	// (ddl.go:5483) and the executor records a contype='n' pg_constraint row with
	// connoinherit='t' via tbl.AddNotNull(name, col, oid, act.NoInherit=true,
	// isLocal=true, 0) (operators_ddl.go:5498), then flushes attnotnull through
	// the delete-old-rows + syncTableToCatalogHeap path. Because the column is
	// LOCAL (notnull_islocal='t') and the name `nn4` differs from the auto-name
	// `nninh4_c_not_null`, pg_dump re-emits the `CONSTRAINT nn4` prefix and the
	// ` NO INHERIT` suffix on the INLINE column (pg_dump.c:17184/17188), exactly
	// like nninh2's `c`. A regression that dropped act.NoInherit on the ALTER path
	// (while the CREATE-inline path kept it) would dump a plain
	// `c integer CONSTRAINT nn4 NOT NULL` here — the silent-twin failure mode the
	// "sibling paths must agree" rule guards against. Verified to match real
	// pg_dump 18.3's rendering of pg_constraint (which is creation-method-agnostic;
	// nninh2 anchors the byte form).
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh4 (c integer, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh4: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nninh4 ADD CONSTRAINT nn4 NOT NULL c NO INHERIT"); err != nil {
		t.Fatalf("add named NO INHERIT NOT NULL on local column nninh4.c: %v", err)
	}

	// Slice 276 (negative twin of slice 275): a NAMED NOT NULL added to a LOCAL
	// column via `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col>` — WITHOUT
	// the `NO INHERIT` trailer — must dump INLINE as `<col> <type> CONSTRAINT
	// <name> NOT NULL` and must NOT grow a spurious ` NO INHERIT` suffix. Slice
	// 275 proved the ALTER path THREADS act.NoInherit when present; this twin
	// proves it does not FABRICATE it when absent. The parser leaves
	// AlterTableAction.NoInherit=false (no `NO INHERIT` trailer at ddl.go:5483),
	// the executor records a contype='n' row with connoinherit='f' via
	// tbl.AddNotNull(name, col, oid, false, isLocal=true, 0) (operators_ddl.go:5498),
	// and pg_dump (pg_dump.c:17184/17188) emits the `CONSTRAINT nn5` prefix
	// (LOCAL + name `nn5` ≠ auto-name `nninh5_c_not_null`) but NO suffix. A
	// regression that defaulted connoinherit to 't' on the ALTER path, or that
	// echoed a stray NO INHERIT, would emit `c integer CONSTRAINT nn5 NOT NULL NO
	// INHERIT` — the exact byte form slice 275 wants but which is WRONG here. This
	// is the inline-rendered ALTER-path counterpart of slice 273's `nninh2.e`
	// (`e integer CONSTRAINT e_nn NOT NULL`), which arrived at table-creation time.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh5 (c integer, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh5: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nninh5 ADD CONSTRAINT nn5 NOT NULL c"); err != nil {
		t.Fatalf("add named NOT NULL on local column nninh5.c: %v", err)
	}

	// Slice 277 (ALTER-path counterpart of slice 274's CREATE-inline nninh3):
	// a NAMED NOT NULL added to a LOCAL column via `ALTER TABLE ... ADD CONSTRAINT
	// <name> NOT NULL <col>` whose explicit name EQUALS the auto-name
	// `<table>_<col>_not_null` must COLLAPSE to the bare `<col> <type> NOT NULL`
	// form — pg_dump must NOT leak the `CONSTRAINT nninh6_c_not_null` prefix even
	// though the constraint was created with an explicit name. Slice 274 proved
	// this collapse for the inline-at-creation path; this twin proves the ALTER
	// path stores the same `conname` (so pg_dump's auto-name suppression at
	// pg_dump.c:17184 — which fires when conname == the computed default — also
	// applies). A regression that stored a different conname, or that skipped the
	// auto-name comparison on the ALTER path, would leak `c integer CONSTRAINT
	// nninh6_c_not_null NOT NULL` into the dump.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh6 (c integer, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh6: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nninh6 ADD CONSTRAINT nninh6_c_not_null NOT NULL c"); err != nil {
		t.Fatalf("add default-named NOT NULL on local column nninh6.c: %v", err)
	}

	// Slice 278 (combines slice 277's auto-name collapse with slice 275's NO
	// INHERIT suffix): a NAMED `NO INHERIT` NOT NULL added to a LOCAL column via
	// `ALTER TABLE ... ADD CONSTRAINT <name> NOT NULL <col> NO INHERIT` whose
	// explicit name EQUALS the auto-name `nninh7_c_not_null` must COLLAPSE the
	// `CONSTRAINT` prefix (conname == computed default, suppressed at
	// pg_dump.c:17184) while the `NO INHERIT` suffix SURVIVES — yielding the bare
	// `c integer NOT NULL NO INHERIT` form. pg_dump renders the column constraint
	// in two independent steps (pg_dump.c:17179-17188): the name-vs-default
	// decision picks bare ` NOT NULL`, then `notnull_noinh[j]` appends ` NO
	// INHERIT`. This twin proves the ALTER path threads BOTH the collapsible
	// conname AND the NO INHERIT bit together: a regression dropping the noinh bit
	// would emit `c integer NOT NULL` (missing suffix); one storing a non-default
	// conname would leak `c integer CONSTRAINT nninh7_c_not_null NOT NULL NO
	// INHERIT`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nninh7 (c integer, d integer)"); err != nil {
		t.Fatalf("create standalone table nninh7: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nninh7 ADD CONSTRAINT nninh7_c_not_null NOT NULL c NO INHERIT"); err != nil {
		t.Fatalf("add default-named NO INHERIT NOT NULL on local column nninh7.c: %v", err)
	}

	// Slice 279 (inherited-child counterpart of slice 277): a DEFAULT-named NOT
	// NULL added to an INHERITED column via `ALTER TABLE ... ADD CONSTRAINT
	// <child>_<col>_not_null NOT NULL <inherited_col>` whose explicit name EQUALS
	// the auto-name must COLLAPSE the `CONSTRAINT` prefix in the STANDALONE body
	// form — pg_dump emits the bare `NOT NULL pid` (no `CONSTRAINT` prefix) rather
	// than `CONSTRAINT idfnd_child_pid_not_null NOT NULL pid`. Slice 271 proved the
	// NAMED (non-default) body form for a conislocal NOT NULL on an inherited
	// column (`CONSTRAINT idfnn_nn NOT NULL pid`); slice 277 proved the auto-name
	// COLLAPSE for a LOCAL inline column on the ALTER path. This twin proves their
	// INTERSECTION: the collapse also fires in the inherited-column body branch
	// (pg_dump.c:17225-17232), which emits `NOT NULL <col>` when notnull_constrs[j]
	// is empty (conname == computed default) and `CONSTRAINT <name> NOT NULL <col>`
	// otherwise. No production change required — the standalone body form reuses the
	// SAME notnull_constrs[] array as the inline path, so once goopg's ALTER path
	// stores the collapsible default conname (slice 277) and getTableAttrs suppresses
	// it, the inherited body form collapses identically. Note the body branch never
	// appends ` NO INHERIT` (that is inline-only, pg_dump.c:17187), so this slice
	// deliberately omits the NO INHERIT dimension. A regression storing a non-default
	// conname (or skipping the default-name comparison) would leak
	// `CONSTRAINT idfnd_child_pid_not_null NOT NULL pid`; one that lost
	// AlterTableAddNotNull would drop the NOT NULL entirely.
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfnd_parent (pid integer, pname text)"); err != nil {
		t.Fatalf("create legacy inheritance parent idfnd_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idfnd_child (extra integer) INHERITS (public.idfnd_parent)"); err != nil {
		t.Fatalf("create legacy inheritance child idfnd_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.idfnd_child ADD CONSTRAINT idfnd_child_pid_not_null NOT NULL pid"); err != nil {
		t.Fatalf("add default-named NOT NULL on inherited column idfnd_child.pid: %v", err)
	}

	// Slice 280 (multi-column inherited NOT NULL body form — attnum ordering +
	// per-column collapse): two conislocal NOT NULL constraints on DISTINCT
	// inherited columns of the same child, added in REVERSE attnum order, must
	// emit their STANDALONE body items in ATTNUM order (not constraint-creation
	// order) because the pg_dump body loop iterates `j` over columns
	// (pg_dump.c:17175-17233). Slices 271/279 proved the named/default body form
	// for a SINGLE inherited column; this twin proves (a) the body emits multiple
	// `NOT NULL <col>` items sorted by attnum and (b) the COLLAPSE decision is
	// PER-COLUMN: `mb`'s constraint carries a NON-default name (`mninh_named` !=
	// `mninh_child_mb_not_null`) so its body item keeps the `CONSTRAINT mninh_named`
	// prefix, while `ma`'s name EQUALS its auto-name (`mninh_child_ma_not_null`) so
	// its body item collapses to the bare `NOT NULL ma`. The ALTERs run mb-first
	// (attnum 2) then ma-second (attnum 1) so a regression that emitted in
	// creation order would print `mb` before `ma`. No production change — the body
	// loop already walks columns in attnum order and reuses the same per-column
	// notnull_constrs[] entries proven by slices 271/277/279. A regression that
	// sorted by constraint OID/creation order would flip the two items; one that
	// applied the default-name collapse globally would drop the `CONSTRAINT
	// mninh_named` prefix; one that lost either AlterTableAddNotNull would drop a
	// body item.
	if err := runSQLSimple(t, c, "CREATE TABLE public.mninh_parent (ma integer, mb integer, mname text)"); err != nil {
		t.Fatalf("create multi-col inheritance parent mninh_parent: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.mninh_child (extra integer) INHERITS (public.mninh_parent)"); err != nil {
		t.Fatalf("create multi-col inheritance child mninh_child: %v", err)
	}
	// mb FIRST (attnum 2, non-default name → keeps CONSTRAINT prefix)...
	if err := runSQLSimple(t, c, "ALTER TABLE public.mninh_child ADD CONSTRAINT mninh_named NOT NULL mb"); err != nil {
		t.Fatalf("add named NOT NULL on inherited column mninh_child.mb: %v", err)
	}
	// ...then ma SECOND (attnum 1, default name → collapses to bare NOT NULL).
	if err := runSQLSimple(t, c, "ALTER TABLE public.mninh_child ADD CONSTRAINT mninh_child_ma_not_null NOT NULL ma"); err != nil {
		t.Fatalf("add default-named NOT NULL on inherited column mninh_child.ma: %v", err)
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
	if err := runSQLSimple(t, c, "CREATE TABLE public.defcol (id integer, status integer DEFAULT 0, created timestamptz DEFAULT now(), touched timestamptz DEFAULT CURRENT_TIMESTAMP, label text DEFAULT lpad('x', 5), meta jsonb DEFAULT '{}'::jsonb, vals integer[] DEFAULT ARRAY[1, 2, 3], grade integer DEFAULT CASE WHEN true THEN 1 ELSE 0 END, pair integer DEFAULT (1, 2), span interval DEFAULT INTERVAL '1' day, nflag boolean DEFAULT (1 IS NOT NULL), bflag boolean DEFAULT (true IS NOT TRUE), dflag boolean DEFAULT (1 IS DISTINCT FROM 2), calc integer DEFAULT (1 + 2) * 3)"); err != nil {
		t.Fatalf("create table defcol with function-call default: %v", err)
	}

	// Slice 302: a column DEFAULT containing a UNARY MINUS. The parser tags `-x`
	// with OpUnaryNeg (NOT OpSub — that is binary subtraction `a - b`), but both
	// deparse twins (catalog.formatExprForAttrdef / executor.defaultExprToSQL)
	// only handled `case parser.OpSub`, so a unary-minus default NEVER matched and
	// fell through to fmt.Sprintf("%v", e) — dumping a Go pointer string like
	// `&{0 - 0xc0001a2f00}` and corrupting every `DEFAULT -…` clause pg_dump
	// re-emits. Both twins now key on OpUnaryNeg. For a unary minus on a COMPOUND
	// operand (an OpExpr PG does NOT constant-fold) PG's get_rule_expr deparses
	// `(- (operand))`, byte-identical to real pg_dump 18.3 (verified):
	//   nb integer DEFAULT (- (1 + 2))
	//   nc integer DEFAULT ((- (1 + 2)) * 3)
	// (A unary minus applied DIRECTLY to a numeric literal — `DEFAULT -5` — is
	// folded by PG's parser into a negative typed Const and deparsed as
	// `'-5'::integer`; goopg is type-blind in this renderer and emits the
	// re-parseable `-5` instead. That bare-literal `'-N'::type` cast form is a
	// separate, deferred slice, so this fixture exercises only the byte-identical
	// COMPOUND cases.) The `nb`/`nc` columns guard the unary-minus path
	// end-to-end through real pg_dump.
	if err := runSQLSimple(t, c, "CREATE TABLE public.negdef (id integer, nb integer DEFAULT -(1 + 2), nc integer DEFAULT -(1 + 2) * 3)"); err != nil {
		t.Fatalf("create table negdef with unary-minus default: %v", err)
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
	// Slice 298: a partial-index predicate with NESTED arithmetic exercises the
	// executor's defaultExprToSQL BinaryOp parenthesization (the index-predicate
	// twin of slice 297's column-default fix). Without full parenthesization the
	// predicate `(qty + id) * mgr_id > 0` deparses to the precedence-corrupted
	// `qty + id * mgr_id > 0`, which restores as `qty + (id * mgr_id) > 0` — a
	// silent semantic change. Real pg_dump 18.3 emits the fully-parenthesized form.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_calc_partial_idx ON public.foo (qty) WHERE (qty + id) * mgr_id > 0"); err != nil {
		t.Fatalf("create nested-arithmetic partial index: %v", err)
	}
	// Slice 299: a nested-arithmetic *expression-index column* `((qty + id) * mgr_id)`
	// exercises slice 298's executor.defaultExprToSQL BinaryOp parenthesization in the
	// SECOND deparse context that renderer feeds — the index-key expression stored in
	// catalog.Index.ColExprStrings (vs slice 298's index *predicate*). pg_get_indexdef
	// wraps the deparsed key in `(%s)` inside the `USING btree (...)` column-list parens,
	// so real pg_dump 18.3 emits FOUR nested parens `((((qty + id) * mgr_id)))` (verified
	// byte-identical). Without the BinaryOp parens the key would deparse to the
	// precedence-corrupt `(qty + id * mgr_id)`, restoring as `qty + (id * mgr_id)` — a
	// silent change to the indexed value. This is the oracle-verified expression-column
	// complement to slice 298's predicate fixture.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_calc_expr_idx ON public.foo (((qty + id) * mgr_id))"); err != nil {
		t.Fatalf("create nested-arithmetic expression index: %v", err)
	}
	// Slice 360: a BARE FUNCTION-CALL expression-index key (`lower(name)`,
	// `lpad(name, 5)`) must dump WITHOUT the extra wrapping parens that slice 299's
	// arithmetic key carries. PG's pg_get_indexdef_worker (ruleutils.c) parenthesizes
	// an expression key column with `(%s)` UNLESS the top node is a bare function
	// call (`IsA(indexkey, FuncExpr) && funcformat == COERCE_EXPLICIT_CALL`), in
	// which case it prints the deparsed call as-is. Real pg_dump 18.3 emits
	// `USING btree (lower(name))` / `(lpad(name, 5))` — one paren level — whereas the
	// nested-arithmetic key (slice 299) keeps `((((qty + id) * mgr_id)))`. Before this
	// slice goopg's catalog.BuildIndexDef unconditionally wrapped every expression
	// key, so a function-call index dumped the byte-divergent `((lower(name)))` /
	// `((lpad(name, 5)))` (one extra paren pair) — semantically harmless but not a
	// byte-identical round-trip. The fix (catalog.indexKeyIsBareFuncCall, keyed on
	// the parsed ColExprs AST) suppresses the wrap for a plain FuncCall while
	// preserving it for every other expression shape. Verified byte-identical vs
	// real pg_dump 18.3 (reference /tmp/du_ref_pg).
	if err := runSQLSimple(t, c, "CREATE INDEX foo_lower_idx ON public.foo (lower(name))"); err != nil {
		t.Fatalf("create lower() expression index: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE INDEX foo_lpad_idx ON public.foo (lpad(name, 5))"); err != nil {
		t.Fatalf("create lpad() expression index: %v", err)
	}
	// Slice 361: a `CREATE INDEX … USING hash` index must dump `USING hash`, not
	// the B-tree substrate's `USING btree`. goopg has no native hash access
	// method, so a hash index routes through createBTreeIndex (catalog.Index
	// .Method stays "btree") and only DeclaredHash remembers the declared method
	// (design 0118-0099). pg_get_indexdef_worker (ruleutils.c) prints `USING %s`
	// from pg_am.amname, so real pg_dump 18.3 emits `USING hash (qty)` (verified
	// byte-identical against a throwaway PG 18.3 cluster). Before this slice
	// BuildIndexDef rendered the stored "btree", emitting the divergent
	// `USING btree (qty)`. The fix surfaces idx.DeclaredHash in BuildIndexDef.
	if err := runSQLSimple(t, c, "CREATE INDEX foo_qty_hash_idx ON public.foo USING hash (qty)"); err != nil {
		t.Fatalf("create hash index: %v", err)
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

	// Slice 365: a VIEW created `WITH [CASCADED|LOCAL] CHECK OPTION` round-trips
	// the clause. PostgreSQL stores the option as the `check_option=<mode>`
	// pg_class.reloption; pg_dump's getTables strips that element from the
	// reloptions array (array_remove, already handled, DU-002 slice 5) and instead
	// re-emits the `\n  WITH <MODE> CHECK OPTION` suffix after the view body
	// (pg_dump.c dumpTableSchema). goopg captures the mode on
	// catalog.Table.CheckOption and surfaces it through the reloptions cell so the
	// pg_dump checkoption CASE (`'check_option=cascaded' = ANY (c.reloptions)`)
	// derives CASCADED/LOCAL. goopg does not yet ENFORCE the option on
	// INSERT/UPDATE through the view — catalog/dump fidelity only. `vchk` carries
	// the bare (→ CASCADED) form, `vchk_local` the LOCAL form, both on their own
	// view so the asserts are isolated from foo_view.
	if err := runSQLSimple(t, c, "CREATE VIEW public.vchk AS SELECT id, name FROM public.foo WHERE qty > 0 WITH CHECK OPTION"); err != nil {
		t.Fatalf("create view vchk: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE VIEW public.vchk_local AS SELECT id, name FROM public.foo WHERE qty > 0 WITH LOCAL CHECK OPTION"); err != nil {
		t.Fatalf("create view vchk_local: %v", err)
	}

	// Slice 366: a VIEW created `WITH (security_barrier=true)` round-trips the
	// reloption. PostgreSQL stores it as the `security_barrier=true`
	// pg_class.reloption; unlike check_option, pg_dump's getTables KEEPS it in the
	// reloptions array (array_remove strips only check_option=*) and re-emits it
	// via appendReloptionsArray as the `WITH (security_barrier='true')` clause
	// after the view name (pg_dump.c dumpTableSchema). goopg captures the flag on
	// catalog.Table.SecurityBarrier and surfaces it through the reloptions cell.
	// `vsecbar` is on its own view so the assert is isolated from foo_view.
	if err := runSQLSimple(t, c, "CREATE VIEW public.vsecbar WITH (security_barrier=true) AS SELECT id, name FROM public.foo WHERE qty > 0"); err != nil {
		t.Fatalf("create view vsecbar: %v", err)
	}

	// Slice 367: a VIEW created `WITH (security_invoker=true)` round-trips the
	// reloption, the sibling of security_barrier. PostgreSQL stores it as the
	// `security_invoker=true` pg_class.reloption; like security_barrier, pg_dump's
	// getTables KEEPS it in the reloptions array (array_remove strips only
	// check_option=*) and re-emits it via appendReloptionsArray as the
	// `WITH (security_invoker='true')` clause after the view name (pg_dump.c
	// dumpTableSchema). goopg captures the flag on catalog.Table.SecurityInvoker
	// and surfaces it through the reloptions cell. `vsecinv` is on its own view so
	// the assert is isolated.
	if err := runSQLSimple(t, c, "CREATE VIEW public.vsecinv WITH (security_invoker=true) AS SELECT id, name FROM public.foo WHERE qty > 0"); err != nil {
		t.Fatalf("create view vsecinv: %v", err)
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
	// Slice 304: the SCHEMA-QUALIFIED 3-part OWNED BY form. Slice 118 above writes
	// the unqualified `OWNED BY owner_tbl.id`; the equally-valid PG form
	// `OWNED BY public.owner_tbl.id` (schema.table.column — exactly what pg_dump
	// itself re-emits) previously errored at validateSeqOwnedBy with
	// `sequence cannot be owned by relation "public"`: the owner string was split
	// on the FIRST dot, so table="public" / column="owner_tbl.id". The column is
	// the LAST dotted component (now split via strings.LastIndex, mirroring
	// InMemory.dependVirtualRows), so the table resolves to public.owner_tbl. The
	// dump must round-trip this byte-identically to the unqualified case.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.qowned_seq OWNED BY public.owner_tbl.label"); err != nil {
		t.Fatalf("create sequence qowned_seq (schema-qualified OWNED BY): %v", err)
	}

	// Slice 305: a table's REPLICA IDENTITY must round-trip. pg_dump emits an
	// `ALTER TABLE ONLY <t> REPLICA IDENTITY {FULL|NOTHING}` clause whenever
	// pg_class.relreplident != 'd' (REPLICA_IDENTITY_DEFAULT) for a regular,
	// partitioned, or matview relation (pg_dump.c:17781). goopg HARDCODED
	// relreplident to 'n' (REPLICA_IDENTITY_NOTHING) in the heap pg_class row
	// builder pg_dump reads (buildUserPGClassRow), so EVERY dumped table got a
	// spurious `... REPLICA IDENTITY NOTHING;` — a silent, pervasive divergence
	// from real pg_dump (which emits nothing for a default-identity table). The
	// fix defaults relreplident to 'd' and threads an actual
	// `ALTER TABLE ... REPLICA IDENTITY {DEFAULT|FULL|NOTHING}` through the
	// parser → catalog.Table.ReplicaIdentity → heap re-sync, so a non-default
	// setting round-trips and a default table emits nothing. goopg has no
	// logical replication; this is dump-fidelity only (like SET STORAGE). The
	// USING INDEX form is covered by slice 306 below.
	if err := runSQLSimple(t, c, "CREATE TABLE public.ri_full (id integer PRIMARY KEY, payload text)"); err != nil {
		t.Fatalf("create table ri_full: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.ri_full REPLICA IDENTITY FULL"); err != nil {
		t.Fatalf("alter table ri_full replica identity full: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.ri_nothing (id integer PRIMARY KEY, payload text)"); err != nil {
		t.Fatalf("create table ri_nothing: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.ri_nothing REPLICA IDENTITY NOTHING"); err != nil {
		t.Fatalf("alter table ri_nothing replica identity nothing: %v", err)
	}

	// Slice 306: REPLICA IDENTITY USING INDEX must round-trip. Unlike the
	// FULL/NOTHING forms (which pg_dump emits at TABLE-dump time when
	// relreplident != 'd'), pg_dump emits the USING INDEX clause at INDEX-dump
	// time, keyed on pg_index.indisreplident for the chosen index (pg_dump.c
	// dumpIndex:18186) — `ALTER TABLE ONLY public.ri_index REPLICA IDENTITY
	// USING INDEX ri_uidx;`. goopg previously rejected the USING INDEX form with
	// 0A000; it now (a) validates the index per PG's check_replica_identity
	// (unique, immediate, non-expression, non-partial, NOT NULL key columns),
	// (b) sets the table's relreplident to 'i', and (c) marks the chosen index's
	// indisreplident (clearing any prior one) in BOTH the virtual pg_index
	// builder pg_dump reads and the pg_index heap row (restart durability). The
	// referenced index must be on a NOT NULL column and UNIQUE.
	if err := runSQLSimple(t, c, "CREATE TABLE public.ri_index (id integer NOT NULL, payload text)"); err != nil {
		t.Fatalf("create table ri_index: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE UNIQUE INDEX ri_uidx ON public.ri_index (id)"); err != nil {
		t.Fatalf("create unique index ri_uidx: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.ri_index REPLICA IDENTITY USING INDEX ri_uidx"); err != nil {
		t.Fatalf("alter table ri_index replica identity using index: %v", err)
	}

	// Slice 307: a FOREIGN KEY added with NOT VALID must round-trip. PG records
	// the unvalidated state in pg_constraint.convalidated='f', and
	// pg_get_constraintdef_worker (ruleutils.c:2604) appends a trailing
	// ` NOT VALID` to the constraint definition AFTER the DEFERRABLE clauses —
	// the shared tail common to every constraint type. pg_dump's getConstraints
	// renders the FK via pg_get_constraintdef, so the dumped
	// `ALTER TABLE ONLY ... ADD CONSTRAINT nv_child_fk FOREIGN KEY (ref_id)
	// REFERENCES public.nv_ref(id) NOT VALID;` carries the suffix and the restored
	// FK is likewise unvalidated. goopg already tracks catalog.ForeignKey.NotValid
	// (set at ALTER TABLE ADD CONSTRAINT ... NOT VALID time) and projects
	// convalidated='f' in the pg_constraint virtual builder; the gap was purely
	// buildForeignKeyDefString never emitting the ` NOT VALID` tail, so the dump
	// silently re-validated the constraint (a restore would then enforce it on
	// existing rows that NOT VALID was meant to grandfather). Dump-fidelity only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nv_ref (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create table nv_ref: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.nv_child (id integer, ref_id integer)"); err != nil {
		t.Fatalf("create table nv_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nv_child ADD CONSTRAINT nv_child_fk FOREIGN KEY (ref_id) REFERENCES public.nv_ref (id) NOT VALID"); err != nil {
		t.Fatalf("alter table nv_child add fk not valid: %v", err)
	}

	// Slice 308: a CHECK constraint added with NOT VALID must round-trip with the
	// same ` NOT VALID` tail. ` NOT VALID` is the SHARED final clause of
	// pg_get_constraintdef_worker (ruleutils.c:2604), common to FK *and* CHECK;
	// slice 307 wired the FK path, this wires CHECK. pg_dump reads convalidated
	// for contype='c' rows and sets separate=!validated (pg_dump.c:9757), so an
	// unvalidated CHECK is emitted AFTER data as a standalone
	// `ALTER TABLE public.nvc_tbl\n    ADD CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;`
	// rather than inline in CREATE TABLE — exactly so possibly-violating rows load
	// first. goopg now carries catalog.NamedCheckConstraint.NotValid (set at
	// ALTER TABLE ADD CONSTRAINT ... CHECK ... NOT VALID time), projects
	// convalidated='f', and appends the ` NOT VALID` tail in pg_get_constraintdef's
	// CHECK branch. Dump-fidelity only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.nvc_tbl (id integer, val integer)"); err != nil {
		t.Fatalf("create table nvc_tbl: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.nvc_tbl ADD CONSTRAINT nvc_chk CHECK (val > 0) NOT VALID"); err != nil {
		t.Fatalf("alter table nvc_tbl add check not valid: %v", err)
	}

	// Slice 309: a FOREIGN KEY declared with MATCH FULL must round-trip the match
	// type. PG records pg_constraint.confmatchtype='f' (vs 's' for the MATCH
	// SIMPLE default), and pg_get_constraintdef_worker (ruleutils.c) emits a
	// ` MATCH FULL` clause BETWEEN the REFERENCES column list and the ON
	// UPDATE/DELETE clauses. pg_dump's getConstraints renders the FK via
	// pg_get_constraintdef, so the dumped
	// `ALTER TABLE ONLY ... ADD CONSTRAINT mf_child_fk FOREIGN KEY (a, b)
	// REFERENCES public.mf_ref(a, b) MATCH FULL;` carries the clause and the
	// restored FK keeps MATCH FULL's all-or-nothing NULL semantics. The gap was
	// twofold: the parser silently dropped the MATCH clause (it was never part of
	// the FK grammar) and buildForeignKeyDefString never emitted ` MATCH FULL`,
	// so a MATCH FULL FK silently degraded to MATCH SIMPLE on restore. goopg now
	// parses MATCH FULL|PARTIAL|SIMPLE in all three FK forms, carries
	// catalog.ForeignKey.MatchFull, projects confmatchtype='f', and re-emits the
	// clause. Dump-fidelity only (goopg does not yet enforce FK matching). A
	// multi-column FK exercises MATCH FULL's intended use (mixed-NULL keys).
	if err := runSQLSimple(t, c, "CREATE TABLE public.mf_ref (a integer, b integer, PRIMARY KEY (a, b))"); err != nil {
		t.Fatalf("create table mf_ref: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.mf_child (a integer, b integer)"); err != nil {
		t.Fatalf("create table mf_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.mf_child ADD CONSTRAINT mf_child_fk FOREIGN KEY (a, b) REFERENCES public.mf_ref (a, b) MATCH FULL"); err != nil {
		t.Fatalf("alter table mf_child add fk match full: %v", err)
	}

	// Slice 310: a PARTIAL EXCLUDE constraint (`EXCLUDE USING btree (a WITH =)
	// WHERE (b > 0)`) must round-trip its WHERE predicate. PG renders the
	// exclusion def via pg_get_indexdef_worker, which appends ` WHERE (%s)`
	// (ruleutils.c:1564) after the operator/INCLUDE list and BEFORE the
	// DEFERRABLE tail, so pg_get_constraintdef emits
	// `EXCLUDE USING btree (a WITH =) WHERE (b > 0)` and pg_dump re-emits it as
	// `ADD CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b > 0);`.
	// The gap was at parse time: parseExcludeConstraint never consumed a trailing
	// WHERE, so the predicate was silently dropped and a partial exclusion
	// degraded on restore into one applying to EVERY row (a semantic change, not
	// just a cosmetic one). goopg now parses the WHERE expression into
	// TableConstraintDef.ExclusionWhere, threads it onto the backing index's
	// PredicateString (defaultExprToSQL — fully parenthesized like PG), and the
	// EXCLUDE branch of buildConstraintDefString appends ` WHERE (pred)` before
	// DEFERRABLE. btree-equality EXCLUDE so goopg backs it with a real index
	// (matching slice 143's form).
	if err := runSQLSimple(t, c, "CREATE TABLE public.pex (a integer, b integer, CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b > 0))"); err != nil {
		t.Fatalf("create table pex with partial exclude: %v", err)
	}

	// Slice 311: a FOREIGN KEY whose `ON DELETE SET NULL` is restricted to a
	// column subset (PG15 confdelsetcols) must round-trip the column list.
	// pg_get_constraintdef (ruleutils.c:2376) appends ` (col, …)` after the
	// `ON DELETE SET NULL` keyword via decompile_column_index_array when
	// pg_constraint.confdelsetcols is non-null, so pg_dump emits
	// `... ON DELETE SET NULL (b);`. The gap was at parse time: parseFKAction
	// consumed `SET NULL`/`SET DEFAULT` but never the trailing column list, so a
	// restricted action silently degraded into a whole-key SET NULL on restore
	// (a SEMANTIC change — the other FK columns would also be nulled). goopg now
	// parses the list into {Column,Table,AlterTable}…OnDeleteSetCols, threads it
	// onto catalog.ForeignKey, projects pg_constraint.confdelsetcols (attnum
	// array), and buildForeignKeyDefString re-emits ` (cols)` after the ON DELETE
	// clause. A two-column referencing key makes the restriction observable
	// (only sfk_b is nulled). The referenced table needs a UNIQUE/PK on (id).
	if err := runSQLSimple(t, c, "CREATE TABLE public.sfk_ref (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create table sfk_ref: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.sfk_child (a integer, b integer)"); err != nil {
		t.Fatalf("create table sfk_child: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.sfk_child ADD CONSTRAINT sfk_child_fk FOREIGN KEY (b) REFERENCES public.sfk_ref (id) ON DELETE SET NULL (b)"); err != nil {
		t.Fatalf("alter table sfk_child add fk on delete set null (b): %v", err)
	}

	// Slice 312: a CREATE INDEX with a non-default per-column operator class
	// (`text_pattern_ops`) must round-trip the opclass. pg_get_indexdef_worker
	// (ruleutils.c get_opclass_name) emits the opclass after the column — and
	// after any COLLATE — and before ASC/DESC, suppressing only the type's default
	// opclass. The gap was at parse time: parseIndexColumnList consumed the bare
	// opclass ident but DISCARDED it, and catalog.Index had no per-column opclass
	// field, so `CREATE INDEX … (a text_pattern_ops)` dumped as a plain `(a)` —
	// silently widening the index back to the default opclass on restore (a
	// semantic change: text_pattern_ops drives LIKE/prefix-range scans, the
	// default text_ops does not). goopg now threads the opclass onto
	// IndexColOrder.OpClass → catalog.Index.ColOpClasses → BuildIndexDef. A
	// second key column with a default opclass confirms only the explicit one is
	// emitted.
	if err := runSQLSimple(t, c, "CREATE TABLE public.opcidx (a text, b text)"); err != nil {
		t.Fatalf("create table opcidx: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE INDEX opcidx_pat ON public.opcidx (a text_pattern_ops, b)"); err != nil {
		t.Fatalf("create index opcidx_pat with opclass: %v", err)
	}

	// Slice 313: a CREATE INDEX with a non-default per-column COLLATE must
	// round-trip the collation. pg_get_indexdef_worker (ruleutils.c) emits the
	// collation after the column/expression and BEFORE the operator class (via
	// generate_collation_name, which quotes the collname as an identifier),
	// suppressing the type's default collation. The gap was the sibling of slice
	// 312: parseIndexColumnList consumed the COLLATE name but DISCARDED it, and
	// catalog.Index had no per-column collation field, so `CREATE INDEX … (a
	// COLLATE "C")` dumped as a plain `(a)` — silently widening the index back to
	// the default collation on restore. goopg now threads the collation onto
	// IndexColOrder.Collation → catalog.Index.ColCollations → BuildIndexDef. A
	// second key column with the default collation confirms only the explicit one
	// is emitted.
	if err := runSQLSimple(t, c, "CREATE TABLE public.collidx (a text, b text)"); err != nil {
		t.Fatalf("create table collidx: %v", err)
	}
	if err := runSQLSimple(t, c, `CREATE INDEX collidx_c ON public.collidx (a COLLATE "C", b)`); err != nil {
		t.Fatalf("create index collidx_c with collation: %v", err)
	}

	// Slice 314: a CREATE STATISTICS extended-statistics object must round-trip.
	// pg_dump's dumpStatisticsExt selects pg_get_statisticsobjdef(oid) and emits
	// the result verbatim (plus a semicolon). Before this slice goopg's parser
	// discarded the kinds clause AND the ON column list (only name + FROM table
	// were captured), and pg_get_statisticsobjdef was unimplemented — so the
	// statistics object was silently dropped from the dump. ruleutils.c
	// pg_get_statisticsobj_worker suppresses the kinds clause when all three kinds
	// (ndistinct/dependencies/mcv) are enabled (the default), so a plain
	// `CREATE STATISTICS … ON a, b FROM t` dumps with no kinds clause; an explicit
	// single-kind object dumps `(ndistinct)`. goopg now threads Kinds/Columns onto
	// catalog.StatisticsObject → BuildStatisticsObjDef. Both forms are exercised.
	if err := runSQLSimple(t, c, "CREATE TABLE public.statext_t (a integer, b integer, c integer)"); err != nil {
		t.Fatalf("create table statext_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE STATISTICS public.statext_all ON a, b FROM public.statext_t"); err != nil {
		t.Fatalf("create statistics statext_all: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE STATISTICS public.statext_nd (ndistinct) ON b, c FROM public.statext_t"); err != nil {
		t.Fatalf("create statistics statext_nd: %v", err)
	}
	// Slice 316: expression extended-statistics objects must also round-trip.
	// PG's grammar requires expression elements to be parenthesized; ruleutils.c
	// pg_get_statisticsobj_worker emits all simple columns first, then each
	// expression (parenthesized unless it is a bare function call), and suppresses
	// the kinds clause when the object spans a single target. Before this slice
	// goopg flagged HasExpr and BuildStatisticsObjDef declined, silently dropping
	// the object. goopg now parses the ON-list expression into an AST and the
	// executor deparses it (defaultExprToSQL already parenthesizes binary ops and
	// leaves bare function calls unwrapped, matching ruleutils.c). `statext_expr`
	// (single expression → no kinds clause); `statext_mix` (column + expression).
	if err := runSQLSimple(t, c, "CREATE STATISTICS public.statext_expr ON (a + b) FROM public.statext_t"); err != nil {
		t.Fatalf("create statistics statext_expr: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE STATISTICS public.statext_mix ON a, (b + c) FROM public.statext_t"); err != nil {
		t.Fatalf("create statistics statext_mix: %v", err)
	}
	// Slice 317: a non-default extended-statistics target must round-trip. pg_dump
	// (getExtendedStatistics + dumpStatisticsExt) reads pg_statistic_ext.stxstattarget
	// and emits `ALTER STATISTICS … SET STATISTICS <n>` after the CREATE whenever
	// stxstattarget >= 0; the default (PG18 stores NULL → -1) emits nothing. goopg
	// records the target on catalog.StatisticsObject.StatTarget via
	// `ALTER STATISTICS … SET STATISTICS n`; the pg_statistic_ext virtual row now
	// projects it (NULL when unset). `statext_nd` gets a target of 250 (must
	// re-emit); `statext_all` stays default (must NOT emit an ALTER).
	if err := runSQLSimple(t, c, "ALTER STATISTICS public.statext_nd SET STATISTICS 250"); err != nil {
		t.Fatalf("alter statistics statext_nd set statistics: %v", err)
	}

	// Slice 319: a CREATE TRIGGER must round-trip through pg_dump. pg_dump's
	// getTriggers selects pg_get_triggerdef(t.oid, false) and dumpTrigger emits
	// the result verbatim (plus a trailing semicolon). Before this slice goopg's
	// pg_trigger view hardcoded zero rows (VirtualRows → nil) AND
	// pg_get_triggerdef was unimplemented, so a user trigger was silently dropped
	// from the dump. goopg now assigns each trigger an OID at CREATE TRIGGER time,
	// projects it through pg_trigger, and reconstructs the statement via
	// pg_get_triggerdef. Two triggers exercise both timings/levels and the OR-ed
	// event list: a BEFORE INSERT OR UPDATE row-level trigger and an AFTER DELETE
	// statement-level trigger, both on public.trig_t.
	if err := runSQLSimple(t, c, "CREATE TABLE public.trig_t (a integer, b integer)"); err != nil {
		t.Fatalf("create table trig_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$"); err != nil {
		t.Fatalf("create trigger function trig_fn: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_biu BEFORE INSERT OR UPDATE ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_biu: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_ad AFTER DELETE ON public.trig_t FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_ad: %v", err)
	}
	// Slice 326: a column-specific `UPDATE OF a, b` trigger. pg_get_triggerdef
	// appends ` OF <cols>` right after the UPDATE event; before this slice the
	// parser tripped on the `OF` keyword and the clause was dropped. Combine it
	// with INSERT to exercise the OR-ed event list with the OF clause attached.
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_uof BEFORE INSERT OR UPDATE OF a, b ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_uof: %v", err)
	}
	// Slice 327: a CONSTRAINT TRIGGER. pg_get_triggerdef emits `CREATE CONSTRAINT
	// TRIGGER` plus the `[NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED}` clause
	// after the ON-table name. trg_cdef takes the default (NOT DEFERRABLE
	// INITIALLY IMMEDIATE); trg_cdfr is explicitly DEFERRABLE INITIALLY DEFERRED.
	// Constraint triggers are always AFTER / FOR EACH ROW (enforced by PG).
	if err := runSQLSimple(t, c, "CREATE CONSTRAINT TRIGGER trg_cdef AFTER INSERT ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create constraint trigger trg_cdef: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE CONSTRAINT TRIGGER trg_cdfr AFTER UPDATE ON public.trig_t DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create constraint trigger trg_cdfr: %v", err)
	}
	// Slice 328: a REFERENCING transition-table trigger. pg_get_triggerdef emits
	// `REFERENCING OLD TABLE AS … NEW TABLE AS …` between the ON-table name and
	// FOR EACH ROW (transition tables are an AFTER, statement-level feature).
	// trg_ref carries both OLD and NEW; trg_refn carries NEW only.
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_ref AFTER UPDATE ON public.trig_t REFERENCING OLD TABLE AS ot NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_ref: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_refn AFTER INSERT ON public.trig_t REFERENCING NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_refn: %v", err)
	}

	// Slice 329: a row-level trigger with a `WHEN (condition)` qualification.
	// pg_get_triggerdef re-emits `WHEN (…)` between FOR EACH ROW and EXECUTE
	// FUNCTION, building OLD/NEW range-table entries so the condition's column
	// references render with lowercased `old.`/`new.` qualifiers and the boolean
	// OpExpr is fully parenthesized (→ `WHEN ((new.b <> old.b))`). Before this
	// slice the parser skipped the WHEN body entirely, so the condition was lost
	// on dump. trg_when compares NEW vs OLD across an UPDATE; trg_whna tests a
	// single NEW column against a constant (no top-level OpExpr-on-OpExpr nesting).
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_when BEFORE UPDATE ON public.trig_t FOR EACH ROW WHEN (NEW.b <> OLD.b) EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_when: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_whna BEFORE INSERT ON public.trig_t FOR EACH ROW WHEN (NEW.a > 0) EXECUTE FUNCTION public.trig_fn()"); err != nil {
		t.Fatalf("create trigger trg_whna: %v", err)
	}
	// Slice 368: a trigger whose EXECUTE FUNCTION carries STRING arguments
	// (TG_ARGV). pg_get_triggerdef (ruleutils.c pg_get_triggerdef_worker:462-486)
	// renders `EXECUTE FUNCTION public.trig_fn(` then each tgargs entry
	// comma-separated (`, `) and single-quoted via simple_quote_literal (embedded
	// single-quotes doubled). goopg's parser already collected the string-literal
	// args into CreateTriggerStmt.FuncArgs, execCreateTrigger threads them to
	// catalog.Trigger.Args, and buildTriggerDefString re-emits them with identical
	// `', '` separation and `''`-doubled quoting. trg_arg passes two args, the
	// second carrying an embedded single quote to exercise the simple_quote_literal
	// escaping path. NO production change — the whole parse→catalog→deparse path
	// already existed but had no oracle-verified fixture pinning the rendered form.
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_arg AFTER INSERT ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn('hello', 'wo''rld')"); err != nil {
		t.Fatalf("create trigger trg_arg with function args: %v", err)
	}
	// Slice 369: a trigger whose EXECUTE FUNCTION carries NON-string arguments —
	// an integer, a float, and a bare identifier. PG's grammar (gram.y
	// TriggerFuncArg) stores EVERY argument form as a string in pg_trigger.tgargs:
	// an Iconst via psprintf("%d") (so "0042" canonicalises to "42"), an FCONST by
	// its lexeme, and a ColLabel identifier by its text. pg_get_triggerdef then
	// re-quotes ALL of them as `'…'` literals, so `trig_fn(0042, 3.14, foo)` dumps
	// as `trig_fn('42', '3.14', 'foo')`. goopg's parser previously SKIPPED these
	// non-string tokens (dropping the args entirely); it now captures their text
	// (integers canonicalised) into CreateTriggerStmt.FuncArgs, and the existing
	// buildTriggerDefString quotes them identically.
	if err := runSQLSimple(t, c, "CREATE TRIGGER trg_narg AFTER INSERT ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn(0042, 3.14, foo)"); err != nil {
		t.Fatalf("create trigger trg_narg with non-string function args: %v", err)
	}

	// Slice 320: a clustered index must round-trip through pg_dump. pg_dump's
	// getIndexes selects pg_index.indisclustered and dumpIndex/dumpConstraint
	// append a trailing `ALTER TABLE <t> CLUSTER ON <idx>;` after the index's
	// CREATE INDEX / ADD CONSTRAINT when the flag is set (pg_dump.c:18141 /
	// :18483). Before this slice goopg's pg_index view hardcoded
	// indisclustered='f' and CLUSTER was a pure no-op, so the clustering
	// selection was silently dropped from the dump. goopg now records the
	// selection on the chosen index (catalog.Index.IsClustered), clears it on
	// the table's other indexes, and re-syncs the pg_index heap row pg_dump
	// reads. Two surfaces are exercised: a plain secondary index marked via
	// `CLUSTER <t> USING <idx>` (dumpIndex path) and a PRIMARY KEY constraint
	// index marked the same way (dumpConstraint path).
	if err := runSQLSimple(t, c, "CREATE TABLE public.clus_t (a integer PRIMARY KEY, b integer)"); err != nil {
		t.Fatalf("create table clus_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE INDEX clus_t_b_idx ON public.clus_t (b)"); err != nil {
		t.Fatalf("create index clus_t_b_idx: %v", err)
	}
	if err := runSQLSimple(t, c, "CLUSTER public.clus_t USING clus_t_b_idx"); err != nil {
		t.Fatalf("cluster clus_t using clus_t_b_idx: %v", err)
	}
	// A second table clustered on its PRIMARY KEY index exercises the
	// dumpConstraint CLUSTER-ON branch (constraint-backed index, not a plain
	// CREATE INDEX). PG names the PK index <table>_pkey.
	if err := runSQLSimple(t, c, "CREATE TABLE public.clus_pk (a integer PRIMARY KEY, b integer)"); err != nil {
		t.Fatalf("create table clus_pk: %v", err)
	}
	if err := runSQLSimple(t, c, "CLUSTER public.clus_pk USING clus_pk_pkey"); err != nil {
		t.Fatalf("cluster clus_pk using clus_pk_pkey: %v", err)
	}

	// Slice 322: ROW LEVEL SECURITY must round-trip through pg_dump. pg_dump's
	// getPolicies probes pg_class.relrowsecurity and represents an RLS-enabled
	// table with a null-polname PolicyInfo, which dumpPolicy emits as
	// `ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;` (pg_dump.c). dumpTableSchema
	// separately emits `ALTER TABLE ONLY <t> FORCE ROW LEVEL SECURITY;` from
	// relforcerowsecurity. Before this slice goopg hardcoded both pg_class
	// columns to 'f' and silently consumed the ENABLE clause as a trigger no-op,
	// so the RLS state was dropped from the dump (and goopg could not restore its
	// own output). goopg now records catalog.Table.RowSecurity /
	// ForceRowSecurity, projects them through both pg_class builders, and re-syncs
	// the pg_class heap row pg_dump reads. goopg enforces no row-level security —
	// dump-fidelity only. `rls_t` carries both flags; the two are independent.
	if err := runSQLSimple(t, c, "CREATE TABLE public.rls_t (a integer)"); err != nil {
		t.Fatalf("create table rls_t: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.rls_t ENABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("enable row level security on rls_t: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.rls_t FORCE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("force row level security on rls_t: %v", err)
	}

	// Slice 323: CREATE POLICY must round-trip through pg_dump. pg_dump's
	// getPolicies reads pg_policy (polname, polcmd, polpermissive, polroles,
	// pg_get_expr(polqual/polwithcheck)) and dumpPolicy re-emits the CREATE
	// POLICY (pg_dump.c). Before this slice CREATE POLICY was a hard parse error
	// and pg_policy was an empty stub, so a policy was silently lost on
	// dump/restore. goopg now records catalog.Table.Policies and projects them
	// through the pg_policy virtual catalog; polqual/polwithcheck render via the
	// catalog-side pg_get_expr deparser (fully parenthesized), so pg_dump emits
	// `USING ((expr))` / `WITH CHECK ((expr))` byte-identically. goopg enforces
	// no RLS — dump fidelity only. `pol_t` carries one policy per command form
	// (PERMISSIVE FOR ALL, RESTRICTIVE FOR SELECT, FOR INSERT WITH CHECK). All
	// policies are TO PUBLIC ({0}); named-role policies are a follow-up slice.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pol_t (a integer, b text)"); err != nil {
		t.Fatalf("create table pol_t: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.pol_t ENABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("enable row level security on pol_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE POLICY p_simple ON public.pol_t USING (a > 0)"); err != nil {
		t.Fatalf("create policy p_simple: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE POLICY p_restr ON public.pol_t AS RESTRICTIVE FOR SELECT USING (a > 5)"); err != nil {
		t.Fatalf("create policy p_restr: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE POLICY p_check ON public.pol_t FOR INSERT WITH CHECK (a < 100)"); err != nil {
		t.Fatalf("create policy p_check: %v", err)
	}

	// Slice 330: a named-role policy (`CREATE POLICY ... TO <role>`) must
	// round-trip through pg_dump. Before this slice goopg had no per-role OID
	// registry, so polroles could only hold the {0} PUBLIC sentinel and CREATE
	// POLICY rejected any named role. Now CREATE ROLE mints a per-role OID
	// (catalog.RegisterRole), pg_roles exposes it, and execCreatePolicy records
	// the role's OID in pg_policy.polroles. pg_dump's getPolicies resolves the
	// OID array back to the name via
	// `array_to_string(ARRAY(SELECT quote_ident(rolname) FROM pg_roles
	// WHERE oid = ANY(polroles)), ', ')`, so dumpPolicy emits ` TO pol_role`
	// before the USING clause (pg_dump.c order: ON … [AS][FOR][TO][USING]).
	// goopg enforces no RLS — dump fidelity only.
	if err := runSQLSimple(t, c, "CREATE TABLE public.pol_rt (a integer)"); err != nil {
		t.Fatalf("create table pol_rt: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.pol_rt ENABLE ROW LEVEL SECURITY"); err != nil {
		t.Fatalf("enable row level security on pol_rt: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE pol_role"); err != nil {
		t.Fatalf("create role pol_role: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE POLICY p_role ON public.pol_rt FOR SELECT TO pol_role USING (a > 0)"); err != nil {
		t.Fatalf("create policy p_role: %v", err)
	}

	// Slice 331: a table-level GRANT must round-trip through pg_dump. pg_dump's
	// getTables selects `c.relacl` directly and `acldefault('r', relowner)` as
	// the baseline, then buildACLCommands (src/bin/pg_dump/dumputils.c) parses
	// the aclitem[] text CLIENT-SIDE and emits the GRANT/REVOKE diff — no
	// server-side aclexplode/aclitemout is involved. Before this slice goopg
	// always projected relacl as NULL even after a GRANT, so the privilege was
	// silently lost on dump/restore. goopg already records table grants in its
	// catalog ACL store (Catalog.GrantTablePrivilege, server/grant_ddl.go); now
	// the pg_class virtual builder renders that store as the materialized
	// aclitem[] (owner full default first, grantor=postgres, then each grantee),
	// matching what PostgreSQL stores once the first GRANT materializes relacl.
	// pg_dump diffs it against acldefault('r', 10) so the owner entry cancels and
	// only the grantee's `GRANT SELECT ON TABLE public.grant_t TO grantee_role;`
	// is emitted.
	if err := runSQLSimple(t, c, "CREATE TABLE public.grant_t (a integer)"); err != nil {
		t.Fatalf("create table grant_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE grantee_role"); err != nil {
		t.Fatalf("create role grantee_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.grant_t TO grantee_role"); err != nil {
		t.Fatalf("grant select on grant_t: %v", err)
	}

	// Slice 332: a GRANT … WITH GRANT OPTION must round-trip. aclitemout renders
	// a grant-option privilege as "<letter>*" (here "r*" for SELECT WITH GRANT
	// OPTION), and pg_dump's buildACLCommands splits grant-option privileges into
	// a separate `GRANT … WITH GRANT OPTION;` (privswgo branch, dumputils.c).
	// goopg now records the option flag in its catalog ACL store and renders the
	// trailing `*` in pg_class.relacl, so the WITH GRANT OPTION clause survives.
	if err := runSQLSimple(t, c, "CREATE TABLE public.grant_g (a integer)"); err != nil {
		t.Fatalf("create table grant_g: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE grantee2_role"); err != nil {
		t.Fatalf("create role grantee2_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT OPTION"); err != nil {
		t.Fatalf("grant select with grant option on grant_g: %v", err)
	}

	// Slice 333: a GRANT … ON SEQUENCE must round-trip through pg_dump. pg_dump's
	// getTables selects relacl for sequences (relkind 'S') too and computes the
	// baseline as acldefault('s', relowner) → "{postgres=rwU/postgres}" (USAGE/
	// SELECT/UPDATE), and dumpTableSchema passes objtype "SEQUENCE" to dumpACL so
	// the diff is re-emitted as `GRANT … ON SEQUENCE …`. Before this slice goopg's
	// grant_ddl recorder bailed on ON SEQUENCE (no-op) and the sequence's relacl
	// stayed NULL, silently dropping the privilege. goopg now records sequence
	// grants in the shared relation ACL store and the pg_class virtual builder
	// renders a sequence's relacl with the sequence privilege order / owner
	// default "rwU".
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.grant_seq"); err != nil {
		t.Fatalf("create sequence grant_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE seq_role"); err != nil {
		t.Fatalf("create role seq_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role"); err != nil {
		t.Fatalf("grant usage on grant_seq: %v", err)
	}

	// Slice 334: a GRANT … TO PUBLIC must round-trip through pg_dump. PostgreSQL
	// stores a grant to the PUBLIC pseudo-role with an EMPTY grantee in the
	// aclitem (relacl entry "=r/postgres"), and pg_dump's buildACLCommands
	// (dumputils.c) renders an empty grantee as the keyword PUBLIC, emitting
	// `GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;`. goopg records the
	// grant under the reserved role name "public" (no real role may carry that
	// name); the pg_class relacl projection maps it back to the empty grantee so
	// pg_dump re-emits the TO PUBLIC clause.
	if err := runSQLSimple(t, c, "CREATE TABLE public.grant_pub (a integer)"); err != nil {
		t.Fatalf("create table grant_pub: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.grant_pub TO PUBLIC"); err != nil {
		t.Fatalf("grant select on grant_pub to public: %v", err)
	}

	// Slice 335: a GRANT … ON SCHEMA must round-trip through pg_dump. pg_dump's
	// getNamespaces reads `n.nspacl` from pg_namespace, diffs it against
	// acldefault('n', nspowner) = "{postgres=UC/postgres}" client-side in
	// buildACLCommands, and dumpACL (objtype "SCHEMA") re-emits the diff as
	// `GRANT … ON SCHEMA …`. Before this slice goopg projected nspacl as a
	// constant NULL even after a GRANT (the grant_ddl recorder bailed on ON
	// SCHEMA), so the privilege was silently lost on dump/restore. goopg now
	// records the schema grant in the OID-keyed ACL store (schemas share it with
	// relations) and renders nspacl with the schema privilege order (USAGE 'U' <
	// CREATE 'C') / owner-default "UC". A dedicated schema keeps the grant
	// isolated from the `s` schema asserted elsewhere.
	if err := runSQLSimple(t, c, "CREATE SCHEMA grant_sch"); err != nil {
		t.Fatalf("create schema grant_sch: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE schema_role"); err != nil {
		t.Fatalf("create role schema_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE ON SCHEMA grant_sch TO schema_role"); err != nil {
		t.Fatalf("grant usage on schema grant_sch: %v", err)
	}

	// Slice 336: a GRANT to a role whose name needs quoting must round-trip. PG's
	// aclitemout (putid) double-quotes a grantee name containing any character
	// outside [A-Za-z0-9_] (here a hyphen) in pg_class.relacl, and pg_dump's getid
	// parser relies on those quotes to read the whole name; buildACLCommands then
	// re-emits the GRANT through fmtId (also quoted). goopg previously rendered the
	// grantee raw ("weird-role=r/postgres"), which pg_dump would mis-parse at the
	// hyphen. (A reserved-keyword name like "user" needs no aclitem quoting — it is
	// all-alnum, so putid leaves it bare and pg_dump's fmtId adds the quotes
	// client-side; that case already round-trips.)
	if err := runSQLSimple(t, c, `CREATE ROLE "weird-role"`); err != nil {
		t.Fatalf("create role weird-role: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.grant_q (a integer)"); err != nil {
		t.Fatalf("create table grant_q: %v", err)
	}
	if err := runSQLSimple(t, c, `GRANT SELECT ON TABLE public.grant_q TO "weird-role"`); err != nil {
		t.Fatalf("grant select on grant_q to weird-role: %v", err)
	}

	// Slice 337: a GRANT to a role whose name is case-significant (double-quoted
	// mixed case) must round-trip. PostgreSQL role names are case-significant
	// when double-quoted, and aclitemout renders the role's TRUE name in
	// pg_class.relacl. A mixed-case name like "MixedCase" is all-alnum, so putid
	// leaves it bare in the aclitem (MixedCase=r/postgres), but pg_dump's
	// getid/fmtId re-quote it client-side → GRANT … TO "MixedCase". goopg's ACL
	// store keys privileges by the lower-cased name (case-insensitive lookups),
	// so without preserving the original spelling it would render `mixedcase`
	// and pg_dump would emit TO mixedcase (a different, nonexistent role).
	if err := runSQLSimple(t, c, `CREATE ROLE "MixedCase"`); err != nil {
		t.Fatalf("create role MixedCase: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.grant_mc (a integer)"); err != nil {
		t.Fatalf("create table grant_mc: %v", err)
	}
	if err := runSQLSimple(t, c, `GRANT SELECT ON TABLE public.grant_mc TO "MixedCase"`); err != nil {
		t.Fatalf("grant select on grant_mc to MixedCase: %v", err)
	}

	// Slice 338: a GRANT followed by a partial REVOKE must round-trip. PostgreSQL
	// REVOKE clears the named bits from the grantee's aclitem mask: GRANT SELECT,
	// INSERT then REVOKE INSERT leaves pg_class.relacl as `grantee=r/postgres`
	// (the lone SELECT), and pg_dump's buildACLCommands diffs that against
	// acldefault and re-emits only `GRANT SELECT ON TABLE public.revoke_t TO
	// revoke_role;` — NOT the revoked INSERT. goopg previously treated REVOKE as a
	// pure no-op, so the relacl still carried INSERT and the dump over-emitted
	// `GRANT INSERT, SELECT`. The REVOKE recorder (tryRecordTableRevoke) now
	// removes the bit so the materialized relacl reflects only what remains.
	if err := runSQLSimple(t, c, "CREATE ROLE revoke_role"); err != nil {
		t.Fatalf("create role revoke_role: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.revoke_t (a integer)"); err != nil {
		t.Fatalf("create table revoke_t: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT, INSERT ON TABLE public.revoke_t TO revoke_role"); err != nil {
		t.Fatalf("grant select,insert on revoke_t to revoke_role: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE INSERT ON TABLE public.revoke_t FROM revoke_role"); err != nil {
		t.Fatalf("revoke insert on revoke_t from revoke_role: %v", err)
	}

	// Slice 339: a schema GRANT followed by a partial REVOKE must round-trip. This
	// is the nspacl analogue of slice 338: GRANT USAGE, CREATE ON SCHEMA then
	// REVOKE CREATE leaves pg_namespace.nspacl as `revoke_sch_role=U/postgres` (the
	// lone USAGE), and pg_dump's buildACLCommands diffs that against
	// acldefault('n', owner) = "{postgres=UC/postgres}" and re-emits only `GRANT
	// USAGE ON SCHEMA revoke_sch TO revoke_sch_role;` — NOT the revoked CREATE.
	// goopg's REVOKE recorder (tryRecordTableRevoke) previously bailed on ON SCHEMA
	// (only table/sequence relacl was modelled), so the revoked CREATE survived in
	// nspacl and the dump over-emitted `GRANT CREATE, USAGE`. The recorder now
	// routes ON SCHEMA to recordSchemaRevoke, the mirror of recordSchemaGrant, so
	// the materialized nspacl reflects only what remains. A dedicated schema keeps
	// the revoke isolated from grant_sch (slice 335) and the `s` schema.
	if err := runSQLSimple(t, c, "CREATE SCHEMA revoke_sch"); err != nil {
		t.Fatalf("create schema revoke_sch: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE revoke_sch_role"); err != nil {
		t.Fatalf("create role revoke_sch_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE, CREATE ON SCHEMA revoke_sch TO revoke_sch_role"); err != nil {
		t.Fatalf("grant usage,create on schema revoke_sch to revoke_sch_role: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE CREATE ON SCHEMA revoke_sch FROM revoke_sch_role"); err != nil {
		t.Fatalf("revoke create on schema revoke_sch from revoke_sch_role: %v", err)
	}

	// Slice 340: an owner-side REVOKE-of-default must round-trip. PostgreSQL leaves
	// pg_class.relacl NULL while the owner holds its implicit default privileges;
	// the first `REVOKE <priv> ON TABLE t FROM postgres` materializes relacl as the
	// owner's full default set minus the revoked bits. `REVOKE TRIGGER ON TABLE
	// ownrev_t FROM postgres` yields relacl `{postgres=arwdDxm/postgres}` (the full
	// "arwdDxtm" minus 't'), and pg_dump's buildACLCommands diffs that against
	// acldefault('r', 10) and re-emits the transform as
	// `REVOKE ALL … FROM postgres;` + `GRANT <remaining> … TO postgres;` (verified
	// byte-identical to real pg_dump 18.3). Before this slice goopg's REVOKE recorder
	// only modelled non-owner grantees, so an owner revoke was silently dropped and
	// relacl stayed NULL → pg_dump emitted nothing, losing the privilege change on
	// restore. The recorder now materializes the owner's default ACL via
	// Catalog.MaterializeOwnerACL before removing the revoked bits.
	if err := runSQLSimple(t, c, "CREATE TABLE public.ownrev_t (a integer, b text)"); err != nil {
		t.Fatalf("create table ownrev_t: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE TRIGGER ON TABLE public.ownrev_t FROM postgres"); err != nil {
		t.Fatalf("revoke trigger on ownrev_t from postgres: %v", err)
	}

	// Slice 341: a full owner-side REVOKE ALL must round-trip as the empty aclitem
	// array. `REVOKE ALL ON TABLE ownrevall_t FROM postgres` strips the owner's
	// implicit default privileges, leaving relacl = `{}` (a non-NULL but empty
	// array, distinct from the NULL of a never-granted table). pg_dump's
	// buildACLCommands diffs {} against acldefault('r', 10) and emits a bare
	// `REVOKE ALL … FROM postgres;` with no re-GRANT (verified byte-identical to
	// real pg_dump 18.3). Before this slice the owner REVOKE ALL reverted relacl
	// to NULL (the owner entry was dropped entirely) so pg_dump emitted nothing,
	// silently restoring the owner's default privileges on restore. goopg now
	// records the emptied state (catalog.relACLEmptied) so relacl projects "{}".
	if err := runSQLSimple(t, c, "CREATE TABLE public.ownrevall_t (a integer, b text)"); err != nil {
		t.Fatalf("create table ownrevall_t: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE ALL ON TABLE public.ownrevall_t FROM postgres"); err != nil {
		t.Fatalf("revoke all on ownrevall_t from postgres: %v", err)
	}

	// Slice 342: a full owner-side REVOKE ALL ON SCHEMA must round-trip as the
	// empty nspacl array — the namespace analogue of slice 341. `REVOKE ALL ON
	// SCHEMA ownrevall_sch FROM postgres` strips the owner's implicit default
	// schema privileges (USAGE, CREATE), leaving pg_namespace.nspacl = `{}` (a
	// non-NULL but empty array, distinct from the NULL of a never-granted schema).
	// pg_dump's buildACLCommands diffs {} against acldefault('n', 10) =
	// "{postgres=UC/postgres}" and emits a bare `REVOKE ALL ON SCHEMA … FROM
	// postgres;` with no re-GRANT. Before this slice the schema REVOKE recorder
	// (recordSchemaRevoke) only modelled grantees, so an owner-side revoke was
	// silently dropped and nspacl stayed NULL → pg_dump emitted nothing, restoring
	// the owner's default schema privileges. recordSchemaRevoke now materializes
	// the owner's default schema ACL via Catalog.MaterializeOwnerACL before
	// removing the revoked bits (the type-agnostic relACLEmptied path shared with
	// slice 341).
	if err := runSQLSimple(t, c, "CREATE SCHEMA ownrevall_sch"); err != nil {
		t.Fatalf("create schema ownrevall_sch: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE ALL ON SCHEMA ownrevall_sch FROM postgres"); err != nil {
		t.Fatalf("revoke all on schema ownrevall_sch from postgres: %v", err)
	}

	// Slice 343: a full owner-side REVOKE ALL ON SEQUENCE must round-trip as the
	// empty relacl array — the sequence analogue of slice 341 (table) and slice 342
	// (schema). `REVOKE ALL ON SEQUENCE ownrevall_seq FROM postgres` strips the
	// owner's implicit default sequence privileges (USAGE, SELECT, UPDATE), leaving
	// pg_class.relacl = `{}` (a non-NULL but empty array, distinct from the NULL of
	// a never-granted sequence). pg_dump's buildACLCommands diffs {} against
	// acldefault('s', 10) = "{postgres=rwU/postgres}" and (objtype "SEQUENCE")
	// emits a bare `REVOKE ALL ON SEQUENCE public.ownrevall_seq FROM postgres;`
	// with NO re-GRANT (verified byte-identical to real pg_dump 18.3 above). The
	// server path needs NO new code: recordTableRevoke already passes
	// allSequencePrivileges to Catalog.MaterializeOwnerACL for an owner-side
	// `REVOKE … ON SEQUENCE … FROM postgres`, and the empty-array (relACLEmptied)
	// state plus its relaclTextLockedSeq rendering are object-type-agnostic. This
	// slice pins that end-to-end round-trip as a regression guard.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.ownrevall_seq"); err != nil {
		t.Fatalf("create sequence ownrevall_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE ALL ON SEQUENCE public.ownrevall_seq FROM postgres"); err != nil {
		t.Fatalf("revoke all on sequence ownrevall_seq from postgres: %v", err)
	}

	// Slice 344: owner-zero coexisting with a grantee. After a full owner-side
	// `REVOKE ALL ON TABLE ownerzero_t FROM postgres` empties relacl to {}, a
	// `GRANT SELECT … TO bob` re-materializes the array but the owner stays at
	// zero (absent): PostgreSQL stores `{bob=r/postgres}` with NO owner entry.
	// pg_dump's buildACLCommands diffs that against acldefault('r', 10) =
	// "{postgres=arwdDxtm/postgres}" and emits BOTH the owner's
	// `REVOKE ALL ON TABLE public.ownerzero_t FROM postgres;` AND
	// `GRANT SELECT ON TABLE public.ownerzero_t TO bob;`. goopg previously
	// re-inserted the owner's full default via the owner-default fallback
	// ({postgres=arwdDxtm/postgres,bob=r/postgres}), dropping the owner REVOKE on
	// round-trip and silently restoring the owner default on restore. The fix is
	// catalog-only: a GRANT to a non-owner no longer clears relACLEmptied, and
	// relaclTextLockedFor suppresses the leading owner entry when the owner is
	// zeroed. This pins the end-to-end round-trip as a regression guard.
	if err := runSQLSimple(t, c, "CREATE ROLE bob NOLOGIN"); err != nil {
		t.Fatalf("create role bob: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.ownerzero_t (a integer)"); err != nil {
		t.Fatalf("create table ownerzero_t: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE ALL ON TABLE public.ownerzero_t FROM postgres"); err != nil {
		t.Fatalf("revoke all on ownerzero_t from postgres: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.ownerzero_t TO bob"); err != nil {
		t.Fatalf("grant select on ownerzero_t to bob: %v", err)
	}

	// Slice 345: a function-level GRANT must round-trip through pg_dump from
	// pg_proc.proacl, the routine analogue of the table relacl slices (331+).
	// PostgreSQL's acldefault for a function grants EXECUTE to BOTH the owner and
	// PUBLIC, so `GRANT EXECUTE ON FUNCTION public.grantfn(integer) TO func_grantee`
	// materializes proacl as "{=X/postgres,postgres=X/postgres,func_grantee=X/postgres}".
	// pg_dump's getFuncs reads proacl, diffs it against acldefault('f', 10), and
	// buildACLCommands emits `GRANT ALL ON FUNCTION public.grantfn(integer) TO
	// func_grantee;` (EXECUTE is the sole function privilege, so the grantee's
	// full set renders as ALL). Before this slice goopg left proacl NULL for every
	// routine, so the function GRANT was silently dropped from the dump.
	if err := runSQLSimple(t, c, "CREATE ROLE func_grantee NOLOGIN"); err != nil {
		t.Fatalf("create role func_grantee: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.grantfn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function grantfn: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT EXECUTE ON FUNCTION public.grantfn(integer) TO func_grantee"); err != nil {
		t.Fatalf("grant execute on grantfn to func_grantee: %v", err)
	}

	// Slice 346: a function-level REVOKE … FROM PUBLIC must round-trip through
	// pg_dump from pg_proc.proacl, the routine REVOKE analogue of the table REVOKE
	// slices (338+). A function's implicit default proacl grants EXECUTE to BOTH
	// the owner and PUBLIC, so `REVOKE EXECUTE ON FUNCTION public.revokefn(integer)
	// FROM PUBLIC` materializes proacl as "{postgres=X/postgres}" (owner only;
	// PUBLIC's implicit EXECUTE removed). pg_dump's getFuncs diffs it against
	// acldefault('f', 10) and emits `REVOKE ALL ON FUNCTION public.revokefn(integer)
	// FROM PUBLIC;`. Before this slice goopg treated the function REVOKE as a pure
	// no-op, leaving proacl NULL, so the dump silently re-granted PUBLIC's default
	// EXECUTE on restore.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.revokefn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function revokefn: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE EXECUTE ON FUNCTION public.revokefn(integer) FROM PUBLIC"); err != nil {
		t.Fatalf("revoke execute on revokefn from public: %v", err)
	}

	// Slice 347: the owner-side function REVOKE, the counterpart of slice 346's
	// PUBLIC-side one. A function's acldefault('f', 10) grants EXECUTE to BOTH the
	// owner and PUBLIC, so `REVOKE EXECUTE ON FUNCTION public.ownrevfn(integer)
	// FROM postgres` materializes proacl as "{=X/postgres}" — PUBLIC's implicit
	// EXECUTE survives, the owner's is removed (distinct from a table/sequence,
	// whose default grants only the owner, so an owner REVOKE ALL empties to {}).
	// pg_dump's getFuncs diffs {=X/postgres} against acldefault('f', 10) and emits
	// `REVOKE ALL ON FUNCTION public.ownrevfn(integer) FROM postgres;` (verified
	// byte-identical to pg_dump 18.3). Before this slice goopg dropped the owner to
	// {} or NULL — re-granting the owner's default EXECUTE on restore and losing
	// PUBLIC's — because the function REVOKE recorder never materialized PUBLIC's
	// implicit default and the renderer re-added the absent owner.
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.ownrevfn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function ownrevfn: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE EXECUTE ON FUNCTION public.ownrevfn(integer) FROM postgres"); err != nil {
		t.Fatalf("revoke execute on ownrevfn from postgres: %v", err)
	}

	// Slice 348: a function-level GRANT … WITH GRANT OPTION must round-trip
	// through pg_dump from pg_proc.proacl, the routine analogue of the table
	// grant-option slice 332. `GRANT EXECUTE ON FUNCTION public.gofn(integer) TO
	// gofn_grantee WITH GRANT OPTION` materializes proacl as
	// "{=X/postgres,postgres=X/postgres,gofn_grantee=X*/postgres}" — the grantee's
	// EXECUTE carries the grant-option `*`. pg_dump's getFuncs diffs it against
	// acldefault('f', 10) and buildACLCommands routes the grant-option privilege
	// to its privswgo branch, emitting `GRANT ALL ON FUNCTION public.gofn(integer)
	// TO gofn_grantee WITH GRANT OPTION;` (EXECUTE is the sole function privilege,
	// so the grantee's full set renders as ALL; verified byte-identical to real
	// pg_dump 18.3). Before this slice goopg recorded the function grantee with a
	// plain GrantTablePrivilege, dropping the grant-option flag, so the restored
	// grant silently lost WITH GRANT OPTION.
	if err := runSQLSimple(t, c, "CREATE ROLE gofn_grantee NOLOGIN"); err != nil {
		t.Fatalf("create role gofn_grantee: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.gofn(integer) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function gofn: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT EXECUTE ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT OPTION"); err != nil {
		t.Fatalf("grant execute on gofn to gofn_grantee with grant option: %v", err)
	}

	// Slice 349: a sequence-level GRANT … WITH GRANT OPTION must round-trip through
	// pg_dump from the sequence's pg_class.relacl, the sequence analogue of the
	// table grant-option slice 332 and the function grant-option slice 348. Unlike
	// a function (whose sole privilege EXECUTE collapses to ALL), a sequence has
	// three distinct privileges (USAGE/SELECT/UPDATE), so a single USAGE grant
	// stays USAGE rather than rendering as ALL. `GRANT USAGE ON SEQUENCE
	// public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION` materializes relacl as
	// "{postgres=rwU/postgres,seq_wgo_role=U*/postgres}" — the grantee's USAGE
	// carries the grant-option `*`. pg_dump's getTables diffs it against
	// acldefault('s', 10) = "{postgres=rwU/postgres}" and buildACLCommands routes
	// the grant-option USAGE to its privswgo branch, emitting `GRANT USAGE ON
	// SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION;` (verified
	// byte-identical to real pg_dump 18.3). The grant-option `*` projection in the
	// relacl renderer and the privswgo split are object-type-agnostic, so the
	// sequence path needs no new engine work beyond slice 333's recorder plumbing
	// (which already threads WITH GRANT OPTION through the shared relation ACL
	// store). Before grant-option threading goopg dropped the flag, emitting a
	// plain `GRANT USAGE …;`.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.gowgo_seq"); err != nil {
		t.Fatalf("create sequence gowgo_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE seq_wgo_role"); err != nil {
		t.Fatalf("create role seq_wgo_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION"); err != nil {
		t.Fatalf("grant usage on gowgo_seq to seq_wgo_role with grant option: %v", err)
	}

	// Slice 357 (M0119-0004-ACLHEAP): a TYPE-level GRANT must round-trip through
	// pg_dump from pg_type.typacl. Unlike relacl/proacl/nspacl — whose catalogs
	// goopg serves *virtually* (the server records the GRANT in the ACL store and
	// the virtual builder re-projects it) — pg_type is heap-backed for PG18-standby
	// basebackup parity (M0097-0022), so a `GRANT USAGE ON TYPE … TO role` must run
	// through the executor (query.go excludes ON TYPE from the server fast path),
	// which updates the OID-keyed ACL store AND re-syncs the pg_type heap row's
	// typacl to a PG-native _aclitem ArrayType. A type's acldefault('T', owner)
	// grants USAGE to BOTH the owner and PUBLIC, so the GRANT materializes typacl as
	// "{postgres=U/postgres,=U/postgres,typg_grantee=U/postgres}". pg_dump's getTypes
	// reads typacl, diffs it against acldefault('T', 10), and buildACLCommands emits
	// `GRANT ALL ON TYPE public.gtype TO typg_grantee;` (USAGE is the sole type
	// privilege, so the grantee's full set renders as ALL — like a function's
	// EXECUTE). Before this milestone goopg baked typacl NULL on every pg_type row
	// and bailed on every TYPE GRANT, so it was silently dropped from the dump.
	if err := runSQLSimple(t, c, "CREATE TYPE public.gtype AS ENUM ('lo', 'hi')"); err != nil {
		t.Fatalf("create type gtype: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE typg_grantee NOLOGIN"); err != nil {
		t.Fatalf("create role typg_grantee: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE ON TYPE public.gtype TO typg_grantee"); err != nil {
		t.Fatalf("grant usage on type gtype to typg_grantee: %v", err)
	}

	// Slice 358 (M0119-0004-ACLHEAP, attacl half): a column-level GRANT must
	// round-trip from the heap-backed pg_attribute.attacl — the column analogue of
	// the TYPE grant (slice 357). `GRANT SELECT (cola) ON TABLE public.gcoltbl TO
	// colgrantee` runs through the executor (query.go excludes a parenthesised-column
	// GRANT from the server fast path), which updates the (relOID, attnum)-keyed
	// column ACL store and re-syncs the pg_attribute heap row's attacl to a PG-native
	// _aclitem array "{colgrantee=r/postgres}". A column has no acldefault('c', owner),
	// so attacl stays NULL until this GRANT. pg_dump's getColumnACLs reads attacl back
	// (decoded to canonical aclitemout text by the seqscan hook) and emits
	// `GRANT SELECT(cola) ON TABLE public.gcoltbl TO colgrantee;`.
	if err := runSQLSimple(t, c, "CREATE TABLE public.gcoltbl (cola int, colb int)"); err != nil {
		t.Fatalf("create table gcoltbl: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE colgrantee NOLOGIN"); err != nil {
		t.Fatalf("create role colgrantee: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT (cola) ON TABLE public.gcoltbl TO colgrantee"); err != nil {
		t.Fatalf("grant select(cola) on gcoltbl to colgrantee: %v", err)
	}

	// Slice 350: a sequence GRANT followed by a partial REVOKE must round-trip,
	// the sequence analogue of the table partial-REVOKE slice 338 and the schema
	// partial-REVOKE slice 339. A sequence exposes three privileges
	// (USAGE/SELECT/UPDATE), so `GRANT USAGE, SELECT ON SEQUENCE` then `REVOKE
	// SELECT` clears only the SELECT bit, leaving pg_class.relacl as
	// "{postgres=rwU/postgres,seqrev_role=U/postgres}" (the lone USAGE). pg_dump's
	// getTables diffs that against acldefault('s', 10) = "{postgres=rwU/postgres}"
	// and re-emits only `GRANT USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;`
	// — NOT the revoked SELECT. Verified byte-identical to real pg_dump 18.3. The
	// shared REVOKE recorder (tryRecordTableRevoke) already removes the bit from
	// the sequence's relacl (sequences share the relation ACL store with tables),
	// so this slice adds only a fixture + assert guarding against a regression
	// that would let the revoked SELECT survive and over-emit `GRANT SELECT, USAGE`.
	if err := runSQLSimple(t, c, "CREATE SEQUENCE public.seqrev_seq"); err != nil {
		t.Fatalf("create sequence seqrev_seq: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE seqrev_role"); err != nil {
		t.Fatalf("create role seqrev_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT USAGE, SELECT ON SEQUENCE public.seqrev_seq TO seqrev_role"); err != nil {
		t.Fatalf("grant usage,select on seqrev_seq to seqrev_role: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE SELECT ON SEQUENCE public.seqrev_seq FROM seqrev_role"); err != nil {
		t.Fatalf("revoke select on seqrev_seq from seqrev_role: %v", err)
	}

	// Slice 351 (setup): a table GRANT ALL collapses to the `ALL` keyword on
	// round-trip. `GRANT ALL ON TABLE public.grantall_t TO grantall_role` gives the
	// grantee every table privilege, so pg_class.relacl materializes as
	// "{postgres=arwdDxtm/postgres,grantall_role=arwdDxtm/postgres}" (all eight
	// letters, owner default unchanged). pg_dump's getTables diffs the grantee's
	// full set against acldefault('r', 10) and, recognising it equals
	// ACL_ALL_RIGHTS_RELATION, re-emits the single `GRANT ALL ON TABLE
	// public.grantall_t TO grantall_role;` — the `ALL` collapse, not an eight-way
	// privilege list. This is the table analogue of the function GRANT ALL (slice
	// 345) and sequence (slice 333) collapses, completing the GRANT ALL coverage
	// for the most-used object class. Verified byte-identical to real pg_dump 18.3
	// (relacl + ACL line captured above). The shared grant recorder expands ALL to
	// allTablePrivileges and renderACLLetters emits "arwdDxtm", so this slice adds
	// only a fixture + assert guarding against a regression that would drop a
	// privilege bit (then pg_dump would list the survivors explicitly instead of
	// `ALL`).
	if err := runSQLSimple(t, c, "CREATE TABLE public.grantall_t(id int)"); err != nil {
		t.Fatalf("create table grantall_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE grantall_role"); err != nil {
		t.Fatalf("create role grantall_role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT ALL ON TABLE public.grantall_t TO grantall_role"); err != nil {
		t.Fatalf("grant all on grantall_t to grantall_role: %v", err)
	}

	// Slice 352 (setup): two distinct grantees on one table each round-trip as
	// their own GRANT line. `GRANT SELECT … TO mg_role_a` then `GRANT INSERT … TO
	// mg_role_b` materializes relacl as
	// "{postgres=arwdDxtm/postgres,mg_role_a=r/postgres,mg_role_b=a/postgres}"
	// (owner default first, then one aclitem per grantee). pg_dump's
	// buildACLCommands walks the aclitem array and emits a separate
	// `GRANT <privs> ON TABLE … TO <grantee>;` per non-owner entry, so the dump
	// carries two GRANT lines — NOT a merged grantee list. goopg's
	// relaclTextLockedFor renders grantees in GRANT order (mg_role_a granted before
	// mg_role_b), matching PostgreSQL's append-on-grant aclitem array, so the relacl
	// text and both GRANT lines are byte-identical to real pg_dump 18.3 (relacl +
	// ACL lines captured). The catalog multi-grantee grant-order rendering is
	// unit-covered (TestRelaclText two-grantee case); this slice adds the
	// end-to-end pg_dump round-trip guarding the per-grantee fan-out.
	if err := runSQLSimple(t, c, "CREATE TABLE public.multigrant_t(id int)"); err != nil {
		t.Fatalf("create table multigrant_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE mg_role_a"); err != nil {
		t.Fatalf("create role mg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE mg_role_b"); err != nil {
		t.Fatalf("create role mg_role_b: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.multigrant_t TO mg_role_a"); err != nil {
		t.Fatalf("grant select on multigrant_t to mg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT INSERT ON TABLE public.multigrant_t TO mg_role_b"); err != nil {
		t.Fatalf("grant insert on multigrant_t to mg_role_b: %v", err)
	}

	// Slice 353 (setup): two grantees granted the SAME privilege on one table still
	// round-trip as two separate GRANT lines — PostgreSQL never merges grantees into
	// a single `GRANT … TO a, b;`, even when their privilege sets are byte-identical.
	// This is the most tempting case for a (wrong) grantee-merge optimization, so it
	// gets its own guard distinct from slice 352's differing-priv pair. `GRANT SELECT
	// … TO sg_role_a` then `GRANT SELECT … TO sg_role_b` materializes relacl as
	// "{postgres=arwdDxtm/postgres,sg_role_a=r/postgres,sg_role_b=r/postgres}" (owner
	// default first, then one aclitem per grantee). pg_dump's buildACLCommands walks
	// the aclitem array and emits one `GRANT SELECT ON TABLE … TO <grantee>;` per
	// non-owner entry, so the dump carries two identical-privilege GRANT lines.
	// relaclTextLockedFor renders grantees in GRANT order (sg_role_a granted before
	// sg_role_b), matching PostgreSQL's append-on-grant aclitem array, so the relacl
	// text and both GRANT lines are byte-identical to real pg_dump 18.3 (relacl + ACL
	// lines captured against PG 18.3). Test-only — NO engine change.
	if err := runSQLSimple(t, c, "CREATE TABLE public.samegrant_t(id int)"); err != nil {
		t.Fatalf("create table samegrant_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE sg_role_a"); err != nil {
		t.Fatalf("create role sg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE sg_role_b"); err != nil {
		t.Fatalf("create role sg_role_b: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.samegrant_t TO sg_role_a"); err != nil {
		t.Fatalf("grant select on samegrant_t to sg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.samegrant_t TO sg_role_b"); err != nil {
		t.Fatalf("grant select on samegrant_t to sg_role_b: %v", err)
	}

	// Slice 354 (setup): grantee aclitems are ordered by GRANT ORDER, NOT
	// alphabetically. Every prior multi-grantee slice (352/353) happened to grant
	// in alphabetical order, masking a divergence: goopg previously rendered relacl
	// grantees via sort.Strings, while PostgreSQL's aclupdate APPENDS a brand-new
	// grantee's aclitem to the end of the array (src/backend/utils/adt/acl.c), so
	// the array preserves grant order. This fixture grants in REVERSE alphabetical
	// order — `GRANT SELECT … TO og_role_z` then `… TO og_role_a` — which real PG
	// 18.3 materializes as
	// "{postgres=arwdDxtm/postgres,og_role_z=r/postgres,og_role_a=r/postgres}"
	// (z before a — verified against PG 18.3). The old sort.Strings rendering would
	// have emitted og_role_a before og_role_z, producing pg_dump GRANT lines in the
	// WRONG order vs real pg_dump. goopg now tracks per-relation grant order
	// (catalog.tableACLOrder) and renders grantees in that order, so both the relacl
	// text and the two GRANT lines round-trip byte-identically. This is the slice
	// that motivated the grant-order fix (engine change in internal/catalog).
	if err := runSQLSimple(t, c, "CREATE TABLE public.ordergrant_t(id int)"); err != nil {
		t.Fatalf("create table ordergrant_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE og_role_z"); err != nil {
		t.Fatalf("create role og_role_z: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE og_role_a"); err != nil {
		t.Fatalf("create role og_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.ordergrant_t TO og_role_z"); err != nil {
		t.Fatalf("grant select on ordergrant_t to og_role_z: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.ordergrant_t TO og_role_a"); err != nil {
		t.Fatalf("grant select on ordergrant_t to og_role_a: %v", err)
	}

	// Slice 355: REVOKE-then-re-GRANT moves a grantee to the END of the relacl
	// array. PostgreSQL's aclupdate (src/backend/utils/adt/acl.c) DELETES a
	// fully-revoked grantee's aclitem; a later GRANT to that same grantee APPENDS
	// a fresh aclitem at the end of the array — it does NOT restore the grantee's
	// original slot. So granting SELECT to rg_role_a, then rg_role_b, then
	// REVOKEing rg_role_a's only privilege (SELECT), then re-GRANTing INSERT to
	// rg_role_a, materializes relacl as
	// "{postgres=arwdDxtm/postgres,rg_role_b=r/postgres,rg_role_a=a/postgres}"
	// (b before a, even though a was granted first AND sorts first — verified
	// against PG 18.3 in ./postgres/local_install). This exercises the grant-order
	// teardown + re-append path landed in slice 354 (catalog.dropTableACLOrderRole
	// removes a on the full revoke, then the next GRANT re-appends it at the end);
	// the pre-fix sort.Strings rendering — and any naive teardown that preserved
	// a's original position — would both emit a before b, diverging from pg_dump.
	if err := runSQLSimple(t, c, "CREATE TABLE public.regrant_t(id int)"); err != nil {
		t.Fatalf("create table regrant_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE rg_role_a"); err != nil {
		t.Fatalf("create role rg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE rg_role_b"); err != nil {
		t.Fatalf("create role rg_role_b: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.regrant_t TO rg_role_a"); err != nil {
		t.Fatalf("grant select on regrant_t to rg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SELECT ON TABLE public.regrant_t TO rg_role_b"); err != nil {
		t.Fatalf("grant select on regrant_t to rg_role_b: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE SELECT ON TABLE public.regrant_t FROM rg_role_a"); err != nil {
		t.Fatalf("revoke select on regrant_t from rg_role_a: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT INSERT ON TABLE public.regrant_t TO rg_role_a"); err != nil {
		t.Fatalf("grant insert on regrant_t to rg_role_a: %v", err)
	}

	// Slice 324: an unconditional DO-NOTHING CREATE RULE must round-trip through
	// pg_dump. pg_dump's getRules reads pg_rewrite (rulename, ev_class, ev_type,
	// is_instead, ev_enabled) and dumpRule re-emits the rule from
	// pg_get_ruledef(oid) verbatim (pg_dump.c). Before this slice CREATE RULE was
	// a parse no-op and pg_rewrite was an empty stub, so a DO-NOTHING rule was
	// silently lost on dump/restore. goopg now records catalog.Table.Rules,
	// projects them through pg_rewrite, and reconstructs the CREATE RULE in its
	// pg_get_ruledef handler byte-identically to PG's PRETTYFLAG_INDENT form.
	// goopg does NOT implement the rewrite system — dump fidelity only. `rule_t`
	// carries one rule per (event, instead/also) form. Conditional WHERE and
	// action-command rules are a follow-up slice.
	if err := runSQLSimple(t, c, "CREATE TABLE public.rule_t (a integer, b text)"); err != nil {
		t.Fatalf("create table rule_t: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE RULE r_noins AS ON INSERT TO public.rule_t DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("create rule r_noins: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE RULE r_noupd AS ON UPDATE TO public.rule_t DO ALSO NOTHING"); err != nil {
		t.Fatalf("create rule r_noupd: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE RULE r_nodel AS ON DELETE TO public.rule_t DO NOTHING"); err != nil {
		t.Fatalf("create rule r_nodel: %v", err)
	}

	// Slice 325: a rule whose pg_rewrite.ev_enabled is not 'O' must round-trip the
	// `ALTER TABLE … {ENABLE ALWAYS|ENABLE REPLICA|DISABLE} RULE` clause. pg_dump's
	// dumpRule reads ev_enabled (getRules) and, for any non-'O' rule, emits the
	// ALTER TABLE *in addition to* the CREATE RULE (pg_dump.c). Before this slice
	// ALTER TABLE … RULE was a parse no-op and pg_rewrite hard-coded ev_enabled='O',
	// so a disabled/replica/always rule restored as plain-enabled. goopg now records
	// catalog.RuleInfo.Enabled and ATExecEnableDisableRule mutates it (pg_rewrite is
	// virtual, so the change is immediately visible to pg_dump). goopg implements no
	// rewrite system — dump fidelity only. Disable r_noupd, set r_nodel ENABLE
	// ALWAYS; r_noins stays origin ('O', no ALTER TABLE emitted).
	if err := runSQLSimple(t, c, "ALTER TABLE public.rule_t DISABLE RULE r_noupd"); err != nil {
		t.Fatalf("disable rule r_noupd: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE public.rule_t ENABLE ALWAYS RULE r_nodel"); err != nil {
		t.Fatalf("enable always rule r_nodel: %v", err)
	}

	// Slice 359: a CONDITIONAL DO-NOTHING CREATE RULE (`WHERE (qual) DO INSTEAD
	// NOTHING`) must round-trip through pg_dump — the follow-up slice 324 deferred.
	// pg_get_ruledef's PRETTYFLAG_INDENT layout puts the WHERE clause on its own
	// line with a 3-space indent and trails the DO action on that line:
	//   CREATE RULE r AS
	//       ON UPDATE TO public.rcond
	//      WHERE (old.a <> new.a) DO INSTEAD NOTHING;
	// (verified byte-identical vs real pg_dump 18.3, reference /tmp/du359_ref).
	// Before this slice goopg captured only the unconditional form (slice 324);
	// a conditional rule fell through to the CompatNoopStmt path and was silently
	// dropped on dump. goopg now parses the WHERE qual into an expression AST
	// (parser.CreateRuleStmt.Qual), deparses it via defaultExprToSQL (the same
	// renderer the trigger WHEN + index-predicate paths use → single-paren
	// `(old.a <> new.a)`), stores it on catalog.RuleInfo.Qual, and emits it from
	// the pg_get_ruledef handler. goopg implements no rewrite system — dump
	// fidelity only. `rcond` carries both a parenthesized (UPDATE) and a
	// no-paren (DELETE) source qual; both must normalize to the canonical form.
	if err := runSQLSimple(t, c, "CREATE TABLE public.rcond (a integer, b integer)"); err != nil {
		t.Fatalf("create table rcond: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE RULE rcond_upd AS ON UPDATE TO public.rcond WHERE (old.a <> new.a) DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("create rule rcond_upd: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE RULE rcond_del AS ON DELETE TO public.rcond WHERE old.b > 0 DO INSTEAD NOTHING"); err != nil {
		t.Fatalf("create rule rcond_del: %v", err)
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

	// Slice 303: an IDENTITY column declared WITH a non-default `(sequence_options)`
	// clause must round-trip EVERY option through the backing sequence, not just
	// START WITH. pg_dump reads the identity sequence's definition from pg_sequence
	// (seqstart/seqincrement/seqmin/seqmax/seqcache/seqcycle) and renders it inside
	// the `ADD GENERATED ... AS IDENTITY (...)` block. goopg's identity parser
	// previously captured ONLY START WITH (scanning the parenthesised clause for the
	// `start` keyword) and hard-coded the backing sequence to `increment=1,
	// cycle=false, cache=1, type-default min/max` — so `INCREMENT BY 5`, `MINVALUE`,
	// `MAXVALUE`, `CACHE n`, and `CYCLE` were SILENTLY DROPPED and the column
	// restored with the wrong step/bounds. The parser now parses the full sequence-
	// option grammar (mirroring CREATE SEQUENCE: parseCreateSequenceTail) into new
	// ColumnDef.Identity{Increment,Min,Max,Cache,Cycle} fields, the executor threads
	// them to RegisterSequence + SetSequenceCache, and the existing slice-120 dump
	// path re-emits them. `idrich` exercises ALL options together
	// (ascending, fully-bounded, CYCLE); `idbd` exercises `BY DEFAULT` with an
	// explicit increment and the explicit `NO MINVALUE / NO MAXVALUE` (which must
	// keep the type defaults → `NO MINVALUE / NO MAXVALUE` in the dump, not a
	// spurious bound). Verified byte-identical to real pg_dump 18.3. Each carries it
	// on its own table so the heavily-asserted tables are untouched.
	if err := runSQLSimple(t, c, "CREATE TABLE public.idrich (id integer GENERATED ALWAYS AS IDENTITY "+
		"(START WITH 100 INCREMENT BY 5 MINVALUE 10 MAXVALUE 9999 CACHE 7 CYCLE), x text)"); err != nil {
		t.Fatalf("create table idrich: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.idbd (id bigint GENERATED BY DEFAULT AS IDENTITY "+
		"(INCREMENT BY 2 NO MINVALUE NO MAXVALUE), y text)"); err != nil {
		t.Fatalf("create table idbd: %v", err)
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

	// Slice 243: a stand-alone composite type (`CREATE TYPE x AS (...)`) must
	// round-trip. Slice 242 made the type visible to pg_dump's getTypes via the
	// pg_type row (typtype='c'), but left typrelid=0 so dumpCompositeType found no
	// fields. This slice seeds the implicit pg_class relation (relkind='c') +
	// one pg_attribute row per field, sets typrelid → that relation, so
	// dumpCompositeType walks typrelid → pg_class → pg_attribute and re-emits the
	// field list via format_type(atttypid, atttypmod). `addr` carries scalar
	// fields whose declared spellings (`int`, `text`) round-trip to their
	// canonical names (`integer`, `text`).
	if err := runSQLSimple(t, c, "CREATE TYPE public.addr AS (street text, zip int)"); err != nil {
		t.Fatalf("create type addr: %v", err)
	}

	// Slice 247: a composite type whose FIELD carries a TYPMOD (numeric(10,2),
	// varchar(8)) must round-trip the declared precision/length. dumpCompositeType
	// renders each field via format_type(atttypid, atttypmod), so the encoded
	// typmod must survive. goopg's CREATE TYPE parser previously broke a composite
	// field's type collection on the FIRST ','/')' — which is *inside* the typmod
	// for numeric(10,2)/varchar(8) — so the field list mis-parsed and the type
	// never reached the catalog intact. The parser now balances parens, capturing
	// the full `numeric ( 10 , 2 )` token run; executor.parseCompositeFieldType
	// already decoded that space-joined form into base + atttypmod (it had been
	// unreachable via SQL). Real pg_dump 18.3 renders numeric(10,2) and
	// `character varying(8)` (the canonical spelling of varchar).
	if err := runSQLSimple(t, c, "CREATE TYPE public.money_amt AS (amount numeric(10,2), code varchar(8))"); err != nil {
		t.Fatalf("create type money_amt: %v", err)
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
	// DU-002 slice 363: a COMPOUND (`VALUE > 0 AND VALUE < 100`) or FUNCTION-CALL
	// (`length(VALUE) > 0`) generic domain CHECK. Like the table-CHECK twin
	// (slice 362), PG's pg_get_constraintdef re-deparses the stored node with
	// get_rule_expr, fully parenthesizing every sub-node and deparsing the value
	// placeholder as the uppercase keyword VALUE: `CHECK (((VALUE > 0) AND (VALUE
	// < 100)))` and `CHECK ((length(VALUE) > 0))`. goopg previously emitted its
	// token-reconstructed raw text wrapped once (`CHECK ((VALUE > 0 AND VALUE <
	// 100))`), which diverged on the per-operand parens and the call-paren spacing.
	// renderDomainCheckPredicate now re-parses + deparses through the same
	// fully-parenthesizing renderer the table path uses, upcasing the placeholder.
	// Verified byte-identical against a throwaway real PG 18.3 cluster. The
	// single-comparison posqty/named_chk above stay byte-unchanged. (A negative
	// literal like `VALUE < -5` would dump `'-5'::integer` — the type-blind
	// literal-cast gap, deferred — so the compound case uses positive literals.)
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.dchkand AS integer CHECK (VALUE > 0 AND VALUE < 100)"); err != nil {
		t.Fatalf("create domain dchkand: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.dchkfn AS text CHECK (length(VALUE) > 0)"); err != nil {
		t.Fatalf("create domain dchkfn: %v", err)
	}
	// DU-002 slice 364: a unary minus applied DIRECTLY to a numeric literal — in a
	// CHECK predicate, a column DEFAULT, an expression-index key, and a domain
	// CHECK. PG's parser folds `-N` into a negative typed Const (gram.y doNegate)
	// that ruleutils.c get_const_expr deparses as the quoted-value-plus-cast
	// `'-N'::type` so it re-parses as ONE constant (not a constant-plus-operator):
	// an integer literal whose negation fits int4 → `::integer`, a wider one →
	// `::bigint`, a decimal → `::numeric`. The cast type is the LITERAL's own type,
	// NOT the column type — a bigint column's `<> -100` still dumps `'-100'::integer`,
	// and a bigint `DEFAULT -9000000000` dumps `'-9000000000'::bigint`. goopg
	// previously emitted the re-parseable bare `-N` (slice 302 — semantically equal
	// but byte-diverging), the deferred gap behind slices 302/360(a)/362(b)/363.
	// parser.NegatedLiteralSQL now reproduces PG's exact form in BOTH deparse twins
	// (catalog.formatExprForAttrdef + executor.defaultExprToSQL). Verified
	// byte-identical against a throwaway real PG 18.3 cluster. negdef above keeps the
	// COMPOUND `(- (1 + 2))` cases (PG does not fold those).
	if err := runSQLSimple(t, c, "CREATE DOMAIN public.dchkneg AS integer CHECK (VALUE < -5)"); err != nil {
		t.Fatalf("create domain dchkneg: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.neglit (a integer CHECK (a < -5), b numeric CHECK (b > -3.5), c bigint CHECK (c <> -100), d integer DEFAULT -7, e bigint DEFAULT -9000000000)"); err != nil {
		t.Fatalf("create table neglit with negative-literal check/default: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE INDEX neglit_ix ON public.neglit ((a + -7))"); err != nil {
		t.Fatalf("create index neglit_ix on negative-literal expression: %v", err)
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
	// Slice 248: a composite type whose FIELD is a user-defined DOMAIN. The parser
	// records the raw domain name in CompositeField.ColType (composite fields,
	// unlike table columns, are NOT resolved to the base type at CREATE TYPE), so
	// it folds to the text fallback in parseCompositeFieldType. The pg_attribute
	// builder re-resolves it to the domain's pg_type OID (cat.LookupDomain), so
	// pg_dump's dumpCompositeType renders the field via format_type as the
	// schema-qualified domain name (public.zipcode / public.numd) rather than the
	// base type. The domains must already exist, so this composite is created after
	// the domain block above.
	if err := runSQLSimple(t, c, "CREATE TYPE public.dom_comp AS (z zipcode, n numd)"); err != nil {
		t.Fatalf("create type dom_comp: %v", err)
	}
	// Slice 249: a composite type whose FIELD is itself another user-defined
	// COMPOSITE type (a nested composite). Like the enum/domain field cases, the
	// parser records the raw composite name (`addr`) in CompositeField.ColType, so
	// it folds to the text fallback in parseCompositeFieldType. The pg_attribute
	// builder re-resolves it to the inner composite's pg_type OID
	// (cat.LookupCompositeType) with the pass-by-ref varlena layout, so
	// dumpCompositeType renders the field via format_type as `public.addr` rather
	// than `text`. public.addr was created above, so this composite follows it
	// (lower OID dumps first → no forward reference).
	if err := runSQLSimple(t, c, "CREATE TYPE public.nested_comp AS (label text, location addr)"); err != nil {
		t.Fatalf("create type nested_comp: %v", err)
	}
	// Slice 250: a composite field whose declared type is an ARRAY of another
	// user-defined composite (`stops addr[]`). Like the scalar nested-composite
	// case it folds to the text fallback in parseCompositeFieldType, but with the
	// array suffix detected. The pg_attribute builder re-resolves it to the inner
	// composite's auto-generated array OID (cat.LookupCompositeType().ArrayOID),
	// attndims=1, varlena-array layout, so dumpCompositeType renders the field via
	// format_type as `public.addr[]` rather than `text[]`. public.addr was created
	// above, so this composite follows it (lower OID dumps first).
	if err := runSQLSimple(t, c, "CREATE TYPE public.route AS (name text, stops addr[])"); err != nil {
		t.Fatalf("create type route: %v", err)
	}
	// Slice 252: a composite field whose declared type is an ARRAY of a
	// user-defined DOMAIN (`zips zipcode[]`). Like the composite-array case it
	// folds to the text fallback in parseCompositeFieldType with the array suffix
	// detected; the pg_attribute builder re-resolves it to the domain's
	// auto-generated array OID (cat.LookupDomain().ArrayOID), attndims=1,
	// varlena-array layout with the base element's alignment, so dumpCompositeType
	// renders the field via format_type as `public.zipcode[]` rather than `text[]`.
	// public.zipcode (a text domain) was created above.
	if err := runSQLSimple(t, c, "CREATE TYPE public.dom_arr_comp AS (label text, zips zipcode[])"); err != nil {
		t.Fatalf("create type dom_arr_comp: %v", err)
	}
	// Slice 257: a composite field with an explicit per-field COLLATE must round
	// through pg_dump. The field's pg_attribute.attcollation shadows the type
	// default (text typcollation=100 → C=950 / POSIX=951), so pg_dump's
	// dumpCompositeType reports `attcollation <> typcollation` and re-emits a
	// `COLLATE pg_catalog."<name>"` clause inline; the uncollated field `b` must
	// stay clause-free.
	if err := runSQLSimple(t, c, `CREATE TYPE public.coll_comp AS (a text COLLATE "C", b text, p text COLLATE "POSIX")`); err != nil {
		t.Fatalf("create type coll_comp: %v", err)
	}
	// Slice 253: ALTER TYPE … ADD ATTRIBUTE appends a field to an existing
	// composite type. The new attribute must round-trip through pg_dump's
	// dumpCompositeType, which walks pg_type.typrelid → pg_class → pg_attribute;
	// goopg re-syncs the full composite heap row set (OIDs stable) with the
	// appended field. The added type carries a typmod to prove the type-token
	// collection survives the inner `(`,`)`.
	if err := runSQLSimple(t, c, "CREATE TYPE public.alt_comp AS (a integer)"); err != nil {
		t.Fatalf("create type alt_comp: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp ADD ATTRIBUTE b text"); err != nil {
		t.Fatalf("alter type alt_comp add attribute b: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp ADD ATTRIBUTE c numeric(10,2)"); err != nil {
		t.Fatalf("alter type alt_comp add attribute c: %v", err)
	}
	// Slice 254: RENAME ATTRIBUTE renames an existing field in place; the renamed
	// attribute (b -> b_renamed) must dump under its new name.
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp RENAME ATTRIBUTE b TO b_renamed"); err != nil {
		t.Fatalf("alter type alt_comp rename attribute b: %v", err)
	}
	// Slice 255: DROP ATTRIBUTE removes a field in place; the dropped attribute
	// (c) must no longer appear in the dump. DROP ATTRIBUTE IF EXISTS of an
	// absent field is a no-op NOTICE (not an error), exercising that branch.
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp DROP ATTRIBUTE c"); err != nil {
		t.Fatalf("alter type alt_comp drop attribute c: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp DROP ATTRIBUTE IF EXISTS nonexistent"); err != nil {
		t.Fatalf("alter type alt_comp drop attribute if exists nonexistent: %v", err)
	}
	// Slice 256: ALTER ATTRIBUTE … TYPE re-types a field in place. After this
	// alt_comp is (a bigint, b_renamed numeric(12,3)): `a` widens integer→bigint
	// (SET DATA TYPE spelling), `b_renamed` becomes numeric with a typmod to prove
	// the paren-tracked type-token collection survives the inner `(`,`)` and that
	// the re-synced heap re-resolves atttypid/atttypmod.
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp ALTER ATTRIBUTE a SET DATA TYPE bigint"); err != nil {
		t.Fatalf("alter type alt_comp alter attribute a type bigint: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER TYPE public.alt_comp ALTER ATTRIBUTE b_renamed TYPE numeric(12,3)"); err != nil {
		t.Fatalf("alter type alt_comp alter attribute b_renamed type numeric: %v", err)
	}
	// Slice 258: ALTER TYPE … ADD ATTRIBUTE with a per-attribute COLLATE. The new
	// field `cc` is text COLLATE "C"; its attcollation (950) shadows text's
	// typcollation (100) on the re-synced heap, so dumpCompositeType must re-emit
	// `COLLATE pg_catalog."C"` inline — mirroring the CREATE TYPE path (slice 257)
	// but reached through the ADD ATTRIBUTE re-sync. After this alt_comp is
	// (a bigint, b_renamed numeric(12,3), cc text COLLATE "C").
	if err := runSQLSimple(t, c, `ALTER TYPE public.alt_comp ADD ATTRIBUTE cc text COLLATE "C"`); err != nil {
		t.Fatalf("alter type alt_comp add attribute cc collate C: %v", err)
	}
	// Slice 259: ALTER ATTRIBUTE … TYPE with a per-attribute COLLATE. Re-typing
	// `cc` (currently text COLLATE "C") to text COLLATE "POSIX" replaces the
	// attribute's type AND collation in place, so the re-synced heap row stamps
	// attcollation=951 (POSIX); dumpCompositeType must now re-emit
	// `COLLATE pg_catalog."POSIX"` for cc. This exercises capturing the COLLATE
	// clause on the ALTER ATTRIBUTE path (slice 256 stub-consumed it).
	if err := runSQLSimple(t, c, `ALTER TYPE public.alt_comp ALTER ATTRIBUTE cc TYPE text COLLATE "POSIX"`); err != nil {
		t.Fatalf("alter type alt_comp alter attribute cc type text collate POSIX: %v", err)
	}
	// Slice 260: multi-subcommand ALTER TYPE — a single statement comma-combining
	// ADD/DROP/ALTER ATTRIBUTE actions (PG's alter_type_cmds list). goopg folds
	// every action into one field slice (left to right) and re-syncs the
	// composite heap once, so all of them must round-trip together. Starting from
	// (a integer, b text, c numeric(10,2)) the combined statement adds d, drops b,
	// re-types c→numeric(12,3), and adds a collated e, leaving the final shape
	// (a integer, c numeric(12,3), d text, e text COLLATE "C"). A fresh type keeps
	// this fixture independent of the alt_comp chain above.
	if err := runSQLSimple(t, c, "CREATE TYPE public.multi_comp AS (a integer, b text, c numeric(10,2))"); err != nil {
		t.Fatalf("create type multi_comp: %v", err)
	}
	if err := runSQLSimple(t, c, `ALTER TYPE public.multi_comp ADD ATTRIBUTE d text, DROP ATTRIBUTE b, ALTER ATTRIBUTE c TYPE numeric(12,3), ADD ATTRIBUTE e text COLLATE "C"`); err != nil {
		t.Fatalf("alter type multi_comp multi-subcommand: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.dom (id integer PRIMARY KEY, zip zipcode, zip_nn zipcode_nn, q qty, lbl label, vc vcdef, v20 vc20, c4 ch4, nd numd, pq posqty, nc named_chk, dca dchkand, dcf dchkfn, co colr, ni named_in, vci vc_in, vc20i vc20_in, chi ch_in, ii i_in, iin i_in_n, ni2 n_in, bi b_in, boi bo_in, di d_in, ri r_in, f8i f8_in, tsi ts_in, tmi tm_in, ui u_in, sii si_in, byi by_in, ineti inet_in, maci mac_in, mac8i mac8_in, cidri cidr_in, nmi nm_in, jbi jb_in, jsi js_in, xmli xml_in, oidi oid_in, biti bit_in, vbiti vbit_in, lsni lsn_in, tidi tid_in, xidi xid_in, cidi cid_in, ivi iv_in, mnyi mny_in, eni enum_in, tstzi tstz_in, ttzi ttz_in, x8i x8_in, i2vi i2v_in, oveci ovec_in, tsvi tsv_in, tsqi tsq_in, zips zipcode[])"); err != nil {
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

	// Slice 301: a function whose parameter DEFAULT is a NESTED-ARITHMETIC binary
	// op (`a integer DEFAULT (1 + 2) * 3`). This is the FOURTH (and last) deparse
	// context fed by executor.defaultExprToSQL — after slice 298's index predicate,
	// slice 299's expression-index column, and slice 300's partition key. The
	// function-arg-default path differs from those three: pg_dump reconstructs the
	// signature from pg_get_function_arguments(oid), and PG's print_function_arguments
	// (ruleutils.c:3428) appends the default via `deparse_expression(expr, NIL, false,
	// false)` — which, UNLIKE pg_get_partkeydef (slice 300), adds NO extra `(%s)`
	// wrap. The full parenthesization comes entirely from goopg storing the
	// deparse-canonical `((1 + 2) * 3)` in catalog.Routine.ArgDefaults (the parser's
	// `a.Default` → defaultExprToSQL at CREATE FUNCTION time, operators_ddl.go:7138),
	// matching get_oper_expr's non-pretty mode which wraps every OpExpr in parens.
	// So this slice is FIXTURE-ONLY: defaultExprToSQL's slice-298 BinaryOp fix already
	// produces the correct `((1 + 2) * 3)`, and buildFunctionArguments emits it
	// verbatim after ` DEFAULT `. Verified byte-identical vs real pg_dump 18.3:
	//   CREATE FUNCTION public.add_calcdef(a integer DEFAULT ((1 + 2) * 3)) RETURNS integer
	//       LANGUAGE sql
	//       AS $_$ SELECT $1 $_$;
	// A precedence-corrupted render (`DEFAULT 1 + 2 * 3`, re-parsing as 1+(2*3)=7
	// not (1+2)*3=9) or a one-paren-short `DEFAULT (1 + 2) * 3` would surface in the
	// assertion below. The `$1` body forces the `$_$` delimiter (same as add_default).
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.add_calcdef(a integer DEFAULT (1 + 2) * 3) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function add_calcdef: %v", err)
	}

	// Slice 302 (executor twin): a FUNCTION-ARGUMENT default with a UNARY MINUS on
	// a compound operand. pg_dump reconstructs the signature from
	// pg_get_function_arguments(oid), which goopg answers from the deparse-canonical
	// string stored in catalog.Routine.ArgDefaults (the parser's `a.Default` →
	// executor.defaultExprToSQL at CREATE FUNCTION time). Before slice 302 the
	// unary minus matched no arm (OpUnaryNeg vs the dead OpSub case) and stored a Go
	// pointer string; it now renders `(- (1 + 2))`, byte-identical to real pg_dump
	// 18.3 (verified):
	//   CREATE FUNCTION public.fneg(x integer DEFAULT (- (1 + 2))) RETURNS integer
	// The `$1` body forces the `$_$` delimiter (same as add_calcdef/add_default).
	if err := runSQLSimple(t, c, "CREATE FUNCTION public.fneg(x integer DEFAULT -(1 + 2)) RETURNS integer LANGUAGE sql AS $$ SELECT $1 $$"); err != nil {
		t.Fatalf("create function fneg: %v", err)
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
		// Slice 315: COMMENT ON STATISTICS must survive the dump now that the
		// extended-statistics object itself round-trips (slice 314). goopg already
		// parsed COMMENT ON STATISTICS (parser.go) and execCommentOn keys it under
		// pg_statistic_ext (classoid 3381, via LookupStatistics) — but with no
		// dumpable statistics object before slice 314 the collectComments path was
		// never reachable, so this was untested. pg_dump's dumpStatisticsExt calls
		// dumpComment for the stats object, which finds the pg_description row
		// (classoid=3381, objoid=stxoid) and re-emits
		// `COMMENT ON STATISTICS <nsp>.<name> IS '...'`.
		"COMMENT ON STATISTICS public.statext_all IS 'a statistics comment'",
		// Slice 370: COMMENT ON TRIGGER must survive the dump. Before this slice,
		// parseCommentOnTail had no TRIGGER branch, so the server silently swallowed
		// it and the comment never reached pg_description. The parser now recognises
		// `TRIGGER <name> ON <table>` (the shape pg_dump itself emits), and
		// execCommentOn resolves the trigger by name on the named table and keys it
		// under pg_trigger (classoid 2620, objsubid 0). pg_dump's dumpTrigger calls
		// dumpComment with the trigger's catalogId (tableoid=2620), finds the
		// pg_description row, and re-emits
		// `COMMENT ON TRIGGER <name> ON <schema>.<table> IS '...'`. trg_biu is the
		// BEFORE INSERT OR UPDATE trigger on public.trig_t created above (slice 319).
		"COMMENT ON TRIGGER trg_biu ON public.trig_t IS 'a trigger comment'",
		// Slice 371: COMMENT ON POLICY must survive the dump. Before this slice,
		// parseCommentOnTail had no POLICY branch, so the server silently swallowed
		// it and the comment never reached pg_description. The parser now recognises
		// `POLICY <name> ON <table>` (the shape pg_dump itself emits), and
		// execCommentOn resolves the policy by name on the named table and keys it
		// under pg_policy (classoid 3256, objsubid 0). pg_dump's dumpPolicy calls
		// dumpComment with the policy's catalogId (tableoid=3256), finds the
		// pg_description row, and re-emits
		// `COMMENT ON POLICY <name> ON <schema>.<table> IS '...'`. p_simple is the
		// permissive ALL policy on public.pol_t created above (slice 323).
		"COMMENT ON POLICY p_simple ON public.pol_t IS 'a policy comment'",
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
		// **Slice 225 + 226 + 227 + 228 + 229 + 230 + 231 + 232 + 233 + 234 + 235 + 236 + 237 + 238 + 239 closed (asserted):**
		// a second RELOPT_KIND_TOAST boolean (toast.vacuum_truncate), the first
		// RELOPT_KIND_TOAST integer (toast.autovacuum_vacuum_threshold), two
		// RELOPT_KIND_TOAST reals (toast.autovacuum_vacuum_scale_factor,
		// toast.autovacuum_vacuum_cost_delay), a second RELOPT_KIND_TOAST integer
		// (toast.autovacuum_vacuum_cost_limit), six RELOPT_KIND_TOAST
		// autovacuum-age integers (toast.autovacuum_freeze_min_age,
		// toast.autovacuum_freeze_max_age, toast.autovacuum_freeze_table_age,
		// toast.autovacuum_multixact_freeze_min_age,
		// toast.autovacuum_multixact_freeze_max_age,
		// toast.autovacuum_multixact_freeze_table_age), and the
		// RELOPT_KIND_TOAST logging integer (toast.log_autovacuum_min_duration,
		// floor -1 not 0), the two RELOPT_KIND_TOAST insert/max-vacuum integers
		// (toast.autovacuum_vacuum_insert_threshold and
		// toast.autovacuum_vacuum_max_threshold, both floor -1 not 0), and the
		// RELOPT_KIND_TOAST insert-vacuum real
		// (toast.autovacuum_vacuum_insert_scale_factor, range 0.0–100.0), the
		// RELOPT_KIND_TOAST page-fraction real
		// (toast.vacuum_max_eager_freeze_failure_rate, range 0.0–1.0), and the only
		// RELOPT_KIND_TOAST enum (toast.vacuum_index_cleanup, stored verbatim) on the
		// same table exercise the multi-element toast reloptions array. The
		// synthesized TOAST relation's reloptions are
		// `{autovacuum_enabled=false,vacuum_truncate=false,autovacuum_vacuum_threshold=100,autovacuum_vacuum_scale_factor=2.5,autovacuum_vacuum_cost_delay=10.5,autovacuum_vacuum_cost_limit=500,autovacuum_freeze_min_age=200000000,autovacuum_freeze_max_age=500000000,autovacuum_freeze_table_age=0,autovacuum_multixact_freeze_min_age=150000000,autovacuum_multixact_freeze_max_age=500000000,autovacuum_multixact_freeze_table_age=250000000,log_autovacuum_min_duration=-1,autovacuum_vacuum_insert_threshold=1000,autovacuum_vacuum_max_threshold=2000,autovacuum_vacuum_insert_scale_factor=1.5,vacuum_max_eager_freeze_failure_rate=0.5,vacuum_index_cleanup=on}`
		// (code order), so pg_dump emits all eighteen prefixed options in one WITH clause.
		if !strings.Contains(res.Stdout, "WITH (toast.autovacuum_enabled='false', toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100', toast.autovacuum_vacuum_scale_factor='2.5', toast.autovacuum_vacuum_cost_delay='10.5', toast.autovacuum_vacuum_cost_limit='500', toast.autovacuum_freeze_min_age='200000000', toast.autovacuum_freeze_max_age='500000000', toast.autovacuum_freeze_table_age='0', toast.autovacuum_multixact_freeze_min_age='150000000', toast.autovacuum_multixact_freeze_max_age='500000000', toast.autovacuum_multixact_freeze_table_age='250000000', toast.log_autovacuum_min_duration='-1', toast.autovacuum_vacuum_insert_threshold='1000', toast.autovacuum_vacuum_max_threshold='2000', toast.autovacuum_vacuum_insert_scale_factor='1.5', toast.vacuum_max_eager_freeze_failure_rate='0.5', toast.vacuum_index_cleanup='on')") {
			t.Errorf("pg_dump dropped a toast.* reloption; missing %q\n  full stdout=%q", "WITH (toast.autovacuum_enabled='false', toast.vacuum_truncate='false', toast.autovacuum_vacuum_threshold='100', toast.autovacuum_vacuum_scale_factor='2.5', toast.autovacuum_vacuum_cost_delay='10.5', toast.autovacuum_vacuum_cost_limit='500', toast.autovacuum_freeze_min_age='200000000', toast.autovacuum_freeze_max_age='500000000', toast.autovacuum_freeze_table_age='0', toast.autovacuum_multixact_freeze_min_age='150000000', toast.autovacuum_multixact_freeze_max_age='500000000', toast.autovacuum_multixact_freeze_table_age='250000000', toast.log_autovacuum_min_duration='-1', toast.autovacuum_vacuum_insert_threshold='1000', toast.autovacuum_vacuum_max_threshold='2000', toast.autovacuum_vacuum_insert_scale_factor='1.5', toast.vacuum_max_eager_freeze_failure_rate='0.5', toast.vacuum_index_cleanup='on')", res.Stdout)
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
		// **Slice 300 closed (asserted):** a nested-arithmetic EXPRESSION partition
		// key — the third deparse context fed by defaultExprToSQL (index predicate
		// → slice 298, index column → slice 299, partition key → here), reached via
		// pg_get_partkeydef. pg_get_partkeydef_worker wraps each non-function key in
		// `(%s)`, so `RANGE (((a + b) * c))` dumps as the FOUR-paren form
		// `RANGE ((((a + b) * c)))` (verified byte-identical vs real pg_dump 18.3).
		// goopg previously emitted only THREE parens (no `(%s)` wrap). The exact
		// four-paren clause is a tight guard: the three-paren bug form is NOT a
		// superstring of it, so a missing wrap fails this assertion.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.pexpr (") ||
			!strings.Contains(res.Stdout, "PARTITION BY RANGE ((((a + b) * c)))") {
			t.Errorf("pg_dump dropped/mangled the expression partition key; want CREATE TABLE public.pexpr / PARTITION BY RANGE ((((a + b) * c)))\n  full stdout=%q", res.Stdout)
		}
		// Negative guard on the INNER BinaryOp parenthesization (slice 298): the
		// precedence-corrupt `a + b * c` (the `+` left un-parenthesized) must NOT
		// appear — the correct render is `((a + b) * c)`, where a `)` always
		// separates `b` from ` *`, so this literal is absent unless the inner wrap
		// regressed.
		if strings.Contains(res.Stdout, "RANGE ((a + b * c))") {
			t.Errorf("pg_dump emitted a precedence-corrupt expression partition key (inner BinaryOp parens dropped); found %q\n  full stdout=%q", "RANGE ((a + b * c))", res.Stdout)
		}
		// **Slice 262 closed (asserted):** a MULTI-COLUMN RANGE bound with a
		// MINVALUE/MAXVALUE open edge on a non-leading column. FormatPartitionBound
		// joins the parallel From/ToValueLiterals tuples with `, `, so the two-element
		// bounds must re-emit verbatim with bare keywords (slices 169/261). Assert (1)
		// the parent's multi-column key clause `PARTITION BY RANGE (a, b)`, and (2) the
		// child's mixed open/concrete two-element bound, proving the suffix MINVALUE/
		// MAXVALUE survive un-quoted alongside the concrete leading `10`.
		if !strings.Contains(res.Stdout, "CREATE TABLE public.pmc (") ||
			!strings.Contains(res.Stdout, "PARTITION BY RANGE (a, b)") {
			t.Errorf("pg_dump dropped/mangled the multi-column RANGE parent; missing CREATE TABLE public.pmc / PARTITION BY RANGE (a, b)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pmc_lo FOR VALUES FROM (MINVALUE, MINVALUE) TO (10, MAXVALUE)") {
			t.Errorf("pg_dump dropped/mangled the multi-column RANGE bound; want %q\n  full stdout=%q", "ATTACH PARTITION public.pmc_lo FOR VALUES FROM (MINVALUE, MINVALUE) TO (10, MAXVALUE)", res.Stdout)
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
		// **Slice 263 (asserted):** WIDE multi-level partition tree. Two fan-out
		// shapes beyond slice 171's single leaf: (a) `psub_east` now has TWO leaves —
		// the second leaf `psub_east_hi` must get its own ATTACH with the (100,200)
		// bound, proving the per-parent inhseqno counter increments independently per
		// child; (b) the sibling sub-partitioned middle node `psub_west` must emit its
		// OWN PARTITION BY clause, its ATTACH-to-top with the LIST bound, and its leaf
		// `psub_west_lo` must ATTACH to `psub_west` (NOT the sibling `psub_east` nor the
		// grandparent `psub`) even though its (0,100) bound text is identical to
		// `psub_east_lo`'s. The immediate-parent link is verified via the full
		// `ALTER TABLE ONLY public.psub_west ATTACH PARTITION public.psub_west_lo`
		// single-line form, which a wrong-parent regression would break.
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.psub_east_hi FOR VALUES FROM (100) TO (200)") {
			t.Errorf("pg_dump dropped the second leaf's ATTACH-to-middle bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.psub_east_hi FOR VALUES FROM (100) TO (200)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE TABLE public.psub_west (") ||
			!strings.Contains(res.Stdout, "ATTACH PARTITION public.psub_west FOR VALUES IN ('west')") {
			t.Errorf("pg_dump dropped the sibling sub-partitioned middle node psub_west (CREATE TABLE / ATTACH-to-top with LIST bound)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ALTER TABLE ONLY public.psub_west ATTACH PARTITION public.psub_west_lo FOR VALUES FROM (0) TO (100)") {
			t.Errorf("pg_dump linked the leaf psub_west_lo to the wrong parent; missing immediate-parent ATTACH %q\n  full stdout=%q", "ALTER TABLE ONLY public.psub_west ATTACH PARTITION public.psub_west_lo FOR VALUES FROM (0) TO (100)", res.Stdout)
		}
		// **Slice 264 (asserted):** child-only CHECK on a partition leaf. The leaf
		// pchk_1 inherits column `a` from pchk (pg_dump prints no columns for it),
		// but its locally-declared CHECK (conislocal='t') must survive: pg_dump
		// emits `CONSTRAINT pchk_1_pos CHECK ((a > 0))` inside the otherwise
		// column-less CREATE TABLE body, then the LIST-bound ATTACH. A regression
		// that dropped conislocal, the pg_constraint row, or the
		// pg_get_constraintdef CHECK branch would lose the constraint silently.
		if !strings.Contains(res.Stdout, "CONSTRAINT pchk_1_pos CHECK ((a > 0))") {
			t.Errorf("pg_dump dropped the partition leaf's child-only CHECK; missing %q\n  full stdout=%q", "CONSTRAINT pchk_1_pos CHECK ((a > 0))", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pchk_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pchk_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 265 (asserted):** child-only column DEFAULT on a partition leaf.
		// The leaf pdfl_1 prints its full inherited column list (shouldPrintColumn
		// is true for every partition column); the per-column override DEFAULT must
		// re-attach inside that list as `b integer DEFAULT 42`, with the inherited
		// `a integer` kept and no spurious default on it. A regression dropping the
		// leaf's pg_attrdef row (or mis-rendering adbin) would lose `DEFAULT 42`
		// silently and break restore (the leaf would no longer default b to 42).
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pdfl_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "a integer") {
				t.Errorf("pg_dump dropped the leaf's inherited column; want %q in pdfl_1 block\n  block=%q", "a integer", block)
			}
			if !strings.Contains(block, "b integer DEFAULT 42") {
				t.Errorf("pg_dump dropped/corrupted the child-only DEFAULT; want %q in pdfl_1 block\n  block=%q", "b integer DEFAULT 42", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pdfl_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pdfl_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pdfl_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 266 (asserted):** child-only NOT NULL override on a partition
		// leaf — the LAST per-column override form. The leaf pnnl_1 prints its full
		// inherited column list (shouldPrintColumn is true for every partition
		// column); the per-column override NOT NULL must re-attach inside that list
		// as the inline decoration `b integer NOT NULL`, with the inherited
		// `a integer` kept and no spurious NOT NULL on it. A regression dropping the
		// leaf's local NOT NULL (or rendering it as a separate CONSTRAINT clause
		// instead of the inline column decoration) would diverge from PG 18.3 and
		// could lose the constraint on restore.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pnnl_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "a integer") {
				t.Errorf("pg_dump dropped the leaf's inherited column; want %q in pnnl_1 block\n  block=%q", "a integer", block)
			}
			if !strings.Contains(block, "b integer NOT NULL") {
				t.Errorf("pg_dump dropped/corrupted the child-only NOT NULL; want %q in pnnl_1 block\n  block=%q", "b integer NOT NULL", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pnnl_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pnnl_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pnnl_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 281 (asserted):** NOT NULL added to a partition leaf's INHERITED
		// column via ALTER TABLE ADD CONSTRAINT routes to the INLINE column form (not
		// the standalone body form the legacy-inheritance mninh/idfnd siblings use),
		// because shouldPrintColumn returns true for every partition column
		// (ispartition). The leaf pnna_1 prints its full inherited column list with
		// the two ALTER-added NOT NULLs as INLINE decorations: `qb` keeps its
		// non-default name (`qb integer CONSTRAINT pnna_named NOT NULL`) while `qc`'s
		// default name collapses to the bare `qc text NOT NULL`; the partition key
		// `qa` stays a plain `qa integer`. The standalone body form
		// (`CONSTRAINT pnna_named NOT NULL qb` with a trailing column name) must NOT
		// appear — its presence would mean ispartition was ignored in
		// shouldPrintColumn. Twin of slice 280 (same per-column collapse on the
		// legacy-inheritance standalone path).
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pnna_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "qa integer") {
				t.Errorf("pg_dump dropped the leaf's inherited partition-key column; want %q in pnna_1 block\n  block=%q", "qa integer", block)
			}
			if !strings.Contains(block, "qb integer CONSTRAINT pnna_named NOT NULL") {
				t.Errorf("pg_dump dropped/corrupted the non-default inline NOT NULL on a partition leaf; want %q in pnna_1 block\n  block=%q", "qb integer CONSTRAINT pnna_named NOT NULL", block)
			}
			if !strings.Contains(block, "qc text NOT NULL") {
				t.Errorf("pg_dump dropped/corrupted the collapsed default-named inline NOT NULL on a partition leaf; want %q in pnna_1 block\n  block=%q", "qc text NOT NULL", block)
			}
			if strings.Contains(block, "CONSTRAINT pnna_1_qc_not_null") {
				t.Errorf("pg_dump leaked the default constraint name on a partition leaf; want bare %q, not %q in pnna_1 block\n  block=%q", "qc text NOT NULL", "CONSTRAINT pnna_1_qc_not_null NOT NULL qc", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pnna_1\n  full stdout=%q", res.Stdout)
		}
		// The standalone inherited-NOT-NULL body form (column name AFTER the keyword)
		// is the legacy-inheritance shape; it must never appear for a partition leaf.
		if strings.Contains(res.Stdout, "CONSTRAINT pnna_named NOT NULL qb") {
			t.Errorf("pg_dump emitted the standalone body NOT NULL form for a partition leaf (ispartition ignored in shouldPrintColumn); unexpected %q\n  full stdout=%q", "CONSTRAINT pnna_named NOT NULL qb", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pnna_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pnna_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 282 (asserted):** a DEFAULT added to a partition leaf's INHERITED
		// column via ALTER TABLE ALTER COLUMN SET DEFAULT rides INLINE on the printed
		// column (`kb integer DEFAULT 7`), not as a standalone ALTER. shouldPrintColumn
		// is true for every partition column (ispartition), so attrdefs[].separate stays
		// false (pg_dump.c:9527-9535) and the default joins the CREATE TABLE body. The
		// partition key `ka` stays a plain `ka integer`. The legacy-inheritance standalone
		// form (slice 269) must NOT appear. DEFAULT analog of slice 281; partition-inline
		// twin of slice 269.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pdfa_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "ka integer") {
				t.Errorf("pg_dump dropped the leaf's inherited partition-key column; want %q in pdfa_1 block\n  block=%q", "ka integer", block)
			}
			if !strings.Contains(block, "kb integer DEFAULT 7") {
				t.Errorf("pg_dump dropped/corrupted the inline DEFAULT on a partition leaf; want %q in pdfa_1 block\n  block=%q", "kb integer DEFAULT 7", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pdfa_1\n  full stdout=%q", res.Stdout)
		}
		// The standalone SET DEFAULT form is the legacy-inheritance shape (slice 269);
		// it must never appear for a partition leaf whose column already prints inline.
		if strings.Contains(res.Stdout, "ALTER COLUMN kb SET DEFAULT 7") {
			t.Errorf("pg_dump emitted the standalone SET DEFAULT form for a partition leaf (separate flag set despite ispartition); unexpected %q\n  full stdout=%q", "ALTER COLUMN kb SET DEFAULT 7", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pdfa_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pdfa_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 283 (asserted):** a STORED generated column inherited onto a
		// partition leaf prints its generation clause INLINE in the leaf body
		// (`gb integer GENERATED ALWAYS AS (ga * 2) STORED`). attgenerated forces
		// attrdefs[].separate=false UNCONDITIONALLY (pg_dump.c:9507) — a generation
		// expression can never be split into a standalone ALTER ... SET DEFAULT — and
		// ispartition makes every column print (slices 281/282). The partition key
		// `ga` stays a plain `ga integer`. Discriminator-distinct from slices 281/282:
		// here separate is held false by attgenerated, not (only) by ispartition.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgna_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "ga integer") {
				t.Errorf("pg_dump dropped the leaf's inherited partition-key column; want %q in pgna_1 block\n  block=%q", "ga integer", block)
			}
			if !strings.Contains(block, "gb integer GENERATED ALWAYS AS (ga * 2) STORED") {
				t.Errorf("pg_dump dropped/corrupted the inline generated column on a partition leaf; want %q in pgna_1 block\n  block=%q", "gb integer GENERATED ALWAYS AS (ga * 2) STORED", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgna_1\n  full stdout=%q", res.Stdout)
		}
		// A generation expression can never be a standalone SET DEFAULT; assert the
		// illegal standalone form is absent for the leaf's generated column.
		if strings.Contains(res.Stdout, "ALTER COLUMN gb SET DEFAULT") {
			t.Errorf("pg_dump emitted an (illegal) standalone SET DEFAULT for a generated partition-leaf column (separate set despite attgenerated); unexpected %q\n  full stdout=%q", "ALTER COLUMN gb SET DEFAULT", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgna_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgna_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 284 (asserted):** the VIRTUAL counterpart of slice 283. A VIRTUAL
		// generated column inherited onto a partition leaf prints its generation
		// clause INLINE in the leaf body, but rendered WITHOUT a trailing keyword
		// (`vb integer GENERATED ALWAYS AS (va * 2)`). attgenerated forces
		// attrdefs[].separate=false UNCONDITIONALLY (pg_dump.c:9507) exactly as for
		// STORED — ATTRIBUTE_GENERATED_VIRTUAL ('v') is non-empty like
		// ATTRIBUTE_GENERATED_STORED ('s') — but the render branch differs:
		// pg_dump.c:17171 emits `GENERATED ALWAYS AS (%s)` with NO trailing STORED for
		// a virtual column (the STORED branch at 17168 is skipped). ispartition makes
		// every column print (slices 281/282). Discriminator-distinct from slice 283:
		// the render must NOT carry a trailing STORED.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pvna_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "va integer") {
				t.Errorf("pg_dump dropped the leaf's inherited partition-key column; want %q in pvna_1 block\n  block=%q", "va integer", block)
			}
			if !strings.Contains(block, "vb integer GENERATED ALWAYS AS (va * 2)") {
				t.Errorf("pg_dump dropped/corrupted the inline virtual generated column on a partition leaf; want %q in pvna_1 block\n  block=%q", "vb integer GENERATED ALWAYS AS (va * 2)", block)
			}
			// A virtual generated column must NOT render a trailing STORED: a regression
			// that mis-mapped attgenerated 'v'→'s' (pg_dump.c:17168) would surface here.
			if strings.Contains(block, "vb integer GENERATED ALWAYS AS (va * 2) STORED") {
				t.Errorf("pg_dump emitted a spurious trailing STORED on a VIRTUAL generated partition-leaf column\n  block=%q", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pvna_1\n  full stdout=%q", res.Stdout)
		}
		// A generation expression can never be a standalone SET DEFAULT; assert the
		// illegal standalone form is absent for the leaf's virtual generated column.
		if strings.Contains(res.Stdout, "ALTER COLUMN vb SET DEFAULT") {
			t.Errorf("pg_dump emitted an (illegal) standalone SET DEFAULT for a virtual generated partition-leaf column (separate set despite attgenerated); unexpected %q\n  full stdout=%q", "ALTER COLUMN vb SET DEFAULT", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pvna_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pvna_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 285 (asserted):** a MULTI-ATTRIBUTE generation expression inherited
		// onto a partition leaf. Where slices 283/284 referenced a single column in the
		// generation clause (`ga * 2`), pgmc_1's inherited `gb` is
		// `GENERATED ALWAYS AS (ga + gc) STORED` — a binary expression over two distinct
		// inherited Vars. The render path is identical to slice 283 (attgenerated forces
		// separate=false, ispartition forces every column to print) but this asserts the
		// deparse resolves BOTH Vars to the right column names: the leaf body must carry
		// all three plain columns (`ga integer`, `gc integer`) plus the inline generated
		// `gb integer GENERATED ALWAYS AS (ga + gc) STORED`. A regression that resolved
		// only the first Var or swapped ga↔gc by attnum would corrupt the clause here.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgmc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "ga integer") {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgmc_1 block\n  block=%q", "ga integer", block)
			}
			if !strings.Contains(block, "gc integer") {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgmc_1 block\n  block=%q", "gc integer", block)
			}
			if !strings.Contains(block, "gb integer GENERATED ALWAYS AS (ga + gc) STORED") {
				t.Errorf("pg_dump dropped/corrupted the multi-attr inline generated column on a partition leaf; want %q in pgmc_1 block\n  block=%q", "gb integer GENERATED ALWAYS AS (ga + gc) STORED", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgmc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgmc_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgmc_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 286 (asserted):** a FORWARD-REFERENCE generation expression inherited
		// onto a partition leaf. Where slice 285's `gb` referenced only columns declared
		// before it, pgfr_1's inherited `gz` is attnum 1 and references `ya` (attnum 2) and
		// `yc` (attnum 3), both declared AFTER it. The leaf body must carry the inline
		// generated `gz integer GENERATED ALWAYS AS (ya + yc) STORED` FIRST (attnum order),
		// then the two plain columns `ya integer`, `yc integer`. This asserts the generation
		// deparse resolves both Vars by NAME even though neither operand precedes the
		// generated column — a forward-only positional scan would corrupt the `(ya + yc)`
		// clause here.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgfr_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			gzIdx := strings.Index(block, "gz integer GENERATED ALWAYS AS (ya + yc) STORED")
			if gzIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the forward-reference inline generated column on a partition leaf; want %q in pgfr_1 block\n  block=%q", "gz integer GENERATED ALWAYS AS (ya + yc) STORED", block)
			}
			yaIdx := strings.Index(block, "ya integer")
			if yaIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgfr_1 block\n  block=%q", "ya integer", block)
			}
			if !strings.Contains(block, "yc integer") {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgfr_1 block\n  block=%q", "yc integer", block)
			}
			// Forward-reference ordering: the generated column (attnum 1) must print BEFORE
			// the columns its expression references (attnum 2/3).
			if gzIdx >= 0 && yaIdx >= 0 && gzIdx > yaIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want generated %q before %q in pgfr_1 block\n  block=%q", "gz", "ya", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgfr_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgfr_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgfr_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 287 (asserted):** a mid-position generated column mixing a backward
		// Var, a literal, and a forward Var in one expression. pgmx_1's inherited `mg`
		// is attnum 2 and references `ma` (attnum 1, declared BEFORE) and `mc` (attnum
		// 3, declared AFTER) plus the literal `1`. The leaf body must print in attnum
		// order: `ma integer`, then the inline `mg integer GENERATED ALWAYS AS
		// (ma + 1 + mc) STORED`, then `mc integer`. This asserts the generation deparse
		// resolves BOTH directions by NAME in a single expression (slices 285/286 each
		// exercised only one direction); the three-operand `ma + 1 + mc` prints flat.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgmx_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			maIdx := strings.Index(block, "ma integer")
			if maIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgmx_1 block\n  block=%q", "ma integer", block)
			}
			mgIdx := strings.Index(block, "mg integer GENERATED ALWAYS AS (ma + 1 + mc) STORED")
			if mgIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the mid-position mixed-direction generated column on a partition leaf; want %q in pgmx_1 block\n  block=%q", "mg integer GENERATED ALWAYS AS (ma + 1 + mc) STORED", block)
			}
			mcIdx := strings.Index(block, "mc integer")
			if mcIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgmx_1 block\n  block=%q", "mc integer", block)
			}
			// Attnum order: ma (1) before mg (2) before mc (3) — the generated column
			// prints between the two columns its expression references.
			if maIdx >= 0 && mgIdx >= 0 && maIdx > mgIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgmx_1 block\n  block=%q", "ma", "mg", block)
			}
			if mgIdx >= 0 && mcIdx >= 0 && mgIdx > mcIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want generated %q before %q in pgmx_1 block\n  block=%q", "mg", "mc", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgmx_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgmx_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgmx_1 FOR VALUES IN (1)", res.Stdout)
		}
		// **Slice 288 (asserted):** a TEXT generated column over the `||` string
		// concatenation operator (every prior generation slice used integer `+`/`*`).
		// pgcc_1's inherited `cc` is attnum 3 and references `ca`/`cb` (both text);
		// the leaf body must print in attnum order `ca text`, `cb text`, then the
		// inline `cc text GENERATED ALWAYS AS (ca || cb) STORED`. This asserts the
		// inherited-leaf generation render path is type-agnostic (text, not integer)
		// and operator-agnostic (`||`, not arithmetic): attGeneratedFor inspects no
		// type and goopg's pg_get_expr passes the source through verbatim, so the
		// `||` deparse stays flat with no nested parens.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgcc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			caIdx := strings.Index(block, "ca text")
			if caIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgcc_1 block\n  block=%q", "ca text", block)
			}
			cbIdx := strings.Index(block, "cb text")
			if cbIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgcc_1 block\n  block=%q", "cb text", block)
			}
			ccIdx := strings.Index(block, "cc text GENERATED ALWAYS AS (ca || cb) STORED")
			if ccIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the text-concatenation generated column on a partition leaf; want %q in pgcc_1 block\n  block=%q", "cc text GENERATED ALWAYS AS (ca || cb) STORED", block)
			}
			// Attnum order: ca (1) before cb (2) before cc (3).
			if caIdx >= 0 && cbIdx >= 0 && caIdx > cbIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgcc_1 block\n  block=%q", "ca", "cb", block)
			}
			if cbIdx >= 0 && ccIdx >= 0 && cbIdx > ccIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgcc_1 block\n  block=%q", "cb", "cc", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgcc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgcc_1 FOR VALUES IN ('x')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgcc_1 FOR VALUES IN ('x')", res.Stdout)
		}
		// **Slice 289 (asserted):** a generation expression with precedence-grouping
		// parens (`(fa + fb) * 2`). This is the FIRST generation slice with nested
		// parens; it pins the production deparse fix (joinGeneratedExprTokens). The
		// leaf pgpp_1 must print `fa integer`, `fb integer`, then the inline
		// generated `fc integer GENERATED ALWAYS AS ((fa + fb) * 2) STORED` — with
		// the inner precedence paren rendered TIGHT (`(fa + fb)`, not `( fa + fb )`),
		// matching real pg_dump's pg_get_expr. A regression to the naive space-join
		// would reintroduce the spurious inner spaces and fail the exact-substring
		// check below.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgpp_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			faIdx := strings.Index(block, "fa integer")
			if faIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgpp_1 block\n  block=%q", "fa integer", block)
			}
			fbIdx := strings.Index(block, "fb integer")
			if fbIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgpp_1 block\n  block=%q", "fb integer", block)
			}
			fcIdx := strings.Index(block, "fc integer GENERATED ALWAYS AS ((fa + fb) * 2) STORED")
			if fcIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the parenthesised generated column on a partition leaf; want %q in pgpp_1 block\n  block=%q", "fc integer GENERATED ALWAYS AS ((fa + fb) * 2) STORED", block)
			}
			// Attnum order: fa (1) before fb (2) before fc (3).
			if faIdx >= 0 && fbIdx >= 0 && faIdx > fbIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgpp_1 block\n  block=%q", "fa", "fb", block)
			}
			if fbIdx >= 0 && fcIdx >= 0 && fbIdx > fcIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgpp_1 block\n  block=%q", "fb", "fc", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgpp_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgpp_1 FOR VALUES IN (1)") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgpp_1 FOR VALUES IN (1)", res.Stdout)
		}

		// **Slice 290 (asserted):** function-call generation expression on a
		// partition leaf. The leaf pgfx_1 must print `fn text`, then the inline
		// generated `fu text GENERATED ALWAYS AS (upper(fn)) STORED` — with the call
		// parens rendered TIGHT (`upper(fn)`, not `upper ( fn )`), matching real
		// pg_dump's pg_get_expr. A regression to the naive space-join would emit the
		// spurious inner spaces and fail the exact-substring check below.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgfx_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			fnIdx := strings.Index(block, "fn text")
			if fnIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgfx_1 block\n  block=%q", "fn text", block)
			}
			fuIdx := strings.Index(block, "fu text GENERATED ALWAYS AS (upper(fn)) STORED")
			if fuIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the function-call generated column on a partition leaf; want %q in pgfx_1 block\n  block=%q", "fu text GENERATED ALWAYS AS (upper(fn)) STORED", block)
			}
			// Attnum order: fn (1) before fu (2).
			if fnIdx >= 0 && fuIdx >= 0 && fnIdx > fuIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgfx_1 block\n  block=%q", "fn", "fu", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgfx_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgfx_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgfx_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 291 (asserted):** TWO-ARGUMENT function-call generation expression
		// on a partition leaf. The leaf pgcl_1 must print `cn text`, `dn text`, then
		// the inline generated `en text GENERATED ALWAYS AS (coalesce(cn, dn)) STORED`
		// — with the argument list rendered `cn, dn` (comma tight-left, spaced-right),
		// matching what pg_get_expr returns for goopg's stored source. A regression to
		// a naive space-join would emit `coalesce(cn , dn)` (or drop the comma's
		// trailing space) and fail the exact-substring check below.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgcl_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			cnIdx := strings.Index(block, "cn text")
			if cnIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgcl_1 block\n  block=%q", "cn text", block)
			}
			dnIdx := strings.Index(block, "dn text")
			if dnIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgcl_1 block\n  block=%q", "dn text", block)
			}
			enIdx := strings.Index(block, "en text GENERATED ALWAYS AS (coalesce(cn, dn)) STORED")
			if enIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the two-arg function-call generated column on a partition leaf; want %q in pgcl_1 block\n  block=%q", "en text GENERATED ALWAYS AS (coalesce(cn, dn)) STORED", block)
			}
			// Attnum order: cn (1) before dn (2) before en (3).
			if cnIdx >= 0 && dnIdx >= 0 && cnIdx > dnIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgcl_1 block\n  block=%q", "cn", "dn", block)
			}
			if dnIdx >= 0 && enIdx >= 0 && dnIdx > enIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgcl_1 block\n  block=%q", "dn", "en", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgcl_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgcl_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgcl_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 292 (asserted):** NESTED function-call generation expression on a
		// partition leaf. The leaf pgnc_1 must print `gn text`, `hn text`, then the
		// inline generated `jn text GENERATED ALWAYS AS (upper(coalesce(gn, hn))) STORED`
		// — with BOTH call parens rendered tight and only the inner argument comma
		// spaced (`upper(coalesce(gn, hn))`), matching what pg_get_expr returns for
		// goopg's stored source. A regression to a naive space-join would emit
		// `upper ( coalesce ( gn ,hn ) )` (or any variant with stray paren spaces) and
		// fail the exact-substring check below.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgnc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			gnIdx := strings.Index(block, "gn text")
			if gnIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgnc_1 block\n  block=%q", "gn text", block)
			}
			hnIdx := strings.Index(block, "hn text")
			if hnIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgnc_1 block\n  block=%q", "hn text", block)
			}
			jnIdx := strings.Index(block, "jn text GENERATED ALWAYS AS (upper(coalesce(gn, hn))) STORED")
			if jnIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the nested function-call generated column on a partition leaf; want %q in pgnc_1 block\n  block=%q", "jn text GENERATED ALWAYS AS (upper(coalesce(gn, hn))) STORED", block)
			}
			// Attnum order: gn (1) before hn (2) before jn (3).
			if gnIdx >= 0 && hnIdx >= 0 && gnIdx > hnIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgnc_1 block\n  block=%q", "gn", "hn", block)
			}
			if hnIdx >= 0 && jnIdx >= 0 && hnIdx > jnIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgnc_1 block\n  block=%q", "hn", "jn", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgnc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgnc_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgnc_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 293 (asserted):** THREE-argument function-call generation
		// expression on a partition leaf. The leaf pg3c_1 must print `ka text`,
		// `la text`, `ma text`, then the inline generated
		// `na text GENERATED ALWAYS AS (concat(ka, la, ma)) STORED` — with the
		// single call paren tight and BOTH argument commas spaced (`, `), matching
		// what pg_get_expr returns for goopg's stored source. A regression that
		// emitted only one separator (`concat(ka, la,ma)`) or stray paren spaces
		// would fail the exact-substring check below.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pg3c_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			kaIdx := strings.Index(block, "ka text")
			if kaIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pg3c_1 block\n  block=%q", "ka text", block)
			}
			laIdx := strings.Index(block, "la text")
			if laIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pg3c_1 block\n  block=%q", "la text", block)
			}
			maIdx := strings.Index(block, "ma text")
			if maIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pg3c_1 block\n  block=%q", "ma text", block)
			}
			naIdx := strings.Index(block, "na text GENERATED ALWAYS AS (concat(ka, la, ma)) STORED")
			if naIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the three-arg function-call generated column on a partition leaf; want %q in pg3c_1 block\n  block=%q", "na text GENERATED ALWAYS AS (concat(ka, la, ma)) STORED", block)
			}
			// Attnum order: ka (1) before la (2) before ma (3) before na (4).
			if kaIdx >= 0 && laIdx >= 0 && kaIdx > laIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pg3c_1 block\n  block=%q", "ka", "la", block)
			}
			if laIdx >= 0 && maIdx >= 0 && laIdx > maIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pg3c_1 block\n  block=%q", "la", "ma", block)
			}
			if maIdx >= 0 && naIdx >= 0 && maIdx > naIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pg3c_1 block\n  block=%q", "ma", "na", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pg3c_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pg3c_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pg3c_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 294 (asserted):** function-call generation expression with a
		// STRING-LITERAL argument on a partition leaf. The leaf pglc_1 must print
		// `ka text`, `la text`, then the inline generated
		// `na text GENERATED ALWAYS AS (concat(ka, '-', la)) STORED` — with the
		// literal RE-QUOTED (`'-'`, not the bare `-` the pre-fix helper would have
		// emitted) and the surrounding commas spaced. The substring check below is
		// the production-fix regression guard: before slice 294 the helper dropped
		// the literal's quotes, so this block would have contained the malformed
		// `concat(ka, -, la)`.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pglc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			kaIdx := strings.Index(block, "ka text")
			if kaIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pglc_1 block\n  block=%q", "ka text", block)
			}
			laIdx := strings.Index(block, "la text")
			if laIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pglc_1 block\n  block=%q", "la text", block)
			}
			naIdx := strings.Index(block, "na text GENERATED ALWAYS AS (concat(ka, '-', la)) STORED")
			if naIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the string-literal-argument function-call generated column on a partition leaf; want %q in pglc_1 block\n  block=%q", "na text GENERATED ALWAYS AS (concat(ka, '-', la)) STORED", block)
			}
			// Guard against the pre-fix regression: the malformed bare-literal
			// form must NOT appear (quotes were dropped → `concat(ka, -, la)`).
			if strings.Contains(block, "concat(ka, -, la)") {
				t.Errorf("pg_dump emitted the pre-slice-294 malformed generation expr with the string literal's quotes dropped; got %q in pglc_1 block\n  block=%q", "concat(ka, -, la)", block)
			}
			// Attnum order: ka (1) before la (2) before na (3).
			if kaIdx >= 0 && laIdx >= 0 && kaIdx > laIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pglc_1 block\n  block=%q", "ka", "la", block)
			}
			if laIdx >= 0 && naIdx >= 0 && laIdx > naIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pglc_1 block\n  block=%q", "la", "na", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pglc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pglc_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pglc_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 295 (asserted):** function-call generation expression whose
		// string-literal argument's BODY IS A COMMA on a partition leaf — the
		// adversarial complement to slice 294. The leaf pgkc_1 must print `ka text`,
		// `la text`, then the inline generated
		// `na text GENERATED ALWAYS AS (concat(ka, ',', la)) STORED` — with the
		// comma literal RE-QUOTED and distinct from the separator commas. The
		// regression guard below pins slice 294's TokenSymbol gating on the oracle
		// path: a Value-based switch would have matched the literal `,` against the
		// separator case and dropped its quotes, collapsing the block into the
		// malformed `concat(ka,,,la)`.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgkc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			kaIdx := strings.Index(block, "ka text")
			if kaIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgkc_1 block\n  block=%q", "ka text", block)
			}
			laIdx := strings.Index(block, "la text")
			if laIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgkc_1 block\n  block=%q", "la text", block)
			}
			naIdx := strings.Index(block, "na text GENERATED ALWAYS AS (concat(ka, ',', la)) STORED")
			if naIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the comma-literal-argument function-call generated column on a partition leaf; want %q in pgkc_1 block\n  block=%q", "na text GENERATED ALWAYS AS (concat(ka, ',', la)) STORED", block)
			}
			// Guard against the pre-fix regression: the malformed collapsed-comma
			// form must NOT appear (quotes dropped, literal merged into separators).
			if strings.Contains(block, "concat(ka,,,la)") {
				t.Errorf("pg_dump emitted the pre-slice-294 malformed generation expr with the comma literal collapsed into the separators; got %q in pgkc_1 block\n  block=%q", "concat(ka,,,la)", block)
			}
			// Attnum order: ka (1) before la (2) before na (3).
			if kaIdx >= 0 && laIdx >= 0 && kaIdx > laIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgkc_1 block\n  block=%q", "ka", "la", block)
			}
			if laIdx >= 0 && naIdx >= 0 && laIdx > naIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgkc_1 block\n  block=%q", "la", "na", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgkc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgkc_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgkc_1 FOR VALUES IN ('a')", res.Stdout)
		}

		// **Slice 296 (asserted):** function-call generation expression whose
		// string-literal argument's BODY IS A SINGLE QUOTE on a partition leaf — the
		// adversarial complement to slices 294 (body `-`) and 295 (body `,`), and the
		// only fixture that exercises slice 294's quote-DOUBLING on the oracle path.
		// The leaf pgqc_1 must print `ka text`, `la text`, then the inline generated
		// `na text GENERATED ALWAYS AS (concat(ka, '''', la)) STORED` — the embedded
		// quote DOUBLED to the balanced four-quote literal. The regression guards
		// below pin renderTok's quote-doubling: a fix that re-quoted but forgot to
		// double the embedded `'` would emit the unbalanced three-quote
		// `concat(ka, ''', la)`; the pre-slice-294 raw space-join would emit the
		// single-quote `concat(ka, ', la)` (the lone `'` opening a phantom string).
		if start := strings.Index(res.Stdout, "CREATE TABLE public.pgqc_1 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			kaIdx := strings.Index(block, "ka text")
			if kaIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgqc_1 block\n  block=%q", "ka text", block)
			}
			laIdx := strings.Index(block, "la text")
			if laIdx < 0 {
				t.Errorf("pg_dump dropped an inherited plain column on a partition leaf; want %q in pgqc_1 block\n  block=%q", "la text", block)
			}
			naIdx := strings.Index(block, "na text GENERATED ALWAYS AS (concat(ka, '''', la)) STORED")
			if naIdx < 0 {
				t.Errorf("pg_dump dropped/corrupted the embedded-quote-literal-argument function-call generated column on a partition leaf; want %q in pgqc_1 block\n  block=%q", "na text GENERATED ALWAYS AS (concat(ka, '''', la)) STORED", block)
			}
			// Guard against the forgot-to-double regression: the unbalanced
			// three-quote form must NOT appear (embedded quote re-quoted but not
			// doubled). `''''` contains `'''` as a substring, so match the full
			// generated column text minus the trailing quote to isolate the bug.
			if strings.Contains(block, "concat(ka, ''', la)") {
				t.Errorf("pg_dump emitted the forgot-to-double malformed generation expr (embedded quote not doubled); got %q in pgqc_1 block\n  block=%q", "concat(ka, ''', la)", block)
			}
			// Attnum order: ka (1) before la (2) before na (3).
			if kaIdx >= 0 && laIdx >= 0 && kaIdx > laIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before %q in pgqc_1 block\n  block=%q", "ka", "la", block)
			}
			if laIdx >= 0 && naIdx >= 0 && laIdx > naIdx {
				t.Errorf("pg_dump emitted partition-leaf columns out of attnum order; want %q before generated %q in pgqc_1 block\n  block=%q", "la", "na", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.pgqc_1\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ATTACH PARTITION public.pgqc_1 FOR VALUES IN ('a')") {
			t.Errorf("pg_dump dropped the partition leaf's ATTACH bound; missing %q\n  full stdout=%q", "ATTACH PARTITION public.pgqc_1 FOR VALUES IN ('a')", res.Stdout)
		}
		// **Slice 267 (asserted):** local CHECK on a legacy (non-partition) INHERITS
		// child. Unlike the partition leaves above (whose ispartition forces every
		// column to print), ichk_child must (1) print ONLY its local column `extra`
		// — the inherited `pid`/`pname` are omitted because shouldPrintColumn gates
		// on attislocal alone here — (2) emit its conislocal CHECK
		// `CONSTRAINT ichk_child_pos CHECK ((extra > 0))` inside that body, and
		// (3) close with the `INHERITS (public.ichk_parent)` clause (legacy
		// inheritance, NOT an ATTACH). A regression that re-emitted the inherited
		// columns, dropped the conislocal CHECK, or lost the INHERITS clause would
		// produce a structurally different table on restore.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.ichk_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the legacy child's local column; want %q in ichk_child block\n  block=%q", "extra integer", block)
			}
			if !strings.Contains(block, "CONSTRAINT ichk_child_pos CHECK ((extra > 0))") {
				t.Errorf("pg_dump dropped the legacy child's local CHECK; want %q in ichk_child block\n  block=%q", "CONSTRAINT ichk_child_pos CHECK ((extra > 0))", block)
			}
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in ichk_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.ichk_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.ichk_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.ichk_parent)", res.Stdout)
		}
		// **Slice 268 (asserted):** local column DEFAULT on a legacy (non-partition)
		// INHERITS child. Like ichk_child above, idfl_child must (1) print ONLY its
		// local column `extra` — the inherited `pid`/`pname` are omitted because
		// shouldPrintColumn gates on attislocal alone — but now (2) that local column
		// must carry its attrdef inline as `extra integer DEFAULT 42`, and (3) the
		// block must close with the `INHERITS (public.idfl_parent)` clause. A
		// regression that re-emitted the inherited columns, dropped the DEFAULT, or
		// lost the INHERITS clause would restore a structurally different table.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.idfl_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer DEFAULT 42") {
				t.Errorf("pg_dump dropped the legacy child's local DEFAULT; want %q in idfl_child block\n  block=%q", "extra integer DEFAULT 42", block)
			}
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in idfl_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.idfl_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.idfl_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.idfl_parent)", res.Stdout)
		}
		// **Slice 269 (asserted):** child-level DEFAULT on an INHERITED column,
		// emitted as a SEPARATE ALTER (not inline). idfa_child's CREATE TABLE
		// body must print ONLY its local `extra` column — the inherited
		// `pid`/`pname` are suppressed (attislocal=false) and arrive via the
		// INHERITS clause — and the DEFAULT set on the suppressed `pid` must
		// surface as the standalone statement
		// `ALTER TABLE ONLY public.idfa_child ALTER COLUMN pid SET DEFAULT 7;`
		// (pg_dump.c marks attrdefs[].separate for non-printed columns). A
		// regression that lost the new AlterTableSetDefault support would drop
		// the ALTER entirely (atthasdef=false → pg_dump emits nothing); a
		// regression that re-emitted the inherited columns or rode the DEFAULT
		// inline would diverge from PG 18.3.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.idfa_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the legacy child's local column; want %q in idfa_child block\n  block=%q", "extra integer", block)
			}
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in idfa_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.idfa_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.idfa_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.idfa_parent)", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ALTER TABLE ONLY public.idfa_child ALTER COLUMN pid SET DEFAULT 7;") {
			t.Errorf("pg_dump dropped the child-level DEFAULT on the inherited column; missing %q\n  full stdout=%q", "ALTER TABLE ONLY public.idfa_child ALTER COLUMN pid SET DEFAULT 7;", res.Stdout)
		}
		// **Slice 270 (asserted):** child-level NOT NULL on an INHERITED column,
		// emitted as a standalone `NOT NULL pid` constraint item INSIDE the body
		// (NOT a separate ALTER — the NOT NULL twin of the slice 269 DEFAULT
		// case). idfn_child's CREATE TABLE body must print its local `extra`
		// column AND `NOT NULL pid`; the inherited `pid`/`pname` are suppressed
		// as full columns (attislocal=false) and arrive via INHERITS. Verified
		// against real pg_dump 18.3: `NOT NULL pid` precedes `extra integer` in
		// attnum order. A regression that lost AlterTableSetNotNull would drop
		// the contype='n' constraint (pg_dump emits nothing); a regression that
		// re-emitted the inherited columns or rode the constraint inline as
		// `pid integer NOT NULL` would diverge from PG 18.3.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.idfn_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the legacy child's local column; want %q in idfn_child block\n  block=%q", "extra integer", block)
			}
			if !strings.Contains(block, "NOT NULL pid") {
				t.Errorf("pg_dump dropped the child-level NOT NULL on the inherited column; want %q in idfn_child block\n  block=%q", "NOT NULL pid", block)
			}
			// The inherited columns must NOT be re-emitted as full columns.
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in idfn_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.idfn_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.idfn_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.idfn_parent)", res.Stdout)
		}
		// **Slice 271 (asserted):** a *named* NOT NULL on an inherited column,
		// emitted as the `CONSTRAINT idfnn_nn NOT NULL pid` body form — the named
		// counterpart of slice 270's unnamed `NOT NULL pid`. pg_dump prints the
		// CONSTRAINT prefix only when the conname differs from the computed default
		// `<table>_<col>_not_null` (pg_dump.c:17228), so the explicit `idfnn_nn`
		// name must survive into pg_constraint.conname. idfnn_child's body must
		// print its local `extra` column AND `CONSTRAINT idfnn_nn NOT NULL pid`;
		// the inherited `pid`/`pname` are suppressed and arrive via INHERITS. A
		// regression that dropped the explicit name would fall back to the unnamed
		// form (no `CONSTRAINT idfnn_nn` prefix); one that lost AlterTableAddNotNull
		// would emit nothing for the NOT NULL.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.idfnn_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the legacy child's local column; want %q in idfnn_child block\n  block=%q", "extra integer", block)
			}
			if !strings.Contains(block, "CONSTRAINT idfnn_nn NOT NULL pid") {
				t.Errorf("pg_dump dropped the named child-level NOT NULL; want %q in idfnn_child block\n  block=%q", "CONSTRAINT idfnn_nn NOT NULL pid", block)
			}
			// The inherited columns must NOT be re-emitted as full columns.
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in idfnn_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.idfnn_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.idfnn_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.idfnn_parent)", res.Stdout)
		}
		// **Slice 272 (asserted):** a `NO INHERIT` NOT NULL on a STANDALONE table,
		// dumped inline as `c integer NOT NULL NO INHERIT`. pg_dump appends ` NO
		// INHERIT` after the inline `NOT NULL` only when pg_constraint reports
		// connoinherit='t' (notnull_noinh[j]; pg_dump.c:17188); the column is local
		// and the constraint name matches the default, so the UNNAMED inline form is
		// emitted. A regression that lost the NoInherit thread (parser → executor →
		// pg_constraint builder) would dump a plain `c integer NOT NULL` here.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer NOT NULL NO INHERIT") {
				t.Errorf("pg_dump dropped the NO INHERIT on a standalone NOT NULL column; want %q in nninh block\n  block=%q", "c integer NOT NULL NO INHERIT", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh\n  full stdout=%q", res.Stdout)
		}
		// **Slice 273 (asserted):** a NAMED inline NOT NULL whose name differs from
		// the auto-name forces pg_dump to re-emit the `CONSTRAINT <name>` prefix
		// (pg_dump.c:17184). `c` carries NO INHERIT, `e` does not. A regression that
		// dropped the inline named NOT NULL (the pre-slice-273 parser had no NOT
		// NULL arm in the inline-CONSTRAINT switch) would dump a plain `c integer` /
		// `e integer` with no NOT NULL at all.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh2 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer CONSTRAINT c_nn NOT NULL NO INHERIT") {
				t.Errorf("pg_dump dropped the named NOT NULL NO INHERIT; want %q in nninh2 block\n  block=%q", "c integer CONSTRAINT c_nn NOT NULL NO INHERIT", block)
			}
			if !strings.Contains(block, "e integer CONSTRAINT e_nn NOT NULL") {
				t.Errorf("pg_dump dropped the named NOT NULL; want %q in nninh2 block\n  block=%q", "e integer CONSTRAINT e_nn NOT NULL", block)
			}
			// The plain named NOT NULL on `e` must NOT spuriously gain NO INHERIT.
			if strings.Contains(block, "e integer CONSTRAINT e_nn NOT NULL NO INHERIT") {
				t.Errorf("pg_dump added a spurious NO INHERIT to a plain named NOT NULL\n  block=%q", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh2\n  full stdout=%q", res.Stdout)
		}
		// **Slice 274 (asserted):** a NAMED inline NOT NULL whose name EQUALS the
		// computed default `<table>_<col>_not_null` must COLLAPSE to the bare
		// `NOT NULL` form. The user spelled out `CONSTRAINT nninh3_c_not_null`,
		// but because it matches pg_dump's default name the `CONSTRAINT <name>`
		// prefix is dropped (pg_dump.c:17184). This is slice 273's boundary twin:
		// 273 asserted the prefix is re-emitted when the name DIFFERS; here it is
		// suppressed when the name MATCHES. A regression that always emitted the
		// prefix for an explicitly-named NOT NULL would leak `CONSTRAINT
		// nninh3_c_not_null` into the column definition.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh3 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer NOT NULL") {
				t.Errorf("pg_dump dropped the NOT NULL on the default-named column; want %q in nninh3 block\n  block=%q", "c integer NOT NULL", block)
			}
			// The default-named NOT NULL must NOT carry a CONSTRAINT prefix.
			if strings.Contains(block, "CONSTRAINT nninh3_c_not_null") {
				t.Errorf("pg_dump leaked the default constraint name; want bare %q, not %q in nninh3 block\n  block=%q", "c integer NOT NULL", "CONSTRAINT nninh3_c_not_null", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh3 block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh3\n  full stdout=%q", res.Stdout)
		}
		// **Slice 275 (asserted):** a NAMED `NO INHERIT` NOT NULL added to a LOCAL
		// column via `ALTER TABLE ... ADD CONSTRAINT nn4 NOT NULL c NO INHERIT`
		// must dump INLINE as `c integer CONSTRAINT nn4 NOT NULL NO INHERIT` —
		// identical to nninh2's inline-created `c`, proving the AlterTableAddNotNull
		// executor threads act.NoInherit (operators_ddl.go:5498) and the conname
		// just as the CREATE-TABLE-inline path does. A regression that dropped the
		// NO INHERIT on the ALTER path would emit `c integer CONSTRAINT nn4 NOT
		// NULL` (connoinherit='f'); one that dropped the name would emit a bare
		// `c integer NOT NULL NO INHERIT`.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh4 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer CONSTRAINT nn4 NOT NULL NO INHERIT") {
				t.Errorf("pg_dump dropped the named NO INHERIT NOT NULL added via ALTER; want %q in nninh4 block\n  block=%q", "c integer CONSTRAINT nn4 NOT NULL NO INHERIT", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh4 block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh4\n  full stdout=%q", res.Stdout)
		}
		// **Slice 276 (asserted, negative twin of slice 275):** a NAMED NOT NULL
		// added to a LOCAL column via `ALTER TABLE ... ADD CONSTRAINT nn5 NOT NULL
		// c` (no `NO INHERIT`) must dump INLINE as `c integer CONSTRAINT nn5 NOT
		// NULL` and must NOT acquire a spurious ` NO INHERIT` suffix. This proves
		// the AlterTableAddNotNull executor records connoinherit='f' when the
		// trailer is absent — it does not fabricate NO INHERIT. A regression that
		// defaulted connoinherit to 't' on the ALTER path would emit `c integer
		// CONSTRAINT nn5 NOT NULL NO INHERIT` (the slice-275 byte form, wrong here).
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh5 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer CONSTRAINT nn5 NOT NULL") {
				t.Errorf("pg_dump dropped the named NOT NULL added via ALTER; want %q in nninh5 block\n  block=%q", "c integer CONSTRAINT nn5 NOT NULL", block)
			}
			if strings.Contains(block, "CONSTRAINT nn5 NOT NULL NO INHERIT") {
				t.Errorf("pg_dump fabricated a NO INHERIT suffix on the named NOT NULL; want bare %q, not %q in nninh5 block\n  block=%q", "c integer CONSTRAINT nn5 NOT NULL", "CONSTRAINT nn5 NOT NULL NO INHERIT", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh5 block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh5\n  full stdout=%q", res.Stdout)
		}
		// **Slice 277 (asserted, ALTER-path counterpart of slice 274):** a NAMED
		// NOT NULL added to a LOCAL column via `ALTER TABLE ... ADD CONSTRAINT
		// nninh6_c_not_null NOT NULL c`, whose explicit name EQUALS the auto-name
		// `nninh6_c_not_null`, must COLLAPSE to the bare `c integer NOT NULL` form.
		// pg_dump suppresses a NOT NULL constraint name when it matches the computed
		// default (pg_dump.c:17184), so the explicit name must NOT leak into the
		// dump. A regression storing a non-default conname on the ALTER path would
		// emit `c integer CONSTRAINT nninh6_c_not_null NOT NULL`.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh6 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer NOT NULL") {
				t.Errorf("pg_dump dropped the default-named NOT NULL added via ALTER; want %q in nninh6 block\n  block=%q", "c integer NOT NULL", block)
			}
			if strings.Contains(block, "CONSTRAINT nninh6_c_not_null") {
				t.Errorf("pg_dump leaked the default constraint name from the ALTER path; want bare %q, not %q in nninh6 block\n  block=%q", "c integer NOT NULL", "CONSTRAINT nninh6_c_not_null", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh6 block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh6\n  full stdout=%q", res.Stdout)
		}
		// Slice 278 (combines slice 277's collapse with slice 275's suffix):
		// `ALTER TABLE public.nninh7 ADD CONSTRAINT nninh7_c_not_null NOT NULL c
		// NO INHERIT`, whose explicit name EQUALS the auto-name `nninh7_c_not_null`,
		// must COLLAPSE the `CONSTRAINT` prefix while keeping `NO INHERIT` — the
		// bare `c integer NOT NULL NO INHERIT` form. pg_dump suppresses the
		// matching default name (pg_dump.c:17184) but still appends ` NO INHERIT`
		// from notnull_noinh (pg_dump.c:17187). A regression dropping the noinh bit
		// would emit `c integer NOT NULL`; one storing a non-default conname would
		// leak `CONSTRAINT nninh7_c_not_null`.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.nninh7 ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "c integer NOT NULL NO INHERIT") {
				t.Errorf("pg_dump dropped the NO INHERIT or collapsed form on the default-named NO INHERIT NOT NULL added via ALTER; want %q in nninh7 block\n  block=%q", "c integer NOT NULL NO INHERIT", block)
			}
			if strings.Contains(block, "CONSTRAINT nninh7_c_not_null") {
				t.Errorf("pg_dump leaked the default constraint name from the ALTER path; want bare %q, not %q in nninh7 block\n  block=%q", "c integer NOT NULL NO INHERIT", "CONSTRAINT nninh7_c_not_null", block)
			}
			if !strings.Contains(block, "d integer") {
				t.Errorf("pg_dump dropped the standalone table's plain column; want %q in nninh7 block\n  block=%q", "d integer", block)
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.nninh7\n  full stdout=%q", res.Stdout)
		}
		// Slice 279 (inherited-child counterpart of slice 277):
		// `ALTER TABLE public.idfnd_child ADD CONSTRAINT idfnd_child_pid_not_null
		// NOT NULL pid`, whose explicit name EQUALS the auto-name
		// `idfnd_child_pid_not_null`, must COLLAPSE the `CONSTRAINT` prefix in the
		// STANDALONE body form — the bare `NOT NULL pid` (pg_dump.c:17225-17232
		// emits `NOT NULL <col>` when notnull_constrs[j] is empty). idfnd_child's
		// body must print its local `extra` column AND `NOT NULL pid`; the inherited
		// `pid`/`pname` are suppressed and arrive via INHERITS. A regression storing
		// a non-default conname would leak `CONSTRAINT idfnd_child_pid_not_null`;
		// one that lost AlterTableAddNotNull would emit nothing for the NOT NULL.
		if start := strings.Index(res.Stdout, "CREATE TABLE public.idfnd_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the legacy child's local column; want %q in idfnd_child block\n  block=%q", "extra integer", block)
			}
			if !strings.Contains(block, "NOT NULL pid") {
				t.Errorf("pg_dump dropped the default-named NOT NULL on the inherited column; want %q in idfnd_child block\n  block=%q", "NOT NULL pid", block)
			}
			if strings.Contains(block, "CONSTRAINT idfnd_child_pid_not_null") {
				t.Errorf("pg_dump leaked the default constraint name from the ALTER path; want bare %q, not %q in idfnd_child block\n  block=%q", "NOT NULL pid", "CONSTRAINT idfnd_child_pid_not_null NOT NULL pid", block)
			}
			// The inherited columns must NOT be re-emitted as full columns.
			for _, inheritedCol := range []string{"pid integer", "pname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in idfnd_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.idfnd_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.idfnd_parent)") {
			t.Errorf("pg_dump dropped the legacy child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.idfnd_parent)", res.Stdout)
		}
		// Slice 280 (multi-column inherited NOT NULL body form — attnum ordering +
		// per-column collapse): mninh_child carries two conislocal NOT NULL
		// constraints on distinct inherited columns, added in reverse attnum order.
		// The body must (1) print the local `extra` column, (2) collapse `ma`'s
		// default-named constraint to the bare `NOT NULL ma`, (3) keep `mb`'s
		// non-default name as `CONSTRAINT mninh_named NOT NULL mb`, (4) emit `ma`
		// BEFORE `mb` (attnum order despite mb being ALTERed first), and (5) suppress
		// the inherited columns (they arrive via INHERITS).
		if start := strings.Index(res.Stdout, "CREATE TABLE public.mninh_child ("); start >= 0 {
			rest := res.Stdout[start:]
			end := strings.Index(rest, ");")
			if end < 0 {
				end = len(rest)
			}
			block := rest[:end]
			if !strings.Contains(block, "extra integer") {
				t.Errorf("pg_dump dropped the child's local column; want %q in mninh_child block\n  block=%q", "extra integer", block)
			}
			maIdx := strings.Index(block, "NOT NULL ma")
			mbIdx := strings.Index(block, "NOT NULL mb")
			if maIdx < 0 {
				t.Errorf("pg_dump dropped the default-named NOT NULL on inherited column ma; want %q in mninh_child block\n  block=%q", "NOT NULL ma", block)
			}
			if mbIdx < 0 {
				t.Errorf("pg_dump dropped the named NOT NULL on inherited column mb; want %q in mninh_child block\n  block=%q", "NOT NULL mb", block)
			}
			if strings.Contains(block, "CONSTRAINT mninh_child_ma_not_null") {
				t.Errorf("pg_dump leaked the default constraint name from the ALTER path; want bare %q, not %q in mninh_child block\n  block=%q", "NOT NULL ma", "CONSTRAINT mninh_child_ma_not_null NOT NULL ma", block)
			}
			if !strings.Contains(block, "CONSTRAINT mninh_named NOT NULL mb") {
				t.Errorf("pg_dump dropped the non-default constraint name on mb; want %q in mninh_child block\n  block=%q", "CONSTRAINT mninh_named NOT NULL mb", block)
			}
			if maIdx >= 0 && mbIdx >= 0 && maIdx >= mbIdx {
				t.Errorf("pg_dump emitted the standalone NOT NULL body items out of attnum order; want %q (attnum 1) before %q (attnum 2) despite mb being ALTERed first\n  block=%q", "NOT NULL ma", "NOT NULL mb", block)
			}
			// The inherited columns must NOT be re-emitted as full columns.
			for _, inheritedCol := range []string{"ma integer", "mb integer", "mname text"} {
				if strings.Contains(block, inheritedCol) {
					t.Errorf("pg_dump re-emitted inherited column %q in mninh_child (should arrive via INHERITS)\n  block=%q", inheritedCol, block)
				}
			}
		} else {
			t.Errorf("pg_dump missing CREATE TABLE public.mninh_child\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "INHERITS (public.mninh_parent)") {
			t.Errorf("pg_dump dropped the multi-col child's INHERITS clause; missing %q\n  full stdout=%q", "INHERITS (public.mninh_parent)", res.Stdout)
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
			// **Slice 297 (asserted):** a NESTED-arithmetic column DEFAULT must
			// round-trip with PG-faithful FULL parenthesization. The `calc` column
			// carries `DEFAULT (1 + 2) * 3`, parsed as Mul(Add(1,2), 3). PG's
			// pg_get_expr (prettyFlags=0, the mode pg_dump uses for pg_attrdef.adbin)
			// wraps every binary OpExpr node, so real PG 18.3 emits
			// `DEFAULT ((1 + 2) * 3)` (empirically verified). Before slice 297
			// formatExprForAttrdef rendered the operator WITHOUT parens, so the dump
			// emitted `DEFAULT 1 + 2 * 3` — which re-parses to Mul(1, Mul(2,3)) and
			// evaluates to 7, not 9, on restore: a SILENT precedence corruption.
			// The fix wraps each BinaryOp `(left op right)`; the recursion
			// parenthesizes the inner Add. Assert the fully-parenthesized form.
			if !strings.Contains(block, "calc integer DEFAULT ((1 + 2) * 3)") {
				t.Errorf("pg_dump dropped/corrupted the nested-arithmetic default; want %q in defcol block\n  block=%q", "calc integer DEFAULT ((1 + 2) * 3)", block)
			}
			// Guard the pre-fix corruption shape explicitly: the un-parenthesized
			// `DEFAULT 1 + 2 * 3` (precedence-changed) must NOT appear.
			if strings.Contains(block, "DEFAULT 1 + 2 * 3") {
				t.Errorf("pg_dump emitted the precedence-corrupted (un-parenthesized) nested-arithmetic default `DEFAULT 1 + 2 * 3`\n  block=%q", block)
			}
		}
		// **Slice 302 (asserted):** a UNARY-MINUS column DEFAULT on a compound
		// operand. The parser tags unary minus with OpUnaryNeg (NOT OpSub), but
		// both deparse twins only handled OpSub — so a `DEFAULT -…` fell through to
		// fmt.Sprintf("%v", e) and dumped a Go pointer string. `negdef.nb` carries
		// `DEFAULT -(1 + 2)` and `negdef.nc` carries `DEFAULT -(1 + 2) * 3`. PG's
		// get_rule_expr deparses a unary minus on a non-folded OpExpr as
		// `(- (operand))`, so real pg_dump 18.3 emits `DEFAULT (- (1 + 2))` and
		// `DEFAULT ((- (1 + 2)) * 3)` (empirically verified). Assert the PG-faithful
		// forms.
		for _, sub := range []string{
			"nb integer DEFAULT (- (1 + 2))",
			"nc integer DEFAULT ((- (1 + 2)) * 3)",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/corrupted a unary-minus default; want %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Guard the pre-fix corruption shape: the OpUnaryNeg fall-through dumped a
		// Go pointer string `&{… - 0x…}`. Its `DEFAULT &{` form must NOT appear.
		if strings.Contains(res.Stdout, "DEFAULT &{") {
			t.Errorf("pg_dump emitted the pre-fix Go-pointer-string corruption for a unary-minus default (`DEFAULT &{…}`)\n  full stdout=%q", res.Stdout)
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
			// **Slice 362:** a compound/function-call table-level CHECK round-trips
			// with PG's per-node parenthesization. `a > 0 AND b > 0` deparses each
			// operand with its own parens; `length(name) > 0` drops the spaces the
			// token reconstruction inserts around the call's parens. The legacy
			// `CHECK ((<raw>))` wrap produced `CHECK ((a > 0 AND b > 0))` and
			// `CHECK ((length ( name ) > 0))` — both byte-divergences from real pg_dump.
			"CONSTRAINT chkand_check CHECK (((a > 0) AND (b > 0)))",
			"CONSTRAINT chkor_check CHECK (((a < 0) OR (b > 10)))",
			"CONSTRAINT chkfn_name_check CHECK ((length(name) > 0))",
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
		// Slice 362 negative guards: the legacy token-text wrap would emit the
		// un-parenthesized compound predicate and the space-padded function call;
		// either is a byte-divergence that would re-parse with different precedence.
		for _, neg := range []string{
			"CHECK ((a > 0 AND b > 0))",
			"CHECK ((a < 0 OR b > 10))",
			"length ( name )",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted the legacy token-text CHECK wrap: %q\n  full stdout=%q", neg, res.Stdout)
			}
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
			// **Slice 315 (asserted):** COMMENT ON STATISTICS must round-trip. The
			// extended-statistics object only became dumpable in slice 314, so the
			// dumpStatisticsExt→dumpComment path that re-emits this line was never
			// exercised before. pg_dump keys it off pg_description (classoid=3381,
			// pg_statistic_ext) and renders the schema-qualified object name.
			"COMMENT ON STATISTICS public.statext_all IS 'a statistics comment';",
			// **Slice 370 (asserted):** COMMENT ON TRIGGER must round-trip. It was
			// silently swallowed (parser had no TRIGGER branch). The parser now
			// captures `TRIGGER <name> ON <table>` and execCommentOn keys the comment
			// under pg_trigger (classoid 2620). pg_dump's dumpTrigger calls dumpComment
			// with the trigger's catalogId (tableoid=2620) and re-emits the line below.
			"COMMENT ON TRIGGER trg_biu ON public.trig_t IS 'a trigger comment';",
			// **Slice 371 (asserted):** COMMENT ON POLICY must round-trip. It was
			// silently swallowed (parser had no POLICY branch). The parser now
			// captures `POLICY <name> ON <table>` and execCommentOn keys the comment
			// under pg_policy (classoid 3256). pg_dump's dumpPolicy calls dumpComment
			// with the policy's catalogId (tableoid=3256) and re-emits the line below.
			"COMMENT ON POLICY p_simple ON public.pol_t IS 'a policy comment';",
		}
		for _, sub := range comments {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a COMMENT; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 314 (asserted):** the CREATE STATISTICS objects themselves must
		// round-trip. Slice 314 wired the parser→catalog→pg_get_statisticsobjdef
		// path and created the two objects in the fixture, but never asserted the
		// emitted DDL through pg_dump. dumpStatisticsExt runs
		// pg_get_statisticsobjdef(oid) and emits the result verbatim + ';'. A
		// default object (all three kinds, >1 column) emits no kinds clause; an
		// explicit single-kind object emits `(ndistinct)`. Both forms are guarded
		// here so a regression in BuildStatisticsObjDef (kinds suppression, column
		// list, or schema qualification) is caught.
		statExtDDL := []string{
			"CREATE STATISTICS public.statext_all ON a, b FROM public.statext_t;",
			"CREATE STATISTICS public.statext_nd (ndistinct) ON b, c FROM public.statext_t;",
			// **Slice 316 (asserted):** expression extended-statistics objects must
			// round-trip. A single-expression object suppresses the kinds clause
			// (it must be expression stats); a column+expression object lists the
			// column first then the parenthesized expression. Mirrors ruleutils.c
			// pg_get_statisticsobj_worker. Before slice 316 BuildStatisticsObjDef
			// declined on HasExpr, so pg_dump dropped these objects entirely.
			"CREATE STATISTICS public.statext_expr ON (a + b) FROM public.statext_t;",
			"CREATE STATISTICS public.statext_mix ON a, (b + c) FROM public.statext_t;",
		}
		for _, sub := range statExtDDL {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a CREATE STATISTICS object; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// **Slice 317 (asserted):** a non-default extended-statistics target must
		// re-emit as `ALTER STATISTICS … SET STATISTICS <n>` after the CREATE
		// (pg_dump dumpStatisticsExt; fires when stxstattarget >= 0). `statext_nd`
		// was set to 250.
		if !strings.Contains(res.Stdout, "ALTER STATISTICS public.statext_nd SET STATISTICS 250;") {
			t.Errorf("pg_dump dropped the ALTER STATISTICS … SET STATISTICS target\n  full stdout=%q", res.Stdout)
		}
		// The default-target objects must NOT emit an ALTER STATISTICS line.
		if strings.Contains(res.Stdout, "ALTER STATISTICS public.statext_all SET STATISTICS") {
			t.Errorf("pg_dump emitted a spurious ALTER STATISTICS for a default-target object\n  full stdout=%q", res.Stdout)
		}
		// **Slice 318 (asserted):** every extended-statistics object must round-trip
		// its ownership as `ALTER STATISTICS <nsp>.<name> OWNER TO <role>;`. pg_dump
		// builds the STATISTICS archive entry with `.owner = getRoleName(stxowner)`
		// (pg_dump.c dumpStatisticsExt); the archiver's _printTocEntry then renders
		// the OWNER TO line because "STATISTICS" is in _getObjectDescription's
		// ALTER-able object list (pg_backup_archiver.c:3799). This exercises the
		// goopg-specific pg_statistic_ext.stxowner projection (=10, the bootstrap
		// superuser) end-to-end: if that cell regressed to NULL or a dangling OID,
		// getRoleName would fail and the OWNER TO line would vanish (or pg_dump would
		// error). Slices 314–317 dumped the CREATE/COMMENT/SET STATISTICS but never
		// asserted ownership. The role name matches the table OWNER TO above, so the
		// prefix is asserted (as with `ALTER TABLE public.foo OWNER TO`).
		for _, stxName := range []string{"statext_all", "statext_nd", "statext_expr", "statext_mix"} {
			want := "ALTER STATISTICS public." + stxName + " OWNER TO "
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped statistics ownership; missing %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// **Slice 319 (asserted):** a CREATE TRIGGER must round-trip. pg_dump's
		// getTriggers selects pg_get_triggerdef(t.oid, false) and dumpTrigger emits
		// the string verbatim with a trailing semicolon. Before this slice goopg's
		// pg_trigger view returned zero rows and pg_get_triggerdef was unimplemented,
		// so the trigger was silently dropped. The reconstruction mirrors ruleutils.c
		// pg_get_triggerdef_worker: timing keyword, OR-joined events in PG's fixed
		// order (INSERT, DELETE, UPDATE, TRUNCATE), schema-qualified table (pg_dump
		// runs search_path=''), FOR EACH ROW/STATEMENT, and EXECUTE FUNCTION with the
		// schema-qualified function. Assert the exact statements for both triggers.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_biu BEFORE INSERT OR UPDATE ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the BEFORE INSERT OR UPDATE row-level trigger\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_ad AFTER DELETE ON public.trig_t FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the AFTER DELETE statement-level trigger\n  full stdout=%q", res.Stdout)
		}
		// **Slice 326 (asserted):** a column-specific `UPDATE OF a, b` trigger must
		// round-trip. pg_get_triggerdef_worker appends ` OF <cols>` immediately
		// after the UPDATE event (the OR-ed events stay in PG's fixed INSERT,
		// DELETE, UPDATE order). Before this slice goopg's parser tripped on the
		// `OF` keyword and the column list was dropped, so the dumped trigger fired
		// on every column. Verified byte-identical to real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_uof BEFORE INSERT OR UPDATE OF a, b ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the column-specific UPDATE OF trigger\n  full stdout=%q", res.Stdout)
		}
		// **Slice 327 (asserted):** a CONSTRAINT TRIGGER must round-trip. pg_dump's
		// getTriggers emits pg_get_triggerdef, which (ruleutils.c
		// pg_get_triggerdef_worker, gated on a valid tgconstraint) renders
		// `CREATE CONSTRAINT TRIGGER` plus the `[NOT ]DEFERRABLE INITIALLY
		// {IMMEDIATE|DEFERRED}` clause between the ON-table name and FOR EACH ROW.
		// Before this slice the parser's CONSTRAINT branch was dead (it matched via
		// acceptIdentKeyword, but CONSTRAINT is a reserved keyword token) so
		// `CREATE CONSTRAINT TRIGGER` failed to parse outright. Verified
		// byte-identical to real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE CONSTRAINT TRIGGER trg_cdef AFTER INSERT ON public.trig_t NOT DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the NOT DEFERRABLE constraint trigger\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE CONSTRAINT TRIGGER trg_cdfr AFTER UPDATE ON public.trig_t DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the DEFERRABLE INITIALLY DEFERRED constraint trigger\n  full stdout=%q", res.Stdout)
		}
		// **Slice 328 (asserted):** a REFERENCING transition-table trigger must
		// round-trip. pg_get_triggerdef (ruleutils.c pg_get_triggerdef_worker)
		// reads pg_trigger.tgoldtable/tgnewtable and renders `REFERENCING OLD
		// TABLE AS … NEW TABLE AS …` (OLD before NEW) between the ON-table name
		// and FOR EACH ROW. Before this slice goopg's parser had no REFERENCING
		// branch, so such a trigger failed to parse. Verified byte-identical to
		// real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_ref AFTER UPDATE ON public.trig_t REFERENCING OLD TABLE AS ot NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the REFERENCING OLD/NEW transition-table trigger\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_refn AFTER INSERT ON public.trig_t REFERENCING NEW TABLE AS nt FOR EACH STATEMENT EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the REFERENCING NEW-only transition-table trigger\n  full stdout=%q", res.Stdout)
		}
		// **Slice 329 (asserted):** a trigger with a `WHEN (condition)` must
		// round-trip. pg_get_triggerdef (ruleutils.c pg_get_triggerdef_worker)
		// reads pg_trigger.tgqual and renders `WHEN (…)` between FOR EACH ROW and
		// EXECUTE FUNCTION, building OLD/NEW range-table entries so the column
		// references deparse with lowercased `old.`/`new.` qualifiers and the
		// boolean OpExpr is fully parenthesized (prettyFlags=0) → the comparison
		// renders `WHEN ((new.b <> old.b))`. Before this slice goopg's parser
		// skipped the WHEN body, so the condition was silently lost. Verified
		// byte-identical to real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_when BEFORE UPDATE ON public.trig_t FOR EACH ROW WHEN ((new.b <> old.b)) EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the WHEN-condition trigger (NEW vs OLD)\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_whna BEFORE INSERT ON public.trig_t FOR EACH ROW WHEN ((new.a > 0)) EXECUTE FUNCTION public.trig_fn();") {
			t.Errorf("pg_dump dropped/mangled the WHEN-condition trigger (NEW vs constant)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 368 (asserted):** a trigger whose EXECUTE FUNCTION carries STRING
		// arguments (TG_ARGV) must round-trip. pg_get_triggerdef (ruleutils.c
		// pg_get_triggerdef_worker:462-486) renders the call with each tgargs entry
		// comma-separated (`, `) and single-quoted via simple_quote_literal (embedded
		// single-quotes doubled → `wo''rld`). goopg's parser collected the args into
		// CreateTriggerStmt.FuncArgs, execCreateTrigger threaded them to
		// catalog.Trigger.Args, and buildTriggerDefString re-emitted them with the
		// same separation/escaping — this slice pins the rendered form (including an
		// embedded-quote arg) with an oracle fixture. Verified byte-identical to real
		// pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_arg AFTER INSERT ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn('hello', 'wo''rld');") {
			t.Errorf("pg_dump dropped/mangled the trigger function string arguments\n  full stdout=%q", res.Stdout)
		}
		// **Slice 369 (asserted):** a trigger whose EXECUTE FUNCTION carries
		// NON-string arguments (integer, float, bare identifier) must round-trip.
		// PG (gram.y TriggerFuncArg) stores every form as a string in tgargs — an
		// Iconst via psprintf("%d") (so "0042" → "42"), an FCONST by its lexeme, a
		// ColLabel by its text — and pg_get_triggerdef re-quotes them all as `'…'`
		// literals → `trig_fn('42', '3.14', 'foo')`. goopg's parser previously
		// dropped these tokens; it now captures their text (integers canonicalised)
		// so buildTriggerDefString emits the identical quoted call. Verified
		// byte-identical to real pg_dump 18.3.
		if !strings.Contains(res.Stdout, "CREATE TRIGGER trg_narg AFTER INSERT ON public.trig_t FOR EACH ROW EXECUTE FUNCTION public.trig_fn('42', '3.14', 'foo');") {
			t.Errorf("pg_dump dropped/mangled the trigger function non-string arguments\n  full stdout=%q", res.Stdout)
		}
		// **Slice 320 (asserted):** a clustered index must round-trip. pg_dump's
		// getIndexes reads pg_index.indisclustered and dumpIndex/dumpConstraint
		// append `ALTER TABLE <t> CLUSTER ON <idx>;` (index name unqualified)
		// after the index's CREATE INDEX / ADD CONSTRAINT when the flag is set
		// (pg_dump.c:18141 / :18483). Before this slice goopg hardcoded
		// indisclustered='f' and CLUSTER was a no-op, so the clustering selection
		// was silently dropped. goopg now records IsClustered on the chosen index
		// and re-syncs the pg_index heap row. Assert both the plain-index
		// (dumpIndex) and PRIMARY KEY constraint-index (dumpConstraint) surfaces.
		if !strings.Contains(res.Stdout, "ALTER TABLE public.clus_t CLUSTER ON clus_t_b_idx;") {
			t.Errorf("pg_dump dropped the CLUSTER ON for the plain secondary index\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ALTER TABLE public.clus_pk CLUSTER ON clus_pk_pkey;") {
			t.Errorf("pg_dump dropped the CLUSTER ON for the PRIMARY KEY constraint index\n  full stdout=%q", res.Stdout)
		}
		// **Slice 322 (asserted):** ROW LEVEL SECURITY must round-trip. pg_dump's
		// getPolicies reads pg_class.relrowsecurity and emits `ALTER TABLE <t>
		// ENABLE ROW LEVEL SECURITY;` (via a null-polname PolicyInfo); dumpTableSchema
		// reads relforcerowsecurity and emits `ALTER TABLE ONLY <t> FORCE ROW LEVEL
		// SECURITY;`. Before this slice goopg hardcoded both pg_class columns to 'f'
		// and consumed the ENABLE clause as a trigger no-op, dropping the RLS state.
		if !strings.Contains(res.Stdout, "ALTER TABLE public.rls_t ENABLE ROW LEVEL SECURITY;") {
			t.Errorf("pg_dump dropped ENABLE ROW LEVEL SECURITY for rls_t\n  full stdout=%q", res.Stdout)
		}
		if !strings.Contains(res.Stdout, "ALTER TABLE ONLY public.rls_t FORCE ROW LEVEL SECURITY;") {
			t.Errorf("pg_dump dropped FORCE ROW LEVEL SECURITY for rls_t\n  full stdout=%q", res.Stdout)
		}
		// **Slice 323 (asserted):** CREATE POLICY must round-trip. pg_dump's
		// getPolicies reads pg_policy and dumpPolicy re-emits the CREATE POLICY,
		// wrapping the (already fully-parenthesized) pg_get_expr output in one
		// more paren layer: `USING ((expr))` / `WITH CHECK ((expr))`. RESTRICTIVE
		// emits ` AS RESTRICTIVE`; FOR SELECT/INSERT emit ` FOR SELECT`/` FOR
		// INSERT`. All three policies are TO PUBLIC ({0}) so no TO clause is
		// emitted. Verified byte-identical to real pg_dump 18.3.
		for _, want := range []string{
			"CREATE POLICY p_simple ON public.pol_t USING ((a > 0));",
			"CREATE POLICY p_restr ON public.pol_t AS RESTRICTIVE FOR SELECT USING ((a > 5));",
			"CREATE POLICY p_check ON public.pol_t FOR INSERT WITH CHECK ((a < 100));",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped or mis-rendered policy: want %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// **Slice 330 (asserted):** a named-role policy round-trips its TO clause.
		// pg_dump's getPolicies resolves the polroles OID array back to the role
		// name via the pg_roles view, and dumpPolicy emits ` TO pol_role` between
		// the FOR clause and USING. Verified byte-identical to real pg_dump 18.3.
		if want := "CREATE POLICY p_role ON public.pol_rt FOR SELECT TO pol_role USING ((a > 0));"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered named-role policy: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 331 (asserted):** a table-level GRANT round-trips. pg_dump reads
		// the materialized pg_class.relacl, diffs it against acldefault('r', 10)
		// (the owner's own entry cancels), and buildACLCommands emits a single
		// `GRANT <priv> ON TABLE <nsp>.<name> TO <grantee>;`. Verified
		// byte-identical to real pg_dump 18.3.
		if want := "GRANT SELECT ON TABLE public.grant_t TO grantee_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered table GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 332 (asserted):** a GRANT … WITH GRANT OPTION round-trips. The
		// grantee's relacl entry carries "r*" (grant option), which pg_dump's
		// buildACLCommands routes through the privswgo branch into a dedicated
		// `GRANT … WITH GRANT OPTION;`. Verified byte-identical to real pg_dump 18.3.
		if want := "GRANT SELECT ON TABLE public.grant_g TO grantee2_role WITH GRANT OPTION;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered WITH GRANT OPTION: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 333 (asserted):** a GRANT … ON SEQUENCE round-trips. pg_dump
		// reads the sequence's relacl, diffs it against acldefault('s', 10) =
		// "{postgres=rwU/postgres}", and dumpACL (objtype "SEQUENCE") emits a
		// single `GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role;`. Verified
		// byte-identical to real pg_dump 18.3.
		if want := "GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered sequence GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 334 (asserted):** a GRANT … TO PUBLIC round-trips. PostgreSQL
		// stores the grant with an empty grantee in relacl ("=r/postgres"), and
		// pg_dump's buildACLCommands renders the empty grantee as the keyword
		// PUBLIC, emitting `GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;`.
		// Verified byte-identical to real pg_dump 18.3.
		if want := "GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered GRANT TO PUBLIC: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 335 (asserted):** a GRANT … ON SCHEMA round-trips. pg_dump reads
		// pg_namespace.nspacl, diffs it against acldefault('n', 10) =
		// "{postgres=UC/postgres}", and dumpACL (objtype "SCHEMA") emits a single
		// `GRANT USAGE ON SCHEMA grant_sch TO schema_role;`. Verified byte-identical
		// to real pg_dump 18.3.
		if want := "GRANT USAGE ON SCHEMA grant_sch TO schema_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered schema GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 336 (asserted):** a GRANT to a role whose name needs quoting
		// round-trips. PG's aclitemout double-quotes the hyphenated grantee in
		// relacl ("weird-role"=r/postgres); pg_dump parses the quoted name and
		// re-emits it via fmtId (also quoted). goopg previously rendered the
		// grantee raw, which pg_dump would mis-parse at the hyphen. Verified
		// byte-identical to real pg_dump 18.3.
		if want := `GRANT SELECT ON TABLE public.grant_q TO "weird-role";`; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered quoted-role GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 337 (asserted):** a GRANT to a case-significant (double-quoted
		// mixed-case) role round-trips. PG stores the role's true case in relacl
		// (MixedCase=r/postgres, bare because all-alnum); pg_dump re-quotes it via
		// fmtId → TO "MixedCase". goopg lower-cases the ACL store key but now
		// preserves the original spelling for rendering. Verified byte-identical
		// to real pg_dump 18.3.
		if want := `GRANT SELECT ON TABLE public.grant_mc TO "MixedCase";`; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered mixed-case-role GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 338 (asserted):** a GRANT … then partial REVOKE round-trips.
		// GRANT SELECT, INSERT then REVOKE INSERT leaves relacl as
		// revoke_role=r/postgres, so pg_dump re-emits only the surviving SELECT
		// grant — NOT the revoked INSERT. goopg's REVOKE recorder now clears the
		// privilege bit; previously REVOKE was a no-op and the dump over-emitted
		// the INSERT. Verified byte-identical to real pg_dump 18.3.
		if want := "GRANT SELECT ON TABLE public.revoke_t TO revoke_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the surviving SELECT grant after REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		if bad := "INSERT ON TABLE public.revoke_t TO revoke_role"; strings.Contains(res.Stdout, bad) {
			t.Errorf("pg_dump re-emitted the REVOKEd INSERT grant: unexpected %q\n  full stdout=%q", bad, res.Stdout)
		}
		// **Slice 339 (asserted):** a schema GRANT … then partial REVOKE round-trips
		// (the nspacl analogue of slice 338). GRANT USAGE, CREATE ON SCHEMA then
		// REVOKE CREATE leaves nspacl as revoke_sch_role=U/postgres, so pg_dump
		// re-emits only the surviving USAGE grant — NOT the revoked CREATE. goopg's
		// REVOKE recorder now routes ON SCHEMA to recordSchemaRevoke and clears the
		// privilege bit; previously the schema REVOKE was a no-op and the dump
		// over-emitted CREATE. Verified byte-identical to real pg_dump 18.3.
		if want := "GRANT USAGE ON SCHEMA revoke_sch TO revoke_sch_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the surviving USAGE grant after schema REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		if bad := "CREATE ON SCHEMA revoke_sch TO revoke_sch_role"; strings.Contains(res.Stdout, bad) {
			t.Errorf("pg_dump re-emitted the REVOKEd CREATE schema grant: unexpected %q\n  full stdout=%q", bad, res.Stdout)
		}
		// **Slice 340 (asserted):** an owner-side REVOKE-of-default round-trips.
		// `REVOKE TRIGGER ON TABLE ownrev_t FROM postgres` materializes relacl as
		// `{postgres=arwdDxm/postgres}` (the owner default minus 't'), which
		// pg_dump's buildACLCommands renders as a `REVOKE ALL … FROM postgres;`
		// followed by a `GRANT <remaining> … TO postgres;` whose privilege list
		// omits TRIGGER. Both lines (and the exact privilege list) are asserted
		// byte-identical to real pg_dump 18.3. Before this slice the owner revoke
		// was a no-op (relacl stayed NULL) so neither line was emitted.
		if want := "REVOKE ALL ON TABLE public.ownrev_t FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the owner-side REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		if want := "GRANT SELECT,INSERT,REFERENCES,DELETE,TRUNCATE,MAINTAIN,UPDATE ON TABLE public.ownrev_t TO postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped/mis-rendered the owner re-GRANT after revoke: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// The revoked TRIGGER must NOT reappear in the owner re-GRANT.
		if strings.Contains(res.Stdout, "TRIGGER ON TABLE public.ownrev_t TO postgres") {
			t.Errorf("pg_dump re-granted the REVOKEd TRIGGER to the owner\n  full stdout=%q", res.Stdout)
		}
		// **Slice 341 (asserted):** a full owner-side REVOKE ALL round-trips as the
		// empty aclitem array. `REVOKE ALL ON TABLE ownrevall_t FROM postgres`
		// leaves relacl = `{}`, which pg_dump renders as a bare
		// `REVOKE ALL … FROM postgres;` with NO re-GRANT (the owner retains
		// nothing). Asserted byte-identical to real pg_dump 18.3. Before this slice
		// the owner REVOKE ALL reverted relacl to NULL so pg_dump emitted nothing.
		if want := "REVOKE ALL ON TABLE public.ownrevall_t FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the owner-side REVOKE ALL: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// No GRANT … ownrevall_t … TO postgres must follow (the owner holds no
		// privileges after REVOKE ALL).
		if strings.Contains(res.Stdout, "GRANT") && strings.Contains(res.Stdout, "ownrevall_t TO postgres") {
			t.Errorf("pg_dump re-granted privileges to the owner after REVOKE ALL\n  full stdout=%q", res.Stdout)
		}
		// **Slice 342 (asserted):** a full owner-side REVOKE ALL ON SCHEMA round-trips
		// as the empty nspacl array (the namespace analogue of slice 341). `REVOKE ALL
		// ON SCHEMA ownrevall_sch FROM postgres` leaves nspacl = `{}`, which pg_dump
		// renders as a bare `REVOKE ALL ON SCHEMA … FROM postgres;` with NO re-GRANT.
		// Before this slice the owner schema REVOKE ALL was a no-op (nspacl stayed
		// NULL) so pg_dump emitted nothing.
		if want := "REVOKE ALL ON SCHEMA ownrevall_sch FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the owner-side schema REVOKE ALL: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// No GRANT … ON SCHEMA ownrevall_sch … TO postgres must follow (the owner
		// holds no schema privileges after REVOKE ALL).
		if strings.Contains(res.Stdout, "GRANT") && strings.Contains(res.Stdout, "ON SCHEMA ownrevall_sch TO postgres") {
			t.Errorf("pg_dump re-granted schema privileges to the owner after REVOKE ALL\n  full stdout=%q", res.Stdout)
		}
		// **Slice 343 (asserted):** a full owner-side REVOKE ALL ON SEQUENCE round-trips
		// as the empty relacl array (the sequence analogue of slices 341/342). `REVOKE
		// ALL ON SEQUENCE ownrevall_seq FROM postgres` leaves relacl = `{}`, which
		// pg_dump renders as a bare `REVOKE ALL ON SEQUENCE … FROM postgres;` with NO
		// re-GRANT. Asserted byte-identical to real pg_dump 18.3. The server already
		// wired this (recordTableRevoke passes allSequencePrivileges to
		// MaterializeOwnerACL), so this guards against a regression that would revert
		// the sequence relacl to NULL and silently restore the owner's default privs.
		if want := "REVOKE ALL ON SEQUENCE public.ownrevall_seq FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the owner-side sequence REVOKE ALL: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// No GRANT … ownrevall_seq … TO postgres must follow (the owner holds no
		// sequence privileges after REVOKE ALL).
		if strings.Contains(res.Stdout, "GRANT") && strings.Contains(res.Stdout, "ON SEQUENCE public.ownrevall_seq TO postgres") {
			t.Errorf("pg_dump re-granted sequence privileges to the owner after REVOKE ALL\n  full stdout=%q", res.Stdout)
		}
		// **Slice 344 (asserted):** owner-zero coexisting with a grantee. After
		// `REVOKE ALL ON TABLE ownerzero_t FROM postgres` then
		// `GRANT SELECT … TO bob`, PostgreSQL stores relacl = `{bob=r/postgres}`
		// (owner absent). pg_dump must emit BOTH the owner's REVOKE ALL and the
		// grantee GRANT. Before this slice goopg's owner-default fallback rendered
		// `{postgres=arwdDxtm/postgres,bob=r/postgres}`, so pg_dump saw the owner
		// holding its full default and dropped the REVOKE ALL — silently restoring
		// the owner privileges on restore.
		if want := "REVOKE ALL ON TABLE public.ownerzero_t FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the owner REVOKE ALL when a grantee coexists: want %q\n  full stdout=%q", want, res.Stdout)
		}
		if want := "GRANT SELECT ON TABLE public.ownerzero_t TO bob;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped the grantee GRANT alongside the owner REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// The owner must NOT be re-granted any privilege on ownerzero_t (it holds
		// zero after REVOKE ALL; only bob has SELECT).
		if strings.Contains(res.Stdout, "ownerzero_t TO postgres") {
			t.Errorf("pg_dump re-granted privileges to the zeroed owner of ownerzero_t\n  full stdout=%q", res.Stdout)
		}
		// **Slice 345 (asserted):** a function-level GRANT round-trips from
		// pg_proc.proacl (the routine analogue of slice 331's table relacl).
		// `GRANT EXECUTE ON FUNCTION public.grantfn(integer) TO func_grantee`
		// materializes proacl as "{=X/postgres,postgres=X/postgres,func_grantee=X/postgres}";
		// pg_dump's getFuncs diffs it against acldefault('f', 10) and emits a single
		// `GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;` (EXECUTE
		// is the only function privilege, so the grantee's full set renders ALL).
		// Verified byte-identical to real pg_dump 18.3. Before this slice goopg left
		// every routine's proacl NULL, so the function GRANT was dropped.
		if want := "GRANT ALL ON FUNCTION public.grantfn(integer) TO func_grantee;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the function GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 346 (asserted):** a function-level REVOKE … FROM PUBLIC round-trips
		// from pg_proc.proacl (the routine analogue of the table REVOKE slices 338+).
		// `REVOKE EXECUTE ON FUNCTION public.revokefn(integer) FROM PUBLIC` materializes
		// proacl as "{postgres=X/postgres}"; pg_dump's getFuncs diffs it against
		// acldefault('f', 10) = "{=X/postgres,postgres=X/postgres}" and emits a single
		// `REVOKE ALL ON FUNCTION public.revokefn(integer) FROM PUBLIC;`. Verified
		// byte-identical to real pg_dump 18.3. Before this slice goopg treated the
		// function REVOKE as a no-op (proacl NULL), silently restoring PUBLIC's
		// default EXECUTE on restore.
		if want := "REVOKE ALL ON FUNCTION public.revokefn(integer) FROM PUBLIC;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the function REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 347 (asserted):** the owner-side function REVOKE, counterpart of
		// slice 346. `REVOKE EXECUTE ON FUNCTION public.ownrevfn(integer) FROM
		// postgres` materializes proacl as "{=X/postgres}" (PUBLIC's implicit
		// EXECUTE survives, the owner's is removed); pg_dump's getFuncs diffs it
		// against acldefault('f', 10) and emits a single `REVOKE ALL ON FUNCTION
		// public.ownrevfn(integer) FROM postgres;`. Verified byte-identical to real
		// pg_dump 18.3. The dump must NOT also emit a `… FROM PUBLIC;` line (PUBLIC
		// retains its default) — that would mean goopg emptied proacl to {}.
		if want := "REVOKE ALL ON FUNCTION public.ownrevfn(integer) FROM postgres;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the owner-side function REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		if notWant := "REVOKE ALL ON FUNCTION public.ownrevfn(integer) FROM PUBLIC;"; strings.Contains(res.Stdout, notWant) {
			t.Errorf("pg_dump wrongly revoked PUBLIC's surviving default EXECUTE on ownrevfn: unexpected %q\n  full stdout=%q", notWant, res.Stdout)
		}
		// **Slice 348 (asserted):** a function GRANT … WITH GRANT OPTION round-trips.
		// `GRANT EXECUTE ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT
		// OPTION` materializes proacl as "{=X/postgres,postgres=X/postgres,
		// gofn_grantee=X*/postgres}"; pg_dump's buildACLCommands routes the
		// grant-option EXECUTE to its privswgo branch and emits a single `GRANT ALL
		// ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT OPTION;`
		// (EXECUTE is the sole function privilege, so the grantee's full set renders
		// as ALL). Verified byte-identical to real pg_dump 18.3. Before this slice
		// goopg dropped the grant-option flag, emitting a plain `GRANT ALL …;`.
		if want := "GRANT ALL ON FUNCTION public.gofn(integer) TO gofn_grantee WITH GRANT OPTION;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the function WITH GRANT OPTION: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 349 (asserted):** a sequence GRANT … WITH GRANT OPTION round-trips.
		// `GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION`
		// materializes relacl as "{postgres=rwU/postgres,seq_wgo_role=U*/postgres}";
		// pg_dump's buildACLCommands routes the grant-option USAGE to its privswgo
		// branch and emits a single `GRANT USAGE ON SEQUENCE public.gowgo_seq TO
		// seq_wgo_role WITH GRANT OPTION;`. Unlike the function case (slice 348) the
		// privilege stays USAGE — sequences expose three privileges so a single one
		// does not collapse to ALL. Verified byte-identical to real pg_dump 18.3.
		// Before grant-option threading goopg dropped the flag, emitting a plain
		// `GRANT USAGE …;`.
		if want := "GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the sequence WITH GRANT OPTION: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 357 (asserted, M0119-0004-ACLHEAP):** a TYPE-level GRANT round-trips
		// from the heap-backed pg_type.typacl — the new capability this milestone adds.
		// `GRANT USAGE ON TYPE public.gtype TO typg_grantee` runs through the executor
		// (not the server virtual-ACL fast path), which updates the OID-keyed ACL store
		// and re-syncs the pg_type heap row's typacl to a PG-native _aclitem array
		// "{postgres=U/postgres,=U/postgres,typg_grantee=U/postgres}". pg_dump's getTypes
		// reads typacl back (decoded to canonical aclitemout text by the seqscan hook),
		// diffs it against acldefault('T', 10) = "{=U/postgres,postgres=U/postgres}", and
		// buildACLCommands emits a single `GRANT ALL ON TYPE public.gtype TO typg_grantee;`
		// (USAGE is the sole type privilege, so the grantee's full set renders ALL, like a
		// function's EXECUTE). Verified against real pg_dump 18.3. Before this milestone
		// goopg baked typacl NULL on every pg_type row and bailed on TYPE GRANT, dropping
		// it from the dump.
		if want := "GRANT ALL ON TYPE public.gtype TO typg_grantee;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the TYPE GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 358 (asserted, M0119-0004-ACLHEAP attacl half):** a column-level
		// GRANT round-trips from the heap-backed pg_attribute.attacl — the column
		// analogue of slice 357. `GRANT SELECT (cola) ON TABLE public.gcoltbl TO
		// colgrantee` runs through the executor (not the server virtual fast path),
		// which updates the (relOID, attnum)-keyed column ACL store and re-syncs the
		// pg_attribute heap row's attacl to "{colgrantee=r/postgres}". pg_dump's
		// getAdditionalACLs finds the non-NULL attacl, getColumnACLs reads it back
		// (decoded by the seqscan hook), and buildACLCommands emits the column GRANT
		// with the privilege keyword carrying the column name in parentheses
		// (AddAcl → "SELECT(cola)"). Verified against real pg_dump 18.3. Before this
		// milestone goopg baked attacl NULL on every pg_attribute row, dropping every
		// column GRANT from the dump.
		if want := "GRANT SELECT(cola) ON TABLE public.gcoltbl TO colgrantee;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the column GRANT: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// **Slice 350 (asserted):** a sequence GRANT followed by a partial REVOKE
		// round-trips (the sequence analogue of slices 338/339). `GRANT USAGE, SELECT
		// ON SEQUENCE … TO seqrev_role` then `REVOKE SELECT …` leaves relacl =
		// "{postgres=rwU/postgres,seqrev_role=U/postgres}"; pg_dump diffs that against
		// acldefault('s', 10) and re-emits only `GRANT USAGE ON SEQUENCE
		// public.seqrev_seq TO seqrev_role;` — NOT the revoked SELECT. Verified
		// byte-identical to real pg_dump 18.3. A regression that left SELECT in the
		// relacl would over-emit `GRANT SELECT, USAGE …`.
		if want := "GRANT USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the sequence partial REVOKE: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// The revoked SELECT must NOT reappear (relacl must carry only USAGE for the
		// grantee). pg_dump would render a surviving SELECT as `GRANT SELECT, USAGE`.
		if notWant := "GRANT SELECT, USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;"; strings.Contains(res.Stdout, notWant) {
			t.Errorf("pg_dump over-emitted the revoked SELECT on seqrev_seq: unexpected %q\n  full stdout=%q", notWant, res.Stdout)
		}
		// **Slice 351 (asserted):** a table GRANT ALL collapses to the `ALL` keyword.
		// `GRANT ALL ON TABLE public.grantall_t TO grantall_role` materializes relacl
		// as "{postgres=arwdDxtm/postgres,grantall_role=arwdDxtm/postgres}" (the
		// grantee holds every table privilege). pg_dump's buildACLCommands recognises
		// the grantee's full set equals ACL_ALL_RIGHTS_RELATION and re-emits the
		// single `GRANT ALL ON TABLE public.grantall_t TO grantall_role;` rather than
		// an eight-way list. The table analogue of the function (slice 345) and
		// sequence (slice 333) GRANT ALL collapses. Verified byte-identical to real
		// pg_dump 18.3.
		if want := "GRANT ALL ON TABLE public.grantall_t TO grantall_role;"; !strings.Contains(res.Stdout, want) {
			t.Errorf("pg_dump dropped or mis-rendered the table GRANT ALL: want %q\n  full stdout=%q", want, res.Stdout)
		}
		// A regression that dropped a privilege bit from the grantee's relacl would
		// make pg_dump list the survivors explicitly (e.g. `GRANT INSERT, SELECT, …`)
		// instead of collapsing to `ALL`; guard against the SELECT-led explicit form.
		if notWant := "GRANT INSERT, SELECT"; strings.Contains(res.Stdout, notWant+" ON TABLE public.grantall_t") {
			t.Errorf("pg_dump failed to collapse the table GRANT ALL to ALL: unexpected explicit list\n  full stdout=%q", res.Stdout)
		}
		// **Slice 352 (asserted):** two distinct grantees on one table each emit
		// their own GRANT line. relacl materializes as
		// "{postgres=arwdDxtm/postgres,mg_role_a=r/postgres,mg_role_b=a/postgres}";
		// pg_dump's buildACLCommands fans out one `GRANT <privs> … TO <grantee>;`
		// per non-owner aclitem, so the dump must carry BOTH the SELECT line for
		// mg_role_a and the INSERT line for mg_role_b. Verified byte-identical to
		// real pg_dump 18.3.
		for _, want := range []string{
			"GRANT SELECT ON TABLE public.multigrant_t TO mg_role_a;",
			"GRANT INSERT ON TABLE public.multigrant_t TO mg_role_b;",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped a per-grantee GRANT line: want %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// **Slice 353 (asserted):** two grantees with the SAME privilege set still
		// emit two separate GRANT lines — PostgreSQL never merges grantees, even when
		// their privileges are identical. relacl materializes as
		// "{postgres=arwdDxtm/postgres,sg_role_a=r/postgres,sg_role_b=r/postgres}";
		// pg_dump's buildACLCommands fans out one `GRANT SELECT … TO <grantee>;` per
		// non-owner aclitem, so the dump must carry BOTH SELECT lines. Verified
		// byte-identical to real pg_dump 18.3.
		for _, want := range []string{
			"GRANT SELECT ON TABLE public.samegrant_t TO sg_role_a;",
			"GRANT SELECT ON TABLE public.samegrant_t TO sg_role_b;",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped a same-priv per-grantee GRANT line: want %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// A grantee-merge regression would collapse the two grantees into one
		// `GRANT SELECT … TO sg_role_a, sg_role_b;` line; PostgreSQL never does this,
		// so guard against the merged form explicitly.
		if notWant := "TO sg_role_a, sg_role_b"; strings.Contains(res.Stdout, notWant) {
			t.Errorf("pg_dump wrongly merged same-priv grantees into one GRANT line: unexpected %q\n  full stdout=%q", notWant, res.Stdout)
		}
		// **Slice 354 (asserted):** grantee GRANT lines emit in GRANT ORDER, not
		// alphabetically. og_role_z was granted before og_role_a, so PostgreSQL's
		// relacl is "{postgres=arwdDxtm/postgres,og_role_z=r/postgres,og_role_a=r/
		// postgres}" (z before a) and pg_dump fans the aclitem array out in array
		// order → the og_role_z GRANT line precedes the og_role_a one. Both lines
		// must be present AND in z-before-a order; the pre-fix sort.Strings rendering
		// would have emitted og_role_a first, diverging from real pg_dump 18.3.
		ogZ := "GRANT SELECT ON TABLE public.ordergrant_t TO og_role_z;"
		ogA := "GRANT SELECT ON TABLE public.ordergrant_t TO og_role_a;"
		ogZi := strings.Index(res.Stdout, ogZ)
		ogAi := strings.Index(res.Stdout, ogA)
		if ogZi < 0 || ogAi < 0 {
			t.Errorf("pg_dump dropped a grant-order GRANT line: og_role_z present=%v og_role_a present=%v\n  full stdout=%q", ogZi >= 0, ogAi >= 0, res.Stdout)
		} else if ogZi > ogAi {
			t.Errorf("pg_dump emitted grantees out of grant order: og_role_z (granted first) must precede og_role_a, got z@%d a@%d\n  full stdout=%q", ogZi, ogAi, res.Stdout)
		}
		// **Slice 355 (asserted):** REVOKE-then-re-GRANT moves a grantee to the END
		// of the relacl array. rg_role_a was granted SELECT first, then rg_role_b,
		// then rg_role_a's SELECT was REVOKEd (deleting its aclitem) and INSERT
		// re-GRANTed (appending a fresh aclitem at the end). So relacl is
		// "{postgres=arwdDxtm/postgres,rg_role_b=r/postgres,rg_role_a=a/postgres}"
		// and pg_dump fans the array out in order → the rg_role_b SELECT line
		// precedes the rg_role_a INSERT line (b before a, the reverse of both
		// alphabetical and original grant order). Both lines must be present AND in
		// b-before-a order.
		rgB := "GRANT SELECT ON TABLE public.regrant_t TO rg_role_b;"
		rgA := "GRANT INSERT ON TABLE public.regrant_t TO rg_role_a;"
		rgBi := strings.Index(res.Stdout, rgB)
		rgAi := strings.Index(res.Stdout, rgA)
		if rgBi < 0 || rgAi < 0 {
			t.Errorf("pg_dump dropped a re-grant-order GRANT line: rg_role_b present=%v rg_role_a present=%v\n  full stdout=%q", rgBi >= 0, rgAi >= 0, res.Stdout)
		} else if rgBi > rgAi {
			t.Errorf("pg_dump emitted re-granted grantee out of order: rg_role_b (still-held SELECT) must precede rg_role_a (revoked+re-granted INSERT), got b@%d a@%d\n  full stdout=%q", rgBi, rgAi, res.Stdout)
		}
		// **Slice 324 (asserted):** an unconditional DO-NOTHING CREATE RULE must
		// round-trip. pg_dump's getRules reads pg_rewrite and dumpRule prints
		// pg_get_ruledef(oid) verbatim; the single-arg pg_get_ruledef uses
		// PRETTYFLAG_INDENT, so the event line is broken onto a new line indented
		// four spaces (`CREATE RULE r AS\n    ON <EVENT> TO public.rule_t DO
		// [INSTEAD ]NOTHING;`). DO ALSO NOTHING and plain DO NOTHING both render
		// without the INSTEAD keyword. Verified byte-identical to real pg_dump 18.3.
		for _, want := range []string{
			"CREATE RULE r_noins AS\n    ON INSERT TO public.rule_t DO INSTEAD NOTHING;",
			"CREATE RULE r_noupd AS\n    ON UPDATE TO public.rule_t DO NOTHING;",
			"CREATE RULE r_nodel AS\n    ON DELETE TO public.rule_t DO NOTHING;",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped or mis-rendered rule: want %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// **Slice 325 (asserted):** a rule whose pg_rewrite.ev_enabled is not 'O'
		// round-trips an `ALTER TABLE … RULE` clause emitted by dumpRule in addition
		// to the CREATE RULE. r_noupd was DISABLEd ('D') and r_nodel set ENABLE
		// ALWAYS ('A'); each yields the exact clause below. Verified byte-identical to
		// real pg_dump 18.3.
		for _, want := range []string{
			"ALTER TABLE public.rule_t DISABLE RULE r_noupd;",
			"ALTER TABLE public.rule_t ENABLE ALWAYS RULE r_nodel;",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped ALTER TABLE … RULE clause: want %q\n  full stdout=%q", want, res.Stdout)
			}
		}
		// r_noins stays origin ('O'); dumpRule emits NO ALTER TABLE … RULE for it.
		if strings.Contains(res.Stdout, "RULE r_noins;") {
			t.Errorf("pg_dump emitted a spurious ALTER TABLE … RULE r_noins (rule is origin-enabled)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 359 (asserted):** a CONDITIONAL DO-NOTHING CREATE RULE must
		// round-trip its WHERE clause. pg_get_ruledef's PRETTYFLAG_INDENT layout
		// puts WHERE on its own line (3-space indent) and trails DO INSTEAD NOTHING
		// on that line; the qual is the single-paren `(old.x <> new.x)` form
		// regardless of whether the source had outer parens. Verified byte-identical
		// to real pg_dump 18.3 (reference /tmp/du359_ref).
		for _, want := range []string{
			"CREATE RULE rcond_upd AS\n    ON UPDATE TO public.rcond\n   WHERE (old.a <> new.a) DO INSTEAD NOTHING;",
			"CREATE RULE rcond_del AS\n    ON DELETE TO public.rcond\n   WHERE (old.b > 0) DO INSTEAD NOTHING;",
		} {
			if !strings.Contains(res.Stdout, want) {
				t.Errorf("pg_dump dropped or mis-rendered conditional rule: want %q\n  full stdout=%q", want, res.Stdout)
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
		// **Slice 301 (asserted):** the function whose parameter DEFAULT is a
		// nested-arithmetic binary op (add_calcdef) must round-trip the default as
		// the FULLY parenthesized `((1 + 2) * 3)` — the function-arg-default deparse
		// context of executor.defaultExprToSQL (the fourth, after slice 298 index
		// predicate / 299 index column / 300 partition key). pg_dump reads the
		// signature from pg_get_function_arguments(oid); goopg stores the canonical
		// `((1 + 2) * 3)` in ArgDefaults and buildFunctionArguments emits it verbatim
		// after ` DEFAULT `. A one-paren-short `(1 + 2) * 3` or a precedence-corrupted
		// `1 + 2 * 3` (evaluates to 7 not 9 on restore) surfaces exactly here. Real
		// pg_dump 18.3 renders (verified byte-identical):
		//   CREATE FUNCTION public.add_calcdef(a integer DEFAULT ((1 + 2) * 3)) RETURNS integer
		//       LANGUAGE sql
		//       AS $_$ SELECT $1 $_$;
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.add_calcdef(a integer DEFAULT ((1 + 2) * 3)) RETURNS integer") {
			t.Errorf("pg_dump dropped/corrupted add_calcdef's nested-arithmetic parameter default; want fully-parenthesized %q\n  full stdout=%q", "DEFAULT ((1 + 2) * 3)", res.Stdout)
		}
		// Negative guard: the precedence-corrupted one-paren-short form must NOT appear.
		if strings.Contains(res.Stdout, "add_calcdef(a integer DEFAULT (1 + 2) * 3)") {
			t.Errorf("pg_dump emitted the one-paren-short add_calcdef default (re-parses with wrong precedence)\n  full stdout=%q", res.Stdout)
		}
		// **Slice 302 (asserted, executor twin):** a UNARY-MINUS function-argument
		// default. pg_dump rebuilds the signature from pg_get_function_arguments,
		// which goopg answers from catalog.Routine.ArgDefaults (populated via
		// executor.defaultExprToSQL). Before slice 302 the unary minus matched the
		// dead OpSub arm and stored a Go pointer string; it now renders
		// `(- (1 + 2))`, so real pg_dump 18.3 emits (empirically verified):
		//   CREATE FUNCTION public.fneg(x integer DEFAULT (- (1 + 2))) RETURNS integer
		if !strings.Contains(res.Stdout, "CREATE FUNCTION public.fneg(x integer DEFAULT (- (1 + 2))) RETURNS integer") {
			t.Errorf("pg_dump dropped/corrupted fneg's unary-minus parameter default; want %q\n  full stdout=%q", "DEFAULT (- (1 + 2))", res.Stdout)
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
			// partial index predicate. Slice 298 PRODUCTION fix: real pg_dump 18.3
			// fully parenthesizes the predicate (pg_get_expr prettyFlags=0), so even a
			// single top-level comparison dumps as `WHERE (qty > 0)`, not the bare
			// `WHERE qty > 0` goopg previously emitted — a fidelity divergence the
			// old self-substring assertion masked.
			"CREATE INDEX foo_qty_partial_idx ON public.foo USING btree (qty) WHERE (qty > 0);",
			// Slice 298: nested arithmetic in the predicate must be fully
			// parenthesized to preserve precedence on restore (verified byte-identical
			// vs real pg_dump 18.3).
			"CREATE INDEX foo_calc_partial_idx ON public.foo USING btree (qty) WHERE (((qty + id) * mgr_id) > 0);",
			// Slice 299: a nested-arithmetic expression-index COLUMN. pg_get_indexdef
			// wraps the deparsed key `((qty + id) * mgr_id)` in `(%s)` inside the
			// `USING btree (...)` column-list parens → four nested parens, byte-identical
			// to real pg_dump 18.3. Locks in slice 298's defaultExprToSQL BinaryOp
			// parenthesization in the index-key-expression deparse context.
			"CREATE INDEX foo_calc_expr_idx ON public.foo USING btree ((((qty + id) * mgr_id)));",
			// Slice 360: a bare function-call key dumps WITHOUT the extra wrapping
			// parens (pg_get_indexdef_worker prints a COERCE_EXPLICIT_CALL FuncExpr
			// as-is); one paren level only, NOT the double-paren the arithmetic key
			// above carries. Byte-identical to real pg_dump 18.3.
			"CREATE INDEX foo_lower_idx ON public.foo USING btree (lower(name));",
			"CREATE INDEX foo_lpad_idx ON public.foo USING btree (lpad(name, 5));",
			// Slice 361: a `USING hash` index must dump `USING hash`, not the
			// B-tree substrate's `USING btree`. goopg builds a hash index on the
			// B-tree substrate (catalog.Index.Method stays "btree"; only
			// DeclaredHash records the declared method), and BuildIndexDef now
			// surfaces DeclaredHash so pg_get_indexdef_worker's `USING %s`
			// (pg_am.amname) round-trips byte-identically to real pg_dump 18.3.
			"CREATE INDEX foo_qty_hash_idx ON public.foo USING hash (qty);",
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
		// Slice 298 negative guard: the precedence-corrupt under-parenthesized
		// predicate must NOT appear — its presence would mean a restore re-parses
		// `(qty + id) * mgr_id` as `qty + (id * mgr_id)`, silently changing which
		// rows the partial index covers.
		if strings.Contains(res.Stdout, "WHERE qty + id * mgr_id") {
			t.Errorf("pg_dump emitted a precedence-corrupt (under-parenthesized) index predicate\n  full stdout=%q", res.Stdout)
		}
		// Slice 299 negative guard: the precedence-corrupt under-parenthesized
		// expression-INDEX-COLUMN form must NOT appear — `(qty + id * mgr_id)` would
		// restore as `qty + (id * mgr_id)`, silently changing the indexed value.
		if strings.Contains(res.Stdout, "(qty + id * mgr_id)") {
			t.Errorf("pg_dump emitted a precedence-corrupt (under-parenthesized) expression-index column\n  full stdout=%q", res.Stdout)
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
		// **Slice 365 (asserted):** a VIEW `WITH [CASCADED|LOCAL] CHECK OPTION`
		// must round-trip the clause as the `\n  WITH <MODE> CHECK OPTION` suffix
		// after the view body (pg_dump.c dumpTableSchema). goopg surfaces the mode
		// as the `check_option=<mode>` reloption; pg_dump's getTables derives
		// CASCADED/LOCAL from it and strips it from the array, so the clause must
		// appear as the suffix and NOT inside a `WITH (...)` storage-option list.
		checkOptDefs := []string{
			"CREATE VIEW public.vchk AS",
			"  WITH CASCADED CHECK OPTION;",
			"CREATE VIEW public.vchk_local AS",
			"  WITH LOCAL CHECK OPTION;",
		}
		for _, sub := range checkOptDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a view CHECK OPTION clause; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// The check_option must NOT leak into a `WITH (...)` reloptions clause —
		// pg_dump strips it from the array and emits it only as the suffix.
		if strings.Contains(res.Stdout, "WITH (check_option") {
			t.Errorf("pg_dump leaked check_option into a WITH (...) reloptions clause\n  full stdout=%q", res.Stdout)
		}

		// **Slice 366 (asserted):** a VIEW `WITH (security_barrier=true)` must
		// round-trip the reloption. Unlike check_option, pg_dump keeps it in the
		// reloptions array and emits it as the `WITH (security_barrier='true')`
		// clause after the view name (appendReloptionsArray quotes the value).
		if sub := "CREATE VIEW public.vsecbar WITH (security_barrier='true') AS"; !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dump dropped the view security_barrier reloption; missing %q\n  full stdout=%q", sub, res.Stdout)
		}

		// **Slice 367 (asserted):** a VIEW `WITH (security_invoker=true)` must
		// round-trip the reloption, the sibling of security_barrier. pg_dump keeps
		// it in the reloptions array and emits it as the `WITH (security_invoker='true')`
		// clause after the view name (appendReloptionsArray quotes the value).
		if sub := "CREATE VIEW public.vsecinv WITH (security_invoker='true') AS"; !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dump dropped the view security_invoker reloption; missing %q\n  full stdout=%q", sub, res.Stdout)
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
		// **Slice 304 (asserted):** the schema-qualified 3-part OWNED BY
		// (`public.owner_tbl.label`) must round-trip identically to the unqualified
		// slice-118 form. Before the LastIndex split fix the CREATE SEQUENCE itself
		// errored (`sequence cannot be owned by relation "public"`), so this string
		// never appeared. pg_dump always re-qualifies, so the dumped form is the
		// canonical `ALTER SEQUENCE public.qowned_seq OWNED BY public.owner_tbl.label;`.
		if !strings.Contains(res.Stdout, "ALTER SEQUENCE public.qowned_seq OWNED BY public.owner_tbl.label;") {
			t.Errorf("pg_dump dropped the schema-qualified OWNED BY sequence; missing the qowned_seq OWNED BY line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 305 (asserted):** REPLICA IDENTITY round-trip. A FULL/NOTHING
		// override must surface as the exact `ALTER TABLE ONLY ... REPLICA
		// IDENTITY ...` clause pg_dump emits when relreplident != 'd'.
		for _, sub := range []string{
			"ALTER TABLE ONLY public.ri_full REPLICA IDENTITY FULL;",
			"ALTER TABLE ONLY public.ri_nothing REPLICA IDENTITY NOTHING;",
		} {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a REPLICA IDENTITY override; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// A default-identity table (relreplident='d', the implicit value) must
		// emit NO REPLICA IDENTITY clause. The legacy hardcoded 'n' produced a
		// spurious `ALTER TABLE ONLY public.foo REPLICA IDENTITY NOTHING;` for
		// EVERY dumped table — the core divergence this slice corrects. `foo`,
		// `bar`, and the partitioned `part` carry no override, so none may appear.
		for _, neg := range []string{
			"ALTER TABLE ONLY public.foo REPLICA IDENTITY",
			"ALTER TABLE ONLY public.bar REPLICA IDENTITY",
			"ALTER TABLE ONLY public.part REPLICA IDENTITY",
		} {
			if strings.Contains(res.Stdout, neg) {
				t.Errorf("pg_dump emitted a spurious REPLICA IDENTITY for a default-identity table: %q\n  full stdout=%q", neg, res.Stdout)
			}
		}
		// **Slice 306 (asserted):** REPLICA IDENTITY USING INDEX round-trip.
		// pg_dump emits this at index-dump time keyed on the index's
		// pg_index.indisreplident — the index name is unqualified in this
		// syntax (pg_dump.c dumpIndex:18186). The clause must name ri_uidx.
		if !strings.Contains(res.Stdout, "ALTER TABLE ONLY public.ri_index REPLICA IDENTITY USING INDEX ri_uidx;") {
			t.Errorf("pg_dump dropped the REPLICA IDENTITY USING INDEX clause; missing the ri_index/ri_uidx line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 307 (asserted):** a NOT-VALID FOREIGN KEY round-trips with the
		// trailing ` NOT VALID` that pg_get_constraintdef appends for
		// convalidated='f' (ruleutils.c:2604). A regression that dropped the
		// suffix would silently re-validate the constraint on restore.
		if !strings.Contains(res.Stdout, "ADD CONSTRAINT nv_child_fk FOREIGN KEY (ref_id) REFERENCES public.nv_ref(id) NOT VALID;") {
			t.Errorf("pg_dump dropped the NOT VALID suffix on an unvalidated FK; missing the nv_child_fk line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 308 (asserted):** a NOT-VALID CHECK constraint round-trips with
		// the same ` NOT VALID` tail. pg_dump emits an unvalidated CHECK as a
		// separate ALTER TABLE ADD CONSTRAINT (separate=!validated, pg_dump.c:9757)
		// so possibly-violating data loads first. A regression dropping the suffix
		// would silently re-validate the constraint on restore.
		if !strings.Contains(res.Stdout, "ADD CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;") {
			t.Errorf("pg_dump dropped the NOT VALID suffix on an unvalidated CHECK; missing the nvc_chk line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 309 (asserted):** a MATCH FULL FOREIGN KEY round-trips the match
		// type. pg_get_constraintdef emits ` MATCH FULL` between the REFERENCES
		// column list and the (absent here) ON UPDATE/DELETE clauses for
		// confmatchtype='f' (ruleutils.c). A regression dropping the clause would
		// silently downgrade the restored FK to MATCH SIMPLE, changing mixed-NULL
		// key semantics.
		if !strings.Contains(res.Stdout, "ADD CONSTRAINT mf_child_fk FOREIGN KEY (a, b) REFERENCES public.mf_ref(a, b) MATCH FULL;") {
			t.Errorf("pg_dump dropped the MATCH FULL clause on an FK; missing the mf_child_fk line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 310 (asserted):** a PARTIAL EXCLUDE constraint round-trips its
		// WHERE predicate. pg_get_constraintdef (via pg_get_indexdef_worker) emits
		// ` WHERE (b > 0)` after the operator list and before DEFERRABLE
		// (ruleutils.c:1564). A regression dropping the clause would silently
		// promote the partial exclusion to one applying to every row on restore.
		if !strings.Contains(res.Stdout, "ADD CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b > 0);") {
			t.Errorf("pg_dump dropped the WHERE predicate on a partial EXCLUDE; missing the pex_excl line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 311 (asserted):** a FOREIGN KEY with `ON DELETE SET NULL`
		// restricted to a column subset round-trips the column list (PG15
		// confdelsetcols). pg_get_constraintdef appends ` (b)` after the ON DELETE
		// SET NULL keyword (ruleutils.c:2376). A regression dropping the list would
		// silently widen the action to the whole key on restore — a semantic change.
		if !strings.Contains(res.Stdout, "ADD CONSTRAINT sfk_child_fk FOREIGN KEY (b) REFERENCES public.sfk_ref(id) ON DELETE SET NULL (b);") {
			t.Errorf("pg_dump dropped the ON DELETE SET NULL column list; missing the sfk_child_fk line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 312 (asserted):** a CREATE INDEX with a non-default per-column
		// operator class round-trips the opclass. pg_get_indexdef_worker emits
		// ` text_pattern_ops` after the column (get_opclass_name, ruleutils.c) and
		// suppresses the default opclass on the sibling column `b`. A regression
		// dropping the opclass would silently widen the index to the default
		// opclass on restore — a semantic change (text_pattern_ops vs text_ops).
		if !strings.Contains(res.Stdout, "CREATE INDEX opcidx_pat ON public.opcidx USING btree (a text_pattern_ops, b);") {
			t.Errorf("pg_dump dropped the index column operator class; missing the opcidx_pat line\n  full stdout=%q", res.Stdout)
		}
		// **Slice 313 (asserted):** a CREATE INDEX with a non-default per-column
		// COLLATE round-trips the collation. pg_get_indexdef_worker emits
		// ` COLLATE "C"` after the column and before the operator class
		// (generate_collation_name, ruleutils.c) and suppresses the default
		// collation on the sibling column `b`. A regression dropping the collation
		// would silently widen the index back to the default collation on restore.
		if !strings.Contains(res.Stdout, `CREATE INDEX collidx_c ON public.collidx USING btree (a COLLATE "C", b);`) {
			t.Errorf("pg_dump dropped the index column collation; missing the collidx_c line\n  full stdout=%q", res.Stdout)
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
		// **Slice 303 (asserted):** an IDENTITY column declared with a non-default
		// `(sequence_options)` clause must round-trip EVERY captured option
		// (INCREMENT BY, MINVALUE, MAXVALUE, CACHE, CYCLE), not just START WITH. The
		// whole `ADD GENERATED ... AS IDENTITY (...)` block is pinned byte-for-byte
		// against real pg_dump 18.3. `idrich` proves the fully-bounded ascending +
		// CYCLE case; `idbd` proves `BY DEFAULT` with an explicit increment keeps the
		// type-default `NO MINVALUE / NO MAXVALUE` (no spurious bound) and the default
		// CACHE 1. Before this slice the backing sequence was hard-coded to
		// increment=1/cache=1/no-cycle/type-min-max, so each emitted line below would
		// have diverged.
		identityOptDefs := []string{
			"ALTER TABLE public.idrich ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (\n    SEQUENCE NAME public.idrich_id_seq\n    START WITH 100\n    INCREMENT BY 5\n    MINVALUE 10\n    MAXVALUE 9999\n    CACHE 7\n    CYCLE\n);",
			"ALTER TABLE public.idbd ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (\n    SEQUENCE NAME public.idbd_id_seq\n    START WITH 1\n    INCREMENT BY 2\n    NO MINVALUE\n    NO MAXVALUE\n    CACHE 1\n);",
		}
		for _, sub := range identityOptDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped a non-default identity sequence option; missing %q\n  full stdout=%q", sub, res.Stdout)
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
		// **Slice 243 (asserted):** a stand-alone composite type round-trips. PG's
		// dumpCompositeType walks pg_type.typrelid → pg_class (relkind='c') →
		// pg_attribute, rendering each field as `\n\t<name> <format_type>`. The
		// declared `int`/`text` field types resolve to their canonical
		// `integer`/`text` spellings via format_type(atttypid, atttypmod).
		compositeDefs := []string{
			"CREATE TYPE public.addr AS (",
			"\tstreet text,",
			"\tzip integer",
			// Slice 247: typmod composite fields round-trip precision/length.
			"CREATE TYPE public.money_amt AS (",
			"\tamount numeric(10,2),",
			"\tcode character varying(8)",
			// Slice 248: a composite field whose type is a user-defined DOMAIN
			// renders as the schema-qualified domain name, not the base type.
			"CREATE TYPE public.dom_comp AS (",
			"\tz public.zipcode,",
			"\tn public.numd",
			// Slice 249: a composite field whose type is itself another composite
			// type renders as the schema-qualified composite name, not `text`.
			"CREATE TYPE public.nested_comp AS (",
			"\tlabel text,",
			"\tlocation public.addr",
			// Slice 250: a composite field whose type is an ARRAY of another
			// composite renders as the schema-qualified composite array name,
			// not `text[]`.
			"CREATE TYPE public.route AS (",
			"\tname text,",
			"\tstops public.addr[]",
			// Slice 252: a composite field whose type is an ARRAY of a user-defined
			// DOMAIN renders as the schema-qualified domain array name, not `text[]`.
			"CREATE TYPE public.dom_arr_comp AS (",
			"\tlabel text,",
			"\tzips public.zipcode[]",
			// Slice 253: ALTER TYPE … ADD ATTRIBUTE appends fields that must dump
			// alongside the original one — the type re-synced its heap rows with
			// the new attributes (typmod preserved on the numeric one).
			// Slice 254: RENAME ATTRIBUTE renamed b -> b_renamed in place.
			// Slice 255: DROP ATTRIBUTE removed c.
			// Slice 256: ALTER ATTRIBUTE … TYPE re-typed a→bigint and
			// b_renamed→numeric(12,3) (typmod preserved).
			// Slice 258: ADD ATTRIBUTE cc text COLLATE "C" appended a collated field
			// via the re-sync path; its attcollation (950) shadows text's default
			// (100), so dumpCompositeType re-emits the COLLATE clause inline, and
			// `cc` is now the final (comma-less) field.
			// Slice 259: ALTER ATTRIBUTE cc TYPE text COLLATE "POSIX" re-typed cc in
			// place, replacing its collation C→POSIX (attcollation 950→951), so the
			// final dump emits COLLATE pg_catalog."POSIX" for cc.
			"CREATE TYPE public.alt_comp AS (",
			"\ta bigint,",
			"\tb_renamed numeric(12,3),",
			"\tcc text COLLATE pg_catalog.\"POSIX\"\n",
			// Slice 260: multi-subcommand ALTER TYPE folded ADD d / DROP b /
			// ALTER c TYPE numeric(12,3) / ADD e COLLATE "C" into one re-sync, so
			// all four changes round-trip together: b is gone, c carries its new
			// typmod, d appended, e is the final collated field (comma-less).
			"CREATE TYPE public.multi_comp AS (",
			"\ta integer,",
			"\tc numeric(12,3),",
			"\td text,",
			"\te text COLLATE pg_catalog.\"C\"\n",
			// Slice 257: a per-field COLLATE round-trips inline. The field's
			// attcollation (C=950 / POSIX=951) differs from text's typcollation
			// (100), so dumpCompositeType re-emits `COLLATE pg_catalog."<name>"`.
			// The uncollated middle field `b` (default 100) carries no clause.
			"CREATE TYPE public.coll_comp AS (",
			"\ta text COLLATE pg_catalog.\"C\",",
			"\tb text,",
			"\tp text COLLATE pg_catalog.\"POSIX\"\n",
		}
		for _, sub := range compositeDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped/mangled the composite TYPE round-trip; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Slice 255: the DROP ATTRIBUTE'd field (c numeric(10,2)) must be gone
		// from alt_comp — the re-synced heap dropped its pg_attribute row.
		if strings.Contains(res.Stdout, "c numeric(10,2)") {
			t.Errorf("pg_dump still emitted the DROP ATTRIBUTE'd composite field c (slice-255 drop regressed)\n  full stdout=%q", res.Stdout)
		}
		// Slice 260: the multi-subcommand fold must emit multi_comp's fields in
		// exactly (a, c, d, e) order with the DROP'd `b` gone — assert the whole
		// contiguous block so an out-of-order field or a surviving `b text` (which
		// would appear between a and c) fails the test.
		multiCompBlock := "CREATE TYPE public.multi_comp AS (\n\ta integer,\n\tc numeric(12,3),\n\td text,\n\te text COLLATE pg_catalog.\"C\"\n);"
		if !strings.Contains(res.Stdout, multiCompBlock) {
			t.Errorf("pg_dump mangled the multi-subcommand ALTER TYPE round-trip (slice-260); missing contiguous block %q\n  full stdout=%q", multiCompBlock, res.Stdout)
		}
		// The auto-generated `_addr` array type must NOT dump as its own CREATE
		// TYPE (the isarray subquery suppresses it, like `_mood`).
		if strings.Contains(res.Stdout, "CREATE TYPE public._addr") {
			t.Errorf("pg_dump emitted the auto-generated composite array type as a separate CREATE TYPE\n  full stdout=%q", res.Stdout)
		}
		// Slice 250: the composite-array field must not degrade to text[].
		if strings.Contains(res.Stdout, "stops text[]") {
			t.Errorf("pg_dump rendered the composite-array field as text[] (slice-250 array OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		// Slice 252: the domain-array composite field must not degrade to text[].
		if strings.Contains(res.Stdout, "zips text[]") {
			t.Errorf("pg_dump rendered the domain-array composite field as text[] (slice-252 array OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		// Slice 257: the uncollated middle field of coll_comp must NOT carry a
		// spurious COLLATE clause. The positive assertion above pins the exact
		// `\tb text,` form (tab-prefixed, comma-suffixed) — a leaked override would
		// render `\tb text COLLATE …,` instead, so that substring would be absent.
		// (A broad `b text COLLATE` check would false-positive on the collcol TABLE,
		// whose column b is legitimately `COLLATE "POSIX"`.)
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
			// Slice 363: compound + function-call generic domain CHECKs gain PG's
			// per-operand parens and call-paren spacing; the value placeholder stays
			// uppercase across every nested ColumnRef. Byte-identical to real PG 18.3.
			"CREATE DOMAIN public.dchkand AS integer",
			"CONSTRAINT dchkand_check CHECK (((VALUE > 0) AND (VALUE < 100)))",
			"dca public.dchkand",
			"CREATE DOMAIN public.dchkfn AS text",
			"CONSTRAINT dchkfn_check CHECK ((length(VALUE) > 0))",
			"dcf public.dchkfn",
			// Slice 364: a unary minus on a numeric literal folds to PG's
			// quoted-value-plus-cast `'-N'::type` Const form in CHECK predicates,
			// column DEFAULTs, expression-index keys, and domain CHECKs. The cast type
			// is the LITERAL's own type (a bigint column's `<> -100` → `'-100'::integer`;
			// `DEFAULT -9000000000` → `'-9000000000'::bigint`). Byte-identical to real PG.
			"CREATE DOMAIN public.dchkneg AS integer",
			"CONSTRAINT dchkneg_check CHECK ((VALUE < '-5'::integer))",
			"CONSTRAINT neglit_a_check CHECK ((a < '-5'::integer))",
			"CONSTRAINT neglit_b_check CHECK ((b > '-3.5'::numeric))",
			"CONSTRAINT neglit_c_check CHECK ((c <> '-100'::integer))",
			"d integer DEFAULT '-7'::integer",
			"e bigint DEFAULT '-9000000000'::bigint",
			"CREATE INDEX neglit_ix ON public.neglit USING btree (((a + '-7'::integer)))",
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
			// Slice 251: a column declared `zipcode[]` (an ARRAY of a user-defined
			// DOMAIN) must render as the schema-qualified domain array name, not the
			// base type's array. The domain's auto-generated `_zipcode` array type
			// (allocated at CREATE DOMAIN) gives the column a real array pg_type OID
			// that format_type resolves to `public.zipcode[]`.
			"zips public.zipcode[]",
		}
		for _, sub := range domainDefs {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped the DOMAIN round-trip; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
		}
		// Slice 251: the domain-array column must NOT degrade to the base type's
		// array, and the auto-generated `_zipcode` array type must NOT dump as its
		// own CREATE TYPE (the domain's typarray points back at it, so pg_dump's
		// isarray subquery suppresses it).
		if strings.Contains(res.Stdout, "zips text[]") {
			t.Errorf("pg_dump rendered the domain-array column as text[] (slice-251 array OID resolution regressed)\n  full stdout=%q", res.Stdout)
		}
		if strings.Contains(res.Stdout, "CREATE TYPE public._zipcode") {
			t.Errorf("pg_dump emitted the auto-generated domain array type as a separate CREATE TYPE (slice-251 isarray suppression regressed)\n  full stdout=%q", res.Stdout)
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
