package executor

// operators_pg_get_publication_tables_test.go — VARIADIC parser unblock +
// pg_get_publication_tables FROM-clause SRF coverage for libpqrcv
// fetch_table_list probe-survival. M0103-0008.

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runQueryRows builds and opens the operator for the given SELECT, then
// drains every produced row. Test helper local to this file.
func runQueryRows(t *testing.T, ctx *Context, sql string) []Row {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan(%q): %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build(%q): %v", sql, err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open(%q): %v", sql, err)
	}
	defer op.Close()
	var rows []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next(%q): %v", sql, err)
		}
		// projectOp.out aliases the slot's row across Next() calls;
		// defensive-copy so multi-row tests see distinct snapshots.
		rows = append(rows, append(Row(nil), slot.Row()...))
	}
	return rows
}

// TestPgGetPublicationTablesFromClauseFilter pins the FROM-clause SRF path:
// the operator emits one row per (publication, table) pair, restricted to
// the publications named in the argument list. Used by libpqrcv's
// fetch_table_list probe. M0103-0008.
func TestPgGetPublicationTablesFromClauseFilter(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}

	rows := runQueryRows(t, ctx, "SELECT * FROM pg_get_publication_tables('p')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("relid is NULL, want a non-zero OID")
	}
}

// TestPgGetPublicationTablesVariadicArrayArgument pins the libpqrcv shape:
// the VARIADIC marker on a text[] argument must parse, plan, execute, and
// flatten the array into per-element filter names. M0103-0008.
func TestPgGetPublicationTablesVariadicArrayArgument(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE PUBLICATION q FOR TABLE items"); err != nil {
		t.Fatal(err)
	}

	// flattenTextArg unit-coverage: a brace-wrapped text[] payload is split
	// element-by-element. Two publications named ⇒ two matched rows.
	rows := runQueryRows(t, ctx, "SELECT * FROM pg_get_publication_tables(VARIADIC '{p,q}')")
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
}

// TestPgGetPublicationTablesEmptyFilter pins the "no argument" shortcut:
// every (publication, table) pair the registry knows about is emitted.
// M0103-0008.
func TestPgGetPublicationTablesEmptyFilter(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, "SELECT * FROM pg_get_publication_tables()")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
}

// TestPgGetPublicationTablesUnknownPublication pins the empty-set behaviour
// when the supplied name does not match any registered publication. The
// previous parser rejected the query before it got here; this fixes the
// VARIADIC unblock path under M0103-0008.
func TestPgGetPublicationTablesUnknownPublication(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	rows := runQueryRows(t, ctx, "SELECT * FROM pg_get_publication_tables('nope')")
	if len(rows) != 0 {
		t.Fatalf("row count = %d, want 0", len(rows))
	}
}


// TestIndirectionStarTargetListPlansAsFromSrf pins the target-list
// `(srf(...)).*` planner rewrite. The IndirectionStar target is rewritten
// into a FROM-clause SRF reference (`__irs_0`) and the target becomes
// `__irs_0.*`. Execution must emit the same rows as the equivalent
// `SELECT * FROM pg_get_publication_tables(...)` form. M0103-0008.
func TestIndirectionStarTargetListPlansAsFromSrf(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, "SELECT (pg_get_publication_tables('p')).*")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	// Composite expansion: three columns (relid, attrs, qual).
	if len(rows[0]) != 3 {
		t.Fatalf("column count = %d, want 3", len(rows[0]))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("relid is NULL, want a non-zero OID")
	}
}

// TestIndirectionStarRejectsAggregateArgument confirms the planner emits
// a clean PG-compatible error for the libpqrcv-probe shape
// `(srf(array_agg(...))).*` (aggregate-in-SRF-args). Full support requires
// a ProjectSet-style plan node; tracked as the next M0103-0008 sub-step.
// TestIndirectionStarWithAggregateArgument pins the libpqrcv-probe shape
// `(pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*`. The
// planner lowers this into `Aggregate → ProjectSet(srf(...))` where the
// SRF's argument is a ColumnRef into the aggregate's `array_agg` output
// (M0103-0008 final sub-step). The executor evaluates the SRF once per
// child row and spreads its three-column composite. We use a regular
// table named `pg_publication` (with a `pubname` column) as a stand-in
// for the catalog view — the planner shape is identical and the table
// fixture exercises the lowering end-to-end.
func TestIndirectionStarWithAggregateArgument(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pg_publication (pubname text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO pg_publication VALUES ('p')"); err != nil {
		t.Fatal(err)
	}

	rows := runQueryRows(t, ctx, "SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).* FROM pg_publication")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if len(rows[0]) != 3 {
		t.Fatalf("column count = %d, want 3 (relid/attrs/qual)", len(rows[0]))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("relid is NULL, want a non-zero OID")
	}
}


// TestIndirectionStarWithAggregateArgumentAndWhere pins the full libpqrcv
// probe shape: the SRF call sits over an aggregate that itself filters
// the source rows via a WHERE clause. Two publications exist (`p` and `q`)
// but only `p` survives the IN filter; the SRF must be called with a
// 1-element text[] and emit one row per (publication, table) pair. M0103-0008.
func TestIndirectionStarWithAggregateArgumentAndWhere(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE PUBLICATION q FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pg_publication (pubname text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO pg_publication VALUES ('p'), ('q')"); err != nil {
		t.Fatal(err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).* "+
			"FROM pg_publication WHERE pubname IN ('p')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (only publication 'p' survives WHERE)", len(rows))
	}
	if len(rows[0]) != 3 {
		t.Fatalf("column count = %d, want 3 (relid/attrs/qual)", len(rows[0]))
	}
}

// TestArrayAggText pins array_agg(text) returning a PG text-array literal
// `{e1,e2,...}`. The libpqrcv fetch_table_list probe builds its VARIADIC
// argument this way. M0103-0008.
func TestArrayAggText(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (k text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES ('a'), ('b'), ('c')"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, "SELECT array_agg(k) FROM t")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	got := rows[0][0].StringValue()
	if got != "{a,b,c}" {
		t.Fatalf("array_agg result = %q, want {a,b,c}", got)
	}
}


// TestIndirectionStarInsideDerivedSubquery — the libpqrcv fetch_table_list
// probe wraps the SRF call inside a derived subquery joined into the
// outer SELECT. This pins that the IndirectionStar rewrite fires for the
// inner SelectStmt too (the rewrite is idempotent + runs at planSelect
// entry for every nested SELECT). The probe's actual shape uses
// `array_agg(pubname::text)` in the SRF arg list — covered by the
// separate aggregate-rejection test. Here we use a constant arg so the
// non-aggregate fast path exercises end-to-end. The outer SELECT must
// resolve `gpt.relid` against the SRF's `relid` column, which requires
// the derived subquery to propagate the SRF's three-column schema (not
// just the single StarExpr target). M0103-0008.
func TestIndirectionStarInsideDerivedSubquery(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, `SELECT gpt.relid FROM ( SELECT (pg_get_publication_tables('p')).* ) AS gpt`)
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("relid is NULL, want a non-zero OID")
	}
}

// TestIndirectionStarInsideDerivedSubqueryStarSelect pins that the
// outer SELECT can read every SRF column through the derived-subquery
// wrapper, not just one named column. M0103-0008.
func TestIndirectionStarInsideDerivedSubqueryStarSelect(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, `SELECT * FROM ( SELECT (pg_get_publication_tables('p')).* ) AS gpt`)
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if len(rows[0]) != 3 {
		t.Fatalf("column count = %d, want 3 (relid/attrs/qual)", len(rows[0]))
	}
}

// TestIndirectionStarDerivedSubqueryExplicitAliases pins that explicit
// column aliases on the derived table — `… AS gpt (r, a, q)` — override
// the SRF's default column names and remain resolvable at the outer
// scope. M0103-0008.
func TestIndirectionStarDerivedSubqueryExplicitAliases(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	rows := runQueryRows(t, ctx, `SELECT gpt.r FROM ( SELECT (pg_get_publication_tables('p')).* ) AS gpt (r, a, q)`)
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("r (aliased relid) is NULL, want a non-zero OID")
	}
}

// TestLateralPgGetPublicationTablesFromOuterRef pins the rung-6 executor
// path: a `LATERAL pg_get_publication_tables(p.pubname)` FROM-list item
// must evaluate its argument against the per-row outer slot bound by
// the parent Join.Lateral. Two outer rows (`p`, `q`) must each yield one
// SRF result row whose `relid` is non-NULL. Before the BindLateralOuter
// + openLateral fix the operator evaluated `p.pubname` against a nil
// slot and raised `XX000: column ref pubname/0 on nil slot`.
// M0103-0008 rung 6.
func TestLateralPgGetPublicationTablesFromOuterRef(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE PUBLICATION q FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	// Stand-in for the catalog view: a regular table with a `pubname`
	// column. Two rows so the lateral path runs the SRF twice and we
	// confirm the per-outer-row binding is fresh each iteration.
	if err := runDDL(t, ctx, "CREATE TABLE pg_publication (pubname text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO pg_publication VALUES ('p'), ('q')"); err != nil {
		t.Fatal(err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT gpt.relid FROM pg_publication p, "+
			"LATERAL pg_get_publication_tables(p.pubname) gpt")
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2 (one SRF result per outer row)", len(rows))
	}
	for i, r := range rows {
		if r[0].IsNull() {
			t.Fatalf("rows[%d].relid is NULL, want a non-zero OID", i)
		}
	}
}

// TestLateralPgGetPublicationTablesUnknownYieldsZero pins LEFT-vs-CROSS
// behaviour: when the outer row produces an SRF result for one publication
// but the other publication name is not registered, the CROSS-join shape
// drops the unmatched outer row entirely. M0103-0008 rung 6.
func TestLateralPgGetPublicationTablesUnknownYieldsZero(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pg_publication (pubname text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO pg_publication VALUES ('p'), ('nope')"); err != nil {
		t.Fatal(err)
	}

	rows := runQueryRows(t, ctx,
		"SELECT gpt.relid FROM pg_publication p, "+
			"LATERAL pg_get_publication_tables(p.pubname) gpt")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1 (only 'p' resolves to a publication)", len(rows))
	}
}

// contains is a 1-line strings.Contains shim local to this file to avoid
// dragging in strings just for one assertion.
// TestPgGetPublicationTablesRelidMatchesPgClassOid pins rung 15 of
// M0103-0008: gpt.relid must compare equal to pg_class.oid so that PG's
// CREATE SUBSCRIPTION fetch_table_list join — `JOIN ... AS gpt ON
// gpt.relid = c.oid` — produces a non-empty result set. v0's pg_class.oid
// stores the relation NAME as text (regclass cast is a no-op for pgbench
// compatibility); pg_get_publication_tables must therefore emit relid in
// the same shape. Before the fix, relid was a numeric OID (e.g. 16384) and
// the comparison `"16384" = "t"` silently produced zero rows, leaving
// `pg_subscription_rel` empty and the apply worker skipping every change.
// TestPgGetPublicationTablesRelidMatchesPgClassOid pins rung 16 of
// M0103-0008: gpt.relid must compare equal to pg_class.oid so that PG's
// CREATE SUBSCRIPTION fetch_table_list join — `JOIN ... AS gpt ON
// gpt.relid = c.oid` — produces a non-empty result set. Rung 15 had
// both sides emit the relation NAME as text (matching the legacy v0
// pg_class.oid text shape); rung 16 flips both sides to the table's
// numeric OID because libpqrcv's fetch_remote_table_info wire-decodes
// pg_class.oid via DatumGetObjectId — text "items" parses as uint32
// → 0 and lrel->remoteid then misses every downstream column-list
// LATERAL probe.
// TestPgGetPublicationTablesRelidMatchesPgClassOid pins rung 16 of
// M0103-0008: gpt.relid must compare equal to pg_class.oid so that PG's
// CREATE SUBSCRIPTION fetch_table_list join — `JOIN ... AS gpt ON
// gpt.relid = c.oid` — produces a non-empty result set. Rung 15 had
// both sides emit the relation NAME as text (matching the legacy v0
// pg_class.oid text shape); rung 16 flips both sides to the table's
// numeric OID (KindString decimal text) because libpqrcv's
// fetch_remote_table_info wire-decodes pg_class.oid via
// DatumGetObjectId — text "items" parses as uint32 → 0 and
// lrel->remoteid then misses every downstream column-list LATERAL
// probe.
func TestPgGetPublicationTablesRelidMatchesPgClassOid(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()

	if err := runDDL(t, ctx, "CREATE PUBLICATION p FOR TABLE items"); err != nil {
		t.Fatal(err)
	}

	// Lookup the registered table so the assertion compares against the
	// real numeric OID rather than a literal that drifts as the catalog
	// is restructured.
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	if !ok || tbl == nil {
		t.Fatalf("items table not found in catalog")
	}
	wantOID := strconv.Itoa(int(tbl.OID))

	// Direct shape: SRF on the inside, pg_class on the outside, JOIN on
	// the gpt.relid = c.oid equality. The PG18 fetch_table_list also
	// joins pg_namespace and an aggregate subquery, but the minimum
	// pre-condition under test is that gpt.relid compares equal to
	// pg_class.oid for the same user table.
	rows := runQueryRows(t, ctx,
		"SELECT c.relname, gpt.relid FROM pg_class c "+
			"JOIN ( SELECT * FROM pg_get_publication_tables('p') ) AS gpt "+
			"ON gpt.relid = c.oid")
	if len(rows) != 1 {
		t.Fatalf("join row count = %d, want 1 (single published table)", len(rows))
	}
	if rows[0][0].IsNull() {
		t.Fatalf("rows[0].relname is NULL, want \"items\"")
	}
	if got := rows[0][0].StringValue(); got != "items" {
		t.Fatalf("rows[0].relname = %q, want \"items\"", got)
	}
	if rows[0][1].IsNull() {
		t.Fatalf("rows[0].relid is NULL, want %q (table OID)", wantOID)
	}
	if got := rows[0][1].StringValue(); got != wantOID {
		t.Fatalf("rows[0].relid = %q, want %q (numeric OID, M0103-0008 rung 16)", got, wantOID)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
