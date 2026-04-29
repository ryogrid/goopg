package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

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

// TestAnalyzeOnConflictRejectsUnknownConstraint — `ON CONSTRAINT
// name` is now supported (M0017 Stage B), but unknown / mismatched
// constraint names surface 42704 ("constraint does not exist") so
// tooling can branch on the canonical upstream diagnostic.
func TestAnalyzeOnConflictRejectsUnknownConstraint(t *testing.T) {
	cat := analyzerCatalog(t)
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid) VALUES (1) ON CONFLICT ON CONSTRAINT no_such_pk DO NOTHING",
		"42704")
}

// TestAnalyzeOnConflictAcceptsConstraintTarget — Stage B happy
// path. Analyzer must accept `ON CONSTRAINT name` once the
// constraint exists as a unique index on the target table.
func TestAnalyzeOnConflictAcceptsConstraintTarget(t *testing.T) {
	cat := analyzerCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_pk"}, tbl, []string{"aid"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	sql := `INSERT INTO pgbench_accounts (aid, abalance) VALUES (1, 0)
		ON CONFLICT ON CONSTRAINT pgbench_accounts_pk DO UPDATE SET abalance = excluded.abalance`
	if err := Analyze(parseOne(t, sql), cat); err != nil {
		t.Fatalf("Analyze: %v", err)
	}
}

// TestAnalyzeOnConflictRejectsNonUniqueConstraint — pinning
// upstream's "is not a unique constraint" rule (42P10): a regular
// non-unique index can't arbitrate conflicts.
func TestAnalyzeOnConflictRejectsNonUniqueConstraint(t *testing.T) {
	cat := analyzerCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_idx"}, tbl, []string{"aid"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid) VALUES (1) ON CONFLICT ON CONSTRAINT pgbench_accounts_idx DO NOTHING",
		"42P10")
}

// TestAnalyzeOnConflictRejectsConstraintOnDifferentTable — the
// named constraint must live on the INSERT target table; otherwise
// 42704 (canonical "doesn't belong" diagnostic).
func TestAnalyzeOnConflictRejectsConstraintOnDifferentTable(t *testing.T) {
	cat := analyzerCatalog(t)
	other, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_history"})
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_history_pk"}, other, []string{"tid"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	expectAnalyzeCode(t, cat,
		"INSERT INTO pgbench_accounts (aid) VALUES (1) ON CONFLICT ON CONSTRAINT pgbench_history_pk DO NOTHING",
		"42704")
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
