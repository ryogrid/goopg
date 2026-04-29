package analyzer

import "testing"

// TestAnalyzeOnConflictDoNothingNoTarget pins the simplest accepted
// UPSERT shape — no target, no SET — through the analyzer. No
// catalog lookup is needed beyond the INSERT target itself.
func TestAnalyzeOnConflictDoNothingNoTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "INSERT INTO pgbench_history (tid, bid, aid, delta) VALUES (1, 2, 3, 4) ON CONFLICT DO NOTHING"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

// TestAnalyzeOnConflictDoNothingWithTarget pins the column-list
// target form of DO NOTHING. The analyzer must accept it and verify
// the named columns exist on the target table.
func TestAnalyzeOnConflictDoNothingWithTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := "INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT (aid) DO NOTHING"
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

// TestAnalyzeOnConflictDoUpdateBasic — the headline Stage A path.
// Bare column refs in DO UPDATE SET resolve to the target table;
// `excluded.col` resolves via the pseudo-table scope. Type
// compatibility is enforced just like in plain UPDATE.
func TestAnalyzeOnConflictDoUpdateBasic(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := `INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0)
		ON CONFLICT (aid) DO UPDATE SET abalance = excluded.abalance`
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

// TestAnalyzeOnConflictDoUpdateMixedRefs — a SET expression mixing
// the target's own column (bare) with `excluded.col`. Pins the
// "bare column resolves to target only" rule: were it not for
// `qualifiedOnly`, this would error with 42702 ambiguous.
func TestAnalyzeOnConflictDoUpdateMixedRefs(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := `INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0)
		ON CONFLICT (aid) DO UPDATE SET abalance = abalance + excluded.abalance`
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

// TestAnalyzeOnConflictDoUpdateWithWhere — the optional predicate.
// Must analyze under the same target+excluded scope and return
// boolean.
func TestAnalyzeOnConflictDoUpdateWithWhere(t *testing.T) {
	cat := analyzerCatalog(t)
	sql := `INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0)
		ON CONFLICT (aid) DO UPDATE SET abalance = excluded.abalance
		WHERE pgbench_accounts.abalance < excluded.abalance`
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatal(err)
	}
}

// TestAnalyzeOnConflictRejectsConstraintTarget pins the Stage B
// gate: `ON CONSTRAINT name` parses but the analyzer rejects with
// SQLSTATE 0A000 so tooling can branch on a stable diagnostic.
func TestAnalyzeOnConflictRejectsConstraintTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid) VALUES (1) ON CONFLICT ON CONSTRAINT pk DO NOTHING",
		"0A000")
}

// TestAnalyzeOnConflictRejectsUpdateWithoutTarget pins upstream's
// "DO UPDATE requires inference specification or constraint name"
// rule. Without a target there's no arbiter to drive conflict
// resolution.
func TestAnalyzeOnConflictRejectsUpdateWithoutTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT DO UPDATE SET abalance = 1",
		"42601")
}

// TestAnalyzeOnConflictTargetUnknownColumn — the conflict target
// columns are validated against the target table catalog. A
// nonexistent name surfaces 42703 "column does not exist" with the
// table name in the message.
func TestAnalyzeOnConflictTargetUnknownColumn(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid) VALUES (1) ON CONFLICT (nonexistent) DO NOTHING",
		"42703")
}

// TestAnalyzeOnConflictUpdateSetUnknownColumn pins SET-side column
// validation: same 42703 contract as plain UPDATE.
func TestAnalyzeOnConflictUpdateSetUnknownColumn(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT (aid) DO UPDATE SET nope = 1",
		"42703")
}

// TestAnalyzeOnConflictUpdateExcludedUnknownColumn pins the
// `excluded` pseudo-table column validation: the qualified ref
// resolves through the synthesised excluded scope, so a missing
// column on the target table also misses on excluded — surfaces as
// 42703.
func TestAnalyzeOnConflictUpdateExcludedUnknownColumn(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT (aid) DO UPDATE SET abalance = excluded.nope",
		"42703")
}

// TestAnalyzeOnConflictUpdateTypeMismatch pins SET-RHS type
// checking — same 42804 contract as plain UPDATE / INSERT.
func TestAnalyzeOnConflictUpdateTypeMismatch(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT (aid) DO UPDATE SET abalance = 'not-an-int'",
		"42804")
}

// TestAnalyzeOnConflictUpdateWhereNonBoolean pins the WHERE-must-be
// -boolean contract — same as plain UPDATE WHERE.
func TestAnalyzeOnConflictUpdateWhereNonBoolean(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0) ON CONFLICT (aid) DO UPDATE SET abalance = 1 WHERE abalance",
		"42804")
}
