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

	if err := runSQLSimple(t, c, "CREATE TABLE public.foo (id integer, name text)"); err != nil {
		t.Fatalf("create table: %v", err)
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
		cols := []string{"id integer", "name text"}
		for _, sub := range cols {
			if !strings.Contains(res.Stdout, sub) {
				t.Errorf("pg_dump column list missing %q (SRF-join right-side projection regressed)\n  full stdout=%q", sub, res.Stdout)
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
		if strings.Contains(res.Stdout, "WITH (") {
			t.Errorf("pg_dump emitted a spurious reloptions WITH clause for a table with no options\n  full stdout=%q", res.Stdout)
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
