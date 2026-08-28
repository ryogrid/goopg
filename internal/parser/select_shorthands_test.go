package parser

import "testing"

// TestSelectShorthandsAndTempViews pins P5.8 — the two SELECT shorthands
// (`TABLE t`, top-level `VALUES ...`), CREATE [OR REPLACE] TEMP VIEW, and
// `(expr).*`.
//
// TABLE and VALUES needed no grammar at all: both already parsed identically
// and only the routing entry was missing.
//
// CREATE VIEW is spelled as TWO alternatives rather than
// `CREATE opt_or_replace opt_create_modifier VIEW`: with both optional
// nonterminals in front, a TEMP after CREATE had to choose between reducing an
// empty opt_or_replace (the view path) and shifting into opt_create_modifier
// (the table path), and shift won — which killed `CREATE TEMP VIEW` outright.
//
// `(expr).*` is a parse-time placeholder that legacy rewrites at the end of
// parseSelect into a synthetic `__irs_N`-aliased table-function FROM entry
// plus a qualified star. The grammar reuses RewriteIndirectionStarTargets
// rather than reimplementing it, so the alias numbering — which is the TARGET
// INDEX, not a running counter — stays identical.
func TestSelectShorthandsAndTempViews(t *testing.T) {
	for _, q := range []string{
		"TABLE sometable",
		"TABLE s.t",
		"VALUES (1,2), (3,4+4)",
		"VALUES (1)",
		"CREATE TEMP VIEW v AS SELECT 1",
		"CREATE TEMPORARY VIEW v AS SELECT 1",
		"CREATE OR REPLACE TEMP VIEW v AS SELECT 1",
		"CREATE OR REPLACE VIEW v AS SELECT 1",
		"CREATE VIEW v (a, b) AS SELECT 1, 2",
		"CREATE TEMP VIEW v AS SELECT 1 WITH CASCADED CHECK OPTION",
		"select (row(1, 2.0)).*",
		"SELECT (json_populate_record(NULL::jsrec, js)).* FROM jspoptest",
		"select (row(1,2)).*, x from t",
	} {
		assertParity(t, q)
	}
	// Legacy rejects the non-star indirection forms outright, and TABLE takes
	// no trailing clauses.
	assertBothReject(t, "select (row(1, 2.0)).f1")
	assertBothReject(t, "select (a).b from t")
	// PG ACCEPTS this: select_no_parens wraps simple_select (gram.y:12970
	// `TABLE relation_expr`) with opt_sort_clause. Legacy rejected it; the
	// grammar is the PG-faithful side.
	assertParity(t, "TABLE sometable ORDER BY a")
}

// TestSelectIntoIsContextChecked pins the four contexts legacy REJECTS a
// `SELECT ... INTO` in, each with its own message. Only a top-level SELECT may
// carry one: intoWrap consumes the recorded target when it turns the query
// into a CreateTableStmt, so anything left over after the parse reached a
// cursor, a subquery or an INSERT source — and a view body is checked in its
// own rule because its message and its missing caret differ.
//
// DECLARE's body is SelectStmt rather than `stmt` for exactly this reason:
// routed through `stmt`, `DECLARE c CURSOR FOR SELECT 1 INTO t` became a
// CREATE TABLE instead of an error.
func TestSelectIntoIsContextChecked(t *testing.T) {
	for _, q := range []string{
		"DECLARE foo CURSOR FOR SELECT 1 INTO int4_tbl",
		"SELECT * FROM (SELECT 1 INTO f) bar",
		"CREATE VIEW foo AS SELECT 1 INTO int4_tbl",
		"INSERT INTO int4_tbl SELECT 1 INTO f",
	} {
		assertBothReject(t, q)
	}
	// ... while the top-level form stays a CREATE TABLE ... AS in disguise.
	assertParity(t, "SELECT 1 INTO t")
	assertParity(t, "SELECT 1 INTO TABLE t")
	assertParity(t, "SELECT a INTO t FROM u WHERE a > 1 ORDER BY a LIMIT 2")
}
