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
