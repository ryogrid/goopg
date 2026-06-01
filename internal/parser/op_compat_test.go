package parser

import (
	"testing"
)

func TestParseCreateOperatorArgTypes(t *testing.T) {
	sql := `CREATE OPERATOR @#@
        (leftarg = int8, rightarg = int8, procedure = int8xor)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "operator" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "operator")
	}
	if ns.ObjName.Name != "@#@" {
		t.Errorf("ObjName.Name = %q, want %q", ns.ObjName.Name, "@#@")
	}
	if len(ns.ArgTypes) != 2 {
		t.Fatalf("ArgTypes len = %d, want 2: %v", len(ns.ArgTypes), ns.ArgTypes)
	}
	if ns.ArgTypes[0] != "int8" {
		t.Errorf("ArgTypes[0] = %q, want %q", ns.ArgTypes[0], "int8")
	}
	if ns.ArgTypes[1] != "int8" {
		t.Errorf("ArgTypes[1] = %q, want %q", ns.ArgTypes[1], "int8")
	}
}

func TestParseCreateRuleTableName(t *testing.T) {
	sql := `CREATE RULE test_rule_exists AS ON INSERT TO test_exists
    DO INSTEAD
    INSERT INTO test_exists VALUES (1, 'x')`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	ns, ok := stmts[0].(*CompatNoopStmt)
	if !ok {
		t.Fatalf("Expected *CompatNoopStmt, got %T", stmts[0])
	}
	if ns.ObjType != "rule" {
		t.Errorf("ObjType = %q, want %q", ns.ObjType, "rule")
	}
	if ns.ObjName.Name != "test_rule_exists" {
		t.Errorf("ObjName.Name = %q, want %q", ns.ObjName.Name, "test_rule_exists")
	}
	if ns.TableName.Name != "test_exists" {
		t.Errorf("TableName.Name = %q, want %q", ns.TableName.Name, "test_exists")
	}
}
