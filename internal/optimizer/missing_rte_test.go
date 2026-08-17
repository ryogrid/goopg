package optimizer

import "testing"

// TestPlanErrorMissingRTEAliasHint is the planner half of the errorMissingRTE
// port (upstream postgres/src/backend/parser/parse_relation.c). The analyzer
// half lives in internal/analyzer/missing_rte_test.go and the two must move
// together — the analyzer is the first error source for the statements it
// covers, but RETURNING scopes are built after analysis, so a qualified star
// there (upstream `returning.out`) reaches only errorMissingRTEPlan.
func TestPlanErrorMissingRTEAliasHint(t *testing.T) {
	cat := pgbenchCatalog(t)

	// `INSERT INTO foo AS bar … RETURNING foo.*` — returning.out asserts the
	// aliased form of the message, not the bald one.
	_, err := Plan(parseOne(t,
		"INSERT INTO pgbench_accounts AS a VALUES (1, 2, 3, 'x') RETURNING pgbench_accounts.*"), cat)
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("aliased RETURNING star: err=%v type=%T, want *PlanError", err, err)
	}
	// 42P01 == ERRCODE_UNDEFINED_TABLE, upstream's code on every
	// errorMissingRTE branch.
	if pe.Code != "42P01" {
		t.Errorf("aliased RETURNING star: code=%s want 42P01", pe.Code)
	}
	if want := `invalid reference to FROM-clause entry for table "pgbench_accounts"`; pe.Message != want {
		t.Errorf("aliased RETURNING star:\n got %q\nwant %q", pe.Message, want)
	}
	if want := `Perhaps you meant to reference the table alias "a".`; pe.Hint != want {
		t.Errorf("aliased RETURNING star:\n got hint %q\nwant hint %q", pe.Hint, want)
	}

	// Negative control — an unaliased target keeps the table's own name
	// referenceable, so the same star resolves rather than erroring.
	if _, err := Plan(parseOne(t,
		"INSERT INTO pgbench_accounts VALUES (1, 2, 3, 'x') RETURNING pgbench_accounts.*"), cat); err != nil {
		t.Errorf("unaliased RETURNING star should resolve, got: %v", err)
	}
}
