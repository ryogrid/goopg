package analyzer

import "testing"

// TestAnalyzeErrorMissingRTEAliasHint guards the port of upstream's
// errorMissingRTE() (postgres/src/backend/parser/parse_relation.c).
//
// The distinction it protects: a table-qualified reference that fails to
// resolve is NOT automatically "the relation is absent". PostgreSQL looks the
// refname up a second time ignoring aliases; when it finds a FROM entry the
// user renamed, it reports "invalid reference to FROM-clause entry" plus a HINT
// naming the alias, because `DELETE FROM t alias WHERE t.col` is a common
// mistake with a specific remedy. Only a refname matching nothing at all gets
// the bald "missing FROM-clause entry".
//
// Before this port goopg emitted the bald message for both, which is what made
// upstream `regress/delete` diverge (nightly AI-20260806-011323-016) — that
// case is a one-statement assertion on exactly this text.
func TestAnalyzeErrorMissingRTEAliasHint(t *testing.T) {
	cat := analyzerCatalog(t)

	const wantMsg = `invalid reference to FROM-clause entry for table "pgbench_accounts"`
	const wantHint = `Perhaps you meant to reference the table alias "a".`

	// One case per shape the upstream regress corpus asserts on that the
	// analyzer owns: DELETE (delete.out), UPDATE (update.out), ON CONFLICT
	// DO UPDATE (insert_conflict.out), plus the plain SELECT column and star
	// forms. RETURNING's qualified star (returning.out) never reaches the
	// analyzer — it is guarded on the planner twin, in
	// TestPlanErrorMissingRTEAliasHint.
	renamed := []string{
		"DELETE FROM pgbench_accounts a WHERE pgbench_accounts.aid > 25",
		"UPDATE pgbench_accounts AS a SET bid = pgbench_accounts.bid + 10 WHERE a.aid = 1",
		// Full value list: `INSERT INTO t AS a (col) …` parses the
		// parenthesised list as the ALIAS's column list, not the insert
		// target list, so a short list would fail on arity first.
		"INSERT INTO pgbench_accounts AS a VALUES (1, 2, 3, 'x') ON CONFLICT (aid) DO UPDATE SET bid = pgbench_accounts.bid",
		"SELECT pgbench_accounts.aid FROM pgbench_accounts a",
		"SELECT pgbench_accounts.* FROM pgbench_accounts a",
	}
	for _, sql := range renamed {
		err := Analyze(parseOne(t, sql), cat)
		if err == nil {
			t.Errorf("Analyze(%q): expected an error", sql)
			continue
		}
		ae, ok := err.(*AnalyzeError)
		if !ok {
			t.Errorf("Analyze(%q): err type=%T, want *AnalyzeError", sql, err)
			continue
		}
		// 42P01 == ERRCODE_UNDEFINED_TABLE, the code upstream's
		// errorMissingRTE() raises on every branch.
		if ae.Code != "42P01" {
			t.Errorf("Analyze(%q): code=%s want 42P01", sql, ae.Code)
		}
		if ae.Message != wantMsg {
			t.Errorf("Analyze(%q):\n got msg %q\nwant msg %q", sql, ae.Message, wantMsg)
		}
		if ae.Hint != wantHint {
			t.Errorf("Analyze(%q):\n got hint %q\nwant hint %q", sql, ae.Hint, wantHint)
		}
	}

	// Negative control 1 — a refname matching no FROM entry keeps the bald
	// message and carries no hint. Losing this is the failure mode of a
	// too-eager alias search.
	err := Analyze(parseOne(t, "SELECT nosuch.aid FROM pgbench_accounts a"), cat)
	ae, ok := err.(*AnalyzeError)
	if !ok {
		t.Fatalf("absent-relation ref: err=%v type=%T, want *AnalyzeError", err, err)
	}
	if want := `missing FROM-clause entry for table "nosuch"`; ae.Message != want {
		t.Errorf("absent-relation ref:\n got %q\nwant %q", ae.Message, want)
	}
	if ae.Hint != "" {
		t.Errorf("absent-relation ref: unexpected hint %q", ae.Hint)
	}

	// Negative control 2 — upstream's `strcmp(aliasname, relname) != 0`
	// guard: an entry aliased to its own name was never renamed, so the
	// reference resolves normally rather than erroring with a hint that
	// would name the same identifier the user already wrote.
	if err := Analyze(parseOne(t, "SELECT pgbench_accounts.aid FROM pgbench_accounts pgbench_accounts"), cat); err != nil {
		t.Errorf("self-aliased relation should resolve, got: %v", err)
	}
}
