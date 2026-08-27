package sqlparser

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
// plus a qualified star. The grammar reuses parser.RewriteIndirectionStarTargets
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
	assertBothReject(t, "TABLE sometable ORDER BY a")
}
