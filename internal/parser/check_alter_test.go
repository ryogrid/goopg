package parser

import (
	"testing"
)

func TestParseAlterTableAddConstraintCheck(t *testing.T) {
	sql := "ALTER TABLE inhx add constraint foo CHECK (xx = 'text')"
	stmts, err := Parse(sql)
	if err != nil { t.Fatalf("parse error: %v", err) }
	if len(stmts) != 1 { t.Fatalf("expected 1 stmt, got %d", len(stmts)) }
	alter, ok := stmts[0].(*AlterTableStmt)
	if !ok { t.Fatalf("expected *AlterTableStmt, got %T", stmts[0]) }
	if len(alter.Actions) != 1 { t.Fatalf("expected 1 action, got %d", len(alter.Actions)) }
	act := alter.Actions[0]
	t.Logf("Action: Kind=%d CheckExpr=%q ConstraintName=%q", act.Kind, act.CheckExpr, act.ConstraintName)
	if act.Kind != AlterTableAddCheck {
		t.Errorf("Expected AlterTableAddCheck (%d), got %d", AlterTableAddCheck, act.Kind)
	}
	if act.CheckExpr != "xx = 'text'" {
		t.Errorf("Expected CheckExpr \"xx = 'text'\", got %q", act.CheckExpr)
	}
	if act.ConstraintName != "foo" {
		t.Errorf("Expected ConstraintName \"foo\", got %q", act.ConstraintName)
	}
}

// TestParseCheckNotEnforced covers DU-002 slice 430: a `NOT ENFORCED` trailer
// (PG18) on a CHECK constraint must be captured, not silently discarded, in
// every form goopg accepts it: ALTER TABLE ADD CONSTRAINT (named), an
// anonymous table-level CHECK, a named table-level CHECK, an anonymous
// inline column CHECK, and a named inline column CHECK. It must also compose
// with NOT VALID in either order on the ALTER TABLE form, and a bare
// `ENFORCED`/no trailer at all must leave the flag false.
func TestParseCheckNotEnforced(t *testing.T) {
	t.Run("AlterTableAddConstraint", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.CheckNotEnforced {
			t.Errorf("expected CheckNotEnforced=true")
		}
		if act.NotValid {
			t.Errorf("expected NotValid=false when only NOT ENFORCED is given")
		}
	})
	t.Run("AlterTableAddConstraintNotValidThenNotEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT VALID NOT ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if !act.CheckNotEnforced || !act.NotValid {
			t.Errorf("expected both NotValid and CheckNotEnforced true, got NotValid=%v CheckNotEnforced=%v", act.NotValid, act.CheckNotEnforced)
		}
	})
	t.Run("AlterTableAddConstraintPlainNotValid", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) NOT VALID")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.CheckNotEnforced {
			t.Errorf("expected CheckNotEnforced=false for a plain NOT VALID constraint")
		}
		if !act.NotValid {
			t.Errorf("expected NotValid=true")
		}
	})
	t.Run("AlterTableAddConstraintBareEnforced", func(t *testing.T) {
		stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT c CHECK (a > 0) ENFORCED")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		act := stmts[0].(*AlterTableStmt).Actions[0]
		if act.CheckNotEnforced {
			t.Errorf("expected CheckNotEnforced=false for a bare ENFORCED constraint")
		}
	})
	t.Run("CreateTableAnonymousTableLevel", func(t *testing.T) {
		stmts, err := Parse("CREATE TABLE t (a integer, CHECK (a > 0) NOT ENFORCED)")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.TableChecks) != 1 || len(ct.TableCheckNotEnforced) != 1 {
			t.Fatalf("expected 1 anonymous table check, got TableChecks=%v TableCheckNotEnforced=%v", ct.TableChecks, ct.TableCheckNotEnforced)
		}
		if !ct.TableCheckNotEnforced[0] {
			t.Errorf("expected TableCheckNotEnforced[0]=true")
		}
	})
	t.Run("CreateTableNamedTableLevel", func(t *testing.T) {
		stmts, err := Parse("CREATE TABLE t (a integer, CONSTRAINT c CHECK (a > 0) NOT ENFORCED)")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.TableNamedChecks) != 1 {
			t.Fatalf("expected 1 named table check, got %v", ct.TableNamedChecks)
		}
		if !ct.TableNamedChecks[0].NotEnforced {
			t.Errorf("expected TableNamedChecks[0].NotEnforced=true")
		}
	})
	t.Run("CreateTableInlineColumnAnonymous", func(t *testing.T) {
		stmts, err := Parse("CREATE TABLE t (a integer CHECK (a > 0) NOT ENFORCED)")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if !ct.Columns[0].CheckNotEnforced {
			t.Errorf("expected Columns[0].CheckNotEnforced=true")
		}
	})
	t.Run("CreateTableInlineColumnNamed", func(t *testing.T) {
		stmts, err := Parse("CREATE TABLE t (a integer CONSTRAINT c CHECK (a > 0) NOT ENFORCED)")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if !ct.Columns[0].CheckNotEnforced {
			t.Errorf("expected Columns[0].CheckNotEnforced=true")
		}
	})
	t.Run("CreateTableNoTrailerDefaultsFalse", func(t *testing.T) {
		stmts, err := Parse("CREATE TABLE t (a integer, CHECK (a > 0))")
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.TableCheckNotEnforced) != 1 || ct.TableCheckNotEnforced[0] {
			t.Errorf("expected TableCheckNotEnforced=[false], got %v", ct.TableCheckNotEnforced)
		}
	})
}
