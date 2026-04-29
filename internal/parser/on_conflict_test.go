package parser

import "testing"

// TestParseInsertOnConflictNoTargetDoNothing — the simplest UPSERT
// shape: `ON CONFLICT DO NOTHING` with no conflict target. Pins the
// no-target branch (Target stays nil) and the OnConflictNothing
// action enum. Exercises the canonical idempotent-insert idiom that
// ORMs emit when they want a "best-effort INSERT".
func TestParseInsertOnConflictNoTargetDoNothing(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a) VALUES (1) ON CONFLICT DO NOTHING")
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := stmts[0].(*InsertStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if ins.OnConflict == nil {
		t.Fatal("OnConflict is nil")
	}
	if ins.OnConflict.Target != nil {
		t.Errorf("Target = %+v, want nil (no-target form)", ins.OnConflict.Target)
	}
	if ins.OnConflict.Action != OnConflictNothing {
		t.Errorf("Action = %v, want OnConflictNothing", ins.OnConflict.Action)
	}
	if ins.OnConflict.UpdateSet != nil {
		t.Errorf("UpdateSet = %+v, want nil", ins.OnConflict.UpdateSet)
	}
}

// TestParseInsertOnConflictColumnTargetDoNothing — column-list
// target combined with DO NOTHING. Pins the target-column-list
// parser branch.
func TestParseInsertOnConflictColumnTargetDoNothing(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a, b) VALUES (1, 2) ON CONFLICT (a) DO NOTHING")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict == nil || ins.OnConflict.Target == nil {
		t.Fatalf("OnConflict / Target nil: %+v", ins.OnConflict)
	}
	if got := ins.OnConflict.Target.Columns; len(got) != 1 || got[0] != "a" {
		t.Errorf("Target.Columns = %+v, want [a]", got)
	}
	if ins.OnConflict.Target.Constraint != "" {
		t.Errorf("Target.Constraint = %q, want empty", ins.OnConflict.Target.Constraint)
	}
	if ins.OnConflict.Action != OnConflictNothing {
		t.Errorf("Action = %v", ins.OnConflict.Action)
	}
}

// TestParseInsertOnConflictDoUpdate — the full Stage A surface:
// column-list target + DO UPDATE SET with multiple assignments
// referencing both the target table and the `excluded` pseudo-table.
// `excluded.col` parses as a normal qualified ColumnRef at this
// stage; the analyzer (Stage B) handles the special pseudo-scope.
func TestParseInsertOnConflictDoUpdate(t *testing.T) {
	stmts, err := Parse(`INSERT INTO t (a, b) VALUES (1, 2)
		ON CONFLICT (a) DO UPDATE SET b = excluded.b, c = c + 1`)
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict == nil {
		t.Fatal("OnConflict nil")
	}
	if ins.OnConflict.Action != OnConflictUpdate {
		t.Errorf("Action = %v, want OnConflictUpdate", ins.OnConflict.Action)
	}
	if got := ins.OnConflict.UpdateSet; len(got) != 2 {
		t.Fatalf("UpdateSet len = %d, want 2", len(got))
	}
	if c := ins.OnConflict.UpdateSet[0].Column; c != "b" {
		t.Errorf("UpdateSet[0].Column = %q, want b", c)
	}
	// excluded.b parses as a qualified ColumnRef — pin the shape so
	// the analyzer slice has a stable input.
	colRef, ok := ins.OnConflict.UpdateSet[0].Expr.(*ColumnRef)
	if !ok {
		t.Fatalf("UpdateSet[0].Expr = %T, want *ColumnRef", ins.OnConflict.UpdateSet[0].Expr)
	}
	if colRef.Table != "excluded" || colRef.Column != "b" {
		t.Errorf("ColumnRef = %+v, want {Table:excluded, Column:b}", colRef)
	}
	if ins.OnConflict.UpdateWhere != nil {
		t.Errorf("UpdateWhere = %+v, want nil (no WHERE)", ins.OnConflict.UpdateWhere)
	}
}

// TestParseInsertOnConflictDoUpdateWithWhere — the WHERE-predicate
// form of DO UPDATE: only update existing rows that match the
// predicate, leaving the rest of the conflict resolution to the
// no-op default. Pins the optional UpdateWhere field.
func TestParseInsertOnConflictDoUpdateWithWhere(t *testing.T) {
	stmts, err := Parse(`INSERT INTO t (a, b) VALUES (1, 2)
		ON CONFLICT (a) DO UPDATE SET b = 3 WHERE t.b < 10`)
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict == nil || ins.OnConflict.UpdateWhere == nil {
		t.Fatalf("UpdateWhere nil: %+v", ins.OnConflict)
	}
}

// TestParseInsertOnConflictConstraintTarget — `ON CONFLICT ON
// CONSTRAINT name` shape. Stage B-only at the analyzer level, but
// the parser already accepts the form so the AST shape is stable
// across stages — operators won't see a parser error change when
// Stage B promotes the constraint-name path.
func TestParseInsertOnConflictConstraintTarget(t *testing.T) {
	stmts, err := Parse(`INSERT INTO t (a) VALUES (1)
		ON CONFLICT ON CONSTRAINT t_pkey DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict == nil || ins.OnConflict.Target == nil {
		t.Fatalf("OnConflict/Target nil: %+v", ins.OnConflict)
	}
	if ins.OnConflict.Target.Constraint != "t_pkey" {
		t.Errorf("Target.Constraint = %q, want t_pkey", ins.OnConflict.Target.Constraint)
	}
	if len(ins.OnConflict.Target.Columns) != 0 {
		t.Errorf("Target.Columns = %+v, want empty", ins.OnConflict.Target.Columns)
	}
}

// TestParseInsertOnConflictWithReturning — combine ON CONFLICT with
// RETURNING in the upstream-mandated order (RETURNING comes after).
func TestParseInsertOnConflictWithReturning(t *testing.T) {
	stmts, err := Parse(`INSERT INTO t (a) VALUES (1)
		ON CONFLICT (a) DO UPDATE SET a = 2 RETURNING a, b`)
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict == nil {
		t.Fatal("OnConflict nil")
	}
	if len(ins.Returning) != 2 {
		t.Errorf("Returning len = %d, want 2", len(ins.Returning))
	}
}

// TestParseInsertOnConflictCTE — the WITH wrapper around an INSERT
// composes cleanly with ON CONFLICT (M0016 and M0017 surface combine
// without parser interaction). Pins the AST so future analyzer work
// can rely on `stmt.OnConflict` being reachable through the CTE
// path.
func TestParseInsertOnConflictCTE(t *testing.T) {
	stmts, err := Parse(`WITH src AS (SELECT 1 AS a)
		INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.With == nil {
		t.Fatal("With nil")
	}
	if ins.OnConflict == nil {
		t.Fatal("OnConflict nil")
	}
	if ins.OnConflict.Action != OnConflictNothing {
		t.Errorf("Action = %v", ins.OnConflict.Action)
	}
}

// TestParseInsertOnConflictRejectsBadAction guards the action
// keyword set: anything other than NOTHING / UPDATE after `DO`
// errors at parse time. Pins the diagnostic so tooling that surfaces
// SQLSTATE 42601 can rely on the error path.
func TestParseInsertOnConflictRejectsBadAction(t *testing.T) {
	cases := []string{
		"INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO REPLACE",
		"INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO INSERT",
	}
	for _, sql := range cases {
		if _, err := Parse(sql); err == nil {
			t.Errorf("Parse(%q) succeeded; want error", sql)
		}
	}
}

// TestParseInsertOnConflictRejectsMissingDo guards the `DO` keyword
// requirement: bare `ON CONFLICT (a) NOTHING` (without DO) errors.
func TestParseInsertOnConflictRejectsMissingDo(t *testing.T) {
	if _, err := Parse("INSERT INTO t (a) VALUES (1) ON CONFLICT (a) NOTHING"); err == nil {
		t.Error("expected error on missing DO")
	}
}

// TestParseInsertWithoutOnConflictUnchanged is the rollout
// guardrail: an INSERT statement that doesn't use ON CONFLICT must
// continue to produce a nil OnConflict field — no spurious empty
// clauses for callers that haven't migrated. Mirrors the
// step-1 AST-stability pattern from M0016-0001 / M0018-0001.
func TestParseInsertWithoutOnConflictUnchanged(t *testing.T) {
	stmts, err := Parse("INSERT INTO t (a) VALUES (1)")
	if err != nil {
		t.Fatal(err)
	}
	ins := stmts[0].(*InsertStmt)
	if ins.OnConflict != nil {
		t.Errorf("OnConflict = %+v, want nil", ins.OnConflict)
	}
}
