package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func analyzerCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}},
		{Name: "filler", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_history"}, []catalog.Column{
		{Name: "tid", Type: catalog.Type{Name: "int4"}},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "aid", Type: catalog.Type{Name: "int4"}},
		{Name: "delta", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func parseOne(t *testing.T, sql string) parser.Stmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d statements", sql, len(stmts))
	}
	return stmts[0]
}

func expectAnalyzeCode(t *testing.T, cat catalog.Catalog, sql, code string) {
	t.Helper()
	err := Analyze(parseOne(t, sql), cat)
	if err == nil {
		t.Fatalf("Analyze(%q) expected error", sql)
	}
	ae, ok := err.(*AnalyzeError)
	if !ok {
		t.Fatalf("Analyze(%q) err type=%T", sql, err)
	}
	if ae.Code != code {
		t.Fatalf("Analyze(%q) code=%s want %s", sql, ae.Code, code)
	}
}

func TestAnalyzeSelectAliasAndQualifiedStar(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT a.aid, a.* FROM pgbench_accounts a WHERE a.aid = 1 ORDER BY a.aid LIMIT 10 OFFSET 2"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeRejectUnsupportedSelectFeatures(t *testing.T) {
	cat := analyzerCatalog(t)
	// DISTINCT (M0097-0005) and the set operations UNION/INTERSECT/EXCEPT
	// with optional ALL (M0097-0024) are all accepted by the analyzer now.
	accepted := []string{
		"SELECT DISTINCT aid FROM pgbench_accounts",
		"SELECT 1 UNION SELECT 2",
		"SELECT 1 UNION ALL SELECT 2",
		"SELECT 1 INTERSECT SELECT 2",
		"SELECT 1 EXCEPT ALL SELECT 2",
	}
	for _, sql := range accepted {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Errorf("Analyze(%q) should be supported now, got: %v", sql, err)
		}
	}
}

func TestAnalyzeSelectJoinAndGroupBy(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT a.aid, sum(h.delta) FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid GROUP BY a.aid HAVING sum(h.delta) > 0 ORDER BY a.aid"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeAmbiguousColumnError(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid", "42702")
}

func TestAnalyzeTypeErrors(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts WHERE aid", "42804")
	expectAnalyzeCode(t, cat, "SELECT aid || abalance FROM pgbench_accounts", "42883")
	// A bare string literal is now typed `unknown` (like PG's UNKNOWNOID), so
	// `LIMIT 'x'` no longer fails at analysis — it defers to runtime, which
	// rejects a non-integer LIMIT with 42804 (operators.go limitOp.Open),
	// consistent with how string-literal-into-int is deferred (M0097-0003).
	// A concrete non-integer type (bool) is still caught at analysis:
	expectAnalyzeCode(t, cat, "SELECT aid FROM pgbench_accounts LIMIT true", "42804")
}

func TestAnalyzeByteaLike(t *testing.T) {
	// bytea column `a`, text column `t`, int column `i` — exercises the
	// bytea/bytea lane of the OpLike gate (M0134-0070) without creating
	// cross-type operators.
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "byteatest"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "bytea"}},
		{Name: "t", Type: catalog.Type{Name: "text"}},
		{Name: "i", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	// bytea lane: column vs unknown literal, bytea/bytea, unknown-literal
	// (via ::bytea cast) vs column, and NOT LIKE all analyze clean and
	// yield bool.
	for _, sql := range []string{
		"SELECT a FROM byteatest WHERE a LIKE '%1%'",
		"SELECT a FROM byteatest WHERE a LIKE 'x'::bytea",
		"SELECT a FROM byteatest WHERE a LIKE a",
		"SELECT a FROM byteatest WHERE 'x'::bytea LIKE a",
		"SELECT a FROM byteatest WHERE a NOT LIKE '%1%'",
	} {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
	// No cross-type operator: bytea vs text stays a 42804.
	expectAnalyzeCode(t, cat, "SELECT a FROM byteatest WHERE a LIKE t", "42804")
	expectAnalyzeCode(t, cat, "SELECT a FROM byteatest WHERE t LIKE a", "42804")
	// OpConcat must NOT gain a bytea lane: int || bytea stays 42883.
	expectAnalyzeCode(t, cat, "SELECT i || a FROM byteatest", "42883")
	// Existing string lane is unchanged.
	if err := Analyze(parseOne(t, "SELECT a FROM byteatest WHERE t LIKE 'x%'"), cat); err != nil {
		t.Fatalf("Analyze(text LIKE): %v", err)
	}
}

func TestAnalyzeDMLTypeAndReturningErrors(t *testing.T) {
	// String literals ('x') are now allowed for integer columns at analysis time
	// (for PostgreSQL compatibility: untyped literals can be assigned to any type,
	// with validation deferred to runtime). See M0097-0003.
	// INSERT/UPDATE of 'x' into int4 columns now fails at runtime (22P02).
	// DELETE/UPDATE RETURNING is now supported (M0100-0005); no error expected.
}

func TestAnalyzeWindowFunctionAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT row_number() OVER () FROM pgbench_accounts",
		"SELECT rank() OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
		"SELECT aid FROM pgbench_accounts ORDER BY row_number() OVER (ORDER BY aid)",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

func TestAnalyzeWindowFunctionUnsupportedRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	// count(*) OVER () and first_value/last_value/nth_value/ntile/
	// cume_dist/percent_rank/dense_rank are now supported (M0122-0004).
	// PostgreSQL has no window-function allow-list — any ordinary
	// aggregate is usable as a window function
	// (postgres/src/backend/parser/parse_agg.c:transformWindowFuncCall) —
	// so array_agg() OVER () is now ACCEPTED (M0134-0022b widened
	// analyzeWindowFuncCall's default arm to isAnalyzerAggregateName; see
	// TestAnalyzeWindowAggregateFunctionAccepted below). The rejection
	// case is now a genuine non-aggregate, non-window scalar function.
	expectAnalyzeCode(t, cat,
		"SELECT abs(aid) OVER () FROM pgbench_accounts",
		"0A000")
}

// TestAnalyzeWindowAggregateFunctionAccepted pins the M0134-0022b
// widening: array_agg() OVER () — previously rejected here as the
// canonical "not supported in v0 analyzer" case (see the comment above)
// — analyzes cleanly now that the analyzer's default arm routes any
// standard-aggregate name through the same validation an ordinary
// aggregate call gets.
func TestAnalyzeWindowAggregateFunctionAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t, "SELECT array_agg(aid) OVER () FROM pgbench_accounts"), cat); err != nil {
		t.Fatalf("Analyze(array_agg(aid) OVER ()): %v", err)
	}
}

// TestAnalyzeWindowValueFunctionsAccepted pins the M0122-0004
// first_value/last_value/nth_value window functions.
func TestAnalyzeWindowValueFunctionsAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT first_value(abalance) OVER (ORDER BY aid) FROM pgbench_accounts",
		"SELECT last_value(abalance) OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
		"SELECT nth_value(abalance, 2) OVER (ORDER BY aid) FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowValueFunctionsRejected pins the argument-shape checks
// for first_value/last_value/nth_value.
func TestAnalyzeWindowValueFunctionsRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT first_value(*) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT last_value(abalance, aid) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT nth_value(abalance) OVER () FROM pgbench_accounts",
		"42601")
}

// TestAnalyzeWindowRankingFunctionsAccepted pins the M0122-0004
// ntile/cume_dist/percent_rank/dense_rank window functions.
func TestAnalyzeWindowRankingFunctionsAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT ntile(4) OVER (ORDER BY aid) FROM pgbench_accounts",
		"SELECT cume_dist() OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
		"SELECT percent_rank() OVER (ORDER BY aid) FROM pgbench_accounts",
		"SELECT dense_rank() OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowRankingFunctionsRejected pins the argument-shape checks
// for ntile/cume_dist/percent_rank.
func TestAnalyzeWindowRankingFunctionsRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT ntile() OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT ntile(4, 5) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT cume_dist(1) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT percent_rank(*) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT dense_rank(1) OVER () FROM pgbench_accounts",
		"42601")
}

// TestAnalyzeWindowAggregateFunctionsAccepted pins the M0122-0004
// frame-consuming aggregate window functions: sum/count/avg/min/max
// used with OVER (...) rather than GROUP BY.
func TestAnalyzeWindowAggregateFunctionsAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT count(*) OVER () FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (PARTITION BY bid ORDER BY aid) FROM pgbench_accounts",
		"SELECT count(abalance) OVER () FROM pgbench_accounts",
		"SELECT avg(abalance) OVER () FROM pgbench_accounts",
		"SELECT min(abalance) OVER (PARTITION BY bid) FROM pgbench_accounts",
		"SELECT max(abalance) OVER (PARTITION BY bid) FROM pgbench_accounts",
		"SELECT sum(abalance) FILTER (WHERE aid > 1) OVER () FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowAggregateFunctionsRejected pins the real PostgreSQL
// restrictions on aggregate window functions (DISTINCT / ORDER BY in the
// argument list), which parse_func.c's transformAggregateCall enforces
// with ERRCODE_FEATURE_NOT_SUPPORTED (0A000) — not a v0 gap.
func TestAnalyzeWindowAggregateFunctionsRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT sum(DISTINCT abalance) OVER () FROM pgbench_accounts",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT sum(abalance ORDER BY abalance) OVER () FROM pgbench_accounts",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT sum(*) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT sum() OVER () FROM pgbench_accounts",
		"42601")
}

// TestAnalyzeCreateFunctionPassesThrough pins M0015 Stage A step 3:
// the analyzer drops the step-1 SQLSTATE 0A000 rejection now that
// the executor's CREATE FUNCTION operator lands the catalog row.
// DDL flows straight through Plan to the executor; the analyzer
// no longer needs to gatekeep these statements.
func TestAnalyzeCreateFunctionPassesThrough(t *testing.T) {
	cat := analyzerCatalog(t)
	if err := Analyze(parseOne(t,
		"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$"), cat); err != nil {
		t.Errorf("Analyze CREATE FUNCTION = %v, want nil", err)
	}
	if err := Analyze(parseOne(t, "DROP FUNCTION IF EXISTS f(int)"), cat); err != nil {
		t.Errorf("Analyze DROP FUNCTION = %v, want nil", err)
	}
}

func TestAnalyzeWindowFunctionArgShapeRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT row_number(1) OVER () FROM pgbench_accounts",
		"42601")
	expectAnalyzeCode(t, cat,
		"SELECT rank(DISTINCT aid) OVER () FROM pgbench_accounts",
		"42601")
}

func TestAnalyzeWindowFunctionPlacementRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts WHERE row_number() OVER () > 0",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts GROUP BY row_number() OVER ()",
		"0A000")
	expectAnalyzeCode(t, cat,
		"SELECT aid FROM pgbench_accounts HAVING row_number() OVER () > 0",
		"0A000")
}

// TestAnalyzeNamedWindowClauseAccepted pins the M0020 named-window
// slice: a bare `OVER w` reference resolves against a trailing
// `WINDOW w AS (...)` clause, including two functions sharing one
// named window and two functions each with their own.
func TestAnalyzeNamedWindowClauseAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid ORDER BY aid)",
		"SELECT row_number() OVER w, rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid ORDER BY aid)",
		"SELECT row_number() OVER w1, rank() OVER w2 FROM pgbench_accounts WINDOW w1 AS (PARTITION BY bid), w2 AS (ORDER BY aid)",
		"SELECT aid FROM pgbench_accounts WINDOW w AS (ORDER BY aid) ORDER BY row_number() OVER w",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeNamedWindowBareRefToFramedWindowAccepted pins a real-PG-verified
// distinction the M0122-0004 combining-forms merge validation must NOT
// trip over: a parenthesis-free `OVER w` reference is a transparent alias
// (parse_agg.c's transformWindowFuncCall bare-name lookup, never routed
// through transformWindowDefinitions/mergeWindowDef) even when `w` itself
// declares a frame clause — only the parenthesized `OVER (w ...)` combining
// form is subject to "cannot copy window ... because it has a frame
// clause". Confirmed against a live PostgreSQL 18.3 instance.
func TestAnalyzeNamedWindowBareRefToFramedWindowAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "SELECT sum(abalance) OVER w FROM pgbench_accounts " +
		"WINDOW w AS (PARTITION BY bid ORDER BY aid ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING)"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatalf("Analyze(%q): %v", sql, err)
	}
}

// TestAnalyzeNamedWindowUndefinedRejected pins the 42704
// (ERRCODE_UNDEFINED_OBJECT) diagnostic for `OVER name` with no matching
// WINDOW clause item — upstream's "window %q does not exist"
// (transformWindowFuncCall, parse_agg.c), confirmed against a live
// PostgreSQL 18.3 instance (M0122-0004 combining-forms follow-up; this
// was previously misclassified as 42P20, the windowing_error code
// upstream reserves for the "already defined"/override-validation
// errors pinned by TestAnalyzeNamedWindowCombiningFormErrors below).
func TestAnalyzeNamedWindowUndefinedRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER missing FROM pgbench_accounts",
		"42704")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER w FROM pgbench_accounts WINDOW w2 AS (PARTITION BY bid)",
		"42704")
}

// TestAnalyzeNamedWindowCombiningFormErrors pins the SQL:2008 7.11
// <window clause> combining-form validations (parse_clause.c's
// transformWindowDefinitions), each confirmed byte-for-byte against a
// live PostgreSQL 18.3 instance: a duplicate WINDOW name, an inline
// `OVER (win_name PARTITION BY ...)` trying to override the referenced
// window's PARTITION BY, an ORDER BY override when the referenced window
// already has one, and copying a window that already has a frame clause.
func TestAnalyzeNamedWindowCombiningFormErrors(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER w FROM pgbench_accounts WINDOW w AS (PARTITION BY bid), w AS (ORDER BY aid)",
		"42P20")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER (w PARTITION BY aid) FROM pgbench_accounts WINDOW w AS (PARTITION BY bid)",
		"42P20")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER (w ORDER BY aid) FROM pgbench_accounts WINDOW w AS (ORDER BY bid)",
		"42P20")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER (w ROWS UNBOUNDED PRECEDING) FROM pgbench_accounts WINDOW w AS (ORDER BY aid ROWS UNBOUNDED PRECEDING)",
		"42P20")
	expectAnalyzeCode(t, cat,
		"SELECT rank() OVER w2 FROM pgbench_accounts WINDOW w1 AS (ORDER BY aid ROWS UNBOUNDED PRECEDING), w2 AS (w1)",
		"42P20")
}

// TestAnalyzeNamedWindowCombiningFormAccepted pins the successful merge
// paths: an inline OVER inheriting a referenced window's PARTITION BY
// while adding its own ORDER BY, and a named window built on top of
// another named window, both confirmed against a live PostgreSQL 18.3
// instance.
func TestAnalyzeNamedWindowCombiningFormAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT rank() OVER (w ORDER BY aid) FROM pgbench_accounts WINDOW w AS (PARTITION BY bid)",
		"SELECT row_number() OVER w2 FROM pgbench_accounts WINDOW w1 AS (PARTITION BY bid), w2 AS (w1 ORDER BY aid)",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowFrameRowsAccepted pins that a ROWS frame clause
// (any valid bound shape) analyzes cleanly (M0122-0004 frame-clause
// slice) — the executor is the one that actually applies these
// bounds; the analyzer only validates mode/ordering/offset-expr shape.
func TestAnalyzeWindowFrameRowsAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS UNBOUNDED PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN 1 PRECEDING AND CURRENT ROW EXCLUDE TIES) FROM pgbench_accounts",
		"SELECT first_value(abalance) OVER (ORDER BY aid ROWS BETWEEN 2 PRECEDING AND 2 FOLLOWING) FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowFrameRangeOffsetOrderByCount pins that a RANGE value
// offset bound (PRECEDING/FOLLOWING) requires exactly one ORDER BY column
// (42P20, mirroring parse_clause.c's transformFrameOffset); a single ORDER
// BY column analyzes cleanly (the executor applies the in_range arithmetic,
// see docs/design/0122-0004-range-offset-window-frame.md).
func TestAnalyzeWindowFrameRangeOffsetOrderByCount(t *testing.T) {
	cat := analyzerCatalog(t)
	// Zero ORDER BY columns → 42P20.
	expectAnalyzeCode(t, cat,
		"SELECT sum(abalance) OVER (RANGE 5 PRECEDING) FROM pgbench_accounts",
		"42P20")
	// Two ORDER BY columns → 42P20.
	expectAnalyzeCode(t, cat,
		"SELECT sum(abalance) OVER (ORDER BY aid, bid RANGE BETWEEN 1 PRECEDING AND 2 FOLLOWING) FROM pgbench_accounts",
		"42P20")
	// Exactly one ORDER BY column → accepted.
	accepted := []string{
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE 5 PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE BETWEEN 1 PRECEDING AND 2 FOLLOWING) FROM pgbench_accounts",
	}
	for _, sql := range accepted {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowFrameRangeNonOffsetAccepted pins that RANGE with
// only UNBOUNDED/CURRENT ROW bounds analyzes cleanly — those bounds
// are purely peer-based and need no value arithmetic
// (see docs/design/0122-0004-range-window-frame.md).
func TestAnalyzeWindowFrameRangeNonOffsetAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE UNBOUNDED PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid RANGE BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) FROM pgbench_accounts",
		// RANGE without ORDER BY is legal for non-offset bounds (all
		// rows are peers → whole partition), unlike GROUPS.
		"SELECT sum(abalance) OVER (RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowFrameGroupsAccepted pins that a GROUPS frame clause
// (any valid bound shape, with an ORDER BY clause present) analyzes
// cleanly, mirroring TestAnalyzeWindowFrameRowsAccepted.
func TestAnalyzeWindowFrameGroupsAccepted(t *testing.T) {
	cat := analyzerCatalog(t)
	queries := []string{
		"SELECT sum(abalance) OVER (ORDER BY aid GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid GROUPS UNBOUNDED PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid GROUPS BETWEEN CURRENT ROW AND UNBOUNDED FOLLOWING) FROM pgbench_accounts",
		"SELECT first_value(abalance) OVER (ORDER BY aid GROUPS BETWEEN 2 PRECEDING AND 2 FOLLOWING) FROM pgbench_accounts",
	}
	for _, sql := range queries {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
	}
}

// TestAnalyzeWindowFrameGroupsRequiresOrderByRejected pins the spec
// (and gram.y parse_clause.c) restriction that GROUPS mode requires an
// ORDER BY clause in the window definition — 42P20.
func TestAnalyzeWindowFrameGroupsRequiresOrderByRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"SELECT sum(abalance) OVER (GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW) FROM pgbench_accounts",
		"42P20")
}

// TestAnalyzeWindowFrameBoundOrderingRejected pins gram.y's
// frame_extent/frame_bound windowing-error validations (all 42P20):
// an UNBOUNDED FOLLOWING start, an UNBOUNDED PRECEDING end, and the
// two "starting row can't come after the range it must cover" shapes.
func TestAnalyzeWindowFrameBoundOrderingRejected(t *testing.T) {
	cat := analyzerCatalog(t)
	cases := []string{
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS UNBOUNDED FOLLOWING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN CURRENT ROW AND 1 PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN 1 FOLLOWING AND CURRENT ROW) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS BETWEEN 1 FOLLOWING AND 1 PRECEDING) FROM pgbench_accounts",
		"SELECT sum(abalance) OVER (ORDER BY aid ROWS 1 FOLLOWING) FROM pgbench_accounts",
	}
	for _, sql := range cases {
		expectAnalyzeCode(t, cat, sql, "42P20")
	}
}

// TestAnalyzeWithOrdinalityNamedColumn is the regression pin for the
// WITH-ORDINALITY 42703 bug: tableFuncColumns never threaded
// rv.TableFunc.WithOrdinality through, so the analyzer's synthetic FROM-item
// table never had an ordinality column, and unnest/regexp_matches fell to
// the generic single-int8-column default — naming either the ordinality
// column or the SRF's own element column explicitly in the outer SELECT hit
// "column does not exist" even though the planner produced the row
// correctly. Root cause: internal/analyzer/analyzer.go's tableFuncColumns
// (not the planner's wrapOrdinality/planFromUnnest, which were always
// correct). See .ralph/working_set.md history for the diagnosis loop.
func TestAnalyzeWithOrdinalityNamedColumn(t *testing.T) {
	cat := analyzerCatalog(t)
	ok := []string{
		"SELECT n, m FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(m, n)",
		"SELECT ord FROM generate_series(1, 3) WITH ORDINALITY AS t(val, ord)",
		"SELECT ord FROM regexp_matches('a1b2', '[0-9]', 'g') WITH ORDINALITY AS t(m, ord)",
		"SELECT a, b, n FROM unnest(ARRAY[1,2], ARRAY[3,4]) WITH ORDINALITY AS t(a, b, n)",
		"SELECT val FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(val, n)",
	}
	for _, sql := range ok {
		if err := Analyze(parseOne(t, sql), cat); err != nil {
			t.Errorf("Analyze(%q) expected success, got: %v", sql, err)
		}
	}
	expectAnalyzeCode(t, cat,
		"SELECT bogus FROM unnest(ARRAY[1,2,3]) WITH ORDINALITY AS t(m, n)",
		"42703")
}
