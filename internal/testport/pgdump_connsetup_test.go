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
		"mac macaddr, macs macaddr[], mac8 macaddr8, mac8s macaddr8[])"); err != nil {
		t.Fatalf("create table arr: %v", err)
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
		}
		for _, sub := range arrCols {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump dropped an array column type; missing %q\n  full stdout=%q", sub, res.Stdout)
			}
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
